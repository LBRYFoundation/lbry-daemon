package rpc

import (
	"context"
	"encoding/hex"
	"testing"

	walletpkg "lbry/daemon/wallet"
)

func TestSupportCreatePreviewCommentReleasesFunding(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	result := fileMutationRPCResult(t, fixture.server, "support_create", map[string]any{
		"claim_id": fixture.claimID, "amount": "1.0", "comment": "hello", "preview": true,
	})
	encoded, ok := result.(map[string]any)
	if !ok || encoded["txid"] == "" {
		t.Fatalf("support preview = %#v", result)
	}
	spendable, err := fixture.ledger.Database.ListSpendableOutputs(
		context.Background(), []string{fixture.account.ID},
	)
	if err != nil || len(spendable) != 1 {
		t.Fatalf("support preview spendables = %#v, %v", spendable, err)
	}
	outputs := encoded["outputs"].([]any)
	output := outputs[0].(map[string]any)
	if output["type"] != "support" || output["amount"] != "1.0" || output["protobuf"] != nil {
		t.Fatalf("support preview output = %#v", output)
	}
}

func TestSupportCreateTipBroadcastsToClaimAddress(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	fixture.network.downloadComplete = nil
	result := fileMutationRPCResult(t, fixture.server, "support_create", map[string]any{
		"claim_id": fixture.claimID, "amount": "1.0", "tip": true,
	})
	if encoded, ok := result.(map[string]any); !ok || encoded["txid"] == "" {
		t.Fatalf("support tip result = %#v", result)
	}
	fixture.network.mu.Lock()
	broadcasts := append([]string(nil), fixture.network.broadcasts...)
	fixture.network.mu.Unlock()
	if len(broadcasts) != 1 {
		t.Fatalf("support broadcasts = %v", broadcasts)
	}
	transaction, err := walletpkg.ParseTransaction(mustDecodeHex(t, broadcasts[0]))
	if err != nil || len(transaction.Outputs) == 0 || !transaction.Outputs[0].Script.IsSupportClaim() {
		t.Fatalf("support transaction = %#v, %v", transaction, err)
	}
	claimHash, err := hex.DecodeString(fixture.claimID)
	if err != nil {
		t.Fatal(err)
	}
	for left, right := 0, len(claimHash)-1; left < right; left, right = left+1, right-1 {
		claimHash[left], claimHash[right] = claimHash[right], claimHash[left]
	}
	if hex.EncodeToString(transaction.Outputs[0].Script.ClaimID) != hex.EncodeToString(claimHash) {
		t.Fatalf("support claim id = %x", transaction.Outputs[0].Script.ClaimID)
	}
	supports, err := fixture.store.GetSupports(context.Background(), fixture.claimID)
	if err != nil || len(supports) != 1 || supports[0].ClaimID != fixture.claimID ||
		supports[0].Amount != 100_000_000 || supports[0].Address == "" ||
		supports[0].Outpoint != transaction.ID+":-1" {
		t.Fatalf("cached supports = %#v, %v", supports, err)
	}
}

func TestSupportCreateSignsCommentWithOwnedChannel(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	channel, channelID, _ := persistChannelUpdateFixture(t, &fixture)
	fixture.network.downloadComplete = nil
	fileMutationRPCResult(t, fixture.server, "support_create", map[string]any{
		"claim_id": fixture.claimID, "amount": "1.0", "comment": "signed",
		"channel_id": channelID,
	})
	fixture.network.mu.Lock()
	broadcasts := append([]string(nil), fixture.network.broadcasts...)
	fixture.network.mu.Unlock()
	if len(broadcasts) != 1 {
		t.Fatalf("support broadcasts = %v", broadcasts)
	}
	transaction, err := walletpkg.ParseTransaction(mustDecodeHex(t, broadcasts[0]))
	if err != nil || len(transaction.Outputs) == 0 {
		t.Fatalf("signed support transaction = %#v, %v", transaction, err)
	}
	value, err := walletpkg.DecodeSupportValue(transaction.Outputs[0].Script.Support)
	channelValue, channelErr := walletpkg.DecodeClaimValue(channel.Outputs[0].Script.Claim)
	valid, verifyErr := walletpkg.VerifyTransactionSupportSignature(value, transaction, channelValue)
	if err != nil || channelErr != nil || verifyErr != nil || !valid || !value.IsSigned() ||
		value.Comment != "signed" || value.SigningChannelID() == nil || *value.SigningChannelID() != channelID {
		t.Fatalf("signed support = value %#v, valid %v, errors %v / %v / %v", value, valid, err, channelErr, verifyErr)
	}
}

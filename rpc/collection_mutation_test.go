package rpc

import (
	"testing"

	walletpkg "lbry/daemon/wallet"
)

func TestCollectionCreatePreviewEncodesReferencesAndMetadata(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	result := fileMutationRPCResult(t, fixture.server, "collection_create", map[string]any{
		"name": "playlist", "bid": "1.0", "claims": []any{fixture.claimID},
		"title": "Playlist", "tags": []any{"one", "two"}, "preview": true,
	})
	encoded, ok := result.(map[string]any)
	if !ok || encoded["txid"] == "" {
		t.Fatalf("collection create = %#v", result)
	}
	output := encoded["outputs"].([]any)[0].(map[string]any)
	value := output["value"].(map[string]any)
	claims := value["claims"].([]any)
	if output["value_type"] != "collection" || value["title"] != "Playlist" ||
		len(claims) != 1 || claims[0] != fixture.claimID {
		t.Fatalf("collection output = %#v", output)
	}
}

func TestCollectionCreateSignsWithOwnedChannel(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	channel, channelID, _ := persistChannelUpdateFixture(t, &fixture)
	fixture.network.downloadComplete = nil
	fileMutationRPCResult(t, fixture.server, "collection_create", map[string]any{
		"name": "signed-list", "bid": "1.0", "claims": []any{fixture.claimID},
		"channel_id": channelID,
	})
	fixture.network.mu.Lock()
	broadcasts := append([]string(nil), fixture.network.broadcasts...)
	fixture.network.mu.Unlock()
	if len(broadcasts) != 1 {
		t.Fatalf("collection broadcasts = %v", broadcasts)
	}
	transaction, err := walletpkg.ParseTransaction(mustDecodeHex(t, broadcasts[0]))
	if err != nil || len(transaction.Outputs) == 0 {
		t.Fatalf("signed collection transaction = %#v, %v", transaction, err)
	}
	value, err := walletpkg.DecodeClaimValue(transaction.Outputs[0].Script.Claim)
	channelValue, channelErr := walletpkg.DecodeClaimValue(channel.Outputs[0].Script.Claim)
	valid, verifyErr := walletpkg.VerifyTransactionClaimSignature(value, transaction, channelValue)
	if err != nil || channelErr != nil || verifyErr != nil || !valid || !value.IsSigned() ||
		value.SigningChannelID() == nil || *value.SigningChannelID() != channelID {
		t.Fatalf("signed collection = value %#v, valid %v, errors %v / %v / %v", value, valid, err, channelErr, verifyErr)
	}
}

package rpc

import (
	"context"
	"strings"
	"testing"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestChannelCreatePreviewGeneratesSigningKeyAndReleasesFunding(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	result := fileMutationRPCResult(t, fixture.server, "channel_create", map[string]any{
		"name": "@creator", "bid": "1.0", "preview": true,
	})
	encoded, ok := result.(map[string]any)
	if !ok || encoded["txid"] == "" {
		t.Fatalf("channel preview = %#v", result)
	}
	outputs := encoded["outputs"].([]any)
	output := outputs[0].(map[string]any)
	if output["type"] != "claim" || output["value_type"] != "channel" ||
		output["has_signing_key"] != true || output["amount"] != "1.0" {
		t.Fatalf("channel output = %#v", output)
	}
	spendable, err := fixture.ledger.Database.ListSpendableOutputs(
		context.Background(), []string{fixture.account.ID},
	)
	if err != nil || len(spendable) != 1 {
		t.Fatalf("channel preview spendables = %#v, %v", spendable, err)
	}
}

func TestChannelUpdatePreservesBidAndKeyWhileMutatingMetadata(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	parent, claimID, oldKey := persistChannelUpdateFixture(t, &fixture)
	result := fileMutationRPCResult(t, fixture.server, "channel_update", map[string]any{
		"claim_id": claimID, "title": "Updated", "preview": true,
	})
	encoded, ok := result.(map[string]any)
	if !ok || encoded["txid"] == "" {
		t.Fatalf("channel update = %#v", result)
	}
	output := encoded["outputs"].([]any)[0].(map[string]any)
	value := output["value"].(map[string]any)
	if output["claim_op"] != "update" || output["amount"] != "2.0" ||
		output["has_signing_key"] != true || value["title"] != "Updated" ||
		value["description"] != "Original description" || value["public_key"] != oldKey {
		t.Fatalf("channel update output = %#v", output)
	}
	claims, err := fixture.ledger.GetClaims(
		context.Background(), walletpkg.ClaimListOptions{Query: ledgerdb.OutputQuery{
			AccountIDs: []string{fixture.account.ID}, TXOID: parent.Outputs[0].ID(),
		}},
	)
	if err != nil || len(claims) != 1 {
		t.Fatalf("channel update preview release = %#v, %v", claims, err)
	}
}

func TestChannelUpdateReplaceRotatesKeyAndClearsMetadata(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	_, claimID, oldKey := persistChannelUpdateFixture(t, &fixture)
	result := fileMutationRPCResult(t, fixture.server, "channel_update", map[string]any{
		"claim_id": claimID, "replace": true, "new_signing_key": true,
		"email": "new@example.com", "preview": true,
	})
	output := result.(map[string]any)["outputs"].([]any)[0].(map[string]any)
	value := output["value"].(map[string]any)
	if value["email"] != "new@example.com" || value["title"] != nil ||
		value["description"] != nil || value["public_key"] == oldKey {
		t.Fatalf("replaced channel value = %#v", value)
	}
}

func persistChannelUpdateFixture(
	t *testing.T, fixture *paidGetFixture,
) (*walletpkg.Transaction, string, string) {
	t.Helper()
	addresses, err := fixture.account.Receiving.GetAddresses(context.Background(), false)
	if err != nil || len(addresses) == 0 {
		t.Fatalf("channel addresses = %v, %v", addresses, err)
	}
	address := addresses[0]
	decodedAddress, err := keys.DecodeBase58(address)
	if err != nil || len(decodedAddress) < 21 {
		t.Fatalf("decode channel address = %x, %v", decodedAddress, err)
	}
	privateKey, err := fixture.account.DeterministicChannelKeys.GenerateNextKey(
		fixture.ledger.ChannelKeyUsage(context.Background()),
	)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := walletpkg.BuildChannelClaim(
		nil, privateKey.PublicKey().CompressedBytes(), false,
		map[string]any{"title": "Original", "description": "Original description"},
	)
	if err != nil {
		t.Fatal(err)
	}
	output := walletpkg.NewClaimNameOutput(200_000_000, "@owned", claim, decodedAddress[1:21])
	transaction := walletpkg.NewTransaction().AddInputs([]walletpkg.TransactionInput{{
		PreviousIndex: ^uint32(0), Sequence: ^uint32(0), Coinbase: []byte{5},
	}}).AddOutputs([]walletpkg.TransactionOutput{output})
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	transaction.Height, transaction.Position, transaction.IsVerified = 4, 4, true
	claimID, err := transaction.Outputs[0].ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	claimName := "@owned"
	if err := fixture.ledger.Database.SaveTransactionIOBatch(
		context.Background(), []ledgerdb.TransactionIORow{{
			Transaction: ledgerdb.TransactionRow{
				TXID: transaction.ID, Raw: transaction.Raw, Height: 4, Position: 4, IsVerified: true,
			},
			Outputs: []ledgerdb.TransactionOutputRow{{
				TXOID: transaction.Outputs[0].ID(), Address: &address, Position: 0,
				Amount: 200_000_000, Script: transaction.Outputs[0].Script.Source,
				TXOType: walletpkg.TransactionOutputTypeChannel, ClaimID: &claimID, ClaimName: &claimName,
			}},
		}}, address, "",
	); err != nil {
		t.Fatal(err)
	}
	return transaction, claimID, stringHex(privateKey.PublicKey().CompressedBytes())
}

func stringHex(value []byte) string {
	const alphabet = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index, item := range value {
		encoded[index*2] = alphabet[item>>4]
		encoded[index*2+1] = alphabet[item&15]
	}
	return string(encoded)
}

func TestChannelCreateBroadcastsCanonicalClaim(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	fixture.network.downloadComplete = nil
	result := fileMutationRPCResult(t, fixture.server, "channel_create", map[string]any{
		"name": "@creator", "bid": "1.0", "title": "Creator",
		"description": "Channel description", "email": "creator@example.com",
		"website_url": "https://example.com", "thumbnail_url": "https://example.com/thumb",
		"cover_url": "https://example.com/cover", "tags": []any{"one", "two"},
		"featured": []any{strings.Repeat("33", 20)},
	})
	if encoded, ok := result.(map[string]any); !ok || encoded["txid"] == "" {
		t.Fatalf("channel create result = %#v", result)
	}
	fixture.network.mu.Lock()
	broadcasts := append([]string(nil), fixture.network.broadcasts...)
	fixture.network.mu.Unlock()
	if len(broadcasts) != 1 {
		t.Fatalf("channel broadcasts = %v", broadcasts)
	}
	transaction, err := walletpkg.ParseTransaction(mustDecodeHex(t, broadcasts[0]))
	if err != nil || len(transaction.Outputs) == 0 {
		t.Fatalf("channel transaction = %#v, %v", transaction, err)
	}
	claimValue, err := walletpkg.DecodeClaimValue(transaction.Outputs[0].Script.Claim)
	if err != nil || claimValue.Type != "channel" || claimValue.Value["public_key"] == "" {
		t.Fatalf("channel claim = %#v, %v", claimValue, err)
	}
	if claimValue.Value["title"] != "Creator" || claimValue.Value["description"] != "Channel description" ||
		claimValue.Value["email"] != "creator@example.com" ||
		claimValue.Value["website_url"] != "https://example.com" {
		t.Fatalf("channel metadata = %#v", claimValue.Value)
	}
	if featured, ok := claimValue.Value["featured"].([]any); !ok ||
		len(featured) != 1 || featured[0] != strings.Repeat("33", 20) {
		t.Fatalf("channel featured = %#v", claimValue.Value["featured"])
	}
}

func TestChannelCreateNameValidation(t *testing.T) {
	tests := []struct {
		name, message string
	}{
		{"", "Channel name cannot be blank."},
		{"plain", "Channel names must start with '@' symbol."},
		{"@bad/name", "Channel name has invalid character"},
	}
	for _, test := range tests {
		if err := validateChannelCreateName(test.name); err == nil || err.Error() != test.message {
			t.Fatalf("name %q error = %v, want %q", test.name, err, test.message)
		}
	}
}

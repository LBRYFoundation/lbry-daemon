package rpc

import (
	"context"
	"testing"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestSupportAbandonByClaimIDKeepsReplacementAndReleasesPreview(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	parent := persistSupportAbandonFixture(t, &fixture)
	result := fileMutationRPCResult(t, fixture.server, "support_abandon", map[string]any{
		"claim_id": fixture.claimID, "keep": "1.0", "preview": true,
	})
	encoded, ok := result.(map[string]any)
	if !ok || encoded["txid"] == "" {
		t.Fatalf("support abandon preview = %#v", result)
	}
	outputs := encoded["outputs"].([]any)
	if len(outputs) < 1 || outputs[0].(map[string]any)["amount"] != "1.0" {
		t.Fatalf("support abandon outputs = %#v", outputs)
	}
	supports, err := fixture.ledger.GetSupports(
		context.Background(), walletpkg.ClaimListOptions{
			Query: ledgerdb.OutputQuery{
				AccountIDs: []string{fixture.account.ID}, TXOID: parent.Outputs[0].ID(),
			},
		},
	)
	if err != nil || len(supports) != 1 {
		t.Fatalf("preview did not release support: %#v, %v", supports, err)
	}
}

func TestSupportAbandonByOutpointBroadcasts(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	fixture.network.downloadComplete = nil
	parent := persistSupportAbandonFixture(t, &fixture)
	result := fileMutationRPCResult(t, fixture.server, "support_abandon", map[string]any{
		"txid": parent.ID, "nout": 0,
	})
	if encoded, ok := result.(map[string]any); !ok || encoded["txid"] == "" {
		t.Fatalf("support abandon result = %#v", result)
	}
	fixture.network.mu.Lock()
	broadcasts := len(fixture.network.broadcasts)
	fixture.network.mu.Unlock()
	if broadcasts != 1 {
		t.Fatalf("support abandon broadcasts = %d", broadcasts)
	}
}

func persistSupportAbandonFixture(t *testing.T, fixture *paidGetFixture) *walletpkg.Transaction {
	t.Helper()
	addresses, err := fixture.account.Receiving.GetAddresses(context.Background(), false)
	if err != nil || len(addresses) == 0 {
		t.Fatalf("support addresses = %v, %v", addresses, err)
	}
	address := addresses[0]
	decoded, err := keys.DecodeBase58(address)
	if err != nil || len(decoded) < 21 {
		t.Fatalf("decode support address = %x, %v", decoded, err)
	}
	output, err := walletpkg.NewSupportOutput(
		200_000_000, "paid", fixture.claimID, decoded[1:21],
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction := walletpkg.NewTransaction().AddInputs([]walletpkg.TransactionInput{{
		PreviousIndex: ^uint32(0), Sequence: ^uint32(0), Coinbase: []byte{3},
	}}).AddOutputs([]walletpkg.TransactionOutput{output})
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	transaction.Height, transaction.Position, transaction.IsVerified = 2, 2, true
	claimID := fixture.claimID
	if err := fixture.ledger.Database.SaveTransactionIOBatch(
		context.Background(), []ledgerdb.TransactionIORow{{
			Transaction: ledgerdb.TransactionRow{
				TXID: transaction.ID, Raw: transaction.Raw, Height: 2, Position: 2, IsVerified: true,
			},
			Outputs: []ledgerdb.TransactionOutputRow{{
				TXOID: transaction.Outputs[0].ID(), Address: &address, Position: 0,
				Amount: 200_000_000, Script: transaction.Outputs[0].Script.Source,
				TXOType: walletpkg.TransactionOutputTypeSupport, ClaimID: &claimID,
			}},
		}}, address, "",
	); err != nil {
		t.Fatal(err)
	}
	return transaction
}

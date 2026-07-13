package rpc

import (
	"context"
	"testing"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestClaimAbandonWrappersPreserveGenericClaimSelection(t *testing.T) {
	for _, method := range []string{"stream_abandon", "collection_abandon", "channel_abandon"} {
		t.Run(method, func(t *testing.T) {
			fixture := newPaidGetFixture(t, false)
			parent, claimID := persistClaimAbandonFixture(t, &fixture)
			result := fileMutationRPCResult(t, fixture.server, method, map[string]any{
				"claim_id": claimID, "preview": true,
			})
			if encoded, ok := result.(map[string]any); !ok || encoded["txid"] == "" {
				t.Fatalf("%s result = %#v", method, result)
			}
			claims, err := fixture.ledger.GetClaims(
				context.Background(), walletpkg.ClaimListOptions{Query: ledgerdb.OutputQuery{
					AccountIDs: []string{fixture.account.ID}, TXOID: parent.Outputs[0].ID(),
				}},
			)
			if err != nil || len(claims) != 1 {
				t.Fatalf("%s preview release = %#v, %v", method, claims, err)
			}
		})
	}
}

func TestStreamAbandonExactOutpointBroadcasts(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	fixture.network.downloadComplete = nil
	parent, _ := persistClaimAbandonFixture(t, &fixture)
	result := fileMutationRPCResult(t, fixture.server, "stream_abandon", map[string]any{
		"txid": parent.ID, "nout": 0, "blocking": false,
	})
	if encoded, ok := result.(map[string]any); !ok || encoded["txid"] == "" {
		t.Fatalf("stream abandon result = %#v", result)
	}
	fixture.network.mu.Lock()
	broadcasts := len(fixture.network.broadcasts)
	fixture.network.mu.Unlock()
	if broadcasts != 1 {
		t.Fatalf("stream abandon broadcasts = %d", broadcasts)
	}
}

func persistClaimAbandonFixture(
	t *testing.T, fixture *paidGetFixture,
) (*walletpkg.Transaction, string) {
	t.Helper()
	addresses, err := fixture.account.Receiving.GetAddresses(context.Background(), false)
	if err != nil || len(addresses) == 0 {
		t.Fatalf("claim addresses = %v, %v", addresses, err)
	}
	address := addresses[0]
	decoded, err := keys.DecodeBase58(address)
	if err != nil || len(decoded) < 21 {
		t.Fatalf("decode claim address = %x, %v", decoded, err)
	}
	output := walletpkg.NewClaimNameOutput(
		200_000_000, "local-stream", getTestStreamClaim(getTestHash([]byte("local-sd"))), decoded[1:21],
	)
	transaction := walletpkg.NewTransaction().AddInputs([]walletpkg.TransactionInput{{
		PreviousIndex: ^uint32(0), Sequence: ^uint32(0), Coinbase: []byte{4},
	}}).AddOutputs([]walletpkg.TransactionOutput{output})
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	transaction.Height, transaction.Position, transaction.IsVerified = 3, 3, true
	claimID, err := transaction.Outputs[0].ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	claimName := "local-stream"
	if err := fixture.ledger.Database.SaveTransactionIOBatch(
		context.Background(), []ledgerdb.TransactionIORow{{
			Transaction: ledgerdb.TransactionRow{
				TXID: transaction.ID, Raw: transaction.Raw, Height: 3, Position: 3, IsVerified: true,
			},
			Outputs: []ledgerdb.TransactionOutputRow{{
				TXOID: transaction.Outputs[0].ID(), Address: &address, Position: 0,
				Amount: 200_000_000, Script: transaction.Outputs[0].Script.Source,
				TXOType: walletpkg.TransactionOutputTypeStream, ClaimID: &claimID, ClaimName: &claimName,
				HasSource: true,
			}},
		}}, address, "",
	); err != nil {
		t.Fatal(err)
	}
	return transaction, claimID
}

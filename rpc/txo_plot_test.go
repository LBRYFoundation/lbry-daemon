package rpc

import (
	"context"
	"testing"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/keys"
)

func TestTXOPlotGroupsDaysAndAppliesExplicitBounds(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	addresses, err := fixture.account.Receiving.GetAddresses(context.Background(), false)
	if err != nil || len(addresses) == 0 {
		t.Fatalf("addresses = %v, %v", addresses, err)
	}
	decoded, err := keys.DecodeBase58(addresses[0])
	if err != nil {
		t.Fatal(err)
	}
	var target [20]byte
	copy(target[:], decoded[1:21])
	for index, item := range []struct {
		day    string
		amount uint64
	}{
		{"2024-01-01", 100_000_000},
		{"2024-01-01", 200_000_000},
		{"2024-01-03", 300_000_000},
	} {
		julian, err := txoPlotJulianDay(item.day)
		if err != nil {
			t.Fatal(err)
		}
		transaction := walletpkg.NewTransaction().AddInputs([]walletpkg.TransactionInput{{
			PreviousIndex: ^uint32(0), Sequence: ^uint32(0), Coinbase: []byte{byte(index + 10)},
		}}).AddOutputs([]walletpkg.TransactionOutput{
			walletpkg.NewPayPubKeyHashOutput(item.amount, target[:]),
		})
		transaction.Height, transaction.Position, transaction.IsVerified = int64(100+index), int64(index), true
		transaction.JulianDay = &julian
		if err := transaction.RebuildDerived(); err != nil {
			t.Fatal(err)
		}
		if err := fixture.ledger.SaveTransactionIOBatch(
			context.Background(), []*walletpkg.Transaction{transaction}, addresses[0], target, "",
		); err != nil {
			t.Fatal(err)
		}
	}

	bounded := fileMutationRPCResult(t, fixture.server, "txo_plot", map[string]any{
		"account_id": fixture.account.ID, "start_day": "2024-01-01",
		"end_day": "2024-01-02", "days_after": 99,
	}).([]any)
	if len(bounded) != 1 || bounded[0].(map[string]any)["day"] != "2024-01-01" ||
		bounded[0].(map[string]any)["total"] != "3.0" {
		t.Fatalf("bounded txo_plot = %#v", bounded)
	}
	window := fileMutationRPCResult(t, fixture.server, "txo_plot", map[string]any{
		"start_day": "2024-01-01", "days_after": 2,
	}).([]any)
	if len(window) != 2 || window[1].(map[string]any)["day"] != "2024-01-03" ||
		window[1].(map[string]any)["total"] != "3.0" {
		t.Fatalf("window txo_plot = %#v", window)
	}
}

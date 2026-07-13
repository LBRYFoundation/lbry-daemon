package rpc

import (
	"context"
	"testing"
)

func TestAccountSendPreviewBuildsPaymentsAndReleasesInputs(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	addresses, err := fixture.account.Receiving.GetAddresses(context.Background(), false)
	if err != nil || len(addresses) == 0 {
		t.Fatalf("addresses = %v, %v", addresses, err)
	}
	result := fileMutationRPCResult(t, fixture.server, "account_send", map[string]any{
		"amount": "1.0", "addresses": []any{addresses[0], addresses[1]}, "preview": true,
	}).(map[string]any)
	outputs := result["outputs"].([]any)
	if result["txid"] == "" || len(outputs) != 3 ||
		outputs[0].(map[string]any)["amount"] != "1.0" || outputs[1].(map[string]any)["amount"] != "1.0" {
		t.Fatalf("account_send = %#v", result)
	}
	assertAccountTransactionSpendables(t, &fixture, 1)
}

func TestAccountFundSplitsOutputsAndReleasesByDefault(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	result := fileMutationRPCResult(t, fixture.server, "account_fund", map[string]any{
		"amount": "2.0", "outputs": 2,
	}).(map[string]any)
	outputs := result["outputs"].([]any)
	if len(outputs) != 3 || outputs[0].(map[string]any)["amount"] != "1.0" ||
		outputs[1].(map[string]any)["amount"] != "1.0" {
		t.Fatalf("account_fund = %#v", result)
	}
	assertAccountTransactionSpendables(t, &fixture, 1)
}

func assertAccountTransactionSpendables(t *testing.T, fixture *paidGetFixture, expected int) {
	t.Helper()
	spendables, err := fixture.ledger.Database.ListSpendableOutputs(
		context.Background(), []string{fixture.account.ID},
	)
	if err != nil || len(spendables) != expected {
		t.Fatalf("spendables = %#v, %v, want %d", spendables, err, expected)
	}
}

package rpc

import (
	"context"
	"testing"
)

func TestTXOSpendPreviewBatchesAndReturnsCompactIDs(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	persistStreamMutationFixture(t, &fixture, "first-spend")
	persistStreamMutationFixture(t, &fixture, "second-spend")
	result := fileMutationRPCResult(t, fixture.server, "txo_spend", map[string]any{
		"account_id": fixture.account.ID, "batch_size": 2, "preview": true,
	}).([]any)
	if len(result) != 2 {
		t.Fatalf("txo_spend batches = %#v", result)
	}
	for index, item := range result {
		compact := item.(map[string]any)
		if len(compact) != 1 || compact["txid"] == "" {
			t.Fatalf("compact transaction %d = %#v", index, compact)
		}
	}
	spendables, err := fixture.ledger.Database.ListSpendableOutputs(
		context.Background(), []string{fixture.account.ID},
	)
	if err != nil || len(spendables) != 1 {
		t.Fatalf("ordinary spendables after preview = %#v, %v", spendables, err)
	}
}

func TestTXOSpendPreviewFullTransactionAndTypeFilter(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	persistStreamMutationFixture(t, &fixture, "filtered-spend")
	result := fileMutationRPCResult(t, fixture.server, "txo_spend", map[string]any{
		"type": "stream", "include_full_tx": true, "preview": true,
	}).([]any)
	if len(result) != 1 {
		t.Fatalf("filtered txo_spend = %#v", result)
	}
	transaction := result[0].(map[string]any)
	inputs := transaction["inputs"].([]any)
	if transaction["txid"] == "" || len(inputs) != 1 || len(transaction["outputs"].([]any)) != 1 {
		t.Fatalf("full txo_spend = %#v", transaction)
	}
}

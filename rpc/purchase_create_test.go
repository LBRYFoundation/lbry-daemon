package rpc

import (
	"context"
	"testing"
)

func TestPurchaseCreateURLPreviewReleasesWithoutBroadcast(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	result := fileMutationRPCResult(t, fixture.server, "purchase_create", map[string]any{
		"url": "paid", "preview": true,
	})
	transaction, ok := result.(map[string]any)
	if !ok || transaction["txid"] == "" || transaction["outputs"] == nil {
		t.Fatalf("purchase preview result = %#v", result)
	}
	fixture.network.mu.Lock()
	broadcasts := len(fixture.network.broadcasts)
	fixture.network.mu.Unlock()
	if broadcasts != 0 {
		t.Fatalf("preview broadcasts = %d", broadcasts)
	}
	spendable, err := fixture.ledger.Database.ListSpendableOutputs(
		context.Background(), []string{fixture.account.ID},
	)
	if err != nil || len(spendable) != 1 {
		t.Fatalf("preview spendables = %#v, %v", spendable, err)
	}
}

func TestPurchaseCreateURLBroadcastsFundedTransaction(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	fixture.network.downloadComplete = nil
	result := fileMutationRPCResult(t, fixture.server, "purchase_create", map[string]any{
		"url": "paid",
	})
	transaction, ok := result.(map[string]any)
	if !ok || transaction["txid"] == "" {
		t.Fatalf("purchase result = %#v", result)
	}
	fixture.network.mu.Lock()
	broadcasts := len(fixture.network.broadcasts)
	fixture.network.mu.Unlock()
	if broadcasts != 1 {
		t.Fatalf("purchase broadcasts = %d", broadcasts)
	}
	spendable, err := fixture.ledger.Database.ListSpendableOutputs(
		context.Background(), []string{fixture.account.ID},
	)
	if err != nil || len(spendable) != 0 {
		t.Fatalf("broadcast spendables = %#v, %v", spendable, err)
	}
}

func TestPurchaseCreateOverrideMaximumFee(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	if _, err := fixture.server.settings.Set("max_key_fee", map[string]any{
		"currency": "LBC", "amount": 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	result := fileMutationRPCResult(t, fixture.server, "purchase_create", map[string]any{
		"url": "paid", "preview": true, "override_max_key_fee": true,
	})
	if transaction, ok := result.(map[string]any); !ok || transaction["txid"] == "" {
		t.Fatalf("override purchase result = %#v", result)
	}
}

func TestPurchaseCreateClaimIDUsesClaimSearch(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	result := fileMutationRPCResult(t, fixture.server, "purchase_create", map[string]any{
		"claim_id": fixture.claimID, "preview": true,
	})
	if transaction, ok := result.(map[string]any); !ok || transaction["txid"] == "" {
		t.Fatalf("claim-id purchase result = %#v", result)
	}
}

package wallet

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"

	"lbry/daemon/wallet/ledgerdb"
)

func TestEnrichResolvedTransactionOutputAnnotationsDirectionAndCopySemantics(t *testing.T) {
	ctx := context.Background()
	ledger := newTransactionOutputQueryLedger(t)
	targetTransaction := transactionHistoryUnitCoinbase(t, 9_101, NewClaimNameOutput(
		1, "target", claimWireOracleMustHex(t, claimWireOracleFixtureProto),
		bytes.Repeat([]byte{0x81}, 20),
	))
	targetTransaction.Height = 7
	target := &targetTransaction.Outputs[0]
	claimID, err := target.ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	targetAddress, err := target.Address(ledger.Network)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Database.AddKeys(ctx, "account-a", []ledgerdb.AddressKey{
		{Address: targetAddress, PublicKey: []byte{1}, ChainCode: []byte{2}},
		{Address: "owned-support", PublicKey: []byte{3}, ChainCode: []byte{4}, N: 1},
	}); err != nil {
		t.Fatal(err)
	}
	persistResolvedAnnotationOutput(
		t, ledger, targetTransaction, targetAddress, TransactionOutputTypeStream, claimID, nil,
		targetAddress,
	)

	persistResolvedAnnotationSupport(t, ledger, 9_111, 11, claimID, "owned-support", "owned-support", false)
	persistResolvedAnnotationSupport(t, ledger, 9_112, 12, claimID, "owned-support", "owned-support", true)
	persistResolvedAnnotationSupport(t, ledger, 9_113, 22, claimID, "foreign-output", "owned-support", false)
	persistResolvedAnnotationSupport(t, ledger, 9_114, 23, claimID, "foreign-output", "owned-support", true)
	persistResolvedAnnotationSupport(t, ledger, 9_115, 33, claimID, "owned-support", "foreign-input", false)
	persistResolvedAnnotationSupport(t, ledger, 9_116, 34, claimID, "owned-support", "foreign-input", true)
	persistResolvedAnnotationSupport(t, ledger, 9_117, 99, claimID, "foreign-output", "foreign-input", false)
	persistResolvedAnnotationSupport(t, ledger, 9_118, 100, "other-claim", "owned-support", "owned-support", false)

	staleTrue, staleAmount := true, int64(999)
	channel := &TransactionOutput{TransactionID: "channel"}
	receipt := &TransactionOutput{TransactionID: "receipt"}
	target.IsSpent = &staleTrue
	target.IsMyOutput = &staleTrue
	target.IsMyInput = &staleTrue
	target.IsInternalTransfer = &staleTrue
	target.SentSupports = &staleAmount
	target.SentTips = &staleAmount
	target.ReceivedTips = &staleAmount
	target.Channel = channel
	target.PurchaseReceipt = receipt
	target.Meta = map[string]any{"shared": true}
	malformed := transactionHistoryUnitCoinbase(t, 9_102, NewClaimNameOutput(
		1, "malformed", []byte{0, 0x80}, bytes.Repeat([]byte{0x82}, 20),
	))
	malformed.Outputs[0].ReceivedTips = &staleAmount
	payment := transactionHistoryUnitCoinbase(
		t, 9_103, NewPayPubKeyHashOutput(1, bytes.Repeat([]byte{0x83}, 20)),
	)
	payment.Outputs[0].SentTips = &staleAmount

	enriched, err := ledger.EnrichResolvedTransactionOutputAnnotations(
		ctx,
		[]*TransactionOutput{target, &malformed.Outputs[0], &payment.Outputs[0], nil},
		ResolvedTransactionOutputAnnotationOptions{
			AccountIDs:          []string{"account-a"},
			IncludeIsMyOutput:   true,
			IncludeSentSupports: true,
			IncludeSentTips:     true,
			IncludeReceivedTips: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(enriched) != 4 || enriched[3] != nil {
		t.Fatalf("enriched output shape = %#v", enriched)
	}
	got := enriched[0]
	if got == target || got.owner == target.owner || currentTransactionOutput(got) != got ||
		got.owner == nil || got.owner.Height != target.owner.Height {
		t.Fatalf("throwaway output copy = %p owner %p, source %p owner %p",
			got, got.owner, target, target.owner)
	}
	if got.IsSpent != nil || got.IsMyInput != nil || got.IsInternalTransfer != nil ||
		got.IsMyOutput == nil || !*got.IsMyOutput ||
		got.SentSupports == nil || *got.SentSupports != 11 ||
		got.SentTips == nil || *got.SentTips != 45 ||
		got.ReceivedTips == nil || *got.ReceivedTips != 67 {
		t.Fatalf("resolved annotations = %#v", got)
	}
	if got.Channel != channel || got.PurchaseReceipt != nil {
		t.Fatalf("resolved relation reset = channel %p receipt %p", got.Channel, got.PurchaseReceipt)
	}
	if target.SentSupports == nil || *target.SentSupports != 999 ||
		target.ReceivedTips == nil || *target.ReceivedTips != 999 ||
		target.Channel != channel || target.PurchaseReceipt != receipt {
		t.Fatalf("cached source annotations were mutated: %#v", target)
	}
	got.Meta["shared"] = "updated"
	if target.Meta["shared"] != "updated" {
		t.Fatalf("shallow-copy meta = %#v, source %#v", got.Meta, target.Meta)
	}
	if enriched[1].ReceivedTips != nil || enriched[2].SentTips != nil {
		t.Fatalf("non-decodable outputs were enriched: malformed %#v payment %#v", enriched[1], enriched[2])
	}

	receivedWithPurchaseGate, err := ledger.EnrichResolvedTransactionOutputAnnotations(
		ctx, []*TransactionOutput{target},
		ResolvedTransactionOutputAnnotationOptions{
			AccountIDs:               []string{"account-a"},
			PurchaseReceiptRequested: true,
			IncludeReceivedTips:      true,
		},
	)
	if err != nil || len(receivedWithPurchaseGate) != 1 ||
		receivedWithPurchaseGate[0].ReceivedTips == nil ||
		*receivedWithPurchaseGate[0].ReceivedTips != 67 {
		t.Fatalf(
			"purchase-receipt received-tips gate = %#v, %v, want 67",
			receivedWithPurchaseGate, err,
		)
	}
}

func TestEnrichResolvedTransactionOutputAnnotationsReceivedTipsGate(t *testing.T) {
	ledger := newTransactionOutputQueryLedger(t)
	target := transactionHistoryUnitCoinbase(t, 9_201, NewClaimNameOutput(
		1, "target", claimWireOracleMustHex(t, claimWireOracleFixtureProto),
		bytes.Repeat([]byte{0x91}, 20),
	))
	stale := int64(9)
	target.Outputs[0].ReceivedTips = &stale

	enriched, err := ledger.EnrichResolvedTransactionOutputAnnotations(
		nil, []*TransactionOutput{&target.Outputs[0]},
		ResolvedTransactionOutputAnnotationOptions{
			AccountIDs: []string{"account-a"}, IncludeReceivedTips: true,
		},
	)
	if err != nil || len(enriched) != 1 || enriched[0].ReceivedTips != nil {
		t.Fatalf("received-tips-only gate = %#v, %v, want reset nil", enriched, err)
	}
	if target.Outputs[0].ReceivedTips == nil || *target.Outputs[0].ReceivedTips != 9 {
		t.Fatalf("received-tips gate mutated source: %#v", target.Outputs[0].ReceivedTips)
	}

	var nilLedger *Ledger
	if outputs, err := nilLedger.EnrichResolvedTransactionOutputAnnotations(
		nil, []*TransactionOutput{&target.Outputs[0]},
		ResolvedTransactionOutputAnnotationOptions{
			AccountIDs: []string{"account-a"}, IncludeSentTips: true,
		},
	); outputs != nil || !errors.Is(err, ErrTransactionOutputQueryUnavailable) {
		t.Fatalf("unavailable annotation query = %#v, %v", outputs, err)
	}
}

func persistResolvedAnnotationOutput(
	t *testing.T,
	ledger *Ledger,
	transaction *Transaction,
	address string,
	outputType int64,
	claimID string,
	inputs []ledgerdb.TransactionInputRow,
	inputAddress string,
) {
	t.Helper()
	storedAddress := address
	if err := ledger.Database.SaveTransactionIOBatch(context.Background(), []ledgerdb.TransactionIORow{{
		Transaction: ledgerdb.TransactionRow{
			TXID: transaction.ID, Raw: append([]byte(nil), transaction.Raw...),
			Height: transaction.Height, Position: transaction.Position,
		},
		Inputs: inputs,
		Outputs: []ledgerdb.TransactionOutputRow{{
			TXOID: transaction.Outputs[0].ID(), Address: &storedAddress,
			Position: 0, Amount: int64(transaction.Outputs[0].Amount),
			Script:  append([]byte(nil), transaction.Outputs[0].Script.Source...),
			TXOType: outputType, ClaimID: &claimID,
		}},
	}}, inputAddress, ""); err != nil {
		t.Fatal(err)
	}
}

func persistResolvedAnnotationSupport(
	t *testing.T,
	ledger *Ledger,
	nonce uint32,
	amount int64,
	claimID string,
	outputAddress string,
	inputAddress string,
	spent bool,
) {
	t.Helper()
	var previousHash [32]byte
	previousHash[0] = byte(nonce)
	previousHash[1] = byte(nonce >> 8)
	transaction := NewTransaction().AddInputs([]TransactionInput{{
		PreviousHash: previousHash, PreviousIndex: 0, Sequence: math.MaxUint32,
		Script: TransactionInputScript{Source: []byte{0x51}},
	}}).AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(uint64(amount), bytes.Repeat([]byte{byte(nonce)}, 20)),
	})
	transaction.LockTime = nonce
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	persistResolvedAnnotationOutput(
		t, ledger, transaction, outputAddress, TransactionOutputTypeSupport, claimID,
		[]ledgerdb.TransactionInputRow{{TXOID: "funding:" + transaction.ID, Position: 0}},
		inputAddress,
	)
	if spent {
		markTransactionOutputQuerySpent(
			t, ledger, inputAddress, transaction.Outputs[0].ID(), nonce+10_000,
		)
	}
}

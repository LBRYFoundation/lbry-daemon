package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"math"
	"testing"

	"lbry/daemon/wallet/ledgerdb"
)

const transactionOutputReceiptPricedStream = "000a5c0a360a02000112096d6f7669652e6d70341881808080808080102209766964656f2f6d70342a0968747470733a2f2f78320202033a02040528ffffffffffffffffff0132090803120300010218015a0c08800f10b80818017a02080242055469746c65520a0a01aa2a057468756d625a036f6e655a0374776f62070801105818ec016205100518fa016a0e08fa011205737461746528023001"

func TestEnrichResolvedTransactionOutputPurchaseReceiptsBulkAndAccountScope(t *testing.T) {
	ctx := context.Background()
	ledger := newTransactionOutputQueryLedger(t)
	accountA, addressA := newTransactionOutputQueryAccount(t, ledger, 0xa1, "fallback-a")
	accountB, addressB := newTransactionOutputQueryAccount(t, ledger, 0xb1, "fallback-b")
	wallet := NewWallet(WithWalletAccounts([]*Account{accountA, accountB}))

	pricedPayload := transactionOutputReceiptPayload(t, transactionOutputReceiptPricedStream)
	pricedA := transactionHistoryUnitCoinbase(t, 31_001, NewClaimNameOutput(
		100_000_000, "priced-a", pricedPayload, bytes.Repeat([]byte{0x31}, 20),
	))
	pricedB := transactionHistoryUnitCoinbase(t, 31_002, NewClaimNameOutput(
		100_000_000, "priced-b", pricedPayload, bytes.Repeat([]byte{0x32}, 20),
	))
	unpriced := transactionHistoryUnitCoinbase(t, 31_003, NewClaimNameOutput(
		100_000_000, "unpriced", transactionOutputReceiptPayload(t, claimWireOracleFixtureProto),
		bytes.Repeat([]byte{0x33}, 20),
	))
	claimAID := transactionOutputReceiptClaimID(t, &pricedA.Outputs[0])
	claimBID := transactionOutputReceiptClaimID(t, &pricedB.Outputs[0])
	receiptA := persistTransactionOutputReceiptPurchase(t, ledger, 31_101, claimAID, addressA)
	receiptB := persistTransactionOutputReceiptPurchase(t, ledger, 31_102, claimBID, addressB)
	if !transactionOutputHasPrice(&pricedA.Outputs[0]) || !transactionOutputHasPrice(&pricedB.Outputs[0]) ||
		transactionOutputHasPrice(&unpriced.Outputs[0]) {
		t.Fatal("priced stream fixture classification does not match the pinned has_price gate")
	}
	accountAIDs, err := transactionPurchaseAccountIDs([]*Account{accountA})
	if err != nil {
		t.Fatal(err)
	}
	selectedPurchases, err := ledger.getPurchasesByAccountIDs(
		ctx,
		ledgerdb.TransactionQuery{PurchasedClaimIDs: []string{claimAID, claimBID}},
		accountAIDs,
		wallet.Accounts,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(selectedPurchases) != 1 || selectedPurchases[0].TransactionID != receiptA.ID ||
		selectedPurchases[0].Purchase == nil {
		t.Fatalf("bulk selected-account purchases = %#v", selectedPurchases)
	}

	staleReceipt := &TransactionOutput{TransactionID: "stale"}
	pricedA.Outputs[0].PurchaseReceipt = staleReceipt
	pricedB.Outputs[0].PurchaseReceipt = staleReceipt

	selectedA, err := ledger.EnrichResolvedTransactionOutputAnnotations(
		ctx,
		[]*TransactionOutput{&pricedA.Outputs[0], &pricedB.Outputs[0], &unpriced.Outputs[0]},
		ResolvedTransactionOutputAnnotationOptions{
			Accounts: []*Account{accountA}, Wallet: wallet, PurchaseReceiptRequested: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertTransactionOutputReceipt(t, selectedA[0].PurchaseReceipt, receiptA.ID, claimAID)
	if selectedA[1].PurchaseReceipt != nil || selectedA[2].PurchaseReceipt != nil {
		t.Fatalf("selected account receipts = %#v", selectedA)
	}
	if pricedA.Outputs[0].PurchaseReceipt != staleReceipt || pricedB.Outputs[0].PurchaseReceipt != staleReceipt {
		t.Fatal("purchase receipt enrichment mutated cached claim outputs")
	}
	accountIDs, err := transactionPurchaseAccountIDs([]*Account{accountA, accountB})
	if err != nil {
		t.Fatal(err)
	}
	allPurchases, err := ledger.getPurchasesByAccountIDs(
		ctx,
		ledgerdb.TransactionQuery{PurchasedClaimIDs: []string{claimAID, claimBID}},
		accountIDs,
		wallet.Accounts,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(allPurchases) != 2 {
		t.Fatalf(
			"bulk two-account purchases = %#v, want receipts %s and %s for accounts %v",
			allPurchases, receiptA.ID, receiptB.ID, accountIDs,
		)
	}

	selectedBoth, err := ledger.EnrichResolvedTransactionOutputAnnotations(
		ctx,
		[]*TransactionOutput{&pricedA.Outputs[0], &pricedB.Outputs[0], &unpriced.Outputs[0]},
		ResolvedTransactionOutputAnnotationOptions{
			Accounts: []*Account{accountA, accountB}, Wallet: wallet, PurchaseReceiptRequested: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertTransactionOutputReceipt(t, selectedBoth[0].PurchaseReceipt, receiptA.ID, claimAID)
	assertTransactionOutputReceipt(t, selectedBoth[1].PurchaseReceipt, receiptB.ID, claimBID)
	if selectedBoth[2].PurchaseReceipt != nil {
		t.Fatalf("unpriced claim receipt = %#v, want nil", selectedBoth[2].PurchaseReceipt)
	}
}

func TestEnrichResolvedTransactionOutputPurchaseReceiptLookupSkipsDisabledAndUnpriced(t *testing.T) {
	ledger := newTransactionOutputQueryLedger(t)
	if err := ledger.Database.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	priced := transactionHistoryUnitCoinbase(t, 32_001, NewClaimNameOutput(
		100_000_000, "priced", transactionOutputReceiptPayload(t, transactionOutputReceiptPricedStream),
		bytes.Repeat([]byte{0x41}, 20),
	))
	unpriced := transactionHistoryUnitCoinbase(t, 32_002, NewClaimNameOutput(
		100_000_000, "unpriced", transactionOutputReceiptPayload(t, claimWireOracleFixtureProto),
		bytes.Repeat([]byte{0x42}, 20),
	))
	staleReceipt := &TransactionOutput{TransactionID: "stale"}
	priced.Outputs[0].PurchaseReceipt = staleReceipt
	unpriced.Outputs[0].PurchaseReceipt = staleReceipt

	disabled, err := ledger.EnrichResolvedTransactionOutputAnnotations(
		context.Background(), []*TransactionOutput{&priced.Outputs[0]},
		ResolvedTransactionOutputAnnotationOptions{},
	)
	if err != nil || len(disabled) != 1 || disabled[0].PurchaseReceipt != nil {
		t.Fatalf("disabled purchase receipt enrichment = %#v, %v", disabled, err)
	}

	skipped, err := ledger.EnrichResolvedTransactionOutputAnnotations(
		context.Background(), []*TransactionOutput{&unpriced.Outputs[0]},
		ResolvedTransactionOutputAnnotationOptions{
			Accounts: []*Account{{ID: "account-a"}}, PurchaseReceiptRequested: true,
		},
	)
	if err != nil || len(skipped) != 1 || skipped[0].PurchaseReceipt != nil {
		t.Fatalf("unpriced purchase receipt enrichment = %#v, %v", skipped, err)
	}
	if priced.Outputs[0].PurchaseReceipt != staleReceipt || unpriced.Outputs[0].PurchaseReceipt != staleReceipt {
		t.Fatal("skipped purchase receipt enrichment mutated cached claim outputs")
	}

	withoutDatabase := &Ledger{}
	malformed := transactionHistoryUnitCoinbase(t, 32_003, NewClaimNameOutput(
		100_000_000, "malformed", []byte{0, 0x80}, bytes.Repeat([]byte{0x43}, 20),
	))
	skipped, err = withoutDatabase.EnrichResolvedTransactionOutputAnnotations(
		context.Background(), []*TransactionOutput{nil, &malformed.Outputs[0]},
		ResolvedTransactionOutputAnnotationOptions{
			Accounts:                 []*Account{{ID: "account-a"}},
			PurchaseReceiptRequested: true, IncludeIsMyOutput: true,
		},
	)
	if err != nil || len(skipped) != 2 || skipped[0] != nil || skipped[1] == nil {
		t.Fatalf("non-querying enrichment without database = %#v, %v", skipped, err)
	}
	_, err = withoutDatabase.EnrichResolvedTransactionOutputAnnotations(
		context.Background(), []*TransactionOutput{&malformed.Outputs[0]},
		ResolvedTransactionOutputAnnotationOptions{
			Accounts: []*Account{nil}, IncludeIsMyOutput: true,
		},
	)
	if err != nil {
		t.Fatalf("non-querying malformed account scope = %v", err)
	}
}

func persistTransactionOutputReceiptPurchase(
	t *testing.T, ledger *Ledger, nonce uint32, claimID, inputAddress string,
) *Transaction {
	t.Helper()
	purchaseData, err := NewPurchaseDataOutput(claimID)
	if err != nil {
		t.Fatal(err)
	}
	var previousHash [32]byte
	previousHash[0] = byte(nonce)
	previousHash[1] = byte(nonce >> 8)
	previousTxID := hex.EncodeToString(reverseTransactionBytes(previousHash[:]))
	transaction := NewTransaction().AddInputs([]TransactionInput{{
		PreviousHash: previousHash, PreviousTxID: previousTxID,
		PreviousIndex: 0, Sequence: math.MaxUint32,
		Script: TransactionInputScript{Source: []byte{0x51}},
	}}).AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(50_000_000, bytes.Repeat([]byte{byte(nonce)}, 20)),
		purchaseData,
	})
	transaction.LockTime = nonce
	transaction.Height = int64(nonce)
	transaction.Position = int64(nonce % 100)
	transaction.IsVerified = true
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	metadata := ProjectTransactionMetadata(transaction)
	if metadata.PurchasedClaimID == nil || *metadata.PurchasedClaimID != claimID {
		t.Fatalf("purchase metadata = %#v, want claim %s", metadata, claimID)
	}
	outputAddress := "merchant-" + transaction.ID
	if err := ledger.Database.SaveTransactionIOBatch(
		context.Background(),
		[]ledgerdb.TransactionIORow{{
			Transaction: ledgerdb.TransactionRow{
				TXID: transaction.ID, Raw: append([]byte(nil), transaction.Raw...),
				Height: transaction.Height, Position: transaction.Position,
				IsVerified: true, PurchasedClaimID: metadata.PurchasedClaimID,
			},
			Inputs: []ledgerdb.TransactionInputRow{{
				TXOID: transaction.Inputs[0].PreviousOutputID(), Position: 0,
			}},
			Outputs: []ledgerdb.TransactionOutputRow{{
				TXOID: transaction.Outputs[0].ID(), Address: &outputAddress,
				Position: 0, Amount: int64(transaction.Outputs[0].Amount),
				Script:  append([]byte(nil), transaction.Outputs[0].Script.Source...),
				TXOType: TransactionOutputTypePurchase, ClaimID: metadata.PurchasedClaimID,
			}},
		}},
		inputAddress,
		"",
	); err != nil {
		t.Fatal(err)
	}
	return transaction
}

func assertTransactionOutputReceipt(
	t *testing.T, receipt *TransactionOutput, transactionID, claimID string,
) {
	t.Helper()
	if receipt == nil || receipt.TransactionID != transactionID || receipt.Position != 0 ||
		receipt.Purchase == nil {
		t.Fatalf("purchase receipt = %#v, want %s:0", receipt, transactionID)
	}
	gotClaimID, ok := decodeTransactionPurchase(receipt.Purchase.Script)
	if !ok || gotClaimID != claimID {
		t.Fatalf("purchase receipt claim = %q, %v, want %q", gotClaimID, ok, claimID)
	}
}

func transactionOutputReceiptClaimID(t *testing.T, output *TransactionOutput) string {
	t.Helper()
	claimID, err := output.ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	return claimID
}

func transactionOutputReceiptPayload(t *testing.T, encoded string) []byte {
	t.Helper()
	payload, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

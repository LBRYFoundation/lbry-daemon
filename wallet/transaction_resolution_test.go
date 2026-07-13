package wallet

import (
	"context"
	"errors"
	"strings"
	"testing"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestResolveTransactionInputsUsesPendingThenStoredOutputThenStoredRaw(t *testing.T) {
	ctx := context.Background()
	database, err := ledgerdb.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	ledger := &Ledger{Network: keys.MainNet, Database: database}

	pending, err := ParseTransaction(mustTransactionHex(t, genesisTransactionHex))
	if err != nil {
		t.Fatal(err)
	}
	pendingChild := transactionResolutionChild("pending-child", pending.ID, 0)
	if err := ledger.ResolveTransactionInputs(
		ctx, []*Transaction{pending, pendingChild}, transactionResolutionHistory(pending.ID),
	); err != nil {
		t.Fatal(err)
	}
	if pendingChild.Inputs[0].ResolvedOutput != &pending.Outputs[0] {
		t.Fatalf("pending output = %#v, want pending transaction output", pendingChild.Inputs[0].ResolvedOutput)
	}

	directID := strings.Repeat("11", 32)
	directHash := transactionPersistenceHash(0x22)
	directScript := transactionP2PKH(directHash[:])
	directAddress := "direct-address"
	if err := database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
		Transaction: ledgerdb.TransactionRow{TXID: directID, Raw: []byte{1}},
		Outputs: []ledgerdb.TransactionOutputRow{{
			TXOID: directID + ":0", Address: &directAddress,
			Amount: 17, Script: directScript,
		}},
	}}, "unused", ""); err != nil {
		t.Fatal(err)
	}
	directChild := transactionResolutionChild("direct-child", directID, 0)
	if err := ledger.ResolveTransactionInputs(
		ctx, []*Transaction{directChild}, transactionResolutionHistory(directID),
	); err != nil {
		t.Fatal(err)
	}
	direct := directChild.Inputs[0].ResolvedOutput
	if direct == nil || direct.TransactionID != directID || direct.Amount != 17 ||
		direct.Script.Template != TransactionScriptPayPubKeyHash {
		t.Fatalf("direct stored output = %#v", direct)
	}

	if err := database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
		Transaction: ledgerdb.TransactionRow{
			TXID: pending.ID, Raw: pending.Raw, Height: 8, Position: 3, IsVerified: true,
		},
	}}, "unused", ""); err != nil {
		t.Fatal(err)
	}
	rawChild := transactionResolutionChild("raw-child", pending.ID, 0)
	if err := ledger.ResolveTransactionInputs(
		ctx, []*Transaction{rawChild}, transactionResolutionHistory(pending.ID),
	); err != nil {
		t.Fatal(err)
	}
	rawOutput := rawChild.Inputs[0].ResolvedOutput
	if rawOutput == nil || rawOutput.TransactionID != pending.ID ||
		rawOutput.Amount != pending.Outputs[0].Amount ||
		rawOutput.Script.Template != TransactionScriptPayPubKeyHash {
		t.Fatalf("raw fallback output = %#v", rawOutput)
	}
}

func TestResolveTransactionInputsSkipsUnrelatedAndMissingPredecessors(t *testing.T) {
	ctx := context.Background()
	database, err := ledgerdb.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	ledger := &Ledger{Database: database}
	txid := strings.Repeat("33", 32)

	unrelated := transactionResolutionChild("unrelated", txid, 0)
	if err := ledger.ResolveTransactionInputs(ctx, []*Transaction{unrelated}, nil); err != nil {
		t.Fatal(err)
	}
	if unrelated.Inputs[0].ResolvedOutput != nil {
		t.Fatalf("unrelated predecessor resolved = %#v", unrelated.Inputs[0].ResolvedOutput)
	}

	missing := transactionResolutionChild("missing", txid, 0)
	if err := ledger.ResolveTransactionInputs(
		ctx, []*Transaction{missing}, transactionResolutionHistory(txid),
	); err != nil {
		t.Fatal(err)
	}
	if missing.Inputs[0].ResolvedOutput != nil {
		t.Fatalf("missing predecessor resolved = %#v", missing.Inputs[0].ResolvedOutput)
	}
}

func TestResolveTransactionInputsReportsInvalidReferencedOutputs(t *testing.T) {
	ctx := context.Background()
	parentHash := transactionPersistenceHash(1)
	parent := transactionPersistenceTransaction("parent", 0,
		ParseTransactionOutputScript(transactionP2PKH(parentHash[:])),
	)
	child := transactionResolutionChild("child", parent.ID, 4)
	var ledger *Ledger
	if err := ledger.ResolveTransactionInputs(
		ctx, []*Transaction{parent, child}, transactionResolutionHistory(parent.ID),
	); !errors.Is(err, ErrTransactionOutputOutOfRange) {
		t.Fatalf("pending output range error = %v", err)
	}
}

func transactionResolutionChild(id, previousID string, previousIndex uint32) *Transaction {
	return &Transaction{
		ID: id, Raw: []byte(id), Height: 0, Position: -1,
		Inputs: []TransactionInput{{
			PreviousTxID: previousID, PreviousIndex: previousIndex,
		}},
	}
}

func transactionResolutionHistory(txids ...string) map[string]struct{} {
	history := make(map[string]struct{}, len(txids))
	for _, txid := range txids {
		history[txid] = struct{}{}
	}
	return history
}

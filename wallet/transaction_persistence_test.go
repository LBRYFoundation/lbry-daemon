package wallet

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestProjectTransactionIOBatchFiltersIncomingAndOutgoingOutputs(t *testing.T) {
	targetHash := transactionPersistenceHash(0x11)
	otherHash := transactionPersistenceHash(0x22)
	ledger := transactionPersistenceLedger(nil)

	incoming := transactionPersistenceTransaction("incoming", 0,
		ParseTransactionOutputScript(transactionP2PKH(targetHash[:])),
		ParseTransactionOutputScript(transactionP2SH(targetHash[:])),
		ParseTransactionOutputScript(transactionP2PKH(otherHash[:])),
		ParseTransactionOutputScript(append([]byte{transactionOp0}, transactionPush(targetHash[:])...)),
	)
	resolved := transactionPersistenceOutput(
		"previous", 3, 50, ParseTransactionOutputScript(transactionP2PKH(targetHash[:])),
	)
	outgoing := transactionPersistenceTransaction("outgoing", -1,
		ParseTransactionOutputScript(transactionP2PKH(otherHash[:])),
		ParseTransactionOutputScript(transactionP2SH(otherHash[:])),
		ParseTransactionOutputScript(append([]byte{transactionOp0}, transactionPush(otherHash[:])...)),
	)
	outgoing.Inputs = []TransactionInput{{Position: 2, ResolvedOutput: &resolved}}

	address, err := resolved.Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := ledger.ProjectTransactionIOBatch(
		[]*Transaction{incoming, outgoing}, address, targetHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || len(rows[0].Inputs) != 0 || len(rows[0].Outputs) != 1 ||
		rows[0].Outputs[0].TXOID != "incoming:0" ||
		len(rows[1].Inputs) != 1 || rows[1].Inputs[0].TXOID != "previous:3" ||
		rows[1].Inputs[0].Position != 2 || len(rows[1].Outputs) != 2 ||
		rows[1].Outputs[0].TXOID != "outgoing:0" ||
		rows[1].Outputs[1].TXOID != "outgoing:1" {
		t.Fatalf("projected transaction rows = %#v", rows)
	}
	if rows[0].Outputs[0].Address == nil || *rows[0].Outputs[0].Address != address ||
		rows[1].Outputs[0].Address == nil || *rows[1].Outputs[0].Address == address {
		t.Fatalf("projected addresses = %#v / %#v", rows[0].Outputs[0], rows[1].Outputs[0])
	}
}

func TestSaveTransactionIOBatchPersistsProjectedOwnership(t *testing.T) {
	ctx := context.Background()
	database, err := ledgerdb.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	targetHash := transactionPersistenceHash(0x33)
	resolved := transactionPersistenceOutput(
		"previous", 0, 9, ParseTransactionOutputScript(transactionP2PKH(targetHash[:])),
	)
	address, err := resolved.Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AddKeys(ctx, "account", []ledgerdb.AddressKey{{
		Address: address, PublicKey: []byte{1}, ChainCode: []byte{2},
	}}); err != nil {
		t.Fatal(err)
	}
	transaction := transactionPersistenceTransaction("stored", 0,
		ParseTransactionOutputScript(transactionP2PKH(targetHash[:])),
	)
	transaction.Inputs = []TransactionInput{{ResolvedOutput: &resolved}}
	ledger := transactionPersistenceLedger(database)
	if err := ledger.SaveTransactionIOBatch(
		ctx, []*Transaction{transaction}, address, targetHash, "stored:0:",
	); err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetTransaction(ctx, "stored")
	if err != nil || stored == nil || !bytes.Equal(stored.Raw, transaction.Raw) {
		t.Fatalf("stored transaction = %#v, %v", stored, err)
	}
	outputs, err := database.GetOutputsByID(ctx, []string{"stored:0"})
	if err != nil || len(outputs) != 1 || outputs["stored:0"].Address == nil ||
		*outputs["stored:0"].Address != address {
		t.Fatalf("stored outputs = %#v, %v", outputs, err)
	}
}

func TestProjectTransactionIOBatchPropagatesOnlyObservableScriptAndMetadataErrors(t *testing.T) {
	targetHash := transactionPersistenceHash(0x44)
	otherHash := transactionPersistenceHash(0x55)
	ledger := transactionPersistenceLedger(nil)
	target := transactionPersistenceOutput(
		"target", 0, 1, ParseTransactionOutputScript(transactionP2PKH(targetHash[:])),
	)
	address, err := target.Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}

	invalidExternalClaim := transactionPersistenceTransaction("external", 0,
		ParseTransactionOutputScript(transactionClaimScript(
			transactionOpClaimName, []byte{0xff}, nil, []byte{0}, nil,
			transactionP2PKH(otherHash[:]),
		)),
	)
	if _, err := ledger.ProjectTransactionIOBatch(
		[]*Transaction{invalidExternalClaim}, address, targetHash,
	); err != nil {
		t.Fatalf("unselected invalid claim name = %v", err)
	}

	invalidSelectedClaim := transactionPersistenceTransaction("selected", 0,
		ParseTransactionOutputScript(transactionClaimScript(
			transactionOpClaimName, []byte{0xff}, nil, []byte{0}, nil,
			transactionP2PKH(targetHash[:]),
		)),
	)
	if _, err := ledger.ProjectTransactionIOBatch(
		[]*Transaction{invalidSelectedClaim}, address, targetHash,
	); !errors.Is(err, ErrInvalidTransactionClaimName) {
		t.Fatalf("selected invalid claim name = %v", err)
	}

	invalidScript := transactionPersistenceTransaction("invalid-script", 0,
		ParseTransactionOutputScript(transactionP2PKH(otherHash[:])),
		ParseTransactionOutputScript([]byte{0xff}),
	)
	if _, err := ledger.ProjectTransactionIOBatch(
		[]*Transaction{invalidScript}, address, targetHash,
	); !errors.Is(err, ErrInvalidTransactionScript) {
		t.Fatalf("invalid output script = %v", err)
	}
}

func TestProjectTransactionIOBatchChecksSelectedSQLiteAmountAndJulianDay(t *testing.T) {
	targetHash := transactionPersistenceHash(0x66)
	otherHash := transactionPersistenceHash(0x77)
	ledger := transactionPersistenceLedger(nil)
	target := transactionPersistenceOutput(
		"target", 0, 1, ParseTransactionOutputScript(transactionP2PKH(targetHash[:])),
	)
	address, err := target.Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}

	unselected := transactionPersistenceTransaction("unselected-overflow", 1,
		ParseTransactionOutputScript(transactionP2PKH(otherHash[:])),
	)
	unselected.Outputs[0].Amount = math.MaxUint64
	rows, err := ledger.ProjectTransactionIOBatch([]*Transaction{unselected}, address, targetHash)
	if err != nil || len(rows) != 1 || len(rows[0].Outputs) != 0 || rows[0].Transaction.Day == nil ||
		math.Mod(*rows[0].Transaction.Day, 1) != 0.5 || unselected.JulianDay == nil {
		t.Fatalf("unselected projection = %#v, %v", rows, err)
	}

	selected := transactionPersistenceTransaction("selected-overflow", 0,
		ParseTransactionOutputScript(transactionP2PKH(targetHash[:])),
	)
	selected.Outputs[0].Amount = math.MaxUint64
	if _, err := ledger.ProjectTransactionIOBatch(
		[]*Transaction{selected}, address, targetHash,
	); !errors.Is(err, ErrTransactionAmountOverflow) {
		t.Fatalf("selected overflow = %v", err)
	}
}

func transactionPersistenceLedger(database *ledgerdb.DB) *Ledger {
	if database == nil {
		database = ledgerdb.New(":memory:")
	}
	return &Ledger{
		Network: keys.MainNet, Database: database,
		Headers: NewHeadersForNetwork(":memory:", keys.MainNet),
	}
}

func transactionPersistenceTransaction(
	id string, height int64, scripts ...TransactionOutputScript,
) *Transaction {
	transaction := &Transaction{
		ID: id, Raw: []byte(id), Height: height, Position: -1,
		Outputs: make([]TransactionOutput, len(scripts)),
	}
	for index, script := range scripts {
		transaction.Outputs[index] = transactionPersistenceOutput(
			id, uint32(index), uint64(index+1), script,
		)
	}
	return transaction
}

func transactionPersistenceOutput(
	transactionID string, position uint32, amount uint64, script TransactionOutputScript,
) TransactionOutput {
	return TransactionOutput{
		TransactionID: transactionID, Position: position, Amount: amount, Script: script,
	}
}

func transactionPersistenceHash(value byte) [20]byte {
	var hash [20]byte
	for index := range hash {
		hash[index] = value
	}
	return hash
}

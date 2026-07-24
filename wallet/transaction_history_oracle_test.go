package wallet

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"lbry/daemon/wallet/ledgerdb"
)

const (
	transactionHistoryOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	transactionHistoryOraclePinnedVersion = "0.113.0"
)

var transactionHistoryOraclePinnedSources = map[string]string{
	"lbry/__init__.py":        "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/wallet/database.py": "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
}

var transactionHistoryOraclePinnedMethods = map[string]string{
	"Database.get_transaction_count": "dc84e16ec72283863b144c22e405139c31bd0f1c1b1dc5291e9153e91304fdb3",
	"Database.get_transactions":      "5c5bff04bc5b5d0a3e3f402d421226ede46c59f9ba481137b9d6de2120efdf2f",
	"Database.get_txos":              "54eb3def5f8d9f5bbbb83cef52cbe5ba55735fb3294b8c382fcd33a68a785c01",
	"Database.select_transactions":   "e90345f73d9b5cda3444c90c3c316b86fce4433fce86344be43c93f2edad224e",
	"Database.select_txos":           "3fbf0b31b8d3917e1b44834c8c1fbca3a0f1ab0155a87bf2e5a6e625271a2bc2",
	"constraints_to_sql":             "12bd52e0ff61bb1040401402c6de5cd09d31cde2484212995fd07973eed84925",
	"query":                          "b7496b9058c2c08487def378800baf08f715615a887fc596fbce694282384b9a",
}

type transactionHistoryOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
		MethodSHA256 map[string]string `json:"method_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion            string `json:"python_version"`
		ExtractedMethodsExecuted bool   `json:"extracted_methods_executed"`
		StdlibSQLiteUsed         bool   `json:"stdlib_sqlite_used"`
		ExternalNetworkUsed      bool   `json:"external_network_used"`
	} `json:"metadata"`
	Default                         []transactionHistoryOracleTransaction `json:"default"`
	Page                            []transactionHistoryOracleTransaction `json:"page_offset_2_limit_3"`
	TXIDBypassesAccountScope        []transactionHistoryOracleTransaction `json:"txid_bypasses_account_scope"`
	TXIDsBypassWithoutAccounts      []transactionHistoryOracleTransaction `json:"txids_bypass_without_accounts"`
	EmptyTXIDBypassesScope          []string                              `json:"empty_txid_bypasses_scope"`
	EmptyTXIDsBypassAndOmitFilter   []string                              `json:"empty_txids_bypass_and_omit_filter"`
	CountIgnoringPaginationAndOrder int64                                 `json:"count_with_ignored_pagination_order"`
	MissingScopeError               *transactionHistoryOracleError        `json:"missing_scope_error"`
}

type transactionHistoryOracleError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type transactionHistoryOracleTransaction struct {
	Name       string                           `json:"name"`
	TXID       string                           `json:"txid"`
	Height     int64                            `json:"height"`
	Position   int64                            `json:"position"`
	IsVerified bool                             `json:"is_verified"`
	Inputs     []transactionHistoryOracleInput  `json:"inputs"`
	Outputs    []transactionHistoryOracleOutput `json:"outputs"`
}

type transactionHistoryOracleInput struct {
	Previous           string  `json:"previous"`
	PreviousPosition   uint32  `json:"previous_position"`
	Resolved           bool    `json:"resolved"`
	ResolvedAmount     *uint64 `json:"resolved_amount"`
	ResolvedIsMyOutput *bool   `json:"resolved_is_my_output"`
}

type transactionHistoryOracleOutput struct {
	Position           uint32  `json:"position"`
	Amount             uint64  `json:"amount"`
	Script             string  `json:"script"`
	IsSpent            *bool   `json:"is_spent"`
	IsMyInput          *bool   `json:"is_my_input"`
	IsMyOutput         *bool   `json:"is_my_output"`
	IsInternalTransfer *bool   `json:"is_internal_transfer"`
	PurchaseOutput     *uint32 `json:"purchase_output"`
	PurchasedClaimID   *string `json:"purchased_claim_id"`
}

func TestTransactionHistoryMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runTransactionHistoryOracle(t)
	if oracle.Reference.Commit != transactionHistoryOraclePinnedCommit ||
		oracle.Reference.Version != transactionHistoryOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, transactionHistoryOraclePinnedSources) ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, transactionHistoryOraclePinnedMethods) {
		t.Fatalf("transaction history oracle reference = %+v", oracle.Reference)
	}
	if !oracle.Metadata.ExtractedMethodsExecuted || !oracle.Metadata.StdlibSQLiteUsed ||
		oracle.Metadata.ExternalNetworkUsed {
		t.Fatalf("transaction history oracle metadata = %+v", oracle.Metadata)
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" &&
		oracle.Metadata.PythonVersion != want {
		t.Fatalf("transaction history Python version = %q, want %q",
			oracle.Metadata.PythonVersion, want)
	}

	assertTransactionHistoryOracleContract(t, oracle)
	assertGoTransactionHistorySelectionOracle(t, oracle)
	assertGoTransactionHistoryHydrationOracle(t, oracle)
}

func assertTransactionHistoryOracleContract(
	t *testing.T, oracle transactionHistoryOracleResponse,
) {
	t.Helper()
	if got, want := transactionHistoryOracleNames(oracle.Default), []string{
		"outgoing", "incoming", "parent-purchase", "internal",
		"parent-spent", "parent-internal", "missing-reference", "purchase",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Python default transaction order = %v, want %v", got, want)
	}
	if got, want := transactionHistoryOracleNames(oracle.Page), []string{
		"parent-purchase", "internal", "parent-spent",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Python transaction page = %v, want %v", got, want)
	}
	if got := transactionHistoryOracleNames(oracle.TXIDBypassesAccountScope); !reflect.DeepEqual(got, []string{"foreign"}) {
		t.Fatalf("Python txid account bypass = %v", got)
	}
	if got := transactionHistoryOracleNames(oracle.TXIDsBypassWithoutAccounts); !reflect.DeepEqual(got, []string{"foreign", "incoming"}) {
		t.Fatalf("Python txids account bypass = %v", got)
	}
	if len(oracle.EmptyTXIDBypassesScope) != 0 {
		t.Fatalf("Python empty txid result = %v, want empty", oracle.EmptyTXIDBypassesScope)
	}
	if want := []string{
		"outgoing", "foreign", "incoming", "parent-purchase", "internal",
		"parent-spent", "parent-internal", "missing-reference", "purchase",
	}; !reflect.DeepEqual(oracle.EmptyTXIDsBypassAndOmitFilter, want) {
		t.Fatalf("Python empty txids result = %v, want %v",
			oracle.EmptyTXIDsBypassAndOmitFilter, want)
	}
	if oracle.CountIgnoringPaginationAndOrder != 8 {
		t.Fatalf("Python transaction count = %d, want 8",
			oracle.CountIgnoringPaginationAndOrder)
	}
	if oracle.MissingScopeError == nil ||
		oracle.MissingScopeError.Type != "AssertionError" ||
		oracle.MissingScopeError.Message != ledgerdb.ErrTransactionAccountsRequired.Error() {
		t.Fatalf("Python missing-scope error = %+v", oracle.MissingScopeError)
	}

	outgoing := transactionHistoryOracleNamed(t, oracle.Default, "outgoing")
	if len(outgoing.Inputs) != 1 || !outgoing.Inputs[0].Resolved ||
		outgoing.Inputs[0].Previous != "parent-spent" ||
		outgoing.Inputs[0].ResolvedAmount == nil || *outgoing.Inputs[0].ResolvedAmount != 111 ||
		outgoing.Inputs[0].ResolvedIsMyOutput == nil || !*outgoing.Inputs[0].ResolvedIsMyOutput ||
		len(outgoing.Outputs) != 1 || outgoing.Outputs[0].Amount != 101 ||
		!transactionHistoryOracleBool(outgoing.Outputs[0].IsMyInput) ||
		transactionHistoryOracleBool(outgoing.Outputs[0].IsMyOutput) {
		t.Fatalf("Python outgoing hydration = %+v", outgoing)
	}

	internal := transactionHistoryOracleNamed(t, oracle.Default, "internal")
	if len(internal.Outputs) != 1 ||
		!transactionHistoryOracleBool(internal.Outputs[0].IsMyInput) ||
		!transactionHistoryOracleBool(internal.Outputs[0].IsMyOutput) ||
		!transactionHistoryOracleBool(internal.Outputs[0].IsInternalTransfer) {
		t.Fatalf("Python internal-transfer annotations = %+v", internal)
	}

	parent := transactionHistoryOracleNamed(t, oracle.Default, "parent-spent")
	if len(parent.Outputs) != 1 || parent.Outputs[0].Amount != 111 ||
		!transactionHistoryOracleBool(parent.Outputs[0].IsSpent) ||
		!transactionHistoryOracleBool(parent.Outputs[0].IsMyOutput) ||
		parent.Outputs[0].Script == "51" {
		t.Fatalf("Python raw hydration and spent annotations = %+v", parent)
	}

	missing := transactionHistoryOracleNamed(t, oracle.Default, "missing-reference")
	if len(missing.Inputs) != 1 || missing.Inputs[0].Resolved ||
		missing.Inputs[0].Previous != "1111111111111111111111111111111111111111111111111111111111111111" ||
		missing.Inputs[0].PreviousPosition != 4 {
		t.Fatalf("Python missing input reference = %+v", missing.Inputs)
	}

	purchase := transactionHistoryOracleNamed(t, oracle.Default, "purchase")
	if len(purchase.Outputs) != 2 || purchase.Outputs[0].PurchaseOutput == nil ||
		*purchase.Outputs[0].PurchaseOutput != 1 ||
		purchase.Outputs[0].PurchasedClaimID == nil ||
		*purchase.Outputs[0].PurchasedClaimID != "001122334455" ||
		purchase.Outputs[1].IsMyOutput == nil || *purchase.Outputs[1].IsMyOutput ||
		purchase.Outputs[1].IsSpent != nil || purchase.Outputs[1].IsMyInput != nil {
		t.Fatalf("Python purchase linkage = %+v", purchase.Outputs)
	}
}

func assertGoTransactionHistorySelectionOracle(
	t *testing.T, oracle transactionHistoryOracleResponse,
) {
	t.Helper()
	ctx := context.Background()
	database, fixture := transactionHistoryOracleFixture(t)
	query := ledgerdb.TransactionQuery{AccountIDs: []string{"account-a"}}
	rows, err := database.ListTransactions(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionHistoryOracleRowNames(rows, fixture),
		transactionHistoryOracleNames(oracle.Default); !reflect.DeepEqual(got, want) {
		t.Fatalf("Go default transaction selection = %v, Python %v", got, want)
	}
	transactionHistoryOracleAssertRows(t, rows, oracle.Default)

	limit, offset := 3, 2
	query.Limit, query.Offset = &limit, &offset
	rows, err = database.ListTransactions(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionHistoryOracleRowNames(rows, fixture),
		transactionHistoryOracleNames(oracle.Page); !reflect.DeepEqual(got, want) {
		t.Fatalf("Go transaction page = %v, Python %v", got, want)
	}

	foreign := fixture["foreign"]
	foreignID := foreign.ID
	rows, err = database.ListTransactions(ctx, ledgerdb.TransactionQuery{
		AccountIDs: []string{"account-a"}, TXID: &foreignID,
	})
	if err != nil || !reflect.DeepEqual(
		transactionHistoryOracleRowNames(rows, fixture), []string{"foreign"},
	) {
		t.Fatalf("Go txid account bypass = %v, %v",
			transactionHistoryOracleRowNames(rows, fixture), err)
	}

	incoming := fixture["incoming"]
	rows, err = database.ListTransactions(ctx, ledgerdb.TransactionQuery{
		TXIDs: []string{foreign.ID, incoming.ID}, Order: ledgerdb.TransactionOrderNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	gotNames := transactionHistoryOracleRowNames(rows, fixture)
	if len(gotNames) != 2 || !transactionHistoryOracleNameSetEqual(
		gotNames, []string{"foreign", "incoming"},
	) {
		t.Fatalf("Go txids account bypass = %v", gotNames)
	}

	emptyTXID := ""
	rows, err = database.ListTransactions(ctx, ledgerdb.TransactionQuery{TXID: &emptyTXID})
	if err != nil || len(rows) != 0 {
		t.Fatalf("Go empty txid result = %d, %v, want empty", len(rows), err)
	}
	rows, err = database.ListTransactions(ctx, ledgerdb.TransactionQuery{TXIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := transactionHistoryOracleRowNames(rows, fixture); !reflect.DeepEqual(
		got, oracle.EmptyTXIDsBypassAndOmitFilter,
	) {
		t.Fatalf("Go empty txids result = %v, Python %v",
			got, oracle.EmptyTXIDsBypassAndOmitFilter)
	}

	zero, farOffset := 0, 999
	count, err := database.CountTransactions(ctx, ledgerdb.TransactionQuery{
		AccountIDs: []string{"account-a"}, Limit: &zero, Offset: &farOffset,
		Order: ledgerdb.TransactionOrder(255),
	})
	if err != nil || count != oracle.CountIgnoringPaginationAndOrder {
		t.Fatalf("Go transaction count = %d, %v; Python %d",
			count, err, oracle.CountIgnoringPaginationAndOrder)
	}
	if _, err := database.ListTransactions(ctx, ledgerdb.TransactionQuery{}); !errors.Is(err, ledgerdb.ErrTransactionAccountsRequired) {
		t.Fatalf("Go missing account scope = %v", err)
	}
}

func assertGoTransactionHistoryHydrationOracle(
	t *testing.T, oracle transactionHistoryOracleResponse,
) {
	t.Helper()
	ctx := context.Background()
	database, fixture := transactionHistoryOracleFixture(t)
	ledger := &Ledger{Database: database}
	options := TransactionListOptions{
		Query:                ledgerdb.TransactionQuery{AccountIDs: []string{"account-a"}},
		AnnotationAccountIDs: []string{"account-a"},
		IncludeIsSpent:       true,
		IncludeIsMyInput:     true,
		IncludeIsMyOutput:    true,
	}
	transactions, err := ledger.GetTransactions(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := transactionHistoryOracleGoTransactions(t, transactions, fixture); !reflect.DeepEqual(got, oracle.Default) {
		t.Fatalf("Go hydrated transaction history = %+v, Python %+v", got, oracle.Default)
	}

	limit, offset := 3, 2
	options.Query.Limit, options.Query.Offset = &limit, &offset
	transactions, err = ledger.GetTransactions(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := transactionHistoryOracleGoTransactions(t, transactions, fixture); !reflect.DeepEqual(got, oracle.Page) {
		t.Fatalf("Go hydrated transaction page = %+v, Python %+v", got, oracle.Page)
	}

	foreignID := fixture["foreign"].ID
	options.Query = ledgerdb.TransactionQuery{
		AccountIDs: []string{"account-a"}, TXID: &foreignID,
	}
	transactions, err = ledger.GetTransactions(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := transactionHistoryOracleGoTransactions(t, transactions, fixture); !reflect.DeepEqual(got, oracle.TXIDBypassesAccountScope) {
		t.Fatalf("Go hydrated txid bypass = %+v, Python %+v",
			got, oracle.TXIDBypassesAccountScope)
	}

	options = TransactionListOptions{
		Query: ledgerdb.TransactionQuery{TXIDs: []string{
			fixture["foreign"].ID, fixture["incoming"].ID,
		}},
		AnnotationAccountIDs: []string{"account-a"},
		IncludeIsMyOutput:    true,
	}
	transactions, err = ledger.GetTransactions(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := transactionHistoryOracleGoTransactions(t, transactions, fixture); !reflect.DeepEqual(got, oracle.TXIDsBypassWithoutAccounts) {
		t.Fatalf("Go hydrated txids bypass = %+v, Python %+v",
			got, oracle.TXIDsBypassWithoutAccounts)
	}

	zero, farOffset := 0, 999
	count, err := ledger.CountTransactions(ctx, ledgerdb.TransactionQuery{
		AccountIDs: []string{"account-a"}, Limit: &zero, Offset: &farOffset,
		Order: ledgerdb.TransactionOrder(255),
	})
	if err != nil || count != oracle.CountIgnoringPaginationAndOrder {
		t.Fatalf("Go public transaction count = %d, %v; Python %d",
			count, err, oracle.CountIgnoringPaginationAndOrder)
	}
}

func transactionHistoryOracleFixture(
	t *testing.T,
) (*ledgerdb.DB, map[string]*Transaction) {
	t.Helper()
	ctx := context.Background()
	database, err := ledgerdb.Open(ctx, filepath.Join(t.TempDir(), ledgerdb.Filename))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Errorf("close transaction history oracle database: %v", err)
		}
	})
	addKeys := func(accountID string, addresses ...string) {
		t.Helper()
		keys := make([]ledgerdb.AddressKey, len(addresses))
		for index, address := range addresses {
			keys[index] = ledgerdb.AddressKey{
				Address: address, PublicKey: []byte{1}, ChainCode: []byte{2}, N: int64(index),
			}
		}
		if err := database.AddKeys(ctx, accountID, keys); err != nil {
			t.Fatal(err)
		}
	}
	addKeys("account-a", "a1", "a2")
	addKeys("account-b", "b1")

	transactions := make(map[string]*Transaction)
	persist := func(
		name string, transaction *Transaction, height, position int64,
		inputAddress string, outputs []transactionHistoryFixtureOutput,
		purchasedClaimID *string,
	) {
		t.Helper()
		transaction.Height = height
		transaction.Position = position
		transaction.IsVerified = height > 0
		row := ledgerdb.TransactionIORow{Transaction: ledgerdb.TransactionRow{
			TXID: transaction.ID, Raw: append([]byte(nil), transaction.Raw...),
			Height: height, Position: position, IsVerified: height > 0,
			PurchasedClaimID: purchasedClaimID,
		}}
		if inputAddress != "" {
			row.Inputs = []ledgerdb.TransactionInputRow{{
				TXOID: transaction.Inputs[0].PreviousOutputID(), Position: 0,
			}}
		}
		for _, output := range outputs {
			address := output.address
			row.Outputs = append(row.Outputs, ledgerdb.TransactionOutputRow{
				TXOID: transaction.Outputs[output.position].ID(), Address: &address,
				Position: int64(output.position),
				Amount:   int64(transaction.Outputs[output.position].Amount) + 9000,
				Script:   []byte{0x51}, TXOType: output.outputType,
			})
		}
		if err := database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{row}, inputAddress, ""); err != nil {
			t.Fatal(err)
		}
		transactions[name] = transaction
	}

	parentSpent := transactionHistoryOracleCoinbaseTransaction(t, 1, 111)
	persist("parent-spent", parentSpent, 8, 1, "", []transactionHistoryFixtureOutput{{0, "a1", 0}}, nil)
	parentInternal := transactionHistoryOracleCoinbaseTransaction(t, 2, 222)
	persist("parent-internal", parentInternal, 7, 2, "", []transactionHistoryFixtureOutput{{0, "a1", 0}}, nil)
	parentPurchase := transactionHistoryOracleCoinbaseTransaction(t, 3, 333)
	persist("parent-purchase", parentPurchase, 11, 3, "", []transactionHistoryFixtureOutput{{0, "a1", 0}}, nil)

	outgoing := transactionHistoryOracleSpendTransaction(t, 9, &parentSpent.Outputs[0],
		NewPayPubKeyHashOutput(101, bytes.Repeat([]byte{4}, 20)))
	persist("outgoing", outgoing, 0, 9, "a1", []transactionHistoryFixtureOutput{{0, "z1", 0}}, nil)
	internal := transactionHistoryOracleSpendTransaction(t, 7, &parentInternal.Outputs[0],
		NewPayPubKeyHashOutput(202, bytes.Repeat([]byte{5}, 20)))
	persist("internal", internal, 9, 7, "a1", []transactionHistoryFixtureOutput{{0, "a2", 0}}, nil)

	claimID := "001122334455"
	purchaseData, err := NewPurchaseDataOutput(claimID)
	if err != nil {
		t.Fatal(err)
	}
	purchase := transactionHistoryOracleSpendTransaction(
		t, 8, &parentPurchase.Outputs[0],
		NewPayPubKeyHashOutput(303, bytes.Repeat([]byte{6}, 20)), purchaseData,
	)
	persist("purchase", purchase, -1, 8, "a1",
		[]transactionHistoryFixtureOutput{{0, "z2", TransactionOutputTypePurchase}}, &claimID)

	incoming := transactionHistoryOracleCoinbaseTransaction(t, 4, 404)
	incoming.Outputs[0] = NewPayPubKeyHashOutput(404, bytes.Repeat([]byte{7}, 20))
	if err := incoming.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	persist("incoming", incoming, 12, 4, "", []transactionHistoryFixtureOutput{{0, "a2", 0}}, nil)

	missing := transactionHistoryOracleMissingTransaction(t)
	persist("missing-reference", missing, 5, 5, "", []transactionHistoryFixtureOutput{{0, "a1", 0}}, nil)

	foreign := transactionHistoryOracleCoinbaseTransaction(t, 1, 606)
	foreign.Outputs[0] = NewPayPubKeyHashOutput(606, bytes.Repeat([]byte{9}, 20))
	if err := foreign.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	persist("foreign", foreign, 20, 1, "", []transactionHistoryFixtureOutput{{0, "b1", 0}}, nil)
	return database, transactions
}

type transactionHistoryFixtureOutput struct {
	position   uint32
	address    string
	outputType int64
}

func transactionHistoryOracleCoinbaseTransaction(
	t *testing.T, position int64, amount uint64,
) *Transaction {
	t.Helper()
	lockTime := uint32(position + 100)
	coinbase := make([]byte, 4)
	binary.LittleEndian.PutUint32(coinbase, lockTime)
	transaction := NewTransaction()
	transaction.LockTime = lockTime
	transaction.AddInputs([]TransactionInput{{
		PreviousIndex: math.MaxUint32, Sequence: math.MaxUint32, Coinbase: coinbase,
	}})
	transaction.AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(amount, bytes.Repeat([]byte{byte(position)}, 20)),
	})
	return transaction
}

func transactionHistoryOracleSpendTransaction(
	t *testing.T, position int64, previous *TransactionOutput, outputs ...TransactionOutput,
) *Transaction {
	t.Helper()
	transaction := NewTransaction()
	transaction.LockTime = uint32(position + 100)
	transaction.AddInputs([]TransactionInput{{
		PreviousHash: previous.TransactionHash, PreviousTxID: previous.TransactionID,
		PreviousIndex: previous.Position, Sequence: math.MaxUint32,
		Script: TransactionInputScript{Source: []byte{0x51}},
	}})
	transaction.AddOutputs(outputs)
	return transaction
}

func transactionHistoryOracleMissingTransaction(t *testing.T) *Transaction {
	t.Helper()
	var previousHash [32]byte
	copy(previousHash[:], bytes.Repeat([]byte{0x11}, 32))
	transaction := NewTransaction()
	transaction.LockTime = 105
	transaction.AddInputs([]TransactionInput{{
		PreviousHash:  previousHash,
		PreviousTxID:  "1111111111111111111111111111111111111111111111111111111111111111",
		PreviousIndex: 4, Sequence: math.MaxUint32,
		Script: TransactionInputScript{Source: []byte{0x51}},
	}})
	transaction.AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(505, bytes.Repeat([]byte{8}, 20)),
	})
	return transaction
}

func transactionHistoryOracleAssertRows(
	t *testing.T, rows []ledgerdb.TransactionRow,
	want []transactionHistoryOracleTransaction,
) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("Go transaction rows = %d, Python %d", len(rows), len(want))
	}
	for index, row := range rows {
		if row.TXID != want[index].TXID || row.Height != want[index].Height ||
			row.Position != want[index].Position || row.IsVerified != want[index].IsVerified {
			t.Fatalf("Go transaction row %d = %+v, Python %+v", index, row, want[index])
		}
	}
}

func transactionHistoryOracleGoTransactions(
	t *testing.T, transactions []*Transaction, fixture map[string]*Transaction,
) []transactionHistoryOracleTransaction {
	t.Helper()
	namesByID := make(map[string]string, len(fixture))
	for name, transaction := range fixture {
		namesByID[transaction.ID] = name
	}
	result := make([]transactionHistoryOracleTransaction, len(transactions))
	for transactionIndex, transaction := range transactions {
		if transaction == nil {
			t.Fatalf("Go hydrated transaction %d is nil", transactionIndex)
		}
		name, ok := namesByID[transaction.ID]
		if !ok {
			t.Fatalf("Go hydrated transaction ID %s is not in fixture", transaction.ID)
		}
		shaped := transactionHistoryOracleTransaction{
			Name: name, TXID: transaction.ID, Height: transaction.Height,
			Position: transaction.Position, IsVerified: transaction.IsVerified,
			Inputs:  make([]transactionHistoryOracleInput, len(transaction.Inputs)),
			Outputs: make([]transactionHistoryOracleOutput, len(transaction.Outputs)),
		}
		for inputIndex := range transaction.Inputs {
			input := &transaction.Inputs[inputIndex]
			previous := input.PreviousTxID
			if previousName, found := namesByID[previous]; found {
				previous = previousName
			}
			shapedInput := transactionHistoryOracleInput{
				Previous: previous, PreviousPosition: input.PreviousIndex,
				Resolved: input.ResolvedOutput != nil,
			}
			if input.ResolvedOutput != nil {
				amount := currentTransactionOutput(input.ResolvedOutput).Amount
				shapedInput.ResolvedAmount = &amount
				shapedInput.ResolvedIsMyOutput = transactionHistoryOracleCloneBool(
					currentTransactionOutput(input.ResolvedOutput).IsMyOutput,
				)
			}
			shaped.Inputs[inputIndex] = shapedInput
		}
		for outputIndex := range transaction.Outputs {
			output := &transaction.Outputs[outputIndex]
			shapedOutput := transactionHistoryOracleOutput{
				Position: output.Position, Amount: output.Amount,
				Script:             hex.EncodeToString(output.Script.Source),
				IsSpent:            transactionHistoryOracleCloneBool(output.IsSpent),
				IsMyInput:          transactionHistoryOracleCloneBool(output.IsMyInput),
				IsMyOutput:         transactionHistoryOracleCloneBool(output.IsMyOutput),
				IsInternalTransfer: transactionHistoryOracleCloneBool(output.IsInternalTransfer),
			}
			if output.Purchase != nil {
				purchasePosition := output.Purchase.Position
				shapedOutput.PurchaseOutput = &purchasePosition
				claimID, decoded := decodeTransactionPurchase(output.Purchase.Script)
				if !decoded {
					t.Fatalf("Go linked purchase output %s did not decode", output.Purchase.ID())
				}
				shapedOutput.PurchasedClaimID = &claimID
			}
			shaped.Outputs[outputIndex] = shapedOutput
		}
		result[transactionIndex] = shaped
	}
	return result
}

func transactionHistoryOracleCloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func transactionHistoryOracleRowNames(
	rows []ledgerdb.TransactionRow, fixture map[string]*Transaction,
) []string {
	byID := make(map[string]string, len(fixture))
	for name, transaction := range fixture {
		byID[transaction.ID] = name
	}
	names := make([]string, len(rows))
	for index, row := range rows {
		names[index] = byID[row.TXID]
	}
	return names
}

func transactionHistoryOracleNames(
	transactions []transactionHistoryOracleTransaction,
) []string {
	names := make([]string, len(transactions))
	for index, transaction := range transactions {
		names[index] = transaction.Name
	}
	return names
}

func transactionHistoryOracleNamed(
	t *testing.T, transactions []transactionHistoryOracleTransaction, name string,
) transactionHistoryOracleTransaction {
	t.Helper()
	for _, transaction := range transactions {
		if transaction.Name == name {
			return transaction
		}
	}
	t.Fatalf("transaction history oracle has no %q case", name)
	return transactionHistoryOracleTransaction{}
}

func transactionHistoryOracleBool(value *bool) bool {
	return value != nil && *value
}

func transactionHistoryOracleNameSetEqual(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	counts := make(map[string]int, len(first))
	for _, value := range first {
		counts[value]++
	}
	for _, value := range second {
		counts[value]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func runTransactionHistoryOracle(t *testing.T) transactionHistoryOracleResponse {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate transaction history oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	for relative := range transactionHistoryOraclePinnedSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local transaction history source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(daemonRoot, "compat", "transaction_history_oracle.py")
	if _, err := os.Stat(script); errors.Is(err, os.ErrNotExist) {
		t.Skipf("transaction history oracle script is unavailable: %s", script)
	} else if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("transaction history oracle failed: %v\n%s", err, output)
	}
	var oracle transactionHistoryOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode transaction history oracle: %v\n%s", err, output)
	}
	return oracle
}

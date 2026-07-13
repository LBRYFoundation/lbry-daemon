package ledgerdb

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestListOutputsDeduplicatesSharedOwnershipAndFiltersSpentState(t *testing.T) {
	t.Parallel()
	database := openOutputQueryTestDB(t)
	addOutputQueryTestAccount(t, database, "first", "shared")
	addOutputQueryTestAccount(t, database, "second", "shared")
	addOutputQueryTestAccount(t, database, "other", "foreign")

	addOutputQueryTestTransaction(t, database, "available", 4, 0, true)
	addOutputQueryTestOutput(t, database, "available", 0, "shared", 10, 0, false)
	addOutputQueryTestTransaction(t, database, "reserved", 3, 0, true)
	addOutputQueryTestOutput(t, database, "reserved", 0, "shared", 20, 0, true)
	addOutputQueryTestTransaction(t, database, "spent", 2, 0, true)
	addOutputQueryTestOutput(t, database, "spent", 0, "shared", 30, 0, false)
	spendOutputQueryTestOutput(t, database, "spent:0", "shared")
	addOutputQueryTestTransaction(t, database, "spent-reserved", 1, 0, true)
	addOutputQueryTestOutput(t, database, "spent-reserved", 0, "shared", 40, 4, true)
	spendOutputQueryTestOutput(t, database, "spent-reserved:0", "shared")
	addOutputQueryTestTransaction(t, database, "foreign", 5, 0, true)
	addOutputQueryTestOutput(t, database, "foreign", 0, "foreign", 50, 0, false)

	sharedQuery := OutputQuery{AccountIDs: []string{"first", "second"}}
	rows, err := database.ListOutputs(context.Background(), sharedQuery)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := outputQueryTestIDs(rows), []string{
		"available:0", "reserved:0", "spent:0", "spent-reserved:0",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shared-account outputs = %v, want %v", got, want)
	}
	count, err := database.CountOutputs(context.Background(), sharedQuery)
	if err != nil || count != 4 {
		t.Fatalf("shared-account count = %d, %v, want 4", count, err)
	}

	unspent := false
	rows, err = database.ListOutputs(context.Background(), OutputQuery{
		AccountIDs: sharedQuery.AccountIDs, IsSpent: &unspent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := outputQueryTestIDs(rows), []string{"available:0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unspent outputs = %v, want %v", got, want)
	}

	spent := true
	rows, err = database.ListOutputs(context.Background(), OutputQuery{
		AccountIDs: sharedQuery.AccountIDs, IsSpent: &spent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := outputQueryTestIDs(rows), []string{"spent:0", "spent-reserved:0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("spent outputs = %v, want %v", got, want)
	}
	if len(rows) != 2 || rows[0].IsReserved || !rows[1].IsReserved {
		t.Fatalf("spent reservation flags = %#v", rows)
	}

	rows, err = database.ListOutputs(context.Background(), OutputQuery{
		AccountIDs: []string{"other"},
	})
	if err != nil || !reflect.DeepEqual(outputQueryTestIDs(rows), []string{"foreign:0"}) {
		t.Fatalf("foreign account outputs = %v, %v", outputQueryTestIDs(rows), err)
	}
}

func TestListOutputsFiltersOrderingEmptyINSlicesAndPagination(t *testing.T) {
	t.Parallel()
	database := openOutputQueryTestDB(t)
	addOutputQueryTestAccount(t, database, "account", "owned")

	addOutputQueryTestTransaction(t, database, "zero", 0, 1, false)
	addOutputQueryTestOutput(t, database, "zero", 0, "owned", 10, 0, false)
	addOutputQueryTestTransaction(t, database, "pending", -1, 1, false)
	addOutputQueryTestOutput(t, database, "pending", 0, "owned", 20, 4, false)
	addOutputQueryTestTransaction(t, database, "high", 10, 9, true)
	addOutputQueryTestOutput(t, database, "high", 0, "owned", 30, 1, false)
	addOutputQueryTestTransaction(t, database, "same", 10, 2, true)
	addOutputQueryTestOutput(t, database, "same", 0, "owned", 40, 4, false)
	addOutputQueryTestOutput(t, database, "same", 1, "owned", 50, 6, false)
	addOutputQueryTestTransaction(t, database, "low", 5, 8, true)
	addOutputQueryTestOutput(t, database, "low", 0, "owned", 60, 0, false)
	addOutputQueryTestTransaction(t, database, "abandoned", -2, 7, false)
	addOutputQueryTestOutput(t, database, "abandoned", 0, "owned", 70, 3, false)

	wantAll := []string{
		"zero:0", "pending:0", "high:0", "same:0", "same:1", "low:0", "abandoned:0",
	}
	rows, err := database.ListOutputs(context.Background(), OutputQuery{
		AccountIDs: []string{"account"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := outputQueryTestIDs(rows); !reflect.DeepEqual(got, wantAll) {
		t.Fatalf("default output order = %v, want %v", got, wantAll)
	}
	if rows[2].Address == nil || *rows[2].Address != "owned" || rows[2].Height != 10 ||
		rows[2].TXPosition != 9 || rows[2].OutputPosition != 0 || !rows[2].IsVerified ||
		rows[2].Amount != 30 || rows[2].TXOType != 1 {
		t.Fatalf("projected high output = %#v", rows[2])
	}

	assertOutputQueryTestIDs(t, database, OutputQuery{TXID: "same"}, []string{"same:0", "same:1"})
	assertOutputQueryTestIDs(t, database, OutputQuery{
		TXIDs: []string{"same", "zero"},
	}, []string{"zero:0", "same:0", "same:1"})
	assertOutputQueryTestIDs(t, database, OutputQuery{TXOID: "same:1"}, []string{"same:1"})
	assertOutputQueryTestIDs(t, database, OutputQuery{
		TXOIDs: []string{"pending:0", "low:0"},
	}, []string{"pending:0", "low:0"})
	assertOutputQueryTestIDs(t, database, OutputQuery{
		Types: []int64{4, 6},
	}, []string{"pending:0", "same:0", "same:1"})
	mustExec(t, database.sql, "UPDATE txo SET claim_id = ? WHERE txoid = ?", "claim-a", "pending:0")
	mustExec(t, database.sql, "UPDATE txo SET claim_id = ? WHERE txoid = ?", "claim-b", "same:0")
	mustExec(t, database.sql, "UPDATE txo SET claim_id = ? WHERE txoid = ?", "claim-c", "same:1")
	assertOutputQueryTestIDs(t, database, OutputQuery{
		ClaimIDs: []string{"claim-a", "claim-c"},
	}, []string{"pending:0", "same:1"})
	assertOutputQueryTestIDs(t, database, OutputQuery{
		ClaimIDs: []string{"claim-b", "claim-c"}, Types: []int64{6},
	}, []string{"same:1"})

	maximumHeight, minimumHeight := int64(10), int64(5)
	assertOutputQueryTestIDs(t, database, OutputQuery{
		HeightLTE: &maximumHeight, HeightGT: &minimumHeight,
	}, []string{"high:0", "same:0", "same:1"})

	assertOutputQueryTestIDs(t, database, OutputQuery{
		AccountIDs: []string{}, TXIDs: []string{}, TXOIDs: []string{},
		ClaimIDs: []string{}, Types: []int64{},
	}, wantAll)

	limit, offset := 2, 2
	assertOutputQueryTestIDs(t, database, OutputQuery{
		Limit: &limit, Offset: &offset,
	}, []string{"high:0", "same:0"})
}

func TestOutputClaimIDFilterComposesWithSpentAndReservationState(t *testing.T) {
	t.Parallel()
	database := openOutputQueryTestDB(t)

	addOutputQueryTestTransaction(t, database, "available", 4, 0, true)
	addOutputQueryTestOutput(t, database, "available", 0, "owned", 10, 1, false)
	addOutputQueryTestTransaction(t, database, "reserved", 3, 0, true)
	addOutputQueryTestOutput(t, database, "reserved", 0, "owned", 20, 1, true)
	addOutputQueryTestTransaction(t, database, "spent", 2, 0, true)
	addOutputQueryTestOutput(t, database, "spent", 0, "owned", 30, 1, false)
	spendOutputQueryTestOutput(t, database, "spent:0", "owned")
	addOutputQueryTestTransaction(t, database, "different", 1, 0, true)
	addOutputQueryTestOutput(t, database, "different", 0, "owned", 40, 1, false)

	for _, txoid := range []string{"available:0", "reserved:0", "spent:0"} {
		mustExec(t, database.sql, "UPDATE txo SET claim_id = ? WHERE txoid = ?", "wanted", txoid)
	}
	mustExec(t, database.sql, "UPDATE txo SET claim_id = ? WHERE txoid = ?", "other", "different:0")

	unspent := false
	query := OutputQuery{ClaimIDs: []string{"wanted"}, Types: []int64{1}, IsSpent: &unspent}
	assertOutputQueryTestIDs(t, database, query, []string{"available:0"})
	count, err := database.CountOutputs(context.Background(), query)
	if err != nil || count != 1 {
		t.Fatalf("claim-filtered unspent count = %d, %v, want 1", count, err)
	}
	total, err := database.SumOutputs(context.Background(), query)
	if err != nil || total != 10 {
		t.Fatalf("claim-filtered unspent sum = %d, %v, want 10", total, err)
	}
}

func TestListOutputsIncludesWalletReceivedTips(t *testing.T) {
	t.Parallel()
	database := openOutputQueryTestDB(t)
	addOutputQueryTestAccount(t, database, "first", "first-owned")
	addOutputQueryTestAccount(t, database, "second", "second-owned")
	addOutputQueryTestAccount(t, database, "foreign", "foreign-owned")

	addOutputQueryTestTransaction(t, database, "claim-a", 10, 0, true)
	addOutputQueryTestOutput(t, database, "claim-a", 0, "foreign-owned", 1, 1, false)
	addOutputQueryTestTransaction(t, database, "claim-b", 9, 0, true)
	addOutputQueryTestOutput(t, database, "claim-b", 0, "foreign-owned", 1, 1, false)
	for txoid, claimID := range map[string]string{
		"claim-a:0": "claim-a", "claim-b:0": "claim-b",
	} {
		mustExec(t, database.sql, "UPDATE txo SET claim_id = ? WHERE txoid = ?", claimID, txoid)
	}

	type supportFixture struct {
		txid       string
		address    string
		amount     int64
		outputType int64
		claimID    string
		spent      bool
	}
	for index, support := range []supportFixture{
		{txid: "first-tip", address: "first-owned", amount: 11, outputType: 3, claimID: "claim-a"},
		{txid: "second-tip", address: "second-owned", amount: 22, outputType: 3, claimID: "claim-a"},
		{txid: "foreign-tip", address: "foreign-owned", amount: 33, outputType: 3, claimID: "claim-a"},
		{txid: "wrong-type", address: "first-owned", amount: 44, outputType: 1, claimID: "claim-a"},
		{txid: "other-claim", address: "second-owned", amount: 55, outputType: 3, claimID: "claim-b"},
		{txid: "spent-tip", address: "first-owned", amount: 66, outputType: 3, claimID: "claim-a", spent: true},
	} {
		addOutputQueryTestTransaction(t, database, support.txid, int64(8-index), 0, true)
		addOutputQueryTestOutput(
			t, database, support.txid, 0, support.address,
			support.amount, support.outputType, false,
		)
		mustExec(
			t, database.sql, "UPDATE txo SET claim_id = ? WHERE txoid = ?",
			support.claimID, support.txid+":0",
		)
		if support.spent {
			spendOutputQueryTestOutput(t, database, support.txid+":0", support.address)
		}
	}

	query := OutputQuery{
		TXIDs:                []string{"claim-a", "claim-b"},
		AnnotationAccountIDs: []string{"first", "second"},
		IncludeIsMyInput:     true,
		IncludeIsMyOutput:    true,
		IncludeReceivedTips:  true,
	}
	rows, err := database.ListOutputs(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("claim rows = %d, want 2", len(rows))
	}
	got := make(map[string]*int64, len(rows))
	for _, row := range rows {
		got[row.TXID] = row.ReceivedTips
	}
	for txid, want := range map[string]int64{"claim-a": 33, "claim-b": 55} {
		if got[txid] == nil || *got[txid] != want {
			t.Fatalf("%s received tips = %v, want %d", txid, got[txid], want)
		}
	}

	rows, err = database.ListOutputs(context.Background(), OutputQuery{
		TXID:                 "claim-a",
		AnnotationAccountIDs: []string{"missing"},
		IncludeReceivedTips:  true,
	})
	if err != nil || len(rows) != 1 || rows[0].ReceivedTips == nil || *rows[0].ReceivedTips != 0 {
		t.Fatalf("missing-account received tips = %#v, %v, want non-nil zero", rows, err)
	}
	rows, err = database.ListOutputs(context.Background(), OutputQuery{TXID: "claim-a"})
	if err != nil || len(rows) != 1 || rows[0].ReceivedTips != nil {
		t.Fatalf("unrequested received tips = %#v, %v, want nil", rows, err)
	}
	if _, err := database.ListOutputs(context.Background(), OutputQuery{
		IncludeReceivedTips: true,
	}); !errors.Is(err, ErrOutputAnnotationAccountsRequired) {
		t.Fatalf("accountless received tips error = %v", err)
	}
}

func TestBuildOutputQueryOrdersClaimIDArgumentsBetweenOutputIDsAndTypes(t *testing.T) {
	t.Parallel()
	unspent := false
	statement, arguments, err := buildOutputQuery("COUNT(*)", OutputQuery{
		TXOIDs:   []string{"tx:0", "tx:1"},
		ClaimIDs: []string{"claim-a", "claim-b"},
		Types:    []int64{1, 2},
		IsSpent:  &unspent,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	wantStatement := "SELECT COUNT(*) FROM txo\n" +
		"        JOIN tx ON (tx.txid = txo.txid)" +
		" LEFT JOIN txi AS spent ON (spent.txoid = txo.txoid)" +
		" WHERE txo.txoid IN (?,?) AND txo.claim_id IN (?,?)" +
		" AND txo.txo_type IN (?,?) AND txo.is_reserved = 0 AND spent.txoid IS NULL"
	if statement != wantStatement {
		t.Fatalf("claim-filtered statement = %q, want %q", statement, wantStatement)
	}
	wantArguments := []any{"tx:0", "tx:1", "claim-a", "claim-b", int64(1), int64(2)}
	if !reflect.DeepEqual(arguments, wantArguments) {
		t.Fatalf("claim-filtered arguments = %#v, want %#v", arguments, wantArguments)
	}
}

func TestOutputAggregatesIgnorePresentationAndReturnZeroForNullSum(t *testing.T) {
	t.Parallel()
	database := openOutputQueryTestDB(t)
	addOutputQueryTestAccount(t, database, "account", "owned")

	addOutputQueryTestTransaction(t, database, "ordinary", 3, 0, true)
	addOutputQueryTestOutput(t, database, "ordinary", 0, "owned", 10, 0, false)
	addOutputQueryTestTransaction(t, database, "purchase", 2, 0, true)
	addOutputQueryTestOutput(t, database, "purchase", 0, "owned", 20, 4, false)
	addOutputQueryTestTransaction(t, database, "reserved", 1, 0, true)
	addOutputQueryTestOutput(t, database, "reserved", 0, "owned", 30, 0, true)
	addOutputQueryTestTransaction(t, database, "spent", 0, 0, false)
	addOutputQueryTestOutput(t, database, "spent", 0, "owned", 40, 0, false)
	spendOutputQueryTestOutput(t, database, "spent:0", "owned")

	unspent := false
	zero, farOffset := 0, 100
	query := OutputQuery{
		AccountIDs: []string{"account"}, IsSpent: &unspent,
		Order: OutputOrder(255), Limit: &zero, Offset: &farOffset,
	}
	count, err := database.CountOutputs(context.Background(), query)
	if err != nil || count != 2 {
		t.Fatalf("aggregate count = %d, %v, want 2", count, err)
	}
	total, err := database.SumOutputs(context.Background(), query)
	if err != nil || total != 30 {
		t.Fatalf("aggregate sum = %d, %v, want 30", total, err)
	}

	total, err = database.SumOutputs(context.Background(), OutputQuery{
		AccountIDs: []string{"account"}, Types: []int64{99}, IsSpent: &unspent,
	})
	if err != nil || total != 0 {
		t.Fatalf("empty aggregate sum = %d, %v, want 0", total, err)
	}
	count, err = database.CountOutputs(context.Background(), OutputQuery{
		AccountIDs: []string{"missing"},
	})
	if err != nil || count != 0 {
		t.Fatalf("empty aggregate count = %d, %v, want 0", count, err)
	}
}

func TestReleaseAllOutputsSupportsUnknownScopedAndGlobalRelease(t *testing.T) {
	t.Parallel()
	database := openOutputQueryTestDB(t)
	addOutputQueryTestAccount(t, database, "first", "first-address")
	addOutputQueryTestAccount(t, database, "second", "second-address")

	addOutputQueryTestTransaction(t, database, "first", 3, 0, true)
	addOutputQueryTestOutput(t, database, "first", 0, "first-address", 10, 0, true)
	addOutputQueryTestTransaction(t, database, "first-spent", 2, 0, true)
	addOutputQueryTestOutput(t, database, "first-spent", 0, "first-address", 20, 6, true)
	spendOutputQueryTestOutput(t, database, "first-spent:0", "first-address")
	addOutputQueryTestTransaction(t, database, "second", 1, 0, true)
	addOutputQueryTestOutput(t, database, "second", 0, "second-address", 30, 4, true)
	addOutputQueryTestTransaction(t, database, "orphan", 0, 0, false)
	addOutputQueryTestOutput(t, database, "orphan", 0, "orphan-address", 40, 0, true)
	addOutputQueryTestTransaction(t, database, "plain", -1, 0, false)
	addOutputQueryTestOutput(t, database, "plain", 0, "first-address", 50, 0, false)

	unknown := "unknown"
	if err := database.ReleaseAllOutputs(context.Background(), &unknown); err != nil {
		t.Fatal(err)
	}
	if got := outputQueryTestReservedCount(t, database); got != 4 {
		t.Fatalf("unknown-account release left %d reservations, want 4", got)
	}

	first := "first"
	if err := database.ReleaseAllOutputs(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	if got := outputQueryTestReservedCount(t, database); got != 2 {
		t.Fatalf("scoped release left %d reservations, want 2", got)
	}
	if got := queryInt(t, database.sql, `SELECT COUNT(*) FROM txo
		WHERE txoid IN ('first:0', 'first-spent:0') AND is_reserved = 1`); got != 0 {
		t.Fatalf("scoped release retained %d owned reservations", got)
	}

	if err := database.ReleaseAllOutputs(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := outputQueryTestReservedCount(t, database); got != 0 {
		t.Fatalf("global release left %d reservations", got)
	}
}

func TestOutputQueryMethodsRequireOpenDatabaseAndHonorCancellation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var nilDatabase *DB
	assertOutputQueryTestUnavailable(t, nilDatabase, ctx)

	closed, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	assertOutputQueryTestUnavailable(t, closed, ctx)

	database := openOutputQueryTestDB(t)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := database.ListOutputs(canceled, OutputQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list error = %v, want context.Canceled", err)
	}
	if _, err := database.CountOutputs(canceled, OutputQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled count error = %v, want context.Canceled", err)
	}
	if _, err := database.SumOutputs(canceled, OutputQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled sum error = %v, want context.Canceled", err)
	}
	if err := database.ReleaseAllOutputs(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled release error = %v, want context.Canceled", err)
	}
}

func openOutputQueryTestDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Errorf("close output-query database: %v", err)
		}
	})
	return database
}

func addOutputQueryTestAccount(t *testing.T, database *DB, account, address string) {
	t.Helper()
	mustExec(t, database.sql, "INSERT OR IGNORE INTO pubkey_address (address) VALUES (?)", address)
	mustExec(t, database.sql, `INSERT INTO account_address
		(account, address, chain, pubkey, chain_code, n, depth)
		VALUES (?, ?, 0, x'', x'', 0, 0)`, account, address)
}

func addOutputQueryTestTransaction(
	t *testing.T, database *DB, txid string, height, position int64, verified bool,
) {
	t.Helper()
	mustExec(t, database.sql, `INSERT INTO tx
		(txid, raw, height, position, is_verified) VALUES (?, ?, ?, ?, ?)`,
		txid, []byte{0x01, byte(len(txid))}, height, position, verified)
}

func addOutputQueryTestOutput(
	t *testing.T,
	database *DB,
	txid string,
	position int64,
	address string,
	amount int64,
	outputType int64,
	reserved bool,
) {
	t.Helper()
	mustExec(t, database.sql, "INSERT OR IGNORE INTO pubkey_address (address) VALUES (?)", address)
	txoid := txid + ":" + outputQueryTestPosition(position)
	mustExec(t, database.sql, `INSERT INTO txo
		(txid, txoid, address, position, amount, script, is_reserved, txo_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, txid, txoid, address, position, amount,
		[]byte{0x51, byte(position)}, reserved, outputType)
}

func spendOutputQueryTestOutput(t *testing.T, database *DB, txoid, address string) {
	t.Helper()
	mustExec(t, database.sql, `INSERT INTO txi (txid, txoid, address, position)
		VALUES (?, ?, ?, 0)`, "spending-"+txoid, txoid, address)
}

func assertOutputQueryTestIDs(
	t *testing.T, database *DB, query OutputQuery, want []string,
) {
	t.Helper()
	rows, err := database.ListOutputs(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if got := outputQueryTestIDs(rows); !reflect.DeepEqual(got, want) {
		t.Fatalf("output IDs = %v, want %v for query %#v", got, want, query)
	}
}

func outputQueryTestIDs(rows []OutputRow) []string {
	ids := make([]string, len(rows))
	for index, row := range rows {
		ids[index] = row.TXOID
	}
	return ids
}

func outputQueryTestReservedCount(t *testing.T, database *DB) int {
	t.Helper()
	return queryInt(t, database.sql, "SELECT COUNT(*) FROM txo WHERE is_reserved = 1")
}

func assertOutputQueryTestUnavailable(t *testing.T, database *DB, ctx context.Context) {
	t.Helper()
	if _, err := database.ListOutputs(ctx, OutputQuery{}); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("unavailable list error = %v, want ErrNotOpen", err)
	}
	if _, err := database.CountOutputs(ctx, OutputQuery{}); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("unavailable count error = %v, want ErrNotOpen", err)
	}
	if _, err := database.SumOutputs(ctx, OutputQuery{}); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("unavailable sum error = %v, want ErrNotOpen", err)
	}
	if err := database.ReleaseAllOutputs(ctx, nil); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("unavailable release error = %v, want ErrNotOpen", err)
	}
}

func outputQueryTestPosition(position int64) string {
	if position == 0 {
		return "0"
	}
	if position == 1 {
		return "1"
	}
	panic("output query test position only supports zero and one")
}

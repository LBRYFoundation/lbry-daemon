package ledgerdb

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestTransactionQueriesScopeAccountsAcrossIncomingAndOutgoingRows(t *testing.T) {
	t.Parallel()
	database := openTransactionQueryTestDB(t)
	addTransactionQueryTestAccount(t, database, "account", "mine")

	addTransactionQueryTestTransaction(t, database, "incoming", 1, 0, false)
	addTransactionQueryTestOutput(t, database, "incoming", "incoming:0", "mine")
	addTransactionQueryTestTransaction(t, database, "outgoing", 2, 0, false)
	addTransactionQueryTestInput(t, database, "outgoing", "previous:0", "mine")
	addTransactionQueryTestTransaction(t, database, "both", 3, 0, true)
	addTransactionQueryTestOutput(t, database, "both", "both:0", "mine")
	addTransactionQueryTestInput(t, database, "both", "previous:1", "mine")
	addTransactionQueryTestTransaction(t, database, "unrelated", 4, 0, false)
	addTransactionQueryTestOutput(t, database, "unrelated", "unrelated:0", "theirs")

	transactions, err := database.ListTransactions(context.Background(), TransactionQuery{
		AccountIDs: []string{"account"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionQueryTestIDs(transactions), []string{"both", "outgoing", "incoming"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("account transaction IDs = %v, want %v", got, want)
	}
	if len(transactions) != 3 || !transactions[0].IsVerified ||
		string(transactions[0].Raw) != "raw-both" {
		t.Fatalf("hydrated account transactions = %#v", transactions)
	}
	count, err := database.CountTransactions(context.Background(), TransactionQuery{
		AccountIDs: []string{"account"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("account transaction count = %d, want 3", count)
	}
}

func TestTransactionQueriesRequireAccountsWithoutTXIDConstraint(t *testing.T) {
	t.Parallel()
	database := openTransactionQueryTestDB(t)

	transactions, listErr := database.ListTransactions(
		context.Background(), TransactionQuery{},
	)
	if transactions != nil {
		t.Fatalf("transactions on missing account error = %#v, want nil", transactions)
	}
	if listErr != ErrTransactionAccountsRequired ||
		listErr.Error() != "'accounts' argument required when no 'txid' constraint is present" {
		t.Fatalf("missing-account list error = %v", listErr)
	}
	count, countErr := database.CountTransactions(
		context.Background(), TransactionQuery{AccountIDs: []string{}},
	)
	if count != 0 {
		t.Fatalf("count on missing account error = %d, want 0", count)
	}
	if countErr != ErrTransactionAccountsRequired ||
		countErr.Error() != "'accounts' argument required when no 'txid' constraint is present" {
		t.Fatalf("missing-account count error = %v", countErr)
	}
}

func TestTransactionQueriesTXIDConstraintsBypassAccountScope(t *testing.T) {
	t.Parallel()
	database := openTransactionQueryTestDB(t)
	addTransactionQueryTestAccount(t, database, "scoped", "mine")
	for index, txid := range []string{"first", "second", "third"} {
		addTransactionQueryTestTransaction(
			t, database, txid, int64(index+1), 0, false,
		)
	}
	addTransactionQueryTestOutput(t, database, "first", "first:0", "mine")

	txid := "third"
	transactions, err := database.ListTransactions(context.Background(), TransactionQuery{
		AccountIDs: []string{"scoped"},
		TXID:       &txid,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionQueryTestIDs(transactions), []string{"third"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit TXID result = %v, want %v", got, want)
	}

	transactions, err = database.ListTransactions(context.Background(), TransactionQuery{
		AccountIDs: []string{"missing-account"},
		TXIDs:      []string{"first", "third"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionQueryTestIDs(transactions), []string{"third", "first"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit TXIDs result = %v, want %v", got, want)
	}

	// A non-nil empty slice means the caller supplied txid__in. The pinned
	// constraint helper emits no IN predicate while still bypassing accounts.
	emptyTXIDs := []string{}
	transactions, err = database.ListTransactions(context.Background(), TransactionQuery{
		TXIDs: emptyTXIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionQueryTestIDs(transactions), []string{"third", "second", "first"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("empty supplied TXIDs result = %v, want %v", got, want)
	}
	count, err := database.CountTransactions(context.Background(), TransactionQuery{
		TXIDs: emptyTXIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("empty supplied TXIDs count = %d, want 3", count)
	}
}

func TestTransactionQueriesPreservePrimitiveTXIDValues(t *testing.T) {
	t.Parallel()
	database := openTransactionQueryTestDB(t)
	addTransactionQueryTestTransaction(t, database, "1", 1, 0, false)

	for _, value := range []any{int64(1), true} {
		transactions, err := database.ListTransactions(context.Background(), TransactionQuery{
			TXIDValue: value, HasTXIDValue: true,
		})
		if err != nil {
			t.Fatalf("primitive TXID %v: %v", value, err)
		}
		if got := transactionQueryTestIDs(transactions); !reflect.DeepEqual(got, []string{"1"}) {
			t.Fatalf("primitive TXID %v result = %v, want [1]", value, got)
		}
	}

	transactions, err := database.ListTransactions(context.Background(), TransactionQuery{
		TXIDValue: nil, HasTXIDValue: true,
	})
	if err != nil || len(transactions) != 0 {
		t.Fatalf("null TXID result = %#v, %v", transactions, err)
	}
}

func TestListTransactionsDefaultOrderHeightFiltersAndPagination(t *testing.T) {
	t.Parallel()
	database := openTransactionQueryTestDB(t)
	rows := []struct {
		txid     string
		height   int64
		position int64
	}{
		{"negative-two", -2, 10},
		{"positive-two", 2, 3},
		{"zero-low", 0, 1},
		{"positive-nine-low", 9, 2},
		{"negative-one", -1, 20},
		{"zero-high", 0, 8},
		{"positive-nine-high", 9, 7},
	}
	for _, row := range rows {
		addTransactionQueryTestTransaction(
			t, database, row.txid, row.height, row.position, false,
		)
	}
	emptyTXIDs := []string{}
	transactions, err := database.ListTransactions(context.Background(), TransactionQuery{
		TXIDs: emptyTXIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{
		"zero-high", "zero-low", "positive-nine-high", "positive-nine-low",
		"positive-two", "negative-one", "negative-two",
	}
	if got := transactionQueryTestIDs(transactions); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("default transaction order = %v, want %v", got, wantOrder)
	}

	heightLTE, heightGT := int64(9), int64(0)
	transactions, err = database.ListTransactions(context.Background(), TransactionQuery{
		TXIDs: emptyTXIDs, HeightLTE: &heightLTE, HeightGT: &heightGT,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionQueryTestIDs(transactions),
		[]string{"positive-nine-high", "positive-nine-low", "positive-two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("height-filtered IDs = %v, want %v", got, want)
	}
	filteredCount, err := database.CountTransactions(context.Background(), TransactionQuery{
		TXIDs: emptyTXIDs, HeightLTE: &heightLTE, HeightGT: &heightGT,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filteredCount != 3 {
		t.Fatalf("height-filtered count = %d, want 3", filteredCount)
	}

	limit, offset := 3, 2
	transactions, err = database.ListTransactions(context.Background(), TransactionQuery{
		TXIDs: emptyTXIDs, Limit: &limit, Offset: &offset,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionQueryTestIDs(transactions),
		[]string{"positive-nine-high", "positive-nine-low", "positive-two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paginated IDs = %v, want %v", got, want)
	}
}

func TestCountTransactionsDropsPresentationConstraints(t *testing.T) {
	t.Parallel()
	database := openTransactionQueryTestDB(t)
	for index := 0; index < 4; index++ {
		addTransactionQueryTestTransaction(
			t, database, fmt.Sprintf("tx-%d", index), int64(index), int64(index), false,
		)
	}
	limit, offset := 1, 100
	count, err := database.CountTransactions(context.Background(), TransactionQuery{
		TXIDs: []string{}, Limit: &limit, Offset: &offset,
		Order: TransactionOrder(255),
	})
	if err != nil {
		t.Fatalf("count with discarded presentation constraints = %v", err)
	}
	if count != 4 {
		t.Fatalf("count with discarded presentation constraints = %d, want 4", count)
	}
}

func TestTransactionQueriesReuseNumberedAccountPlaceholders(t *testing.T) {
	t.Parallel()
	database := openTransactionQueryTestDB(t)
	const accountCount = 600
	accounts := make([]string, accountCount)
	for index := range accounts {
		accounts[index] = fmt.Sprintf("account-%03d", index)
	}
	addTransactionQueryTestAccount(t, database, accounts[len(accounts)-1], "mine")
	addTransactionQueryTestTransaction(t, database, "matched", 7, 0, false)
	addTransactionQueryTestOutput(t, database, "matched", "matched:0", "mine")
	heightLTE := int64(7)
	query := TransactionQuery{AccountIDs: accounts, HeightLTE: &heightLTE}

	statement, arguments, err := buildTransactionQuery("tx.txid", query, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(statement, "?600") != 2 {
		t.Fatalf("highest numbered placeholder occurrences = %d, want 2 in %q",
			strings.Count(statement, "?600"), statement)
	}
	if len(arguments) != accountCount+1 {
		t.Fatalf("query argument count = %d, want %d", len(arguments), accountCount+1)
	}
	if arguments[0] != accounts[0] || arguments[accountCount-1] != accounts[accountCount-1] ||
		arguments[accountCount] != heightLTE {
		t.Fatalf("query argument boundaries = %#v, %#v, %#v",
			arguments[0], arguments[accountCount-1], arguments[accountCount])
	}

	transactions, err := database.ListTransactions(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionQueryTestIDs(transactions), []string{"matched"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("many-account result = %v, want %v", got, want)
	}
}

func TestListTransactionsRejectsUnknownOrder(t *testing.T) {
	t.Parallel()
	database := openTransactionQueryTestDB(t)
	transactions, err := database.ListTransactions(context.Background(), TransactionQuery{
		TXIDs: []string{}, Order: TransactionOrder(42),
	})
	if transactions != nil {
		t.Fatalf("transactions with invalid order = %#v, want nil", transactions)
	}
	if err == nil || err.Error() != "unknown wallet transaction order 42" {
		t.Fatalf("invalid-order error = %v", err)
	}
}

func TestTransactionQueriesUnavailableAndCanceled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	query := TransactionQuery{TXIDs: []string{}}
	var nilDatabase *DB
	assertTransactionQueryTestUnavailable(t, nilDatabase, ctx, query)

	closed, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	assertTransactionQueryTestUnavailable(t, closed, ctx, query)

	database := openTransactionQueryTestDB(t)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := database.ListTransactions(canceled, query); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list error = %v, want context.Canceled", err)
	}
	if _, err := database.CountTransactions(canceled, query); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled count error = %v, want context.Canceled", err)
	}
}

func openTransactionQueryTestDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Errorf("close transaction-query database: %v", err)
		}
	})
	return database
}

func addTransactionQueryTestAccount(t *testing.T, database *DB, account, address string) {
	t.Helper()
	mustExec(t, database.sql, "INSERT OR IGNORE INTO pubkey_address (address) VALUES (?)", address)
	mustExec(t, database.sql, `INSERT INTO account_address
		(account, address, chain, pubkey, chain_code, n, depth)
		VALUES (?, ?, 0, x'', x'', 0, 0)`, account, address)
}

func addTransactionQueryTestTransaction(
	t *testing.T, database *DB, txid string, height, position int64, verified bool,
) {
	t.Helper()
	mustExec(t, database.sql, `INSERT INTO tx
		(txid, raw, height, position, is_verified) VALUES (?, ?, ?, ?, ?)`,
		txid, []byte("raw-"+txid), height, position, verified)
}

func addTransactionQueryTestOutput(
	t *testing.T, database *DB, txid, txoid, address string,
) {
	t.Helper()
	mustExec(t, database.sql, "INSERT OR IGNORE INTO pubkey_address (address) VALUES (?)", address)
	mustExec(t, database.sql, `INSERT INTO txo
		(txid, txoid, address, position, amount, script)
		VALUES (?, ?, ?, 0, 1, x'')`, txid, txoid, address)
}

func addTransactionQueryTestInput(
	t *testing.T, database *DB, txid, txoid, address string,
) {
	t.Helper()
	mustExec(t, database.sql, "INSERT OR IGNORE INTO pubkey_address (address) VALUES (?)", address)
	mustExec(t, database.sql, `INSERT INTO txi (txid, txoid, address, position)
		VALUES (?, ?, ?, 0)`, txid, txoid, address)
}

func transactionQueryTestIDs(transactions []TransactionRow) []string {
	ids := make([]string, len(transactions))
	for index, transaction := range transactions {
		ids[index] = transaction.TXID
	}
	return ids
}

func assertTransactionQueryTestUnavailable(
	t *testing.T, database *DB, ctx context.Context, query TransactionQuery,
) {
	t.Helper()
	if _, err := database.ListTransactions(ctx, query); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("unavailable list error = %v, want ErrNotOpen", err)
	}
	if _, err := database.CountTransactions(ctx, query); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("unavailable count error = %v, want ErrNotOpen", err)
	}
}

package ledgerdb

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestListSpendableOutputsMatchesOwnedUTXOOrderingAndFilters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	addSpendableTestAccount(t, database, "account", "owned")
	addSpendableTestAccount(t, database, "other", "other-address")

	addSpendableTestOutput(t, database, "zero", "zero:0", "owned", 0, 3, 30, false, 0)
	addSpendableTestOutput(t, database, "pending", "pending:0", "owned", -1, 8, 80, false, 4)
	addSpendableTestOutput(t, database, "high", "high:0", "owned", 10, 1, 100, true, 6)
	addSpendableTestOutput(t, database, "low", "low:0", "owned", 5, 9, 50, true, 0)
	addSpendableTestOutput(t, database, "reserved", "reserved:0", "owned", 20, 0, 200, true, 0)
	mustExec(t, database.sql, "UPDATE txo SET is_reserved=1 WHERE txoid='reserved:0'")
	addSpendableTestOutput(t, database, "spent", "spent:0", "owned", 30, 0, 300, true, 0)
	mustExec(t, database.sql, "INSERT INTO txi (txid, txoid, address, position) VALUES ('consumer', 'spent:0', 'owned', 0)")
	addSpendableTestOutput(t, database, "foreign", "foreign:0", "other-address", 40, 0, 400, true, 0)

	rows, err := database.ListSpendableOutputs(ctx, []string{"account"})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(rows))
	for index, row := range rows {
		got[index] = row.TXOID
		if row.TXID+":0" != row.TXOID || row.OutputPosition != 0 || len(row.Raw) != 1 || len(row.Script) != 1 {
			t.Fatalf("row %d = %#v", index, row)
		}
	}
	want := []string{"zero:0", "pending:0", "low:0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spendable order = %v, want %v", got, want)
	}
}

func TestSQLiteSpendableSelectionPrefersVerifiedAndReservesAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	addSpendableTestAccount(t, database, "account", "owned")
	addSpendableTestOutput(t, database, "unconfirmed", "unconfirmed:0", "owned", 0, 0, 100, false, 0)
	addSpendableTestOutput(t, database, "confirmed-small", "confirmed-small:0", "owned", 5, 0, 150, true, 0)
	addSpendableTestOutput(t, database, "confirmed-large", "confirmed-large:0", "owned", 4, 0, 200, true, 0)
	addSpendableTestOutput(t, database, "claim", "claim:0", "owned", 6, 0, 1000, true, 1)

	selected, err := database.GetAndReserveSpendableOutputs(
		ctx, []string{"account"}, 250, 1, 0, true, false, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := spendableTestIDs(selected), []string{"confirmed-small:0", "confirmed-large:0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("verified selection = %v, want %v", got, want)
	}
	if got := queryInt(t, database.sql, "SELECT COUNT(*) FROM txo WHERE is_reserved=1"); got != 2 {
		t.Fatalf("reserved outputs = %d, want 2", got)
	}
	if err := database.ReserveOutputs(ctx, spendableTestIDs(selected), false); err != nil {
		t.Fatal(err)
	}

	selected, err = database.GetAndReserveSpendableOutputs(
		ctx, []string{"account"}, 400, 1, 0, true, false, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := spendableTestIDs(selected), []string{
		"confirmed-small:0", "confirmed-large:0", "unconfirmed:0",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback selection = %v, want %v", got, want)
	}
	if got := queryInt(t, database.sql, "SELECT is_reserved FROM txo WHERE txoid='claim:0'"); got != 0 {
		t.Fatalf("non-payment output reservation = %d, want 0", got)
	}
}

func TestSQLiteSpendableSelectionInsufficientAndZeroFloorQuirks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	addSpendableTestAccount(t, database, "account", "owned")
	addSpendableTestOutput(t, database, "available", "available:0", "owned", 2, 0, 500, true, 0)

	selected, err := database.GetAndReserveSpendableOutputs(
		ctx, []string{"account"}, 1000, 1, 0, true, false, nil, nil,
	)
	if err != nil || len(selected) != 0 {
		t.Fatalf("insufficient selection = %#v, %v", selected, err)
	}
	if got := queryInt(t, database.sql, "SELECT is_reserved FROM txo WHERE txoid='available:0'"); got != 0 {
		t.Fatalf("insufficient reservation = %d, want 0", got)
	}

	selected, err = database.GetAndReserveSpendableOutputs(
		ctx, []string{"account"}, 1000, 1, 0, true, true, nil, nil,
	)
	if err != nil || !reflect.DeepEqual(spendableTestIDs(selected), []string{"available:0"}) {
		t.Fatalf("diagnostic insufficient selection = %#v, %v", selected, err)
	}
	if got := queryInt(t, database.sql, "SELECT is_reserved FROM txo WHERE txoid='available:0'"); got != 0 {
		t.Fatalf("diagnostic insufficient reservation = %d, want 0", got)
	}

	selected, err = database.GetAndReserveSpendableOutputs(
		ctx, []string{"account"}, 5, 0, 0, true, false, nil, nil,
	)
	if err != nil || len(selected) != 0 {
		t.Fatalf("zero-floor selection = %#v, %v", selected, err)
	}
}

func TestSQLiteSpendableSelectionDecoderFailureRollsBackReservation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	addSpendableTestAccount(t, database, "account", "owned")
	addSpendableTestOutput(t, database, "invalid", "invalid:0", "owned", 2, 0, 500, true, 0)
	wantErr := errors.New("unsupported stored output")
	_, err = database.GetAndReserveSpendableOutputs(
		ctx, []string{"account"}, 100, 1, 0, true, false,
		func(SpendableOutputRow) (int64, error) { return 0, wantErr },
		nil,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("decoder error = %v, want %v", err, wantErr)
	}
	if got := queryInt(t, database.sql, "SELECT is_reserved FROM txo WHERE txoid='invalid:0'"); got != 0 {
		t.Fatalf("failed decoder reservation = %d, want 0", got)
	}
}

func TestSQLiteSpendableSelectionStreamsDecodeBeforeDeferredUnconfirmedSpend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	addSpendableTestAccount(t, database, "account", "owned")
	addSpendableTestOutput(t, database, "early", "early:0", "owned", 0, 0, 100, false, 0)
	addSpendableTestOutput(t, database, "enough", "enough:0", "owned", 2, 0, 200, true, 0)
	addSpendableTestOutput(t, database, "unseen", "unseen:0", "owned", 0, 0, 300, false, 0)

	wantErr := errors.New("early parent decode failed")
	_, err = database.GetAndReserveSpendableOutputs(
		ctx, []string{"account"}, 150, 1, 0, true, false,
		func(row SpendableOutputRow) (int64, error) { return row.Amount, nil },
		func(row SpendableOutputRow) error {
			if row.TXOID == "early:0" {
				return wantErr
			}
			return nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("early inspection error = %v, want %v", err, wantErr)
	}
	if got := queryInt(t, database.sql, "SELECT COUNT(*) FROM txo WHERE is_reserved=1"); got != 0 {
		t.Fatalf("failed streamed inspection reserved %d outputs", got)
	}

	inspected := make([]string, 0)
	effective := make([]string, 0)
	selected, err := database.GetAndReserveSpendableOutputs(
		ctx, []string{"account"}, 150, 1, 0, true, false,
		func(row SpendableOutputRow) (int64, error) {
			effective = append(effective, row.TXOID)
			return row.Amount, nil
		},
		func(row SpendableOutputRow) error {
			inspected = append(inspected, row.TXOID)
			if row.TXOID == "unseen:0" {
				return errors.New("row after sufficient verified value was inspected")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := spendableTestIDs(selected), []string{"enough:0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("streamed selection = %v, want %v", got, want)
	}
	if want := []string{"early:0", "enough:0"}; !reflect.DeepEqual(inspected, want) {
		t.Fatalf("inspected rows = %v, want %v", inspected, want)
	}
	if want := []string{"enough:0"}; !reflect.DeepEqual(effective, want) {
		t.Fatalf("effective rows = %v, want %v", effective, want)
	}
}

func TestSQLiteSpendableSelectionEffectiveAmountOverflowRollsBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	addSpendableTestAccount(t, database, "account", "owned")
	addSpendableTestOutput(t, database, "first", "first:0", "owned", 2, 0, 100, true, 0)
	addSpendableTestOutput(t, database, "second", "second:0", "owned", 2, 0, 200, true, 0)
	_, err = database.GetAndReserveSpendableOutputs(
		ctx, []string{"account"}, math.MaxInt64, 1, 0, true, false,
		func(row SpendableOutputRow) (int64, error) {
			if row.TXOID == "first:0" {
				return math.MaxInt64 - 1, nil
			}
			return 10, nil
		},
		nil,
	)
	if !errors.Is(err, ErrSpendableAmountOverflow) {
		t.Fatalf("effective amount overflow = %v", err)
	}
	if got := queryInt(t, database.sql, "SELECT COUNT(*) FROM txo WHERE is_reserved=1"); got != 0 {
		t.Fatalf("overflow reserved %d outputs", got)
	}
}

func TestSQLiteSpendableSelectionTreatsNonzeroVerifiedIntegerAsTrue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	addSpendableTestAccount(t, database, "account", "owned")
	addSpendableTestOutput(t, database, "truthy", "truthy:0", "owned", 0, 0, 200, false, 0)
	mustExec(t, database.sql, "UPDATE tx SET is_verified=2 WHERE txid='truthy'")
	selected, err := database.GetAndReserveSpendableOutputs(
		ctx, []string{"account"}, 100, 1, 0, true, false, nil, nil,
	)
	if err != nil || len(selected) != 1 || !selected[0].IsVerified {
		t.Fatalf("nonzero verified selection = %#v, %v", selected, err)
	}
}

func TestSpendableMethodsRequireOpenDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var database *DB
	if _, err := database.ListSpendableOutputs(ctx, nil); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("nil list error = %v", err)
	}
	if _, err := database.GetAndReserveSpendableOutputs(ctx, nil, 1, 1, 50, true, false, nil, nil); err == nil {
		t.Fatal("nil SQLite selection unexpectedly succeeded")
	}
}

func addSpendableTestAccount(t *testing.T, database *DB, account, address string) {
	t.Helper()
	mustExec(t, database.sql, `INSERT OR IGNORE INTO pubkey_address (address) VALUES (?)`, address)
	mustExec(t, database.sql, `INSERT INTO account_address
        (account, address, chain, pubkey, chain_code, n, depth)
        VALUES (?, ?, 0, x'', x'', 0, 0)`, account, address)
}

func addSpendableTestOutput(
	t *testing.T,
	database *DB,
	txid string,
	txoid string,
	address string,
	height int64,
	txPosition int64,
	amount int64,
	verified bool,
	txoType int64,
) {
	t.Helper()
	mustExec(t, database.sql, `INSERT INTO tx
        (txid, raw, height, position, is_verified) VALUES (?, x'01', ?, ?, ?)`,
		txid, height, txPosition, verified)
	mustExec(t, database.sql, `INSERT INTO txo
        (txid, txoid, address, position, amount, script, txo_type)
        VALUES (?, ?, ?, 0, ?, x'51', ?)`, txid, txoid, address, amount, txoType)
}

func spendableTestIDs(rows []SpendableOutputRow) []string {
	result := make([]string, len(rows))
	for index, row := range rows {
		result[index] = row.TXOID
	}
	return result
}

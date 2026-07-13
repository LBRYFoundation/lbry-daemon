package ledgerdb

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

func TestAddKeysAndAddressHistoryMatchPinnedTransactions(t *testing.T) {
	t.Parallel()

	database, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	keys := []AddressKey{
		{Address: "address-0", Chain: 0, PublicKey: []byte{1, 2}, ChainCode: []byte{3, 4}, N: 0, Depth: 4},
		{Address: "address-1", Chain: 1, PublicKey: []byte{5, 6}, ChainCode: []byte{7, 8}, N: 1, Depth: 4},
	}
	if err := database.AddKeys(context.Background(), "account", keys); err != nil {
		t.Fatal(err)
	}
	changed := []AddressKey{{
		Address: "address-0", Chain: 99, PublicKey: []byte{9}, ChainCode: []byte{9}, N: 99, Depth: 99,
	}}
	if err := database.AddKeys(context.Background(), "account", changed); err != nil {
		t.Fatal(err)
	}
	if got := queryInt(t, database.sql, "SELECT COUNT(*) FROM account_address"); got != 2 {
		t.Fatalf("account key rows = %d, want 2", got)
	}
	if got := queryInt(t, database.sql, "SELECT COUNT(*) FROM pubkey_address"); got != 2 {
		t.Fatalf("public address rows = %d, want 2", got)
	}
	var chain, n, depth int64
	var publicKey, chainCode []byte
	if err := database.sql.QueryRow(`SELECT chain, pubkey, chain_code, n, depth
        FROM account_address WHERE account='account' AND address='address-0'`).Scan(
		&chain, &publicKey, &chainCode, &n, &depth,
	); err != nil {
		t.Fatal(err)
	}
	if chain != 0 || n != 0 || depth != 4 ||
		!bytes.Equal(publicKey, []byte{1, 2}) || !bytes.Equal(chainCode, []byte{3, 4}) {
		t.Fatalf("INSERT OR IGNORE row changed: chain=%d n=%d depth=%d pub=%x code=%x", chain, n, depth, publicKey, chainCode)
	}

	history := "txid:0:other:1:"
	if err := database.SetAddressHistory(context.Background(), "address-0", history); err != nil {
		t.Fatal(err)
	}
	var storedHistory string
	var usedTimes int
	if err := database.sql.QueryRow(
		"SELECT history, used_times FROM pubkey_address WHERE address='address-0'",
	).Scan(&storedHistory, &usedTimes); err != nil {
		t.Fatal(err)
	}
	if storedHistory != history || usedTimes != 2 {
		t.Fatalf("history = %q uses %d", storedHistory, usedTimes)
	}
	if err := database.SetAddressHistory(context.Background(), "unknown", "a:b:"); err != nil {
		t.Fatalf("unknown history update = %v", err)
	}
	if err := database.AddKeys(context.Background(), "account", nil); err != nil {
		t.Fatalf("empty AddKeys = %v", err)
	}
}

func TestAddKeysKeepsFirstTransactionOnSecondPhaseFailure(t *testing.T) {
	t.Parallel()

	database, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	mustExec(t, database.sql, `CREATE TRIGGER reject_pubkey_address
        BEFORE INSERT ON pubkey_address BEGIN SELECT RAISE(ABORT, 'rejected'); END`)
	err = database.AddKeys(context.Background(), "account", []AddressKey{{
		Address: "partial", PublicKey: []byte{1}, ChainCode: []byte{2},
	}})
	if err == nil {
		t.Fatal("second AddKeys phase unexpectedly succeeded")
	}
	if got := queryInt(t, database.sql, "SELECT COUNT(*) FROM account_address WHERE address='partial'"); got != 1 {
		t.Fatalf("first-phase rows = %d, want 1", got)
	}
	if got := queryInt(t, database.sql, "SELECT COUNT(*) FROM pubkey_address WHERE address='partial'"); got != 0 {
		t.Fatalf("second-phase rows = %d, want 0", got)
	}
}

func TestGetAddressesPreservesPinnedInventoryOrderingAndFilters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(ctx)
	keys := make([]AddressKey, 4)
	for index := range keys {
		keys[index] = AddressKey{
			Address: "address-" + string(rune('0'+index)), Chain: 0,
			PublicKey: []byte{byte(index)}, ChainCode: []byte{byte(index + 10)},
			N: int64(index), Depth: 2,
		}
	}
	if err := database.AddKeys(ctx, "account", keys); err != nil {
		t.Fatal(err)
	}
	if err := database.SetAddressHistory(ctx, "address-0", "a:1:"); err != nil {
		t.Fatal(err)
	}
	if err := database.SetAddressHistory(ctx, "address-2", "a:1:b:2:c:3:"); err != nil {
		t.Fatal(err)
	}
	chain := int64(0)
	records, err := database.GetAddresses(ctx, AddressQuery{
		Account: "account", Chain: &chain, Order: AddressOrderUsedTimesAscending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := addressRecordIndexes(records); !reflect.DeepEqual(got, []int64{1, 3, 0, 2}) {
		t.Fatalf("inventory order = %v", got)
	}
	maximumUses := int64(2)
	records, err = database.GetAddresses(ctx, AddressQuery{
		Account: "account", Chain: &chain, UsedTimesLT: &maximumUses,
		Order: AddressOrderUsedTimesAscending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := addressRecordIndexes(records); !reflect.DeepEqual(got, []int64{1, 3, 0}) {
		t.Fatalf("usable order = %v", got)
	}
	limit := 2
	records, err = database.GetAddresses(ctx, AddressQuery{
		Account: "account", Chain: &chain, Order: AddressOrderIndexDescending, Limit: &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := addressRecordIndexes(records); !reflect.DeepEqual(got, []int64{3, 2}) {
		t.Fatalf("recent order = %v", got)
	}
	records, err = database.GetAddresses(ctx, AddressQuery{
		Addresses: []string{"address-3", "address-1"}, Order: AddressOrderIndexAscending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := addressRecordIndexes(records); !reflect.DeepEqual(got, []int64{1, 3}) {
		t.Fatalf("address-list filter = %v", got)
	}
	records, err = database.GetAddresses(ctx, AddressQuery{Addresses: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 {
		t.Fatalf("empty address-list filter returned %d records, want all 4", len(records))
	}
	record, err := database.GetAddress(ctx, "address-0")
	if err != nil || record == nil || record.History == nil || *record.History != "a:1:" ||
		record.UsedTimes != 1 || !bytes.Equal(record.PublicKey, []byte{0}) {
		t.Fatalf("address record = %#v, %v", record, err)
	}
	missing, err := database.GetAddress(ctx, "missing")
	if err != nil || missing != nil {
		t.Fatalf("missing address = %#v, %v", missing, err)
	}
}

func addressRecordIndexes(records []AddressRecord) []int64 {
	indexes := make([]int64, len(records))
	for index, record := range records {
		indexes[index] = record.N
	}
	return indexes
}

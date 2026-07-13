package ledgerdb

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
)

func TestSaveTransactionIOBatchStoresProjectedRowsAndHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	addTransactionTestAddress(t, database, "watched")

	purchasedClaimID := "purchased"
	day := 2461232.5
	outputAddress := "destination"
	claimID := "claim"
	claimName := "name"
	channelID := "channel"
	repostedClaimID := "repost"
	rows := []TransactionIORow{{
		Transaction: TransactionRow{
			TXID: "tx-one", Raw: []byte{0x01, 0x02}, Height: 9,
			Position: 3, IsVerified: true, PurchasedClaimID: &purchasedClaimID, Day: &day,
		},
		Inputs: []TransactionInputRow{{TXOID: "previous:0", Position: 2}},
		Outputs: []TransactionOutputRow{{
			TXOID: "tx-one:0", Address: &outputAddress, Position: 0,
			Amount: 42, Script: []byte{0x51}, TXOType: 6,
			ClaimID: &claimID, ClaimName: &claimName, HasSource: true,
			ChannelID: &channelID, RepostedClaimID: &repostedClaimID,
		}},
	}, {
		Transaction: TransactionRow{
			TXID: "tx-two", Raw: []byte{0x03}, Height: 0, Position: -1,
		},
		Outputs: []TransactionOutputRow{{
			TXOID: "tx-two:0", Address: &outputAddress, Amount: 7,
			Script: []byte{0x52},
		}},
	}}
	if err := database.SaveTransactionIOBatch(
		ctx, rows, "watched", "tx-one:9:tx-two:0:",
	); err != nil {
		t.Fatal(err)
	}

	var raw []byte
	var height, position int64
	var verified bool
	var storedPurchased sql.NullString
	var storedDay sql.NullFloat64
	var dayType string
	if err := database.sql.QueryRow(`SELECT raw, height, position, is_verified,
        purchased_claim_id, day, typeof(day) FROM tx WHERE txid = 'tx-one'`).Scan(
		&raw, &height, &position, &verified, &storedPurchased, &storedDay, &dayType,
	); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, []byte{0x01, 0x02}) || height != 9 || position != 3 ||
		!verified || !storedPurchased.Valid || storedPurchased.String != purchasedClaimID ||
		!storedDay.Valid || storedDay.Float64 != day || dayType != "real" {
		t.Fatalf("stored transaction = raw %x height %d position %d verified %v purchased %#v day %#v (%s)",
			raw, height, position, verified, storedPurchased, storedDay, dayType)
	}
	if got := queryInt(t, database.sql, "SELECT COUNT(*) FROM tx"); got != 2 {
		t.Fatalf("transaction rows = %d, want 2", got)
	}

	var inputTXID, inputTXOID, inputAddress string
	var inputPosition int64
	if err := database.sql.QueryRow(
		"SELECT txid, txoid, address, position FROM txi",
	).Scan(&inputTXID, &inputTXOID, &inputAddress, &inputPosition); err != nil {
		t.Fatal(err)
	}
	if inputTXID != "tx-one" || inputTXOID != "previous:0" ||
		inputAddress != "watched" || inputPosition != 2 {
		t.Fatalf("stored input = %q %q %q %d", inputTXID, inputTXOID, inputAddress, inputPosition)
	}

	var outputTXID, storedOutputAddress string
	var outputPosition, amount, reserved, txoType int64
	var script []byte
	var storedClaimID, storedClaimName, storedChannelID, storedRepostID sql.NullString
	var hasSource bool
	if err := database.sql.QueryRow(`SELECT txid, address, position, amount, script,
        is_reserved, txo_type, claim_id, claim_name, has_source, channel_id,
        reposted_claim_id FROM txo WHERE txoid = 'tx-one:0'`).Scan(
		&outputTXID, &storedOutputAddress, &outputPosition, &amount, &script,
		&reserved, &txoType, &storedClaimID, &storedClaimName, &hasSource,
		&storedChannelID, &storedRepostID,
	); err != nil {
		t.Fatal(err)
	}
	if outputTXID != "tx-one" || storedOutputAddress != outputAddress ||
		outputPosition != 0 || amount != 42 || !bytes.Equal(script, []byte{0x51}) ||
		reserved != 0 || txoType != 6 || !hasSource ||
		storedClaimID.String != claimID || storedClaimName.String != claimName ||
		storedChannelID.String != channelID || storedRepostID.String != repostedClaimID {
		t.Fatalf("stored output = txid %q address %q position %d amount %d script %x reserved %d type %d claim %#v/%#v source %v channel %#v repost %#v",
			outputTXID, storedOutputAddress, outputPosition, amount, script, reserved,
			txoType, storedClaimID, storedClaimName, hasSource, storedChannelID, storedRepostID)
	}
	assertTransactionTestHistory(t, database, "watched", "tx-one:9:tx-two:0:", 2)

	var defaultReserved, defaultType int64
	var defaultClaim, defaultName, defaultChannel, defaultRepost sql.NullString
	var defaultSource bool
	if err := database.sql.QueryRow(`SELECT is_reserved, txo_type, claim_id,
        claim_name, has_source, channel_id, reposted_claim_id
        FROM txo WHERE txoid = 'tx-two:0'`).Scan(
		&defaultReserved, &defaultType, &defaultClaim, &defaultName,
		&defaultSource, &defaultChannel, &defaultRepost,
	); err != nil {
		t.Fatal(err)
	}
	if defaultReserved != 0 || defaultType != 0 || defaultSource ||
		defaultClaim.Valid || defaultName.Valid || defaultChannel.Valid || defaultRepost.Valid {
		t.Fatalf("default output = reserved %d type %d source %v claim %#v name %#v channel %#v repost %#v",
			defaultReserved, defaultType, defaultSource, defaultClaim, defaultName,
			defaultChannel, defaultRepost)
	}
}

func TestSaveTransactionIOBatchReplacesTransactionsAndIgnoresDuplicateIO(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	addTransactionTestAddress(t, database, "watched")

	oldPurchase := "old-purchase"
	oldDay := 2460000.5
	oldAddress := "old-address"
	oldClaim := "old-claim"
	first := []TransactionIORow{{
		Transaction: TransactionRow{
			TXID: "same", Raw: []byte{0x01}, Height: 1, Position: 2,
			IsVerified: true, PurchasedClaimID: &oldPurchase, Day: &oldDay,
		},
		Inputs: []TransactionInputRow{{TXOID: "spent:0", Position: 1}},
		Outputs: []TransactionOutputRow{{
			TXOID: "same:0", Address: &oldAddress, Amount: 10,
			Script: []byte{0x01}, TXOType: 1, ClaimID: &oldClaim, HasSource: true,
		}},
	}}
	if err := database.SaveTransactionIOBatch(ctx, first, "watched", "same:1:"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, database.sql, "UPDATE txo SET is_reserved = 1 WHERE txoid = 'same:0'")

	newAddress := "new-address"
	newClaim := "new-claim"
	second := []TransactionIORow{{
		Transaction: TransactionRow{
			TXID: "same", Raw: []byte{0x02}, Height: 20, Position: 8,
		},
		Inputs: []TransactionInputRow{{TXOID: "spent:0", Position: 9}},
		Outputs: []TransactionOutputRow{{
			TXOID: "same:0", Address: &newAddress, Position: 4, Amount: 99,
			Script: []byte{0x02}, TXOType: 6, ClaimID: &newClaim,
		}, {
			TXOID: "same:1", Address: &newAddress, Position: 1, Amount: 5,
			Script: []byte{0x03},
		}},
	}}
	if err := database.SaveTransactionIOBatch(ctx, second, "watched", "same:20:"); err != nil {
		t.Fatal(err)
	}

	var raw []byte
	var height, position int64
	var verified bool
	var purchase sql.NullString
	var day sql.NullFloat64
	if err := database.sql.QueryRow(`SELECT raw, height, position, is_verified,
        purchased_claim_id, day FROM tx WHERE txid = 'same'`).Scan(
		&raw, &height, &position, &verified, &purchase, &day,
	); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, []byte{0x02}) || height != 20 || position != 8 ||
		verified || purchase.Valid || day.Valid {
		t.Fatalf("replaced transaction = raw %x height %d position %d verified %v purchase %#v day %#v",
			raw, height, position, verified, purchase, day)
	}

	var inputAddress string
	var inputPosition int64
	if err := database.sql.QueryRow(
		"SELECT address, position FROM txi WHERE txoid = 'spent:0'",
	).Scan(&inputAddress, &inputPosition); err != nil {
		t.Fatal(err)
	}
	if inputAddress != "watched" || inputPosition != 1 {
		t.Fatalf("duplicate input changed to address %q position %d", inputAddress, inputPosition)
	}

	var outputAddress string
	var outputPosition, amount, reserved, txoType int64
	var script []byte
	var claim sql.NullString
	var hasSource bool
	if err := database.sql.QueryRow(`SELECT address, position, amount, script,
        is_reserved, txo_type, claim_id, has_source FROM txo WHERE txoid = 'same:0'`).Scan(
		&outputAddress, &outputPosition, &amount, &script, &reserved, &txoType,
		&claim, &hasSource,
	); err != nil {
		t.Fatal(err)
	}
	if outputAddress != oldAddress || outputPosition != 0 || amount != 10 ||
		!bytes.Equal(script, []byte{0x01}) || reserved != 1 || txoType != 1 ||
		!claim.Valid || claim.String != oldClaim || !hasSource {
		t.Fatalf("duplicate output changed = address %q position %d amount %d script %x reserved %d type %d claim %#v source %v",
			outputAddress, outputPosition, amount, script, reserved, txoType, claim, hasSource)
	}
	if got := queryInt(t, database.sql, "SELECT is_reserved FROM txo WHERE txoid = 'same:1'"); got != 0 {
		t.Fatalf("new output reservation = %d, want 0", got)
	}
	assertTransactionTestHistory(t, database, "watched", "same:20:", 1)
}

func TestSaveTransactionIOBatchRollsBackOnLaterRowFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	addTransactionTestAddress(t, database, "watched")
	if err := database.SetAddressHistory(ctx, "watched", "old:1:"); err != nil {
		t.Fatal(err)
	}
	rows := []TransactionIORow{{
		Transaction: TransactionRow{TXID: "first", Raw: []byte{0x01}},
		Inputs:      []TransactionInputRow{{TXOID: "spent:0"}},
	}, {
		Transaction: TransactionRow{TXID: "invalid", Raw: nil},
	}}
	if err := database.SaveTransactionIOBatch(ctx, rows, "watched", "new:2:"); err == nil {
		t.Fatal("batch with NULL transaction raw unexpectedly succeeded")
	}
	assertEmptyTransactionTables(t, database)
	assertTransactionTestHistory(t, database, "watched", "old:1:", 1)
}

func TestSaveTransactionIOBatchRollsBackWhenFinalHistoryUpdateFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	addTransactionTestAddress(t, database, "watched")
	mustExec(t, database.sql, `CREATE TRIGGER reject_transaction_history
        BEFORE UPDATE ON pubkey_address
        BEGIN SELECT RAISE(ABORT, 'rejected'); END`)
	outputAddress := "destination"
	rows := []TransactionIORow{{
		Transaction: TransactionRow{TXID: "tx", Raw: []byte{0x01}},
		Inputs:      []TransactionInputRow{{TXOID: "spent:0"}},
		Outputs: []TransactionOutputRow{{
			TXOID: "tx:0", Address: &outputAddress, Script: []byte{0x51},
		}},
	}}
	if err := database.SaveTransactionIOBatch(ctx, rows, "watched", "tx:1:"); err == nil {
		t.Fatal("batch with rejected history update unexpectedly succeeded")
	}
	assertEmptyTransactionTables(t, database)
	var history sql.NullString
	var usedTimes int64
	if err := database.sql.QueryRow(`SELECT history, used_times
        FROM pubkey_address WHERE address = 'watched'`).Scan(&history, &usedTimes); err != nil {
		t.Fatal(err)
	}
	if history.Valid || usedTimes != 0 {
		t.Fatalf("rolled-back address state = history %#v used_times %d", history, usedTimes)
	}
}

func TestSaveTransactionIOBatchUnknownAddressAndEmptyBatchMatchPinnedBehavior(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	addTransactionTestAddress(t, database, "watched")

	rows := []TransactionIORow{{
		Transaction: TransactionRow{TXID: "unknown-address-tx", Raw: []byte{0x01}},
	}}
	if err := database.SaveTransactionIOBatch(
		ctx, rows, "unknown", "unknown-address-tx:1:",
	); err != nil {
		t.Fatalf("unknown address save = %v", err)
	}
	if got := queryInt(t, database.sql, "SELECT COUNT(*) FROM tx"); got != 1 {
		t.Fatalf("unknown address transaction rows = %d, want 1", got)
	}
	if got := queryInt(t, database.sql, "SELECT COUNT(*) FROM pubkey_address WHERE address = 'unknown'"); got != 0 {
		t.Fatalf("unknown address rows = %d, want 0", got)
	}

	if err := database.SaveTransactionIOBatch(
		ctx, nil, "watched", "one:1:two:-1:odd",
	); err != nil {
		t.Fatalf("empty batch = %v", err)
	}
	assertTransactionTestHistory(t, database, "watched", "one:1:two:-1:odd", 2)
	if got := queryInt(t, database.sql, "SELECT COUNT(*) FROM tx"); got != 1 {
		t.Fatalf("empty batch transaction rows = %d, want 1", got)
	}
	if err := database.SaveTransactionIOBatch(ctx, nil, "watched", ""); err != nil {
		t.Fatalf("empty history = %v", err)
	}
	assertTransactionTestHistory(t, database, "watched", "", 0)
	if err := database.SaveTransactionIOBatch(ctx, nil, "missing", ""); err != nil {
		t.Fatalf("empty batch for missing address = %v", err)
	}
}

func TestSaveTransactionIOEmptyBatchOnlyTouchesAddressTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	addTransactionTestAddress(t, database, "watched")
	// A current-version Python database is accepted without schema repair. An
	// operation only references the tables needed by the projected rows.
	mustExec(t, database.sql, "DROP TABLE txi")
	mustExec(t, database.sql, "DROP TABLE txo")
	if err := database.SaveTransactionIOBatch(ctx, []TransactionIORow{{
		Transaction: TransactionRow{TXID: "tx", Raw: []byte{0x01}},
	}}, "watched", "tx:1:"); err != nil {
		t.Fatalf("transaction-only batch with absent I/O tables = %v", err)
	}
	assertTransactionTestHistory(t, database, "watched", "tx:1:", 1)

	// An empty batch does not reference even the transaction table.
	mustExec(t, database.sql, "DROP TABLE tx")
	if err := database.SaveTransactionIOBatch(ctx, nil, "watched", "other:2:"); err != nil {
		t.Fatalf("empty batch with absent transaction tables = %v", err)
	}
	assertTransactionTestHistory(t, database, "watched", "other:2:", 1)
}

func addTransactionTestAddress(t *testing.T, database *DB, address string) {
	t.Helper()
	if err := database.AddKeys(context.Background(), "account", []AddressKey{{
		Address: address, PublicKey: []byte{0x01}, ChainCode: []byte{0x02},
	}}); err != nil {
		t.Fatal(err)
	}
}

func assertTransactionTestHistory(
	t *testing.T, database *DB, address, history string, usedTimes int64,
) {
	t.Helper()
	var storedHistory sql.NullString
	var storedUsedTimes int64
	if err := database.sql.QueryRow(
		"SELECT history, used_times FROM pubkey_address WHERE address = ?", address,
	).Scan(&storedHistory, &storedUsedTimes); err != nil {
		t.Fatal(err)
	}
	if !storedHistory.Valid || storedHistory.String != history || storedUsedTimes != usedTimes {
		t.Fatalf("address state = history %#v used_times %d, want %q and %d",
			storedHistory, storedUsedTimes, history, usedTimes)
	}
}

func assertEmptyTransactionTables(t *testing.T, database *DB) {
	t.Helper()
	for _, table := range []string{"tx", "txi", "txo"} {
		if got := queryInt(t, database.sql, "SELECT COUNT(*) FROM "+table); got != 0 {
			t.Fatalf("%s rows after rollback = %d, want 0", table, got)
		}
	}
}

package ledgerdb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestGetTransactionPreservesStoredValuesAndMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())

	mustExec(t, database.sql, `
        INSERT INTO tx
        (txid, raw, height, position, is_verified, purchased_claim_id, day)
        VALUES ('stored', x'0102', 17, 4, 1, 'purchase', 2461232.25),
               ('empty', x'', -2, -1, 0, NULL, NULL)`)

	stored, err := database.GetTransaction(ctx, "stored")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.TXID != "stored" ||
		!bytes.Equal(stored.Raw, []byte{0x01, 0x02}) ||
		stored.Height != 17 || stored.Position != 4 || !stored.IsVerified ||
		stored.PurchasedClaimID == nil || *stored.PurchasedClaimID != "purchase" ||
		stored.Day == nil || *stored.Day != 2461232.25 {
		t.Fatalf("stored transaction = %#v", stored)
	}
	stored.Raw[0] = 0xff
	again, err := database.GetTransaction(ctx, "stored")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.Raw, []byte{0x01, 0x02}) {
		t.Fatalf("second raw read = %x, want 0102", again.Raw)
	}

	empty, err := database.GetTransaction(ctx, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || empty.Raw == nil || len(empty.Raw) != 0 ||
		empty.PurchasedClaimID != nil || empty.Day != nil {
		t.Fatalf("empty transaction = %#v", empty)
	}

	missing, err := database.GetTransaction(ctx, "missing")
	if err != nil || missing != nil {
		t.Fatalf("missing transaction = %#v, %v", missing, err)
	}
}

func TestGetOutputsByIDPreservesNullableMetadataAndCollapsesRequests(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())

	mustExec(t, database.sql, `INSERT INTO tx
        (txid, raw, height, position, is_verified) VALUES
        ('a', x'00', 1, 2, 1), ('z', x'01', 3, 4, 0)`)
	mustExec(t, database.sql, `INSERT INTO txo
        (txid, txoid, address, position, amount, script, is_reserved,
         txo_type, claim_id, claim_name, has_source, channel_id,
         reposted_claim_id) VALUES
        ('a', 'a:0', NULL, 0, 11, x'', 0, 0, NULL, NULL, NULL, NULL, NULL),
        ('z', 'z:1', 'destination', 1, 22, x'5152', 1, 6,
         'claim', 'name', 0, 'channel', 'repost')`)

	requested := make([]string, 0, transactionReadVariableLimit+5)
	for index := 0; index < transactionReadVariableLimit+2; index++ {
		requested = append(requested, fmt.Sprintf("missing:%04d", index))
	}
	requested = append(requested, "z:1", "a:0", "z:1")
	outputs, err := database.GetOutputsByID(ctx, requested)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 2 {
		t.Fatalf("output count = %d, want 2", len(outputs))
	}
	a := outputs["a:0"]
	if a.TXID != "a" || a.TXOID != "a:0" || a.Address != nil ||
		a.Position != 0 || a.Amount != 11 || a.Script == nil || len(a.Script) != 0 ||
		a.IsReserved || a.TXOType != 0 || a.ClaimID != nil || a.ClaimName != nil ||
		a.HasSource != nil || a.ChannelID != nil || a.RepostedClaimID != nil {
		t.Fatalf("nullable output = %#v", a)
	}
	z := outputs["z:1"]
	if z.TXID != "z" || z.Address == nil || *z.Address != "destination" ||
		z.Position != 1 || z.Amount != 22 || !bytes.Equal(z.Script, []byte{0x51, 0x52}) ||
		!z.IsReserved || z.TXOType != 6 || z.ClaimID == nil || *z.ClaimID != "claim" ||
		z.ClaimName == nil || *z.ClaimName != "name" || z.HasSource == nil || *z.HasSource ||
		z.ChannelID == nil || *z.ChannelID != "channel" ||
		z.RepostedClaimID == nil || *z.RepostedClaimID != "repost" {
		t.Fatalf("annotated output = %#v", z)
	}
	z.Script[0] = 0xff
	again, err := database.GetOutputsByID(ctx, []string{"z:1"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again["z:1"].Script, []byte{0x51, 0x52}) {
		t.Fatalf("second script read = %x, want 5152", again["z:1"].Script)
	}
}

func TestTransactionReadsPreserveNullBlobsInAcceptedCurrentSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), Filename)
	raw := openRawTestDB(t, path)
	mustExec(t, raw, "CREATE TABLE version (version TEXT)")
	mustExec(t, raw, "INSERT INTO version VALUES (?)", SchemaVersion)
	mustExec(t, raw, `CREATE TABLE tx (
        txid TEXT PRIMARY KEY, raw BLOB, height INTEGER, position INTEGER,
        is_verified BOOLEAN, purchased_claim_id TEXT, day INTEGER)`)
	mustExec(t, raw, `CREATE TABLE txo (
        txid TEXT, txoid TEXT PRIMARY KEY, address TEXT, position INTEGER,
        amount INTEGER, script BLOB, is_reserved BOOLEAN, txo_type INTEGER,
        claim_id TEXT, claim_name TEXT, has_source BOOL, channel_id TEXT,
        reposted_claim_id TEXT)`)
	mustExec(t, raw, `INSERT INTO tx VALUES
        ('null', NULL, 0, -1, 0, NULL, 2460000.75)`)
	mustExec(t, raw, `INSERT INTO txo VALUES
        ('null', 'null:0', NULL, 0, 1, NULL, 0, 0, NULL, NULL, NULL, NULL, NULL)`)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	transaction, err := database.GetTransaction(ctx, "null")
	if err != nil {
		t.Fatal(err)
	}
	if transaction == nil || transaction.Raw != nil ||
		transaction.Day == nil || *transaction.Day != 2460000.75 {
		t.Fatalf("NULL-blob transaction = %#v", transaction)
	}
	outputs, err := database.GetOutputsByID(ctx, []string{"null:0"})
	if err != nil {
		t.Fatal(err)
	}
	if output := outputs["null:0"]; output.Script != nil {
		t.Fatalf("NULL script = %x, want nil", output.Script)
	}
}

func TestTransactionReadsEmptyAndNotOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := database.GetOutputsByID(ctx, nil)
	if err != nil || outputs == nil || len(outputs) != 0 {
		t.Fatalf("empty output lookup = %#v, %v", outputs, err)
	}
	if err := database.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetTransaction(ctx, "tx"); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("closed transaction lookup = %v, want ErrNotOpen", err)
	}
	if _, err := database.GetOutputsByID(ctx, nil); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("closed empty output lookup = %v, want ErrNotOpen", err)
	}
	var nilDatabase *DB
	if _, err := nilDatabase.GetTransaction(ctx, "tx"); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("nil transaction lookup = %v, want ErrNotOpen", err)
	}
	if _, err := nilDatabase.GetOutputsByID(ctx, nil); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("nil output lookup = %v, want ErrNotOpen", err)
	}
}

package database

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lbry/daemon/wallet"
	"lbry/daemon/wallet/keys"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestResolvedClaimStoreOpenPreservesExistingDatabaseAndOwnsLifecycle(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	path := SQLitePath(dataDir)
	connection, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(`
        CREATE TABLE existing_marker (value text);
        INSERT INTO existing_marker VALUES ('kept');`); err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}

	store := NewResolvedClaimStore(dataDir)
	if got := store.Path(); got != path {
		t.Fatalf("Path() = %q, want %q", got, path)
	}
	if err := store.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Open(ctx); !errors.Is(err, ErrResolvedClaimStoreAlreadyOpen) {
		t.Fatalf("second Open() error = %v", err)
	}

	var marker string
	if err := store.db.QueryRow("SELECT value FROM existing_marker").Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != "kept" {
		t.Fatalf("existing marker = %q", marker)
	}
	for _, table := range []string{
		"blob", "stream", "stream_blob", "claim", "torrent", "torrent_node",
		"torrent_tracker", "torrent_http_seed", "file", "content_claim", "support",
		"reflected_stream", "peer",
	} {
		var found string
		if err := store.db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&found); err != nil {
			t.Fatalf("table %q: %v", table, err)
		}
	}
	var indexName string
	if err := store.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name='blob_data'",
	).Scan(&indexName); err != nil {
		t.Fatalf("blob_data index: %v", err)
	}
	var foreignKeys int
	if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("idempotent Close() = %v", err)
	}
	if err := store.SaveResolvedClaims(ctx, nil, nil); !errors.Is(err, ErrResolvedClaimStoreNotOpen) {
		t.Fatalf("save after close = %v", err)
	}
	if err := (*ResolvedClaimStore)(nil).Close(); err != nil {
		t.Fatalf("nil Close() = %v", err)
	}
}

func TestResolvedClaimRowFromOutputMatchesLegacyFields(t *testing.T) {
	ledger := resolvedClaimTestLedger(t)
	sourceHash := bytes.Repeat([]byte{0x36}, 48)
	message := resolvedClaimTestMessage(sourceHash, "Fixture")
	channelHash := make([]byte, 20)
	for index := range channelHash {
		channelHash[index] = byte(index + 1)
	}
	payload := []byte{1}
	payload = append(payload, channelHash...)
	payload = append(payload, bytes.Repeat([]byte{0x7a}, 64)...)
	payload = append(payload, message...)
	output := resolvedClaimTestOutput(t, 123_456_789, "MiXeD", payload, 321)

	row, err := resolvedClaimRowFromOutput(ledger, output)
	if err != nil {
		t.Fatal(err)
	}
	claimID, err := output.ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	address, err := output.Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	reversedChannel := append([]byte(nil), channelHash...)
	for left, right := 0, len(reversedChannel)-1; left < right; left, right = left+1, right-1 {
		reversedChannel[left], reversedChannel[right] = reversedChannel[right], reversedChannel[left]
	}
	if row.Outpoint != output.ID() || row.ClaimID != claimID || row.Name != "MiXeD" ||
		row.Amount != 123_456_789 || row.Height != int64(321) || row.Address != address ||
		row.ClaimSequence != -1 || row.ValueType != "stream" ||
		row.SourceHash != hex.EncodeToString(sourceHash) ||
		row.ChannelClaimID == nil || *row.ChannelClaimID != hex.EncodeToString(reversedChannel) {
		t.Fatalf("resolved claim row = %#v", row)
	}
	if got, want := row.SerializedMetadata, []byte(hex.EncodeToString(payload)); !bytes.Equal(got, want) {
		t.Fatalf("serialized metadata = %q, want %q", got, want)
	}
}

func TestResolvedClaimRowStoresCanonicalLegacyClaimBytes(t *testing.T) {
	ledger := resolvedClaimTestLedger(t)
	payload := []byte(`{"sources":{"lbry_sd_hash":"00ff"},"title":"Legacy"}`)
	decoded, err := wallet.DecodeLegacyV0ClaimValue(payload)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := decoded.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	output := resolvedClaimTestOutput(t, 1, "legacy", payload, 9)
	row, err := resolvedClaimRowFromOutput(ledger, output)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(canonical, payload) {
		t.Fatal("legacy fixture unexpectedly has identical input and canonical bytes")
	}
	if got, want := row.SerializedMetadata, []byte(hex.EncodeToString(canonical)); !bytes.Equal(got, want) {
		t.Fatalf("serialized metadata = %q, want converted canonical %q", got, want)
	}
	if row.SourceHash != "00ff" || row.ValueType != "stream" {
		t.Fatalf("legacy row = %#v", row)
	}
}

func TestResolvedClaimStoreSavesAndReplacesClaimRow(t *testing.T) {
	ctx := context.Background()
	store := openResolvedClaimTestStore(t)
	ledger := resolvedClaimTestLedger(t)
	payload := append([]byte{0}, resolvedClaimTestMessage(nil, "First")...)
	output := resolvedClaimTestOutput(t, 100_000_000, "first", payload, 42)
	if err := store.SaveResolvedClaims(ctx, ledger, []*wallet.TransactionOutput{output}); err != nil {
		t.Fatal(err)
	}

	assertStoredResolvedClaim(t, store.db, output, payload, 100_000_000, 42)
	output.Amount = 250_000_000
	if err := store.SaveResolvedClaims(ctx, ledger, []*wallet.TransactionOutput{output}); err != nil {
		t.Fatal(err)
	}
	assertStoredResolvedClaim(t, store.db, output, payload, 250_000_000, 42)
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM claim").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("claim count = %d, want 1", count)
	}
}

func TestResolvedClaimStoreEmptyOutputsStillSucceeds(t *testing.T) {
	store := openResolvedClaimTestStore(t)
	if err := store.SaveResolvedClaims(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM claim").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("claim count = %d, want 0", count)
	}
}

func TestResolvedClaimStoreMalformedClaimIsNamedDecodeErrorAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	store := openResolvedClaimTestStore(t)
	ledger := resolvedClaimTestLedger(t)
	valid := resolvedClaimTestOutput(
		t, 1, "valid", append([]byte{0}, resolvedClaimTestMessage(nil, "Valid")...), 5,
	)
	malformed := resolvedClaimTestOutput(t, 2, "bad", []byte{0, 0x80}, 6)
	err := store.SaveResolvedClaims(ctx, ledger, []*wallet.TransactionOutput{valid, malformed})
	var named interface{ PythonErrorName() string }
	if !errors.As(err, &named) || named.PythonErrorName() != "DecodeError" {
		t.Fatalf("error = %T %v, want named DecodeError", err, err)
	}
	var count int
	if queryErr := store.db.QueryRow("SELECT COUNT(*) FROM claim").Scan(&count); queryErr != nil {
		t.Fatal(queryErr)
	}
	if count != 0 {
		t.Fatalf("claim count = %d after decode failure, want 0", count)
	}
}

func TestResolvedClaimStoreUpdatesDownloadedStreamContentClaim(t *testing.T) {
	ctx := context.Background()
	store := openResolvedClaimTestStore(t)
	ledger := resolvedClaimTestLedger(t)
	sourceHash := bytes.Repeat([]byte{0x41}, 48)
	sourceHashHex := hex.EncodeToString(sourceHash)
	seedResolvedClaimStream(t, store.db, "stream-one", sourceHashHex)

	payload := append([]byte{0}, resolvedClaimTestMessage(sourceHash, "Create")...)
	created := resolvedClaimTestOutput(t, 10, "stream", payload, 10)
	if err := store.SaveResolvedClaims(ctx, ledger, []*wallet.TransactionOutput{created}); err != nil {
		t.Fatal(err)
	}
	assertResolvedContentClaim(t, store.db, "stream-one", created.ID())
	claimID, err := created.ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	updatedValue, err := wallet.NewUpdateClaimOutput(
		20, "stream", claimID, payload, bytes.Repeat([]byte{0x52}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	updated := resolvedClaimAttachOutput(updatedValue, 11)
	if err := store.SaveResolvedClaims(ctx, ledger, []*wallet.TransactionOutput{updated}); err != nil {
		t.Fatal(err)
	}
	assertResolvedContentClaim(t, store.db, "stream-one", updated.ID())
}

func TestResolvedClaimStoreRollsBackBatchOnContentClaimMismatch(t *testing.T) {
	ctx := context.Background()
	store := openResolvedClaimTestStore(t)
	ledger := resolvedClaimTestLedger(t)
	sourceHash := bytes.Repeat([]byte{0x61}, 48)
	sourceHashHex := hex.EncodeToString(sourceHash)
	seedResolvedClaimStream(t, store.db, "stream-conflict", sourceHashHex)

	const oldOutpoint = "old:0"
	oldClaimID := strings.Repeat("a", 40)
	oldPayload := append([]byte{0}, resolvedClaimTestMessage(sourceHash, "Old")...)
	if _, err := store.db.Exec(resolvedClaimInsertSQL,
		oldOutpoint, oldClaimID, "old", 1, 1,
		[]byte(hex.EncodeToString(oldPayload)), nil, "address", -1,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		"INSERT INTO content_claim VALUES (?, NULL, ?)", "stream-conflict", oldOutpoint,
	); err != nil {
		t.Fatal(err)
	}

	unrelated := resolvedClaimTestOutput(
		t, 2, "unrelated", append([]byte{0}, resolvedClaimTestMessage(nil, "Other")...), 2,
	)
	conflicting := resolvedClaimTestOutput(
		t, 3, "conflicting", append([]byte{0}, resolvedClaimTestMessage(sourceHash, "New")...), 3,
	)
	newClaimID, err := conflicting.ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	if newClaimID == oldClaimID {
		t.Fatal("fixture claim IDs unexpectedly match")
	}
	err = store.SaveResolvedClaims(
		ctx, ledger, []*wallet.TransactionOutput{unrelated, conflicting},
	)
	if err == nil || !strings.Contains(err.Error(), "mismatching claim ids when updating stream") {
		t.Fatalf("save error = %v", err)
	}

	var count int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM claim WHERE claim_outpoint IN (?, ?)",
		unrelated.ID(), conflicting.ID(),
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("new rows after rollback = %d, want 0", count)
	}
	assertResolvedContentClaim(t, store.db, "stream-conflict", oldOutpoint)
}

func openResolvedClaimTestStore(t *testing.T) *ResolvedClaimStore {
	t.Helper()
	store := NewResolvedClaimStore(t.TempDir())
	if err := store.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close resolved claim store: %v", err)
		}
	})
	return store
}

func resolvedClaimTestLedger(t *testing.T) *wallet.Ledger {
	t.Helper()
	return &wallet.Ledger{
		Network: keys.MainNet,
		Headers: wallet.NewHeadersForNetwork(filepath.Join(t.TempDir(), "headers"), keys.MainNet),
	}
}

func resolvedClaimTestOutput(
	t *testing.T, amount uint64, name string, claim []byte, height int64,
) *wallet.TransactionOutput {
	t.Helper()
	return resolvedClaimAttachOutput(wallet.NewClaimNameOutput(
		amount, name, claim, bytes.Repeat([]byte{0x42}, 20),
	), height)
}

func resolvedClaimAttachOutput(
	output wallet.TransactionOutput, height int64,
) *wallet.TransactionOutput {
	transaction := wallet.NewTransaction()
	transaction.AddOutputs([]wallet.TransactionOutput{output})
	transaction.Height = height
	return &transaction.Outputs[0]
}

func resolvedClaimTestMessage(sourceHash []byte, title string) []byte {
	var stream []byte
	if sourceHash != nil {
		var source []byte
		source = protowire.AppendTag(source, 6, protowire.BytesType)
		source = protowire.AppendBytes(source, sourceHash)
		stream = protowire.AppendTag(stream, 1, protowire.BytesType)
		stream = protowire.AppendBytes(stream, source)
	}
	var claim []byte
	claim = protowire.AppendTag(claim, 1, protowire.BytesType)
	claim = protowire.AppendBytes(claim, stream)
	if title != "" {
		claim = protowire.AppendTag(claim, 8, protowire.BytesType)
		claim = protowire.AppendString(claim, title)
	}
	return claim
}

func assertStoredResolvedClaim(
	t *testing.T, connection *sql.DB, output *wallet.TransactionOutput,
	payload []byte, amount, height int64,
) {
	t.Helper()
	var got struct {
		Outpoint, ClaimID, Name, MetadataType, Address string
		Amount, Height, Sequence                       int64
		Metadata                                       []byte
		ChannelID                                      sql.NullString
	}
	err := connection.QueryRow(`
        SELECT claim_outpoint, claim_id, claim_name, amount, height,
               serialized_metadata, typeof(serialized_metadata),
               channel_claim_id, address, claim_sequence
        FROM claim WHERE claim_outpoint=?`, output.ID()).Scan(
		&got.Outpoint, &got.ClaimID, &got.Name, &got.Amount, &got.Height,
		&got.Metadata, &got.MetadataType, &got.ChannelID, &got.Address, &got.Sequence,
	)
	if err != nil {
		t.Fatal(err)
	}
	claimID, _ := output.ClaimID()
	address, _ := output.Address(keys.MainNet)
	wantMetadata := []byte(hex.EncodeToString(payload))
	if got.Outpoint != output.ID() || got.ClaimID != claimID || got.Name != "first" ||
		got.Amount != amount || got.Height != height || !bytes.Equal(got.Metadata, wantMetadata) ||
		got.MetadataType != "blob" || got.ChannelID.Valid || got.Address != address || got.Sequence != -1 {
		t.Fatalf("stored claim = %#v; metadata %q, want %q", got, got.Metadata, wantMetadata)
	}
}

func seedResolvedClaimStream(
	t *testing.T, connection *sql.DB, streamHash, sourceHash string,
) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO blob VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, []any{
			sourceHash, 0, 0, 0, "finished", nil, nil, 1, 0,
		}},
		{`INSERT INTO stream VALUES (?, ?, ?, ?, ?)`, []any{
			streamHash, sourceHash, "key", "name", "suggested",
		}},
		{`INSERT INTO file VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?)`, []any{
			streamHash, nil, nil, 0.0, "running", 0, nil, 1,
		}},
	}
	for _, statement := range statements {
		if _, err := connection.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed resolved stream with %q: %v", statement.query, err)
		}
	}
}

func assertResolvedContentClaim(
	t *testing.T, connection *sql.DB, streamHash, wantOutpoint string,
) {
	t.Helper()
	var gotStream string
	var torrent sql.NullString
	var gotOutpoint string
	if err := connection.QueryRow(`
        SELECT stream_hash, bt_infohash, claim_outpoint
        FROM content_claim WHERE stream_hash=?`, streamHash,
	).Scan(&gotStream, &torrent, &gotOutpoint); err != nil {
		t.Fatal(err)
	}
	if gotStream != streamHash || torrent.Valid || gotOutpoint != wantOutpoint {
		t.Fatalf("content claim = (%q, %#v, %q), want (%q, NULL, %q)",
			gotStream, torrent, gotOutpoint, streamHash, wantOutpoint)
	}
}

func TestResolvedClaimSchemaMatchesPinnedColumnOrder(t *testing.T) {
	store := openResolvedClaimTestStore(t)
	want := []string{
		"claim_outpoint", "claim_id", "claim_name", "amount", "height",
		"serialized_metadata", "channel_claim_id", "address", "claim_sequence",
	}
	rows, err := store.db.Query("PRAGMA table_info(claim)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claim columns = %v, want %v", got, want)
	}
}

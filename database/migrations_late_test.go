package database

import (
	"bytes"
	"context"
	"crypto/sha512"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"lbry/daemon/blob"

	_ "modernc.org/sqlite"
)

func TestMigrateRevision6To15StepRejectsUnsupportedRevisionWithoutOpeningDatabase(t *testing.T) {
	directory := t.TempDir()
	handled, err := migrateRevision6To15Step(
		context.Background(), directory, "", 5, nil,
	)
	if handled || err != nil {
		t.Fatalf("revision 5 dispatch = handled %t, error %v", handled, err)
	}
	if _, err := os.Stat(SQLitePath(directory)); !os.IsNotExist(err) {
		t.Fatalf("unsupported dispatch created database: %v", err)
	}
}

func TestMigrateRevision6Through15PreservesValidDataAndCleansInvalidStream(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	downloadDirectory := t.TempDir()
	fileName := "movie.mp4"
	if err := os.WriteFile(filepath.Join(downloadDirectory, fileName), []byte("movie"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := openLateMigrationFixture(t, directory)

	valid := lateMigrationDescriptor(t, 0x11)
	validSD := descriptorHash(t, valid)
	invalid := lateMigrationDescriptor(t, 0x31)
	invalidSD := hex.EncodeToString(bytes.Repeat([]byte{0xee}, 48))
	insertRevision6Stream(
		t, db, valid, validSD,
		hex.EncodeToString([]byte(fileName)), hex.EncodeToString([]byte(downloadDirectory)),
	)
	insertRevision6Stream(t, db, invalid, invalidSD, "{stream}", "{stream}")
	if _, err := db.Exec(`INSERT INTO content_claim VALUES (?, ?)`, invalid.StreamHash, "invalid:0"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	blobDirectory := filepath.Join(directory, "blobfiles")
	if err := os.MkdirAll(blobDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, info := range invalid.ContentBlobs() {
		if err := os.WriteFile(filepath.Join(blobDirectory, info.BlobHash), []byte("invalid"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	now := func() time.Time { return time.Unix(1234, 0) }
	for revision := 6; revision < CurrentRevision; revision++ {
		handled, err := migrateRevision6To15Step(
			ctx, directory, downloadDirectory, revision, now,
		)
		if err != nil || !handled {
			t.Fatalf("revision %d dispatch = handled %t, error %v", revision, handled, err)
		}
	}

	db = openLateMigrationDatabase(t, directory)
	defer db.Close()
	assertLateMigrationColumns(t, db, "blob", []string{
		"blob_hash", "blob_length", "next_announce_time", "should_announce", "status",
		"last_announced_time", "single_announce", "added_on", "is_mine",
	})
	assertLateMigrationColumns(t, db, "file", []string{
		"stream_hash", "bt_infohash", "file_name", "download_directory", "blob_data_rate",
		"status", "saved_file", "content_fee", "added_on",
	})
	for _, object := range []struct{ kind, name string }{
		{"table", "reflected_stream"},
		{"table", "torrent"},
		{"table", "torrent_node"},
		{"table", "torrent_tracker"},
		{"table", "torrent_http_seed"},
		{"table", "peer"},
		{"index", "blob_data"},
	} {
		var count int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?", object.kind, object.name,
		).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s %s count = %d, %v", object.kind, object.name, count, err)
		}
	}

	var nextAnnounce, shouldAnnounce, addedOn, isMine int64
	if err := db.QueryRow(`
        SELECT next_announce_time, should_announce, added_on, is_mine
        FROM blob WHERE blob_hash=?`, validSD,
	).Scan(&nextAnnounce, &shouldAnnounce, &addedOn, &isMine); err != nil {
		t.Fatal(err)
	}
	if nextAnnounce != 0 || shouldAnnounce != 1 || addedOn != 0 || isMine != 1 {
		t.Fatalf("migrated SD blob = next %d announce %d added %d mine %d",
			nextAnnounce, shouldAnnounce, addedOn, isMine)
	}
	var saved int
	if err := db.QueryRow(
		"SELECT saved_file, added_on FROM file WHERE stream_hash=?", valid.StreamHash,
	).Scan(&saved, &addedOn); err != nil {
		t.Fatal(err)
	}
	if saved != 1 || addedOn != 1234 {
		t.Fatalf("migrated file = saved %d, added_on %d", saved, addedOn)
	}
	for _, table := range []string{"stream", "file", "content_claim"} {
		var count int
		query := "SELECT COUNT(*) FROM " + table + " WHERE stream_hash=?"
		if err := db.QueryRow(query, invalid.StreamHash).Scan(&count); err != nil || count != 0 {
			t.Fatalf("invalid stream rows in %s = %d, %v", table, count, err)
		}
	}
	for _, info := range invalid.ContentBlobs() {
		if _, err := os.Stat(filepath.Join(blobDirectory, info.BlobHash)); !os.IsNotExist(err) {
			t.Fatalf("invalid content blob remains: %s, %v", info.BlobHash, err)
		}
	}
}

func TestMigrate10To11ClearsSentinelAndMalformedPaths(t *testing.T) {
	directory := t.TempDir()
	db := openLateMigrationFixture(t, directory)
	descriptor := lateMigrationDescriptor(t, 0x51)
	sdHash := descriptorHash(t, descriptor)
	insertRevision6Stream(t, db, descriptor, sdHash, "{stream}", "{stream}")
	other := lateMigrationDescriptor(t, 0x71)
	insertRevision6Stream(t, db, other, descriptorHash(t, other), "zz", "01")
	if _, err := db.Exec("ALTER TABLE blob ADD COLUMN last_announced_time INTEGER"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE blob ADD COLUMN single_announce INTEGER"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	handled, err := migrateRevision6To15Step(
		context.Background(), directory, "", 10, func() time.Time { return time.Unix(1, 0) },
	)
	if err != nil || !handled {
		t.Fatalf("revision 10 migration = handled %t, %v", handled, err)
	}
	db = openLateMigrationDatabase(t, directory)
	defer db.Close()
	rows, err := db.Query("SELECT file_name, download_directory, saved_file FROM file")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name, path sql.NullString
		var saved int
		if err := rows.Scan(&name, &path, &saved); err != nil {
			t.Fatal(err)
		}
		if name.Valid || path.Valid || saved != 0 {
			t.Fatalf("cleared file path = name %v, path %v, saved %d", name, path, saved)
		}
		count++
	}
	if err := rows.Err(); err != nil || count != 2 {
		t.Fatalf("cleared rows = %d, %v", count, err)
	}
}

func openLateMigrationFixture(t *testing.T, directory string) *sql.DB {
	t.Helper()
	db := openLateMigrationDatabase(t, directory)
	for _, statement := range []string{
		`CREATE TABLE blob (
            blob_hash char(96) primary key not null,
            blob_length integer not null,
            next_announce_time integer not null,
            should_announce integer not null default 0,
            status text not null)`,
		`CREATE TABLE stream (
            stream_hash char(96) not null primary key,
            sd_hash char(96) not null references blob,
            stream_key text not null,
            stream_name text not null,
            suggested_filename text not null)`,
		`CREATE TABLE stream_blob (
            stream_hash char(96) not null references stream,
            blob_hash char(96) references blob,
            position integer not null,
            iv char(32) not null,
            primary key (stream_hash, blob_hash))`,
		`CREATE TABLE claim (
            claim_outpoint text not null primary key,
            claim_id char(40) not null,
            claim_name text not null,
            amount integer not null,
            height integer not null,
            serialized_metadata blob not null,
            channel_claim_id text,
            address text not null,
            claim_sequence integer not null)`,
		`CREATE TABLE file (
            stream_hash text primary key not null references stream,
            file_name text not null,
            download_directory text not null,
            blob_data_rate real not null,
            status text not null)`,
		`CREATE TABLE content_claim (
            stream_hash text unique not null references file,
            claim_outpoint text not null references claim,
            primary key (stream_hash, claim_outpoint))`,
		`CREATE TABLE support (
            support_outpoint text not null primary key,
            claim_id text not null, amount integer not null, address text not null)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func openLateMigrationDatabase(t *testing.T, directory string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", SQLitePath(directory))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func lateMigrationDescriptor(t *testing.T, seed byte) *blob.StreamDescriptor {
	t.Helper()
	descriptor := &blob.StreamDescriptor{
		StreamName:        hex.EncodeToString([]byte("stream")),
		Key:               hex.EncodeToString(bytes.Repeat([]byte{seed}, 16)),
		SuggestedFileName: hex.EncodeToString([]byte("movie.mp4")),
		StreamType:        "lbryfile",
		Blobs: []blob.BlobInfo{
			{
				BlobHash: hex.EncodeToString(bytes.Repeat([]byte{seed + 1}, 48)),
				BlobNum:  0, IV: hex.EncodeToString(bytes.Repeat([]byte{seed + 2}, 16)), Length: 16,
			},
			{BlobNum: 1, IV: hex.EncodeToString(bytes.Repeat([]byte{seed + 3}, 16)), Length: 0},
		},
	}
	descriptor.StreamHash = blob.CalculateStreamHash(descriptor)
	return descriptor
}

func descriptorHash(t *testing.T, descriptor *blob.StreamDescriptor) string {
	t.Helper()
	encoded, err := blob.MarshalDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha512.Sum384(encoded)
	return hex.EncodeToString(digest[:])
}

func insertRevision6Stream(
	t *testing.T,
	db *sql.DB,
	descriptor *blob.StreamDescriptor,
	sdHash string,
	fileName string,
	downloadDirectory string,
) {
	t.Helper()
	if _, err := db.Exec(
		"INSERT INTO blob VALUES (?, ?, 100, 0, 'finished')", sdHash, 100,
	); err != nil {
		t.Fatal(err)
	}
	for _, info := range descriptor.ContentBlobs() {
		if _, err := db.Exec(
			"INSERT INTO blob VALUES (?, ?, 100, 0, 'finished')", info.BlobHash, info.Length,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(
		"INSERT INTO stream VALUES (?, ?, ?, ?, ?)",
		descriptor.StreamHash, sdHash, descriptor.Key,
		mustDecodeMigrationHex(t, descriptor.StreamName),
		mustDecodeMigrationHex(t, descriptor.SuggestedFileName),
	); err != nil {
		t.Fatal(err)
	}
	for _, info := range descriptor.Blobs {
		var hash any
		if info.BlobHash != "" {
			hash = info.BlobHash
		}
		if _, err := db.Exec(
			"INSERT INTO stream_blob VALUES (?, ?, ?, ?)",
			descriptor.StreamHash, hash, info.BlobNum, info.IV,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(
		"INSERT INTO file VALUES (?, ?, ?, 0.25, 'running')",
		descriptor.StreamHash, fileName, downloadDirectory,
	); err != nil {
		t.Fatal(err)
	}
}

func mustDecodeMigrationHex(t *testing.T, value string) string {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(decoded)
}

func assertLateMigrationColumns(t *testing.T, db *sql.DB, table string, want []string) {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var sequence, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&sequence, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s columns = %v, want %v", table, got, want)
	}
}

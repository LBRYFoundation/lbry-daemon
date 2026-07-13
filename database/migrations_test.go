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
)

func TestMigratorUpgradesRevision1FixtureThroughRevision15(t *testing.T) {
	directory := t.TempDir()
	downloadDirectory := t.TempDir()
	writeFixtureRevision(t, directory, "1")

	descriptor := legacyMigrationDescriptor(t)
	descriptorBytes, err := blob.MarshalDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	sdDigest := sha512.Sum384(descriptorBytes)
	sdHash := hex.EncodeToString(sdDigest[:])
	content := descriptor.ContentBlobs()[0]
	fileName, err := hex.DecodeString(descriptor.StreamName)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloadDirectory, string(fileName)), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	metadata := openMigrationFixtureDB(t, filepath.Join(directory, "blockchainname.db"))
	mustMigrationExec(t, metadata, `
CREATE TABLE name_metadata (name text, txid text, sd_hash text);
CREATE TABLE claim_ids (claimId text, name text, txid text);
CREATE TABLE claim_cache (
    claim_id text, claim_sequence integer, claim_address text,
    height integer, amount integer, claim_pb blob
);
INSERT INTO name_metadata VALUES ('fixture', 'txid', '`+sdHash+`');
INSERT INTO claim_ids VALUES ('claim-id', 'fixture', 'txid');
INSERT INTO claim_cache VALUES ('claim-id', 7, 'bAddress', 10, 25, X'000A00');`)
	if err := metadata.Close(); err != nil {
		t.Fatal(err)
	}

	files := openMigrationFixtureDB(t, filepath.Join(directory, "lbryfile_info.db"))
	mustMigrationExec(t, files, `
CREATE TABLE lbry_files (
    stream_hash text, key text, stream_name text, suggested_filename text
);
CREATE TABLE lbry_file_descriptors (sd_blob_hash text, stream_hash text);
CREATE TABLE lbry_file_blobs (
    blob_hash text, stream_hash text, position integer, iv text, length integer
);
CREATE TABLE lbry_file_options (stream_hash text, blob_data_rate real, status text);`)
	if _, err := files.Exec("INSERT INTO lbry_files VALUES (?, ?, ?, ?)",
		descriptor.StreamHash, descriptor.Key, string(fileName), string(fileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Exec("INSERT INTO lbry_file_descriptors VALUES (?, ?)", sdHash, descriptor.StreamHash); err != nil {
		t.Fatal(err)
	}
	for _, info := range descriptor.Blobs {
		var hash any
		if info.BlobHash != "" {
			hash = info.BlobHash
		}
		if _, err := files.Exec("INSERT INTO lbry_file_blobs VALUES (?, ?, ?, ?, ?)",
			hash, descriptor.StreamHash, info.BlobNum, info.IV, info.Length); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := files.Exec("INSERT INTO lbry_file_options VALUES (?, 0.25, 'running')", descriptor.StreamHash); err != nil {
		t.Fatal(err)
	}
	if err := files.Close(); err != nil {
		t.Fatal(err)
	}

	blobs := openMigrationFixtureDB(t, filepath.Join(directory, "blobs.db"))
	mustMigrationExec(t, blobs, `CREATE TABLE blobs (
        blob_hash text, blob_length integer, status text, next_announce_time integer
    )`)
	for _, row := range []struct {
		hash   string
		length int
	}{{sdHash, len(descriptorBytes)}, {content.BlobHash, content.Length}} {
		if _, err := blobs.Exec("INSERT INTO blobs VALUES (?, ?, 'finished', 99)", row.hash, row.length); err != nil {
			t.Fatal(err)
		}
	}
	if err := blobs.Close(); err != nil {
		t.Fatal(err)
	}

	migrator := NewMigrator(directory, downloadDirectory)
	migrator.Now = func() time.Time { return time.Unix(1234, 0) }
	migrator.Logf = func(string, ...any) {}
	result, err := EnsureRevision(directory, migrator.MigrationFunc)
	if err != nil {
		t.Fatal(err)
	}
	if result.PreviousRevision != 1 || !result.Migrated || readFixtureRevision(t, directory) != "15" {
		t.Fatalf("migration result = %#v, revision %q", result, readFixtureRevision(t, directory))
	}

	db := openMigrationFixtureDB(t, SQLitePath(directory))
	defer db.Close()
	var saved, addedOn, isMine int64
	var claimOutpoint string
	if err := db.QueryRow(`
SELECT file.saved_file, file.added_on, blob.is_mine, content_claim.claim_outpoint
FROM file JOIN stream USING (stream_hash)
JOIN blob ON blob.blob_hash=stream.sd_hash
JOIN content_claim USING (stream_hash)
WHERE file.stream_hash=?`, descriptor.StreamHash).Scan(&saved, &addedOn, &isMine, &claimOutpoint); err != nil {
		t.Fatal(err)
	}
	if saved != 0 || addedOn != 1234 || isMine != 1 || claimOutpoint != "txid:-1" {
		t.Fatalf("migrated file = saved %d added %d mine %d outpoint %q",
			saved, addedOn, isMine, claimOutpoint)
	}
	columns, err := migrationTableColumnNames(context.Background(), db, "blob")
	if err != nil {
		t.Fatal(err)
	}
	wantColumns := []string{
		"blob_hash", "blob_length", "next_announce_time", "should_announce", "status",
		"last_announced_time", "single_announce", "added_on", "is_mine",
	}
	if !reflect.DeepEqual(columns, wantColumns) {
		t.Fatalf("blob columns = %v, want %v", columns, wantColumns)
	}
}

func TestMigratorKeepsBackupAndRevisionOnFailure(t *testing.T) {
	directory := t.TempDir()
	writeFixtureRevision(t, directory, "14")
	if err := os.WriteFile(SQLitePath(directory), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	migrator := NewMigrator(directory, "")
	migrator.Logf = func(string, ...any) {}
	_, err := EnsureRevision(directory, migrator.MigrationFunc)
	if err == nil {
		t.Fatal("invalid database migration succeeded")
	}
	if got := readFixtureRevision(t, directory); got != "14" {
		t.Fatalf("failed migration revision = %q", got)
	}
	backup := filepath.Join(directory, "rev_14_unmigrated_database.sqlite")
	contents, readErr := os.ReadFile(backup)
	if readErr != nil || !bytes.Equal(contents, []byte("not sqlite")) {
		t.Fatalf("failure backup = %q, %v", contents, readErr)
	}
}

func legacyMigrationDescriptor(t *testing.T) *blob.StreamDescriptor {
	t.Helper()
	descriptor := &blob.StreamDescriptor{
		StreamName:        hex.EncodeToString([]byte("movie.mp4")),
		SuggestedFileName: hex.EncodeToString([]byte("movie.mp4")),
		Key:               hex.EncodeToString(bytes.Repeat([]byte{0x11}, 16)),
		StreamType:        "lbryfile",
		Blobs: []blob.BlobInfo{
			{
				BlobHash: hex.EncodeToString(bytes.Repeat([]byte{0x22}, 48)),
				BlobNum:  0, IV: hex.EncodeToString(bytes.Repeat([]byte{0x33}, 16)), Length: 16,
			},
			{BlobNum: 1, IV: hex.EncodeToString(bytes.Repeat([]byte{0x44}, 16)), Length: 0},
		},
	}
	descriptor.StreamHash = blob.CalculateStreamHash(descriptor)
	return descriptor
}

func openMigrationFixtureDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
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

func mustMigrationExec(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

package database

import (
	"bytes"
	"context"
	"crypto/sha512"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lbry/daemon/blob"
	"lbry/daemon/wallet"
)

func TestManagedStreamPersistenceRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openResolvedClaimTestStore(t)
	ledger := resolvedClaimTestLedger(t)
	sourceHash := bytes.Repeat([]byte{0x71}, 48)
	sdHash := hex.EncodeToString(sourceHash)
	descriptor := &blob.StreamDescriptor{
		Key:               "00112233445566778899aabbccddeeff",
		StreamName:        hex.EncodeToString([]byte("source-name")),
		SuggestedFileName: hex.EncodeToString([]byte("movie.mp4")),
		Blobs: []blob.BlobInfo{
			{BlobHash: hex.EncodeToString(bytes.Repeat([]byte{0x73}, 48)), BlobNum: 0, IV: "00112233445566778899aabbccddeeff", Length: 16},
			{BlobHash: hex.EncodeToString(bytes.Repeat([]byte{0x74}, 48)), BlobNum: 1, IV: "102132435465768798a9babbdcddedef", Length: 32},
			{BlobNum: 2, IV: "ffeeddccbbaa99887766554433221100", Length: 0},
		},
	}
	descriptor.StreamHash = blob.CalculateStreamHash(descriptor)
	streamHash := descriptor.StreamHash
	if err := store.SaveStreamDescriptor(ctx, sdHash, 321, descriptor, 100, true); err != nil {
		t.Fatal(err)
	}
	// Normal blob rows are idempotent. SQLite permits another terminator row
	// because NULL does not collide in the composite primary key, as in Python.
	if err := store.SaveStreamDescriptor(ctx, sdHash, 999, descriptor, 200, false); err != nil {
		t.Fatal(err)
	}

	directory := t.TempDir()
	name := "movie.mp4"
	if err := os.WriteFile(filepath.Join(directory, name), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	rowID, err := store.SaveManagedFile(
		ctx, streamHash, &name, &directory, 0.25, "running", []byte{0xaa, 0xbb}, 101,
	)
	if err != nil || rowID < 1 {
		t.Fatalf("save managed file = %d, %v", rowID, err)
	}

	payload := append([]byte{0}, resolvedClaimTestMessage(sourceHash, "Movie")...)
	claim := resolvedClaimTestOutput(t, 25, "movie", payload, 10)
	if err := store.SaveResolvedClaims(ctx, ledger, []*wallet.TransactionOutput{claim}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkStreamReflected(ctx, sdHash, "reflector:5566"); err != nil {
		t.Fatal(err)
	}

	files, err := store.ListManagedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("managed files = %d, want 1", len(files))
	}
	got := files[0]
	claimID, _ := claim.ClaimID()
	if got.RowID != rowID || got.StreamHash != streamHash || got.SDHash != sdHash ||
		got.FileName == nil || *got.FileName != name ||
		got.DownloadDirectory == nil || *got.DownloadDirectory != directory ||
		got.BlobDataRate != 0.25 || got.Status != "running" || !got.SavedFile ||
		got.ContentFeeHex == nil || *got.ContentFeeHex != "aabb" ||
		got.ClaimOutpoint != claim.ID() || got.ClaimID != claimID ||
		got.ClaimName != "movie" || got.ClaimAmount != 25 || got.ClaimHeight != 10 ||
		got.ChannelClaimID != nil || got.ChannelName != nil || !got.FullyReflected {
		t.Fatalf("managed file = %#v", got)
	}
	if countManagedRows(t, store, "blob") != 3 ||
		countManagedRows(t, store, "stream") != 1 ||
		countManagedRows(t, store, "stream_blob") != 4 {
		t.Fatalf("descriptor row counts = blob %d stream %d stream_blob %d",
			countManagedRows(t, store, "blob"), countManagedRows(t, store, "stream"),
			countManagedRows(t, store, "stream_blob"))
	}
	recovered, err := store.RecoverManagedDescriptor(ctx, streamHash)
	if err != nil || len(recovered.Blobs) != len(descriptor.Blobs) ||
		recovered.Blobs[len(recovered.Blobs)-1].BlobHash != "" {
		t.Fatalf("recovered descriptor = %#v, %v", recovered, err)
	}
	if err := store.ChangeManagedFileStatus(ctx, streamHash, "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := store.ChangeManagedFilePath(ctx, streamHash, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SetManagedFileSaved(ctx, streamHash, false); err != nil {
		t.Fatal(err)
	}
	mutated, err := store.ListManagedFiles(ctx)
	if err != nil || len(mutated) != 1 || mutated[0].Status != "stopped" ||
		mutated[0].FileName != nil || mutated[0].DownloadDirectory != nil || mutated[0].SavedFile {
		t.Fatalf("mutated managed file = %#v, %v", mutated, err)
	}
	deletedHashes, err := store.DeleteManagedStream(ctx, streamHash)
	if err != nil || len(deletedHashes) != 3 {
		t.Fatalf("deleted stream hashes = %v, %v", deletedHashes, err)
	}
	if countManagedRows(t, store, "blob") != 0 || countManagedRows(t, store, "stream") != 0 ||
		countManagedRows(t, store, "stream_blob") != 0 || countManagedRows(t, store, "file") != 0 ||
		countManagedRows(t, store, "content_claim") != 0 || countManagedRows(t, store, "claim") != 1 ||
		countManagedRows(t, store, "reflected_stream") != 1 {
		t.Fatal("managed stream deletion did not preserve the pinned row boundaries")
	}
}

func TestCleanManagedBlobsRemovesFinishedNetworkInventoryAtZeroLimit(t *testing.T) {
	ctx := context.Background()
	store := openResolvedClaimTestStore(t)
	manager := blob.NewManager()
	data := []byte("network")
	digest := sha512.Sum384(data)
	hash := hex.EncodeToString(digest[:])
	if _, err := store.db.ExecContext(ctx, `
        INSERT INTO blob VALUES (?, ?, 0, 0, 'finished', 0, 0, 1, 0)`,
		hash, 2*1024*1024,
	); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(hash, data, false); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.CleanManagedBlobs(ctx, manager, 0, 0)
	if err != nil || deleted != 1 || manager.Has(hash) || countManagedRows(t, store, "blob") != 0 {
		t.Fatalf("cleaned network blobs = %d, %v; cached=%t", deleted, err, manager.Has(hash))
	}
}

func TestCleanManagedBlobsDeletesPersistentFileBeforeDatabaseRow(t *testing.T) {
	ctx := context.Background()
	store := openResolvedClaimTestStore(t)
	data := []byte("persisted network blob")
	digest := sha512.Sum384(data)
	hash := hex.EncodeToString(digest[:])
	directory := t.TempDir()
	manager := blob.NewPersistentManager(directory)
	if _, err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(hash, data, false); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCompletedBlob(ctx, hash, len(data), 1, false); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.CleanManagedBlobs(ctx, manager, 0, -1)
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup = %d, %v", deleted, err)
	}
	if _, err := os.Stat(filepath.Join(directory, hash)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("persistent blob remained after cleanup: %v", err)
	}
	if countManagedRows(t, store, "blob") != 0 {
		t.Fatal("database blob row remained after cleanup")
	}
}

func TestCleanManagedBlobsUsesContentThenDescriptorCandidateOrder(t *testing.T) {
	ctx := context.Background()
	store := openResolvedClaimTestStore(t)
	manager := blob.NewManager()
	streamHash := hex.EncodeToString(bytes.Repeat([]byte{0xa1}, 48))
	sdData, contentData := []byte("sd"), []byte("content")
	sdDigest, contentDigest := sha512.Sum384(sdData), sha512.Sum384(contentData)
	sdHash := hex.EncodeToString(sdDigest[:])
	contentHash := hex.EncodeToString(contentDigest[:])
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO blob VALUES (?, ?, 0, 0, 'finished', 0, 0, 1, 0)`, []any{sdHash, 2 * 1024 * 1024}},
		{`INSERT INTO blob VALUES (?, ?, 0, 0, 'finished', 0, 0, 1, 0)`, []any{contentHash, 2 * 1024 * 1024}},
		{`INSERT INTO stream VALUES (?, ?, '', '', '')`, []any{streamHash, sdHash}},
		{`INSERT INTO stream_blob VALUES (?, ?, 0, '')`, []any{streamHash, contentHash}},
		{`INSERT INTO file VALUES (?, NULL, NULL, NULL, 0, 'running', 0, NULL, 1)`, []any{streamHash}},
	} {
		if _, err := store.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	_ = manager.Set(sdHash, sdData, true)
	_ = manager.Set(contentHash, contentData, false)
	deleted, err := store.CleanManagedBlobs(ctx, manager, -1, 100)
	if err != nil || deleted != 2 || manager.Has(contentHash) || manager.Has(sdHash) {
		t.Fatalf("content cleanup = %d, %v", deleted, err)
	}
	var status string
	if err := store.db.QueryRow("SELECT status FROM file WHERE stream_hash=?", streamHash).Scan(&status); err != nil || status != "stopped" {
		t.Fatalf("file status after cleanup = %q, %v", status, err)
	}
}

func TestSyncStoredBlobsRepairsMissingAndUpsertsDiskInventory(t *testing.T) {
	ctx := context.Background()
	store := openResolvedClaimTestStore(t)
	missing := hex.EncodeToString(bytes.Repeat([]byte{0xc1}, 48))
	present := hex.EncodeToString(bytes.Repeat([]byte{0xc2}, 48))
	orphan := hex.EncodeToString(bytes.Repeat([]byte{0xc3}, 48))
	for _, row := range []struct {
		hash, status string
		length       int64
	}{{missing, "finished", 10}, {present, "pending", 20}} {
		if _, err := store.db.ExecContext(ctx, `
            INSERT INTO blob VALUES (?, ?, 0, 0, ?, 0, 0, 1, 0)`,
			row.hash, row.length, row.status,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SyncStoredBlobs(ctx, map[string]int64{present: 21, orphan: 30}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		hash, status string
		length       int64
	}{{missing, "pending", 10}, {present, "finished", 21}, {orphan, "finished", 30}} {
		var status string
		var length int64
		if err := store.db.QueryRow(
			"SELECT status, blob_length FROM blob WHERE blob_hash=?", want.hash,
		).Scan(&status, &length); err != nil || status != want.status || length != want.length {
			t.Fatalf("blob %s = %q/%d, %v; want %q/%d", want.hash[:8], status, length, err, want.status, want.length)
		}
	}
}

func TestStoredBlobDiskUsageMatchesPinnedBuckets(t *testing.T) {
	ctx := context.Background()
	store := openResolvedClaimTestStore(t)
	streamHash := hex.EncodeToString(bytes.Repeat([]byte{0xd1}, 48))
	sdHash := hex.EncodeToString(bytes.Repeat([]byte{0xd2}, 48))
	publicHash := hex.EncodeToString(bytes.Repeat([]byte{0xd3}, 48))
	privateHash := hex.EncodeToString(bytes.Repeat([]byte{0xd4}, 48))
	networkHash := hex.EncodeToString(bytes.Repeat([]byte{0xd5}, 48))
	pendingHash := hex.EncodeToString(bytes.Repeat([]byte{0xd6}, 48))
	for _, row := range []struct {
		hash, status string
		length, mine int64
	}{
		{sdHash, "finished", 1, 0}, {publicHash, "finished", 3, 0},
		{privateHash, "finished", 4, 1}, {networkHash, "finished", 2, 0},
		{pendingHash, "pending", 100, 0},
	} {
		if _, err := store.db.ExecContext(ctx, `
            INSERT INTO blob VALUES (?, ?, 0, 0, ?, 0, 0, 1, ?)`,
			row.hash, row.length, row.status, row.mine,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO stream VALUES (?, ?, '', '', '')", streamHash, sdHash,
	); err != nil {
		t.Fatal(err)
	}
	for position, hash := range []string{publicHash, privateHash} {
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO stream_blob VALUES (?, ?, ?, '')", streamHash, hash, position,
		); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := store.StoredBlobDiskUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Total != 9 || usage.Network != 2 || usage.Content != 3 || usage.Private != 4 {
		t.Fatalf("blob usage = %+v", usage)
	}
}

func TestBlobAnnouncementQueueAndSuccessfulSchedule(t *testing.T) {
	ctx := context.Background()
	store := openResolvedClaimTestStore(t)
	hashes := []string{
		hex.EncodeToString(bytes.Repeat([]byte{0xb1}, 48)),
		hex.EncodeToString(bytes.Repeat([]byte{0xb2}, 48)),
	}
	for index, hash := range hashes {
		if _, err := store.db.ExecContext(ctx, `
            INSERT INTO blob VALUES (?, 10, 0, ?, 'finished', NULL, 0, ?, 0)`,
			hash, 1-index, index,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.QueueBlobAnnouncements(ctx, []string{hashes[1]}, true); err != nil {
		t.Fatal(err)
	}
	eligible, err := store.BlobsToAnnounce(ctx, true, 10, time.Now().Unix()+1)
	if err != nil || len(eligible) != 2 || eligible[0] != hashes[0] || eligible[1] != hashes[1] {
		t.Fatalf("eligible announcements = %v, %v", eligible, err)
	}
	if err := store.MarkBlobsAnnounced(ctx, hashes, 1000); err != nil {
		t.Fatal(err)
	}
	var next, last, single int64
	if err := store.db.QueryRow(`SELECT next_announce_time, last_announced_time, single_announce
        FROM blob WHERE blob_hash=?`, hashes[1]).Scan(&next, &last, &single); err != nil {
		t.Fatal(err)
	}
	if next != 44200 || last != 1000 || single != 0 {
		t.Fatalf("announcement schedule = next %d last %d single %d", next, last, single)
	}
}

func TestSaveManagedFileValidatesPairedPathAndLifecycle(t *testing.T) {
	store := NewResolvedClaimStore(t.TempDir())
	name := "name"
	if _, err := store.SaveManagedFile(
		context.Background(), "stream", &name, nil, 0, "running", nil, 1,
	); err == nil {
		t.Fatal("unpaired file path was accepted")
	}
	if _, err := store.SaveManagedFile(
		context.Background(), "stream", nil, nil, 0, "running", nil, 1,
	); err != ErrResolvedClaimStoreNotOpen {
		t.Fatalf("closed store error = %v", err)
	}
}

func TestManagedFileStartupReconciliationAndRecoveryFinalization(t *testing.T) {
	ctx := context.Background()
	store := openResolvedClaimTestStore(t)
	streamHash := hex.EncodeToString(bytes.Repeat([]byte{0xe1}, 48))
	sdHash := hex.EncodeToString(bytes.Repeat([]byte{0xe2}, 48))
	if _, err := store.db.ExecContext(ctx, `
        INSERT INTO blob VALUES (?, 10, 0, 0, 'pending', 0, 0, 1, 0)`, sdHash); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO stream VALUES (?, ?, '', '', ?)", streamHash, sdHash, hex.EncodeToString([]byte("suggested.mp4")),
	); err != nil {
		t.Fatal(err)
	}
	missingDirectory := t.TempDir()
	missingName := "removed.mp4"
	if _, err := store.db.ExecContext(ctx, `
        INSERT INTO file VALUES (?, NULL, ?, ?, 0, 'finished', 1, NULL, 1)`,
		streamHash, hex.EncodeToString([]byte(missingName)), hex.EncodeToString([]byte(missingDirectory)),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileManagedFilePaths(ctx); err != nil {
		t.Fatal(err)
	}
	var name, directory sql.NullString
	var status string
	var saved int
	if err := store.db.QueryRow(`
        SELECT file_name, download_directory, status, saved_file FROM file WHERE stream_hash=?`,
		streamHash,
	).Scan(&name, &directory, &status, &saved); err != nil {
		t.Fatal(err)
	}
	if name.Valid || directory.Valid || status != "finished" || saved != 0 {
		t.Fatalf("reconciled file = name %v dir %v status %q saved %d", name, directory, status, saved)
	}

	recoveryDirectory := t.TempDir()
	if err := store.FinalizeManagedDescriptorRecovery(
		ctx, streamHash, hex.EncodeToString([]byte("suggested.mp4")), recoveryDirectory,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`
        SELECT file_name, download_directory, status, saved_file FROM file WHERE stream_hash=?`,
		streamHash,
	).Scan(&name, &directory, &status, &saved); err != nil {
		t.Fatal(err)
	}
	if !name.Valid || name.String != hex.EncodeToString([]byte("suggested.mp4")) ||
		!directory.Valid || directory.String != hex.EncodeToString([]byte(recoveryDirectory)) ||
		status != "stopped" || saved != 0 {
		t.Fatalf("recovered file = name %v dir %v status %q saved %d", name, directory, status, saved)
	}
	if err := store.CompleteManagedFileSave(ctx, streamHash); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(
		"SELECT status, saved_file FROM file WHERE stream_hash=?", streamHash,
	).Scan(&status, &saved); err != nil || status != "finished" || saved != 1 {
		t.Fatalf("completed file = status %q saved %d, %v", status, saved, err)
	}
}

func countManagedRows(t *testing.T, store *ResolvedClaimStore, table string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

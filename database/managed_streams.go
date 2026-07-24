package database

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lbry/daemon/blob"
	"lbry/daemon/wallet"
)

type ManagedFileRow struct {
	RowID                 int64
	AddedOn               int64
	StreamHash            string
	FileName              *string
	DownloadDirectory     *string
	BlobDataRate          float64
	Status                string
	SavedFile             bool
	ContentFeeHex         *string
	SDHash                string
	Key                   string
	StreamName            string
	SuggestedFileName     string
	ClaimOutpoint         string
	ClaimID               string
	ClaimName             string
	ClaimAmount           int64
	ClaimHeight           int64
	SerializedMetadataHex string
	ChannelClaimID        *string
	Address               string
	ClaimSequence         int64
	ChannelName           *string
	FullyReflected        bool
	BlobsCompleted        int64
	BlobsInStream         int64
	TotalBytesLowerBound  int64
	TotalBytesUpperBound  int64
}

type BlobDiskUsage struct {
	Total   int64
	Network int64
	Content int64
	Private int64
}

func (store *ResolvedClaimStore) SyncStoredBlobs(ctx context.Context, files map[string]int64) error {
	if store == nil || ctx == nil {
		return errors.New("stored blob synchronization is unavailable")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return ErrResolvedClaimStoreNotOpen
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(syncErr error) error { return errors.Join(syncErr, transaction.Rollback()) }
	rows, err := transaction.QueryContext(ctx, "SELECT blob_hash FROM blob WHERE status='finished'")
	if err != nil {
		return rollback(err)
	}
	var missing []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			rows.Close()
			return rollback(err)
		}
		if _, exists := files[hash]; !exists {
			missing = append(missing, hash)
		}
	}
	if err := rows.Close(); err != nil {
		return rollback(err)
	}
	for _, hash := range missing {
		if _, err := transaction.ExecContext(ctx,
			"UPDATE blob SET status='pending' WHERE blob_hash=?", hash,
		); err != nil {
			return rollback(err)
		}
	}
	now := time.Now().Unix()
	for hash, length := range files {
		if _, err := transaction.ExecContext(ctx, `
            INSERT OR IGNORE INTO blob VALUES (?, ?, 0, 0, 'finished', 0, 0, ?, 0)`,
			hash, length, now,
		); err != nil {
			return rollback(err)
		}
		if _, err := transaction.ExecContext(ctx,
			"UPDATE blob SET status='finished', blob_length=? WHERE blob_hash=?", length, hash,
		); err != nil {
			return rollback(err)
		}
	}
	return transaction.Commit()
}

func (store *ResolvedClaimStore) RecordCompletedBlob(
	ctx context.Context, hash string, length int, addedOn int64, isMine bool,
) error {
	if store == nil || ctx == nil {
		return errors.New("completed blob store is unavailable")
	}
	if addedOn == 0 {
		addedOn = time.Now().Unix()
	}
	mine := 0
	if isMine {
		mine = 1
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return ErrResolvedClaimStoreNotOpen
	}
	if _, err := store.db.ExecContext(ctx, `
        INSERT OR IGNORE INTO blob VALUES (?, ?, 0, 0, 'finished', 0, 0, ?, ?)`,
		hash, length, addedOn, mine,
	); err != nil {
		return err
	}
	_, err := store.db.ExecContext(ctx,
		"UPDATE blob SET status='finished', blob_length=? WHERE blob_hash=?", length, hash,
	)
	return err
}

func (store *ResolvedClaimStore) StoredBlobDiskUsage(ctx context.Context) (BlobDiskUsage, error) {
	if store == nil || ctx == nil {
		return BlobDiskUsage{}, errors.New("stored blob usage is unavailable")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return BlobDiskUsage{}, ErrResolvedClaimStoreNotOpen
	}
	var usage BlobDiskUsage
	err := store.db.QueryRowContext(ctx, `
        SELECT COALESCE(SUM(blob_length), 0),
               COALESCE(SUM(CASE WHEN stream_blob.stream_hash IS NULL THEN blob_length ELSE 0 END), 0),
               COALESCE(SUM(CASE WHEN stream_blob.blob_hash IS NOT NULL AND is_mine=0 THEN blob_length ELSE 0 END), 0),
               COALESCE(SUM(CASE WHEN is_mine=1 THEN blob_length ELSE 0 END), 0)
        FROM blob LEFT JOIN stream_blob USING (blob_hash)
        WHERE blob_hash NOT IN (SELECT sd_hash FROM stream) AND blob.status='finished'`).Scan(
		&usage.Total, &usage.Network, &usage.Content, &usage.Private,
	)
	return usage, err
}

func (store *ResolvedClaimStore) StopAllManagedFiles(ctx context.Context) error {
	return store.updateManagedFile(ctx, "UPDATE file SET status='stopped'")
}

func (store *ResolvedClaimStore) ReconcileManagedFilePaths(ctx context.Context) error {
	if store == nil || ctx == nil {
		return errors.New("managed file reconciliation is unavailable")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return ErrResolvedClaimStoreNotOpen
	}
	rows, err := store.db.QueryContext(ctx, `
        SELECT stream_hash, download_directory, file_name FROM file
        WHERE saved_file=1 AND stream_hash IS NOT NULL`)
	if err != nil {
		return err
	}
	var removed []string
	for rows.Next() {
		var streamHash string
		var encodedDirectory, encodedName sql.NullString
		if err := rows.Scan(&streamHash, &encodedDirectory, &encodedName); err != nil {
			rows.Close()
			return err
		}
		name, nameErr := decodeManagedPath(encodedName)
		directory, directoryErr := decodeManagedPath(encodedDirectory)
		if nameErr != nil || directoryErr != nil {
			rows.Close()
			return errors.Join(nameErr, directoryErr)
		}
		if name == nil || directory == nil {
			continue
		}
		if info, statErr := os.Stat(filepath.Join(*directory, *name)); statErr != nil || !info.Mode().IsRegular() {
			removed = append(removed, streamHash)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, streamHash := range removed {
		if _, err := store.db.ExecContext(ctx, `
            UPDATE file SET file_name=NULL, download_directory=NULL, saved_file=0
            WHERE stream_hash=?`, streamHash); err != nil {
			return err
		}
	}
	return nil
}

func (store *ResolvedClaimStore) RecoverManagedDescriptor(
	ctx context.Context, streamHash string,
) (*blob.StreamDescriptor, error) {
	if store == nil || ctx == nil {
		return nil, errors.New("managed descriptor recovery is unavailable")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return nil, ErrResolvedClaimStoreNotOpen
	}
	descriptor := &blob.StreamDescriptor{StreamHash: streamHash, StreamType: "lbryfile"}
	if err := store.db.QueryRowContext(ctx, `
        SELECT stream_key, stream_name, suggested_filename FROM stream WHERE stream_hash=?`,
		streamHash,
	).Scan(&descriptor.Key, &descriptor.StreamName, &descriptor.SuggestedFileName); err != nil {
		return nil, err
	}
	rows, err := store.db.QueryContext(ctx, `
        SELECT stream_blob.blob_hash, stream_blob.position, stream_blob.iv,
               COALESCE(blob.blob_length, 0)
        FROM stream_blob LEFT JOIN blob ON blob.blob_hash=stream_blob.blob_hash
        WHERE stream_blob.stream_hash=? ORDER BY stream_blob.position ASC LIMIT ?`,
		streamHash, blob.MaxStreamDescriptorBlobs+1,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hash sql.NullString
		var info blob.BlobInfo
		if err := rows.Scan(&hash, &info.BlobNum, &info.IV, &info.Length); err != nil {
			return nil, err
		}
		if hash.Valid {
			info.BlobHash = hash.String
		}
		descriptor.Blobs = append(descriptor.Blobs, info)
		if len(descriptor.Blobs) > blob.MaxStreamDescriptorBlobs {
			return nil, fmt.Errorf(
				"managed descriptor exceeds the resource limit of %d blobs",
				blob.MaxStreamDescriptorBlobs,
			)
		}
		if !hash.Valid {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(descriptor.Blobs) == 0 {
		return nil, errors.New("managed descriptor has no blob rows")
	}
	return descriptor, nil
}

func (store *ResolvedClaimStore) FinalizeManagedDescriptorRecovery(
	ctx context.Context, streamHash, suggestedFileName, downloadDirectory string,
) error {
	if decoded, err := hex.DecodeString(suggestedFileName); err == nil {
		suggestedFileName = string(decoded)
	}
	name := filepath.Base(suggestedFileName)
	saved := 0
	if info, err := os.Stat(filepath.Join(downloadDirectory, name)); err == nil && info.Mode().IsRegular() {
		saved = 1
	}
	return store.updateManagedFile(ctx, `
        UPDATE file SET file_name=?, download_directory=?, status='stopped', saved_file=?, added_on=?
        WHERE stream_hash=?`,
		hex.EncodeToString([]byte(name)), hex.EncodeToString([]byte(downloadDirectory)),
		saved, time.Now().Unix(), streamHash,
	)
}

func (store *ResolvedClaimStore) SaveStreamDescriptor(
	ctx context.Context,
	sdHash string,
	sdLength int,
	descriptor *blob.StreamDescriptor,
	addedOn int64,
	isMine bool,
) error {
	if store == nil {
		return errors.New("resolved claim store is nil")
	}
	if ctx == nil {
		return errors.New("managed stream context is nil")
	}
	if descriptor == nil {
		return errors.New("stream descriptor is nil")
	}
	if !blob.ValidHash(sdHash) {
		return fmt.Errorf("stream descriptor hash %q is invalid", sdHash)
	}
	if sdLength <= 0 || sdLength > blob.MaxStreamDescriptorSize {
		return fmt.Errorf(
			"stream descriptor size %d exceeds the resource limit of %d bytes",
			sdLength, blob.MaxStreamDescriptorSize,
		)
	}
	if err := blob.ValidateDescriptor(descriptor); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return ErrResolvedClaimStoreNotOpen
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(saveErr error) error {
		return errors.Join(saveErr, transaction.Rollback())
	}
	mine := 0
	if isMine {
		mine = 1
	}
	insertBlob := func(hash string, length int) error {
		_, insertErr := transaction.ExecContext(ctx, `
            INSERT OR IGNORE INTO blob VALUES (?, ?, 0, 0, 'pending', 0, 0, ?, ?)`,
			hash, length, addedOn, mine,
		)
		return insertErr
	}
	for _, blobInfo := range descriptor.Blobs {
		if blobInfo.BlobHash == "" {
			continue
		}
		if err := insertBlob(blobInfo.BlobHash, blobInfo.Length); err != nil {
			return rollback(err)
		}
	}
	if err := insertBlob(sdHash, sdLength); err != nil {
		return rollback(err)
	}
	if _, err := transaction.ExecContext(ctx, `
        INSERT OR IGNORE INTO stream VALUES (?, ?, ?, ?, ?)`,
		descriptor.StreamHash, sdHash, descriptor.Key,
		descriptor.StreamName, descriptor.SuggestedFileName,
	); err != nil {
		return rollback(err)
	}
	for _, blobInfo := range descriptor.Blobs {
		var hash any
		if blobInfo.BlobHash != "" {
			hash = blobInfo.BlobHash
		}
		if _, err := transaction.ExecContext(ctx, `
            INSERT OR IGNORE INTO stream_blob VALUES (?, ?, ?, ?)`,
			descriptor.StreamHash, hash, blobInfo.BlobNum, blobInfo.IV,
		); err != nil {
			return rollback(err)
		}
	}
	if _, err := transaction.ExecContext(ctx,
		"UPDATE blob SET should_announce=1 WHERE blob_hash=?", sdHash,
	); err != nil {
		return rollback(err)
	}
	return transaction.Commit()
}

func (store *ResolvedClaimStore) SaveManagedFile(
	ctx context.Context,
	streamHash string,
	fileName, downloadDirectory *string,
	dataRate float64,
	status string,
	contentFee []byte,
	addedOn int64,
) (int64, error) {
	if store == nil {
		return 0, errors.New("resolved claim store is nil")
	}
	if ctx == nil {
		return 0, errors.New("managed file context is nil")
	}
	if (fileName == nil) != (downloadDirectory == nil) {
		return 0, errors.New("file name and download directory must both be set or unset")
	}
	if addedOn == 0 {
		addedOn = time.Now().Unix()
	}
	var encodedName, encodedDirectory, encodedFee any
	saved := 0
	if fileName != nil {
		encodedName = hex.EncodeToString([]byte(*fileName))
		encodedDirectory = hex.EncodeToString([]byte(*downloadDirectory))
		if info, err := os.Stat(filepath.Join(*downloadDirectory, *fileName)); err == nil && !info.IsDir() {
			saved = 1
		}
	}
	if len(contentFee) > 0 {
		encodedFee = hex.EncodeToString(contentFee)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return 0, ErrResolvedClaimStoreNotOpen
	}
	if _, err := store.db.ExecContext(ctx, `
        INSERT OR REPLACE INTO file VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?)`,
		streamHash, encodedName, encodedDirectory, dataRate, status,
		saved, encodedFee, addedOn,
	); err != nil {
		return 0, err
	}
	var rowID int64
	if err := store.db.QueryRowContext(ctx,
		"SELECT rowid FROM file WHERE stream_hash=?", streamHash,
	).Scan(&rowID); err != nil {
		return 0, err
	}
	return rowID, nil
}

func (store *ResolvedClaimStore) ListManagedFiles(ctx context.Context) ([]ManagedFileRow, error) {
	if store == nil {
		return nil, errors.New("resolved claim store is nil")
	}
	if ctx == nil {
		return nil, errors.New("managed file context is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return nil, ErrResolvedClaimStoreNotOpen
	}
	rows, err := store.db.QueryContext(ctx, `
        SELECT file.rowid, file.added_on, file.stream_hash,
            file.file_name, file.download_directory, file.blob_data_rate,
            file.status, file.saved_file, file.content_fee,
            stream.sd_hash, stream.stream_key, stream.stream_name,
            stream.suggested_filename, claim.claim_outpoint, claim.claim_id,
            claim.claim_name, claim.amount, claim.height,
            claim.serialized_metadata, claim.channel_claim_id, claim.address,
            claim.claim_sequence,
            (SELECT signing.claim_name FROM claim AS signing
                WHERE signing.claim_id=claim.channel_claim_id LIMIT 1),
            EXISTS(SELECT 1 FROM reflected_stream
                WHERE reflected_stream.sd_hash=stream.sd_hash),
            (SELECT COUNT(*) FROM stream_blob
                WHERE stream_blob.stream_hash=stream.stream_hash
                    AND stream_blob.blob_hash IS NOT NULL),
            (SELECT COUNT(*) FROM stream_blob
                INNER JOIN blob ON blob.blob_hash=stream_blob.blob_hash
                WHERE stream_blob.stream_hash=stream.stream_hash
                    AND blob.status='finished'),
            (SELECT COALESCE(SUM(blob.blob_length), 0) - COUNT(*) - 15
                FROM stream_blob INNER JOIN blob ON blob.blob_hash=stream_blob.blob_hash
                WHERE stream_blob.stream_hash=stream.stream_hash),
            (SELECT COALESCE(SUM(blob.blob_length), 0) - COUNT(*) + 1
                FROM stream_blob INNER JOIN blob ON blob.blob_hash=stream_blob.blob_hash
                WHERE stream_blob.stream_hash=stream.stream_hash)
        FROM file
        INNER JOIN stream ON file.stream_hash=stream.stream_hash
        INNER JOIN content_claim ON file.stream_hash=content_claim.stream_hash
        INNER JOIN claim ON content_claim.claim_outpoint=claim.claim_outpoint
        ORDER BY claim.rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ManagedFileRow, 0)
	for rows.Next() {
		var row ManagedFileRow
		var encodedName, encodedDirectory, contentFee, channelID, channelName sql.NullString
		var saved, reflected int64
		var serialized []byte
		if err := rows.Scan(
			&row.RowID, &row.AddedOn, &row.StreamHash,
			&encodedName, &encodedDirectory, &row.BlobDataRate,
			&row.Status, &saved, &contentFee, &row.SDHash, &row.Key,
			&row.StreamName, &row.SuggestedFileName, &row.ClaimOutpoint,
			&row.ClaimID, &row.ClaimName, &row.ClaimAmount, &row.ClaimHeight,
			&serialized, &channelID, &row.Address, &row.ClaimSequence,
			&channelName, &reflected,
			&row.BlobsInStream, &row.BlobsCompleted,
			&row.TotalBytesLowerBound, &row.TotalBytesUpperBound,
		); err != nil {
			return nil, err
		}
		row.FileName, err = decodeManagedPath(encodedName)
		if err != nil {
			return nil, fmt.Errorf("decode managed file name: %w", err)
		}
		row.DownloadDirectory, err = decodeManagedPath(encodedDirectory)
		if err != nil {
			return nil, fmt.Errorf("decode managed download directory: %w", err)
		}
		row.SavedFile = saved != 0
		row.ContentFeeHex = nullableManagedString(contentFee)
		row.SerializedMetadataHex = string(serialized)
		row.ChannelClaimID = nullableManagedString(channelID)
		row.ChannelName = nullableManagedString(channelName)
		row.FullyReflected = reflected != 0
		streamName, decodeErr := decodeManagedHexText(row.StreamName)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode managed stream name: %w", decodeErr)
		}
		row.StreamName = streamName
		suggestedName, decodeErr := decodeManagedHexText(row.SuggestedFileName)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode managed suggested file name: %w", decodeErr)
		}
		row.SuggestedFileName = suggestedName
		result = append(result, row)
	}
	return result, rows.Err()
}

func decodeManagedHexText(value string) (string, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func decodeManagedPath(value sql.NullString) (*string, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	decoded, err := hex.DecodeString(value.String)
	if err != nil {
		return nil, err
	}
	text := string(decoded)
	return &text, nil
}

func nullableManagedString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

func (store *ResolvedClaimStore) ChangeManagedFileStatus(
	ctx context.Context, streamHash, status string,
) error {
	return store.updateManagedFile(ctx,
		"UPDATE file SET status=? WHERE stream_hash=?", status, streamHash,
	)
}

func (store *ResolvedClaimStore) ChangeManagedFilePath(
	ctx context.Context, streamHash string, fileName, downloadDirectory *string,
) error {
	if (fileName == nil) != (downloadDirectory == nil) {
		return errors.New("file name and download directory must both be set or unset")
	}
	var encodedName, encodedDirectory any
	if fileName != nil {
		encodedName = hex.EncodeToString([]byte(*fileName))
		encodedDirectory = hex.EncodeToString([]byte(*downloadDirectory))
	}
	return store.updateManagedFile(ctx, `
        UPDATE file SET download_directory=?, file_name=? WHERE stream_hash=?`,
		encodedDirectory, encodedName, streamHash,
	)
}

func (store *ResolvedClaimStore) SetManagedFileSaved(
	ctx context.Context, streamHash string, saved bool,
) error {
	value := 0
	if saved {
		value = 1
	}
	return store.updateManagedFile(ctx,
		"UPDATE file SET saved_file=? WHERE stream_hash=?", value, streamHash,
	)
}

func (store *ResolvedClaimStore) CompleteManagedFileSave(ctx context.Context, streamHash string) error {
	return store.updateManagedFile(ctx, `
        UPDATE file SET saved_file=1, status='finished' WHERE stream_hash=?`, streamHash,
	)
}

func (store *ResolvedClaimStore) MarkManagedBlobsFinished(
	ctx context.Context, blobHashes []string,
) error {
	if len(blobHashes) == 0 {
		return nil
	}
	if store == nil {
		return errors.New("resolved claim store is nil")
	}
	if ctx == nil {
		return errors.New("managed blob context is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return ErrResolvedClaimStoreNotOpen
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, blobHash := range blobHashes {
		if _, err := transaction.ExecContext(ctx,
			"UPDATE blob SET status='finished' WHERE blob_hash=?", blobHash,
		); err != nil {
			_ = transaction.Rollback()
			return err
		}
	}
	return transaction.Commit()
}

func (store *ResolvedClaimStore) updateManagedFile(
	ctx context.Context, statement string, arguments ...any,
) error {
	if store == nil {
		return errors.New("resolved claim store is nil")
	}
	if ctx == nil {
		return errors.New("managed file context is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return ErrResolvedClaimStoreNotOpen
	}
	_, err := store.db.ExecContext(ctx, statement, arguments...)
	return err
}

func (store *ResolvedClaimStore) MarkStreamReflected(
	ctx context.Context, sdHash, reflectorAddress string,
) error {
	return store.updateManagedFile(ctx, `
        INSERT OR REPLACE INTO reflected_stream VALUES (?, ?, ?)`,
		sdHash, reflectorAddress, time.Now().Unix(),
	)
}

func (store *ResolvedClaimStore) QueueBlobAnnouncements(
	ctx context.Context, blobHashes []string, immediate bool,
) error {
	now := time.Now().Unix()
	for _, hash := range blobHashes {
		if immediate {
			if err := store.updateManagedFile(ctx, `
                UPDATE blob SET single_announce=1, next_announce_time=?
                WHERE blob_hash=? AND status='finished'`, now, hash); err != nil {
				return err
			}
		} else if err := store.updateManagedFile(ctx, `
            UPDATE blob SET single_announce=1 WHERE blob_hash=? AND status='finished'`, hash); err != nil {
			return err
		}
	}
	return nil
}

func (store *ResolvedClaimStore) BlobsToAnnounce(
	ctx context.Context, headOnly bool, limit int, now int64,
) ([]string, error) {
	condition := ""
	if headOnly {
		condition = "AND (should_announce=1 OR single_announce=1)"
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return nil, ErrResolvedClaimStoreNotOpen
	}
	rows, err := store.db.QueryContext(ctx, `SELECT blob_hash FROM blob
        WHERE blob_hash IS NOT NULL `+condition+`
          AND next_announce_time < ? AND status='finished'
        ORDER BY next_announce_time ASC LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		result = append(result, hash)
	}
	return result, rows.Err()
}

func (store *ResolvedClaimStore) MarkBlobsAnnounced(
	ctx context.Context, blobHashes []string, now int64,
) error {
	for _, hash := range blobHashes {
		if err := store.updateManagedFile(ctx, `UPDATE blob
            SET next_announce_time=?, last_announced_time=?, single_announce=0
            WHERE blob_hash=?`, now+43200, now, hash); err != nil {
			return err
		}
	}
	return nil
}

// CleanManagedBlobs applies the revision-15 DiskSpaceManager candidate order.
func (store *ResolvedClaimStore) CleanManagedBlobs(
	ctx context.Context, manager *blob.BlobManager, contentLimitMB, networkLimitMB int64,
) (int, error) {
	if store == nil || manager == nil {
		return 0, errors.New("managed blob cleaner is unavailable")
	}
	if ctx == nil {
		return 0, errors.New("managed blob context is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return 0, ErrResolvedClaimStoreNotOpen
	}
	deleted := make([]string, 0)
	for _, cleanup := range []struct {
		network bool
		limit   int64
	}{
		{network: false, limit: contentLimitMB},
		{network: true, limit: networkLimitMB},
	} {
		if !cleanup.network && cleanup.limit == 0 {
			continue
		}
		usage, err := storedBlobUsage(store.db, cleanup.network)
		if err != nil {
			return len(deleted), err
		}
		available := cleanup.limit - usage/(1024*1024)
		if cleanup.network && available >= 0 {
			continue
		}
		candidates, err := storedBlobCandidates(store.db, cleanup.network)
		if err != nil {
			return len(deleted), err
		}
		for _, candidate := range candidates {
			deleted = append(deleted, candidate.hash)
			available += candidate.length / (1024 * 1024)
			if available >= 0 {
				break
			}
		}
	}
	connection, err := store.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return 0, err
	}
	defer func() { _, _ = connection.ExecContext(context.Background(), "PRAGMA foreign_keys=ON") }()
	if len(deleted) > 0 {
		if _, err := connection.ExecContext(ctx, "UPDATE file SET status='stopped'"); err != nil {
			return 0, err
		}
		if err := manager.Delete(deleted...); err != nil {
			return 0, err
		}
	}
	for _, hash := range deleted {
		if _, err := connection.ExecContext(ctx, "DELETE FROM blob WHERE blob_hash=?", hash); err != nil {
			return 0, err
		}
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		return 0, err
	}
	return len(deleted), nil
}

type storedBlobCandidate struct {
	hash   string
	length int64
}

func storedBlobUsage(db *sql.DB, network bool) (int64, error) {
	condition := "stream_blob.blob_hash IS NOT NULL"
	if network {
		condition = "stream_blob.stream_hash IS NULL"
	}
	var usage int64
	err := db.QueryRow(`SELECT COALESCE(SUM(blob_length), 0) FROM blob
        LEFT JOIN stream_blob USING (blob_hash)
        WHERE blob_hash NOT IN (SELECT sd_hash FROM stream)
          AND blob.status='finished' AND ` + condition).Scan(&usage)
	return usage, err
}

func storedBlobCandidates(db *sql.DB, network bool) ([]storedBlobCandidate, error) {
	query := `SELECT blob.blob_hash, blob.blob_length FROM blob
        LEFT JOIN stream_blob USING (blob_hash)
        WHERE stream_blob.stream_hash IS NULL AND blob.is_mine=0 AND blob.status='finished'
        ORDER BY blob.blob_length DESC, blob.added_on ASC`
	if !network {
		query = `SELECT blob.blob_hash, blob.blob_length FROM blob
            JOIN stream_blob USING (blob_hash) JOIN stream USING (stream_hash) JOIN file USING (stream_hash)
            WHERE blob.is_mine=0 AND blob.status='finished'
            ORDER BY blob.added_on ASC, blob.blob_length ASC`
	}
	result, err := queryStoredBlobCandidates(db, query)
	if err != nil {
		return nil, err
	}
	if !network {
		descriptors, descriptorErr := queryStoredBlobCandidates(db, `
            SELECT blob.blob_hash, blob.blob_length FROM blob
            JOIN stream ON blob.blob_hash=stream.sd_hash JOIN file USING (stream_hash)
            WHERE blob.is_mine=0 ORDER BY blob.added_on ASC`)
		if descriptorErr != nil {
			return nil, descriptorErr
		}
		result = append(result, descriptors...)
	}
	return result, nil
}

func queryStoredBlobCandidates(db *sql.DB, query string) ([]storedBlobCandidate, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]storedBlobCandidate, 0)
	for rows.Next() {
		var candidate storedBlobCandidate
		if err := rows.Scan(&candidate.hash, &candidate.length); err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}

// DeleteManagedStream removes one stream and its file/content linkage while
// retaining claim rows, matching SQLiteStorage.delete_stream.
func (store *ResolvedClaimStore) DeleteManagedStream(
	ctx context.Context, streamHash string,
) ([]string, error) {
	if store == nil {
		return nil, errors.New("resolved claim store is nil")
	}
	if ctx == nil {
		return nil, errors.New("managed stream context is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return nil, ErrResolvedClaimStoreNotOpen
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	rollback := func(deleteErr error) ([]string, error) {
		return nil, errors.Join(deleteErr, transaction.Rollback())
	}
	rows, err := transaction.QueryContext(ctx, `
        SELECT blob_hash FROM stream_blob
            WHERE stream_hash=? AND blob_hash IS NOT NULL
        UNION SELECT sd_hash FROM stream WHERE stream_hash=?`, streamHash, streamHash)
	if err != nil {
		return rollback(err)
	}
	var blobHashes []string
	for rows.Next() {
		var blobHash string
		if err := rows.Scan(&blobHash); err != nil {
			rows.Close()
			return rollback(err)
		}
		blobHashes = append(blobHashes, blobHash)
	}
	if err := rows.Close(); err != nil {
		return rollback(err)
	}
	for _, statement := range []string{
		"DELETE FROM content_claim WHERE stream_hash=?",
		"DELETE FROM file WHERE stream_hash=?",
		"DELETE FROM stream_blob WHERE stream_hash=?",
		"DELETE FROM stream WHERE stream_hash=?",
	} {
		if _, err := transaction.ExecContext(ctx, statement, streamHash); err != nil {
			return rollback(err)
		}
	}
	for _, blobHash := range blobHashes {
		if _, err := transaction.ExecContext(ctx,
			"DELETE FROM blob WHERE blob_hash=?", blobHash,
		); err != nil {
			return rollback(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	return blobHashes, nil
}

func (store *ResolvedClaimStore) LinkManagedStreamClaim(
	ctx context.Context, streamHash, claimOutpoint string,
) error {
	if store == nil {
		return errors.New("resolved claim store is nil")
	}
	if ctx == nil {
		return errors.New("managed stream context is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return ErrResolvedClaimStoreNotOpen
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(linkErr error) error {
		return errors.Join(linkErr, transaction.Rollback())
	}
	var claimID string
	var serialized []byte
	if err := transaction.QueryRowContext(ctx,
		"SELECT claim_id, serialized_metadata FROM claim WHERE claim_outpoint=?", claimOutpoint,
	).Scan(&claimID, &serialized); errors.Is(err, sql.ErrNoRows) {
		return rollback(errors.New("claim not found"))
	} else if err != nil {
		return rollback(err)
	}
	canonical, err := hex.DecodeString(string(serialized))
	if err != nil {
		return rollback(err)
	}
	value, err := wallet.DecodeClaimValue(canonical)
	if err != nil {
		value, err = wallet.DecodeClaimValue(append([]byte{0}, canonical...))
	}
	if err != nil || value.Type != "stream" {
		return rollback(errors.New("claim does not contain a stream"))
	}
	var knownSDHash string
	if err := transaction.QueryRowContext(ctx,
		"SELECT sd_hash FROM stream WHERE stream_hash=?", streamHash,
	).Scan(&knownSDHash); errors.Is(err, sql.ErrNoRows) {
		return rollback(errors.New("stream not found"))
	} else if err != nil {
		return rollback(err)
	}
	source, _ := value.Value["source"].(map[string]any)
	claimSDHash, _ := source["sd_hash"].(string)
	if claimSDHash != knownSDHash {
		return rollback(errors.New("stream mismatch"))
	}
	var currentClaimID string
	err = transaction.QueryRowContext(ctx, `
        SELECT claim.claim_id FROM claim INNER JOIN content_claim
            ON claim.claim_outpoint=content_claim.claim_outpoint
        WHERE content_claim.stream_hash=?`, streamHash).Scan(&currentClaimID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	if err == nil && currentClaimID != claimID {
		return rollback(fmt.Errorf(
			"mismatching claim ids when updating stream %s vs %s", currentClaimID, claimID,
		))
	}
	if _, err := transaction.ExecContext(ctx,
		"DELETE FROM content_claim WHERE stream_hash=?", streamHash,
	); err != nil {
		return rollback(err)
	}
	if _, err := transaction.ExecContext(ctx,
		"INSERT INTO content_claim VALUES (?, NULL, ?)", streamHash, claimOutpoint,
	); err != nil {
		return rollback(err)
	}
	return transaction.Commit()
}

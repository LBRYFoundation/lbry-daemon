package database

import (
	"context"
	"crypto/sha512"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lbry/daemon/blob"

	_ "modernc.org/sqlite"
)

// migrateRevision6To15Step applies one pinned daemon database migration. The
// caller owns sequencing and updates db_revision only after the full chain
// succeeds.
func migrateRevision6To15Step(
	ctx context.Context,
	dataDir string,
	downloadDir string,
	revision int,
	now func() time.Time,
) (bool, error) {
	if revision < 6 || revision > 14 {
		return false, nil
	}
	if ctx == nil {
		return true, errors.New("database migration context is nil")
	}
	if now == nil {
		now = time.Now
	}

	db, err := sql.Open("sqlite", SQLitePath(dataDir))
	if err != nil {
		return true, fmt.Errorf("open revision %d database: %w", revision, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return true, fmt.Errorf("open revision %d database: %w", revision, err)
	}

	var migrationErr error
	switch revision {
	case 6:
		migrationErr = migrate6To7(ctx, db)
	case 7:
		migrationErr = migrate7To8(ctx, db)
	case 8:
		migrationErr = migrate8To9(ctx, db, filepath.Join(dataDir, "blobfiles"))
	case 9:
		migrationErr = migrate9To10(ctx, db)
	case 10:
		migrationErr = migrate10To11(ctx, db, downloadDir)
	case 11:
		migrationErr = migrate11To12(ctx, db, now)
	case 12:
		migrationErr = migrate12To13(ctx, db)
	case 13:
		migrationErr = migrate13To14(ctx, db)
	case 14:
		migrationErr = migrate14To15(ctx, db)
	}
	closeErr := db.Close()
	if migrationErr != nil {
		return true, fmt.Errorf("migrate daemon database revision %d to %d: %w", revision, revision+1, migrationErr)
	}
	if closeErr != nil {
		return true, fmt.Errorf("close daemon database after revision %d migration: %w", revision, closeErr)
	}
	return true, nil
}

func migrate6To7(ctx context.Context, db *sql.DB) error {
	return runMigrationTransaction(ctx, db, func(tx *sql.Tx) error {
		for _, statement := range []string{
			"ALTER TABLE blob ADD COLUMN last_announced_time INTEGER",
			"ALTER TABLE blob ADD COLUMN single_announce INTEGER",
			"UPDATE blob SET next_announce_time=0",
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})
}

func migrate7To8(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE reflected_stream (
        sd_hash text not null,
        reflector_address text not null,
        timestamp integer,
        primary key (sd_hash, reflector_address)
    )`)
	return err
}

type lateMigrationStream struct {
	streamHash        string
	sdHash            string
	key               string
	streamName        string
	suggestedFileName string
	blobs             []blob.BlobInfo
}

func migrate8To9(ctx context.Context, db *sql.DB, blobDir string) error {
	streams, err := loadRevision8Streams(ctx, db)
	if err != nil {
		return err
	}
	for _, stream := range streams {
		descriptor := &blob.StreamDescriptor{
			StreamName:        hex.EncodeToString([]byte(stream.streamName)),
			Key:               stream.key,
			SuggestedFileName: hex.EncodeToString([]byte(stream.suggestedFileName)),
			StreamHash:        stream.streamHash,
			StreamType:        "lbryfile",
			Blobs:             stream.blobs,
		}
		encoded, err := blob.MarshalDescriptor(descriptor)
		if err != nil {
			return err
		}
		digest := sha512.Sum384(encoded)
		if hex.EncodeToString(digest[:]) == stream.sdHash {
			continue
		}
		if err := deleteRevision8Stream(ctx, db, blobDir, stream); err != nil {
			return err
		}
	}
	return nil
}

func loadRevision8Streams(ctx context.Context, db *sql.DB) ([]lateMigrationStream, error) {
	rows, err := db.QueryContext(ctx, `
        SELECT stream_hash, sd_hash, stream_key, stream_name, suggested_filename
        FROM stream`)
	if err != nil {
		return nil, err
	}
	var streams []lateMigrationStream
	for rows.Next() {
		var stream lateMigrationStream
		if err := rows.Scan(
			&stream.streamHash, &stream.sdHash, &stream.key,
			&stream.streamName, &stream.suggestedFileName,
		); err != nil {
			rows.Close()
			return nil, err
		}
		streams = append(streams, stream)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for index := range streams {
		blobRows, err := db.QueryContext(ctx, `
            SELECT stream_blob.position, stream_blob.iv,
                   blob.blob_hash, COALESCE(blob.blob_length, 0)
            FROM stream_blob LEFT JOIN blob ON blob.blob_hash=stream_blob.blob_hash
            WHERE stream_blob.stream_hash=? ORDER BY stream_blob.position`, streams[index].streamHash)
		if err != nil {
			return nil, err
		}
		for blobRows.Next() {
			var hash sql.NullString
			var info blob.BlobInfo
			if err := blobRows.Scan(&info.BlobNum, &info.IV, &hash, &info.Length); err != nil {
				blobRows.Close()
				return nil, err
			}
			if hash.Valid {
				info.BlobHash = hash.String
			}
			streams[index].blobs = append(streams[index].blobs, info)
		}
		if err := blobRows.Close(); err != nil {
			return nil, err
		}
		if len(streams[index].blobs) == 0 {
			return nil, fmt.Errorf("stream %s has no blob rows", streams[index].streamHash)
		}
	}
	return streams, nil
}

func deleteRevision8Stream(
	ctx context.Context, db *sql.DB, blobDir string, stream lateMigrationStream,
) error {
	return runMigrationTransaction(ctx, db, func(tx *sql.Tx) error {
		for _, statement := range []struct {
			query string
			arg   string
		}{
			{"DELETE FROM content_claim WHERE stream_hash=?", stream.streamHash},
			{"DELETE FROM file WHERE stream_hash=?", stream.streamHash},
			{"DELETE FROM stream_blob WHERE stream_hash=?", stream.streamHash},
			{"DELETE FROM stream WHERE stream_hash=?", stream.streamHash},
			{"DELETE FROM blob WHERE blob_hash=?", stream.sdHash},
		} {
			if _, err := tx.ExecContext(ctx, statement.query, statement.arg); err != nil {
				return err
			}
		}
		for _, info := range stream.blobs {
			if info.BlobHash == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM blob WHERE blob_hash=?", info.BlobHash); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(blobDir, info.BlobHash)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	})
}

func migrate9To10(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "SELECT stream_hash, sd_hash FROM stream")
	if err != nil {
		return err
	}
	type streamHead struct{ streamHash, sdHash string }
	var streams []streamHead
	for rows.Next() {
		var stream streamHead
		if err := rows.Scan(&stream.streamHash, &stream.sdHash); err != nil {
			rows.Close()
			return err
		}
		streams = append(streams, stream)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return runMigrationTransaction(ctx, db, func(tx *sql.Tx) error {
		for _, stream := range streams {
			var head string
			err := tx.QueryRowContext(ctx,
				"SELECT blob_hash FROM stream_blob WHERE position=0 AND stream_hash=?", stream.streamHash,
			).Scan(&head)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE blob SET should_announce=1 WHERE blob_hash IN (?, ?)", stream.sdHash, head,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func migrate10To11(ctx context.Context, db *sql.DB, _ string) error {
	columns, err := migrationTableColumns(ctx, db, "file")
	if err != nil {
		return err
	}
	if columns["content_fee"] || columns["saved_file"] {
		return nil
	}
	type fileRow struct {
		streamHash, fileName, downloadDirectory, status string
		dataRate                                        float64
	}
	rows, err := db.QueryContext(ctx,
		"SELECT stream_hash, file_name, download_directory, blob_data_rate, status FROM file")
	if err != nil {
		return err
	}
	var files []fileRow
	for rows.Next() {
		var row fileRow
		if err := rows.Scan(
			&row.streamHash, &row.fileName, &row.downloadDirectory, &row.dataRate, &row.status,
		); err != nil {
			rows.Close()
			return err
		}
		files = append(files, row)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	return runMigrationTransaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS new_file (
            stream_hash text primary key not null references stream,
            file_name text,
            download_directory text,
            blob_data_rate real not null,
            status text not null,
            saved_file integer not null,
            content_fee text
        )`); err != nil {
			return err
		}
		for _, row := range files {
			var fileName any = row.fileName
			var directory any = row.downloadDirectory
			saved := 0
			if row.downloadDirectory == "{stream}" || row.fileName == "{stream}" {
				fileName, directory = nil, nil
			} else if path, ok := decodeRevision10Path(row.downloadDirectory, row.fileName); ok {
				if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
					saved = 1
				} else {
					fileName, directory = nil, nil
				}
			} else {
				fileName, directory = nil, nil
			}
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO new_file VALUES (?, ?, ?, ?, ?, ?, NULL)",
				row.streamHash, fileName, directory, row.dataRate, row.status, saved,
			); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DROP TABLE file"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "ALTER TABLE new_file RENAME TO file")
		return err
	})
}

func decodeRevision10Path(encodedDirectory, encodedName string) (string, bool) {
	directory, err := hex.DecodeString(encodedDirectory)
	if err != nil {
		return "", false
	}
	name, err := hex.DecodeString(encodedName)
	if err != nil {
		return "", false
	}
	return filepath.Join(string(directory), string(name)), true
}

func migrate11To12(ctx context.Context, db *sql.DB, now func() time.Time) error {
	columns, err := migrationTableColumns(ctx, db, "file")
	if err != nil {
		return err
	}
	if columns["added_on"] {
		return nil
	}
	type fileRow struct {
		streamHash, status                      string
		fileName, downloadDirectory, contentFee sql.NullString
		dataRate                                any
		saved                                   int
	}
	rows, err := db.QueryContext(ctx, `
        SELECT stream_hash, file_name, download_directory, blob_data_rate,
               status, saved_file, content_fee FROM file`)
	if err != nil {
		return err
	}
	var files []fileRow
	for rows.Next() {
		var row fileRow
		if err := rows.Scan(
			&row.streamHash, &row.fileName, &row.downloadDirectory, &row.dataRate,
			&row.status, &row.saved, &row.contentFee,
		); err != nil {
			rows.Close()
			return err
		}
		files = append(files, row)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	return runMigrationTransaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS new_file"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `CREATE TABLE new_file (
            stream_hash text not null primary key references stream,
            file_name text,
            download_directory text,
            blob_data_rate text not null,
            status text not null,
            saved_file integer not null,
            content_fee text,
            added_on integer not null
        )`); err != nil {
			return err
		}
		for _, row := range files {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO new_file VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
				row.streamHash, nullableMigrationString(row.fileName),
				nullableMigrationString(row.downloadDirectory), row.dataRate,
				row.status, row.saved, nullableMigrationString(row.contentFee), now().Unix(),
			); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DROP TABLE file"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "ALTER TABLE new_file RENAME TO file")
		return err
	})
}

func migrate12To13(ctx context.Context, db *sql.DB) error {
	columns, err := migrationTableColumns(ctx, db, "file")
	if err != nil {
		return err
	}
	if columns["bt_infohash"] {
		return ensureRevision15SchemaExtras(ctx, db)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	return runMigrationTransaction(ctx, db, func(tx *sql.Tx) error {
		statements := []string{
			`CREATE TABLE IF NOT EXISTS torrent (
                bt_infohash char(20) not null primary key,
                tracker text, length integer not null, name text not null)`,
			`CREATE TABLE IF NOT EXISTS torrent_node (
                bt_infohash char(20) not null references torrent,
                host text not null, port integer not null)`,
			`CREATE TABLE IF NOT EXISTS torrent_tracker (
                bt_infohash char(20) not null references torrent, tracker text not null)`,
			`CREATE TABLE IF NOT EXISTS torrent_http_seed (
                bt_infohash char(20) not null references torrent, http_seed text not null)`,
			`CREATE TABLE IF NOT EXISTS new_file (
                stream_hash char(96) references stream,
                bt_infohash char(20) references torrent,
                file_name text, download_directory text,
                blob_data_rate real not null, status text not null,
                saved_file integer not null, content_fee text, added_on integer not null)`,
			`CREATE TABLE IF NOT EXISTS new_content_claim (
                stream_hash char(96) references stream,
                bt_infohash char(20) references torrent,
                claim_outpoint text unique not null references claim)`,
			`INSERT INTO new_file
                (stream_hash, bt_infohash, file_name, download_directory, blob_data_rate,
                 status, saved_file, content_fee, added_on)
                SELECT stream_hash, NULL, file_name, download_directory, blob_data_rate,
                       status, saved_file, content_fee, added_on FROM file`,
			`INSERT OR IGNORE INTO new_content_claim (stream_hash, bt_infohash, claim_outpoint)
                SELECT stream_hash, NULL, claim_outpoint FROM content_claim`,
			"DROP TABLE file",
			"DROP TABLE content_claim",
			"ALTER TABLE new_file RENAME TO file",
			"ALTER TABLE new_content_claim RENAME TO content_claim",
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})
}

func migrate13To14(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS peer (
        node_id char(96) not null primary key,
        address text not null,
        udp_port integer not null,
        tcp_port integer,
        unique (address, udp_port)
    )`)
	return err
}

func migrate14To15(ctx context.Context, db *sql.DB) error {
	if err := runMigrationTransaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"ALTER TABLE blob ADD COLUMN added_on INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"ALTER TABLE blob ADD COLUMN is_mine INTEGER NOT NULL DEFAULT 1")
		return err
	}); err != nil {
		return err
	}
	return ensureRevision15SchemaExtras(ctx, db)
}

func ensureRevision15SchemaExtras(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS torrent_node (
            bt_infohash char(20) not null references torrent,
            host text not null, port integer not null)`,
		`CREATE TABLE IF NOT EXISTS torrent_tracker (
            bt_infohash char(20) not null references torrent, tracker text not null)`,
		`CREATE TABLE IF NOT EXISTS torrent_http_seed (
            bt_infohash char(20) not null references torrent, http_seed text not null)`,
		`CREATE TABLE IF NOT EXISTS peer (
            node_id char(96) not null primary key,
            address text not null, udp_port integer not null, tcp_port integer,
            unique (address, udp_port))`,
		"CREATE INDEX IF NOT EXISTS blob_data ON blob(blob_hash, blob_length, is_mine)",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func migrationTableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var sequence, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&sequence, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func nullableMigrationString(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func runMigrationTransaction(ctx context.Context, db *sql.DB, operation func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := operation(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

package database

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lbry/daemon/blob"
)

const legacyRevision6Schema = `
PRAGMA foreign_keys=ON;
PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS blob (
    blob_hash char(96) PRIMARY KEY NOT NULL,
    blob_length integer NOT NULL,
    next_announce_time integer NOT NULL,
    should_announce integer NOT NULL DEFAULT 0,
    status text NOT NULL
);
CREATE TABLE IF NOT EXISTS stream (
    stream_hash char(96) NOT NULL PRIMARY KEY,
    sd_hash char(96) NOT NULL REFERENCES blob,
    stream_key text NOT NULL,
    stream_name text NOT NULL,
    suggested_filename text NOT NULL
);
CREATE TABLE IF NOT EXISTS stream_blob (
    stream_hash char(96) NOT NULL REFERENCES stream,
    blob_hash char(96) REFERENCES blob,
    position integer NOT NULL,
    iv char(32) NOT NULL,
    PRIMARY KEY (stream_hash, blob_hash)
);
CREATE TABLE IF NOT EXISTS claim (
    claim_outpoint text NOT NULL PRIMARY KEY,
    claim_id char(40) NOT NULL,
    claim_name text NOT NULL,
    amount integer NOT NULL,
    height integer NOT NULL,
    serialized_metadata blob NOT NULL,
    channel_claim_id text,
    address text NOT NULL,
    claim_sequence integer NOT NULL
);
CREATE TABLE IF NOT EXISTS file (
    stream_hash text PRIMARY KEY NOT NULL REFERENCES stream,
    file_name text NOT NULL,
    download_directory text NOT NULL,
    blob_data_rate real NOT NULL,
    status text NOT NULL
);
CREATE TABLE IF NOT EXISTS content_claim (
    stream_hash text UNIQUE NOT NULL REFERENCES file,
    claim_outpoint text NOT NULL REFERENCES claim,
    PRIMARY KEY (stream_hash, claim_outpoint)
);
CREATE TABLE IF NOT EXISTS support (
    support_outpoint text NOT NULL PRIMARY KEY,
    claim_id text NOT NULL,
    amount integer NOT NULL,
    address text NOT NULL
);`

// Migrator upgrades the daemon databases through the pinned SDK's revision-15
// chain. Successful data/schema behavior follows Python; failures are returned
// so startup cannot silently stamp a damaged database as current.
type Migrator struct {
	DataDir     string
	DownloadDir string
	Now         func() time.Time
	Logf        func(string, ...any)
}

func NewMigrator(dataDir, downloadDir string) *Migrator {
	return &Migrator{
		DataDir: dataDir, DownloadDir: downloadDir, Now: time.Now,
		Logf: log.Printf,
	}
}

// MigrationFunc adapts Migrator to EnsureRevision.
func (m *Migrator) MigrationFunc(fromRevision, toRevision int) error {
	if m == nil {
		return errors.New("database migrator is nil")
	}
	if fromRevision < 1 || toRevision > CurrentRevision || fromRevision >= toRevision {
		return fmt.Errorf("unsupported database migration range %d to %d", fromRevision, toRevision)
	}
	if m.Now == nil {
		m.Now = time.Now
	}
	if m.Logf == nil {
		m.Logf = func(string, ...any) {}
	}

	backup, err := m.prepareFailureBackup(fromRevision)
	if err != nil {
		return err
	}
	succeeded := false
	defer func() {
		if succeeded && backup != "" {
			_ = os.Remove(backup)
		}
	}()

	m.Logf("Database: upgrading revision %d to %d...", fromRevision, toRevision)
	for revision := fromRevision; revision < toRevision; revision++ {
		if err := m.migrateStep(context.Background(), revision); err != nil {
			if backup != "" {
				m.Logf("Database: migration stopped at revision %d; pre-migration backup kept at %s.", revision, backup)
			}
			return fmt.Errorf("migrate database revision %d to %d: %w", revision, revision+1, err)
		}
		m.Logf("Database: migrated revision %d to %d.", revision, revision+1)
	}
	succeeded = true
	m.Logf("Database: upgrade complete at revision %d.", toRevision)
	return nil
}

func (m *Migrator) migrateStep(ctx context.Context, revision int) error {
	switch revision {
	case 1:
		return m.migrate1To2(ctx)
	case 2:
		return m.migrate2To3(ctx)
	case 3:
		return m.migrate3To4(ctx)
	case 4:
		return m.migrate4To5(ctx)
	case 5:
		return m.migrate5To6(ctx)
	default:
		handled, err := migrateRevision6To15Step(ctx, m.DataDir, m.DownloadDir, revision, m.Now)
		if !handled && err == nil {
			return fmt.Errorf("database migration of version %d to %d is not available", revision, revision+1)
		}
		return err
	}
}

func (m *Migrator) prepareFailureBackup(revision int) (string, error) {
	source := SQLitePath(m.DataDir)
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("stat database before migration: %w", err)
	}
	backup := filepath.Join(m.DataDir, fmt.Sprintf("rev_%d_unmigrated_database.sqlite", revision))
	for sequence := 1; ; sequence++ {
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			return "", fmt.Errorf("stat migration backup: %w", err)
		}
		backup = filepath.Join(m.DataDir, fmt.Sprintf("rev_%d_unmigrated_database_%d.sqlite", revision, sequence))
	}
	input, err := os.Open(source)
	if err != nil {
		return "", fmt.Errorf("open database migration backup source: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create database migration backup: %w", err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return "", fmt.Errorf("copy database migration backup: %w", err)
	}
	if err := output.Close(); err != nil {
		return "", fmt.Errorf("close database migration backup: %w", err)
	}
	return backup, nil
}

func openMigrationDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

func migrationFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

func (m *Migrator) migrate1To2(ctx context.Context) error {
	path := filepath.Join(m.DataDir, "blockchainname.db")
	exists, err := migrationFileExists(path)
	if err != nil || !exists {
		return err
	}
	db, err := openMigrationDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	nameRows, err := queryRows(ctx, db, "SELECT name, txid, sd_hash FROM name_metadata", 3)
	if err != nil {
		return err
	}
	claimRows, err := queryRows(ctx, db, "SELECT claimId, name, txid FROM claim_ids", 3)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(err error) error { return errors.Join(err, tx.Rollback()) }
	if _, err := tx.ExecContext(ctx, `
DROP TABLE name_metadata;
CREATE TABLE name_metadata (name text, txid text, n integer, sd_hash text);
DROP TABLE claim_ids;
CREATE TABLE claim_ids (claimId text, name text, txid text, n integer);`); err != nil {
		return rollback(err)
	}
	for _, row := range nameRows {
		if _, err := tx.ExecContext(ctx, "INSERT INTO name_metadata VALUES (?, ?, -1, ?)", row...); err != nil {
			return rollback(err)
		}
	}
	for _, row := range claimRows {
		if _, err := tx.ExecContext(ctx, "INSERT INTO claim_ids VALUES (?, ?, ?, -1)", row...); err != nil {
			return rollback(err)
		}
	}
	return tx.Commit()
}

func (m *Migrator) migrate2To3(ctx context.Context) error {
	path := filepath.Join(m.DataDir, "blockchainname.db")
	exists, err := migrationFileExists(path)
	if err != nil || !exists {
		return err
	}
	db, err := openMigrationDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `
DROP TABLE IF EXISTS tmp_name_metadata_table;
CREATE TABLE tmp_name_metadata_table (
    name TEXT UNIQUE NOT NULL, txid TEXT NOT NULL, n INTEGER NOT NULL, sd_hash TEXT NOT NULL
);
INSERT OR IGNORE INTO tmp_name_metadata_table (name, txid, n, sd_hash)
    SELECT name, txid, n, sd_hash FROM name_metadata;
DROP TABLE name_metadata;
ALTER TABLE tmp_name_metadata_table RENAME TO name_metadata;`)
	return err
}

func (m *Migrator) migrate3To4(ctx context.Context) error {
	blobsPath := filepath.Join(m.DataDir, "blobs.db")
	filesPath := filepath.Join(m.DataDir, "lbryfile_info.db")
	blobsExist, err := migrationFileExists(blobsPath)
	if err != nil || !blobsExist {
		return err
	}
	blobsDB, err := openMigrationDB(blobsPath)
	if err != nil {
		return err
	}
	defer blobsDB.Close()
	columns, err := migrationTableColumnNames(ctx, blobsDB, "blobs")
	if err != nil {
		return err
	}
	if !containsString(columns, "should_announce") {
		if _, err := blobsDB.ExecContext(ctx, "ALTER TABLE blobs ADD COLUMN should_announce integer NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	filesExist, err := migrationFileExists(filesPath)
	if err != nil || !filesExist {
		return err
	}
	filesDB, err := openMigrationDB(filesPath)
	if err != nil {
		return err
	}
	defer filesDB.Close()
	rows, err := queryRows(ctx, filesDB, `
SELECT sd_blob_hash FROM lbry_file_descriptors
UNION ALL
SELECT blob_hash FROM lbry_file_blobs WHERE position=0`, 1)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := blobsDB.ExecContext(ctx, "UPDATE blobs SET should_announce=1 WHERE blob_hash=?", row[0]); err != nil {
			return err
		}
	}
	return nil
}

func (m *Migrator) migrate4To5(ctx context.Context) error {
	metadataPath := filepath.Join(m.DataDir, "blockchainname.db")
	filesPath := filepath.Join(m.DataDir, "lbryfile_info.db")
	metadataExists, err := migrationFileExists(metadataPath)
	if err != nil {
		return err
	}
	filesExist, err := migrationFileExists(filesPath)
	if err != nil || (!metadataExists && !filesExist) || !filesExist {
		return err
	}
	if !metadataExists {
		return errors.New("blockchainname.db is missing")
	}
	metadataDB, err := openMigrationDB(metadataPath)
	if err != nil {
		return err
	}
	defer metadataDB.Close()
	filesDB, err := openMigrationDB(filesPath)
	if err != nil {
		return err
	}
	defer filesDB.Close()
	if _, err := filesDB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS lbry_file_metadata (
        lbry_file integer PRIMARY KEY, txid text, n integer,
        FOREIGN KEY(lbry_file) REFERENCES lbry_files(rowid)
    )`); err != nil {
		return err
	}
	rows, err := queryRows(ctx, filesDB, `
SELECT lbry_files.rowid, lbry_file_descriptors.sd_blob_hash
FROM lbry_files INNER JOIN lbry_file_descriptors USING (stream_hash)`, 2)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var txid sql.NullString
		var n sql.NullInt64
		err := metadataDB.QueryRowContext(ctx,
			"SELECT txid, n FROM name_metadata WHERE sd_hash=? LIMIT 1", row[1],
		).Scan(&txid, &n)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var txidValue, nValue any
		if txid.Valid {
			txidValue = txid.String
		}
		if n.Valid {
			nValue = n.Int64
		}
		if _, err := filesDB.ExecContext(ctx,
			"INSERT OR REPLACE INTO lbry_file_metadata VALUES (?, ?, ?)", row[0], txidValue, nValue,
		); err != nil {
			return err
		}
	}
	return nil
}

type legacyStreamBlob struct {
	Hash     sql.NullString
	Position int64
	IV       string
	Length   int64
}

type legacyFile struct {
	RowID, DataRate         any
	SDHash, StreamHash, Key string
	StreamName, Suggested   string
	Status                  string
	Blobs                   []legacyStreamBlob
	ClaimTxID               sql.NullString
	ClaimN                  sql.NullInt64
}

type legacyClaim struct {
	TxID, ClaimID, Name, Address string
	N, Sequence, Height, Amount  int64
	Serialized                   []byte
}

func (m *Migrator) migrate5To6(ctx context.Context) error {
	metadataDB, err := openRequiredLegacyDB(m.DataDir, "blockchainname.db")
	if err != nil {
		return err
	}
	defer metadataDB.Close()
	filesDB, err := openRequiredLegacyDB(m.DataDir, "lbryfile_info.db")
	if err != nil {
		return err
	}
	defer filesDB.Close()
	blobsDB, err := openRequiredLegacyDB(m.DataDir, "blobs.db")
	if err != nil {
		return err
	}
	defer blobsDB.Close()

	blobRows, err := queryRows(ctx, blobsDB, "SELECT * FROM blobs", 5)
	if err != nil {
		return err
	}
	files, err := readLegacyFiles(ctx, filesDB, metadataDB)
	if err != nil {
		return err
	}
	claims, err := readLegacyClaims(ctx, metadataDB)
	if err != nil {
		return err
	}

	newDB, err := openMigrationDB(SQLitePath(m.DataDir))
	if err != nil {
		return err
	}
	defer newDB.Close()
	if _, err := newDB.ExecContext(ctx, legacyRevision6Schema); err != nil {
		return err
	}
	tx, err := newDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(err error) error { return errors.Join(err, tx.Rollback()) }
	for _, row := range blobRows {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO blob VALUES (?, ?, ?, ?, 'finished')",
			row[0], row[1], row[3], row[4]); err != nil {
			return rollback(err)
		}
	}

	downloadHex := hex.EncodeToString([]byte(m.DownloadDir))
	importedFiles := make(map[string]legacyFile)
	for _, file := range files {
		if err := recoverLegacyDescriptor(ctx, tx, m.DataDir, file); err != nil {
			return rollback(err)
		}
		result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO stream VALUES (?, ?, ?, ?, ?)",
			file.StreamHash, file.SDHash, file.Key, file.StreamName, file.Suggested)
		if err != nil {
			continue // Python skips streams whose SD blob could not be recovered.
		}
		if count, _ := result.RowsAffected(); count == 0 {
			continue
		}
		for _, item := range file.Blobs {
			if item.Hash.Valid {
				if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO blob VALUES (?, ?, 0, 0, 'pending')",
					item.Hash.String, item.Length); err != nil {
					return rollback(err)
				}
			}
			var hash any
			if item.Hash.Valid {
				hash = item.Hash.String
			}
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO stream_blob VALUES (?, ?, ?, ?)",
				file.StreamHash, hash, item.Position, item.IV); err != nil {
				return rollback(err)
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO file VALUES (?, ?, ?, ?, ?)",
			file.StreamHash, file.StreamName, downloadHex, file.DataRate, file.Status); err != nil {
			return rollback(err)
		}
		importedFiles[file.StreamHash] = file
	}

	claimsByOutpoint := make(map[string]legacyClaim, len(claims))
	for _, claim := range claims {
		claimsByOutpoint[legacyOutpoint(claim.TxID, claim.N)] = claim
	}
	for streamHash, file := range importedFiles {
		if !file.ClaimTxID.Valid || !file.ClaimN.Valid {
			continue
		}
		outpoint := legacyOutpoint(file.ClaimTxID.String, file.ClaimN.Int64)
		claim, ok := claimsByOutpoint[outpoint]
		if !ok {
			continue
		}
		channelID, err := legacyClaimChannelID(claim.Serialized)
		if err != nil {
			return rollback(err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO claim VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			outpoint, claim.ClaimID, claim.Name, claim.Amount, claim.Height,
			claim.Serialized, channelID, claim.Address, claim.Sequence); err != nil {
			return rollback(err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO content_claim VALUES (?, ?)", streamHash, outpoint); err != nil {
			return rollback(err)
		}
	}
	return tx.Commit()
}

func openRequiredLegacyDB(dataDir, name string) (*sql.DB, error) {
	path := filepath.Join(dataDir, name)
	exists, err := migrationFileExists(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("legacy database %s is missing", name)
	}
	return openMigrationDB(path)
}

func readLegacyFiles(ctx context.Context, filesDB, metadataDB *sql.DB) ([]legacyFile, error) {
	metadataRows, err := queryRows(ctx, filesDB, "SELECT lbry_file, txid, n FROM lbry_file_metadata", 3)
	if err != nil {
		return nil, err
	}
	fileOutpoints := make(map[string][2]any)
	for _, row := range metadataRows {
		fileOutpoints[fmt.Sprint(row[0])] = [2]any{row[1], row[2]}
	}
	sdRows, err := queryRows(ctx, metadataDB, "SELECT txid, n, sd_hash FROM name_metadata", 3)
	if err != nil {
		return nil, err
	}
	sdOutpoints := make(map[string][2]any)
	for _, row := range sdRows {
		sdOutpoints[fmt.Sprint(row[2])] = [2]any{row[0], row[1]}
	}

	blobsByStream := make(map[string][]legacyStreamBlob)
	rows, err := filesDB.QueryContext(ctx, "SELECT blob_hash, stream_hash, position, iv, length FROM lbry_file_blobs")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var item legacyStreamBlob
		var streamHash string
		if err := rows.Scan(&item.Hash, &streamHash, &item.Position, &item.IV, &item.Length); err != nil {
			rows.Close()
			return nil, err
		}
		blobsByStream[streamHash] = append(blobsByStream[streamHash], item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = filesDB.QueryContext(ctx, `
SELECT DISTINCT lbry_files.rowid, d.sd_blob_hash, lbry_files.stream_hash,
       lbry_files.key, lbry_files.stream_name, lbry_files.suggested_filename,
       o.blob_data_rate, o.status
FROM lbry_files
INNER JOIN lbry_file_descriptors d ON lbry_files.stream_hash=d.stream_hash
INNER JOIN lbry_file_options o ON lbry_files.stream_hash=o.stream_hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []legacyFile
	for rows.Next() {
		var file legacyFile
		if err := rows.Scan(&file.RowID, &file.SDHash, &file.StreamHash, &file.Key,
			&file.StreamName, &file.Suggested, &file.DataRate, &file.Status); err != nil {
			return nil, err
		}
		file.Blobs = blobsByStream[file.StreamHash]
		outpoint, ok := fileOutpoints[fmt.Sprint(file.RowID)]
		if !ok {
			outpoint, ok = sdOutpoints[file.SDHash]
		}
		if ok {
			file.ClaimTxID = nullString(outpoint[0])
			file.ClaimN = nullInt64(outpoint[1])
		}
		result = append(result, file)
	}
	return result, rows.Err()
}

func readLegacyClaims(ctx context.Context, metadataDB *sql.DB) ([]legacyClaim, error) {
	rows, err := metadataDB.QueryContext(ctx, `
SELECT DISTINCT c.txid, c.n, c.claimId, c.name, claim_cache.claim_sequence,
       claim_cache.claim_address, claim_cache.height, claim_cache.amount, claim_cache.claim_pb
FROM claim_cache INNER JOIN claim_ids c ON claim_cache.claim_id=c.claimId`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []legacyClaim
	seen := make(map[string]struct{})
	for rows.Next() {
		var claim legacyClaim
		if err := rows.Scan(&claim.TxID, &claim.N, &claim.ClaimID, &claim.Name, &claim.Sequence,
			&claim.Address, &claim.Height, &claim.Amount, &claim.Serialized); err != nil {
			return nil, err
		}
		key := legacyOutpoint(claim.TxID, claim.N)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, claim)
	}
	return result, rows.Err()
}

func recoverLegacyDescriptor(ctx context.Context, tx *sql.Tx, dataDir string, file legacyFile) error {
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM blob WHERE blob_hash=?", file.SDHash).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	contents, err := os.ReadFile(filepath.Join(dataDir, "blobfiles", file.SDHash))
	if err != nil {
		return nil
	}
	descriptor, err := blob.ParseDescriptor(contents)
	if err != nil || descriptor.StreamHash != file.StreamHash || blob.ValidateDescriptor(descriptor) != nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO blob VALUES (?, ?, 0, 1, 'finished')",
		file.SDHash, len(contents)); err != nil {
		return err
	}
	for _, item := range descriptor.ContentBlobs() {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO blob VALUES (?, ?, 0, 0, 'pending')",
			item.BlobHash, item.Length); err != nil {
			return err
		}
	}
	return nil
}

func legacyClaimChannelID(serialized []byte) (any, error) {
	value, err := decodeResolvedClaimValue(serialized)
	if err != nil {
		if decoded, decodeErr := hex.DecodeString(string(serialized)); decodeErr == nil {
			value, err = decodeResolvedClaimValue(decoded)
		}
	}
	if err != nil {
		return nil, err
	}
	if channelID := value.SigningChannelID(); channelID != nil {
		return *channelID, nil
	}
	return nil, nil
}

func legacyOutpoint(txid string, n int64) string { return fmt.Sprintf("%s:%d", txid, n) }

func migrationTableColumnNames(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		result = append(result, name)
	}
	return result, rows.Err()
}

func queryRows(ctx context.Context, db *sql.DB, query string, count int) ([][]any, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result [][]any
	for rows.Next() {
		values := make([]any, count)
		destinations := make([]any, count)
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		result = append(result, values)
	}
	return result, rows.Err()
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func nullString(value any) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: fmt.Sprint(value), Valid: true}
}

func nullInt64(value any) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	switch value := value.(type) {
	case int64:
		return sql.NullInt64{Int64: value, Valid: true}
	case int:
		return sql.NullInt64{Int64: int64(value), Valid: true}
	case []byte:
		return parseNullInt64(string(value))
	default:
		return parseNullInt64(fmt.Sprint(value))
	}
}

func parseNullInt64(value string) sql.NullInt64 {
	var parsed int64
	if _, err := fmt.Sscan(strings.TrimSpace(value), &parsed); err != nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: parsed, Valid: true}
}

package database

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"

	"lbry/daemon/wallet"

	_ "modernc.org/sqlite"
)

var (
	ErrResolvedClaimStoreAlreadyOpen = errors.New("resolved claim store is already open")
	ErrResolvedClaimStoreNotOpen     = errors.New("resolved claim store is not open")
)

const resolvedClaimInsertSQL = `
        INSERT OR REPLACE INTO claim VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

// resolvedClaimSchema is the pinned SDK's complete revision-15 daemon schema.
// Opening an existing database only applies IF NOT EXISTS statements and never
// rewrites existing tables.
var resolvedClaimSchema = []string{
	`CREATE TABLE IF NOT EXISTS blob (
            blob_hash char(96) primary key not null,
            blob_length integer not null,
            next_announce_time integer not null,
            should_announce integer not null default 0,
            status text not null,
            last_announced_time integer,
            single_announce integer,
            added_on integer not null,
            is_mine integer not null default 0
        )`,
	`CREATE TABLE IF NOT EXISTS stream (
            stream_hash char(96) not null primary key,
            sd_hash char(96) not null references blob,
            stream_key text not null,
            stream_name text not null,
            suggested_filename text not null
        )`,
	`CREATE TABLE IF NOT EXISTS stream_blob (
            stream_hash char(96) not null references stream,
            blob_hash char(96) references blob,
            position integer not null,
            iv char(32) not null,
            primary key (stream_hash, blob_hash)
        )`,
	`CREATE TABLE IF NOT EXISTS claim (
            claim_outpoint text not null primary key,
            claim_id char(40) not null,
            claim_name text not null,
            amount integer not null,
            height integer not null,
            serialized_metadata blob not null,
            channel_claim_id text,
            address text not null,
            claim_sequence integer not null
        )`,
	`CREATE TABLE IF NOT EXISTS torrent (
            bt_infohash char(20) not null primary key,
            tracker text,
            length integer not null,
            name text not null
        )`,
	`CREATE TABLE IF NOT EXISTS torrent_node (
            bt_infohash char(20) not null references torrent,
            host text not null,
            port integer not null
        )`,
	`CREATE TABLE IF NOT EXISTS torrent_tracker (
            bt_infohash char(20) not null references torrent,
            tracker text not null
        )`,
	`CREATE TABLE IF NOT EXISTS torrent_http_seed (
            bt_infohash char(20) not null references torrent,
            http_seed text not null
        )`,
	`CREATE TABLE IF NOT EXISTS file (
            stream_hash char(96) references stream,
            bt_infohash char(20) references torrent,
            file_name text,
            download_directory text,
            blob_data_rate real not null,
            status text not null,
            saved_file integer not null,
            content_fee text,
            added_on integer not null
        )`,
	`CREATE TABLE IF NOT EXISTS content_claim (
            stream_hash char(96) references stream,
            bt_infohash char(20) references torrent,
            claim_outpoint text unique not null references claim
        )`,
	`CREATE TABLE IF NOT EXISTS support (
            support_outpoint text not null primary key,
            claim_id text not null,
            amount integer not null,
            address text not null
        )`,
	`CREATE TABLE IF NOT EXISTS reflected_stream (
            sd_hash text not null,
            reflector_address text not null,
            timestamp integer,
            primary key (sd_hash, reflector_address)
        )`,
	`CREATE TABLE IF NOT EXISTS peer (
            node_id char(96) not null primary key,
            address text not null,
            udp_port integer not null,
            tcp_port integer,
            unique (address, udp_port)
        )`,
	`CREATE INDEX IF NOT EXISTS blob_data ON blob(blob_hash, blob_length, is_mine)`,
}

type SupportRow struct {
	Outpoint string
	ClaimID  string
	Amount   int64
	Address  string
}

// SaveSupports mirrors SQLiteStorage.save_supports for one affected claim:
// stale cached rows are removed before the replacement rows are inserted.
func (store *ResolvedClaimStore) SaveSupports(
	ctx context.Context, claimID string, supports []SupportRow,
) error {
	if store == nil {
		return errors.New("resolved claim store is nil")
	}
	if ctx == nil {
		return errors.New("support store context is nil")
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
	rollback := func(saveErr error) error { return errors.Join(saveErr, transaction.Rollback()) }
	if _, err := transaction.ExecContext(ctx, "DELETE FROM support WHERE claim_id=?", claimID); err != nil {
		return rollback(err)
	}
	for _, support := range supports {
		if _, err := transaction.ExecContext(ctx,
			"INSERT INTO support VALUES (?, ?, ?, ?)",
			support.Outpoint, support.ClaimID, support.Amount, support.Address,
		); err != nil {
			return rollback(err)
		}
	}
	return transaction.Commit()
}

func (store *ResolvedClaimStore) GetSupports(ctx context.Context, claimID string) ([]SupportRow, error) {
	if store == nil {
		return nil, errors.New("resolved claim store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return nil, ErrResolvedClaimStoreNotOpen
	}
	rows, err := store.db.QueryContext(ctx,
		"SELECT support_outpoint, claim_id, amount, address FROM support WHERE claim_id=?", claimID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SupportRow
	for rows.Next() {
		var row SupportRow
		if err := rows.Scan(&row.Outpoint, &row.ClaimID, &row.Amount, &row.Address); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// ResolvedClaimStore owns the revision-15 lbrynet.sqlite subset used to cache
// claims returned by resolve. Its lifecycle is explicit so database startup,
// shutdown, and failures remain at the same component boundary as Python.
type ResolvedClaimStore struct {
	mu   sync.Mutex
	path string
	db   *sql.DB
}

func NewResolvedClaimStore(dataDir string) *ResolvedClaimStore {
	return &ResolvedClaimStore{path: SQLitePath(dataDir)}
}

func (store *ResolvedClaimStore) Path() string {
	if store == nil {
		return ""
	}
	return store.path
}

// Open opens or creates lbrynet.sqlite and adds only the tables needed by the
// resolved-claim persistence path. Close is safe after any Open failure.
func (store *ResolvedClaimStore) Open(ctx context.Context) error {
	if store == nil {
		return errors.New("resolved claim store is nil")
	}
	if ctx == nil {
		return errors.New("resolved claim store context is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db != nil {
		return ErrResolvedClaimStoreAlreadyOpen
	}

	connection, err := sql.Open("sqlite", store.path)
	if err != nil {
		return err
	}
	connection.SetMaxOpenConns(1)
	connection.SetMaxIdleConns(1)
	closeOnError := func(openErr error) error {
		return errors.Join(openErr, connection.Close())
	}
	if err := connection.PingContext(ctx); err != nil {
		return closeOnError(err)
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys=ON;"); err != nil {
		return closeOnError(fmt.Errorf("enable resolved claim foreign keys: %w", err))
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA journal_mode=WAL;"); err != nil {
		return closeOnError(fmt.Errorf("enable resolved claim WAL: %w", err))
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA busy_timeout=5000;"); err != nil {
		return closeOnError(fmt.Errorf("set resolved claim busy timeout: %w", err))
	}
	if err := initializeResolvedClaimSchema(ctx, connection); err != nil {
		return closeOnError(err)
	}
	store.db = connection
	return nil
}

// Close is idempotent and waits for an in-flight save or open to finish.
func (store *ResolvedClaimStore) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return nil
	}
	connection := store.db
	store.db = nil
	return connection.Close()
}

// SaveResolvedClaims implements the persistence boundary consumed by the RPC
// resolve handler. All output conversion completes before the SQL transaction,
// matching save_claim_from_output's eager Python list comprehension.
func (store *ResolvedClaimStore) SaveResolvedClaims(
	ctx context.Context, ledger *wallet.Ledger, outputs []*wallet.TransactionOutput,
) error {
	if store == nil {
		return errors.New("resolved claim store is nil")
	}
	if ctx == nil {
		return errors.New("resolved claim store context is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		return ErrResolvedClaimStoreNotOpen
	}

	rows := make([]resolvedClaimRow, len(outputs))
	for index, output := range outputs {
		row, err := resolvedClaimRowFromOutput(ledger, output)
		if err != nil {
			return err
		}
		rows[index] = row
	}
	return saveResolvedClaimRows(ctx, store.db, rows)
}

func initializeResolvedClaimSchema(ctx context.Context, connection *sql.DB) error {
	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, statement := range resolvedClaimSchema {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("initialize resolved claim schema: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit resolved claim schema: %w", err)
	}
	return nil
}

type resolvedClaimRow struct {
	Outpoint           string
	ClaimID            string
	Name               string
	Amount             uint64
	Height             int64
	SerializedMetadata []byte
	ChannelClaimID     *string
	Address            string
	ClaimSequence      int64
	SourceHash         string
	ValueType          string
}

func resolvedClaimRowFromOutput(
	ledger *wallet.Ledger, output *wallet.TransactionOutput,
) (resolvedClaimRow, error) {
	var row resolvedClaimRow
	if output == nil {
		return row, errors.New("'NoneType' object has no attribute 'claim_id'")
	}

	claimID, err := output.ClaimID()
	if err != nil {
		return row, err
	}
	if !utf8.Valid(output.Script.ClaimName) {
		return row, &resolvedClaimProjectionError{
			name: "UnicodeDecodeError", message: "'utf-8' codec can't decode claim name",
		}
	}
	if ledger == nil {
		return row, errors.New("'NoneType' object has no attribute 'network'")
	}
	address, err := output.Address(ledger.Network)
	if err != nil {
		return row, err
	}
	value, err := decodeResolvedClaimValue(output.Script.Claim)
	if err != nil {
		return row, err
	}
	canonical, err := value.MarshalBinary()
	if err != nil {
		return row, err
	}
	row = resolvedClaimRow{
		Outpoint:           output.ID(),
		ClaimID:            claimID,
		Name:               string(output.Script.ClaimName),
		Amount:             output.Amount,
		Height:             output.TransactionHeight(),
		SerializedMetadata: []byte(hex.EncodeToString(canonical)),
		ChannelClaimID:     value.SigningChannelID(),
		Address:            address,
		ClaimSequence:      -1,
		ValueType:          value.Type,
	}
	if value.Type == "stream" {
		if source, ok := value.Value["source"].(map[string]any); ok {
			row.SourceHash, _ = source["sd_hash"].(string)
		}
	}
	return row, nil
}

func decodeResolvedClaimValue(payload []byte) (*wallet.ClaimValue, error) {
	if len(payload) == 0 || payload[0] == 0 || payload[0] == 1 {
		return wallet.DecodeClaimValue(payload)
	}
	if payload[0] == '{' {
		return wallet.DecodeLegacyV0ClaimValue(payload)
	}
	return wallet.DecodeLegacyV1ClaimValue(payload)
}

func saveResolvedClaimRows(
	ctx context.Context, connection *sql.DB, rows []resolvedClaimRow,
) error {
	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(saveErr error) error {
		return errors.Join(saveErr, transaction.Rollback())
	}

	links := make([]resolvedClaimLink, 0, len(rows))
	for _, row := range rows {
		if _, err := transaction.ExecContext(ctx, resolvedClaimInsertSQL,
			row.Outpoint, row.ClaimID, row.Name, row.Amount, row.Height,
			row.SerializedMetadata, row.ChannelClaimID, row.Address,
			row.ClaimSequence,
		); err != nil {
			return rollback(err)
		}
		if row.SourceHash == "" {
			continue
		}
		var streamHash string
		err := transaction.QueryRowContext(ctx, `
                SELECT file.stream_hash FROM stream
                INNER JOIN file ON file.stream_hash=stream.stream_hash
                WHERE sd_hash=?`, row.SourceHash).Scan(&streamHash)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return rollback(err)
		}
		links = append(links, resolvedClaimLink{StreamHash: streamHash, Row: row})
	}

	for _, link := range links {
		if err := saveResolvedContentClaim(ctx, transaction, link); err != nil {
			return rollback(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return err
	}
	return nil
}

type resolvedClaimLink struct {
	StreamHash string
	Row        resolvedClaimRow
}

func saveResolvedContentClaim(
	ctx context.Context, transaction *sql.Tx, link resolvedClaimLink,
) error {
	if link.Row.ValueType != "stream" {
		return errors.New("claim does not contain a stream")
	}
	var knownSourceHash string
	if err := transaction.QueryRowContext(ctx,
		"SELECT sd_hash FROM stream WHERE stream_hash=?", link.StreamHash,
	).Scan(&knownSourceHash); errors.Is(err, sql.ErrNoRows) {
		return errors.New("stream not found")
	} else if err != nil {
		return err
	}
	if knownSourceHash != link.Row.SourceHash {
		return errors.New("stream mismatch")
	}

	var currentClaimID string
	err := transaction.QueryRowContext(ctx, `
            SELECT claim_id FROM claim
            INNER JOIN content_claim ON claim.claim_outpoint=content_claim.claim_outpoint
            WHERE content_claim.stream_hash=?`, link.StreamHash).Scan(&currentClaimID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && currentClaimID != link.Row.ClaimID {
		return fmt.Errorf(
			"mismatching claim ids when updating stream %s vs %s",
			currentClaimID, link.Row.ClaimID,
		)
	}
	if _, err := transaction.ExecContext(ctx,
		"DELETE FROM content_claim WHERE stream_hash=?", link.StreamHash,
	); err != nil {
		return err
	}
	_, err = transaction.ExecContext(ctx,
		"INSERT INTO content_claim VALUES (?, NULL, ?)",
		link.StreamHash, link.Row.Outpoint,
	)
	return err
}

type resolvedClaimProjectionError struct {
	name    string
	message string
}

func (err *resolvedClaimProjectionError) Error() string { return err.message }

func (err *resolvedClaimProjectionError) PythonErrorName() string { return err.name }

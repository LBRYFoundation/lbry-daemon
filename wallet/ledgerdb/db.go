// Package ledgerdb owns the Python wallet ledger's blockchain.db compatibility
// boundary. It is intentionally separate from the daemon database revision
// package because the two SQLite files have unrelated schemas and lifecycles.
package ledgerdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
)

const (
	Filename      = "blockchain.db"
	SchemaVersion = "1.6"
)

var (
	ErrAlreadyOpen            = errors.New("wallet ledger database is already open")
	ErrNotOpen                = errors.New("wallet ledger database is not open")
	ErrForeignKeysEnabled     = errors.New("wallet ledger database foreign keys are enabled")
	ErrChannelDecoderRequired = errors.New("channel claim decoder is required")
)

type sqlOpenFunc func(driverName, dataSourceName string) (*sql.DB, error)

type openOptions struct {
	openSQL sqlOpenFunc
}

type Option func(*openOptions)

func defaultOpenOptions() openOptions {
	return openOptions{openSQL: sql.Open}
}

// withSQLOpener is deliberately private. The production database always uses
// the pinned modernc driver; tests can replace only the connection constructor.
func withSQLOpener(opener sqlOpenFunc) Option {
	return func(options *openOptions) {
		if opener != nil {
			options.openSQL = opener
		}
	}
}

type DB struct {
	mu sync.Mutex

	path string
	sql  *sql.DB
}

func New(path string) *DB {
	return &DB{path: path}
}

// Open constructs and opens a database in one call. New plus (*DB).Open is
// available for ledgers that mirror Python's unopened constructor state.
func Open(ctx context.Context, path string, options ...Option) (*DB, error) {
	database := New(path)
	if err := database.Open(ctx, options...); err != nil {
		return nil, err
	}
	return database, nil
}

func (database *DB) Path() string {
	if database == nil {
		return ""
	}
	return database.path
}

func (database *DB) IsOpen() bool {
	if database == nil {
		return false
	}
	database.mu.Lock()
	defer database.mu.Unlock()
	return database.sql != nil
}

func (database *DB) Open(ctx context.Context, options ...Option) error {
	if database == nil {
		return errors.New("wallet ledger database is nil")
	}
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.sql != nil {
		return ErrAlreadyOpen
	}
	settings := defaultOpenOptions()
	for _, option := range options {
		if option != nil {
			option(&settings)
		}
	}

	connection, err := settings.openSQL(sqliteDriverName, database.path)
	if err != nil {
		return err
	}
	connection.SetMaxOpenConns(1)
	connection.SetMaxIdleConns(1)
	if err := connection.PingContext(ctx); err != nil {
		_ = connection.Close()
		return err
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA busy_timeout=5000;"); err != nil {
		_ = connection.Close()
		return fmt.Errorf("set SQLite busy timeout: %w", err)
	}
	var foreignKeys int
	if err := connection.QueryRowContext(ctx, "PRAGMA foreign_keys;").Scan(&foreignKeys); err != nil {
		_ = connection.Close()
		return fmt.Errorf("read SQLite foreign-key mode: %w", err)
	}
	if foreignKeys != 0 {
		_ = connection.Close()
		return ErrForeignKeysEnabled
	}
	if err := initializeSchema(ctx, connection); err != nil {
		_ = connection.Close()
		return err
	}
	database.sql = connection
	return nil
}

func (database *DB) Close(ctx context.Context) error {
	if database == nil {
		return nil
	}
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.sql == nil {
		return nil
	}
	connection := database.sql
	database.sql = nil

	checkpointErr := checkpoint(ctx, connection)
	closeErr := connection.Close()
	return errors.Join(checkpointErr, closeErr)
}

func checkpoint(ctx context.Context, connection *sql.DB) error {
	rows, err := connection.QueryContext(ctx, "PRAGMA wal_checkpoint(FULL);")
	if err != nil {
		return fmt.Errorf("checkpoint wallet ledger database: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close wallet ledger checkpoint result: %w", err)
	}
	return nil
}

func (database *DB) transaction(
	ctx context.Context, operation func(*sql.Tx) error,
) error {
	if database == nil {
		return errors.New("wallet ledger database is nil")
	}
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.sql == nil {
		return ErrNotOpen
	}
	transaction, err := database.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := operation(transaction); err != nil {
		_ = transaction.Rollback()
		return err
	}
	return transaction.Commit()
}

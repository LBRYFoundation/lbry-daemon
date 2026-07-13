package ledgerdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const createVersionTableSQL = `
        create table if not exists version (
            version text
        );`

var freshSchemaStatements = []string{
	"pragma journal_mode=WAL;",
	`create table if not exists account_address (
            account text not null,
            address text not null,
            chain integer not null,
            pubkey blob not null,
            chain_code blob not null,
            n integer not null,
            depth integer not null,
            primary key (account, address)
        );`,
	"create index if not exists address_account_idx on account_address (address, account);",
	`create table if not exists pubkey_address (
            address text primary key,
            history text,
            used_times integer not null default 0
        );`,
	`create table if not exists tx (
            txid text primary key,
            raw blob not null,
            height integer not null,
            position integer not null,
            is_verified boolean not null default 0,
            purchased_claim_id text,
            day integer
        );`,
	"create index if not exists tx_purchased_claim_id_idx on tx (purchased_claim_id);",
	`create table if not exists txo (
            txid text references tx,
            txoid text primary key,
            address text references pubkey_address,
            position integer not null,
            amount integer not null,
            script blob not null,
            is_reserved boolean not null default 0,

            txo_type integer not null default 0,
            claim_id text,
            claim_name text,
            has_source bool,

            channel_id text,
            reposted_claim_id text
        );`,
	"create index if not exists txo_txid_idx on txo (txid);",
	"create index if not exists txo_address_idx on txo (address);",
	"create index if not exists txo_claim_id_idx on txo (claim_id, txo_type);",
	"create index if not exists txo_claim_name_idx on txo (claim_name);",
	"create index if not exists txo_txo_type_idx on txo (txo_type);",
	"create index if not exists txo_channel_id_idx on txo (channel_id);",
	"create index if not exists txo_reposted_claim_idx on txo (reposted_claim_id);",
	`create table if not exists txi (
            txid text references tx,
            txoid text references txo primary key,
            address text references pubkey_address,
            position integer not null
        );`,
	"create index if not exists txi_address_idx on txi (address);",
	"create index if not exists first_input_idx on txi (txid, address) where position=0;",
}

func initializeSchema(ctx context.Context, connection *sql.DB) error {
	tables, err := tableNames(ctx, connection)
	if err != nil {
		return err
	}
	if len(tables) > 0 {
		if containsString(tables, "version") {
			version, found, err := firstSchemaVersion(ctx, connection)
			if err != nil {
				return err
			}
			if found && version.Valid && version.String == SchemaVersion {
				// The pinned SDK accepts a current version immediately. It does not
				// repair missing schema objects or reapply WAL on this path.
				return nil
			}
			if found && version.Valid && version.String == "1.5" {
				if _, err := connection.ExecContext(
					ctx, "ALTER TABLE txo ADD COLUMN has_source bool DEFAULT 1;",
				); err != nil {
					return fmt.Errorf("migrate wallet ledger database from 1.5: %w", err)
				}
				if _, err := connection.ExecContext(
					ctx, "UPDATE version SET version = ?", SchemaVersion,
				); err != nil {
					return fmt.Errorf("record wallet ledger database version 1.6: %w", err)
				}
				return nil
			}
		}
		if err := resetSchema(ctx, connection, tables); err != nil {
			return err
		}
	}
	if _, err := connection.ExecContext(ctx, createVersionTableSQL); err != nil {
		return fmt.Errorf("create wallet ledger version table: %w", err)
	}
	if _, err := connection.ExecContext(
		ctx, "INSERT INTO version VALUES (?)", SchemaVersion,
	); err != nil {
		return fmt.Errorf("record wallet ledger database version: %w", err)
	}
	for _, statement := range freshSchemaStatements {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create wallet ledger schema with %q: %w", summarizeSQL(statement), err)
		}
	}
	return nil
}

func tableNames(ctx context.Context, connection *sql.DB) ([]string, error) {
	rows, err := connection.QueryContext(
		ctx, "SELECT name FROM sqlite_master WHERE type='table';",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	return tables, rows.Err()
}

func firstSchemaVersion(
	ctx context.Context, connection *sql.DB,
) (sql.NullString, bool, error) {
	var version sql.NullString
	var storageClass string
	err := connection.QueryRowContext(
		ctx, "SELECT version, typeof(version) FROM version LIMIT 1;",
	).Scan(&version, &storageClass)
	if errors.Is(err, sql.ErrNoRows) {
		return version, false, nil
	}
	// database/sql can scan a SQLite BLOB into a string. CPython returns bytes
	// for that storage class, which does not compare equal to schema text.
	if err == nil && storageClass != "text" {
		version.Valid = false
	}
	return version, err == nil, err
}

func resetSchema(ctx context.Context, connection *sql.DB, tables []string) error {
	for _, table := range tables {
		// Python interpolates sqlite_master names directly without quoting.
		// Retaining that behavior also preserves its failure mode for unusual
		// third-party table names.
		if _, err := connection.ExecContext(ctx, "DROP TABLE "+table+";"); err != nil {
			return fmt.Errorf("drop incompatible wallet ledger table %q: %w", table, err)
		}
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA WAL_CHECKPOINT(FULL);"); err != nil {
		return fmt.Errorf("checkpoint reset wallet ledger database: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "VACUUM;"); err != nil {
		return fmt.Errorf("vacuum reset wallet ledger database: %w", err)
	}
	return nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func summarizeSQL(statement string) string {
	return strings.Join(strings.Fields(statement), " ")
}

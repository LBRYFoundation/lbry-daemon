package ledgerdb

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestFreshSchemaMatchesPinnedWalletDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), Filename)
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if got := queryString(t, database.sql, "SELECT version FROM version LIMIT 1"); got != SchemaVersion {
		t.Fatalf("schema version = %q, want %q", got, SchemaVersion)
	}
	if got := strings.ToLower(queryString(t, database.sql, "PRAGMA journal_mode")); got != "wal" {
		t.Fatalf("journal mode = %q, want wal", got)
	}
	if got := queryInt(t, database.sql, "PRAGMA foreign_keys"); got != 0 {
		t.Fatalf("foreign keys = %d, want 0", got)
	}
	if got := queryInt(t, database.sql, "PRAGMA busy_timeout"); got != 5000 {
		t.Fatalf("busy timeout = %d, want 5000", got)
	}

	wantTables := []string{"account_address", "pubkey_address", "tx", "txi", "txo", "version"}
	if got := userTableNames(t, database.sql); !reflect.DeepEqual(got, wantTables) {
		t.Fatalf("tables = %v, want %v", got, wantTables)
	}
	wantTXOColumns := []string{
		"txid", "txoid", "address", "position", "amount", "script", "is_reserved",
		"txo_type", "claim_id", "claim_name", "has_source", "channel_id", "reposted_claim_id",
	}
	columns := tableColumns(t, database.sql, "txo")
	if got := columnNames(columns); !reflect.DeepEqual(got, wantTXOColumns) {
		t.Fatalf("fresh txo columns = %v, want %v", got, wantTXOColumns)
	}
	if columns[10].Default.Valid {
		t.Fatalf("fresh has_source default = %q, want NULL", columns[10].Default.String)
	}
	if got, want := explicitIndexes(t, database.sql), []string{
		"address_account_idx", "first_input_idx", "tx_purchased_claim_id_idx",
		"txi_address_idx", "txo_address_idx", "txo_channel_id_idx", "txo_claim_id_idx",
		"txo_claim_name_idx", "txo_reposted_claim_idx", "txo_txid_idx", "txo_txo_type_idx",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("indexes = %v, want %v", got, want)
	}
	if got := foreignKeyTargets(t, database.sql, "txo"); !reflect.DeepEqual(got, []string{"pubkey_address", "tx"}) {
		t.Fatalf("declared txo foreign keys = %v", got)
	}
	if got := partialIndexPredicate(t, database.sql, "first_input_idx"); !strings.Contains(got, "position=0") {
		t.Fatalf("first_input_idx SQL = %q", got)
	}
}

func TestCurrentVersionReturnsWithoutRepairOrWAL(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), Filename)
	raw := openRawTestDB(t, path)
	mustExec(t, raw, "PRAGMA journal_mode=DELETE")
	mustExec(t, raw, "CREATE TABLE version (version TEXT)")
	mustExec(t, raw, "INSERT INTO version VALUES (?)", SchemaVersion)
	mustExec(t, raw, "CREATE TABLE sentinel (value TEXT)")
	mustExec(t, raw, "INSERT INTO sentinel VALUES ('preserved')")
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	if got := strings.ToLower(queryString(t, database.sql, "PRAGMA journal_mode")); got != "delete" {
		t.Fatalf("early-return journal mode = %q, want delete", got)
	}
	if got := queryString(t, database.sql, "SELECT value FROM sentinel"); got != "preserved" {
		t.Fatalf("sentinel = %q", got)
	}
	if tableExists(t, database.sql, "txo") {
		t.Fatal("current-version early return repaired a missing txo table")
	}
}

func TestVersion15MigrationAppendsHasSourceAndPreservesRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), Filename)
	raw := openRawTestDB(t, path)
	mustExec(t, raw, "CREATE TABLE version (version TEXT)")
	mustExec(t, raw, "INSERT INTO version VALUES ('1.5'), ('older duplicate')")
	mustExec(t, raw, legacyTXO15SQL)
	mustExec(t, raw, `INSERT INTO txo
        (txid, txoid, address, position, amount, script, channel_id, reposted_claim_id)
        VALUES ('tx', 'tx:0', 'address', 0, 1, x'01', 'channel', 'repost')`)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(context.Background())
	columns := tableColumns(t, database.sql, "txo")
	if got := columnNames(columns); got[len(got)-1] != "has_source" {
		t.Fatalf("migrated txo columns = %v", got)
	}
	last := columns[len(columns)-1]
	if !last.Default.Valid || last.Default.String != "1" {
		t.Fatalf("migrated has_source default = %#v, want 1", last.Default)
	}
	if got := queryInt(t, database.sql, "SELECT COUNT(*) FROM version WHERE version='1.6'"); got != 2 {
		t.Fatalf("updated version rows = %d, want 2", got)
	}
	if got := queryString(t, database.sql, "SELECT txoid FROM txo LIMIT 1"); got != "tx:0" {
		t.Fatalf("migrated sentinel row = %q", got)
	}
	if got := queryInt(t, database.sql, "SELECT has_source FROM txo LIMIT 1"); got != 1 {
		t.Fatalf("migrated row has_source = %d, want 1", got)
	}
}

func TestIncompatibleOrMissingVersionsDestructivelyReset(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		versionSetup []string
	}{
		{name: "missing version table"},
		{name: "empty version table", versionSetup: []string{"CREATE TABLE version (version TEXT)"}},
		{name: "null version", versionSetup: []string{"CREATE TABLE version (version TEXT)", "INSERT INTO version VALUES (NULL)"}},
		{name: "blob version", versionSetup: []string{"CREATE TABLE version (version TEXT)", "INSERT INTO version VALUES (CAST('1.6' AS BLOB))"}},
		{name: "unknown version", versionSetup: []string{"CREATE TABLE version (version TEXT)", "INSERT INTO version VALUES ('1.4')"}},
		{name: "first duplicate wins", versionSetup: []string{"CREATE TABLE version (version TEXT)", "INSERT INTO version VALUES ('1.4'), ('1.6')"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), Filename)
			raw := openRawTestDB(t, path)
			for _, statement := range test.versionSetup {
				mustExec(t, raw, statement)
			}
			mustExec(t, raw, "CREATE TABLE sentinel (value TEXT)")
			mustExec(t, raw, "INSERT INTO sentinel VALUES ('lost')")
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
			database, err := Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close(context.Background())
			if tableExists(t, database.sql, "sentinel") {
				t.Fatal("incompatible reset retained unrelated sentinel table")
			}
			if got := queryInt(t, database.sql, "SELECT COUNT(*) FROM version"); got != 1 {
				t.Fatalf("version rows = %d, want 1", got)
			}
			if got := queryString(t, database.sql, "SELECT version FROM version"); got != SchemaVersion {
				t.Fatalf("reset version = %q", got)
			}
		})
	}
}

func TestLifecycleErrorsAndUnopenedOperations(t *testing.T) {
	t.Parallel()

	database := New(filepath.Join(t.TempDir(), Filename))
	if database.Path() == "" {
		t.Fatal("database path was not retained")
	}
	if err := database.SetAddressHistory(context.Background(), "missing", ""); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("unopened operation error = %v", err)
	}
	if err := database.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := database.Open(context.Background()); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("second open error = %v", err)
	}
	if err := database.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(context.Background()); err != nil {
		t.Fatalf("idempotent close error = %v", err)
	}
	missingParent := New(filepath.Join(t.TempDir(), "missing", Filename))
	if err := missingParent.Open(context.Background()); err == nil {
		t.Fatal("opening below a missing directory succeeded")
	}
}

const legacyTXO15SQL = `CREATE TABLE txo (
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
    channel_id text,
    reposted_claim_id text
)`

type tableColumn struct {
	Name    string
	Default sql.NullString
}

func tableColumns(t *testing.T, database *sql.DB, table string) []tableColumn {
	t.Helper()
	rows, err := database.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []tableColumn
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, tableColumn{Name: name, Default: defaultValue})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func columnNames(columns []tableColumn) []string {
	names := make([]string, len(columns))
	for index, column := range columns {
		names[index] = column.Name
	}
	return names
}

func userTableNames(t *testing.T, database *sql.DB) []string {
	t.Helper()
	rows, err := database.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(name, "sqlite_") {
			names = append(names, name)
		}
	}
	return names
}

func explicitIndexes(t *testing.T, database *sql.DB) []string {
	t.Helper()
	rows, err := database.Query("SELECT name FROM sqlite_master WHERE type='index' AND name NOT LIKE 'sqlite_autoindex_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func foreignKeyTargets(t *testing.T, database *sql.DB, table string) []string {
	t.Helper()
	rows, err := database.Query("PRAGMA foreign_key_list(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var targets []string
	for rows.Next() {
		var id, sequence int
		var target, from, onUpdate, onDelete, match string
		var to sql.NullString
		if err := rows.Scan(&id, &sequence, &target, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets
}

func partialIndexPredicate(t *testing.T, database *sql.DB, name string) string {
	t.Helper()
	return queryString(t, database, "SELECT sql FROM sqlite_master WHERE type='index' AND name=?", name)
}

func tableExists(t *testing.T, database *sql.DB, name string) bool {
	t.Helper()
	return queryInt(t, database, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name) != 0
}

func openRawTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open(sqliteDriverName, path)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.Ping(); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	return database
}

func mustExec(t *testing.T, database *sql.DB, query string, arguments ...any) {
	t.Helper()
	if _, err := database.Exec(query, arguments...); err != nil {
		t.Fatalf("execute %q: %v", query, err)
	}
}

func queryString(t *testing.T, database *sql.DB, query string, arguments ...any) string {
	t.Helper()
	var value string
	if err := database.QueryRow(query, arguments...).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return value
}

func queryInt(t *testing.T, database *sql.DB, query string, arguments ...any) int {
	t.Helper()
	var value int
	if err := database.QueryRow(query, arguments...).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return value
}

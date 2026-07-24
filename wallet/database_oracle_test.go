package wallet

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"lbry/daemon/wallet/ledgerdb"
)

const (
	walletDatabaseOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	walletDatabaseOraclePinnedVersion = "0.113.0"
)

var walletDatabaseOraclePinnedSourceHashes = map[string]string{
	"lbry/__init__.py":                   "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/wallet/constants.py":           "099e5b3a18a70439b9d7039717f0cb61c096c5936126fe6574a4ccda600a780f",
	"lbry/wallet/database.py":            "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
	"tests/unit/wallet/test_database.py": "7af85de707b329d8715cd22419a4f761b10792a3ecc023202389dd86e3011c51",
}

var walletDatabaseOracleCaseNames = []string{
	"fresh 1.6",
	"reopen 1.6 no repair",
	"1.5 migration",
	"unknown version reset",
	"nonempty missing version reset",
	"duplicate version first row",
}

type walletDatabaseOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
	} `json:"reference"`
	Metadata struct {
		SchemaVersion    string `json:"schema_version"`
		StdlibOnly       bool   `json:"stdlib_only"`
		PythonAssertions bool   `json:"python_assertions"`
		SQLiteVersion    string `json:"sqlite_version"`
	} `json:"metadata"`
	Cases      []walletDatabaseOracleCase `json:"cases"`
	MethodCase walletDatabaseMethodCase   `json:"method_case"`
}

type walletDatabaseOracleCase struct {
	Name            string                      `json:"name"`
	JournalMode     string                      `json:"journal_mode"`
	VersionRows     []string                    `json:"version_rows"`
	Tables          []walletDatabaseOracleTable `json:"tables"`
	Indexes         []walletDatabaseOracleIndex `json:"indexes"`
	SentinelCount   *int64                      `json:"sentinel_count"`
	LegacyHasSource *int64                      `json:"legacy_has_source"`
}

type walletDatabaseOracleTable struct {
	Name    string                       `json:"name"`
	Columns []walletDatabaseOracleColumn `json:"columns"`
}

type walletDatabaseOracleColumn struct {
	CID        int64   `json:"cid"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	NotNull    bool    `json:"not_null"`
	Default    *string `json:"default"`
	PrimaryKey int64   `json:"primary_key"`
}

type walletDatabaseOracleIndex struct {
	Name    string   `json:"name"`
	Table   string   `json:"table"`
	Unique  bool     `json:"unique"`
	Partial bool     `json:"partial"`
	Columns []string `json:"columns"`
}

type walletDatabaseMethodCase struct {
	AddressRows  []walletDatabaseAddressRow  `json:"address_rows"`
	ChannelCases []walletDatabaseChannelCase `json:"channel_cases"`
}

type walletDatabaseAddressRow struct {
	Account      string  `json:"account"`
	Address      string  `json:"address"`
	Chain        int64   `json:"chain"`
	PublicKeyHex string  `json:"pubkey_hex"`
	ChainCodeHex string  `json:"chain_code_hex"`
	N            int64   `json:"n"`
	Depth        int64   `json:"depth"`
	History      *string `json:"history"`
	UsedTimes    int64   `json:"used_times"`
}

type walletDatabaseChannelCase struct {
	Name         string `json:"name"`
	Account      string `json:"account"`
	CandidateHex string `json:"candidate_hex"`
	Used         bool   `json:"used"`
}

func TestWalletDatabaseMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runWalletDatabaseOracle(t)
	if oracle.Reference.Commit != walletDatabaseOraclePinnedCommit ||
		oracle.Reference.Version != walletDatabaseOraclePinnedVersion {
		t.Fatalf("wallet database oracle reference = %#v", oracle.Reference)
	}
	if !reflect.DeepEqual(
		oracle.Reference.SourceSHA256, walletDatabaseOraclePinnedSourceHashes,
	) {
		t.Fatalf("wallet database source hashes = %#v, want %#v",
			oracle.Reference.SourceSHA256, walletDatabaseOraclePinnedSourceHashes,
		)
	}
	if oracle.Metadata.SchemaVersion != ledgerdb.SchemaVersion ||
		!oracle.Metadata.StdlibOnly || !oracle.Metadata.PythonAssertions {
		t.Fatalf("wallet database oracle metadata = %#v", oracle.Metadata)
	}

	ctx := context.Background()
	actualCases := make([]walletDatabaseOracleCase, 0, len(walletDatabaseOracleCaseNames))
	for _, name := range walletDatabaseOracleCaseNames {
		name := name
		t.Run("lifecycle/"+name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ledgerdb.Filename)
			setupWalletDatabaseCase(t, ctx, path, name)
			database := ledgerdb.New(path)
			if database.Path() != path {
				t.Fatalf("DB.Path() = %q, want %q", database.Path(), path)
			}
			if err := database.Open(ctx); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(ctx); err != nil {
				t.Fatal(err)
			}
			actualCases = append(actualCases, inspectWalletDatabaseCase(t, path, name))
		})
	}
	assertWalletDatabaseOracleEqual(t, "lifecycle", actualCases, oracle.Cases)

	actualMethods := executeWalletDatabaseMethodCase(t, ctx)
	assertWalletDatabaseOracleEqual(t, "methods", actualMethods, oracle.MethodCase)
}

func setupWalletDatabaseCase(t *testing.T, ctx context.Context, path, name string) {
	t.Helper()
	if name == "fresh 1.6" {
		return
	}
	if name != "nonempty missing version reset" {
		database, err := ledgerdb.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	connection := openWalletDatabaseRaw(t, path)
	defer connection.Close()
	switch name {
	case "reopen 1.6 no repair":
		execWalletDatabaseRaw(t, connection,
			"DROP INDEX txi_address_idx",
			"CREATE TABLE sentinel (value text)",
			"INSERT INTO sentinel VALUES ('preserved')",
		)
	case "1.5 migration":
		execWalletDatabaseRaw(t, connection,
			"ALTER TABLE txo DROP COLUMN has_source",
			"UPDATE version SET version = '1.5'",
			"INSERT INTO txo (txoid, position, amount, script, claim_name) "+
				"VALUES ('legacy:0', 0, 5, X'00', 'legacy')",
		)
	case "unknown version reset":
		execWalletDatabaseRaw(t, connection,
			"UPDATE version SET version = '9.9'",
			"CREATE TABLE sentinel (value text)",
			"INSERT INTO sentinel VALUES ('destroyed')",
		)
	case "nonempty missing version reset":
		execWalletDatabaseRaw(t, connection,
			"CREATE TABLE sentinel (value text)",
			"INSERT INTO sentinel VALUES ('destroyed')",
		)
	case "duplicate version first row":
		execWalletDatabaseRaw(t, connection,
			"INSERT INTO version VALUES ('9.9')",
			"DROP INDEX txo_claim_name_idx",
			"CREATE TABLE sentinel (value text)",
			"INSERT INTO sentinel VALUES ('preserved')",
		)
	default:
		t.Fatalf("unknown wallet database oracle case %q", name)
	}
}

func inspectWalletDatabaseCase(t *testing.T, path, name string) walletDatabaseOracleCase {
	t.Helper()
	connection := openWalletDatabaseRaw(t, path)
	defer connection.Close()
	outcome := walletDatabaseOracleCase{Name: name}
	if err := connection.QueryRow("PRAGMA journal_mode").Scan(&outcome.JournalMode); err != nil {
		t.Fatal(err)
	}
	tables := queryWalletDatabaseStrings(t, connection,
		"SELECT name FROM sqlite_master WHERE type='table' ORDER BY name",
	)
	for _, tableName := range tables {
		outcome.Tables = append(outcome.Tables, walletDatabaseOracleTable{
			Name: tableName, Columns: inspectWalletDatabaseColumns(t, connection, tableName),
		})
	}
	outcome.Indexes = inspectWalletDatabaseIndexes(t, connection)
	if walletDatabaseContains(tables, "version") {
		outcome.VersionRows = queryWalletDatabaseStrings(t, connection,
			"SELECT version FROM version ORDER BY rowid",
		)
	} else {
		outcome.VersionRows = []string{}
	}
	if walletDatabaseContains(tables, "sentinel") {
		var count int64
		if err := connection.QueryRow("SELECT COUNT(*) FROM sentinel").Scan(&count); err != nil {
			t.Fatal(err)
		}
		outcome.SentinelCount = &count
	}
	if walletDatabaseContains(tables, "txo") {
		var hasSource sql.NullInt64
		err := connection.QueryRow(
			"SELECT has_source FROM txo WHERE txoid = 'legacy:0'",
		).Scan(&hasSource)
		if err == nil && hasSource.Valid {
			value := hasSource.Int64
			outcome.LegacyHasSource = &value
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
	}
	return outcome
}

func inspectWalletDatabaseColumns(
	t *testing.T, connection *sql.DB, table string,
) []walletDatabaseOracleColumn {
	t.Helper()
	rows, err := connection.Query(fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []walletDatabaseOracleColumn
	for rows.Next() {
		var column walletDatabaseOracleColumn
		var notNull int64
		var defaultValue sql.NullString
		if err := rows.Scan(
			&column.CID, &column.Name, &column.Type, &notNull,
			&defaultValue, &column.PrimaryKey,
		); err != nil {
			t.Fatal(err)
		}
		column.NotNull = notNull != 0
		if defaultValue.Valid {
			value := defaultValue.String
			column.Default = &value
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func inspectWalletDatabaseIndexes(
	t *testing.T, connection *sql.DB,
) []walletDatabaseOracleIndex {
	t.Helper()
	rows, err := connection.Query(
		"SELECT name, tbl_name FROM sqlite_master " +
			"WHERE type='index' AND sql IS NOT NULL ORDER BY name",
	)
	if err != nil {
		t.Fatal(err)
	}
	type indexIdentity struct{ name, table string }
	var identities []indexIdentity
	for rows.Next() {
		var identity indexIdentity
		if err := rows.Scan(&identity.name, &identity.table); err != nil {
			t.Fatal(err)
		}
		identities = append(identities, identity)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var indexes []walletDatabaseOracleIndex
	for _, identity := range identities {
		index := walletDatabaseOracleIndex{Name: identity.name, Table: identity.table}
		listRows, err := connection.Query(fmt.Sprintf("PRAGMA index_list(%q)", identity.table))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for listRows.Next() {
			var sequence, unique, partial int64
			var listedName, origin string
			if err := listRows.Scan(&sequence, &listedName, &unique, &origin, &partial); err != nil {
				t.Fatal(err)
			}
			if listedName == identity.name {
				index.Unique = unique != 0
				index.Partial = partial != 0
				found = true
			}
		}
		if err := listRows.Close(); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("index %q missing from PRAGMA index_list(%q)", identity.name, identity.table)
		}
		infoRows, err := connection.Query(fmt.Sprintf("PRAGMA index_info(%q)", identity.name))
		if err != nil {
			t.Fatal(err)
		}
		for infoRows.Next() {
			var sequence, columnID int64
			var column string
			if err := infoRows.Scan(&sequence, &columnID, &column); err != nil {
				t.Fatal(err)
			}
			index.Columns = append(index.Columns, column)
		}
		if err := infoRows.Close(); err != nil {
			t.Fatal(err)
		}
		indexes = append(indexes, index)
	}
	return indexes
}

func executeWalletDatabaseMethodCase(
	t *testing.T, ctx context.Context,
) walletDatabaseMethodCase {
	t.Helper()
	path := filepath.Join(t.TempDir(), ledgerdb.Filename)
	database, err := ledgerdb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	keys := []ledgerdb.AddressKey{
		walletDatabaseAddressKey(t, "bAddress1", "02"+repeatWalletDatabaseHex("11", 32), "22", 3, 5),
		walletDatabaseAddressKey(t, "bAddress2", "03"+repeatWalletDatabaseHex("33", 32), "44", 4, 5),
		walletDatabaseAddressKey(t, "bAddress1", "03"+repeatWalletDatabaseHex("55", 32), "66", 99, 99),
	}
	if err := database.AddKeys(ctx, "account-1", keys); err != nil {
		t.Fatal(err)
	}
	other := []ledgerdb.AddressKey{
		walletDatabaseAddressKey(t, "bOtherAddress", "02"+repeatWalletDatabaseHex("77", 32), "88", 0, 1),
	}
	if err := database.AddKeys(ctx, "account-2", other); err != nil {
		t.Fatal(err)
	}
	if err := database.SetAddressHistory(ctx, "bAddress1", "a:1:b:2:c:3:"); err != nil {
		t.Fatal(err)
	}
	if err := database.SetAddressHistory(ctx, "missing", "z:9:"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(ctx); err != nil {
		t.Fatal(err)
	}

	connection := openWalletDatabaseRaw(t, path)
	seedWalletDatabaseChannels(t, connection)
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.Open(ctx); err != nil {
		t.Fatal(err)
	}
	channelCases := []walletDatabaseChannelCase{
		{Name: "owned matching key", Account: "account-1", CandidateHex: "02" + repeatWalletDatabaseHex("aa", 32)},
		{Name: "owned unused key", Account: "account-1", CandidateHex: "02" + repeatWalletDatabaseHex("cc", 32)},
		{Name: "other account does not count", Account: "account-missing", CandidateHex: "02" + repeatWalletDatabaseHex("aa", 32)},
	}
	decode := func(script []byte) ([]byte, bool, error) {
		switch string(script) {
		case "different":
			return walletDatabaseMustHex(t, "03"+repeatWalletDatabaseHex("bb", 32)), true, nil
		case "matching":
			return walletDatabaseMustHex(t, "02"+repeatWalletDatabaseHex("aa", 32)), true, nil
		default:
			return nil, false, nil
		}
	}
	for index := range channelCases {
		candidate := walletDatabaseMustHex(t, channelCases[index].CandidateHex)
		used, err := database.IsChannelKeyUsed(
			ctx, channelCases[index].Account, candidate, decode,
		)
		if err != nil {
			t.Fatal(err)
		}
		channelCases[index].Used = used
	}
	if err := database.Close(ctx); err != nil {
		t.Fatal(err)
	}

	connection = openWalletDatabaseRaw(t, path)
	defer connection.Close()
	rows, err := connection.Query(`
		SELECT account_address.account, account_address.address,
		       account_address.chain, account_address.pubkey,
		       account_address.chain_code, account_address.n,
		       account_address.depth, pubkey_address.history,
		       pubkey_address.used_times
		FROM account_address JOIN pubkey_address USING (address)
		ORDER BY account_address.account, account_address.address`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var addressRows []walletDatabaseAddressRow
	for rows.Next() {
		var row walletDatabaseAddressRow
		var publicKey, chainCode []byte
		var history sql.NullString
		if err := rows.Scan(
			&row.Account, &row.Address, &row.Chain, &publicKey, &chainCode,
			&row.N, &row.Depth, &history, &row.UsedTimes,
		); err != nil {
			t.Fatal(err)
		}
		row.PublicKeyHex = hex.EncodeToString(publicKey)
		row.ChainCodeHex = hex.EncodeToString(chainCode)
		if history.Valid {
			value := history.String
			row.History = &value
		}
		addressRows = append(addressRows, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return walletDatabaseMethodCase{AddressRows: addressRows, ChannelCases: channelCases}
}

func walletDatabaseAddressKey(
	t *testing.T, address, publicKeyHex, chainCodeByte string, n, depth int64,
) ledgerdb.AddressKey {
	t.Helper()
	return ledgerdb.AddressKey{
		Address:   address,
		Chain:     0,
		PublicKey: walletDatabaseMustHex(t, publicKeyHex),
		ChainCode: walletDatabaseMustHex(t, repeatWalletDatabaseHex(chainCodeByte, 32)),
		N:         n,
		Depth:     depth,
	}
}

func seedWalletDatabaseChannels(t *testing.T, connection *sql.DB) {
	t.Helper()
	rows := []struct {
		txid    string
		height  int64
		address string
		script  string
		txoType int64
	}{
		{"invalid", 40, "bAddress1", "invalid", ledgerdb.ChannelTXOType},
		{"different", 30, "bAddress2", "different", ledgerdb.ChannelTXOType},
		{"matching", 20, "bAddress1", "matching", ledgerdb.ChannelTXOType},
		{"wrong-type", 50, "bAddress1", "matching", 1},
		{"other-account", 60, "bOtherAddress", "matching", ledgerdb.ChannelTXOType},
	}
	for position, row := range rows {
		if _, err := connection.Exec(
			"INSERT INTO tx (txid, raw, height, position) VALUES (?, X'00', ?, ?)",
			row.txid, row.height, position,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Exec(
			"INSERT INTO txo "+
				"(txid, txoid, address, position, amount, script, txo_type) "+
				"VALUES (?, ?, ?, 0, 1, ?, ?)",
			row.txid, row.txid+":0", row.address, []byte(row.script), row.txoType,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func openWalletDatabaseRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	connection, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(1)
	if err := connection.Ping(); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	return connection
}

func execWalletDatabaseRaw(t *testing.T, connection *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := connection.Exec(statement); err != nil {
			t.Fatalf("execute %q: %v", statement, err)
		}
	}
}

func queryWalletDatabaseStrings(
	t *testing.T, connection *sql.DB, query string,
) []string {
	t.Helper()
	rows, err := connection.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

func walletDatabaseContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func repeatWalletDatabaseHex(value string, count int) string {
	var result string
	for range count {
		result += value
	}
	return result
}

func walletDatabaseMustHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func runWalletDatabaseOracle(t *testing.T) walletDatabaseOracleResponse {
	t.Helper()
	sdkRoot, script := walletDatabaseOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	payload, err := json.Marshal(map[string]any{"case_names": walletDatabaseOracleCaseNames})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python wallet database oracle failed: %v\n%s", err, stderr.String())
	}
	var oracle walletDatabaseOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode Python wallet database oracle: %v\n%s", err, output)
	}
	return oracle
}

func walletDatabaseOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate wallet database oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "wallet_database_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "lbry", "wallet", "database.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "constants.py"),
		filepath.Join(sdkRoot, "tests", "unit", "wallet", "test_database.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python wallet database source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}

func assertWalletDatabaseOracleEqual(t *testing.T, name string, got, want any) {
	t.Helper()
	if reflect.DeepEqual(got, want) {
		return
	}
	gotJSON, _ := json.MarshalIndent(got, "", "  ")
	wantJSON, _ := json.MarshalIndent(want, "", "  ")
	t.Fatalf("%s differ from pinned Python oracle\ngot:\n%s\nwant:\n%s", name, gotJSON, wantJSON)
}

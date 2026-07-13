package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"lbry/daemon/wallet/ledgerdb"
)

const (
	transactionUTXOOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	transactionUTXOOraclePinnedVersion = "0.113.0"
)

var transactionUTXOOraclePinnedSources = map[string]string{
	"lbry/__init__.py":         "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/wallet/account.py":   "ea2ca30bddf9c0145469e989d9855dbe7be5184943ae7b8ca690eda41eb7db50",
	"lbry/wallet/constants.py": "099e5b3a18a70439b9d7039717f0cb61c096c5936126fe6574a4ccda600a780f",
	"lbry/wallet/database.py":  "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
	"lbry/wallet/ledger.py":    "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
}

var transactionUTXOOraclePinnedMethods = map[string]string{
	"Account.get_balance":                             "aa33c0e7e9271ee543bbf20d29afa5de171d8ba6322d0e3a30cb79e48b9fa928",
	"Database._clean_txo_constraints_for_aggregation": "761b46a7540ad147461436682c439c3d8a0f7770b6f147443ca22f539fdf7cc7",
	"Database.get_balance":                            "7f6712bd3a394a8e59fd12ef61045721d4468785fb70b5a65288da0657cac861",
	"Database.get_txo_count":                          "0fa4a7ea182a214310f86b5780bd971f91e54de9240948ee2e1bc622a7494f1a",
	"Database.get_txo_sum":                            "c5fc698e49727203f0111f5714da0f079900da71f1f830f559a0cd0c0aa10b16",
	"Database.get_txos":                               "54eb3def5f8d9f5bbbb83cef52cbe5ba55735fb3294b8c382fcd33a68a785c01",
	"Database.get_utxo_count":                         "2b1e7fb0711a7c0914d600852cdc7482a44fbae2b42be9274b90cca6046f0279",
	"Database.get_utxos":                              "f5b83e1f73fbc6248a5f81a68a1f1944f47770f7d3c1e1b2e07021165d9e3cea",
	"Database.release_all_outputs":                    "86824a7e41900a648eb0d091a8468890260a0b66b8f13e35f9a6b6b8e6343853",
	"Database.select_txos":                            "3fbf0b31b8d3917e1b44834c8c1fbca3a0f1ab0155a87bf2e5a6e625271a2bc2",
	"Ledger.constraint_spending_utxos":                "76d9fcdcdd7deee75e5f5575ec4c61d18daea9f7122ba7872c32824404a0815e",
	"Ledger.get_utxo_count":                           "592914e51f83c12fc016e2730f9404eb5b15520ccc7da5929486864353769f4c",
	"Ledger.get_utxos":                                "4db2164d552430474feddbbf75ca55f19742bd16c5719a50abd9a023148d4757",
	"constraints_to_sql":                              "12bd52e0ff61bb1040401402c6de5cd09d31cde2484212995fd07973eed84925",
	"query":                                           "b7496b9058c2c08487def378800baf08f715615a887fc596fbce694282384b9a",
}

type transactionUTXOOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
		MethodSHA256 map[string]string `json:"method_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion            string `json:"python_version"`
		ExtractedMethodsExecuted bool   `json:"extracted_methods_executed"`
		StdlibSQLiteUsed         bool   `json:"stdlib_sqlite_used"`
		ExternalNetworkUsed      bool   `json:"external_network_used"`
	} `json:"metadata"`
	PublicUTXOs            []transactionUTXOOracleOutput `json:"public_utxos"`
	PublicUTXOsDefaultPage []transactionUTXOOracleOutput `json:"public_utxos_default_offset_2_limit_3"`
	Aggregations           struct {
		UTXOCount              int64 `json:"utxo_count_with_ignored_pagination_order"`
		TXOCount               int64 `json:"txo_count_with_ignored_pagination_order"`
		TXOSum                 int64 `json:"txo_sum_with_ignored_pagination_order"`
		OverlappingOwnersCount int64 `json:"overlapping_account_ownership_count"`
	} `json:"aggregations"`
	Balances     []transactionUTXOOracleBalance `json:"balances"`
	ReceivedTips int64                          `json:"received_tips"`
	ReleaseAll   struct {
		Before        map[string]bool `json:"before"`
		AfterAccountA map[string]bool `json:"after_account_a"`
		AfterGlobal   map[string]bool `json:"after_global"`
	} `json:"release_all"`
}

type transactionUTXOOracleOutput struct {
	TXID     string `json:"txid"`
	TXOID    string `json:"txoid"`
	Height   int64  `json:"height"`
	Position int64  `json:"position"`
	Amount   int64  `json:"amount"`
}

type transactionUTXOOracleBalance struct {
	Confirmations int64 `json:"confirmations"`
	IncludeClaims bool  `json:"include_claims"`
	Amount        int64 `json:"amount"`
}

func TestTransactionUTXOAccountingMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runTransactionUTXOOracle(t)
	if oracle.Reference.Commit != transactionUTXOOraclePinnedCommit ||
		oracle.Reference.Version != transactionUTXOOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, transactionUTXOOraclePinnedSources) ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, transactionUTXOOraclePinnedMethods) {
		t.Fatalf("transaction UTXO oracle reference = %+v", oracle.Reference)
	}
	if !oracle.Metadata.ExtractedMethodsExecuted || !oracle.Metadata.StdlibSQLiteUsed ||
		oracle.Metadata.ExternalNetworkUsed {
		t.Fatalf("transaction UTXO oracle metadata = %+v", oracle.Metadata)
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" &&
		oracle.Metadata.PythonVersion != want {
		t.Fatalf("transaction UTXO Python version = %q, want %q",
			oracle.Metadata.PythonVersion, want)
	}

	assertTransactionUTXOOracleContract(t, oracle)
	assertGoTransactionUTXOOracle(t, oracle)
}

func assertTransactionUTXOOracleContract(t *testing.T, oracle transactionUTXOOracleResponse) {
	t.Helper()
	if got, want := transactionUTXOOracleIDs(oracle.PublicUTXOs), []string{
		"u0:0", "u4:0", "c12:0", "c10:0", "c10:1", "c5:0",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Python public UTXOs = %v, want %v", got, want)
	}
	if got, want := transactionUTXOOracleIDs(oracle.PublicUTXOsDefaultPage), []string{
		"c12:0", "c10:0", "c10:1",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Python default UTXO page = %v, want %v", got, want)
	}
	if oracle.Aggregations.UTXOCount != 6 || oracle.Aggregations.TXOCount != 6 ||
		oracle.Aggregations.TXOSum != 750 || oracle.Aggregations.OverlappingOwnersCount != 6 {
		t.Fatalf("Python aggregations = %+v", oracle.Aggregations)
	}
	wantBalances := []transactionUTXOOracleBalance{
		{Confirmations: 0, Amount: 750},
		{Confirmations: 1, Amount: 540},
		{Confirmations: 2, Amount: 420},
		{Confirmations: 8, Amount: 150},
		{Confirmations: 9, Amount: 0},
		{Confirmations: 0, IncludeClaims: true, Amount: 950},
	}
	if !reflect.DeepEqual(oracle.Balances, wantBalances) {
		t.Fatalf("Python balances = %+v, want %+v", oracle.Balances, wantBalances)
	}
	if oracle.ReceivedTips != 33 {
		t.Fatalf("Python received tips = %d, want 33", oracle.ReceivedTips)
	}
	if !oracle.ReleaseAll.Before["reserved-a:0"] ||
		!oracle.ReleaseAll.Before["reserved-a-claim:0"] ||
		!oracle.ReleaseAll.Before["reserved-b:0"] ||
		!oracle.ReleaseAll.Before["reserved-unowned:0"] ||
		oracle.ReleaseAll.AfterAccountA["reserved-a:0"] ||
		oracle.ReleaseAll.AfterAccountA["reserved-a-claim:0"] ||
		!oracle.ReleaseAll.AfterAccountA["reserved-b:0"] ||
		!oracle.ReleaseAll.AfterAccountA["reserved-unowned:0"] {
		t.Fatalf("Python account-scoped release = %+v", oracle.ReleaseAll)
	}
	for txoid, reserved := range oracle.ReleaseAll.AfterGlobal {
		if reserved {
			t.Fatalf("Python global release retained %s", txoid)
		}
	}
}

func assertGoTransactionUTXOOracle(t *testing.T, oracle transactionUTXOOracleResponse) {
	t.Helper()
	ctx := context.Background()
	database, ledger, account := transactionUTXOOracleFixture(t)
	unspent := false
	databaseQuery := ledgerdb.OutputQuery{
		AccountIDs: []string{"account-a"},
		Types:      []int64{TransactionOutputTypeOther, TransactionOutputTypePurchase},
		IsSpent:    &unspent,
	}
	rows, err := database.ListOutputs(ctx, databaseQuery)
	if err != nil {
		t.Fatal(err)
	}
	if got := transactionUTXOOracleRows(rows); !reflect.DeepEqual(got, oracle.PublicUTXOs) {
		t.Fatalf("Go ledger DB public UTXOs = %+v, Python %+v", got, oracle.PublicUTXOs)
	}
	limit, offset := 3, 2
	databaseQuery.Limit = &limit
	databaseQuery.Offset = &offset
	rows, err = database.ListOutputs(ctx, databaseQuery)
	if err != nil {
		t.Fatal(err)
	}
	if got := transactionUTXOOracleRows(rows); !reflect.DeepEqual(got, oracle.PublicUTXOsDefaultPage) {
		t.Fatalf("Go ledger DB default UTXO page = %+v, Python %+v",
			got, oracle.PublicUTXOsDefaultPage)
	}

	outputs, err := account.GetUTXOs(ctx, ledgerdb.OutputQuery{Types: []int64{1}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionUTXOOracleGoOutputs(outputs), transactionUTXOOracleShape(oracle.PublicUTXOs); !reflect.DeepEqual(got, want) {
		t.Fatalf("Go public UTXOs = %+v, Python %+v", got, want)
	}

	outputs, err = account.GetUTXOs(ctx, ledgerdb.OutputQuery{
		Types: []int64{1}, Limit: &limit, Offset: &offset,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionUTXOOracleGoOutputs(outputs), transactionUTXOOracleShape(oracle.PublicUTXOsDefaultPage); !reflect.DeepEqual(got, want) {
		t.Fatalf("Go default UTXO page = %+v, Python %+v", got, want)
	}

	zero, farOffset := 0, 999
	aggregateQuery := ledgerdb.OutputQuery{
		AccountIDs: []string{"account-a"},
		Types:      []int64{TransactionOutputTypeOther, TransactionOutputTypePurchase},
		IsSpent:    transactionUTXOOracleBool(false),
		Limit:      &zero,
		Offset:     &farOffset,
		Order:      ledgerdb.OutputOrderNone,
	}
	if got, err := account.GetUTXOCount(ctx, aggregateQuery); err != nil ||
		got != oracle.Aggregations.UTXOCount {
		t.Fatalf("Go UTXO count = %d, %v; Python %d", got, err, oracle.Aggregations.UTXOCount)
	}
	if got, err := ledger.CountTransactionOutputs(ctx, aggregateQuery); err != nil ||
		got != oracle.Aggregations.TXOCount {
		t.Fatalf("Go TXO count = %d, %v; Python %d", got, err, oracle.Aggregations.TXOCount)
	}
	if got, err := ledger.SumTransactionOutputs(ctx, aggregateQuery); err != nil ||
		got != oracle.Aggregations.TXOSum {
		t.Fatalf("Go TXO sum = %d, %v; Python %d", got, err, oracle.Aggregations.TXOSum)
	}
	overlappingQuery := aggregateQuery
	overlappingQuery.AccountIDs = []string{"account-a", "account-c"}
	if got, err := ledger.CountTransactionOutputs(ctx, overlappingQuery); err != nil ||
		got != oracle.Aggregations.OverlappingOwnersCount {
		t.Fatalf("Go overlapping-owner count = %d, %v; Python %d",
			got, err, oracle.Aggregations.OverlappingOwnersCount)
	}

	for _, want := range oracle.Balances {
		query := ledgerdb.OutputQuery{}
		if !want.IncludeClaims {
			// The public non-claim path must overwrite caller types.
			query.Types = []int64{TransactionOutputTypeStream}
		}
		got, err := account.GetBalance(ctx, AccountBalanceOptions{
			Confirmations: want.Confirmations,
			IncludeClaims: want.IncludeClaims,
			Query:         query,
		})
		if err != nil || got != want.Amount {
			t.Fatalf("Go balance confirmations=%d include_claims=%t = %d, %v; Python %d",
				want.Confirmations, want.IncludeClaims, got, err, want.Amount)
		}
	}

	if got := transactionUTXOOracleReservationState(t, database); !reflect.DeepEqual(got, oracle.ReleaseAll.Before) {
		t.Fatalf("Go reservations before release = %+v, Python %+v", got, oracle.ReleaseAll.Before)
	}
	if err := account.ReleaseAllOutputs(ctx); err != nil {
		t.Fatal(err)
	}
	if got := transactionUTXOOracleReservationState(t, database); !reflect.DeepEqual(got, oracle.ReleaseAll.AfterAccountA) {
		t.Fatalf("Go reservations after account release = %+v, Python %+v",
			got, oracle.ReleaseAll.AfterAccountA)
	}
	if err := ledger.ReleaseAllOutputs(ctx); err != nil {
		t.Fatal(err)
	}
	if got := transactionUTXOOracleReservationState(t, database); !reflect.DeepEqual(got, oracle.ReleaseAll.AfterGlobal) {
		t.Fatalf("Go reservations after global release = %+v, Python %+v",
			got, oracle.ReleaseAll.AfterGlobal)
	}
	transactionUTXOOracleAssertReceivedTips(t, database, oracle.ReceivedTips)
}

func transactionUTXOOracleAssertReceivedTips(t *testing.T, database *ledgerdb.DB, want int64) {
	t.Helper()
	ctx := context.Background()
	claimID, otherClaimID := "wanted", "other"
	fixtures := []struct {
		txid, address   string
		amount, txoType int64
		claimID         *string
	}{
		{"tip-target", "a1", 1, 1, &claimID},
		{"tip-a1", "a1", 11, 3, &claimID},
		{"tip-a2", "a2", 22, 3, &claimID},
		{"tip-foreign", "b1", 33, 3, &claimID},
		{"tip-spent", "a1", 44, 3, &claimID},
		{"tip-wrong-type", "a1", 55, 1, &claimID},
		{"tip-wrong-claim", "a1", 66, 3, &otherClaimID},
	}
	rows := make([]ledgerdb.TransactionIORow, 0, len(fixtures)+1)
	for _, fixture := range fixtures {
		address := fixture.address
		rows = append(rows, ledgerdb.TransactionIORow{
			Transaction: ledgerdb.TransactionRow{
				TXID: fixture.txid, Raw: []byte("raw"), Height: 1,
			},
			Outputs: []ledgerdb.TransactionOutputRow{{
				TXOID: fixture.txid + ":0", Address: &address, Amount: fixture.amount,
				Script: []byte("script"), TXOType: fixture.txoType, ClaimID: fixture.claimID,
			}},
		})
	}
	rows = append(rows, ledgerdb.TransactionIORow{
		Transaction: ledgerdb.TransactionRow{TXID: "tip-spender", Raw: []byte("raw")},
		Inputs:      []ledgerdb.TransactionInputRow{{TXOID: "tip-spent:0", Position: 0}},
	})
	if err := database.SaveTransactionIOBatch(ctx, rows, "a1", ""); err != nil {
		t.Fatal(err)
	}
	outputs, err := database.ListOutputs(ctx, ledgerdb.OutputQuery{
		TXID: "tip-target", AnnotationAccountIDs: []string{"account-a"},
		IncludeReceivedTips: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 || outputs[0].ReceivedTips == nil || *outputs[0].ReceivedTips != want {
		t.Fatalf("Go received tips = %#v, want %d", outputs, want)
	}
}

func transactionUTXOOracleFixture(t *testing.T) (*ledgerdb.DB, *Ledger, *Account) {
	t.Helper()
	ctx := context.Background()
	database, err := ledgerdb.Open(ctx, filepath.Join(t.TempDir(), ledgerdb.Filename))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Errorf("close transaction UTXO oracle database: %v", err)
		}
	})
	addKeys := func(accountID string, addresses ...string) {
		t.Helper()
		keys := make([]ledgerdb.AddressKey, len(addresses))
		for index, address := range addresses {
			keys[index] = ledgerdb.AddressKey{
				Address: address, PublicKey: []byte{1}, ChainCode: []byte{2},
				N: int64(index),
			}
		}
		if err := database.AddKeys(ctx, accountID, keys); err != nil {
			t.Fatal(err)
		}
	}
	addKeys("account-a", "a1", "a2")
	addKeys("account-b", "b1")
	addKeys("account-c", "a1")

	type fixtureOutput struct {
		txoid    string
		address  string
		amount   int64
		reserved bool
		txoType  int64
	}
	type fixtureTransaction struct {
		txid     string
		height   int64
		position int64
		outputs  []fixtureOutput
	}
	fixtures := []fixtureTransaction{
		{"u0", 0, 5, []fixtureOutput{{"u0:0", "a1", 100, false, 0}}},
		{"u4", -1, 6, []fixtureOutput{{"u4:0", "a1", 110, false, 4}}},
		{"c12", 12, 4, []fixtureOutput{{"c12:0", "a1", 120, false, 0}}},
		{"c10", 10, 8, []fixtureOutput{
			{"c10:0", "a2", 140, false, 4},
			{"c10:1", "a1", 130, false, 0},
		}},
		{"c5", 5, 1, []fixtureOutput{{"c5:0", "a2", 150, false, 4}}},
		{"claim", 11, 2, []fixtureOutput{{"claim:0", "a1", 200, false, 1}}},
		{"reserved-a", 9, 3, []fixtureOutput{{"reserved-a:0", "a1", 300, true, 0}}},
		{"reserved-a-claim", 8, 2, []fixtureOutput{{"reserved-a-claim:0", "a2", 310, true, 1}}},
		{"spent", 7, 1, []fixtureOutput{{"spent:0", "a1", 400, false, 4}}},
		{"b-only", 6, 1, []fixtureOutput{{"b-only:0", "b1", 500, false, 0}}},
		{"reserved-b", 4, 1, []fixtureOutput{{"reserved-b:0", "b1", 510, true, 4}}},
		{"reserved-unowned", 3, 1, []fixtureOutput{{"reserved-unowned:0", "z1", 520, true, 0}}},
	}
	rows := make([]ledgerdb.TransactionIORow, 0, len(fixtures)+1)
	reservedIDs := make([]string, 0, 4)
	for fixtureIndex, fixture := range fixtures {
		outputs := make([]TransactionOutput, len(fixture.outputs))
		for index, output := range fixture.outputs {
			outputs[index] = NewPayPubKeyHashOutput(
				uint64(output.amount), bytes.Repeat([]byte{byte(fixtureIndex + 1)}, 20),
			)
		}
		transaction := NewTransaction().AddInputs([]TransactionInput{{
			PreviousIndex: math.MaxUint32,
			Sequence:      math.MaxUint32,
			Coinbase:      []byte{byte(fixtureIndex + 1)},
		}}).AddOutputs(outputs)
		row := ledgerdb.TransactionIORow{Transaction: ledgerdb.TransactionRow{
			TXID: fixture.txid, Raw: transaction.Raw, Height: fixture.height,
			Position: fixture.position, IsVerified: fixture.height > 0,
		}}
		for index, output := range fixture.outputs {
			address := output.address
			row.Outputs = append(row.Outputs, ledgerdb.TransactionOutputRow{
				TXOID: output.txoid, Address: &address, Position: int64(index),
				Amount: output.amount, Script: transaction.Outputs[index].Script.Source,
				TXOType: output.txoType,
			})
			if output.reserved {
				reservedIDs = append(reservedIDs, output.txoid)
			}
		}
		rows = append(rows, row)
	}
	spender := NewTransaction().AddInputs([]TransactionInput{{
		PreviousIndex: math.MaxUint32, Sequence: math.MaxUint32, Coinbase: []byte{0xff},
	}})
	rows = append(rows, ledgerdb.TransactionIORow{
		Transaction: ledgerdb.TransactionRow{
			TXID: "spender", Raw: spender.Raw, Height: 0, Position: 0,
		},
		Inputs: []ledgerdb.TransactionInputRow{{TXOID: "spent:0", Position: 0}},
	})
	if err := database.SaveTransactionIOBatch(ctx, rows, "a1", ""); err != nil {
		t.Fatal(err)
	}
	if err := database.ReserveOutputs(ctx, reservedIDs, true); err != nil {
		t.Fatal(err)
	}

	ledger := &Ledger{Database: database, Headers: &Headers{size: 13}}
	account := &Account{ID: "account-a", ledger: ledger}
	return database, ledger, account
}

func transactionUTXOOracleReservationState(t *testing.T, database *ledgerdb.DB) map[string]bool {
	t.Helper()
	rows, err := database.ListOutputs(context.Background(), ledgerdb.OutputQuery{
		Order: ledgerdb.OutputOrderNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := make(map[string]bool, len(rows))
	for _, row := range rows {
		state[row.TXOID] = row.IsReserved
	}
	return state
}

func transactionUTXOOracleGoOutputs(outputs []*TransactionOutput) []transactionUTXOOracleOutput {
	result := make([]transactionUTXOOracleOutput, len(outputs))
	for index, output := range outputs {
		result[index] = transactionUTXOOracleOutput{
			Height: output.owner.Height, Position: int64(output.Position), Amount: int64(output.Amount),
		}
	}
	return result
}

func transactionUTXOOracleRows(rows []ledgerdb.OutputRow) []transactionUTXOOracleOutput {
	result := make([]transactionUTXOOracleOutput, len(rows))
	for index, row := range rows {
		result[index] = transactionUTXOOracleOutput{
			TXID: row.TXID, TXOID: row.TXOID, Height: row.Height,
			Position: row.OutputPosition, Amount: row.Amount,
		}
	}
	return result
}

func transactionUTXOOracleShape(outputs []transactionUTXOOracleOutput) []transactionUTXOOracleOutput {
	result := make([]transactionUTXOOracleOutput, len(outputs))
	for index, output := range outputs {
		result[index] = transactionUTXOOracleOutput{
			Height: output.Height, Position: output.Position, Amount: output.Amount,
		}
	}
	return result
}

func transactionUTXOOracleIDs(outputs []transactionUTXOOracleOutput) []string {
	ids := make([]string, len(outputs))
	for index, output := range outputs {
		ids[index] = output.TXOID
	}
	return ids
}

func transactionUTXOOracleBool(value bool) *bool {
	return &value
}

func runTransactionUTXOOracle(t *testing.T) transactionUTXOOracleResponse {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate transaction UTXO oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	for relative := range transactionUTXOOraclePinnedSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local transaction UTXO source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(daemonRoot, "compat", "transaction_utxo_oracle.py")
	if _, err := os.Stat(script); errors.Is(err, os.ErrNotExist) {
		t.Skipf("transaction UTXO oracle script is unavailable: %s", script)
	} else if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("transaction UTXO oracle failed: %v\n%s", err, output)
	}
	var oracle transactionUTXOOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode transaction UTXO oracle: %v\n%s", err, output)
	}
	return oracle
}

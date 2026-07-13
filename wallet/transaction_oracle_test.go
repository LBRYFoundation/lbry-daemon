package wallet

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

const (
	transactionOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	transactionOraclePinnedVersion = "0.113.0"
)

var transactionOraclePinnedSources = map[string]string{
	"lbry/__init__.py":                      "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/crypto/hash.py":                   "bfc430bd3fe98578b406caa3a8e2116a40f492c7b68e269176e838b4ef426a72",
	"lbry/wallet/bcd_data_stream.py":        "ce1f81aaa823d30954959cfc294520b6c774f65b174ab381aeb079a2277ac292",
	"lbry/wallet/hash.py":                   "bac0ea401bef9481aba1bcfffb826326a4aaef094b0df3ac84d074e9941eb92d",
	"lbry/wallet/script.py":                 "bbfdeb4a2401f26eca81acd27c598cafb6ca7737fb5af195d8508dbf81c05c6d",
	"lbry/wallet/transaction.py":            "e73491aeb915fbce931acbb4d9631f3e05440a7d26c598db85e66e524a798d15",
	"tests/unit/wallet/test_script.py":      "128b1d2afa9a02796eae7d8d1254fe46c2e35669d5e77344cc1bbc67ee64231f",
	"tests/unit/wallet/test_transaction.py": "738b0b5010d7671a2cf1dd47024879cf9585204b088ae920de53b35f2a99130e",
}

type transactionOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion                  string `json:"python_version"`
		TransactionDeserializeExecuted bool   `json:"transaction_deserialize_executed"`
		MutableHashExecuted            bool   `json:"mutable_hash_executed"`
		ScriptParserExecuted           bool   `json:"script_parser_executed"`
		InputScriptParserExecuted      bool   `json:"input_script_parser_executed"`
		FixtureSource                  string `json:"fixture_source"`
	} `json:"metadata"`
	Transactions []transactionOracleTransaction `json:"transactions"`
	Scripts      []transactionOracleScript      `json:"scripts"`
	InputScripts []transactionOracleScript      `json:"input_scripts"`
}

type transactionOracleTransaction struct {
	Name             string                    `json:"name"`
	RawHex           string                    `json:"raw_hex"`
	OK               bool                      `json:"ok"`
	Version          uint32                    `json:"version"`
	LockTime         uint32                    `json:"locktime"`
	SegWitFlag       byte                      `json:"segwit_flag"`
	RawSansSegWitHex string                    `json:"raw_sans_segwit_hex"`
	ResetRawHex      string                    `json:"reset_raw_hex"`
	HashHex          string                    `json:"hash_hex"`
	ID               string                    `json:"id"`
	Height           int64                     `json:"height"`
	Position         int64                     `json:"position"`
	IsVerified       bool                      `json:"is_verified"`
	WitnessesHex     []string                  `json:"witnesses_hex"`
	TrailingHex      string                    `json:"trailing_hex"`
	Inputs           []transactionOracleInput  `json:"inputs"`
	Outputs          []transactionOracleOutput `json:"outputs"`
	ErrorType        *string                   `json:"error_type"`
	ErrorMessage     *string                   `json:"error_message"`
}

type transactionOracleInput struct {
	Position        uint32                   `json:"position"`
	PreviousHashHex string                   `json:"previous_hash_hex"`
	PreviousID      string                   `json:"previous_id"`
	PreviousIndex   uint32                   `json:"previous_index"`
	Sequence        uint32                   `json:"sequence"`
	CoinbaseHex     *string                  `json:"coinbase_hex"`
	Script          *transactionOracleScript `json:"script"`
}

type transactionOracleOutput struct {
	ID       string                  `json:"id"`
	Position uint32                  `json:"position"`
	Amount   uint64                  `json:"amount"`
	Script   transactionOracleScript `json:"script"`
	ClaimID  *string                 `json:"claim_id"`
}

type transactionOracleScript struct {
	Name           string                      `json:"name"`
	SourceHex      string                      `json:"source_hex"`
	OK             bool                        `json:"ok"`
	Template       string                      `json:"template"`
	Values         map[string]string           `json:"values"`
	SignaturesHex  []string                    `json:"signatures_hex"`
	Subscript      *transactionOracleSubscript `json:"subscript"`
	HasPubKeyHash  bool                        `json:"has_pubkey_hash"`
	HasScriptHash  bool                        `json:"has_script_hash"`
	IsClaimName    bool                        `json:"is_claim_name"`
	IsUpdateClaim  bool                        `json:"is_update_claim"`
	IsSupportClaim bool                        `json:"is_support_claim"`
	IsSupportData  bool                        `json:"is_support_data"`
	ErrorType      *string                     `json:"error_type"`
	ErrorMessage   *string                     `json:"error_message"`
}

type transactionOracleSubscript struct {
	SourceHex       string   `json:"source_hex"`
	Template        string   `json:"template"`
	Height          string   `json:"height"`
	PubKeyHashHex   string   `json:"pubkey_hash_hex"`
	SignaturesCount uint8    `json:"signatures_count"`
	PublicKeysHex   []string `json:"pubkeys_hex"`
	PublicKeysCount uint8    `json:"pubkeys_count"`
}

func TestTransactionsMatchPinnedPythonOracle(t *testing.T) {
	oracle := runTransactionOracle(t)
	if oracle.Reference.Commit != transactionOraclePinnedCommit ||
		oracle.Reference.Version != transactionOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, transactionOraclePinnedSources) {
		t.Fatalf("transaction oracle reference = %+v", oracle.Reference)
	}
	metadata := oracle.Metadata
	if !metadata.TransactionDeserializeExecuted || !metadata.MutableHashExecuted ||
		!metadata.ScriptParserExecuted || !metadata.InputScriptParserExecuted ||
		metadata.FixtureSource != "tests/unit/wallet/test_transaction.py" {
		t.Fatalf("transaction oracle metadata = %+v", metadata)
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" && metadata.PythonVersion != want {
		t.Fatalf("transaction oracle Python version = %q, want %q", metadata.PythonVersion, want)
	}
	if len(oracle.Transactions) != 11 || len(oracle.Scripts) != 20 || len(oracle.InputScripts) != 9 {
		t.Fatalf(
			"transaction oracle fixture counts = %d transactions, %d output scripts, %d input scripts",
			len(oracle.Transactions), len(oracle.Scripts), len(oracle.InputScripts),
		)
	}

	for _, expected := range oracle.Transactions {
		expected := expected
		t.Run("transaction/"+expected.Name, func(t *testing.T) {
			assertTransactionOracleCase(t, expected)
		})
	}
	for _, expected := range oracle.Scripts {
		expected := expected
		t.Run("script/"+expected.Name, func(t *testing.T) {
			source := transactionOracleDecodeHex(t, expected.SourceHex)
			assertTransactionOracleOutputScript(t, expected, ParseTransactionOutputScript(source))
		})
	}
	for _, expected := range oracle.InputScripts {
		expected := expected
		t.Run("input_script/"+expected.Name, func(t *testing.T) {
			source := transactionOracleDecodeHex(t, expected.SourceHex)
			assertTransactionOracleInputScript(t, expected, ParseTransactionInputScript(source))
		})
	}
}

func assertTransactionOracleCase(t *testing.T, expected transactionOracleTransaction) {
	t.Helper()
	raw := transactionOracleDecodeHex(t, expected.RawHex)
	transaction, err := ParseTransaction(raw)
	if !expected.OK {
		if err == nil || !errors.Is(err, ErrInvalidWalletTransaction) {
			t.Fatalf("Go accepted Python-rejected transaction: %#v, %v", transaction, err)
		}
		if expected.ErrorType == nil || expected.ErrorMessage == nil {
			t.Fatalf("Python rejection lacks an error: %+v", expected)
		}
		return
	}
	if err != nil {
		t.Fatalf("Go rejected Python-accepted transaction: %v", err)
	}
	if !bytes.Equal(transaction.Raw, raw) || transaction.Version != expected.Version ||
		transaction.LockTime != expected.LockTime || transaction.SegWitFlag != expected.SegWitFlag ||
		transaction.ID != expected.ID || hex.EncodeToString(transaction.Hash[:]) != expected.HashHex ||
		transaction.Height != expected.Height || transaction.Position != expected.Position ||
		transaction.IsVerified != expected.IsVerified {
		t.Fatalf("Go transaction header differs\nGo: %#v\nPython: %+v", transaction, expected)
	}
	if got := hex.EncodeToString(transaction.RawSansSegWit); got != expected.RawSansSegWitHex {
		t.Fatalf("raw sans SegWit = %s, want %s", got, expected.RawSansSegWitHex)
	}
	if got := hex.EncodeToString(transaction.serializeSansSegWit()); got != expected.ResetRawHex {
		t.Fatalf("canonical reset raw = %s, want %s", got, expected.ResetRawHex)
	}
	if got := hex.EncodeToString(transaction.Trailing); got != expected.TrailingHex {
		t.Fatalf("trailing bytes = %s, want %s", got, expected.TrailingHex)
	}
	if got := transactionOracleHexSlices(transaction.Witnesses); !reflect.DeepEqual(got, expected.WitnessesHex) {
		t.Fatalf("witnesses = %#v, want %#v", got, expected.WitnessesHex)
	}
	if len(transaction.Inputs) != len(expected.Inputs) || len(transaction.Outputs) != len(expected.Outputs) {
		t.Fatalf("input/output counts = %d/%d, want %d/%d", len(transaction.Inputs), len(transaction.Outputs), len(expected.Inputs), len(expected.Outputs))
	}
	for index, want := range expected.Inputs {
		got := transaction.Inputs[index]
		if got.Position != want.Position || got.PreviousIndex != want.PreviousIndex ||
			got.Sequence != want.Sequence || got.PreviousTxID != want.PreviousID ||
			hex.EncodeToString(got.PreviousHash[:]) != want.PreviousHashHex {
			t.Fatalf("input %d differs\nGo: %#v\nPython: %+v", index, got, want)
		}
		if want.CoinbaseHex == nil {
			if got.Coinbase != nil {
				t.Fatalf("input %d coinbase = %x, want nil", index, got.Coinbase)
			}
		} else if hex.EncodeToString(got.Coinbase) != *want.CoinbaseHex {
			t.Fatalf("input %d coinbase = %x, want %s", index, got.Coinbase, *want.CoinbaseHex)
		}
		if want.Script == nil {
			if got.Script.Source != nil || got.Script.Template != "" || got.Script.Err != nil {
				t.Fatalf("coinbase input script = %#v", got.Script)
			}
		} else {
			assertTransactionOracleInputScript(t, *want.Script, got.Script)
		}
	}
	for index, want := range expected.Outputs {
		got := transaction.Outputs[index]
		if got.Position != want.Position || got.Amount != want.Amount ||
			got.TransactionID != transaction.ID || got.TransactionHash != transaction.Hash ||
			got.ID() != want.ID {
			t.Fatalf("output %d differs\nGo: %#v\nPython: %+v", index, got, want)
		}
		if want.ClaimID != nil {
			claimID, err := got.ClaimID()
			if err != nil || claimID != *want.ClaimID {
				t.Fatalf("output %d claim ID = %q, %v; want %q", index, claimID, err, *want.ClaimID)
			}
		}
		assertTransactionOracleOutputScript(t, want.Script, got.Script)
	}
}

func assertTransactionOracleInputScript(
	t *testing.T, expected transactionOracleScript, actual TransactionInputScript,
) {
	t.Helper()
	if hex.EncodeToString(actual.Source) != expected.SourceHex {
		t.Fatalf("input script source = %x, want %s", actual.Source, expected.SourceHex)
	}
	if !expected.OK {
		if actual.Err == nil || !errors.Is(actual.Err, ErrInvalidTransactionScript) {
			t.Fatalf("Go accepted Python-rejected input script: %#v", actual)
		}
		return
	}
	if actual.Err != nil || actual.Template != expected.Template {
		t.Fatalf("input script = %#v, Python %+v", actual, expected)
	}
	values := make(map[string]string)
	switch actual.Template {
	case TransactionInputPubKey:
		values["signature"] = hex.EncodeToString(actual.Signature)
	case TransactionInputPubKeyHash, TransactionInputScriptHashTime:
		values["signature"] = hex.EncodeToString(actual.Signature)
		values["pubkey"] = hex.EncodeToString(actual.PublicKey)
	}
	if !reflect.DeepEqual(values, expected.Values) {
		t.Fatalf("input script values = %#v, want %#v", values, expected.Values)
	}
	if got := transactionOracleHexSlices(actual.Signatures); !reflect.DeepEqual(got, expected.SignaturesHex) {
		t.Fatalf("input script signatures = %#v, want %#v", got, expected.SignaturesHex)
	}
	assertTransactionOracleSubscript(t, expected.Subscript, actual.Script)
}

func assertTransactionOracleSubscript(
	t *testing.T, expected *transactionOracleSubscript, actual *TransactionInputSubscript,
) {
	t.Helper()
	if expected == nil {
		if actual != nil {
			t.Fatalf("input subscript = %#v, want nil", actual)
		}
		return
	}
	if actual == nil {
		t.Fatalf("input subscript = nil, want %+v", expected)
	}
	height := ""
	if actual.Height != nil {
		height = actual.Height.String()
	}
	if hex.EncodeToString(actual.Source) != expected.SourceHex || actual.Template != expected.Template ||
		height != expected.Height || hex.EncodeToString(actual.PubKeyHash) != expected.PubKeyHashHex ||
		actual.SignaturesCount != expected.SignaturesCount ||
		!reflect.DeepEqual(transactionOracleHexSlices(actual.PublicKeys), expected.PublicKeysHex) ||
		actual.PublicKeysCount != expected.PublicKeysCount {
		t.Fatalf("input subscript = %#v, Python %+v", actual, expected)
	}
}

func assertTransactionOracleOutputScript(
	t *testing.T, expected transactionOracleScript, actual TransactionOutputScript,
) {
	t.Helper()
	if hex.EncodeToString(actual.Source) != expected.SourceHex {
		t.Fatalf("output script source = %x, want %s", actual.Source, expected.SourceHex)
	}
	if !expected.OK {
		if actual.Err == nil || !errors.Is(actual.Err, ErrInvalidTransactionScript) {
			t.Fatalf("Go accepted Python-rejected output script: %#v", actual)
		}
		return
	}
	if actual.Err != nil || actual.Template != expected.Template ||
		actual.HasPubKeyHash != expected.HasPubKeyHash ||
		actual.HasScriptHash != expected.HasScriptHash ||
		actual.IsClaimName() != expected.IsClaimName ||
		actual.IsUpdateClaim() != expected.IsUpdateClaim ||
		actual.IsSupportClaim() != expected.IsSupportClaim ||
		actual.IsSupportData() != expected.IsSupportData {
		t.Fatalf("output script = %#v, Python %+v", actual, expected)
	}
	values := transactionOracleOutputValues(actual)
	if !reflect.DeepEqual(values, expected.Values) {
		t.Fatalf("output script values = %#v, want %#v", values, expected.Values)
	}
}

func transactionOracleOutputValues(script TransactionOutputScript) map[string]string {
	values := make(map[string]string)
	if script.Template == TransactionScriptPayPubKeyFull {
		values["pubkey"] = hex.EncodeToString(script.PublicKey)
	}
	if script.HasPubKeyHash {
		values["pubkey_hash"] = hex.EncodeToString(script.PubKeyHash)
	}
	if script.HasScriptHash {
		values["script_hash"] = hex.EncodeToString(script.ScriptHash)
	}
	if script.Template == TransactionScriptReturnData {
		values["data"] = hex.EncodeToString(script.Data)
	}
	if script.IsClaimInvolved() {
		values["claim_name"] = hex.EncodeToString(script.ClaimName)
	}
	if script.IsUpdateClaim() || script.IsSupportClaim() {
		values["claim_id"] = hex.EncodeToString(script.ClaimID)
	}
	if script.IsClaimName() || script.IsUpdateClaim() {
		values["claim"] = hex.EncodeToString(script.Claim)
	}
	if script.HasSupportData {
		values["support"] = hex.EncodeToString(script.Support)
	}
	return values
}

func transactionOracleDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func transactionOracleHexSlices(values [][]byte) []string {
	encoded := make([]string, len(values))
	for index, value := range values {
		encoded[index] = hex.EncodeToString(value)
	}
	return encoded
}

func runTransactionOracle(t *testing.T) transactionOracleResponse {
	t.Helper()
	sdkRoot, script := transactionOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python transaction oracle failed: %v\n%s", err, output)
	}
	var oracle transactionOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode transaction oracle: %v\n%s", err, output)
	}
	return oracle
}

func transactionOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate transaction oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "transaction_oracle.py")
	for relative := range transactionOraclePinnedSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local transaction source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(script); errors.Is(err, os.ErrNotExist) {
		t.Skipf("transaction oracle script is unavailable: %s", script)
	} else if err != nil {
		t.Fatal(err)
	}
	return sdkRoot, script
}

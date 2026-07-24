package wallet

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

const (
	headersOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	headersOraclePinnedVersion = "0.113.0"
	headersOracleMaxTargetHex  = "0000ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

var headersOraclePinnedSourceHashes = map[string]string{
	"lbry/__init__.py":                  "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/wallet/header.py":             "139376a70a383bb8b265b377b50abc959e370f7d7678614c938ab3ac65824a54",
	"lbry/wallet/checkpoints.py":        "1301ad68706ca8d25d00a44517767c794cda1e7dc7dd8a216342b91febd1e011",
	"lbry/wallet/util.py":               "08f697c88ec36d2bb417609194266f279eba2f69b1a62a10b1de69b9c1733d5a",
	"lbry/crypto/hash.py":               "bfc430bd3fe98578b406caa3a8e2116a40f492c7b68e269176e838b4ef426a72",
	"tests/unit/wallet/test_headers.py": "e706f1709427131147dbf76d69199e7291b9ebebb5b8618fca942659f769998b",
	"tests/unit/wallet/test_utils.py":   "3008c26e38b8b62aba48214ab5b9de54f180d97dea76037005c3f0cc8a7cb4ce",
}

type headersOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
	} `json:"reference"`
	Metadata struct {
		HeaderSize       int             `json:"header_size"`
		FixtureHeaders   int             `json:"fixture_headers"`
		StdlibOnly       bool            `json:"stdlib_only"`
		PythonAssertions bool            `json:"python_assertions"`
		SelfChecks       map[string]bool `json:"adapter_self_checks"`
		Checkpoints      struct {
			Count       int    `json:"count"`
			Interval    int    `json:"interval"`
			FirstHeight int    `json:"first_height"`
			LastHeight  int    `json:"last_height"`
			FirstHash   string `json:"first_hash"`
			LastHash    string `json:"last_hash"`
			RawSize     int    `json:"raw_size"`
			RawSHA256   string `json:"raw_sha256"`
		} `json:"checkpoints"`
	} `json:"metadata"`
	HeaderCases  []headersOracleHeaderCase  `json:"header_cases"`
	HashCases    []headersOracleHashCase    `json:"hash_cases"`
	PoWCases     []headersOraclePoWCase     `json:"pow_cases"`
	CompactCases []headersOracleCompactCase `json:"compact_cases"`
	TargetCases  []headersOracleTargetCase  `json:"target_cases"`
	ChainCases   []headersOracleChainCase   `json:"chain_cases"`
}

func TestMainnetCheckpointsMatchPinnedPythonOracle(t *testing.T) {
	oracle := runHeadersOracle(t, map[string]any{})
	headersOracleAssertReference(t, oracle)
	checkpoints := oracle.Metadata.Checkpoints
	if checkpoints.Count != mainnetCheckpoints.len() ||
		checkpoints.Interval != checkpointInterval ||
		checkpoints.FirstHeight != 0 ||
		checkpoints.LastHeight != mainnetCheckpoints.lastHeight() ||
		checkpoints.RawSize != len(mainnetCheckpointData) {
		t.Fatalf(
			"checkpoint oracle dimensions = count:%d interval:%d heights:%d..%d bytes:%d",
			checkpoints.Count, checkpoints.Interval, checkpoints.FirstHeight,
			checkpoints.LastHeight, checkpoints.RawSize,
		)
	}
	if checkpoints.RawSHA256 != mainnetCheckpointDataSHA256 {
		t.Fatalf("checkpoint oracle raw SHA-256 = %s, want %s",
			checkpoints.RawSHA256, mainnetCheckpointDataSHA256,
		)
	}
	assertCheckpointLookup(t, checkpoints.FirstHeight, checkpoints.FirstHash)
	assertCheckpointLookup(t, checkpoints.LastHeight, checkpoints.LastHash)
}

type headersOracleHeader struct {
	Version       uint32 `json:"version"`
	PreviousHash  string `json:"prev_block_hash"`
	MerkleRoot    string `json:"merkle_root"`
	ClaimTrieRoot string `json:"claim_trie_root"`
	Timestamp     uint32 `json:"timestamp"`
	Bits          uint32 `json:"bits"`
	Nonce         uint32 `json:"nonce"`
	BlockHeight   int    `json:"block_height"`
}

type headersOracleHeaderCase struct {
	Name          string              `json:"name"`
	Height        int                 `json:"height"`
	SerializedHex string              `json:"serialized_hex"`
	Deserialized  headersOracleHeader `json:"deserialized"`
	RoundTripHex  string              `json:"round_trip_hex"`
	HashHex       string              `json:"hash_hex"`
}

type headersOracleHashCase struct {
	Name    string  `json:"name"`
	DataHex *string `json:"data_hex"`
	HashHex string  `json:"hash_hex"`
}

type headersOraclePoWCase struct {
	Name          string `json:"name"`
	DataHex       string `json:"data_hex"`
	HeaderHashHex string `json:"header_hash_hex"`
	PoWHashHex    string `json:"pow_hash_hex"`
	PoWValue      string `json:"pow_value"`
}

type headersOracleCompactCase struct {
	Name            string  `json:"name"`
	InputCompact    *uint32 `json:"input_compact"`
	InputValue      *string `json:"input_value"`
	Value           string  `json:"value"`
	Bits            int     `json:"bits"`
	Compact         uint32  `json:"compact"`
	Negative        uint32  `json:"negative"`
	Multiplier      *int64  `json:"multiplier"`
	MultipliedValue *string `json:"multiplied_value"`
	Divisor         *int64  `json:"divisor"`
	DividedValue    *string `json:"divided_value"`
}

type headersOracleTargetHeader struct {
	Timestamp uint32 `json:"timestamp"`
	Bits      uint32 `json:"bits"`
}

type headersOracleTargetCase struct {
	Name           string                     `json:"name"`
	MaxTargetHex   string                     `json:"max_target_hex"`
	TargetTimespan int64                      `json:"target_timespan"`
	Previous       *headersOracleTargetHeader `json:"previous"`
	Current        *headersOracleTargetHeader `json:"current"`
	Value          string                     `json:"value"`
	Compact        uint32                     `json:"compact"`
}

type headersOracleChainCase struct {
	Name               string  `json:"name"`
	InitialHex         string  `json:"initial_hex"`
	InputHex           string  `json:"input_hex"`
	Start              int     `json:"start"`
	ValidateDifficulty bool    `json:"validate_difficulty"`
	Added              int     `json:"added"`
	Height             int     `json:"height"`
	SerializedHex      string  `json:"serialized_hex"`
	TipHashHex         *string `json:"tip_hash_hex"`
}

func TestHeadersMatchPinnedPythonOracle(t *testing.T) {
	payload := map[string]any{
		"fixture_header_indices": []int{0, 1, 2, 10},
		"hash_cases": []map[string]any{
			{"name": "nil header", "data_hex": nil},
			{"name": "empty header", "data_hex": ""},
			{"name": "short bytes", "data_text": "test string"},
		},
		"pow_cases": []map[string]any{
			{"name": "test string", "data_text": "test string"},
			{"name": "70 a bytes", "repeat_char": "a", "repeat_count": 70},
			{"name": "140 d bytes", "repeat_char": "d", "repeat_count": 140},
		},
		"compact_cases": []map[string]any{
			{"name": "zero", "compact": uint32(0)},
			{"name": "zero mantissa", "compact": uint32(0x00123456)},
			{"name": "negative marker", "compact": uint32(0x01fedcba)},
			{"name": "two bytes", "compact": uint32(0x02123456)},
			{"name": "three bytes", "compact": uint32(0x03123456)},
			{"name": "sign bit", "compact": uint32(0x04923456)},
			{"name": "wide mantissa", "compact": uint32(0x05009234)},
			{"name": "256 bit", "compact": uint32(0x20123456)},
			{"name": "compact sign avoidance", "value": "0x80"},
			{
				"name":       "wrapped multiply and float division",
				"value":      "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
				"multiplier": 225, "divisor": 150,
			},
		},
		"target_cases": []map[string]any{
			{
				"name": "both missing", "max_target_hex": headersOracleMaxTargetHex,
				"target_timespan": 150, "previous": nil, "current": nil,
			},
			{
				"name": "maximum retarget", "max_target_hex": headersOracleMaxTargetHex,
				"target_timespan": 150,
				"previous":        map[string]any{"timestamp": 1386475638},
				"current":         map[string]any{"timestamp": 1386475638, "bits": uint32(0x1f00ffff)},
			},
			{
				"name": "difficulty increase", "max_target_hex": headersOracleMaxTargetHex,
				"target_timespan": 150,
				"previous":        map[string]any{"timestamp": 1386475638},
				"current":         map[string]any{"timestamp": 1386475638, "bits": uint32(0x1f00a000)},
			},
			{
				"name": "difficulty decrease", "max_target_hex": headersOracleMaxTargetHex,
				"target_timespan": 150,
				"previous":        map[string]any{"timestamp": 1386475638},
				"current": map[string]any{
					"timestamp": 1386475638 + 60*20, "bits": uint32(0x1f00a000),
				},
			},
			{
				"name": "pow limit", "max_target_hex": headersOracleMaxTargetHex,
				"target_timespan": 150,
				"previous":        map[string]any{"timestamp": 1386475638},
				"current": map[string]any{
					"timestamp": 1386475638 + 600, "bits": uint32(0x1f00ffff),
				},
			},
		},
		"chain_cases": []map[string]any{
			{
				"name": "valid first three", "fixture_start": 0, "fixture_count": 3,
				"start": 0, "validate_difficulty": true,
			},
			{
				"name":          "invalid third link rejects practical chunk",
				"fixture_start": 0, "fixture_count": 3, "start": 0,
				"validate_difficulty": true,
				"mutations":           []map[string]any{{"offset": 2*HeaderSize + 4, "xor": 1}},
			},
			{
				"name": "append third", "initial_fixture_start": 0,
				"initial_fixture_count": 2, "fixture_start": 2, "fixture_count": 1,
				"start": 2, "validate_difficulty": true,
			},
		},
	}
	oracle := runHeadersOracle(t, payload)
	headersOracleAssertReference(t, oracle)

	for _, fixture := range oracle.HeaderCases {
		fixture := fixture
		t.Run("header/"+fixture.Name, func(t *testing.T) {
			raw := headersOracleMustHex(t, fixture.SerializedHex)
			got, err := DeserializeHeader(fixture.Height, raw)
			if err != nil {
				t.Fatal(err)
			}
			headersOracleAssertHeader(t, got, fixture.Deserialized)
			serialized, err := SerializeHeader(got)
			if err != nil {
				t.Fatal(err)
			}
			if gotHex := hex.EncodeToString(serialized); gotHex != fixture.RoundTripHex {
				t.Fatalf("SerializeHeader() = %s, want %s", gotHex, fixture.RoundTripHex)
			}
			if gotHash := string(HashHeader(raw)); gotHash != fixture.HashHex {
				t.Fatalf("HashHeader() = %s, want %s", gotHash, fixture.HashHex)
			}
		})
	}

	for _, fixture := range oracle.HashCases {
		fixture := fixture
		t.Run("hash/"+fixture.Name, func(t *testing.T) {
			var value []byte
			if fixture.DataHex != nil {
				value = headersOracleMustHex(t, *fixture.DataHex)
				if *fixture.DataHex == "" {
					value = make([]byte, 0)
				}
			}
			if got := string(HashHeader(value)); got != fixture.HashHex {
				t.Fatalf("HashHeader() = %s, want %s", got, fixture.HashHex)
			}
		})
	}

	for _, fixture := range oracle.PoWCases {
		fixture := fixture
		t.Run("pow/"+fixture.Name, func(t *testing.T) {
			value := headersOracleMustHex(t, fixture.DataHex)
			headerHash := HashHeader(value)
			if got := string(headerHash); got != fixture.HeaderHashHex {
				t.Fatalf("HashHeader() = %s, want %s", got, fixture.HeaderHashHex)
			}
			powHash, err := HeaderHashToPoWHash(headerHash)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(powHash); got != fixture.PoWHashHex {
				t.Fatalf("HeaderHashToPoWHash() = %s, want %s", got, fixture.PoWHashHex)
			}
			proof, err := ProofOfWork(headerHash)
			if err != nil {
				t.Fatal(err)
			}
			if got := proof.Value().String(); got != fixture.PoWValue {
				t.Fatalf("ProofOfWork() = %s, want %s", got, fixture.PoWValue)
			}
		})
	}

	for _, fixture := range oracle.CompactCases {
		fixture := fixture
		t.Run("compact/"+fixture.Name, func(t *testing.T) {
			var number *ArithUint256
			if fixture.InputCompact != nil {
				number = ArithUint256FromCompact(*fixture.InputCompact)
			} else {
				number = NewArithUint256(headersOracleMustInteger(t, *fixture.InputValue, 0))
			}
			if got := number.Value().String(); got != fixture.Value {
				t.Fatalf("Value() = %s, want %s", got, fixture.Value)
			}
			if got := number.Bits(); got != fixture.Bits {
				t.Fatalf("Bits() = %d, want %d", got, fixture.Bits)
			}
			if got := number.Compact(); got != fixture.Compact {
				t.Fatalf("Compact() = %#x, want %#x", got, fixture.Compact)
			}
			if got := number.Negative(); got != fixture.Negative {
				t.Fatalf("Negative() = %#x, want %#x", got, fixture.Negative)
			}
			if fixture.Multiplier != nil {
				if got := number.Multiply(*fixture.Multiplier).Value().String(); got != *fixture.MultipliedValue {
					t.Fatalf("Multiply() = %s, want %s", got, *fixture.MultipliedValue)
				}
			}
			if fixture.Divisor != nil {
				if got := number.Divide(*fixture.Divisor).Value().String(); got != *fixture.DividedValue {
					t.Fatalf("Divide() = %s, want %s", got, *fixture.DividedValue)
				}
			}
		})
	}

	for _, fixture := range oracle.TargetCases {
		fixture := fixture
		t.Run("target/"+fixture.Name, func(t *testing.T) {
			maximum := NewArithUint256(headersOracleMustInteger(t, fixture.MaxTargetHex, 16))
			var previous, current *BlockHeader
			if fixture.Previous != nil {
				previous = &BlockHeader{Timestamp: fixture.Previous.Timestamp, Bits: fixture.Previous.Bits}
			}
			if fixture.Current != nil {
				current = &BlockHeader{Timestamp: fixture.Current.Timestamp, Bits: fixture.Current.Bits}
			}
			target := NextBlockTarget(maximum, fixture.TargetTimespan, previous, current)
			if got := target.Value().String(); got != fixture.Value {
				t.Fatalf("NextBlockTarget().Value() = %s, want %s", got, fixture.Value)
			}
			if got := target.Compact(); got != fixture.Compact {
				t.Fatalf("NextBlockTarget().Compact() = %#x, want %#x", got, fixture.Compact)
			}
		})
	}

	for _, fixture := range oracle.ChainCases {
		fixture := fixture
		t.Run("chain/"+fixture.Name, func(t *testing.T) {
			chain := newCheckpointIndependentHeaders(":memory:", WithHeaderValidation(fixture.ValidateDifficulty))
			if err := chain.Open(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := chain.Close(); err != nil {
					t.Error(err)
				}
			})
			initial := headersOracleMustHex(t, fixture.InitialHex)
			if len(initial) > 0 {
				added, err := chain.Connect(0, initial)
				if err != nil {
					t.Fatalf("connect initial headers: %v", err)
				}
				if added != len(initial)/HeaderSize {
					t.Fatalf("initial Connect() added %d, want %d", added, len(initial)/HeaderSize)
				}
			}
			added, err := chain.Connect(fixture.Start, headersOracleMustHex(t, fixture.InputHex))
			if err != nil {
				t.Fatal(err)
			}
			if added != fixture.Added {
				t.Fatalf("Connect() added %d, want %d", added, fixture.Added)
			}
			if got := chain.Height(); got != fixture.Height {
				t.Fatalf("Height() = %d, want %d", got, fixture.Height)
			}
			var serialized []byte
			for height := 0; height < chain.Len(); height++ {
				raw, err := chain.GetRaw(height)
				if err != nil {
					t.Fatal(err)
				}
				serialized = append(serialized, raw...)
			}
			if got := hex.EncodeToString(serialized); got != fixture.SerializedHex {
				t.Fatalf("stored headers = %s, want %s", got, fixture.SerializedHex)
			}
			if fixture.TipHashHex != nil {
				tip, err := chain.Hash(nil)
				if err != nil {
					t.Fatal(err)
				}
				if got := string(tip); got != *fixture.TipHashHex {
					t.Fatalf("tip hash = %s, want %s", got, *fixture.TipHashHex)
				}
			}
		})
	}
}

func headersOracleAssertReference(t *testing.T, oracle headersOracleResponse) {
	t.Helper()
	if oracle.Reference.Commit != headersOraclePinnedCommit || oracle.Reference.Version != headersOraclePinnedVersion {
		t.Fatalf("header oracle reference = %s/%s, want %s/%s",
			oracle.Reference.Commit, oracle.Reference.Version,
			headersOraclePinnedCommit, headersOraclePinnedVersion,
		)
	}
	if len(oracle.Reference.SourceSHA256) != len(headersOraclePinnedSourceHashes) {
		t.Fatalf("header oracle source hash count = %d, want %d",
			len(oracle.Reference.SourceSHA256), len(headersOraclePinnedSourceHashes),
		)
	}
	for path, expected := range headersOraclePinnedSourceHashes {
		if got := oracle.Reference.SourceSHA256[path]; got != expected {
			t.Fatalf("header oracle source hash for %s = %s, want %s", path, got, expected)
		}
	}
	if oracle.Metadata.HeaderSize != HeaderSize || oracle.Metadata.FixtureHeaders != 20 {
		t.Fatalf("header oracle metadata size/count = %d/%d, want %d/20",
			oracle.Metadata.HeaderSize, oracle.Metadata.FixtureHeaders, HeaderSize,
		)
	}
	if !oracle.Metadata.StdlibOnly || !oracle.Metadata.PythonAssertions {
		t.Fatalf("header oracle safety metadata = stdlib:%t assertions:%t",
			oracle.Metadata.StdlibOnly, oracle.Metadata.PythonAssertions,
		)
	}
	if len(oracle.Metadata.SelfChecks) < 7 {
		t.Fatalf("header oracle self-checks = %v", oracle.Metadata.SelfChecks)
	}
	for name, passed := range oracle.Metadata.SelfChecks {
		if !passed {
			t.Fatalf("header oracle self-check %s failed", name)
		}
	}
}

func headersOracleAssertHeader(t *testing.T, got BlockHeader, want headersOracleHeader) {
	t.Helper()
	if got.Version != want.Version ||
		string(got.PreviousHash) != want.PreviousHash ||
		string(got.MerkleRoot) != want.MerkleRoot ||
		string(got.ClaimTrieRoot) != want.ClaimTrieRoot ||
		got.Timestamp != want.Timestamp || got.Bits != want.Bits ||
		got.Nonce != want.Nonce || got.BlockHeight != want.BlockHeight {
		t.Fatalf("DeserializeHeader() = %+v, want %+v", got, want)
	}
}

func runHeadersOracle(t *testing.T, payload map[string]any) headersOracleResponse {
	t.Helper()
	sdkRoot, script := headersOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Stdin = bytes.NewReader(encoded)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python header oracle failed: %v\n%s", err, stderr.String())
	}
	var oracle headersOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode Python header oracle: %v\n%s", err, output)
	}
	return oracle
}

func headersOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate header oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "headers_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "lbry", "wallet", "header.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "checkpoints.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "util.py"),
		filepath.Join(sdkRoot, "tests", "unit", "wallet", "test_headers.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python SDK header source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}

func headersOracleMustHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func headersOracleMustInteger(t *testing.T, value string, base int) *big.Int {
	t.Helper()
	parsed, ok := new(big.Int).SetString(value, base)
	if !ok {
		t.Fatalf("invalid integer %q in base %d", value, base)
	}
	return parsed
}

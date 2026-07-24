package spv

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

const (
	namedRequestOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	namedRequestOraclePinnedVersion = "0.113.0"
)

var namedRequestOraclePinnedSources = map[string]string{
	"lbry/__init__.py":           "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/wallet/network.py":     "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
	"lbry/wallet/rpc/jsonrpc.py": "6da90b83bdb2e192929abddbb8b33824eac7d24f7ab126c1942db5ed6b7c1269",
}

var namedRequestOraclePinnedMethods = map[string]string{
	"JSONRPC.encode_payload":    "f7dbd676c1b644cd5d39ffad9ca717a2f77e6215ad1ec53ca9727487b5207112",
	"JSONRPCv2.request_payload": "8b734a26b7b76f85e71c85e12f7d0b01c1946c8bd8746ec1bb5273ecfc3be5a9",
	"Network.claim_search":      "8ffee30536ef5354fc5c6028eb06b70512ce0f2ed09bf9567edb8689a4957968",
	"SingleRequest.__init__":    "dc1aeed643cb1d3f02872ded23fe1d0c3870c27a3d36c8a8b388646dac7d749a",
}

type namedRequestOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
		MethodSHA256 map[string]string `json:"method_sha256"`
	} `json:"reference"`
	Cases []namedRequestOracleCase `json:"cases"`
}

type namedRequestOracleCase struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Method        string `json:"method"`
	ID            int64  `json:"id"`
	ParamsPresent bool   `json:"params_present"`
	Encoded       string `json:"encoded"`
}

func TestNamedRequestsMatchPinnedPythonOracle(t *testing.T) {
	oracle := runNamedRequestOracle(t)
	if oracle.Reference.Commit != namedRequestOraclePinnedCommit ||
		oracle.Reference.Version != namedRequestOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, namedRequestOraclePinnedSources) ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, namedRequestOraclePinnedMethods) {
		t.Fatalf("named request oracle reference = %+v", oracle.Reference)
	}

	claimArgs := map[string]any{
		"text":            "open source",
		"claim_type":      []any{"stream", "repost"},
		"channel_ids":     []any{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"not_channel_ids": []any{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		"order_by":        []any{"effective_amount", "^height"},
		"page":            2,
		"page_size":       20,
		"no_totals":       true,
	}
	fixtures := map[string]struct {
		kind   string
		method string
		id     int64
		params any
	}{
		"positional_nonempty": {
			kind: "positional", method: "server.version", id: 1,
			params: []any{"LBRY SDK 0.113.0", "0.113.0"},
		},
		"positional_empty_list": {
			kind: "positional", method: "server.features", id: 2, params: []any{},
		},
		"named_empty_object": {
			kind: "named", method: "fixture.empty", id: 3, params: map[string]any{},
		},
		"claimtrie_search_named": {
			kind: "named", method: "blockchain.claimtrie.search", id: 4, params: claimArgs,
		},
		"named_nested_values": {
			kind: "named", method: "fixture.nested", id: 5,
			params: map[string]any{
				"filters": map[string]any{
					"heights": []any{">=100", "<200"},
					"flags":   []any{true, false, nil},
				},
				"locations": []any{map[string]any{"country": "PL", "city": "Lodz"}},
				"label":     "snowman \u2603 and newline\n",
			},
		},
		"named_nonfinite_floats": {
			kind: "named", method: "fixture.special", id: 6,
			params: map[string]any{
				"nan": math.NaN(),
				"nested": map[string]any{
					"values":  []any{math.Inf(1), math.Inf(-1), 1.25},
					"literal": "Infinity",
				},
			},
		},
		"positional_nonfinite_floats": {
			kind: "positional", method: "fixture.special", id: 7,
			params: []any{math.NaN(), map[string]any{"values": []any{math.Inf(1), math.Inf(-1)}}},
		},
	}

	if len(oracle.Cases) != len(fixtures) {
		t.Fatalf("named request oracle returned %d cases, want %d", len(oracle.Cases), len(fixtures))
	}
	seen := make(map[string]bool, len(fixtures))
	for _, oracleCase := range oracle.Cases {
		fixture, exists := fixtures[oracleCase.Name]
		if !exists {
			t.Fatalf("unknown named request oracle case %q", oracleCase.Name)
		}
		seen[oracleCase.Name] = true
		if fixture.kind != oracleCase.Kind || fixture.method != oracleCase.Method || fixture.id != oracleCase.ID {
			t.Fatalf("case %q identity = %q, %q, %d", oracleCase.Name, oracleCase.Kind, oracleCase.Method, oracleCase.ID)
		}

		var (
			encoded []byte
			err     error
		)
		switch fixture.kind {
		case "positional":
			encoded, err = encodeRequest(fixture.method, fixture.params.([]any), fixture.id)
		case "named":
			encoded, err = encodeNamedRequest(fixture.method, fixture.params.(map[string]any), fixture.id)
		default:
			t.Fatalf("case %q has unsupported kind %q", oracleCase.Name, fixture.kind)
		}
		if err != nil {
			t.Fatalf("encode case %q: %v", oracleCase.Name, err)
		}
		if !bytes.HasSuffix(encoded, []byte{'\n'}) {
			t.Fatalf("case %q is not newline framed: %q", oracleCase.Name, encoded)
		}

		goPayload := decodeLegacyRequestPayload(t, encoded)
		pythonPayload := decodeLegacyRequestPayload(t, []byte(oracleCase.Encoded))
		if !reflect.DeepEqual(goPayload, pythonPayload) {
			t.Fatalf("case %q differs\nGo:     %#v\nPython: %#v", oracleCase.Name, goPayload, pythonPayload)
		}
		mapping, ok := goPayload.(map[string]any)
		if !ok {
			t.Fatalf("case %q payload type = %T", oracleCase.Name, goPayload)
		}
		_, paramsPresent := mapping["params"]
		if paramsPresent != oracleCase.ParamsPresent {
			t.Fatalf("case %q params presence = %t, want %t", oracleCase.Name, paramsPresent, oracleCase.ParamsPresent)
		}
	}
	for name := range fixtures {
		if !seen[name] {
			t.Fatalf("named request oracle omitted case %q", name)
		}
	}
}

func decodeLegacyRequestPayload(t *testing.T, encoded []byte) any {
	t.Helper()
	encoded = quoteLegacySpecialFloats(bytes.TrimSpace(encoded))
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("decode legacy request payload %q: %v", encoded, err)
	}
	return payload
}

func quoteLegacySpecialFloats(encoded []byte) []byte {
	var result bytes.Buffer
	inString, escaped := false, false
	for index := 0; index < len(encoded); {
		if inString {
			value := encoded[index]
			result.WriteByte(value)
			index++
			switch {
			case escaped:
				escaped = false
			case value == '\\':
				escaped = true
			case value == '"':
				inString = false
			}
			continue
		}
		if encoded[index] == '"' {
			inString = true
			result.WriteByte(encoded[index])
			index++
			continue
		}
		matched := false
		for literal, replacement := range map[string]string{
			"-Infinity": `"__LBRY_NEG_INFINITY__"`,
			"Infinity":  `"__LBRY_INFINITY__"`,
			"NaN":       `"__LBRY_NAN__"`,
		} {
			if bytes.HasPrefix(encoded[index:], []byte(literal)) {
				result.WriteString(replacement)
				index += len(literal)
				matched = true
				break
			}
		}
		if !matched {
			result.WriteByte(encoded[index])
			index++
		}
	}
	return result.Bytes()
}

func runNamedRequestOracle(t *testing.T) namedRequestOracleResponse {
	t.Helper()
	sdkRoot, script := namedRequestOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python named request oracle failed: %v\n%s", err, output)
	}
	var oracle namedRequestOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode named request oracle: %v\n%s", err, output)
	}
	return oracle
}

func namedRequestOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate named request oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(filepath.Dir(sourceFile)))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "spv_named_request_oracle.py")
	for relative := range namedRequestOraclePinnedSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local named request source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(script); errors.Is(err, os.ErrNotExist) {
		t.Skipf("named request oracle script is unavailable: %s", script)
	} else if err != nil {
		t.Fatal(err)
	}
	return sdkRoot, script
}

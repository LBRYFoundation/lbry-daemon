package wallet

import (
	"crypto/sha256"
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
)

const legacyV0ClaimPinnedCommit = "e7666f489418e96b6d2104974e93915b539235c5"

const legacyV0ClaimPythonProbe = `
import json
import sys
import types
from pathlib import Path

root = Path(sys.argv[1])
sys.path.insert(0, str(root))

def module(name, **attrs):
    value = types.ModuleType(name)
    for key, item in attrs.items():
        setattr(value, key, item)
    sys.modules[name] = value
    return value

class DummyLog:
    use_print = False

# These optional publishing/inspection dependencies are not involved in
# Claim.from_bytes or compat.from_old_json_schema.
module("asn1crypto")
module("asn1crypto.keys", PublicKeyInfo=object)
module("coincurve", PublicKey=object)
module("hachoir")
module("hachoir.core")
module("hachoir.core.log", log=DummyLog())
module("hachoir.parser", createParser=lambda *args: None)
module("hachoir.metadata", extractMetadata=lambda *args: None)
module("filetype", guess=lambda *args: None)

from lbry.schema.claim import Claim

base = '"sources":{"lbry_sd_hash":"00ff"}'
fixtures = [
    ("basic", ('{' + base + '}').encode()),
    ("metadata source tags locations", ('{' + base +
        ',"content_type":"video/mp4","content-type":"audio/mpeg"'
        ',"title":"T","description":"D","thumbnail":"U","author":"A"'
        ',"license":"L","license_url":"LU","language":"English","nsfw":true'
        ',"tags":["ignored"],"locations":["US:CA:LA"]}').encode()),
    ("content underscore empty", ('{' + base +
        ',"content_type":"","content-type":"audio/mpeg"}').encode()),
    ("language invalid retained", ('{' + base + ',"language":"zz"}').encode()),
    ("language partial retained", ('{' + base + ',"language":"en-US-extra"}').encode()),
    ("nsfw truthy string", ('{' + base + ',"nsfw":"false"}').encode()),
    ("fee lbc integer", ('{' + base +
        ',"fee":{"LBC":{"amount":1,"address":"1"}}}').encode()),
    ("fee lbc float", ('{' + base +
        ',"fee":{"LBC":{"amount":0.29,"address":"2"}}}').encode()),
	("fee lbc decimal string", ('{' + base +
		',"fee":{"LBC":{"amount":"0.29","address":"2"}}}').encode()),
	("fee lbc exponent number", ('{' + base +
		',"fee":{"LBC":{"amount":1e-8,"address":"2"}}}').encode()),
	("fee lbc exponent string", ('{' + base +
		',"fee":{"LBC":{"amount":"1e-8","address":"2"}}}').encode()),
	("fee usd float", ('{' + base +
		',"fee":{"USD":{"amount":0.29,"address":"3"}}}').encode()),
	("fee usd round up float", ('{' + base +
		',"fee":{"USD":{"amount":0.1,"address":"3"}}}').encode()),
	("fee usd tiny string", ('{' + base +
		',"fee":{"USD":{"amount":"1e-8","address":"3"}}}').encode()),
    ("fee btc boolean", ('{' + base +
        ',"fee":{"BTC":{"amount":true,"address":"11"}}}').encode()),
    ("fee non-object ignored", ('{' + base + ',"fee":7}').encode()),
    ("fee first currency", ('{' + base +
        ',"fee":{"USD":{"amount":"1.01","address":"3"},'
        '"LBC":{"amount":2,"address":"4"}}}').encode()),
    ("fee duplicate currency", ('{' + base +
        ',"fee":{"LBC":{"amount":1,"address":"2"},'
        '"LBC":{"amount":2,"address":"3"}}}').encode()),
    ("unicode metadata", ('{' + base +
        ',"title":"Zażółć gęślą jaźń","description":"日本語"}').encode()),
    ("ignored escaped surrogate", b'{"sources":{"lbry_sd_hash":"00"},"extra":"\\ud800"}'),
    ("malformed", b'{bad'),
    ("invalid utf8", b'{"sources":{"lbry_sd_hash":"\xff"}}'),
    ("missing sources", b'{}'),
    ("missing sd hash", b'{"sources":{}}'),
    ("null title", ('{' + base + ',"title":null}').encode()),
    ("numeric media type", ('{' + base + ',"content_type":4}').encode()),
    ("numeric thumbnail", ('{' + base + ',"thumbnail":4}').encode()),
    ("numeric language", ('{' + base + ',"language":4}').encode()),
    ("odd sd hash", b'{"sources":{"lbry_sd_hash":"0"}}'),
    ("nonhex sd hash", b'{"sources":{"lbry_sd_hash":"zz"}}'),
    ("empty fee", ('{' + base + ',"fee":{}}').encode()),
    ("unknown fee", ('{' + base +
        ',"fee":{"EUR":{"amount":1,"address":"1"}}}').encode()),
    ("empty fee address", ('{' + base +
        ',"fee":{"LBC":{"amount":1,"address":""}}}').encode()),
    ("invalid fee address", ('{' + base +
        ',"fee":{"LBC":{"amount":1,"address":"0"}}}').encode()),
    ("missing fee amount", ('{' + base +
        ',"fee":{"LBC":{"address":"1"}}}').encode()),
    ("null fee amount", ('{' + base +
        ',"fee":{"LBC":{"amount":null,"address":"1"}}}').encode()),
]

results = []
for name, payload in fixtures:
    result = {"name": name, "payload_hex": payload.hex()}
    try:
        claim = Claim.from_bytes(payload)
        result.update({
            "ok": True,
            "type": claim.claim_type,
            "value": claim.stream.to_dict(),
            "canonical_hex": claim.to_bytes().hex(),
        })
    except Exception as error:
        result.update({
            "ok": False,
            "error_type": type(error).__name__,
            "error_message": str(error),
        })
    results.append(result)

print(json.dumps(results, ensure_ascii=True, sort_keys=True))
`

type legacyV0ClaimOracleFixture struct {
	Name         string         `json:"name"`
	PayloadHex   string         `json:"payload_hex"`
	OK           bool           `json:"ok"`
	Type         string         `json:"type"`
	Value        map[string]any `json:"value"`
	CanonicalHex string         `json:"canonical_hex"`
	ErrorType    string         `json:"error_type"`
	ErrorMessage string         `json:"error_message"`
}

func TestDecodeLegacyV0ClaimValueMatchesPinnedPythonOracle(t *testing.T) {
	fixtures := runLegacyV0ClaimPythonOracle(t)
	if len(fixtures) != 36 {
		t.Fatalf("Python legacy v0 fixture count = %d, want 36", len(fixtures))
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			payload, err := hex.DecodeString(fixture.PayloadHex)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeLegacyV0ClaimValue(payload)
			if !fixture.OK {
				if decoded != nil || err == nil {
					t.Fatalf("Go decode = %+v, %v; Python error = %s: %s", decoded, err, fixture.ErrorType, fixture.ErrorMessage)
				}
				var pythonError *ClaimValueDecodeError
				if !errors.As(err, &pythonError) || pythonError.PythonErrorName() != fixture.ErrorType ||
					err.Error() != fixture.ErrorMessage {
					t.Fatalf("Go error = %T %q/%q, Python = %q/%q", err,
						pythonError.PythonErrorName(), err, fixture.ErrorType, fixture.ErrorMessage)
				}
				return
			}
			if err != nil {
				t.Fatalf("Go error = %v; Python decoded successfully", err)
			}
			if decoded.Type != fixture.Type || !reflect.DeepEqual(decoded.Value, fixture.Value) ||
				hex.EncodeToString(decoded.Canonical) != fixture.CanonicalHex || decoded.IsSigned() {
				t.Fatalf("Go = type %q, value %#v, canonical %x; Python = %+v",
					decoded.Type, decoded.Value, decoded.Canonical, fixture)
			}
		})
	}
}

func TestDecodeLegacyV0ClaimValueFormatBoundary(t *testing.T) {
	for _, payload := range [][]byte{nil, {}, {0}, []byte("[]"), []byte("  {}")} {
		decoded, err := DecodeLegacyV0ClaimValue(payload)
		if decoded != nil || !errors.Is(err, ErrUnsupportedLegacyClaimValue) {
			t.Fatalf("DecodeLegacyV0ClaimValue(%q) = %+v, %v", payload, decoded, err)
		}
	}
}

func runLegacyV0ClaimPythonOracle(t *testing.T) []legacyV0ClaimOracleFixture {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate legacy v0 claim oracle source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}

	sources := map[string]string{
		"lbry/schema/compat.py":             "e4659df85632e329a5e1b786ea17c0d65336b46a81da3e17a7d907aafb6928ff",
		"lbry/schema/claim.py":              "2b2a58f580efc2d5ea7bbfadfff28ea150429dbcc71fcefaf8992fa5213027af",
		"lbry/schema/attrs.py":              "e2c01abf8a152ca224f557d38a4932b40ce0ceb880c27b2dbe0bca15c4a51624",
		"lbry/schema/base.py":               "898875eebd916eee0ea4ad9e2be8aff53f8f56a1479f664e143f0461ffba7140",
		"lbry/schema/types/v2/claim_pb2.py": "3edb36895d7d2f294e27019438332ca8a7ed4cb3c0f30ee33c9aa406bf000c98",
		"lbry/crypto/base58.py":             "bed7bff1169a9d7b89473a08b6054170f52f50e3fb2126a9752c36e745e9b94f",
		"lbry/crypto/hash.py":               "bfc430bd3fe98578b406caa3a8e2116a40f492c7b68e269176e838b4ef426a72",
		"lbry/crypto/util.py":               "b943c2c53ad09d808b09977edf2b274e21c33a2e70bdaca9eb0e8a7363d20727",
		"lbry/schema/tags.py":               "32409bb011eaf2b0ff65a96889ac2ba8cb306f8005f750d96f539dea53a61837",
		"lbry/constants.py":                 "db92b1aa8be15be77e5ac582daecc23b1842c4b509b1a438b717285d6eaf1ced",
	}
	for relative, want := range sources {
		source, err := os.ReadFile(filepath.Join(sdkRoot, filepath.FromSlash(relative)))
		if errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned SDK source is unavailable: %s", relative)
		} else if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(source)); got != want {
			t.Fatalf("%s at pinned commit %s has hash %s, want %s", relative, legacyV0ClaimPinnedCommit, got, want)
		}
	}

	command := exec.Command(python, "-c", legacyV0ClaimPythonProbe, sdkRoot)
	command.Env = append(os.Environ(), "PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION=python")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python legacy v0 claim oracle failed: %v\n%s", err, output)
	}
	var fixtures []legacyV0ClaimOracleFixture
	if err := json.Unmarshal(output, &fixtures); err != nil {
		t.Fatalf("decode Python legacy v0 claim oracle: %v\n%s", err, output)
	}
	return fixtures
}

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

const transactionSupportValuePythonProbe = `
import importlib.util
import hashlib
import json
from pathlib import Path
import sys
import types

root = Path(sys.argv[1])

def package(name, path):
    module = types.ModuleType(name)
    module.__path__ = [str(path)]
    sys.modules[name] = module

def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module

package("lbry", root / "lbry")
package("lbry.schema", root / "lbry" / "schema")
package("lbry.schema.types", root / "lbry" / "schema" / "types")
package("lbry.schema.types.v2", root / "lbry" / "schema" / "types" / "v2")
load("lbry.schema.types.v2.support_pb2", root / "lbry" / "schema" / "types" / "v2" / "support_pb2.py")
load("lbry.schema.base", root / "lbry" / "schema" / "base.py")
Support = load("lbry.schema.support", root / "lbry" / "schema" / "support.py").Support

header = bytes(range(1, 21))
signature = bytes(range(21, 85))
fixtures = [
    ("empty", b""),
    ("unsigned empty", bytes.fromhex("00")),
    ("unknown wrapper", bytes.fromhex("02")),
    ("emoji", bytes.fromhex("000a04f09f9880")),
    ("comment", bytes.fromhex("001203686579")),
    ("reverse fields", bytes.fromhex("0012036865790a04f09f9880")),
    ("duplicate emoji", bytes.fromhex("000a01610a0162")),
    ("duplicate then empty", bytes.fromhex("000a01610a00")),
    ("unknown before", bytes.fromhex("0018070a0161")),
    ("unknown middle", bytes.fromhex("000a016118071203686579")),
    ("wrong wire known", bytes.fromhex("0008010a0161")),
    ("overlong known tag", bytes.fromhex("008a000161")),
    ("overlong known length", bytes.fromhex("000a810061")),
    ("overlong unknown", bytes.fromhex("0098008700")),
    ("unknown groups", bytes.fromhex("001b231824241c")),
    ("field zero", bytes.fromhex("000001")),
    ("reserved wire", bytes.fromhex("000e")),
    ("unexpected end group", bytes.fromhex("001c")),
    ("open group", bytes.fromhex("001b0801")),
    ("truncated tag", bytes.fromhex("0080")),
    ("truncated string", bytes.fromhex("000a02ff")),
    ("invalid emoji utf8", bytes.fromhex("000a01ff")),
    ("invalid comment utf8", bytes.fromhex("001201ff")),
    ("truncated emoji utf8", bytes.fromhex("000a02e282")),
    ("surrogate emoji utf8", bytes.fromhex("000a03eda080")),
    ("overlong comment utf8", bytes.fromhex("001202c080")),
    ("signed empty", bytes([1]) + header + signature),
    ("signed fields", bytes([1]) + header + signature + bytes.fromhex("1201780a0179")),
    ("signed empty body short", bytes.fromhex("01")),
    ("signed short hash", bytes.fromhex("01010203")),
    ("signed one signature byte", bytes([1]) + header + bytes([21])),
]

# Deterministic malformed/unknown-field coverage. Each digest is exercised as
# an unsigned message or as the message portion of a complete signed wrapper.
for index in range(128):
    digest = hashlib.sha256(("support-value-%d" % index).encode()).digest()
    body = digest[:index % 33]
    if index % 2:
        payload = bytes([1]) + header + signature + body
    else:
        payload = bytes([0]) + body
    fixtures.append(("digest %03d" % index, payload))

results = []
for name, payload in fixtures:
    result = {"name": name, "payload_hex": payload.hex()}
    try:
        support = Support.from_bytes(payload)
        result.update({
            "ok": True,
            "emoji": support.emoji,
            "comment": support.comment,
            "value": support.to_dict(),
            "signed": support.is_signed,
            "channel_id": support.signing_channel_id,
            "channel_hash_hex": support.signing_channel_hash.hex() if support.signing_channel_hash is not None else None,
            "signature_hex": support.signature.hex() if support.signature is not None else None,
            "canonical_hex": support.to_bytes().hex(),
        })
    except Exception as error:
        result.update({
            "ok": False,
            "error_type": type(error).__name__,
            "error_message": str(error),
        })
    results.append(result)

print(json.dumps(results, sort_keys=True))
`

type transactionSupportValueOracleFixture struct {
	Name           string         `json:"name"`
	PayloadHex     string         `json:"payload_hex"`
	OK             bool           `json:"ok"`
	Emoji          string         `json:"emoji"`
	Comment        string         `json:"comment"`
	Value          map[string]any `json:"value"`
	Signed         bool           `json:"signed"`
	ChannelID      *string        `json:"channel_id"`
	ChannelHashHex *string        `json:"channel_hash_hex"`
	SignatureHex   *string        `json:"signature_hex"`
	CanonicalHex   string         `json:"canonical_hex"`
	ErrorType      string         `json:"error_type"`
	ErrorMessage   string         `json:"error_message"`
}

func TestDecodeSupportValueMatchesPinnedPythonOracle(t *testing.T) {
	fixtures := runTransactionSupportValuePythonOracle(t)
	if len(fixtures) != 159 {
		t.Fatalf("Python fixture count = %d, want 159", len(fixtures))
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			payload, err := hex.DecodeString(fixture.PayloadHex)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeSupportValue(payload)
			if !fixture.OK {
				if decoded != nil || err == nil {
					t.Fatalf("Go decode = %+v, %v; Python error = %s: %s", decoded, err, fixture.ErrorType, fixture.ErrorMessage)
				}
				var pythonError *SupportValueDecodeError
				if !errors.As(err, &pythonError) || pythonError.PythonErrorName() != fixture.ErrorType ||
					err.Error() != fixture.ErrorMessage {
					t.Fatalf("Go error = %T %q/%q, Python = %q/%q", err, pythonError.PythonErrorName(), err,
						fixture.ErrorType, fixture.ErrorMessage)
				}
				return
			}
			if err != nil {
				t.Fatalf("Go error = %v, Python decoded successfully", err)
			}
			if decoded.Emoji != fixture.Emoji || decoded.Comment != fixture.Comment || decoded.IsSigned() != fixture.Signed ||
				!reflect.DeepEqual(decoded.Value(), fixture.Value) ||
				!reflect.DeepEqual(decoded.SigningChannelID(), fixture.ChannelID) ||
				hex.EncodeToString(decoded.Canonical) != fixture.CanonicalHex ||
				!reflect.DeepEqual(supportBytesHex(decoded.SigningChannelHash, !decoded.IsSigned()), fixture.ChannelHashHex) ||
				!reflect.DeepEqual(supportBytesHex(decoded.Signature, !decoded.IsSigned()), fixture.SignatureHex) {
				t.Fatalf("Go = %+v, value %#v, channel %v; Python = %+v", decoded, decoded.Value(), decoded.SigningChannelID(), fixture)
			}
		})
	}
}

func runTransactionSupportValuePythonOracle(t *testing.T) []transactionSupportValueOracleFixture {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate support value oracle source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}

	sources := map[string]string{
		"lbry/schema/base.py":                 "898875eebd916eee0ea4ad9e2be8aff53f8f56a1479f664e143f0461ffba7140",
		"lbry/schema/support.py":              "3c8868541d4d2c58893e03b4bfce2c48580d3851806d844fcb786626cd37d31f",
		"lbry/schema/types/v2/support_pb2.py": "fea8198f476609b523992ef4f0f446fa004f38facae2ca1f33cfe41a905f825a",
	}
	for relative, want := range sources {
		source, err := os.ReadFile(filepath.Join(sdkRoot, filepath.FromSlash(relative)))
		if errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned SDK source is unavailable: %s", relative)
		} else if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(source)); got != want {
			t.Fatalf("%s hash = %s, want %s", relative, got, want)
		}
	}

	command := exec.Command(python, "-c", transactionSupportValuePythonProbe, sdkRoot)
	command.Env = append(os.Environ(), "PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION=python")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python support value oracle failed: %v\n%s", err, output)
	}
	var fixtures []transactionSupportValueOracleFixture
	if err := json.Unmarshal(output, &fixtures); err != nil {
		t.Fatalf("decode Python support value oracle: %v\n%s", err, output)
	}
	return fixtures
}

func supportBytesHex(value []byte, absent bool) *string {
	if absent {
		return nil
	}
	encoded := hex.EncodeToString(value)
	return &encoded
}

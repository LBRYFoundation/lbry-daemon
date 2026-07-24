package wallet

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

const (
	claimSignatureOraclePinnedCommit = "e7666f489418e96b6d2104974e93915b539235c5"
	claimSignatureTransactionSHA256  = "e73491aeb915fbce931acbb4d9631f3e05440a7d26c598db85e66e524a798d15"
	claimSignatureProtoSHA256        = "3edb36895d7d2f294e27019438332ca8a7ed4cb3c0f30ee33c9aa406bf000c98"
)

const claimSignaturePythonProbe = `
import ast
import hashlib
import importlib.util
import json
import pathlib
import struct
import sys
from types import SimpleNamespace

sdk_root = pathlib.Path(sys.argv[1])
claim_pb2_path = sdk_root / "lbry/schema/types/v2/claim_pb2.py"
spec = importlib.util.spec_from_file_location("_pinned_claim_pb2", claim_pb2_path)
claim_pb2 = importlib.util.module_from_spec(spec)
spec.loader.exec_module(claim_pb2)

raw_message = bytes.fromhex("42065369676e65640a00")
claim = claim_pb2.Claim()
claim.ParseFromString(raw_message)
canonical_message = claim.SerializeToString()

# Execute the exact pinned method body while avoiding imports of the retired
# SDK's unavailable coincurve wheel.
transaction_path = sdk_root / "lbry/wallet/transaction.py"
tree = ast.parse(transaction_path.read_text(encoding="utf-8"), str(transaction_path))
output_class = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "Output")
digest_method = next(
    node for node in output_class.body
    if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name == "get_signature_digest"
)
probe_class = ast.ClassDef(
    name="PinnedOutput", bases=[], keywords=[], body=[digest_method], decorator_list=[]
)
probe_module = ast.fix_missing_locations(ast.Module(body=[probe_class], type_ignores=[]))
namespace = {"sha256": lambda value: hashlib.sha256(value).digest()}
exec(compile(probe_module, str(transaction_path), "exec"), namespace)

previous_hash = bytes(range(32))
previous_index = 0x78563412
channel_hash = bytes(range(0xa0, 0xb4))
outpoint = previous_hash + struct.pack("<I", previous_index)
signable = SimpleNamespace(
    unsigned_payload=None,
    signing_channel_hash=channel_hash,
    to_message_bytes=lambda: canonical_message,
)
subject = SimpleNamespace(
    signable=signable,
    tx_ref=SimpleNamespace(
        tx=SimpleNamespace(
            inputs=[SimpleNamespace(txo_ref=SimpleNamespace(hash=outpoint))]
        )
    ),
)
digest = namespace["PinnedOutput"].get_signature_digest(subject, None)

missing_input = SimpleNamespace(
    signable=signable,
    tx_ref=SimpleNamespace(tx=SimpleNamespace(inputs=[])),
)
try:
    namespace["PinnedOutput"].get_signature_digest(missing_input, None)
except Exception as error:
    missing_input_error = type(error).__name__
else:
    missing_input_error = None

print(json.dumps({
    "raw_message_hex": raw_message.hex(),
    "canonical_message_hex": canonical_message.hex(),
    "outpoint_hex": outpoint.hex(),
    "digest_hex": digest.hex(),
    "missing_input_error": missing_input_error,
}, sort_keys=True))
`

type claimSignatureOracleResult struct {
	RawMessageHex       string `json:"raw_message_hex"`
	CanonicalMessageHex string `json:"canonical_message_hex"`
	OutpointHex         string `json:"outpoint_hex"`
	DigestHex           string `json:"digest_hex"`
	MissingInputError   string `json:"missing_input_error"`
}

func TestClaimSignatureDigestMatchesPinnedPythonMethod(t *testing.T) {
	oracle := runClaimSignaturePythonOracle(t)
	if oracle.RawMessageHex != "42065369676e65640a00" ||
		oracle.CanonicalMessageHex != "0a0042065369676e6564" ||
		oracle.OutpointHex != "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f12345678" ||
		oracle.MissingInputError != "IndexError" {
		t.Fatalf("pinned Python claim signature oracle = %+v", oracle)
	}

	value, firstInput, _, _ := claimSignatureFixture(t)
	digest, err := ClaimSignatureDigest(value, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(digest[:]); got != oracle.DigestHex || got != claimSignatureDigestHex {
		t.Fatalf("Go digest = %s; Python = %s; pinned fixture = %s", got, oracle.DigestHex, claimSignatureDigestHex)
	}
	if _, err := TransactionClaimSignatureDigest(value, &Transaction{}); !errors.Is(err, ErrClaimSignatureMissingInput) {
		t.Fatalf("Go missing-input error = %v; Python = %s", err, oracle.MissingInputError)
	}
}

func runClaimSignaturePythonOracle(t *testing.T) claimSignatureOracleResult {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate claim signature oracle source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	requiredSources := []struct {
		path   string
		digest string
	}{
		{filepath.Join(sdkRoot, "lbry", "wallet", "transaction.py"), claimSignatureTransactionSHA256},
		{filepath.Join(sdkRoot, "lbry", "schema", "types", "v2", "claim_pb2.py"), claimSignatureProtoSHA256},
	}
	for _, required := range requiredSources {
		if _, err := os.Stat(required.path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python SDK claim-signature source is unavailable: %s", required.path)
		} else if err != nil {
			t.Fatal(err)
		}
		source, err := os.ReadFile(required.path)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(source)
		if actual := hex.EncodeToString(digest[:]); actual != required.digest {
			t.Fatalf(
				"Python SDK source %s is not pinned commit %s: SHA256 %s, want %s",
				required.path, claimSignatureOraclePinnedCommit, actual, required.digest,
			)
		}
	}

	command := exec.Command(python, "-c", claimSignaturePythonProbe, sdkRoot)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python claim-signature oracle failed: %v\n%s", err, stderr.String())
	}
	var result claimSignatureOracleResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode Python claim-signature oracle: %v\n%s", err, output)
	}
	return result
}

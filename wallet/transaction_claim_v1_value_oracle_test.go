package wallet

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

const legacyV1ClaimPythonProbe = `
import hashlib
import importlib.util
import json
from pathlib import Path
import sys
import types

root = Path(sys.argv[1])

def package(name, path=None):
    module = types.ModuleType(name)
    module.__path__ = [] if path is None else [str(path)]
    sys.modules[name] = module
    return module

def plain_module(name, **attrs):
    module = types.ModuleType(name)
    module.__dict__.update(attrs)
    sys.modules[name] = module
    return module

def load(name, relative):
    path = root / relative
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module

package("lbry", root / "lbry")
package("lbry.schema", root / "lbry/schema")
package("lbry.schema.types", root / "lbry/schema/types")
package("lbry.schema.types.v1", root / "lbry/schema/types/v1")
package("lbry.schema.types.v2", root / "lbry/schema/types/v2")
package("lbry.crypto")
plain_module("lbry.constants", COIN=100_000_000)

class Base58:
    chars = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

    @classmethod
    def encode(cls, value):
        integer = int.from_bytes(value, "big")
        encoded = ""
        while integer:
            integer, digit = divmod(integer, 58)
            encoded += cls.chars[digit]
        for item in value:
            if item:
                break
            encoded += "1"
        return encoded[::-1]

    @classmethod
    def decode(cls, value):
        integer = 0
        for character in value:
            integer = integer * 58 + cls.chars.index(character)
        length = max(1, (integer.bit_length() + 7) // 8)
        decoded = integer.to_bytes(length, "big")
        leading = len(value) - len(value.lstrip("1"))
        return bytes(leading) + decoded

plain_module("lbry.crypto.base58", Base58=Base58)

class FixtureError(Exception):
    pass

plain_module(
    "lbry.error",
    MissingPublishedFileError=FixtureError,
    EmptyPublishedFileError=FixtureError,
    InputValueIsNoneError=FixtureError,
)
plain_module("lbry.schema.tags", clean_tags=lambda value: value, normalize_tag=lambda value: value)
plain_module("filetype", guess=lambda value: None)
load("lbry.schema.mime_types", "lbry/schema/mime_types.py")
plain_module("asn1crypto")
plain_module("asn1crypto.keys", PublicKeyInfo=object)
plain_module("coincurve", PublicKey=object)
package("hachoir")
package("hachoir.core")
plain_module("hachoir.core.log", log=types.SimpleNamespace(use_print=False))
plain_module("hachoir.parser", createParser=lambda value: None)
plain_module("hachoir.metadata", extractMetadata=lambda value: None)

for stem in ("fee", "metadata", "source", "stream", "certificate", "signature", "legacy_claim"):
    load(
        "lbry.schema.types.v1.%s_pb2" % stem,
        "lbry/schema/types/v1/%s_pb2.py" % stem,
    )
load("lbry.schema.types.v2.claim_pb2", "lbry/schema/types/v2/claim_pb2.py")
load("lbry.schema.base", "lbry/schema/base.py")
load("lbry.schema.attrs", "lbry/schema/attrs.py")
load("lbry.schema.compat", "lbry/schema/compat.py")
Claim = load("lbry.schema.claim", "lbry/schema/claim.py").Claim
OldClaim = sys.modules["lbry.schema.types.v1.legacy_claim_pb2"].Claim

def stream_claim():
    claim = OldClaim()
    claim.version = 1
    claim.claimType = 1
    claim.stream.version = 1
    metadata = claim.stream.metadata
    metadata.version = 4
    metadata.language = 1
    metadata.title = "Title"
    metadata.description = "Description"
    metadata.author = "Author"
    metadata.license = "License"
    metadata.nsfw = False
    source = claim.stream.source
    source.version = 1
    source.sourceType = 1
    source.source = bytes.fromhex("000102ff")
    source.contentType = "video/mp4"
    return claim

def signed(claim, signature_type=1, signature=bytes(range(64)), certificate_id=bytes(range(20))):
    value = claim.publisherSignature
    value.version = 1
    value.signatureType = signature_type
    value.signature = signature
    value.certificateId = certificate_id
    return claim

def channel_claim(with_signature=False):
    claim = OldClaim()
    claim.version = 1
    claim.claimType = 2
    claim.certificate.version = 1
    claim.certificate.keyType = 3
    claim.certificate.publicKey = bytes.fromhex("02" + "11" * 32)
    if with_signature:
        signed(claim)
    return claim

fixtures = []

def add(name, claim=None, payload=None):
    if payload is None:
        payload = claim.SerializePartialToString()
    fixtures.append((name, payload))

add("stream metadata source", stream_claim())

for currency, amount, address in ((1, 0.123456789, b"\x00\x01"), (2, 2.00000003, b"\x01\x02"), (3, 1.234, b"\x00\x01")):
    claim = stream_claim()
    fee = claim.stream.metadata.fee
    fee.version = 1
    fee.currency = currency
    fee.address = address
    fee.amount = amount
    add("fee %d" % currency, claim)

claim = stream_claim()
claim.stream.metadata.thumbnail = "https://example.test/thumb.png"
claim.stream.metadata.licenseUrl = "https://example.test/license"
claim.stream.metadata.nsfw = True
add("thumbnail license mature", claim)

add("channel", channel_claim())
add("channel ignores publisher signature", channel_claim(True))
add("signed stream", signed(stream_claim()))
add(
    "signed short fields",
    signed(stream_claim(), signature_type=0, signature=b"sig", certificate_id=b"channel"),
)

minimal = OldClaim()
minimal.version = 1
minimal.claimType = 1
add("missing nested unsigned", minimal)

missing_root = OldClaim()
signed(missing_root)
add("missing root signed", missing_root)

missing_nested = OldClaim()
missing_nested.version = 1
missing_nested.claimType = 1
missing_nested.stream.SetInParent()
signed(missing_nested)
add("missing nested signed", missing_nested)

unknown_fee = stream_claim()
fee = unknown_fee.stream.metadata.fee
fee.version = 1
fee.currency = 0
fee.address = b"\x00"
fee.amount = 1
add("unsupported fee currency", unknown_fee)

negative_fee = stream_claim()
fee = negative_fee.stream.metadata.fee
fee.version = 1
fee.currency = 1
fee.address = b"\x00"
fee.amount = -1.25
add("negative fee", negative_fee)

add("truncated field", payload=bytes.fromhex("08"))
add("invalid wire type", payload=bytes.fromhex("0f"))

address = "bTMNi3y5tYPZrLRjeuqMXtvcXrA97wAUAZ"
results = []
for name, payload in fixtures:
    result = {"name": name, "payload_hex": payload.hex()}
    try:
        claim = Claim.from_bytes(payload)
        branch = claim.stream if claim.is_stream else claim.channel
        result.update({
            "ok": True,
            "type": claim.claim_type,
            "value": branch.to_dict(),
            "signed": claim.is_signed,
            "signature_type": claim.signature_type,
            "channel_hash_hex": claim.signing_channel_hash.hex() if claim.signing_channel_hash is not None else None,
            "signature_hex": claim.signature.hex() if claim.signature is not None else None,
            "unsigned_payload_hex": claim.unsigned_payload.hex() if claim.unsigned_payload is not None else None,
            "canonical_hex": claim.to_bytes().hex(),
            "digest_hex": hashlib.sha256(
                Base58.decode(address) + claim.unsigned_payload + claim.signing_channel_hash[::-1]
            ).hexdigest() if claim.unsigned_payload is not None else None,
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

type legacyV1ClaimOracleFixture struct {
	Name               string         `json:"name"`
	PayloadHex         string         `json:"payload_hex"`
	OK                 bool           `json:"ok"`
	Type               string         `json:"type"`
	Value              map[string]any `json:"value"`
	Signed             bool           `json:"signed"`
	SignatureType      string         `json:"signature_type"`
	ChannelHashHex     *string        `json:"channel_hash_hex"`
	SignatureHex       *string        `json:"signature_hex"`
	UnsignedPayloadHex *string        `json:"unsigned_payload_hex"`
	CanonicalHex       string         `json:"canonical_hex"`
	DigestHex          *string        `json:"digest_hex"`
	ErrorType          string         `json:"error_type"`
	ErrorMessage       string         `json:"error_message"`
}

func TestDecodeLegacyV1ClaimValueMatchesPinnedPythonOracle(t *testing.T) {
	fixtures := runLegacyV1ClaimPythonOracle(t)
	if len(fixtures) != 16 {
		t.Fatalf("Python fixture count = %d, want 16", len(fixtures))
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			payload, err := hex.DecodeString(fixture.PayloadHex)
			if err != nil {
				t.Fatal(err)
			}
			decoded, metadata, err := DecodeLegacyV1ClaimValueWithMetadata(payload)
			if !fixture.OK {
				if decoded != nil || metadata != nil || err == nil {
					t.Fatalf("Go decode = %+v, %+v, %v; Python error = %s: %s", decoded, metadata, err, fixture.ErrorType, fixture.ErrorMessage)
				}
				var pythonError *LegacyV1ClaimDecodeError
				if !errors.As(err, &pythonError) || pythonError.PythonErrorName() != fixture.ErrorType ||
					err.Error() != fixture.ErrorMessage {
					t.Fatalf("Go error = %T %q/%q; Python = %q/%q", err, pythonError.PythonErrorName(), err, fixture.ErrorType, fixture.ErrorMessage)
				}
				return
			}
			if err != nil {
				t.Fatalf("Go error = %v; Python decoded successfully", err)
			}
			if decoded.Type != fixture.Type || decoded.IsSigned() != fixture.Signed ||
				!reflect.DeepEqual(decoded.Value, fixture.Value) || metadata.Version != 1 ||
				metadata.SignatureType != fixture.SignatureType ||
				!reflect.DeepEqual(optionalLegacyV1Hex(decoded.SigningChannelHash, !decoded.IsSigned()), fixture.ChannelHashHex) ||
				!reflect.DeepEqual(optionalLegacyV1Hex(decoded.Signature, !decoded.IsSigned()), fixture.SignatureHex) ||
				!reflect.DeepEqual(optionalLegacyV1Hex(metadata.UnsignedPayload, metadata.UnsignedPayload == nil), fixture.UnsignedPayloadHex) ||
				hex.EncodeToString(decoded.Canonical) != fixture.CanonicalHex {
				t.Fatalf("Go = value %+v metadata %+v; Python = %+v", decoded, metadata, fixture)
			}
			plain, err := DecodeLegacyV1ClaimValue(payload)
			if err != nil || !reflect.DeepEqual(plain, decoded) {
				t.Fatalf("plain DecodeLegacyV1ClaimValue = %+v, %v", plain, err)
			}
			if fixture.DigestHex != nil {
				digest, err := LegacyV1ClaimSignatureDigest("bTMNi3y5tYPZrLRjeuqMXtvcXrA97wAUAZ", decoded, metadata)
				if err != nil || hex.EncodeToString(digest[:]) != *fixture.DigestHex {
					t.Fatalf("legacy signature digest = %x, %v; want %s", digest, err, *fixture.DigestHex)
				}
			}
		})
	}
}

func TestDecodeLegacyV1ClaimValueFormatBoundaryAndCopies(t *testing.T) {
	if value, metadata, err := DecodeLegacyV1ClaimValueWithMetadata(nil); value != nil || metadata != nil ||
		!errors.Is(err, ErrInvalidLegacyV1ClaimValue) {
		t.Fatalf("empty v1 claim = %+v, %+v, %v", value, metadata, err)
	} else if named, ok := err.(interface{ PythonErrorName() string }); !ok || named.PythonErrorName() != "IndexError" {
		t.Fatalf("empty Python error = %T %v", err, err)
	}
	for _, payload := range [][]byte{{0}, {1}, []byte("{}")} {
		if value, err := DecodeLegacyV1ClaimValue(payload); value != nil || !errors.Is(err, ErrNotLegacyV1ClaimValue) {
			t.Fatalf("non-v1 payload %x = %+v, %v", payload, value, err)
		}
	}
	if _, err := LegacyV1ClaimSignatureDigest("1", nil, nil); !errors.Is(err, ErrLegacyV1SignatureMaterial) {
		t.Fatalf("missing digest material error = %v", err)
	}
}

func TestLegacyV1ClaimDescriptorBundleDigest(t *testing.T) {
	compressed, err := base64.StdEncoding.DecodeString(legacyV1DescriptorSetGzipBase64)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(compressed)); got != "1e6c9ed99c35f0419096c43dfcd3ed2fa2f93b85976e103727d49a4c6ef98662" {
		t.Fatalf("compressed legacy descriptor SHA256 = %s", got)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "3bdb37ca5594b0f4bebbac8ba49df063231a81035584546066e8a9793d779c2f" {
		t.Fatalf("raw legacy descriptor SHA256 = %s", got)
	}
}

func optionalLegacyV1Hex(value []byte, absent bool) *string {
	if absent {
		return nil
	}
	encoded := hex.EncodeToString(value)
	return &encoded
}

func runLegacyV1ClaimPythonOracle(t *testing.T) []legacyV1ClaimOracleFixture {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate legacy v1 claim oracle source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}

	sources := map[string]string{
		"lbry/schema/attrs.py":                     "e2c01abf8a152ca224f557d38a4932b40ce0ceb880c27b2dbe0bca15c4a51624",
		"lbry/schema/base.py":                      "898875eebd916eee0ea4ad9e2be8aff53f8f56a1479f664e143f0461ffba7140",
		"lbry/schema/claim.py":                     "2b2a58f580efc2d5ea7bbfadfff28ea150429dbcc71fcefaf8992fa5213027af",
		"lbry/schema/compat.py":                    "e4659df85632e329a5e1b786ea17c0d65336b46a81da3e17a7d907aafb6928ff",
		"lbry/schema/mime_types.py":                "cd4314aa1a8ea55091955f733a9d08d2a6b0e88db2a42b034fee19cbbb1c9fd3",
		"lbry/schema/types/v1/certificate_pb2.py":  "b9826daaf405775656126807e83d6ca08d5a3b755e004b112305e2aeea29cc58",
		"lbry/schema/types/v1/fee_pb2.py":          "3718d1671bda274cd022fdafb40d6e781580743cec2cb4ba2c77042a9f983295",
		"lbry/schema/types/v1/legacy_claim_pb2.py": "ea30a98d92e10fe2918ad64ae33c2c9e438b79742a99a95499ac14cd896d4383",
		"lbry/schema/types/v1/metadata_pb2.py":     "9a6781ad31213480c94f50672464e985b9473488dba6a1b4487cdd375d80a1a0",
		"lbry/schema/types/v1/signature_pb2.py":    "3dd1167ffd474592c94ca39816a606a5ad4e2bf6d55f67835a03b449ce749e40",
		"lbry/schema/types/v1/source_pb2.py":       "a10fc644965aded228ce2e41bbbb64bee98d0f8c1ca5a9c5b86f46bb674807b9",
		"lbry/schema/types/v1/stream_pb2.py":       "1657b2a2c3f3f6234f0ca1ec6c0a80c9fcd724096d449c5ca9f3a35ec5cc09a1",
		"lbry/schema/types/v2/claim_pb2.py":        "3edb36895d7d2f294e27019438332ca8a7ed4cb3c0f30ee33c9aa406bf000c98",
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

	command := exec.Command(python, "-c", legacyV1ClaimPythonProbe, sdkRoot)
	command.Env = append(os.Environ(), "PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION=python")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python legacy v1 claim oracle failed: %v\n%s", err, stderr.String())
	}
	var fixtures []legacyV1ClaimOracleFixture
	if err := json.Unmarshal(output, &fixtures); err != nil {
		t.Fatalf("decode Python legacy v1 claim oracle: %v\n%s", err, output)
	}
	return fixtures
}

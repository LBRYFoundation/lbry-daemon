#!/usr/bin/env python3
"""Pinned offline probes for legacy claim conversion in transaction_show.

The real SDK Claim.from_bytes path converts v0 JSON and v1 proto2 payloads to
the v2 Claim message.  The selected JSONResponseEncoder methods are then
AST-executed against deterministic output fixtures.  No network or wallet
database is used.
"""

import argparse
import ast
from binascii import hexlify, unhexlify
import copy
from datetime import datetime
from decimal import Decimal
import hashlib
import json
from json import JSONEncoder
import os
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace
import types


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py":
        "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/constants.py":
        "db92b1aa8be15be77e5ac582daecc23b1842c4b509b1a438b717285d6eaf1ced",
    "lbry/crypto/base58.py":
        "bed7bff1169a9d7b89473a08b6054170f52f50e3fb2126a9752c36e745e9b94f",
    "lbry/crypto/hash.py":
        "bfc430bd3fe98578b406caa3a8e2116a40f492c7b68e269176e838b4ef426a72",
    "lbry/crypto/util.py":
        "b943c2c53ad09d808b09977edf2b274e21c33a2e70bdaca9eb0e8a7363d20727",
    "lbry/error/__init__.py":
        "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
    "lbry/error/base.py":
        "2cfc86ca9ac8396fad5009c12afda5e682c6ec1cb64911de9bfe78e464671b76",
    "lbry/extras/daemon/json_response_encoder.py":
        "047fd406c20236025414b8805669b1a830b0b412386c1613498aa1ebaa021732",
    "lbry/schema/__init__.py":
        "f6ee4d84982fdc48b44843c051a49433d1eb7700b8c1cd7d4de5356301dc7a39",
    "lbry/schema/attrs.py":
        "e2c01abf8a152ca224f557d38a4932b40ce0ceb880c27b2dbe0bca15c4a51624",
    "lbry/schema/base.py":
        "898875eebd916eee0ea4ad9e2be8aff53f8f56a1479f664e143f0461ffba7140",
    "lbry/schema/claim.py":
        "2b2a58f580efc2d5ea7bbfadfff28ea150429dbcc71fcefaf8992fa5213027af",
    "lbry/schema/compat.py":
        "e4659df85632e329a5e1b786ea17c0d65336b46a81da3e17a7d907aafb6928ff",
    "lbry/schema/mime_types.py":
        "cd4314aa1a8ea55091955f733a9d08d2a6b0e88db2a42b034fee19cbbb1c9fd3",
    "lbry/schema/tags.py":
        "32409bb011eaf2b0ff65a96889ac2ba8cb306f8005f750d96f539dea53a61837",
    "lbry/schema/types/v1/certificate_pb2.py":
        "b9826daaf405775656126807e83d6ca08d5a3b755e004b112305e2aeea29cc58",
    "lbry/schema/types/v1/fee_pb2.py":
        "3718d1671bda274cd022fdafb40d6e781580743cec2cb4ba2c77042a9f983295",
    "lbry/schema/types/v1/legacy_claim_pb2.py":
        "ea30a98d92e10fe2918ad64ae33c2c9e438b79742a99a95499ac14cd896d4383",
    "lbry/schema/types/v1/metadata_pb2.py":
        "9a6781ad31213480c94f50672464e985b9473488dba6a1b4487cdd375d80a1a0",
    "lbry/schema/types/v1/signature_pb2.py":
        "3dd1167ffd474592c94ca39816a606a5ad4e2bf6d55f67835a03b449ce749e40",
    "lbry/schema/types/v1/source_pb2.py":
        "a10fc644965aded228ce2e41bbbb64bee98d0f8c1ca5a9c5b86f46bb674807b9",
    "lbry/schema/types/v1/stream_pb2.py":
        "1657b2a2c3f3f6234f0ca1ec6c0a80c9fcd724096d449c5ca9f3a35ec5cc09a1",
    "lbry/schema/types/v2/claim_pb2.py":
        "3edb36895d7d2f294e27019438332ca8a7ed4cb3c0f30ee33c9aa406bf000c98",
    "lbry/wallet/dewies.py":
        "67506d75a5f0ddb3f7c2ea832ba7b13fb49ae4193f060a1fdf541b5f50a3084a",
    "lbry/wallet/util.py":
        "08f697c88ec36d2bb417609194266f279eba2f69b1a62a10b1de69b9c1733d5a",
}
PINNED_METHOD_HASHES = {
    "Claim.from_bytes":
        "3ce060b4daf38073681481493f45c6bbfc1f6b132466617f514b38772dd23ad5",
    "JSONResponseEncoder.__init__":
        "bf1a658c1eed62bbae283ebe132f8067f986e534771ebf3417685536472fdb1e",
    "JSONResponseEncoder.default":
        "298986ed087ef927a948ecc2d8f55730ca2e57a9c6ec032255d30bc92448c4a8",
    "JSONResponseEncoder.encode_claim":
        "c537d439cc940682b1954615726587d125615e5d4bda62f26b9e78085c5ed088",
    "JSONResponseEncoder.encode_claim_meta":
        "7998df829f2f3a45d3f851ec1fa08910d4c6106c58a0a5a22690390ff8371c05",
    "JSONResponseEncoder.encode_output":
        "fc124a8362451a2449d83b06e252d9c3d85ec6b006b5f9d0dc5dfd60b5db92be",
    "Signable.from_bytes":
        "9a21b3bc622983084470ddef59d5f0a057e618bfc39cd399cd64bf4c6e52360c",
    "Signable.is_signed":
        "e8366b90739fe59a0844b1cbd87c31c10993f6206d7f43db618c04ebd35005d1",
    "Signable.signing_channel_id":
        "e5d2e50f1fdf9f2a7cb055ef9b6c79d33de65bea2fbda924d90000adb307feba",
    "Signable.to_bytes":
        "c8b13a227f46397354450ae1921ac124d5bc61896c724dd1bf2361f51b75d5bf",
    "Signable.to_message_bytes":
        "abba7749d2619869c1bd3266bc307bf06f43d26172ed497a2ac4c67ca2fc740f",
    "dewies_to_lbc":
        "e134ee4ea5e7d5000bb7f3a1d37dd40b6913724e142ba5c6b8e1f235c064fc5b",
    "from_old_json_schema":
        "fb2ad0944c9d930475dfa58dc272daf1ca2f55dbe611300108a9ed0a38d51f9a",
    "from_types_v1":
        "ffbc2cf47f628bef0580606ee8c09f3ee83ee64dc17d4c27bb143d0f0c26dc69",
    "satoshis_to_coins":
        "ff81838bc9fc0d2583372395b8299c1cd6aca6ee95b5e4819b28e883b2e1ad50",
}

TXID = "ab" * 32
CLAIM_ID = "11" * 20
CLAIM_NAME = "Legacy-Claim"
ADDRESS = "fixture-address"

V0_RICH_JSON = (
    b'{"sources":{"lbry_sd_hash":"00112233445566778899aabbccddeeff"},'
    b'"content_type":"video/mp4","title":"Legacy JSON title",'
    b'"description":"Legacy JSON description","thumbnail":"https://example.test/v0.jpg",'
    b'"author":"Legacy author","license":"CC-BY","license_url":"https://example.test/license",'
    b'"language":"English","nsfw":true,'
    b'"fee":{"LBC":{"amount":"1.23456789","address":"1"}}}'
)


def verify_source(sdk_root):
    commit = subprocess.run(
        ["git", "-C", str(sdk_root), "rev-parse", "HEAD"],
        check=True, capture_output=True, text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    for relative, expected in PINNED_SOURCE_HASHES.items():
        actual = hashlib.sha256((sdk_root / relative).read_bytes()).hexdigest()
        if actual != expected:
            raise RuntimeError(f"{relative} hash is {actual}, expected {expected}")


def method_hash(path, class_name, method_name, occurrence=0):
    source = path.read_text()
    scope = ast.parse(source).body
    if class_name is not None:
        scope = next(
            node.body for node in scope
            if isinstance(node, ast.ClassDef) and node.name == class_name
        )
    matches = [
        node for node in scope
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name == method_name
    ]
    node = matches[occurrence]
    return hashlib.sha256(ast.get_source_segment(source, node).encode()).hexdigest()


def verify_method_hashes(sdk_root):
    specs = {
        "Claim.from_bytes": ("lbry/schema/claim.py", "Claim", "from_bytes", 0),
        "Signable.from_bytes": ("lbry/schema/base.py", "Signable", "from_bytes", 0),
        "Signable.is_signed": ("lbry/schema/base.py", "Signable", "is_signed", 0),
        "Signable.signing_channel_id":
            ("lbry/schema/base.py", "Signable", "signing_channel_id", 0),
        "Signable.to_bytes": ("lbry/schema/base.py", "Signable", "to_bytes", 0),
        "Signable.to_message_bytes":
            ("lbry/schema/base.py", "Signable", "to_message_bytes", 0),
        "from_old_json_schema":
            ("lbry/schema/compat.py", None, "from_old_json_schema", 0),
        "from_types_v1": ("lbry/schema/compat.py", None, "from_types_v1", 0),
        "dewies_to_lbc": ("lbry/wallet/dewies.py", None, "dewies_to_lbc", 0),
        "satoshis_to_coins":
            ("lbry/wallet/util.py", None, "satoshis_to_coins", 0),
    }
    for method in (
        "__init__", "default", "encode_output", "encode_claim_meta", "encode_claim",
    ):
        specs[f"JSONResponseEncoder.{method}"] = (
            "lbry/extras/daemon/json_response_encoder.py",
            "JSONResponseEncoder", method, 0,
        )
    hashes = {
        name: method_hash(sdk_root / relative, class_name, method_name, occurrence)
        for name, (relative, class_name, method_name, occurrence) in specs.items()
    }
    if hashes != PINNED_METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {PINNED_METHOD_HASHES}")
    return hashes


def selected_functions(path, names):
    source = path.read_text()
    selected = []
    for node in ast.parse(source).body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in names:
            selected.append(copy.deepcopy(node))
    return selected


def selected_methods(path, class_name, names):
    source_class = next(
        node for node in ast.parse(path.read_text()).body
        if isinstance(node, ast.ClassDef) and node.name == class_name
    )
    return [
        copy.deepcopy(node) for node in source_class.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in names
    ]


def extracted_class(name, methods, base=None):
    bases = [] if base is None else [ast.Name(base, ast.Load())]
    return ast.ClassDef(name, bases, [], methods or [ast.Pass()], [])


def extract_encoder(sdk_root, Claim, DecodeError):
    encoder_path = sdk_root / "lbry/extras/daemon/json_response_encoder.py"
    encoder_methods = selected_methods(
        encoder_path, "JSONResponseEncoder",
        {"__init__", "default", "encode_output", "encode_claim_meta", "encode_claim"},
    )
    module = ast.fix_missing_locations(ast.Module(body=[
        *selected_functions(sdk_root / "lbry/wallet/util.py", {"satoshis_to_coins"}),
        *selected_functions(sdk_root / "lbry/wallet/dewies.py", {"dewies_to_lbc"}),
        extracted_class("JSONResponseEncoder", encoder_methods, "JSONEncoder"),
    ], type_ignores=[]))
    namespace = {
        "Account": type("Account", (), {}),
        "Claim": Claim,
        "COIN": 100_000_000,
        "Decimal": Decimal,
        "DecodeError": DecodeError,
        "JSONEncoder": JSONEncoder,
        "Ledger": type("Ledger", (), {}),
        "ManagedStream": type("ManagedStream", (), {}),
        "Output": type("Output", (), {}),
        "PublicKey": type("PublicKey", (), {}),
        "Support": type("Support", (), {}),
        "TorrentSource": type("TorrentSource", (), {}),
        "Transaction": type("Transaction", (), {}),
        "Wallet": type("Wallet", (), {}),
        "datetime": datetime,
        "hexlify": hexlify,
        "unhexlify": unhexlify,
    }
    exec(compile(module, str(encoder_path), "exec"), namespace)
    return namespace


def install_optional_dependency_stubs():
    filetype = types.ModuleType("filetype")
    filetype.guess = lambda _path: None
    sys.modules["filetype"] = filetype

    asn1crypto = types.ModuleType("asn1crypto")
    asn1crypto.__path__ = []
    sys.modules["asn1crypto"] = asn1crypto
    asn1crypto_keys = types.ModuleType("asn1crypto.keys")
    asn1crypto_keys.PublicKeyInfo = type("PublicKeyInfo", (), {})
    sys.modules["asn1crypto.keys"] = asn1crypto_keys

    coincurve = types.ModuleType("coincurve")
    coincurve.PublicKey = type("PublicKey", (), {})
    sys.modules["coincurve"] = coincurve

    hachoir = types.ModuleType("hachoir")
    hachoir.__path__ = []
    sys.modules["hachoir"] = hachoir
    hachoir_core = types.ModuleType("hachoir.core")
    hachoir_core.__path__ = []
    sys.modules["hachoir.core"] = hachoir_core
    hachoir_log = types.ModuleType("hachoir.core.log")
    hachoir_log.log = type("Log", (), {"use_print": False})()
    sys.modules["hachoir.core.log"] = hachoir_log
    hachoir_parser = types.ModuleType("hachoir.parser")
    hachoir_parser.createParser = lambda _path: None
    sys.modules["hachoir.parser"] = hachoir_parser
    hachoir_metadata = types.ModuleType("hachoir.metadata")
    hachoir_metadata.extractMetadata = lambda _parser: None
    sys.modules["hachoir.metadata"] = hachoir_metadata


class FixtureHeaders:
    height = 10

    @staticmethod
    def estimated_timestamp(height, try_real_headers=True):
        del try_real_headers
        return 7_000 + height if height > 0 else None


class FixtureLedger:
    headers = FixtureHeaders()

    @staticmethod
    def public_key_to_address(public_key):
        del public_key
        return "fixture-public-key-address"


class FixtureScript:
    def __init__(self, operation, payload):
        self.is_claim_name = operation == "create"
        self.is_update_claim = operation == "update"
        self.is_support_claim = False
        self.is_support_claim_data = False
        self.is_return_data = False
        self.is_claim_involved = True
        self.payload = payload


class FixtureOutput:
    def __init__(self, operation, payload, Claim):
        self.tx_ref = SimpleNamespace(id=TXID, height=7)
        self.position = 0
        self.amount = 100_000_000
        self.script = FixtureScript(operation, payload)
        self.address = ADDRESS
        self.has_address = True
        self.is_spent = None
        self.is_my_output = None
        self.is_my_input = None
        self.sent_supports = None
        self.sent_tips = None
        self.received_tips = None
        self.is_internal_transfer = None
        self.purchase = None
        self.purchased_claim = None
        self.purchase_receipt = None
        self.reposted_claim = None
        self.claims = None
        self.channel = None
        self.meta = {}
        self.claim_name = CLAIM_NAME
        self.normalized_name = CLAIM_NAME.casefold()
        self.claim_id = CLAIM_ID
        self.permanent_url = f"lbry://{CLAIM_NAME}#{CLAIM_ID}"
        self.has_private_key = False
        self._claim = None
        self.Claim = Claim

    def get_address(self, ledger):
        del ledger
        return self.address

    @property
    def claim(self):
        if self._claim is None:
            self._claim = self.Claim.from_bytes(self.script.payload)
        return self._claim

    @property
    def signable(self):
        return self.claim


def make_v1_stream(OldClaim, signed=False, unsupported_currency=False):
    old = OldClaim()
    old.version = 1
    old.claimType = 1
    old.stream.version = 1
    metadata = old.stream.metadata
    metadata.version = 4
    metadata.language = 1
    metadata.title = "Legacy V1 title"
    metadata.description = "Legacy V1 description"
    metadata.author = "Legacy V1 author"
    metadata.license = "CC-BY-SA"
    metadata.licenseUrl = "https://example.test/v1-license"
    metadata.thumbnail = "https://example.test/v1.jpg"
    metadata.nsfw = True
    metadata.fee.version = 1
    metadata.fee.currency = 0 if unsupported_currency else 1
    metadata.fee.address = b"\x00"
    metadata.fee.amount = 1.25
    source = old.stream.source
    source.version = 1
    source.sourceType = 1
    source.source = bytes.fromhex("11223344556677889900aabbccddeeff")
    source.contentType = "video/mp4"
    if signed:
        signature = old.publisherSignature
        signature.version = 1
        signature.signatureType = 3
        signature.signature = bytes(range(64))
        signature.certificateId = bytes.fromhex("00112233445566778899aabbccddeeff00112233")
    return old.SerializeToString()


def make_v1_channel(OldClaim):
    old = OldClaim()
    old.version = 1
    old.claimType = 2
    old.certificate.version = 1
    old.certificate.keyType = 3
    old.certificate.publicKey = b"\x02" + (b"\x44" * 32)
    return old.SerializeToString()


def make_v1_incomplete_signed(OldClaim):
    old = OldClaim()
    old.claimType = 1
    old.publisherSignature.certificateId = b"\x10\x20"
    return old.SerializePartialToString()


def fixture_cases(OldClaim):
    payloads = [
        ("v0_json_stream", "v0_json", V0_RICH_JSON),
        ("v1_stream_unsigned", "v1_proto2", make_v1_stream(OldClaim)),
        ("v1_stream_signed", "v1_proto2", make_v1_stream(OldClaim, signed=True)),
        ("v1_channel", "v1_proto2", make_v1_channel(OldClaim)),
    ]
    cases = []
    for base_name, legacy_format, payload in payloads:
        for operation in ("create", "update"):
            for include_protobuf in (False, True):
                cases.append({
                    "name": f"{base_name}_{operation}_{'protobuf' if include_protobuf else 'plain'}",
                    "legacy_format": legacy_format,
                    "operation": operation,
                    "include_protobuf": include_protobuf,
                    "payload": payload,
                })

    malformed = [
        ("empty_payload", "unknown", b""),
        ("malformed_v2", "v2_envelope", b"\x00\x80"),
        ("short_signed_v2", "v2_envelope", b"\x01"),
        ("truncated_v1", "v1_proto2", b"\x08"),
        ("malformed_v0_json", "v0_json", b"{"),
        ("v0_json_missing_sources", "v0_json", b"{}"),
        ("v0_json_unknown_currency", "v0_json", (
            b'{"sources":{"lbry_sd_hash":"00"},'
            b'"fee":{"DOGE":{"amount":"1","address":"1"}}}'
        )),
        ("v0_json_odd_sd_hash", "v0_json", b'{"sources":{"lbry_sd_hash":"0"}}'),
        ("v0_json_leading_space", "v0_json", b" " + V0_RICH_JSON),
        ("v1_signed_missing_required", "v1_proto2", make_v1_incomplete_signed(OldClaim)),
        ("v1_unknown_fee_currency", "v1_proto2", make_v1_stream(
            OldClaim, unsupported_currency=True,
        )),
    ]
    for name, legacy_format, payload in malformed:
        cases.append({
            "name": name,
            "legacy_format": legacy_format,
            "operation": "create",
            "include_protobuf": True,
            "payload": payload,
        })
    return cases


def error_dict(error, stage):
    return {
        "stage": stage,
        "type": type(error).__name__,
        "module": type(error).__module__,
        "message": str(error),
    }


def conversion_dict(payload, Claim, encoder):
    claim = Claim.from_bytes(payload)
    return {
        "source_version": claim.version,
        "value_type": claim.claim_type,
        "value": json.loads(encoder.encode(claim)),
        "is_signed": claim.is_signed,
        "signature_type": claim.signature_type,
        "signature_hex": claim.signature.hex() if claim.signature is not None else None,
        "signing_channel_hash_hex": (
            claim.signing_channel_hash.hex() if claim.signing_channel_hash is not None else None
        ),
        "signing_channel_id": claim.signing_channel_id,
        "unsigned_v1_payload_hex": (
            claim.unsigned_payload.hex() if claim.unsigned_payload is not None else None
        ),
        "v2_message_hex": claim.to_message_bytes().hex(),
        "canonical_v2_hex": claim.to_bytes().hex(),
    }


def execute_case(fixture, Claim, encoder_class):
    encoder = encoder_class(
        ledger=FixtureLedger(), include_protobuf=fixture["include_protobuf"],
    )
    result = {
        "name": fixture["name"],
        "legacy_format": fixture["legacy_format"],
        "operation": fixture["operation"],
        "include_protobuf": fixture["include_protobuf"],
        "original_payload_hex": fixture["payload"].hex(),
        "conversion": None,
        "conversion_error": None,
        "encoded_output": None,
        "encoded_output_fields": None,
        "encoding_error": None,
    }
    try:
        result["conversion"] = conversion_dict(fixture["payload"], Claim, encoder)
    except Exception as error:
        result["conversion_error"] = error_dict(error, "Claim.from_bytes/conversion")

    output = FixtureOutput(fixture["operation"], fixture["payload"], Claim)
    try:
        encoded = encoder.encode_output(output)
        result["encoded_output_fields"] = sorted(encoded)
    except Exception as error:
        result["encoding_error"] = error_dict(error, "JSONResponseEncoder.encode_output")
        return result
    try:
        result["encoded_output"] = json.loads(encoder.encode(encoded))
    except Exception as error:
        result["encoding_error"] = error_dict(error, "JSONResponseEncoder JSON serialization")
    return result


def run(sdk_root):
    verify_source(sdk_root)
    method_hashes = verify_method_hashes(sdk_root)
    os.environ.setdefault("PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION", "python")
    sys.path.insert(0, str(sdk_root))
    install_optional_dependency_stubs()

    from google.protobuf.message import DecodeError
    from lbry import __version__
    from lbry.schema.claim import Claim
    from lbry.schema.types.v1.legacy_claim_pb2 import Claim as OldClaim

    if __version__ != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {__version__}, expected {PINNED_VERSION}")
    namespace = extract_encoder(sdk_root, Claim, DecodeError)
    namespace["Output"] = FixtureOutput
    encoder_class = namespace["JSONResponseEncoder"]
    cases = [
        execute_case(fixture, Claim, encoder_class)
        for fixture in fixture_cases(OldClaim)
    ]
    return {
        "reference": {
            "commit": PINNED_COMMIT,
            "version": PINNED_VERSION,
            "source_sha256": PINNED_SOURCE_HASHES,
            "method_sha256": method_hashes,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "protobuf_version": __import__("google.protobuf").protobuf.__version__,
            "real_claim_from_bytes_executed": True,
            "real_legacy_proto2_messages_executed": True,
            "extracted_encoder_methods_executed": True,
            "external_network_used": False,
            "case_count": len(cases),
            "matrix_case_count": 16,
            "malformed_case_count": 11,
        },
        "cases": cases,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    arguments = parser.parse_args()
    print(json.dumps(run(arguments.sdk_root.resolve()), sort_keys=True))


if __name__ == "__main__":
    main()

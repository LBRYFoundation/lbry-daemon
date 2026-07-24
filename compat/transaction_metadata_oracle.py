#!/usr/bin/env python3
"""Pinned legacy Claim.from_bytes probes for wallet metadata projection."""

import argparse
import hashlib
import json
import math
from pathlib import Path
import subprocess
import sys
import types


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/constants.py": "db92b1aa8be15be77e5ac582daecc23b1842c4b509b1a438b717285d6eaf1ced",
    "lbry/crypto/base58.py": "bed7bff1169a9d7b89473a08b6054170f52f50e3fb2126a9752c36e745e9b94f",
    "lbry/crypto/hash.py": "bfc430bd3fe98578b406caa3a8e2116a40f492c7b68e269176e838b4ef426a72",
    "lbry/crypto/util.py": "b943c2c53ad09d808b09977edf2b274e21c33a2e70bdaca9eb0e8a7363d20727",
    "lbry/error/__init__.py": "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
    "lbry/error/base.py": "2cfc86ca9ac8396fad5009c12afda5e682c6ec1cb64911de9bfe78e464671b76",
    "lbry/schema/attrs.py": "e2c01abf8a152ca224f557d38a4932b40ce0ceb880c27b2dbe0bca15c4a51624",
    "lbry/schema/base.py": "898875eebd916eee0ea4ad9e2be8aff53f8f56a1479f664e143f0461ffba7140",
    "lbry/schema/claim.py": "2b2a58f580efc2d5ea7bbfadfff28ea150429dbcc71fcefaf8992fa5213027af",
    "lbry/schema/compat.py": "e4659df85632e329a5e1b786ea17c0d65336b46a81da3e17a7d907aafb6928ff",
    "lbry/schema/mime_types.py": "cd4314aa1a8ea55091955f733a9d08d2a6b0e88db2a42b034fee19cbbb1c9fd3",
    "lbry/schema/tags.py": "32409bb011eaf2b0ff65a96889ac2ba8cb306f8005f750d96f539dea53a61837",
    "lbry/schema/types/v1/certificate_pb2.py": "b9826daaf405775656126807e83d6ca08d5a3b755e004b112305e2aeea29cc58",
    "lbry/schema/types/v1/fee_pb2.py": "3718d1671bda274cd022fdafb40d6e781580743cec2cb4ba2c77042a9f983295",
    "lbry/schema/types/v1/legacy_claim_pb2.py": "ea30a98d92e10fe2918ad64ae33c2c9e438b79742a99a95499ac14cd896d4383",
    "lbry/schema/types/v1/metadata_pb2.py": "9a6781ad31213480c94f50672464e985b9473488dba6a1b4487cdd375d80a1a0",
    "lbry/schema/types/v1/signature_pb2.py": "3dd1167ffd474592c94ca39816a606a5ad4e2bf6d55f67835a03b449ce749e40",
    "lbry/schema/types/v1/source_pb2.py": "a10fc644965aded228ce2e41bbbb64bee98d0f8c1ca5a9c5b86f46bb674807b9",
    "lbry/schema/types/v1/stream_pb2.py": "1657b2a2c3f3f6234f0ca1ec6c0a80c9fcd724096d449c5ca9f3a35ec5cc09a1",
    "lbry/schema/types/v2/claim_pb2.py": "3edb36895d7d2f294e27019438332ca8a7ed4cb3c0f30ee33c9aa406bf000c98",
}


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


def v1_signed(old_claim, include_version=True, include_empty_stream=False):
    if include_version:
        old_claim.version = 1
    old_claim.claimType = 1
    if include_empty_stream:
        old_claim.stream.SetInParent()
    old_claim.publisherSignature.certificateId = bytes((0x10, 0x20))
    return old_claim.SerializePartialToString()


def v1_fee(old_claim, currency, amount):
    old_claim.claimType = 1
    fee = old_claim.stream.metadata.fee
    fee.currency = currency
    fee.amount = amount
    return old_claim.SerializePartialToString()


def probe(name, payload, Claim):
    result = {"name": name, "payload_hex": payload.hex()}
    try:
        claim = Claim.from_bytes(payload)
        result.update({
            "decoded": True,
            "claim_type": claim.claim_type,
            "has_source": claim.stream.has_source if claim.is_stream else False,
            "channel_id": claim.signing_channel_id if claim.is_signed else None,
            "error_type": None,
        })
    except Exception as error:  # Claim.can_decode_claim catches every exception.
        result.update({
            "decoded": False,
            "claim_type": None,
            "has_source": False,
            "channel_id": None,
            "error_type": type(error).__name__,
        })
    return result


def run(sdk_root):
    verify_source(sdk_root)
    sys.path.insert(0, str(sdk_root))
    install_optional_dependency_stubs()

    from lbry import __version__
    from lbry.schema.claim import Claim
    from lbry.schema.types.v1.legacy_claim_pb2 import Claim as OldClaim

    if __version__ != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {__version__}, expected {PINNED_VERSION}")

    fixtures = [
        ("v1_signed", v1_signed(OldClaim())),
        ("v1_signed_missing_version", v1_signed(OldClaim(), include_version=False)),
        ("v1_signed_incomplete_stream", v1_signed(OldClaim(), include_empty_stream=True)),
    ]
    unsigned_incomplete = OldClaim()
    unsigned_incomplete.claimType = 1
    unsigned_incomplete.stream.SetInParent()
    fixtures.append(("v1_unsigned_incomplete_stream", unsigned_incomplete.SerializePartialToString()))
    fixtures.extend([
        ("v1_fee_lbc", v1_fee(OldClaim(), 1, 1.25)),
        ("v1_fee_unknown_currency", v1_fee(OldClaim(), 0, 1.0)),
        ("v1_fee_negative", v1_fee(OldClaim(), 1, -1.0)),
        ("v1_fee_tiny_negative", v1_fee(OldClaim(), 1, -1e-10)),
        ("v1_fee_nan", v1_fee(OldClaim(), 1, math.nan)),
        ("v1_fee_infinity", v1_fee(OldClaim(), 1, math.inf)),
        ("json_fee_lbc", b'{"sources":{"lbry_sd_hash":"00"},"fee":{"LBC":{"amount":"1","address":"1"}}}'),
        ("json_fee_lbc_number", b'{"sources":{"lbry_sd_hash":"00"},"fee":{"LBC":{"amount":1.25,"address":"1"}}}'),
        ("json_fee_max", b'{"sources":{"lbry_sd_hash":"00"},"fee":{"LBC":{"amount":"184467440737.09551615","address":"1"}}}'),
        ("json_fee_overflow", b'{"sources":{"lbry_sd_hash":"00"},"fee":{"LBC":{"amount":"184467440737.09551616","address":"1"}}}'),
        ("json_fee_bad_decimal", b'{"sources":{"lbry_sd_hash":"00"},"fee":{"LBC":{"amount":"bad","address":"1"}}}'),
        ("json_fee_empty_address", b'{"sources":{"lbry_sd_hash":"00"},"fee":{"LBC":{"amount":"1","address":""}}}'),
        ("json_fee_bad_base58", b'{"sources":{"lbry_sd_hash":"00"},"fee":{"LBC":{"amount":"1","address":"0"}}}'),
        ("json_fee_usd_round", b'{"sources":{"lbry_sd_hash":"00"},"fee":{"USD":{"amount":"0.001","address":"1"}}}'),
        ("json_fee_usd_negative", b'{"sources":{"lbry_sd_hash":"00"},"fee":{"USD":{"amount":"-0.001","address":"1"}}}'),
    ])
    return {
        "reference": {
            "commit": PINNED_COMMIT,
            "version": PINNED_VERSION,
            "source_sha256": PINNED_SOURCE_HASHES,
        },
        "fixtures": [probe(name, payload, Claim) for name, payload in fixtures],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    arguments = parser.parse_args()
    print(json.dumps(run(arguments.sdk_root.resolve()), sort_keys=True))


if __name__ == "__main__":
    main()

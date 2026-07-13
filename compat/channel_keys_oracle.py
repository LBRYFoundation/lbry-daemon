#!/usr/bin/env python3
"""Model the pinned SDK channel-key contract without third-party imports.

Input is one JSON object. Every top-level case collection is optional::

    {
      "sign_cases": [{
        "name": "vector", "private_key_hex": "<32 bytes>",
        "digest_hex": "<at least 32 bytes; signing ignores the remainder>"
      }],
      "verify_cases": [{
        "name": "vector", "public_key_hex": "<compressed 33 bytes>",
        "signature_hex": "<raw r||s, 64 bytes>",
        "digest_hex": "<32 bytes>"
      }],
      "pem_cases": [{
        "name": "encode", "operation": "encode",
        "private_key_hex": "<32 bytes>"
      }, {
        "name": "decode", "operation": "decode", "pem": "<PEM text>"
      }],
      "account_cases": [{
        "name": "ordered account", "address_prefix_hex": "55",
        "certificates": [["address", "PEM or arbitrary JSON value"]],
        "root_private_key_hex": "<32 bytes>",
        "root_chain_code_hex": "<32 bytes>",
        "usage": [true, false], "save_errors": [null],
        "actions": [
          {"action": "add", "private_key_hex": "<32 bytes>"},
          {"action": "get", "public_key_hex": "<bytes hashed verbatim>"},
          {"action": "migrate"}, {"action": "generate"},
          {"action": "prime"},
          {"action": "maybe", "public_key_hex": "<compressed 33 bytes>"},
          {"action": "get_cached", "address": "..."},
          {"action": "set_usage", "usage": [false]},
          {"action": "set_certificates", "certificates": [["key", "value"]]}
        ]
      }],
      "manager_cases": [{
        "name": "m/2/index", "seed_hex": "<master seed bytes>",
        "usage": [true, true, false],
        "actions": [{"action": "prime"}, {"action": "generate"}]
      }]
    }

``manager_cases`` and ``account_cases`` share the same sequential action
runner. A manager root may be supplied as ``seed_hex`` or as both
``root_private_key_hex`` and ``root_chain_code_hex``; omitting it models a
watch-only account. Usage entries are booleans, ``null``/``"error"``, or an
object ``{"error": "message"}``. Every usage lookup and wallet save is
recorded. ``save_errors`` entries use the same null/error convention.

Each stateless outcome contains ``result``, ``error_type``, and ``error``.
Each stateful action additionally contains the post-action ordered certificate
list, deterministic cache, ``last_known``, usage calls, and save snapshots.
The adapter safely rejects a signing digest shorter than 32 bytes because the
pinned CFFI call would read beyond that Python buffer. Longer signing buffers
use their first 32 bytes, matching the C API. Verification preserves the pinned
public-key, signature-length, digest-length, compact-scalar, high-S
normalization, and boolean failure behavior.

The implementation is stdlib-only. It reproduces Coincurve 15's PKCS#8 output
and accepts both SEC1 and PKCS#8 PEM input, including the pinned helper's
permissive treatment of PEM labels and base64 whitespace. SDK sources are
verified at runtime. Audited Coincurve sources may optionally be checked with
``--coincurve-root`` or ``COINCURVE15_SOURCE_PATH``. The audited, historically
unpinned asn1crypto 1.5.1 baseline can likewise be checked with
``--asn1crypto-wheel`` or ``ASN1CRYPTO151_WHEEL_PATH`` without becoming a
runtime dependency. ASN.1 fixture parsing is intentionally bounded to 1 MiB,
64 nested values, and 4096 nodes; these are oracle safety limits, not claims
about resource limits in the legacy SDK.
"""

import argparse
import ast
import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import subprocess
import sys
import zipfile


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_COINCURVE_VERSION = "15.0.0"
PINNED_ASN1CRYPTO_VERSION = "1.5.1"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/wallet/bip32.py": "bbc027ae706338bd7a232290c110dcefc308b2b635179e01f51487cf8b05825a",
    "lbry/wallet/account.py": "ea2ca30bddf9c0145469e989d9855dbe7be5184943ae7b8ca690eda41eb7db50",
    "setup.py": "1b7b416536ad4f44869216dd794aaa3b41ffd1c9eb1899cdf1799ab50961dae1",
}
PINNED_COINCURVE_SOURCE_HASHES = {
    "PKG-INFO": "a9d3fe9368f5cc87f6b20c28a3d721a7bfcfa4e7da6d21212838b9e087395a52",
    "setup.py": "6c511f37a76611877c11629f405439335c57930657bffa930e29e6eaf51e3ced",
    "coincurve/__init__.py": "e665970c7cf62662c581e5749ae1466a27c98569ede35ce24add3a502623d1c5",
    "coincurve/keys.py": "00963e19be4ae78fed97403cb3aae56bd378b6bd2a5677a93c2b5d0f6dd60998",
    "coincurve/utils.py": "60cf3d64c9752c1f3c9484c28649fa9155e9257dcc92e7f674526af7c185e1bd",
    "coincurve/ecdsa.py": "65e9538199d541362cad77860250242d417288188af502ab791ac7618c9f8174",
    "libsecp256k1/include/secp256k1.h": "2605a12002cdd6064733c04064e32e24466c7820495aa296acca75a87be532b9",
    "libsecp256k1/src/secp256k1.c": "2d2c8d66a652fe6557023d4ee8f88db50e6039b2666a34ab17acfac325f5af48",
    "libsecp256k1/src/hash_impl.h": "dcaccfbbf8001e28f728237e09cf2064518e66d3de848ee7697d834d5ef98251",
    "libsecp256k1/src/ecdsa_impl.h": "475d3dd3d6d4338201468465b88c08f8cf8cc7ff6c610d7cd3a03721bc01c9f7",
}
PINNED_ASN1CRYPTO_WHEEL_SHA256 = (
    "db4e40728b728508912cbb3d44f19ce188f218e9eba635821bb4b68564f8fd67"
)
PINNED_ASN1CRYPTO_SOURCE_HASHES = {
    "asn1crypto/__init__.py": "6bef152c96b37317de70e065c561f5217e02ae6e8dbd1f8b864a3d193a6f9cfd",
    "asn1crypto/keys.py": "58e88ef7f2a88253eba27d71dc55204666f41288698f8d2c8bb2ce4c223688b4",
    "asn1crypto/core.py": "d7cc8f6a0057180b6c982153baa45b58a9c8cb56a9c28880123fe2d99c1cf45d",
    "asn1crypto/parser.py": "801ecffeda781aa263810bf9cca9153a681d9a29b97097f1c8889df932080f52",
    "asn1crypto/util.py": "b213291c3bdc398c83712ae51681aa4cb5211e9b131da9b8e6ee0a7513b7cb0c",
    "asn1crypto/_types.py": "3b0b17df47a9bfe11323d786acb5bd18baafd0a7a889246725c8cdf76ae8aa14",
    "asn1crypto-1.5.1.dist-info/METADATA": (
        "29112bfcab83d0af2e494a5de4e95e71c51e1113255d7f82536160366550f194"
    ),
}

SECP256K1_P = 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F
SECP256K1_N = 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141
SECP256K1_G = (
    0x79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798,
    0x483ADA7726A3C4655DA4FBFC0E1108A8FD17B448A68554199C47D08FFB10D4B8,
)
BASE58_ALPHABET = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
EC_PUBLIC_KEY_OID = bytes.fromhex("06072a8648ce3d0201")
SECP256K1_OID = bytes.fromhex("06052b8104000a")
COINCURVE_REQUIREMENT = "coincurve==15.0.0"
MAX_ASN1_INPUT_BYTES = 1 << 20
MAX_ASN1_DEPTH = 64
MAX_ASN1_NODES = 4096
MAX_ASN1_TAG_OCTETS = 8
MAX_ASN1_LENGTH_OCTETS = 8
MAX_PEM_BODY_BYTES = ((MAX_ASN1_INPUT_BYTES + 2) // 3) * 4 + 4096


def sha256_file(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def verify_hashes(root, expected_hashes, label):
    for relative_path, expected in expected_hashes.items():
        path = root / relative_path
        if not path.is_file():
            raise RuntimeError(f"{label} source is missing {relative_path}")
        actual = sha256_file(path)
        if actual != expected:
            raise RuntimeError(
                f"{relative_path} does not match pinned {label}: "
                f"sha256 is {actual}, expected {expected}"
            )


def verify_asn1crypto_wheel(wheel_path):
    actual_wheel_hash = hashlib.sha256(wheel_path.read_bytes()).hexdigest()
    if actual_wheel_hash != PINNED_ASN1CRYPTO_WHEEL_SHA256:
        raise RuntimeError(
            f"asn1crypto wheel sha256 is {actual_wheel_hash}, "
            f"expected {PINNED_ASN1CRYPTO_WHEEL_SHA256}"
        )
    with zipfile.ZipFile(wheel_path) as archive:
        for member, expected in PINNED_ASN1CRYPTO_SOURCE_HASHES.items():
            try:
                source = archive.read(member)
            except KeyError as error:
                raise RuntimeError(f"asn1crypto wheel is missing {member}") from error
            actual = hashlib.sha256(source).hexdigest()
            if actual != expected:
                raise RuntimeError(
                    f"{member} does not match audited asn1crypto "
                    f"{PINNED_ASN1CRYPTO_VERSION}: sha256 is {actual}, expected {expected}"
                )


def read_sdk_version(sdk_root):
    path = sdk_root / "lbry" / "__init__.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    for node in tree.body:
        if isinstance(node, ast.Assign) and any(
            isinstance(target, ast.Name) and target.id == "__version__"
            for target in node.targets
        ):
            return ast.literal_eval(node.value)
    raise RuntimeError("could not read SDK version")


def read_setup_requirements(sdk_root):
    path = sdk_root / "setup.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue
        if not isinstance(node.func, ast.Name) or node.func.id != "setup":
            continue
        for keyword in node.keywords:
            if keyword.arg == "install_requires":
                return ast.literal_eval(keyword.value)
    raise RuntimeError("could not read SDK install_requires")


def verify_pinned_sources(sdk_root, coincurve_root=None, asn1crypto_wheel=None):
    commit = subprocess.run(
        ["git", "-C", str(sdk_root), "rev-parse", "HEAD"],
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    verify_hashes(sdk_root, PINNED_SOURCE_HASHES, f"SDK commit {PINNED_COMMIT}")
    version = read_sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    requirements = read_setup_requirements(sdk_root)
    if COINCURVE_REQUIREMENT not in requirements:
        raise RuntimeError(f"pinned setup.py does not require {COINCURVE_REQUIREMENT}")
    if coincurve_root is not None:
        verify_hashes(
            coincurve_root,
            PINNED_COINCURVE_SOURCE_HASHES,
            f"Coincurve {PINNED_COINCURVE_VERSION}",
        )
    if asn1crypto_wheel is not None:
        verify_asn1crypto_wheel(asn1crypto_wheel)
    return commit, version


def point_add(left, right):
    if left is None:
        return right
    if right is None:
        return left
    x1, y1 = left
    x2, y2 = right
    if x1 == x2 and (y1 + y2) % SECP256K1_P == 0:
        return None
    if left == right:
        slope = (3 * x1 * x1) * pow(2 * y1, -1, SECP256K1_P)
    else:
        slope = (y2 - y1) * pow(x2 - x1, -1, SECP256K1_P)
    slope %= SECP256K1_P
    x3 = (slope * slope - x1 - x2) % SECP256K1_P
    return x3, (slope * (x1 - x3) - y1) % SECP256K1_P


def point_multiply(scalar, point=SECP256K1_G):
    if point is None or scalar % SECP256K1_N == 0:
        return None
    scalar %= SECP256K1_N
    result = None
    addend = point
    while scalar:
        if scalar & 1:
            result = point_add(result, addend)
        addend = point_add(addend, addend)
        scalar >>= 1
    return result


def serialize_point(point, compressed=True):
    if point is None:
        raise ValueError("invalid public key point")
    x, y = point
    if compressed:
        return bytes((2 | (y & 1),)) + x.to_bytes(32, "big")
    return b"\x04" + x.to_bytes(32, "big") + y.to_bytes(32, "big")


def parse_compressed_point(public_key):
    if not isinstance(public_key, (bytes, bytearray)):
        raise TypeError("pubkey must be raw bytes")
    if len(public_key) != 33:
        raise ValueError("pubkey must be 33 bytes")
    if public_key[0] not in (2, 3):
        raise ValueError("invalid pubkey prefix byte")
    x = int.from_bytes(public_key[1:], "big")
    if x >= SECP256K1_P:
        raise ValueError("The public key could not be parsed or is invalid.")
    y_squared = (pow(x, 3, SECP256K1_P) + 7) % SECP256K1_P
    y = pow(y_squared, (SECP256K1_P + 1) // 4, SECP256K1_P)
    if y * y % SECP256K1_P != y_squared:
        raise ValueError("The public key could not be parsed or is invalid.")
    if y & 1 != public_key[0] & 1:
        y = SECP256K1_P - y
    return x, y


def validate_secret(secret):
    if not 0 < secret < SECP256K1_N:
        raise ValueError(
            "Secret scalar must be greater than 0 and less than "
            f"{SECP256K1_N}."
        )
    return secret


def private_key_from_hex(value):
    raw = bytes.fromhex(value)
    if len(raw) != 32:
        raise ValueError("private key must be 32 bytes")
    return validate_secret(int.from_bytes(raw, "big"))


def public_key_bytes(secret):
    return serialize_point(point_multiply(validate_secret(secret)))


def rfc6979_nonce(secret, digest, counter=0):
    key_data = secret.to_bytes(32, "big") + digest
    value = b"\x01" * 32
    key = b"\x00" * 32
    key = hmac.new(key, value + b"\x00" + key_data, hashlib.sha256).digest()
    value = hmac.new(key, value, hashlib.sha256).digest()
    key = hmac.new(key, value + b"\x01" + key_data, hashlib.sha256).digest()
    value = hmac.new(key, value, hashlib.sha256).digest()
    for attempt in range(counter + 1):
        if attempt:
            key = hmac.new(key, value + b"\x00", hashlib.sha256).digest()
            value = hmac.new(key, value, hashlib.sha256).digest()
        value = hmac.new(key, value, hashlib.sha256).digest()
    return int.from_bytes(value, "big")


def sign_compact(secret, digest):
    validate_secret(secret)
    if len(digest) < 32:
        raise ValueError("Digest must be 32 bytes long.")
    # The pinned CFFI call passes a pointer with no Python-side length. The C
    # API reads exactly 32 bytes, so a longer buffer is silently truncated.
    digest = digest[:32]
    message = int.from_bytes(digest, "big") % SECP256K1_N
    counter = 0
    while True:
        nonce = rfc6979_nonce(secret, digest, counter)
        counter += 1
        if not 0 < nonce < SECP256K1_N:
            continue
        point = point_multiply(nonce)
        r = point[0] % SECP256K1_N
        if r == 0:
            continue
        s = (pow(nonce, -1, SECP256K1_N) * (message + r * secret)) % SECP256K1_N
        if s == 0:
            continue
        if s > SECP256K1_N // 2:
            s = SECP256K1_N - s
        return r.to_bytes(32, "big") + s.to_bytes(32, "big")


def verify_compact(public_key, signature, digest):
    point = parse_compressed_point(public_key)
    if len(signature) != 64:
        raise ValueError("Signature must be 64 bytes long.")
    if len(digest) != 32:
        raise ValueError("Digest must be 32 bytes long.")
    r = int.from_bytes(signature[:32], "big")
    s = int.from_bytes(signature[32:], "big")
    if r >= SECP256K1_N or s >= SECP256K1_N:
        raise AssertionError()
    if s > SECP256K1_N // 2:
        s = SECP256K1_N - s
    if r == 0 or s == 0:
        return False
    message = int.from_bytes(digest, "big") % SECP256K1_N
    inverse = pow(s, -1, SECP256K1_N)
    candidate = point_add(
        point_multiply(message * inverse),
        point_multiply(r * inverse, point),
    )
    return candidate is not None and candidate[0] % SECP256K1_N == r


def der_length(length):
    if length < 0:
        raise ValueError("negative DER length")
    if length < 128:
        return bytes((length,))
    encoded = length.to_bytes((length.bit_length() + 7) // 8, "big")
    return bytes((0x80 | len(encoded),)) + encoded


def der_tlv(tag, content):
    return bytes((tag,)) + der_length(len(content)) + content


def private_key_to_der(secret):
    secret = validate_secret(secret)
    uncompressed = serialize_point(point_multiply(secret), compressed=False)
    ec_private_key = der_tlv(
        0x30,
        der_tlv(0x02, b"\x01")
        + der_tlv(0x04, secret.to_bytes(32, "big"))
        + der_tlv(0xA1, der_tlv(0x03, b"\x00" + uncompressed)),
    )
    algorithm = der_tlv(0x30, EC_PUBLIC_KEY_OID + SECP256K1_OID)
    return der_tlv(
        0x30,
        der_tlv(0x02, b"\x00") + algorithm + der_tlv(0x04, ec_private_key),
    )


def der_to_pem(der):
    encoded = base64.b64encode(der)
    lines = [encoded[index:index + 64] for index in range(0, len(encoded), 64)]
    return (
        b"-----BEGIN PRIVATE KEY-----\n"
        + b"\n".join(lines)
        + b"\n-----END PRIVATE KEY-----\n"
    ).decode("ascii")


def private_key_to_pem(secret):
    return der_to_pem(private_key_to_der(secret))


def pem_to_der(pem):
    # PrivateKey.from_pem() calls pem.encode() before Coincurve's permissive
    # helper, so non-text certificate values retain the pinned AttributeError.
    lines = pem.encode().strip().splitlines()
    body_lines = lines[1:-1]
    body_size = sum(map(len, body_lines))
    if body_size > MAX_PEM_BODY_BYTES:
        raise ValueError("PEM body exceeds oracle input limit")
    decoded = base64.b64decode(b"".join(body_lines))
    if len(decoded) > MAX_ASN1_INPUT_BYTES:
        raise ValueError("decoded PEM exceeds oracle input limit")
    return decoded


class ASN1Node:
    __slots__ = (
        "tag_class", "constructed", "indefinite", "tag", "content", "children"
    )

    def __init__(
            self, tag_class, constructed, tag, content=None, children=None,
            indefinite=False):
        self.tag_class = tag_class
        self.constructed = constructed
        self.indefinite = indefinite
        self.tag = tag
        self.content = content
        self.children = children


class ASN1Budget:
    __slots__ = "nodes"

    def __init__(self):
        self.nodes = 0


def read_asn1_identifier(data, offset):
    if offset >= len(data):
        raise ValueError("Insufficient data")
    first = data[offset]
    offset += 1
    tag_class = first >> 6
    constructed = bool(first & 0x20)
    tag = first & 0x1F
    if tag == 0x1F:
        tag = 0
        saw_octet = False
        tag_octets = 0
        while True:
            if offset >= len(data):
                raise ValueError("Insufficient data")
            octet = data[offset]
            offset += 1
            tag_octets += 1
            if tag_octets > MAX_ASN1_TAG_OCTETS:
                raise ValueError("ASN.1 tag exceeds oracle limit")
            if not saw_octet and octet == 0x80:
                raise ValueError("Invalid high-tag-number encoding")
            saw_octet = True
            tag = (tag << 7) | (octet & 0x7F)
            if not octet & 0x80:
                break
        if tag < 0x1F:
            raise ValueError("Non-minimal tag encoding")
    return tag_class, constructed, tag, offset


def read_asn1_node(data, offset=0, depth=0, budget=None):
    if depth > MAX_ASN1_DEPTH:
        raise ValueError("ASN.1 nesting exceeds oracle limit")
    if budget is None:
        budget = ASN1Budget()
    budget.nodes += 1
    if budget.nodes > MAX_ASN1_NODES:
        raise ValueError("ASN.1 node count exceeds oracle limit")
    tag_class, constructed, tag, offset = read_asn1_identifier(data, offset)
    if tag_class == 0 and tag == 0:
        raise ValueError("Unexpected end-of-contents marker")
    if offset >= len(data):
        raise ValueError("Insufficient data")
    first_length = data[offset]
    offset += 1
    indefinite = first_length == 0x80
    if first_length & 0x80 and not indefinite:
        count = first_length & 0x7F
        if count == 0x7F or count > MAX_ASN1_LENGTH_OCTETS:
            raise ValueError("ASN.1 length exceeds oracle encoding limit")
        if offset + count > len(data):
            raise ValueError("Insufficient data")
        length = int.from_bytes(data[offset:offset + count], "big")
        offset += count
    elif indefinite:
        length = None
    else:
        length = first_length

    if indefinite:
        if not constructed:
            raise ValueError("Indefinite length is only valid for constructed values")
        children = []
        while True:
            if offset + 2 > len(data):
                raise ValueError("Insufficient data")
            if data[offset:offset + 2] == b"\x00\x00":
                return ASN1Node(
                    tag_class, True, tag, children=children, indefinite=True
                ), offset + 2
            child, offset = read_asn1_node(data, offset, depth + 1, budget)
            children.append(child)

    end = offset + length
    if end > len(data):
        raise ValueError("Insufficient data")
    if not constructed:
        return ASN1Node(tag_class, False, tag, content=data[offset:end]), end
    children = []
    while offset < end:
        child, offset = read_asn1_node(data, offset, depth + 1, budget)
        children.append(child)
    if offset != end:
        raise ValueError("Constructed value exceeded its declared length")
    return ASN1Node(tag_class, True, tag, children=children), end


def parse_asn1_root(data):
    if not isinstance(data, (bytes, bytearray)):
        raise TypeError("ASN.1 input must be raw bytes")
    if len(data) > MAX_ASN1_INPUT_BYTES:
        raise ValueError("ASN.1 input exceeds oracle limit")
    node, _end = read_asn1_node(bytes(data))
    # Asn1Value.load(strict=False), used by the pinned SDK, ignores trailing data.
    return node


def require_node(node, tag, label, tag_class=0, constructed=None):
    if node.tag_class != tag_class or node.tag != tag:
        raise ValueError(f"invalid {label}")
    if constructed is not None and node.constructed != constructed:
        raise ValueError(f"invalid {label} encoding method")
    return node


def sequence_children(node, label):
    require_node(node, 16, label, constructed=True)
    return node.children


def validate_integer(node, label):
    require_node(node, 2, label, constructed=False)
    if not node.content:
        raise ValueError(f"invalid empty {label}")


def validate_null(node, label):
    require_node(node, 5, label, constructed=False)
    if node.content:
        raise ValueError(f"invalid {label}")


def validate_oid(node, label):
    require_node(node, 6, label, constructed=False)
    if not node.content or node.content[-1] & 0x80:
        raise ValueError(f"invalid {label}")
    offset = 1
    while offset < len(node.content):
        while node.content[offset] & 0x80:
            offset += 1
            if offset >= len(node.content):
                raise ValueError(f"invalid {label}")
        offset += 1
    return node.content


def octet_string_bytes(node, label):
    require_node(node, 4, label)
    if not node.constructed:
        return node.content
    if not node.indefinite:
        raise ValueError(f"definite {label} must use primitive encoding")
    chunks = []
    total = 0
    for child in node.children:
        chunk = octet_string_bytes(child, label)
        total += len(chunk)
        if total > MAX_ASN1_INPUT_BYTES:
            raise ValueError(f"{label} exceeds oracle limit")
        chunks.append(chunk)
    return b"".join(chunks)


def bit_string_value(node, label):
    require_node(node, 3, label)
    if node.constructed:
        if not node.indefinite:
            raise ValueError(f"definite {label} must use primitive encoding")
        chunks = []
        total = 0
        final_unused_bits = 0
        for index, child in enumerate(node.children):
            chunk, unused_bits = bit_string_value(child, label)
            if index + 1 != len(node.children) and unused_bits:
                raise ValueError(f"non-final {label} segment has unused bits")
            final_unused_bits = unused_bits
            total += len(chunk)
            if total > MAX_ASN1_INPUT_BYTES:
                raise ValueError(f"{label} exceeds oracle limit")
            chunks.append(chunk)
        return b"".join(chunks), final_unused_bits
    if not node.content or node.content[0] > 7:
        raise ValueError(f"invalid {label}")
    unused_bits = node.content[0]
    value = node.content[1:]
    if unused_bits:
        if not value:
            raise ValueError(f"invalid {label}")
        value = value[:-1] + bytes((value[-1] & (0xFF << unused_bits),))
    return value, unused_bits


def bit_string_bytes(node, label):
    value, _unused_bits = bit_string_value(node, label)
    return value


def validate_unknown_node(node):
    """Force the useful subset of generic Asn1Value.native validation."""
    if node.tag_class != 0:
        if node.tag == 0:
            raise ValueError("unknown context-specific tag 0")
        if node.constructed:
            for child in node.children:
                validate_unknown_node(child)
        return
    if node.tag == 1:
        require_node(node, 1, "boolean", constructed=False)
        if len(node.content) != 1:
            raise ValueError("invalid boolean")
    elif node.tag == 2:
        validate_integer(node, "integer")
    elif node.tag == 3:
        bit_string_bytes(node, "bit string")
    elif node.tag == 4:
        octet_string_bytes(node, "octet string")
    elif node.tag == 5:
        validate_null(node, "null")
    elif node.tag == 6:
        validate_oid(node, "object identifier")
    elif node.tag in (12, 19, 22, 26, 27, 30):
        require_node(node, node.tag, "string", constructed=False)
        node.content.decode("utf-8" if node.tag == 12 else "ascii")
    elif node.constructed:
        for child in node.children:
            validate_unknown_node(child)


def validate_algorithm_identifier(node, label="algorithm identifier"):
    fields = sequence_children(node, label)
    if not fields:
        raise ValueError(f"invalid {label}")
    validate_oid(fields[0], f"{label} OID")
    for field in fields[1:]:
        validate_unknown_node(field)


def validate_field_id(node):
    fields = sequence_children(node, "EC field identifier")
    if len(fields) < 2:
        raise ValueError("invalid EC field identifier")
    field_oid = validate_oid(fields[0], "EC field type")
    if field_oid == bytes.fromhex("2a8648ce3d0101"):
        validate_integer(fields[1], "prime-field parameters")
    else:
        validate_unknown_node(fields[1])
    for field in fields[2:]:
        validate_unknown_node(field)


def validate_curve(node):
    fields = sequence_children(node, "EC curve")
    if len(fields) < 2:
        raise ValueError("invalid EC curve")
    octet_string_bytes(fields[0], "EC curve coefficient a")
    octet_string_bytes(fields[1], "EC curve coefficient b")
    if len(fields) >= 3:
        bit_string_bytes(fields[2], "EC curve seed")
    for field in fields[3:]:
        validate_unknown_node(field)


def validate_specified_ec_domain(node):
    fields = sequence_children(node, "specified EC domain")
    if len(fields) < 5:
        raise ValueError("invalid specified EC domain")
    validate_integer(fields[0], "specified EC domain version")
    validate_field_id(fields[1])
    validate_curve(fields[2])
    octet_string_bytes(fields[3], "EC base point")
    validate_integer(fields[4], "EC group order")
    position = 5
    if position < len(fields) and fields[position].tag_class == 0 and fields[position].tag == 2:
        validate_integer(fields[position], "EC cofactor")
        position += 1
    if position < len(fields) and fields[position].tag_class == 0 and fields[position].tag == 16:
        validate_algorithm_identifier(fields[position], "EC hash algorithm")
        position += 1
    for field in fields[position:]:
        validate_unknown_node(field)


def validate_ec_domain_parameters(node):
    if node.tag_class != 0:
        raise ValueError("invalid EC domain parameters")
    if node.tag == 16:
        validate_specified_ec_domain(node)
    elif node.tag == 6:
        validate_oid(node, "named EC curve")
    elif node.tag == 5:
        validate_null(node, "implicit EC parameters")
    else:
        raise ValueError("invalid EC domain parameters")


def explicit_child(node, tag, label):
    require_node(node, tag, label, tag_class=2, constructed=True)
    if len(node.children) != 1:
        raise ValueError(f"invalid {label}")
    return node.children[0]


def parse_ec_private_key_node(node):
    fields = sequence_children(node, "SEC1 private key")
    if len(fields) < 2:
        raise ValueError("invalid SEC1 private key")
    validate_integer(fields[0], "SEC1 version")
    private_key = octet_string_bytes(fields[1], "SEC1 private key scalar")

    position = 2
    if position < len(fields) and fields[position].tag_class == 2 and fields[position].tag == 0:
        parameters = explicit_child(fields[position], 0, "SEC1 parameters")
        validate_ec_domain_parameters(parameters)
        position += 1
    if position < len(fields) and fields[position].tag_class == 2 and fields[position].tag == 1:
        public_key = explicit_child(fields[position], 1, "SEC1 public key")
        bit_string_bytes(public_key, "SEC1 public key")
        position += 1
    for field in fields[position:]:
        validate_unknown_node(field)
    return int.from_bytes(private_key, "big")


def validate_attributes(node):
    require_node(node, 0, "PKCS#8 attributes", tag_class=2, constructed=True)
    for attribute in node.children:
        fields = sequence_children(attribute, "PKCS#8 attribute")
        if len(fields) < 2:
            raise ValueError("invalid PKCS#8 attribute")
        validate_oid(fields[0], "PKCS#8 attribute type")
        values = fields[1]
        require_node(values, 17, "PKCS#8 attribute values", constructed=True)
        for value in values.children:
            validate_unknown_node(value)
        for field in fields[2:]:
            validate_unknown_node(field)


def parse_pkcs8_private_key_node(node):
    fields = sequence_children(node, "PKCS#8 private key")
    if len(fields) < 3:
        raise ValueError("invalid PKCS#8 private key")
    validate_integer(fields[0], "PKCS#8 version")
    algorithm = sequence_children(fields[1], "PKCS#8 private key algorithm")
    if not algorithm:
        raise ValueError("invalid PKCS#8 private key algorithm")
    algorithm_oid = validate_oid(algorithm[0], "PKCS#8 private key algorithm OID")
    if algorithm_oid != EC_PUBLIC_KEY_OID[2:]:
        raise ValueError("PKCS#8 private key algorithm is not EC")
    if len(algorithm) >= 2:
        validate_ec_domain_parameters(algorithm[1])
    for field in algorithm[2:]:
        validate_unknown_node(field)

    private_key_der = octet_string_bytes(fields[2], "PKCS#8 private key value")
    private_key = parse_ec_private_key_node(parse_asn1_root(private_key_der))
    position = 3
    if position < len(fields) and fields[position].tag_class == 2 and fields[position].tag == 0:
        validate_attributes(fields[position])
        position += 1
    for field in fields[position:]:
        validate_unknown_node(field)
    return private_key


def private_key_from_der(der):
    node = parse_asn1_root(der)
    try:
        private_key = parse_ec_private_key_node(node)
    except ValueError:
        private_key = parse_pkcs8_private_key_node(node)
    # Leading zero octets are intentionally accepted, matching IntegerOctetString.native.
    return validate_secret(private_key)


def private_key_from_pem(pem):
    return private_key_from_der(pem_to_der(pem))


def run_adapter_self_checks():
    secret = int("2423f3dc6087d9683f73a684935abc0ccd8bc26370588f56653128c6a6f0bf7c", 16)
    digest = bytes.fromhex(
        "9fc0b0a4a1e7a2aa2b0cd0a5566f4847ed9f66f92c7f0fc3cc4e3cea6f29a0ff"
    )
    expected_signature = bytes.fromhex(
        "100f7542643e64d9efa3c78c60210de67585889e5efa715eb2b30ae5d047d809"
        "1a9f9d0e13182b030eefcb567f3a6c5597259bd21ac0275a2b394c28a6c5e61e"
    )
    if sign_compact(secret, digest) != expected_signature:
        raise RuntimeError("compact signing self-check failed")
    if sign_compact(secret, digest + b"ignored trailing bytes") != expected_signature:
        raise RuntimeError("long compact-signing buffer self-check failed")
    try:
        sign_compact(secret, digest[:-1])
    except ValueError:
        pass
    else:
        raise RuntimeError("short compact-signing buffer self-check failed")

    encoded = private_key_to_der(1)
    if len(encoded) != 135 or hashlib.sha256(encoded).hexdigest() != (
        "1f36610a06478ad6206acb33c0b902c683c45c2a03bd6c77be53ef5c1a252d3b"
    ):
        raise RuntimeError("Coincurve PKCS#8 encoding self-check failed")
    if private_key_from_der(encoded) != 1:
        raise RuntimeError("PKCS#8 decoding self-check failed")

    integer_one = der_tlv(0x02, b"\x01")
    leading_zero_scalar = der_tlv(0x04, bytes(32) + b"\x01")
    sec1 = der_tlv(0x30, integer_one + leading_zero_scalar)
    if private_key_from_der(sec1) != 1:
        raise RuntimeError("leading-zero SEC1 scalar self-check failed")

    # Exercise nested indefinite constructed SEQUENCE, OCTET STRING, explicit
    # optional fields, PKCS#8 attributes, and an OCTET STRING split mid-value.
    scalar_fragments = (
        b"\x24\x80"
        + der_tlv(0x04, bytes(16))
        + der_tlv(0x04, bytes(16) + b"\x01")
        + b"\x00\x00"
    )
    parameters = b"\xa0\x80" + SECP256K1_OID + b"\x00\x00"
    public_key = (
        b"\xa1\x80"
        + der_tlv(0x03, b"\x00" + serialize_point(SECP256K1_G, compressed=False))
        + b"\x00\x00"
    )
    inner_ber = (
        b"\x30\x80" + integer_one + scalar_fragments + parameters + public_key + b"\x00\x00"
    )
    algorithm_ber = (
        b"\x30\x80" + EC_PUBLIC_KEY_OID + SECP256K1_OID + b"\x00\x00"
    )
    private_key_fragments = (
        b"\x24\x80"
        + der_tlv(0x04, inner_ber[:7])
        + der_tlv(0x04, inner_ber[7:])
        + b"\x00\x00"
    )
    attribute = der_tlv(
        0x30,
        der_tlv(0x06, bytes.fromhex("2a0304"))
        + b"\x31\x80"
        + der_tlv(0x0C, b"ok")
        + b"\x00\x00",
    )
    attributes_ber = b"\xa0\x80" + attribute + b"\x00\x00"
    pkcs8_ber = (
        b"\x30\x80"
        + der_tlv(0x02, b"\x00")
        + algorithm_ber
        + private_key_fragments
        + attributes_ber
        + b"\x00\x00"
    )
    if private_key_from_der(pkcs8_ber) != 1:
        raise RuntimeError("BER and PKCS#8 attributes self-check failed")

    definite_constructed_scalar = bytes.fromhex("30080201012403040101")
    try:
        private_key_from_der(definite_constructed_scalar)
    except ValueError:
        pass
    else:
        raise RuntimeError("definite constructed OCTET STRING self-check failed")

    final_unused_bits = bytes.fromhex(
        "3010020101040101a1082380030201020000"
    )
    if private_key_from_der(final_unused_bits) != 1:
        raise RuntimeError("constructed BIT STRING unused-bits self-check failed")

    non_minimal_high_tag = bytes.fromhex("3f1006020101040101")
    try:
        private_key_from_der(non_minimal_high_tag)
    except ValueError:
        pass
    else:
        raise RuntimeError("non-minimal high-tag self-check failed")

    invalid_public_key = der_tlv(
        0x30, integer_one + der_tlv(0x04, b"\x01") + der_tlv(0xA1, der_tlv(0x04, b"x"))
    )
    try:
        private_key_from_der(invalid_public_key)
    except ValueError:
        pass
    else:
        raise RuntimeError("SEC1 optional-field validation self-check failed")

    misplaced_padding_pem = (
        "-----BEGIN PRIVATE KEY-----\nTWFu=QUJD\n-----END PRIVATE KEY-----\n"
    )
    if pem_to_der(misplaced_padding_pem) != b"ManABC":
        raise RuntimeError("non-strict base64 self-check failed")

    too_deep = der_tlv(0x05, b"")
    for _index in range(MAX_ASN1_DEPTH + 2):
        too_deep = b"\x30\x80" + too_deep + b"\x00\x00"
    try:
        parse_asn1_root(too_deep)
    except ValueError:
        pass
    else:
        raise RuntimeError("ASN.1 depth-limit self-check failed")

    too_many_nodes = (
        b"\x30\x80" + der_tlv(0x05, b"") * MAX_ASN1_NODES + b"\x00\x00"
    )
    try:
        parse_asn1_root(too_many_nodes)
    except ValueError:
        pass
    else:
        raise RuntimeError("ASN.1 node-limit self-check failed")

    try:
        parse_asn1_root(bytes(MAX_ASN1_INPUT_BYTES + 1))
    except ValueError:
        pass
    else:
        raise RuntimeError("ASN.1 input-limit self-check failed")
    return {
        "asn1_optional_fields": True,
        "asn1_encoding_edges": True,
        "ber_indefinite_constructed": True,
        "compact_long_buffer_truncation": True,
        "leading_zero_scalar": True,
        "non_strict_base64": True,
        "pkcs8_attributes": True,
        "resource_limits": True,
    }


def hash160(value):
    return hashlib.new("ripemd160", hashlib.sha256(value).digest()).digest()


def double_sha256(value):
    return hashlib.sha256(hashlib.sha256(value).digest()).digest()


def base58_encode(value):
    zero_count = len(value) - len(value.lstrip(b"\x00"))
    number = int.from_bytes(value, "big")
    encoded = ""
    while number:
        number, remainder = divmod(number, 58)
        encoded = BASE58_ALPHABET[remainder] + encoded
    return "1" * zero_count + encoded


def public_key_to_address(public_key, prefix=b"\x55"):
    payload = prefix + hash160(public_key)
    return base58_encode(payload + double_sha256(payload)[:4])


class PrivateKey:
    def __init__(self, secret, chain_code=None, prefix=b"\x55", n=0, depth=0):
        self.secret = validate_secret(secret)
        self.chain_code = bytes(32) if chain_code is None else bytes(chain_code)
        if len(self.chain_code) != 32:
            raise ValueError("invalid chain code")
        if not 0 <= n < 1 << 32:
            raise ValueError("invalid child number")
        if not 0 <= depth < 256:
            raise ValueError("invalid depth")
        self.prefix = prefix
        self.n = n
        self.depth = depth

    @classmethod
    def from_seed(cls, seed, prefix=b"\x55"):
        digest = hmac.new(b"Bitcoin seed", seed, hashlib.sha512).digest()
        return cls(int.from_bytes(digest[:32], "big"), digest[32:], prefix)

    @property
    def private_key_bytes(self):
        return self.secret.to_bytes(32, "big")

    @property
    def public_key_bytes(self):
        return public_key_bytes(self.secret)

    @property
    def address(self):
        return public_key_to_address(self.public_key_bytes, self.prefix)

    def child(self, index):
        if not 0 <= index < 1 << 32:
            raise ValueError("invalid BIP32 private key child number")
        if index >= 1 << 31:
            serial_key = b"\x00" + self.private_key_bytes
        else:
            serial_key = self.public_key_bytes
        digest = hmac.new(
            self.chain_code,
            serial_key + index.to_bytes(4, "big"),
            hashlib.sha512,
        ).digest()
        tweak = int.from_bytes(digest[:32], "big")
        child_secret = (self.secret + tweak) % SECP256K1_N
        if tweak >= SECP256K1_N or child_secret == 0:
            raise ValueError(
                "The tweak was out of range, or the resulting private key is invalid."
            )
        return PrivateKey(child_secret, digest[32:], self.prefix, index, self.depth + 1)

    def to_pem(self):
        return private_key_to_pem(self.secret)


def key_view(private_key, include_pem=False):
    if private_key is None:
        return None
    result = {
        "address": private_key.address,
        "private_key_hex": private_key.private_key_bytes.hex(),
        "public_key_hex": private_key.public_key_bytes.hex(),
        "chain_code_hex": private_key.chain_code.hex(),
        "n": private_key.n,
        "depth": private_key.depth,
    }
    if include_pem:
        result["pem"] = private_key.to_pem()
        result["der_hex"] = private_key_to_der(private_key.secret).hex()
    return result


class UsageRecorder:
    def __init__(self, values=None):
        self.values = list(values or [])
        self.calls = []

    def reset(self, values):
        self.values = list(values)

    def is_used(self, key):
        self.calls.append({
            "index": key.n,
            "address": key.address,
            "public_key_hex": key.public_key_bytes.hex(),
        })
        if not self.values:
            raise RuntimeError("usage sequence exhausted")
        value = self.values.pop(0)
        if isinstance(value, bool):
            return value
        if value is None or value == "error":
            raise RuntimeError("injected channel-key usage error")
        if isinstance(value, dict) and "error" in value:
            raise RuntimeError(str(value["error"]))
        raise TypeError("usage entries must be booleans, null, 'error', or error objects")


class SaveRecorder:
    def __init__(self, values=None):
        self.values = list(values or [])
        self.calls = []

    def save(self, certificates):
        self.calls.append([[key, value] for key, value in certificates.items()])
        if not self.values:
            return None
        value = self.values.pop(0)
        if value is None or value is False:
            return None
        if isinstance(value, dict) and "error" in value:
            raise RuntimeError(str(value["error"]))
        raise RuntimeError(str(value) if value != "error" else "injected wallet save error")


class DeterministicChannelKeyManager:
    def __init__(self, root_private_key, usage):
        self.root_private_key = root_private_key
        self.usage = usage
        self.last_known = 0
        self.cache = {}
        self._private_key = None

    @property
    def private_key(self):
        if self._private_key is None and self.root_private_key is not None:
            self._private_key = self.root_private_key.child(2)
        return self._private_key

    def maybe_generate(self, public_key):
        if self.private_key is None:
            return None
        next_private_key = self.private_key.child(self.last_known)
        if bytes(public_key) == next_private_key.public_key_bytes:
            self.cache[next_private_key.address] = next_private_key
            self.last_known += 1
        return None

    def ensure_cache_primed(self):
        if self.private_key is not None:
            self.generate_next_key()
        return None

    def generate_next_key(self):
        while True:
            next_private_key = self.private_key.child(self.last_known)
            self.cache[next_private_key.address] = next_private_key
            if not self.usage.is_used(next_private_key):
                return next_private_key
            self.last_known += 1

    def get(self, address):
        return self.cache.get(address)


class AccountChannelKeys:
    def __init__(self, certificates, root_private_key, prefix, usage, saves):
        self.channel_keys = certificates
        self.prefix = prefix
        self.manager = DeterministicChannelKeyManager(root_private_key, usage)
        self.saves = saves

    def add(self, private_key):
        self.channel_keys[private_key.address] = private_key.to_pem()

    def get(self, public_key):
        address = public_key_to_address(public_key, self.prefix)
        private_key_pem = self.channel_keys.get(address)
        if private_key_pem:
            secret = private_key_from_pem(private_key_pem)
            return PrivateKey(secret, prefix=self.prefix)
        return self.manager.get(address)

    def migrate(self):
        channel_keys = {}
        for private_key_pem in self.channel_keys.values():
            if not isinstance(private_key_pem, str):
                continue
            if not private_key_pem.startswith("-----BEGIN"):
                continue
            secret = private_key_from_pem(private_key_pem)
            private_key = PrivateKey(secret, prefix=self.prefix)
            channel_keys[private_key.address] = private_key_pem
        if self.channel_keys != channel_keys:
            self.channel_keys = channel_keys
            self.saves.save(self.channel_keys)
        return None


def capture(function):
    try:
        return function(), None, None
    except Exception as error:  # pylint: disable=broad-except
        return None, type(error).__name__, str(error)


def outcome(case, function):
    result, error_type, error = capture(function)
    return {
        "name": case.get("name", ""),
        "result": result,
        "error_type": error_type,
        "error": error,
    }


def parse_prefix(case):
    prefix = bytes.fromhex(case.get("address_prefix_hex", "55"))
    if not prefix:
        raise ValueError("address prefix must not be empty")
    return prefix


def parse_certificates(value):
    if value is None:
        return {}
    if isinstance(value, dict):
        return dict(value)
    if not isinstance(value, list):
        raise TypeError("certificates must be an object or ordered pair array")
    result = {}
    for entry in value:
        if not isinstance(entry, list) or len(entry) != 2:
            raise ValueError("certificate entries must be [key, value] pairs")
        result[entry[0]] = entry[1]
    return result


def load_root_key(case, prefix):
    if "seed_hex" in case:
        return PrivateKey.from_seed(bytes.fromhex(case["seed_hex"]), prefix)
    if "root_private_key_hex" not in case and "root_chain_code_hex" not in case:
        return None
    if "root_private_key_hex" not in case or "root_chain_code_hex" not in case:
        raise ValueError("root private key and chain code must be supplied together")
    secret = private_key_from_hex(case["root_private_key_hex"])
    chain_code = bytes.fromhex(case["root_chain_code_hex"])
    return PrivateKey(secret, chain_code, prefix)


def state_view(account):
    manager = account.manager
    return {
        "certificates": [[key, value] for key, value in account.channel_keys.items()],
        "cache": [
            [address, key_view(private_key)]
            for address, private_key in manager.cache.items()
        ],
        "last_known": manager.last_known,
        "manager_private_key_loaded": manager._private_key is not None,
        "manager_private_key": key_view(manager._private_key),
        "usage_calls": list(manager.usage.calls),
        "usage_remaining": list(manager.usage.values),
        "save_calls": list(account.saves.calls),
        "save_remaining": list(account.saves.values),
    }


def execute_action(account, prefix, action):
    name = action["action"]
    if name in ("add", "add_channel_private_key"):
        key = PrivateKey(private_key_from_hex(action["private_key_hex"]), prefix=prefix)
        account.add(key)
        return None
    if name in ("get", "get_channel_private_key"):
        return key_view(account.get(bytes.fromhex(action["public_key_hex"])), include_pem=True)
    if name in ("migrate", "maybe_migrate_certificates"):
        return account.migrate()
    if name in ("generate", "generate_next_key", "generate_channel_private_key"):
        return key_view(account.manager.generate_next_key(), include_pem=True)
    if name in ("prime", "ensure_cache_primed"):
        return account.manager.ensure_cache_primed()
    if name in ("maybe", "maybe_generate_deterministic_key_for_channel"):
        return account.manager.maybe_generate(bytes.fromhex(action["public_key_hex"]))
    if name in ("get_cached", "get_private_key_from_pubkey_hash"):
        return key_view(account.manager.get(action["address"]), include_pem=True)
    if name == "set_usage":
        account.manager.usage.reset(action.get("usage", []))
        return None
    if name == "set_certificates":
        account.channel_keys = parse_certificates(action.get("certificates"))
        return None
    raise ValueError(f"unknown channel-key action: {name}")


def run_stateful_case(case):
    account, error_type, error = capture(lambda: make_account(case))
    result = {
        "name": case.get("name", ""),
        "error_type": error_type,
        "error": error,
        "initial": state_view(account) if account is not None else None,
        "actions": [],
    }
    if account is None:
        return result
    prefix = account.prefix
    for action in case.get("actions", []):
        value, error_type, error = capture(
            lambda action=action: execute_action(account, prefix, action)
        )
        operation = {
            "action": action.get("action"),
            "result": value,
            "error_type": error_type,
            "error": error,
        }
        operation.update(state_view(account))
        result["actions"].append(operation)
    return result


def make_account(case):
    prefix = parse_prefix(case)
    root = load_root_key(case, prefix)
    usage = UsageRecorder(case.get("usage", []))
    saves = SaveRecorder(case.get("save_errors", []))
    return AccountChannelKeys(
        parse_certificates(case.get("certificates")), root, prefix, usage, saves
    )


def run_sign_case(case):
    def execute():
        secret = private_key_from_hex(case["private_key_hex"])
        return {
            "signature_hex": sign_compact(secret, bytes.fromhex(case["digest_hex"])).hex(),
            "public_key_hex": public_key_bytes(secret).hex(),
        }
    return outcome(case, execute)


def run_verify_case(case):
    return outcome(
        case,
        lambda: verify_compact(
            bytes.fromhex(case["public_key_hex"]),
            bytes.fromhex(case["signature_hex"]),
            bytes.fromhex(case["digest_hex"]),
        ),
    )


def run_pem_case(case):
    def execute():
        operation = case.get("operation", "encode")
        if operation == "encode":
            key = PrivateKey(private_key_from_hex(case["private_key_hex"]))
        elif operation == "decode":
            key = PrivateKey(private_key_from_pem(case["pem"]))
        elif operation == "round_trip":
            key = PrivateKey(private_key_from_pem(case["pem"]))
        else:
            raise ValueError(f"unknown PEM operation: {operation}")
        return key_view(key, include_pem=True)
    return outcome(case, execute)


def run_generic_case(case):
    operation = case.get("operation")
    if operation == "sign":
        return run_sign_case(case)
    if operation == "verify":
        return run_verify_case(case)
    if operation in ("pem_encode", "pem_decode", "pem_round_trip"):
        adapted = dict(case)
        adapted["operation"] = operation[len("pem_"):]
        return run_pem_case(adapted)
    raise ValueError(f"unknown generic operation: {operation}")


def run(sdk_root, payload, coincurve_root=None, asn1crypto_wheel=None):
    if not __debug__:
        raise RuntimeError("channel-key oracle requires Python assertions (__debug__)")
    commit, version = verify_pinned_sources(
        sdk_root, coincurve_root, asn1crypto_wheel
    )
    self_checks = run_adapter_self_checks()
    if isinstance(payload, list):
        payload = {"cases": payload}
    if not isinstance(payload, dict):
        raise TypeError("oracle input must be an object or generic fixture array")
    return {
        "reference": {
            "commit": commit,
            "version": version,
            "source_sha256": PINNED_SOURCE_HASHES,
            "coincurve": {
                "version": PINNED_COINCURVE_VERSION,
                "requirement": COINCURVE_REQUIREMENT,
                "source_sha256": PINNED_COINCURVE_SOURCE_HASHES,
                "source_verified": coincurve_root is not None,
            },
            "asn1crypto": {
                "version": PINNED_ASN1CRYPTO_VERSION,
                "wheel_sha256": PINNED_ASN1CRYPTO_WHEEL_SHA256,
                "source_sha256": PINNED_ASN1CRYPTO_SOURCE_HASHES,
                "source_verified": asn1crypto_wheel is not None,
            },
        },
        "metadata": {
            "compact_signature_format": "32-byte big-endian r || 32-byte big-endian s",
            "compact_signature_bytes": 64,
            "digest_bytes": 32,
            "compact_signing_input": "first 32 bytes; shorter buffers rejected",
            "deterministic_channel_path": "m/2/index",
            "default_address_prefix_hex": "55",
            "pem_output": "PKCS#8 PRIVATE KEY",
            "pem_input": "SEC1 or PKCS#8 BER with non-strict base64",
            "oracle_limits": {
                "asn1_input_bytes": MAX_ASN1_INPUT_BYTES,
                "asn1_depth": MAX_ASN1_DEPTH,
                "asn1_nodes": MAX_ASN1_NODES,
                "asn1_tag_octets": MAX_ASN1_TAG_OCTETS,
                "asn1_length_octets": MAX_ASN1_LENGTH_OCTETS,
                "pem_body_bytes": MAX_PEM_BODY_BYTES,
            },
            "stdlib_only": True,
            "python_assertions": __debug__,
            "adapter_self_checks": self_checks,
        },
        "cases": [run_generic_case(case) for case in payload.get("cases", [])],
        "sign_cases": [run_sign_case(case) for case in payload.get("sign_cases", [])],
        "verify_cases": [run_verify_case(case) for case in payload.get("verify_cases", [])],
        "pem_cases": [run_pem_case(case) for case in payload.get("pem_cases", [])],
        "account_cases": [
            run_stateful_case(case) for case in payload.get("account_cases", [])
        ],
        "manager_cases": [
            run_stateful_case(case) for case in payload.get("manager_cases", [])
        ],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    parser.add_argument("--coincurve-root", type=Path)
    parser.add_argument("--asn1crypto-wheel", type=Path)
    arguments = parser.parse_args()
    coincurve_root = arguments.coincurve_root
    if coincurve_root is None and os.environ.get("COINCURVE15_SOURCE_PATH"):
        coincurve_root = Path(os.environ["COINCURVE15_SOURCE_PATH"])
    asn1crypto_wheel = arguments.asn1crypto_wheel
    if asn1crypto_wheel is None and os.environ.get("ASN1CRYPTO151_WHEEL_PATH"):
        asn1crypto_wheel = Path(os.environ["ASN1CRYPTO151_WHEEL_PATH"])
    result = run(
        arguments.sdk_root.resolve(),
        json.load(sys.stdin),
        coincurve_root.resolve() if coincurve_root is not None else None,
        asn1crypto_wheel.resolve() if asn1crypto_wheel is not None else None,
    )
    json.dump(result, sys.stdout, sort_keys=True, ensure_ascii=True, separators=(",", ":"))
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

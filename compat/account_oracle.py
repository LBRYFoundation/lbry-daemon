#!/usr/bin/env python3
"""Execute pinned Account serialization, merge, hash, and encryption in isolation."""

import argparse
import ast
import base64
import copy
import hashlib
from hashlib import sha256
import hmac
import json
import logging
from pathlib import Path
import random
import string
import subprocess
import sys
from typing import Any, Dict, List, Optional, Tuple, Type
import unicodedata


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/crypto/base58.py": "bed7bff1169a9d7b89473a08b6054170f52f50e3fb2126a9752c36e745e9b94f",
    "lbry/crypto/hash.py": "bfc430bd3fe98578b406caa3a8e2116a40f492c7b68e269176e838b4ef426a72",
    "lbry/crypto/crypt.py": "d2df4360c8dd306730290624fe49a805091171730526307f07165c8729129216",
    "lbry/wallet/account.py": "ea2ca30bddf9c0145469e989d9855dbe7be5184943ae7b8ca690eda41eb7db50",
    "lbry/wallet/bip32.py": "bbc027ae706338bd7a232290c110dcefc308b2b635179e01f51487cf8b05825a",
    "lbry/wallet/ledger.py": "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
    "lbry/wallet/mnemonic.py": "6d731208e9274f397ed15eb445ce0024f6ad9adcd8a1a40cd5ed08b7d41fc2bc",
    "lbry/wallet/words/english.py": "ec702cc5b02ea7bc749f742a70b56d55c26bf8ab6e0a0ce10429266051968dd3",
}

BASE58_ALPHABET = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
BASE58_INDEX = {character: index for index, character in enumerate(BASE58_ALPHABET)}
SECP256K1_P = 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F
SECP256K1_N = 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141
SECP256K1_G = (
    0x79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798,
    0x483ADA7726A3C4655DA4FBFC0E1108A8FD17B448A68554199C47D08FFB10D4B8,
)
CJK_INTERVALS = (
    (0x4E00, 0x9FFF), (0x3400, 0x4DBF), (0x20000, 0x2A6DF),
    (0x2A700, 0x2B73F), (0x2B740, 0x2B81F), (0xF900, 0xFAFF),
    (0x2F800, 0x2FA1D), (0x3190, 0x319F), (0x2E80, 0x2EFF),
    (0x2F00, 0x2FDF), (0x31C0, 0x31EF), (0x2FF0, 0x2FFF),
    (0xE0100, 0xE01EF), (0x3100, 0x312F), (0x31A0, 0x31BF),
    (0xFF00, 0xFFEF), (0x3040, 0x309F), (0x30A0, 0x30FF),
    (0x31F0, 0x31FF), (0x1B000, 0x1B0FF), (0xAC00, 0xD7AF),
    (0x1100, 0x11FF), (0xA960, 0xA97F), (0xD7B0, 0xD7FF),
    (0x3130, 0x318F), (0xA4D0, 0xA4FF), (0x16F00, 0x16F9F),
    (0xA000, 0xA48F), (0xA490, 0xA4CF),
)


class InvalidPasswordError(Exception):
    """Minimal stand-in for lbry.error.InvalidPasswordError."""


class Base58Error(Exception):
    """Stand-in for lbry.crypto.base58.Base58Error."""


class FixtureClock:
    def __init__(self):
        self.now = 0.0

    def time(self):
        return self.now


class FixtureOS:
    def __init__(self):
        self.values = []
        self.calls = []

    def reset(self, values=None):
        self.values = [bytes.fromhex(value) for value in (values or [])]
        self.calls = []

    def urandom(self, size):
        self.calls.append(size)
        value = self.values.pop(0) if self.values else bytes(size)
        if len(value) != size:
            raise ValueError(f"fixture random value has length {len(value)}, expected {size}")
        return value


class KeyPath:
    RECEIVE = 0
    CHANGE = 1
    CHANNEL = 2


def verify_pinned_sources(sdk_root):
    commit = subprocess.run(
        ["git", "-C", str(sdk_root), "rev-parse", "HEAD"],
        check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    for relative_path, expected in PINNED_SOURCE_HASHES.items():
        source_path = sdk_root / relative_path
        actual = hashlib.sha256(source_path.read_bytes()).hexdigest()
        if actual != expected:
            raise RuntimeError(
                f"{relative_path} does not match pinned commit {PINNED_COMMIT}: "
                f"sha256 is {actual}, expected {expected}"
            )
    return commit


def read_sdk_version(sdk_root):
    source_path = sdk_root / "lbry" / "__init__.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    for node in tree.body:
        if not isinstance(node, ast.Assign):
            continue
        if any(isinstance(target, ast.Name) and target.id == "__version__" for target in node.targets):
            return ast.literal_eval(node.value)
    raise RuntimeError("could not read SDK version")


def read_english_words(sdk_root):
    source_path = sdk_root / "lbry" / "wallet" / "words" / "english.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    assignment = next(
        node for node in tree.body
        if isinstance(node, ast.Assign)
        and any(isinstance(target, ast.Name) and target.id == "words" for target in node.targets)
    )
    words = ast.literal_eval(assignment.value)
    if len(words) != 2048 or len(set(words)) != len(words):
        raise RuntimeError("pinned English mnemonic word list changed")
    return words


def double_sha256(value):
    return hashlib.sha256(hashlib.sha256(value).digest()).digest()


def hash160(value):
    return hashlib.new("ripemd160", hashlib.sha256(value).digest()).digest()


def base58_encode(value):
    value = bytes(value)
    zero_count = len(value) - len(value.lstrip(b"\0"))
    number = int.from_bytes(value, "big")
    encoded = ""
    while number:
        number, remainder = divmod(number, 58)
        encoded = BASE58_ALPHABET[remainder] + encoded
    return "1" * zero_count + encoded


def base58_decode(value):
    if not value:
        raise Base58Error("string cannot be empty")
    number = 0
    for character in value:
        if character not in BASE58_INDEX:
            raise Base58Error(f'invalid base 58 character "{character}"')
        number = number * 58 + BASE58_INDEX[character]
    decoded = number.to_bytes((number.bit_length() + 7) // 8, "big") if number else b""
    return bytes(len(value) - len(value.lstrip("1"))) + decoded


def base58_encode_check(value):
    return base58_encode(value + double_sha256(value)[:4])


def base58_decode_check(value):
    decoded = base58_decode(value)
    if len(decoded) < 4 or decoded[-4:] != double_sha256(decoded[:-4])[:4]:
        raise Base58Error(f"invalid base 58 checksum for {value}")
    return decoded[:-4]


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
    if scalar % SECP256K1_N == 0 or point is None:
        return None
    result = None
    addend = point
    while scalar:
        if scalar & 1:
            result = point_add(result, addend)
        addend = point_add(addend, addend)
        scalar >>= 1
    return result


def serialize_point(point):
    if point is None:
        raise ValueError("invalid public key point")
    x, y = point
    return bytes((2 | (y & 1),)) + x.to_bytes(32, "big")


def parse_point(value):
    if len(value) != 33 or value[0] not in (2, 3):
        raise ValueError("invalid compressed public key")
    x = int.from_bytes(value[1:], "big")
    if x >= SECP256K1_P:
        raise ValueError("invalid compressed public key")
    y = pow((pow(x, 3, SECP256K1_P) + 7) % SECP256K1_P, (SECP256K1_P + 1) // 4, SECP256K1_P)
    if y & 1 != value[0] & 1:
        y = SECP256K1_P - y
    point = (x, y)
    if (y * y - x * x * x - 7) % SECP256K1_P:
        raise ValueError("invalid compressed public key")
    return point


class PublicKey:
    def __init__(self, ledger, point, chain_code, n=0, depth=0, parent=None):
        if not isinstance(chain_code, (bytes, bytearray)):
            raise TypeError("chain code must be raw bytes")
        if len(chain_code) != 32:
            raise ValueError("invalid chain code")
        if not 0 <= n < 1 << 32:
            raise ValueError("invalid child number")
        if not 0 <= depth < 256:
            raise ValueError("invalid depth")
        if parent is not None and not isinstance(parent, PublicKey):
            raise TypeError("parent key has bad type")
        self.ledger = ledger
        self.point = parse_point(point) if isinstance(point, (bytes, bytearray)) else point
        self.chain_code = bytes(chain_code)
        self.n = n
        self.depth = depth
        self.parent = parent

    @property
    def pubkey_bytes(self):
        return serialize_point(self.point)

    @property
    def address(self):
        return self.ledger.public_key_to_address(self.pubkey_bytes)

    def identifier(self):
        return hash160(self.pubkey_bytes)

    def fingerprint(self):
        return self.identifier()[:4]

    def parent_fingerprint(self):
        return self.parent.fingerprint() if self.parent else bytes(4)

    def child(self, n):
        if not 0 <= n < 1 << 31:
            raise ValueError("invalid BIP32 public key child number")
        digest = hmac.new(
            self.chain_code, self.pubkey_bytes + n.to_bytes(4, "big"), hashlib.sha512
        ).digest()
        tweak = int.from_bytes(digest[:32], "big")
        if not 0 <= tweak < SECP256K1_N:
            raise ValueError("The tweak was out of range, or the resulting public key is invalid")
        point = point_add(self.point, point_multiply(tweak))
        if point is None:
            raise ValueError("The tweak was out of range, or the resulting public key is invalid")
        return PublicKey(self.ledger, point, digest[32:], n, self.depth + 1, self)

    def extended_key_string(self):
        raw = (
            self.ledger.extended_public_key_prefix
            + bytes((self.depth,))
            + self.parent_fingerprint()
            + self.n.to_bytes(4, "big")
            + self.chain_code
            + self.pubkey_bytes
        )
        return base58_encode_check(raw)


class PrivateKey:
    HARDENED = 1 << 31

    def __init__(self, ledger, secret, chain_code, n=0, depth=0, parent=None):
        if not isinstance(chain_code, (bytes, bytearray)):
            raise TypeError("chain code must be raw bytes")
        if len(chain_code) != 32:
            raise ValueError("invalid chain code")
        if not 0 <= n < 1 << 32:
            raise ValueError("invalid child number")
        if not 0 <= depth < 256:
            raise ValueError("invalid depth")
        if parent is not None and not isinstance(parent, PrivateKey):
            raise TypeError("parent key has bad type")
        secret = int.from_bytes(secret, "big") if isinstance(secret, (bytes, bytearray)) else secret
        if not 0 < secret < SECP256K1_N:
            raise ValueError("The private key was invalid")
        self.ledger = ledger
        self.secret = secret
        self.chain_code = bytes(chain_code)
        self.n = n
        self.depth = depth
        self.parent = parent

    @classmethod
    def from_seed(cls, ledger, seed):
        digest = hmac.new(b"Bitcoin seed", seed, hashlib.sha512).digest()
        return cls(ledger, digest[:32], digest[32:])

    @property
    def private_key_bytes(self):
        return self.secret.to_bytes(32, "big")

    @property
    def public_key(self):
        parent = self.parent.public_key if self.parent else None
        return PublicKey(
            self.ledger, point_multiply(self.secret), self.chain_code,
            self.n, self.depth, parent,
        )

    @property
    def address(self):
        return self.public_key.address

    def identifier(self):
        return self.public_key.identifier()

    def fingerprint(self):
        return self.identifier()[:4]

    def parent_fingerprint(self):
        return self.parent.fingerprint() if self.parent else bytes(4)

    def child(self, n):
        if not 0 <= n < 1 << 32:
            raise ValueError("invalid BIP32 private key child number")
        serial_key = (
            b"\0" + self.private_key_bytes
            if n >= self.HARDENED else self.public_key.pubkey_bytes
        )
        digest = hmac.new(
            self.chain_code, serial_key + n.to_bytes(4, "big"), hashlib.sha512
        ).digest()
        tweak = int.from_bytes(digest[:32], "big")
        secret = (self.secret + tweak) % SECP256K1_N
        if not 0 <= tweak < SECP256K1_N or secret == 0:
            raise ValueError("The tweak was out of range, or the resulting private key is invalid")
        return PrivateKey(self.ledger, secret, digest[32:], n, self.depth + 1, self)

    def extended_key_string(self):
        raw = (
            self.ledger.extended_private_key_prefix
            + bytes((self.depth,))
            + self.parent_fingerprint()
            + self.n.to_bytes(4, "big")
            + self.chain_code
            + b"\0" + self.private_key_bytes
        )
        return base58_encode_check(raw)


def from_extended_key_string(ledger, value):
    raw = base58_decode_check(value)
    if len(raw) != 78:
        raise ValueError("extended key must have length 78")
    depth = raw[4]
    n = int.from_bytes(raw[9:13], "big")
    chain_code = raw[13:45]
    if raw[:4] == ledger.extended_public_key_prefix:
        return PublicKey(ledger, raw[45:], chain_code, n, depth)
    if raw[:4] == ledger.extended_private_key_prefix:
        if raw[45] != 0:
            raise ValueError("invalid extended private key prefix byte")
        return PrivateKey(ledger, raw[46:], chain_code, n, depth)
    raise ValueError("version bytes unrecognised")


def is_cjk(character):
    codepoint = ord(character)
    return any(start <= codepoint <= end for start, end in CJK_INTERVALS)


def normalize_text(value):
    value = unicodedata.normalize("NFKD", value).lower()
    value = "".join(character for character in value if not unicodedata.combining(character))
    value = " ".join(value.split())
    return "".join(
        value[index] for index in range(len(value))
        if not (
            value[index] in string.whitespace
            and is_cjk(value[index - 1])
            and is_cjk(value[index + 1])
        )
    )


class Mnemonic:
    words = None

    def __init__(self, lang="en"):
        if lang not in ("en",) and lang in ("es", "ja", "pt", "zh"):
            raise ModuleNotFoundError(f"No module named 'lbry.wallet.client.words'")
        self.words = type(self).words

    @staticmethod
    def mnemonic_to_seed(mnemonic, passphrase=""):
        return hashlib.pbkdf2_hmac(
            "sha512", normalize_text(mnemonic).encode(), normalize_text(passphrase).encode(),
            2048, dklen=64,
        )

    def mnemonic_decode(self, seed):
        number = 0
        words = seed.split()
        while words:
            number = number * len(self.words) + self.words.index(words.pop())
        return number


class Ledger:
    def __init__(self, configuration=None):
        configuration = configuration or {}
        network = configuration.get("network", "mainnet")
        testnet = network in ("testnet", "regtest")
        self.ledger_id = configuration.get(
            "id", "lbc_" + (network if testnet else "mainnet")
        )
        self.extended_public_key_prefix = bytes.fromhex(configuration.get(
            "extended_public_key_prefix", "043587cf" if testnet else "0488b21e"
        ))
        self.extended_private_key_prefix = bytes.fromhex(configuration.get(
            "extended_private_key_prefix", "04358394" if testnet else "0488ade4"
        ))
        self.pubkey_address_prefix = bytes.fromhex(configuration.get(
            "pubkey_address_prefix", "6f" if testnet else "55"
        ))
        self.accounts = []

    def get_id(self):
        return self.ledger_id

    def public_key_to_address(self, public_key):
        raw = self.pubkey_address_prefix + hash160(public_key)
        return base58_encode_check(raw)

    def add_account(self, account):
        self.accounts.append(account)


class Wallet:
    def __init__(self):
        self.accounts = []

    def add_account(self, account):
        self.accounts.append(account)


def openssl_aes_cbc(key, init_vector, value, decrypt=False):
    command = [
        "openssl", "enc", "-aes-256-cbc", "-d" if decrypt else "-e",
        "-K", key.hex(), "-iv", init_vector.hex(), "-nopad",
    ]
    completed = subprocess.run(
        command, input=value, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
    )
    if completed.returncode:
        raise ValueError(completed.stderr.decode(errors="replace").strip())
    return completed.stdout


def aes_encrypt(secret, value, init_vector=None):
    if init_vector is not None:
        assert len(init_vector) == 16
    else:
        init_vector = bytes(16)
    key = double_sha256(secret.encode())
    data = value.encode()
    padding = 16 - len(data) % 16
    data += bytes((padding,)) * padding
    encrypted = openssl_aes_cbc(key, init_vector, data)
    return base64.b64encode(init_vector + encrypted).decode()


def aes_decrypt(secret, value):
    try:
        data = base64.b64decode(value.encode())
        key = double_sha256(secret.encode())
        init_vector, encrypted = data[:16], data[16:]
        if len(init_vector) != 16:
            raise ValueError(f"Invalid IV size ({len(init_vector)}) for CBC.")
        # The pinned implementation never calls decryptor.finalize(). CBC's
        # update method buffers and silently drops any trailing partial block.
        encrypted = encrypted[:len(encrypted) // 16 * 16]
        decrypted = openssl_aes_cbc(key, init_vector, encrypted, decrypt=True)
        if not decrypted:
            raise InvalidPasswordError()
        padding = decrypted[-1]
        if padding < 1 or padding > 16 or decrypted[-padding:] != bytes((padding,)) * padding:
            raise InvalidPasswordError()
        return decrypted[:-padding].decode(), init_vector
    except UnicodeDecodeError as error:
        raise InvalidPasswordError() from error


def load_contract(sdk_root):
    source_path = sdk_root / "lbry" / "wallet" / "account.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    wanted = {
        "DeterministicChannelKeyManager", "AddressManager",
        "HierarchicalDeterministic", "SingleKey", "Account",
    }
    selected = [
        node for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name in wanted
    ]
    if {node.name for node in selected} != wanted:
        raise RuntimeError("pinned account classes changed")

    clock = FixtureClock()
    fixture_os = FixtureOS()
    namespace = {
        "Any": Any,
        "Dict": Dict,
        "List": List,
        "Optional": Optional,
        "Tuple": Tuple,
        "Type": Type,
        "COIN": 100_000_000,
        "InvalidPasswordError": InvalidPasswordError,
        "KeyPath": KeyPath,
        "Mnemonic": Mnemonic,
        "PrivateKey": PrivateKey,
        "PublicKey": PublicKey,
        "TXO_TYPES": {"other": 0, "purchase": 1},
        "aes_decrypt": aes_decrypt,
        "aes_encrypt": aes_encrypt,
        "asyncio": __import__("asyncio"),
        "from_extended_key_string": from_extended_key_string,
        "json": json,
        "log": logging.getLogger(__name__),
        "os": fixture_os,
        "random": random,
        "sha256": sha256,
        "time": clock,
    }
    module = ast.fix_missing_locations(ast.Module(body=selected, type_ignores=[]))
    exec(compile(module, str(source_path), "exec"), namespace)  # pylint: disable=exec-used
    return namespace["Account"], clock, fixture_os


def capture(function):
    try:
        return function(), None, None
    except Exception as error:  # pylint: disable=broad-except
        return None, type(error).__name__, str(error)


def json_text(value):
    return json.dumps(value)


def key_string(key):
    return key.extended_key_string() if key is not None else None


def state_dict(account):
    return {
        "id": account.id,
        "name": account.name,
        "seed": account.seed,
        "modified_on": account.modified_on,
        "private_key_string": account.private_key_string,
        "encrypted": account.encrypted,
        "private_key": key_string(account.private_key),
        "private_key_type": type(account.private_key).__name__ if account.private_key is not None else None,
        "public_key": key_string(account.public_key),
        "public_key_type": type(account.public_key).__name__,
        "address_generator": account.address_generator.to_dict(account.receiving, account.change),
        "certificates": copy.deepcopy(account.channel_keys),
        "init_vectors": {
            key: account.init_vectors[key].hex() for key in sorted(account.init_vectors)
        },
    }


def state_view(account):
    state = state_dict(account)
    return {"state": state, "state_json": json_text(state)}


def dict_view(account, encrypt_password=None, include_channel_keys=True):
    result, error_type, error = capture(lambda: account.to_dict(
        encrypt_password=encrypt_password,
        include_channel_keys=include_channel_keys,
    ))
    if result is not None:
        result = copy.deepcopy(result)
    return {
        "result": result,
        "key_order": list(result) if result is not None else None,
        "address_generator_key_order": (
            list(result["address_generator"]) if result is not None else None
        ),
        "receiving_key_order": (
            list(result["address_generator"].get("receiving", {})) if result is not None else None
        ),
        "change_key_order": (
            list(result["address_generator"].get("change", {})) if result is not None else None
        ),
        "json": json_text(result) if result is not None else None,
        "error_type": error_type,
        "error": error,
    }


def hash_view(account):
    hash_input, input_error_type, input_error = capture(
        lambda: account.to_dict(include_channel_keys=False)
    )
    digest, error_type, error = capture(lambda: account.hash.hex())
    return {
        "input": hash_input,
        "input_json": json_text(hash_input) if hash_input is not None else None,
        "input_error_type": input_error_type,
        "input_error": input_error,
        "hash": digest,
        "error_type": error_type,
        "error": error,
    }


def configure_case(clock, fixture_os, case):
    clock.now = case.get("now", 1_700_000_000.75)
    fixture_os.reset(case.get("urandom"))


def load_account(account_type, clock, fixture_os, case, record=None):
    configure_case(clock, fixture_os, case)
    ledger = Ledger(case.get("ledger"))
    wallet = Wallet()
    record = copy.deepcopy(case["record"] if record is None else record)
    account = account_type.from_dict(ledger, wallet, record)
    for key, value in case.get("init_vectors", {}).items():
        account.init_vectors[key] = bytes.fromhex(value)
    return account


def full_view(account, encrypt_password=None):
    view = state_view(account)
    view["to_dict"] = dict_view(account)
    view["to_dict_without_channel_keys"] = dict_view(
        account, include_channel_keys=False
    )
    if encrypt_password is not None:
        view["to_dict_with_password"] = dict_view(account, encrypt_password=encrypt_password)
    view["hash"] = hash_view(account)
    return view


def run_load_case(account_type, clock, fixture_os, case):
    account, error_type, error = capture(lambda: load_account(
        account_type, clock, fixture_os, case
    ))
    result = {
        "name": case.get("name", ""),
        "error_type": error_type,
        "error": error,
        "urandom_calls": fixture_os.calls,
    }
    if account is not None:
        result.update(full_view(account, case.get("encrypt_password")))
        result["urandom_calls"] = fixture_os.calls
    return result


def run_hash_case(account_type, clock, fixture_os, case):
    certificate_sets = case.get("certificate_sets")
    if certificate_sets is None:
        certificate_sets = [{"name": "", "certificates": case["record"].get("certificates", {})}]
    variants = []
    for certificate_set in certificate_sets:
        record = copy.deepcopy(case["record"])
        record["certificates"] = copy.deepcopy(certificate_set.get("certificates", {}))
        account, error_type, error = capture(lambda record=record: load_account(
            account_type, clock, fixture_os, case, record
        ))
        variant = {
            "name": certificate_set.get("name", ""),
            "error_type": error_type,
            "error": error,
        }
        if account is not None:
            variant.update(state_view(account))
            variant["hash"] = hash_view(account)
        variants.append(variant)
    return {"name": case.get("name", ""), "variants": variants}


def run_merge_case(account_type, clock, fixture_os, case):
    account, error_type, error = capture(lambda: load_account(
        account_type, clock, fixture_os, case
    ))
    result = {
        "name": case.get("name", ""),
        "error_type": error_type,
        "error": error,
        "initial": full_view(account) if account is not None else None,
        "merges": [],
    }
    if account is None:
        return result
    for merge_record in case.get("merges", []):
        value, merge_error_type, merge_error = capture(
            lambda merge_record=merge_record: account.merge(copy.deepcopy(merge_record))
        )
        merged = {
            "result": value,
            "error_type": merge_error_type,
            "error": merge_error,
        }
        merged.update(full_view(account))
        result["merges"].append(merged)
    return result


def run_crypt_case(account_type, clock, fixture_os, case):
    account, error_type, error = capture(lambda: load_account(
        account_type, clock, fixture_os, case
    ))
    result = {
        "name": case.get("name", ""),
        "error_type": error_type,
        "error": error,
        "initial": full_view(account) if account is not None else None,
        "actions": [],
    }
    if account is None:
        return result
    for action in case.get("actions", []):
        action_name = action["action"]
        if action_name == "to_dict":
            view = dict_view(
                account,
                encrypt_password=action.get("password"),
                include_channel_keys=action.get("include_channel_keys", True),
            )
            value = view["result"]
            error_type = view["error_type"]
            error = view["error"]
            action_view = {"action_to_dict": view}
        elif action_name == "encrypt":
            value, error_type, error = capture(lambda: account.encrypt(action["password"]))
            action_view = {}
        elif action_name == "decrypt":
            value, error_type, error = capture(lambda: account.decrypt(action["password"]))
            action_view = {}
        else:
            raise ValueError(f"unknown crypt action: {action_name}")
        operation = {
            "action": action_name,
            "result": value,
            "error_type": error_type,
            "error": error,
        }
        operation.update(action_view)
        operation.update(full_view(account))
        operation["urandom_calls"] = list(fixture_os.calls)
        result["actions"].append(operation)
    return result


def run(sdk_root, payload):
    if not __debug__:
        raise RuntimeError("account oracle requires Python assertions (__debug__)")
    commit = verify_pinned_sources(sdk_root)
    version = read_sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    Mnemonic.words = read_english_words(sdk_root)
    account_type, clock, fixture_os = load_contract(sdk_root)
    openssl_version = subprocess.run(
        ["openssl", "version"], check=True, stdout=subprocess.PIPE, text=True
    ).stdout.strip()
    return {
        "reference": {
            "commit": commit,
            "version": version,
            "source_sha256": PINNED_SOURCE_HASHES,
        },
        "metadata": {
            "python_version": ".".join(map(str, sys.version_info[:2])),
            "unicode_version": unicodedata.unidata_version,
            "python_debug": __debug__,
            "openssl_version": openssl_version,
            "default_fixture_time": 1_700_000_000.75,
            "default_fixture_iv_hex": bytes(16).hex(),
            "default_ledger_id": "lbc_mainnet",
            "default_public_key_prefix_hex": "0488b21e",
            "default_private_key_prefix_hex": "0488ade4",
            "default_address_prefix_hex": "55",
            "address_generators": list(account_type.address_generators),
        },
        "load_cases": [
            run_load_case(account_type, clock, fixture_os, case)
            for case in payload.get("load_cases", [])
        ],
        "hash_cases": [
            run_hash_case(account_type, clock, fixture_os, case)
            for case in payload.get("hash_cases", [])
        ],
        "merge_cases": [
            run_merge_case(account_type, clock, fixture_os, case)
            for case in payload.get("merge_cases", [])
        ],
        "crypt_cases": [
            run_crypt_case(account_type, clock, fixture_os, case)
            for case in payload.get("crypt_cases", [])
        ],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    arguments = parser.parse_args()
    result = run(arguments.sdk_root.resolve(), json.load(sys.stdin))
    json.dump(result, sys.stdout, sort_keys=True, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

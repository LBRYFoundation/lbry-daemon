#!/usr/bin/env python3
"""Run the pinned wallet sync envelope and merge contracts in isolation.

Input is one JSON object with any of these arrays:

``better_aes_cases``
    ``{"name", "operation": "encrypt", "password", "value_base64",
    "urandom": [hex, ...]}`` or
    ``{"name", "operation": "decrypt", "password", "encrypted"}``.

``pack_cases``
    ``{"name", "document", "ledgers"?, "password", "urandom"?, "now"?}``.
    ``document`` is a wallet JSON object. ``ledgers`` is an insertion-ordered
    object mapping ledger ids to configs.

``unpack_cases``
    ``{"name", "password", "encrypted"}``. ``encrypted`` may be an external
    Go-produced pack. The decoded JSON is returned without schema validation,
    exactly like ``Wallet.unpack``.

``merge_cases``
    ``{"name", "document", "ledgers"?, "password"?, "data"? ,
    "incoming"?, "encoding"?: "json"|"pack", "urandom"?, "now"?}``.
    An omitted or null password selects clear JSON; a present empty string is
    an AES password. ``data`` is used verbatim. Otherwise ``incoming`` is
    json-dumped, or loaded as a source wallet and packed when encoding is
    ``pack``.

Output contains source/runtime metadata and one result array per input array.
Every result records exception type/message rather than aborting the batch.
Pack results include the opaque base64 pack, decoded envelope fields, the
compressed plaintext bytes, and before/after wallet state. Merge results expose
added/merged id order plus post-error wallet and ledger state so partial
mutation is observable.
"""

import argparse
import ast
import base64
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import zlib


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/crypto/base58.py": "bed7bff1169a9d7b89473a08b6054170f52f50e3fb2126a9752c36e745e9b94f",
    "lbry/crypto/crypt.py": "d2df4360c8dd306730290624fe49a805091171730526307f07165c8729129216",
    "lbry/crypto/hash.py": "bfc430bd3fe98578b406caa3a8e2116a40f492c7b68e269176e838b4ef426a72",
    "lbry/error/__init__.py": "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
    "lbry/wallet/account.py": "ea2ca30bddf9c0145469e989d9855dbe7be5184943ae7b8ca690eda41eb7db50",
    "lbry/wallet/bip32.py": "bbc027ae706338bd7a232290c110dcefc308b2b635179e01f51487cf8b05825a",
    "lbry/wallet/ledger.py": "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
    "lbry/wallet/manager.py": "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
    "lbry/wallet/mnemonic.py": "6d731208e9274f397ed15eb445ce0024f6ad9adcd8a1a40cd5ed08b7d41fc2bc",
    "lbry/wallet/wallet.py": "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
    "lbry/wallet/words/english.py": "ec702cc5b02ea7bc749f742a70b56d55c26bf8ab6e0a0ce10429266051968dd3",
}
DEFAULT_LEDGER_CONFIG = {"lbc_mainnet": {"data_path": "/tmp/wallet-sync-oracle"}}


def load_local_module(name, filename):
    source_path = Path(__file__).with_name(filename)
    specification = importlib.util.spec_from_file_location(name, source_path)
    module = importlib.util.module_from_spec(specification)
    specification.loader.exec_module(module)
    return module


WALLET_ADAPTER = load_local_module(
    "lbry_wallet_manager_oracle_adapter", "wallet_manager_oracle.py"
)
ACCOUNT_ADAPTER = WALLET_ADAPTER.ACCOUNT_ADAPTER


class InvalidPasswordError(Exception):
    def __init__(self):
        super().__init__("Password is invalid.")


class FixtureEntropy:
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
            raise ValueError(
                f"fixture random value has length {len(value)}, expected {size}"
            )
        return value


SYNC_ENTROPY = FixtureEntropy()


def verify_pinned_sources(sdk_root):
    commit = subprocess.run(
        ["git", "-C", str(sdk_root), "rev-parse", "HEAD"], check=True,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    for relative_path, expected in PINNED_SOURCE_HASHES.items():
        actual = hashlib.sha256((sdk_root / relative_path).read_bytes()).hexdigest()
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
        if isinstance(node, ast.Assign) and any(
                isinstance(target, ast.Name) and target.id == "__version__"
                for target in node.targets):
            return ast.literal_eval(node.value)
    raise RuntimeError("could not read SDK version")


def openssl_aes_cbc(key, initialization_vector, value, decrypt=False):
    if len(initialization_vector) != 16:
        raise ValueError(
            f"Invalid IV size ({len(initialization_vector)}) for CBC."
        )
    if not value:
        return b""
    command = [
        "openssl", "enc", "-aes-256-cbc", "-d" if decrypt else "-e",
        "-K", key.hex(), "-iv", initialization_vector.hex(), "-nopad",
    ]
    completed = subprocess.run(
        command, input=value, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        check=False,
    )
    if completed.returncode:
        raise ValueError(completed.stderr.decode(errors="replace").strip())
    return completed.stdout


def derive_scrypt(password, salt, n=1 << 13, r=16, p=1):
    return hashlib.scrypt(password, salt=salt, n=n, r=r, p=p, dklen=32)


def pkcs7_pad(value):
    padding = 16 - len(value) % 16
    return value + bytes((padding,)) * padding


def pkcs7_unpad(value):
    if not value:
        raise InvalidPasswordError()
    padding = value[-1]
    if padding < 1 or padding > 16 or value[-padding:] != bytes((padding,)) * padding:
        raise InvalidPasswordError()
    return value[:-padding]


def better_aes_encrypt(secret, value):
    initialization_vector = SYNC_ENTROPY.urandom(16)
    key = derive_scrypt(secret.encode(), initialization_vector)
    encrypted = openssl_aes_cbc(
        key, initialization_vector, pkcs7_pad(value), decrypt=False
    )
    return base64.b64encode(
        b"s:8192:16:1:" + initialization_vector + encrypted
    )


def better_aes_decrypt(secret, value):
    data = base64.b64decode(value)
    _, scrypt_n, scrypt_r, scrypt_p, data = data.split(b":", maxsplit=4)
    initialization_vector, encrypted = data[:16], data[16:]
    key = derive_scrypt(
        secret.encode(), initialization_vector,
        int(scrypt_n), int(scrypt_r), int(scrypt_p),
    )
    # The pinned implementation omits decryptor.finalize(), so a partial final
    # block remains buffered and is discarded.
    encrypted = encrypted[:len(encrypted) // 16 * 16]
    decrypted = openssl_aes_cbc(
        key, initialization_vector, encrypted, decrypt=True
    )
    return pkcs7_unpad(decrypted)


def load_contract(sdk_root):
    ACCOUNT_ADAPTER.Mnemonic.words = ACCOUNT_ADAPTER.read_english_words(sdk_root)
    account_type, account_clock, account_entropy = ACCOUNT_ADAPTER.load_contract(sdk_root)
    wallet_type, storage_type, _preferences_type, wallet_clock = (
        WALLET_ADAPTER.load_wallet_contract(sdk_root, account_type)
    )
    manager_type = WALLET_ADAPTER.load_manager_contract(
        sdk_root, wallet_type, storage_type, account_type
    )
    WALLET_ADAPTER.patch_account_generation(account_type)

    namespace = wallet_type.pack.__globals__
    namespace["better_aes_encrypt"] = better_aes_encrypt
    namespace["better_aes_decrypt"] = better_aes_decrypt
    namespace["InvalidPasswordError"] = InvalidPasswordError
    return (
        wallet_type, storage_type, manager_type,
        wallet_clock, account_clock, account_entropy,
    )


def capture(function):
    try:
        return function(), None, None
    except Exception as error:  # pylint: disable=broad-except
        return None, type(error).__name__, str(error)


def configure_clocks(wallet_clock, account_clock, now):
    wallet_clock.now = now
    account_clock.now = now


def configured_manager(manager_type, ledgers=None):
    manager = manager_type()
    configurations = copy.deepcopy(
        DEFAULT_LEDGER_CONFIG if ledgers is None else ledgers
    )
    for ledger_id, configuration in configurations.items():
        manager.get_or_create_ledger(ledger_id, configuration)
    return manager


def load_wallet(
        wallet_type, storage_type, manager_type, document, ledgers=None):
    manager = configured_manager(manager_type, ledgers)
    storage = storage_type(default=copy.deepcopy(document))
    wallet = wallet_type.from_storage(storage, manager)
    manager.wallets.append(wallet)
    return wallet, manager


def account_view(account):
    generator = account.address_generator.to_dict(account.receiving, account.change)
    return {
        "id": account.id,
        "ledger": account.ledger.get_id(),
        "name": account.name,
        "modified_on": account.modified_on,
        "encrypted": account.encrypted,
        "public_key": account.public_key.extended_key_string(),
        "address_generator": copy.deepcopy(generator),
        "certificates": copy.deepcopy(account.channel_keys),
    }


def wallet_view(wallet):
    dictionary, dictionary_error_type, dictionary_error = capture(wallet.to_dict)
    encoded, json_error_type, json_error = capture(wallet.to_json)
    return {
        "id": wallet.id,
        "name": wallet.name,
        "preferences": copy.deepcopy(wallet.preferences.data),
        "accounts": [account_view(account) for account in wallet.accounts],
        "is_locked": wallet.is_locked,
        "encryption_password": wallet.encryption_password,
        "to_dict": copy.deepcopy(dictionary),
        "to_dict_json": json.dumps(dictionary) if dictionary is not None else None,
        "to_dict_error_type": dictionary_error_type,
        "to_dict_error": dictionary_error,
        "to_json": encoded,
        "to_json_error_type": json_error_type,
        "to_json_error": json_error,
    }


def manager_view(manager):
    ledgers = []
    for ledger_class, ledger in manager.ledgers.items():
        ledgers.append({
            "id": ledger_class.get_id(),
            "account_ids": [account.id for account in ledger.accounts],
        })
    return {
        "wallet_account_ids": [
            [account.id for account in wallet.accounts] for wallet in manager.wallets
        ],
        "ledgers": ledgers,
    }


def envelope_view(encrypted):
    decoded, error_type, error = capture(lambda: base64.b64decode(encrypted))
    result = {
        "decoded_hex": decoded.hex() if decoded is not None else None,
        "error_type": error_type,
        "error": error,
        "marker": None,
        "scrypt_n": None,
        "scrypt_r": None,
        "scrypt_p": None,
        "initialization_vector": None,
        "ciphertext_hex": None,
    }
    if decoded is None:
        return result
    fields, split_error_type, split_error = capture(
        lambda: decoded.split(b":", maxsplit=4)
    )
    if split_error_type is not None or len(fields) != 5:
        result["error_type"] = split_error_type or "ValueError"
        result["error"] = split_error or "not enough envelope fields"
        return result
    marker, scrypt_n, scrypt_r, scrypt_p, payload = fields
    result.update({
        "marker": marker.decode("ascii", errors="backslashreplace"),
        "scrypt_n": scrypt_n.decode("ascii", errors="backslashreplace"),
        "scrypt_r": scrypt_r.decode("ascii", errors="backslashreplace"),
        "scrypt_p": scrypt_p.decode("ascii", errors="backslashreplace"),
        "initialization_vector": payload[:16].hex(),
        "ciphertext_hex": payload[16:].hex(),
    })
    return result


def run_better_aes_case(case):
    SYNC_ENTROPY.reset(case.get("urandom"))
    operation = case["operation"]
    if operation == "encrypt":
        value = base64.b64decode(case.get("value_base64", ""))
        result, error_type, error = capture(
            lambda: better_aes_encrypt(case["password"], value)
        )
        encrypted = result.decode("ascii") if result is not None else None
        return {
            "name": case.get("name", ""),
            "operation": operation,
            "encrypted": encrypted,
            "envelope": envelope_view(encrypted) if encrypted is not None else None,
            "error_type": error_type,
            "error": error,
            "urandom_calls": list(SYNC_ENTROPY.calls),
        }
    if operation == "decrypt":
        result, error_type, error = capture(
            lambda: better_aes_decrypt(case["password"], case["encrypted"])
        )
        return {
            "name": case.get("name", ""),
            "operation": operation,
            "value_base64": base64.b64encode(result).decode() if result is not None else None,
            "value_hex": result.hex() if result is not None else None,
            "error_type": error_type,
            "error": error,
            "urandom_calls": list(SYNC_ENTROPY.calls),
        }
    raise ValueError(f"unknown better AES operation: {operation}")


def run_pack_case(contract, case):
    wallet_type, storage_type, manager_type, wallet_clock, account_clock, account_entropy = contract
    now = case.get("now", 1_700_000_000.75)
    configure_clocks(wallet_clock, account_clock, now)
    account_entropy.reset(case.get("account_urandom"))
    SYNC_ENTROPY.reset(case.get("urandom"))
    wallet, manager = load_wallet(
        wallet_type, storage_type, manager_type,
        case.get("document"), case.get("ledgers"),
    )
    before = wallet_view(wallet)
    packed, error_type, error = capture(lambda: wallet.pack(case["password"]))
    packed_text = packed.decode("ascii") if packed is not None else None
    compressed = None
    compressed_error_type = compressed_error = None
    unpacked = None
    unpack_error_type = unpack_error = None
    if packed is not None:
        compressed, compressed_error_type, compressed_error = capture(
            lambda: better_aes_decrypt(case["password"], packed)
        )
        unpacked, unpack_error_type, unpack_error = capture(
            lambda: wallet_type.unpack(case["password"], packed)
        )
    return {
        "name": case.get("name", ""),
        "before": before,
        "packed": packed_text,
        "envelope": envelope_view(packed) if packed is not None else None,
        "compressed_hex": compressed.hex() if compressed is not None else None,
        "compressed_error_type": compressed_error_type,
        "compressed_error": compressed_error,
        "unpacked": copy.deepcopy(unpacked),
        "unpacked_json": json.dumps(unpacked) if unpacked is not None else None,
        "unpack_error_type": unpack_error_type,
        "unpack_error": unpack_error,
        "error_type": error_type,
        "error": error,
        "after": wallet_view(wallet),
        "manager": manager_view(manager),
        "urandom_calls": list(SYNC_ENTROPY.calls),
        "account_urandom_calls": list(account_entropy.calls),
    }


def run_unpack_case(wallet_type, case):
    result, error_type, error = capture(
        lambda: wallet_type.unpack(case["password"], case["encrypted"])
    )
    return {
        "name": case.get("name", ""),
        "result": copy.deepcopy(result),
        "result_json": json.dumps(result) if result is not None else None,
        "key_order": list(result) if isinstance(result, dict) else None,
        "error_type": error_type,
        "error": error,
    }


def merge_data(contract, case):
    if "data" in case:
        return case["data"]
    incoming = copy.deepcopy(case.get("incoming"))
    encoding = case.get("encoding", "json")
    if encoding == "json":
        return json.dumps(incoming)
    if encoding != "pack":
        raise ValueError(f"unknown merge encoding: {encoding}")
    password = case.get("password")
    wallet_type, storage_type, manager_type, _wallet_clock, _account_clock, _account_entropy = contract
    source, _manager = load_wallet(
        wallet_type, storage_type, manager_type,
        incoming, case.get("source_ledgers", case.get("ledgers")),
    )
    return source.pack(password).decode("ascii")


def run_merge_case(contract, case):
    wallet_type, storage_type, manager_type, wallet_clock, account_clock, account_entropy = contract
    now = case.get("now", 1_700_000_000.75)
    configure_clocks(wallet_clock, account_clock, now)
    account_entropy.reset(case.get("account_urandom"))
    SYNC_ENTROPY.reset(case.get("urandom"))
    wallet, manager = load_wallet(
        wallet_type, storage_type, manager_type,
        case.get("document"), case.get("ledgers"),
    )
    before = wallet_view(wallet)
    data, prepare_error_type, prepare_error = capture(lambda: merge_data(contract, case))
    result = None
    error_type = error = None
    if data is not None:
        password = case.get("password") if "password" in case else None
        result, error_type, error = capture(
            lambda: wallet.merge(manager, password, data)
        )
    added_ids = merged_ids = None
    if result is not None:
        added, merged = result
        added_ids = [account.id for account in added]
        merged_ids = [account.id for account in merged]
    return {
        "name": case.get("name", ""),
        "password_present": "password" in case,
        "data": data,
        "prepare_error_type": prepare_error_type,
        "prepare_error": prepare_error,
        "added_ids": added_ids,
        "merged_ids": merged_ids,
        "error_type": error_type,
        "error": error,
        "before": before,
        "after": wallet_view(wallet),
        "manager": manager_view(manager),
        "urandom_calls": list(SYNC_ENTROPY.calls),
        "account_urandom_calls": list(account_entropy.calls),
    }


def run(sdk_root, payload):
    if not __debug__:
        raise RuntimeError("wallet sync oracle requires Python assertions (__debug__)")
    commit = verify_pinned_sources(sdk_root)
    version = read_sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    contract = load_contract(sdk_root)
    wallet_type = contract[0]
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
            "python_debug": __debug__,
            "zlib_version": zlib.ZLIB_VERSION,
            "zlib_runtime_version": zlib.ZLIB_RUNTIME_VERSION,
            "openssl_version": openssl_version,
        },
        "better_aes_cases": [
            run_better_aes_case(case)
            for case in payload.get("better_aes_cases", [])
        ],
        "pack_cases": [
            run_pack_case(contract, case)
            for case in payload.get("pack_cases", [])
        ],
        "unpack_cases": [
            run_unpack_case(wallet_type, case)
            for case in payload.get("unpack_cases", [])
        ],
        "merge_cases": [
            run_merge_case(contract, case)
            for case in payload.get("merge_cases", [])
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

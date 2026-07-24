#!/usr/bin/env python3
"""Source-pinned transaction and script parsing contract for SDK 0.113.0."""

import argparse
import ast
import asyncio
from binascii import hexlify, unhexlify
from collections import namedtuple
import hashlib
import hmac
from io import BytesIO
import json
import logging
from pathlib import Path
import struct
import subprocess
import sys
import typing
from itertools import chain
from typing import Iterable, List, Optional, Tuple


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/crypto/hash.py": "bfc430bd3fe98578b406caa3a8e2116a40f492c7b68e269176e838b4ef426a72",
    "lbry/wallet/bcd_data_stream.py": "ce1f81aaa823d30954959cfc294520b6c774f65b174ab381aeb079a2277ac292",
    "lbry/wallet/hash.py": "bac0ea401bef9481aba1bcfffb826326a4aaef094b0df3ac84d074e9941eb92d",
    "lbry/wallet/script.py": "bbfdeb4a2401f26eca81acd27c598cafb6ca7737fb5af195d8508dbf81c05c6d",
    "lbry/wallet/transaction.py": "e73491aeb915fbce931acbb4d9631f3e05440a7d26c598db85e66e524a798d15",
    "tests/unit/wallet/test_script.py": "128b1d2afa9a02796eae7d8d1254fe46c2e35669d5e77344cc1bbc67ee64231f",
    "tests/unit/wallet/test_transaction.py": "738b0b5010d7671a2cf1dd47024879cf9585204b088ae920de53b35f2a99130e",
}
PINNED_SIGNING_SOURCE_HASHES = {
    "lbry/wallet/bip32.py": "bbc027ae706338bd7a232290c110dcefc308b2b635179e01f51487cf8b05825a",
}
PINNED_BALANCING_SOURCE_HASHES = {
    "lbry/error/__init__.py": "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
    "lbry/wallet/coinselection.py": "96c686fc3a9037468e6d9c684080af4ee84f3710be7f6b42f1ddcc6ce5dc474e",
    "lbry/wallet/constants.py": "099e5b3a18a70439b9d7039717f0cb61c096c5936126fe6574a4ccda600a780f",
    "tests/unit/wallet/test_coinselection.py": "effdccee1eba922d311ca85c6a7c8eb0cc5381d8e54f05331e813d97249d63f7",
}

SECP256K1_P = 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F
SECP256K1_N = 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141
SECP256K1_G = (
    0x79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798,
    0x483ADA7726A3C4655DA4FBFC0E1108A8FD17B448A68554199C47D08FFB10D4B8,
)


def sha256_file(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def sdk_version(sdk_root):
    path = sdk_root / "lbry" / "__init__.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    for node in tree.body:
        if isinstance(node, ast.Assign) and any(
            isinstance(target, ast.Name) and target.id == "__version__"
            for target in node.targets
        ):
            return ast.literal_eval(node.value)
    raise RuntimeError("could not read SDK version")


def verify_reference(sdk_root):
    commit = subprocess.run(
        ["git", "-C", str(sdk_root), "rev-parse", "HEAD"],
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    for relative, expected in PINNED_SOURCE_HASHES.items():
        actual = sha256_file(sdk_root / relative)
        if actual != expected:
            raise RuntimeError(
                f"{relative} does not match pinned SDK: {actual}, expected {expected}"
            )
    for relative, expected in PINNED_SIGNING_SOURCE_HASHES.items():
        actual = sha256_file(sdk_root / relative)
        if actual != expected:
            raise RuntimeError(
                f"{relative} does not match pinned signing source: "
                f"{actual}, expected {expected}"
            )
    for relative, expected in PINNED_BALANCING_SOURCE_HASHES.items():
        actual = sha256_file(sdk_root / relative)
        if actual != expected:
            raise RuntimeError(
                f"{relative} does not match pinned balancing source: "
                f"{actual}, expected {expected}"
            )
    version = sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
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


def compressed_public_key(secret):
    if not 0 < secret < SECP256K1_N:
        raise ValueError("private key scalar is out of range")
    x, y = point_multiply(secret)
    return bytes((2 | (y & 1),)) + x.to_bytes(32, "big")


def double_sha256(value):
    return hashlib.sha256(hashlib.sha256(value).digest()).digest()


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


def deterministic_signature_scalars(secret, digest):
    message = int.from_bytes(digest, "big") % SECP256K1_N
    counter = 0
    while True:
        nonce = rfc6979_nonce(secret, digest, counter)
        counter += 1
        if not 0 < nonce < SECP256K1_N:
            continue
        point = point_multiply(nonce)
        r_value = point[0] % SECP256K1_N
        if r_value == 0:
            continue
        s_value = (
            pow(nonce, -1, SECP256K1_N) * (message + r_value * secret)
        ) % SECP256K1_N
        if s_value == 0:
            continue
        if s_value > SECP256K1_N // 2:
            s_value = SECP256K1_N - s_value
        return r_value, s_value


def der_integer(value):
    encoded = value.to_bytes(max(1, (value.bit_length() + 7) // 8), "big")
    encoded = encoded.lstrip(b"\x00") or b"\x00"
    if encoded[0] & 0x80:
        encoded = b"\x00" + encoded
    return b"\x02" + bytes((len(encoded),)) + encoded


def deterministic_der_signature(secret, payload):
    digest = double_sha256(payload)
    r_value, s_value = deterministic_signature_scalars(secret, digest)
    content = der_integer(r_value) + der_integer(s_value)
    return b"\x30" + bytes((len(content),)) + content


class OraclePublicKey:
    def __init__(self, secret):
        self.pubkey_bytes = compressed_public_key(secret)


class OraclePrivateKey:
    def __init__(self, secret):
        self.secret = secret
        self.public_key = OraclePublicKey(secret)
        self.payloads = []

    def sign(self, payload):
        self.payloads.append(bytes(payload))
        return deterministic_der_signature(self.secret, payload)


class SigningWallet:
    pass


class SigningLedger:
    def __init__(self):
        self.keys = {}
        self.lookups = []

    def hash160_to_address(self, pubkey_hash):
        return "hash160:" + pubkey_hash.hex()

    async def get_private_key_for_address(self, wallet, address):
        self.lookups.append(address)
        return self.keys.get(address)


class SigningAccount:
    def __init__(self, ledger, wallet):
        self.ledger = ledger
        self.wallet = wallet


class InsufficientFundsError(Exception):
    def __init__(self):
        super().__init__("Not enough funds to cover this transaction.")


def execute_nodes(path, predicate, namespace):
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    nodes = [node for node in tree.body if predicate(node)]
    module = ast.fix_missing_locations(ast.Module(body=nodes, type_ignores=[]))
    exec(compile(module, str(path), "exec"), namespace)
    return namespace


def load_runtime(sdk_root):
    stream_path = sdk_root / "lbry" / "wallet" / "bcd_data_stream.py"
    stream_namespace = {"struct": struct, "BytesIO": BytesIO}
    execute_nodes(
        stream_path,
        lambda node: isinstance(node, ast.ClassDef) and node.name == "BCDataStream",
        stream_namespace,
    )
    data_stream = stream_namespace["BCDataStream"]

    def subclass_tuple(name, base):
        return type(name, (base,), {"__slots__": ()})

    script_path = sdk_root / "lbry" / "wallet" / "script.py"
    script_namespace = {
        "List": List,
        "chain": chain,
        "hexlify": hexlify,
        "namedtuple": namedtuple,
        "BCDataStream": data_stream,
        "subclass_tuple": subclass_tuple,
    }
    execute_nodes(
        script_path,
        lambda node: not isinstance(node, (ast.Import, ast.ImportFrom)),
        script_namespace,
    )

    hash_path = sdk_root / "lbry" / "wallet" / "hash.py"
    hash_namespace = {
        "hexlify": hexlify,
        "unhexlify": unhexlify,
        "NULL_HASH32": bytes(32),
    }
    execute_nodes(hash_path, lambda node: isinstance(node, ast.ClassDef), hash_namespace)

    class Placeholder:
        pass

    class ReadOnlyList:
        def __init__(self, values):
            self.values = values

        def __getitem__(self, key):
            return self.values[key]

        def __len__(self):
            return len(self.values)

    transaction_log = logging.getLogger("transaction_oracle")
    transaction_log.handlers = [logging.NullHandler()]
    transaction_log.propagate = False
    transaction_namespace = {
        "struct": struct,
        "logging": logging,
        "typing": typing,
        "hexlify": hexlify,
        "unhexlify": unhexlify,
        "List": List,
        "Iterable": Iterable,
        "Optional": Optional,
        "Tuple": Tuple,
        "InsufficientFundsError": InsufficientFundsError,
        "hash160": lambda value: hashlib.new("ripemd160", hashlib.sha256(value).digest()).digest(),
        "sha256": lambda value: hashlib.sha256(value).digest(),
        "Base58": Placeholder,
        "normalize_name": lambda value: value,
        "Claim": Placeholder,
        "Signable": Placeholder,
        "Purchase": Placeholder,
        "Support": Placeholder,
        "InputScript": script_namespace["InputScript"],
        "OutputScript": script_namespace["OutputScript"],
        "COIN": 100_000_000,
        "DUST": 1_000,
        "NULL_HASH32": bytes(32),
        "BCDataStream": data_stream,
        "TXRef": hash_namespace["TXRef"],
        "TXRefImmutable": hash_namespace["TXRefImmutable"],
        "ReadOnlyList": ReadOnlyList,
        "PrivateKey": Placeholder,
        "PublicKey": Placeholder,
        "log": transaction_log,
    }
    transaction_path = sdk_root / "lbry" / "wallet" / "transaction.py"
    wanted_classes = {
        "TXRefMutable",
        "TXORef",
        "TXORefResolvable",
        "InputOutput",
        "Input",
        "OutputEffectiveAmountEstimator",
        "Output",
        "Transaction",
    }
    execute_nodes(
        transaction_path,
        lambda node: isinstance(node, ast.ClassDef) and node.name in wanted_classes,
        transaction_namespace,
    )
    return {
        "Transaction": transaction_namespace["Transaction"],
        "Input": transaction_namespace["Input"],
        "Output": transaction_namespace["Output"],
        "InputScript": script_namespace["InputScript"],
        "OutputScript": script_namespace["OutputScript"],
        "BCDataStream": data_stream,
    }


def extract_raw_fixture(sdk_root, method_name):
    path = sdk_root / "tests" / "unit" / "wallet" / "test_transaction.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    method = next(
        node
        for owner in tree.body
        if isinstance(owner, ast.ClassDef)
        for node in owner.body
        if isinstance(node, ast.FunctionDef) and node.name == method_name
    )
    assignment = next(
        node
        for node in method.body
        if isinstance(node, ast.Assign)
        and any(isinstance(target, ast.Name) and target.id == "raw" for target in node.targets)
    )
    call = assignment.value
    if not isinstance(call, ast.Call) or not call.args:
        raise RuntimeError(f"unexpected {method_name} fixture structure")
    return unhexlify(ast.literal_eval(call.args[0]))


def extract_signing_signature_fixture(sdk_root):
    path = sdk_root / "tests" / "unit" / "wallet" / "test_transaction.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    method = next(
        node
        for owner in tree.body
        if isinstance(owner, ast.ClassDef) and owner.name == "TestTransactionSigning"
        for node in owner.body
        if isinstance(node, ast.AsyncFunctionDef) and node.name == "test_sign"
    )
    assertion = next(
        node.value
        for node in method.body
        if isinstance(node, ast.Expr)
        and isinstance(node.value, ast.Call)
        and isinstance(node.value.func, ast.Attribute)
        and node.value.func.attr == "assertEqual"
    )
    encoded = ast.literal_eval(assertion.args[1])
    return encoded.decode("ascii") if isinstance(encoded, bytes) else encoded


def signing_adapter_unit_fixture(sdk_root):
    # m/0/0 for the source-pinned test_sign seed. Keeping the scalar and
    # preimage explicit makes this a Coincurve compatibility vector rather than
    # a second model of account/mnemonic derivation.
    private_key = bytes.fromhex(
        "aa2d0f7f58abf07145ea83735a57d5170aff682bf47e61ff8b2a0f9a0cb61d3f"
    )
    preimage = bytes.fromhex(
        "0100000001a9f894c5a7c8493625f883cbd4e28b9f757b6fc2b5e3eb09c49725c66f7cc7dd"
        "000000001976a91401244bd9f88fab49355f927b105d5650a8db344888acffffffff01802b53"
        "0b000000001976a91415a5ba33e2057819330e043b6b0b27b6f292c50c88ac000000000100"
        "0000"
    )
    expected = extract_signing_signature_fixture(sdk_root)
    actual = (
        deterministic_der_signature(int.from_bytes(private_key, "big"), preimage).hex()
        + "01"
    )
    if actual != expected:
        raise RuntimeError(
            f"deterministic signing adapter produced {actual}, pinned fixture is {expected}"
        )
    return {
        "private_key_hex": private_key.hex(),
        "public_key_hex": compressed_public_key(int.from_bytes(private_key, "big")).hex(),
        "preimage_hex": preimage.hex(),
        "digest_hex": double_sha256(preimage).hex(),
        "signature_hex": expected,
        "matches": True,
    }


def normalize_values(values):
    normalized = {}
    for key, value in values.items():
        if isinstance(value, bytes):
            normalized[key] = value.hex()
        elif isinstance(value, int):
            normalized[key] = str(value)
        else:
            raise RuntimeError(f"unsupported script value {key}: {type(value).__name__}")
    return normalized


def summarize_subscript(script):
    template = script.template.name
    values = script.values
    return {
        "source_hex": script.source.hex(),
        "template": template,
        "height": str(values["height"]) if "height" in values else "",
        "pubkey_hash_hex": values.get("pubkey_hash", b"").hex(),
        "signatures_count": values.get("signatures_count", 0),
        "pubkeys_hex": [value.hex() for value in values.get("pubkeys", [])],
        "pubkeys_count": values.get("pubkeys_count", 0),
    }


def summarize_script(script):
    try:
        template = script.template.name
        raw_values = script.values
        values = normalize_values({
            key: value for key, value in raw_values.items()
            if key not in ("signatures", "script")
        })
        signatures = [value.hex() for value in raw_values.get("signatures", [])]
        subscript = (
            summarize_subscript(raw_values["script"])
            if "script" in raw_values else None
        )
        error_type = None
        error_message = None
    except Exception as error:  # pylint: disable=broad-except
        template = ""
        values = {}
        signatures = []
        subscript = None
        error_type = type(error).__name__
        error_message = str(error)
    return {
        "source_hex": script.source.hex(),
        "ok": error_type is None,
        "template": template,
        "values": values,
        "signatures_hex": signatures,
        "subscript": subscript,
        "has_pubkey_hash": "pubkey_hash" in values,
        "has_script_hash": "script_hash" in values,
        "is_claim_name": bool(getattr(script, "is_claim_name", False)) if not error_type else False,
        "is_update_claim": bool(getattr(script, "is_update_claim", False)) if not error_type else False,
        "is_support_claim": bool(getattr(script, "is_support_claim", False)) if not error_type else False,
        "is_support_data": bool(getattr(script, "is_support_claim_data", False)) if not error_type else False,
        "error_type": error_type,
        "error_message": error_message,
    }


def summarize_transaction(name, raw, trailing_hex, transaction_class):
    try:
        transaction = transaction_class(raw)
        transaction_hash = transaction.hash
        transaction_id = transaction.id
        raw_sans_segwit = transaction.raw_sans_segwit
        inputs = []
        for tx_input in transaction.inputs:
            inputs.append({
                "position": tx_input.position,
                "previous_hash_hex": tx_input.txo_ref.tx_ref.hash.hex(),
                "previous_id": tx_input.txo_ref.tx_ref.id,
                "previous_index": tx_input.txo_ref.position,
                "sequence": tx_input.sequence,
                "coinbase_hex": None if tx_input.coinbase is None else tx_input.coinbase.hex(),
                "script": None if tx_input.script is None else summarize_script(tx_input.script),
            })
        outputs = []
        for tx_output in transaction.outputs:
            outputs.append({
                "id": tx_output.id,
                "position": tx_output.position,
                "amount": tx_output.amount,
                "script": summarize_script(tx_output.script),
                "claim_id": (
                    tx_output.claim_id
                    if tx_output.script.is_claim_involved else None
                ),
            })
        witnesses = [witness.hex() for witness in transaction.witnesses]
        transaction._reset()  # pylint: disable=protected-access
        reset_raw = transaction.raw
        return {
            "name": name,
            "raw_hex": raw.hex(),
            "ok": True,
            "version": transaction.version,
            "locktime": transaction.locktime,
            "segwit_flag": transaction.is_segwit_flag,
            "raw_sans_segwit_hex": raw_sans_segwit.hex(),
            "reset_raw_hex": reset_raw.hex(),
            "hash_hex": transaction_hash.hex(),
            "id": transaction_id,
            "height": transaction.height,
            "position": transaction.position,
            "is_verified": transaction.is_verified,
            "witnesses_hex": witnesses,
            "trailing_hex": trailing_hex,
            "inputs": inputs,
            "outputs": outputs,
            "error_type": None,
            "error_message": None,
        }
    except Exception as error:  # pylint: disable=broad-except
        return {
            "name": name,
            "raw_hex": raw.hex(),
            "ok": False,
            "error_type": type(error).__name__,
            "error_message": str(error),
        }


def transaction_cases(sdk_root, transaction_class):
    genesis = extract_raw_fixture(sdk_root, "test_genesis_transaction")
    claim = extract_raw_fixture(sdk_root, "test_claim_transaction")
    timelock = extract_raw_fixture(sdk_root, "test_redeem_scripthash_transaction")
    segwit = bytes.fromhex(
        "02000000000101" + "11" * 32 +
        "0200000000feffffff01e8030000000000001976a914" + "22" * 20 +
        "88ac0201aa02bbcc03000000"
    )
    noncanonical = genesis[:4] + bytes.fromhex("fd0100") + genesis[5:]
    fixtures = [
        ("genesis", genesis, ""),
        ("claim", claim, ""),
        ("timelock input", timelock, ""),
        ("segwit", segwit, ""),
        ("trailing bytes", genesis + bytes.fromhex("deadbeef"), "deadbeef"),
        ("noncanonical compact size", noncanonical, ""),
        ("empty", b"", ""),
        ("short version", bytes.fromhex("010000"), ""),
        ("missing locktime", genesis[:-1], ""),
        ("truncated variable bytes", bytes.fromhex(
            "0100000001" + "00" * 32 + "ffffffff05aabb"
        ), ""),
        ("truncated witness", segwit[:-5], ""),
    ]
    return [
        summarize_transaction(name, raw, trailing, transaction_class)
        for name, raw, trailing in fixtures
    ]


def generated_script(output_script, template, values):
    return output_script(template=template, values=values)


def extract_multisig_script_fixture(sdk_root):
    path = sdk_root / "tests" / "unit" / "wallet" / "test_script.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    method = next(
        node
        for owner in tree.body
        if isinstance(owner, ast.ClassDef)
        for node in owner.body
        if isinstance(node, ast.FunctionDef) and node.name == "test_redeem_script_hash_1"
    )
    assertion = next(
        node.value
        for node in method.body
        if isinstance(node, ast.Expr)
        and isinstance(node.value, ast.Call)
        and isinstance(node.value.func, ast.Attribute)
        and node.value.func.attr == "assertEqual"
    )
    return unhexlify(ast.literal_eval(assertion.args[1]))


def input_script_cases(sdk_root, input_script, transaction_class):
    signature = bytes.fromhex("300102")
    pubkey = bytes.fromhex("02" + "03" * 32)
    timelock_transaction = transaction_class(
        extract_raw_fixture(sdk_root, "test_redeem_scripthash_transaction")
    )
    fixtures = [
        ("no script", input_script(b"")),
        ("redeem pubkey", generated_script(
            input_script, input_script.REDEEM_PUBKEY, {"signature": signature}
        )),
        ("redeem pubkey hash", input_script.redeem_pubkey_hash(signature, pubkey)),
        ("redeem timelock", timelock_transaction.inputs[0].script),
        ("redeem multisig", input_script(extract_multisig_script_fixture(sdk_root))),
        ("missing pushdata1 length", input_script(bytes.fromhex("4c"))),
        ("missing pushdata2 length", input_script(bytes.fromhex("4d"))),
        ("invalid input", input_script(bytes.fromhex("ff"))),
        ("partial pushdata2 length", input_script(bytes.fromhex("4d01"))),
    ]
    return [
        {"name": name, **summarize_script(script)}
        for name, script in fixtures
    ]


def script_cases(output_script):
    pubkey = bytes.fromhex("02" + "03" * 32)
    pubkey_hash = bytes.fromhex("11" * 20)
    script_hash = bytes.fromhex("22" * 20)
    claim_id = bytes.fromhex("33" * 20)
    name = b"name"
    claim = bytes.fromhex("010203")
    support = bytes.fromhex("0405")
    success = [
        ("no script", output_script(b"")),
        ("pay pubkey full", generated_script(
            output_script, output_script.PAY_PUBKEY_FULL, {"pubkey": pubkey}
        )),
        ("pay pubkey hash", output_script.pay_pubkey_hash(pubkey_hash)),
        ("pay script hash", output_script.pay_script_hash(script_hash)),
        ("pay segwit", generated_script(
            output_script, output_script.PAY_SEGWIT, {"script_hash": script_hash}
        )),
        ("return data", output_script.return_data(bytes.fromhex("aabb"))),
        ("claim pubkey hash", generated_script(
            output_script, output_script.CLAIM_NAME_PUBKEY,
            {"claim_name": name, "claim": claim, "pubkey_hash": pubkey_hash},
        )),
        ("claim script hash", generated_script(
            output_script, output_script.CLAIM_NAME_SCRIPT,
            {"claim_name": name, "claim": claim, "script_hash": script_hash},
        )),
        ("support pubkey hash", generated_script(
            output_script, output_script.SUPPORT_CLAIM_PUBKEY,
            {"claim_name": name, "claim_id": claim_id, "pubkey_hash": pubkey_hash},
        )),
        ("support script hash", generated_script(
            output_script, output_script.SUPPORT_CLAIM_SCRIPT,
            {"claim_name": name, "claim_id": claim_id, "script_hash": script_hash},
        )),
        ("support data pubkey hash", generated_script(
            output_script, output_script.SUPPORT_CLAIM_DATA_PUBKEY,
            {
                "claim_name": name, "claim_id": claim_id,
                "support": support, "pubkey_hash": pubkey_hash,
            },
        )),
        ("support empty data script hash", generated_script(
            output_script, output_script.SUPPORT_CLAIM_DATA_SCRIPT,
            {
                "claim_name": name, "claim_id": claim_id,
                "support": b"", "script_hash": script_hash,
            },
        )),
        ("update pubkey hash", generated_script(
            output_script, output_script.UPDATE_CLAIM_PUBKEY,
            {
                "claim_name": name, "claim_id": claim_id,
                "claim": claim, "pubkey_hash": pubkey_hash,
            },
        )),
        ("update script hash", generated_script(
            output_script, output_script.UPDATE_CLAIM_SCRIPT,
            {
                "claim_name": name, "claim_id": claim_id,
                "claim": claim, "script_hash": script_hash,
            },
        )),
        ("noncanonical pushdata", output_script(
            bytes.fromhex("76a94c14") + pubkey_hash + bytes.fromhex("88ac")
        )),
        ("return truncated payload", output_script(bytes.fromhex("6a05aabb"))),
        ("return missing pushdata1 length", output_script(bytes.fromhex("6a4c"))),
    ]
    failures = [
        ("unknown opcode", output_script(bytes.fromhex("ff"))),
        ("truncated pushdata", output_script(bytes.fromhex("4c"))),
        ("extra token", output_script(
            bytes.fromhex("76a914") + pubkey_hash + bytes.fromhex("88ac00")
        )),
    ]
    return [
        {"name": name, **summarize_script(script)}
        for name, script in success + failures
    ]


def serialize_io(value, data_stream):
    stream = data_stream()
    value.serialize_to(stream)
    return stream.get_bytes()


def summarize_constructed_transaction(name, transaction):
    raw = transaction.raw
    transaction_hash = transaction.hash
    transaction_id = transaction.id
    inputs = list(transaction.inputs)
    outputs = list(transaction.outputs)
    return {
        "name": name,
        "version": transaction.version,
        "locktime": transaction.locktime,
        "height": transaction.height,
        "position": transaction.position,
        "is_verified": transaction.is_verified,
        "raw_hex": raw.hex(),
        "raw_sans_segwit_hex": transaction.raw_sans_segwit.hex(),
        "hash_hex": transaction_hash.hex(),
        "id": transaction_id,
        "size": transaction.size,
        "base_size": transaction.base_size,
        "input_sum": transaction.input_sum,
        "output_sum": transaction.output_sum,
        "fee": transaction.fee,
        "input_positions": [value.position for value in inputs],
        "output_positions": [value.position for value in outputs],
        "input_transaction_ids": [value.tx_ref.id for value in inputs],
        "input_previous_ids": [value.txo_ref.id for value in inputs],
        "output_ids": [value.id for value in outputs],
        "input_sizes": [value.size for value in inputs],
        "output_sizes": [value.size for value in outputs],
    }


def generated_output_cases(output_class, output_script, data_stream):
    pubkey_hash = bytes.fromhex("11" * 20)
    script_hash = bytes.fromhex("22" * 20)
    claim_id = bytes.fromhex("33" * 20)
    claim_name = b"name"
    claim = bytes.fromhex("010203")
    support = bytes.fromhex("0405")
    fixtures = [
        ("pay pubkey hash", output_class.pay_pubkey_hash(1_001, pubkey_hash)),
        ("pay script hash", output_class.pay_script_hash(2_002, script_hash)),
        ("return data", output_class(
            0, output_script.return_data(bytes.fromhex("aabb"))
        )),
        ("claim pubkey hash", output_class(
            3_003,
            output_script.pay_claim_name_pubkey_hash(claim_name, claim, pubkey_hash),
        )),
        ("update pubkey hash", output_class(
            4_004,
            output_script.pay_update_claim_pubkey_hash(
                claim_name, claim_id, claim, pubkey_hash
            ),
        )),
        ("support data pubkey hash", output_class(
            5_005,
            output_script.pay_support_data_pubkey_hash(
                claim_name, claim_id, support, pubkey_hash
            ),
        )),
    ]
    return [
        {
            "name": name,
            "amount": output.amount,
            "size": output.size,
            "serialized_hex": serialize_io(output, data_stream).hex(),
            "script": summarize_script(output.script),
        }
        for name, output in fixtures
    ]


def make_parent_output(transaction_class, output_class, amount, pubkey_hash, version=1):
    transaction = transaction_class(version=version).add_outputs([
        output_class.pay_pubkey_hash(amount, pubkey_hash)
    ])
    # Populate both mutable reference caches before another transaction keeps the
    # output reference. This is the normal Input.spend construction path.
    _ = transaction.raw
    _ = transaction.id
    return transaction, transaction.outputs[0]


def spend_placeholder_case(transaction_class, input_class, output_class, data_stream):
    parent, parent_output = make_parent_output(
        transaction_class, output_class, 123_456_789, bytes.fromhex("44" * 20)
    )
    transaction_input = input_class.spend(parent_output)
    return {
        "parent_raw_hex": parent.raw.hex(),
        "parent_id": parent.id,
        "parent_output_id": parent_output.id,
        "previous_transaction_hash_hex": transaction_input.txo_ref.tx_ref.hash.hex(),
        "previous_output_hash_hex": transaction_input.txo_ref.hash.hex(),
        "previous_index": transaction_input.txo_ref.position,
        "position": transaction_input.position,
        "sequence": transaction_input.sequence,
        "amount": transaction_input.amount,
        "size": transaction_input.size,
        "serialized_hex": serialize_io(transaction_input, data_stream).hex(),
        "script": summarize_script(transaction_input.script),
    }


def chained_construction_case(transaction_class, input_class, output_class):
    _, first_parent_output = make_parent_output(
        transaction_class, output_class, 700_000, bytes.fromhex("51" * 20), 2
    )
    _, second_parent_output = make_parent_output(
        transaction_class, output_class, 900_000, bytes.fromhex("52" * 20), 3
    )
    first_input = input_class.spend(first_parent_output)
    second_input = input_class.spend(second_parent_output)
    transaction = transaction_class(version=4, locktime=77)
    returned = transaction \
        .add_inputs([first_input]) \
        .add_outputs([output_class.pay_pubkey_hash(500_000, bytes.fromhex("61" * 20))]) \
        .add_inputs([second_input]) \
        .add_outputs([output_class.pay_script_hash(1_000_000, bytes.fromhex("62" * 20))])
    result = summarize_constructed_transaction("chained add", transaction)
    result["returned_self"] = returned is transaction
    return result


def mutation_reset_case(transaction_class, input_class, output_class):
    _, parent_output = make_parent_output(
        transaction_class, output_class, 800_000, bytes.fromhex("71" * 20)
    )
    transaction = transaction_class() \
        .add_inputs([input_class.spend(parent_output)]) \
        .add_outputs([output_class.pay_pubkey_hash(700_000, bytes.fromhex("72" * 20))])
    initial_raw = transaction.raw
    initial_id = transaction.id
    initial_output_id = transaction.outputs[0].id
    initial_amount = transaction.outputs[0].amount

    transaction.outputs[0].amount += 1
    cached_raw = transaction.raw
    cached_id = transaction.id
    cached_output_id = transaction.outputs[0].id

    transaction._reset()  # pylint: disable=protected-access
    reset_raw = transaction.raw
    reset_id = transaction.id
    reset_output_id = transaction.outputs[0].id
    return {
        "initial_amount": initial_amount,
        "mutated_amount": transaction.outputs[0].amount,
        "initial_raw_hex": initial_raw.hex(),
        "cached_raw_hex": cached_raw.hex(),
        "reset_raw_hex": reset_raw.hex(),
        "initial_id": initial_id,
        "cached_id": cached_id,
        "reset_id": reset_id,
        "initial_output_id": initial_output_id,
        "cached_output_id": cached_output_id,
        "reset_output_id": reset_output_id,
        "cached_raw_unchanged": cached_raw == initial_raw,
        "cached_id_unchanged": cached_id == initial_id,
        "reset_raw_changed": reset_raw != initial_raw,
        "reset_id_changed": reset_id != initial_id,
    }


def canonical_reset_case(name, raw, trailing, transaction_class):
    transaction = transaction_class(raw)
    original_raw = transaction.raw
    original_id = transaction.id
    transaction._reset()  # pylint: disable=protected-access
    canonical_raw = transaction.raw
    canonical_id = transaction.id
    return {
        "name": name,
        "original_raw_hex": original_raw.hex(),
        "original_id": original_id,
        "canonical_raw_hex": canonical_raw.hex(),
        "canonical_id": canonical_id,
        "trailing_hex": trailing.hex(),
        "raw_changed": canonical_raw != original_raw,
        "id_changed": canonical_id != original_id,
    }


def unsigned_construction_cases(sdk_root, runtime):
    transaction_class = runtime["Transaction"]
    input_class = runtime["Input"]
    output_class = runtime["Output"]
    output_script = runtime["OutputScript"]
    data_stream = runtime["BCDataStream"]
    genesis = extract_raw_fixture(sdk_root, "test_genesis_transaction")
    noncanonical = genesis[:4] + bytes.fromhex("fd0100") + genesis[5:]
    trailing = bytes.fromhex("deadbeef")
    return {
        "defaults": [
            summarize_constructed_transaction("default", transaction_class()),
            summarize_constructed_transaction(
                "version and locktime", transaction_class(version=2, locktime=42)
            ),
        ],
        "generated_outputs": generated_output_cases(
            output_class, output_script, data_stream
        ),
        "spend_placeholder": spend_placeholder_case(
            transaction_class, input_class, output_class, data_stream
        ),
        "chained": chained_construction_case(
            transaction_class, input_class, output_class
        ),
        "mutation_reset": mutation_reset_case(
            transaction_class, input_class, output_class
        ),
        "canonical_resets": [
            canonical_reset_case(
                "noncanonical compact size", noncanonical, b"", transaction_class
            ),
            canonical_reset_case(
                "trailing bytes", genesis + trailing, trailing, transaction_class
            ),
        ],
    }


def oracle_hash160(value):
    return hashlib.new("ripemd160", hashlib.sha256(value).digest()).digest()


def signing_parent(transaction_class, output_class, output):
    transaction = transaction_class().add_outputs([output])
    _ = transaction.raw
    _ = transaction.id
    return transaction, transaction.outputs[0]


def signing_summary(name, transaction, unsigned_raw, unsigned_id, preimages,
                    private_keys, ledger, details=None):
    post_sign_raw_cache_is_none = transaction._raw is None  # pylint: disable=protected-access
    post_sign_id_cache_is_none = transaction.ref._id is None  # pylint: disable=protected-access
    final_raw = transaction.raw
    final_hash = transaction.hash
    final_id = transaction.id
    inputs = list(transaction.inputs)
    result = {
        "name": name,
        "unsigned_raw_hex": unsigned_raw.hex(),
        "unsigned_id": unsigned_id,
        "preimages_hex": [value.hex() for value in preimages],
        "digests_hex": [double_sha256(value).hex() for value in preimages],
        "key_payloads_hex": [
            [value.hex() for value in private_key.payloads]
            for private_key in private_keys
        ],
        "lookup_addresses": list(ledger.lookups),
        "signatures_hex": [
            value.script.values["signature"].hex() for value in inputs
        ],
        "der_signatures_hex": [
            value.script.values["signature"][:-1].hex() for value in inputs
        ],
        "public_keys_hex": [
            value.script.values["pubkey"].hex() for value in inputs
        ],
        "input_scripts_hex": [value.script.source.hex() for value in inputs],
        "preimages_after_hex": [
            transaction._serialize_for_signature(index).hex()  # pylint: disable=protected-access
            for index in range(len(inputs))
        ],
        "post_sign_raw_cache_is_none": post_sign_raw_cache_is_none,
        "post_sign_id_cache_is_none": post_sign_id_cache_is_none,
        "final_raw_hex": final_raw.hex(),
        "final_hash_hex": final_hash.hex(),
        "final_id": final_id,
        "raw_changed": final_raw != unsigned_raw,
        "id_changed": final_id != unsigned_id,
    }
    if details:
        result.update(details)
    return result


async def p2pkh_signing_case(runtime):
    transaction_class = runtime["Transaction"]
    input_class = runtime["Input"]
    output_class = runtime["Output"]
    key = OraclePrivateKey(1)
    pubkey_hash = oracle_hash160(key.public_key.pubkey_bytes)
    _, parent_output = signing_parent(
        transaction_class, output_class,
        output_class.pay_pubkey_hash(1_600_000, pubkey_hash),
    )
    transaction = transaction_class(version=2, locktime=9) \
        .add_inputs([input_class.spend(parent_output)]) \
        .add_outputs([output_class.pay_pubkey_hash(
            1_500_000, bytes.fromhex("91" * 20)
        )])
    preimages = [transaction._serialize_for_signature(0)]  # pylint: disable=protected-access
    unsigned_raw = transaction.raw
    unsigned_id = transaction.id
    ledger = SigningLedger()
    wallet = SigningWallet()
    account = SigningAccount(ledger, wallet)
    ledger.keys[ledger.hash160_to_address(pubkey_hash)] = key
    await transaction.sign([account])
    return signing_summary(
        "single p2pkh", transaction, unsigned_raw, unsigned_id,
        preimages, [key], ledger,
        {"previous_output_script_hex": parent_output.script.source.hex()},
    )


async def multi_input_signing_case(runtime):
    transaction_class = runtime["Transaction"]
    input_class = runtime["Input"]
    output_class = runtime["Output"]
    keys = [OraclePrivateKey(2), OraclePrivateKey(0x12345)]
    pubkey_hashes = [
        oracle_hash160(private_key.public_key.pubkey_bytes)
        for private_key in keys
    ]
    parent_outputs = []
    for index in range(2):
        _, output = signing_parent(
            transaction_class, output_class,
            output_class.pay_pubkey_hash(
                1_000_000 + index * 500_000, pubkey_hashes[index]
            ),
        )
        parent_outputs.append(output)
    transaction = transaction_class(version=3, locktime=11) \
        .add_inputs([input_class.spend(output) for output in parent_outputs]) \
        .add_outputs([output_class.pay_pubkey_hash(
            2_300_000, bytes.fromhex("92" * 20)
        )])
    preimages = [
        transaction._serialize_for_signature(index)  # pylint: disable=protected-access
        for index in range(2)
    ]
    unsigned_raw = transaction.raw
    unsigned_id = transaction.id
    ledger = SigningLedger()
    wallet = SigningWallet()
    account = SigningAccount(ledger, wallet)
    for pubkey_hash, private_key in zip(pubkey_hashes, keys):
        ledger.keys[ledger.hash160_to_address(pubkey_hash)] = private_key
    await transaction.sign([account])
    return signing_summary(
        "two p2pkh inputs", transaction, unsigned_raw, unsigned_id,
        preimages, keys, ledger,
        {"selected_scripts_hex": [
            output.script.source.hex() for output in parent_outputs
        ]},
    )


async def timelock_signing_case(runtime):
    transaction_class = runtime["Transaction"]
    input_class = runtime["Input"]
    input_script = runtime["InputScript"]
    output_class = runtime["Output"]
    key = OraclePrivateKey(3)
    pubkey_hash = oracle_hash160(key.public_key.pubkey_bytes)
    redeem_script = input_script(
        template=input_script.TIME_LOCK_SCRIPT,
        values={"height": 500, "pubkey_hash": pubkey_hash},
    )
    _, parent_output = signing_parent(
        transaction_class, output_class,
        output_class.pay_script_hash(2_000_000, oracle_hash160(redeem_script.source)),
    )
    transaction_input = input_class.spend_time_lock(parent_output, redeem_script.source)
    transaction_input.sequence = 0xFFFFFFFE
    transaction = transaction_class(version=2, locktime=500) \
        .add_inputs([transaction_input]) \
        .add_outputs([output_class.pay_pubkey_hash(
            1_900_000, bytes.fromhex("93" * 20)
        )])
    preimages = [transaction._serialize_for_signature(0)]  # pylint: disable=protected-access
    unsigned_raw = transaction.raw
    unsigned_id = transaction.id
    ledger = SigningLedger()
    wallet = SigningWallet()
    account = SigningAccount(ledger, wallet)
    await transaction.sign([account], {"timelock": key})
    return signing_summary(
        "p2sh timelock", transaction, unsigned_raw, unsigned_id,
        preimages, [key], ledger,
        {
            "redeem_script_hex": redeem_script.source.hex(),
            "previous_output_script_hex": parent_output.script.source.hex(),
        },
    )


def signing_error_view(name, transaction, error, before_scripts):
    inputs = list(transaction.inputs)
    signatures = []
    for value in inputs:
        try:
            signatures.append(value.script.values.get("signature", b"").hex())
        except Exception:  # pylint: disable=broad-except
            signatures.append("")
    return {
        "name": name,
        "error_type": type(error).__name__,
        "error_message": str(error),
        "raw_cache_is_none": transaction._raw is None,  # pylint: disable=protected-access
        "id_cache_is_none": transaction.ref._id is None,  # pylint: disable=protected-access
        "before_scripts_hex": before_scripts,
        "after_scripts_hex": [value.script.source.hex() for value in inputs],
        "signatures_hex": signatures,
    }


async def signing_error_cases(runtime):
    transaction_class = runtime["Transaction"]
    input_class = runtime["Input"]
    input_script = runtime["InputScript"]
    output_class = runtime["Output"]
    output_script = runtime["OutputScript"]
    ledger = SigningLedger()
    wallet = SigningWallet()
    account = SigningAccount(ledger, wallet)
    result = []

    first_key = OraclePrivateKey(4)
    missing_key = OraclePrivateKey(5)
    hashes = [
        oracle_hash160(first_key.public_key.pubkey_bytes),
        oracle_hash160(missing_key.public_key.pubkey_bytes),
    ]
    parents = []
    for index, pubkey_hash in enumerate(hashes):
        _, output = signing_parent(
            transaction_class, output_class,
            output_class.pay_pubkey_hash(1_000_000 + index, pubkey_hash),
        )
        parents.append(output)
    partial = transaction_class() \
        .add_inputs([input_class.spend(output) for output in parents]) \
        .add_outputs([output_class.pay_pubkey_hash(1_500_000, bytes.fromhex("94" * 20))])
    before_scripts = [value.script.source.hex() for value in partial.inputs]
    _ = partial.raw
    _ = partial.id
    ledger.keys[ledger.hash160_to_address(hashes[0])] = first_key
    try:
        await partial.sign([account])
    except Exception as error:  # pylint: disable=broad-except
        view = signing_error_view("missing second key after partial sign", partial, error, before_scripts)
        view["first_key_payloads_hex"] = [value.hex() for value in first_key.payloads]
        result.append(view)

    full_pubkey = compressed_public_key(6)
    full_script = output_script(
        template=output_script.PAY_PUBKEY_FULL, values={"pubkey": full_pubkey}
    )
    _, unsupported_output = signing_parent(
        transaction_class, output_class, output_class(1_000_000, full_script)
    )
    unsupported_input = input_class(
        unsupported_output.ref,
        input_script.redeem_pubkey_hash(
            input_class.NULL_SIGNATURE, input_class.NULL_PUBLIC_KEY
        ),
    )
    unsupported = transaction_class() \
        .add_inputs([unsupported_input]) \
        .add_outputs([output_class.pay_pubkey_hash(900_000, bytes.fromhex("95" * 20))])
    before_scripts = [unsupported.inputs[0].script.source.hex()]
    try:
        await unsupported.sign([account])
    except Exception as error:  # pylint: disable=broad-except
        result.append(signing_error_view(
            "unsupported previous output", unsupported, error, before_scripts
        ))

    _, unresolved_output = signing_parent(
        transaction_class, output_class,
        output_class.pay_pubkey_hash(1_000_000, hashes[0]),
    )
    unresolved_input = input_class.spend(unresolved_output)
    unresolved_input.txo_ref._txo = None  # pylint: disable=protected-access
    unresolved = transaction_class() \
        .add_inputs([unresolved_input]) \
        .add_outputs([output_class.pay_pubkey_hash(900_000, bytes.fromhex("96" * 20))])
    before_scripts = [unresolved.inputs[0].script.source.hex()]
    try:
        await unresolved.sign([account])
    except Exception as error:  # pylint: disable=broad-except
        result.append(signing_error_view(
            "unresolved previous output", unresolved, error, before_scripts
        ))

    timelock_key = OraclePrivateKey(7)
    timelock_hash = oracle_hash160(timelock_key.public_key.pubkey_bytes)
    redeem_script = input_script(
        template=input_script.TIME_LOCK_SCRIPT,
        values={"height": 600, "pubkey_hash": timelock_hash},
    )
    _, script_hash_output = signing_parent(
        transaction_class, output_class,
        output_class.pay_script_hash(1_000_000, oracle_hash160(redeem_script.source)),
    )
    missing_extra = transaction_class() \
        .add_inputs([input_class.spend_time_lock(
            script_hash_output, redeem_script.source
        )]) \
        .add_outputs([output_class.pay_pubkey_hash(900_000, bytes.fromhex("97" * 20))])
    before_scripts = [missing_extra.inputs[0].script.source.hex()]
    try:
        await missing_extra.sign([account])
    except Exception as error:  # pylint: disable=broad-except
        result.append(signing_error_view(
            "p2sh missing extra keys", missing_extra, error, before_scripts
        ))
    return result


def signing_cases(sdk_root, runtime):
    async def execute():
        return {
            "unit_fixture_signature_hex": extract_signing_signature_fixture(sdk_root),
            "unit_fixture": signing_adapter_unit_fixture(sdk_root),
            "p2pkh": await p2pkh_signing_case(runtime),
            "multi_input": await multi_input_signing_case(runtime),
            "timelock": await timelock_signing_case(runtime),
            "errors": await signing_error_cases(runtime),
        }
    return asyncio.run(execute())


class BalancingWallet:
    def __init__(self, name):
        self.name = name


class BalancingFailingFee:
    def __rmul__(self, _value):
        raise RuntimeError("initial fee failed")


class BalancingChange:
    def __init__(self, account, address="change-address", error=None):
        self.account = account
        self.address = address
        self.error = error
        self.calls = 0

    async def get_or_create_usable_address(self):
        self.calls += 1
        if self.error is not None:
            raise self.error
        return self.address


class BalancingAccount:
    def __init__(self, name, ledger, wallet, change_error=None):
        self.name = name
        self.ledger = ledger
        self.wallet = wallet
        self.change = BalancingChange(self, error=change_error)


class BalancingLedger:
    def __init__(self, name="ledger", fee_per_byte=50, fee_per_name_char=200_000):
        self.name = name
        self.fee_per_byte = fee_per_byte
        self.fee_per_name_char = fee_per_name_char
        self.selection_batches = []
        self.selection_calls = []
        self.address_hash_calls = []
        self.key_lookups = []
        self.keys = {}
        self.release_snapshots = []
        self.release_error = None
        self.change_hash = bytes.fromhex("a1" * 20)

    async def get_spendable_utxos(self, amount, funding_accounts):
        accounts = list(funding_accounts)
        self.selection_calls.append({
            "amount": amount,
            "accounts": [account.name for account in accounts],
        })
        batch = self.selection_batches.pop(0) if self.selection_batches else []
        if isinstance(batch, Exception):
            raise batch
        return [
            value if hasattr(value, "effective_amount") else value.get_estimator(self)
            for value in batch
        ]

    async def release_tx(self, transaction):
        self.release_snapshots.append(balancing_release_snapshot(transaction))
        if self.release_error is not None:
            raise self.release_error

    def address_to_hash160(self, address):
        self.address_hash_calls.append(address)
        return self.change_hash

    def hash160_to_address(self, pubkey_hash):
        return "hash160:" + pubkey_hash.hex()

    async def get_private_key_for_address(self, wallet, address):
        self.key_lookups.append(address)
        return self.keys.get(address)


def balancing_release_snapshot(transaction):
    return {
        "input_amounts": [
            value.amount if value.txo_ref.txo is not None else None
            for value in transaction.inputs
        ],
        "output_amounts": [value.amount for value in transaction.outputs],
        "input_scripts_hex": [
            None if value.script is None else value.script.source.hex()
            for value in transaction.inputs
        ],
        "output_scripts_hex": [value.script.source.hex() for value in transaction.outputs],
    }


def balancing_transaction_summary(transaction):
    inputs = list(transaction.inputs)
    outputs = list(transaction.outputs)
    return {
        "version": transaction.version,
        "locktime": transaction.locktime,
        "raw_hex": transaction.raw.hex(),
        "id": transaction.id,
        "size": transaction.size,
        "base_size": transaction.base_size,
        "input_sum": transaction.input_sum,
        "output_sum": transaction.output_sum,
        "fee": transaction.fee,
        "input_amounts": [value.amount for value in inputs],
        "input_effective_amounts": [
            value.amount - value.size * 50 for value in inputs
        ],
        "input_sizes": [value.size for value in inputs],
        "input_sequences": [value.sequence for value in inputs],
        "input_previous_ids": [value.txo_ref.id for value in inputs],
        "input_scripts_hex": [value.script.source.hex() for value in inputs],
        "input_signatures_hex": [value.script.values["signature"].hex() for value in inputs],
        "input_public_keys_hex": [value.script.values["pubkey"].hex() for value in inputs],
        "output_amounts": [value.amount for value in outputs],
        "output_sizes": [value.size for value in outputs],
        "output_scripts_hex": [value.script.source.hex() for value in outputs],
        "output_internal_transfers": [value.is_internal_transfer for value in outputs],
    }


def balancing_side_effects(ledger, accounts):
    return {
        "selection_calls": list(ledger.selection_calls),
        "selection_batches_remaining": len(ledger.selection_batches),
        "change_calls": {account.name: account.change.calls for account in accounts},
        "address_hash_calls": list(ledger.address_hash_calls),
        "key_lookups": list(ledger.key_lookups),
        "release_count": len(ledger.release_snapshots),
        "release_snapshots": list(ledger.release_snapshots),
    }


async def run_balancing_create_case(name, transaction_class, inputs, outputs,
                                    funding_accounts, change_account, sign):
    ledgers = []
    accounts = []
    for account in [*funding_accounts, change_account]:
        if account is not None and account not in accounts:
            accounts.append(account)
        if account is not None and account.ledger not in ledgers:
            ledgers.append(account.ledger)
    primary_ledger = funding_accounts[0].ledger if funding_accounts else (
        change_account.ledger if change_account is not None else None
    )
    try:
        transaction = await transaction_class.create(
            inputs, outputs, funding_accounts, change_account, sign=sign
        )
        return {
            "name": name,
            "ok": True,
            "sign_requested": sign,
            "transaction": balancing_transaction_summary(transaction),
            "error_type": None,
            "error_message": None,
            "side_effects": balancing_side_effects(primary_ledger, accounts),
        }
    except Exception as error:  # pylint: disable=broad-except
        side_effects = (
            balancing_side_effects(primary_ledger, accounts)
            if primary_ledger is not None else {
                "selection_calls": [], "selection_batches_remaining": 0,
                "change_calls": {}, "address_hash_calls": [], "key_lookups": [],
                "release_count": 0, "release_snapshots": [],
            }
        )
        return {
            "name": name,
            "ok": False,
            "sign_requested": sign,
            "transaction": None,
            "error_type": type(error).__name__,
            "error_message": str(error),
            "side_effects": side_effects,
        }


def balancing_parent_output(transaction_class, output_class, amount, pubkey_hash):
    _, output = make_parent_output(
        transaction_class, output_class, amount, pubkey_hash
    )
    return output


async def balancing_success_cases(runtime):
    transaction_class = runtime["Transaction"]
    input_class = runtime["Input"]
    output_class = runtime["Output"]
    results = []

    ledger = BalancingLedger()
    wallet = BalancingWallet("wallet")
    account = BalancingAccount("funding", ledger, wallet)
    provided = balancing_parent_output(
        transaction_class, output_class, 1_600_000, bytes.fromhex("11" * 20)
    )
    results.append(await run_balancing_create_case(
        "provided input and output with change", transaction_class,
        [input_class.spend(provided)],
        [output_class.pay_pubkey_hash(1_500_000, bytes.fromhex("12" * 20))],
        [account], account, False,
    ))

    ledger = BalancingLedger()
    wallet = BalancingWallet("wallet")
    account = BalancingAccount("funding", ledger, wallet)
    ledger.selection_batches = [
        [balancing_parent_output(
            transaction_class, output_class, 1_000_000, bytes.fromhex("21" * 20)
        )],
        [balancing_parent_output(
            transaction_class, output_class, 1_100_000, bytes.fromhex("22" * 20)
        )],
    ]
    results.append(await run_balancing_create_case(
        "deficit selects once then breaks underfunded", transaction_class, [],
        [output_class.pay_pubkey_hash(2_000_000, bytes.fromhex("23" * 20))],
        [account], account, False,
    ))

    for name, parent_amount in (
        ("change exactly dust is omitted", 913_400),
        ("change above dust is added", 913_401),
    ):
        ledger = BalancingLedger()
        wallet = BalancingWallet("wallet")
        account = BalancingAccount("funding", ledger, wallet)
        parent = balancing_parent_output(
            transaction_class, output_class, parent_amount, bytes.fromhex("31" * 20)
        )
        results.append(await run_balancing_create_case(
            name, transaction_class, [input_class.spend(parent)],
            [output_class.pay_pubkey_hash(900_000, bytes.fromhex("32" * 20))],
            [account], account, False,
        ))

    ledger = BalancingLedger()
    wallet = BalancingWallet("wallet")
    account = BalancingAccount("funding", ledger, wallet)
    initial = balancing_parent_output(
        transaction_class, output_class, 11_200, bytes.fromhex("41" * 20)
    )
    ledger.selection_batches = [
        [balancing_parent_output(
            transaction_class, output_class, 8_400, bytes.fromhex("42" * 20)
        )],
        [balancing_parent_output(
            transaction_class, output_class, 15_304, bytes.fromhex("43" * 20)
        )],
    ]
    results.append(await run_balancing_create_case(
        "zero-output retry then change", transaction_class,
        [input_class.spend(initial)], [], [account], account, False,
    ))

    ledger = BalancingLedger()
    wallet = BalancingWallet("wallet")
    account = BalancingAccount("funding", ledger, wallet)
    initial = balancing_parent_output(
        transaction_class, output_class, 11_200, bytes.fromhex("51" * 20)
    )
    ledger.selection_batches = [
        [balancing_parent_output(
            transaction_class, output_class, 13_002, bytes((0x52 + index,)) * 20
        )]
        for index in range(2)
    ]
    results.append(await run_balancing_create_case(
        "five-pass zero-output return", transaction_class,
        [input_class.spend(initial)], [], [account], account, False,
    ))

    ledger = BalancingLedger()
    wallet = BalancingWallet("wallet")
    account = BalancingAccount("funding", ledger, wallet)
    private_key = OraclePrivateKey(8)
    pubkey_hash = oracle_hash160(private_key.public_key.pubkey_bytes)
    ledger.keys[ledger.hash160_to_address(pubkey_hash)] = private_key
    parent = balancing_parent_output(
        transaction_class, output_class, 1_600_000, pubkey_hash
    )
    results.append(await run_balancing_create_case(
        "default signing enabled", transaction_class,
        [input_class.spend(parent)],
        [output_class.pay_pubkey_hash(1_500_000, bytes.fromhex("61" * 20))],
        [account], account, True,
    ))
    return results


async def balancing_failure_cases(runtime):
    transaction_class = runtime["Transaction"]
    input_class = runtime["Input"]
    output_class = runtime["Output"]
    results = []

    ledger = BalancingLedger()
    ledger.fee_per_byte = BalancingFailingFee()
    wallet = BalancingWallet("wallet")
    account = BalancingAccount("funding", ledger, wallet)
    results.append(await run_balancing_create_case(
        "initial fee failure is not released", transaction_class, [],
        [output_class.pay_pubkey_hash(1_000_000, bytes.fromhex("70" * 20))],
        [account], account, False,
    ))

    ledger = BalancingLedger()
    wallet = BalancingWallet("wallet")
    account = BalancingAccount("funding", ledger, wallet)
    results.append(await run_balancing_create_case(
        "insufficient immediately", transaction_class, [],
        [output_class.pay_pubkey_hash(1_000_000, bytes.fromhex("71" * 20))],
        [account], account, False,
    ))

    ledger = BalancingLedger()
    wallet = BalancingWallet("wallet")
    account = BalancingAccount("funding", ledger, wallet)
    initial = balancing_parent_output(
        transaction_class, output_class, 11_200, bytes.fromhex("72" * 20)
    )
    ledger.selection_batches = [[balancing_parent_output(
        transaction_class, output_class, 8_400, bytes.fromhex("73" * 20)
    )], []]
    results.append(await run_balancing_create_case(
        "insufficient after partial selection", transaction_class,
        [input_class.spend(initial)], [],
        [account], account, False,
    ))

    ledger = BalancingLedger()
    wallet = BalancingWallet("wallet")
    account = BalancingAccount("funding", ledger, wallet)
    ledger.selection_batches = [RuntimeError("selection failed")]
    results.append(await run_balancing_create_case(
        "selector failure", transaction_class, [],
        [output_class.pay_pubkey_hash(1_000_000, bytes.fromhex("74" * 20))],
        [account], account, False,
    ))

    ledger = BalancingLedger()
    wallet = BalancingWallet("wallet")
    account = BalancingAccount(
        "funding", ledger, wallet, change_error=RuntimeError("change address failed")
    )
    parent = balancing_parent_output(
        transaction_class, output_class, 1_600_000, bytes.fromhex("75" * 20)
    )
    results.append(await run_balancing_create_case(
        "change address failure", transaction_class, [input_class.spend(parent)],
        [output_class.pay_pubkey_hash(1_500_000, bytes.fromhex("76" * 20))],
        [account], account, False,
    ))

    ledger = BalancingLedger()
    wallet = BalancingWallet("wallet")
    account = BalancingAccount("funding", ledger, wallet)
    private_key = OraclePrivateKey(9)
    pubkey_hash = oracle_hash160(private_key.public_key.pubkey_bytes)
    parent = balancing_parent_output(
        transaction_class, output_class, 1_600_000, pubkey_hash
    )
    results.append(await run_balancing_create_case(
        "signing key failure", transaction_class, [input_class.spend(parent)],
        [output_class.pay_pubkey_hash(1_500_000, bytes.fromhex("77" * 20))],
        [account], account, True,
    ))

    ledger = BalancingLedger()
    wallet = BalancingWallet("wallet")
    account = BalancingAccount("funding", ledger, wallet)
    ledger.selection_batches = [RuntimeError("selection failed before release")]
    ledger.release_error = RuntimeError("release failed")
    results.append(await run_balancing_create_case(
        "release failure masks balancing failure", transaction_class, [],
        [output_class.pay_pubkey_hash(1_000_000, bytes.fromhex("78" * 20))],
        [account], account, False,
    ))
    return results


async def balancing_validation_cases(runtime):
    transaction_class = runtime["Transaction"]
    cases = []

    ledger_one = BalancingLedger("ledger-one")
    ledger_two = BalancingLedger("ledger-two")
    wallet_one = BalancingWallet("wallet-one")
    wallet_two = BalancingWallet("wallet-two")
    one = BalancingAccount("one", ledger_one, wallet_one)
    two_ledger = BalancingAccount("two-ledger", ledger_two, wallet_one)
    two_wallet = BalancingAccount("two-wallet", ledger_one, wallet_two)
    cases.append(await run_balancing_create_case(
        "mixed funding ledgers", transaction_class, [], [],
        [one, two_ledger], one, False,
    ))
    cases.append(await run_balancing_create_case(
        "mixed funding wallets", transaction_class, [], [],
        [one, two_wallet], one, False,
    ))
    cases.append(await run_balancing_create_case(
        "change ledger mismatch", transaction_class, [], [], [one], two_ledger, False,
    ))
    cases.append(await run_balancing_create_case(
        "change wallet mismatch", transaction_class, [], [], [one], two_wallet, False,
    ))
    cases.append(await run_balancing_create_case(
        "no funding accounts", transaction_class, [], [], [], None, False,
    ))
    no_wallet = BalancingAccount("no-wallet", ledger_one, None)
    cases.append(await run_balancing_create_case(
        "no wallet", transaction_class, [], [], [no_wallet], None, False,
    ))
    return cases


def balancing_fee_contract(runtime):
    transaction_class = runtime["Transaction"]
    input_class = runtime["Input"]
    output_class = runtime["Output"]
    output_script = runtime["OutputScript"]
    ledger = BalancingLedger()
    parent = balancing_parent_output(
        transaction_class, output_class, 100_000, bytes.fromhex("81" * 20)
    )
    estimator = parent.get_estimator(ledger)
    ordinary = output_class.pay_pubkey_hash(1, bytes.fromhex("82" * 20))
    change_probe = output_class.pay_pubkey_hash(100_000_000, bytes(32))
    claim = output_class(1, output_script.pay_claim_name_pubkey_hash(
        b"abc", b"", bytes.fromhex("83" * 20)
    ))
    transaction = transaction_class() \
        .add_inputs([input_class.spend(parent)]) \
        .add_outputs([ordinary])
    return {
        "fee_per_byte": ledger.fee_per_byte,
        "fee_per_name_char": ledger.fee_per_name_char,
        "dust": 1_000,
        "input_size": estimator.txi.size,
        "input_fee": estimator.fee,
        "input_effective_amount": estimator.effective_amount,
        "ordinary_output_size": ordinary.size,
        "ordinary_output_fee": ordinary.get_fee(ledger),
        "change_probe_size": change_probe.size,
        "change_probe_fee": change_probe.get_fee(ledger),
        "claim_output_size": claim.size,
        "claim_output_fee": claim.get_fee(ledger),
        "base_size": transaction.base_size,
        "base_fee": transaction.get_base_fee(ledger),
        "effective_input_sum": transaction.get_effective_input_sum(ledger),
        "total_output_sum": transaction.get_total_output_sum(ledger),
    }


def balancing_cases(runtime):
    async def execute():
        return {
            "fee_contract": balancing_fee_contract(runtime),
            "success": await balancing_success_cases(runtime),
            "failures": await balancing_failure_cases(runtime),
            "validation": await balancing_validation_cases(runtime),
        }
    return asyncio.run(execute())


def run(sdk_root):
    commit, version = verify_reference(sdk_root)
    runtime = load_runtime(sdk_root)
    return {
        "reference": {
            "commit": commit,
            "version": version,
            "source_sha256": PINNED_SOURCE_HASHES,
            "signing_source_sha256": PINNED_SIGNING_SOURCE_HASHES,
            "balancing_source_sha256": PINNED_BALANCING_SOURCE_HASHES,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "transaction_deserialize_executed": True,
            "mutable_hash_executed": True,
            "script_parser_executed": True,
            "input_script_parser_executed": True,
            "unsigned_construction_executed": True,
            "transaction_sign_executed": True,
            "deterministic_secp256k1_adapter": True,
            "transaction_create_executed": True,
            "fixture_source": "tests/unit/wallet/test_transaction.py",
        },
        "transactions": transaction_cases(sdk_root, runtime["Transaction"]),
        "scripts": script_cases(runtime["OutputScript"]),
        "input_scripts": input_script_cases(
            sdk_root, runtime["InputScript"], runtime["Transaction"]
        ),
        "unsigned_construction": unsigned_construction_cases(sdk_root, runtime),
        "signing": signing_cases(sdk_root, runtime),
        "balancing": balancing_cases(runtime),
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    arguments = parser.parse_args()
    json.dump(run(arguments.sdk_root.resolve()), sys.stdout, sort_keys=True, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

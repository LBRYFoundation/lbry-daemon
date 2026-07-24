#!/usr/bin/env python3
"""Pinned legacy general-transaction selection and hydration probes.

The probe AST-executes the relevant methods from the pinned Python SDK over a
stdlib SQLite fixture.  Transaction decoding is supplied by a small adapter so
the SDK's native/protobuf dependency graph and every external network remain
unused.  Fixture ``raw`` values are ordinary serialized transactions.
"""

import argparse
import ast
import asyncio
import copy
import hashlib
import json
from pathlib import Path
import sqlite3
import struct
import subprocess
import sys
from types import SimpleNamespace
from typing import Any, Dict, List, Tuple


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/wallet/database.py": "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
}
PINNED_METHOD_HASHES = {
    "Database.get_transaction_count": "dc84e16ec72283863b144c22e405139c31bd0f1c1b1dc5291e9153e91304fdb3",
    "Database.get_transactions": "5c5bff04bc5b5d0a3e3f402d421226ede46c59f9ba481137b9d6de2120efdf2f",
    "Database.get_txos": "54eb3def5f8d9f5bbbb83cef52cbe5ba55735fb3294b8c382fcd33a68a785c01",
    "Database.select_transactions": "e90345f73d9b5cda3444c90c3c316b86fce4433fce86344be43c93f2edad224e",
    "Database.select_txos": "3fbf0b31b8d3917e1b44834c8c1fbca3a0f1ab0155a87bf2e5a6e625271a2bc2",
    "constraints_to_sql": "12bd52e0ff61bb1040401402c6de5cd09d31cde2484212995fd07973eed84925",
    "query": "b7496b9058c2c08487def378800baf08f715615a887fc596fbce694282384b9a",
}
TXO_TYPES = {
    "other": 0,
    "stream": 1,
    "channel": 2,
    "support": 3,
    "purchase": 4,
    "collection": 5,
    "repost": 6,
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


def source_nodes(path):
    source = path.read_text()
    return source, ast.parse(source)


def extracted_method(path, class_name, method_names):
    source, tree = source_nodes(path)
    source_class = next(
        node for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == class_name
    )
    selected = []
    hashes = {}
    for node in source_class.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in method_names:
            selected.append(copy.deepcopy(node))
            hashes[f"{class_name}.{node.name}"] = hashlib.sha256(
                ast.get_source_segment(source, node).encode()
            ).hexdigest()
    return selected, hashes


def extracted_function(path, function_names):
    source, tree = source_nodes(path)
    selected = []
    hashes = {}
    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in function_names:
            selected.append(copy.deepcopy(node))
            hashes[node.name] = hashlib.sha256(
                ast.get_source_segment(source, node).encode()
            ).hexdigest()
    return selected, hashes


class OutputScript:
    def __init__(self, source):
        self.source = bytes(source) if source is not None else b""


class TXRef:
    def __init__(self, txid, height=-2):
        self.id = txid
        self.height = height


class TXRefImmutable:
    @staticmethod
    def from_id(txid, height):
        return TXRef(txid, height)


class TXORef:
    def __init__(self, tx_ref, position, txo=None):
        self.tx_ref = tx_ref
        self.position = position
        self._txo = txo

    @property
    def id(self):
        return f"{self.tx_ref.id}:{self.position}"

    @property
    def txo(self):
        return self._txo


class Input:
    def __init__(self, tx_ref, position):
        self.txo_ref = TXORef(tx_ref, position)


class Output:
    def __init__(
            self, amount, script, tx_ref=None, position=None,
            is_internal_transfer=None, is_spent=None,
            is_my_output=None, is_my_input=None, **_kwargs):
        self.amount = amount
        self.script = script
        self.tx_ref = tx_ref
        self.position = position
        self.is_internal_transfer = is_internal_transfer
        self.is_spent = is_spent
        self.is_my_output = is_my_output
        self.is_my_input = is_my_input
        self.sent_supports = None
        self.sent_tips = None
        self.received_tips = None
        self.channel = None
        self.private_key = None
        self.purchase = None

    @property
    def ref(self):
        return TXORef(self.tx_ref, self.position, self)

    @property
    def id(self):
        return self.ref.id

    @property
    def is_claim(self):
        return False

    @property
    def can_decode_claim(self):
        return False

    @property
    def can_decode_purchase_data(self):
        try:
            return self.purchase_data
        except ValueError:
            return False

    @property
    def purchase_data(self):
        source = self.script.source
        if len(source) < 4 or source[0] != 0x6A:
            raise ValueError("Output does not have purchase data.")
        payload_length = source[1]
        payload = source[2:2 + payload_length]
        if len(payload) < 1 or payload[0] != ord("P"):
            raise ValueError("Output does not have purchase data.")
        if len(payload) == 1:
            claim_hash = b""
        elif len(payload) >= 3 and payload[1] == 0x0A and payload[2] == len(payload) - 3:
            claim_hash = payload[3:]
        else:
            raise ValueError("Output does not have purchase data.")
        return SimpleNamespace(claim_id=claim_hash[::-1].hex())

    @property
    def purchased_claim_id(self):
        if self.purchase is None:
            return None
        return self.purchase.purchase_data.claim_id

    def update_annotations(self, annotated):
        if annotated is None:
            annotated = Output(None, None)
        self.is_internal_transfer = annotated.is_internal_transfer
        self.is_spent = annotated.is_spent
        self.is_my_output = annotated.is_my_output
        self.is_my_input = annotated.is_my_input
        self.sent_supports = annotated.sent_supports
        self.sent_tips = annotated.sent_tips
        self.received_tips = annotated.received_tips
        self.channel = annotated.channel
        self.private_key = annotated.private_key


def read_compact(raw, offset):
    prefix = raw[offset]
    offset += 1
    if prefix < 0xFD:
        return prefix, offset
    sizes = {0xFD: 2, 0xFE: 4, 0xFF: 8}
    size = sizes[prefix]
    return int.from_bytes(raw[offset:offset + size], "little"), offset + size


class Transaction:
    def __init__(
            self, raw, height=-2, position=-1, is_verified=False, **_kwargs):
        self.raw = bytes(raw)
        self.height = height
        self.position = position
        self.is_verified = is_verified
        self.id = hashlib.sha256(hashlib.sha256(self.raw).digest()).digest()[::-1].hex()
        self.ref = TXRef(self.id, height)
        self.inputs = []
        self.outputs = []
        self._deserialize()

    def _deserialize(self):
        raw = self.raw
        offset = 4
        input_count, offset = read_compact(raw, offset)
        for _ in range(input_count):
            previous_hash = raw[offset:offset + 32]
            offset += 32
            previous_index = int.from_bytes(raw[offset:offset + 4], "little")
            offset += 4
            script_length, offset = read_compact(raw, offset)
            offset += script_length + 4
            self.inputs.append(Input(TXRef(previous_hash[::-1].hex(), -1), previous_index))
        output_count, offset = read_compact(raw, offset)
        for output_position in range(output_count):
            amount = int.from_bytes(raw[offset:offset + 8], "little")
            offset += 8
            script_length, offset = read_compact(raw, offset)
            script = raw[offset:offset + script_length]
            offset += script_length
            self.outputs.append(Output(
                amount, OutputScript(script), self.ref, output_position,
            ))


def extract_sdk_slice(sdk_root):
    database_path = sdk_root / "lbry/wallet/database.py"
    top_level, top_hashes = extracted_function(
        database_path, {"constraints_to_sql", "query"},
    )
    database_methods, database_hashes = extracted_method(
        database_path, "Database", {
            "select_transactions", "get_transactions", "get_transaction_count",
            "select_txos", "get_txos",
        },
    )
    hashes = {**top_hashes, **database_hashes}
    if hashes != PINNED_METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {PINNED_METHOD_HASHES}")

    body = list(top_level)
    body.append(ast.ClassDef(
        name="ProbeDatabase", bases=[], keywords=[], body=database_methods,
        decorator_list=[],
    ))
    module = ast.fix_missing_locations(ast.Module(body=body, type_ignores=[]))
    namespace = {
        "Any": Any,
        "Dict": Dict,
        "List": List,
        "Tuple": Tuple,
        "Output": Output,
        "OutputScript": OutputScript,
        "TXRefImmutable": TXRefImmutable,
        "TXO_TYPES": TXO_TYPES,
        "Transaction": Transaction,
    }
    exec(compile(module, str(database_path), "exec"), namespace)
    database_class = namespace["ProbeDatabase"]
    database_class.MAX_QUERY_VARIABLES = 999
    database_class.TXO_NOT_MINE = Output(None, None, is_my_output=False)
    return database_class, hashes


SCHEMA = """
CREATE TABLE tx (
    txid TEXT PRIMARY KEY, raw BLOB, height INTEGER, position INTEGER,
    is_verified BOOLEAN, purchased_claim_id TEXT, day INTEGER
);
CREATE TABLE txo (
    txid TEXT, txoid TEXT PRIMARY KEY, address TEXT, position INTEGER,
    amount INTEGER, script BLOB, is_reserved BOOLEAN DEFAULT 0,
    txo_type INTEGER DEFAULT 0, claim_id TEXT, claim_name TEXT,
    has_source BOOLEAN, channel_id TEXT, reposted_claim_id TEXT
);
CREATE TABLE txi (
    txid TEXT, txoid TEXT PRIMARY KEY, address TEXT, position INTEGER
);
CREATE TABLE account_address (
    account TEXT, address TEXT
);
"""


def compact(value):
    if value < 0xFD:
        return bytes([value])
    if value <= 0xFFFF:
        return b"\xfd" + struct.pack("<H", value)
    if value <= 0xFFFFFFFF:
        return b"\xfe" + struct.pack("<I", value)
    return b"\xff" + struct.pack("<Q", value)


def p2pkh(tag):
    return b"\x76\xa9\x14" + bytes([tag]) * 20 + b"\x88\xac"


def purchase_script(claim_id):
    claim_hash = bytes.fromhex(claim_id)[::-1]
    payload = b"P\x0a" + bytes([len(claim_hash)]) + claim_hash
    return b"\x6a" + bytes([len(payload)]) + payload


def serialize_transaction(previous, outputs, locktime):
    raw = bytearray(struct.pack("<I", 1))
    raw += compact(1)
    if previous is None:
        raw += bytes(32) + struct.pack("<I", 0xFFFFFFFF)
        input_script = struct.pack("<I", locktime)
    else:
        previous_txid, previous_position = previous
        raw += bytes.fromhex(previous_txid)[::-1] + struct.pack("<I", previous_position)
        input_script = b"\x51"
    raw += compact(len(input_script)) + input_script + struct.pack("<I", 0xFFFFFFFF)
    raw += compact(len(outputs))
    for amount, script in outputs:
        raw += struct.pack("<Q", amount) + compact(len(script)) + script
    raw += struct.pack("<I", locktime)
    raw = bytes(raw)
    txid = hashlib.sha256(hashlib.sha256(raw).digest()).digest()[::-1].hex()
    return txid, raw


class SQLiteExecutor:
    def __init__(self):
        self.connection = sqlite3.connect(":memory:")
        self.connection.row_factory = sqlite3.Row
        self.connection.executescript(SCHEMA)
        self.connection.executemany(
            "INSERT INTO account_address (account, address) VALUES (?, ?)",
            [("account-a", "a1"), ("account-a", "a2"), ("account-b", "b1")],
        )
        self.names = {}
        self.transactions = {}
        self.queries = []
        self._populate()

    def add_transaction(
            self, name, previous, outputs, height, position, rows,
            input_address=None, purchased_claim_id=None):
        previous_value = None
        if previous is not None:
            previous_name, previous_position = previous
            previous_value = (self.transactions[previous_name][0], previous_position)
        txid, raw = serialize_transaction(previous_value, outputs, position + 100)
        self.names[txid] = name
        self.transactions[name] = (txid, raw)
        self.connection.execute(
            "INSERT INTO tx VALUES (?, ?, ?, ?, ?, ?, ?)",
            (txid, raw, height, position, height > 0, purchased_claim_id, None),
        )
        if previous_value is not None and input_address is not None:
            self.connection.execute(
                "INSERT INTO txi VALUES (?, ?, ?, ?)",
                (txid, f"{previous_value[0]}:{previous_value[1]}", input_address, 0),
            )
        for output_position, address, output_type in rows:
            amount, _script = outputs[output_position]
            self.connection.execute(
                "INSERT INTO txo VALUES (?, ?, ?, ?, ?, ?, 0, ?, NULL, NULL, 0, NULL, NULL)",
                (
                    txid, f"{txid}:{output_position}", address, output_position,
                    amount + 9000, b"\x51", output_type,
                ),
            )

    def _populate(self):
        self.add_transaction(
            "parent-spent", None, [(111, p2pkh(1))], 8, 1,
            [(0, "a1", TXO_TYPES["other"])],
        )
        self.add_transaction(
            "parent-internal", None, [(222, p2pkh(2))], 7, 2,
            [(0, "a1", TXO_TYPES["other"])],
        )
        self.add_transaction(
            "parent-purchase", None, [(333, p2pkh(3))], 11, 3,
            [(0, "a1", TXO_TYPES["other"])],
        )
        self.add_transaction(
            "outgoing", ("parent-spent", 0), [(101, p2pkh(4))], 0, 9,
            [(0, "z1", TXO_TYPES["other"])], input_address="a1",
        )
        self.add_transaction(
            "internal", ("parent-internal", 0), [(202, p2pkh(5))], 9, 7,
            [(0, "a2", TXO_TYPES["other"])], input_address="a1",
        )
        claim_id = "001122334455"
        self.add_transaction(
            "purchase", ("parent-purchase", 0),
            [(303, p2pkh(6)), (0, purchase_script(claim_id))], -1, 8,
            [(0, "z2", TXO_TYPES["purchase"])], input_address="a1",
            purchased_claim_id=claim_id,
        )
        self.add_transaction(
            "incoming", None, [(404, p2pkh(7))], 12, 4,
            [(0, "a2", TXO_TYPES["other"])],
        )

        missing_txid = "11" * 32
        txid, raw = serialize_transaction((missing_txid, 4), [(505, p2pkh(8))], 105)
        self.names[txid] = "missing-reference"
        self.transactions["missing-reference"] = (txid, raw)
        self.connection.execute(
            "INSERT INTO tx VALUES (?, ?, 5, 5, 1, NULL, NULL)", (txid, raw),
        )
        self.connection.execute(
            "INSERT INTO txo VALUES (?, ?, 'a1', 0, 9505, x'51', 0, 0, NULL, NULL, 0, NULL, NULL)",
            (txid, f"{txid}:0"),
        )

        self.add_transaction(
            "foreign", None, [(606, p2pkh(9))], 20, 1,
            [(0, "b1", TXO_TYPES["other"])],
        )

    async def execute_fetchall(self, sql, parameters=None, read_only=False):
        parameters = parameters or {}
        self.queries.append({
            "sql": " ".join(sql.split()),
            "parameters": dict(parameters) if isinstance(parameters, dict) else list(parameters),
            "read_only": read_only,
        })
        cursor = self.connection.execute(sql, parameters)
        return [dict(row) for row in cursor.fetchall()]


def nullable(value):
    return value if value is not None else None


def serialize_transaction_result(transaction, names):
    inputs = []
    for txi in transaction.inputs:
        resolved = txi.txo_ref.txo
        inputs.append({
            "previous": names.get(txi.txo_ref.tx_ref.id, txi.txo_ref.tx_ref.id),
            "previous_position": txi.txo_ref.position,
            "resolved": resolved is not None,
            "resolved_amount": resolved.amount if resolved is not None else None,
            "resolved_is_my_output": (
                nullable(resolved.is_my_output) if resolved is not None else None
            ),
        })
    outputs = []
    for txo in transaction.outputs:
        outputs.append({
            "position": txo.position,
            "amount": txo.amount,
            "script": txo.script.source.hex(),
            "is_spent": nullable(txo.is_spent),
            "is_my_input": nullable(txo.is_my_input),
            "is_my_output": nullable(txo.is_my_output),
            "is_internal_transfer": nullable(txo.is_internal_transfer),
            "purchase_output": (
                txo.purchase.position if txo.purchase is not None else None
            ),
            "purchased_claim_id": txo.purchased_claim_id,
        })
    return {
        "name": names[transaction.id],
        "txid": transaction.id,
        "height": transaction.height,
        "position": transaction.position,
        "is_verified": bool(transaction.is_verified),
        "inputs": inputs,
        "outputs": outputs,
    }


async def run_cases(database_class):
    executor = SQLiteExecutor()
    database = database_class()
    database.db = executor
    account = SimpleNamespace(public_key=SimpleNamespace(address="account-a"))
    wallet = SimpleNamespace(accounts=[account])
    common = {
        "wallet": wallet,
        "include_is_spent": True,
        "include_is_my_input": True,
        "include_is_my_output": True,
        "accounts": [account],
    }
    default = await database.get_transactions(**common)
    page = await database.get_transactions(**common, offset=2, limit=3)

    foreign_txid = executor.transactions["foreign"][0]
    bypass = await database.get_transactions(
        wallet=wallet, accounts=[account], txid=foreign_txid,
        include_is_spent=True, include_is_my_input=True, include_is_my_output=True,
    )
    ids_bypass = await database.get_transactions(
        wallet=wallet,
        txid__in=[executor.transactions["foreign"][0], executor.transactions["incoming"][0]],
        include_is_my_output=True, order_by="height DESC",
    )
    empty_txid = await database.select_transactions("txid", txid="")
    empty_txids = await database.select_transactions(
        "txid", txid__in=[],
        order_by=["height=0 DESC", "height DESC", "position DESC"],
    )
    count = await database.get_transaction_count(
        accounts=[account], offset=999, limit=0, order_by=object(),
    )
    try:
        await database.get_transactions(wallet=wallet)
    except Exception as error:  # pylint: disable=broad-except
        scope_error = {"type": type(error).__name__, "message": str(error)}
    else:
        scope_error = None

    return {
        "default": [serialize_transaction_result(tx, executor.names) for tx in default],
        "page_offset_2_limit_3": [
            serialize_transaction_result(tx, executor.names) for tx in page
        ],
        "txid_bypasses_account_scope": [
            serialize_transaction_result(tx, executor.names) for tx in bypass
        ],
        "txids_bypass_without_accounts": [
            serialize_transaction_result(tx, executor.names) for tx in ids_bypass
        ],
        "empty_txid_bypasses_scope": [executor.names[row["txid"]] for row in empty_txid],
        "empty_txids_bypass_and_omit_filter": [
            executor.names[row["txid"]] for row in empty_txids
        ],
        "count_with_ignored_pagination_order": count,
        "missing_scope_error": scope_error,
        "sql": executor.queries,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    arguments = parser.parse_args()
    sdk_root = arguments.sdk_root.resolve()
    verify_source(sdk_root)
    database_class, method_hashes = extract_sdk_slice(sdk_root)
    cases = asyncio.run(run_cases(database_class))
    print(json.dumps({
        "reference": {
            "commit": PINNED_COMMIT,
            "version": PINNED_VERSION,
            "source_sha256": PINNED_SOURCE_HASHES,
            "method_sha256": method_hashes,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "extracted_methods_executed": True,
            "stdlib_sqlite_used": True,
            "external_network_used": False,
        },
        **cases,
    }, sort_keys=True))


if __name__ == "__main__":
    main()

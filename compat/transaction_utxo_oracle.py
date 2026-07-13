#!/usr/bin/env python3
"""Pinned legacy public-UTXO, accounting, and reservation probes.

The probe executes AST-extracted methods from the pinned Python SDK against a
stdlib in-memory SQLite database.  It does not import the SDK or use a network.
"""

import argparse
import ast
import asyncio
import copy
import hashlib
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
from types import SimpleNamespace
from typing import Any, Dict, List, Tuple


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/wallet/account.py": "ea2ca30bddf9c0145469e989d9855dbe7be5184943ae7b8ca690eda41eb7db50",
    "lbry/wallet/constants.py": "099e5b3a18a70439b9d7039717f0cb61c096c5936126fe6574a4ccda600a780f",
    "lbry/wallet/database.py": "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
    "lbry/wallet/ledger.py": "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
}
PINNED_METHOD_HASHES = {
    "Account.get_balance": "aa33c0e7e9271ee543bbf20d29afa5de171d8ba6322d0e3a30cb79e48b9fa928",
    "Database._clean_txo_constraints_for_aggregation": "761b46a7540ad147461436682c439c3d8a0f7770b6f147443ca22f539fdf7cc7",
    "Database.get_balance": "7f6712bd3a394a8e59fd12ef61045721d4468785fb70b5a65288da0657cac861",
    "Database.get_txo_count": "0fa4a7ea182a214310f86b5780bd971f91e54de9240948ee2e1bc622a7494f1a",
    "Database.get_txo_sum": "c5fc698e49727203f0111f5714da0f079900da71f1f830f559a0cd0c0aa10b16",
    "Database.get_txos": "54eb3def5f8d9f5bbbb83cef52cbe5ba55735fb3294b8c382fcd33a68a785c01",
    "Database.get_utxo_count": "2b1e7fb0711a7c0914d600852cdc7482a44fbae2b42be9274b90cca6046f0279",
    "Database.get_utxos": "f5b83e1f73fbc6248a5f81a68a1f1944f47770f7d3c1e1b2e07021165d9e3cea",
    "Database.release_all_outputs": "86824a7e41900a648eb0d091a8468890260a0b66b8f13e35f9a6b6b8e6343853",
    "Database.select_txos": "3fbf0b31b8d3917e1b44834c8c1fbca3a0f1ab0155a87bf2e5a6e625271a2bc2",
    "Ledger.constraint_spending_utxos": "76d9fcdcdd7deee75e5f5575ec4c61d18daea9f7122ba7872c32824404a0815e",
    "Ledger.get_utxo_count": "592914e51f83c12fc016e2730f9404eb5b15520ccc7da5929486864353769f4c",
    "Ledger.get_utxos": "4db2164d552430474feddbbf75ca55f19742bd16c5719a50abd9a023148d4757",
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
    tree = ast.parse(source)
    return source, tree


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
        self.source = bytes(source)


class TXRefImmutable:
    @staticmethod
    def from_id(txid, height):
        return SimpleNamespace(id=txid, height=height)


class Output:
    def __init__(self, amount, script, tx_ref, position):
        self.amount = amount
        self.script = script
        self.tx_ref = tx_ref
        self.position = position
        self.received_tips = None


def extract_sdk_slice(sdk_root):
    database_path = sdk_root / "lbry/wallet/database.py"
    top_level, top_hashes = extracted_function(
        database_path, {"constraints_to_sql", "query"},
    )
    database_methods, database_hashes = extracted_method(
        database_path, "Database", {
            "select_txos", "get_txos", "_clean_txo_constraints_for_aggregation",
            "get_txo_count", "get_txo_sum", "get_utxos", "get_utxo_count",
            "get_balance", "release_all_outputs",
        },
    )
    ledger_methods, ledger_hashes = extracted_method(
        sdk_root / "lbry/wallet/ledger.py", "Ledger", {
            "constraint_spending_utxos", "get_utxos", "get_utxo_count",
        },
    )
    account_methods, account_hashes = extracted_method(
        sdk_root / "lbry/wallet/account.py", "Account", {"get_balance"},
    )
    hashes = {**top_hashes, **database_hashes, **ledger_hashes, **account_hashes}
    if hashes != PINNED_METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {PINNED_METHOD_HASHES}")

    body = list(top_level)
    body.extend([
        ast.ClassDef(
            name="ProbeDatabase", bases=[], keywords=[], body=database_methods,
            decorator_list=[],
        ),
        ast.ClassDef(
            name="ProbeLedger", bases=[], keywords=[], body=ledger_methods,
            decorator_list=[],
        ),
        ast.ClassDef(
            name="ProbeAccount", bases=[], keywords=[], body=account_methods,
            decorator_list=[],
        ),
    ])
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
        # The no_tx=True probes never construct these SDK types.
        "Transaction": object,
    }
    exec(compile(module, str(database_path), "exec"), namespace)
    return (
        namespace["ProbeDatabase"], namespace["ProbeLedger"],
        namespace["ProbeAccount"], hashes,
    )


SCHEMA = """
CREATE TABLE tx (
    txid TEXT PRIMARY KEY, raw BLOB, height INTEGER, position INTEGER,
    is_verified BOOLEAN
);
CREATE TABLE txo (
    txid TEXT, txoid TEXT PRIMARY KEY, address TEXT, position INTEGER,
    amount INTEGER, script BLOB, is_reserved BOOLEAN, txo_type INTEGER,
    claim_id TEXT
);
CREATE TABLE txi (
    txid TEXT, txoid TEXT, address TEXT, position INTEGER
);
CREATE TABLE account_address (
    account TEXT, address TEXT
);
"""

TRANSACTIONS = [
    ("u0", 0, 5),
    ("u4", -1, 6),
    ("c12", 12, 4),
    ("c10", 10, 8),
    ("c5", 5, 1),
    ("claim", 11, 2),
    ("reserved-a", 9, 3),
    ("reserved-a-claim", 8, 2),
    ("spent", 7, 1),
    ("b-only", 6, 1),
    ("reserved-b", 4, 1),
    ("reserved-unowned", 3, 1),
]

OUTPUTS = [
    # txid, txoid, address, position, amount, reserved, type
    ("u0", "u0:0", "a1", 0, 100, 0, 0),
    ("u4", "u4:0", "a1", 0, 110, 0, 4),
    ("c12", "c12:0", "a1", 0, 120, 0, 0),
    ("c10", "c10:1", "a1", 1, 130, 0, 0),
    ("c10", "c10:0", "a2", 0, 140, 0, 4),
    ("c5", "c5:0", "a2", 0, 150, 0, 4),
    ("claim", "claim:0", "a1", 0, 200, 0, 1),
    ("reserved-a", "reserved-a:0", "a1", 0, 300, 1, 0),
    ("reserved-a-claim", "reserved-a-claim:0", "a2", 0, 310, 1, 1),
    ("spent", "spent:0", "a1", 0, 400, 0, 4),
    ("b-only", "b-only:0", "b1", 0, 500, 0, 0),
    ("reserved-b", "reserved-b:0", "b1", 0, 510, 1, 4),
    ("reserved-unowned", "reserved-unowned:0", "z1", 0, 520, 1, 0),
]


class SQLiteExecutor:
    def __init__(self):
        self.connection = sqlite3.connect(":memory:")
        self.connection.row_factory = sqlite3.Row
        self.connection.executescript(SCHEMA)
        self.connection.executemany(
            "INSERT INTO account_address (account, address) VALUES (?, ?)",
            [
                ("account-a", "a1"), ("account-a", "a2"),
                ("account-b", "b1"), ("account-c", "a1"),
            ],
        )
        self.connection.executemany(
            "INSERT INTO tx (txid, raw, height, position, is_verified) VALUES (?, ?, ?, ?, ?)",
            [(txid, b"raw", height, position, height > 0)
             for txid, height, position in TRANSACTIONS],
        )
        self.connection.executemany(
            "INSERT INTO txo (txid, txoid, address, position, amount, script, is_reserved, txo_type) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
            [(txid, txoid, address, position, amount, b"script", reserved, txo_type)
             for txid, txoid, address, position, amount, reserved, txo_type in OUTPUTS],
        )
        self.connection.execute(
            "INSERT INTO txi (txid, txoid, address, position) VALUES (?, ?, ?, ?)",
            ("spender", "spent:0", "a1", 0),
        )
        self.queries = []

    async def execute_fetchall(self, sql, parameters=None, read_only=False):
        parameters = parameters or {}
        self.queries.append({
            "sql": " ".join(sql.split()),
            "parameters": dict(parameters) if isinstance(parameters, dict) else list(parameters),
            "read_only": read_only,
        })
        try:
            cursor = self.connection.execute(sql, parameters)
        except sqlite3.Error as err:
            raise RuntimeError(f"SQL failed: {sql!r} with {parameters!r}") from err
        if cursor.description is None:
            return []
        return [dict(row) for row in cursor.fetchall()]

    def reservation_state(self):
        return {
            row["txoid"]: bool(row["is_reserved"])
            for row in self.connection.execute(
                "SELECT txoid, is_reserved FROM txo ORDER BY txoid"
            ).fetchall()
        }


def serialize_output(output):
    return {
        "txid": output.tx_ref.id,
        "txoid": f"{output.tx_ref.id}:{output.position}",
        "height": output.tx_ref.height,
        "position": output.position,
        "amount": output.amount,
    }


async def run_cases(database_class, ledger_class, account_class):
    executor = SQLiteExecutor()
    database = database_class()
    database.db = executor
    ledger = ledger_class()
    ledger.db = database
    ledger.headers = SimpleNamespace(height=12)
    account = account_class()
    account.ledger = ledger
    account.public_key = SimpleNamespace(address="account-a")
    account.wallet = SimpleNamespace(accounts=[account])

    public = await ledger.get_utxos(
        wallet=account.wallet, accounts=[account], no_tx=True,
        no_channel_info=True,
    )
    public_page = await ledger.get_utxos(
        wallet=account.wallet, accounts=[account], no_tx=True,
        no_channel_info=True, limit=3, offset=2,
        # Ledger overwrites this caller-provided type constraint with (0, 4).
        txo_type__in=(1,),
    )
    count = await ledger.get_utxo_count(
        accounts=[account], limit=0, offset=999, order_by="amount DESC",
    )
    total = await database.get_txo_sum(
        accounts=[account], txo_type__in=(0, 4), is_spent=False,
        limit=0, offset=999, order_by="amount DESC",
    )
    generic_count = await database.get_txo_count(
        accounts=[account], txo_type__in=(0, 4), is_spent=False,
        limit=0, offset=999, order_by="amount DESC",
    )
    overlapping_ownership_count = await database.get_txo_count(
        accounts=[
            SimpleNamespace(public_key=SimpleNamespace(address="account-a")),
            SimpleNamespace(public_key=SimpleNamespace(address="account-c")),
        ],
        txo_type__in=(0, 4), is_spent=False,
    )

    balances = []
    for confirmations in (0, 1, 2, 8, 9):
        balances.append({
            "confirmations": confirmations,
            "include_claims": False,
            "amount": await account.get_balance(confirmations=confirmations),
        })
    balances.append({
        "confirmations": 0,
        "include_claims": True,
        "amount": await account.get_balance(confirmations=0, include_claims=True),
    })

    reserved_before = executor.reservation_state()
    await database.release_all_outputs(account)
    reserved_after_account = executor.reservation_state()
    await database.release_all_outputs()
    reserved_after_global = executor.reservation_state()

    executor.connection.executemany(
        "INSERT INTO tx (txid, raw, height, position, is_verified) VALUES (?, ?, ?, ?, ?)",
        [(txid, b"raw", 1, 0, 1) for txid in (
            "tip-target", "tip-a1", "tip-a2", "tip-foreign", "tip-spent",
            "tip-wrong-type", "tip-wrong-claim",
        )],
    )
    executor.connection.executemany(
        "INSERT INTO txo (txid, txoid, address, position, amount, script, "
        "is_reserved, txo_type, claim_id) VALUES (?, ?, ?, 0, ?, ?, 0, ?, ?)",
        [
            ("tip-target", "tip-target:0", "a1", 1, b"target", 1, "wanted"),
            ("tip-a1", "tip-a1:0", "a1", 11, b"support", 3, "wanted"),
            ("tip-a2", "tip-a2:0", "a2", 22, b"support", 3, "wanted"),
            ("tip-foreign", "tip-foreign:0", "b1", 33, b"support", 3, "wanted"),
            ("tip-spent", "tip-spent:0", "a1", 44, b"support", 3, "wanted"),
            ("tip-wrong-type", "tip-wrong-type:0", "a1", 55, b"stream", 1, "wanted"),
            ("tip-wrong-claim", "tip-wrong-claim:0", "a1", 66, b"support", 3, "other"),
        ],
    )
    executor.connection.execute(
        "INSERT INTO txi (txid, txoid, address, position) VALUES (?, ?, ?, ?)",
        ("tip-spender", "tip-spent:0", "a1", 0),
    )
    received_tip_outputs = await database.get_txos(
        wallet=account.wallet, accounts=[account], no_tx=True,
        no_channel_info=True, txid="tip-target", include_received_tips=True,
        include_is_spent=True, include_is_my_input=True, include_is_my_output=True,
    )

    return {
        "public_utxos": [serialize_output(output) for output in public],
        "public_utxos_default_offset_2_limit_3": [
            serialize_output(output) for output in public_page
        ],
        "aggregations": {
            "utxo_count_with_ignored_pagination_order": count,
            "txo_count_with_ignored_pagination_order": generic_count,
            "txo_sum_with_ignored_pagination_order": total,
            "overlapping_account_ownership_count": overlapping_ownership_count,
        },
        "balances": balances,
        "release_all": {
            "before": reserved_before,
            "after_account_a": reserved_after_account,
            "after_global": reserved_after_global,
        },
        "received_tips": received_tip_outputs[0].received_tips,
        "sql": executor.queries,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    arguments = parser.parse_args()
    sdk_root = arguments.sdk_root.resolve()
    verify_source(sdk_root)
    database_class, ledger_class, account_class, method_hashes = extract_sdk_slice(sdk_root)
    cases = asyncio.run(run_cases(database_class, ledger_class, account_class))
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

#!/usr/bin/env python3
"""Pinned offline probes for paginate_rows and transaction_list.

The selected SDK methods are AST-executed against deterministic fake wallets
and SQLite-backed ledgers. No SDK import, hub, or external network is used.
"""

import argparse
import ast
import asyncio
import copy
from functools import partial
import hashlib
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
from typing import Callable, Optional


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/error/__init__.py": "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
    "lbry/extras/daemon/daemon.py": "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
    "lbry/wallet/account.py": "ea2ca30bddf9c0145469e989d9855dbe7be5184943ae7b8ca690eda41eb7db50",
    "lbry/wallet/database.py": "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
    "lbry/wallet/manager.py": "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
    "lbry/wallet/wallet.py": "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
}
PINNED_METHOD_HASHES = {
    "Account.get_transaction_history": "86ad050ddcb07b015ac40f76cdd321d5f148b6673cdc287b6bf5a14dab59222b",
    "Account.get_transaction_history_count": "f9b01e74174ca43a3286ad85406a4c371c7dc61b7805b7b99423fe40cdb3cb77",
    "Database.select_transactions": "e90345f73d9b5cda3444c90c3c316b86fce4433fce86344be43c93f2edad224e",
    "Daemon.jsonrpc_transaction_list": "b12a50a7275c160f383257aabcecefeb57822dd6641d56da143b256fccded213",
    "Daemon.ledger": "c0aad64201976cc6d3b4ae3fa49fe9434093c578706b84f45b8cc687c7276f46",
    "JSONRPCError.__init__": "6694af6fe018ba7f86d734992597aaea26b18d70389263a7ff5fd2be1995144b",
    "JSONRPCError.create_command_exception": "fc56255c3a3e15b5279f3d583d6ee67959109f5f0c4766c0d10928bf12cc659e",
    "JSONRPCError.filter_traceback": "db8da5a9ff8f43e6ce64bdaad60f5e67cc3e071f1992b29d79fa8f2dafa97f86",
    "JSONRPCError.to_dict": "4a92e56be4937d195c7307f337b8fcac7a36b306d945b2dbe29108748882a347",
    "Wallet.default_account": "76e84d5c63726f3c268e161ee2ef54e0573ab02a4aab04d9b7c6dae0fc95961e",
    "Wallet.get_account_or_error": "e5296b46722e7337b8332c93047cf8c7aef042a35dd762c777d2b0150541305c",
    "WalletManager.default_account": "6b5ae4ee1fd368d8b3bb05e3a8a3362a0f958f4e5385787958ff83fdb855e731",
    "WalletManager.default_wallet": "b985d6bbf6126a982f1f0084fc6872592cff7717f50b59fbe3a745f498c8de48",
    "WalletManager.get_wallet_or_default": "a78f3e4003c8bc2c25c95681532cb166eb3685a611aecd6024893fa6c94e8537",
    "WalletManager.get_wallet_or_error": "ac6310a5232801623f12f4be0909a0e64a595a94330465f3c825b9ac34c51eec",
    "WalletNotLoadedError.__init__": "be7802498b2b6c25bef47a189cafe172d0e5b702989cd318957686f1f0ea2d29",
    "paginate_rows": "d5af80505ca81eafd134236e1e5fbc8242e9a0ea97d88716be2ec1e846f8becc",
}

DEFAULT_PAGE_SIZE = 20
ROWS = [f"r{index}" for index in range(5)]


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


def selected_function(path, name):
    source = path.read_text()
    node = next(
        node for node in ast.parse(source).body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name == name
    )
    digest = hashlib.sha256(ast.get_source_segment(source, node).encode()).hexdigest()
    return copy.deepcopy(node), digest


def selected_methods(path, class_name, names):
    source = path.read_text()
    source_class = next(
        node for node in ast.parse(source).body
        if isinstance(node, ast.ClassDef) and node.name == class_name
    )
    methods = []
    hashes = {}
    for node in source_class.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in names:
            methods.append(copy.deepcopy(node))
            hashes[f"{class_name}.{node.name}"] = hashlib.sha256(
                ast.get_source_segment(source, node).encode()
            ).hexdigest()
    return methods, hashes


def extract_sdk_slice(sdk_root):
    daemon_path = sdk_root / "lbry/extras/daemon/daemon.py"
    paginate, paginate_hash = selected_function(daemon_path, "paginate_rows")
    daemon_methods, daemon_hashes = selected_methods(
        daemon_path, "Daemon", {"ledger", "jsonrpc_transaction_list"},
    )
    for method in daemon_methods:
        if method.name == "jsonrpc_transaction_list":
            method.decorator_list = []
    error_methods, error_hashes = selected_methods(
        daemon_path, "JSONRPCError",
        {"__init__", "to_dict", "filter_traceback", "create_command_exception"},
    )
    manager_methods, manager_hashes = selected_methods(
        sdk_root / "lbry/wallet/manager.py", "WalletManager",
        {"default_wallet", "default_account", "get_wallet_or_default", "get_wallet_or_error"},
    )
    wallet_methods, wallet_hashes = selected_methods(
        sdk_root / "lbry/wallet/wallet.py", "Wallet",
        {"default_account", "get_account_or_error"},
    )
    account_methods, account_hashes = selected_methods(
        sdk_root / "lbry/wallet/account.py", "Account",
        {"get_transaction_history", "get_transaction_history_count"},
    )
    database_methods, database_hashes = selected_methods(
        sdk_root / "lbry/wallet/database.py", "Database", {"select_transactions"},
    )
    not_loaded_methods, not_loaded_hashes = selected_methods(
        sdk_root / "lbry/error/__init__.py", "WalletNotLoadedError", {"__init__"},
    )
    hashes = {
        "paginate_rows": paginate_hash,
        **daemon_hashes,
        **error_hashes,
        **manager_hashes,
        **wallet_hashes,
        **account_hashes,
        **database_hashes,
        **not_loaded_hashes,
    }
    if hashes != PINNED_METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {PINNED_METHOD_HASHES}")

    classes = [
        ast.ClassDef("WalletNotLoadedError", [ast.Name("Exception", ast.Load())], [],
                     not_loaded_methods, []),
        ast.ClassDef("Wallet", [], [], wallet_methods, []),
        ast.ClassDef("Account", [], [], account_methods, []),
        ast.ClassDef("Database", [], [], database_methods, []),
        ast.ClassDef("WalletManager", [], [], manager_methods, []),
        ast.ClassDef("JSONRPCError", [], [], error_methods, []),
        ast.ClassDef("Daemon", [], [], daemon_methods, []),
    ]
    module = ast.fix_missing_locations(ast.Module(body=[paginate, *classes], type_ignores=[]))
    namespace = {
        "Callable": Callable,
        "DEFAULT_PAGE_SIZE": DEFAULT_PAGE_SIZE,
        "Optional": Optional,
        "WalletNotLoadedError": Exception,
        "constraints_to_sql": lambda constraints: ("1", {}),
        "partial": partial,
        "query": lambda select, **constraints: (select, constraints),
    }
    exec(compile(module, str(daemon_path), "exec"), namespace)
    namespace["JSONRPCError"].CODE_APPLICATION_ERROR = -32500
    return namespace, hashes


class FixtureDatabaseExecutor:
    async def execute_fetchall(self, *unused_args, **unused_kwargs):
        return []


class FixtureLedger:
    def __init__(self, name, calls, database_class):
        self.name = name
        self.calls = calls
        self.database = database_class()
        self.database.db = FixtureDatabaseExecutor()
        self.sqlite = sqlite3.connect(":memory:")
        self.sqlite.execute("CREATE TABLE rows (position INTEGER, value TEXT)")
        self.sqlite.executemany("INSERT INTO rows VALUES (?, ?)", enumerate(ROWS))

    @staticmethod
    def _identity(constraints):
        wallet = constraints.get("wallet")
        accounts = constraints.get("accounts") or []
        return getattr(wallet, "id", None), [account.id for account in accounts]

    def _rows(self, constraints):
        wallet_id, account_ids = self._identity(constraints)
        if self.name == "ledger-a" and wallet_id == "wallet-a" and "a2" in account_ids:
            return ROWS
        if self.name == "ledger-b" and wallet_id == "wallet-b" and account_ids == ["b2"]:
            return ROWS
        return []

    def _record(self, method, constraints):
        wallet_id, account_ids = self._identity(constraints)
        self.calls.append({
            "method": method,
            "ledger": self.name,
            "wallet": wallet_id,
            "accounts": account_ids,
            "read_only": constraints.get("read_only"),
            "offset": constraints.get("offset"),
            "limit": constraints.get("limit"),
        })

    async def get_transaction_history(self, read_only=False, **constraints):
        constraints["read_only"] = read_only
        self._record("get_transaction_history", constraints)
        await self.database.select_transactions(
            "*", accounts=constraints.get("accounts"), read_only=read_only,
        )
        rows = self._rows(constraints)
        offset, limit = constraints["offset"], constraints["limit"]
        selected = [row[0] for row in self.sqlite.execute(
            f"SELECT value FROM rows ORDER BY position LIMIT {limit} OFFSET {offset}"
        )]
        return selected if rows else []

    async def get_transaction_history_count(self, read_only=False, **constraints):
        constraints["read_only"] = read_only
        self._record("get_transaction_history_count", constraints)
        return len(self._rows(constraints))


def fixture_daemon(namespace, manager_state="default"):
    calls = []
    ledger_a = FixtureLedger("ledger-a", calls, namespace["Database"])
    ledger_b = FixtureLedger("ledger-b", calls, namespace["Database"])
    account_class = namespace["Account"]
    wallet_class = namespace["Wallet"]
    manager_class = namespace["WalletManager"]
    daemon_class = namespace["Daemon"]

    def account(account_id, ledger):
        value = account_class()
        value.id = account_id
        value.ledger = ledger
        value.public_key = type("FixturePublicKey", (), {"address": account_id})()
        return value

    a1, a2 = account("a1", ledger_a), account("a2", ledger_a)
    b1, b2 = account("b1", ledger_b), account("b2", ledger_b)
    wallet_a = wallet_class()
    wallet_a.id, wallet_a.accounts = "wallet-a", [a1, a2]
    wallet_b = wallet_class()
    wallet_b.id, wallet_b.accounts = "wallet-b", [b1, b2]
    for wallet in (wallet_a, wallet_b):
        for value in wallet.accounts:
            value.wallet = wallet
    manager = manager_class()
    manager.wallets = [wallet_a, wallet_b]
    if manager_state == "no_wallets":
        manager.wallets = []
    elif manager_state == "empty_default":
        wallet_a.accounts = []
    elif manager_state == "empty_selected":
        wallet_b.accounts = []
    daemon = daemon_class()
    daemon.wallet_manager = manager
    return daemon, calls


def cases():
    return [
        {"name": "defaults", "omit_params": True},
        {"name": "explicit nulls", "params": {
            "account_id": None, "wallet_id": None, "page": None, "page_size": None,
        }},
        {"name": "middle page", "params": {"account_id": "a2", "page": 2, "page_size": 2}},
        {"name": "tail page", "params": {"account_id": "a2", "page": 3, "page_size": 2}},
        {"name": "beyond page", "params": {"account_id": "a2", "page": 4, "page_size": 2}},
        {"name": "zero defaults", "params": {"account_id": "a2", "page": 0, "page_size": 0}},
        {"name": "negative clamps", "params": {"account_id": "a2", "page": -7, "page_size": -3}},
        {"name": "boolean one", "params": {"account_id": "a2", "page": True, "page_size": True}},
        {"name": "empty strings default", "params": {
            "account_id": "a2", "page": "", "page_size": "",
        }},
        {"name": "empty containers default", "params": {
            "account_id": "a2", "page": [], "page_size": {},
        }},
        {"name": "second default-size page", "params": {"account_id": "a2", "page": 2}},
        {"name": "falsy account selects wallet", "params": {"account_id": False, "page_size": 2}},
        {"name": "selected wallet uses default ledger", "params": {
            "wallet_id": "wallet-b", "page_size": 2,
        }},
        {"name": "empty totals retain page", "params": {
            "wallet_id": "wallet-b", "page": 7, "page_size": 3,
        }},
        {"name": "selected account uses account ledger", "params": {
            "wallet_id": "wallet-b", "account_id": "b2", "page": 2, "page_size": 2,
        }},
        {"name": "empty account selects wallet", "params": {
            "wallet_id": "wallet-b", "account_id": "", "page_size": 2,
        }},
        {"name": "legacy positional", "params": [["a2", None, 2, 2], {}]},
        {"name": "missing wallet", "params": {"wallet_id": "missing"}},
        {"name": "empty wallet", "params": {"wallet_id": ""}},
        {"name": "missing account", "params": {"account_id": "missing"}},
        {"name": "foreign account", "params": {"wallet_id": "wallet-b", "account_id": "a2"}},
        {"name": "string page error", "params": {"account_id": "a2", "page": "2"}},
        {"name": "string page size error", "params": {"account_id": "a2", "page_size": "2"}},
        {"name": "list page error", "params": {"account_id": "a2", "page": [2]}},
        {"name": "legacy positional page error", "params": [["a2", None, "2", 2], {}]},
        {"name": "integral float pagination", "params": {
            "account_id": "a2", "page": 2.0, "page_size": 2.0,
        }},
        {"name": "fractional page integral offset", "params": {
            "account_id": "a2", "page": 1.5, "page_size": 2,
        }},
        {"name": "no wallets wallet wide", "manager_state": "no_wallets", "omit_params": True},
        {"name": "no wallets account", "manager_state": "no_wallets", "params": {
            "account_id": "missing", "page": "2",
        }},
        {"name": "empty default precedes pagination", "manager_state": "empty_default", "params": {
            "page": "2",
        }},
        {"name": "empty default account error", "manager_state": "empty_default", "params": {
            "account_id": "missing", "page": "2",
        }},
        {"name": "empty selected assertion", "manager_state": "empty_selected", "params": {
            "wallet_id": "wallet-b",
        }},
        {"name": "empty selected pagination error", "manager_state": "empty_selected", "params": {
            "wallet_id": "wallet-b", "page": "2",
        }},
        {"name": "object wallet id", "params": {"wallet_id": {"x": 1}}},
        {"name": "object account id", "params": {"account_id": {"x": 1}}},
        {"name": "empty object account selects wallet", "params": {
            "account_id": {}, "page_size": 2,
        }},
        {"name": "missing wallet precedes invalid page", "params": {
            "wallet_id": "missing", "page": "2",
        }},
        {"name": "missing account precedes invalid page", "params": {
            "account_id": "missing", "page": "2",
        }},
        {"name": "fractional offset sqlite error", "params": {
            "account_id": "a2", "page": 1.5, "page_size": 3,
        }},
        {"name": "fractional size sqlite error", "params": {
            "account_id": "a2", "page": 2, "page_size": 1.5,
        }},
        {"name": "largest sqlite offset", "params": {
            "account_id": "a2", "page": 9223372036854775808, "page_size": 1,
        }},
        {"name": "oversized sqlite offset", "params": {
            "account_id": "a2", "page": 9223372036854775809, "page_size": 1,
        }},
        {"name": "largest sqlite page size", "params": {
            "account_id": "a2", "page": 1, "page_size": 9223372036854775807,
        }},
        {"name": "oversized sqlite page size", "params": {
            "account_id": "a2", "page": 1, "page_size": 9223372036854775808,
        }},
        {"name": "mixed huge float overflow", "params": {
            "account_id": "a2", "page": 10 ** 400, "page_size": 2.0,
        }},
        {"name": "mixed fractional overflow precedence", "params": {
            "account_id": "a2", "page": 10 ** 400, "page_size": 1.5,
        }},
        {"name": "mixed subtraction before float conversion", "params": {
            "account_id": "a2", "page": 9007199254740993, "page_size": 2.0,
        }},
    ]


async def execute_case(namespace, fixture):
    daemon, calls = fixture_daemon(namespace, fixture.get("manager_state", "default"))
    params = fixture.get("params", {})
    if isinstance(params, dict):
        args, kwargs = [], params
    else:
        args, kwargs = params
    try:
        result = daemon.jsonrpc_transaction_list(*args, **kwargs)
        if asyncio.iscoroutine(result):
            result = await result
        return {**fixture, "result": result, "calls": calls}
    except Exception as error:  # pylint: disable=broad-except
        rpc_error = namespace["JSONRPCError"].create_command_exception(
            "transaction_list", args, dict(kwargs), error, "oracle traceback",
        ).to_dict()
        return {**fixture, "error": rpc_error, "calls": calls}


async def run_cases(namespace):
    return [await execute_case(namespace, fixture) for fixture in cases()]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    arguments = parser.parse_args()
    sdk_root = arguments.sdk_root.resolve()
    verify_source(sdk_root)
    namespace, method_hashes = extract_sdk_slice(sdk_root)
    results = asyncio.run(run_cases(namespace))
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
            "fixture_rows": len(ROWS),
            "case_count": len(results),
        },
        "cases": results,
    }, sort_keys=True))


if __name__ == "__main__":
    main()

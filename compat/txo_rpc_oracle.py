#!/usr/bin/env python3
"""Pinned offline probes for public TXO list/sum RPC behavior.

The selected SDK methods are AST-executed against deterministic fake wallets
and ledgers.  The probe imports no SDK modules and uses no external network.
"""

import argparse
import ast
import asyncio
import copy
from functools import partial
import hashlib
import json
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace
from typing import Callable, Optional


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/error/__init__.py": "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
    "lbry/extras/daemon/daemon.py": "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
    "lbry/wallet/account.py": "ea2ca30bddf9c0145469e989d9855dbe7be5184943ae7b8ca690eda41eb7db50",
    "lbry/wallet/constants.py": "099e5b3a18a70439b9d7039717f0cb61c096c5936126fe6574a4ccda600a780f",
    "lbry/wallet/database.py": "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
    "lbry/wallet/manager.py": "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
    "lbry/wallet/wallet.py": "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
}
PINNED_METHOD_HASHES = {
    "Account.get_txo_count": "b8c777541f32104a03c4238b31872f9ff9649d7118b8dea8a074d8c6c59b75a6",
    "Account.get_txos": "4a5e44989f39294eaadc4429093d71033ee6315fb7cea86d0cc386978090901f",
    "Daemon._constrain_txo_from_kwargs": "06967894afd91192eec4a0c8853457894fb3d2e6f1de4a15ffce131e281d9f3b",
    "Daemon.jsonrpc_txo_list": "907389b3558247e0ea65cdf75d56ef4bc3c13261a154f79b5dfbfaab348bc0b5",
    "Daemon.jsonrpc_txo_sum": "d9e8f81e9814fe6d51155d04f9ff06b3cf50772910e97424e75d4463e712da5b",
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
    "constrain_single_or_list": "42bbf049d51cf87b94a4b95367f8fb2415e9b97422e9886ea03e3079de97abba",
    "paginate_rows": "d5af80505ca81eafd134236e1e5fbc8242e9a0ea97d88716be2ec1e846f8becc",
}
TXO_TYPES = {
    "other": 0, "stream": 1, "channel": 2, "support": 3,
    "purchase": 4, "collection": 5, "repost": 6,
}
DEFAULT_PAGE_SIZE = 20
ROWS = [f"row-{index}" for index in range(5)]


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
    methods, hashes = [], {}
    for node in source_class.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in names:
            methods.append(copy.deepcopy(node))
            hashes[f"{class_name}.{node.name}"] = hashlib.sha256(
                ast.get_source_segment(source, node).encode()
            ).hexdigest()
    return methods, hashes


def public_txo_methods(path):
    tree = ast.parse(path.read_text())
    daemon = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "Daemon")
    return sorted(
        node.name.removeprefix("jsonrpc_") for node in daemon.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name.startswith("jsonrpc_txo_")
    )


def extract_sdk_slice(sdk_root):
    daemon_path = sdk_root / "lbry/extras/daemon/daemon.py"
    paginate, paginate_hash = selected_function(daemon_path, "paginate_rows")
    constrain, constrain_hash = selected_function(
        sdk_root / "lbry/wallet/database.py", "constrain_single_or_list",
    )
    daemon_methods, daemon_hashes = selected_methods(
        daemon_path, "Daemon",
        {"ledger", "_constrain_txo_from_kwargs", "jsonrpc_txo_list", "jsonrpc_txo_sum"},
    )
    for method in daemon_methods:
        if method.name.startswith("jsonrpc_"):
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
        sdk_root / "lbry/wallet/account.py", "Account", {"get_txos", "get_txo_count"},
    )
    not_loaded_methods, not_loaded_hashes = selected_methods(
        sdk_root / "lbry/error/__init__.py", "WalletNotLoadedError", {"__init__"},
    )
    hashes = {
        "paginate_rows": paginate_hash,
        "constrain_single_or_list": constrain_hash,
        **daemon_hashes, **error_hashes, **manager_hashes, **wallet_hashes,
        **account_hashes, **not_loaded_hashes,
    }
    if hashes != PINNED_METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {PINNED_METHOD_HASHES}")

    classes = [
        ast.ClassDef("WalletNotLoadedError", [ast.Name("Exception", ast.Load())], [], not_loaded_methods, []),
        ast.ClassDef("Wallet", [], [], wallet_methods, []),
        ast.ClassDef("Account", [], [], account_methods, []),
        ast.ClassDef("WalletManager", [], [], manager_methods, []),
        ast.ClassDef("JSONRPCError", [], [], error_methods, []),
        ast.ClassDef("Daemon", [], [], daemon_methods, []),
    ]
    module = ast.fix_missing_locations(ast.Module(body=[paginate, constrain, *classes], type_ignores=[]))
    namespace = {
        "Callable": Callable, "Optional": Optional, "DEFAULT_PAGE_SIZE": DEFAULT_PAGE_SIZE,
        "TXO_TYPES": TXO_TYPES, "partial": partial,
    }
    exec(compile(module, str(daemon_path), "exec"), namespace)
    namespace["JSONRPCError"].CODE_APPLICATION_ERROR = -32500
    namespace["database"] = SimpleNamespace(
        constrain_single_or_list=namespace["constrain_single_or_list"],
    )
    return namespace, hashes, public_txo_methods(daemon_path)


def normalize(value):
    if isinstance(value, dict):
        return {key: normalize(item) for key, item in value.items()}
    if isinstance(value, (list, tuple)):
        return [normalize(item) for item in value]
    if hasattr(value, "id"):
        return value.id
    return value


class FixtureLedger:
    def __init__(self, name, calls, state):
        self.name, self.calls, self.state = name, calls, state

    def _record(self, method, constraints):
        self.calls.append({
            "method": method,
            "ledger": self.name,
            "constraints": normalize(constraints),
        })

    @staticmethod
    def _accounts(constraints):
        return constraints.get("accounts") or []

    async def get_txos(self, **constraints):
        self._record("get_txos", constraints)
        if self.state == "records_error":
            raise RuntimeError("fixture records failure")
        rows = ROWS if self._accounts(constraints) else []
        offset, limit = constraints.get("offset", 0), constraints.get("limit", len(rows))
        return rows[offset:offset + limit]

    async def get_txo_count(self, **constraints):
        self._record("get_txo_count", constraints)
        if self.state == "count_error":
            raise RuntimeError("fixture count failure")
        return len(ROWS) if self._accounts(constraints) else 0

    async def get_txo_sum(self, **constraints):
        self._record("get_txo_sum", constraints)
        amounts = {"a1": 10, "a2": 20, "b1": 30, "b2": 40}
        return sum(amounts.get(account.id, 0) for account in self._accounts(constraints))


def fixture_daemon(namespace, manager_state="default", ledger_state="default"):
    calls = []
    ledger_a = FixtureLedger("ledger-a", calls, ledger_state)
    ledger_b = FixtureLedger("ledger-b", calls, ledger_state)

    def account(account_id, ledger):
        value = namespace["Account"]()
        value.id, value.ledger = account_id, ledger
        return value

    a1, a2 = account("a1", ledger_a), account("a2", ledger_a)
    b1, b2 = account("b1", ledger_b), account("b2", ledger_b)
    wallet_a, wallet_b = namespace["Wallet"](), namespace["Wallet"]()
    wallet_a.id, wallet_a.accounts = "wallet-a", [a1, a2]
    wallet_b.id, wallet_b.accounts = "wallet-b", [b1, b2]
    for wallet in (wallet_a, wallet_b):
        for value in wallet.accounts:
            value.wallet = wallet
    manager = namespace["WalletManager"]()
    manager.wallets = [wallet_a, wallet_b]
    if manager_state == "no_wallets":
        manager.wallets = []
    elif manager_state == "empty_default":
        wallet_a.accounts = []
    elif manager_state == "empty_selected":
        wallet_b.accounts = []
    daemon = namespace["Daemon"]()
    daemon.wallet_manager = manager
    return daemon, calls


def cases():
    return [
        {"name": "list defaults", "method": "txo_list", "omit_params": True},
        {"name": "list explicit nulls", "method": "txo_list", "params": {
            "account_id": None, "wallet_id": None, "page": None, "page_size": None,
            "resolve": False, "order_by": None, "no_totals": False,
            "include_received_tips": False,
        }},
        {"name": "list middle page", "method": "txo_list", "params": {
            "page": 2, "page_size": 2,
        }},
        {"name": "list zero pagination defaults", "method": "txo_list", "params": {
            "page": 0, "page_size": 0,
        }},
        {"name": "list negative pagination clamps", "method": "txo_list", "params": {
            "page": -4, "page_size": -2,
        }},
        {"name": "list string page error", "method": "txo_list", "params": {"page": "2"}},
        {"name": "list no totals", "method": "txo_list", "params": {
            "page": 2, "page_size": 2, "no_totals": True,
        }},
        {"name": "list truthy no totals", "method": "txo_list", "params": {
            "page_size": 2, "no_totals": "yes",
        }},
        {"name": "list resolve and received tips", "method": "txo_list", "params": {
            "resolve": True, "include_received_tips": True,
        }},
        {"name": "list selected account", "method": "txo_list", "params": {"account_id": "a2"}},
        {"name": "list selected wallet", "method": "txo_list", "params": {"wallet_id": "wallet-b"}},
        {"name": "list selected wallet account", "method": "txo_list", "params": {
            "wallet_id": "wallet-b", "account_id": "b2",
        }},
        {"name": "list selected empty wallet", "method": "txo_list", "manager_state": "empty_selected",
         "params": {"wallet_id": "wallet-b", "page": 7, "page_size": 3}},
        {"name": "list falsy account is wallet wide", "method": "txo_list", "params": {
            "account_id": "", "page_size": 2,
        }},
        {"name": "list legacy positional", "method": "txo_list", "params": [
            ["a2", None, 2, 2, True, "height", True, True], {"type": "stream"},
        ]},
        {"name": "list order name", "method": "txo_list", "params": {"order_by": "name"}},
        {"name": "list order height", "method": "txo_list", "params": {"order_by": "height"}},
        {"name": "list order amount", "method": "txo_list", "params": {"order_by": "amount"}},
        {"name": "list order none", "method": "txo_list", "params": {"order_by": "none"}},
        {"name": "list invalid order", "method": "txo_list", "params": {"order_by": "txid"}},
        {"name": "list scalar filters and precedence", "method": "txo_list", "params": {
            "type": "stream", "txid": "tx", "claim_id": "claim", "channel_id": "channel",
            "not_channel_id": "blocked", "name": "name", "reposted_claim_id": "repost",
            "is_spent": True, "is_not_spent": True, "has_source": True, "has_no_source": True,
            "exclude_internal_transfers": True,
        }},
        {"name": "list negative filters", "method": "txo_list", "params": {
            "is_not_spent": True, "has_no_source": True, "is_not_my_input": True,
            "is_not_my_output": True,
        }},
        {"name": "list ownership union precedence", "method": "txo_list", "params": {
            "is_my_input_or_output": True, "is_my_input": True, "is_not_my_input": True,
            "is_my_output": True, "is_not_my_output": True,
        }},
        {"name": "list ownership booleans require true singleton", "method": "txo_list", "params": {
            "is_my_input_or_output": 1, "is_my_input": 1, "is_not_my_input": 1,
            "is_my_output": 1, "is_not_my_output": 1,
        }},
        {"name": "list one element filters collapse", "method": "txo_list", "params": {
            "type": ["channel"], "txid": ["tx"], "claim_id": ["claim"],
            "channel_id": ["channel"], "not_channel_id": ["blocked"], "name": ["name"],
            "reposted_claim_id": ["repost"],
        }},
        {"name": "list multiple filters use in", "method": "txo_list", "params": {
            "type": ["stream", "support"], "txid": ["tx1", "tx2"],
            "claim_id": ["claim1", "claim2"], "channel_id": ["channel1", "channel2"],
            "not_channel_id": ["blocked1", "blocked2"], "name": ["name1", "name2"],
            "reposted_claim_id": ["repost1", "repost2"],
        }},
        {"name": "list empty filters ignored", "method": "txo_list", "params": {
            "type": [], "txid": [], "claim_id": [], "channel_id": [],
            "not_channel_id": [], "name": [], "reposted_claim_id": [],
        }},
        {"name": "list invalid type", "method": "txo_list", "params": {"type": "video"}},
        {"name": "list unknown filter", "method": "txo_list", "params": {"height": 10}},
        {"name": "list missing wallet", "method": "txo_list", "params": {"wallet_id": "missing"}},
        {"name": "list missing account", "method": "txo_list", "params": {"account_id": "missing"}},
        {"name": "list foreign account", "method": "txo_list", "params": {
            "wallet_id": "wallet-b", "account_id": "a2",
        }},
        {"name": "list wallet error precedes order", "method": "txo_list", "params": {
            "wallet_id": "missing", "order_by": "txid",
        }},
        {"name": "list account error precedes type", "method": "txo_list", "params": {
            "account_id": "missing", "type": "video",
        }},
        {"name": "list no wallets", "method": "txo_list", "manager_state": "no_wallets"},
        {"name": "list empty default wallet", "method": "txo_list", "manager_state": "empty_default"},
        {"name": "list records error stops count", "method": "txo_list", "ledger_state": "records_error"},
        {"name": "list count error follows records", "method": "txo_list", "ledger_state": "count_error"},
        {"name": "sum defaults", "method": "txo_sum", "omit_params": True},
        {"name": "sum selected account", "method": "txo_sum", "params": {"account_id": "a2"}},
        {"name": "sum selected wallet", "method": "txo_sum", "params": {"wallet_id": "wallet-b"}},
        {"name": "sum selected wallet account", "method": "txo_sum", "params": {
            "wallet_id": "wallet-b", "account_id": "b2",
        }},
        {"name": "sum legacy positional", "method": "txo_sum", "params": [
            ["b2", "wallet-b"], {"type": "purchase", "is_not_spent": True},
        ]},
        {"name": "sum filters", "method": "txo_sum", "params": {
            "type": ["stream", "channel"], "txid": ["tx1", "tx2"], "claim_id": "claim",
            "channel_id": "channel", "not_channel_id": ["blocked1", "blocked2"],
            "name": "name", "reposted_claim_id": "repost", "is_not_spent": True,
            "has_no_source": True, "is_my_input": True, "is_not_my_output": True,
            "exclude_internal_transfers": True,
        }},
        {"name": "sum invalid type", "method": "txo_sum", "params": {"type": "video"}},
        {"name": "sum unknown filter", "method": "txo_sum", "params": {"resolve": True}},
        {"name": "sum missing wallet", "method": "txo_sum", "params": {"wallet_id": "missing"}},
        {"name": "sum missing account", "method": "txo_sum", "params": {"account_id": "missing"}},
        {"name": "sum foreign account", "method": "txo_sum", "params": {
            "wallet_id": "wallet-b", "account_id": "a2",
        }},
        {"name": "sum no wallets", "method": "txo_sum", "manager_state": "no_wallets"},
    ]


async def execute_case(namespace, fixture):
    daemon, calls = fixture_daemon(
        namespace, fixture.get("manager_state", "default"), fixture.get("ledger_state", "default"),
    )
    params = fixture.get("params", {})
    args, kwargs = ([], params) if isinstance(params, dict) else params
    try:
        result = getattr(daemon, f"jsonrpc_{fixture['method']}")(*args, **kwargs)
        if asyncio.iscoroutine(result):
            result = await result
        return {**fixture, "result": result, "calls": calls}
    except Exception as error:  # pylint: disable=broad-except
        rpc_error = namespace["JSONRPCError"].create_command_exception(
            fixture["method"], args, dict(kwargs), error, "oracle traceback",
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
    namespace, method_hashes, public_methods = extract_sdk_slice(sdk_root)
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
            "external_network_used": False,
            "case_count": len(results),
            "public_txo_methods": public_methods,
            "has_public_txo_count": "txo_count" in public_methods,
        },
        "cases": results,
    }, sort_keys=True))


if __name__ == "__main__":
    main()

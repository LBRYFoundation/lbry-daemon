#!/usr/bin/env python3
"""Pinned offline probes for claim-family list RPC behavior.

The selected SDK methods are AST-executed against deterministic fake wallets
and ledgers. The probe imports no SDK modules and uses no external network.
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
from types import MethodType


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/error/__init__.py": "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
    "lbry/extras/daemon/daemon.py": "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
    "lbry/wallet/account.py": "ea2ca30bddf9c0145469e989d9855dbe7be5184943ae7b8ca690eda41eb7db50",
    "lbry/wallet/constants.py": "099e5b3a18a70439b9d7039717f0cb61c096c5936126fe6574a4ccda600a780f",
    "lbry/wallet/manager.py": "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
    "lbry/wallet/wallet.py": "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
}
PINNED_METHOD_HASHES = {
    "Account.get_collection_count": "c97e9844ce97192ac53fcd57225e49a13528eb2932842fcb0104728c2c86655b",
    "Account.get_collections": "ea06f6578fb9292136c1b448c823430a8eb4a18a196faf3ad09c039760f74302",
    "Daemon.jsonrpc_channel_list": "f3a69dfbbd0876d2350dbfe62d56139d0b8a57d65483c57c894ddbce3658f857",
    "Daemon.jsonrpc_claim_list": "51c3ea59814eebd26eeccab67ac1fa9906260c593971de00247dbd8d231f074c",
    "Daemon.jsonrpc_collection_list": "e8448728400a8f4410bdc203d793578fa7880f82b4917e0e8669a651f18bbaed",
    "Daemon.jsonrpc_purchase_list": "3c1bebbee525dce586ce9461f089c4c02dcc44b9afc509dcada787d71726c670",
    "Daemon.jsonrpc_stream_list": "88b0a70621d9c0697d9098d512e4ac90fa09e042c6481aa2a829811dfd3537a0",
    "Daemon.jsonrpc_support_list": "2de0a07d0aa7fb099a58ecdfc6492a7e2b6edf47dd024253cbef00bfdb2563fb",
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
CLAIM_TYPE_NAMES = ["stream", "channel", "collection", "repost"]
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


def public_list_methods(path):
    tree = ast.parse(path.read_text())
    daemon = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "Daemon")
    selected = {
        "claim_list", "channel_list", "stream_list", "support_list",
        "collection_list", "purchase_list",
    }
    return sorted(
        node.name.removeprefix("jsonrpc_") for node in daemon.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name.removeprefix("jsonrpc_") in selected
    )


def extract_sdk_slice(sdk_root):
    daemon_path = sdk_root / "lbry/extras/daemon/daemon.py"
    paginate, paginate_hash = selected_function(daemon_path, "paginate_rows")
    daemon_methods, daemon_hashes = selected_methods(
        daemon_path, "Daemon", {
            "ledger", "jsonrpc_claim_list", "jsonrpc_channel_list",
            "jsonrpc_stream_list", "jsonrpc_support_list",
            "jsonrpc_collection_list", "jsonrpc_purchase_list",
        },
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
        sdk_root / "lbry/wallet/account.py", "Account",
        {"get_collections", "get_collection_count"},
    )
    not_loaded_methods, not_loaded_hashes = selected_methods(
        sdk_root / "lbry/error/__init__.py", "WalletNotLoadedError", {"__init__"},
    )
    hashes = {
        "paginate_rows": paginate_hash,
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
    module = ast.fix_missing_locations(ast.Module(body=[paginate, *classes], type_ignores=[]))
    namespace = {
        "DEFAULT_PAGE_SIZE": DEFAULT_PAGE_SIZE,
        "CLAIM_TYPE_NAMES": CLAIM_TYPE_NAMES,
        "partial": partial,
    }
    exec(compile(module, str(daemon_path), "exec"), namespace)
    namespace["JSONRPCError"].CODE_APPLICATION_ERROR = -32500
    return namespace, hashes, public_list_methods(daemon_path)


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

    async def _records(self, method, constraints):
        self._record(method, constraints)
        if self.state == "records_error":
            raise RuntimeError("fixture records failure")
        rows = ROWS if self._accounts(constraints) else []
        offset, limit = constraints.get("offset", 0), constraints.get("limit", len(rows))
        return rows[offset:offset + limit]

    def _count(self, method, constraints):
        self._record(method, constraints)
        if self.state == "count_error":
            raise RuntimeError("fixture count failure")
        return len(ROWS) if self._accounts(constraints) else 0

    async def get_purchases(self, **constraints):
        return await self._records("get_purchases", constraints)

    async def get_purchase_count(self, **constraints):
        return self._count("get_purchase_count", constraints)

    async def get_collections(self, **constraints):
        return await self._records("get_collections", constraints)

    async def get_collection_count(self, **constraints):
        return self._count("get_collection_count", constraints)


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

    def delegated_txo_list(
            _self, account_id=None, wallet_id=None, page=None, page_size=None,
            resolve=False, order_by=None, no_totals=False,
            include_received_tips=False, **kwargs):
        call = {
            "method": "txo_list",
            "params": normalize({
                "account_id": account_id, "wallet_id": wallet_id,
                "page": page, "page_size": page_size, "resolve": resolve,
                "order_by": order_by, "no_totals": no_totals,
                "include_received_tips": include_received_tips, **kwargs,
            }),
        }
        calls.append(call)
        return {"delegated": call["params"]}

    daemon.jsonrpc_txo_list = MethodType(delegated_txo_list, daemon)
    return daemon, calls


def cases():
    return [
        {"name": "claim defaults", "method": "claim_list", "omit_params": True},
        {"name": "claim scalar type", "method": "claim_list", "params": {"claim_type": "stream"}},
        {"name": "claim type list", "method": "claim_list", "params": {"claim_type": ["channel", "repost"]}},
        {"name": "claim empty type defaults", "method": "claim_list", "params": {"claim_type": []}},
        {"name": "claim spent false", "method": "claim_list", "params": {"is_spent": False}},
        {"name": "claim spent true", "method": "claim_list", "params": {"is_spent": True}},
        {"name": "claim overwrites unspent", "method": "claim_list", "params": {"is_not_spent": False}},
        {"name": "claim forwards txo controls", "method": "claim_list", "params": {
            "account_id": "a2", "wallet_id": "wallet-a", "page": 3, "page_size": 4,
            "resolve": True, "order_by": "height", "no_totals": True,
            "include_received_tips": True, "name": ["one", "two"],
        }},
        {"name": "channel defaults", "method": "channel_list", "omit_params": True},
        {"name": "channel positional account", "method": "channel_list", "params": [["a2"], {}]},
        {"name": "channel spent false", "method": "channel_list", "params": {"is_spent": False}},
        {"name": "channel spent true", "method": "channel_list", "params": {"is_spent": True}},
        {"name": "channel retains explicit unspent", "method": "channel_list", "params": {
            "is_spent": True, "is_not_spent": True,
        }},
        {"name": "channel overwrites unspent", "method": "channel_list", "params": {
            "is_not_spent": False,
        }},
        {"name": "stream defaults", "method": "stream_list", "omit_params": True},
        {"name": "stream positional account", "method": "stream_list", "params": [["a2"], {}]},
        {"name": "stream spent false", "method": "stream_list", "params": {"is_spent": False}},
        {"name": "stream spent true", "method": "stream_list", "params": {"is_spent": True}},
        {"name": "stream overwrites unspent without spent key", "method": "stream_list", "params": {
            "is_not_spent": False,
        }},
        {"name": "support defaults", "method": "support_list", "omit_params": True},
        {"name": "support received", "method": "support_list", "params": {"received": True}},
        {"name": "support sent", "method": "support_list", "params": {"sent": True}},
        {"name": "support sent removes spent filters", "method": "support_list", "params": {
            "sent": True, "is_spent": True, "is_not_spent": True,
        }},
        {"name": "support staked", "method": "support_list", "params": {"staked": True}},
        {"name": "support flag precedence", "method": "support_list", "params": {
            "received": True, "sent": True, "staked": True,
        }},
        {"name": "support false flags", "method": "support_list", "params": {
            "received": False, "sent": False, "staked": False,
        }},
        {"name": "support truthy numeric received", "method": "support_list", "params": {
            "received": 1, "sent": 1,
        }},
        {"name": "support positional account", "method": "support_list", "params": [["a2"], {"staked": True}]},
        {"name": "purchase defaults", "method": "purchase_list", "omit_params": True},
        {"name": "purchase claim resolve page", "method": "purchase_list", "params": {
            "claim_id": "claim", "resolve": True, "page": 2, "page_size": 2,
        }},
        {"name": "purchase selected wallet", "method": "purchase_list", "params": {
            "wallet_id": "wallet-b",
        }},
        {"name": "purchase selected account", "method": "purchase_list", "params": {
            "wallet_id": "wallet-b", "account_id": "b2",
        }},
        {"name": "purchase falsey ids", "method": "purchase_list", "params": {
            "claim_id": "", "account_id": "",
        }},
        {"name": "purchase negative pagination", "method": "purchase_list", "params": {
            "page": -3, "page_size": -2,
        }},
        {"name": "purchase missing wallet", "method": "purchase_list", "params": {
            "wallet_id": "missing",
        }},
        {"name": "purchase missing account", "method": "purchase_list", "params": {
            "account_id": "missing",
        }},
        {"name": "purchase foreign account", "method": "purchase_list", "params": {
            "wallet_id": "wallet-b", "account_id": "a2",
        }},
        {"name": "purchase no wallets", "method": "purchase_list", "manager_state": "no_wallets"},
        {"name": "purchase empty default", "method": "purchase_list", "manager_state": "empty_default"},
        {"name": "purchase records error", "method": "purchase_list", "ledger_state": "records_error"},
        {"name": "purchase count error", "method": "purchase_list", "ledger_state": "count_error"},
        {"name": "collection defaults", "method": "collection_list", "omit_params": True},
        {"name": "collection resolve page", "method": "collection_list", "params": {
            "resolve_claims": 7, "resolve": True, "wallet_id": "wallet-b",
            "page": 2, "page_size": 2,
        }},
        {"name": "collection selected account", "method": "collection_list", "params": {
            "resolve_claims": 3, "resolve": True, "wallet_id": "wallet-b", "account_id": "b2",
        }},
        {"name": "collection falsey account", "method": "collection_list", "params": {
            "account_id": "",
        }},
        {"name": "collection negative pagination", "method": "collection_list", "params": {
            "page": -3, "page_size": -2,
        }},
        {"name": "collection missing wallet", "method": "collection_list", "params": {
            "wallet_id": "missing",
        }},
        {"name": "collection missing account", "method": "collection_list", "params": {
            "account_id": "missing",
        }},
        {"name": "collection foreign account", "method": "collection_list", "params": {
            "wallet_id": "wallet-b", "account_id": "a2",
        }},
        {"name": "collection no wallets", "method": "collection_list", "manager_state": "no_wallets"},
        {"name": "collection empty selected", "method": "collection_list", "params": {
            "wallet_id": "wallet-b", "page": 7, "page_size": 3,
        }, "manager_state": "empty_selected"},
        {"name": "collection records error", "method": "collection_list", "ledger_state": "records_error"},
        {"name": "collection count error", "method": "collection_list", "ledger_state": "count_error"},
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
            "public_list_methods": public_methods,
        },
        "cases": results,
    }, sort_keys=True))


if __name__ == "__main__":
    main()

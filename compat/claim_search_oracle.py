#!/usr/bin/env python3
"""Pinned offline oracle for public claim_search preprocessing and delegation."""

import argparse
import ast
import asyncio
import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/error/__init__.py": "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
    "lbry/extras/daemon/daemon.py": "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
    "lbry/wallet/ledger.py": "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
    "lbry/wallet/manager.py": "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
    "lbry/wallet/network.py": "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
    "lbry/wallet/wallet.py": "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
}
METHOD_HASHES = {
    "Daemon.jsonrpc_claim_search": "c0fe99e03604d69c828d5ff7dbe700da6926a5cf25fac78e0f2107481923ca1a",
    "Daemon.ledger": "c0aad64201976cc6d3b4ae3fa49fe9434093c578706b84f45b8cc687c7276f46",
    "Ledger.claim_search": "1f40805539780f244d81b2c436794dcbcf27dad1e80c3417b4e70d7f8cf227a8",
    "Network.claim_search": "8ffee30536ef5354fc5c6028eb06b70512ce0f2ed09bf9567edb8689a4957968",
    "Wallet.default_account": "76e84d5c63726f3c268e161ee2ef54e0573ab02a4aab04d9b7c6dae0fc95961e",
    "WalletManager.default_account": "6b5ae4ee1fd368d8b3bb05e3a8a3362a0f958f4e5385787958ff83fdb855e731",
    "WalletManager.default_wallet": "b985d6bbf6126a982f1f0084fc6872592cff7717f50b59fbe3a745f498c8de48",
    "WalletManager.get_wallet_or_default": "a78f3e4003c8bc2c25c95681532cb166eb3685a611aecd6024893fa6c94e8537",
    "WalletManager.get_wallet_or_error": "ac6310a5232801623f12f4be0909a0e64a595a94330465f3c825b9ac34c51eec",
}


class ConflictingInputValueError(Exception):
    def __init__(self, first_argument, second_argument):
        super().__init__(f"Only '{first_argument}' or '{second_argument}' is allowed, not both.")


class WalletNotLoadedError(Exception):
    def __init__(self, wallet_id):
        super().__init__(f"Wallet {wallet_id} is not loaded.")


def method_node(path, class_name, method_name):
    source = path.read_text(encoding="utf-8")
    source_class = next(
        node for node in ast.parse(source, filename=str(path)).body
        if isinstance(node, ast.ClassDef) and node.name == class_name
    )
    node = next(
        node for node in source_class.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name == method_name
    )
    return copy.deepcopy(node), hashlib.sha256(ast.get_source_segment(source, node).encode()).hexdigest()


def extracted_property(node):
    node.decorator_list = [ast.Name(id="property", ctx=ast.Load())]
    return node


def extract_types(sdk_root):
    specs = {
        "Daemon.jsonrpc_claim_search": ("lbry/extras/daemon/daemon.py", "Daemon", "jsonrpc_claim_search"),
        "Daemon.ledger": ("lbry/extras/daemon/daemon.py", "Daemon", "ledger"),
        "Ledger.claim_search": ("lbry/wallet/ledger.py", "Ledger", "claim_search"),
        "Network.claim_search": ("lbry/wallet/network.py", "Network", "claim_search"),
        "Wallet.default_account": ("lbry/wallet/wallet.py", "Wallet", "default_account"),
        "WalletManager.default_account": ("lbry/wallet/manager.py", "WalletManager", "default_account"),
        "WalletManager.default_wallet": ("lbry/wallet/manager.py", "WalletManager", "default_wallet"),
        "WalletManager.get_wallet_or_default": ("lbry/wallet/manager.py", "WalletManager", "get_wallet_or_default"),
        "WalletManager.get_wallet_or_error": ("lbry/wallet/manager.py", "WalletManager", "get_wallet_or_error"),
    }
    nodes, hashes = {}, {}
    for name, (relative, class_name, method_name) in specs.items():
        nodes[name], hashes[name] = method_node(sdk_root / relative, class_name, method_name)
    if hashes != METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {METHOD_HASHES}")
    nodes["Daemon.jsonrpc_claim_search"].decorator_list = []
    classes = [
        ast.ClassDef("ProbeDaemon", [], [], [
            nodes["Daemon.jsonrpc_claim_search"], extracted_property(nodes["Daemon.ledger"]),
        ], []),
        ast.ClassDef("ProbeLedger", [], [], [nodes["Ledger.claim_search"]], []),
        ast.ClassDef("ProbeNetwork", [], [], [nodes["Network.claim_search"]], []),
        ast.ClassDef("ProbeWallet", [], [], [extracted_property(nodes["Wallet.default_account"])], []),
        ast.ClassDef("ProbeManager", [], [], [
            extracted_property(nodes["WalletManager.default_account"]),
            extracted_property(nodes["WalletManager.default_wallet"]),
            nodes["WalletManager.get_wallet_or_default"],
            nodes["WalletManager.get_wallet_or_error"],
        ], []),
    ]
    namespace = {
        "ConflictingInputValueError": ConflictingInputValueError,
        "DEFAULT_PAGE_SIZE": 20,
        "WalletNotLoadedError": WalletNotLoadedError,
    }
    module = ast.fix_missing_locations(ast.Module(classes, type_ignores=[]))
    exec(compile(module, "<pinned claim search>", "exec"), namespace)
    return tuple(namespace[name] for name in (
        "ProbeDaemon", "ProbeLedger", "ProbeNetwork", "ProbeWallet", "ProbeManager",
    )), hashes


def account_label(account):
    return account.label


def json_value(value):
    if isinstance(value, ProbeSession):
        return value.label
    return value


class ProbeSession:
    def __init__(self, label):
        self.label = label


def make_fixture(types):
    daemon_type, ledger_type, network_type, wallet_type, manager_type = types
    calls = {"rpc": [], "inflate": []}

    def network_rpc(self, method, params, restricted=True, session=None):
        calls["rpc"].append({
            "ledger": self.label,
            "method": method,
            "params": copy.deepcopy(params),
            "restricted": restricted,
            "session": json_value(session),
        })

        async def response():
            return "offline-result"
        return response()

    async def inflate_outputs(self, query, accounts, include_purchase_receipt=False,
                              include_is_my_output=False, **kwargs):
        encoded = await query
        calls["inflate"].append({
            "ledger": self.label,
            "accounts": [account_label(account) for account in accounts],
            "encoded": encoded,
            "include_purchase_receipt": include_purchase_receipt,
            "include_is_my_output": include_is_my_output,
            "extra_options": kwargs,
        })
        return ["item"], {"total": 5, "channels": []}, 777, 41

    network_type.rpc = network_rpc
    ledger_type._inflate_outputs = inflate_outputs
    default_network, other_network = network_type(), network_type()
    default_network.label, other_network.label = "default-ledger", "other-ledger"
    default_ledger, other_ledger = ledger_type(), ledger_type()
    default_ledger.label, other_ledger.label = "default-ledger", "other-ledger"
    default_ledger.network, other_ledger.network = default_network, other_network

    def account(label, ledger):
        value = type("ProbeAccount", (), {})()
        value.label, value.ledger = label, ledger
        return value

    def wallet(identifier, accounts):
        value = wallet_type()
        value.id, value.accounts = identifier, accounts
        return value

    default_wallet = wallet("default", [account("default-account", default_ledger)])
    other_wallet = wallet("other", [
        account("other-account-0", other_ledger), account("other-account-1", other_ledger),
    ])
    empty_wallet = wallet("empty", [])
    manager = manager_type()
    manager.wallets = [default_wallet, other_wallet, empty_wallet]
    daemon = daemon_type()
    daemon.wallet_manager = manager
    return daemon, calls


async def execute_case(types, name, params):
    daemon, calls = make_fixture(types)
    try:
        result = await daemon.jsonrpc_claim_search(**copy.deepcopy(params))
        error = None
    except Exception as err:  # pylint: disable=broad-except
        result = None
        error = {"type": err.__class__.__name__, "message": str(err)}
    return {"name": name, "params": params, "result": result, "error": error, "calls": calls}


def case_specs():
    return [
        ("defaults", {}),
        ("falsey claim ids null", {"claim_id": "a", "claim_ids": None}),
        ("falsey claim ids empty list", {"claim_id": "a", "claim_ids": []}),
        ("falsey claim ids false", {"claim_id": "a", "claim_ids": False}),
        ("claim id conflict precedes pagination and wallet", {
            "claim_id": "a", "claim_ids": ["b"], "page": "bad", "wallet_id": "missing",
        }),
        ("valid signature", {"valid_channel_signature": True}),
        ("invalid signature", {"invalid_channel_signature": True}),
        ("invalid signature wins", {
            "valid_channel_signature": True, "invalid_channel_signature": True,
        }),
        ("false signature flags removed", {
            "valid_channel_signature": False, "invalid_channel_signature": 0,
        }),
        ("has no source false overwrites", {"has_source": False, "has_no_source": False}),
        ("has no source true", {"has_no_source": True}),
        ("has no source null", {"has_no_source": None}),
        ("order scalar", {"order_by": "height"}),
        ("order migration and dedupe", {"order_by": [
            "trending_mixed", "trending_score", "height", "trending_group", "^amount", "height",
        ]}),
        ("order null", {"order_by": None}),
        ("negative page cap selected wallet and includes", {
            "claim_ids": [], "claim_id": "c", "valid_channel_signature": True,
            "invalid_channel_signature": True, "has_source": False, "has_no_source": False,
            "order_by": ["trending_local", "trending_score", "height"],
            "page": -3, "page_size": 99, "wallet_id": "other",
            "include_purchase_receipt": True, "include_is_my_output": 1,
            "no_totals": True, "offset": 999, "limit": 999, "account_id": "hub-only",
        }),
        ("zero pagination no totals", {"page": 0, "page_size": 0, "no_totals": True}),
        ("zero page size totals fails after request", {"page": 0, "page_size": 0}),
        ("boolean pagination", {"page": False, "page_size": True}),
        ("fractional pagination", {"page": -2.5, "page_size": -6.5}),
        ("bad page precedes missing wallet", {
            "page": "bad", "page_size": 7, "wallet_id": "missing", "valid_channel_signature": True,
        }),
        ("missing wallet after preprocessing", {"page": 2, "wallet_id": "missing"}),
        ("empty selected wallet", {
            "wallet_id": "empty", "include_purchase_receipt": True, "include_is_my_output": True,
        }),
        ("false include flags consumed", {
            "include_purchase_receipt": False, "include_is_my_output": None,
        }),
        ("session override consumed", {"session_override": ProbeSession("alternate-session")}),
    ]


async def execute(types):
    return [await execute_case(types, name, params) for name, params in case_specs()]


def verify_sources(sdk_root):
    commit = subprocess.run(
        ["git", "-C", str(sdk_root), "rev-parse", "HEAD"],
        check=True, capture_output=True, text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    for relative, expected in SOURCE_HASHES.items():
        actual = hashlib.sha256((sdk_root / relative).read_bytes()).hexdigest()
        if actual != expected:
            raise RuntimeError(f"{relative} hash is {actual}, expected {expected}")


def run(sdk_root):
    verify_sources(sdk_root)
    types, hashes = extract_types(sdk_root)
    cases = asyncio.run(execute(types))
    return {
        "reference": {
            "commit": PINNED_COMMIT, "version": PINNED_VERSION,
            "source_sha256": SOURCE_HASHES, "method_sha256": hashes,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "extracted_methods_executed": True,
            "external_network_used": False,
            "case_count": len(cases),
            "proposed_go_contract": {
                "handler": "(*RPCServer).handleClaimSearch(http.ResponseWriter, normalizedRPCParams)",
                "normalizer": "normalizeClaimSearchParams(normalizedRPCParams) (ClaimSearchRequest, error)",
                "ledger": "(*Ledger).ClaimSearch(context.Context, map[string]any, []*Account, ClaimSearchOptions) (HubOutputsPage, error)",
                "note": "Names are advisory; fixture preprocessing, calls, results, and errors are normative.",
            },
            "contract_notes": {
                "ledger_selection": "wallet_id selects enrichment accounts, while the first wallet's first account always supplies the ledger and network.",
                "account_id": "account_id is not a local selector and is forwarded to the hub unchanged.",
                "include_flags": "include_purchase_receipt and include_is_my_output bind in Ledger.claim_search and never reach hub params.",
                "no_totals": "no_totals is sent to the hub, then controls whether the public result includes total_pages and total_items.",
            },
        },
        "cases": cases,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    arguments = parser.parse_args()
    print(json.dumps(run(arguments.sdk_root.resolve()), sort_keys=True, default=json_value))


if __name__ == "__main__":
    main()

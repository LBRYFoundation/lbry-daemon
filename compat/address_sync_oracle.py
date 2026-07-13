#!/usr/bin/env python3
"""Source-pinned address inventory, gap, and status contract for SDK 0.113.0."""

import argparse
import ast
import asyncio
from binascii import hexlify
import hashlib
import json
import logging
from pathlib import Path
import random
import subprocess
import sys
from typing import Any, Dict, List, Optional, Tuple


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/crypto/hash.py": "bfc430bd3fe98578b406caa3a8e2116a40f492c7b68e269176e838b4ef426a72",
    "lbry/wallet/account.py": "ea2ca30bddf9c0145469e989d9855dbe7be5184943ae7b8ca690eda41eb7db50",
    "lbry/wallet/database.py": "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
    "lbry/wallet/ledger.py": "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
    "lbry/wallet/network.py": "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
    "tests/integration/blockchain/test_sync.py": "2788fc83e701eb0082fa4167cbada29e3eccb0e008f77df8e2c5a2810354950d",
    "tests/unit/wallet/test_account.py": "3f6ae1c40230ce2717c44157f757cb96e24f31b7402542d4ad987905826e1c62",
    "tests/unit/wallet/test_database.py": "7af85de707b329d8715cd22419a4f761b10792a3ecc023202389dd86e3011c51",
    "tests/unit/wallet/test_ledger.py": "045a14bc252c0b9b6759d7444e582c5e17c6009689f5ee1fd05e74739711ab88",
}


class KeyPath:
    RECEIVE = 0
    CHANGE = 1
    CHANNEL = 2


class ProbeAnnouncementError(Exception):
    """Controlled failure after address rows have been inserted."""


class ProbePublicKey:
    def __init__(self, path=()):
        self.path = tuple(path)
        self.n = self.path[-1] if self.path else 0
        self.depth = len(self.path)
        self.chain_code = bytes((self.depth,)) * 32

    def child(self, index):
        return ProbePublicKey(self.path + (index,))

    @property
    def address(self):
        if not self.path:
            return "address-root"
        return "address-" + "-".join(map(str, self.path))

    @property
    def pubkey_bytes(self):
        return self.address.encode()


class ProbeDatabase:
    def __init__(self):
        self.records = []

    async def add_keys(self, account, chain, pubkeys):
        for public_key in pubkeys:
            if any(
                row["account"] == account.id and row["address"] == public_key.address
                for row in self.records
            ):
                continue
            self.records.append({
                "account": account.id,
                "address": public_key.address,
                "chain": chain,
                "pubkey": public_key,
                "history": None,
                "used_times": 0,
            })

    async def get_addresses(self, read_only=False, accounts=None, chain=None, **constraints):
        del read_only
        account_ids = None if accounts is None else {account.id for account in accounts}
        rows = [
            row for row in self.records
            if (account_ids is None or row["account"] in account_ids)
            and (chain is None or row["chain"] == chain)
        ]
        maximum_uses = constraints.pop("used_times__lt", None)
        if maximum_uses is not None:
            rows = [row for row in rows if row["used_times"] < maximum_uses]
        order_by = constraints.pop("order_by", None)
        limit = constraints.pop("limit", None)
        if constraints:
            raise AssertionError(f"unsupported address constraints: {constraints}")
        if order_by == "n desc":
            rows.sort(key=lambda row: row["pubkey"].n, reverse=True)
        elif order_by == "n asc":
            rows.sort(key=lambda row: row["pubkey"].n)
        elif order_by == "used_times asc, n asc":
            rows.sort(key=lambda row: (row["used_times"], row["pubkey"].n))
        elif order_by is not None:
            raise AssertionError(f"unsupported address ordering: {order_by}")
        if limit is not None:
            rows = rows[:limit]
        return [dict(row) for row in rows]

    async def get_address(self, address):
        for row in self.records:
            if row["address"] == address:
                return dict(row)
        return None

    async def set_address_history(self, address, history):
        for row in self.records:
            if row["address"] == address:
                row["history"] = history
                row["used_times"] = history.count(":") // 2
                return


class ProbeLedger:
    def __init__(self):
        self.db = ProbeDatabase()
        self.announcements = []
        self.fail_announcement = False

    async def announce_addresses(self, address_manager, addresses):
        self.announcements.append({
            "chain": address_manager.chain_number,
            "addresses": list(addresses),
        })
        if self.fail_announcement:
            self.fail_announcement = False
            raise ProbeAnnouncementError("controlled announcement failure")


class ProbeAccount:
    def __init__(self, ledger, identifier="account"):
        self.ledger = ledger
        self.id = identifier
        self.public_key = ProbePublicKey()


def sha256_digest(value):
    return hashlib.sha256(value).digest()


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
    version = sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    return commit, version


def load_contract(sdk_root):
    account_path = sdk_root / "lbry" / "wallet" / "account.py"
    account_tree = ast.parse(
        account_path.read_text(encoding="utf-8"), filename=str(account_path)
    )
    wanted = {"AddressManager", "HierarchicalDeterministic", "SingleKey"}
    selected = [
        node for node in account_tree.body
        if isinstance(node, ast.ClassDef) and node.name in wanted
    ]
    if {node.name for node in selected} != wanted:
        raise RuntimeError("pinned address-manager classes changed")
    namespace = {
        "Any": Any,
        "Dict": Dict,
        "KeyPath": KeyPath,
        "List": List,
        "Optional": Optional,
        "PublicKey": ProbePublicKey,
        "Tuple": Tuple,
        "asyncio": asyncio,
        "random": random,
    }
    module = ast.fix_missing_locations(ast.Module(body=selected, type_ignores=[]))
    exec(compile(module, str(account_path), "exec"), namespace)  # pylint: disable=exec-used

    ledger_path = sdk_root / "lbry" / "wallet" / "ledger.py"
    ledger_tree = ast.parse(
        ledger_path.read_text(encoding="utf-8"), filename=str(ledger_path)
    )
    ledger_class = next(
        node for node in ledger_tree.body
        if isinstance(node, ast.ClassDef) and node.name == "Ledger"
    )
    local_status = next(
        node for node in ledger_class.body
        if isinstance(node, ast.AsyncFunctionDef)
        and node.name == "get_local_status_and_history"
    )
    status_namespace = {"hexlify": hexlify, "sha256": sha256_digest}
    status_module = ast.fix_missing_locations(ast.Module(body=[local_status], type_ignores=[]))
    exec(compile(status_module, str(ledger_path), "exec"), status_namespace)  # pylint: disable=exec-used
    return (
        namespace["HierarchicalDeterministic"],
        namespace["SingleKey"],
        status_namespace["get_local_status_and_history"],
    )


def record_view(record):
    return {
        "address": record["address"],
        "chain": record["chain"],
        "n": record["pubkey"].n,
        "history": record["history"],
        "used_times": record["used_times"],
    }


async def records_for(manager, only_usable=False):
    records = await manager.get_address_records(only_usable=only_usable)
    return [record_view(record) for record in records]


async def gap_step(manager, ledger, name):
    before = await records_for(manager)
    announcement_offset = len(ledger.announcements)
    created = await manager.ensure_address_gap()
    return {
        "name": name,
        "gap": manager.gap,
        "before": before,
        "created": created,
        "after": await records_for(manager),
        "announcements": ledger.announcements[announcement_offset:],
    }


async def run_address_cases(hierarchical, single_key):
    default_ledger = ProbeLedger()
    default_account = ProbeAccount(default_ledger, "default")
    receiving, change = hierarchical.from_dict(default_account, {})
    default_created = []
    for manager in {item.chain_number: item for item in (receiving, change)}.values():
        default_created.extend(await manager.ensure_address_gap())
    defaults = {
        "generator": hierarchical.name,
        "receiving_gap": receiving.gap,
        "change_gap": change.gap,
        "receiving_maximum_uses": receiving.maximum_uses_per_address,
        "change_maximum_uses": change.maximum_uses_per_address,
        "created": default_created,
        "announcements": default_ledger.announcements,
        "receiving_records": await records_for(receiving),
        "change_records": await records_for(change),
    }

    gap_ledger = ProbeLedger()
    gap_account = ProbeAccount(gap_ledger, "gap")
    gap_manager, _ = hierarchical.from_dict(gap_account, {
        "receiving": {"gap": 4, "maximum_uses_per_address": 2},
        "change": {"gap": 2, "maximum_uses_per_address": 1},
    })
    gap_steps = [await gap_step(gap_manager, gap_ledger, "initial fill")]
    gap_steps.append(await gap_step(gap_manager, gap_ledger, "already full"))
    await gap_ledger.db.set_address_history(gap_steps[0]["created"][0], "a:1:")
    gap_steps.append(await gap_step(gap_manager, gap_ledger, "oldest used"))
    await gap_ledger.db.set_address_history(gap_steps[-1]["created"][0], "b:2:")
    gap_steps.append(await gap_step(gap_manager, gap_ledger, "newest used"))
    await gap_ledger.db.set_address_history("address-0-2", "x:1:y:2:z:3:")
    inventory = {
        "maximum_uses": gap_manager.maximum_uses_per_address,
        "all": await records_for(gap_manager),
        "usable": await records_for(gap_manager, only_usable=True),
        "max_gap": await gap_manager.get_max_gap(),
    }

    untouched_ledger = ProbeLedger()
    untouched_account = ProbeAccount(untouched_ledger, "untouched")
    untouched, _ = hierarchical.from_dict(untouched_account, {
        "receiving": {"gap": 4, "maximum_uses_per_address": 1},
        "change": {"gap": 2, "maximum_uses_per_address": 1},
    })
    await untouched.ensure_address_gap()
    untouched_max_gap = await untouched.get_max_gap()

    lock_result = {"error_type": None, "error_message": None}
    try:
        await gap_manager._generate_keys(100, 100)  # pylint: disable=protected-access
    except Exception as error:  # pylint: disable=broad-except
        lock_result = {
            "error_type": type(error).__name__,
            "error_message": str(error),
        }

    partial_ledger = ProbeLedger()
    partial_account = ProbeAccount(partial_ledger, "partial")
    partial, _ = hierarchical.from_dict(partial_account, {
        "receiving": {"gap": 2, "maximum_uses_per_address": 1},
        "change": {"gap": 1, "maximum_uses_per_address": 1},
    })
    partial_ledger.fail_announcement = True
    partial_error_type = None
    partial_error_message = None
    try:
        await partial.ensure_address_gap()
    except Exception as error:  # pylint: disable=broad-except
        partial_error_type = type(error).__name__
        partial_error_message = str(error)
    partial_failure = {
        "error_type": partial_error_type,
        "error_message": partial_error_message,
        "persisted": await records_for(partial),
        "announcements": partial_ledger.announcements,
        "retry_created": await partial.ensure_address_gap(),
    }

    single_ledger = ProbeLedger()
    single_account = ProbeAccount(single_ledger, "single")
    single_receiving, single_change = single_key.from_dict(single_account, {})
    single_first = await single_receiving.ensure_address_gap()
    await single_ledger.db.set_address_history(single_first[0], "a:1:b:2:c:3:")
    single_case = {
        "same_manager": single_receiving is single_change,
        "first_created": single_first,
        "second_created": await single_receiving.ensure_address_gap(),
        "usable_after_three_uses": await single_receiving.get_or_create_usable_address(),
        "records": await records_for(single_receiving),
        "announcements": single_ledger.announcements,
    }

    return {
        "defaults": defaults,
        "gap_steps": gap_steps,
        "inventory": inventory,
        "all_unused_max_gap": untouched_max_gap,
        "lock_guard": lock_result,
        "partial_announcement_failure": partial_failure,
        "single_address": single_case,
    }


async def run_status_cases(local_status):
    ledger = ProbeLedger()
    account = ProbeAccount(ledger, "status")
    key = account.public_key.child(0).child(0)
    await ledger.db.add_keys(account, 0, [key])
    await ledger.db.set_address_history(key.address, "a:1:")

    cases = [
        ("missing address", "missing", None, False),
        ("stored history", key.address, None, False),
        ("falsey explicit reloads database", key.address, "", True),
        ("explicit empty missing", "missing", "", True),
        ("explicit override", key.address, "tx:0:other:-1:", True),
        ("missing trailing colon", key.address, "a:1", True),
        ("odd trailing field", key.address, "a:1:b:", True),
        ("malformed height", key.address, "tx:not-int:", True),
    ]
    results = []
    for name, address, history, supplied in cases:
        result = None
        error_type = None
        error_message = None
        effective_history = history
        if not history:
            details = await ledger.db.get_address(address)
            effective_history = (details["history"] if details else "") or ""
        try:
            if supplied:
                status, parsed = await local_status(ledger, address, history)
            else:
                status, parsed = await local_status(ledger, address)
            result = {"status": status, "history": [list(item) for item in parsed]}
        except Exception as error:  # pylint: disable=broad-except
            error_type = type(error).__name__
            error_message = str(error)
        results.append({
            "name": name,
            "address": address,
            "supplied": supplied,
            "input_history": history,
            "effective_history": effective_history,
            "result": result,
            "error_type": error_type,
            "error_message": error_message,
        })
    return results


def subscribe_batch_size(sdk_root):
    path = sdk_root / "lbry" / "wallet" / "ledger.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    ledger_class = next(
        node for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "Ledger"
    )
    method = next(
        node for node in ledger_class.body
        if isinstance(node, ast.AsyncFunctionDef) and node.name == "subscribe_addresses"
    )
    return ast.literal_eval(method.args.defaults[-1])


def verify_source_structure(sdk_root):
    account_source = (sdk_root / "lbry" / "wallet" / "account.py").read_text(encoding="utf-8")
    ledger_source = (sdk_root / "lbry" / "wallet" / "ledger.py").read_text(encoding="utf-8")
    network_source = (sdk_root / "lbry" / "wallet" / "network.py").read_text(encoding="utf-8")
    required = {
        account_source: [
            'limit=self.gap, order_by="n desc"',
            "await self.account.ledger.announce_addresses(self, new_keys)",
        ],
        ledger_source: [
            "for address_manager in account.address_managers.values():",
            "self._update_tasks.add(self.update_history(address, remote_status, address_manager))",
        ],
        network_source: [
            "return await self.rpc('blockchain.address.subscribe', addresses, True)",
            "return self.rpc('blockchain.address.get_history', [address], True)",
        ],
    }
    for source, fragments in required.items():
        if any(fragment not in source for fragment in fragments):
            raise RuntimeError("address synchronization source no longer has the pinned structure")


def run(sdk_root):
    if not __debug__:
        raise RuntimeError("address synchronization oracle requires Python assertions")
    commit, version = verify_reference(sdk_root)
    verify_source_structure(sdk_root)
    hierarchical, single_key, local_status = load_contract(sdk_root)
    return {
        "reference": {
            "commit": commit,
            "version": version,
            "source_sha256": PINNED_SOURCE_HASHES,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "python_assertions": __debug__,
            "receiving_chain": KeyPath.RECEIVE,
            "change_chain": KeyPath.CHANGE,
            "subscribe_batch_size": subscribe_batch_size(sdk_root),
            "subscribe_rpc": "blockchain.address.subscribe",
            "history_rpc": "blockchain.address.get_history",
            "notification_method": "blockchain.address.subscribe",
            "history_format": "txid:height:",
            "status_algorithm": "sha256",
            "status_empty_is_null": True,
            "existing_subscription_precedes_gap": True,
            "account_manager_order": ["receiving", "change"],
        },
        "addresses": asyncio.run(run_address_cases(hierarchical, single_key)),
        "status_cases": asyncio.run(run_status_cases(local_status)),
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    arguments = parser.parse_args()
    json.dump(run(arguments.sdk_root.resolve()), sys.stdout, sort_keys=True, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

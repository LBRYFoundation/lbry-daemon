#!/usr/bin/env python3
"""Probe pinned wallet/component lifecycle methods without importing the SDK."""

import argparse
import ast
import asyncio
import gc
from hashlib import sha256
import json
import logging
import os
from pathlib import Path
import subprocess
import sys
import tempfile


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/wallet/manager.py": "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
    "lbry/wallet/ledger.py": "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
    "lbry/extras/daemon/componentmanager.py": "a92e537bb538580549aa4c0456cc1c7981c2fe557f9ee7149cf220a39dc1b3f9",
    "lbry/extras/daemon/components.py": "e1059c789a67c44ec2632bee479afba0bf5091ab1c276afcc9e4fefcbbc68659",
}


class StartFailure(Exception):
    pass


class StopFailure(Exception):
    pass


class DatabaseOpenFailure(Exception):
    pass


class HeadersOpenFailure(Exception):
    pass


class ConnectedGateFailure(Exception):
    pass


class ComponentSetupFailure(Exception):
    pass


class ComponentStopFailure(Exception):
    pass


def verify_pinned_sources(sdk_root):
    commit = subprocess.run(
        ["git", "-C", str(sdk_root), "rev-parse", "HEAD"], check=True,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    for relative_path, expected in PINNED_SOURCE_HASHES.items():
        actual = sha256((sdk_root / relative_path).read_bytes()).hexdigest()
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
            for target in node.targets
        ):
            return ast.literal_eval(node.value)
    raise RuntimeError("could not read SDK version")


def load_method_contract(sdk_root, relative_path, source_class, methods, namespace):
    source_path = sdk_root / relative_path
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    class_node = next(
        (node for node in tree.body
         if isinstance(node, ast.ClassDef) and node.name == source_class),
        None,
    )
    if class_node is None:
        raise RuntimeError(f"{relative_path}: missing {source_class}")
    selected = [
        node for node in class_node.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in methods
    ]
    if {node.name for node in selected} != set(methods):
        raise RuntimeError(f"{relative_path}: lifecycle methods changed")
    contract = ast.ClassDef(
        name="Oracle" + source_class,
        bases=[], keywords=[], body=selected, decorator_list=[],
    )
    module = ast.fix_missing_locations(ast.Module(body=[contract], type_ignores=[]))
    exec(compile(module, str(source_path), "exec"), namespace)  # pylint: disable=exec-used
    return namespace[contract.name]


class ManagerLedgerProbe:
    def __init__(self, manager, events, name, start_failure=False, stop_failure=False):
        self.manager = manager
        self.events = events
        self.name = name
        self.start_failure = start_failure
        self.stop_failure = stop_failure

    async def start(self):
        self.events.append(("start", self.name, self.manager.running))
        await asyncio.sleep(0)
        if self.start_failure:
            raise StartFailure(self.name)

    async def stop(self):
        self.events.append(("stop", self.name, self.manager.running))
        await asyncio.sleep(0)
        if self.stop_failure:
            raise StopFailure(self.name)


async def probe_wallet_manager(manager_type):
    empty = manager_type()
    empty.ledgers = {}
    empty.running = False
    await empty.start()
    after_empty_start = empty.running
    await empty.stop()

    successful = manager_type()
    successful.running = False
    successful_events = []
    successful.ledgers = {
        "one": ManagerLedgerProbe(successful, successful_events, "one"),
        "two": ManagerLedgerProbe(successful, successful_events, "two"),
    }
    await successful.start()

    failed_start = manager_type()
    failed_start.running = False
    failed_start_events = []
    failed_start.ledgers = {
        "one": ManagerLedgerProbe(failed_start, failed_start_events, "one", start_failure=True),
        "two": ManagerLedgerProbe(failed_start, failed_start_events, "two"),
    }
    start_error = None
    try:
        await failed_start.start()
    except Exception as error:  # exact exception propagation is part of the contract
        start_error = type(error).__name__

    successful_stop = manager_type()
    successful_stop.running = True
    successful_stop_events = []
    successful_stop.ledgers = {
        "one": ManagerLedgerProbe(successful_stop, successful_stop_events, "one"),
        "two": ManagerLedgerProbe(successful_stop, successful_stop_events, "two"),
    }
    await successful_stop.stop()

    failed_stop = manager_type()
    failed_stop.running = True
    failed_stop_events = []
    failed_stop.ledgers = {
        "one": ManagerLedgerProbe(failed_stop, failed_stop_events, "one", stop_failure=True),
        "two": ManagerLedgerProbe(failed_stop, failed_stop_events, "two"),
    }
    stop_error = None
    try:
        await failed_stop.stop()
    except Exception as error:  # exact exception propagation is part of the contract
        stop_error = type(error).__name__

    return {
        "empty": {"after_start": after_empty_start, "after_stop": empty.running},
        "start_success": {
            "running": successful.running,
            "all_entered": len(successful_events) == 2,
            "running_seen": [event[2] for event in successful_events],
        },
        "start_failure": {
            "error_type": start_error,
            "running": failed_start.running,
            "all_entered": len(failed_start_events) == 2,
            "running_seen": [event[2] for event in failed_start_events],
        },
        "stop_success": {
            "running": successful_stop.running,
            "all_entered": len(successful_stop_events) == 2,
            "running_seen": [event[2] for event in successful_stop_events],
        },
        "stop_failure": {
            "error_type": stop_error,
            "running": failed_stop.running,
            "all_entered": len(failed_stop_events) == 2,
            "running_seen": [event[2] for event in failed_stop_events],
        },
    }


class ChildOpenProbe:
    def __init__(self, events, name, failure):
        self.events = events
        self.name = name
        self.failure = failure

    async def open(self):
        self.events.append(self.name)
        await asyncio.sleep(0)
        if self.failure is not None:
            raise self.failure(self.name)


class ReadyProbe:
    @property
    def first(self):
        return asyncio.get_running_loop().create_future()


class ConnectedProbe:
    @property
    def first(self):
        async def fail_after_child_wait():
            raise ConnectedGateFailure()
        return fail_after_child_wait()


class NetworkProbe:
    def __init__(self, events):
        self.events = events
        self.on_connected = ConnectedProbe()

    async def start(self):
        self.events.append("network")
        await asyncio.sleep(0)


async def probe_ledger_start(ledger_type, database_failure, headers_failure):
    events = []
    with tempfile.TemporaryDirectory() as directory:
        ledger = ledger_type()
        ledger.path = directory
        ledger.db = ChildOpenProbe(events, "database", database_failure)
        ledger.headers = ChildOpenProbe(events, "headers", headers_failure)
        ledger.on_ready = ReadyProbe()
        ledger.network = NetworkProbe(events)
        error_type = None
        try:
            await ledger.start()
        except Exception as error:  # the next-stage error must win over child errors
            error_type = type(error).__name__
        await asyncio.sleep(0)
    gc.collect()
    await asyncio.sleep(0)
    expected_failures = []
    if database_failure is not None:
        expected_failures.append(database_failure.__name__)
    if headers_failure is not None:
        expected_failures.append(headers_failure.__name__)
    return {
        "child_failures": expected_failures,
        "child_attempts": sorted(event for event in events if event != "network"),
        "error_type": error_type,
        "reached_network": "network" in events,
    }


class ComponentProbe:
    def __init__(self, name, events, running=False, setup_failure=False, stop_failure=False):
        self.name = name
        self.events = events
        self.running = running
        self.setup_failure = setup_failure
        self.stop_failure = stop_failure

    async def _setup(self):
        self.events.append("setup:" + self.name)
        await asyncio.sleep(0)
        if self.setup_failure:
            raise ComponentSetupFailure(self.name)
        self.running = True

    async def _stop(self):
        self.events.append("stop:" + self.name)
        await asyncio.sleep(0)
        if self.stop_failure:
            raise ComponentStopFailure(self.name)
        self.running = False


async def probe_component_manager(component_manager_type):
    start_events = []
    failed = ComponentProbe("failed", start_events, setup_failure=True)
    peer = ComponentProbe("peer", start_events)
    dependent = ComponentProbe("dependent", start_events)
    start_manager = component_manager_type()
    start_manager.started = asyncio.Event()
    start_manager.sort_components = lambda reverse=False: [[failed, peer], [dependent]]
    start_error = None
    try:
        await start_manager.start()
    except Exception as error:
        start_error = type(error).__name__

    stop_events = []
    base = ComponentProbe("base", stop_events, running=True)
    dependent_stop = ComponentProbe("dependent", stop_events, running=True, stop_failure=True)
    stop_manager = component_manager_type()
    stop_manager.sort_components = (
        lambda reverse=False: [[dependent_stop], [base]] if reverse else [[base], [dependent_stop]]
    )
    stop_error = None
    try:
        await stop_manager.stop()
    except Exception as error:
        stop_error = type(error).__name__

    gc.collect()
    await asyncio.sleep(0)
    return {
        "start_child_failure": {
            "error_type": start_error,
            "started_set": start_manager.started.is_set(),
            "later_stage_attempted": "setup:dependent" in start_events,
            "failed_running": failed.running,
            "peer_running": peer.running,
            "dependent_running": dependent.running,
        },
        "stop_child_failure": {
            "error_type": stop_error,
            "later_stage_attempted": "stop:base" in stop_events,
            "failed_running": dependent_stop.running,
            "base_running": base.running,
        },
    }


class ComponentWalletManagerProbe:
    def __init__(self, start_failure=False, stop_failure=False):
        self.start_failure = start_failure
        self.stop_failure = stop_failure

    async def start(self):
        if self.start_failure:
            raise StartFailure()

    async def stop(self):
        if self.stop_failure:
            raise StopFailure()


class ComponentWalletManagerFactory:
    current = None

    @classmethod
    async def from_lbrynet_config(cls, _conf):
        return cls.current


async def probe_wallet_component(wallet_component_type):
    failed_start_manager = ComponentWalletManagerProbe(start_failure=True)
    ComponentWalletManagerFactory.current = failed_start_manager
    failed_start = wallet_component_type()
    failed_start.conf = object()
    failed_start.wallet_manager = None
    start_error = None
    try:
        await failed_start.start()
    except Exception as error:
        start_error = type(error).__name__

    failed_stop_manager = ComponentWalletManagerProbe(stop_failure=True)
    failed_stop = wallet_component_type()
    failed_stop.wallet_manager = failed_stop_manager
    stop_error = None
    try:
        await failed_stop.stop()
    except Exception as error:
        stop_error = type(error).__name__

    successful_stop = wallet_component_type()
    successful_stop.wallet_manager = ComponentWalletManagerProbe()
    await successful_stop.stop()
    return {
        "start_failure": {
            "error_type": start_error,
            "manager_retained": failed_start.wallet_manager is failed_start_manager,
        },
        "stop_failure": {
            "error_type": stop_error,
            "manager_retained": failed_stop.wallet_manager is failed_stop_manager,
        },
        "stop_success": {"manager_cleared": successful_stop.wallet_manager is None},
    }


async def run_async(sdk_root):
    unhandled = []
    loop = asyncio.get_running_loop()
    loop.set_exception_handler(
        lambda _loop, context: unhandled.append(
            type(context.get("exception")).__name__ if context.get("exception") else "unknown"
        )
    )
    base_namespace = {"asyncio": asyncio, "os": os, "log": logging.getLogger(__name__)}
    manager_type = load_method_contract(
        sdk_root, "lbry/wallet/manager.py", "WalletManager", {"start", "stop"},
        dict(base_namespace),
    )
    ledger_type = load_method_contract(
        sdk_root, "lbry/wallet/ledger.py", "Ledger", {"start"}, dict(base_namespace),
    )
    component_manager_type = load_method_contract(
        sdk_root, "lbry/extras/daemon/componentmanager.py", "ComponentManager",
        {"start", "stop"}, dict(base_namespace),
    )
    component_namespace = dict(base_namespace)
    component_namespace["WalletManager"] = ComponentWalletManagerFactory
    wallet_component_type = load_method_contract(
        sdk_root, "lbry/extras/daemon/components.py", "WalletComponent",
        {"start", "stop"}, component_namespace,
    )

    result = {
        "wallet_manager": await probe_wallet_manager(manager_type),
        "ledger_start": [
            await probe_ledger_start(ledger_type, DatabaseOpenFailure, None),
            await probe_ledger_start(ledger_type, None, HeadersOpenFailure),
            await probe_ledger_start(ledger_type, DatabaseOpenFailure, HeadersOpenFailure),
        ],
        "component_manager": await probe_component_manager(component_manager_type),
        "wallet_component": await probe_wallet_component(wallet_component_type),
    }
    gc.collect()
    await asyncio.sleep(0)
    result["unhandled_child_errors"] = sorted(unhandled)
    return result


def run(sdk_root):
    commit = verify_pinned_sources(sdk_root)
    version = read_sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    result = asyncio.run(run_async(sdk_root))
    result.update({
        "reference": {"commit": commit, "version": version},
        "metadata": {"python_assertions": __debug__, "python_version": sys.version.split()[0]},
    })
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    arguments = parser.parse_args()
    json.dump(run(arguments.sdk_root.resolve()), sys.stdout, sort_keys=True, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

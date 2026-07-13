#!/usr/bin/env python3
"""Execute WalletManager.reset extracted from the pinned Python SDK."""

import argparse
import ast
import asyncio
from hashlib import sha256
import json
from pathlib import Path
import subprocess


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
MANAGER_SHA256 = "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db"


class ServerSetting:
    default = [("default", 50001)]

    @staticmethod
    def is_set(config):
        return config.is_set


class Config:
    lbryum_servers = ServerSetting()


class LedgerProbe:
    def __init__(self, events, stop_error=False, start_error=False):
        self.events = events
        self.stop_error = stop_error
        self.start_error = start_error
        self.config = {"old": True}

    async def stop(self):
        self.events.append(["stop", dict(self.config)])
        if self.stop_error:
            raise RuntimeError("stop")

    async def start(self):
        self.events.append(["start", dict(self.config)])
        if self.start_error:
            raise RuntimeError("start")


class LiveConfig:
    def __init__(self, is_set):
        self.is_set = is_set
        self.lbryum_servers = [("explicit", 50002)]
        self.known_hubs = {"known": {}}
        self.jurisdiction = "US"
        self.hub_timeout = 17.5
        self.concurrent_hub_requests = 9
        self.wallet_dir = "/wallet"


def load_reset(sdk_root):
    path = sdk_root / "lbry" / "wallet" / "manager.py"
    if sha256(path.read_bytes()).hexdigest() != MANAGER_SHA256:
        raise RuntimeError("manager.py does not match the pinned SDK")
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    manager = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "WalletManager")
    reset = next(node for node in manager.body if isinstance(node, ast.AsyncFunctionDef) and node.name == "reset")
    oracle = ast.ClassDef("OracleManager", [], [], [reset], [])
    module = ast.fix_missing_locations(ast.Module(body=[oracle], type_ignores=[]))
    namespace = {"Config": Config}
    exec(compile(module, str(path), "exec"), namespace)  # pylint: disable=exec-used
    return namespace["OracleManager"]


async def run_case(manager_type, is_set, stop_error=False, start_error=False):
    manager = manager_type()
    manager.config = LiveConfig(is_set)
    events = []
    manager.ledger = LedgerProbe(events, stop_error, start_error)
    error = None
    try:
        await manager.reset()
    except Exception as err:  # exact first failure is part of the contract
        error = str(err)
    return {"events": events, "config": manager.ledger.config, "error": error}


async def main_async(sdk_root):
    commit = subprocess.run(
        ["git", "-C", str(sdk_root), "rev-parse", "HEAD"], check=True,
        stdout=subprocess.PIPE, text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    manager_type = load_reset(sdk_root)
    return {
        "reference": {"commit": commit, "version": PINNED_VERSION, "manager_sha256": MANAGER_SHA256},
        "unset": await run_case(manager_type, False),
        "explicit": await run_case(manager_type, True),
        "stop_failure": await run_case(manager_type, True, stop_error=True),
        "start_failure": await run_case(manager_type, True, start_error=True),
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(asyncio.run(main_async(args.sdk_root)), sort_keys=True))


if __name__ == "__main__":
    main()

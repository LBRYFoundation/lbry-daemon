#!/usr/bin/env python3
"""Execute pinned Daemon.jsonrpc_get with download-manager probes."""

import argparse
import ast
import asyncio
import copy
from hashlib import sha256
import json
import logging
import os
from pathlib import Path
import subprocess
import tempfile


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
DAEMON_SHA256 = "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0"
FILE_MANAGER_SHA256 = "8687f500460aec804b3327556b6e78e963e7fe8c1c8c8738ab41e651b9344dae"


class DownloadSDTimeoutError(Exception):
    pass


class WalletManagerProbe:
    def __init__(self):
        self.calls = []

    def get_wallet_or_default(self, wallet_id):
        self.calls.append(wallet_id)
        return "wallet:" + str(wallet_id)


class FileManagerProbe:
    def __init__(self, result=None, error=None):
        self.result = result
        self.error = error
        self.calls = []

    async def download_from_uri(self, *args, **kwargs):
        self.calls.append([list(args), kwargs])
        if self.error:
            raise self.error
        return self.result


def load_daemon(sdk_root):
    daemon_path = sdk_root / "lbry" / "extras" / "daemon" / "daemon.py"
    manager_path = sdk_root / "lbry" / "file" / "file_manager.py"
    if sha256(daemon_path.read_bytes()).hexdigest() != DAEMON_SHA256 or \
            sha256(manager_path.read_bytes()).hexdigest() != FILE_MANAGER_SHA256:
        raise RuntimeError("get sources do not match the pinned SDK")
    tree = ast.parse(daemon_path.read_text(encoding="utf-8"), filename=str(daemon_path))
    source_class = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "Daemon")
    method = next(node for node in source_class.body if isinstance(node, ast.AsyncFunctionDef) and node.name == "jsonrpc_get")
    selected = copy.deepcopy(method)
    selected.decorator_list = []
    oracle = ast.ClassDef("OracleDaemon", [], [], [selected], [])
    namespace = {
        "os": os, "log": logging.getLogger("oracle"),
        "DownloadSDTimeoutError": DownloadSDTimeoutError,
    }
    module = ast.fix_missing_locations(ast.Module(body=[oracle], type_ignores=[]))
    exec(compile(module, str(daemon_path), "exec"), namespace)  # pylint: disable=exec-used
    return namespace["OracleDaemon"]


async def invoke(daemon_type, result=None, error=None, **kwargs):
    daemon = daemon_type()
    daemon.wallet_manager = WalletManagerProbe()
    daemon.file_manager = FileManagerProbe(result, error)
    daemon.exchange_rate_manager = "rates"
    returned = await daemon.jsonrpc_get(**kwargs)
    return {
        "result": returned,
        "wallet_calls": daemon.wallet_manager.calls,
        "download_calls": daemon.file_manager.calls,
    }


async def main_async(sdk_root):
    daemon_type = load_daemon(sdk_root)
    with tempfile.TemporaryDirectory() as directory:
        missing = os.path.join(directory, "missing")
        return {
            "success": await invoke(
                daemon_type, result="stream", uri="lbry://name", file_name="file.mp4",
                download_directory=directory, timeout=9, save_file=True, wallet_id="wallet",
            ),
            "missing_directory": await invoke(
                daemon_type, result="stream", uri="lbry://name", download_directory=missing,
            ),
            "none": await invoke(daemon_type, result=None, uri="lbry://name"),
            "failure": await invoke(
                daemon_type, error=ValueError("download failed"), uri="lbry://name",
            ),
        }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    args = parser.parse_args()
    commit = subprocess.run(
        ["git", "-C", str(args.sdk_root), "rev-parse", "HEAD"], check=True,
        stdout=subprocess.PIPE, text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    print(json.dumps({
        "reference": {"commit": commit, "daemon_sha256": DAEMON_SHA256,
                      "file_manager_sha256": FILE_MANAGER_SHA256},
        "cases": asyncio.run(main_async(args.sdk_root)),
    }, sort_keys=True))


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Execute pinned daemon file mutation methods with managed-stream probes."""

import argparse
import ast
import asyncio
import copy
from hashlib import sha256
import json
import logging
from pathlib import Path
import subprocess


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
SOURCE_SHA256 = "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0"


class StreamProbe:
    def __init__(self, name, running=False):
        self.name = name
        self.file_name = name + ".mp4"
        self.running = running
        self.calls = []

    async def save_file(self, file_name=None, download_directory=None):
        self.calls.append(["save_file", file_name, download_directory])
        self.running = True

    async def stop(self):
        self.calls.append(["stop"])
        self.running = False


class FileManagerProbe:
    def __init__(self, streams):
        self.streams = streams
        self.deleted = []
        self.filters = []

    def get_filtered(self, **kwargs):
        self.filters.append(kwargs)
        return self.streams

    async def delete(self, stream, delete_file=False):
        self.deleted.append([stream.name, delete_file])


def load_daemon(sdk_root):
    path = sdk_root / "lbry" / "extras" / "daemon" / "daemon.py"
    if sha256(path.read_bytes()).hexdigest() != SOURCE_SHA256:
        raise RuntimeError("daemon.py does not match the pinned SDK")
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    source_class = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "Daemon")
    names = {"jsonrpc_file_set_status", "jsonrpc_file_delete", "jsonrpc_file_save"}
    methods = []
    for node in source_class.body:
        if isinstance(node, ast.AsyncFunctionDef) and node.name in names:
            selected = copy.deepcopy(node)
            selected.decorator_list = []
            methods.append(selected)
    if {node.name for node in methods} != names:
        raise RuntimeError("file mutation methods changed")
    oracle = ast.ClassDef("OracleDaemon", [], [], methods, [])
    module = ast.fix_missing_locations(ast.Module(body=[oracle], type_ignores=[]))
    namespace = {"log": logging.getLogger("oracle")}
    exec(compile(module, str(path), "exec"), namespace)  # pylint: disable=exec-used
    return namespace["OracleDaemon"]


async def invoke(daemon_type, method, streams, args=None, kwargs=None):
    daemon = daemon_type()
    daemon.file_manager = FileManagerProbe(streams)
    daemon.conf = type("Config", (), {"components_to_skip": ["dht"]})()
    error = None
    result = None
    try:
        result = await getattr(daemon, method)(*(args or []), **(kwargs or {}))
        if isinstance(result, StreamProbe):
            result = result.name
    except Exception as err:
        error = {"type": type(err).__name__, "message": str(err)}
    return {
        "result": result, "error": error,
        "stream_calls": {stream.name: stream.calls for stream in streams},
        "filters": daemon.file_manager.filters, "deleted": daemon.file_manager.deleted,
    }


async def main_async(sdk_root):
    daemon_type = load_daemon(sdk_root)
    return {
        "invalid_status": await invoke(daemon_type, "jsonrpc_file_set_status", [], ["bad"]),
        "missing_status": await invoke(daemon_type, "jsonrpc_file_set_status", [], ["start"], {"sd_hash": "x"}),
        "resume": await invoke(daemon_type, "jsonrpc_file_set_status", [StreamProbe("one")], ["start"]),
        "stop": await invoke(daemon_type, "jsonrpc_file_set_status", [StreamProbe("one", True)], ["stop"]),
        "already_stopped": await invoke(daemon_type, "jsonrpc_file_set_status", [StreamProbe("one")], ["stop"]),
        "delete_guard": await invoke(daemon_type, "jsonrpc_file_delete", [StreamProbe("one"), StreamProbe("two")]),
        "delete_all": await invoke(daemon_type, "jsonrpc_file_delete", [StreamProbe("one"), StreamProbe("two")],
                                   kwargs={"delete_all": True, "delete_from_download_dir": True}),
        "save_missing": await invoke(daemon_type, "jsonrpc_file_save", []),
        "save_multiple": await invoke(daemon_type, "jsonrpc_file_save", [StreamProbe("one"), StreamProbe("two")]),
        "save_one": await invoke(daemon_type, "jsonrpc_file_save", [StreamProbe("one")],
                                 kwargs={"file_name": "new.mp4", "download_directory": "/tmp", "claim_id": "c"}),
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
        "reference": {"commit": commit, "source_sha256": SOURCE_SHA256},
        "cases": asyncio.run(main_async(args.sdk_root)),
    }, sort_keys=True))


if __name__ == "__main__":
    main()

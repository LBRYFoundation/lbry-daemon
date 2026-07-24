#!/usr/bin/env python3
"""Generate the embedded legacy CLI manifest from the pinned daemon source."""

import argparse
import ast
import hashlib
import json
from pathlib import Path


PINNED_DAEMON_HASH = "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0"
NOT_GROUPED = {"routing_table_get", "ffmpeg_find"}


def deprecated_replacement(node):
    for decorator in node.decorator_list:
        if not isinstance(decorator, ast.Call):
            continue
        if isinstance(decorator.func, ast.Name) and decorator.func.id == "deprecated":
            return ast.literal_eval(decorator.args[0])
    return None


def generate(source_path):
    actual_hash = hashlib.sha256(source_path.read_bytes()).hexdigest()
    if actual_hash != PINNED_DAEMON_HASH:
        raise RuntimeError(
            f"{source_path} is not the pinned daemon source: "
            f"sha256 is {actual_hash}, expected {PINNED_DAEMON_HASH}"
        )
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    daemon = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "Daemon")

    groups = {}
    for node in daemon.body:
        if not isinstance(node, ast.Assign) or len(node.targets) != 1:
            continue
        target = node.targets[0]
        if isinstance(target, ast.Name) and target.id.endswith("_DOC"):
            groups[target.id[:-len("_DOC")].lower()] = ast.literal_eval(node.value).strip()

    commands = []
    for node in daemon.body:
        if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            continue
        if not node.name.startswith("jsonrpc_"):
            continue
        full_name = node.name[len("jsonrpc_"):]
        parts = [full_name] if full_name in NOT_GROUPED else full_name.split("_", 1)
        group = None if len(parts) == 1 else parts[0]
        if group is not None and group not in groups:
            raise RuntimeError(f"command {full_name} refers to unknown group {group}")
        commands.append({
            "method": full_name,
            "name": parts[-1],
            "group": group,
            "doc": ast.get_docstring(node, clean=False) or "",
            "replacement": deprecated_replacement(node),
        })
    commands.sort(key=lambda command: command["method"])
    if len(commands) != 94:
        raise RuntimeError(f"found {len(commands)} commands, expected 94")
    return {"groups": groups, "commands": commands}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    arguments = parser.parse_args()
    manifest = generate(arguments.sdk_root / "lbry" / "extras" / "daemon" / "daemon.py")
    arguments.output.parent.mkdir(parents=True, exist_ok=True)
    arguments.output.write_text(
        json.dumps(manifest, ensure_ascii=True, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )


if __name__ == "__main__":
    main()

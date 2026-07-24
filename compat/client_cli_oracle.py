#!/usr/bin/env python3
"""Run non-start CLI argv through the pinned Python docopt implementation."""

import argparse
import ast
import hashlib
import json
from pathlib import Path
import sys

try:
    import docopt
except ImportError as error:  # pragma: no cover - reported to the Go runner
    raise SystemExit("docopt is required to run the client CLI oracle") from error


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/extras/cli.py": "33e612f86a4a9b43e63ca4afb8d71a0edb0439ea1a1b57f706f20abe716bf5f8",
    "lbry/extras/daemon/daemon.py": "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
}
NOT_GROUPED = {"routing_table_get", "ffmpeg_find"}


def verify_pinned_sources(sdk_root):
    for relative_path, expected in PINNED_SOURCE_HASHES.items():
        source_path = sdk_root / relative_path
        actual = hashlib.sha256(source_path.read_bytes()).hexdigest()
        if actual != expected:
            raise RuntimeError(
                f"{relative_path} does not match pinned commit {PINNED_COMMIT}: "
                f"sha256 is {actual}, expected {expected}"
            )


def read_sdk_version(sdk_root):
    source_path = sdk_root / "lbry" / "__init__.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    for node in tree.body:
        if isinstance(node, ast.Assign) and any(
                isinstance(target, ast.Name) and target.id == "__version__" for target in node.targets):
            return ast.literal_eval(node.value)
    raise RuntimeError("could not read SDK version")


def load_normalizers(sdk_root):
    source_path = sdk_root / "lbry" / "extras" / "cli.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    names = {"normalize_value", "remove_brackets", "set_kwargs"}
    nodes = [node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name in names]
    if {node.name for node in nodes} != names:
        raise RuntimeError("pinned CLI source is missing normalization functions")
    module = ast.Module(body=nodes, type_ignores=[])
    ast.fix_missing_locations(module)
    namespace = {}
    exec(compile(module, str(source_path), "exec"), namespace)
    return namespace["set_kwargs"]


def load_commands(sdk_root):
    source_path = sdk_root / "lbry" / "extras" / "daemon" / "daemon.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    daemon = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "Daemon")
    groups = {}
    for node in daemon.body:
        if isinstance(node, ast.Assign) and len(node.targets) == 1:
            target = node.targets[0]
            if isinstance(target, ast.Name) and target.id.endswith("_DOC"):
                groups[target.id[:-len("_DOC")].lower()] = ast.literal_eval(node.value).strip()
    commands = {}
    for node in daemon.body:
        if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) or not node.name.startswith("jsonrpc_"):
            continue
        method = node.name[len("jsonrpc_"):]
        parts = [method] if method in NOT_GROUPED else method.split("_", 1)
        group = None if len(parts) == 1 else parts[0]
        replacement = None
        for decorator in node.decorator_list:
            if isinstance(decorator, ast.Call) and isinstance(decorator.func, ast.Name) \
                    and decorator.func.id == "deprecated":
                replacement = ast.literal_eval(decorator.args[0])
        commands[method] = {
            "method": method,
            "name": parts[-1],
            "group": group,
            "doc": ast.get_docstring(node, clean=False) or "",
            "replacement": replacement,
        }
    return groups, commands


def parse_case(argv, groups, commands, set_kwargs):
    if not argv:
        return {"argv": argv, "result": None, "error": "missing command"}
    if argv[0] in groups:
        if len(argv) < 2:
            return {"argv": argv, "result": None, "error": "missing group command"}
        method = f"{argv[0]}_{argv[1]}"
        command_args = argv[2:]
    else:
        method = argv[0]
        command_args = argv[1:]
    definition = commands.get(method)
    if definition is None:
        return {"argv": argv, "result": None, "error": f"unknown command {method}"}
    requested = method
    notice = ""
    if definition["replacement"]:
        definition = commands[definition["replacement"]]
        notice = f"{requested} is deprecated, using {definition['method']}."
    try:
        parsed = docopt.docopt(definition["doc"], command_args)
    except docopt.DocoptExit as error:
        return {"argv": argv, "result": None, "error": str(error)}
    return {
        "argv": argv,
        "result": {
            "requested_method": requested,
            "method": definition["method"],
            "params": set_kwargs(parsed),
            "notice": notice,
        },
        "error": None,
    }


def run(sdk_root, payload):
    verify_pinned_sources(sdk_root)
    version = read_sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    groups, commands = load_commands(sdk_root)
    set_kwargs = load_normalizers(sdk_root)
    return {
        "reference": {"commit": PINNED_COMMIT, "version": version},
        "manifest": {
            "groups": groups,
            "commands": [commands[name] for name in sorted(commands)],
        },
        "cases": [parse_case(argv, groups, commands, set_kwargs) for argv in payload.get("cases", [])],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    arguments = parser.parse_args()
    result = run(arguments.sdk_root.resolve(), json.load(sys.stdin))
    json.dump(result, sys.stdout, sort_keys=True, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

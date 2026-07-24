#!/usr/bin/env python3
"""Execute pinned file filtering and list pagination helpers."""

import argparse
import ast
from hashlib import sha256
import json
from pathlib import Path
import subprocess
from types import SimpleNamespace


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
SOURCE_MANAGER_SHA256 = "0bb1ecd8a7e26ea286a1d6b700cb6185b2a056deb593bef61f482e6fcabaa8a8"
DAEMON_SHA256 = "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0"
DEFAULT_PAGE_SIZE = 20
COMPARISON_OPERATORS = {
    "eq": lambda a, b: a == b, "ne": lambda a, b: a != b,
    "g": lambda a, b: a > b, "l": lambda a, b: a < b,
    "ge": lambda a, b: a >= b, "le": lambda a, b: a <= b,
}


def extract_function(path, name):
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    return next(node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == name)


def load_contract(sdk_root):
    source_path = sdk_root / "lbry" / "file" / "source_manager.py"
    daemon_path = sdk_root / "lbry" / "extras" / "daemon" / "daemon.py"
    if sha256(source_path.read_bytes()).hexdigest() != SOURCE_MANAGER_SHA256 or \
            sha256(daemon_path.read_bytes()).hexdigest() != DAEMON_SHA256:
        raise RuntimeError("file list sources do not match the pinned SDK")
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    source_class = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "SourceManager")
    method = next(node for node in source_class.body if isinstance(node, ast.FunctionDef) and node.name == "get_filtered")
    oracle_class = ast.ClassDef("OracleManager", [], [], [method], [])
    paginate = extract_function(daemon_path, "paginate_list")
    namespace = {"COMPARISON_OPERATORS": COMPARISON_OPERATORS, "DEFAULT_PAGE_SIZE": DEFAULT_PAGE_SIZE}
    module = ast.fix_missing_locations(ast.Module(body=[oracle_class, paginate], type_ignores=[]))
    exec(compile(module, str(source_path), "exec"), namespace)  # pylint: disable=exec-used
    manager_type = namespace["OracleManager"]
    manager_type.filter_fields = {
        "rowid", "status", "file_name", "added_on", "download_path", "claim_name",
        "claim_height", "claim_id", "outpoint", "txid", "nout", "channel_claim_id",
        "channel_name", "completed", "sd_hash", "stream_hash", "full_status",
        "blobs_remaining", "blobs_in_stream", "uploading_to_reflector", "is_fully_reflected",
    }
    manager_type.set_filter_fields = {
        "claim_ids": "claim_id", "channel_claim_ids": "channel_claim_id", "outpoints": "outpoint",
    }
    return manager_type, namespace["paginate_list"]


def view(items):
    return [item.identifier for item in items]


def run_filter(manager, sort=None, reverse=False, comparison=None, **filters):
    try:
        return {"items": view(manager.get_filtered(sort, reverse, comparison, **filters))}
    except Exception as error:
        return {"error": type(error).__name__, "message": str(error)}


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
    manager_type, paginate = load_contract(args.sdk_root)
    manager = manager_type()
    manager._sources = {
        "b": SimpleNamespace(identifier="b", rowid=2, status="stopped", file_name="beta",
            added_on=20, claim_id="claim-b", outpoint="tx-b:1", channel_claim_id=None),
        "a": SimpleNamespace(identifier="a", rowid=1, status="running", file_name="alpha",
            added_on=10, claim_id="claim-a", outpoint="tx-a:0", channel_claim_id="channel-a"),
    }
    cases = {
        "default": run_filter(manager, "rowid", False, "eq"),
        "reverse": run_filter(manager, "rowid", True, "eq"),
        "status": run_filter(manager, "rowid", False, "eq", status="stopped"),
        "greater": run_filter(manager, "rowid", False, "g", added_on=10),
        "claim_set": run_filter(manager, "rowid", False, "ne", claim_id=["claim-a"]),
        "full_status": run_filter(manager, "rowid", False, "eq", full_status=True),
        "invalid_sort": run_filter(manager, "wrong", False, "eq"),
        "invalid_comparison": run_filter(manager, "rowid", False, "wat"),
        "invalid_search": run_filter(manager, "rowid", False, "eq", wrong=1),
    }
    pagination = {
        "default": paginate(["a", "b"], None, None),
        "clamped": paginate(["a", "b"], 0, -2),
        "past_end": paginate(["a", "b"], 5, 1),
    }
    print(json.dumps({
        "reference": {"commit": commit, "source_manager_sha256": SOURCE_MANAGER_SHA256,
                      "daemon_sha256": DAEMON_SHA256},
        "cases": cases, "pagination": pagination,
    }, sort_keys=True))


if __name__ == "__main__":
    main()

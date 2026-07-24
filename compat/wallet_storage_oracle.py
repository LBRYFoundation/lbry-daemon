#!/usr/bin/env python3
"""Execute pinned WalletStorage and TimestampedPreferences methods in isolation."""

import argparse
import ast
from collections import UserDict
import hashlib
from hashlib import sha256
import json
from pathlib import Path
import os
import stat
import sys
import tempfile


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/wallet/wallet.py": "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
}


class FixtureClock:
    def __init__(self):
        self.now = 0

    def time(self):
        return self.now


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
        if not isinstance(node, ast.Assign):
            continue
        if any(isinstance(target, ast.Name) and target.id == "__version__" for target in node.targets):
            return ast.literal_eval(node.value)
    raise RuntimeError("could not read SDK version")


def load_contract(sdk_root):
    source_path = sdk_root / "lbry" / "wallet" / "wallet.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    wanted = {"TimestampedPreferences", "WalletStorage"}
    classes = [
        node for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name in wanted
    ]
    if {node.name for node in classes} != wanted:
        raise RuntimeError("wallet storage classes changed")

    clock = FixtureClock()
    namespace = {
        "UserDict": UserDict,
        "json": json,
        "os": os,
        "sha256": sha256,
        "stat": stat,
        "time": clock,
    }
    module = ast.fix_missing_locations(ast.Module(body=classes, type_ignores=[]))
    exec(compile(module, str(source_path), "exec"), namespace)  # pylint: disable=exec-used
    storage = namespace["WalletStorage"]
    preferences = namespace["TimestampedPreferences"]
    return storage, preferences, clock


def capture_error(function):
    try:
        return function(), None, None
    except Exception as error:  # pylint: disable=broad-except
        return None, type(error).__name__, str(error)


def run_storage_case(storage_class, case):
    with tempfile.TemporaryDirectory(prefix="lbry-wallet-storage-oracle-") as directory:
        with_path = case.get("with_path", True)
        path = Path(directory) / "wallet.json" if with_path else None
        if "initial" in case:
            path.write_text(case["initial"], encoding="utf-8")
            os.chmod(path, case.get("initial_mode", 0o600))

        if "default" in case:
            storage = storage_class(None if path is None else str(path), case["default"])
        else:
            storage = storage_class(None if path is None else str(path))

        action = case.get("action", "read")
        if action == "read":
            result, error_type, error = capture_error(storage.read)
        elif action == "write":
            result, error_type, error = capture_error(lambda: storage.write(case.get("value")))
        else:
            raise ValueError(f"unknown storage action: {action}")

        final_exists = path is not None and path.exists()
        final_contents = path.read_text(encoding="utf-8") if final_exists and path.is_file() else None
        final_mode = stat.S_IMODE(path.stat().st_mode) if final_exists else None
        temp_exists = bool(list(Path(directory).glob("wallet.json.tmp.*")))
        return {
            "name": case.get("name", ""),
            "result": result,
            "error_type": error_type,
            "error": error,
            "final_exists": final_exists,
            "final_contents": final_contents,
            "final_mode": final_mode,
            "temp_exists": temp_exists,
        }


def run_preference_case(preference_class, clock, case):
    initial = case.get("initial")
    preferences = preference_class(initial)
    gets = []
    error_type = None
    error = None
    try:
        for operation in case.get("operations", []):
            action = operation["action"]
            if action == "set":
                clock.now = operation["time"]
                preferences[operation["key"]] = operation.get("value")
            elif action == "merge":
                preferences.merge(operation["value"])
            elif action == "get":
                gets.append({
                    "key": operation["key"],
                    "value": preferences[operation["key"]],
                })
            else:
                raise ValueError(f"unknown preference action: {action}")
    except Exception as caught:  # pylint: disable=broad-except
        error_type = type(caught).__name__
        error = str(caught)

    without_timestamps = None
    representation = None
    preference_hash = None
    if error_type is None:
        try:
            without_timestamps = preferences.to_dict_without_ts()
            representation = repr(preferences)
            preference_hash = preferences.hash.hex()
        except Exception as caught:  # pylint: disable=broad-except
            error_type = type(caught).__name__
            error = str(caught)

    return {
        "name": case.get("name", ""),
        "data": preferences.data,
        "key_order": list(preferences.data),
        "entry_key_order": {
            key: list(value) if isinstance(value, dict) else None
            for key, value in preferences.data.items()
        },
        "without_timestamps": without_timestamps,
        "repr": representation,
        "hash": preference_hash,
        "gets": gets,
        "error_type": error_type,
        "error": error,
    }


def run(sdk_root, payload):
    verify_pinned_sources(sdk_root)
    version = read_sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    storage, preferences, clock = load_contract(sdk_root)
    return {
        "reference": {"commit": PINNED_COMMIT, "version": version},
        "metadata": {"latest_version": storage.LATEST_VERSION},
        "storage_cases": [
            run_storage_case(storage, case)
            for case in payload.get("storage_cases", [])
        ],
        "preference_cases": [
            run_preference_case(preferences, clock, case)
            for case in payload.get("preference_cases", [])
        ],
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

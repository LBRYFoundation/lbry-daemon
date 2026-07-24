#!/usr/bin/env python3
"""Source-pinned live SPV header synchronization contract for SDK 0.113.0."""

import argparse
import ast
import asyncio
from binascii import unhexlify
import hashlib
import json
from pathlib import Path
import subprocess
import sys
from typing import NamedTuple


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/wallet/database.py": "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
    "lbry/wallet/header.py": "139376a70a383bb8b265b377b50abc959e370f7d7678614c938ab3ac65824a54",
    "lbry/wallet/ledger.py": "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
    "lbry/wallet/network.py": "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
    "lbry/wallet/stream.py": "969e237aedd3003d7d4f9d580bea108ae525f9f6deba126c04803f9173d2f461",
    "tests/integration/blockchain/test_blockchain_reorganization.py": "aa321de0af611cc7217dc1908baa28e2b049842bce13d7393c6a501429bef1cb",
    "tests/unit/wallet/test_ledger.py": "045a14bc252c0b9b6759d7444e582c5e17c6009689f5ee1fd05e74739711ab88",
}


class BlockHeightEvent(NamedTuple):
    height: int
    change: int


class NullLog:
    def warning(self, *_args, **_kwargs):
        return None


class FakeHeaders:
    def __init__(self, length, connect_results):
        self.length = length
        self.connect_results = list(connect_results)
        self.connects = []

    def __len__(self):
        return self.length

    @property
    def height(self):
        return self.length - 1

    async def connect(self, start, raw):
        self.connects.append({"start": start, "hex": raw.hex()})
        result = self.connect_results.pop(0)
        if result > 0:
            self.length = max(self.length, start + result)
        return result


class FakeNetwork:
    def __init__(self, responses):
        self.responses = {height: list(values) for height, values in responses.items()}
        self.requests = []

    async def get_headers(self, height, count):
        self.requests.append({"height": height, "count": count})
        return self.responses[height].pop(0)

    async def retriable_call(self, function, *args, **kwargs):
        return await function(*args, **kwargs)


class FakeController:
    def __init__(self):
        self.events = []

    def add(self, event):
        self.events.append(list(event))


class FakeDatabase:
    def __init__(self):
        self.rewinds = []

    async def rewind_blockchain(self, height):
        self.rewinds.append(height)
        return True


class FakeCache:
    def __init__(self):
        self.clears = 0

    def clear(self):
        self.clears += 1


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


def extract_method(sdk_root, class_name, method_name):
    path = sdk_root / "lbry" / "wallet" / "ledger.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    owner = next(
        node for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == class_name
    )
    return path, next(
        node for node in owner.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name == method_name
    )


def load_update_headers(sdk_root):
    path, method = extract_method(sdk_root, "Ledger", "update_headers")
    module = ast.fix_missing_locations(ast.Module(body=[method], type_ignores=[]))
    namespace = {
        "BlockHeightEvent": BlockHeightEvent,
        "log": NullLog(),
        "unhexlify": unhexlify,
    }
    exec(compile(module, str(path), "exec"), namespace)
    return namespace["update_headers"]


def make_ledger(length, connect_results, responses):
    ledger = type("ProbeLedger", (), {})()
    ledger.headers = FakeHeaders(length, connect_results)
    ledger.network = FakeNetwork(responses)
    ledger._on_header_controller = FakeController()
    ledger.db = FakeDatabase()
    ledger._tx_cache = FakeCache()
    ledger.get_id = lambda: "lbc_mainnet"
    return ledger


async def run_case(update_headers, name, length, connect_results, responses, call):
    ledger = make_ledger(length, connect_results, responses)
    error_type = None
    error_message = None
    try:
        await update_headers(ledger, **call)
    except Exception as error:  # pylint: disable=broad-except
        error_type = type(error).__name__
        error_message = str(error)
    return {
        "name": name,
        "requests": ledger.network.requests,
        "connects": ledger.headers.connects,
        "events": ledger._on_header_controller.events,
        "rewinds": ledger.db.rewinds,
        "cache_clears": ledger._tx_cache.clears,
        "final_length": len(ledger.headers),
        "error_type": error_type,
        "error_message": error_message,
    }


async def cases(update_headers):
    return [
        await run_case(
            update_headers,
            "initial catch-up",
            2,
            [2],
            {2: [{"hex": "aabb"}], 4: [{"hex": ""}]},
            {},
        ),
        await run_case(
            update_headers,
            "direct subscription",
            4,
            [1],
            {},
            {"height": 4, "headers": "cc", "subscription_update": True},
        ),
        await run_case(
            update_headers,
            "future subscription gap",
            4,
            [2],
            {4: [{"hex": "ddee"}], 6: [{"hex": ""}]},
            {"height": 6, "headers": "ignored", "subscription_update": True},
        ),
        await run_case(
            update_headers,
            "reorganization",
            5,
            [0, 2],
            {4: [{"hex": "1122"}], 6: [{"hex": ""}]},
            {"height": 5, "headers": "33", "subscription_update": True},
        ),
        await run_case(
            update_headers,
            "genesis rewind failure",
            0,
            [0],
            {},
            {"height": 0, "headers": "44", "subscription_update": True},
        ),
        await run_case(
            update_headers,
            "negative connect failure",
            1,
            [-1],
            {},
            {"height": 1, "headers": "55", "subscription_update": True},
        ),
    ]


def run(sdk_root):
    commit, version = verify_reference(sdk_root)
    update_headers = load_update_headers(sdk_root)
    ledger_source = (sdk_root / "lbry" / "wallet" / "ledger.py").read_text(encoding="utf-8")
    network_source = (sdk_root / "lbry" / "wallet" / "network.py").read_text(encoding="utf-8")
    required_fragments = [
        "async with self._header_processing_lock:",
        "height=header['height'], headers=header['hex'], subscription_update=True",
        "if rewound >= 100:",
    ]
    if any(fragment not in ledger_source for fragment in required_fragments):
        raise RuntimeError("ledger live-header source no longer matches the pinned structure")
    if "[height, count, 0, b64]" not in network_source:
        raise RuntimeError("network header request source no longer matches the pinned structure")
    return {
        "reference": {
            "commit": commit,
            "version": version,
            "source_sha256": PINNED_SOURCE_HASHES,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "rpc_method": "blockchain.block.headers",
            "batch_size": 2001,
            "restriction_distance": 100,
            "max_rewind": 100,
            "notification_method": "blockchain.headers.subscribe",
            "notification_serialized": True,
            "database_rewind_is_noop": True,
        },
        "cases": asyncio.run(cases(update_headers)),
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    arguments = parser.parse_args()
    json.dump(run(arguments.sdk_root.resolve()), sys.stdout, sort_keys=True, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

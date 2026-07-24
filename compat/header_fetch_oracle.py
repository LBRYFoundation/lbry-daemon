#!/usr/bin/env python3
"""Source-pinned checkpoint fetch model for the legacy header store."""

import argparse
import ast
import base64
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import zlib


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/wallet/header.py": "139376a70a383bb8b265b377b50abc959e370f7d7678614c938ab3ac65824a54",
    "lbry/wallet/ledger.py": "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
    "lbry/wallet/network.py": "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
}


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


def fixture(seed, length):
    return bytes((seed + index * 37) % 251 for index in range(length))


def hash_header(raw):
    return hashlib.sha256(hashlib.sha256(raw).digest()).digest()[::-1].hex()


def encode_chunk(raw, encoding):
    compressor = zlib.compressobj(level=6, wbits=-15)
    compressed = compressor.compress(raw) + compressor.flush()
    if encoding == "trailing_deflate":
        compressed += b"ignored trailing data"
    encoded = base64.b64encode(compressed).decode("ascii")
    if encoding == "noisy_base64":
        encoded = encoded[:17] + " \n!@#$%^&*()?" + encoded[17:]
    return encoded


def run_case(case):
    raw = fixture(case["seed"], case["length"])
    encoded = encode_chunk(raw, case["encoding"])
    if case["checkpoint"] == "self":
        expected = hash_header(raw)
    elif case["checkpoint"] == "other":
        expected = hash_header(fixture(case["other_seed"], case["length"]))
    else:
        expected = None
    missing = {0} if expected is not None else set()
    stored = None
    error_type = None
    error_message = None
    inflated = None
    try:
        inflated = zlib.decompress(
            base64.b64decode(encoded), wbits=-15, bufsize=600_000
        )
        actual = hash_header(inflated)
        if expected == actual:
            stored = inflated
            missing.discard(0)
        elif expected is None:
            pass
        else:
            raise Exception(
                f"Checkpoint mismatch at height 0. Expected {expected}, "
                f"but got {actual} instead."
            )
    except Exception as error:  # exact built-in fetch exceptions are observable
        error_type = type(error).__name__
        error_message = str(error)
    return {
        "name": case["name"],
        "seed": case["seed"],
        "length": case["length"],
        "height": case["height"],
        "checkpointed": expected is not None,
        "checkpoint_hash": expected,
        "encoding": case["encoding"],
        "encoded": encoded,
        "inflated_length": None if inflated is None else len(inflated),
        "inflated_sha256": None if inflated is None else hashlib.sha256(inflated).hexdigest(),
        "wrote_length": None if stored is None else len(stored),
        "wrote_sha256": None if stored is None else hashlib.sha256(stored).hexdigest(),
        "missing_after": sorted(missing, reverse=True),
        "error_type": error_type,
        "error_message": error_message,
        "hardened_divergence": case.get("hardened_divergence", False),
    }


def run(sdk_root):
    commit, version = verify_reference(sdk_root)
    cases = [
        {"name": "matching checkpoint", "seed": 7, "length": 112_000, "height": 321,
         "checkpoint": "self", "encoding": "ordinary"},
        {"name": "permissive base64", "seed": 11, "length": 112_000, "height": 999,
         "checkpoint": "self", "encoding": "noisy_base64"},
        {"name": "trailing deflate data", "seed": 13, "length": 112_000, "height": 1,
         "checkpoint": "self", "encoding": "trailing_deflate"},
        {"name": "checkpoint mismatch", "seed": 17, "other_seed": 19,
         "length": 112_000, "height": 500, "checkpoint": "other", "encoding": "ordinary"},
        {"name": "noncheckpoint discard", "seed": 23, "length": 112_000,
         "height": 1777, "checkpoint": None, "encoding": "ordinary"},
        {"name": "legacy short write", "seed": 29, "length": 111_999, "height": 0,
         "checkpoint": "self", "encoding": "ordinary", "hardened_divergence": True},
        {"name": "legacy oversized write", "seed": 31, "length": 112_001, "height": 0,
         "checkpoint": "self", "encoding": "ordinary", "hardened_divergence": True},
    ]
    return {
        "reference": {
            "commit": commit,
            "version": version,
            "source_sha256": PINNED_SOURCE_HASHES,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "python_assertions": __debug__,
            "raw_deflate": True,
            "base64_validate": False,
            "zlib_bufsize_is_limit": False,
            "rpc_method": "blockchain.block.headers",
            "rpc_params_tail": [1000, 0, True],
            "restriction_distance": 100,
            "response_key": "base64",
        },
        "cases": [run_case(case) for case in cases],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    arguments = parser.parse_args()
    json.dump(run(arguments.sdk_root.resolve()), sys.stdout, sort_keys=True, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

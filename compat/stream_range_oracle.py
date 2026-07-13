#!/usr/bin/env python3
"""Execute ManagedStream range preparation from the pinned Python SDK."""

import argparse
import ast
from hashlib import sha256
import json
import logging
from pathlib import Path
import subprocess
from types import SimpleNamespace
import typing


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
SOURCE_SHA256 = "4e6562664b984a3f489f55cc0de8ebc0330de6481bc71b0900e7cd121dcb179f"
MAX_BLOB_SIZE = 2 * 2 ** 20


class HTTPRequestRangeNotSatisfiable(Exception):
    pass


def load_method(sdk_root):
    path = sdk_root / "lbry" / "stream" / "managed_stream.py"
    if sha256(path.read_bytes()).hexdigest() != SOURCE_SHA256:
        raise RuntimeError("managed_stream.py does not match the pinned SDK")
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    source_class = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "ManagedStream")
    method = next(node for node in source_class.body if isinstance(node, ast.FunctionDef) and node.name == "_prepare_range_response_headers")
    oracle = ast.ClassDef("OracleStream", [], [], [method], [])
    module = ast.fix_missing_locations(ast.Module(body=[oracle], type_ignores=[]))
    namespace = {
        "typing": typing, "MAX_BLOB_SIZE": MAX_BLOB_SIZE,
        "HTTPRequestRangeNotSatisfiable": HTTPRequestRangeNotSatisfiable,
        "log": logging.getLogger("oracle"),
    }
    exec(compile(module, str(path), "exec"), namespace)  # pylint: disable=exec-used
    return namespace["OracleStream"]


def run_case(stream_type, header, lengths):
    stream = stream_type()
    stream.descriptor = SimpleNamespace(
        blobs=[SimpleNamespace(length=length) for length in lengths] + [SimpleNamespace(length=0)]
    )
    stream.stream_claim_info = None
    stream.mime_type = "video/mp4"
    try:
        headers, size, skip_blobs, first_offset = stream._prepare_range_response_headers(header)
        return {
            "headers": headers, "size": size,
            "skip_blobs": skip_blobs, "first_offset": first_offset,
        }
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
    stream_type = load_method(args.sdk_root)
    cases = {
        "default": ("bytes=0-", [11, 6]),
        "bounded": ("bytes=3-7", [11, 6]),
        "second_blob": ("bytes=4194303-4194306", [4194304, 20]),
        "start_at_size": ("bytes=15-", [11, 6]),
        "end_past_size": ("bytes=3-15", [11, 6]),
    }
    print(json.dumps({
        "reference": {"commit": commit, "source_sha256": SOURCE_SHA256},
        "cases": {name: run_case(stream_type, header, lengths) for name, (header, lengths) in cases.items()},
    }, sort_keys=True))


if __name__ == "__main__":
    main()

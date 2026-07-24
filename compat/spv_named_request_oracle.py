#!/usr/bin/env python3
"""Pinned offline oracle for positional and named SPV JSON-RPC requests.

The probe AST-executes the request construction methods from SDK 0.113.0.
It imports no SDK modules and performs no network I/O.
"""

import argparse
import ast
import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py":
        "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/wallet/network.py":
        "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
    "lbry/wallet/rpc/jsonrpc.py":
        "6da90b83bdb2e192929abddbb8b33824eac7d24f7ab126c1942db5ed6b7c1269",
}
PINNED_METHOD_HASHES = {
    "JSONRPC.encode_payload":
        "f7dbd676c1b644cd5d39ffad9ca717a2f77e6215ad1ec53ca9727487b5207112",
    "JSONRPCv2.request_payload":
        "8b734a26b7b76f85e71c85e12f7d0b01c1946c8bd8746ec1bb5273ecfc3be5a9",
    "Network.claim_search":
        "8ffee30536ef5354fc5c6028eb06b70512ce0f2ed09bf9567edb8689a4957968",
    "SingleRequest.__init__":
        "dc1aeed643cb1d3f02872ded23fe1d0c3870c27a3d36c8a8b388646dac7d749a",
}


def source_version(sdk_root):
    path = sdk_root / "lbry/__init__.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    for node in tree.body:
        if isinstance(node, ast.Assign) and any(
            isinstance(target, ast.Name) and target.id == "__version__"
            for target in node.targets
        ):
            return ast.literal_eval(node.value)
    raise RuntimeError("could not read SDK version")


def verify_source(sdk_root):
    commit = subprocess.run(
        ["git", "-C", str(sdk_root), "rev-parse", "HEAD"],
        check=True, capture_output=True, text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    version = source_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    for relative, expected in PINNED_SOURCE_HASHES.items():
        actual = hashlib.sha256((sdk_root / relative).read_bytes()).hexdigest()
        if actual != expected:
            raise RuntimeError(f"{relative} hash is {actual}, expected {expected}")
    return commit, version


def selected_method(path, class_name, method_name):
    source = path.read_text(encoding="utf-8")
    source_class = next(
        node for node in ast.parse(source, filename=str(path)).body
        if isinstance(node, ast.ClassDef) and node.name == class_name
    )
    method = next(
        node for node in source_class.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name == method_name
    )
    digest = hashlib.sha256(ast.get_source_segment(source, method).encode()).hexdigest()
    return copy.deepcopy(method), digest


def extract_sdk_slice(sdk_root):
    jsonrpc_path = sdk_root / "lbry/wallet/rpc/jsonrpc.py"
    network_path = sdk_root / "lbry/wallet/network.py"
    request_init, request_init_hash = selected_method(
        jsonrpc_path, "SingleRequest", "__init__",
    )
    encode_payload, encode_payload_hash = selected_method(
        jsonrpc_path, "JSONRPC", "encode_payload",
    )
    request_payload, request_payload_hash = selected_method(
        jsonrpc_path, "JSONRPCv2", "request_payload",
    )
    claim_search, claim_search_hash = selected_method(
        network_path, "Network", "claim_search",
    )
    hashes = {
        "JSONRPC.encode_payload": encode_payload_hash,
        "JSONRPCv2.request_payload": request_payload_hash,
        "Network.claim_search": claim_search_hash,
        "SingleRequest.__init__": request_init_hash,
    }
    if hashes != PINNED_METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {PINNED_METHOD_HASHES}")

    request_class = ast.ClassDef(
        "PinnedRequest", [], [], [request_init], [],
    )
    protocol_class = ast.ClassDef(
        "PinnedProtocol", [], [], [encode_payload, request_payload], [],
    )
    network_class = ast.ClassDef(
        "PinnedNetwork", [ast.Name("NetworkProbe", ast.Load())], [], [claim_search], [],
    )
    module = ast.fix_missing_locations(ast.Module(
        body=[request_class, protocol_class, network_class], type_ignores=[],
    ))

    class NetworkProbe:

        def rpc(self, method, args, restricted=True, session_override=None):
            return method, args, restricted, session_override

    namespace = {"json": json, "NetworkProbe": NetworkProbe}
    exec(compile(module, str(jsonrpc_path), "exec"), namespace)
    namespace["PinnedProtocol"].INTERNAL_ERROR = -32603
    return namespace, hashes


def request_case(protocol, request_class, name, method, request_id, args, kind):
    request = request_class(method, args)
    payload = protocol.request_payload(request, request_id)
    encoded = protocol.encode_payload(payload).decode("utf-8") + "\n"
    return {
        "name": name,
        "kind": kind,
        "method": method,
        "id": request_id,
        "params_present": "params" in payload,
        "encoded": encoded,
    }


def run(sdk_root):
    commit, version = verify_source(sdk_root)
    namespace, method_hashes = extract_sdk_slice(sdk_root)
    protocol = namespace["PinnedProtocol"]
    request_class = namespace["PinnedRequest"]

    claim_args = {
        "text": "open source",
        "claim_type": ["stream", "repost"],
        "channel_ids": ["a" * 40],
        "not_channel_ids": ["b" * 40],
        "order_by": ["effective_amount", "^height"],
        "page": 2,
        "page_size": 20,
        "no_totals": True,
    }
    network = namespace["PinnedNetwork"]()
    method, network_args, restricted, session_override = network.claim_search(**claim_args)
    if restricted is not False or session_override is not None or network_args != claim_args:
        raise RuntimeError("pinned Network.claim_search forwarding changed")

    cases = [
        request_case(
            protocol, request_class, "positional_nonempty", "server.version", 1,
            ["LBRY SDK 0.113.0", "0.113.0"], "positional",
        ),
        request_case(
            protocol, request_class, "positional_empty_list", "server.features", 2,
            [], "positional",
        ),
        request_case(
            protocol, request_class, "named_empty_object", "fixture.empty", 3,
            {}, "named",
        ),
        request_case(
            protocol, request_class, "claimtrie_search_named", method, 4,
            network_args, "named",
        ),
        request_case(
            protocol, request_class, "named_nested_values", "fixture.nested", 5,
            {
                "filters": {
                    "heights": [">=100", "<200"],
                    "flags": [True, False, None],
                },
                "locations": [{"country": "PL", "city": "Lodz"}],
                "label": "snowman \u2603 and newline\n",
            },
            "named",
        ),
        request_case(
            protocol, request_class, "named_nonfinite_floats", "fixture.special", 6,
            {
                "nan": float("nan"),
                "nested": {
                    "values": [float("inf"), float("-inf"), 1.25],
                    "literal": "Infinity",
                },
            },
            "named",
        ),
        request_case(
            protocol, request_class, "positional_nonfinite_floats", "fixture.special", 7,
            [float("nan"), {"values": [float("inf"), float("-inf")]}],
            "positional",
        ),
    ]
    return {
        "reference": {
            "commit": commit,
            "version": version,
            "source_sha256": PINNED_SOURCE_HASHES,
            "method_sha256": method_hashes,
        },
        "cases": cases,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    arguments = parser.parse_args()
    json.dump(run(arguments.sdk_root.resolve()), sys.stdout, sort_keys=True, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Source-pinned JSON-RPC and SPV network contract for SDK 0.113.0."""

import argparse
import ast
import hashlib
import json
from pathlib import Path
import subprocess
import sys


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/wallet/network.py": "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
    "lbry/wallet/rpc/session.py": "1e4b2a1e49b6bc55f0978e2cb90d98b65533722760190556a60bed347f2eaed7",
    "lbry/wallet/rpc/jsonrpc.py": "6da90b83bdb2e192929abddbb8b33824eac7d24f7ab126c1942db5ed6b7c1269",
    "lbry/wallet/rpc/framing.py": "8e18f9fe4c05344124ef92806ecba4563f979285303ef9382cbd0fdef943e0d6",
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


def request_payload(method, request_id, params):
    payload = {"jsonrpc": "2.0", "method": method, "id": request_id}
    if params or params == {}:
        payload["params"] = params
    return payload


def request_case(method, request_id, params):
    payload = request_payload(method, request_id, params)
    return {
        "method": method,
        "id": request_id,
        "params": params,
        "params_present": "params" in payload,
        "encoded": json.dumps(payload) + "\n",
    }


def version_case(version):
    try:
        compatible = tuple(int(piece) for piece in version.split(".")) >= (0, 65, 0)
        error_type = None
        error_message = None
    except Exception as error:
        compatible = None
        error_type = type(error).__name__
        error_message = str(error)
    return {
        "version": version,
        "compatible": compatible,
        "error_type": error_type,
        "error_message": error_message,
    }


def run(sdk_root):
    commit, version = verify_reference(sdk_root)
    network_source = (sdk_root / "lbry" / "wallet" / "network.py").read_text(
        encoding="utf-8"
    )
    if "asyncio.sleep(30)" not in network_source or "sleep_delay *= 2" not in network_source:
        raise RuntimeError("network retry-loop source no longer has the pinned fixed-delay quirk")
    requests = [
        request_case("server.version", 0, ["LBRY SDK 0.113.0", "0.113.0"]),
        request_case("server.features", 1, []),
        request_case("server.peers.subscribe", 2, []),
        request_case("blockchain.headers.subscribe", 3, [True]),
        request_case("blockchain.block.headers", 4, [9000, 1000, 0, True]),
    ]
    return {
        "reference": {
            "commit": commit,
            "version": version,
            "source_sha256": PINNED_SOURCE_HASHES,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "client_name": "LBRY SDK 0.113.0",
            "protocol_minimum": [0, 65, 0],
            "protocol_maximum": "0.113.0",
            "connect_timeout_seconds": 6,
            "version_timeout_seconds": 3,
            "request_timeout_seconds": 30,
            "concurrency": 32,
            "newline_framing": True,
            "jsonrpc": "2.0",
            "legacy_max_frame_size": 1 << 32,
            "effective_reconnect_seconds": 30,
            "sleep_delay_variable_unused": True,
            "retry_exceptions": ["asyncio.TimeoutError", "ConnectionError"],
            "restricted_is_inert": True,
            "handshake_methods": [case["method"] for case in requests[:4]],
        },
        "requests": requests,
        "versions": [
            version_case("0.64.999"),
            version_case("0.65"),
            version_case("0.65.0"),
            version_case("0.65.0.1"),
            version_case("1.0.0"),
            version_case("not.a.version"),
        ],
        "responses": [
            {
                "name": "result",
                "payload": {"jsonrpc": "2.0", "result": {"height": 42}, "id": 0},
                "result": {"height": 42},
                "error_type": None,
                "error_message": None,
            },
            {
                "name": "rpc error",
                "payload": {
                    "jsonrpc": "2.0",
                    "error": {"code": -32602, "message": "bad arguments"},
                    "id": 0,
                },
                "result": None,
                "error_type": "RPCError",
                "error_message": "bad arguments",
            },
            {
                "name": "both result and error",
                "payload": {
                    "jsonrpc": "2.0",
                    "result": {},
                    "error": None,
                    "id": 0,
                },
                "result": None,
                "error_type": "ProtocolError",
                "error_message": 'response contains both "result" and "error"',
            },
        ],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    arguments = parser.parse_args()
    json.dump(run(arguments.sdk_root.resolve()), sys.stdout, sort_keys=True, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

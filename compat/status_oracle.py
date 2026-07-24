#!/usr/bin/env python3
"""Execute the pinned daemon's real status RPC method with fixture components."""

import argparse
import ast
import asyncio
import hashlib
import json
from json import JSONEncoder
from datetime import datetime
from decimal import Decimal
from pathlib import Path
import sys
import types


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "setup.py": "1b7b416536ad4f44869216dd794aaa3b41ffd1c9eb1899cdf1799ab50961dae1",
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/extras/daemon/component.py": "9a21fcc5667e97df935513938fe5e7b1992249f45192a0b6342459ebbf477af6",
    "lbry/extras/daemon/components.py": "e1059c789a67c44ec2632bee479afba0bf5091ab1c276afcc9e4fefcbbc68659",
    "lbry/extras/daemon/daemon.py": "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
    "lbry/extras/daemon/json_response_encoder.py": "047fd406c20236025414b8805669b1a830b0b412386c1613498aa1ebaa021732",
}


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


def load_class_method(source_path, class_name, method_name, namespace=None):
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    class_node = next(
        node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == class_name
    )
    method_node = next(
        node for node in class_node.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name == method_name
    )
    method_module = ast.Module(body=[method_node], type_ignores=[])
    ast.fix_missing_locations(method_module)
    namespace = dict(namespace or {})
    exec(compile(method_module, str(source_path), "exec"), namespace)
    return namespace[method_name]


def load_status_contract(sdk_root):
    daemon_path = sdk_root / "lbry" / "extras" / "daemon" / "daemon.py"
    component_path = sdk_root / "lbry" / "extras" / "daemon" / "component.py"
    components_path = sdk_root / "lbry" / "extras" / "daemon" / "components.py"
    encoder_path = sdk_root / "lbry" / "extras" / "daemon" / "json_response_encoder.py"
    return {
        "status": load_class_method(daemon_path, "Daemon", "jsonrpc_status"),
        "blob_manager": load_class_method(components_path, "BlobComponent", "get_status"),
        "dht": load_class_method(
            components_path, "DHTComponent", "get_status", {"binascii": __import__("binascii")}
        ),
        "peer_protocol_server": load_class_method(component_path, "Component", "get_status"),
        "upnp": load_class_method(
            components_path, "UPnPComponent", "get_status", {"aioupnp_version": "0.0.18"}
        ),
        "encoder": load_json_response_encoder(encoder_path),
    }


def load_json_response_encoder(source_path):
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    encoder_node = next(
        node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "JSONResponseEncoder"
    )
    encoder_module = ast.Module(body=[encoder_node], type_ignores=[])
    ast.fix_missing_locations(encoder_module)
    placeholder_names = (
        "Account", "Wallet", "Ledger", "ManagedStream", "TorrentSource", "Transaction", "Output",
        "Claim", "Support", "PublicKey",
    )
    namespace = {
        "JSONEncoder": JSONEncoder,
        "Decimal": Decimal,
        "datetime": datetime,
        **{name: type(name, (), {}) for name in placeholder_names},
    }
    exec(compile(encoder_module, str(source_path), "exec"), namespace)
    return namespace["JSONResponseEncoder"]


class FixtureAnalyzer:
    def __init__(self, status):
        self._status = status

    async def status(self):
        return self._status


class FixtureComponent:
    def __init__(self, name, status):
        self.component_name = name
        self._status = status

    async def get_status(self):
        return self._status


class FixtureComponentManager:
    def __init__(self, startup_status, skipped_components, component_status, component_fixtures, methods):
        self._startup_status = startup_status
        self.skip_components = list(skipped_components or [])
        self.components = []
        for name in startup_status:
            fixture = component_fixtures.get(name)
            if fixture is None:
                self.components.append(FixtureComponent(name, component_status.get(name, {})))
            else:
                self.components.append(make_real_component(name, fixture, methods))

    def get_components_status(self):
        return self._startup_status


def make_real_component(name, fixture, methods):
    component_type = type(
        f"Fixture{name.title().replace('_', '')}",
        (),
        {"component_name": name, "get_status": methods[name]},
    )
    component = component_type()
    if name == "blob_manager":
        if fixture.get("started", False):
            connection_manager = types.SimpleNamespace(status=fixture.get("connections", {}))
            component.blob_manager = types.SimpleNamespace(
                completed_blob_hashes=[None] * fixture.get("finished_blobs", 0),
                connection_manager=connection_manager,
            )
        else:
            component.blob_manager = None
    elif name == "dht":
        node_id = fixture.get("node_id")
        if node_id is None:
            component.dht_node = None
        else:
            peers = [object()] * fixture.get("peers_in_routing_table", 0)
            routing_table = types.SimpleNamespace(get_peers=lambda: peers)
            protocol = types.SimpleNamespace(node_id=bytes.fromhex(node_id), routing_table=routing_table)
            component.dht_node = types.SimpleNamespace(protocol=protocol)
    elif name == "upnp":
        component.upnp_redirects = fixture.get("redirects", {})
        gateway = fixture.get("gateway")
        component.upnp = None if gateway is None else types.SimpleNamespace(
            gateway=types.SimpleNamespace(manufacturer_string=gateway)
        )
        component.external_ip = fixture.get("external_ip")
    return component


async def execute_case(contract, fixture):
    daemon_type = type("StatusOracleDaemon", (), {"jsonrpc_status": contract["status"]})
    daemon = daemon_type()
    daemon.installation_id = fixture["installation_id"]
    daemon._video_file_analyzer = FixtureAnalyzer(fixture["ffmpeg_status"])
    daemon.component_manager = FixtureComponentManager(
        fixture["startup_status"],
        fixture["skipped_components"],
        fixture.get("component_status", {}),
        fixture.get("component_fixtures", {}),
        contract,
    )
    response = await daemon.jsonrpc_status()
    encoded = json.dumps(response, cls=contract["encoder"], ledger=None, sort_keys=True)
    return json.loads(encoded)


def run(sdk_root, payload):
    verify_pinned_sources(sdk_root)
    version = read_sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    contract = load_status_contract(sdk_root)
    responses = [
        asyncio.run(execute_case(contract, fixture))
        for fixture in payload.get("cases", [])
    ]
    return {
        "reference": {"commit": PINNED_COMMIT, "version": version},
        "responses": responses,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    arguments = parser.parse_args()
    payload = json.load(sys.stdin)
    result = run(arguments.sdk_root.resolve(), payload)
    json.dump(result, sys.stdout, sort_keys=True, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

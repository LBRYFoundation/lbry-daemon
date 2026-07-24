#!/usr/bin/env python3
"""Source-pinned UDP selection and known-hub contract for SDK 0.113.0."""

import argparse
import ast
import hashlib
import json
from pathlib import Path
import struct
import subprocess
import sys
from typing import Dict, List, NamedTuple, Tuple


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/conf.py": "ddedb9961723e67387fde0e02f7308fc6725f682802e1c3ec9030f6ccceac3e5",
    "lbry/schema/attrs.py": "e2c01abf8a152ca224f557d38a4932b40ce0ceb880c27b2dbe0bca15c4a51624",
    "lbry/schema/types/v2/claim_pb2.py": "3edb36895d7d2f294e27019438332ca8a7ed4cb3c0f30ee33c9aa406bf000c98",
    "lbry/utils.py": "831e7d0062a9beb952a25be28f2b4ff58a721ff9c0b62ddc1d5a5e4d3a1b52d1",
    "lbry/wallet/network.py": "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
    "lbry/wallet/udp.py": "0520ffc127ddcc1285e4964ae995a6b5d36c42ad824d0ceb454e930e2750c094",
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


def assigned_name(node, name):
    return isinstance(node, ast.Assign) and any(
        isinstance(target, ast.Name) and target.id == name for target in node.targets
    )


def country_values(sdk_root):
    path = sdk_root / "lbry" / "schema" / "types" / "v2" / "claim_pb2.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    assignment = next(node for node in tree.body if assigned_name(node, "_LOCATION_COUNTRY"))
    values_node = next(keyword.value for keyword in assignment.value.keywords if keyword.arg == "values")
    values = []
    for enum_call in values_node.elts:
        keywords = {keyword.arg: ast.literal_eval(keyword.value) for keyword in enum_call.keywords}
        values.append((keywords["name"], keywords["number"]))
    if [number for _, number in values] != list(range(len(values))):
        raise RuntimeError("country enum is not contiguous")
    return values


def load_udp_contract(sdk_root, countries):
    path = sdk_root / "lbry" / "wallet" / "udp.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    wanted_assignments = {"_MAGIC", "_PAD_BYTES", "PROTOCOL_VERSION", "PONG_ENCODING"}
    body = [
        node for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name in {"SPVPing", "SPVPong"}
        or isinstance(node, ast.Assign) and any(assigned_name(node, name) for name in wanted_assignments)
    ]
    by_name = {name: number for name, number in countries}
    by_number = {number: name for name, number in countries}

    def country_str_to_int(name):
        lookup = "R" + name if len(name) == 3 else name
        return by_name[lookup]

    def country_int_to_str(number):
        name = by_number[number]
        return name[1:] if name.startswith("R") else name

    namespace = {
        "NamedTuple": NamedTuple,
        "Tuple": Tuple,
        "country_int_to_str": country_int_to_str,
        "country_str_to_int": country_str_to_int,
        "struct": struct,
    }
    module = ast.fix_missing_locations(ast.Module(body=body, type_ignores=[]))
    exec(compile(module, str(path), "exec"), namespace)
    return namespace["SPVPing"], namespace["SPVPong"]


def load_known_hubs_contract(sdk_root):
    path = sdk_root / "lbry" / "conf.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    class_node = next(
        node for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "KnownHubsList"
    )
    namespace = {"Dict": Dict, "List": List, "Tuple": Tuple}
    module = ast.fix_missing_locations(ast.Module(body=[class_node], type_ignores=[]))
    exec(compile(module, str(path), "exec"), namespace)
    return namespace["KnownHubsList"]


def fresh_known_hubs(known_hubs_class):
    known = known_hubs_class.__new__(known_hubs_class)
    known.file_name = "known_hubs.yml"
    known.path = None
    known.hubs = {}
    return known


def serialized_hubs(known):
    return [
        {"host": host, "port": port, "details": details}
        for (host, port), details in known.items()
    ]


def known_hub_cases(known_hubs_class):
    known = fresh_known_hubs(known_hubs_class)
    first = known.set("first:99", {"country": "US", "tier": "paid"})
    duplicate = known.set("first:99", {"country": "KP"})
    ignored = known.set("too:many:colons", {})
    underscore = known.set("underscore:1_2", {})
    known.set("missing:13", {})
    filtered = known.filter(False, country="US", tier="absent")
    match_none = known.filter(True, country="US")

    partial = fresh_known_hubs(known_hubs_class)
    error_type = None
    try:
        partial.add_hubs(["partial:1", "broken:not-a-port", "later:2"])
    except Exception as error:  # pylint: disable=broad-except
        error_type = type(error).__name__

    numeric = fresh_known_hubs(known_hubs_class)
    numeric.set("number:14", {"value": 1})
    falsy = fresh_known_hubs(known_hubs_class)
    falsy.add_hubs([None, False, 0, [], {}, "good:15"])

    return {
        "first_result": first,
        "duplicate_result": duplicate,
        "ignored_result": ignored,
        "underscore_result": underscore,
        "ordered": serialized_hubs(known),
        "filter_or": serialized_hubs(filtered),
        "filter_match_none": serialized_hubs(match_none),
        "partial_error_type": error_type,
        "partial_after_error": serialized_hubs(partial),
        "numeric_true": serialized_hubs(numeric.filter(False, value=True)),
        "numeric_float": serialized_hubs(numeric.filter(False, value=1.0)),
        "falsy_peer_entries": serialized_hubs(falsy),
    }


def run(sdk_root):
    commit, version = verify_reference(sdk_root)
    countries = country_values(sdk_root)
    ping_class, pong_class = load_udp_contract(sdk_root, countries)
    known_hubs_class = load_known_hubs_contract(sdk_root)
    network_source = (sdk_root / "lbry" / "wallet" / "network.py").read_text(encoding="utf-8")
    required_selection_fragments = [
        "if self.config.get('explicit_servers', []):",
        "elif self.known_hubs:",
        "hubs = self.config['default_servers']",
        "random.choice(list(ip_to_hostnames.keys()))",
        "pong.country_name != self.jurisdiction",
    ]
    if any(fragment not in network_source for fragment in required_selection_fragments):
        raise RuntimeError("network selection source no longer matches the pinned structure")

    ping = ping_class.make()
    nonstandard_ping = struct.pack(b"!lB64s", 1446058291, 255, b"x" * 64)
    decoded_nonstandard = ping_class.decode(nonstandard_ping + b"suffix")
    tip = bytes(range(32))
    available_us = pong_class.make(1, 123456, tip, "203.0.113.7", "US")
    decoded_us = pong_class.decode(available_us + b"suffix")
    flags = []
    for value in (0, 1, 2, 3):
        decoded = pong_class.decode(pong_class.make(value, 1, tip, "1.2.3.4", "KP"))
        flags.append({"flags": value, "available": decoded.available})

    country_names = [name[1:] if name.startswith("R") else name for name, _ in countries]
    return {
        "reference": {
            "commit": commit,
            "version": version,
            "source_sha256": PINNED_SOURCE_HASHES,
        },
        "wire": {
            "magic": 1446058291,
            "protocol_version": 1,
            "ping_size": len(ping),
            "ping_hex": ping.hex(),
            "nonstandard_ping": {
                "version": decoded_nonstandard.protocol_version,
                "padding_hex": decoded_nonstandard.pad_bytes.hex(),
            },
            "pong_size": len(available_us),
            "available_us_hex": available_us.hex(),
            "available_us": {
                "version": decoded_us.protocol_version,
                "flags": decoded_us.flags,
                "height": decoded_us.height,
                "tip_hex": decoded_us.tip.hex(),
                "source_address": decoded_us.ip_address,
                "country": decoded_us.country,
                "country_name": decoded_us.country_name,
                "available": decoded_us.available,
            },
            "flags": flags,
        },
        "countries": country_names,
        "country_examples": {"FR": countries[76][1], "KP": countries[118][1], "US": countries[236][1]},
        "selection": {
            "source_precedence": ["explicit_servers", "known_hubs", "default_servers"],
            "dns_cache_seconds": 300,
            "probe_timeout_seconds": 3,
            "available_order": "response_arrival",
            "unavailable_completes_probe": False,
            "no_pong_fallback": "random_numeric_ip",
            "fallback_bypasses_jurisdiction": True,
            "jurisdiction_case_sensitive": True,
            "country_updates_saved_immediately": False,
        },
        "known_hubs": known_hub_cases(known_hubs_class),
        "metadata": {"python_version": sys.version.split()[0]},
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    arguments = parser.parse_args()
    json.dump(run(arguments.sdk_root.resolve()), sys.stdout, sort_keys=True, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

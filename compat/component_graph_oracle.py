#!/usr/bin/env python3
"""Extract and stage the pinned SDK component graph without importing the SDK."""

import argparse
import ast
import hashlib
import json
from pathlib import Path
import sys


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/extras/daemon/component.py": "9a21fcc5667e97df935513938fe5e7b1992249f45192a0b6342459ebbf477af6",
    "lbry/extras/daemon/componentmanager.py": "a92e537bb538580549aa4c0456cc1c7981c2fe557f9ee7149cf220a39dc1b3f9",
    "lbry/extras/daemon/components.py": "e1059c789a67c44ec2632bee479afba0bf5091ab1c276afcc9e4fefcbbc68659",
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


def assignment(class_node, wanted):
    for node in class_node.body:
        if not isinstance(node, ast.Assign):
            continue
        if any(isinstance(target, ast.Name) and target.id == wanted for target in node.targets):
            return node.value
    return None


def resolve_string(node, constants):
    if isinstance(node, ast.Name):
        return constants[node.id]
    value = ast.literal_eval(node)
    if not isinstance(value, str):
        raise TypeError(f"expected string, got {value!r}")
    return value


def extract_components(sdk_root):
    source_path = sdk_root / "lbry" / "extras" / "daemon" / "components.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    constants = {}
    for node in tree.body:
        if not isinstance(node, ast.Assign) or len(node.targets) != 1 or not isinstance(node.targets[0], ast.Name):
            continue
        name = node.targets[0].id
        if name.endswith("_COMPONENT") and isinstance(node.value, ast.Constant) and isinstance(node.value.value, str):
            constants[name] = node.value.value

    components = []
    for node in tree.body:
        if not isinstance(node, ast.ClassDef):
            continue
        if not any(isinstance(base, ast.Name) and base.id == "Component" for base in node.bases):
            continue
        name_node = assignment(node, "component_name")
        if name_node is None:
            continue
        component_name = resolve_string(name_node, constants)
        dependency_node = assignment(node, "depends_on")
        dependencies = []
        if dependency_node is not None:
            if not isinstance(dependency_node, (ast.List, ast.Tuple)):
                raise TypeError(f"{node.name}.depends_on is not a list or tuple")
            dependencies = [resolve_string(item, constants) for item in dependency_node.elts]
        components.append({"class": node.name, "name": component_name, "depends_on": dependencies})
    return sorted(components, key=lambda component: component["name"])


def stages_for(components, skipped):
    active = {
        component["name"]: component
        for component in components
        if component["name"] not in set(skipped)
    }
    remaining = set(active)
    staged = set()
    stages = []
    while remaining:
        stage = sorted(
            name for name in remaining
            if all(dependency in staged for dependency in active[name]["depends_on"])
        )
        if not stage:
            return None, sorted(remaining)
        stages.append(stage)
        staged.update(stage)
        remaining.difference_update(stage)
    return stages, None


def run(sdk_root, payload):
    verify_pinned_sources(sdk_root)
    version = read_sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    components = extract_components(sdk_root)
    cases = []
    for skipped in payload.get("skip_cases", []):
        start, unresolved = stages_for(components, skipped)
        stop = list(reversed(start)) if start is not None else None
        cases.append({
            "skipped": skipped,
            "start": start,
            "stop": stop,
            "unresolved": unresolved,
        })
    return {
        "reference": {"commit": PINNED_COMMIT, "version": version},
        "components": components,
        "cases": cases,
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

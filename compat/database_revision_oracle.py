#!/usr/bin/env python3
"""Execute the pinned SDK DatabaseComponent revision preamble in isolation."""

import argparse
import ast
import asyncio
import copy
import hashlib
import json
import logging
import os
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
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


def assigned_to_storage(statement):
    if not isinstance(statement, (ast.Assign, ast.AnnAssign)):
        return False
    targets = statement.targets if isinstance(statement, ast.Assign) else [statement.target]
    return any(
        isinstance(target, ast.Attribute)
        and isinstance(target.value, ast.Name)
        and target.value.id == "self"
        and target.attr == "storage"
        for target in targets
    )


class InjectMigrator(ast.NodeTransformer):
    def visit_ImportFrom(self, node):  # pylint: disable=invalid-name
        if node.module == "lbry.extras.daemon.migrator" and any(
            alias.name == "dbmigrator" for alias in node.names
        ):
            return ast.copy_location(
                ast.Assign(
                    targets=[ast.Name(id="dbmigrator", ctx=ast.Store())],
                    value=ast.Name(id="_dbmigrator", ctx=ast.Load()),
                ),
                node,
            )
        return node


class InlineExecutorLoop:
    def run_in_executor(self, executor, function, *arguments):
        if executor is not None:
            raise AssertionError("DatabaseComponent unexpectedly selected a custom executor")

        async def invoke():
            return function(*arguments)

        return invoke()


def string_join_tail(function_node, method_name):
    for node in ast.walk(function_node):
        if not isinstance(node, ast.Call) or len(node.args) < 2:
            continue
        function = node.func
        if not (
            isinstance(function, ast.Attribute)
            and function.attr == "join"
            and isinstance(function.value, ast.Attribute)
            and function.value.attr == "path"
        ):
            continue
        tail = node.args[-1]
        if isinstance(tail, ast.Constant) and isinstance(tail.value, str):
            return tail.value
    raise RuntimeError(f"could not extract path filename from {method_name}")


def extract_component(sdk_root):
    source_path = sdk_root / "lbry" / "extras" / "daemon" / "components.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    component = next(
        node for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "DatabaseComponent"
    )
    methods = {
        node.name: node
        for node in component.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
    }
    required = {"get_current_db_revision", "revision_filename", "_write_db_revision_file", "start"}
    if not required.issubset(methods):
        raise RuntimeError(f"DatabaseComponent methods changed: {sorted(methods)}")

    current_method = methods["get_current_db_revision"]
    current_return = next(node for node in current_method.body if isinstance(node, ast.Return))
    current_revision = ast.literal_eval(current_return.value)
    if not isinstance(current_revision, int):
        raise TypeError("database revision is not an integer")

    revision_filename = string_join_tail(methods["revision_filename"], "revision_filename")
    sqlite_filename = None
    for node in ast.walk(methods["start"]):
        if not isinstance(node, ast.Call):
            continue
        if isinstance(node.func, ast.Name) and node.func.id == "SQLiteStorage":
            sqlite_filename = string_join_tail(node, "SQLiteStorage")
            break
    if sqlite_filename is None:
        raise RuntimeError("could not extract SQLite filename")

    selected = []
    for name in ("get_current_db_revision", "revision_filename", "_write_db_revision_file"):
        selected.append(copy.deepcopy(methods[name]))

    start = copy.deepcopy(methods["start"])
    start.body = [statement for statement in start.body if not assigned_to_storage(statement)]
    storage_index = next(
        index for index, statement in enumerate(methods["start"].body)
        if assigned_to_storage(statement)
    )
    start.body = copy.deepcopy(methods["start"].body[:storage_index])
    start = InjectMigrator().visit(start)
    selected.append(start)

    isolated_class = ast.ClassDef(
        name="DatabaseComponent",
        bases=[ast.Name(id="Component", ctx=ast.Load())],
        keywords=[],
        body=selected,
        decorator_list=[],
    )
    module = ast.fix_missing_locations(ast.Module(body=[isolated_class], type_ignores=[]))

    class Component:
        pass

    namespace = {
        "Component": Component,
        "asyncio": SimpleNamespace(get_event_loop=lambda: InlineExecutorLoop()),
        "log": logging.getLogger("database-revision-oracle"),
        "os": os,
        "_dbmigrator": None,
    }
    exec(compile(module, str(source_path), "exec"), namespace)  # pylint: disable=exec-used
    return namespace["DatabaseComponent"], namespace, {
        "current_revision": current_revision,
        "revision_filename": revision_filename,
        "sqlite_filename": sqlite_filename,
    }


def run_case(component_class, namespace, metadata, case):
    calls = []
    with tempfile.TemporaryDirectory(prefix="lbry-db-revision-oracle-") as data_dir:
        revision_path = Path(data_dir) / metadata["revision_filename"]
        if case.get("exists", True):
            revision_path.write_bytes(case.get("contents", "").encode("utf-8"))

        migration = case.get("migration", "success")

        def migrate_db(conf, from_revision, to_revision):
            if conf.data_dir != data_dir:
                raise AssertionError(f"migration received data_dir {conf.data_dir!r}, expected {data_dir!r}")
            calls.append([from_revision, to_revision])
            if migration == "error":
                raise RuntimeError("fixture migration failed")
            if migration != "success":
                raise ValueError(f"unknown migration fixture behavior: {migration}")

        namespace["_dbmigrator"] = SimpleNamespace(migrate_db=migrate_db)
        component = component_class.__new__(component_class)
        component.conf = SimpleNamespace(data_dir=data_dir)

        error_type = None
        error = None
        try:
            asyncio.run(component.start())
        except Exception as caught:  # pylint: disable=broad-except
            error_type = type(caught).__name__
            error = str(caught)

        final_exists = revision_path.exists()
        final_contents = revision_path.read_bytes().decode("utf-8") if final_exists else None
        return {
            "name": case.get("name", ""),
            "calls": calls,
            "error_type": error_type,
            "error": error,
            "final_exists": final_exists,
            "final_contents": final_contents,
        }


def run(sdk_root, payload):
    verify_pinned_sources(sdk_root)
    version = read_sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    component_class, namespace, metadata = extract_component(sdk_root)
    return {
        "reference": {"commit": PINNED_COMMIT, "version": version},
        "metadata": metadata,
        "cases": [
            run_case(component_class, namespace, metadata, case)
            for case in payload.get("cases", [])
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

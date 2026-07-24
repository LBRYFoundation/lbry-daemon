#!/usr/bin/env python3
"""Run settings RPC calls against the pinned Python SDK source.

The full SDK dependency graph is intentionally not imported. This loader executes
the real lbry/conf.py module and extracts the settings methods, dispatcher,
parameter checker, and JSONRPCError from the real daemon.py. Only import-time
symbols unrelated to settings are stubbed.
"""

import argparse
import ast
import asyncio
import hashlib
import inspect
import json
import logging
import os
from pathlib import Path
import sys
import time
from traceback import format_exc
import types

try:
    import yaml
except ImportError as error:  # pragma: no cover - reported to the Go runner
    raise SystemExit("PyYAML is required to run the settings oracle") from error


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
SETTINGS_METHODS = {
    "settings_get",
    "settings_set",
    "settings_clear",
}
DAEMON_METHODS = {
    *(f"jsonrpc_{name}" for name in SETTINGS_METHODS),
    "_process_rpc_call",
    "_verify_method_is_callable",
    "_get_jsonrpc_method",
    "_check_params",
}
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/conf.py": "ddedb9961723e67387fde0e02f7308fc6725f682802e1c3ec9030f6ccceac3e5",
    "lbry/extras/daemon/daemon.py": "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
    "lbry/wallet/coinselection.py": "96c686fc3a9037468e6d9c684080af4ee84f3710be7f6b42f1ddcc6ce5dc474e",
}
ORACLE_LOG = logging.getLogger("settings-oracle")
ORACLE_LOG.addHandler(logging.NullHandler())
ORACLE_LOG.propagate = False


class InvalidCurrencyError(Exception):
    def __init__(self, currency):
        self.currency = currency
        super().__init__(f"Invalid currency: {currency} is not a supported currency.")


class BaseError(Exception):
    pass


class UnknownAPIMethodError(Exception):
    pass


class CommandDoesNotExistError(BaseError):
    def __init__(self, command):
        self.command = command
        super().__init__(f"Command '{command}' does not exist.")


class NoopMetric:
    def labels(self, **unused_labels):
        return self

    def inc(self):
        pass

    def dec(self):
        pass

    def observe(self, unused_value):
        pass


def module(name, **attributes):
    value = types.ModuleType(name)
    for attribute, content in attributes.items():
        setattr(value, attribute, content)
    return value


def coin_selection_strategies(source_path):
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    strategies = ["sqlite"]
    for node in ast.walk(tree):
        if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            continue
        if any(isinstance(decorator, ast.Name) and decorator.id == "strategy" for decorator in node.decorator_list):
            strategies.append(node.name)
    return strategies


def install_import_stubs(sdk_root, work_dir):
    lbry_package = module("lbry")
    lbry_package.__path__ = [str(sdk_root / "lbry")]
    sys.modules["lbry"] = lbry_package

    error_module = module("lbry.error", InvalidCurrencyError=InvalidCurrencyError)
    sys.modules["lbry.error"] = error_module

    dht_package = module("lbry.dht")
    dht_package.__path__ = [str(sdk_root / "lbry" / "dht")]
    constants_module = module("lbry.dht.constants", RPC_TIMEOUT=5.0)
    dht_package.constants = constants_module
    sys.modules["lbry.dht"] = dht_package
    sys.modules["lbry.dht.constants"] = constants_module

    wallet_package = module("lbry.wallet")
    wallet_package.__path__ = [str(sdk_root / "lbry" / "wallet")]
    strategy_source = sdk_root / "lbry" / "wallet" / "coinselection.py"
    coinselection_module = module(
        "lbry.wallet.coinselection",
        STRATEGIES=coin_selection_strategies(strategy_source),
    )
    wallet_package.coinselection = coinselection_module
    sys.modules["lbry.wallet"] = wallet_package
    sys.modules["lbry.wallet.coinselection"] = coinselection_module

    appdirs_root = work_dir / "appdirs"

    def user_data_dir(app_name, app_author=None, *unused_args, **unused_kwargs):
        parts = [appdirs_root]
        if app_author:
            parts.append(Path(app_author))
        parts.append(Path(app_name))
        return str(Path(*parts))

    def user_config_dir(*unused_args, **unused_kwargs):
        return str(work_dir / "config-home")

    sys.modules["appdirs"] = module(
        "appdirs",
        user_data_dir=user_data_dir,
        user_config_dir=user_config_dir,
    )


def load_conf_module(sdk_root, work_dir):
    install_import_stubs(sdk_root, work_dir)
    source_path = sdk_root / "lbry" / "conf.py"
    namespace = {
        "__file__": str(source_path),
        "__name__": "lbry.conf",
        "__package__": "lbry",
    }
    exec(compile(source_path.read_text(encoding="utf-8"), str(source_path), "exec"), namespace)
    return namespace


def load_settings_daemon(sdk_root, conf_namespace):
    source_path = sdk_root / "lbry" / "extras" / "daemon" / "daemon.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    daemon_node = next(
        node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "Daemon"
    )
    json_rpc_error_node = next(
        node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "JSONRPCError"
    )
    error_namespace = {}
    error_module = ast.Module(body=[json_rpc_error_node], type_ignores=[])
    ast.fix_missing_locations(error_module)
    exec(compile(error_module, str(source_path), "exec"), error_namespace)
    json_rpc_error = error_namespace["JSONRPCError"]

    methods = {
        node.name: node
        for node in daemon_node.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name in DAEMON_METHODS
    }
    if set(methods) != DAEMON_METHODS:
        missing = ", ".join(sorted(DAEMON_METHODS - set(methods)))
        raise RuntimeError(f"pinned daemon source is missing settings methods: {missing}")

    method_namespace = {
        "asyncio": asyncio,
        "BaseError": BaseError,
        "CommandDoesNotExistError": CommandDoesNotExistError,
        "format_exc": format_exc,
        "inspect": inspect,
        "is_transactional_function": lambda name: any(
            action in name for action in ("create", "update", "abandon", "send", "fund")
        ),
        "json": json,
        "JSONRPCError": json_rpc_error,
        "log": ORACLE_LOG,
        "NOT_SET": conf_namespace["NOT_SET"],
        "Setting": conf_namespace["Setting"],
        "time": time,
        "undecorated": lambda function: function,
        "UnknownAPIMethodError": UnknownAPIMethodError,
    }
    oracle_type = type("SettingsOracleDaemon", (), {})
    for name, method_node in methods.items():
        method_module = ast.Module(body=[method_node], type_ignores=[])
        ast.fix_missing_locations(method_module)
        compiled_namespace = dict(method_namespace)
        exec(compile(method_module, str(source_path), "exec"), compiled_namespace)
        setattr(oracle_type, name, compiled_namespace[name])
    oracle_type.callable_methods = {
        name: getattr(oracle_type, f"jsonrpc_{name}") for name in SETTINGS_METHODS
    }
    oracle_type.deprecated_methods = {}
    oracle_type._oracle_error_type = json_rpc_error
    return oracle_type


def read_sdk_version(sdk_root):
    init_path = sdk_root / "lbry" / "__init__.py"
    tree = ast.parse(init_path.read_text(encoding="utf-8"), filename=str(init_path))
    for node in tree.body:
        if not isinstance(node, ast.Assign):
            continue
        if any(isinstance(target, ast.Name) and target.id == "__version__" for target in node.targets):
            return ast.literal_eval(node.value)
    raise RuntimeError("could not read SDK version")


def verify_pinned_sources(sdk_root):
    for relative_path, expected in PINNED_SOURCE_HASHES.items():
        source_path = sdk_root / relative_path
        actual = hashlib.sha256(source_path.read_bytes()).hexdigest()
        if actual != expected:
            raise RuntimeError(
                f"{relative_path} does not match pinned commit {PINNED_COMMIT}: "
                f"sha256 is {actual}, expected {expected}"
            )


def yaml_snapshot(path):
    if not path.exists():
        return None
    text = path.read_text(encoding="utf-8")
    return yaml.safe_load(text)


def execute_operation(daemon, operation):
    method = operation.get("method")
    if method not in SETTINGS_METHODS:
        raise ValueError(f"unsupported oracle method: {method}")
    params = operation.get("params", {})
    if not isinstance(params, dict):
        raise ValueError("settings oracle operations require object params")
    result = asyncio.run(daemon._process_rpc_call({"method": method, "params": params}))
    if isinstance(result, daemon._oracle_error_type):
        return {"jsonrpc": "2.0", "error": result.to_dict()}
    return {"jsonrpc": "2.0", "result": result}


def run(sdk_root, work_dir, payload):
    verify_pinned_sources(sdk_root)
    version = read_sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")

    work_dir.mkdir(parents=True, exist_ok=True)
    home = work_dir / "home"
    home.mkdir(exist_ok=True)
    os.environ["HOME"] = str(home)
    os.environ["XDG_DATA_HOME"] = str(work_dir / "xdg-data")
    os.environ["XDG_CONFIG_HOME"] = str(work_dir / "xdg-config")

    data_dir = work_dir / "data"
    download_dir = work_dir / "downloads"
    wallet_dir = work_dir / "wallet"
    for directory in (data_dir, download_dir, wallet_dir):
        directory.mkdir(parents=True, exist_ok=True)
    config_path = work_dir / "daemon_settings.yml"
    if "initial_yaml" in payload and payload["initial_yaml"] is not None:
        config_path.write_text(payload["initial_yaml"], encoding="utf-8")

    conf_namespace = load_conf_module(sdk_root, work_dir)
    config = conf_namespace["Config"](
        config=str(config_path),
        data_dir=str(data_dir),
        download_dir=str(download_dir),
        wallet_dir=str(wallet_dir),
    )
    config.set_persisted(str(config_path))
    oracle_type = load_settings_daemon(sdk_root, conf_namespace)
    daemon = oracle_type()
    daemon.conf = config
    daemon.pending_requests_metric = NoopMetric()
    daemon.requests_count_metric = NoopMetric()
    daemon.cancelled_request_metric = NoopMetric()
    daemon.failed_request_metric = NoopMetric()
    daemon.response_time_metric = NoopMetric()

    steps = []
    for operation in payload.get("operations", []):
        response = execute_operation(daemon, operation)
        steps.append({
            "method": operation.get("method"),
            "response": response,
            "yaml": yaml_snapshot(config_path),
        })

    return {
        "reference": {"commit": PINNED_COMMIT, "version": version},
        "paths": {
            "config": str(config_path),
            "data_dir": str(data_dir),
            "download_dir": str(download_dir),
            "wallet_dir": str(wallet_dir),
        },
        "steps": steps,
        "yaml_text": config_path.read_text(encoding="utf-8") if config_path.exists() else None,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    parser.add_argument("--work-dir", required=True, type=Path)
    arguments = parser.parse_args()
    payload = json.load(sys.stdin)
    result = run(arguments.sdk_root.resolve(), arguments.work_dir.resolve(), payload)
    json.dump(result, sys.stdout, sort_keys=True, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

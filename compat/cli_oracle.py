#!/usr/bin/env python3
"""Parse startup argv with the pinned SDK's real configuration descriptors."""

import argparse
import ast
import hashlib
import json
from pathlib import Path
import sys
import types

try:
    import yaml  # noqa: F401 - conf.py imports and uses this module
except ImportError as error:  # pragma: no cover - reported to the Go runner
    raise SystemExit("PyYAML is required to run the CLI oracle") from error


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/conf.py": "ddedb9961723e67387fde0e02f7308fc6725f682802e1c3ec9030f6ccceac3e5",
    "lbry/extras/cli.py": "33e612f86a4a9b43e63ca4afb8d71a0edb0439ea1a1b57f706f20abe716bf5f8",
    "lbry/wallet/coinselection.py": "96c686fc3a9037468e6d9c684080af4ee84f3710be7f6b42f1ddcc6ce5dc474e",
}


class InvalidCurrencyError(Exception):
    pass


class ParserUsageError(Exception):
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
        if any(isinstance(item, ast.Name) and item.id == "strategy" for item in node.decorator_list):
            strategies.append(node.name)
    return strategies


def install_import_stubs(sdk_root, work_dir):
    lbry_package = module("lbry")
    lbry_package.__path__ = [str(sdk_root / "lbry")]
    sys.modules["lbry"] = lbry_package
    sys.modules["lbry.error"] = module("lbry.error", InvalidCurrencyError=InvalidCurrencyError)

    dht_package = module("lbry.dht")
    dht_package.__path__ = [str(sdk_root / "lbry" / "dht")]
    constants = module("lbry.dht.constants", RPC_TIMEOUT=5.0)
    dht_package.constants = constants
    sys.modules["lbry.dht"] = dht_package
    sys.modules["lbry.dht.constants"] = constants

    wallet_package = module("lbry.wallet")
    wallet_package.__path__ = [str(sdk_root / "lbry" / "wallet")]
    coinselection = module(
        "lbry.wallet.coinselection",
        STRATEGIES=coin_selection_strategies(sdk_root / "lbry" / "wallet" / "coinselection.py"),
    )
    wallet_package.coinselection = coinselection
    sys.modules["lbry.wallet"] = wallet_package
    sys.modules["lbry.wallet.coinselection"] = coinselection

    sys.modules["appdirs"] = module(
        "appdirs",
        user_data_dir=lambda *unused_args, **unused_kwargs: str(work_dir / "data"),
        user_config_dir=lambda *unused_args, **unused_kwargs: str(work_dir / "config"),
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


def read_sdk_version(sdk_root):
    source_path = sdk_root / "lbry" / "__init__.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
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


class OracleParser(argparse.ArgumentParser):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, add_help=False, **kwargs)
        self.add_argument("--help", dest="help", action="store_true", default=False)

    def error(self, message):
        raise ParserUsageError(message)


def argument_parser(conf):
    root = OracleParser("lbrynet", allow_abbrev=False)
    root.add_argument("-v", "--version", dest="cli_version", action="store_true")
    root.set_defaults(command=None)
    conf["CLIConfig"].contribute_to_argparse(root)

    commands = root.add_subparsers(metavar="COMMAND")
    start = commands.add_parser("start")
    start.add_argument("--quiet", dest="quiet", action="store_true")
    start.add_argument("--no-logging", dest="no_logging", action="store_true")
    start.add_argument("--verbose", nargs="*")
    start.add_argument("--initial-headers", dest="initial_headers")
    conf["Config"].contribute_to_argparse(start)
    start.set_defaults(command="start")
    return root


def parse_case(parser, conf, argv):
    try:
        arguments, unknown = parser.parse_known_args(argv)
    except ParserUsageError as error:
        return {"argv": argv, "result": None, "error": str(error)}

    configuration = conf["Config"] if arguments.command == "start" else conf["CLIConfig"]
    not_set = conf["NOT_SET"]
    list_setting = conf["ListSetting"]
    settings = {}
    for setting in configuration.get_settings():
        value = getattr(arguments, setting.name, not_set)
        if value != not_set and not (isinstance(setting, list_setting) and value is None):
            settings[setting.name] = value

    verbose = getattr(arguments, "verbose", None)
    result = {
        "command": arguments.command or "",
        "settings": settings,
        "help": bool(arguments.help),
        "version": bool(arguments.cli_version),
        "quiet": bool(getattr(arguments, "quiet", False)),
        "no_logging": bool(getattr(arguments, "no_logging", False)),
        "verbose": verbose,
        "verbose_set": verbose is not None,
        "initial_headers": getattr(arguments, "initial_headers", None) or "",
        "unknown": unknown or None,
    }
    return {"argv": argv, "result": result, "error": None}


def run(sdk_root, work_dir, payload):
    verify_pinned_sources(sdk_root)
    version = read_sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    conf = load_conf_module(sdk_root, work_dir)
    parser = argument_parser(conf)
    return {
        "reference": {"commit": PINNED_COMMIT, "version": version},
        "cases": [parse_case(parser, conf, argv) for argv in payload.get("cases", [])],
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

#!/usr/bin/env python3
"""Execute pinned Wallet and WalletManager orchestration in isolation."""

import argparse
import ast
import asyncio
import copy
from collections import UserDict
from decimal import Decimal
from hashlib import sha256
import importlib.util
import json
import logging
from operator import attrgetter
import os
from pathlib import Path
import stat
import subprocess
import sys
import time as system_time
import typing
from typing import List, MutableMapping, MutableSequence, Optional, Sequence, Type
import zlib


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/conf.py": "ddedb9961723e67387fde0e02f7308fc6725f682802e1c3ec9030f6ccceac3e5",
    "lbry/wallet/account.py": "ea2ca30bddf9c0145469e989d9855dbe7be5184943ae7b8ca690eda41eb7db50",
    "lbry/wallet/ledger.py": "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
    "lbry/wallet/manager.py": "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
    "lbry/wallet/wallet.py": "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
}
ENCRYPT_ON_DISK = "encrypt-on-disk"
FIXED_SEED = (
    "carbon smart garage balance margin twelve chest sword toast envelope "
    "bottom stomach absent"
)


def load_account_adapter():
    source_path = Path(__file__).with_name("account_oracle.py")
    specification = importlib.util.spec_from_file_location(
        "lbry_account_oracle_adapter", source_path
    )
    module = importlib.util.module_from_spec(specification)
    specification.loader.exec_module(module)
    return module


ACCOUNT_ADAPTER = load_account_adapter()


class InvalidPasswordError(Exception):
    pass


class WalletNotLoadedError(Exception):
    def __init__(self, wallet_id):
        super().__init__(f"Wallet {wallet_id} is not loaded.")


class Placeholder:
    pass


class FixtureClock:
    def __init__(self):
        self.now = 0.0

    def time(self):
        return self.now


class SettingDescriptor:
    default = []

    @staticmethod
    def is_set_to_default(_config):
        return False

    @staticmethod
    def is_set(_config):
        return False


class ConfigType:
    lbryum_servers = SettingDescriptor()


class LedgerRegistry:
    classes = {}

    @classmethod
    def get_ledger_class(cls, ledger_id):
        return cls.classes[ledger_id]


def make_ledger_class(ledger_id, network):
    class OracleLedger(ACCOUNT_ADAPTER.Ledger):
        def __init__(self, configuration=None):
            self.config = configuration or {}
            adapted = dict(self.config)
            adapted.update({"id": ledger_id, "network": network})
            super().__init__(adapted)
            self.coin_selection_strategy = None

        @classmethod
        def get_id(cls):
            return ledger_id

        @property
        def path(self):
            return os.path.join(self.config["data_path"], ledger_id)

        async def start(self):
            return None

        async def stop(self):
            return None

    OracleLedger.__name__ = "Oracle" + ledger_id.title().replace("_", "")
    return OracleLedger


LedgerRegistry.classes = {
    "lbc_mainnet": make_ledger_class("lbc_mainnet", "mainnet"),
    "lbc_testnet": make_ledger_class("lbc_testnet", "testnet"),
    "lbc_regtest": make_ledger_class("lbc_regtest", "regtest"),
}


def verify_pinned_sources(sdk_root):
    commit = subprocess.run(
        ["git", "-C", str(sdk_root), "rev-parse", "HEAD"], check=True,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    for relative_path, expected in PINNED_SOURCE_HASHES.items():
        actual = sha256((sdk_root / relative_path).read_bytes()).hexdigest()
        if actual != expected:
            raise RuntimeError(
                f"{relative_path} does not match pinned commit {PINNED_COMMIT}: "
                f"sha256 is {actual}, expected {expected}"
            )
    ACCOUNT_ADAPTER.verify_pinned_sources(sdk_root)
    return commit


def read_sdk_version(sdk_root):
    source_path = sdk_root / "lbry" / "__init__.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    for node in tree.body:
        if isinstance(node, ast.Assign) and any(
            isinstance(target, ast.Name) and target.id == "__version__"
            for target in node.targets
        ):
            return ast.literal_eval(node.value)
    raise RuntimeError("could not read SDK version")


def load_wallet_contract(sdk_root, account_type):
    source_path = sdk_root / "lbry" / "wallet" / "wallet.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    wanted = {"TimestampedPreferences", "Wallet", "WalletStorage"}
    selected = [
        node for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name in wanted
    ]
    if {node.name for node in selected} != wanted:
        raise RuntimeError("pinned wallet classes changed")
    clock = FixtureClock()
    namespace = {
        "Account": account_type,
        "ENCRYPT_ON_DISK": ENCRYPT_ON_DISK,
        "InvalidPasswordError": InvalidPasswordError,
        "List": List,
        "MutableSequence": MutableSequence,
        "Optional": Optional,
        "Sequence": Sequence,
        "UserDict": UserDict,
        "attrgetter": attrgetter,
        "better_aes_decrypt": lambda *_args: (_ for _ in ()).throw(NotImplementedError()),
        "better_aes_encrypt": lambda *_args: (_ for _ in ()).throw(NotImplementedError()),
        "json": json,
        "log": logging.getLogger(__name__),
        "os": os,
        "sha256": sha256,
        "stat": stat,
        "time": clock,
        "typing": typing,
        "zlib": zlib,
    }
    module = ast.fix_missing_locations(ast.Module(body=selected, type_ignores=[]))
    exec(compile(module, str(source_path), "exec"), namespace)  # pylint: disable=exec-used
    return (
        namespace["Wallet"], namespace["WalletStorage"],
        namespace["TimestampedPreferences"], clock,
    )


def load_manager_contract(sdk_root, wallet_type, storage_type, account_type):
    source_path = sdk_root / "lbry" / "wallet" / "manager.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    selected = [
        node for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "WalletManager"
    ]
    if len(selected) != 1:
        raise RuntimeError("pinned WalletManager class changed")
    namespace = {
        "Account": account_type,
        "CodeMessageError": Placeholder,
        "Config": ConfigType,
        "Database": Placeholder,
        "Decimal": Decimal,
        "ENCRYPT_ON_DISK": ENCRYPT_ON_DISK,
        "ExchangeRateManager": Placeholder,
        "KeyFeeAboveMaxAllowedError": Placeholder,
        "Ledger": ACCOUNT_ADAPTER.Ledger,
        "LedgerRegistry": LedgerRegistry,
        "List": List,
        "MutableMapping": MutableMapping,
        "MutableSequence": MutableSequence,
        "NOT_SET": object(),
        "Optional": Optional,
        "Output": Placeholder,
        "Transaction": Placeholder,
        "Type": Type,
        "Wallet": wallet_type,
        "WalletNotLoadedError": WalletNotLoadedError,
        "WalletStorage": storage_type,
        "asyncio": asyncio,
        "dewies_to_lbc": lambda value: value,
        "json": json,
        "log": logging.getLogger(__name__),
        "os": os,
        "unhexlify": bytes.fromhex,
    }
    module = ast.fix_missing_locations(ast.Module(body=selected, type_ignores=[]))
    exec(compile(module, str(source_path), "exec"), namespace)  # pylint: disable=exec-used
    return namespace["WalletManager"]


def patch_account_generation(account_type):
    @classmethod
    def generate(cls, ledger, wallet, name=None, address_generator=None):
        return cls.from_dict(ledger, wallet, {
            "name": name,
            "seed": FIXED_SEED,
            "address_generator": address_generator or {},
        })

    async def no_cache_prime(_self):
        return None

    account_type.generate = generate
    manager_type = account_type.__init__.__globals__["DeterministicChannelKeyManager"]
    manager_type.ensure_cache_primed = no_cache_prime


class LBRYNetConfig:
    def __init__(self, case, root):
        self.blockchain_name = case.get("blockchain_name", "lbrycrd_main")
        self.wallet_dir = str(root)
        self.wallets = case.get("wallets", ["default_wallet"])
        self.hub_timeout = case.get("hub_timeout", 30)
        self.lbryum_servers = case.get("lbryum_servers", ["hub.example:50001"])
        self.known_hubs = case.get("known_hubs", {})
        self.jurisdiction = case.get("jurisdiction")
        self.concurrent_hub_requests = case.get("concurrent_hub_requests", 32)
        self.transaction_cache_size = case.get("transaction_cache_size", 1024)
        self.coin_selection_strategy = case.get("coin_selection_strategy", "prefer-confirmed")


def capture(function):
    try:
        return function(), None, None
    except Exception as error:  # pylint: disable=broad-except
        return None, type(error).__name__, str(error)


def configure_case(wallet_clock, account_clock, fixture_os, case):
    now = case.get("now", 1_700_000_000.75)
    wallet_clock.now = now
    account_clock.now = now
    fixture_os.reset(case.get("urandom"))


def apply_account_vectors(wallet, case):
    vectors = case.get("init_vectors", {})
    for account in wallet.accounts:
        for key, value in vectors.items():
            account.init_vectors[key] = bytes.fromhex(value)


def account_ids(accounts):
    return [account.id for account in accounts]


def dict_view(wallet, password=None, password_present=False):
    function = (
        (lambda: wallet.to_dict(password)) if password_present
        else wallet.to_dict
    )
    result, error_type, error = capture(function)
    return {
        "result": copy.deepcopy(result),
        "json": json.dumps(result) if result is not None else None,
        "key_order": list(result) if result is not None else None,
        "error_type": error_type,
        "error": error,
    }


def wallet_view(wallet):
    to_json, json_error_type, json_error = capture(wallet.to_json)
    digest, hash_error_type, hash_error = capture(lambda: wallet.hash.hex())
    encrypted, encrypted_error_type, encrypted_error = capture(
        lambda: wallet.is_encrypted
    )
    return {
        "id": wallet.id,
        "name": wallet.name,
        "storage_path": wallet.storage.path,
        "account_ids": account_ids(wallet.accounts),
        "default_account_id": (
            wallet.default_account.id if wallet.default_account is not None else None
        ),
        "preferences": copy.deepcopy(wallet.preferences.data),
        "encryption_password": wallet.encryption_password,
        "is_locked": wallet.is_locked,
        "is_encrypted": encrypted,
        "is_encrypted_error_type": encrypted_error_type,
        "is_encrypted_error": encrypted_error,
        "to_dict": dict_view(wallet),
        "to_json": to_json,
        "to_json_error_type": json_error_type,
        "to_json_error": json_error,
        "hash": digest,
        "hash_error_type": hash_error_type,
        "hash_error": hash_error,
    }


def file_view(path):
    path = Path(path)
    if not path.exists():
        return {"exists": False, "contents": None, "mode": None}
    return {
        "exists": True,
        "contents": path.read_text(encoding="utf-8") if path.is_file() else None,
        "mode": stat.S_IMODE(path.stat().st_mode),
    }


def write_file(path, document, mode=0o600):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    if isinstance(document, str):
        contents = document
    else:
        contents = json.dumps(document)
    path.write_text(contents, encoding="utf-8")
    os.chmod(path, mode)


def run_wallet_action(wallet, action, wallet_clock):
    name = action["action"]
    wallet_clock.now = action.get("now", wallet_clock.now)
    if name == "save":
        result, error_type, error = capture(wallet.save)
    elif name == "set_password":
        wallet.encryption_password = action.get("password")
        result, error_type, error = None, None, None
    elif name == "set_preference":
        def set_preference():
            wallet.preferences[action["key"]] = action.get("value")
        result, error_type, error = capture(set_preference)
    elif name == "encrypt":
        result, error_type, error = capture(lambda: wallet.encrypt(action["password"]))
    elif name == "decrypt":
        result, error_type, error = capture(wallet.decrypt)
    elif name == "lock":
        result, error_type, error = capture(wallet.lock)
    elif name == "unlock":
        result, error_type, error = capture(
            lambda: asyncio.run(wallet.unlock(action["password"]))
        )
    elif name == "account":
        result, error_type, error = capture(
            lambda: wallet.get_account_or_error(action["id"]).id
        )
    elif name == "account_or_default":
        result, error_type, error = capture(
            lambda: wallet.get_account_or_default(action.get("id")).id
        )
    elif name == "accounts_or_all":
        result, error_type, error = capture(
            lambda: account_ids(wallet.get_accounts_or_all(action.get("ids")))
        )
    else:
        raise ValueError(f"unknown wallet action: {name}")
    return {
        "action": name,
        "result": result,
        "error_type": error_type,
        "error": error,
        "wallet": wallet_view(wallet),
        "file": file_view(wallet.storage.path) if wallet.storage.path is not None else None,
    }


def run_wallet_case(
        wallet_type, storage_type, manager_type, wallet_clock,
        account_clock, fixture_os, case):
    configure_case(wallet_clock, account_clock, fixture_os, case)
    manager = manager_type()
    for ledger_id, config in case.get("ledgers", {"lbc_mainnet": {}}).items():
        manager.get_or_create_ledger(ledger_id, config)
    path = case.get("path")
    if path is not None and "document" in case:
        write_file(path, case["document"], case.get("mode", 0o600))
    storage = (
        storage_type(path) if path is not None
        else storage_type(default=case.get("document"))
    )
    wallet, error_type, error = capture(lambda: wallet_type.from_storage(storage, manager))
    result = {
        "name": case.get("name", ""),
        "error_type": error_type,
        "error": error,
        "initial": None,
        "actions": [],
    }
    if wallet is None:
        return result
    apply_account_vectors(wallet, case)
    result["initial"] = wallet_view(wallet)
    for action in case.get("actions", []):
        result["actions"].append(run_wallet_action(wallet, action, wallet_clock))
    return result


def manager_view(manager, root):
    ledgers = []
    for ledger_class, ledger in manager.ledgers.items():
        config = copy.deepcopy(ledger.config)
        if "data_path" in config:
            config["data_path"] = "<ROOT>" + str(config["data_path"])[len(str(root)):]
        ledgers.append({
            "id": ledger_class.get_id(),
            "config": config,
            "account_ids": account_ids(ledger.accounts),
            "path": "<ROOT>" + ledger.path[len(str(root)):],
            "coin_selection_strategy": ledger.coin_selection_strategy,
        })
    wallets = [wallet_view(wallet) for wallet in manager.wallets]
    for wallet in wallets:
        if wallet["storage_path"] is not None and wallet["storage_path"].startswith(str(root)):
            wallet["storage_path"] = "<ROOT>" + wallet["storage_path"][len(str(root)):]
    return {
        "wallets": wallets,
        "wallet_ids": [wallet.id for wallet in manager.wallets],
        "default_wallet_id": manager.default_wallet.id if manager.default_wallet else None,
        "default_account_id": manager.default_account.id if manager.default_account else None,
        "account_ids": account_ids(manager.accounts),
        "ledgers": ledgers,
        "running": manager.running,
    }


def manager_files(root):
    result = {}
    for path in sorted(Path(root).rglob("*")):
        relative = path.relative_to(root).as_posix()
        result[relative] = file_view(path)
    return result


def run_manager_case(
        manager_type, wallet_clock, account_clock, fixture_os, case):
    root = Path(case["root"])
    root.mkdir(parents=True, exist_ok=True)
    configure_case(wallet_clock, account_clock, fixture_os, case)
    for file_data in case.get("files", []):
        write_file(
            os.path.join(str(root), file_data["path"]), file_data["document"],
            file_data.get("mode", 0o600),
        )
    kind = case.get("kind", "from_config")
    if kind == "from_config":
        ledgers = {}
        for ledger_id, config in case.get("ledgers", {}).items():
            config = copy.deepcopy(config)
            if config.get("data_path") == "<ROOT>":
                config["data_path"] = str(root)
            ledgers[ledger_id] = config
        config = {
            "ledgers": ledgers,
            "wallets": [
                os.path.join(str(root), path) for path in case.get("wallets", [])
            ],
        }
        manager, error_type, error = capture(lambda: manager_type.from_config(config))
    elif kind == "lbrynet":
        config = LBRYNetConfig(case, root)
        manager, error_type, error = capture(
            lambda: asyncio.run(manager_type.from_lbrynet_config(config))
        )
    else:
        raise ValueError(f"unknown manager case kind: {kind}")
    return {
        "name": case.get("name", ""),
        "error_type": error_type,
        "error": error,
        "manager": manager_view(manager, root) if manager is not None else None,
        "files": manager_files(root),
        "urandom_calls": list(fixture_os.calls),
    }


def run(sdk_root, payload):
    if not __debug__:
        raise RuntimeError("wallet manager oracle requires Python assertions (__debug__)")
    commit = verify_pinned_sources(sdk_root)
    version = read_sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    ACCOUNT_ADAPTER.Mnemonic.words = ACCOUNT_ADAPTER.read_english_words(sdk_root)
    account_type, account_clock, fixture_os = ACCOUNT_ADAPTER.load_contract(sdk_root)
    patch_account_generation(account_type)
    wallet_type, storage_type, _preferences_type, wallet_clock = load_wallet_contract(
        sdk_root, account_type
    )
    manager_type = load_manager_contract(sdk_root, wallet_type, storage_type, account_type)
    return {
        "reference": {"commit": commit, "version": version, "source_sha256": PINNED_SOURCE_HASHES},
        "metadata": {
            "python_version": ".".join(map(str, sys.version_info[:2])),
            "python_debug": __debug__,
            "fixed_generated_seed": FIXED_SEED,
        },
        "wallet_cases": [
            run_wallet_case(
                wallet_type, storage_type, manager_type, wallet_clock,
                account_clock, fixture_os, case,
            )
            for case in payload.get("wallet_cases", [])
        ],
        "manager_cases": [
            run_manager_case(
                manager_type, wallet_clock, account_clock, fixture_os, case
            )
            for case in payload.get("manager_cases", [])
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

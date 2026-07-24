#!/usr/bin/env python3
"""Pinned offline probes for transaction_show and its basic JSON encoding.

The selected SDK methods are AST-executed against deterministic SQLite and
network fakes.  The transaction fixture contains a P2PKH payment and an
OP_RETURN output.  Claim resolution, protobuf encoding, and external network
access are deliberately outside this oracle.
"""

import argparse
import ast
import asyncio
from binascii import hexlify, unhexlify
import copy
from datetime import datetime
from decimal import Decimal
import hashlib
import json
from json import JSONEncoder
from pathlib import Path
import sqlite3
import struct
import subprocess
import sys
from types import SimpleNamespace


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/extras/daemon/daemon.py": "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
    "lbry/extras/daemon/json_response_encoder.py": "047fd406c20236025414b8805669b1a830b0b412386c1613498aa1ebaa021732",
    "lbry/wallet/database.py": "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
    "lbry/wallet/dewies.py": "67506d75a5f0ddb3f7c2ea832ba7b13fb49ae4193f060a1fdf541b5f50a3084a",
    "lbry/wallet/manager.py": "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
    "lbry/wallet/network.py": "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
    "lbry/wallet/rpc/jsonrpc.py": "6da90b83bdb2e192929abddbb8b33824eac7d24f7ab126c1942db5ed6b7c1269",
    "lbry/wallet/transaction.py": "e73491aeb915fbce931acbb4d9631f3e05440a7d26c598db85e66e524a798d15",
    "lbry/wallet/util.py": "08f697c88ec36d2bb417609194266f279eba2f69b1a62a10b1de69b9c1733d5a",
    "lbry/wallet/wallet.py": "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
}
PINNED_METHOD_HASHES = {
    "CodeMessageError.code": "60732917bbdfac4a69c89c9923c7184b4a21d4d861b04f9846aeb01bc88a760b",
    "CodeMessageError.message": "be31c27412c9dcaef89ed8240b1936c8aaa771a29239e2828f2ae21ce002dc05",
    "Daemon.jsonrpc_transaction_show": "10ec4201cf4cce44bf3442ff6654732bc2d99a1634e905e5d65cc1741d898ad6",
    "Daemon.ledger": "c0aad64201976cc6d3b4ae3fa49fe9434093c578706b84f45b8cc687c7276f46",
    "Database.get_transaction": "170b2b7217a51a966b609a936ef8585cb31e1bc97463157c24278c46658a9107",
    "Input.amount": "ab8d93c4660ea7e42b32857b490008d04182917e50d8a83cef0fcb7f130f7e86",
    "JSONResponseEncoder.__init__": "bf1a658c1eed62bbae283ebe132f8067f986e534771ebf3417685536472fdb1e",
    "JSONResponseEncoder.default": "298986ed087ef927a948ecc2d8f55730ca2e57a9c6ec032255d30bc92448c4a8",
    "JSONResponseEncoder.encode_input": "e5da304910667c242ed9ac6be076a3a6db087404f1769287d35e294b635ee5ce",
    "JSONResponseEncoder.encode_output": "fc124a8362451a2449d83b06e252d9c3d85ec6b006b5f9d0dc5dfd60b5db92be",
    "JSONResponseEncoder.encode_transaction": "4ed03ea6d0a24d79213e63da6b78304a3b1a1c17b368cde3804d76d6efc78f7e",
    "JSONRPCError.__init__": "6694af6fe018ba7f86d734992597aaea26b18d70389263a7ff5fd2be1995144b",
    "JSONRPCError.create_command_exception": "fc56255c3a3e15b5279f3d583d6ee67959109f5f0c4766c0d10928bf12cc659e",
    "JSONRPCError.filter_traceback": "db8da5a9ff8f43e6ce64bdaad60f5e67cc3e071f1992b29d79fa8f2dafa97f86",
    "JSONRPCError.to_dict": "4a92e56be4937d195c7307f337b8fcac7a36b306d945b2dbe29108748882a347",
    "Network.get_transaction_and_merkle": "cbc3a98dc4ea8ddd83218df36146f98ceb2ba6e127f57e5270a336d47745c0a8",
    "Output.get_address": "031f5c186213ba42ed354461e31d3d7075fda2cb285384485077f4e7adab1e8e",
    "Output.has_address": "7b1852917e901fef3875a1c7867bf943f09a4dcc9187feb8c5d87296f2adf2db",
    "Output.is_pubkey_hash": "9ee9f33bac7e1e6fbd748dd79737073f799a7e64e516b81866c363d538b1f4d9",
    "Output.is_script_hash": "bd52941646cbd63eb7eb2df43c0c9708938f26045b1293b9f3d262eb565d0773",
    "Output.pubkey_hash": "d693277604cac1e0861ab6ddf0655fac721f17c235f0aabcc9e9f6999df90099",
    "Output.script_hash": "54452f794077f1418dd41d56bc844bfaf44b3be0d422465d73b40ebdf0191a3f",
    "Transaction.fee": "6ac5b3a88a8bdf8a1d219c56ba0fee4e547c2d3ffa1c2cdff7ee35934e9eb608",
    "Transaction.id": "bac6bf453a7f3cdd446b2dffd60943aa02d8757a207ee78e30f284984dfea695",
    "Transaction.input_sum": "77c367ccf71154a325ebcef4f0731d04ddaccd41d3f5a0c90343fa6834d38295",
    "Transaction.output_sum": "2dc8ce7917177c03dc33b87124b9106edafaa2dd6619455c44d04c974d7cf0c8",
    "Transaction.raw": "109ab68599c6d3a509614d71062874b3d609ec890b440bb7db77acb5d86cc2eb",
    "Wallet.default_account": "76e84d5c63726f3c268e161ee2ef54e0573ab02a4aab04d9b7c6dae0fc95961e",
    "WalletManager.db": "591bfa029cbe61758280557e7e00e5f50f67e5cc6667226b54a83808749f1e93",
    "WalletManager.default_account": "6b5ae4ee1fd368d8b3bb05e3a8a3362a0f958f4e5385787958ff83fdb855e731",
    "WalletManager.default_wallet": "b985d6bbf6126a982f1f0084fc6872592cff7717f50b59fbe3a745f498c8de48",
    "WalletManager.get_transaction": "b71d91ee306c7fe80dbab674633b55b7a07adf314b2d4943e5414ef3641ad2aa",
    "WalletManager.ledger": "20539d4b6adaf2dc3570a00a20a7d9e7bd8653edeb5ae4433603c253a9e0205a",
    "dewies_to_lbc": "e134ee4ea5e7d5000bb7f3a1d37dd40b6913724e142ba5c6b8e1f235c064fc5b",
    "jsonrpc_dumps_pretty": "96430605d1c0312de2f3d13690cb38568b4b8671d383af71833639b3590c5fef",
    "satoshis_to_coins": "ff81838bc9fc0d2583372395b8299c1cd6aca6ee95b5e4819b28e883b2e1ad50",
}

PARENT_HEX = (
    "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff020101"
    "ffffffff0115cd5b07000000001976a914444444444444444444444444444444444444444488ac00000000"
)
TRANSACTION_HEX = (
    "0100000001af53c3ea5813b9277322f9790c1227926cde8f59b8a572f54f70df6afef33c1b"
    "0000000000ffffffff0200e1f505000000001976a9145555555555555555555555555555555555"
    "55555588ac0000000000000000046a02aabb00000000"
)
PARENT_ID = "1b3cf3fe6adf704ff572a5b8598fde6c9227120c79f9227327b91358eac353af"
TRANSACTION_ID = "e56ce90f16f0eed5500f2d6013cfc5cda251a53f0ca41618bc8300e987e0325c"
BASE58_ALPHABET = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"


def verify_source(sdk_root):
    commit = subprocess.run(
        ["git", "-C", str(sdk_root), "rev-parse", "HEAD"],
        check=True, capture_output=True, text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    for relative, expected in PINNED_SOURCE_HASHES.items():
        actual = hashlib.sha256((sdk_root / relative).read_bytes()).hexdigest()
        if actual != expected:
            raise RuntimeError(f"{relative} hash is {actual}, expected {expected}")


def selected_functions(path, names):
    source = path.read_text()
    selected, hashes = [], {}
    for node in ast.parse(source).body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in names:
            selected.append(copy.deepcopy(node))
            hashes[node.name] = hashlib.sha256(
                ast.get_source_segment(source, node).encode()
            ).hexdigest()
    return selected, hashes


def selected_methods(path, class_name, names):
    source = path.read_text()
    source_class = next(
        node for node in ast.parse(source).body
        if isinstance(node, ast.ClassDef) and node.name == class_name
    )
    selected, hashes = [], {}
    for node in source_class.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in names:
            selected.append(copy.deepcopy(node))
            hashes[f"{class_name}.{node.name}"] = hashlib.sha256(
                ast.get_source_segment(source, node).encode()
            ).hexdigest()
    return selected, hashes


def extracted_class(name, methods, base=None):
    bases = [] if base is None else [ast.Name(base, ast.Load())]
    return ast.ClassDef(name, bases, [], methods or [ast.Pass()], [])


def extract_sdk_slice(sdk_root):
    daemon_path = sdk_root / "lbry/extras/daemon/daemon.py"
    encoder_path = sdk_root / "lbry/extras/daemon/json_response_encoder.py"
    wallet_root = sdk_root / "lbry/wallet"

    dump_functions, dump_hashes = selected_functions(daemon_path, {"jsonrpc_dumps_pretty"})
    util_functions, util_hashes = selected_functions(wallet_root / "util.py", {"satoshis_to_coins"})
    dewies_functions, dewies_hashes = selected_functions(wallet_root / "dewies.py", {"dewies_to_lbc"})
    daemon_methods, daemon_hashes = selected_methods(
        daemon_path, "Daemon", {"ledger", "jsonrpc_transaction_show"},
    )
    for method in daemon_methods:
        if method.name == "jsonrpc_transaction_show":
            method.decorator_list = []
    error_methods, error_hashes = selected_methods(
        daemon_path, "JSONRPCError",
        {"__init__", "to_dict", "filter_traceback", "create_command_exception"},
    )
    encoder_methods, encoder_hashes = selected_methods(
        encoder_path, "JSONResponseEncoder",
        {"__init__", "default", "encode_transaction", "encode_output", "encode_input"},
    )
    manager_methods, manager_hashes = selected_methods(
        wallet_root / "manager.py", "WalletManager",
        {"default_wallet", "default_account", "ledger", "db", "get_transaction"},
    )
    wallet_methods, wallet_hashes = selected_methods(
        wallet_root / "wallet.py", "Wallet", {"default_account"},
    )
    database_methods, database_hashes = selected_methods(
        wallet_root / "database.py", "Database", {"get_transaction"},
    )
    network_methods, network_hashes = selected_methods(
        wallet_root / "network.py", "Network", {"get_transaction_and_merkle"},
    )
    code_error_methods, code_error_hashes = selected_methods(
        wallet_root / "rpc/jsonrpc.py", "CodeMessageError", {"code", "message"},
    )
    transaction_methods, transaction_hashes = selected_methods(
        wallet_root / "transaction.py", "Transaction",
        {"id", "raw", "input_sum", "output_sum", "fee"},
    )
    input_methods, input_hashes = selected_methods(
        wallet_root / "transaction.py", "Input", {"amount"},
    )
    output_methods, output_hashes = selected_methods(
        wallet_root / "transaction.py", "Output",
        {"is_pubkey_hash", "pubkey_hash", "is_script_hash", "script_hash", "has_address", "get_address"},
    )
    hashes = {
        **dump_hashes, **util_hashes, **dewies_hashes, **daemon_hashes,
        **error_hashes, **encoder_hashes, **manager_hashes, **wallet_hashes,
        **database_hashes, **network_hashes, **code_error_hashes,
        **transaction_hashes, **input_hashes, **output_hashes,
    }
    if hashes != PINNED_METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {PINNED_METHOD_HASHES}")

    module = ast.fix_missing_locations(ast.Module(body=[
        *util_functions,
        *dewies_functions,
        extracted_class("CodeMessageError", code_error_methods, "Exception"),
        extracted_class("Wallet", wallet_methods),
        extracted_class("Database", database_methods),
        extracted_class("Network", network_methods),
        extracted_class("Input", input_methods),
        extracted_class("Output", output_methods),
        extracted_class("Transaction", transaction_methods),
        extracted_class("WalletManager", manager_methods),
        extracted_class("JSONRPCError", error_methods),
        extracted_class("Daemon", daemon_methods),
        extracted_class("JSONResponseEncoder", encoder_methods, "JSONEncoder"),
        *dump_functions,
    ], type_ignores=[]))
    dummy = type("OracleDummy", (), {})
    namespace = {
        "Account": dummy,
        "Claim": dummy,
        "COIN": 100_000_000,
        "Decimal": Decimal,
        "DecodeError": type("DecodeError", (Exception,), {}),
        "JSONEncoder": JSONEncoder,
        "Ledger": dummy,
        "ManagedStream": dummy,
        "PublicKey": dummy,
        "Support": dummy,
        "TorrentSource": dummy,
        "datetime": datetime,
        "hexlify": hexlify,
        "json": json,
        "unhexlify": unhexlify,
    }
    exec(compile(module, str(daemon_path), "exec"), namespace)
    namespace["JSONRPCError"].CODE_APPLICATION_ERROR = -32500
    namespace["Transaction"].__init__ = fixture_transaction_init(namespace)
    return namespace, hashes


class FixtureScript:
    def __init__(self, source):
        self.source = source
        self.values = {}
        if len(source) == 25 and source[:3] == b"\x76\xa9\x14" and source[-2:] == b"\x88\xac":
            self.values["pubkey_hash"] = source[3:23]
        self.is_claim_name = False
        self.is_update_claim = False
        self.is_support_claim = False
        self.is_support_claim_data = False
        self.is_return_data = source.startswith(b"\x6a")
        self.is_claim_involved = False


class FixtureTransactionRef:
    def __init__(self, transaction):
        self.transaction = transaction

    @property
    def id(self):
        return self.transaction._id

    @property
    def height(self):
        return self.transaction.height


class FixtureOutputRef:
    def __init__(self, output):
        self.txo = output
        self.tx_ref = output.tx_ref
        self.position = output.position


def read_compact(raw, offset):
    value = raw[offset]
    if value >= 0xfd:
        raise ValueError("fixture only supports one-byte compact sizes")
    return value, offset + 1


def fixture_transaction_init(namespace):
    input_class = namespace["Input"]
    output_class = namespace["Output"]

    def initialize(self, raw=None, version=1, locktime=0, is_verified=False,
                   height=-2, position=-1, julian_day=None):
        del version, locktime, julian_day
        self._raw = bytes(raw or b"")
        self.height = height
        self.position = position
        self.is_verified = is_verified
        self._id = hashlib.sha256(hashlib.sha256(self._raw).digest()).digest()[::-1].hex()
        self.ref = FixtureTransactionRef(self)
        self.inputs, self.outputs = [], []
        offset = 4
        input_count, offset = read_compact(self._raw, offset)
        for input_position in range(input_count):
            previous_hash = self._raw[offset:offset+32]
            offset += 32
            previous_index = struct.unpack_from("<I", self._raw, offset)[0]
            offset += 4
            script_size, offset = read_compact(self._raw, offset)
            offset += script_size
            offset += 4
            value = input_class()
            value.position = input_position
            value.txo_ref = SimpleNamespace(
                tx_ref=SimpleNamespace(id=previous_hash[::-1].hex()),
                position=previous_index,
                txo=None,
            )
            self.inputs.append(value)
        output_count, offset = read_compact(self._raw, offset)
        for output_position in range(output_count):
            amount = struct.unpack_from("<Q", self._raw, offset)[0]
            offset += 8
            script_size, offset = read_compact(self._raw, offset)
            script = self._raw[offset:offset+script_size]
            offset += script_size
            value = output_class()
            value.amount = amount
            value.position = output_position
            value.tx_ref = self.ref
            value.script = FixtureScript(script)
            for attribute in (
                    "is_internal_transfer", "is_spent", "is_my_output", "is_my_input",
                    "sent_supports", "sent_tips", "received_tips", "purchase",
                    "purchased_claim", "purchase_receipt", "reposted_claim", "claims",
                    "channel", "private_key"):
                setattr(value, attribute, None)
            value.meta = {}
            value.ref = FixtureOutputRef(value)
            self.outputs.append(value)
        if offset + 4 != len(self._raw):
            raise ValueError("fixture transaction has trailing or missing bytes")

    return initialize


def base58check(prefix, value):
    payload = bytes([prefix]) + value
    encoded = payload + hashlib.sha256(hashlib.sha256(payload).digest()).digest()[:4]
    number = int.from_bytes(encoded, "big")
    result = ""
    while number:
        number, digit = divmod(number, 58)
        result = BASE58_ALPHABET[digit] + result
    leading = len(encoded) - len(encoded.lstrip(b"\x00"))
    return (BASE58_ALPHABET[0] * leading) + result


class FixtureHeaders:
    height = -1
    first_block_timestamp = 1466646588
    timestamp_average_offset = 160.6855883050695

    def __len__(self):
        return 0

    def estimated_timestamp(self, height, try_real_headers=True):
        del try_real_headers
        if height <= 0:
            return None
        return int(self.first_block_timestamp + height * self.timestamp_average_offset)


class FixtureLedger:
    def __init__(self):
        self.headers = FixtureHeaders()
        self.db = None
        self.network = None

    @staticmethod
    def hash160_to_address(value):
        return base58check(0x55, value)

    @staticmethod
    def hash160_to_script_address(value):
        return base58check(0x7a, value)


class FixtureDatabase:
    def __init__(self, database_class, namespace, mode, calls):
        self.__class__ = type("ExtractedFixtureDatabase", (FixtureDatabase, database_class), {})
        self.namespace = namespace
        self.mode = mode
        self.calls = calls
        self.sqlite = sqlite3.connect(":memory:")
        self.sqlite.execute("CREATE TABLE tx (txid TEXT PRIMARY KEY)")
        if mode == "local":
            self.sqlite.execute("INSERT INTO tx VALUES (?)", (TRANSACTION_ID,))

    async def get_transactions(self, **constraints):
        self.calls["database"].append(dict(constraints))
        row = self.sqlite.execute(
            "SELECT txid FROM tx WHERE txid = ? LIMIT ?",
            (constraints.get("txid"), constraints.get("limit", 1)),
        ).fetchone()
        return [local_transaction(self.namespace)] if row else []


class FixtureNetwork:
    def __init__(self, network_class, scenario, calls):
        self.__class__ = type("ExtractedFixtureNetwork", (FixtureNetwork, network_class), {})
        self.scenario = scenario
        self.calls = calls

    async def rpc(self, method, params, restricted):
        self.calls["network"].append({
            "method": method, "params": params, "restricted": restricted,
        })
        if self.scenario == "not_found":
            raise self.code_error(-5, "No such mempool or blockchain transaction.")
        if self.scenario == "not_found_substring":
            raise self.code_error(9, "prefix No such mempool or blockchain transaction. suffix")
        if self.scenario == "coded_error":
            raise self.code_error(73, "hub rejected request")
        height = 0 if self.scenario == "remote_zero" else -1
        return TRANSACTION_HEX, {"block_height": height}


def local_transaction(namespace):
    parent = namespace["Transaction"](unhexlify(PARENT_HEX), height=3, position=0, is_verified=True)
    transaction = namespace["Transaction"](
        unhexlify(TRANSACTION_HEX), height=7, position=1, is_verified=True,
    )
    transaction.inputs[0].txo_ref = parent.outputs[0].ref
    # Database.get_transactions applies TXO_NOT_MINE to raw outputs that do
    # not have a txo-table row. OP_RETURN outputs are not persisted as txos.
    transaction.outputs[1].is_my_output = False
    return transaction


def fixture_daemon(namespace, scenario):
    calls = {"database": [], "network": []}
    ledger = FixtureLedger()
    database = FixtureDatabase(
        namespace["Database"], namespace, "local" if scenario == "local" else "remote", calls,
    )
    network = FixtureNetwork(namespace["Network"], scenario, calls)
    network.code_error = namespace["CodeMessageError"]
    ledger.db, ledger.network = database, network
    account = SimpleNamespace(ledger=ledger)
    wallet = namespace["Wallet"]()
    wallet.accounts = [account]
    manager = namespace["WalletManager"]()
    manager.wallets = [wallet]
    daemon = namespace["Daemon"]()
    daemon.wallet_manager = manager
    return daemon, calls


def cases():
    mismatch = "f" * 64
    return [
        {"name": "local named", "scenario": "local", "params": {"txid": TRANSACTION_ID}},
        {"name": "local wrapped named", "scenario": "local", "params": [{"txid": TRANSACTION_ID}]},
        {"name": "local legacy positional", "scenario": "local", "params": [[TRANSACTION_ID], {}]},
        {"name": "local include protobuf stripped", "scenario": "local", "params": {
            "txid": TRANSACTION_ID, "include_protobuf": True,
        }},
        {"name": "remote mempool", "scenario": "remote_mempool", "params": {"txid": TRANSACTION_ID}},
        {"name": "remote zero height", "scenario": "remote_zero", "params": {"txid": TRANSACTION_ID}},
        {"name": "remote raw id wins", "scenario": "remote_mempool", "params": {"txid": mismatch}},
        {"name": "remote repeat is not cached", "scenario": "remote_mempool", "repetitions": 2,
         "params": {"txid": TRANSACTION_ID}},
        {"name": "remote not found", "scenario": "not_found", "params": {"txid": "0" * 64}},
        {"name": "remote not found substring", "scenario": "not_found_substring", "params": {
            "txid": "1" * 64,
        }},
        {"name": "remote other coded error", "scenario": "coded_error", "params": {"txid": "2" * 64}},
        {"name": "missing txid", "scenario": "remote_mempool", "omit_params": True},
    ]


def split_params(fixture):
    params = copy.deepcopy(fixture.get("params", {}))
    include_protobuf = False
    if isinstance(params, dict):
        include_protobuf = params.pop("include_protobuf", False)
        return [], params, include_protobuf
    if len(params) == 1:
        return [], dict(params[0]), False
    return list(params[0]), dict(params[1]), False


async def execute_case(namespace, fixture):
    daemon, calls = fixture_daemon(namespace, fixture["scenario"])
    args, kwargs, include_protobuf = split_params(fixture)
    responses = []
    for _ in range(fixture.get("repetitions", 1)):
        try:
            result = daemon.jsonrpc_transaction_show(*args, **kwargs)
            if asyncio.iscoroutine(result):
                result = await result
        except Exception as error:  # pylint: disable=broad-except
            result = namespace["JSONRPCError"].create_command_exception(
                "transaction_show", args, dict(kwargs), error, "oracle traceback",
            )
        encoded = namespace["jsonrpc_dumps_pretty"](
            result, ledger=daemon.ledger, include_protobuf=include_protobuf,
        )
        responses.append(json.loads(encoded))
    return {**fixture, "responses": responses, "calls": calls}


async def run_cases(namespace):
    return [await execute_case(namespace, fixture) for fixture in cases()]


def self_checks(namespace):
    parent = namespace["Transaction"](unhexlify(PARENT_HEX), height=3)
    transaction = local_transaction(namespace)
    return {
        "parent_id": parent.id == PARENT_ID,
        "transaction_id": transaction.id == TRANSACTION_ID,
        "payment_and_data": len(transaction.outputs) == 2 and
                            transaction.outputs[0].script.values.get("pubkey_hash") == bytes([0x55]) * 20 and
                            transaction.outputs[1].script.is_return_data,
        "resolved_local_input": transaction.inputs[0].amount == 123456789,
        "raw_round_trip": hexlify(transaction.raw).decode() == TRANSACTION_HEX,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    arguments = parser.parse_args()
    sdk_root = arguments.sdk_root.resolve()
    verify_source(sdk_root)
    namespace, method_hashes = extract_sdk_slice(sdk_root)
    checks = self_checks(namespace)
    if not all(checks.values()):
        raise RuntimeError(f"transaction-show adapter self-check failed: {checks}")
    results = asyncio.run(run_cases(namespace))
    print(json.dumps({
        "reference": {
            "commit": PINNED_COMMIT,
            "version": PINNED_VERSION,
            "source_sha256": PINNED_SOURCE_HASHES,
            "method_sha256": method_hashes,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "extracted_methods_executed": True,
            "stdlib_sqlite_used": True,
            "external_network_used": False,
            "fixture_transaction_id": TRANSACTION_ID,
            "fixture_has_payment_and_data": True,
            "claim_and_protobuf_covered": False,
            "case_count": len(results),
            "adapter_self_checks": checks,
        },
        "cases": results,
    }, sort_keys=True))


if __name__ == "__main__":
    main()

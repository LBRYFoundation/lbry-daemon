#!/usr/bin/env python3
"""Pinned legacy summarized transaction-history probes.

The probe AST-executes the summary and transaction-classification methods from
the pinned Python SDK over deterministic in-memory transaction objects.  It
does not import the SDK, open SQLite, or use an external network.
"""

import argparse
import ast
import asyncio
from binascii import hexlify
import copy
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import struct
import subprocess
import sys
import time
from types import SimpleNamespace
from typing import List, Optional


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/wallet/dewies.py": "67506d75a5f0ddb3f7c2ea832ba7b13fb49ae4193f060a1fdf541b5f50a3084a",
    "lbry/wallet/ledger.py": "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
    "lbry/wallet/transaction.py": "e73491aeb915fbce931acbb4d9631f3e05440a7d26c598db85e66e524a798d15",
    "lbry/wallet/util.py": "08f697c88ec36d2bb417609194266f279eba2f69b1a62a10b1de69b9c1733d5a",
}
PINNED_METHOD_HASHES = {
    "Input.amount": "ab8d93c4660ea7e42b32857b490008d04182917e50d8a83cef0fcb7f130f7e86",
    "Input.is_my_input": "1966d883a99c707deaa0ae1c47b43faaa3c25819a6c67f35f748b63447ee63a8",
    "Ledger.get_transaction_history": "4408177ae1675b712065a212f704c21bfed9deacf63865912cbaba74d853c57b",
    "Ledger.get_transaction_history_count": "e07b9cf91656d840be3445202d9dd17261a9353267049ec3c585ea4c87573bce",
    "Output.claim_hash": "f1ad49634a725bc8bbd1a0f1a8d4e5a46ae4abf6b4fec56a7f59566a7f14c462",
    "Output.claim_id": "634d3a6a1c74ba1e666f93d345007fb3be4544496c72bb3c0f89510b883ab98a",
    "Output.claim_name": "c1c0ec08ebb4716c45bf7a07b02f92d52e0d578aecd51155c1df28cbadda047c",
    "Output.get_address": "031f5c186213ba42ed354461e31d3d7075fda2cb285384485077f4e7adab1e8e",
    "Output.is_claim": "cfee1c756e3c11e46a1ef913c1ab0c4467a9f8aabe438f62536df483cc49e48e",
    "Output.is_pubkey_hash": "9ee9f33bac7e1e6fbd748dd79737073f799a7e64e516b81866c363d538b1f4d9",
    "Output.is_script_hash": "bd52941646cbd63eb7eb2df43c0c9708938f26045b1293b9f3d262eb565d0773",
    "Output.pubkey_hash": "d693277604cac1e0861ab6ddf0655fac721f17c235f0aabcc9e9f6999df90099",
    "Output.purchased_claim_id": "f2737848aa8850ab501dbbe429204a0b5f4d1bf9bae37f17dd91c0e4739375bf",
    "Output.script_hash": "54452f794077f1418dd41d56bc844bfaf44b3be0d422465d73b40ebdf0191a3f",
    "Transaction._filter_any_outputs": "8b9f8d4abfe5323f30f5bc80f047bb05befc9babe3989e331a1ae19b97737e84",
    "Transaction._filter_my_outputs": "7a0f573a6ad3eddfa3e9d5c799199e13baa0332473edc00cf77969ba9082c7f2",
    "Transaction._filter_other_outputs": "91108966d00ce81cdf2bfdb2c4b4f9711c47d94b08d24a232d020e68c7297a55",
    "Transaction.any_purchase_outputs": "f23fc63ae00d731bd1099e89b7c2cb09e5798fe272526a99a6667440e9bc19d0",
    "Transaction.fee": "6ac5b3a88a8bdf8a1d219c56ba0fee4e547c2d3ffa1c2cdff7ee35934e9eb608",
    "Transaction.input_sum": "77c367ccf71154a325ebcef4f0731d04ddaccd41d3f5a0c90343fa6834d38295",
    "Transaction.my_abandon_outputs": "acd69a62ebc395d9fb3402e624e5e454c1b5f4797e42aecaba8449e8bb4c71c5",
    "Transaction.my_claim_outputs": "21c9abc95568752c6d3f233c5cc4a3ee058e794f15edb7b22654219967b2cbda",
    "Transaction.my_support_outputs": "0486220c86b1a036c911cdfb1ceedd4d6cb4662d6035bd3e2a9907e04fddcb09",
    "Transaction.my_update_outputs": "ffbae1348f309b203f85f3b1c2b3fc3f26b9cda6f1e29376508304f34fa46b3f",
    "Transaction.net_account_balance": "2fde71a92ab994330517e7f18c88be9805c6a1a3f4f2d5697bfc4079bc050706",
    "Transaction.other_support_outputs": "f73e063377668f697ec8f1f513030ffa38d8641c429aada4c8d37a3c84faa76f",
    "Transaction.output_sum": "2dc8ce7917177c03dc33b87124b9106edafaa2dd6619455c44d04c974d7cf0c8",
    "dewies_to_lbc": "e134ee4ea5e7d5000bb7f3a1d37dd40b6913724e142ba5c6b8e1f235c064fc5b",
    "satoshis_to_coins": "ff81838bc9fc0d2583372395b8299c1cd6aca6ee95b5e4819b28e883b2e1ad50",
}

OUTPUT_METHODS = {
    "is_pubkey_hash", "pubkey_hash", "is_script_hash", "script_hash",
    "get_address", "is_claim", "claim_hash", "claim_id", "claim_name",
    "purchased_claim_id",
}
INPUT_METHODS = {"amount", "is_my_input"}
TRANSACTION_METHODS = {
    "input_sum", "output_sum", "net_account_balance", "fee",
    "_filter_my_outputs", "_filter_other_outputs", "_filter_any_outputs",
    "my_claim_outputs", "my_update_outputs", "my_support_outputs",
    "any_purchase_outputs", "other_support_outputs", "my_abandon_outputs",
}
LEDGER_METHODS = {"get_transaction_history", "get_transaction_history_count"}
BASE58_ALPHABET = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
COIN = 100_000_000


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


def extracted_functions(path, names):
    source = path.read_text()
    selected = []
    hashes = {}
    for node in ast.parse(source).body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in names:
            selected.append(copy.deepcopy(node))
            hashes[node.name] = hashlib.sha256(
                ast.get_source_segment(source, node).encode()
            ).hexdigest()
    return selected, hashes


def extracted_methods(path, class_name, names):
    source = path.read_text()
    source_class = next(
        node for node in ast.parse(source).body
        if isinstance(node, ast.ClassDef) and node.name == class_name
    )
    selected = []
    hashes = {}
    for node in source_class.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in names:
            selected.append(copy.deepcopy(node))
            hashes[f"{class_name}.{node.name}"] = hashlib.sha256(
                ast.get_source_segment(source, node).encode()
            ).hexdigest()
    return selected, hashes


def hash160(value):
    return hashlib.new("ripemd160", hashlib.sha256(value).digest()).digest()


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


def extract_sdk_slice(sdk_root):
    wallet_root = sdk_root / "lbry/wallet"
    util_functions, util_hashes = extracted_functions(
        wallet_root / "util.py", {"satoshis_to_coins"},
    )
    dewies_functions, dewies_hashes = extracted_functions(
        wallet_root / "dewies.py", {"dewies_to_lbc"},
    )
    output_methods, output_hashes = extracted_methods(
        wallet_root / "transaction.py", "Output", OUTPUT_METHODS,
    )
    input_methods, input_hashes = extracted_methods(
        wallet_root / "transaction.py", "Input", INPUT_METHODS,
    )
    transaction_methods, transaction_hashes = extracted_methods(
        wallet_root / "transaction.py", "Transaction", TRANSACTION_METHODS,
    )
    ledger_methods, ledger_hashes = extracted_methods(
        wallet_root / "ledger.py", "Ledger", LEDGER_METHODS,
    )
    hashes = {
        **util_hashes, **dewies_hashes, **output_hashes, **input_hashes,
        **transaction_hashes, **ledger_hashes,
    }
    if hashes != PINNED_METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {PINNED_METHOD_HASHES}")

    body = list(util_functions) + list(dewies_functions)
    body.extend([
        ast.ClassDef(
            name="Output", bases=[], keywords=[], body=output_methods,
            decorator_list=[],
        ),
        ast.ClassDef(
            name="Input", bases=[], keywords=[], body=input_methods,
            decorator_list=[],
        ),
        ast.ClassDef(
            name="Transaction", bases=[], keywords=[], body=transaction_methods,
            decorator_list=[],
        ),
        ast.ClassDef(
            name="Ledger", bases=[], keywords=[], body=ledger_methods,
            decorator_list=[],
        ),
    ])
    module = ast.fix_missing_locations(ast.Module(body=body, type_ignores=[]))
    namespace = {
        "COIN": COIN,
        "List": List,
        "Optional": Optional,
        "datetime": datetime,
        "hash160": hash160,
        "hexlify": hexlify,
        "struct": struct,
    }
    exec(compile(module, str(wallet_root / "ledger.py"), "exec"), namespace)
    ledger_class = namespace["Ledger"]
    ledger_class.hash160_to_address = lambda _self, value: base58check(0x55, value)
    ledger_class.hash160_to_script_address = lambda _self, value: base58check(0x7A, value)
    return namespace["Input"], namespace["Output"], namespace["Transaction"], ledger_class, hashes


class FixtureScript:
    def __init__(
            self, source, values=None, claim=False, update=False, support=False):
        self.source = bytes(source)
        self.values = values or {}
        self.is_claim_name = claim
        self.is_update_claim = update
        self.is_support_claim = support
        self.is_claim_involved = claim or update or support


def push(value):
    if len(value) >= 0x4C:
        raise ValueError("fixture push only supports direct data")
    return bytes([len(value)]) + value


def p2pkh(tag):
    public_key_hash = bytes([tag]) * 20
    return b"\x76\xa9" + push(public_key_hash) + b"\x88\xac", public_key_hash


def compact(value):
    if value < 0xFD:
        return bytes([value])
    if value <= 0xFFFF:
        return b"\xfd" + struct.pack("<H", value)
    if value <= 0xFFFFFFFF:
        return b"\xfe" + struct.pack("<I", value)
    return b"\xff" + struct.pack("<Q", value)


def make_script(kind, tag=None, claim_name=None, claim_id=None, claim=b"\x00"):
    if kind == "return":
        return FixtureScript(b"\x6a" + push(claim), {})
    payment, public_key_hash = p2pkh(tag)
    values = {"pubkey_hash": public_key_hash}
    if kind == "other":
        return FixtureScript(payment, values)
    name = claim_name.encode()
    values["claim_name"] = name
    if kind == "claim":
        values["claim"] = claim
        return FixtureScript(
            b"\xb5" + push(name) + push(claim) + b"\x6d\x75" + payment,
            values, claim=True,
        )
    internal_claim_id = bytes.fromhex(claim_id)[::-1]
    values["claim_id"] = internal_claim_id
    if kind == "update":
        values["claim"] = claim
        return FixtureScript(
            b"\xb7" + push(name) + push(internal_claim_id) + push(claim) +
            b"\x6d\x6d" + payment,
            values, update=True,
        )
    if kind == "support":
        return FixtureScript(
            b"\xb6" + push(name) + push(internal_claim_id) +
            b"\x6d\x75" + payment,
            values, support=True,
        )
    raise ValueError(f"unknown fixture output kind {kind}")


def fixture_output(
        output_class, amount, kind, tag=None, claim_name=None, claim_id=None,
        is_my_output=False, is_spent=False, claim=b"\x00"):
    output = output_class()
    output.amount = amount
    output.script = make_script(kind, tag, claim_name, claim_id, claim)
    output.tx_ref = None
    output.position = None
    output.is_my_output = is_my_output
    output.is_spent = is_spent
    output.purchase = None
    return output


def fixture_input(input_class, previous=None, missing_hash=None, missing_position=0):
    transaction_input = input_class()
    if previous is None:
        previous_hash = bytes.fromhex(missing_hash)
        transaction_input.txo_ref = SimpleNamespace(txo=None)
    else:
        previous_hash = previous.tx_ref.hash
        missing_position = previous.position
        transaction_input.txo_ref = SimpleNamespace(txo=previous)
    transaction_input.raw_previous_hash = previous_hash
    transaction_input.raw_previous_position = missing_position
    transaction_input.raw_script = b"\x51"
    transaction_input.raw_sequence = 0xFFFFFFFF
    return transaction_input


def coinbase_input(input_class, nonce):
    transaction_input = input_class()
    transaction_input.txo_ref = SimpleNamespace(txo=None)
    transaction_input.raw_previous_hash = bytes(32)
    transaction_input.raw_previous_position = 0xFFFFFFFF
    transaction_input.raw_script = struct.pack("<I", nonce)
    transaction_input.raw_sequence = 0xFFFFFFFF
    return transaction_input


def serialize_transaction(inputs, outputs, locktime):
    raw = bytearray(struct.pack("<I", 1))
    raw += compact(len(inputs))
    for transaction_input in inputs:
        raw += transaction_input.raw_previous_hash
        raw += struct.pack("<I", transaction_input.raw_previous_position)
        raw += compact(len(transaction_input.raw_script))
        raw += transaction_input.raw_script
        raw += struct.pack("<I", transaction_input.raw_sequence)
    raw += compact(len(outputs))
    for output in outputs:
        raw += struct.pack("<Q", output.amount)
        raw += compact(len(output.script.source))
        raw += output.script.source
    raw += struct.pack("<I", locktime)
    return bytes(raw)


def fixture_transaction(
        transaction_class, name, inputs, outputs, locktime, height=-2, position=-1):
    raw = serialize_transaction(inputs, outputs, locktime)
    transaction_hash = hashlib.sha256(hashlib.sha256(raw).digest()).digest()
    transaction = transaction_class()
    transaction.name = name
    transaction.raw = raw
    transaction.hash = transaction_hash
    transaction.id = transaction_hash[::-1].hex()
    transaction.height = height
    transaction.position = position
    transaction.inputs = inputs
    transaction.outputs = outputs
    reference = SimpleNamespace(hash=transaction_hash, id=transaction.id)
    for output_position, output in enumerate(outputs):
        output.position = output_position
        output.tx_ref = reference
    return transaction


def purchase_data_output(output_class, claim_id):
    internal = bytes.fromhex(claim_id)[::-1]
    payload = b"P\x0a" + bytes([len(internal)]) + internal
    output = fixture_output(output_class, 0, "return", claim=payload)
    output.is_my_output = False
    output.is_spent = None
    output.purchase_data = SimpleNamespace(claim_id=claim_id)
    return output


def build_fixtures(input_class, output_class, transaction_class):
    def parent(name, nonce, output):
        return fixture_transaction(
            transaction_class, name, [coinbase_input(input_class, nonce)],
            [output], nonce,
        )

    fund_claim = parent(
        "fund-claim", 101,
        fixture_output(output_class, 500_000_000, "other", tag=1, is_my_output=True, is_spent=True),
    )
    claim_output = fixture_output(
        output_class, 200_000_000, "claim", tag=11, claim_name="alpha",
        is_my_output=True, is_spent=True,
    )
    claim = fixture_transaction(
        transaction_class, "claim", [fixture_input(input_class, fund_claim.outputs[0])],
        [
            claim_output,
            fixture_output(output_class, 299_990_000, "other", tag=12, is_my_output=True),
        ], 201, 10, 1,
    )
    claim_id = claim_output.claim_id

    update_output = fixture_output(
        output_class, 50_000_000, "update", tag=13, claim_name="alpha",
        claim_id=claim_id, is_my_output=True, is_spent=True,
    )
    update = fixture_transaction(
        transaction_class, "update", [fixture_input(input_class, claim_output)],
        [
            update_output,
            fixture_output(output_class, 149_990_000, "other", tag=14, is_my_output=True),
        ], 202, 9, 2,
    )

    support_claim_id = "22" * 20
    prior_support = parent(
        "prior-support", 102,
        fixture_output(
            output_class, 100_000_000, "support", tag=2, claim_name="beta",
            claim_id=support_claim_id, is_my_output=True, is_spent=True,
        ),
    )
    abandon = fixture_transaction(
        transaction_class, "abandon", [
            fixture_input(input_class, update_output),
            fixture_input(input_class, prior_support.outputs[0]),
        ], [
            fixture_output(output_class, 149_990_000, "other", tag=15, is_my_output=True),
        ], 203, 8, 3,
    )

    fund_supports = parent(
        "fund-supports", 103,
        fixture_output(output_class, 700_000_000, "other", tag=3, is_my_output=True, is_spent=True),
    )
    supports = fixture_transaction(
        transaction_class, "out-supports",
        [fixture_input(input_class, fund_supports.outputs[0])], [
            fixture_output(
                output_class, 200_000_000, "support", tag=16, claim_name="beta",
                claim_id=support_claim_id, is_my_output=False,
            ),
            fixture_output(
                output_class, 100_000_000, "support", tag=17, claim_name="beta",
                claim_id=support_claim_id, is_my_output=True,
            ),
            fixture_output(output_class, 399_990_000, "other", tag=18, is_my_output=True),
        ], 204, 7, 4,
    )

    incoming_purchase_id = "44" * 20
    incoming_payment = fixture_output(
        output_class, 70_000_000, "other", tag=19, is_my_output=True,
    )
    incoming_data = purchase_data_output(output_class, incoming_purchase_id)
    incoming_payment.purchase = incoming_data
    incoming = fixture_transaction(
        transaction_class, "incoming-combo", [fixture_input(
            input_class, missing_hash="11" * 32, missing_position=4,
        )], [
            incoming_payment,
            incoming_data,
            fixture_output(
                output_class, 60_000_000, "update", tag=20, claim_name="gamma",
                claim_id="33" * 20, is_my_output=True,
            ),
            fixture_output(
                output_class, 100_000_000, "support", tag=21, claim_name="gamma",
                claim_id="33" * 20, is_my_output=True,
            ),
            fixture_output(
                output_class, 50_000_000, "support", tag=22, claim_name="gamma",
                claim_id="33" * 20, is_my_output=False,
            ),
        ], 205, 0, 5,
    )

    fund_purchase = parent(
        "fund-purchase", 104,
        fixture_output(output_class, 400_000_000, "other", tag=4, is_my_output=True, is_spent=True),
    )
    outgoing_purchase_id = "55" * 20
    outgoing_payment = fixture_output(
        output_class, 300_000_000, "other", tag=23, is_my_output=False,
    )
    outgoing_data = purchase_data_output(output_class, outgoing_purchase_id)
    outgoing_payment.purchase = outgoing_data
    purchase = fixture_transaction(
        transaction_class, "out-purchase",
        [fixture_input(input_class, fund_purchase.outputs[0])], [
            outgoing_payment,
            outgoing_data,
            fixture_output(output_class, 99_990_000, "other", tag=24, is_my_output=True),
        ], 206, -1, 6,
    )

    # This is the exact default Database.select_transactions ordering for the
    # selected heights: height zero first, then descending height.
    selected = [incoming, claim, update, abandon, supports, purchase]
    return selected


class FixtureDatabase:
    def __init__(self, transactions):
        self.transactions = transactions
        self.calls = []

    async def get_transactions(self, **constraints):
        self.calls.append({"method": "get_transactions", "constraints": constraints})
        return self.transactions

    def get_transaction_count(self, **constraints):
        self.calls.append({"method": "get_transaction_count", "constraints": constraints})
        return len(self.transactions)


class FixtureHeaders:
    height = 20
    first_block_timestamp = 1466646588
    timestamp_average_offset = 160.6855883050695

    def estimated_timestamp(self, height):
        if height <= 0:
            return None
        return int(self.first_block_timestamp + (height * self.timestamp_average_offset))


async def run_cases(input_class, output_class, transaction_class, ledger_class):
    transactions = build_fixtures(input_class, output_class, transaction_class)
    database = FixtureDatabase(transactions)
    ledger = ledger_class()
    ledger.db = database
    ledger.headers = FixtureHeaders()
    transaction_ids = [transaction.id for transaction in transactions]
    constraints = {
        "accounts": ["account-a"],
        "txid__in": transaction_ids,
        "offset": 0,
        "limit": 6,
    }
    history = await ledger.get_transaction_history(read_only=True, **constraints)
    count = ledger.get_transaction_history_count(read_only=True, **constraints)
    return history, count, database.calls, transaction_ids


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    arguments = parser.parse_args()
    sdk_root = arguments.sdk_root.resolve()

    os.environ["TZ"] = "UTC"
    if hasattr(time, "tzset"):
        time.tzset()
    verify_source(sdk_root)
    input_class, output_class, transaction_class, ledger_class, method_hashes = \
        extract_sdk_slice(sdk_root)
    history, count, database_calls, transaction_ids = asyncio.run(run_cases(
        input_class, output_class, transaction_class, ledger_class,
    ))
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
            "stdlib_sqlite_used": False,
            "external_network_used": False,
            "timezone": "UTC",
            "fixture_transactions": len(history),
        },
        "transaction_ids": transaction_ids,
        "history": history,
        "count": count,
        "database_calls": database_calls,
    }, sort_keys=True))


if __name__ == "__main__":
    main()

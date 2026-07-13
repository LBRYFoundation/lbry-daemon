#!/usr/bin/env python3
"""Pinned offline probes for resolved transaction-output relationships.

The real JSONResponseEncoder methods are AST-extracted from SDK 0.113.0 and
run against deterministic Output-shaped fixtures containing real v2 Claim
objects.  The fixtures model links populated by transaction lookup, resolve,
collection resolution, and purchase lookup without using a wallet database or
the network.
"""

import argparse
import asyncio
import ast
from binascii import hexlify, unhexlify
import copy
from datetime import datetime
from decimal import Decimal
import hashlib
import json
from json import JSONEncoder
import os
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace
import types
from typing import List, Tuple


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py":
        "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/extras/daemon/daemon.py":
        "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
    "lbry/extras/daemon/json_response_encoder.py":
        "047fd406c20236025414b8805669b1a830b0b412386c1613498aa1ebaa021732",
    "lbry/schema/attrs.py":
        "e2c01abf8a152ca224f557d38a4932b40ce0ceb880c27b2dbe0bca15c4a51624",
    "lbry/schema/base.py":
        "898875eebd916eee0ea4ad9e2be8aff53f8f56a1479f664e143f0461ffba7140",
    "lbry/schema/claim.py":
        "2b2a58f580efc2d5ea7bbfadfff28ea150429dbcc71fcefaf8992fa5213027af",
    "lbry/schema/result.py":
        "b5a506fedc9f40c5e9ea1b0691e1e36f9559acaabafe9e3599ed7db52031a4cf",
    "lbry/schema/types/v2/claim_pb2.py":
        "3edb36895d7d2f294e27019438332ca8a7ed4cb3c0f30ee33c9aa406bf000c98",
    "lbry/wallet/database.py":
        "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
    "lbry/wallet/dewies.py":
        "67506d75a5f0ddb3f7c2ea832ba7b13fb49ae4193f060a1fdf541b5f50a3084a",
    "lbry/wallet/ledger.py":
        "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
    "lbry/wallet/manager.py":
        "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
    "lbry/wallet/transaction.py":
        "e73491aeb915fbce931acbb4d9631f3e05440a7d26c598db85e66e524a798d15",
    "lbry/wallet/util.py":
        "08f697c88ec36d2bb417609194266f279eba2f69b1a62a10b1de69b9c1733d5a",
}
PINNED_METHOD_HASHES = {
    "Daemon.jsonrpc_transaction_show":
        "10ec4201cf4cce44bf3442ff6654732bc2d99a1634e905e5d65cc1741d898ad6",
    "Database.get_purchases":
        "30771b54e4dd985ef5aa714f7a88534aed3362999d3d541631ce98e00bf0b901",
    "Database.get_transactions":
        "5c5bff04bc5b5d0a3e3f402d421226ede46c59f9ba481137b9d6de2120efdf2f",
    "Database.tx_to_row":
        "631c38db61f579f5e4fc4f934da89e17fe5f86097646a758ed83c7b6f68b9c8b",
    "JSONResponseEncoder.__init__":
        "bf1a658c1eed62bbae283ebe132f8067f986e534771ebf3417685536472fdb1e",
    "JSONResponseEncoder.default":
        "298986ed087ef927a948ecc2d8f55730ca2e57a9c6ec032255d30bc92448c4a8",
    "JSONResponseEncoder.encode_claim":
        "c537d439cc940682b1954615726587d125615e5d4bda62f26b9e78085c5ed088",
    "JSONResponseEncoder.encode_claim_meta":
        "7998df829f2f3a45d3f851ec1fa08910d4c6106c58a0a5a22690390ff8371c05",
    "JSONResponseEncoder.encode_output":
        "fc124a8362451a2449d83b06e252d9c3d85ec6b006b5f9d0dc5dfd60b5db92be",
    "Ledger._inflate_outputs":
        "2eb53ed61cabd4456010c5c3c23ec848c5888ca749acb68ec864fc1e92be5cfe",
    "Ledger.get_purchases":
        "a10b63da0d141f7f094eb0d85f8734f4743dcbb76b5fecb5928d692cb6fe2bbb",
    "Ledger.resolve_collection":
        "9692e89042901f82f8a5f5cb06b300bf7ced49f5aadd4718da7ad2cc3c6c7ef3",
    "Output.purchased_claim_id":
        "f2737848aa8850ab501dbbe429204a0b5f4d1bf9bae37f17dd91c0e4739375bf",
    "Outputs.message_to_txo":
        "4369696def2c977a904df2db3d397219bf2b2e1a6e0c3550f3ad184b286d1ce5",
    "WalletManager.get_transaction":
        "b71d91ee306c7fe80dbab674633b55b7a07adf314b2d4943e5414ef3641ad2aa",
}

CLAIM_A = "11" * 20
CLAIM_B = "22" * 20
CLAIM_C = "33" * 20
CHANNEL_A = "aa" * 20
CHANNEL_B = "bb" * 20
TXID = "cd" * 32


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


def method_hash(path, class_name, method_name):
    source = path.read_text()
    scope = next(
        node.body for node in ast.parse(source).body
        if isinstance(node, ast.ClassDef) and node.name == class_name
    )
    node = next(
        node for node in scope
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name == method_name
    )
    return hashlib.sha256(ast.get_source_segment(source, node).encode()).hexdigest()


def verify_method_hashes(sdk_root):
    specs = {
        "Daemon.jsonrpc_transaction_show":
            ("lbry/extras/daemon/daemon.py", "Daemon", "jsonrpc_transaction_show"),
        "Database.get_purchases":
            ("lbry/wallet/database.py", "Database", "get_purchases"),
        "Database.get_transactions":
            ("lbry/wallet/database.py", "Database", "get_transactions"),
        "Database.tx_to_row":
            ("lbry/wallet/database.py", "Database", "tx_to_row"),
        "Ledger._inflate_outputs":
            ("lbry/wallet/ledger.py", "Ledger", "_inflate_outputs"),
        "Ledger.get_purchases":
            ("lbry/wallet/ledger.py", "Ledger", "get_purchases"),
        "Ledger.resolve_collection":
            ("lbry/wallet/ledger.py", "Ledger", "resolve_collection"),
        "Output.purchased_claim_id":
            ("lbry/wallet/transaction.py", "Output", "purchased_claim_id"),
        "Outputs.message_to_txo":
            ("lbry/schema/result.py", "Outputs", "message_to_txo"),
        "WalletManager.get_transaction":
            ("lbry/wallet/manager.py", "WalletManager", "get_transaction"),
    }
    for method in ("__init__", "default", "encode_output", "encode_claim_meta", "encode_claim"):
        specs[f"JSONResponseEncoder.{method}"] = (
            "lbry/extras/daemon/json_response_encoder.py", "JSONResponseEncoder", method,
        )
    hashes = {
        name: method_hash(sdk_root / relative, class_name, method_name)
        for name, (relative, class_name, method_name) in specs.items()
    }
    if hashes != PINNED_METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {PINNED_METHOD_HASHES}")
    return hashes


def selected_functions(path, names):
    source = path.read_text()
    return [
        copy.deepcopy(node) for node in ast.parse(source).body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in names
    ]


def selected_methods(path, class_name, names):
    source_class = next(
        node for node in ast.parse(path.read_text()).body
        if isinstance(node, ast.ClassDef) and node.name == class_name
    )
    return [
        copy.deepcopy(node) for node in source_class.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in names
    ]


def extracted_class(name, methods, base=None):
    bases = [] if base is None else [ast.Name(base, ast.Load())]
    return ast.ClassDef(name, bases, [], methods or [ast.Pass()], [])


def extract_encoder(sdk_root, Claim, DecodeError):
    encoder_path = sdk_root / "lbry/extras/daemon/json_response_encoder.py"
    methods = selected_methods(
        encoder_path, "JSONResponseEncoder",
        {"__init__", "default", "encode_output", "encode_claim_meta", "encode_claim"},
    )
    module = ast.fix_missing_locations(ast.Module(body=[
        *selected_functions(sdk_root / "lbry/wallet/util.py", {"satoshis_to_coins"}),
        *selected_functions(sdk_root / "lbry/wallet/dewies.py", {"dewies_to_lbc"}),
        extracted_class("JSONResponseEncoder", methods, "JSONEncoder"),
    ], type_ignores=[]))
    namespace = {
        "Account": type("Account", (), {}),
        "Claim": Claim,
        "COIN": 100_000_000,
        "Decimal": Decimal,
        "DecodeError": DecodeError,
        "JSONEncoder": JSONEncoder,
        "Ledger": type("Ledger", (), {}),
        "ManagedStream": type("ManagedStream", (), {}),
        "Output": type("Output", (), {}),
        "PublicKey": type("PublicKey", (), {}),
        "Support": type("Support", (), {}),
        "TorrentSource": type("TorrentSource", (), {}),
        "Transaction": type("Transaction", (), {}),
        "Wallet": type("Wallet", (), {}),
        "datetime": datetime,
        "hexlify": hexlify,
        "unhexlify": unhexlify,
    }
    exec(compile(module, str(encoder_path), "exec"), namespace)
    return namespace


def extract_ledger_relationship_methods(sdk_root):
    ledger_path = sdk_root / "lbry/wallet/ledger.py"
    methods = selected_methods(
        ledger_path, "Ledger", {"_inflate_outputs", "get_purchases", "resolve_collection"},
    )
    module = ast.fix_missing_locations(ast.Module(body=[
        extracted_class("PinnedLedgerRelationshipMethods", methods),
    ], type_ignores=[]))
    namespace = {
        "CLAIM_TYPES": (1, 2, 5, 6),
        "List": List,
        "Output": FixtureOutput,
        "Outputs": FixtureInflatedOutputs,
        "TXO_TYPES": {"support": 3},
        "Transaction": type("Transaction", (), {}),
        "Tuple": Tuple,
        "copy": copy,
        "log": SimpleNamespace(exception=lambda *_args, **_kwargs: None),
    }
    exec(compile(module, str(ledger_path), "exec"), namespace)
    return namespace["PinnedLedgerRelationshipMethods"]


def install_optional_dependency_stubs():
    filetype = types.ModuleType("filetype")
    filetype.guess = lambda _path: None
    sys.modules["filetype"] = filetype

    asn1crypto = types.ModuleType("asn1crypto")
    asn1crypto.__path__ = []
    sys.modules["asn1crypto"] = asn1crypto
    asn1crypto_keys = types.ModuleType("asn1crypto.keys")
    asn1crypto_keys.PublicKeyInfo = type("PublicKeyInfo", (), {})
    sys.modules["asn1crypto.keys"] = asn1crypto_keys

    coincurve = types.ModuleType("coincurve")
    coincurve.PublicKey = type("PublicKey", (), {})
    sys.modules["coincurve"] = coincurve

    hachoir = types.ModuleType("hachoir")
    hachoir.__path__ = []
    sys.modules["hachoir"] = hachoir
    hachoir_core = types.ModuleType("hachoir.core")
    hachoir_core.__path__ = []
    sys.modules["hachoir.core"] = hachoir_core
    hachoir_log = types.ModuleType("hachoir.core.log")
    hachoir_log.log = type("Log", (), {"use_print": False})()
    sys.modules["hachoir.core.log"] = hachoir_log
    hachoir_parser = types.ModuleType("hachoir.parser")
    hachoir_parser.createParser = lambda _path: None
    sys.modules["hachoir.parser"] = hachoir_parser
    hachoir_metadata = types.ModuleType("hachoir.metadata")
    hachoir_metadata.extractMetadata = lambda _parser: None
    sys.modules["hachoir.metadata"] = hachoir_metadata


class FixtureHeaders:
    height = 20

    @staticmethod
    def estimated_timestamp(height, try_real_headers=True):
        del try_real_headers
        return 10_000 + height if height > 0 else None


class FixtureLedger:
    headers = FixtureHeaders()

    @staticmethod
    def public_key_to_address(public_key):
        return f"fixture-key-{public_key.hex()[:8]}"


class FixtureScript:
    def __init__(self, kind):
        self.is_claim_name = kind == "create"
        self.is_update_claim = kind == "update"
        self.is_support_claim = False
        self.is_support_claim_data = False
        self.is_return_data = kind == "data"
        self.is_claim_involved = kind in ("create", "update")


class FixtureOutput:
    def __init__(
            self, label, kind="create", claim=None, claim_id=None, name=None,
            position=0, decode_error=None, signature_result=True,
            signature_error=None):
        self.label = label
        self.tx_ref = SimpleNamespace(id=TXID, height=12)
        self.position = position
        self.amount = 100_000_000 + position
        self.script = FixtureScript(kind)
        self.address = f"address-{label}"
        self.has_address = kind != "data"
        self.is_spent = None
        self.is_my_output = None
        self.is_my_input = None
        self.sent_supports = None
        self.sent_tips = None
        self.received_tips = None
        self.is_internal_transfer = None
        self.purchase = None
        self.purchased_claim = None
        self.purchase_receipt = None
        self.reposted_claim = None
        self.claims = None
        self.channel = None
        self.private_key = None
        self.meta = {}
        self.claim_name = name or label
        self.normalized_name = self.claim_name.casefold()
        self.claim_id = claim_id or CLAIM_A
        self.permanent_url = f"lbry://{self.claim_name}#{self.claim_id}"
        self.has_private_key = False
        self.purchase_data = None
        self.has_price = False
        self._claim = claim
        self._decode_error = decode_error
        self._signature_result = signature_result
        self._signature_error = signature_error
        self.signature_trace = []

    def get_address(self, ledger):
        del ledger
        return self.address

    @property
    def claim(self):
        if self._decode_error is not None:
            raise self._decode_error
        return self._claim

    @property
    def signable(self):
        return self.claim

    @property
    def purchased_claim_id(self):
        if self.purchase is not None:
            return self.purchase.purchase_data.claim_id
        if self.purchased_claim is not None:
            return self.purchased_claim.claim_id
        return None

    def is_signed_by(self, channel, ledger):
        del channel, ledger
        self.signature_trace.append(self.label)
        if self._signature_error is not None:
            raise self._signature_error
        return self._signature_result

    @property
    def can_decode_claim(self):
        return self.script.is_claim_involved and self._decode_error is None and self._claim is not None

    def update_annotations(self, annotated):
        if annotated is None:
            self.is_internal_transfer = None
            self.is_spent = None
            self.is_my_output = None
            self.is_my_input = None
            self.sent_supports = None
            self.sent_tips = None
            self.received_tips = None
        else:
            self.is_internal_transfer = annotated.is_internal_transfer
            self.is_spent = annotated.is_spent
            self.is_my_output = annotated.is_my_output
            self.is_my_input = annotated.is_my_input
            self.sent_supports = annotated.sent_supports
            self.sent_tips = annotated.sent_tips
            self.received_tips = annotated.received_tips
        self.channel = annotated.channel if annotated else None
        self.private_key = annotated.private_key if annotated else None


class FixtureInflatedOutputs:
    current = None

    def __init__(self, outputs, offset=0, total=0):
        self.outputs = outputs
        self.offset = offset
        self.total = total
        self.txs = []

    @classmethod
    def from_base64(cls, encoded):
        del encoded
        return cls.current

    def inflate(self, transactions):
        del transactions
        return self.outputs, {"total": 0, "channels": []}


def make_claim(Claim, kind, title, signed_channel_id=None):
    claim = Claim()
    branch = getattr(claim, kind)
    branch.title = title
    if kind == "stream":
        branch.description = f"description:{title}"
    elif kind == "channel":
        branch.public_key_bytes = b"\x02" + bytes([len(title) & 0xff]) * 32
    elif kind == "repost":
        branch.reference.claim_id = CLAIM_A
    elif kind == "collection":
        branch.claims.extend([CLAIM_A, CLAIM_B, CLAIM_C])
    if signed_channel_id is not None:
        claim.signing_channel_id = signed_channel_id
        claim.signature = bytes(range(64))
    return claim


def claim_output(Claim, label, claim_id, kind="stream", signed=False, channel_id=CHANNEL_A):
    claim = make_claim(Claim, kind, label, channel_id if signed else None)
    return FixtureOutput(label, claim=claim, claim_id=claim_id)


def purchase_data(label, claim_id):
    output = FixtureOutput(label, kind="data", position=1)
    output.purchase_data = SimpleNamespace(claim_id=claim_id)
    return output


def attach_channel(Claim, output, label="@channel", claim_id=CHANNEL_A):
    output.channel = claim_output(Claim, label, claim_id, kind="channel")
    return output


def fixture_cases(Claim, DecodeError):
    cases = []

    repost_local = claim_output(Claim, "repost-local", CLAIM_B, kind="repost")
    cases.append(("repost_local_transaction_show_unresolved_plain", "local_raw", False, True, repost_local))

    repost_remote = claim_output(Claim, "repost-remote", CLAIM_B, kind="repost")
    repost_remote.reposted_claim = attach_channel(
        Claim, claim_output(Claim, "original", CLAIM_A, signed=True), "@original", CHANNEL_A,
    )
    cases.append(("repost_remote_resolved_protobuf", "remote_resolved", True, True, repost_remote))

    repost_no_check = attach_channel(
        Claim, claim_output(Claim, "repost-no-check", CLAIM_B, kind="repost", signed=True),
        "@reposter", CHANNEL_B,
    )
    repost_no_check.reposted_claim = attach_channel(
        Claim, claim_output(Claim, "nested-original", CLAIM_A, signed=True),
        "@nested", CHANNEL_A,
    )
    cases.append(("repost_check_signature_false_nested_checked", "resolved_input", False, False, repost_no_check))

    repost_malformed = claim_output(Claim, "repost-malformed", CLAIM_B, kind="repost")
    repost_malformed.reposted_claim = FixtureOutput(
        "malformed-original", claim_id=CLAIM_A,
        decode_error=DecodeError("fixture malformed nested claim"),
    )
    cases.append(("repost_nested_decode_error_suppressed", "remote_resolved", True, True, repost_malformed))

    collection_local = claim_output(Claim, "collection-local", CLAIM_C, kind="collection")
    cases.append(("collection_local_transaction_show_unresolved_plain", "local_raw", False, True, collection_local))

    collection_resolved = claim_output(Claim, "collection-resolved", CLAIM_C, kind="collection")
    collection_resolved.claims = [
        attach_channel(Claim, claim_output(Claim, "alpha", CLAIM_A, signed=True)),
        None,
        claim_output(Claim, "beta", CLAIM_B),
    ]
    cases.append(("collection_remote_resolved_ordered_protobuf", "remote_resolved", True, True, collection_resolved))

    collection_empty = claim_output(Claim, "collection-empty", CLAIM_C, kind="collection")
    collection_empty.claims = []
    cases.append(("collection_remote_resolved_empty", "remote_resolved", False, True, collection_empty))

    collection_fallback = claim_output(Claim, "collection-fallback", CLAIM_C, kind="collection")
    collection_fallback.claims = [claim_output(Claim, "orphan-signed", CLAIM_A, signed=True)]
    cases.append(("collection_signed_channel_absent_fallback", "remote_resolved", False, True, collection_fallback))

    remote_payment = FixtureOutput("remote-payment", kind="payment")
    remote_payment.purchased_claim = claim_output(Claim, "ignored-claim", CLAIM_A)
    cases.append(("purchase_remote_transaction_show_unlinked_payment", "remote_raw", True, True, remote_payment))

    local_purchase = FixtureOutput("local-purchase", kind="payment")
    local_purchase.purchase = purchase_data("local-purchase-data", CLAIM_A)
    cases.append(("purchase_local_linked_unresolved", "local_raw", False, True, local_purchase))

    resolved_purchase = FixtureOutput("resolved-purchase", kind="payment")
    resolved_purchase.purchase = purchase_data("resolved-purchase-data", CLAIM_A)
    resolved_purchase.purchased_claim = attach_channel(
        Claim, claim_output(Claim, "purchased-stream", CLAIM_A, signed=True),
    )
    cases.append(("purchase_resolved_claim_protobuf", "purchase_resolved", True, True, resolved_purchase))

    no_check_purchase = FixtureOutput("no-check-purchase", kind="payment")
    no_check_purchase.purchase = purchase_data("no-check-purchase-data", CLAIM_A)
    no_check_purchase.purchased_claim = attach_channel(
        Claim, claim_output(Claim, "nested-purchased", CLAIM_A, signed=True),
    )
    cases.append(("purchase_check_signature_false_nested_checked", "purchase_resolved", False, False, no_check_purchase))

    receipt_absent = claim_output(Claim, "priced-local", CLAIM_A)
    cases.append(("purchase_receipt_local_absent", "local_raw", True, True, receipt_absent))

    receipt_claim = claim_output(Claim, "priced-resolved", CLAIM_A)
    receipt = FixtureOutput("receipt-payment", kind="payment")
    receipt.purchase = purchase_data("receipt-data", CLAIM_A)
    receipt_claim.purchase_receipt = receipt
    cases.append(("purchase_receipt_resolved_protobuf", "receipt_resolved", True, True, receipt_claim))

    malformed_purchase = FixtureOutput("malformed-purchase", kind="payment")
    malformed_purchase.purchase = purchase_data("malformed-purchase-data", CLAIM_A)
    malformed_purchase.purchased_claim = FixtureOutput(
        "malformed-purchased-claim", claim_id=CLAIM_A,
        decode_error=DecodeError("fixture malformed purchased claim"),
    )
    cases.append(("purchase_nested_decode_error_suppressed", "purchase_resolved", True, True, malformed_purchase))

    signature_failure = claim_output(Claim, "signature-failure-repost", CLAIM_B, kind="repost")
    failing_nested = claim_output(Claim, "signature-failure-original", CLAIM_A, signed=True)
    failing_nested._signature_error = RuntimeError("fixture signature verification failed")
    signature_failure.reposted_claim = attach_channel(Claim, failing_nested)
    cases.append(("signature_error_propagates", "remote_resolved", True, True, signature_failure))

    repost_cycle = claim_output(Claim, "repost-cycle", CLAIM_B, kind="repost")
    repost_cycle.reposted_claim = repost_cycle
    cases.append(("repost_cycle_recursion_error", "remote_resolved", False, True, repost_cycle))

    invalid_collection = claim_output(Claim, "collection-invalid", CLAIM_C, kind="collection")
    invalid_collection.claims = [object()]
    cases.append(("collection_invalid_entry_error", "remote_resolved", False, True, invalid_collection))
    return cases


def set_signature_trace(output, trace, seen=None):
    if output is None or not isinstance(output, FixtureOutput):
        return
    seen = seen or set()
    if id(output) in seen:
        return
    seen.add(id(output))
    output.signature_trace = trace
    for child in (output.channel, output.reposted_claim, output.purchased_claim, output.purchase_receipt):
        set_signature_trace(child, trace, seen)
    if output.claims is not None:
        for child in output.claims:
            set_signature_trace(child, trace, seen)


def collect_field_order(value, path="$", result=None):
    result = result or {}
    if isinstance(value, dict):
        result[path] = list(value)
        for key, child in value.items():
            collect_field_order(child, f"{path}.{key}", result)
    elif isinstance(value, list):
        for index, child in enumerate(value):
            collect_field_order(child, f"{path}[{index}]", result)
    return result


def error_dict(error, stage):
    return {
        "stage": stage,
        "type": type(error).__name__,
        "module": type(error).__module__,
        "message": str(error),
    }


def execute_case(fixture, encoder_class):
    name, source_mode, include_protobuf, check_signature, output = fixture
    trace = []
    set_signature_trace(output, trace)
    encoder = encoder_class(ledger=FixtureLedger(), include_protobuf=include_protobuf)
    result = {
        "name": name,
        "source_mode": source_mode,
        "include_protobuf": include_protobuf,
        "check_signature": check_signature,
        "encoded_output": None,
        "field_order": None,
        "signature_checks": trace,
        "error": None,
    }
    try:
        encoded = encoder.encode_output(output, check_signature=check_signature)
    except Exception as error:
        result["error"] = error_dict(error, "JSONResponseEncoder.encode_output")
        return result
    try:
        projected = json.loads(encoder.encode(encoded))
    except Exception as error:
        result["error"] = error_dict(error, "JSONResponseEncoder JSON serialization")
        return result
    result["encoded_output"] = projected
    result["field_order"] = collect_field_order(projected)
    return result


def execute_annotation_probe(Claim, encoder_class):
    output = claim_output(Claim, "annotation-probe", CLAIM_A)
    output.sent_supports = 0
    output.sent_tips = 123_456_789
    output.received_tips = -1
    output.meta = {
        "short_url": "lbry://annotation#1",
        "canonical_url": "lbry://@channel#2/annotation#1",
        "effective_amount": 123_456_789,
        "support_amount": 10,
        "truthy_amount": True,
        "floating_amount": 2.0,
        "text_amount": "3",
        "creation_height": 3,
        "creation_timestamp": 999,
    }
    source_meta = output.meta.copy()
    result = execute_case(
        ("annotation_probe", "remote_resolved", False, True, output),
        encoder_class,
    )
    if result["error"] is not None:
        raise RuntimeError(f"annotation probe failed: {result['error']}")
    return {
        "encoded_output": result["encoded_output"],
        "source_meta_after_encode": source_meta,
        "fixture_meta_after_encode": output.meta,
    }


async def execute_ledger_relationship_probes(Claim, ledger_methods):
    collection = claim_output(Claim, "resolver-collection", CLAIM_C, kind="collection")
    del collection.claim.collection.claims[:]
    collection.claim.collection.claims.extend([CLAIM_C, CLAIM_A, CLAIM_B, CLAIM_A, CLAIM_C])
    references = collection.claim.collection.claims.ids
    claim_a_first = claim_output(Claim, "claim-a-first", CLAIM_A)
    claim_a_second = claim_output(Claim, "claim-a-second", CLAIM_A)
    claim_b = claim_output(Claim, "claim-b", CLAIM_B)

    collection_queries = []
    collection_ledger = ledger_methods()

    async def collection_search(accounts, claim_ids=None):
        collection_queries.append({"accounts": accounts, "claim_ids": list(claim_ids)})
        return [claim_b, claim_a_first, claim_a_second], 0, 0, None

    collection_ledger.claim_search = collection_search
    collection_results = await collection_ledger.resolve_collection(
        collection, offset=1, page_size=4,
    )

    purchase_a = FixtureOutput("purchase-a", kind="payment")
    purchase_a.purchase = purchase_data("purchase-a-data", CLAIM_A)
    purchase_missing = FixtureOutput("purchase-missing", kind="payment")
    purchase_missing.purchase = purchase_data("purchase-missing-data", CLAIM_C)
    purchase_db_calls = []
    purchase_searches = []
    purchase_ledger = ledger_methods()

    class PurchaseDB:
        @staticmethod
        async def get_purchases(**constraints):
            purchase_db_calls.append(constraints)
            return [purchase_a, purchase_missing]

    async def purchase_search(accounts, claim_ids=None):
        purchase_searches.append({"accounts": accounts, "claim_ids": list(claim_ids)})
        return [claim_a_first, claim_a_second], 0, 0, None

    purchase_ledger.db = PurchaseDB()
    purchase_ledger.claim_search = purchase_search
    resolved_purchases = await purchase_ledger.get_purchases(
        resolve=True, accounts=["fixture-account"], order_by="fixture-order",
    )

    priced_a = claim_output(Claim, "priced-a", CLAIM_A)
    priced_a.has_price = True
    priced_b = claim_output(Claim, "priced-b", CLAIM_B)
    priced_b.has_price = True
    receipt_a_first = FixtureOutput("receipt-a-first", kind="payment")
    receipt_a_first.purchase = purchase_data("receipt-a-first-data", CLAIM_A)
    receipt_b = FixtureOutput("receipt-b", kind="payment")
    receipt_b.purchase = purchase_data("receipt-b-data", CLAIM_B)
    receipt_a_last = FixtureOutput("receipt-a-last", kind="payment")
    receipt_a_last.purchase = purchase_data("receipt-a-last-data", CLAIM_A)
    receipt_db_calls = []
    receipt_ledger = ledger_methods()

    class ReceiptDB:
        @staticmethod
        async def get_purchases(**constraints):
            receipt_db_calls.append(constraints)
            return [receipt_a_first, receipt_b, receipt_a_last]

    async def empty_query():
        return "offline-fixture"

    receipt_ledger.db = ReceiptDB()
    FixtureInflatedOutputs.current = FixtureInflatedOutputs(
        [priced_a, priced_b], offset=7, total=19,
    )
    inflated, blocked, offset, total = await receipt_ledger._inflate_outputs(
        empty_query(), ["fixture-account"], include_purchase_receipt=True,
    )

    annotation_source = claim_output(Claim, "annotation-source", CLAIM_A)
    annotation_source.is_spent = True
    annotation_source.is_my_output = False
    annotation_source.is_my_input = True
    annotation_source.is_internal_transfer = True
    annotation_source.sent_supports = 901
    annotation_source.sent_tips = 902
    annotation_source.received_tips = 903
    annotation_source.private_key = "cached-private-key"
    annotation_source.purchase_receipt = receipt_a_first
    annotation_channel = claim_output(Claim, "annotation-channel", CHANNEL_A, kind="channel")
    annotation_source.channel = annotation_channel
    annotation_calls = []

    class AnnotationDB:
        @staticmethod
        async def get_txo_count(**constraints):
            annotation_calls.append({"method": "get_txo_count", "constraints": constraints})
            return 1

        @staticmethod
        async def get_txo_sum(**constraints):
            annotation_calls.append({"method": "get_txo_sum", "constraints": constraints})
            if constraints["is_my_input"] and constraints["is_my_output"]:
                return 11
            if constraints["is_my_input"] and not constraints["is_my_output"]:
                return 22
            if not constraints["is_my_input"] and constraints["is_my_output"]:
                return 33
            raise AssertionError(f"unexpected annotation constraints: {constraints}")

    annotation_ledger = ledger_methods()
    annotation_ledger.db = AnnotationDB()
    FixtureInflatedOutputs.current = FixtureInflatedOutputs([annotation_source])
    annotated, _, _, _ = await annotation_ledger._inflate_outputs(
        empty_query(), ["fixture-account"],
        include_is_my_output=True,
        include_sent_supports=True,
        include_sent_tips=True,
        include_received_tips=True,
    )
    annotation_result = annotated[0]

    received_only_source = claim_output(Claim, "received-only", CLAIM_B)
    received_only_source.received_tips = 904
    received_only_calls = []

    class ReceivedOnlyDB:
        @staticmethod
        async def get_txo_sum(**constraints):
            received_only_calls.append(constraints)
            return 44

    received_only_ledger = ledger_methods()
    received_only_ledger.db = ReceivedOnlyDB()
    FixtureInflatedOutputs.current = FixtureInflatedOutputs([received_only_source])
    received_only, _, _, _ = await received_only_ledger._inflate_outputs(
        empty_query(), ["fixture-account"], include_received_tips=True,
    )

    return {
        "collection": {
            "all_reference_ids": references,
            "requested_claim_ids": collection_queries[0]["claim_ids"],
            "result_labels": [result.label if result is not None else None for result in collection_results],
            "offset": 1,
            "page_size": 4,
            "selection": "first matching resolve result is reused for repeated ids",
        },
        "purchased_claim": {
            "requested_claim_ids": purchase_searches[0]["claim_ids"],
            "result_labels": [
                purchase.purchased_claim.label if purchase.purchased_claim is not None else None
                for purchase in resolved_purchases
            ],
            "db_constraints": purchase_db_calls[0],
            "selection": "claim-id lookup dict is last-result-wins",
        },
        "purchase_receipt": {
            "requested_claim_ids": receipt_db_calls[0]["purchased_claim_id__in"],
            "result_labels": [
                output.purchase_receipt.label if output.purchase_receipt is not None else None
                for output in inflated
            ],
            "offset": offset,
            "total": total,
            "blocked": blocked,
            "selection": "receipt lookup dict is last-result-wins",
        },
        "annotations": {
            "copy_created": annotation_result is not annotation_source,
            "channel_preserved": annotation_result.channel is annotation_channel,
            "result": {
                "is_spent": annotation_result.is_spent,
                "is_my_output": annotation_result.is_my_output,
                "is_my_input": annotation_result.is_my_input,
                "is_internal_transfer": annotation_result.is_internal_transfer,
                "sent_supports": annotation_result.sent_supports,
                "sent_tips": annotation_result.sent_tips,
                "received_tips": annotation_result.received_tips,
                "private_key": annotation_result.private_key,
                "purchase_receipt": annotation_result.purchase_receipt,
            },
            "source_after": {
                "sent_supports": annotation_source.sent_supports,
                "sent_tips": annotation_source.sent_tips,
                "received_tips": annotation_source.received_tips,
                "private_key": annotation_source.private_key,
                "purchase_receipt": annotation_source.purchase_receipt.label,
            },
            "calls": annotation_calls,
            "received_only": {
                "received_tips": received_only[0].received_tips,
                "source_received_tips": received_only_source.received_tips,
                "call_count": len(received_only_calls),
            },
        },
    }


def run(sdk_root):
    verify_source(sdk_root)
    method_hashes = verify_method_hashes(sdk_root)
    os.environ.setdefault("PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION", "python")
    sys.path.insert(0, str(sdk_root))
    install_optional_dependency_stubs()

    from google.protobuf.message import DecodeError
    from lbry import __version__
    from lbry.schema.claim import Claim

    if __version__ != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {__version__}, expected {PINNED_VERSION}")
    namespace = extract_encoder(sdk_root, Claim, DecodeError)
    namespace["Output"] = FixtureOutput
    encoder_class = namespace["JSONResponseEncoder"]
    ledger_methods = extract_ledger_relationship_methods(sdk_root)
    relationship_probes = asyncio.run(execute_ledger_relationship_probes(Claim, ledger_methods))
    cases = [
        execute_case(fixture, encoder_class)
        for fixture in fixture_cases(Claim, DecodeError)
    ]
    annotation_probe = execute_annotation_probe(Claim, encoder_class)
    return {
        "reference": {
            "commit": PINNED_COMMIT,
            "version": PINNED_VERSION,
            "source_sha256": PINNED_SOURCE_HASHES,
            "method_sha256": method_hashes,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "protobuf_version": __import__("google.protobuf").protobuf.__version__,
            "real_v2_claim_objects_executed": True,
            "extracted_encoder_methods_executed": True,
            "extracted_ledger_relationship_methods_executed": True,
            "external_network_used": False,
            "case_count": len(cases),
            "success_case_count": sum(case["error"] is None for case in cases),
            "error_case_count": sum(case["error"] is not None for case in cases),
            "transaction_show_contract": {
                "remote_raw": "WalletManager.get_transaction builds a raw Transaction; no resolved relationship links are populated.",
                "local_raw": "Database.get_transactions links only output[0].purchase to decodable output[1] purchase data.",
                "repost": "Outputs.message_to_txo populates reposted_claim only for hub-resolved results.",
                "collection": "Ledger.resolve_collection preserves reference order and inserts None for missing claims.",
                "purchased_claim": "Ledger.get_purchases(resolve=True) populates purchased_claim; transaction_show does not request it.",
                "purchase_receipt": "Ledger._inflate_outputs populates receipts only when requested with accounts and only for priced claims.",
                "recursion": "Every relationship calls encode_output without forwarding the caller's check_signature=False.",
                "protobuf": "include_protobuf applies recursively to decoded claim outputs, not purchase payment envelopes.",
            },
            "relationship_probes": relationship_probes,
        },
        "annotation_probe": annotation_probe,
        "cases": cases,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    arguments = parser.parse_args()
    print(json.dumps(run(arguments.sdk_root.resolve()), sort_keys=True))


if __name__ == "__main__":
    main()

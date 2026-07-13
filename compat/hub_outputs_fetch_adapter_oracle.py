#!/usr/bin/env python3
"""Offline oracle for Ledger._inflate_outputs' cached transaction boundary.

The probe AST-executes the pinned SDK methods against deterministic fixtures.
It performs no imports from the SDK and no network or filesystem I/O after
source verification.
"""

import argparse
import ast
import asyncio
import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace
from typing import List, Optional, Tuple


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py":
        "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/schema/result.py":
        "b5a506fedc9f40c5e9ea1b0691e1e36f9559acaabafe9e3599ed7db52031a4cf",
    "lbry/wallet/ledger.py":
        "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
    "lbry/wallet/transaction.py":
        "e73491aeb915fbce931acbb4d9631f3e05440a7d26c598db85e66e524a798d15",
}
PINNED_METHOD_HASHES = {
    "Ledger._inflate_outputs":
        "2eb53ed61cabd4456010c5c3c23ec848c5888ca749acb68ec864fc1e92be5cfe",
    "Ledger.request_transactions":
        "229439ed82b706d22676f59261b4b162450ac68e72ca34aee50fc119f990001b",
    "Output.update_annotations":
        "93c3f5bdac129fa70c6e887c3648030396fdd638c06defead49de63599816eb6",
    "Outputs.inflate":
        "61bfff753fc883560eb1982a08316fc2b0bd8e5aa7fe2ca143dd8a50b71d5870",
    "Outputs.inflate_blocked":
        "e66de7986c315fd7982f58ba68ca5c110282a279ee18e75a6246d25ee8734343",
    "Outputs.message_to_txo":
        "4369696def2c977a904df2db3d397219bf2b2e1a6e0c3550f3ad184b286d1ce5",
}


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


def selected_method(path, class_name, method_name):
    source = path.read_text(encoding="utf-8")
    source_class = next(
        node for node in ast.parse(source, filename=str(path)).body
        if isinstance(node, ast.ClassDef) and node.name == class_name
    )
    method = next(
        node for node in source_class.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name == method_name
    )
    digest = hashlib.sha256(ast.get_source_segment(source, method).encode()).hexdigest()
    return copy.deepcopy(method), digest


class TransactionCacheItem:

    def __init__(self, tx=None):
        self.tx = tx


class FixtureTransaction:

    hash_reads = []

    def __init__(self, label, tx_hash, outputs=None, is_verified=True):
        self.label = label
        self._hash = tx_hash
        self.outputs = outputs if outputs is not None else []
        self.is_verified = is_verified

    @property
    def hash(self):
        self.hash_reads.append(self.label)
        return self._hash


class FixtureClaimMessage:

    def __init__(self, short_url, channel=None, repost=None, claims_in_channel=0):
        self.short_url = short_url
        self.canonical_url = ""
        self.reposted = 0
        self.is_controlling = True
        self.take_over_height = 2
        self.creation_height = 3
        self.activation_height = 4
        self.expiration_height = 5
        self.effective_amount = 6
        self.support_amount = 7
        self.claims_in_channel = claims_in_channel
        self.channel = channel
        self.repost = repost

    def HasField(self, name):
        return getattr(self, name) is not None


class FixtureCode:

    @staticmethod
    def Name(value):
        return {1: "INVALID", 2: "NOT_FOUND", 3: "BLOCKED"}[value]


class FixtureError:

    Code = FixtureCode

    def __init__(self, code, text, channel=None):
        self.code = code
        self.text = text
        self.blocked = SimpleNamespace(channel=channel)


class FixtureMessage:

    def __init__(self, tx_hash=b"", nout=0, claim=None, error=None, label=""):
        self.tx_hash = tx_hash
        self.nout = nout
        self.claim = claim
        self.error = error
        self.label = label

    def WhichOneof(self, _name):
        if self.error is not None:
            return "error"
        if self.claim is not None:
            return "claim"
        return None


def extract_classes(sdk_root):
    specs = {
        "Ledger._inflate_outputs":
            (sdk_root / "lbry/wallet/ledger.py", "Ledger", "_inflate_outputs"),
        "Ledger.request_transactions":
            (sdk_root / "lbry/wallet/ledger.py", "Ledger", "request_transactions"),
        "Output.update_annotations":
            (sdk_root / "lbry/wallet/transaction.py", "Output", "update_annotations"),
        "Outputs.inflate":
            (sdk_root / "lbry/schema/result.py", "Outputs", "inflate"),
        "Outputs.inflate_blocked":
            (sdk_root / "lbry/schema/result.py", "Outputs", "inflate_blocked"),
        "Outputs.message_to_txo":
            (sdk_root / "lbry/schema/result.py", "Outputs", "message_to_txo"),
    }
    selected = {}
    hashes = {}
    for name, (path, class_name, method_name) in specs.items():
        selected[name], hashes[name] = selected_method(path, class_name, method_name)
    if hashes != PINNED_METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {PINNED_METHOD_HASHES}")

    output_class = ast.ClassDef(
        "PinnedOutput", [], [], [selected["Output.update_annotations"]], [],
    )
    outputs_class = ast.ClassDef(
        "PinnedOutputs", [], [], [
            selected["Outputs.inflate"],
            selected["Outputs.inflate_blocked"],
            selected["Outputs.message_to_txo"],
        ], [],
    )
    ledger_class = ast.ClassDef(
        "PinnedLedger", [], [], [
            selected["Ledger.request_transactions"],
            selected["Ledger._inflate_outputs"],
        ], [],
    )
    module = ast.fix_missing_locations(ast.Module(
        body=[output_class, outputs_class, ledger_class], type_ignores=[],
    ))
    namespace = {
        "BLOCKED": "BLOCKED",
        "List": List,
        "Optional": Optional,
        "Output": None,
        "Outputs": None,
        "Transaction": FixtureTransaction,
        "TransactionCacheItem": TransactionCacheItem,
        "Tuple": Tuple,
        "TXO_TYPES": {"support": 3},
        "copy": copy,
    }
    exec(compile(module, "<pinned hub outputs fetch adapter>", "exec"), namespace)
    output_type = namespace["PinnedOutput"]
    outputs_type = namespace["PinnedOutputs"]
    ledger_type = namespace["PinnedLedger"]
    # The extracted functions resolve globals in namespace at call time.
    namespace["Output"] = output_type
    namespace["Outputs"] = outputs_type

    @classmethod
    def from_base64(cls, encoded):
        cls.encoded_inputs.append(encoded)
        return cls.current

    outputs_type.from_base64 = from_base64
    outputs_type.current = None
    outputs_type.encoded_inputs = []
    return output_type, outputs_type, ledger_type, hashes


def fixture_output(output_type, label, is_channel=False):
    output = output_type()
    output.label = label
    output.claim = SimpleNamespace(is_channel=is_channel)
    output.meta = {"cached": label}
    output.channel = None
    output.private_key = "private-" + label
    output.purchase_receipt = "receipt-" + label
    output.is_internal_transfer = True
    output.is_spent = True
    output.is_my_output = True
    output.is_my_input = True
    output.sent_supports = 101
    output.sent_tips = 102
    output.received_tips = 103
    return output


def fixture_outputs(outputs_type, txos, extra_txos, txs, offset=0, total=0,
                    blocked=None, blocked_total=0):
    outputs = outputs_type()
    outputs.txos = txos
    outputs.extra_txos = extra_txos
    outputs.txs = set(txs)
    outputs.offset = offset
    outputs.total = total
    outputs.blocked = blocked or []
    outputs.blocked_total = blocked_total
    return outputs


def new_ledger(ledger_type):
    ledger = ledger_type()
    ledger._tx_cache = {}
    ledger.accounts = []
    ledger.db = SimpleNamespace()
    return ledger


async def cache_batch_case(ledger_type):
    ledger = new_ledger(ledger_type)
    requests = [(f"tx{index:03d}", index) for index in reversed(range(105))]
    verified = FixtureTransaction("verified-010", b"verified-010", is_verified=True)
    stale = FixtureTransaction("stale-102", b"stale-102", is_verified=False)
    ledger._tx_cache["tx010"] = TransactionCacheItem(verified)
    ledger._tx_cache["tx102"] = TransactionCacheItem(stale)
    ledger._tx_cache["tx050"] = TransactionCacheItem()
    calls = []

    async def single_batch(batch, remote_heights):
        calls.append({
            "ids": list(batch),
            "remote_heights": dict(remote_heights),
        })
        return {
            txid: FixtureTransaction("fresh-" + txid, txid.encode())
            for txid in batch
        }

    ledger._single_batch = single_batch
    yielded = []
    async for transactions in ledger.request_transactions(tuple(requests), cached=True):
        yielded.append({
            "keys": sorted(transactions),
            "value_labels": sorted(tx.label for tx in transactions.values()),
        })

    return {
        "name": "verified hits precede height-sorted miss batches",
        "request_count": len(requests),
        "yield_count": len(yielded),
        "yield_sizes": [len(item["keys"]) for item in yielded],
        "cache_hit_keys": yielded[0]["keys"],
        "batch_sizes": [len(call["ids"]) for call in calls],
        "first_batch_first_ids": calls[0]["ids"][:4],
        "first_batch_last_ids": calls[0]["ids"][-4:],
        "second_batch_ids": calls[1]["ids"],
        "remote_height_map_is_global": [
            len(call["remote_heights"]) for call in calls
        ],
        "verified_identity_preserved": ledger._tx_cache["tx010"].tx is verified,
        "unverified_replaced": ledger._tx_cache["tx102"].tx is not stale,
        "placeholder_filled": ledger._tx_cache["tx050"].tx is not None,
        "absent_inserted": ledger._tx_cache["tx000"].tx is not None,
    }


async def duplicate_hash_case(output_type, outputs_type, ledger_type):
    selected_hash = b"shared-hash"
    cached_output = fixture_output(output_type, "cached-choice")
    fetched_output = fixture_output(output_type, "fetched-choice")
    cached = FixtureTransaction("cached-transaction", selected_hash, [cached_output], True)
    fetched = FixtureTransaction("fetched-transaction", selected_hash, [fetched_output], True)
    ledger = new_ledger(ledger_type)
    ledger._tx_cache["cache-id"] = TransactionCacheItem(cached)
    calls = []

    async def single_batch(batch, remote_heights):
        calls.append({"ids": list(batch), "remote_heights": dict(remote_heights)})
        return {"fetch-id": fetched}

    ledger._single_batch = single_batch
    outputs_type.current = fixture_outputs(
        outputs_type,
        [FixtureMessage(selected_hash, label="primary")],
        [],
        [("cache-id", 999), ("fetch-id", 1)],
        offset=8,
        total=9,
    )
    FixtureTransaction.hash_reads = []

    async def query():
        return "duplicate-hash-token"

    txos, blocked, offset, total = await ledger._inflate_outputs(query(), [])
    result = txos[0]
    return {
        "name": "later fetched transaction wins duplicate hash map",
        "batch_calls": calls,
        "supplied_transaction_order": list(FixtureTransaction.hash_reads),
        "selected_output": result.label,
        "selected_is_throwaway_copy": result is not fetched_output,
        "cached_candidate_unchanged": cached_output.purchase_receipt == "receipt-cached-choice",
        "fetched_source_unchanged": fetched_output.purchase_receipt == "receipt-fetched-choice",
        "offset": offset,
        "total": total,
        "blocked": blocked,
    }


async def extras_copy_case(output_type, outputs_type, ledger_type):
    channel_hash = b"channel-hash"
    claim_hash = b"claim-hash"
    channel_source = fixture_output(output_type, "channel-source", is_channel=True)
    claim_source = fixture_output(output_type, "claim-source")
    channel_tx = FixtureTransaction("channel-tx", channel_hash, [channel_source], True)
    claim_tx = FixtureTransaction("claim-tx", claim_hash, [claim_source], True)
    channel_reference = FixtureMessage(channel_hash, 0, label="channel-reference")
    extra = FixtureMessage(
        channel_hash, 0,
        FixtureClaimMessage("@channel", claims_in_channel=77),
        label="extra-channel",
    )
    primary = FixtureMessage(
        claim_hash, 0,
        FixtureClaimMessage("stream", channel=channel_reference),
        label="primary-claim",
    )
    blocked_channel = FixtureMessage(channel_hash, 0, label="blocked-channel")
    blocked = [SimpleNamespace(channel=blocked_channel, count=4)]
    outputs_type.current = fixture_outputs(
        outputs_type, [primary], [extra],
        [("channel-id", 20), ("claim-id", 10)],
        offset=12, total=34, blocked=blocked, blocked_total=5,
    )
    ledger = new_ledger(ledger_type)

    async def single_batch(batch, _remote_heights):
        # Dict insertion order deliberately differs from request height order.
        return {"channel-id": channel_tx, "claim-id": claim_tx}

    ledger._single_batch = single_batch
    FixtureTransaction.hash_reads = []

    async def query():
        return "extras-copy-token"

    txos, inflated_blocked, offset, total = await ledger._inflate_outputs(query(), [])
    result = txos[0]
    annotations = {
        name: getattr(result, name) for name in (
            "is_internal_transfer", "is_spent", "is_my_output", "is_my_input",
            "sent_supports", "sent_tips", "received_tips", "private_key",
            "purchase_receipt",
        )
    }
    return {
        "name": "extras inflate before copied primary and page metadata survives",
        "supplied_transaction_order": list(FixtureTransaction.hash_reads),
        "result_label": result.label,
        "throwaway_copy_created": result is not claim_source,
        "meta_is_shallow_shared": result.meta is claim_source.meta,
        "channel_identity_preserved": result.channel is channel_source,
        "channel_extra_meta_visible": dict(result.channel.meta),
        "result_annotations": annotations,
        "source_annotations_unchanged": {
            "is_spent": claim_source.is_spent,
            "sent_tips": claim_source.sent_tips,
            "private_key": claim_source.private_key,
            "purchase_receipt": claim_source.purchase_receipt,
        },
        "offset": offset,
        "total": total,
        "blocked": {
            "total": inflated_blocked["total"],
            "channels": [{
                "label": item["channel"].label,
                "same_channel_identity": item["channel"] is channel_source,
                "blocked": item["blocked"],
            } for item in inflated_blocked["channels"]],
        },
    }


async def empty_case(outputs_type, ledger_type):
    ledger = new_ledger(ledger_type)
    calls = []

    async def single_batch(batch, remote_heights):
        calls.append([batch, remote_heights])
        return {}

    ledger._single_batch = single_batch
    outputs_type.current = fixture_outputs(
        outputs_type, [], [], [], offset=0, total=0,
    )

    async def query():
        return None

    txos, blocked, offset, total = await ledger._inflate_outputs(query(), [])
    return {
        "name": "empty page skips transaction generator",
        "single_batch_calls": len(calls),
        "encoded_input": outputs_type.encoded_inputs[-1],
        "txos": txos,
        "blocked": blocked,
        "offset": offset,
        "total": total,
    }


async def execute_cases(output_type, outputs_type, ledger_type):
    return [
        await cache_batch_case(ledger_type),
        await duplicate_hash_case(output_type, outputs_type, ledger_type),
        await extras_copy_case(output_type, outputs_type, ledger_type),
        await empty_case(outputs_type, ledger_type),
    ]


def run(sdk_root):
    verify_source(sdk_root)
    output_type, outputs_type, ledger_type, hashes = extract_classes(sdk_root)
    cases = asyncio.run(execute_cases(output_type, outputs_type, ledger_type))
    version_source = (sdk_root / "lbry/__init__.py").read_text(encoding="utf-8")
    version_node = next(
        node for node in ast.parse(version_source).body
        if isinstance(node, ast.Assign)
        and any(isinstance(target, ast.Name) and target.id == "__version__"
                for target in node.targets)
    )
    version = ast.literal_eval(version_node.value)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    return {
        "reference": {
            "commit": PINNED_COMMIT,
            "version": version,
            "source_sha256": PINNED_SOURCE_HASHES,
            "method_sha256": hashes,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "extracted_methods_executed": True,
            "external_network_used": False,
            "case_count": len(cases),
            "proposed_go_contract": {
                "method": "(*Ledger).InflateHubOutputs(context.Context, *HubOutputs) (HubOutputsPage, error)",
                "result": "HubOutputsPage{Items, Blocked, Offset, Total}",
                "cache": "per-ledger verified-transaction cache keyed by requested txid",
                "note": "Names are advisory; fixture semantics are normative.",
            },
            "contract_notes": {
                "cache_hits": "Only non-nil verified cached transactions skip fetch; hits are yielded before all miss batches.",
                "batches": "Misses are stable-sorted by height and fetched in batches of 100; every batch receives the complete miss remote-height map.",
                "transaction_map": "_inflate_outputs concatenates yielded mapping values; Outputs.inflate maps by raw hash and later supplied duplicate hashes win.",
                "copy": "Primary Output results are shallow-copied, wallet annotations and receipt/key are reset, and the resolved channel identity is restored.",
                "inflation": "extra_txos mutate shared cached outputs before primary txos; blocked, offset, and total are returned unchanged.",
            },
        },
        "cases": cases,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    arguments = parser.parse_args()
    print(json.dumps(run(arguments.sdk_root.resolve()), sort_keys=True))


if __name__ == "__main__":
    main()

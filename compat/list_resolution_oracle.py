#!/usr/bin/env python3
"""Pinned, offline oracle for local list-result resolution."""

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
from typing import List


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/wallet/ledger.py": "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
    "lbry/wallet/transaction.py": "e73491aeb915fbce931acbb4d9631f3e05440a7d26c598db85e66e524a798d15",
}
METHOD_SPECS = {
    "Ledger.get_txos": ("lbry/wallet/ledger.py", "Ledger", "get_txos"),
    "Ledger.get_purchases": ("lbry/wallet/ledger.py", "Ledger", "get_purchases"),
    "Ledger._resolve_for_local_results": (
        "lbry/wallet/ledger.py", "Ledger", "_resolve_for_local_results",
    ),
    "Ledger._resolve_for_local_claim_results": (
        "lbry/wallet/ledger.py", "Ledger", "_resolve_for_local_claim_results",
    ),
    "Ledger._resolve_for_local_support_results": (
        "lbry/wallet/ledger.py", "Ledger", "_resolve_for_local_support_results",
    ),
    "Ledger.resolve_collection": (
        "lbry/wallet/ledger.py", "Ledger", "resolve_collection",
    ),
    "Ledger.get_collections": (
        "lbry/wallet/ledger.py", "Ledger", "get_collections",
    ),
    "Output.update_annotations": (
        "lbry/wallet/transaction.py", "Output", "update_annotations",
    ),
}
METHOD_HASHES = {
    "Ledger.get_txos": "4dcd701a9e0cc5142af7dc96b0fdd45b21410df1c1f9ce7d90240b02888f8c01",
    "Ledger.get_purchases": "a10b63da0d141f7f094eb0d85f8734f4743dcbb76b5fecb5928d692cb6fe2bbb",
    "Ledger._resolve_for_local_results": "ae8da93c15547112bf07a1208ac2c7065af2fc9f35f1afb0e7d39fbdd5a9a111",
    "Ledger._resolve_for_local_claim_results": "2c28cec3e2220cf767bf510ae1ea835eb32c6039c51e4103932ff0c2d08525de",
    "Ledger._resolve_for_local_support_results": "49e3b2a9faf0109aa8b2f1cb016cf2e671d1a6b39f6399f2972264f00f80367b",
    "Ledger.resolve_collection": "9692e89042901f82f8a5f5cb06b300bf7ced49f5aadd4718da7ad2cc3c6c7ef3",
    "Ledger.get_collections": "7c7c3bf096b10bf0f03489ad21cba120276a9cafb9df06b57d82e35c8efe9260",
    "Output.update_annotations": "93c3f5bdac129fa70c6e887c3648030396fdd638c06defead49de63599816eb6",
}


class ProbeFailure(Exception):
    pass


class ProbeLog:
    def __init__(self):
        self.messages = []

    def exception(self, message):
        self.messages.append(message)


LOG = ProbeLog()


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
    segment = ast.get_source_segment(source, method)
    return copy.deepcopy(method), hashlib.sha256(segment.encode()).hexdigest()


def verify_reference(sdk_root):
    commit = subprocess.run(
        ["git", "-C", str(sdk_root), "rev-parse", "HEAD"],
        check=True, capture_output=True, text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    actual_hashes = {
        relative: hashlib.sha256((sdk_root / relative).read_bytes()).hexdigest()
        for relative in SOURCE_HASHES
    }
    if actual_hashes != SOURCE_HASHES:
        raise RuntimeError(f"source hashes are {actual_hashes}, expected {SOURCE_HASHES}")
    init_tree = ast.parse((sdk_root / "lbry/__init__.py").read_text(encoding="utf-8"))
    version = next(
        ast.literal_eval(node.value)
        for node in init_tree.body
        if isinstance(node, ast.Assign)
        and any(isinstance(target, ast.Name) and target.id == "__version__" for target in node.targets)
    )
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")


def extract_types(sdk_root):
    nodes, hashes = {}, {}
    for name, (relative, class_name, method_name) in METHOD_SPECS.items():
        nodes[name], hashes[name] = selected_method(
            sdk_root / relative, class_name, method_name,
        )
    if hashes != METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {METHOD_HASHES}")

    output_class = ast.ClassDef(
        name="ProbeOutput", bases=[], keywords=[],
        body=[nodes["Output.update_annotations"]], decorator_list=[],
    )
    ledger_class = ast.ClassDef(
        name="ProbeLedger", bases=[], keywords=[],
        body=[nodes[name] for name in METHOD_SPECS if name.startswith("Ledger.")],
        decorator_list=[],
    )
    namespace = {"List": List, "Output": object, "log": LOG}
    module = ast.fix_missing_locations(ast.Module(
        body=[output_class, ledger_class], type_ignores=[],
    ))
    exec(compile(module, "<pinned list resolution>", "exec"), namespace)
    output_type = namespace["ProbeOutput"]
    ledger_type = namespace["ProbeLedger"]
    namespace["Output"] = output_type

    def output_init(
        self, label, *, claim_id=None, permanent_url=None, decodes_claim=False,
        support=None, collection_ids=None, purchased_claim_id=None,
    ):
        self.label = label
        self.claim_id = claim_id
        self._permanent_url = permanent_url
        self._decodes_claim = decodes_claim
        self._support = support
        self._claim = None
        if collection_ids is not None:
            self._claim = SimpleNamespace(collection=SimpleNamespace(
                claims=SimpleNamespace(ids=list(collection_ids)),
            ))
        self._purchased_claim_id = purchased_claim_id
        self.is_internal_transfer = None
        self.is_spent = None
        self.is_my_output = None
        self.is_my_input = None
        self.sent_supports = None
        self.sent_tips = None
        self.received_tips = None
        self.channel = None
        self.private_key = None
        self.purchase = None
        self.purchased_claim = None
        self.purchase_receipt = None
        self.reposted_claim = None
        self.claims = None
        self.meta = {}

    def get_can_decode_claim(self):
        return SimpleNamespace(label=self.label) if self._decodes_claim else False

    def get_permanent_url(self):
        if self._permanent_url is None:
            raise ValueError("No claim associated.")
        return self._permanent_url

    def get_can_decode_support(self):
        return self._support or False

    def get_support(self):
        if self._support is None:
            raise ValueError("Only supports with data can be represented as Supports.")
        return self._support

    def get_claim(self):
        if self._claim is None:
            raise ValueError("Only claim name and claim update have the claim payload.")
        return self._claim

    output_type.__init__ = output_init
    output_type.can_decode_claim = property(get_can_decode_claim)
    output_type.permanent_url = property(get_permanent_url)
    output_type.can_decode_support = property(get_can_decode_support)
    output_type.support = property(get_support)
    output_type.claim = property(get_claim)
    output_type.purchased_claim_id = property(lambda self: self._purchased_claim_id)
    return output_type, ledger_type, hashes


class ProbeDB:
    def __init__(self, ledger, **rows):
        self.ledger = ledger
        self.rows = rows

    async def _get(self, method, constraints):
        self.ledger.events.append(f"db:{method}")
        self.ledger.db_calls.append({
            "method": method,
            "constraints": copy.deepcopy(constraints),
        })
        return list(self.rows.get(method, []))

    async def get_txos(self, **constraints):
        return await self._get("get_txos", constraints)

    async def get_purchases(self, **constraints):
        return await self._get("get_purchases", constraints)

    async def get_collections(self, **constraints):
        return await self._get("get_collections", constraints)


def make_ledger(ledger_type, *, resolve_result=None, resolve_error=None,
                claim_search_results=None, **rows):
    ledger = ledger_type()
    ledger.events = []
    ledger.db_calls = []
    ledger.resolve_calls = []
    ledger.claim_search_calls = []
    ledger.resolve_result = resolve_result or {}
    ledger.resolve_error = resolve_error
    ledger.claim_search_results = list(claim_search_results or [])
    ledger.db = ProbeDB(ledger, **rows)

    async def resolve(self, accounts, urls):
        self.events.append("resolve")
        self.resolve_calls.append({
            "accounts": copy.deepcopy(accounts), "urls": list(urls),
        })
        if self.resolve_error is not None:
            raise self.resolve_error
        return self.resolve_result

    async def claim_search(self, accounts, **constraints):
        self.events.append("claim_search")
        self.claim_search_calls.append({
            "accounts": copy.deepcopy(accounts),
            "claim_ids": list(constraints.get("claim_ids", [])),
            "constraint_keys": list(constraints),
        })
        if not self.claim_search_results:
            raise ProbeFailure("unexpected claim_search call")
        result = self.claim_search_results.pop(0)
        if isinstance(result, Exception):
            raise result
        return list(result), len(result), 0, {}

    ledger.resolve = resolve.__get__(ledger, ledger_type)
    ledger.claim_search = claim_search.__get__(ledger, ledger_type)
    return ledger


def output_label(value):
    return value.label if value is not None else None


def output_snapshot(output):
    return {
        "label": output.label,
        "is_internal_transfer": output.is_internal_transfer,
        "is_spent": output.is_spent,
        "is_my_output": output.is_my_output,
        "is_my_input": output.is_my_input,
        "sent_supports": output.sent_supports,
        "sent_tips": output.sent_tips,
        "received_tips": output.received_tips,
        "channel": output_label(output.channel),
        "private_key": output.private_key,
        "purchased_claim": output_label(output.purchased_claim),
        "purchase_receipt": output_label(output.purchase_receipt),
        "reposted_claim": output_label(output.reposted_claim),
        "claims": (
            [output_label(claim) for claim in output.claims]
            if output.claims is not None else None
        ),
        "meta": copy.deepcopy(output.meta),
    }


def call_snapshot(ledger):
    return {
        "events": list(ledger.events),
        "db": copy.deepcopy(ledger.db_calls),
        "resolve": copy.deepcopy(ledger.resolve_calls),
        "claim_search": copy.deepcopy(ledger.claim_search_calls),
    }


async def capture_error(awaitable):
    try:
        await awaitable
    except Exception as error:
        return {"type": type(error).__name__, "message": str(error)}
    return None


def annotate_local(output_type, label, url):
    output = output_type(
        label, claim_id=label + "-id", permanent_url=url, decodes_claim=True,
    )
    output.is_internal_transfer = True
    output.is_spent = False
    output.is_my_output = True
    output.is_my_input = False
    output.sent_supports = 11
    output.sent_tips = 22
    output.received_tips = 33
    output.channel = output_type("local-channel")
    output.private_key = "local-private-key"
    output.purchase_receipt = output_type("local-receipt")
    output.meta = {"local": "preserved"}
    return output


async def probe_claim_results(output_type, ledger_type):
    local = annotate_local(output_type, "local-claim", "lbry://local#aaa")
    local_error = output_type(
        "local-error", claim_id="error-id", permanent_url="lbry://error#bbb",
        decodes_claim=True,
    )
    local_error.meta = {"existing": 1}
    plain = output_type("plain")
    remote = output_type("remote-claim", claim_id="local-claim-id")
    remote.channel = output_type("remote-channel")
    remote.private_key = "remote-private-key"
    remote.purchase_receipt = output_type("remote-receipt")
    remote.purchased_claim = output_type("remote-purchased")
    remote.reposted_claim = output_type("remote-repost")
    remote.claims = [output_type("remote-member")]
    remote.meta = {"remote": "preserved"}
    error_value = {"name": "NOT_FOUND", "text": "missing from hub"}
    ledger = make_ledger(
        ledger_type,
        resolve_result={local.permanent_url: remote, local_error.permanent_url: {"error": error_value}},
        get_txos=[local, local_error, plain],
    )
    result = await ledger.get_txos(
        resolve=True, accounts=["account-a"], marker="kept",
    )

    failed_local = output_type(
        "failed-local", permanent_url="lbry://failed#ccc", decodes_claim=True,
    )
    failed = make_ledger(
        ledger_type, resolve_error=ProbeFailure("resolve exploded"),
        get_txos=[failed_local],
    )
    failure = await capture_error(failed.get_txos(
        resolve=True, accounts=["account-failure"],
    ))
    return {
        "success": {
            "calls": call_snapshot(ledger),
            "result_labels": [output.label for output in result],
            "result_is_remote": result[0] is remote,
            "error_result_is_local": result[1] is local_error,
            "plain_result_is_local": result[2] is plain,
            "result": [output_snapshot(output) for output in result],
            "local_after": output_snapshot(local),
        },
        "failure": {"calls": call_snapshot(failed), "error": failure},
    }


async def probe_support_results(output_type, ledger_type):
    signed_support = SimpleNamespace(signing_channel_id="channel-1")
    first = output_type("support-first", support=signed_support)
    second = output_type("support-second", support=signed_support)
    unsigned = output_type(
        "support-unsigned", support=SimpleNamespace(signing_channel_id=None),
    )
    first.channel = output_type("stale-first")
    second.channel = output_type("stale-second")
    unsigned.channel = output_type("unsigned-existing")
    channel_first = output_type("channel-first", claim_id="channel-1")
    channel_last = output_type("channel-last", claim_id="channel-1")
    ledger = make_ledger(
        ledger_type, claim_search_results=[[channel_first, channel_last]],
        get_txos=[first, unsigned, second],
    )
    result = await ledger.get_txos(resolve=True, accounts=["account-support"])

    failing_support = output_type("failing-support", support=signed_support)
    failed = make_ledger(
        ledger_type, claim_search_results=[ProbeFailure("support lookup exploded")],
        get_txos=[failing_support],
    )
    failure = await capture_error(failed.get_txos(
        resolve=True, accounts=["account-support-failure"],
    ))
    return {
        "success": {
            "calls": call_snapshot(ledger),
            "result_labels": [output.label for output in result],
            "channels": [output_label(output.channel) for output in result],
            "identities_preserved": [
                result[0] is first, result[1] is unsigned, result[2] is second,
            ],
        },
        "failure": {"calls": call_snapshot(failed), "error": failure},
    }


async def probe_purchases(output_type, ledger_type):
    purchases = [
        output_type("purchase-a", purchased_claim_id="claim-a"),
        output_type("purchase-missing", purchased_claim_id="claim-missing"),
        output_type("purchase-a-duplicate", purchased_claim_id="claim-a"),
    ]
    claim_first = output_type("claim-a-first", claim_id="claim-a")
    claim_other = output_type("claim-other", claim_id="claim-other")
    claim_last = output_type("claim-a-last", claim_id="claim-a")
    ledger = make_ledger(
        ledger_type, claim_search_results=[[claim_first, claim_other, claim_last]],
        get_purchases=purchases,
    )
    result = await ledger.get_purchases(
        resolve=True, accounts=["database-account"], marker="purchase",
    )

    failed_purchase = output_type("purchase-failed", purchased_claim_id="claim-a")
    failed_purchase.purchased_claim = output_type("stale-purchased-claim")
    failed = make_ledger(
        ledger_type, claim_search_results=[ProbeFailure("purchase lookup exploded")],
        get_purchases=[failed_purchase],
    )
    log_offset = len(LOG.messages)
    failed_result = await failed.get_purchases(resolve=True, marker="failure")
    return {
        "success": {
            "calls": call_snapshot(ledger),
            "result_labels": [output.label for output in result],
            "purchased_claims": [output_label(output.purchased_claim) for output in result],
        },
        "failure": {
            "calls": call_snapshot(failed),
            "result_labels": [output.label for output in failed_result],
            "purchased_claims": [
                output_label(output.purchased_claim) for output in failed_result
            ],
            "logs": LOG.messages[log_offset:],
        },
    }


async def probe_collections(output_type, ledger_type):
    local_first = annotate_local(
        output_type, "local-collection-first", "lbry://collection-first#111",
    )
    local_second = annotate_local(
        output_type, "local-collection-second", "lbry://collection-second#222",
    )
    remote_first = output_type(
        "remote-collection-first", claim_id="collection-first",
        collection_ids=["claim-a", "missing", "claim-a", "not-requested"],
    )
    remote_second = output_type(
        "remote-collection-second", claim_id="collection-second",
        collection_ids=["claim-b", "claim-a"],
    )
    claim_a_first = output_type("claim-a-first", claim_id="claim-a")
    claim_a_last = output_type("claim-a-last", claim_id="claim-a")
    claim_b = output_type("claim-b", claim_id="claim-b")
    ledger = make_ledger(
        ledger_type,
        resolve_result={
            local_first.permanent_url: remote_first,
            local_second.permanent_url: remote_second,
        },
        claim_search_results=[
            [claim_a_first, claim_a_last, claim_b],
            ProbeFailure("collection lookup exploded"),
        ],
        get_collections=[local_first, local_second],
    )
    log_offset = len(LOG.messages)
    result = await ledger.get_collections(
        resolve_claims=3, resolve=True,
        accounts=["account-collection"], marker="collection",
    )

    sliced = output_type(
        "sliced-collection",
        collection_ids=["skip", "claim-b", "claim-a", "missing", "tail"],
    )
    slice_ledger = make_ledger(
        ledger_type,
        claim_search_results=[[claim_a_first, claim_a_last, claim_b]],
    )
    slice_result = await slice_ledger.resolve_collection(
        sliced, offset=1, page_size=3,
    )
    return {
        "list": {
            "calls": call_snapshot(ledger),
            "result_labels": [output.label for output in result],
            "claims": [
                [output_label(claim) for claim in output.claims] for output in result
            ],
            "result_is_remote": [result[0] is remote_first, result[1] is remote_second],
            "annotations": [output_snapshot(output) for output in result],
            "logs": LOG.messages[log_offset:],
        },
        "slice": {
            "calls": call_snapshot(slice_ledger),
            "claims": [output_label(claim) for claim in slice_result],
        },
    }


async def execute_probes(output_type, ledger_type):
    LOG.messages.clear()
    return {
        "claim_results": await probe_claim_results(output_type, ledger_type),
        "support_results": await probe_support_results(output_type, ledger_type),
        "purchases": await probe_purchases(output_type, ledger_type),
        "collections": await probe_collections(output_type, ledger_type),
    }


def run(sdk_root):
    verify_reference(sdk_root)
    output_type, ledger_type, hashes = extract_types(sdk_root)
    probes = asyncio.run(execute_probes(output_type, ledger_type))
    return {
        "reference": {
            "commit": PINNED_COMMIT,
            "version": PINNED_VERSION,
            "source_sha256": SOURCE_HASHES,
            "method_sha256": hashes,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "extracted_methods_executed": True,
            "external_network_used": False,
            "probe_count": 8,
        },
        **probes,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    arguments = parser.parse_args()
    print(json.dumps(run(arguments.sdk_root.resolve()), sort_keys=True))


if __name__ == "__main__":
    main()

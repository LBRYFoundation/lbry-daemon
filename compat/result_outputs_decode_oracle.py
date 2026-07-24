#!/usr/bin/env python3
"""Pinned offline oracle for result.proto Outputs decoding and inflation.

The probe loads the generated result_pb2 module from SDK 0.113.0 and
AST-extracts the real Outputs decoding/inflation methods from schema/result.py.
All transactions and protobuf payloads are deterministic local fixtures.
"""

import argparse
import ast
import base64
from binascii import hexlify
import copy
import hashlib
import importlib.util
from itertools import chain
import json
import os
from pathlib import Path
import subprocess
import sys
from typing import List


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py":
        "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/schema/result.py":
        "b5a506fedc9f40c5e9ea1b0691e1e36f9559acaabafe9e3599ed7db52031a4cf",
    "lbry/schema/types/v2/result_pb2.py":
        "05e396ccaf0bd2385582292d26a5554619d3d1b4a04a915c513c2e187368d096",
}
PINNED_METHOD_HASHES = {
    "Outputs.__init__":
        "a36513bf64852324e9a3061f002cda3e44403f5cfdc431b0eddc4533df0b55c5",
    "Outputs.inflate":
        "61bfff753fc883560eb1982a08316fc2b0bd8e5aa7fe2ca143dd8a50b71d5870",
    "Outputs.inflate_blocked":
        "e66de7986c315fd7982f58ba68ca5c110282a279ee18e75a6246d25ee8734343",
    "Outputs.message_to_txo":
        "4369696def2c977a904df2db3d397219bf2b2e1a6e0c3550f3ad184b286d1ce5",
    "Outputs.from_base64":
        "ba9757279c4464d989292e744f4c35c421d8b09c4d188b6e6bc9affcc44b901d",
    "Outputs.from_bytes":
        "c0419687f777e73f8017df2d8c8b4048d5beb64f2db5edfb5e5ece39b2b353a6",
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
    result_path = sdk_root / "lbry/schema/result.py"
    hashes = {
        name: method_hash(result_path, "Outputs", name.split(".", 1)[1])
        for name in PINNED_METHOD_HASHES
    }
    if hashes != PINNED_METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {PINNED_METHOD_HASHES}")
    return hashes


def load_result_pb2(sdk_root):
    path = sdk_root / "lbry/schema/types/v2/result_pb2.py"
    spec = importlib.util.spec_from_file_location("pinned_result_pb2", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def extract_outputs(sdk_root, outputs_message, blocked_name):
    path = sdk_root / "lbry/schema/result.py"
    source = path.read_text()
    source_class = next(
        node for node in ast.parse(source).body
        if isinstance(node, ast.ClassDef) and node.name == "Outputs"
    )
    selected = {
        "__init__", "inflate", "inflate_blocked", "message_to_txo",
        "from_base64", "from_bytes",
    }
    methods = [
        copy.deepcopy(node) for node in source_class.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name in selected
    ]
    extracted = ast.ClassDef(
        "PinnedOutputs", [], [], methods or [ast.Pass()], [],
    )
    module = ast.fix_missing_locations(ast.Module(body=[extracted], type_ignores=[]))
    namespace = {
        "BLOCKED": blocked_name,
        "List": List,
        "OutputsMessage": outputs_message,
        "base64": base64,
        "chain": chain,
        "hexlify": hexlify,
    }
    exec(compile(module, str(path), "exec"), namespace)
    return namespace["PinnedOutputs"]


class FixtureClaim:

    def __init__(self, is_channel=False):
        self.is_channel = is_channel


class FixtureOutput:

    def __init__(self, label, is_channel=False):
        self.label = label
        self.claim = FixtureClaim(is_channel)
        self.meta = None
        self.channel = None
        self.reposted_claim = None


class FixtureTransaction:

    def __init__(self, label, tx_hash, output_specs):
        self.label = label
        self.hash = tx_hash
        self.outputs = [
            FixtureOutput(output_label, is_channel)
            for output_label, is_channel in output_specs
        ]


TX_MAIN = bytes(range(0, 32))
TX_CHANNEL = bytes(range(32, 64))
TX_REPOST = bytes(range(64, 96))
TX_BARE = bytes(range(96, 128))
TX_MISSING = bytes(range(128, 160))


def make_graph_transactions():
    return [
        FixtureTransaction("main-tx", TX_MAIN, [
            ("main-0", False), ("root-repost", False),
        ]),
        FixtureTransaction("channel-tx", TX_CHANNEL, [
            ("signing-channel", True),
        ]),
        FixtureTransaction("repost-tx", TX_REPOST, [
            ("repost-0", False), ("repost-1", False), ("reposted-stream", False),
        ]),
        FixtureTransaction("bare-tx", TX_BARE, [("bare-output", False)]),
    ]


def make_graph_message(pb):
    page = pb.Outputs()
    page.offset = 7
    page.total = 13
    page.blocked_total = 4

    channel = page.extra_txos.add()
    channel.tx_hash = TX_CHANNEL
    channel.nout = 0
    channel.height = 88
    channel.claim.short_url = "@channel#c"
    channel.claim.claims_in_channel = 42
    channel.claim.creation_height = 8

    reposted = page.extra_txos.add()
    reposted.tx_hash = TX_REPOST
    reposted.nout = 2
    reposted.height = 89
    reposted.claim.short_url = "original#o"
    reposted.claim.canonical_url = "@channel#c/original#o"
    reposted.claim.effective_amount = 5

    root = page.txos.add()
    root.tx_hash = TX_MAIN
    root.nout = 1
    root.height = 90
    root.claim.channel.tx_hash = TX_CHANNEL
    root.claim.channel.nout = 0
    root.claim.channel.height = 88
    root.claim.repost.tx_hash = TX_REPOST
    root.claim.repost.nout = 2
    root.claim.repost.height = 89
    root.claim.short_url = "repost#r"
    root.claim.canonical_url = "@channel#c/repost#r"
    root.claim.is_controlling = True
    root.claim.take_over_height = 70
    root.claim.creation_height = 71
    root.claim.activation_height = 72
    root.claim.expiration_height = 73
    root.claim.claims_in_channel = 99
    root.claim.reposted = 9
    root.claim.effective_amount = 123456789
    root.claim.support_amount = 17

    bare = page.txos.add()
    bare.tx_hash = TX_BARE
    bare.nout = 0
    bare.height = 91

    absent = page.txos.add()
    absent.tx_hash = TX_MISSING
    absent.nout = 0
    absent.height = 92
    absent.claim.short_url = "not-downloaded#n"

    not_found = page.txos.add()
    not_found.error.code = pb.Error.NOT_FOUND
    not_found.error.text = "claim not found"

    invalid = page.txos.add()
    invalid.error.code = pb.Error.INVALID
    invalid.error.text = "invalid uri"

    censored = page.txos.add()
    censored.error.code = pb.Error.BLOCKED
    censored.error.text = "blocked claim"
    censored.error.blocked.count = 2
    censored.error.blocked.channel.tx_hash = TX_CHANNEL
    censored.error.blocked.channel.nout = 0
    censored.error.blocked.channel.height = 88

    blocked = page.blocked.add()
    blocked.count = 3
    blocked.channel.tx_hash = TX_CHANNEL
    blocked.channel.nout = 0
    blocked.channel.height = 88
    return page


def varint(value):
    encoded = bytearray()
    while value >= 0x80:
        encoded.append((value & 0x7f) | 0x80)
        value >>= 7
    encoded.append(value)
    return bytes(encoded)


def field_varint(number, value):
    return varint(number << 3) + varint(value)


def field_bytes(number, value):
    return varint((number << 3) | 2) + varint(len(value)) + value


def reference_wire(tx_hash, nout=0, height=0):
    return b"".join((
        field_bytes(1, tx_hash), field_varint(2, nout), field_varint(3, height),
    ))


def claim_wire(short_url=None, canonical_url=None, effective_amount=None, channel=None):
    fields = []
    if channel is not None:
        fields.append(field_bytes(1, channel))
    if short_url is not None:
        fields.append(field_bytes(3, short_url.encode()))
    if canonical_url is not None:
        fields.append(field_bytes(4, canonical_url.encode()))
    if effective_amount is not None:
        fields.append(field_varint(20, effective_amount))
    return b"".join(fields)


def output_wire(tx_hash=None, nout=None, height=None, claim=None, error=None):
    fields = []
    if tx_hash is not None:
        fields.append(field_bytes(1, tx_hash))
    if nout is not None:
        fields.append(field_varint(2, nout))
    if height is not None:
        fields.append(field_varint(3, height))
    if claim is not None:
        fields.append(field_bytes(7, claim))
    if error is not None:
        fields.append(field_bytes(15, error))
    return b"".join(fields)


def error_wire(code, text=None):
    fields = [field_varint(1, code)]
    if text is not None:
        fields.append(field_bytes(2, text.encode()))
    return b"".join(fields)


def page_wire(*outputs, total=None, offset=None):
    fields = [field_bytes(1, output) for output in outputs]
    if total is not None:
        fields.append(field_varint(3, total))
    if offset is not None:
        fields.append(field_varint(4, offset))
    return b"".join(fields)


def error_dict(error, stage):
    return {
        "stage": stage,
        "type": type(error).__name__,
        "module": type(error).__module__,
        "message": str(error),
    }


def message_summary(message, pb):
    meta = message.WhichOneof("meta")
    summary = {
        "tx_hash": message.tx_hash.hex(),
        "nout": message.nout,
        "height": message.height,
        "meta": meta,
    }
    if meta == "claim":
        claim = message.claim
        summary["claim"] = {
            "short_url": claim.short_url,
            "canonical_url": claim.canonical_url,
            "is_controlling": claim.is_controlling,
            "take_over_height": claim.take_over_height,
            "creation_height": claim.creation_height,
            "activation_height": claim.activation_height,
            "expiration_height": claim.expiration_height,
            "claims_in_channel": claim.claims_in_channel,
            "reposted": claim.reposted,
            "effective_amount": claim.effective_amount,
            "support_amount": claim.support_amount,
            "has_channel": claim.HasField("channel"),
            "has_repost": claim.HasField("repost"),
        }
    elif meta == "error":
        try:
            name = message.error.Code.Name(message.error.code)
        except ValueError:
            name = None
        summary["error"] = {
            "code": message.error.code,
            "name": name,
            "text": message.error.text,
            "has_blocked": message.error.HasField("blocked"),
        }
    return summary


def decoded_summary(decoded, pb):
    return {
        "offset": decoded.offset,
        "total": decoded.total,
        "blocked_total": decoded.blocked_total,
        "txs": [
            {"txid": txid, "height": height}
            for txid, height in sorted(decoded.txs)
        ],
        "txos": [message_summary(message, pb) for message in decoded.txos],
        "extra_txos": [message_summary(message, pb) for message in decoded.extra_txos],
        "blocked": [{
            "count": blocked.count,
            "channel": message_summary(blocked.channel, pb),
        } for blocked in decoded.blocked],
        "txo_reserialized_base64": [
            base64.b64encode(message.SerializeToString()).decode()
            for message in decoded.txos
        ],
    }


def normalize(value):
    if isinstance(value, FixtureOutput):
        return {
            "label": value.label,
            "meta": value.meta,
            "channel": value.channel.label if value.channel is not None else None,
            "reposted_claim": (
                value.reposted_claim.label if value.reposted_claim is not None else None
            ),
        }
    if isinstance(value, dict):
        return {key: normalize(item) for key, item in value.items()}
    if isinstance(value, (list, tuple)):
        return [normalize(item) for item in value]
    return value


def probe(outputs_class, pb, name, encoded, transactions=None):
    transactions = transactions or []
    case = {"name": name, "input_base64": encoded}
    try:
        decoded = outputs_class.from_base64(encoded)
    except Exception as error:
        case["decode_error"] = error_dict(error, "Outputs.from_base64")
        return case

    case["decoded"] = decoded_summary(decoded, pb)
    raw = base64.b64decode(encoded)
    wire_message = pb.Outputs()
    wire_message.ParseFromString(raw)
    reserialized = wire_message.SerializeToString()
    case["message_reserialized_base64"] = base64.b64encode(reserialized).decode()
    case["wire_round_trip_identical"] = reserialized == raw

    try:
        txos, blocked = decoded.inflate(transactions)
        case["inflated"] = {
            "txos": normalize(txos),
            "blocked": normalize(blocked),
            "transaction_outputs": {
                transaction.label: normalize(transaction.outputs)
                for transaction in transactions
            },
        }
    except Exception as error:
        case["inflate_error"] = error_dict(error, "Outputs.inflate")
    return case


def build_cases(outputs_class, pb):
    cases = []

    graph = make_graph_message(pb).SerializeToString()
    cases.append(probe(
        outputs_class, pb, "canonical_relationship_graph",
        base64.b64encode(graph).decode(), make_graph_transactions(),
    ))

    duplicate_scalars = page_wire(output_wire(
        claim=claim_wire(short_url="last-wins#1"),
    ) + b"".join((
        field_bytes(1, TX_MAIN), field_bytes(1, TX_BARE),
        field_varint(2, 0), field_varint(2, 1),
        field_varint(3, 5), field_varint(3, 6),
    )))
    duplicate_transactions = [
        FixtureTransaction("bare-two", TX_BARE, [
            ("bare-two-0", False), ("bare-two-1", False),
        ]),
    ]
    cases.append(probe(
        outputs_class, pb, "duplicate_scalar_fields_last_value_wins",
        base64.b64encode(duplicate_scalars).decode(), duplicate_transactions,
    ))

    claim_then_error = page_wire(output_wire(
        tx_hash=TX_MAIN,
        claim=claim_wire(short_url="discarded#1"),
    ) + field_bytes(15, error_wire(1, "oneof error wins")))
    cases.append(probe(
        outputs_class, pb, "claim_then_error_oneof_error_wins",
        base64.b64encode(claim_then_error).decode(), [],
    ))

    error_then_claim = page_wire(output_wire(
        tx_hash=TX_MAIN,
        error=error_wire(1, "discarded error"),
    ) + field_bytes(7, claim_wire(short_url="oneof-claim#1")))
    cases.append(probe(
        outputs_class, pb, "error_then_claim_oneof_claim_wins",
        base64.b64encode(error_then_claim).decode(), [
            FixtureTransaction("oneof-tx", TX_MAIN, [("oneof-output", False)]),
        ],
    ))

    merged_claim = output_wire(tx_hash=TX_MAIN)
    merged_claim += field_bytes(7, claim_wire(
        short_url="merged-short#1", effective_amount=1,
    ))
    merged_claim += field_bytes(7, claim_wire(
        canonical_url="@merged#2/merged-short#1", effective_amount=2,
    ))
    cases.append(probe(
        outputs_class, pb, "repeated_same_claim_member_merges",
        base64.b64encode(page_wire(merged_claim)).decode(), [
            FixtureTransaction("merge-tx", TX_MAIN, [("merge-output", False)]),
        ],
    ))

    unknown_output = output_wire(tx_hash=TX_BARE) + field_varint(99, 123)
    unknown_page = page_wire(unknown_output) + field_varint(99, 456)
    cases.append(probe(
        outputs_class, pb, "unknown_fields_preserved_and_ignored",
        base64.b64encode(unknown_page).decode(), [
            FixtureTransaction("unknown-tx", TX_BARE, [("unknown-output", False)]),
        ],
    ))

    wrong_wire_total = page_wire(
        output_wire(tx_hash=TX_BARE),
    ) + field_bytes(3, b"wrong-wire-type")
    cases.append(probe(
        outputs_class, pb, "known_field_wrong_wire_type_is_unknown",
        base64.b64encode(wrong_wire_total).decode(), [
            FixtureTransaction("wrong-wire-tx", TX_BARE, [("wrong-wire-output", False)]),
        ],
    ))

    unknown_enum = page_wire(output_wire(error=error_wire(99, "future error")))
    cases.append(probe(
        outputs_class, pb, "unknown_error_enum_decodes_then_inflate_fails",
        base64.b64encode(unknown_enum).decode(), [],
    ))

    cases.append(probe(outputs_class, pb, "empty_payload_defaults", "", []))
    cases.append(probe(
        outputs_class, pb, "non_alphabet_base64_noise_decodes_empty", "@@@\n!!!", [],
    ))
    cases.append(probe(
        outputs_class, pb, "missing_base64_padding_fails", "Cg", [],
    ))
    cases.append(probe(
        outputs_class, pb, "truncated_protobuf_fails",
        base64.b64encode(b"\x0a\x05\x08").decode(), [],
    ))

    out_of_range = page_wire(output_wire(
        tx_hash=TX_MAIN, nout=9, claim=claim_wire(short_url="too-far#1"),
    ))
    cases.append(probe(
        outputs_class, pb, "output_index_out_of_range_fails_inflate",
        base64.b64encode(out_of_range).decode(), [
            FixtureTransaction("short-tx", TX_MAIN, [("only-output", False)]),
        ],
    ))

    missing_channel = page_wire(output_wire(
        tx_hash=TX_MAIN,
        claim=claim_wire(
            short_url="missing-channel#1",
            channel=reference_wire(TX_MISSING),
        ),
    ))
    cases.append(probe(
        outputs_class, pb, "missing_relationship_transaction_fails_inflate",
        base64.b64encode(missing_channel).decode(), [
            FixtureTransaction("relationship-tx", TX_MAIN, [("relationship-output", False)]),
        ],
    ))

    duplicate_hash = page_wire(output_wire(
        tx_hash=TX_MAIN, claim=claim_wire(short_url="duplicate-map#1"),
    ))
    cases.append(probe(
        outputs_class, pb, "duplicate_supplied_transaction_hash_last_value_wins",
        base64.b64encode(duplicate_hash).decode(), [
            FixtureTransaction("duplicate-first", TX_MAIN, [("first-output", False)]),
            FixtureTransaction("duplicate-last", TX_MAIN, [("last-output", False)]),
        ],
    ))
    return cases


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    args = parser.parse_args()
    sdk_root = args.sdk_root.resolve()

    if os.environ.get("PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION") != "python":
        raise RuntimeError("PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION=python is required")

    verify_source(sdk_root)
    method_hashes = verify_method_hashes(sdk_root)
    pb = load_result_pb2(sdk_root)
    outputs_class = extract_outputs(
        sdk_root, pb.Outputs, pb.Error.Code.Name(pb.Error.BLOCKED),
    )
    cases = build_cases(outputs_class, pb)

    import google.protobuf
    response = {
        "reference": {
            "commit": PINNED_COMMIT,
            "version": PINNED_VERSION,
            "source_sha256": PINNED_SOURCE_HASHES,
            "method_sha256": method_hashes,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "protobuf_version": google.protobuf.__version__,
            "generated_result_pb2_executed": True,
            "extracted_outputs_methods_executed": True,
            "external_network_used": False,
            "case_count": len(cases),
            "decode_error_count": sum("decode_error" in case for case in cases),
            "inflate_error_count": sum("inflate_error" in case for case in cases),
            "proposed_go_contract": {
                "types": "HubOutputs, HubOutput, HubClaimMeta, HubError, HubBlocked",
                "decode_bytes": "DecodeHubOutputsBytes([]byte) (*HubOutputs, error)",
                "decode_base64": "DecodeHubOutputsBase64(string) (*HubOutputs, error)",
                "requests": "(*HubOutputs).TransactionRequests() []HubTransactionRequest",
                "inflate": "(*HubOutputs).Inflate([]*Transaction) ([]any, HubBlockedSummary, error)",
                "note": "Names are advisory; fixture semantics and wire bytes are normative.",
            },
            "contract_notes": {
                "transaction_requests": (
                    "Python stores a deduplicated set of top-level non-error tx/hash pairs; "
                    "the oracle sorts that set only for deterministic JSON."
                ),
                "nested_references": (
                    "Channel, repost, censor, and blocked references do not add fetch requests; "
                    "their transactions must also appear as top-level txos or extra_txos."
                ),
                "unknown_fields": (
                    "Outputs semantics ignore unknown fields. Generated result_pb2 preserves them "
                    "when directly reserialized, but Outputs exposes no reserialize method."
                ),
            },
        },
        "cases": cases,
    }
    print(json.dumps(response, sort_keys=True, separators=(",", ":")))


if __name__ == "__main__":
    main()

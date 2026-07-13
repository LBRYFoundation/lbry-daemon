#!/usr/bin/env python3
"""Source-pinned, offline oracle for the legacy resolve pipeline."""

import argparse
import ast
import asyncio
import copy
from functools import partial
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
import typing
import unicodedata
import warnings
from types import SimpleNamespace
from typing import NamedTuple, Optional, Tuple


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/error/__init__.py": "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
    "lbry/extras/daemon/daemon.py": "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
    "lbry/schema/url.py": "8792ddfef84331c8b2e56b441b738565b6018c085944efa6179dbb03df97f6cd",
    "lbry/wallet/ledger.py": "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
    "lbry/wallet/manager.py": "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
    "lbry/wallet/network.py": "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
    "lbry/wallet/wallet.py": "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
}
METHOD_HASHES = {
    "Daemon.jsonrpc_resolve": "92a5bdbe7b286bc70e70805fc2c58b7a792cba3754eb4300aa2d789554e63af1",
    "Daemon.resolve": "2db0511da5825f1f420c699b45555bde6b8fb968e4afa2f81350aa5f6c45853d",
    "Daemon.ledger": "c0aad64201976cc6d3b4ae3fa49fe9434093c578706b84f45b8cc687c7276f46",
    "Ledger.resolve": "bb0af2c7bbae73d82424c27bcfa9eab458eac18891267336d9e83ac3cd57ce23",
    "Network.resolve": "8aa40f10cb04442c7c87133bcaf4a401512d3fa8d9cc60af210dbaa7682a4abb",
    "URL.has_channel": "d97d6b07b1da5692603a02ca5d223eb6f79124012c6874fa4d4d643b21f398a1",
    "URL.has_stream": "cb59fec78ae4cc6a47636a81fd11c4e60a805e35f2eaf1d5472bd5196199f509",
    "URL.has_stream_in_channel": "17b802114df2eae33dfaff2c608fcadd08e30a4fad9191ce2b2b3dbf9c433b55",
    "URL.parse": "9c7da8c30083337ecbfbb64fced493b41f3d2c7ef50355cbaed7100eb9c9c1b8",
    "Wallet.default_account": "76e84d5c63726f3c268e161ee2ef54e0573ab02a4aab04d9b7c6dae0fc95961e",
    "WalletManager.default_account": "6b5ae4ee1fd368d8b3bb05e3a8a3362a0f958f4e5385787958ff83fdb855e731",
    "WalletManager.default_wallet": "b985d6bbf6126a982f1f0084fc6872592cff7717f50b59fbe3a745f498c8de48",
    "WalletManager.get_wallet_or_default": "a78f3e4003c8bc2c25c95681532cb166eb3685a611aecd6024893fa6c94e8537",
    "WalletManager.get_wallet_or_error": "ac6310a5232801623f12f4be0909a0e64a595a94330465f3c825b9ac34c51eec",
}


class WalletNotLoadedError(Exception):
    def __init__(self, wallet_id):
        super().__init__(f"Wallet {wallet_id} is not loaded.")


class DecodeError(Exception):
    pass


class HubFailure(Exception):
    pass


class StorageFailure(Exception):
    pass


class SignatureFailure(Exception):
    pass


class ProbeChannel:
    def __init__(self, label):
        self.label = label


class ProbeOutput:
    def __init__(self, label, channel, signed, calls, signature_error=False):
        self.label = label
        self.channel = channel
        self.signed = signed
        self.calls = calls
        self.signature_error = signature_error

    def is_signed_by(self, channel, ledger):
        call = {
            "output": self.label,
            "channel": channel.label,
            "ledger": ledger.label,
        }
        self.calls["signature"].append(call)
        self.calls["events"].append({"event": "signature", **call})
        if self.signature_error:
            raise SignatureFailure(f"signature check failed for {self.label}")
        call["valid"] = self.signed
        return self.signed


def selected_method(path, class_name, method_name):
    source = path.read_text(encoding="utf-8")
    source_class = next(
        node for node in ast.parse(source, filename=str(path)).body
        if isinstance(node, ast.ClassDef) and node.name == class_name
    )
    method = next(
        node for node in source_class.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name == method_name
    )
    digest = hashlib.sha256(ast.get_source_segment(source, method).encode()).hexdigest()
    return copy.deepcopy(method), digest


def extracted_property(node):
    node.decorator_list = [ast.Name(id="property", ctx=ast.Load())]
    return node


def extract_types(sdk_root):
    specs = {
        "Daemon.jsonrpc_resolve": ("lbry/extras/daemon/daemon.py", "Daemon", "jsonrpc_resolve"),
        "Daemon.resolve": ("lbry/extras/daemon/daemon.py", "Daemon", "resolve"),
        "Daemon.ledger": ("lbry/extras/daemon/daemon.py", "Daemon", "ledger"),
        "Ledger.resolve": ("lbry/wallet/ledger.py", "Ledger", "resolve"),
        "Network.resolve": ("lbry/wallet/network.py", "Network", "resolve"),
        "Wallet.default_account": ("lbry/wallet/wallet.py", "Wallet", "default_account"),
        "WalletManager.default_account": ("lbry/wallet/manager.py", "WalletManager", "default_account"),
        "WalletManager.default_wallet": ("lbry/wallet/manager.py", "WalletManager", "default_wallet"),
        "WalletManager.get_wallet_or_default": (
            "lbry/wallet/manager.py", "WalletManager", "get_wallet_or_default",
        ),
        "WalletManager.get_wallet_or_error": (
            "lbry/wallet/manager.py", "WalletManager", "get_wallet_or_error",
        ),
        "URL.has_channel": ("lbry/schema/url.py", "URL", "has_channel"),
        "URL.has_stream": ("lbry/schema/url.py", "URL", "has_stream"),
        "URL.has_stream_in_channel": ("lbry/schema/url.py", "URL", "has_stream_in_channel"),
        "URL.parse": ("lbry/schema/url.py", "URL", "parse"),
    }
    nodes, hashes = {}, {}
    for name, (relative, class_name, method_name) in specs.items():
        nodes[name], hashes[name] = selected_method(sdk_root / relative, class_name, method_name)
    if hashes != METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {METHOD_HASHES}")

    nodes["Daemon.jsonrpc_resolve"].decorator_list = []
    classes = [
        ast.ClassDef("ProbeDaemon", [], [], [
            nodes["Daemon.jsonrpc_resolve"], nodes["Daemon.resolve"],
            extracted_property(nodes["Daemon.ledger"]),
        ], []),
        ast.ClassDef("ProbeLedger", [], [], [nodes["Ledger.resolve"]], []),
        ast.ClassDef("ProbeNetwork", [], [], [nodes["Network.resolve"]], []),
        ast.ClassDef("ProbeWallet", [], [], [
            extracted_property(nodes["Wallet.default_account"]),
        ], []),
        ast.ClassDef("ProbeManager", [], [], [
            extracted_property(nodes["WalletManager.default_account"]),
            extracted_property(nodes["WalletManager.default_wallet"]),
            nodes["WalletManager.get_wallet_or_default"],
            nodes["WalletManager.get_wallet_or_error"],
        ], []),
    ]

    url_source = (sdk_root / "lbry/schema/url.py").read_text(encoding="utf-8")
    url_module = ast.parse(url_source, filename="lbry/schema/url.py")
    url_nodes = [
        copy.deepcopy(node) for node in url_module.body
        if isinstance(node, ast.FunctionDef) and node.name in {"_create_url_regex", "normalize_name"}
        or isinstance(node, ast.ClassDef) and node.name in {"PathSegment", "URL"}
    ]
    namespace = {
        "DecodeError": DecodeError,
        "INVALID": "INVALID",
        "Ledger": object,
        "NamedTuple": NamedTuple,
        "NOT_FOUND": "NOT_FOUND",
        "Optional": Optional,
        "Output": ProbeOutput,
        "Tuple": Tuple,
        "Wallet": object,
        "WalletNotLoadedError": WalletNotLoadedError,
        "copy": copy,
        "partial": partial,
        "re": re,
        "typing": typing,
        "unicodedata": unicodedata,
    }
    module = ast.fix_missing_locations(ast.Module(url_nodes + classes, type_ignores=[]))
    exec(compile(module, "<pinned resolve>", "exec"), namespace)
    namespace["URL_REGEX"] = namespace["_create_url_regex"]()
    url_type = namespace["URL"]
    return {
        "daemon": namespace["ProbeDaemon"],
        "ledger": namespace["ProbeLedger"],
        "network": namespace["ProbeNetwork"],
        "wallet": namespace["ProbeWallet"],
        "manager": namespace["ProbeManager"],
        "url": url_type,
        "url_parse": url_type.__dict__["parse"].__func__,
    }, hashes


def json_value(value):
    if isinstance(value, ProbeOutput):
        return {
            "kind": "output",
            "label": value.label,
            "channel": value.channel.label if value.channel else None,
        }
    if isinstance(value, ProbeChannel):
        return {"kind": "channel", "label": value.label}
    if isinstance(value, set):
        return sorted(value)
    raise TypeError(f"cannot serialize {value.__class__.__name__}")


def make_fixture(types, config):
    daemon_type = types["daemon"]
    ledger_type = types["ledger"]
    network_type = types["network"]
    wallet_type = types["wallet"]
    manager_type = types["manager"]
    calls = {
        "events": [], "inflate": [], "retriable": [], "rpc": [],
        "signature": [], "storage": [],
    }

    original_parse = types["url_parse"]

    def traced_parse(cls, value):
        calls["events"].append({"event": "url_parse", "url": copy.deepcopy(value)})
        return original_parse(cls, value)

    types["url"].parse = classmethod(traced_parse)

    def network_rpc(self, method, params, restricted=True, session=None):
        call = {
            "ledger": self.label,
            "method": method,
            "params": copy.deepcopy(params),
            "restricted": restricted,
            "session": session,
        }
        calls["rpc"].append(call)
        calls["events"].append({"event": "rpc", "batch_size": len(params)})

        async def response():
            return {"urls": list(params), "batch": len(calls["rpc"])}

        return response()

    async def retriable_call(self, function, *args, **kwargs):
        call_number = len(calls["retriable"]) + 1
        call = {
            "ledger": self.label,
            "function": function.__name__,
            "args": copy.deepcopy(args),
            "kwargs": copy.deepcopy(kwargs),
        }
        calls["retriable"].append(call)
        calls["events"].append({"event": "retriable", "call": call_number})
        if config.get("fail_retriable_call") == call_number:
            raise HubFailure(f"offline hub failure on call {call_number}")
        return await function(*args, **kwargs)

    occurrence = {}

    def output_spec(url):
        sequence = config.get("output_sequences", {}).get(url)
        if sequence:
            index = occurrence.get(url, 0)
            occurrence[url] = index + 1
            return sequence[min(index, len(sequence) - 1)]
        return config.get("outputs", {}).get(url, config.get("default_output", {
            "kind": "output", "label": url,
        }))

    def make_output(url):
        spec = output_spec(url)
        kind = spec.get("kind", "output")
        if kind == "none":
            return None
        if kind == "error":
            return {"error": {"name": spec["name"], "text": spec["text"]}}
        if kind == "raw":
            return copy.deepcopy(spec["value"])
        if kind == "false":
            return False
        channel = ProbeChannel(spec.get("channel")) if spec.get("channel") else None
        return ProbeOutput(
            spec.get("label", url), channel, spec.get("signed", True), calls,
            spec.get("signature_error", False),
        )

    async def inflate_outputs(self, query, accounts,
                              include_purchase_receipt=False,
                              include_is_my_output=False,
                              include_sent_supports=False,
                              include_sent_tips=False,
                              include_received_tips=False):
        call_number = len(calls["inflate"]) + 1
        call = {
            "ledger": self.label,
            "accounts": [account.label for account in accounts],
            "include_purchase_receipt": include_purchase_receipt,
            "include_is_my_output": include_is_my_output,
            "include_sent_supports": include_sent_supports,
            "include_sent_tips": include_sent_tips,
            "include_received_tips": include_received_tips,
            "state": "started",
        }
        calls["inflate"].append(call)
        calls["events"].append({"event": "inflate_start", "call": call_number})
        try:
            encoded = await query
        except Exception as err:
            call["state"] = "error"
            call["error"] = err.__class__.__name__
            raise
        call["encoded_urls"] = encoded["urls"]
        values = [make_output(url) for url in encoded["urls"]]
        if config.get("mismatch_call") == call_number:
            values = values[:-1]
        call["state"] = "complete"
        call["result_count"] = len(values)
        calls["events"].append({"event": "inflate_complete", "call": call_number})
        return values, {}, 0, len(values)

    inflate_outputs.__name__ = "_inflate_outputs"
    inflate_outputs.__qualname__ = "ProbeLedger._inflate_outputs"
    network_type.rpc = network_rpc
    network_type.retriable_call = retriable_call
    ledger_type._inflate_outputs = inflate_outputs

    default_network, other_network = network_type(), network_type()
    default_network.label, other_network.label = "default-ledger", "other-ledger"
    default_ledger, other_ledger = ledger_type(), ledger_type()
    default_ledger.label, other_ledger.label = "default-ledger", "other-ledger"
    default_ledger.network, other_ledger.network = default_network, other_network

    def account(label, ledger):
        return SimpleNamespace(label=label, ledger=ledger)

    def wallet(identifier, accounts):
        value = wallet_type()
        value.id, value.accounts = identifier, accounts
        return value

    default_accounts = [] if config.get("default_wallet_empty") else [
        account("default-account", default_ledger),
    ]
    default_wallet = wallet("default", default_accounts)
    other_wallet = wallet("other", [
        account("other-account-0", other_ledger),
        account("other-account-1", other_ledger),
    ])
    empty_wallet = wallet("empty", [])
    manager = manager_type()
    manager.wallets = [default_wallet, other_wallet, empty_wallet]

    class ProbeStorage:
        async def save_claim_from_output(self, ledger, *outputs):
            call = {"ledger": ledger.label, "outputs": [output.label for output in outputs]}
            calls["storage"].append(call)
            calls["events"].append({"event": "storage", **call})
            if config.get("storage_error") == "decode":
                raise DecodeError("offline decode failure")
            if config.get("storage_error") == "other":
                raise StorageFailure("offline storage failure")

    daemon = daemon_type()
    daemon.wallet_manager = manager
    daemon.conf = SimpleNamespace(save_resolved_claims=config.get("save_resolved_claims", False))
    daemon.storage = ProbeStorage()
    return daemon, default_ledger, calls


async def execute_case(types, case):
    daemon, ledger, calls = make_fixture(types, case.get("config", {}))
    params = copy.deepcopy(case["params"])
    try:
        if case.get("mode", "daemon") == "ledger":
            accounts = [SimpleNamespace(label="direct-account", ledger=ledger)]
            result = await ledger.resolve(
                accounts, params["urls"], **params.get("options", {})
            )
        else:
            result = await daemon.jsonrpc_resolve(**params)
        error = None
    except Exception as err:  # pylint: disable=broad-except
        result = None
        error = {"type": err.__class__.__name__, "message": str(err)}
    return {
        "name": case["name"], "mode": case.get("mode", "daemon"),
        "params": params, "result": result, "error": error, "calls": calls,
    }


def case_specs():
    batch_urls = [f"lbry://batch-{index:03d}" for index in range(205)]
    return [
        {"name": "scalar string default wallet", "params": {"urls": "lbry://one"}, "config": {
            "outputs": {"lbry://one": {"kind": "output", "label": "one"}},
        }},
        {"name": "list deduplicates and filters invalid", "params": {"urls": [
            "lbry://two", "not a url", "lbry://one", "lbry://two",
        ]}},
        {"name": "empty list", "params": {"urls": []}},
        {"name": "mapping iterates keys", "params": {"urls": {
            "lbry://one": 1, "lbry://two": 2,
        }}},
        {"name": "null is not iterable", "params": {"urls": None}},
        {"name": "number is not iterable", "params": {"urls": 7}},
        {"name": "null list entry aborts validation", "params": {"urls": ["lbry://one", None]}},
        {"name": "missing wallet precedes urls", "params": {
            "urls": None, "wallet_id": "missing",
        }},
        {"name": "selected wallet uses default ledger", "params": {
            "urls": "lbry://selected", "wallet_id": "other",
        }},
        {"name": "empty selected wallet", "params": {
            "urls": "lbry://empty", "wallet_id": "empty",
        }},
        {"name": "selected wallet cannot replace missing default ledger", "params": {
            "urls": "lbry://selected", "wallet_id": "other",
        }, "config": {"default_wallet_empty": True}},
        {"name": "all include flags bind at inflation", "params": {
            "urls": "lbry://includes",
            "include_purchase_receipt": True,
            "include_is_my_output": 1,
            "include_sent_supports": "enabled",
            "include_sent_tips": None,
            "include_received_tips": [],
        }},
        {"name": "documented server option fails before retry", "params": {
            "urls": "lbry://option", "new_sdk_server": "https://offline.invalid",
        }},
        {"name": "ledger batches 205 urls", "mode": "ledger", "params": {
            "urls": batch_urls,
        }, "config": {"default_output": {"kind": "raw", "value": {"resolved": True}}}},
        {"name": "second batch retry failure stops", "mode": "ledger", "params": {
            "urls": batch_urls,
        }, "config": {
            "default_output": {"kind": "raw", "value": {"resolved": True}},
            "fail_retriable_call": 2,
        }},
        {"name": "length mismatch precedes mapping", "mode": "ledger", "params": {
            "urls": ["lbry://one", "lbry://two"],
        }, "config": {"mismatch_call": 1}},
        {"name": "missing and hub error entries", "params": {"urls": [
            "lbry://missing", "lbry://blocked",
        ]}, "config": {"outputs": {
            "lbry://missing": {"kind": "none"},
            "lbry://blocked": {"kind": "error", "name": "BLOCKED", "text": "blocked by fixture"},
        }}},
        {"name": "channel absent replaces output", "params": {
            "urls": "lbry://@chan/absent",
        }, "config": {"outputs": {
            "lbry://@chan/absent": {"kind": "output", "label": "absent"},
        }}},
        {"name": "bad channel signature replaces output", "params": {
            "urls": "lbry://@chan/bad",
        }, "config": {"outputs": {
            "lbry://@chan/bad": {
                "kind": "output", "label": "bad", "channel": "chan", "signed": False,
            },
        }}},
        {"name": "valid channel signature preserves output", "params": {
            "urls": "lbry://@chan/good",
        }, "config": {"outputs": {
            "lbry://@chan/good": {
                "kind": "output", "label": "good", "channel": "chan", "signed": True,
            },
        }}},
        {"name": "stream outside channel skips signature", "params": {
            "urls": "lbry://plain",
        }, "config": {"outputs": {
            "lbry://plain": {
                "kind": "output", "label": "plain", "channel": "ignored", "signed": False,
            },
        }}},
        {"name": "direct duplicate last result wins", "mode": "ledger", "params": {
            "urls": ["lbry://duplicate", "lbry://duplicate"],
        }, "config": {"output_sequences": {"lbry://duplicate": [
            {"kind": "output", "label": "first"},
            {"kind": "output", "label": "second"},
        ]}}},
        {"name": "direct malformed url fails after rpc", "mode": "ledger", "params": {
            "urls": ["not a url"],
        }},
        {"name": "storage saves outputs only", "params": {"urls": [
            "lbry://saved", "lbry://missing", "lbry://blocked",
        ]}, "config": {
            "save_resolved_claims": True,
            "outputs": {
                "lbry://saved": {"kind": "output", "label": "saved"},
                "lbry://missing": {"kind": "none"},
                "lbry://blocked": {"kind": "error", "name": "BLOCKED", "text": "blocked"},
            },
        }},
        {"name": "storage decode error is suppressed", "params": {"urls": "lbry://saved"},
         "config": {
             "save_resolved_claims": True, "storage_error": "decode",
             "outputs": {"lbry://saved": {"kind": "output", "label": "saved"}},
         }},
        {"name": "storage failure follows resolution", "params": {"urls": "lbry://saved"},
         "config": {
             "save_resolved_claims": True, "storage_error": "other",
             "outputs": {"lbry://saved": {"kind": "output", "label": "saved"}},
         }},
        {"name": "all invalid skips storage", "params": {"urls": ["not a url"]},
         "config": {"save_resolved_claims": True}},
        {"name": "signature failure follows resolution", "params": {
            "urls": "lbry://@chan/failure",
        }, "config": {
            "save_resolved_claims": True,
            "outputs": {"lbry://@chan/failure": {
                "kind": "output", "label": "failure", "channel": "chan",
                "signature_error": True,
            }},
        }},
    ]


async def execute(types):
    return [await execute_case(types, case) for case in case_specs()]


def verify_sources(sdk_root):
    commit = subprocess.run(
        ["git", "-C", str(sdk_root), "rev-parse", "HEAD"],
        check=True, capture_output=True, text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    for relative, expected in SOURCE_HASHES.items():
        actual = hashlib.sha256((sdk_root / relative).read_bytes()).hexdigest()
        if actual != expected:
            raise RuntimeError(f"{relative} hash is {actual}, expected {expected}")
    version_source = ast.parse((sdk_root / "lbry/__init__.py").read_text(encoding="utf-8"))
    version = next(
        node.value.value for node in version_source.body
        if isinstance(node, ast.Assign) and any(
            isinstance(target, ast.Name) and target.id == "__version__" for target in node.targets
        )
    )
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")


def run(sdk_root):
    verify_sources(sdk_root)
    types, hashes = extract_types(sdk_root)
    cases = asyncio.run(execute(types))
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
            "case_count": len(cases),
            "proposed_go_contract": {
                "handler": "(*RPCServer).handleResolve(http.ResponseWriter, normalizedRPCParams)",
                "ledger": "(*Ledger).Resolve(context.Context, []*Account, []string, ResolveOptions) (map[string]any, error)",
                "network": "Hub retriable blockchain.claimtrie.resolve calls with flat URL params in sequential batches of 100",
                "note": "Names are advisory; fixture calls, results, and failure order are normative.",
            },
            "contract_notes": {
                "public_preprocessing": "Only strings are wrapped; inputs are URL-validated into a set, so invalid URLs are reported locally and valid duplicates are removed.",
                "ledger_selection": "wallet_id selects accounts, while the first wallet's first account supplies the ledger and network.",
                "mapping": "Falsy entries become NOT_FOUND, hub errors survive, and invalid stream-in-channel signatures become INVALID.",
                "persistence": "Resolved Output values are saved after ledger mapping; DecodeError alone is suppressed.",
            },
        },
        "cases": cases,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    arguments = parser.parse_args()
    warnings.simplefilter("ignore", RuntimeWarning)
    print(json.dumps(run(arguments.sdk_root.resolve()), sort_keys=True, default=json_value))


if __name__ == "__main__":
    main()

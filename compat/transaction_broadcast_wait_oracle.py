#!/usr/bin/env python3
"""Pinned legacy Ledger broadcast and transaction-wait behavior probes."""

import argparse
import ast
import asyncio as real_asyncio
import copy
import hashlib
import json
from binascii import hexlify
from functools import partial
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace
from typing import Iterable


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/wallet/ledger.py": "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
}
PINNED_METHOD_HASHES = {
    "broadcast_or_release": "19708de115f2ff002e1b15bd8dca9902889969c2ea79f19689cdecb02f522d60",
    "broadcast": "55b913aba9fae62a8a0bc88fbd5e0c108e17483c06c3a8ed5cf4c3a825faa1c7",
    "wait": "1806a9a177584e7938b4099cd6c89462f10a94bc1b28c8ad48bc13dd45d3c11c",
    "_wait_round": "3fedda13520ba582ed62255c678be20162ae9e60dd39d71af9d166ec0389ca45",
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


class FakeClock:
    def __init__(self):
        self.reset([0])

    def reset(self, values):
        self.values = list(values)
        self.index = 0
        self.calls = []

    def perf_counter(self):
        index = min(self.index, len(self.values) - 1)
        value = self.values[index]
        self.index += 1
        self.calls.append(value)
        return value


class FakeWaiter:
    def __init__(self, matched):
        self.matched = matched


class FakeAsyncIO:
    TimeoutError = real_asyncio.TimeoutError

    def __init__(self):
        self.calls = []

    async def wait(self, waiters, timeout):
        waiters = list(waiters)
        if not waiters:
            raise ValueError("Set of Tasks/Futures is empty.")
        done = {waiter for waiter in waiters if waiter.matched}
        pending = set(waiters) - done
        self.calls.append({
            "count": len(waiters), "matched": len(done),
            "pending": len(pending), "timeout": timeout,
        })
        return done, pending


class FakeLog:
    def __init__(self):
        self.warnings = []

    def warning(self, message, *arguments):
        self.warnings.append(message % arguments)


def extract_ledger_methods(sdk_root, fake_clock, fake_asyncio, fake_log):
    source = (sdk_root / "lbry/wallet/ledger.py").read_text()
    tree = ast.parse(source)
    ledger = next(
        node for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "Ledger"
    )
    names = set(PINNED_METHOD_HASHES)
    methods = [
        copy.deepcopy(node) for node in ledger.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in names
    ]
    method_hashes = {
        node.name: hashlib.sha256(ast.get_source_segment(source, node).encode()).hexdigest()
        for node in ledger.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in names
    }
    if method_hashes != PINNED_METHOD_HASHES:
        raise RuntimeError(f"Ledger method hashes are {method_hashes}, expected {PINNED_METHOD_HASHES}")
    probe_class = ast.ClassDef(
        name="ProbeLedger", bases=[], keywords=[], body=methods, decorator_list=[],
    )
    module = ast.fix_missing_locations(ast.Module(body=[probe_class], type_ignores=[]))
    namespace = {
        "asyncio": fake_asyncio,
        "hexlify": hexlify,
        "partial": partial,
        "time": fake_clock,
        "log": fake_log,
        "Transaction": object,
        "Iterable": Iterable,
    }
    exec(compile(module, str(sdk_root / "lbry/wallet/ledger.py"), "exec"), namespace)
    return namespace["ProbeLedger"], method_hashes


class FakeNetwork:
    def __init__(self, result="transaction-id", error=None):
        self.result = result
        self.error = error
        self.calls = []

    def broadcast(self, raw_hex):
        self.calls.append(raw_hex)

        async def finish():
            if self.error is not None:
                raise self.error
            return self.result

        return finish()


class FakeDB:
    def __init__(self, rounds=None):
        self.rounds = list(rounds or [])
        self.calls = []

    async def get_addresses(self, address__in):
        self.calls.append(sorted(address__in))
        if not self.rounds:
            return []
        index = min(len(self.calls) - 1, len(self.rounds) - 1)
        return [dict(record) for record in self.rounds[index]]


class FakeStream:
    def __init__(self, events=None):
        self.events = list(events or [])
        self.matches = []

    def where(self, predicate):
        matched = any(predicate(event) for event in self.events)
        self.matches.append(matched)
        return FakeWaiter(matched)


class FakeOutput:
    def __init__(self, kind, payload):
        self.is_pubkey_hash = kind == "pubkey"
        self.is_script_hash = kind == "script"
        if self.is_pubkey_hash:
            self.pubkey_hash = bytes.fromhex(payload)
        if self.is_script_hash:
            self.script_hash = bytes.fromhex(payload)


def fake_transaction(
        txid="target", raw=b"\x00\xff", inputs=None, outputs=None, height=0):
    return SimpleNamespace(
        id=txid, raw=raw, inputs=list(inputs or []),
        outputs=list(outputs or []), height=height,
    )


def resolved_input(output):
    return SimpleNamespace(txo_ref=SimpleNamespace(txo=output))


def unresolved_input():
    return SimpleNamespace(txo_ref=SimpleNamespace(txo=None))


def event(address, txid="target", height=0):
    return SimpleNamespace(address=address, tx=fake_transaction(txid=txid, height=height))


def new_ledger(ledger_class, network=None, db=None, events=None):
    ledger = ledger_class()
    ledger.network = network or FakeNetwork()
    ledger.db = db or FakeDB()
    ledger.on_transaction = FakeStream(events)
    ledger.release_calls = 0
    ledger.release_error = None
    ledger.history_calls = []
    ledger.address_calls = []

    async def release_tx(_tx):
        ledger.release_calls += 1
        if ledger.release_error is not None:
            raise ledger.release_error

    async def get_local_status_and_history(address, history=None):
        ledger.history_calls.append({"address": address, "history": history})
        parts = history.split(":")
        parsed = [(parts[index], int(parts[index + 1])) for index in range(0, len(parts) - 1, 2)]
        return None, parsed

    def hash160_to_address(value):
        result = "pub:" + value.hex()
        ledger.address_calls.append(result)
        return result

    def hash160_to_script_address(value):
        result = "script:" + value.hex()
        ledger.address_calls.append(result)
        return result

    ledger.release_tx = release_tx
    ledger.get_local_status_and_history = get_local_status_and_history
    ledger.hash160_to_address = hash160_to_address
    ledger.hash160_to_script_address = hash160_to_script_address
    return ledger


def error_fields(error):
    if error is None:
        return {"error_type": None, "error_message": None}
    return {"error_type": type(error).__name__, "error_message": str(error)}


async def capture(action):
    try:
        result = await action()
        return {"ok": True, "result": result, **error_fields(None)}
    except BaseException as error:
        return {"ok": False, "result": None, **error_fields(error)}


async def broadcast_cases(ledger_class):
    cases = []
    for name, error in [("success", None), ("rpc failure", RuntimeError("rejected"))]:
        network = FakeNetwork(result="accepted", error=error)
        ledger = new_ledger(ledger_class, network=network)
        outcome = await capture(lambda: ledger.broadcast(fake_transaction()))
        cases.append({"name": name, **outcome, "network_calls": network.calls})
    return cases


async def broadcast_or_release_cases(ledger_class):
    specifications = [
        ("success nonblocking", False, None, None, None),
        ("success blocking", True, None, None, None),
        ("broadcast failure releases", False, RuntimeError("rejected"), None, None),
        ("broadcast cancellation releases", False, real_asyncio.CancelledError(), None, None),
        ("release failure masks broadcast", False, RuntimeError("rejected"), ValueError("release failed"), None),
        ("blocking wait failure does not release", True, None, None, real_asyncio.TimeoutError("wait failed")),
    ]
    cases = []
    for name, blocking, broadcast_error, release_error, wait_error in specifications:
        network = FakeNetwork(error=broadcast_error)
        ledger = new_ledger(ledger_class, network=network)
        ledger.release_error = release_error
        wait_calls = []

        async def fake_wait(_tx, height=-1, timeout=1):
            wait_calls.append({"height": height, "timeout": timeout})
            if wait_error is not None:
                raise wait_error

        ledger.wait = fake_wait
        outcome = await capture(
            lambda ledger=ledger, blocking=blocking:
            ledger.broadcast_or_release(fake_transaction(), blocking=blocking)
        )
        cases.append({
            "name": name, "blocking": blocking, **outcome,
            "network_calls": network.calls,
            "release_calls": ledger.release_calls,
            "wait_calls": wait_calls,
        })
    return cases


async def wait_round_case(ledger_class, fake_asyncio, name, records, events, height):
    fake_asyncio.calls.clear()
    database = FakeDB([records, records])
    ledger = new_ledger(ledger_class, db=database, events=events)
    outcome = await capture(
        lambda: ledger._wait_round(fake_transaction(), height, {"pub:01", "pub:02"})
    )
    return {
        "name": name, **outcome, "db_calls": database.calls,
        "event_matches": ledger.on_transaction.matches,
        "event_waits": list(fake_asyncio.calls),
        "history_calls": ledger.history_calls,
    }


async def wait_round_cases(ledger_class, fake_asyncio):
    record = lambda address, history: {"address": address, "history": history}
    return [
        await wait_round_case(
            ledger_class, fake_asyncio, "all addresses observed by events",
            [record("pub:01", ""), record("pub:02", "")],
            [event("pub:01", height=3), event("pub:02", height=3)], 2,
        ),
        await wait_round_case(
            ledger_class, fake_asyncio, "partial events fall back to history",
            [record("pub:01", ""), record("pub:02", "target:3:")],
            [event("pub:01", height=3)], 2,
        ),
        await wait_round_case(
            ledger_class, fake_asyncio, "mempool history satisfies positive height",
            [record("pub:01", "target:0:")], [], 5,
        ),
        await wait_round_case(
            ledger_class, fake_asyncio, "low match stops later record scan",
            [record("pub:01", "target:2:"), record("pub:02", "target:7:")], [], 5,
        ),
        await wait_round_case(
            ledger_class, fake_asyncio, "no owned records",
            [], [], -1,
        ),
    ]


async def full_wait_case(
        ledger_class, fake_clock, fake_asyncio, name, tx, records, events,
        height, timeout, clock_values):
    fake_clock.reset(clock_values)
    fake_asyncio.calls.clear()
    database = FakeDB([records, records])
    ledger = new_ledger(ledger_class, db=database, events=events)
    outcome = await capture(lambda: ledger.wait(tx, height=height, timeout=timeout))
    return {
        "name": name, **outcome, "db_calls": database.calls,
        "address_calls": ledger.address_calls,
        "clock_calls": list(fake_clock.calls),
        "event_waits": list(fake_asyncio.calls),
    }


async def full_wait_cases(ledger_class, fake_clock, fake_asyncio):
    pub = "11" * 20
    script = "22" * 20
    affected = fake_transaction(
        inputs=[unresolved_input(), resolved_input(FakeOutput("pubkey", pub))],
        outputs=[FakeOutput("pubkey", pub), FakeOutput("script", script)],
    )
    records = [
        {"address": "pub:" + pub, "history": ""},
        {"address": "script:" + script, "history": ""},
    ]
    cases = [await full_wait_case(
        ledger_class, fake_clock, fake_asyncio, "collects and deduplicates affected addresses",
        affected, records,
        [event("pub:" + pub), event("script:" + script)], -1, 1, [10, 10],
    )]
    cases.append(await full_wait_case(
        ledger_class, fake_clock, fake_asyncio, "zero timeout expands to 600",
        fake_transaction(outputs=[FakeOutput("pubkey", pub)]),
        [{"address": "pub:" + pub, "history": ""}], [], -1, 0, [0, 0, 601],
    ))
    cases.append(await full_wait_case(
        ledger_class, fake_clock, fake_asyncio, "negative timeout skips rounds",
        fake_transaction(outputs=[FakeOutput("pubkey", pub)]),
        [{"address": "pub:" + pub, "history": ""}], [], -1, -1, [0, 0],
    ))
    script_input = fake_transaction(inputs=[resolved_input(FakeOutput("script", script))])
    cases.append(await full_wait_case(
        ledger_class, fake_clock, fake_asyncio, "resolved script-hash input lacks pubkey hash",
        script_input, [], [], -1, 1, [0, 0],
    ))
    return cases


async def run(sdk_root):
    verify_source(sdk_root)
    fake_clock = FakeClock()
    fake_asyncio = FakeAsyncIO()
    fake_log = FakeLog()
    ledger_class, method_hashes = extract_ledger_methods(
        sdk_root, fake_clock, fake_asyncio, fake_log,
    )
    version_tree = ast.parse((sdk_root / "lbry/__init__.py").read_text())
    version = next(
        node.value.value for node in version_tree.body
        if isinstance(node, ast.Assign) and
        any(isinstance(target, ast.Name) and target.id == "__version__" for target in node.targets)
    )
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    return {
        "reference": {
            "commit": PINNED_COMMIT,
            "version": version,
            "source_sha256": PINNED_SOURCE_HASHES,
            "method_sha256": method_hashes,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "extracted_methods_executed": True,
            "external_network_used": False,
        },
        "broadcast": await broadcast_cases(ledger_class),
        "broadcast_or_release": await broadcast_or_release_cases(ledger_class),
        "wait_round": await wait_round_cases(ledger_class, fake_asyncio),
        "wait": await full_wait_cases(ledger_class, fake_clock, fake_asyncio),
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    arguments = parser.parse_args()
    result = real_asyncio.run(run(arguments.sdk_root.resolve()))
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    main()

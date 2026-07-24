#!/usr/bin/env python3
"""Pinned offline probes for transaction_show claim-related output encoding.

The legacy JSONResponseEncoder methods are extracted from the pinned SDK and
executed against small output fixtures.  The live stream fixture is a hardcoded
snapshot of the transaction supplied for live validation; this script itself
does not access the network.
"""

import argparse
import ast
from binascii import hexlify, unhexlify
import copy
from datetime import datetime
from decimal import Decimal
import hashlib
import json
from json import JSONEncoder
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/extras/daemon/json_response_encoder.py":
        "047fd406c20236025414b8805669b1a830b0b412386c1613498aa1ebaa021732",
    "lbry/schema/base.py":
        "898875eebd916eee0ea4ad9e2be8aff53f8f56a1479f664e143f0461ffba7140",
    "lbry/schema/claim.py":
        "2b2a58f580efc2d5ea7bbfadfff28ea150429dbcc71fcefaf8992fa5213027af",
    "lbry/schema/purchase.py":
        "39ff765aeadf13d0df039c50b93cc9d2240fdc1fa8242a488bcd11d0cdef632f",
    "lbry/schema/support.py":
        "3c8868541d4d2c58893e03b4bfce2c48580d3851806d844fcb786626cd37d31f",
    "lbry/schema/types/v2/claim_pb2.py":
        "3edb36895d7d2f294e27019438332ca8a7ed4cb3c0f30ee33c9aa406bf000c98",
    "lbry/schema/types/v2/purchase_pb2.py":
        "510add43b5fe4f67c24497d8ab2bfc16ae92e20eab3a9e1d0b2a8f29d3aa0aec",
    "lbry/schema/types/v2/support_pb2.py":
        "fea8198f476609b523992ef4f0f446fa004f38facae2ca1f33cfe41a905f825a",
    "lbry/wallet/database.py":
        "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
    "lbry/wallet/dewies.py":
        "67506d75a5f0ddb3f7c2ea832ba7b13fb49ae4193f060a1fdf541b5f50a3084a",
    "lbry/wallet/transaction.py":
        "e73491aeb915fbce931acbb4d9631f3e05440a7d26c598db85e66e524a798d15",
    "lbry/wallet/util.py":
        "08f697c88ec36d2bb417609194266f279eba2f69b1a62a10b1de69b9c1733d5a",
}

PINNED_METHOD_HASHES = {
    "Database.get_transaction":
        "170b2b7217a51a966b609a936ef8585cb31e1bc97463157c24278c46658a9107",
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
    "dewies_to_lbc":
        "e134ee4ea5e7d5000bb7f3a1d37dd40b6913724e142ba5c6b8e1f235c064fc5b",
    "satoshis_to_coins":
        "ff81838bc9fc0d2583372395b8299c1cd6aca6ee95b5e4819b28e883b2e1ad50",
}

LIVE_TXID = "4efd7e17bdc0664b02834f7c743917de4834b79ef2ce725778bafc5554fb3158"
LIVE_HEIGHT = 2088350
LIVE_BLOCK_TIME = 1783777424
LIVE_CLAIM_ID = "bb9a6185f017956711c05f44d71f0bc4ae20a27a"
LIVE_NAME = "T4NG3RIN-ASMR--02"
LIVE_ADDRESS = "bTMNi3y5tYPZrLRjeuqMXtvcXrA97wAUAZ"
LIVE_PROTOBUF = (
    "000aa7010a8d010a30727c8f4f681de1cee70903ccfbef38dac5d39104e247ec4d7cc597fdafc84fd1d8f89333207d01c"
    "16280e1bd4380dd0512175244545f32303236303731315f3130333132352e6d703418a4d18d602209766964656f2f6d7034"
    "323036d72ae4e3d594fe090a17e881f53fd2a1acde20dcb64cc495b72c2f1a0f2cd838517b3eb21b54132367e68e4d601"
    "a581a044e6f6e65289b90c9d2065a0908800a10ce0518d305421154344e473352314e2041534d522023303352412a3f6874"
    "7470733a2f2f7468756d62732e6f647963646e2e636f6d2f6363376164623531363638306531343630346131396462306164"
    "6639626562612e776562705a0461736d725a0b656172206c69636b696e675a0a65617220656174696e675a12633a64697361"
    "626c652d636f6d6d656e74735a0f64697361626c652d737570706f727462020801"
)
LIVE_VALUE = {
    "source": {
        "hash": "727c8f4f681de1cee70903ccfbef38dac5d39104e247ec4d7cc597fdafc84fd1d8f89333207d01c16280e1bd4380dd05",
        "name": "RDT_20260711_103125.mp4",
        "size": "201549988",
        "media_type": "video/mp4",
        "sd_hash": "36d72ae4e3d594fe090a17e881f53fd2a1acde20dcb64cc495b72c2f1a0f2cd838517b3eb21b54132367e68e4d601a58",
    },
    "license": "None",
    "release_time": "1783777307",
    "video": {"width": 1280, "height": 718, "duration": 723},
    "title": "T4NG3R1N ASMR #03",
    "thumbnail": {
        "url": "https://thumbs.odycdn.com/cc7adb516680e14604a19db0adf9beba.webp"
    },
    "tags": [
        "asmr", "ear licking", "ear eating", "c:disable-comments", "disable-support",
    ],
    "languages": ["en"],
    "stream_type": "video",
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
    encoder_path = sdk_root / "lbry/extras/daemon/json_response_encoder.py"
    database_path = sdk_root / "lbry/wallet/database.py"
    wallet_root = sdk_root / "lbry/wallet"
    util_functions, util_hashes = selected_functions(
        wallet_root / "util.py", {"satoshis_to_coins"},
    )
    dewies_functions, dewies_hashes = selected_functions(
        wallet_root / "dewies.py", {"dewies_to_lbc"},
    )
    encoder_methods, encoder_hashes = selected_methods(
        encoder_path, "JSONResponseEncoder",
        {"__init__", "default", "encode_output", "encode_claim_meta", "encode_claim"},
    )
    _, database_hashes = selected_methods(
        database_path, "Database", {"tx_to_row", "get_transactions", "get_transaction"},
    )
    hashes = {**util_hashes, **dewies_hashes, **encoder_hashes, **database_hashes}
    if hashes != PINNED_METHOD_HASHES:
        raise RuntimeError(f"method hashes are {hashes}, expected {PINNED_METHOD_HASHES}")
    module = ast.fix_missing_locations(ast.Module(body=[
        *util_functions,
        *dewies_functions,
        extracted_class("JSONResponseEncoder", encoder_methods, "JSONEncoder"),
    ], type_ignores=[]))
    namespace = {
        "Account": type("Account", (), {}),
        "Claim": type("Claim", (), {}),
        "COIN": 100_000_000,
        "Decimal": Decimal,
        "DecodeError": type("DecodeError", (Exception,), {}),
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
    return namespace, hashes


class FixtureHeaders:
    def __init__(self, height, timestamps=None):
        self.height = height
        self.timestamps = timestamps or {}

    def estimated_timestamp(self, height, try_real_headers=True):
        del try_real_headers
        if height <= 0:
            return None
        return self.timestamps.get(height, 7_000 + height)


class FixtureLedger:
    def __init__(self, height, timestamps=None):
        self.headers = FixtureHeaders(height, timestamps)


class FixtureScript:
    def __init__(self, kind):
        self.is_claim_name = kind == "create"
        self.is_update_claim = kind == "update"
        self.is_support_claim = kind in ("support", "support_data")
        self.is_support_claim_data = kind == "support_data"
        self.is_return_data = kind == "data"
        self.is_claim_involved = kind in ("create", "update", "support", "support_data")


class FixtureClaimBranch:
    def __init__(self, value):
        self.value = value

    def to_dict(self):
        return copy.deepcopy(self.value)


class FixtureClaim:
    def __init__(self, value, protobuf):
        self.claim_type = "stream"
        self.stream = FixtureClaimBranch(value)
        self.protobuf = bytes.fromhex(protobuf)
        self.is_channel = False
        self.is_signed = False
        self.signing_channel_id = None

    def to_bytes(self):
        return self.protobuf


class FixtureSupport:
    def __init__(self):
        self.is_signed = False
        self.signing_channel_id = None

    @staticmethod
    def to_dict():
        return {"emoji": ":)", "comment": "steady"}

    @staticmethod
    def to_bytes():
        return bytes.fromhex("000a023a291206737465616479")


class FixtureOutput:
    def __init__(
        self, txid, height, position, amount, kind, address=None,
        name=None, claim_id=None, signable=None,
    ):
        self.tx_ref = SimpleNamespace(id=txid, height=height)
        self.position = position
        self.amount = amount
        self.script = FixtureScript(kind)
        self.address = address
        self.has_address = address is not None
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
        self.meta = {}
        self.claim_name = name
        self.normalized_name = name.casefold() if name is not None else None
        self.claim_id = claim_id
        self.permanent_url = (
            f"lbry://{name}#{claim_id}" if name is not None and claim_id is not None else None
        )
        self.signable = signable
        self.claim = signable
        self.has_private_key = False
        self.purchased_claim_id = None

    def get_address(self, ledger):
        del ledger
        return self.address


def fixture_cases():
    live_claim = FixtureClaim(LIVE_VALUE, LIVE_PROTOBUF)
    live_output = FixtureOutput(
        LIVE_TXID, LIVE_HEIGHT, 0, 100_000, "create", LIVE_ADDRESS,
        LIVE_NAME, LIVE_CLAIM_ID, live_claim,
    )
    deterministic_txid = "aa" * 32
    deterministic_address = "fixture-address"
    deterministic_claim = FixtureClaim(
        {"title": "Fixture"}, "000a00420746697874757265",
    )
    create = FixtureOutput(
        deterministic_txid, 7, 0, 100_000_000, "create", deterministic_address,
        "MiXeD", "11" * 20, deterministic_claim,
    )
    update = FixtureOutput(
        deterministic_txid, 7, 1, 200_000_000, "update", deterministic_address,
        "MiXeD", "22" * 20, deterministic_claim,
    )
    plain_support = FixtureOutput(
        deterministic_txid, 7, 2, 300_000_000, "support", deterministic_address,
        "MiXeD", "22" * 20,
    )
    support_data = FixtureOutput(
        deterministic_txid, 7, 3, 400_000_000, "support_data", deterministic_address,
        "MiXeD", "22" * 20, FixtureSupport(),
    )
    purchase_payment = FixtureOutput(
        deterministic_txid, 7, 0, 500_000_000, "payment", deterministic_address,
    )
    purchase_data = FixtureOutput(
        deterministic_txid, 7, 1, 0, "data",
    )
    local_purchase_payment = copy.copy(purchase_payment)
    local_purchase_payment.purchase = purchase_data
    local_purchase_payment.purchased_claim_id = "33" * 20
    return [
        {
            "name": "live stream create remote", "source_mode": "remote",
            "include_protobuf": False, "ledger_height": LIVE_HEIGHT,
            "timestamps": {LIVE_HEIGHT: LIVE_BLOCK_TIME}, "outputs": [live_output],
        },
        {
            "name": "live stream create remote protobuf", "source_mode": "remote",
            "include_protobuf": True, "ledger_height": LIVE_HEIGHT,
            "timestamps": {LIVE_HEIGHT: LIVE_BLOCK_TIME}, "outputs": [live_output],
        },
        {
            "name": "deterministic create", "source_mode": "remote",
            "include_protobuf": False, "ledger_height": 10, "outputs": [create],
        },
        {
            "name": "deterministic create protobuf", "source_mode": "remote",
            "include_protobuf": True, "ledger_height": 10, "outputs": [create],
        },
        {
            "name": "deterministic update protobuf", "source_mode": "remote",
            "include_protobuf": True, "ledger_height": 10, "outputs": [update],
        },
        {
            "name": "plain support protobuf ignored", "source_mode": "remote",
            "include_protobuf": True, "ledger_height": 10, "outputs": [plain_support],
        },
        {
            "name": "support data", "source_mode": "remote",
            "include_protobuf": False, "ledger_height": 10, "outputs": [support_data],
        },
        {
            "name": "support data protobuf", "source_mode": "remote",
            "include_protobuf": True, "ledger_height": 10, "outputs": [support_data],
        },
        {
            "name": "purchase remote unhydrated", "source_mode": "remote",
            "include_protobuf": True, "ledger_height": 10,
            "outputs": [purchase_payment, purchase_data],
        },
        {
            "name": "purchase local hydrated", "source_mode": "local",
            "include_protobuf": True, "ledger_height": 10,
            "outputs": [local_purchase_payment, purchase_data],
        },
    ]


def execute_cases(namespace):
    namespace["Claim"] = FixtureClaim
    namespace["Support"] = FixtureSupport
    namespace["Output"] = FixtureOutput
    encoder_class = namespace["JSONResponseEncoder"]
    results = []
    for fixture in fixture_cases():
        ledger = FixtureLedger(
            fixture["ledger_height"], fixture.get("timestamps"),
        )
        encoder = encoder_class(
            ledger=ledger, include_protobuf=fixture["include_protobuf"],
        )
        encoded = [encoder.encode_output(output) for output in fixture["outputs"]]
        projected = json.loads(encoder.encode(encoded))
        results.append({
            "name": fixture["name"],
            "source_mode": fixture["source_mode"],
            "include_protobuf": fixture["include_protobuf"],
            "outputs": projected,
        })
    return results


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    arguments = parser.parse_args()
    sdk_root = arguments.sdk_root.resolve()
    verify_source(sdk_root)
    namespace, method_hashes = extract_sdk_slice(sdk_root)
    results = execute_cases(namespace)
    print(json.dumps({
        "reference": {
            "commit": PINNED_COMMIT,
            "version": PINNED_VERSION,
            "source_sha256": PINNED_SOURCE_HASHES,
            "method_sha256": method_hashes,
        },
        "metadata": {
            "python_version": sys.version.split()[0],
            "extracted_encoder_methods_executed": True,
            "external_network_used": False,
            "case_count": len(results),
            "live_transaction_id": LIVE_TXID,
            "live_snapshot_height": LIVE_HEIGHT,
            "live_snapshot_origin": "public Chainquery records queried 2026-07-11",
            "hydration_contract": {
                "remote_inputs": "unresolved",
                "local_inputs": "resolved when the referenced txo exists in SQLite",
                "remote_purchase_payment": "payment because purchase metadata is not linked",
                "local_purchase_payment": "purchase after get_transactions links output 1 metadata to output 0",
                "remote_signed_claim": "channel-id stub and invalid signature status",
                "local_signed_claim": "resolved signing channel when the channel txo exists in SQLite",
                "claim_meta": "empty for transaction_show local and remote raw outputs",
                "include_protobuf": "only claim create/update and support-with-data add protobuf",
            },
        },
        "cases": results,
    }, sort_keys=True))


if __name__ == "__main__":
    main()

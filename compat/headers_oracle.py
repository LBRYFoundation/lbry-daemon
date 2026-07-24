#!/usr/bin/env python3
"""Source-pinned, stdlib-only model of the legacy 112-byte header chain.

The oracle accepts one JSON object. Case collections are optional::

    {
      "fixture_header_indices": [0, 1, 10],
      "hash_cases": [{"name": "text", "data_text": "test string"}],
      "pow_cases": [{"name": "text", "data_text": "test string"}],
      "compact_cases": [{"name": "bits", "compact": 520159231}],
      "target_cases": [{
        "name": "retarget", "max_target_hex": "0000ffff...",
        "target_timespan": 150,
        "previous": {"timestamp": 1386475638},
        "current": {"timestamp": 1386475638, "bits": 520159231}
      }],
      "chain_cases": [{
        "name": "first three", "fixture_start": 0, "fixture_count": 3,
        "start": 0, "validate_difficulty": true
      }]
    }

Header cases are read from ``tests/unit/wallet/test_headers.py`` with the AST.
Compact checkpoint metadata is read from ``lbry/wallet/checkpoints.py`` with
the AST and returned for every request.
Byte cases may use ``data_hex``, ``data_text``, or ``repeat_char`` plus
``repeat_count``. Chain cases can independently select an initial fixture
slice and the input slice, then apply byte mutations described by ``offset``
and either ``xor`` or ``value``.

This intentionally models the pinned implementation instead of importing it:
the SDK's normal import graph requires historical third-party packages. The
authoritative source files and test fixtures are SHA-256 checked before any
case is evaluated.
"""

import argparse
import ast
import hashlib
import json
from pathlib import Path
import struct
import subprocess
import sys


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/wallet/header.py": "139376a70a383bb8b265b377b50abc959e370f7d7678614c938ab3ac65824a54",
    "lbry/wallet/checkpoints.py": "1301ad68706ca8d25d00a44517767c794cda1e7dc7dd8a216342b91febd1e011",
    "lbry/wallet/util.py": "08f697c88ec36d2bb417609194266f279eba2f69b1a62a10b1de69b9c1733d5a",
    "lbry/crypto/hash.py": "bfc430bd3fe98578b406caa3a8e2116a40f492c7b68e269176e838b4ef426a72",
    "tests/unit/wallet/test_headers.py": "e706f1709427131147dbf76d69199e7291b9ebebb5b8618fca942659f769998b",
    "tests/unit/wallet/test_utils.py": "3008c26e38b8b62aba48214ab5b9de54f180d97dea76037005c3f0cc8a7cb4ce",
}

HEADER_SIZE = 112
CHUNK_SIZE = 10**16
MAX_TARGET = int(
    "0000ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16
)
GENESIS_HASH = "9c89283ba0f3227f6c03b70216b9f665f0118d5e0fa729cedf4fb34d6a34f463"
TARGET_TIMESPAN = 150
UINT256_MODULUS = 2**256


def sha256_file(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def read_sdk_version(sdk_root):
    path = sdk_root / "lbry" / "__init__.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    for node in tree.body:
        if isinstance(node, ast.Assign) and any(
            isinstance(target, ast.Name) and target.id == "__version__"
            for target in node.targets
        ):
            return ast.literal_eval(node.value)
    raise RuntimeError("could not read SDK version")


def verify_pinned_sources(sdk_root):
    commit = subprocess.run(
        ["git", "-C", str(sdk_root), "rev-parse", "HEAD"],
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    for relative_path, expected in PINNED_SOURCE_HASHES.items():
        path = sdk_root / relative_path
        if not path.is_file():
            raise RuntimeError(f"pinned SDK source is missing {relative_path}")
        actual = sha256_file(path)
        if actual != expected:
            raise RuntimeError(
                f"{relative_path} does not match pinned SDK: "
                f"sha256 is {actual}, expected {expected}"
            )
    version = read_sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    return commit, version


def load_header_fixture(sdk_root):
    path = sdk_root / "tests" / "unit" / "wallet" / "test_headers.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    for node in tree.body:
        if not isinstance(node, ast.Assign):
            continue
        if not any(
            isinstance(target, ast.Name) and target.id == "HEADERS"
            for target in node.targets
        ):
            continue
        if not isinstance(node.value, ast.Call) or len(node.value.args) != 1:
            break
        encoded = ast.literal_eval(node.value.args[0])
        if not isinstance(encoded, bytes):
            break
        fixture = bytes.fromhex(encoded.decode("ascii"))
        if len(fixture) % HEADER_SIZE:
            raise RuntimeError("pinned HEADERS fixture is not header aligned")
        return fixture
    raise RuntimeError("could not AST-extract the pinned HEADERS fixture")


def load_checkpoint_metadata(sdk_root):
    path = sdk_root / "lbry" / "wallet" / "checkpoints.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    checkpoints = None
    for node in tree.body:
        if not isinstance(node, ast.Assign):
            continue
        if not any(
            isinstance(target, ast.Name) and target.id == "HASHES"
            for target in node.targets
        ):
            continue
        checkpoints = ast.literal_eval(node.value)
        break
    if not isinstance(checkpoints, dict) or not checkpoints:
        raise RuntimeError("could not AST-extract the pinned checkpoint table")

    heights = list(checkpoints)
    if any(not isinstance(height, int) for height in heights):
        raise RuntimeError("pinned checkpoint heights are not integers")
    interval = heights[1] - heights[0] if len(heights) > 1 else 1000
    expected_heights = list(range(0, heights[-1] + interval, interval))
    if interval != 1000 or heights != expected_heights:
        raise RuntimeError("pinned checkpoints are not ascending 1000-height entries")

    digests = []
    for height in heights:
        checkpoint = checkpoints[height]
        if (
            not isinstance(checkpoint, str)
            or len(checkpoint) != 64
            or checkpoint != checkpoint.lower()
        ):
            raise RuntimeError(f"checkpoint {height} is not lowercase SHA-256 hex")
        try:
            digest = bytes.fromhex(checkpoint)
        except ValueError as error:
            raise RuntimeError(f"checkpoint {height} is not hexadecimal") from error
        if len(digest) != 32:
            raise RuntimeError(f"checkpoint {height} is not a 32-byte digest")
        digests.append(digest)

    raw = b"".join(digests)
    return {
        "count": len(heights),
        "interval": interval,
        "first_height": heights[0],
        "last_height": heights[-1],
        "first_hash": checkpoints[heights[0]],
        "last_hash": checkpoints[heights[-1]],
        "raw_size": len(raw),
        "raw_sha256": hashlib.sha256(raw).hexdigest(),
    }


def double_sha256(value):
    return hashlib.sha256(hashlib.sha256(value).digest()).digest()


def ripemd160(value):
    try:
        digest = hashlib.new("ripemd160")
    except ValueError as error:
        raise RuntimeError("stdlib hashlib does not provide RIPEMD-160") from error
    digest.update(value)
    return digest.digest()


def hash_header(header):
    if header is None:
        return "0" * 64
    return double_sha256(header)[::-1].hex()


def header_hash_to_pow_hash(header_hash):
    header_hash_bytes = bytes.fromhex(header_hash)[::-1]
    wide = hashlib.sha512(header_hash_bytes).digest()
    combined = ripemd160(wide[: len(wide) // 2]) + ripemd160(wide[len(wide) // 2 :])
    return double_sha256(combined)[::-1].hex()


def serialize_header(header):
    return b"".join(
        (
            struct.pack("<I", header["version"]),
            bytes.fromhex(header["prev_block_hash"])[::-1],
            bytes.fromhex(header["merkle_root"])[::-1],
            bytes.fromhex(header["claim_trie_root"])[::-1],
            struct.pack(
                "<III", header["timestamp"], header["bits"], header["nonce"]
            ),
        )
    )


def deserialize_header(height, header):
    version, = struct.unpack("<I", header[:4])
    timestamp, bits, nonce = struct.unpack("<III", header[100:112])
    return {
        "version": version,
        "prev_block_hash": header[4:36][::-1].hex(),
        "merkle_root": header[36:68][::-1].hex(),
        "claim_trie_root": header[68:100][::-1].hex(),
        "timestamp": timestamp,
        "bits": bits,
        "nonce": nonce,
        "block_height": height,
    }


class ArithUint256:
    """Mirror lbry.wallet.util.ArithUint256, including its historical quirks."""

    def __init__(self, value):
        self.value = value

    @classmethod
    def from_compact(cls, compact):
        size = compact >> 24
        word = compact & 0x007FFFFF
        if size <= 3:
            return cls(word >> (8 * (3 - size)))
        return cls(word << (8 * (size - 3)))

    @property
    def bits(self):
        bits = bin(self.value)[2:]
        for index, digit in enumerate(bits):
            # The source tests the one-character string, so even "0" is true.
            if digit:
                return (len(bits) - index) + 1
        return 0

    def calculate_compact(self, negative=False):
        size = (self.bits + 7) // 8
        if size <= 3:
            compact = (self.value & 0xFFFFFFFFFFFFFFFF) << (8 * (3 - size))
        else:
            compact = (self.value >> (8 * (size - 3))) & 0xFFFFFFFFFFFFFFFF
        if compact & 0x00800000:
            compact >>= 8
            size += 1
        assert compact & ~0x007FFFFF == 0
        assert size < 256
        compact |= size << 24
        if negative and compact & 0x007FFFFF:
            compact |= 0x00800000
        return compact

    @property
    def compact(self):
        return self.calculate_compact()

    @property
    def negative(self):
        return self.calculate_compact(negative=True)

    def multiply(self, multiplier):
        return ArithUint256((self.value * multiplier) % UINT256_MODULUS)

    def divide(self, divisor):
        # The pinned __truediv__ uses int/int and then int(), so it rounds via
        # an IEEE-754 binary64 before truncating rather than dividing exactly.
        return ArithUint256(int(self.value / divisor))


def next_block_target(max_target, target_timespan, previous, current):
    if previous is None and current is None:
        return max_target
    if previous is None:
        previous = current
    actual_timespan = current["timestamp"] - previous["timestamp"]
    modulated_timespan = target_timespan + int(
        (actual_timespan - target_timespan) / 8
    )
    minimum_timespan = target_timespan - int(target_timespan / 8)
    maximum_timespan = target_timespan + int(target_timespan / 2)
    clamped_timespan = max(
        minimum_timespan, min(modulated_timespan, maximum_timespan)
    )
    target = ArithUint256.from_compact(current["bits"])
    candidate = target.multiply(clamped_timespan).divide(target_timespan)
    return max_target if max_target.value < candidate.value else candidate


class InvalidHeader(Exception):
    def __init__(self, height, message):
        super().__init__(message)
        self.height = height


class HeaderChain:
    def __init__(self, initial, validate_difficulty=True):
        if len(initial) % HEADER_SIZE:
            raise ValueError("initial header bytes are not aligned")
        self.data = bytearray(initial)
        self.size = len(initial) // HEADER_SIZE
        self.validate_difficulty = validate_difficulty

    @property
    def height(self):
        return self.size - 1

    def read(self, height):
        if not 0 <= height <= self.height:
            raise IndexError(f"{height} is out of bounds, current height: {self.height}")
        offset = height * HEADER_SIZE
        return bytes(self.data[offset : offset + HEADER_SIZE])

    def write(self, height, headers):
        offset = height * HEADER_SIZE
        if offset > len(self.data):
            self.data.extend(bytes(offset - len(self.data)))
        end = offset + len(headers)
        if end > len(self.data):
            self.data.extend(bytes(end - len(self.data)))
        self.data[offset:end] = headers
        self.size = max(self.size, end // HEADER_SIZE)
        return len(headers) // HEADER_SIZE

    def validate_header(self, height, current_hash, header, previous_hash, target):
        if previous_hash is None:
            if current_hash != GENESIS_HASH:
                raise InvalidHeader(
                    height,
                    f"genesis header doesn't match: {current_hash} vs expected {GENESIS_HASH}",
                )
            return
        if header["prev_block_hash"] != previous_hash:
            raise InvalidHeader(
                height,
                f"previous hash mismatch: {header['prev_block_hash']} "
                f"vs expected {previous_hash}",
            )
        if not self.validate_difficulty:
            return
        if header["bits"] != target.compact:
            raise InvalidHeader(
                height, f"bits mismatch: {header['bits']} vs expected {target.compact}"
            )
        proof = ArithUint256(int(header_hash_to_pow_hash(current_hash), 16))
        if proof.value > target.value:
            raise InvalidHeader(
                height,
                f"insufficient proof of work: {proof.value} vs target {target.value}",
            )

    def validate_chunk(self, height, headers):
        previous_hash = previous_header = previous_previous_header = None
        if height > 0:
            raw = self.read(height - 1)
            previous_header = deserialize_header(height - 1, raw)
            previous_hash = hash_header(raw)
        if height > 1:
            previous_previous_header = deserialize_header(
                height - 2, self.read(height - 2)
            )
        for index in range(len(headers) // HEADER_SIZE):
            offset = index * HEADER_SIZE
            raw = headers[offset : offset + HEADER_SIZE]
            current_header = deserialize_header(height + index, raw)
            current_hash = hash_header(raw)
            target = next_block_target(
                ArithUint256(MAX_TARGET),
                TARGET_TIMESPAN,
                previous_previous_header,
                previous_header,
            )
            # validate_chunk passes its starting height for every header.
            self.validate_header(
                height, current_hash, current_header, previous_hash, target
            )
            previous_previous_header = previous_header
            previous_header = current_header
            previous_hash = current_hash

    def connect(self, start, headers):
        assert len(headers) % HEADER_SIZE == 0
        try:
            self.validate_chunk(start, headers)
        except InvalidHeader as error:
            # For all practical inputs there is one huge chunk. Because the
            # error height equals start, the rejected prefix is always empty.
            headers = headers[: (start - error.height) * HEADER_SIZE]
        return self.write(start, headers) if headers else 0


def parse_integer(value):
    if isinstance(value, int):
        return value
    return int(value, 0)


def fixture_slice(fixture, case, prefix=""):
    direct_key = f"{prefix}hex"
    if direct_key in case:
        return bytes.fromhex(case[direct_key])
    start = int(case.get(f"{prefix}fixture_start", 0))
    count = int(case.get(f"{prefix}fixture_count", 0))
    return fixture[start * HEADER_SIZE : (start + count) * HEADER_SIZE]


def resolve_bytes(case):
    if "data_hex" in case:
        value = case["data_hex"]
        return None if value is None else bytes.fromhex(value)
    if "data_text" in case:
        return case["data_text"].encode("utf-8")
    if "repeat_char" in case:
        return case["repeat_char"].encode("utf-8") * int(case["repeat_count"])
    raise ValueError("byte case requires data_hex, data_text, or repeat_char")


def run_header_case(name, height, serialized):
    header = deserialize_header(height, serialized)
    return {
        "name": name,
        "height": height,
        "serialized_hex": serialized.hex(),
        "deserialized": header,
        "round_trip_hex": serialize_header(header).hex(),
        "hash_hex": hash_header(serialized),
    }


def run_hash_case(case):
    data = resolve_bytes(case)
    return {
        "name": case.get("name", ""),
        "data_hex": None if data is None else data.hex(),
        "hash_hex": hash_header(data),
    }


def run_pow_case(case):
    data = resolve_bytes(case)
    if data is None:
        raise ValueError("proof-of-work input cannot be null")
    header_hash = hash_header(data)
    pow_hash = header_hash_to_pow_hash(header_hash)
    return {
        "name": case.get("name", ""),
        "data_hex": data.hex(),
        "header_hash_hex": header_hash,
        "pow_hash_hex": pow_hash,
        "pow_value": str(int(pow_hash, 16)),
    }


def run_compact_case(case):
    compact = case.get("compact")
    input_value = case.get("value")
    if compact is not None:
        number = ArithUint256.from_compact(int(compact))
    elif input_value is not None:
        number = ArithUint256(parse_integer(input_value))
    else:
        raise ValueError("compact case requires compact or value")
    result = {
        "name": case.get("name", ""),
        "input_compact": compact,
        "input_value": input_value,
        "value": str(number.value),
        "bits": number.bits,
        "compact": number.compact,
        "negative": number.negative,
    }
    if "multiplier" in case:
        result["multiplier"] = int(case["multiplier"])
        result["multiplied_value"] = str(
            number.multiply(result["multiplier"]).value
        )
    if "divisor" in case:
        result["divisor"] = int(case["divisor"])
        result["divided_value"] = str(number.divide(result["divisor"]).value)
    return result


def run_target_case(case):
    maximum = ArithUint256(parse_integer("0x" + case["max_target_hex"]))
    target = next_block_target(
        maximum,
        int(case.get("target_timespan", TARGET_TIMESPAN)),
        case.get("previous"),
        case.get("current"),
    )
    return {
        "name": case.get("name", ""),
        "max_target_hex": case["max_target_hex"],
        "target_timespan": int(case.get("target_timespan", TARGET_TIMESPAN)),
        "previous": case.get("previous"),
        "current": case.get("current"),
        "value": str(target.value),
        "compact": target.compact,
    }


def run_chain_case(fixture, case):
    initial = fixture_slice(fixture, case, "initial_")
    supplied = bytearray(fixture_slice(fixture, case))
    for mutation in case.get("mutations", []):
        offset = int(mutation["offset"])
        if not 0 <= offset < len(supplied):
            raise IndexError(f"mutation offset {offset} is outside chain input")
        if "xor" in mutation:
            supplied[offset] ^= int(mutation["xor"])
        else:
            supplied[offset] = int(mutation["value"])
    chain = HeaderChain(initial, bool(case.get("validate_difficulty", True)))
    added = chain.connect(int(case.get("start", 0)), bytes(supplied))
    tip_hash = hash_header(chain.read(chain.height)) if chain.height >= 0 else None
    return {
        "name": case.get("name", ""),
        "initial_hex": initial.hex(),
        "input_hex": bytes(supplied).hex(),
        "start": int(case.get("start", 0)),
        "validate_difficulty": chain.validate_difficulty,
        "added": added,
        "height": chain.height,
        "serialized_hex": bytes(chain.data).hex(),
        "tip_hash_hex": tip_hash,
    }


def run_adapter_self_checks(fixture):
    first = fixture[:HEADER_SIZE]
    first_header = deserialize_header(0, first)
    targets = []
    for elapsed, bits in ((0, 0x1F00FFFF), (0, 0x1F00A000), (1200, 0x1F00A000), (600, 0x1F00FFFF)):
        targets.append(
            next_block_target(
                ArithUint256(MAX_TARGET),
                TARGET_TIMESPAN,
                {"timestamp": 1386475638},
                {"timestamp": 1386475638 + elapsed, "bits": bits},
            ).compact
        )
    valid_chain = HeaderChain(b"")
    valid_added = valid_chain.connect(0, fixture[: 3 * HEADER_SIZE])
    invalid = bytearray(fixture[: 3 * HEADER_SIZE])
    invalid[2 * HEADER_SIZE + 4] ^= 1
    invalid_chain = HeaderChain(b"")
    invalid_added = invalid_chain.connect(0, bytes(invalid))
    return {
        "fixture_is_20_headers": len(fixture) == 20 * HEADER_SIZE,
        "fixture_genesis_hash": hash_header(first) == GENESIS_HASH,
        "fixture_genesis_fields": first_header
        == {
            "version": 1,
            "prev_block_hash": "0" * 64,
            "merkle_root": "b8211c82c3d15bcd78bba57005b86fed515149a53a425eb592c07af99fe559cc",
            "claim_trie_root": "0" * 63 + "1",
            "timestamp": 1446058291,
            "bits": 520159231,
            "nonce": 1287,
            "block_height": 0,
        },
        "pow_vectors": [
            header_hash_to_pow_hash(hash_header(value))
            for value in (b"test string", b"a" * 70, b"d" * 140)
        ]
        == [
            "485f3920d48a0448034b0852d1489cfa475341176838c7d36896765221be35ce",
            "eb44af2f41e7c6522fb8be4773661be5baa430b8b2c3a670247e9ab060608b75",
            "74044747b7c1ff867eb09a84d026b02d8dc539fb6adcec3536f3dfa9266495d9",
        ],
        "compact_vectors": (
            ArithUint256(0x80).compact == 0x02008000
            and ArithUint256.from_compact(0x01FEDCBA).value == 0x7E
            and ArithUint256.from_compact(0x01FEDCBA).negative == 0x01FE0000
            and ArithUint256.from_compact(0x20123456).compact == 0x20123456
        ),
        "target_vectors": targets
        == [0x1F00E146, 0x1F008CCC, 0x1F00F000, 0x1F00FFFF],
        "valid_short_chain": valid_added == 3 and valid_chain.height == 2,
        "invalid_chunk_is_rejected": invalid_added == 0 and invalid_chain.height == -1,
    }


def run(sdk_root, payload):
    if not __debug__:
        raise RuntimeError("header oracle requires Python assertions (__debug__)")
    if not isinstance(payload, dict):
        raise TypeError("oracle input must be an object")
    commit, version = verify_pinned_sources(sdk_root)
    fixture = load_header_fixture(sdk_root)
    checkpoints = load_checkpoint_metadata(sdk_root)
    self_checks = run_adapter_self_checks(fixture)
    if not all(self_checks.values()):
        raise RuntimeError(f"header oracle adapter self-check failed: {self_checks}")
    header_cases = []
    for index in payload.get("fixture_header_indices", []):
        index = int(index)
        serialized = fixture[index * HEADER_SIZE : (index + 1) * HEADER_SIZE]
        if len(serialized) != HEADER_SIZE:
            raise IndexError(f"fixture header {index} is out of bounds")
        header_cases.append(run_header_case(f"fixture {index}", index, serialized))
    for case in payload.get("header_cases", []):
        header_cases.append(
            run_header_case(
                case.get("name", ""),
                int(case.get("height", 0)),
                bytes.fromhex(case["serialized_hex"]),
            )
        )
    return {
        "reference": {
            "commit": commit,
            "version": version,
            "source_sha256": PINNED_SOURCE_HASHES,
        },
        "metadata": {
            "header_size": HEADER_SIZE,
            "fixture_headers": len(fixture) // HEADER_SIZE,
            "stdlib_only": True,
            "python_assertions": __debug__,
            "adapter_self_checks": self_checks,
            "checkpoints": checkpoints,
        },
        "header_cases": header_cases,
        "hash_cases": [run_hash_case(case) for case in payload.get("hash_cases", [])],
        "pow_cases": [run_pow_case(case) for case in payload.get("pow_cases", [])],
        "compact_cases": [
            run_compact_case(case) for case in payload.get("compact_cases", [])
        ],
        "target_cases": [
            run_target_case(case) for case in payload.get("target_cases", [])
        ],
        "chain_cases": [
            run_chain_case(fixture, case) for case in payload.get("chain_cases", [])
        ],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    arguments = parser.parse_args()
    result = run(arguments.sdk_root.resolve(), json.load(sys.stdin))
    json.dump(result, sys.stdout, sort_keys=True, ensure_ascii=True, separators=(",", ":"))
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

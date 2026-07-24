#!/usr/bin/env python3
"""Execute the pinned Electrum-style mnemonic implementation in isolation."""

import argparse
import ast
from binascii import hexlify
import hashlib
import hmac
import importlib
import json
import math
from pathlib import Path
import string
import sys
from types import SimpleNamespace
import unicodedata


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/crypto/hash.py": "bfc430bd3fe98578b406caa3a8e2116a40f492c7b68e269176e838b4ef426a72",
    "lbry/wallet/mnemonic.py": "6d731208e9274f397ed15eb445ce0024f6ad9adcd8a1a40cd5ed08b7d41fc2bc",
    "lbry/wallet/words/english.py": "ec702cc5b02ea7bc749f742a70b56d55c26bf8ab6e0a0ce10429266051968dd3",
}


class PBKDF2Result:
    def __init__(self, password, salt, iterations, macmodule=None, digestmodule=None):
        del macmodule
        digest = digestmodule().name
        self._password = password.encode("utf-8") if isinstance(password, str) else password
        self._salt = salt.encode("utf-8") if isinstance(salt, str) else salt
        self._iterations = iterations
        self._digest = digest

    def read(self, length):
        return hashlib.pbkdf2_hmac(
            self._digest, self._password, self._salt, self._iterations, dklen=length
        )


class FixtureRandom:
    def __init__(self):
        self.values = []
        self.calls = []

    def reset(self, values):
        self.values = list(values)
        self.calls = []

    def randbelow(self, limit):
        self.calls.append(limit)
        if not self.values:
            raise RuntimeError("mnemonic oracle exhausted deterministic entropy")
        value = self.values.pop(0)
        if not 0 <= value < limit:
            raise ValueError(f"fixture entropy {value} is outside [0, {limit})")
        return value


def verify_pinned_sources(sdk_root):
    for relative_path, expected in PINNED_SOURCE_HASHES.items():
        source_path = sdk_root / relative_path
        actual = hashlib.sha256(source_path.read_bytes()).hexdigest()
        if actual != expected:
            raise RuntimeError(
                f"{relative_path} does not match pinned commit {PINNED_COMMIT}: "
                f"sha256 is {actual}, expected {expected}"
            )


def read_sdk_version(sdk_root):
    source_path = sdk_root / "lbry" / "__init__.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    for node in tree.body:
        if not isinstance(node, ast.Assign):
            continue
        if any(isinstance(target, ast.Name) and target.id == "__version__" for target in node.targets):
            return ast.literal_eval(node.value)
    raise RuntimeError("could not read SDK version")


def read_english_words(sdk_root):
    source_path = sdk_root / "lbry" / "wallet" / "words" / "english.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    assignment = next(
        node for node in tree.body
        if isinstance(node, ast.Assign)
        and any(isinstance(target, ast.Name) and target.id == "words" for target in node.targets)
    )
    words = ast.literal_eval(assignment.value)
    if len(words) != 2048 or len(set(words)) != len(words):
        raise RuntimeError("pinned English mnemonic word list changed")
    return words


def load_contract(sdk_root):
    source_path = sdk_root / "lbry" / "wallet" / "mnemonic.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))
    wanted_assignments = {"SEED_PREFIX", "SEED_PREFIX_2FA", "SEED_PREFIX_SW", "CJK_INTERVALS", "LANGUAGE_NAMES"}
    wanted_functions = {"is_cjk", "normalize_text", "load_words", "is_new_seed"}
    selected = []
    for node in tree.body:
        if isinstance(node, ast.Assign) and any(
            isinstance(target, ast.Name) and target.id in wanted_assignments
            for target in node.targets
        ):
            selected.append(node)
        elif isinstance(node, ast.FunctionDef) and node.name in wanted_functions:
            selected.append(node)
        elif isinstance(node, ast.ClassDef) and node.name == "Mnemonic":
            selected.append(node)

    random = FixtureRandom()
    namespace = {
        "english": SimpleNamespace(words=read_english_words(sdk_root)),
        "hashlib": hashlib,
        "hexlify": hexlify,
        "hmac": hmac,
        "hmac_sha512": lambda key, message: hmac.new(key, message, hashlib.sha512).digest(),
        "importlib": importlib,
        "math": math,
        "pbkdf2": SimpleNamespace(PBKDF2=PBKDF2Result),
        "randbelow": random.randbelow,
        "string": string,
        "unicodedata": unicodedata,
    }
    module = ast.fix_missing_locations(ast.Module(body=selected, type_ignores=[]))
    exec(compile(module, str(source_path), "exec"), namespace)  # pylint: disable=exec-used
    return namespace, random


def capture(function):
    try:
        return function(), None, None
    except Exception as error:  # pylint: disable=broad-except
        return None, type(error).__name__, str(error)


def outcome(case, function):
    result, error_type, error = capture(function)
    return {
        "name": case.get("name", ""),
        "result": result,
        "error_type": error_type,
        "error": error,
    }


def run(sdk_root, payload):
    verify_pinned_sources(sdk_root)
    version = read_sdk_version(sdk_root)
    if version != PINNED_VERSION:
        raise RuntimeError(f"SDK version is {version}, expected {PINNED_VERSION}")
    contract, random = load_contract(sdk_root)
    mnemonic_type = contract["Mnemonic"]

    normalize_cases = [
        outcome(case, lambda case=case: contract["normalize_text"](case["text"]))
        for case in payload.get("normalize_cases", [])
    ]
    seed_cases = [
        outcome(
            case,
            lambda case=case: mnemonic_type.mnemonic_to_seed(
                case["mnemonic"], case.get("passphrase", "")
            ).hex(),
        )
        for case in payload.get("seed_cases", [])
    ]
    encode_cases = [
        outcome(
            case,
            lambda case=case: mnemonic_type(case.get("language", "en")).mnemonic_encode(
                int(case["integer"])
            ),
        )
        for case in payload.get("encode_cases", [])
    ]
    decode_cases = [
        outcome(
            case,
            lambda case=case: str(
                mnemonic_type(case.get("language", "en")).mnemonic_decode(case["seed"])
            ),
        )
        for case in payload.get("decode_cases", [])
    ]
    version_cases = [
        outcome(
            case,
            lambda case=case: contract["is_new_seed"](
                case["seed"], case.get("prefix", "01").encode("ascii")
            ),
        )
        for case in payload.get("version_cases", [])
    ]

    make_cases = []
    for case in payload.get("make_cases", []):
        random.reset([int(value) for value in case["entropy"]])
        result, error_type, error = capture(lambda case=case: mnemonic_type(
            case.get("language", "en")
        ).make_seed(
            prefix=case.get("prefix", "01").encode("ascii"),
            num_bits=case.get("num_bits", 132),
        ))
        make_cases.append({
            "name": case.get("name", ""),
            "result": result,
            "error_type": error_type,
            "error": error,
            "randbelow_limits": [str(limit) for limit in random.calls],
        })

    language_cases = []
    for case in payload.get("language_cases", []):
        result, error_type, error = capture(lambda case=case: mnemonic_type(case["language"]))
        language_cases.append({
            "name": case.get("name", ""),
            "word_count": len(result.words) if result is not None else None,
            "first_word": result.words[0] if result is not None else None,
            "last_word": result.words[-1] if result is not None else None,
            "error_type": error_type,
            "error": error,
        })

    english_words = read_english_words(sdk_root)
    return {
        "reference": {"commit": PINNED_COMMIT, "version": version},
        "metadata": {
            "python_version": f"{sys.version_info.major}.{sys.version_info.minor}",
            "unicode_version": unicodedata.unidata_version,
            "seed_prefix": contract["SEED_PREFIX"].decode("ascii"),
            "seed_prefix_2fa": contract["SEED_PREFIX_2FA"].decode("ascii"),
            "seed_prefix_sw": contract["SEED_PREFIX_SW"].decode("ascii"),
            "english_word_count": len(english_words),
            "english_words_sha256": hashlib.sha256("\n".join(english_words).encode()).hexdigest(),
        },
        "normalize_cases": normalize_cases,
        "seed_cases": seed_cases,
        "encode_cases": encode_cases,
        "decode_cases": decode_cases,
        "version_cases": version_cases,
        "make_cases": make_cases,
        "language_cases": language_cases,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    arguments = parser.parse_args()
    result = run(arguments.sdk_root.resolve(), json.load(sys.stdin))
    json.dump(result, sys.stdout, sort_keys=True, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

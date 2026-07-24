#!/usr/bin/env python3
"""Source-pinned, stdlib-only model of the legacy wallet SQLite lifecycle.

The pinned SDK cannot be imported in a current Python environment without its
historical dependency graph.  This adapter instead AST-extracts the schema
strings and version constants from ``lbry/wallet/database.py``, then reproduces
``SQLiteMixin.open`` over temporary SQLite files.

Input is an optional JSON object with ``case_names``.  When omitted, all fixed
state-machine cases are evaluated.  No caller-provided or live database path is
accepted.
"""

import argparse
import ast
import hashlib
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
PINNED_VERSION = "0.113.0"
PINNED_SOURCE_HASHES = {
    "lbry/__init__.py": "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
    "lbry/wallet/constants.py": "099e5b3a18a70439b9d7039717f0cb61c096c5936126fe6574a4ccda600a780f",
    "lbry/wallet/database.py": "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
    "tests/unit/wallet/test_database.py": "7af85de707b329d8715cd22419a4f761b10792a3ecc023202389dd86e3011c51",
}

CASE_NAMES = (
    "fresh 1.6",
    "reopen 1.6 no repair",
    "1.5 migration",
    "unknown version reset",
    "nonempty missing version reset",
    "duplicate version first row",
)


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


def evaluate_constant(node, values):
    if isinstance(node, ast.Constant) and isinstance(node.value, (str, int)):
        return node.value
    if isinstance(node, ast.Name) and node.id in values:
        return values[node.id]
    if isinstance(node, ast.BinOp) and isinstance(node.op, ast.Add):
        return evaluate_constant(node.left, values) + evaluate_constant(
            node.right, values
        )
    raise ValueError("unsupported database contract expression")


def extract_class_constants(tree, class_name):
    for node in tree.body:
        if not isinstance(node, ast.ClassDef) or node.name != class_name:
            continue
        values = {}
        for statement in node.body:
            if not isinstance(statement, (ast.Assign, ast.AnnAssign)):
                continue
            if isinstance(statement, ast.Assign):
                if len(statement.targets) != 1 or not isinstance(
                    statement.targets[0], ast.Name
                ):
                    continue
                name = statement.targets[0].id
                value_node = statement.value
            else:
                if not isinstance(statement.target, ast.Name) or statement.value is None:
                    continue
                name = statement.target.id
                value_node = statement.value
            try:
                values[name] = evaluate_constant(value_node, values)
            except ValueError:
                continue
        return values
    raise RuntimeError(f"could not find {class_name} in database source")


def load_contract(sdk_root):
    path = sdk_root / "lbry" / "wallet" / "database.py"
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    mixin = extract_class_constants(tree, "SQLiteMixin")
    database = extract_class_constants(tree, "Database")
    required_mixin = ("CREATE_VERSION_TABLE",)
    required_database = ("SCHEMA_VERSION", "CREATE_TABLES_QUERY")
    if not all(name in mixin for name in required_mixin):
        raise RuntimeError("could not AST-extract the version table schema")
    if not all(name in database for name in required_database):
        raise RuntimeError("could not AST-extract the wallet database schema")
    constants_path = sdk_root / "lbry" / "wallet" / "constants.py"
    constants_tree = ast.parse(
        constants_path.read_text(encoding="utf-8"), filename=str(constants_path)
    )
    txo_types = None
    for node in constants_tree.body:
        if isinstance(node, ast.Assign) and any(
            isinstance(target, ast.Name) and target.id == "TXO_TYPES"
            for target in node.targets
        ):
            txo_types = ast.literal_eval(node.value)
            break
    if not isinstance(txo_types, dict) or txo_types.get("channel") != 2:
        raise RuntimeError("could not AST-extract TXO_TYPES")
    return {
        "schema_version": database["SCHEMA_VERSION"],
        "create_version_table": mixin["CREATE_VERSION_TABLE"],
        "create_tables": database["CREATE_TABLES_QUERY"],
        "channel_txo_type": txo_types["channel"],
    }


def table_names(connection):
    return [
        row[0]
        for row in connection.execute(
            "SELECT name FROM sqlite_master WHERE type='table';"
        ).fetchall()
    ]


def open_reference(path, contract):
    """Reproduce SQLiteMixin.open and return its writer connection."""
    connection = sqlite3.connect(path, isolation_level=None)
    schema_version = contract["schema_version"]
    tables = table_names(connection)
    if tables:
        if "version" in tables:
            version = connection.execute(
                "SELECT version FROM version LIMIT 1;"
            ).fetchone()
            if version == (schema_version,):
                return connection
            if version == ("1.5",) and schema_version == "1.6":
                connection.execute(
                    "ALTER TABLE txo ADD COLUMN has_source bool DEFAULT 1;"
                )
                connection.execute(
                    "UPDATE version SET version = ?", (schema_version,)
                )
                return connection
        connection.executescript(
            "\n".join(f"DROP TABLE {table};" for table in tables)
            + "\nPRAGMA WAL_CHECKPOINT(FULL);\nVACUUM;"
        )
    connection.execute(contract["create_version_table"])
    connection.execute(
        "INSERT INTO version VALUES (?)", (schema_version,)
    )
    connection.executescript(contract["create_tables"])
    return connection


def close_reference(connection):
    connection.execute("PRAGMA WAL_CHECKPOINT(FULL);")
    connection.close()


def remove_fresh_has_source(schema):
    marker = "            has_source bool,\n"
    if schema.count(marker) != 1:
        raise RuntimeError("could not derive the pinned 1.5 txo schema")
    return schema.replace(marker, "")


def setup_case(path, name, contract):
    if name == "fresh 1.6":
        return

    if name == "reopen 1.6 no repair":
        connection = open_reference(path, contract)
        connection.execute("DROP INDEX txi_address_idx")
        connection.execute("CREATE TABLE sentinel (value text)")
        connection.execute("INSERT INTO sentinel VALUES ('preserved')")
        close_reference(connection)
        return

    if name == "1.5 migration":
        connection = sqlite3.connect(path, isolation_level=None)
        connection.execute(contract["create_version_table"])
        connection.execute("INSERT INTO version VALUES ('1.5')")
        connection.executescript(remove_fresh_has_source(contract["create_tables"]))
        connection.execute(
            "INSERT INTO txo (txoid, position, amount, script, claim_name) "
            "VALUES ('legacy:0', 0, 5, X'00', 'legacy')"
        )
        connection.close()
        return

    if name == "unknown version reset":
        connection = open_reference(path, contract)
        connection.execute("UPDATE version SET version = '9.9'")
        connection.execute("CREATE TABLE sentinel (value text)")
        connection.execute("INSERT INTO sentinel VALUES ('destroyed')")
        close_reference(connection)
        return

    if name == "nonempty missing version reset":
        connection = sqlite3.connect(path, isolation_level=None)
        connection.execute("CREATE TABLE sentinel (value text)")
        connection.execute("INSERT INTO sentinel VALUES ('destroyed')")
        connection.close()
        return

    if name == "duplicate version first row":
        connection = open_reference(path, contract)
        connection.execute("INSERT INTO version VALUES ('9.9')")
        connection.execute("DROP INDEX txo_claim_name_idx")
        connection.execute("CREATE TABLE sentinel (value text)")
        connection.execute("INSERT INTO sentinel VALUES ('preserved')")
        close_reference(connection)
        return

    raise ValueError(f"unknown case name: {name}")


def quote_identifier(value):
    return '"' + value.replace('"', '""') + '"'


def inspect_columns(connection, table):
    rows = connection.execute(
        f"PRAGMA table_info({quote_identifier(table)})"
    ).fetchall()
    return [
        {
            "cid": row[0],
            "name": row[1],
            "type": row[2],
            "not_null": bool(row[3]),
            "default": row[4],
            "primary_key": row[5],
        }
        for row in rows
    ]


def inspect_indexes(connection):
    indexes = []
    rows = connection.execute(
        "SELECT name, tbl_name FROM sqlite_master "
        "WHERE type='index' AND sql IS NOT NULL ORDER BY name"
    ).fetchall()
    for name, table in rows:
        index_list = connection.execute(
            f"PRAGMA index_list({quote_identifier(table)})"
        ).fetchall()
        details = next(row for row in index_list if row[1] == name)
        columns = [
            row[2]
            for row in connection.execute(
                f"PRAGMA index_info({quote_identifier(name)})"
            ).fetchall()
        ]
        indexes.append(
            {
                "name": name,
                "table": table,
                "unique": bool(details[2]),
                "partial": bool(details[4]),
                "columns": columns,
            }
        )
    return indexes


def inspect_case(connection, name):
    tables = sorted(table_names(connection))
    table_schema = [
        {"name": table, "columns": inspect_columns(connection, table)}
        for table in tables
    ]
    version_rows = []
    if "version" in tables:
        version_rows = [
            row[0]
            for row in connection.execute(
                "SELECT version FROM version ORDER BY rowid"
            ).fetchall()
        ]
    sentinel_count = None
    if "sentinel" in tables:
        sentinel_count = connection.execute(
            "SELECT COUNT(*) FROM sentinel"
        ).fetchone()[0]
    legacy_has_source = None
    if "txo" in tables:
        legacy_row = connection.execute(
            "SELECT has_source FROM txo WHERE txoid = 'legacy:0'"
        ).fetchone()
        if legacy_row is not None:
            legacy_has_source = legacy_row[0]
    return {
        "name": name,
        "journal_mode": connection.execute("PRAGMA journal_mode").fetchone()[0],
        "version_rows": version_rows,
        "tables": table_schema,
        "indexes": inspect_indexes(connection),
        "sentinel_count": sentinel_count,
        "legacy_has_source": legacy_has_source,
    }


def run_case(name, contract):
    with tempfile.TemporaryDirectory(prefix="lbry-wallet-db-oracle-") as directory:
        path = Path(directory) / "blockchain.db"
        setup_case(path, name, contract)
        connection = open_reference(path, contract)
        try:
            return inspect_case(connection, name)
        finally:
            close_reference(connection)


METHOD_KEYS = (
    {
        "address": "bAddress1",
        "pubkey": bytes.fromhex("02" + "11" * 32),
        "chain_code": bytes.fromhex("22" * 32),
        "n": 3,
        "depth": 5,
    },
    {
        "address": "bAddress2",
        "pubkey": bytes.fromhex("03" + "33" * 32),
        "chain_code": bytes.fromhex("44" * 32),
        "n": 4,
        "depth": 5,
    },
    {
        "address": "bAddress1",
        "pubkey": bytes.fromhex("03" + "55" * 32),
        "chain_code": bytes.fromhex("66" * 32),
        "n": 99,
        "depth": 99,
    },
)


def add_keys_reference(connection, account, chain, keys):
    connection.executemany(
        "insert or ignore into account_address "
        "(account, address, chain, pubkey, chain_code, n, depth) values "
        "(?, ?, ?, ?, ?, ?, ?)",
        (
            (
                account,
                key["address"],
                chain,
                key["pubkey"],
                key["chain_code"],
                key["n"],
                key["depth"],
            )
            for key in keys
        ),
    )
    connection.executemany(
        "insert or ignore into pubkey_address (address) values (?)",
        ((key["address"],) for key in keys),
    )


def set_address_history_reference(connection, address, history):
    connection.execute(
        "UPDATE pubkey_address SET history = ?, used_times = ? WHERE address = ?",
        (history, history.count(":") // 2, address),
    )


def seed_channel_rows(connection, channel_txo_type):
    add_keys_reference(
        connection,
        "account-2",
        0,
        ({
            "address": "bOtherAddress",
            "pubkey": bytes.fromhex("02" + "77" * 32),
            "chain_code": bytes.fromhex("88" * 32),
            "n": 0,
            "depth": 1,
        },),
    )
    rows = (
        ("invalid", 40, "bAddress1", b"invalid", channel_txo_type),
        ("different", 30, "bAddress2", b"different", channel_txo_type),
        ("matching", 20, "bAddress1", b"matching", channel_txo_type),
        ("wrong-type", 50, "bAddress1", b"matching", 1),
        ("other-account", 60, "bOtherAddress", b"matching", channel_txo_type),
    )
    for position, (txid, height, address, script, txo_type) in enumerate(rows):
        connection.execute(
            "INSERT INTO tx (txid, raw, height, position) VALUES (?, X'00', ?, ?)",
            (txid, height, position),
        )
        connection.execute(
            "INSERT INTO txo "
            "(txid, txoid, address, position, amount, script, txo_type) "
            "VALUES (?, ?, ?, 0, 1, ?, ?)",
            (txid, txid + ":0", address, script, txo_type),
        )


def fake_decode_channel_public_key(script):
    if script == b"different":
        return bytes.fromhex("03" + "bb" * 32)
    if script == b"matching":
        return bytes.fromhex("02" + "aa" * 32)
    return None


def is_channel_key_used_reference(
    connection, account, candidate, channel_txo_type
):
    scripts = connection.execute(
        "SELECT txo.script FROM txo JOIN tx ON (tx.txid=txo.txid) "
        "WHERE txo.txo_type = ? AND txo.address IN "
        "(SELECT address FROM account_address WHERE account = ?) "
        "ORDER BY tx.height in (0, -1) DESC, tx.height DESC, "
        "tx.position DESC, txo.position",
        (channel_txo_type, account),
    ).fetchall()
    for (script,) in scripts:
        decoded = fake_decode_channel_public_key(script)
        if decoded is not None and decoded == candidate:
            return True
    return False


def run_method_case(contract):
    with tempfile.TemporaryDirectory(prefix="lbry-wallet-db-methods-") as directory:
        path = Path(directory) / "blockchain.db"
        connection = open_reference(path, contract)
        try:
            add_keys_reference(connection, "account-1", 0, METHOD_KEYS)
            set_address_history_reference(
                connection, "bAddress1", "a:1:b:2:c:3:"
            )
            set_address_history_reference(connection, "missing", "z:9:")
            seed_channel_rows(connection, contract["channel_txo_type"])
            address_rows = [
                {
                    "account": row[0],
                    "address": row[1],
                    "chain": row[2],
                    "pubkey_hex": row[3].hex(),
                    "chain_code_hex": row[4].hex(),
                    "n": row[5],
                    "depth": row[6],
                    "history": row[7],
                    "used_times": row[8],
                }
                for row in connection.execute(
                    "SELECT account_address.account, account_address.address, "
                    "account_address.chain, account_address.pubkey, "
                    "account_address.chain_code, account_address.n, "
                    "account_address.depth, pubkey_address.history, "
                    "pubkey_address.used_times FROM account_address "
                    "JOIN pubkey_address USING (address) "
                    "ORDER BY account_address.account, account_address.address"
                ).fetchall()
            ]
            channel_cases = []
            for name, account, candidate in (
                (
                    "owned matching key",
                    "account-1",
                    bytes.fromhex("02" + "aa" * 32),
                ),
                (
                    "owned unused key",
                    "account-1",
                    bytes.fromhex("02" + "cc" * 32),
                ),
                (
                    "other account does not count",
                    "account-missing",
                    bytes.fromhex("02" + "aa" * 32),
                ),
            ):
                channel_cases.append(
                    {
                        "name": name,
                        "account": account,
                        "candidate_hex": candidate.hex(),
                        "used": is_channel_key_used_reference(
                            connection,
                            account,
                            candidate,
                            contract["channel_txo_type"],
                        ),
                    }
                )
            return {
                "address_rows": address_rows,
                "channel_cases": channel_cases,
            }
        finally:
            close_reference(connection)


def assert_contract(cases, schema_version):
    by_name = {case["name"]: case for case in cases}
    expected_tables = [
        "account_address",
        "pubkey_address",
        "tx",
        "txi",
        "txo",
        "version",
    ]
    fresh = by_name["fresh 1.6"]
    assert schema_version == "1.6"
    assert [table["name"] for table in fresh["tables"]] == expected_tables
    assert fresh["journal_mode"] == "wal"
    assert fresh["version_rows"] == ["1.6"]

    reopened = by_name["reopen 1.6 no repair"]
    assert reopened["sentinel_count"] == 1
    assert not any(
        index["name"] == "txi_address_idx" for index in reopened["indexes"]
    )

    migrated = by_name["1.5 migration"]
    txo = next(table for table in migrated["tables"] if table["name"] == "txo")
    assert txo["columns"][-1]["name"] == "has_source"
    assert txo["columns"][-1]["default"] == "1"
    assert migrated["legacy_has_source"] == 1
    assert migrated["version_rows"] == ["1.6"]

    for name in ("unknown version reset", "nonempty missing version reset"):
        reset = by_name[name]
        assert [table["name"] for table in reset["tables"]] == expected_tables
        assert reset["sentinel_count"] is None
        assert reset["version_rows"] == ["1.6"]

    duplicate = by_name["duplicate version first row"]
    assert duplicate["version_rows"] == ["1.6", "9.9"]
    assert duplicate["sentinel_count"] == 1
    assert not any(
        index["name"] == "txo_claim_name_idx" for index in duplicate["indexes"]
    )


def run(sdk_root, payload):
    commit, version = verify_pinned_sources(sdk_root)
    contract = load_contract(sdk_root)
    selected = payload.get("case_names", list(CASE_NAMES))
    if not isinstance(selected, list) or any(name not in CASE_NAMES for name in selected):
        raise ValueError("case_names must contain only known wallet database cases")
    if len(set(selected)) != len(selected):
        raise ValueError("case_names must not contain duplicates")
    cases = [run_case(name, contract) for name in selected]
    if tuple(selected) == CASE_NAMES:
        assert_contract(cases, contract["schema_version"])
    methods = run_method_case(contract)
    assert len(methods["address_rows"]) == 3
    account_one = [
        row for row in methods["address_rows"] if row["account"] == "account-1"
    ]
    assert len(account_one) == 2
    assert account_one[0]["n"] == 3
    assert account_one[0]["used_times"] == 3
    assert [case["used"] for case in methods["channel_cases"]] == [True, False, False]
    return {
        "reference": {
            "commit": commit,
            "version": version,
            "source_sha256": PINNED_SOURCE_HASHES,
        },
        "metadata": {
            "schema_version": contract["schema_version"],
            "stdlib_only": True,
            "python_assertions": __debug__,
            "sqlite_version": sqlite3.sqlite_version,
        },
        "cases": cases,
        "method_case": methods,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    arguments = parser.parse_args()
    payload = json.load(sys.stdin)
    result = run(arguments.sdk_root.resolve(), payload)
    json.dump(result, sys.stdout, sort_keys=True, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

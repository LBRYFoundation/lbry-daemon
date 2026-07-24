#!/usr/bin/env python3
"""Execute pinned stream/file SQLite storage functions."""

import argparse
import ast
import binascii
from hashlib import sha256
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import time
from types import SimpleNamespace


PINNED_COMMIT = "e7666f489418e96b6d2104974e93915b539235c5"
SOURCE_SHA256 = "8c74e94de0cc1d2ee60ba7fe2f56b1f3515cc84fc6495cd748698180c23731a5"


def load_functions(sdk_root):
    path = sdk_root / "lbry" / "extras" / "daemon" / "storage.py"
    if sha256(path.read_bytes()).hexdigest() != SOURCE_SHA256:
        raise RuntimeError("storage.py does not match the pinned SDK")
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    wanted = {"store_stream", "store_file"}
    functions = [node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name in wanted]
    if {node.name for node in functions} != wanted:
        raise RuntimeError("managed stream storage functions changed")
    namespace = {"os": os, "time": time, "binascii": binascii}
    module = ast.fix_missing_locations(ast.Module(body=functions, type_ignores=[]))
    exec(compile(module, str(path), "exec"), namespace)  # pylint: disable=exec-used
    return namespace


def schema(connection):
    connection.executescript("""
        create table blob (blob_hash text primary key, blob_length integer, next_announce_time integer,
            should_announce integer, status text, last_announced_time integer, single_announce integer,
            added_on integer, is_mine integer);
        create table stream (stream_hash text primary key, sd_hash text, stream_key text,
            stream_name text, suggested_filename text);
        create table stream_blob (stream_hash text, blob_hash text, position integer, iv text,
            primary key (stream_hash, blob_hash));
        create table file (stream_hash text, bt_infohash text, file_name text, download_directory text,
            blob_data_rate real, status text, saved_file integer, content_fee text, added_on integer);
    """)


def rows(connection, table):
    return [list(row) for row in connection.execute(f"select * from {table} order by rowid")]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", required=True, type=Path)
    args = parser.parse_args()
    commit = subprocess.run(
        ["git", "-C", str(args.sdk_root), "rev-parse", "HEAD"], check=True,
        stdout=subprocess.PIPE, text=True,
    ).stdout.strip()
    if commit != PINNED_COMMIT:
        raise RuntimeError(f"SDK commit is {commit}, expected {PINNED_COMMIT}")
    functions = load_functions(args.sdk_root)
    connection = sqlite3.connect(":memory:")
    schema(connection)
    blobs = [
        SimpleNamespace(blob_hash="blob-a", length=16, blob_num=0, iv="iv-a", added_on=100, is_mine=True),
        SimpleNamespace(blob_hash="blob-b", length=32, blob_num=1, iv="iv-b", added_on=100, is_mine=True),
        SimpleNamespace(blob_hash=None, length=0, blob_num=2, iv="iv-end", added_on=100, is_mine=True),
    ]
    descriptor = SimpleNamespace(
        stream_hash="stream", key="key", stream_name="source",
        suggested_file_name="movie.mp4", blobs=blobs,
    )
    sd_blob = SimpleNamespace(blob_hash="sd", length=321, added_on=100, is_mine=True)
    functions["store_stream"](connection, sd_blob, descriptor)
    functions["store_stream"](connection, sd_blob, descriptor)
    with tempfile.TemporaryDirectory() as directory:
        file_name = "movie.mp4"
        Path(directory, file_name).write_bytes(b"content")
        rowid = functions["store_file"](
            connection, "stream", file_name, directory, 0.25, "running", None, 101,
        )
        file_rows = rows(connection, "file")
        file_rows[0][3] = "<DOWNLOAD>"
    print(json.dumps({
        "reference": {"commit": commit, "source_sha256": SOURCE_SHA256},
        "rowid": rowid, "blob": rows(connection, "blob"),
        "stream": rows(connection, "stream"), "stream_blob": rows(connection, "stream_blob"),
        "file": file_rows,
    }, sort_keys=True))


if __name__ == "__main__":
    main()

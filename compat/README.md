# Python Compatibility Oracles

`settings_oracle.py` executes the settings descriptors from the pinned local
`lbry-sdk/lbry/conf.py` and AST-extracts the settings RPC methods, dispatcher,
parameter checker, and error type from the pinned daemon source. It avoids
importing the SDK's wallet, networking, media, and native dependency graph.

The Go differential runner is `rpc/TestSettingsMatchPinnedPythonOracle`:

```shell
go test ./rpc -run TestSettingsMatchPinnedPythonOracle -v
```

The runner expects `lbry-sdk` beside `lbry-daemon`, plus Python 3 and PyYAML. It
skips explicitly when one of those inputs is absent, so the standalone Go daemon
repository remains testable without cloning the Python reference.
Set `LBRY_SDK_PATH` to an absolute or daemon-root-relative checkout path when the
repositories are not siblings. CI checks out the exact pinned commit and sets
this variable, so all oracle suites are required there rather than skipped.

The oracle verifies SHA-256 hashes for every Python source file it executes, so a
different SDK checkout cannot silently become the compatibility reference.

The comparison covers sequential RPC results, normalized application errors,
legacy setting-name migration, and the parsed YAML file after every operation.
Runtime-specific temporary paths and traceback contents are normalized.

`cli_oracle.py` executes the same pinned configuration descriptors through an
argparse root/start parser matching the legacy scopes. Its Go runner is
`config/TestCommandLineMatchesPinnedPythonOracle`:

```shell
go test ./config -run TestCommandLineMatchesPinnedPythonOracle -v
```

It compares representative successful parses and usage errors, including root
versus start defaults, toggles, repeated lists, max-key-fee values, unknown
arguments, abbreviations, verbose logging selectors, and initial headers.

`client_cli_oracle.py` extracts the complete command/group manifest, docstrings,
deprecations, and normalization helpers from the pinned sources. It compares the
embedded Go manifest plus representative docopt parses in
`legacycli/TestParserMatchesPinnedPythonClientCLIOracle`. This oracle additionally
requires Python `docopt==0.6.2`.

`component_graph_oracle.py` extracts all 14 registered components and their
dependencies from the pinned component source. The Go runner in
`componentgraph/TestLegacyGraphMatchesPinnedPythonASTOracle` compares normal,
reverse, skipped, unresolved, unknown-skip, and all-skipped startup stages.

`status_oracle.py` AST-extracts the real `jsonrpc_status` method plus the blob,
DHT, UPnP, and base component detail methods. It compares pre-start,
partial/skipped, fully running, all-skipped, and detailed component states in the
root daemon tests, including Python's bytes-to-string JSON encoding.

`database_revision_oracle.py` executes the revision preamble from the pinned
`DatabaseComponent.start` method with an inline test executor. The Go runner in
`database/TestRevisionLifecycleMatchesPinnedPythonOracle` compares exact paths,
revision 15, missing/current/older/newer/invalid files, Python integer syntax,
migration calls, errors, and file side effects. This boundary does not claim that
the SQLite schema or the revision 1-to-15 migration chain has been ported.

`wallet_storage_oracle.py` AST-extracts and executes the pinned `WalletStorage`
and `TimestampedPreferences` classes without importing the wallet, ledger, or
native dependency graph. The Go runner in
`wallet/TestWalletStorageAndPreferencesMatchPinnedPythonOracle` compares default
truthiness, version/key validation, the pinned upgrade bug, malformed inputs,
exact sorted/ASCII JSON bytes, float formatting, file modes, timestamp truncation,
insertion-sensitive hashes, and older/equal/newer preference merges. This package
is intentionally not wired to `WalletComponent` or wallet RPCs.

`mnemonic_oracle.py` AST-extracts and executes the pinned Electrum-style
`Mnemonic` implementation and hash helper without importing coincurve or the
wallet graph. `wallet/mnemonic/TestMnemonicMatchesPinnedPythonOracle` compares
Unicode normalization, direct-salt PBKDF2, little-endian word encoding, seed
version prefixes, deterministic entropy generation, word-list metadata, and
legacy language loading. The oracle reports its Python and Unicode versions;
CI requires Python 3.11 and Unicode 14.0 so runtime tables cannot drift silently.

`account_oracle.py` AST-extracts and executes the pinned account and address-manager
classes with source-pinned standalone BIP32, Base58, mnemonic, ledger, and AES
adapters. `wallet/TestAccountMatchesPinnedPythonOracle` compares exact ordered
JSON and hashes across seed/xprv/xpub precedence, both address generators,
arbitrary-size timestamps, certificate merges, partial merge failures, read-only
accounts, transient and persistent encryption, recovered/lazy IV caches, wrong
passwords, and the legacy missing-decrypt-finalize behavior:

```shell
go test ./wallet -run TestAccountMatchesPinnedPythonOracle -v
```

The account oracle records Python assertion mode and runtime Unicode metadata,
and source-hashes every modeled dependency. The runner currently requires error
presence parity but does not claim byte-identical Python exception classes/messages
for malformed dynamic account records.

`address_sync_oracle.py` AST-executes the pinned deterministic and single-address
managers plus `Ledger.get_local_status_and_history` over controlled database and
announcement probes. `wallet/TestAddressSyncHelpersMatchPinnedPythonOracle` pins
receiving/change defaults and order, tail-gap extension, inventory sorting and usable
filtering, the trailing-run max-gap quirk, rows retained after announcement failure,
single-address reuse, exact history parsing, and SHA-256 status values:

```shell
go test ./wallet -run TestAddressSyncHelpersMatchPinnedPythonOracle -v
```

The runner compares production status parsing, SPV constants, and account defaults;
gap planning, ordering, and max-gap edge cases use source-pinned pure models alongside
production database/lifecycle tests. Separate production tests now cover transaction-backed
history ingestion; this oracle still does not claim public event streams or full wallet
readiness.

`wallet_manager_oracle.py` executes the pinned `Wallet` and `WalletManager` class
bodies over the source-pinned account adapter and isolated ledger/config stubs.
`wallet/TestWalletAndManagerMatchPinnedPythonOracle` compares wallet lookup and
registration order, exact compact/disk JSON, hashes, password state, sequential
save/lock/unlock behavior, preserved file modes, ordered ledger construction,
manager path mapping, empty-default generation, and locked preference repair:

```shell
go test ./wallet -run TestWalletAndManagerMatchPinnedPythonOracle -v
```

Runtime error messages are normalized, but translated Go sentinels are mapped to
and compared with their pinned Python exception classes. The adapter deliberately
does not claim ledger startup, live config mutation, legacy address migration,
or channel-key cache priming.

`wallet_sync_oracle.py` executes the pinned wallet pack, unpack, and merge methods
over the same source-pinned account/manager adapter. Its standalone scrypt and
OpenSSL AES-CBC bridge reproduces the pinned `better_aes_*` framing without importing
the SDK's native dependency graph. `wallet/TestWalletSyncMatchesPinnedPythonOracle`
compares fixed primitive vectors, Python-pack to Go-unpack, Go-pack to Python-unpack,
Unicode and empty passwords, duplicate account ordering, lazy key precedence, and
preference/account mutations that survive later failures:

```shell
go test ./wallet -run TestWalletSyncMatchesPinnedPythonOracle -v
```

Complete packed ciphertext is not compared across compressors because Python zlib
and Go emit different valid streams. The test instead requires exact pre-compression
JSON and primitive AES framing, then compares the fully unpacked values in both
directions. This isolated oracle does not claim RPC exposure or a resource policy for
the legacy caller-controlled scrypt and unbounded decompression behavior.

`channel_keys_oracle.py` models the pinned SDK's raw compact ECDSA signatures,
Coincurve 15 PEM encoding, `asn1crypto 1.5.1` SEC1/PKCS#8 BER loading, imported
certificate mapping, migration/save timing, and deterministic `m/2/index` channel-key
manager. `wallet/TestChannelKeysMatchPinnedPythonOracle` compares cryptographic
vectors, high-S and malformed scalar handling, long signing buffers, permissive PEM
labels/Base64/BER/attributes, ordered account side effects, usage-probe errors, cache
observation, and watch-only behavior:

```shell
go test ./wallet -run TestChannelKeysMatchPinnedPythonOracle -v
```

The adapter is standard-library-only and always source-checks the SDK commit and
Coincurve requirement. It records audited Coincurve and asn1crypto source hashes;
optional `COINCURVE15_SOURCE_PATH` and `ASN1CRYPTO151_WHEEL_PATH` inputs verify those
third-party sources without making them normal test dependencies. This slice does not
claim transaction/schema signing or RPC exposure. Ledger database usage and unlock-time
cache priming are covered by the separate wallet-database oracle below.

`headers_oracle.py` is a source-pinned, standard-library model of the raw 112-byte
header chain. It AST-extracts the 20-header SDK fixture and compares header codecs,
reversed double-SHA256, LBRY proof-of-work hashing, compact arithmetic quirks,
retargeting, and practical chunk acceptance in
`wallet/TestHeadersMatchPinnedPythonOracle`. It also AST-extracts and validates the
1,243-entry mainnet checkpoint table against the embedded 39,776-byte Go artifact in
`wallet/TestMainnetCheckpointsMatchPinnedPythonOracle`:

```shell
go test ./wallet -run 'Test(HeadersMatch|MainnetCheckpointsMatch)PinnedPythonOracle' -v
```

`wallet_database_oracle.py` AST-extracts the pinned wallet schema and reproduces
`SQLiteMixin.open` with Python's standard `sqlite3` module over temporary databases.
`wallet/TestWalletDatabaseMatchesPinnedPythonOracle` compares fresh and reopened
schema `1.6`, the non-atomic `1.5` migration, destructive reset cases, duplicate
version behavior, column/index manifests, address-key/history writes, and ordered
channel-key usage lookup:

```shell
go test ./wallet -run TestWalletDatabaseMatchesPinnedPythonOracle -v
```

Database files are compared through canonical SQLite introspection and observable rows,
not raw page bytes, because Python and Go intentionally use different SQLite engine
builds. Every fixture path is created below a test temporary directory; the oracle never
accepts a live wallet database path.

`wallet_lifecycle_oracle.py` AST-extracts the pinned manager, ledger, component
manager, and wallet-component lifecycle methods over stdlib-only probes. The runner
in `wallet/TestLifecycleMatchesPinnedPythonOracle` pins manager running-flag timing,
concurrent child entry, swallowed `asyncio.wait` DB/header and component exceptions,
continued later stages, and manager retention on failed wallet-component start/stop.
The production daemon currently uses persistence plus a provisional SPV checkpoint
prefix; this oracle prevents that bounded integration from being mistaken for full
wallet readiness.

`header_fetch_oracle.py` source-pins the header, ledger, and network fetch path and
emits Python-generated raw-DEFLATE/Base64 checkpoint responses. The runner in
`wallet/TestCheckpointFetchMatchesPinnedPythonOracle` verifies exact matching writes,
permissive Base64, ignored trailing compressed data, mismatch text, missing-set updates,
noncheckpoint discard, and the `blockchain.block.headers` request contract. It also
locks down the intentional Go safety divergence: hash-matching output must be exactly
112,000 bytes and remains subject to encoded/compressed resource limits.

`spv_network_oracle.py` source-pins the legacy network, session, JSON-RPC, and newline
framer modules. The runner in `wallet/spv/TestSPVNetworkMatchesPinnedPythonOracle`
verifies request payload semantics, empty-parameter omission, process constants,
handshake order, protocol tuple comparison, typed response failures, fixed reconnect
timing, and the timeout/connection-only retry contract. It also records the deliberate
1 MiB Go frame cap against Python's 4 GiB setting and reliable Go shutdown cancellation.

`spv_selection_oracle.py` AST-loads the pinned UDP ping/pong classes, all 530 country
enum values, and `KnownHubsList` after verifying the UDP, network, configuration,
resolver, attribute, and generated-schema source hashes. The runner in
`wallet/spv/TestSPVSelectionMatchesPinnedPythonOracle` compares exact packets,
availability flags, legacy country-name quirks, source precedence, probe/fallback
metadata, ordered first-insertion hub behavior, OR/match-none filtering, and partial
peer-discovery mutation.

`spv_tip_oracle.py` AST-executes the pinned `Ledger.update_headers` method with
controlled header, network, database, event, and cache probes. The runner in
`wallet/TestSPVTipSyncMatchesPinnedPythonOracle` compares initial 2001-header pulls,
direct and future notifications, gap recovery, reorganization walkback, emitted
height changes, cache clears, the database rewind argument, and exact failure text.
The source pins also cover the header store, stream dispatcher, database placeholder,
and unit/integration reorganization fixtures.

`transaction_oracle.py` AST-executes the pinned byte stream, script templates,
`Transaction._deserialize`, unsigned constructors, `Transaction.sign`, and the
`TXRefMutable` hash path without importing the SDK's native or protobuf dependency graph.
`wallet/TestTransactionsMatchPinnedPythonOracle` compares the production Go transaction
and output-script parsers against the genesis and claim fixtures, the pinned timelock and
multisig input scripts, a constructed SegWit transaction, all supported output templates,
noncanonical compact sizes and push encodings, trailing bytes, and truncated or invalid
inputs. `wallet/TestUnsignedTransactionConstructionPinnedPythonOracle` separately pins
the production-constructor contract: default/versioned transactions, generated
payment/return/claim/update/support outputs, placeholder spend inputs, chained parent
references and positions, sizes and sums, mutable raw/hash caches, and reset-driven
canonicalization. `wallet/TestTransactionSigningMatchesPinnedPythonOracle` invokes the
real extracted Python signing coroutine with controlled ledger/account/wallet stubs and a
stdlib-only secp256k1 adapter matching Coincurve's double-SHA256, RFC6979, low-S DER
contract. It compares per-input P2PKH and P2SH/timelock preimages and digests, exact
DER+SIGHASH signatures, public keys, final scripts/raw/IDs, reset timing, missing keys,
unsupported and unresolved outputs, and partial mutation when a later input fails. The
oracle separately source-pins `bip32.py` and AST-extracts the published unit signing
signature literal, which also serves as a direct adapter self-check:

`wallet/TestTransactionBalancingMatchesPinnedPythonOracle` invokes the real extracted
`Transaction.create` coroutine over controlled asynchronous ledger, wallet, account,
change-address, key-lookup, and release stubs. It compares estimator sizes and fees,
provided inputs and outputs, selector deficits and effective amounts, the strict dust
threshold, repeated no-output passes, the five-pass return, the legacy early exit with an
underfunded output transaction, optional signing, exact final bytes and IDs, callback
order/counts, and release snapshots. Failure fixtures pin the distinction between initial
fee/validation failures before the coroutine's `try` block and selector, change, and
signing failures inside it, including the rule that a release failure replaces the
original exception.

```shell
go test ./wallet -run TestTransactionsMatchPinnedPythonOracle -v
go test ./wallet -run TestUnsignedTransactionConstructionPinnedPythonOracle -v
go test ./wallet -run TestTransactionSigningMatchesPinnedPythonOracle -v
go test ./wallet -run TestTransactionBalancingMatchesPinnedPythonOracle -v
```

Python's native exception classes and messages for malformed byte streams are recorded,
while Go requires its typed transaction/script sentinels rather than reproducing Python
runtime errors. This boundary covers parsing, legacy transaction IDs, flattened witness
items, canonical reset serialization, every supported script generator, and production Go
construction compared directly with Python-generated fixtures. Separate production tests
cover persisted wallet/account address lookup, encrypted-account rejection, ordered extra
keys, and the SDK's exact mnemonic-derived `m/0/0` signing vector. These tests do not claim
transaction verification or broadcasting. Production tests separately cover the
ledger/account adapter, funding/change identity validation, standard and confirmed selector
families, SQLite confirmed-first band selection, atomic reservations, release behavior,
permissive change-address slicing, persisted change ownership, and account-backed create
signing. Signing coverage remains limited to legacy SIGHASH_ALL P2PKH and timelock/P2SH
inputs supported by the pinned method.

`transaction_metadata_oracle.py` imports the source-pinned `Claim.from_bytes` path with
controlled optional-dependency stubs. `wallet/TestTransactionMetadataLegacyOracle` compares
legacy signed-v1 initialization, fee currency/amount conversion, JSON decimal bounds, raw
Base58 validation, claim type, source presence, and signing channel IDs across 19 fixtures:

```shell
go test ./wallet -run TestTransactionMetadataLegacyOracle -v
```

Production transaction tests separately cover stable 100-ID SPV batches, fallback merkle
verification, ordered wire responses and duplicate keys, partial-response commits, input
resolution, metadata projection, deterministic channel-key observation, wallet ownership
filtering, atomic `tx`/`txo`/`txi` writes, reverse-order post-commit transaction handlers,
cross-batch commits, final address history, gap extension, and an in-memory live SPV client
session. These tests do not claim construction, signing, broadcasting, Python's complete
stream/wait API, or wallet RPC readiness.

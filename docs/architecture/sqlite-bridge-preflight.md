# SQLite bridge preflight

The bridge release requires a one-way SQLite-to-PostgreSQL import before SQLite
retirement at 1.0. `cmd/sqlite-bridge-preflight` implements only the source inventory
milestone. It does not migrate schemas, import rows, connect to PostgreSQL, select a
backend, or certify that switching backends is safe.

Commands assume the repository root is the working directory:

```sh
go run ./cmd/sqlite-bridge-preflight --manifest
go run ./cmd/sqlite-bridge-preflight --source "$OFFLINE_BACKUP/7.db" --account-id 7
```

The source must be an existing regular file named after the positive central
account ID. The filename check is necessary but does not prove that a file belongs
to this installation or account. That provenance must be verified against the
central account database and backup record before import.

Use a standalone offline SQLite backup captured after stopping all writers, or
through a verified SQLite backup mechanism. The command refuses nonempty WAL and
rollback journals instead of silently inspecting only the main file. It opens the
source with `mode=ro`, `immutable=1`, and query-only mode, so SQLite cannot create
shared-memory files or upgrade the database. Do not pass a live database: immutable
mode assumes the input is stable. Before/after metadata and sidecar checks detect
some changes but cannot prove snapshot consistency. A main-file hash would not
prove that committed WAL state was included, so this command issues no backup
fingerprint or consistency certificate.

JSON output contains the schema version, known table names, row and column counts,
and blockers. It excludes paths, profile names, IDs, values, PIN hashes, SQL error
messages, and unknown table names. Unknown tables are represented with a redacted
label and block import even when empty. Integrity errors stop inspection. Missing
tables, schema versions other than the current version 22, and nonempty legacy
session/download tables are blockers. Old sources are never upgraded in place.

The manifest covers all 29 current persistent application tables. Schema 21 remains
an old source: its missing revision tables and version block import, and inspection
does not create the tables or upgrade its version. The inventory does not run the account writer described below. Its manifest
records the following mapping requirements:

- Preserve account-scoped profile, collection and history IDs, visibility,
  restrictions, timestamps, ordering, history identities and hidden cutoffs.
- Preserve all six canonical settings scopes, null/false/empty distinctions,
  revisions, mutation replay records and migration rejects. Allocate destination
  integer surrogate IDs where required, without changing semantic identity.
- Group section overrides across profiles into PostgreSQL's legacy settings JSON
  representation without losing IDs, flags or timestamps.
- Explicitly classify nonempty legacy SQLite session/download rows. They cannot be
  copied into unrelated modern central tables.
- Map `personal_collection_revisions` to PostgreSQL `user_collection_revisions`
  with account identity. Retain revision tombstones for deleted collections;
  absence of a live collection is not permission to discard its witness. Map
  `personal_collection_order_revision` singleton 1 to `user_collection_order_revisions`
  by account ID, not profile or group. The account writer reconciles source
  witnesses with destination trigger increments and existing revisions before
  enabling validators, so old ETags cannot accidentally become valid again.
  Inventory does not implement that reconciliation.
- Define progress sync sequence handling before clients reconnect; SQLite's
  per-file sequence cannot be copied blindly into PostgreSQL's global sequence.

The inventory always reports `ready: false`. The command exits 2 after producing an
inventory, 1 on inspection/output failure, and 0 only for `--manifest`. `go run`
wraps the executable's exit status; scripts that need these exact codes should
build and invoke the binary. Table presence and counts are not completeness proof.
The inventory itself does not verify column mappings, local and central reference
validity, target collisions, semantic read equality, or all-account coverage.

## Supported-account transaction

`internal/userdb/bridgeimport.ImportAccount` implements an explicit account import
into PostgreSQL. It has no command, API route, provider selection, or automatic
startup caller. The caller must first establish backup provenance and consistency,
stop all account writers, retain the source backup, and supply the existing
installation identity, account ID, and verified source SHA-256. The importer checks
that installation identity against `diagnostics.server_instance_id`; a numeric
filename is not sufficient authorization or provenance.

The writer requires the exact supported version-22 table and column shape,
including type, nullability, and primary keys. Unknown or generated columns are
rejected. Both legacy session/download tables must be empty. It validates source
profile/collection references and central catalog references, refuses existing
mapped target account state, and imports all 27 mapped tables in one transaction.
It does not repair, prune, or partially import unsupported data.

Booleans must be 0 or 1. JSON destined for JSONB must be valid and have no duplicate
keys. PostgreSQL timestamp columns require lossless microsecond representation;
timestamps stored as text retain their original text precision. Text must be valid
UTF-8 without NUL. Values and rows are bounded to 16 MiB, and grouped section
settings are also bounded to 16 MiB. Unsupported representation aborts the whole
transaction. Legacy opaque settings and encrypted values retain their exact key
and value; successful copying does not certify that external encryption keys are
available. Canonical settings remain separate from legacy settings.

Rows are copied in bounded batches and verified by ordered semantic row counts
and digests against PostgreSQL's target representation. Generated surrogate IDs
and deliberately rebased sequence/revision values are excluded from that
projection. Collection revision witnesses, including deleted collection
tombstones, advance above both source and destination values. Section overrides
use the owning PostgreSQL settings representation, and a preexisting source
setting at the same derived key is a collision, not permission to overwrite it.

The required progress transition runs in the same PostgreSQL transaction as the
imported rows and receipt. `progresssync.RotateGeneration` supplies the durable
generation transition and invalidates old bootstrap snapshots. A source maximum
sequence does not bound cursors retained by clients after source rows were deleted.
The bridge therefore remains blocked until the full replacement bootstrap and
coordinated client reset protocol are active.

The durable receipt binds installation, account, source digest, schema and mapping
version, semantic verification, and progress generation. Equal receipt replay
returns the original result without rewriting rows or rotating the generation;
a different backup for that account is refused. Account deletion cascades its
receipt. A failed transaction rolls back rows, generation, and receipt together.
The source is opened read-only and its digest is checked again before commit.
Errors identify fixed phases or known schema fields without exposing source values.

Success is `account_imported` with `provider_switch: blocked`, never global
readiness. The read-only inventory still always reports `ready: false`. Before
activation, an owning operational workflow must verify every account, establish
client progress reset safety, verify encryption and backup recovery prerequisites,
and fence all nodes onto the same selected backend. Existing source files and
SQLite support remain through that transition.

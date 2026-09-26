# NEW initial setup sequences

This packet is separate from the frozen scenario catalog and existing NEW
acceptance counts. It does not modify an original oracle or use the household
fixture seeder.

`TestNewInitialSetupSequences` covers initial setup with an explicitly named
profile and with profile creation omitted, independently through v1 and v2. Each
sequence consumes its own virgin PostgreSQL database. It reads initial status,
creates the first administrator, uses the issued access token at the real v2
current-account route, rejects the refresh token as an access credential, refuses
a different subsequent setup candidate, and reads completed status.

Full row snapshots cover every public table around each request. Only initial
setup may create the exact administrator, optional primary profile and login
session. Password verification uses bcrypt; both returned JWT signatures, roles,
user/session bindings, token types and configured lifetimes are verified. Exact
stored fields and bounded timestamps are checked. All other tables, including
server settings and access-group configuration, must remain unchanged. Account
case is preserved while whitespace is trimmed. The profile flag is optional;
no default profile is created when it is omitted.

`TestNewInitialSetupCompetingBoundary` tests the first-administrator boundary with
two distinct public callers against the serialized admission that production setup
now takes: every setup transaction acquires the database-wide advisory lock
`auth.InitialSetupAdvisoryLock` before recounting accounts inside that same
transaction. The test holds that key at session level, observes both real
handlers parked as two ungranted advisory waiters with every table unchanged and
zero account, profile or session rows, then releases the key. The database then
serializes the two transactions. The required result is one creation and one
completed-setup refusal (401 `setup_complete` on v1, 409 on v2), exactly one
administrator and one login session, and no other table effect. The second
caller is not required to pass the emptiness check; it must be refused by the
recount under the lock. The original test-only BEFORE INSERT trigger barrier
(pre-correction evidence) is retired because only the winner now reaches INSERT.

Run with an exclusively owned disposable PostgreSQL instance with pgvector,
bound to an ephemeral loopback port. Record ownership before creating resources.
Create separate empty databases named with the `silo_worker_initial_` prefix for
`profile_v1`, `profile_v2`, `no_profile_v1`, `no_profile_v2`, `competing_v1` and
`competing_v2`. Supply their DSNs privately in the JSON environment variable
`SILO_INITIAL_SETUP_DATABASES`, keyed by those names. Never reuse a migrated DB
for a subsequent run; the constructor refuses any non-system relation before
migration, not just recognizable user data.

```sh
SILO_INITIAL_SETUP_REQUIRED=1 SILO_INITIAL_SETUP_OWNED=1 \
  go test -v -count=1 -run '^TestNewInitialSetupSequences$' \
  ./internal/scenariocatalog/executor

SILO_INITIAL_SETUP_REQUIRED=1 SILO_INITIAL_SETUP_OWNED=1 \
  go test -v -count=1 -run '^TestNewInitialSetupCompetingBoundary$' \
  ./internal/scenariocatalog/executor
```

`SILO_INITIAL_SETUP_EVIDENCE` optionally names an existing private directory for
per-snapshot table row counts and SHA256 digests. It contains no raw credentials.
Preserve initial failures, guard results and final evidence before removing only
the recorded owned resources. Virgin, initialized-database, occupied-sentinel and
preconnection ownership/loopback/port guards are separate from acceptance credit.

This packet does not prove configuration persistence across a real deployment,
process-crash recovery, setup/profile failure rollback, identity-provider
behavior, real enrollment or fleet safety. The competing-setup source defect
blocks complete acceptance of the NEW family. Production source, shared executor,
ledger and generated contracts remain outside this packet.

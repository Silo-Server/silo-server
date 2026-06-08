# Encrypt integration credentials at rest (issue #45) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Do not edit this plan file while implementing.

**Issue:** [#45 — Encrypt arr integration credentials at rest (Requests + Autoscan)](https://github.com/Silo-Server/silo-server/issues/45)

> **Revision note (post-review):** This plan was revised after a code review. Key corrections baked in:
> the arr backfill now **resolves the legacy `server_settings` reference before encrypting** (otherwise
> rows holding a setting-name like `requests.radarr.api_key` would be encrypted as the *name*, not the
> credential); the sensitive-settings allowlist is **audited from the config loader's real inputs**
> (incl. `redis.sentinel_password` and legacy `s3.operational_*` aliases) rather than copied from the
> admin redaction map; AAD binds to **row identity** (`table:column:pk`); read paths **pass through
> legacy plaintext** and hard-fail only on corrupt ciphertext; `settingsRepo` is **re-wrapped after the
> max-connections pool recreation**; history-import session tokens are **included**; and
> `plugin_runtime_configs.config_value` is an **explicit, documented gap** (see Out of scope).

**Goal:** Stop storing third-party integration credentials in plaintext at rest. Today Sonarr/Radarr
API keys, S3 keys, and a dozen other secrets sit in the database as naked `text` — `request_integrations.api_key_ref`
and `autoscan_connections.api_key_ref` are resolved through a plaintext `server_settings` key/value
store, and both resolvers contain a **ref-vs-literal fallback**: when the secret lookup returns empty,
the stored string is used verbatim as the API key. This plan introduces a real symmetric-encryption
layer (AES-256-GCM, `SECRET_KEY`-derived via HKDF), encrypts every recoverable server-owned credential
**inline** in its owning row, deletes the ambiguous resolver fallback, and backfills existing rows
automatically on startup.

**Decisions (locked with the requester):**
- **Master key:** a **required** `SECRET_KEY` environment variable. The server fatals at startup if it
  is missing. The key lives outside Postgres, so encrypted secrets survive a full DB compromise/dump.
- **Scope:** **all silo-server-owned credential storage** (was framed "full sweep"). Every recoverable
  server-owned secret — arr keys, S3 keys, all sensitive `server_settings`, and the per-table token
  columns — is covered. Two categories are deliberately *not* in this PR because they are a different
  *shape* of problem, not because they are unimportant: equality-looked-up secrets (need **hashing**,
  not encryption) and plugin runtime config JSONB (cross-repo + manifest-driven). Both are called out in
  Out of scope with follow-ups.
- **Data model (best practice):** **inline-encrypted**. Store the ciphertext directly in the owning
  column and decrypt on read. Drop the `server_settings` "ref" indirection and the `SecretResolver`
  literal fallback entirely — this kills the ambiguity at its root and matches the repo's one existing
  good example (`oauth_completion.token_ciphertext` lives inline in its own row).
- **Rollout:** idempotent Go startup **backfill** encrypts any plaintext (no-envelope-prefix) value in
  place on first boot after deploy. No manual steps.

**Architecture:** A new `internal/secret` package owns the one cryptographic primitive everything reuses:
AES-256-GCM with a random 12-byte nonce, `base64.RawURLEncoding`, and a versioned `enc:v1:` envelope
prefix; the data key is HKDF-SHA256–derived from `SECRET_KEY` with a domain label. Each ciphertext is
GCM-bound to its logical **row** via additional-authenticated-data (`"table:column:<pk>"`, or
`"server_settings:<key>"`) so a DB-write attacker cannot transplant a credential blob into another row
or column. Three wiring seams consume the cipher: (1) an `EncryptedSettingsRepo` **decorator** around
`catalog.ServerSettingsRepo` that transparently encrypts/decrypts the sensitive `server_settings` keys,
so `config.LoadFromDB` and every downstream consumer keep seeing plaintext; (2) the two `api_key_ref`
columns become inline ciphertext and the entire `SecretResolver` indirection is deleted from
`internal/requests` and `internal/autoscan`; (3) a handful of per-table repos gain a `*secret.Cipher`
and encrypt-on-write / decrypt-on-read. A startup backfill sweeps all groups idempotently — with a
**special resolve-then-encrypt path for the two arr columns** so legacy `server_settings` references are
collapsed to the real credential before encryption.

**Out of scope (each a deliberate, documented decision — not an oversight):**
- **Equality-looked-up secrets → need hashing, not encryption.** `api_keys.api_key`
  (`WHERE api_key = $1`) and `webhook_sync_connections.webhook_secret` (`WHERE webhook_secret = $1`,
  [repo.go:101](internal/webhooksync/repo.go:101)) are matched by exact value. AES-GCM uses a fresh
  random nonce, so the same plaintext encrypts differently each time — encrypting these would silently
  break API-key auth and inbound-webhook routing. The correct fix is a deterministic **hash** column +
  lookup-by-hash, a distinct lookup-semantics + migration change. **Follow-up issue.**
- **`plugin_runtime_configs.config_value` → cross-repo + manifest-driven.** This is opaque plugin-defined
  JSONB ([runtime_config.go:55](internal/plugins/runtime_config.go:55)); some plugins store secrets in it
  (e.g. the legacy one-time copy of `introdb.api_key` at [main.go:2341](cmd/silo/main.go:2341)). It is
  read/written only through `RuntimeConfigStore` *in this repo*, so whole-blob encrypt-on-write /
  decrypt-on-read at that store is technically feasible — **but** host-side plugin runtime lives in the
  separate **`Silo`** repo (per CLAUDE.md) and may read this table directly, and which fields are secret
  is declared in each plugin's manifest. Doing this correctly needs a coordinated, manifest-aware design
  across repos. **Explicitly deferred — known gap.** Note: while deferred, the legacy `introdb.api_key`
  copy means that secret still exists in plaintext inside plugin config even after the `server_settings`
  copy is encrypted; the follow-up must re-encrypt or purge it.
- **`plex_sync_connections`** — dead table, zero Go references (superseded by `webhook_sync_connections`
  in migration `060`). Not touched.
- **Already-protected / non-secret:** `users.password_hash` (bcrypt), `oauth_completion.token_ciphertext`
  (already AES-GCM), `abs_sessions.token_hash` / `oauth_completion.code_hash` (hashed), `oauth_session.state`
  (equality CSRF), `subtitle_provider_config.username` (a username, not a secret),
  `history_import_plex_sessions.pin_id`/`pin_code` (transient OAuth pairing handshake, not a stored
  access credential).
- `internal/auth/oauth_store.go` is left as-is (its key is jwt-derived and its data is ephemeral).
  Optionally a shared low-level seal/open helper can be extracted later; not required here.

**Tech Stack:** Go 1.26.3 (module declares `go 1.26.3`; stdlib `crypto/hkdf` is available — no new
dependency). PostgreSQL via `pgxpool`. **No schema migration** — every target column is already `text`
(or `jsonb` left untouched) and now simply holds ciphertext; the backfill is pure Go. No frontend or
client (silo-android / silo-apple) changes — API responses already mask these values to `has_api_key` /
`has_credentials` booleans, so no contract changes.

Commands assume the repository root is the cwd.

---

## Running tests

The `internal/secret` package is pure Go (no libvips/CGO). If a Go toolchain is on the host:

```bash
GOWORK=off go test ./internal/secret/... -v
```

`GOWORK=off` is required when running from a `.claude/worktrees/` checkout — the parent `go.work`
otherwise pins the module to `main` and the build fails with "main module does not contain package".

If the host has no Go toolchain, run in a throwaway container (a named volume caches module downloads):

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  go test ./internal/secret/... -v
```

Full backend build/vet before opening the MR: `GOWORK=off go build ./...` then `make lint` and
`make verify-local-paths`.

---

## File structure

**New files:**
- `internal/secret/cipher.go` — **create**. `Cipher` type; `New`, `Encrypt`, `Decrypt`, `IsEncrypted`,
  `EncryptIfPlaintext`; envelope + HKDF + version dispatch. Row-bound AAD passed by callers.
- `internal/secret/cipher_test.go` — **create**. Round-trip, tamper/AAD-mismatch rejection, wrong-key,
  `IsEncrypted` edge cases, version dispatch, idempotency.
- `internal/secret/backfill.go` — **create**. Generic in-place backfill for literal-credential columns +
  the special resolve-then-encrypt backfill for the two arr columns.
- `internal/secret/backfill_test.go` — **create**. Idempotency, mixed plaintext/ciphertext rows, empty
  values skipped, failure isolation, arr resolve-then-encrypt.

**Modified files:**
- `internal/config/bootstrap.go` — add `SecretKey []byte` to `BootstrapConfig`; read `SECRET_KEY`; fatal
  if missing or `< 32` chars.
- `cmd/silo/main.go` — construct `*secret.Cipher` after bootstrap; build the `EncryptedSettingsRepo`;
  run the settings + column backfills after migrations and before settings are read; **re-wrap
  `settingsRepo` after the max-connections pool recreation (line ~399)**; thread the cipher into the
  affected repo constructors; delete the `SetSecretResolver` wiring.
- `internal/catalog/encrypted_settings_repo.go` — **create**. `EncryptedSettingsRepo` decorator +
  exported, **audited** `SensitiveSettingKeys` (single source of truth) + `BackfillSensitiveSettings`.
- `internal/catalog/server_settings_repo.go` — unchanged behavior; remains the raw inner store.
- `internal/api/handlers/admin.go` — replace the local `sensitiveSettingKeys` map with
  `catalog.SensitiveSettingKeys` (redaction now shares the audited allowlist; closes the prior gap where
  `redis.sentinel_password` and `s3.operational_*` were returned in plaintext).
- `internal/requests/repository.go` — `Repository` gains `*secret.Cipher`; encrypt `api_key_ref` on
  insert/update (AAD bound to integration id); decrypt (pass-through legacy plaintext) in `scanIntegration`.
- `internal/requests/service.go` — delete `SecretResolver` interface, `secrets` field,
  `SetSecretResolver`, and `resolveAPIKey`; call sites use the already-decrypted `Integration.APIKeyRef`.
- `internal/autoscan/repository.go` — `Repository` gains `*secret.Cipher`; encrypt `api_key_ref` on
  create/update (AAD bound to connection id); decrypt (pass-through legacy) in `scanConnection`.
- `internal/autoscan/connection.go` — delete `SecretResolver` interface, `secrets` field, and the
  secret-resolution branch in `Resolve`; linked-integration path uses the requests repo's decrypted key.
- `internal/api/autoscan_wiring.go` — drop the `AutoscanSecretResolver` param from `BuildAutoscanService`.
- `internal/api/router.go` — drop the `SetSecretResolver(settingsRepo)` calls; pass the
  `EncryptedSettingsRepo` where `settingsRepo` is used; thread the cipher into repo constructors.
- `internal/subtitles/pgrepo.go` — `*secret.Cipher`; encrypt `api_key` + `password` on upsert; decrypt on
  read. PK for AAD = `provider_name`.
- `internal/watchsync/repository.go` — `*secret.Cipher`; encrypt `access_token` + `refresh_token` on
  upsert; decrypt in a `decryptConnection` helper called after every scan. PK for AAD = connection `id`.
- `internal/webhooksync/repo.go` — `*secret.Cipher`; encrypt `access_token` on create; decrypt on scan.
  **`webhook_secret` is NOT encrypted** (equality lookup — carved out). PK for AAD = connection `id`.
- `internal/historyimport/repo_admin.go` — `*secret.Cipher`; encrypt in `SetSourceAdminToken`; decrypt in
  `GetSourceWithAdminToken`. PK for AAD = source `id`.
- `internal/historyimport/repo.go` — `*secret.Cipher`; encrypt `connect_access_token`
  (`history_import_connect_sessions`) + `auth_token` (`history_import_plex_sessions`) on insert/update;
  decrypt on scan. PK for AAD = session `id`.
- `docs/architecture/secret-encryption.md` — **create**. Operator runbook (SECRET_KEY generation, backup,
  key-loss, rollback/downgrade).

**No migration files.** All target columns are already `text`; the backfill is Go-side.

---

## Reference tables

### What is encrypted vs hashed vs excluded

AAD column shows the GCM additional-authenticated-data string; `<pk>` is the row's stable primary key.

| Group | Table.column | AAD binding | Action |
| --- | --- | --- | --- |
| Arr keys | `request_integrations.api_key_ref` | `request_integrations:api_key_ref:<id>` | **Resolve-then-encrypt** (backfill) / encrypt inline (writes) |
| Arr keys | `autoscan_connections.api_key_ref` | `autoscan_connections:api_key_ref:<id>` | **Resolve-then-encrypt** (backfill) / encrypt inline (writes) |
| Settings | every key in `SensitiveSettingKeys` (see audited list below) | `server_settings:<key>` | **Encrypt via decorator** |
| Subtitles | `subtitle_provider_config.api_key`, `.password` | `subtitle_provider_config:<col>:<provider_name>` | **Encrypt inline** |
| Watch sync | `watch_provider_connections.access_token`, `.refresh_token` | `watch_provider_connections:<col>:<id>` | **Encrypt inline** |
| Webhook sync | `webhook_sync_connections.access_token` | `webhook_sync_connections:access_token:<id>` | **Encrypt inline** |
| History import | `history_import_sources.admin_token` | `history_import_sources:admin_token:<id>` | **Encrypt inline** |
| History import | `history_import_connect_sessions.connect_access_token` | `history_import_connect_sessions:connect_access_token:<id>` | **Encrypt inline** |
| History import | `history_import_plex_sessions.auth_token` | `history_import_plex_sessions:auth_token:<id>` | **Encrypt inline** |
| — | `api_keys.api_key` | — | **Carved out → hash** (follow-up) |
| — | `webhook_sync_connections.webhook_secret` | — | **Carved out → hash** (follow-up) |
| — | `plugin_runtime_configs.config_value` | — | **Deferred → cross-repo/manifest design** (follow-up) |
| — | `plex_sync_connections.*` | — | **Excluded** (dead table) |

### Audited `SensitiveSettingKeys` (single source of truth)

Built from the config loader's actual secret inputs ([internal/config/db_loader.go](internal/config/db_loader.go))
plus the admin redaction map plus other known consumers — **not** just the current redaction map. The
union is intentional and also fixes a pre-existing redaction leak. Implementers must re-audit
`db_loader.go` and `grep` for any `*secret*`/`*token*`/`*password*`/`*api_key*` settings keys before
finalizing.

```
auth.jwt_secret
s3.public_access_key   s3.public_secret_key   s3.public_token_secret
s3.private_access_key  s3.private_secret_key
s3.user_db_access_key  s3.user_db_secret_key
s3.operational_access_key  s3.operational_secret_key  s3.operational_token_secret   # legacy aliases still read as fallbacks (db_loader L165-192); migration 086 copied but did not delete them
redis.url                    # may embed credentials (redis://:pass@host)
redis.sentinel_password      # read at db_loader L334 — MISSING from the old redaction map
tmdb.api_key
mdblist.api_key
introdb.api_key              # read in main.go (legacy → plugin copy)
subtitle_ai.api_key
recommendations.embedding_auth_token   recommendations.openai_api_key   # legacy alias (db_loader L409)
watchsync.trakt.client_id   watchsync.trakt.client_secret
watchsync.simkl.client_id   watchsync.simkl.client_secret
requests.radarr.api_key     requests.sonarr.api_key                     # legacy single-instance arr keys
```

`auth.jwt_secret` IS encrypted at rest under `SECRET_KEY` (it is no longer the encryption root — `SECRET_KEY`
is). `redis.url`/client-id entries are not high-value secrets but are kept in the set as a harmless safe
superset and to match existing redaction.

### Envelope format

```
enc:v1:<base64.RawURLEncoding( nonce[12] ‖ gcm.Seal(...) )>
```

- `enc:v1:` — exactly 7 ASCII chars; not valid base64url, not a UUID/JWT/URL/integer prefix. Collision-proof
  against any real plaintext credential in practice.
- `IsEncrypted(s)` ≡ `strings.HasPrefix(s, "enc:v1:")`.
- Decrypt parses the version (`strings.SplitN(s, ":", 3)`), dispatches to the v1 opener; an unknown
  version returns an explicit `ErrUnknownVersion`. This is the forward-compat hook for a future `enc:v2:`
  key rotation — implement the dispatch now even though only `v1` exists.

### Read-path contract (applies to every decrypt site — decorator and repos)

1. `value == ""` → return `""`.
2. `!IsEncrypted(value)` → **return the value unchanged** (legacy plaintext pass-through during the
   backfill window; the value is no worse than today). This is unambiguous — there is no longer any
   "ref vs literal" question because the `_ref` indirection is gone.
3. `IsEncrypted(value)` → `Decrypt`; **any error (wrong key, tamper, truncation) is propagated, never
   swallowed and never returned as the ciphertext string.** A corrupt ciphertext fails the operation.

This reconciles the best-effort backfill (a skipped/failed row stays readable as plaintext) with the
hard guarantee that a real `enc:v1:` value never silently degrades to using ciphertext as a credential.

---

## Task 1: The `internal/secret` cipher package

**Files:** Create `internal/secret/cipher.go`, `internal/secret/cipher_test.go`.

This is the foundation; it has no dependencies on `catalog`/`config` and is independently testable.

- [ ] **Step 1: `Cipher` type + `New`.** Derive a 32-byte AES key with stdlib
  `hkdf.Key(sha256.New, masterKey, nil /*salt*/, "silo/data-encryption/v1", 32)`. Return an error if
  `len(masterKey) < 32`. Store the derived key as `[32]byte`. The type is safe for concurrent use.
- [ ] **Step 2: `Encrypt(plaintext, aad string) (string, error)`.** Empty plaintext returns `"", nil`
  (do not produce an `enc:v1:` blob for an empty secret). Otherwise: `aes.NewCipher` → `cipher.NewGCM` →
  12-byte `rand` nonce → `gcm.Seal(nonce, nonce, []byte(plaintext), []byte(aad))` →
  `envelopePrefix + base64.RawURLEncoding.EncodeToString(sealed)`. `aad` is the row-bound context string.
- [ ] **Step 3: `Decrypt(ciphertext, aad string) (string, error)`.** Strip + validate the version prefix;
  unknown version → `ErrUnknownVersion`. base64-decode; reject if shorter than the nonce size; split
  nonce/body; `gcm.Open(nil, nonce, body, []byte(aad))`. **Any failure returns an error — never fall back
  to returning the ciphertext or an empty string.** (Pass-through of *non-prefixed* plaintext is the
  caller's job per the read-path contract, not `Decrypt`'s.)
- [ ] **Step 4: `IsEncrypted(s string) bool`** and `EncryptIfPlaintext(s, aad string) (string, bool, error)`.
  `EncryptIfPlaintext` returns `(s, false, nil)` when already encrypted or empty; otherwise encrypts and
  returns `(ct, true, nil)`. This is the idempotent primitive the backfill uses.
- [ ] **Step 5: Tests** (`cipher_test.go`):
  - round-trip with AAD recovers plaintext; ciphertext has the `enc:v1:` prefix.
  - two encryptions of the same plaintext+AAD differ (random nonce works).
  - tamper: flip one byte of the decoded body → `Decrypt` errors.
  - AAD mismatch: encrypt with `"t:c:1"`, decrypt with `"t:c:2"` → errors (proves row binding).
  - wrong key: encrypt with key A, decrypt with key B → errors.
  - `New` with a `< 32`-byte key → error.
  - `IsEncrypted`: `"enc:v1:x"`→true, `"sa_abc"`→false, `""`→false, `"ENC:V1:x"`→false.
  - unknown version `"enc:v99:..."` → `ErrUnknownVersion`.
  - `EncryptIfPlaintext` is idempotent (second call is a no-op, `changed=false`).
- [ ] **Step 6:** `GOWORK=off go test ./internal/secret/... -v` passes.

---

## Task 2: `SECRET_KEY` bootstrap + cipher construction

**Files:** Modify `internal/config/bootstrap.go`, `cmd/silo/main.go`.

- [ ] **Step 1:** Add `SecretKey []byte` to `BootstrapConfig`. In `LoadBootstrap`, after the
  `DATABASE_URL` check, read `os.Getenv("SECRET_KEY")`; return an error (which the existing
  `log.Fatalf` path surfaces) if it is empty or `< 32` characters. Message must be actionable, e.g.
  `SECRET_KEY is required (>=32 chars); generate one with: openssl rand -base64 48`. `godotenv` already
  loads `.env`, so dev sets it there.
- [ ] **Step 2:** In `cmd/silo/main.go`, immediately after `LoadBootstrap` returns, construct the cipher:
  `dataCipher, err := secret.New(bc.SecretKey)` and `log.Fatalf` on error. The cipher is threaded
  explicitly as a dependency — **never** a package-level global.
- [ ] **Step 3:** Add `SECRET_KEY=...` to `.env.example` (or the documented dev env) and note it in the
  operator runbook (Task 7).
- [ ] **Step 4:** `GOWORK=off go build ./cmd/silo` compiles; starting without `SECRET_KEY` fatals with the
  actionable message.

---

## Task 3: `server_settings` encryption decorator

**Files:** Create `internal/catalog/encrypted_settings_repo.go`; modify `internal/api/handlers/admin.go`,
`cmd/silo/main.go`, `internal/api/router.go`.

- [ ] **Step 1: Build the audited allowlist.** Create exported `var SensitiveSettingKeys map[string]bool`
  in `internal/catalog` populated from the "Audited `SensitiveSettingKeys`" list above. **Do not** simply
  move the admin redaction map — re-audit `internal/config/db_loader.go` (and other `server_settings`
  consumers) for every secret-bearing key, and include the legacy aliases (`s3.operational_*`,
  `recommendations.openai_api_key`) and `redis.sentinel_password`. Update `admin.go`
  (`HandleGetSettings` redaction, `HandleGetSensitiveStatus`, the `PUT` response branch) to reference
  `catalog.SensitiveSettingKeys`. One allowlist now drives **both** redaction and encryption.
- [ ] **Step 2:** Implement `EncryptedSettingsRepo` wrapping `*ServerSettingsRepo` + `*secret.Cipher`:
  - `Set(key, value)`: if `SensitiveSettingKeys[key]` and `value != ""`, encrypt with AAD
    `"server_settings:"+key` before delegating; else delegate raw.
  - `Get(key)` / `GetAll()`: delegate, then apply the **read-path contract** to each sensitive key
    (pass through legacy plaintext; decrypt prefixed values; propagate decrypt errors).
- [ ] **Step 3:** Add `BackfillSensitiveSettings(ctx)` on the decorator: for each key in
  `SensitiveSettingKeys`, read the raw inner value; if non-empty and not `IsEncrypted`, `inner.Set` the
  encrypted value (AAD `"server_settings:"+key`). Idempotent. Returns a count + aggregated errors.
- [ ] **Step 4:** In `cmd/silo/main.go`, build `settingsRepo` as the `EncryptedSettingsRepo` wrapping the
  raw `catalog.NewServerSettingsRepo(pool)`. Confirm the auto-generate-`jwt_secret` step still works: the
  `GetAll` map carries plaintext, the `== ""` check is unchanged, and `Set("auth.jwt_secret", encoded)`
  now writes **encrypted** while the in-memory map keeps the plaintext for `LoadFromDB`. Any function
  currently typed to the concrete `*catalog.ServerSettingsRepo` changes to the `ServerSettingsStore`
  interface that both already satisfy.
- [ ] **Step 5 (re-wrap after pool recreation):** `cmd/silo/main.go` rebuilds a **raw**
  `catalog.NewServerSettingsRepo(pool)` after recreating the pool for `max_connections` (line ~399).
  Re-wrap it there too (`settingsRepo = catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), dataCipher)`)
  so every later consumer gets the encrypting repo, not a raw one. Audit for any other raw construction
  of the settings repo and wrap each.
- [ ] **Step 6:** Tests: decorator round-trip (raw store holds `enc:v1:`, decorator `Get` returns
  plaintext); non-sensitive keys pass through unencrypted; legacy-plaintext pass-through on read;
  `BackfillSensitiveSettings` idempotency.
- [ ] **Step 7:** `GOWORK=off go build ./...` compiles; admin settings GET still redacts (now including
  `redis.sentinel_password`); a sensitive key written via the admin API lands as `enc:v1:` in the DB.

---

## Task 4: Inline-encrypt arr keys + delete the `SecretResolver` indirection

**Files:** Modify `internal/requests/repository.go`, `internal/requests/service.go`,
`internal/autoscan/repository.go`, `internal/autoscan/connection.go`,
`internal/api/autoscan_wiring.go`, `internal/api/router.go`, `cmd/silo/main.go`.

> The legacy-data resolution for these two columns is handled by the **resolve-then-encrypt backfill** in
> Task 6 (a row may currently hold a `server_settings` key name, not a literal key). The repo write path
> below only ever encrypts the literal credential an admin submits going forward.

- [ ] **Step 1:** `requests.NewRepository` gains a `*secret.Cipher` parameter. In `insertIntegration` and
  `updateIntegration`, encrypt `i.APIKeyRef` (AAD `"request_integrations:api_key_ref:"+i.ID`) before
  binding — preserve the existing `CASE WHEN $5 = '' THEN api_key_ref ELSE $5 END` keep-existing
  semantics (encrypt only the non-empty incoming value). In `scanIntegration`, apply the read-path
  contract to the scanned `api_key_ref`.
- [ ] **Step 2:** Delete `requests.SecretResolver` (interface), the `Service.secrets` field,
  `Service.SetSecretResolver`, and `Service.resolveAPIKey`. Every former `resolveAPIKey(ctx, integration)`
  call site uses `strings.TrimSpace(integration.APIKeyRef)` directly (the repo already decrypted it).
- [ ] **Step 3:** `autoscan.NewRepository` gains a `*secret.Cipher`. Encrypt `api_key_ref` in
  `CreateConnection` / `UpdateConnection` (AAD `"autoscan_connections:api_key_ref:"+id`, same
  keep-existing CASE semantics); apply the read-path contract in `scanConnection`.
- [ ] **Step 4:** In `internal/autoscan/connection.go`, delete the `SecretResolver` interface, the
  `ConnectionResolver.secrets` field, and the secret-resolution branch in `Resolve`. For a linked Requests
  integration, `RequestIntegrationLookup.Get` returns the requests repo's **already-decrypted** key — use
  it directly. For a standalone connection, the autoscan repo already decrypted it. `Resolve` now only
  resolves base URL + linkage and returns plaintext.
- [ ] **Step 5:** Remove the `AutoscanSecretResolver` parameter from `BuildAutoscanService`
  (`internal/api/autoscan_wiring.go`) and drop the `SetSecretResolver(settingsRepo)` calls in
  `internal/api/router.go` and `cmd/silo/main.go` (including the reconcile-worker service). Thread
  `dataCipher` into the requests/autoscan repo constructors at all wiring sites.
- [ ] **Step 6:** Verify no API response serializes the raw key: the integration/connection response DTOs
  still emit only `has_api_key`. Add/extend a test asserting the create→store→read round-trip yields the
  original key, and that the stored column value is `enc:v1:`-prefixed.
- [ ] **Step 7:** `GOWORK=off go build ./...`; `GOWORK=off go test ./internal/requests/... ./internal/autoscan/...`.

---

## Task 5: Per-table credential columns

**Files:** Modify `internal/subtitles/pgrepo.go`, `internal/watchsync/repository.go`,
`internal/webhooksync/repo.go`, `internal/historyimport/repo_admin.go`, `internal/historyimport/repo.go`,
plus their constructor call sites.

Each repo gains a `*secret.Cipher` constructor arg and follows encrypt-on-write / decrypt-on-read with a
row-bound AAD. Preserve existing keep-existing CASE/COALESCE semantics — encrypt only non-empty incoming
values. All read paths use the read-path contract (pass through legacy plaintext, hard-fail corrupt
ciphertext). These columns **never** had the resolver indirection — they always stored literal
credentials — so no resolve-then-encrypt is needed (unlike the arr columns).

- [ ] **Step 1: subtitles** — encrypt `api_key` + `password` in `UpsertProviderConfig`; decrypt in
  `ListProviderConfigs` and `GetProviderConfig`. Keep the existing `HasAPIKey`/`HasCredentials` flags
  (set them from the decrypted values). AAD PK = `provider_name`.
- [ ] **Step 2: watchsync** — encrypt `access_token` + `refresh_token` in `UpsertConnection`. Add a
  `decryptConnection(*Connection)` helper and call it after **every** scan path (list/get/by-provider/etc.
  — there are several). AAD PK = connection `id`.
- [ ] **Step 3: webhooksync** — encrypt `access_token` in `CreateConnection`; decrypt in `scanConnection`.
  **Do NOT touch `webhook_secret`** — it is equality-looked-up in `GetConnectionBySecret`
  (`WHERE webhook_secret = $1`) and is carved out for the hashing follow-up. AAD PK = connection `id`.
- [ ] **Step 4: historyimport admin token** — encrypt in `SetSourceAdminToken`; decrypt in
  `GetSourceWithAdminToken`; `ClearSourceAdminToken` (writes NULL) is unchanged. AAD PK = source `id`.
- [ ] **Step 5: historyimport session tokens** — in `internal/historyimport/repo.go`, encrypt
  `connect_access_token` (`history_import_connect_sessions`) and `auth_token`
  (`history_import_plex_sessions`) on insert/update; decrypt on scan (note the existing
  `COALESCE(auth_token, '')` read). These are read by session `id`, not by token value. `pin_id`/`pin_code`
  are transient pairing artifacts — leave them. AAD PK = session `id`.
- [ ] **Step 6:** Thread `dataCipher` into each constructor at its wiring site (`router.go` / `main.go`).
- [ ] **Step 7:** `GOWORK=off go build ./...`; package tests for each modified repo pass.

---

## Task 6: Idempotent startup backfill

**Files:** Create `internal/secret/backfill.go`, `internal/secret/backfill_test.go`; modify `cmd/silo/main.go`.

- [ ] **Step 1: Generic in-place backfill** for the literal-credential columns (subtitle ×2, watchsync ×2,
  webhook `access_token`, historyimport `admin_token`, historyimport session `connect_access_token` +
  `auth_token`). Per `BackfillTarget {table, column, idColumn, aadPrefix}`: select rows where the column
  is non-null, non-empty, and `NOT LIKE 'enc:v1:%'`; for each, compute AAD = `aadPrefix + ":" + id`,
  `EncryptIfPlaintext`, and `UPDATE ... SET col = $1 WHERE id = $2 AND col = $3` (the `AND col = $3` guard
  makes concurrent multi-node boots converge without double-encrypting). Empty values are skipped (never
  encrypt `""`).
- [ ] **Step 2: Resolve-then-encrypt backfill for the two arr columns** (`request_integrations.api_key_ref`,
  `autoscan_connections.api_key_ref`). This is **separate** from Step 1 because legacy rows may hold a
  `server_settings` key name rather than a literal key. For each row whose value is not already
  `enc:v1:`-prefixed, replicate the old `resolveAPIKey` semantics **once**:
  1. `resolved := encryptedSettingsRepo.Get(ctx, value)` (the decrypting decorator — handles both a
     sensitive-and-encrypted target like `requests.radarr.api_key` and a plaintext custom key).
  2. If `resolved` is non-empty, the row held a *reference* → encrypt `resolved` inline.
     Else the row held a *literal* key → encrypt `value` inline.
  3. `UPDATE ... SET api_key_ref = <enc> WHERE id = $2 AND api_key_ref = <original>`, AAD bound to the row id.
  Run this **after** `BackfillSensitiveSettings` so the referenced settings are consistent (the decorator
  decrypts regardless, but ordering keeps it deterministic). Optionally log/flag now-orphaned
  `requests.{radarr,sonarr}.api_key` settings rows for a later cleanup (do not delete in this PR).
- [ ] **Step 3: Failure posture (decided):** best-effort. Collect per-row/per-target errors, log each at
  ERROR, and emit a single startup summary line (`secret backfill: encrypted N, failed M`). A failed
  encrypt leaves the pre-existing plaintext (no *new* exposure vs. today) and reads still succeed via the
  read-path pass-through, so it must **not** block boot — reliability over a hard stop. (Read-path decrypt
  failures on an `enc:v1:` value still hard-fail the individual operation.)
- [ ] **Step 4: Ordering in `cmd/silo/main.go`** — gate on migration-running modes only (mirror the
  existing migration guard; proxy/transcode nodes skip the backfill). Sequence:
  `LoadBootstrap (read SECRET_KEY) → open pool → RunMigrations → construct dataCipher → wrap settingsRepo →
  BackfillSensitiveSettings → generic column backfill → arr resolve-then-encrypt backfill → GetAll(decrypts)
  → auto-gen jwt_secret (writes encrypted) → [pool recreate? re-wrap settingsRepo] → LoadFromDB(plaintext) → validate`.
  The settings backfill + `GetAll` must precede `LoadFromDB` so config consumers read decrypted values.
- [ ] **Step 5: Concurrency note** — the `AND col = $3` idempotency guard is sufficient for the common
  single-primary deploy. For multi-primary races, optionally wrap the backfill in a dedicated Postgres
  advisory lock with a **fresh** lock id (do **not** reuse the migration `legacyBootstrapLocker` id — that
  would extend the migration lock's held duration unpredictably).
- [ ] **Step 6: Tests** (`backfill_test.go`, against a test DB): seed plaintext rows → backfill →
  all become `enc:v1:` and decrypt to the originals; second run is a no-op (no double-encrypt); mixed
  plaintext/ciphertext rows handled; empty values skipped; one failing row does not abort the rest;
  **arr resolve-then-encrypt:** a row holding `requests.radarr.api_key` (with that settings key present)
  ends up encrypting the *resolved* credential, and a row holding a literal key encrypts the literal.
- [ ] **Step 7:** Boot against a DB pre-seeded with plaintext credentials **and** a legacy arr row that
  references a settings key; confirm the summary log, that every target column is `enc:v1:` afterward, that
  the arr row decrypts to the real Radarr key (not the setting name), and that a second boot reports
  `encrypted 0`.

---

## Task 7: Verification, docs, follow-up

- [ ] **Step 1: Regression guard tests:**
  - API key auth still works — `api_keys.api_key` is stored as the literal `sa_`-prefixed token and
    `GetByKey` (`WHERE api_key = $1`) still resolves (proves it was *not* routed through encryption).
  - `auth.jwt_secret` is consistent across two simulated boots (encrypted at rest, plaintext to
    `LoadFromDB`), and the stored `server_settings` row is `enc:v1:`-prefixed.
  - Inbound webhook routing still works (`webhook_secret` untouched).
  - A legacy arr integration that referenced `requests.sonarr.api_key` still fulfills after backfill.
- [ ] **Step 2:** `GOWORK=off go build ./...`, `make lint`, `make verify-local-paths`,
  `cd web && pnpm run lint && pnpm run format:check` (frontend unaffected — confirm green).
- [ ] **Step 3:** Write `docs/architecture/secret-encryption.md`: how `SECRET_KEY` is generated, that it
  must be backed up **separately** from DB dumps (treat like a CA private key), what happens on key loss
  (encrypted secrets unrecoverable → re-enter integrations), the rollback/downgrade hazard (an old binary
  reads `enc:v1:auth.jwt_secret` as a literal JWT secret → all sessions invalid; document that downgrade
  requires decrypting settings back to plaintext or clearing `auth.jwt_secret`), and the envelope/version
  scheme for future rotation.
- [ ] **Step 4: File the follow-up issues:**
  1. **Hash equality-lookup secrets** (`api_keys.api_key`, `webhook_sync_connections.webhook_secret`) —
     store a deterministic hash, look up by hash.
  2. **Encrypt plugin runtime config** (`plugin_runtime_configs.config_value`) — coordinated with the
     `Silo` host repo and plugin-manifest secret-field metadata; must also re-encrypt/purge the legacy
     plaintext `introdb.api_key` copy. Note the dead `plex_sync_connections` table as a drop candidate.
- [ ] **Step 5:** Update the issue #45 acceptance check: no plaintext arr keys at rest (Requests +
  Autoscan); `api_key_ref` is unambiguous (inline ciphertext, no literal fallback); existing rows
  encrypted by the backfill (arr rows resolved-then-encrypted); S3 + the broader server-owned credential
  set covered; remaining gaps (equality-lookup hashing, plugin JSONB) tracked as follow-ups.

---

## Safety checklist (the implementer must not skip)

1. `SECRET_KEY` is read in `LoadBootstrap` and **fatals** if absent/short — never defaults to a zero key
   or a hash of `DATABASE_URL`.
2. The cipher is constructed once, right after bootstrap, before any `settingsRepo` call — threaded as a
   dependency, never a global.
3. The settings backfill + decryption happen **before** `config.LoadFromDB` consumes the map.
4. The auto-generated `auth.jwt_secret` is written **encrypted**, while the in-memory map keeps plaintext
   for `LoadFromDB`.
5. **`settingsRepo` is re-wrapped as the `EncryptedSettingsRepo` after the max-connections pool recreation
   (main.go ~399)** and at every other construction site — no raw settings repo escapes into later wiring.
6. The two **arr columns** use the **resolve-then-encrypt** backfill (not in-place) so legacy
   `server_settings` references become the real credential before encryption; the other columns use the
   plain in-place backfill.
7. `SensitiveSettingKeys` is **audited from the config loader's real inputs** (incl.
   `redis.sentinel_password` and the legacy `s3.operational_*` / `recommendations.openai_api_key`
   aliases), not copied from the old redaction map.
8. `api_keys.api_key` and `webhook_sync_connections.webhook_secret` are **never** routed through
   encryption — they are equality-looked-up and would break auth/routing (separate hashing task).
9. Every decrypt site obeys the **read-path contract**: pass through non-prefixed plaintext; **error
   (never fall back)** on a corrupt `enc:v1:` value. Audit every old `if resolved == ""` site removed in Task 4.
10. AAD binds to **row identity** (`table:column:<pk>`); PKs in scope (integration/connection ids, uuids,
    `provider_name`) are stable. server_settings binds to its key.
11. The backfill is prefix-gated and idempotent (`AND col = $3` guard); proxy/transcode nodes skip it.
12. `plex_sync_connections` is not a target (dead table); `plugin_runtime_configs.config_value` is a
    documented deferred gap, not silently ignored; `subtitle_provider_config.username` stays plaintext.

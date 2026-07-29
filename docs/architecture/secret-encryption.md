# At-rest credential encryption

Silo encrypts third-party integration credentials and other server-owned secrets
at rest. Sonarr/Radarr API keys, S3 keys, watch-sync OAuth tokens, the
Audiobookshelf-compat signing key, and the sensitive `server_settings` entries
are stored as ciphertext in PostgreSQL and decrypted only in memory. The key that
protects them lives **outside** the database, so a full DB dump or compromise
does not expose the secrets.

## How it works

- **Cipher:** AES-256-GCM with a random 12-byte nonce per value. Implemented in
  `internal/secret`.
- **Key derivation:** the data key is HKDF-SHA256–derived from the `SECRET_KEY`
  environment variable with a versioned domain label. `SECRET_KEY` is the
  encryption root; it is never stored in the database.
- **Envelope:** every ciphertext is stored as `enc:v1:<base64url(nonce‖sealed)>`.
  The `enc:v1:` prefix is the version marker (see *Key rotation* below).
- **Row binding (AAD):** each ciphertext is GCM-bound to its logical row via
  additional-authenticated data — `table:column:<pk>` for per-row columns,
  `server_settings:<key>` for settings. A DB-write attacker therefore cannot move
  a credential blob from one row/column to another: decrypting under a different
  binding fails authentication.
- **Read path:** an empty value stays empty; a non-`enc:v1:` value is treated as
  legacy plaintext and passed through unchanged (so a not-yet-migrated row keeps
  working); an `enc:v1:` value is decrypted and **any** failure (wrong key,
  tampering, truncation) is surfaced as an error — it is never silently used as a
  credential.

## SECRET_KEY

`SECRET_KEY` is **required**. The server refuses to start without it (it fatals
in `config.LoadBootstrap`), and it must be at least 32 characters.

Generate one with:

```bash
openssl rand -base64 48
```

Set it in the deployment environment (or in `.env` for source/dev runs, which
`godotenv` loads at startup). See `.env.example`.

### Back it up — separately from the database

Treat `SECRET_KEY` like a CA private key:

- **Store it outside your database backups.** A DB dump contains only ciphertext;
  the value of at-rest encryption is entirely lost if the key travels with the
  dump.
- Keep it in a secrets manager / sealed secret / password manager, not in the
  repo or in the same bucket as your Postgres backups.
- Rotating the *deployment* (new host, restored DB) requires the **same**
  `SECRET_KEY`. Restoring a database backup onto a node with a different
  `SECRET_KEY` makes every encrypted secret unreadable.

### Key loss

If `SECRET_KEY` is lost, the encrypted secrets are **unrecoverable** — that is the
point of keeping the key out of the database. Recovery means re-entering the
affected credentials:

- Re-enter Sonarr/Radarr and Autoscan API keys, S3 keys, watch-sync connections,
  history-import tokens, subtitle provider credentials, and plugin runtime
  configuration.
- `auth.jwt_secret` becomes unreadable, so all existing sessions are invalid and
  users must log in again (a new secret is generated on next boot if the row is
  cleared — see below).

## Rollout (automatic backfill)

On the first boot after deploying this change, the primary (migration-running)
node runs an idempotent, best-effort startup backfill that encrypts any
remaining plaintext in place:

1. sensitive `server_settings` keys,
2. the per-table credential columns (subtitles, watch-sync, webhook-sync,
   history-import, including temporary server-list credentials stored in
   session JSON),
3. the two arr `api_key_ref` columns — these are **resolved-then-encrypted**: a
   legacy row that held a `server_settings` reference (e.g.
   `requests.radarr.api_key`) is collapsed to the real credential before being
   encrypted,
4. whole `plugin_runtime_configs.config_value` objects. Plugin config is
   intentionally opaque to the host storage layer: plugins may write undeclared
   keys and manifest annotations may be unavailable or change over time, so
   manifest-selected field encryption cannot fail closed.

The backfill is safe to run repeatedly: already-encrypted values are skipped, and
a per-row guard makes concurrent multi-node boots converge without
double-encrypting. A failed row is logged and left as plaintext (no new exposure)
rather than blocking boot. Proxy/transcode nodes skip the backfill; they read
whatever the primary encrypted. No manual steps are required.

## Rollback / downgrade hazard

Downgrading to a binary that predates this change is **not** safe while secrets
are encrypted, because the old binary has no read path: it would read
`enc:v1:auth.jwt_secret` as a literal JWT secret (invalidating all sessions) and
read `enc:v1:`-prefixed integration keys as garbage credentials. It would also
pass the reserved plugin-config envelope object to plugins instead of their
configuration.

To downgrade safely you must first return the affected values to plaintext, for
example:

- Decrypt the sensitive `server_settings` back to plaintext (run a one-off that
  reads each via the encrypting repo and writes the plaintext via the raw repo),
  **or**
- At minimum, clear `auth.jwt_secret` so the older binary regenerates a fresh
  plaintext one (this still logs everyone out), and re-enter any integration
  credentials as plaintext.

There is no automatic "decrypt everything" downgrade path in this release; plan
downgrades accordingly.

## Key rotation (future)

The `enc:v1:` envelope and the HKDF domain label are versioned. A future rotation
would introduce `enc:v2:` with a new derived key (and `Decrypt` already dispatches
on the version, returning an explicit error for an unknown version). Rotation is
out of scope for this release; the versioning exists so it can be added without a
data migration of the envelope format.

## Scope and known gaps

Covered: arr (Requests + Autoscan) API keys, S3 keys, all sensitive
`server_settings`, watch-sync tokens, webhook-sync `access_token`,
history-import admin/session tokens and temporary server-list credentials,
subtitle provider `api_key`/`password`,
the Jellyfin-compat session's bridged Silo access/refresh tokens
(`jellycompat_sessions.streamapp_access_token` / `streamapp_refresh_token`), and
the ABS signing key. Plugin runtime configuration is encrypted as one opaque
row-bound envelope rather than by manifest field. Consequently, runtime code
must use `RuntimeConfigStore`; database JSON-member queries and indexes are not
supported for `plugin_runtime_configs.config_value`.

Deliberately **not** encrypted (tracked as follow-ups):

- **Equality-looked-up secrets** — these are matched by exact value
  (`WHERE … = $1`), and AES-GCM is randomized, so encrypting them would break the
  lookup. They need a deterministic **blind-index hash** column instead:
  `api_keys.api_key`, `webhook_sync_connections.webhook_secret`,
  `jellycompat_sessions.token`, and `watch_together_rooms.join_token`.
Excluded (not a gap): `plex_sync_connections.*` is a dead table (zero Go
references); `oauth_completion.token_ciphertext` is already AES-GCM;
`users.password_hash` and the `*_hash` columns are already hashed.

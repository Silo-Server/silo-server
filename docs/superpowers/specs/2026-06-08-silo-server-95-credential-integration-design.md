# silo-server: rebase requests-pluginization onto main + adopt #95's at-rest credential model

Status: design • 2026-06-08
Scope: `silo-server` only. Lands the host side of the request-router
pluginization on current `origin/main` for a clean upstream PR, reconciling it
with two PRs that merged upstream after we forked: #95 (credential encryption)
and #39 (multi-instance routing).

## Context

`feat/requests-pluginization` was built on an old fork-main and is deployed live.
Upstream `Silo-Server/silo-server` then **force-pushed `main`**, absorbing the
fork's history. The result vs our branch:
- **188 of our 285 commits are already upstream** (patch-equivalent under new
  SHAs) and auto-drop on rebase.
- **~68 are genuinely ours** (the request-router pluginization) to replay.

Two upstream PRs reshaped the exact requests code we pluginized:
- **#95 "encrypt server-owned credentials at rest"** — added `internal/secret`
  (AES-256-GCM, HKDF from a **required `SECRET_KEY`** at boot), threads a
  `*secret.Cipher` into `requests.NewRepository`, **inline-encrypts**
  `request_integrations.api_key_ref` (encrypt on write via `encryptAPIKey`,
  decrypt on read in `scanIntegration`), and **deleted the `SecretResolver`
  indirection** (`resolveAPIKey`/`SetSecretResolver`). Post-#95, `Integration.
  APIKeyRef` holds the literal key in memory; empty means unconfigured.
- **#39 "multi-instance Sonarr/Radarr routing"** — was already in our base; our
  pluginization is the evolution/replacement of it. #39-area conflicts resolve in
  our favor.

## Goal

Produce a branch that is current `origin/main` + our 68 request-router commits,
with the plugin credential path re-expressed on #95's inline-cipher model — ready
for a clean host PR (`fluxis` fork → `Silo-Server/silo-server`).

## Non-goals

- Opening the host PR (separate step, after the rebase is built + verified).
- The live redeploy / `SECRET_KEY` provisioning / credential backfill on the box
  (deploy-time actions, noted below but out of scope for this change).
- Re-litigating the plugin design — only the credential sourcing changes.

## Approach: per-commit rebase, not squash/overlay

`git diff origin/main...HEAD` spans all 285 commits (diff vs merge-base), so a
tree overlay/squash would revert origin/main's force-pushed versions of unrelated
files (autoscan, jellycompat, …) to our stale copies. Only a **per-commit rebase**
isolates our 68 correctly — it drops the 188 patch-equivalent commits by patch-id
and replays just ours.

```
git config rerere.enabled true     # record conflict resolutions, auto-replay repeats
git rebase origin/main             # on feat/requests-pluginization (backup: backup/pre-main-rebase-20260608)
```

Conflicts concentrate in `internal/requests/repository.go`,
`internal/requests/service.go`, `internal/requests/service_test.go`, and
`cmd/silo/main.go`. Resolution rules:
- **#39-area** (legacy multi-instance arr code our pluginization replaced): take
  **ours**.
- **#95 credential path**: **adopt #95's model** (below), merged with our plugin
  schema.

## Credential integration (the substantive merge)

### `internal/requests/repository.go`
Merge #95 + ours:
- Keep #95's `cipher *secret.Cipher` field, `NewRepository(pool, cipher)`,
  `apiKeyAAD`, `encryptAPIKey`, and the **decrypting `scanIntegration` method**.
- Add our plugin columns to the column list, insert, and update:
  `capability_id`, `installation_id`, `supported_media_types`, `plugin_config`
  (with our defaulting: `capability_id` trimmed/non-empty, `plugin_config`
  marshaled, `supported_media_types` non-nil).
- `scanIntegration` scans the plugin columns **and** decrypts `api_key_ref`
  (single decrypting method, `r.scanIntegration`; no package-func variant).
- Insert/update bind the **encrypted** `apiKeyRef` **and** the plugin columns.

### `internal/requests/service.go`
- **Delete** our `SecretResolver` interface, `secrets` field,
  `SetSecretResolver`, and `resolveAPIKeyCached` (align with #95's removal).
- `resolveRouterConnections`: replace the `s.resolveAPIKeyCached(ctx, fc, in)`
  call with **direct use of `in.APIKeyRef`** (already the literal key);
  `strings.TrimSpace(in.APIKeyRef) == ""` is the "skip unconfigured" guard. The
  plugin still receives the literal key in `ResolvedRouterConnection.APIKey`.
- `fulfillContext.secrets` (the per-cycle apiKeyRef→plaintext memo) is removed —
  no resolution happens anymore; the repo already decrypted.
- All other pluginization (capability containment, `eligibleRouterConnection`,
  requester-identity population, single-default exclusivity) is unchanged.

### `cmd/silo/main.go`
- `requests.NewRepository(pool, dataCipher)` — #95 already constructs
  `dataCipher` from `SECRET_KEY` early in bootstrap.
- Our plugin wiring stays: `SetRouterProvider`, `SetRequesterIdentityResolver`,
  `SetEntitlementResolver`. The `SetSecretResolver` call is **removed**.

### `service_test.go`
Tests that injected a fake `SecretResolver` switch to seeding the integration's
`APIKeyRef` with the literal key directly (the repo-decrypted contract). The
request-router tests added this session (capability containment, requester
identity, etc.) keep working against the literal-key contract.

## Verification

- `PATH=$PATH:/tmp/go/bin go test ./internal/requests/ ./internal/plugins/` green;
  `go vet` clean. Full server build in the libvips container if needed.
- **Security review** of the merged credential path: api keys encrypted at rest
  (encrypt-on-write, decrypt-on-read), the literal key never logged nor written to
  `plugin_config`/`metadata_json`, `_SENSITIVE_METADATA_KEYS` stripping intact,
  no plaintext leak through plugin Fulfill/Validate outputs.
- Confirm the rebase dropped exactly the patch-equivalent commits and the tree
  diff vs `origin/main` is *only* our feature (`git diff --stat origin/main`).

## Deployment implications (out of scope, but must be flagged on the PR)

- The live container must have **`SECRET_KEY`** set before any post-#95 redeploy
  or the server refuses to boot (fatal in lifespan).
- #95's startup backfill encrypts existing plaintext credentials (incl. the arr
  api keys) on first boot of the primary node.
- Backup branch `backup/pre-main-rebase-20260608` preserves the pre-rebase state.

## Rollout

1. Rebase + resolve (this change), verify (build/test/security review).
2. Push the rebased branch to the `fork` remote; open the host PR to
   `Silo-Server/silo-server` (stacked on the SDK being published, like the
   plugins — the host go.mod also `replace`s the SDK today).
3. Live redeploy with `SECRET_KEY` provisioned (separate, deploy-time).

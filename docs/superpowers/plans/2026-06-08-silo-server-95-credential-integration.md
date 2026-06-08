# silo-server rebase onto main + #95 credential adoption — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land our 68 request-router commits on current `origin/main` and re-express the plugin credential path on #95's at-rest `secret.Cipher` model, producing a clean host PR branch.

**Architecture:** A two-phase approach. Phase 1 is a per-commit rebase that drops the 188 already-upstream commits and replays ours, resolving credential-file conflicts by **taking our (pre-#95) version** so the branch builds against our self-consistent feature. Phase 2 is a single focused commit that swaps our `SecretResolver` model for #95's inline cipher — done once against the final state, properly tested.

**Tech Stack:** Go; `git rebase` + `rerere`. Repo: `/opt/silo`, branch `feat/requests-pluginization` (backup: `backup/pre-main-rebase-20260608`). Go: prefix `PATH=$PATH:/tmp/go/bin`. `internal/requests` + `internal/plugins` build/test bare-host; the full `cmd/silo` build needs the `golang:1.26 + libvips-dev` container.

**Note on execution:** Task 1 is a guided git operation, not TDD — it's inherently interactive (conflict-by-conflict). Task 2 is the real TDD engineering. Best run inline by someone with full context on our feature, not a fresh subagent.

---

### Task 1: Rebase our 68 commits onto origin/main (take-ours on credential files)

**Files (conflict surface):** `internal/requests/repository.go`, `internal/requests/service.go`, `internal/requests/service_test.go`, `cmd/silo/main.go` (+ any #39-area files).

- [ ] **Step 1: Pre-flight.**
```bash
cd /opt/silo && PATH=$PATH:/tmp/go/bin
git fetch origin
git rev-parse --verify backup/pre-main-rebase-20260608   # backup must exist; create if missing: git branch backup/... feat/requests-pluginization
git config rerere.enabled true
git status --short | grep -vE 'vendor/|Dockerfile.deploy|cmd/plugininstall' && echo "WORKTREE DIRTY — stash/clean first" || echo "worktree clean (ignoring untracked deploy aids)"
```

- [ ] **Step 2: Start the rebase.**
```bash
git rebase origin/main
```
It auto-skips the 188 patch-equivalent commits and replays ~68, stopping at each conflict.

- [ ] **Step 3: Resolve each conflict by these rules, then `git rebase --continue`.** Repeat until the rebase completes.

  **Resolution rules:**
  - **`internal/requests/repository.go`, `internal/requests/service.go`, `internal/requests/service_test.go`** → take **OURS** (the commit being applied):
    ```bash
    git checkout --theirs internal/requests/repository.go internal/requests/service.go internal/requests/service_test.go 2>/dev/null
    git add internal/requests/repository.go internal/requests/service.go internal/requests/service_test.go
    ```
    (In rebase, `--theirs` = the commit being replayed = our code. We deliberately drop #95's cipher from these files here; Task 2 re-adds it cleanly. Only run `git checkout --theirs` for the files actually marked conflicted at this stop.)
  - **`cmd/silo/main.go`** → **MERGE manually**: keep ALL of origin/main's lines (the `dataCipher` construction at ~L306, and every other-repo cipher wiring: `historyimport`, `autoscan`, `catalog.NewEncryptedSettingsRepo`, `nodeconfig.NewWatcher`, `SecretCipher: dataCipher`, etc.), AND add our request-service plugin wiring (`SetRouterProvider`, `SetRequesterIdentityResolver`, `SetEntitlementResolver`). **Critical for compilation:** the `mediarequests.NewRepository(...)` call must match OUR repository signature at this phase — i.e. drop the `deps.SecretCipher` arg so it reads `mediarequests.NewRepository(deps.DB)` (Task 2 restores the cipher arg). Resolve, then `git add cmd/silo/main.go`.
  - **Any other (#39-area legacy arr) file** → take **OURS** (our pluginization supersedes #39): `git checkout --theirs <file> && git add <file>`.
  - After staging the resolved files for that stop: `git rebase --continue`.

- [ ] **Step 4: Rebase complete — verify the right thing happened.**
```bash
git log --oneline origin/main..HEAD | wc -l      # ~68, only our feature commits
git diff --stat origin/main -- internal/requests | tail -3   # only requests changes, no autoscan/jellycompat noise
```
Expected: ~68 commits; the diff vs origin/main touches our feature areas only (requests, web plugins, our migrations, plugin host), NOT the 188 absorbed files.

- [ ] **Step 5: Verify the branch builds + tests (pre-#95 credential model, self-consistent).**
```bash
PATH=$PATH:/tmp/go/bin go test ./internal/requests/ ./internal/plugins/ && go vet ./internal/requests/ ./internal/plugins/
```
Expected: PASS (our feature's own tests, still on our `SecretResolver` model). If `cmd/silo` references drift (signature mismatches from the merge), fix the merge in `cmd/silo/main.go` until `go build ./internal/... ./cmd/silo/` compiles in the libvips container (or at least `./internal/requests ./internal/plugins` bare-host + a container build).

- [ ] **Step 6: No commit needed** — the rebase already rewrote history. Do NOT `git commit`; the branch tip is the replayed feature. (If you want a safety tag: `git tag rebased-pre-integration`.)

---

### Task 2: Adopt #95's inline cipher in the requests credential path

**Files:**
- Modify: `internal/requests/repository.go` (cipher field, encrypt on write, decrypt + plugin columns in scan)
- Modify: `internal/requests/service.go` (delete `SecretResolver`; read `APIKeyRef` directly)
- Modify: `internal/requests/service_test.go` (seed literal `APIKeyRef`; drop fake resolver)
- Modify: `cmd/silo/main.go` (`NewRepository(deps.DB, deps.SecretCipher)`)

- [ ] **Step 1: Write/adapt the failing test.** In `internal/requests/service_test.go`, the request-router tests currently inject api keys via a fake `SecretResolver`. Change the fake-store integrations to carry the literal key in `APIKeyRef` directly, and DELETE the fake `SecretResolver` wiring. Add a focused repo test in `internal/requests/repository_test.go` (create if absent) proving round-trip encryption — insert an integration with `APIKeyRef:"plainkey"`, read it back via `ListIntegrations`, assert `APIKeyRef == "plainkey"` (decrypted) AND the raw DB column is NOT plaintext:
```go
func TestIntegrationAPIKeyEncryptedAtRest(t *testing.T) {
	pool := testPool(t)                      // existing test-pool helper; if none, use the repository package's standard test harness
	cipher, _ := secret.New("test-secret-key-at-least-32-bytes-long!!")
	repo := NewRepository(pool, cipher)
	in := Integration{ID: "i1", Kind: "sonarr", Name: "S", Enabled: true, BaseURL: "http://x", APIKeyRef: "plainkey", CapabilityID: "arr", InstallationID: ptr(1)}
	if _, err := repo.insertIntegration(context.Background(), pool, in); err != nil { t.Fatal(err) }
	got, _ := repo.ListIntegrations(context.Background())
	if got[0].APIKeyRef != "plainkey" { t.Fatalf("decrypted key = %q, want plainkey", got[0].APIKeyRef) }
	var raw string
	pool.QueryRow(context.Background(), "SELECT api_key_ref FROM request_integrations WHERE id='i1'").Scan(&raw)
	if raw == "plainkey" { t.Fatal("api key stored in plaintext — encryption not applied") }
}
```
(Match the package's existing DB-test pattern; if the repo has no DB-test harness, assert encryption at the cipher boundary instead and keep the literal-key contract test in service_test.go.)

- [ ] **Step 2: Run — fails to compile** (`NewRepository` takes one arg; `secret` not imported). `PATH=$PATH:/tmp/go/bin go test ./internal/requests/ -run TestIntegrationAPIKeyEncryptedAtRest`.

- [ ] **Step 3: `repository.go` — adopt #95's cipher, keep our columns.**
  - Add the field + constructor:
    ```go
    type Repository struct {
        pool   *pgxpool.Pool
        cipher *secret.Cipher
    }
    func NewRepository(pool *pgxpool.Pool, cipher *secret.Cipher) *Repository {
        return &Repository{pool: pool, cipher: cipher}
    }
    ```
  - Add #95's helpers:
    ```go
    func apiKeyAAD(id string) string { return "request_integrations:api_key_ref:" + id }
    func (r *Repository) encryptAPIKey(id, apiKey string) (string, error) {
        apiKey = strings.TrimSpace(apiKey)
        if apiKey == "" { return "", nil }
        return r.cipher.Encrypt(apiKey, apiKeyAAD(id))
    }
    ```
    (Confirm the exact `apiKeyAAD` string + `Encrypt`/`DecryptIfEncrypted` signatures against `git show origin/main:internal/requests/repository.go` — copy them verbatim so the AAD matches what #95's backfill wrote.)
  - In `insertIntegration` and `updateIntegration`: compute `apiKeyRef, err := r.encryptAPIKey(i.ID, i.APIKeyRef)` and bind that (encrypted) value, KEEPING our plugin-column binds (`capability_id`, `installation_id`, `supported_media_types`, `plugin_config`) and our defaulting.
  - In `scanIntegration` (the `r.`-method form): after scanning, decrypt — `apiKey, err := r.cipher.DecryptIfEncrypted(i.APIKeyRef, apiKeyAAD(i.ID)); i.APIKeyRef = apiKey` — and KEEP scanning our plugin columns into `i.CapabilityID/InstallationID/SupportedMediaTypes/PluginConfig`. Ensure all callers use `r.scanIntegration` (method), not a package func.
  - Add `"github.com/Silo-Server/silo-server/internal/secret"` to imports.

- [ ] **Step 4: `service.go` — delete the resolver, read `APIKeyRef` directly.**
  - DELETE: the `SecretResolver` interface, the `secrets SecretResolver` field on `Service`, `SetSecretResolver`, `resolveAPIKeyCached`, and `fulfillContext.secrets` (the apiKeyRef→plaintext memo + its initialization in `newFulfillContext`).
  - In `resolveRouterConnections`, replace:
    ```go
    apiKey, err := s.resolveAPIKeyCached(ctx, fc, in)
    if err != nil { slog.WarnContext(...); continue }
    if strings.TrimSpace(apiKey) == "" { slog.WarnContext(...); continue }
    ```
    with:
    ```go
    // in.APIKeyRef was decrypted by the repo on read; empty means unconfigured.
    apiKey := strings.TrimSpace(in.APIKeyRef)
    if apiKey == "" {
        slog.WarnContext(ctx, "requests: skipping router connection with no api key", "connection_id", in.ID)
        continue
    }
    ```
    Keep the rest (capability containment, lock-after-key, `ResolvedRouterConnection{APIKey: apiKey}`).
  - Grep for any other `resolveAPIKey`/`.secrets`/`SecretResolver` use in the package and remove/adjust (e.g. `integrationConfigured`, validate paths) to the literal-key contract — mirror `git show origin/main:internal/requests/service.go` for the exact post-#95 shapes of those spots.

- [ ] **Step 5: `cmd/silo/main.go` — pass the cipher.** Change the requests-repo construction back to `mediarequests.NewRepository(deps.DB, deps.SecretCipher)` (it was temporarily `(deps.DB)` in Task 1). Remove any leftover `SetSecretResolver(...)` call on the requests service.

- [ ] **Step 6: Run tests + build.**
```bash
PATH=$PATH:/tmp/go/bin go test ./internal/requests/ ./internal/plugins/ && go vet ./internal/requests/
```
Expected PASS. Then a container build of the whole server (libvips) to confirm `cmd/silo` compiles.

- [ ] **Step 7: Commit.**
```bash
git add internal/requests cmd/silo/main.go
git commit -m "feat(requests): adopt at-rest credential cipher (#95) for plugin api keys; drop SecretResolver"
```

---

### Task 3: Security review of the merged credential path

- [ ] **Step 1: Dispatch the security-reviewer agent** on the Task-2 commit, focused on: api keys encrypted at rest (encrypt-on-write present in BOTH insert + update; AAD matches #95's backfill so existing rows decrypt), decrypt-on-read in `scanIntegration` for every read path (`ListIntegrations`, get-by-id), the literal key never logged and never written into `plugin_config`/`metadata_json`/client responses (`_SENSITIVE_METADATA_KEYS` stripping intact), and no plaintext leak through plugin `Fulfill`/`Validate` outputs. Fix any findings (TDD) and re-review.

---

### Task 4: PR prep — pin published SDK v0.6.0, push, open the host PR

- [ ] **Step 1: Drop the local SDK replace + pin v0.6.0** (the SDK is now published).
```bash
cd /opt/silo
grep -n "replace github.com/Silo-Server/silo-plugin-sdk" go.mod    # confirm a local replace exists
sed -i '/replace github.com\/Silo-Server\/silo-plugin-sdk =>/d' go.mod
PATH=$PATH:/tmp/go/bin go get github.com/Silo-Server/silo-plugin-sdk@v0.6.0
PATH=$PATH:/tmp/go/bin go mod tidy
git add go.mod go.sum && git commit -m "build: pin published silo-plugin-sdk v0.6.0 (drop local replace)"
```
(Then `go mod vendor` is only needed for the deploy image, not the PR.)

- [ ] **Step 2: Push to the fork + open the PR.**
```bash
git push -u fork feat/requests-pluginization
gh pr create --repo Silo-Server/silo-server --base main --head fluxis:feat/requests-pluginization \
  --title "feat(requests): pluginize content-request fulfillment via request_router.v1" \
  --body-file /tmp/host-pr-body.md
```
PR body (`/tmp/host-pr-body.md`) must state: the request_router pluginization (host owns governance, plugins own fulfillment); built on silo-plugin-sdk v0.6.0; **adopts #95's at-rest credential model** (api keys encrypted, `SecretResolver` removed); supersedes #39's in-host multi-instance routing; **DEPLOY NOTE: `SECRET_KEY` must be set on every node before upgrade or the server refuses to boot, and #95's startup backfill encrypts existing plaintext credentials**; AI-use disclosure.

- [ ] **Step 3: Address CI / CodeRabbit** on the host PR as it reports (same as the SDK PR).

---

## Notes for the implementer
- Do not edit the spec or this plan.
- Task 1 is the risk; the backup branch makes it fully recoverable (`git rebase --abort`, or reset to `backup/pre-main-rebase-20260608`).
- Copy #95's `apiKeyAAD`/`encryptAPIKey`/`DecryptIfEncrypted` usage VERBATIM from `origin/main` so the AAD matches its backfill — a mismatched AAD means existing encrypted rows fail to decrypt on the live box.
- The live redeploy (`SECRET_KEY` provisioning) is out of scope here — it's a deploy-time action gated on this PR merging.

# Single-flight the plugin client launch (fix cold-start thundering herd)

Status: design • 2026-06-08
Scope: `internal/plugins` (the host plugin runtime). Standalone fix — unrelated to
the request-router feature; surfaced while testing it.

## Context

Every host call that needs a running plugin (`RequestRouterClient`,
`MetadataProviderClient`, `ScanSourceClient`, … — 9 getters) funnels through
`Service.ensureClient(ctx, installationID)`:

```
ensureClient → host.Client(id)
                 ├─ found + healthy        → reuse
                 ├─ ErrClientNotFound       → s.Start(id) → host.Start(...) [launch process]
                 ├─ ErrPluginUnhealthy      → host.Stop + s.Start
                 └─ manifest drift          → host.Stop + s.Start
```

`Host.Start` deliberately releases its mutex during the slow launch (process spawn
+ go-plugin handshake + `GetManifest`), so it cannot dedupe. When N callers hit a
**cold** installation at once, they all get `ErrClientNotFound` and each calls
`Start` → **N redundant plugin processes** launch concurrently. The launches
contend (CPU, handshake), individual calls stretch to 12–30s, and some exceed the
plugin HTTP client's 30s timeout → user-visible "couldn't load options" errors.
Observed live: opening the Requests admin page with 4 connections right after a
deploy restart fired 4 concurrent option probes and launched ~4 arr processes; the
errors vanished once the plugin warmed (steady-state reuses one cached client).

This is a pre-existing runtime race, not specific to requests — any plugin hit by
concurrent first-use after a restart is affected.

## Goal

Concurrent `ensureClient` calls for the same installation launch **at most one**
plugin process; the others wait for and share that client.

## Non-goals

- No change to `Host.Start`'s semantics (it stays the explicit force-(re)start
  primitive used by hot-reload / manifest-drift / install).
- No change to lazy-start behavior (plugins still start on first use; preload only
  warms the installation/manifest cache, not the process).
- No retry/backoff or timeout tuning — the herd is the root cause; removing it is
  the fix.

## Design

Add a per-installation `singleflight.Group` (from `golang.org/x/sync/singleflight`,
already a dependency and already used in `internal/metadata` + `internal/sections`)
to `Service`, and run the existing `ensureClient` body through it keyed by the
installation id:

- Rename the current `ensureClient` body to `doEnsureClient(ctx, installationID)`
  (unchanged logic).
- New `ensureClient`:
  ```go
  func (s *Service) ensureClient(ctx context.Context, installationID int) (pluginClient, error) {
      key := strconv.Itoa(installationID)
      v, err, _ := s.launchGroup.Do(key, func() (any, error) {
          return s.doEnsureClient(ctx, installationID)
      })
      if err != nil {
          return nil, err
      }
      return v.(pluginClient), nil
  }
  ```

Behavior: the first caller for a cold installation runs `doEnsureClient`
(`Client`-check → `Start` → cache); concurrent callers for the same id block in
`Do` and receive the same `(*Client, error)`. Exactly one `host.Start` runs, so one
process. After `Do` returns, singleflight frees the key, so the next call re-runs
`doEnsureClient` and hits the now-warm cache (fast). The unhealthy / manifest-drift
restart branches inside `doEnsureClient` are also deduped, which is desirable
(concurrent callers should not each restart).

**Context:** standard singleflight — the shared launch runs under the first
caller's `ctx`. `Host.Start` already bounds the launch with its own
`DefaultControlTimeout`. If the first caller cancels mid-launch, waiters get the
error and retry (re-running re-checks the cache). Accepted as a rare edge.

**Keying:** `strconv.Itoa(installationID)` — distinct installations never share a
flight, so different plugins still start in parallel.

## Error handling

`singleflight.Do` returns the shared fn's error to every concurrent caller — a
failed launch fails all current waiters identically (same as today, minus the
redundant processes). The next call after the flight completes re-attempts.

## Testing

`internal/plugins` test with a fake `Host` (or `pluginClient` factory) whose
`Start` increments an atomic counter and blocks on a channel/short sleep to force
overlap:

1. **Herd collapses to one launch:** launch N (e.g. 20) goroutines calling
   `ensureClient(ctx, 7)` simultaneously (released together via a `sync.WaitGroup`
   / start barrier); assert `Start` call count == 1 and every goroutine received
   the same non-nil client.
2. **Post-completion reuse:** after the flight, a fresh `ensureClient(ctx, 7)`
   returns the cached client without another `Start` (count stays 1).
3. **Distinct installations are independent:** concurrent `ensureClient` for ids 7
   and 8 each launch once (count per id == 1) — different keys don't serialize.
4. **Failed launch propagates:** a fake `Start` returning an error makes all
   concurrent callers receive that error; a subsequent call re-attempts.

Run via the libvips container if `internal/plugins` requires CGO at test time;
otherwise bare-host `go test ./internal/plugins/` (the package is pure-Go).

Gates: `go test ./internal/plugins/`, `go vet ./internal/plugins/`.

## Rollout

Host-only Go change; no migration, no frontend, no plugin change. Rebuild the
silo-server image (`Dockerfile.deploy`, vendored) + redeploy. After deploy, the
first multi-connection page load no longer spawns redundant processes.

# Plugin-Platform SDK Helpers Design (code-review #10)

**Date:** 2026-06-08
**Status:** Approved (brainstorming)
**Related:** `2026-06-07-requests-pluginization-design.md`, `2026-06-07-schema-driven-config-form-design.md`, `2026-06-07-seerr-request-router-design.md`. Sibling follow-up (separate spec): code-review #9 (typed 4K/tier signal in the contract).

## Goal

Extract the duplicated request_router plugin "platform" code into shared `silo-plugin-sdk` helpers, then migrate the two existing plugins (`silo-plugin-requests-arr`, `silo-plugin-requests-seerr`) onto them, so the next plugin author implements only backend-specific logic instead of re-copying the boilerplate.

## Why (the problem)

Two plugins now carry near-verbatim copies of two pieces of infrastructure:

1. **An outbound HTTP client** (`internal/arr/client.go`, `internal/seerr/client.go`) — `New`/`DoJSON`/`GetJSON`/`PostJSON`, the `X-Api-Key` credential header, a 1 MiB response-body cap, base-URL trimming, and error types. It is **byte-identical except the error model**, and the copies have **already diverged**: arr has `DecodeError` + `IsEmptyOrTruncatedDecodeError` empty-body tolerance that seerr's copy dropped — which produced a real false-failure bug (seerr CR finding #1). This is security-sensitive code (it carries the credential header), so divergence is a liability, not just duplication.

2. **The `main.go` bootstrap** (~50 lines) — `//go:embed manifest.json`, the self-checksum (`sha256` of the running binary via `os.Executable`), the `runtimeServer` type with `GetManifest`/`Configure`, and the `runtime.Serve` wiring. The two files are **byte-identical except one import path**. Both also hand-roll `runtimeServer` by embedding `pluginv1.UnimplementedRuntimeServer` directly, which leaves the host broker unhooked (`runtime.Host()` returns nil forever) — harmless for today's stateless plugins but a latent trap.

Every fix to either piece (timeout/retry tuning, redacting the API key from a logged error body, a checksum-scheme change, a new Runtime RPC) must currently be mirrored across every plugin by hand; the missed repo ships a divergent — possibly buggy or insecure — behavior. The cost scales with plugin count, and the architecture exists to grow that count.

## Scope

**In scope:**
- New SDK package `pkg/pluginsdk/httpclient` (the unified outbound client).
- New SDK function `manifest.LoadWithChecksum`.
- New SDK function `runtime.ServeManifest`.
- Migrating `silo-plugin-requests-seerr` and `silo-plugin-requests-arr` onto all three.

**Out of scope:**
- Any change to silo-server (`/opt/silo`). This is purely SDK + the two plugin repos.
- The `request_router.proto` contract / the 4K-tier signal — that is code-review #9, its own spec.
- Other plugins (tmdb, tvdb, autoscan-arr): not migrated here. They may adopt the helpers later; this spec does not touch them.
- Retry/backoff, gzip, circuit-breaking, or any new client behavior beyond what the two plugins do today. The unified client is behavior-preserving (with one deliberate exception, below), not a feature expansion.

**Local-only:** commits only; no push/tag/PR. The machine-local `replace github.com/Silo-Server/silo-plugin-sdk => /opt/silo-plugin-sdk` stays in both plugins' `go.mod`. The SDK work lands on its existing branch `feat/request-router-capability`; each plugin on its `master`.

**Repos / toolchain:** SDK `/opt/silo-plugin-sdk` (module `github.com/Silo-Server/silo-plugin-sdk`, go 1.26 — multi-`%w` available). Plugins `/opt/silo-plugin-requests-arr`, `/opt/silo-plugin-requests-seerr`. Go toolchain `/opt/deployarr/.local/go-sdk/go/bin/go` (pure Go, bare-host build). No outbound-HTTP helper exists in the SDK today; this is greenfield (the existing `pkg/pluginsdk/runtimehost` is a host-proxied plugin→host gRPC client — unrelated).

## The error-model decision

arr and seerr take **opposite** approaches to an empty 2xx body. arr surfaces it (a `DecodeError` wrapping `io.EOF`, detected by `IsEmptyOrTruncatedDecodeError`, then recovered by an id-lookup); seerr swallows it (returns nil, then the router checks `id <= 0` and recovers). The shared client must pick one.

**Decision: the shared client swallows a fully-empty 2xx body (`io.EOF` → return nil, zero-value `dest`); consumers recover by checking the decoded zero value (e.g. `id == 0` → look up by external id).** This is seerr's pattern (and exactly the pattern written into seerr during its code-review fixes). A **truncated** body (`io.ErrUnexpectedEOF`, i.e. the stream cut mid-JSON) becomes a plain decode error — it is NOT swallowed (a half-delivered response cannot be trusted).

Consequence — the one deliberate behavior change: arr currently auto-recovers from a *truncated* 201 too (via `IsEmptyOrTruncatedDecodeError`). Under the unified client, a truncated 201 fails the target instead of recovering. This is acceptable and arguably correct: a truncated response is a genuine error, the case is rare (it requires a mid-stream cut on a local/LAN call to Sonarr/Radarr), and it self-heals on retry (arr's re-add hits "already exists"). The *empty*-body recovery — the common Radarr/Sonarr case and the stated must-keep behavior — is fully preserved.

This collapses the shared error surface to a single type (`*StatusError`) with no `DecodeError`/`IsEmptyOrTruncated`/sentinel machinery, and makes both plugins converge on one recovery pattern.

## Component 1 — `pkg/pluginsdk/httpclient`

A stateless, credential-carrying JSON client. Package `httpclient`, import `github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/httpclient`.

```go
package httpclient

const maxResponseBody = 1 << 20 // 1 MiB

// Client talks to one external API instance over HTTP with an X-Api-Key header.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// New builds a client. A nil hc gets a default 30s-timeout client. baseURL is
// right-trimmed of "/"; apiKey is space-trimmed.
func New(baseURL, apiKey string, hc *http.Client) *Client

func (c *Client) GetJSON(ctx context.Context, path string, dest any) error
func (c *Client) PostJSON(ctx context.Context, path string, body, dest any) error
func (c *Client) DoJSON(ctx context.Context, method, path string, body, dest any) error

// StatusError is any non-2xx response. Message is the parsed {"message":...}
// field when present, else the trimmed raw body; Body is always the raw trimmed
// body. Pointer receiver so errors.As(err, &se) works.
type StatusError struct {
	StatusCode int
	Body       string
	Message    string
}
func (e *StatusError) Error() string // "httpclient: HTTP %d[: %s]" using Message||Body
```

Behavior (preserves the two plugins' shared scaffolding exactly):
- `DoJSON` errors early if `baseURL` or `apiKey` is empty.
- Encodes `body` as JSON when non-nil; builds `http.NewRequestWithContext(ctx, method, baseURL+path, reader)`.
- Headers: always `Accept: application/json` and `X-Api-Key: <apiKey>`; `Content-Type: application/json` only when there is a body.
- `resp.StatusCode >= 400` → `&StatusError{StatusCode, Body: trimmed-raw, Message: parseMessage(raw)}` (read through `io.LimitReader(body, maxResponseBody)`). `parseMessage` extracts `{"message": ...}` and falls back to the trimmed raw body (seerr's helper, now the default — arr ignores `Message`, so no regression).
- Success: `dest == nil || StatusCode == 204` → drain + return nil. Else decode through the 1 MiB `LimitReader`; if `Decode` returns **bare `io.EOF`** (empty body) → return nil (success, no content); any other decode error (incl. `io.ErrUnexpectedEOF`) → `fmt.Errorf("httpclient: decode response: %w", err)`.
- No per-status sentinels: consumers branch on `se.StatusCode` (seerr: 409 duplicate, 404 deleted).

**Tests** (`httpclient_test.go`, `net/http/httptest`): X-Api-Key + Accept set on GET and POST; Content-Type only with a body; base-URL trailing-slash trim; empty base-URL / empty key → error; a non-2xx with a `{"message":"x"}` body → `*StatusError{StatusCode, Message:"x"}` recoverable via `errors.As`; a non-2xx with a plain body → `Message == Body == raw`; a 2xx with an empty body → nil error + zero-value dest; a 2xx with a valid body → decoded; a 2xx with a truncated body → a non-nil decode error (NOT swallowed); the 1 MiB cap applies.

## Component 2 — `manifest.LoadWithChecksum`

Added to the existing `pkg/pluginsdk/manifest` package (real package name `manifest`; plugins alias it `publicmanifest`).

```go
// LoadWithChecksum loads an embedded manifest, optionally overrides its version,
// and stamps Checksum with the hex sha256 of the *running binary*. It reads
// os.Executable() itself, so it cannot be precomputed at SDK build time.
func LoadWithChecksum(embedded []byte, version string) (*pluginv1.PluginManifest, error)
```

Body = the current per-plugin `loadManifest` verbatim: `Load(embedded)` (existing, validates) → `if version != "" { m.Version = version }` → `os.Executable()` → `os.ReadFile(exe)` → `sha256.Sum256` → `m.Checksum = hex.EncodeToString(sum[:])`. Wraps each error with context.

**Test:** write a tiny valid manifest to a temp embed, call `LoadWithChecksum(bytes, "9.9.9")`, assert `Version == "9.9.9"` and `Checksum == hex(sha256(<test-binary>))` (read `os.Executable()` in the test to compute the expected). A `version == ""` call leaves the manifest's declared version untouched.

## Component 3 — `runtime.ServeManifest`

Added to `pkg/pluginsdk/runtime`. Collapses the `main.go` boilerplate and fixes the unhooked-broker gap.

```go
// ServeManifest loads+checksums the embedded manifest, installs a default
// Runtime server (manifest + no-op Configure, with the host broker wired via
// runtimedefault.Server), and serves the given capability servers. The caller
// supplies only the non-Runtime capability servers; ServeManifest sets Runtime.
// It never returns: on a fatal manifest error it panics (a misbuilt plugin
// cannot serve), matching today's main() which panics on loadManifest error.
func ServeManifest(manifestBytes []byte, version string, servers CapabilityServers)
```

Internals:
- A private `manifestRuntime` type embeds `runtimedefault.Server` (which provides `BindHostBroker` → `SetHostBrokerID`, so `runtime.Host()` works for any future plugin that needs a host-proxied call) and holds the loaded `*pluginv1.PluginManifest`. It implements `GetManifest` (returns the manifest) and `Configure` (no-op `&ConfigureResponse{}`).
- `ServeManifest` calls `manifest.LoadWithChecksum(manifestBytes, version)` (panic on error), sets `servers.Runtime = &manifestRuntime{manifest: m}`, and calls `Serve(ServeConfig{Servers: servers})`.
- Generalized to any capability: a request_router plugin passes `CapabilityServers{RequestRouter: router.New()}`; a future metadata plugin passes `CapabilityServers{MetadataProvider: ...}`. Plugins that need a real (non-no-op) `Configure` keep using the lower-level `runtime.Serve` directly (unchanged, still exported).

**Note on `Configure`:** today both plugins' `Configure` is a no-op. `ServeManifest` bakes in the no-op default. This is sufficient for all current capabilities; a config-consuming plugin is a future concern handled via the lower-level `Serve`. Not a placeholder — a deliberate YAGNI boundary.

**Test** (in the `runtime` package): a `manifestRuntime` built from a test manifest returns it from `GetManifest` and an empty `ConfigureResponse` from `Configure`; `BindHostBroker` is reachable (inherited from `runtimedefault.Server`) — assert the type satisfies `pluginv1.RuntimeServer` and that `BindHostBroker` sets the broker id (or at minimum compiles + the method is promoted). `ServeManifest` itself blocks on `plugin.Serve`, so it is exercised indirectly via the plugins' existing manifest-load tests rather than a direct unit test.

## Component 4 — Migrate `silo-plugin-requests-seerr`

seerr already matches the target error/empty-body model, so this is mostly deletion + retargeting:
- **Delete** `internal/seerr/client.go`'s `Client`, `New`, `DoJSON`/`GetJSON`/`PostJSON`, `APIError`, `parseMessage`, `maxResponseBody`, and the `ErrDuplicate` sentinel + its 409 wrapping. Keep `ErrNotFound` **only if** it's still used as a plugin-local sentinel (it is — `FindExistingRequest` raises it; it is not an HTTP-layer error). Move `ErrNotFound` to `internal/seerr/api.go` (or a small `errors.go`) so `client.go` can be removed entirely.
- `internal/seerr/api.go`: `CreateRequest`/`GetRequest`/`FindExistingRequest`/`Me` take a `*httpclient.Client` (constructed by the router via `httpclient.New(baseURL, apiKey, nil)`), calling `GetJSON`/`PostJSON` as before.
- `internal/router/server.go`: replace `seerr.New(...)` with `httpclient.New(...)`; the duplicate-detection branch `errors.Is(err, seerr.ErrDuplicate)` becomes `var se *httpclient.StatusError; errors.As(err, &se) && se.StatusCode == http.StatusConflict`; the 404 branch becomes `errors.As(err, &se) && se.StatusCode == http.StatusNotFound`. The empty-body/id-recovery path (`created.ID <= 0`) is unchanged.
- `main.go`: replace the `runtimeServer`/`loadManifest`/`runtime.Serve` block with `runtime.ServeManifest(manifestJSON, version, runtime.CapabilityServers{RequestRouter: router.New()})`. Keep `//go:embed manifest.json` + `var version`.
- `go.mod`: no new external deps (httpclient is in the already-required SDK). Run `go mod tidy`.
- All existing seerr tests stay green; update any test that referenced the deleted `seerr.APIError`/`seerr.ErrDuplicate` to the `httpclient.StatusError` equivalents.

## Component 5 — Migrate `silo-plugin-requests-arr`

The behavior-change one (empty-body recovery trigger):
- **Delete** `internal/arr/client.go`'s `Client`, `New`, `DoJSON`/`GetJSON`/`PostJSON`, `HTTPError`, `DecodeError`, `IsEmptyOrTruncatedDecodeError`, `maxResponseBody`. (Keep `AcceptedWithoutResponse` — it's a plugin result helper, not HTTP.)
- **Retarget every arr helper that takes `*arr.Client` to `*httpclient.Client`** — this is broader than just the submit files. `resources.go` (`ListRootFolders`/`ListQualityProfiles`/`ListTags`), `options.go`, `queue.go` (`queueDetails`), and `radarr.go`/`sonarr.go` all currently accept `*arr.Client` and call its `GetJSON`/`PostJSON`; the method names + signatures match `httpclient.Client`, so each is a `*arr.Client` → `*httpclient.Client` parameter-type swap plus constructing `httpclient.New(integration.BaseURL, integration.APIKeyRef, nil)` at the call roots (the `NewRadarrClient`/`NewSonarrClient` wrappers in `radarr.go`/`sonarr.go`).
- **Change the empty-body recovery** in `SubmitMovie`/`SubmitSeries`: today they do `if !IsEmptyOrTruncatedDecodeError(err) { return …, err }` then look up by id. Replace with: `PostJSON(...)` returns nil on an empty body (zero-value `created`); after a nil-error POST, `if created.ID == 0 { recover via findMovieByTMDBID/findSeriesByTVDBID; else AcceptedWithoutResponse(kind) }`. A non-nil error is returned as before. (A truncated body now returns the decode error — the deliberate change above.)
- Any `HTTPError` consumer (the option-listing / status paths that surfaced the typed error) now reads `*httpclient.StatusError` (only the error string is used downstream, so this is a type-name swap).
- `main.go`: replace with `runtime.ServeManifest(manifestJSON, version, runtime.CapabilityServers{RequestRouter: router.New()})`.
- `go.mod`: `go mod tidy`.
- All existing arr tests stay green. The arr client tests that asserted `DecodeError`/`IsEmptyOrTruncatedDecodeError` move to assert the new id-check recovery (a 201-with-empty-body submit → recovered id via the lookup stub; a 201-with-truncated-body submit → a returned error). The arr router/server tests are unaffected.

## Build order

SDK first (each consumer compiles against it via the local `replace`), then the plugin that already matches the model, then the behavior-change plugin:

1. **SDK:** `httpclient` (TDD) → `manifest.LoadWithChecksum` (TDD) → `runtime.ServeManifest` (TDD). Commit each. `go build ./... && go test ./...` green in the SDK after each.
2. **seerr migration:** retarget to `httpclient` + `ServeManifest`, delete the local client, fix tests. `go build ./... && go vet ./... && go test ./...` green.
3. **arr migration:** retarget + the empty-body recovery change + fix tests. `go build ./... && go vet ./... && go test ./...` green.

After all three: both plugins build + test green against the modified SDK; the SDK builds + tests green; `git -C /opt/silo status --porcelain` is clean (zero host changes).

## Testing summary

- SDK `httpclient`: full `httptest` suite (auth header, content-type, base-url, errors, empty/truncated/valid body, message parsing, 1 MiB cap) — see Component 1.
- SDK `manifest.LoadWithChecksum`: version override + checksum == hex sha256 of the running test binary.
- SDK `runtime`: `manifestRuntime` satisfies `RuntimeServer`, returns the manifest, no-op Configure, broker method promoted.
- seerr + arr: their existing suites stay green (the migration is behavior-preserving except arr's truncated-body edge); arr's client-level tests move from `IsEmptyOrTruncatedDecodeError` assertions to id-check-recovery assertions.

## Risks & notes

- **arr truncated-2xx behavior change** (documented above): the single intentional behavior difference. Empty-body recovery — the must-keep behavior — is preserved.
- **Credential safety:** consolidating the credential-carrying client into one tested package is a net security improvement (one place to review/redact, no drift). The unified client must continue to never log the API key (it doesn't today; tests should not assert key contents in error strings).
- **Pre-publish coupling:** this refactor changes the SDK's public surface (new package + two funcs) and both plugins' `main.go`. It should land **before** the plugins are published (the pre-publish checklist swaps the `go.mod replace` for a published SDK version anyway), so the published plugins ship on the shared helpers rather than needing an immediate follow-up. Sequencing-wise this slots into the pre-publish window.
- **`runtimedefault.Server` adoption** fixes the latent unhooked-broker gap for free; no current plugin calls `runtime.Host()`, so there is no behavior change today, only a removed future trap.
- **Other first-party plugins** (tmdb/tvdb/autoscan-arr) are untouched; they can adopt `httpclient`/`ServeManifest` opportunistically later.

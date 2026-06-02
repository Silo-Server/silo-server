# Sonarr/Radarr Scan-Source Plugin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a new installable Silo plugin that implements `scan_source.v1` for Sonarr/Radarr: when the host pulls, it polls arr `/history`, extracts imported + renamed paths, applies its own path rewrites, and returns Silo-native paths + a marker.

**Architecture:** A standalone Go module structured like `silo-plugin-tmdb` — a `manifest.json` declaring `scan_source.v1`, a `main.go` serving `runtime.CapabilityServers{Runtime, ScanSource}` via `runtime.Serve`, and the arr-specific logic ported from the closed silo-server PR #43 (`internal/autoscan/history.go`, `rewrite.go`, `suggest.go`).

**Tech Stack:** Go, `github.com/Silo-Server/silo-plugin-sdk` (`v0.5.0`, the `scan_source.v1` capability), `hashicorp/go-plugin` runtime (via the SDK), arr HTTP API (`X-Api-Key`).

Commands assume the **new plugin repository root** is the cwd (suggested checkout: `/opt/silo-plugin-autoscan-arr`, a real sibling like `/opt/silo-plugin-sdk`).

---

## Prerequisites

1. **SDK `v0.5.0`.** Needs `scan_source.v1` from `silo-plugin-sdk` PR #2. Until tagged, depend on the local checkout via replace:
   ```bash
   go mod edit -replace github.com/Silo-Server/silo-plugin-sdk=/opt/silo-plugin-sdk
   ```
   Task 8 finalizes to `v0.5.0`.
2. **Template:** `silo-plugin-tmdb` (checked out at `/opt/silo-plugin-tmdb`) — copy its `main.go` runtime scaffold, `Makefile`, and `manifest.json` shape.
3. **Salvage source:** the arr logic is on silo-server `main` at `internal/autoscan/history.go` (imports + renames extraction, bounded window), `rewrite.go` (`applyRewrites`, `normalizeSeparators`), `suggest.go`/`suggest_deps.go` (root-folder suffix-match suggester). Port these verbatim where possible.
4. **New repo:** create `Silo-Server/silo-plugin-autoscan-arr` (or fork pattern). The plugin is registered in the `silo-plugins` catalog in a separate follow-up (Task 9 notes it).

---

## File Structure

| File | Responsibility | Action |
|---|---|---|
| `go.mod` | module `github.com/Silo-Server/silo-plugin-autoscan-arr`; SDK dep | Create |
| `manifest.json` | declares `scan_source.v1` capability + `global_config_schema` (arr URL/key, rewrites) | Create |
| `main.go` | `runtimeServer` (GetManifest/Configure) + `scanSourceServer` (PollChanges); `runtime.Serve` | Create |
| `internal/arr/history.go` | poll `/history`, imports + renames, bounded window | Create (port) |
| `internal/arr/rewrite.go` | `applyRewrites`, `normalizeSeparators` | Create (port) |
| `internal/arr/client.go` | arr HTTP client (`X-Api-Key`, response cap, timeout) | Create |
| `internal/config/config.go` | parse Configure payload → `{baseURL, apiKey, rewrites, sinceWindow}` | Create |
| `Makefile` | build (cross-platform), like tmdb | Create |

---

## Task 1: Scaffold the plugin (serves an empty `scan_source.v1`)

**Files:** Create `go.mod`, `manifest.json`, `main.go`, `Makefile`.

- [ ] **Step 1: Module + SDK dep**

```bash
go mod init github.com/Silo-Server/silo-plugin-autoscan-arr
go mod edit -replace github.com/Silo-Server/silo-plugin-sdk=/opt/silo-plugin-sdk
go get github.com/Silo-Server/silo-plugin-sdk@v0.4.0   # replace points at the scan_source branch checkout
```

- [ ] **Step 2: manifest.json**

Mirror tmdb's shape, declaring the scan source:

```json
{
  "plugin_id": "silo.autoscan.arr",
  "version": "0.1.0",
  "checksum": "__CHECKSUM__",
  "silo_api_version": "v1",
  "supported_platforms": [
    {"os": "linux", "arch": "amd64"},
    {"os": "linux", "arch": "arm64"},
    {"os": "darwin", "arch": "arm64"}
  ],
  "capabilities": [
    {
      "type": "scan_source.v1",
      "id": "arr",
      "display_name": "Sonarr / Radarr",
      "description": "Triggers Silo rescans from Sonarr/Radarr import and rename history."
    }
  ],
  "global_config_schema": [
    {"key": "base_url", "type": "string", "required": true, "display_name": "Server URL"},
    {"key": "api_key", "type": "secret", "required": true, "display_name": "API Key"},
    {"key": "path_rewrites", "type": "json", "required": false, "display_name": "Path rewrites"}
  ]
}
```

(Confirm the exact `global_config_schema` entry shape against `silo-plugin-sdk` `pkg/pluginsdk/config` — adapt field names to the SDK's config schema type.)

- [ ] **Step 3: main.go scaffold**

Copy tmdb's `runtimeServer` (GetManifest from embedded manifest; Configure stores config) and add a `scanSourceServer` returning empty for now:

```go
type scanSourceServer struct{ cfg *config.Config }

func (s *scanSourceServer) PollChanges(ctx context.Context, req *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error) {
	return &pluginv1.PollChangesResponse{}, nil
}

func main() {
	manifest := mustLoadManifest()
	rt := &runtimeServer{manifest: manifest}
	runtime.Serve(runtime.ServeConfig{
		Servers: runtime.CapabilityServers{
			Runtime:    rt,
			ScanSource: &scanSourceServer{cfg: rt.cfg},
		},
	})
}
```

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 5: Commit**

```bash
git init && git add -A
git commit -m "feat: scaffold autoscan-arr scan_source.v1 plugin"
```

---

## Task 2: Config parsing (Configure → arr connection + rewrites)

**Files:** Create `internal/config/config.go`, `internal/config/config_test.go`.

- [ ] **Step 1: Failing test**

```go
func TestParseConfig(t *testing.T) {
	cfg, err := Parse(map[string]string{
		"base_url": "http://sonarr:8989",
		"api_key":  "KEY",
		"path_rewrites": `[{"from":"/mnt/arr/tv","to":"/mnt/media/tv"}]`,
	})
	if err != nil { t.Fatalf("Parse: %v", err) }
	if cfg.BaseURL != "http://sonarr:8989" || cfg.APIKey != "KEY" { t.Fatalf("bad creds: %+v", cfg) }
	if len(cfg.Rewrites) != 1 || cfg.Rewrites[0].From != "/mnt/arr/tv" { t.Fatalf("bad rewrites: %+v", cfg.Rewrites) }
}
```

- [ ] **Step 2: Run → fail.** `go test ./internal/config/` → undefined `Parse`.

- [ ] **Step 3: Implement** `Parse(map[string]string) (*Config, error)` with `Config{BaseURL, APIKey string; Rewrites []Rewrite}` where `Rewrite{From, To string}`; unmarshal `path_rewrites` JSON. Wire `runtimeServer.Configure` to store the parsed config so `scanSourceServer` reads it.

- [ ] **Step 4: Run → pass.**

- [ ] **Step 5: Commit** `feat(config): parse arr connection and path rewrites`.

---

## Task 3: arr history client (imports + renames, bounded window)

**Files:** Create `internal/arr/client.go`, `internal/arr/history.go`, `internal/arr/history_test.go`.

Port from silo-server `main:internal/autoscan/history.go` (the `ChangedPaths` logic: `downloadFolderImported.importedPath`; `episodeFileRenamed`/`movieFileRenamed` → `path` + `sourcePath`; deletes ignored) and the client (`X-Api-Key`, 1 MiB cap, timeout). The marker is an RFC3339 timestamp; empty marker ⇒ "now".

- [ ] **Step 1: Failing test** — copy the salvaged `history_test.go` (`TestArrHistoryChangedPaths`: imports + both rename paths returned, `grabbed`/`episodeFileDeleted` ignored), renamed to this package, asserting the returned `(paths, nextMarker)`.

- [ ] **Step 2: Run → fail.**

- [ ] **Step 3: Implement** `ChangedPaths(ctx, baseURL, apiKey, since time.Time) (paths []string, newest time.Time, err error)` — port `history.go`, additionally returning the newest history timestamp as the next marker. Apply the bounded window (24h max-lookback floor + overlap buffer) here.

- [ ] **Step 4: Run → pass.**

- [ ] **Step 5: Commit** `feat(arr): poll history for imports and renames`.

---

## Task 4: Path rewrites (→ Silo-native paths)

**Files:** Create `internal/arr/rewrite.go`, `internal/arr/rewrite_test.go`.

Port `applyRewrites` (boundary-safe prefix: `path==trimmed || HasPrefix(path, trimmed+"/")`) and `normalizeSeparators` from silo-server `main:internal/autoscan/rewrite.go`.

- [ ] **Step 1: Failing test** — copy salvaged `rewrite_test.go` cases.
- [ ] **Step 2: Run → fail.**
- [ ] **Step 3: Implement** the port.
- [ ] **Step 4: Run → pass.**
- [ ] **Step 5: Commit** `feat(arr): boundary-safe path rewrites`.

---

## Task 5: Wire `PollChanges` end-to-end

**Files:** Modify `main.go` (`scanSourceServer.PollChanges`), add `main_test.go`.

- [ ] **Step 1: Failing test** — a `scanSourceServer` configured with a stub arr server (httptest) and one rewrite; assert `PollChanges` with empty marker returns rewritten Silo-native paths and a non-empty `next_marker`; a second call with that marker queries arr with the right `since`.

- [ ] **Step 2: Run → fail.**

- [ ] **Step 3: Implement**

```go
func (s *scanSourceServer) PollChanges(ctx context.Context, req *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error) {
	since := time.Time{} // empty marker => now (history client floors to now)
	if m := req.GetMarker(); m != "" {
		if t, err := time.Parse(time.RFC3339, m); err == nil { since = t }
	}
	raw, newest, err := arr.ChangedPaths(ctx, s.cfg.BaseURL, s.cfg.APIKey, since)
	if err != nil { return nil, err }
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		out = append(out, arr.ApplyRewrites(arr.NormalizeSeparators(p), s.cfg.Rewrites))
	}
	return &pluginv1.PollChangesResponse{ChangedPaths: out, NextMarker: newest.UTC().Format(time.RFC3339)}, nil
}
```

- [ ] **Step 4: Run → pass.**

- [ ] **Step 5: Commit** `feat: PollChanges returns Silo-native paths from arr history`.

---

## Task 6: Rewrite suggester ("Sync from arr") — root folders

**Files:** Create `internal/arr/suggest.go`, `suggest_test.go`.

Port `suggestRewrites` (suffix-match arr root folders → Silo folders) from `main:internal/autoscan/suggest.go`. **Open question (spec §14 risk #2):** how the operator triggers "Sync" and how the plugin reaches Silo's media-folder list.

- [ ] **Step 1:** Decide the delivery mechanism — recommended: the plugin reads Silo's folder list via the host library-listing service (`pluginhost` library lister, reachable through the SDK `runtime.Host()` client). Confirm that path exists in `v0.5.0`; if not, scope "Sync" to a follow-up and ship Tasks 1–5 (manual rewrites only) first.
- [ ] **Step 2:** Port the suffix-match suggester + its tests (unique/ambiguous/covered/no-op/normalization cases from `suggest_test.go`).
- [ ] **Step 3:** Expose it — either via an `http_routes.v1` capability the admin UI calls, or as part of the plugin config flow. Document the chosen mechanism.
- [ ] **Step 4:** Tests pass; commit `feat(arr): suffix-match rewrite suggester`.

> If §14 risk #2 is unresolved at execution time, **skip this task**, ship manual rewrites (Tasks 1–5), and file the suggester as a follow-up. The plugin is fully functional without it.

---

## Task 7: Cross-repo smoke test against the host

**Files:** none (verification).

- [ ] **Step 1:** Build the plugin binary; install it into a dev silo-server (with the Part-1 backend) via the plugin installer.
- [ ] **Step 2:** Configure a source pointing at a real/stub arr; trigger an autoscan poll; confirm the host enqueues a scan for an imported path. (This exercises the full SDK contract end-to-end.)
- [ ] **Step 3:** Note results; no commit unless fixes are needed.

---

## Task 8: Finalize SDK dependency

- [ ] After `silo-plugin-sdk v0.5.0` is tagged: `go mod edit -dropreplace ...; go get .../silo-plugin-sdk@v0.5.0; go mod tidy; go build ./...`.
- [ ] Commit `build: depend on silo-plugin-sdk v0.5.0`.
- [ ] Do not publish/tag the plugin with a `replace` directive in `go.mod`.

---

## Task 9: Catalog registration (follow-up, separate repo)

- [ ] Add `silo.autoscan.arr` to the `silo-plugins` catalog manifest so it is installable. This is a separate PR in the `silo-plugins` repo (not this module) — note it; out of scope for this plan's repo.

---

## Self-Review notes

- **Spec coverage:** §10 (arr plugin: history imports + renames, bounded window, rewrites, Silo-native paths) Tasks 3–5; §5 contract (`PollChanges`, opaque marker = RFC3339 timestamp, first-run "now") Task 5; "Sync from arr" §10/§14-risk-2 Task 6 (with an explicit skip path).
- **Salvage:** history/rewrite/suggest ported from the closed PR (Tasks 3,4,6) — exactly the files the host backend plan deletes (Part-1 Task 8).
- **Risk:** §14 risk #1 (one plugin installation per arr server vs. many) — this plugin holds one connection per installation/config; multiple arr servers ⇒ multiple installs unless the runtime supports multiple capability instances. §14 risk #2 (Sync UI) gated in Task 6 with a clean skip.
- **Type consistency:** `Config{BaseURL,APIKey,Rewrites}`, `Rewrite{From,To}`, `arr.ChangedPaths(...) ([]string, time.Time, error)`, `arr.ApplyRewrites`/`NormalizeSeparators`, `scanSourceServer.PollChanges` consistent across Tasks 2–6.

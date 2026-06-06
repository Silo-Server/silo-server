# Autoscan Host Backend Implementation Plan (Part 1 of 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the silo-server backend for the standalone Autoscan category: poll `scan_source.v1` plugins on a timer, resolve their Silo-native paths to library folders, and enqueue rescans — with connections decoupled from Requests.

**Architecture:** Mirror the existing `scheduled_task.v1` host dispatch (`pluginhost.Client` capability wrapper + `plugins.Service` resolver + a `taskmanager` task). Generalize the closed PR #43 autoscan engine (resolve → suppress → enqueue) so its input is "Silo-native paths from a scan-source plugin" instead of "arr history." Replace the old `request_integrations`-coupled schema with `autoscan_connections` + `autoscan_sources`.

**Tech Stack:** Go, pgx/v5, the `internal/plugins`/`internal/pluginhost` runtime, `internal/scantrigger`/`internal/scanqueue`, `internal/taskmanager`, numbered SQL migrations, `github.com/Silo-Server/silo-plugin-sdk` (the new `scan_source.v1` capability).

Commands assume the silo-server repository root is the cwd. This is **Part 1 (backend)**; the Autoscan admin UI is Part 2.

---

## Prerequisites

1. **SDK dependency.** This plan needs the SDK's `scan_source.v1` symbols (`runtime.Client.ScanSource()`, `pluginv1.ScanSourceClient`, `pluginv1.PollChangesRequest/Response`, `capability.ScanSource`). They are in `silo-plugin-sdk` PR #2 but **not yet released**. Until `v0.5.0` is tagged, add a local replace directive so the host builds against the working checkout:

   ```bash
   # from the repository root (cwd)
   go mod edit -replace github.com/Silo-Server/silo-plugin-sdk=../silo-plugin-sdk
   go mod tidy
   ```

   This points the replace at the `silo-plugin-sdk` checkout (a sibling of this repo).

   The final task removes the replace and bumps to the tagged version.

2. **Closed PR #43 is on `main`.** The in-process arr autoscan (`internal/autoscan/*`, `internal/api/handlers/autoscan.go`, `internal/taskmanager/tasks/autoscan_poll.go`, router routes, migration `171_autoscan`, the `AdminRequests.tsx` tab) is the salvage source. This plan **generalizes the engine and replaces the schema/wiring**; it does not preserve the arr-specific history client (that moves to the arr plugin in a separate plan).

3. **Tests that touch `internal/api/handlers` or `internal/api` link libvips via CGO.** Run the suite in the libvips-equipped container (see project memory `silo-test-libvips-gotcha`), not the bare host, or those packages report `[build failed]` and skip silently.

---

## Pattern references (read before starting)

- Host capability client wrapper: `internal/pluginhost/client.go` — `ScheduledTaskClient` type (l.42), `Client.ScheduledTask` accessor (l.111), `requireCapability` (l.157), `Run` (l.226).
- Host resolver: `internal/plugins/service.go:413` `ScheduledTaskClient(ctx, installationID, capabilityID)` → `ensureClient` → `client.ScheduledTask(capabilityID)`.
- Timer task over plugin capabilities: `internal/plugins/task_registry.go` (iterate enabled installations, filter capability type, build a `taskmanager.Task`).
- Salvageable engine (generalize, don't rewrite): `internal/autoscan/service.go` (resolve/suppress/enqueue loop), `dedupe.go` (`uniqueParentDirs`), `suppress.go` (`Suppressor`). Discard `history.go`, `rewrite.go`, `suggest*.go` (arr-specific → arr plugin).
- Migration mechanics: paired `migrations/NNN_*.up.sql`/`.down.sql`, embedded via `migrations/embed.go`, auto-applied on boot.
- Credential decryption for the reuse-from-Requests case: `internal/requests/repository.go` (`integrationColumns` has `base_url, api_key_ref`); Fernet resolution via `catalog.ServerSettingsRepo.Get` (the same path PR #43's `SecretResolver` used).

---

## File Structure

| File | Responsibility | Action |
|---|---|---|
| `internal/pluginhost/client.go` | `ScanSourceClient` wrapper + `Client.ScanSource()` accessor | Modify |
| `internal/pluginhost/client_test.go` | `requireCapability` gate for `scan_source.v1` | Modify |
| `internal/plugins/service.go` | `ScanSourceClient(ctx, installationID, capabilityID)` resolver | Modify |
| `migrations/172_autoscan_v2.up.sql` / `.down.sql` | `autoscan_connections` + `autoscan_sources`; drop the `171` tables | Create |
| `internal/autoscan/types.go` | `Source`, `Connection`, `Settings`, `PollChanges` provider seam | Rewrite |
| `internal/autoscan/repository.go` | CRUD for connections + sources; `AdvanceMarker` | Rewrite |
| `internal/autoscan/provider.go` | `ScanSourceProvider` interface + adapter over the plugins resolver | Create |
| `internal/autoscan/connection.go` | Resolve a `Connection` (own or Requests-linked) → `{base_url, api_key}` | Create |
| `internal/autoscan/service.go` | Generic engine: per source → `PollChanges(marker)` → resolve → suppress → enqueue → store marker | Rewrite |
| `internal/autoscan/dedupe.go`, `suppress.go` | Unchanged salvage | Keep |
| `internal/autoscan/history.go`, `rewrite.go`, `suggest*.go` | Arr-specific | Delete |
| `internal/taskmanager/tasks/autoscan_poll.go` | Poll task → `Service.PollOnce` | Keep (retarget) |
| `internal/api/handlers/autoscan.go` | Admin API (settings/connections/sources/trigger/status) | Rewrite |
| `internal/api/router.go`, `cmd/silo/main.go` | Wire the rebuilt service + connection resolver; drop request-coupled wiring | Modify |
| `go.mod` | SDK replace (dev) → `v0.5.0` (final) | Modify |

---

## Task 1: Host `pluginhost` client wrapper for `scan_source.v1`

**Files:**
- Modify: `internal/pluginhost/client.go`
- Modify: `internal/pluginhost/client_test.go`

- [ ] **Step 1: Write the failing test**

In `internal/pluginhost/client_test.go`, add a test mirroring the existing `scheduled_task` capability-gate test (find it first; it constructs a `Client` with a manifest declaring a capability and asserts the accessor errors when the capability is absent and succeeds when present):

```go
func TestClientScanSourceRequiresCapability(t *testing.T) {
	// Client whose manifest does NOT declare scan_source.v1
	c := newTestClientWithCapabilities(t, "metadata_provider.v1")
	if _, err := c.ScanSource("missing"); err == nil {
		t.Fatal("ScanSource: expected error when capability absent")
	}
	// Client WHOSE manifest declares scan_source.v1 with id "arr"
	c2 := newTestClientWithCapabilities(t, "scan_source.v1:arr")
	if _, err := c2.ScanSource("arr"); err != nil {
		t.Fatalf("ScanSource: unexpected error when capability present: %v", err)
	}
}
```

Use whatever helper the sibling `scheduled_task` test uses to construct a `Client` with declared capabilities; mirror it exactly (do not invent a helper that does not exist — read `client_test.go` and copy the construction).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/pluginhost/ -run TestClientScanSourceRequiresCapability`
Expected: FAIL — `c.ScanSource undefined`.

- [ ] **Step 3: Add the wrapper type and accessor**

In `internal/pluginhost/client.go`, after the `ScheduledTaskClient` type (l.42), add:

```go
type ScanSourceClient struct {
	client  pluginv1.ScanSourceClient
	timeout time.Duration
}
```

After the `Client.ScheduledTask` accessor (l.117), add:

```go
func (c *Client) ScanSource(capabilityID string) (*ScanSourceClient, error) {
	if err := c.requireCapability("scan_source.v1", capabilityID); err != nil {
		return nil, err
	}
	return &ScanSourceClient{
		client:  c.rpc.ScanSource(),
		timeout: DefaultControlTimeout,
	}, nil
}

func (c *ScanSourceClient) PollChanges(ctx context.Context, req *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.PollChanges(callCtx, req)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/pluginhost/ -run TestClientScanSourceRequiresCapability`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pluginhost/client.go internal/pluginhost/client_test.go
git commit -m "feat(pluginhost): scan_source.v1 capability client wrapper"
```

---

## Task 2: `plugins.Service` resolver for `scan_source.v1`

**Files:**
- Modify: `internal/plugins/service.go`

- [ ] **Step 1: Add the resolver method**

After `ScheduledTaskClient` (l.413–423), add the identical shape for scan source:

```go
func (s *Service) ScanSourceClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*pluginhost.ScanSourceClient, error) {
	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return client.ScanSource(capabilityID)
}
```

- [ ] **Step 2: Build to verify it compiles**

Run: `go build ./internal/plugins/`
Expected: exits 0.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/service.go
git commit -m "feat(plugins): expose scan_source.v1 client resolver"
```

---

## Task 3: Schema — `autoscan_connections` + `autoscan_sources`

**Files:**
- Create: `migrations/172_autoscan_v2.up.sql`, `migrations/172_autoscan_v2.down.sql`

- [ ] **Step 1: Write the up migration**

Create `migrations/172_autoscan_v2.up.sql`. It drops PR #43's `171` tables and creates the decoupled schema (no FK into `request_integrations`; the optional reuse link is `ON DELETE SET NULL`):

```sql
DROP TABLE IF EXISTS public.autoscan_sources;
DROP TABLE IF EXISTS public.autoscan_settings;

CREATE TABLE public.autoscan_settings (
    id boolean PRIMARY KEY DEFAULT true,
    enabled boolean NOT NULL DEFAULT false,
    default_poll_interval_seconds integer NOT NULL DEFAULT 600,
    debounce_seconds integer NOT NULL DEFAULT 60,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT autoscan_settings_singleton CHECK (id),
    CONSTRAINT autoscan_settings_interval_pos CHECK (default_poll_interval_seconds > 0),
    CONSTRAINT autoscan_settings_debounce_nonneg CHECK (debounce_seconds >= 0)
);
INSERT INTO public.autoscan_settings (id) VALUES (true) ON CONFLICT (id) DO NOTHING;

CREATE TABLE public.autoscan_connections (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,
    kind text NOT NULL,
    -- Own credentials (used when request_integration_id IS NULL):
    base_url text,
    api_key_ref text,
    -- Optional soft reuse of a Requests arr server (live link; SET NULL on delete):
    request_integration_id text REFERENCES public.request_integrations(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT autoscan_connections_source_present
        CHECK (request_integration_id IS NOT NULL OR base_url IS NOT NULL)
);

CREATE TABLE public.autoscan_sources (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    installation_id integer NOT NULL,
    capability_id text NOT NULL,
    connection_id uuid NOT NULL REFERENCES public.autoscan_connections(id) ON DELETE RESTRICT,
    enabled boolean NOT NULL DEFAULT false,
    poll_interval_seconds integer,
    marker text,
    last_run_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT autoscan_sources_interval_pos CHECK (poll_interval_seconds IS NULL OR poll_interval_seconds > 0),
    CONSTRAINT autoscan_sources_capability_unique UNIQUE (installation_id, capability_id)
);
```

- [ ] **Step 2: Write the down migration**

Create `migrations/172_autoscan_v2.down.sql`:

```sql
DROP TABLE IF EXISTS public.autoscan_sources;
DROP TABLE IF EXISTS public.autoscan_connections;
DROP TABLE IF EXISTS public.autoscan_settings;
```

(Recreating the `171` tables on down-migration is unnecessary: `171` is unreleased and superseded; the down simply removes `172`'s tables.)

- [ ] **Step 3: Apply and verify against a disposable DB**

Run the migrations against a throwaway Postgres (the project's disposable-DB pattern) and confirm boot applies cleanly:
```bash
docker compose up -d postgres
go run ./cmd/silo --migrate-only 2>&1 | grep -i "migrations applied" || echo "check migration output"
```
Expected: migrations apply without error; `\d autoscan_connections` shows the soft FK `request_integration_id`.

- [ ] **Step 4: Commit**

```bash
git add migrations/172_autoscan_v2.up.sql migrations/172_autoscan_v2.down.sql
git commit -m "feat(migrations): autoscan v2 schema (connections + sources, no requests FK)"
```

---

## Task 4: Types and repository

**Files:**
- Rewrite: `internal/autoscan/types.go`
- Rewrite: `internal/autoscan/repository.go`

- [ ] **Step 1: Define the types**

Replace `internal/autoscan/types.go` with the v2 model:

```go
package autoscan

import "time"

type Settings struct {
	Enabled                    bool
	DefaultPollIntervalSeconds int
	DebounceSeconds            int
}

// Connection is an arr server the host can reach: either own credentials, or a
// live reference to a Requests integration (RequestIntegrationID set).
type Connection struct {
	ID                   string
	Name                 string
	Kind                 string
	BaseURL              string // own; empty when linked
	APIKeyRef            string // own; empty when linked
	RequestIntegrationID *string
}

// Source ties a scan_source plugin capability instance to a connection plus the
// host-owned scheduling/bookkeeping state.
type Source struct {
	ID                  string
	InstallationID      int
	CapabilityID        string
	ConnectionID        string
	Enabled             bool
	PollIntervalSeconds *int    // nil => use settings default
	Marker              *string // opaque; nil on first run
	LastRunAt           *time.Time
	LastError           *string
}
```

- [ ] **Step 2: Write a failing repository test**

Add `internal/autoscan/repository_test.go` exercising round-trip + `AdvanceMarker` against a disposable DB (gate with the project's DB-test build tag/helper if one exists; otherwise a `//go:build integration` tag matching sibling DB tests). Minimal:

```go
func TestRepositoryMarkerRoundTrip(t *testing.T) {
	repo := newTestRepo(t) // disposable-DB helper used by sibling repo tests
	connID := repo.mustCreateConnection(t, Connection{Name: "Sonarr", Kind: "sonarr", BaseURL: "http://x", APIKeyRef: "k"})
	srcID := repo.mustCreateSource(t, Source{InstallationID: 1, CapabilityID: "arr", ConnectionID: connID, Enabled: true})

	if err := repo.AdvanceMarker(ctx, srcID, "2026-06-02T14:10:00Z"); err != nil {
		t.Fatalf("AdvanceMarker: %v", err)
	}
	got, _ := repo.ListEnabledSources(ctx)
	if len(got) != 1 || got[0].Marker == nil || *got[0].Marker != "2026-06-02T14:10:00Z" {
		t.Fatalf("marker not persisted: %+v", got)
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/autoscan/ -run TestRepositoryMarkerRoundTrip` (with the DB helper available)
Expected: FAIL — undefined repository methods.

- [ ] **Step 4: Implement the repository**

Rewrite `internal/autoscan/repository.go` with: `GetSettings`, `UpdateSettings`, connection CRUD (`CreateConnection`, `UpdateConnection`, `DeleteConnection`, `ListConnections`, `GetConnection`), source CRUD (`UpsertSource`, `ListEnabledSources`, `ListSources`, `GetSource`), and `AdvanceMarker(ctx, sourceID, marker string)` (sets `marker`, `last_run_at = now()`, clears `last_error`). Use pgx; follow the column/scan style of the existing (pre-rewrite) `repository.go` and `internal/requests/repository.go`.

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./internal/autoscan/ -run TestRepositoryMarkerRoundTrip`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/autoscan/types.go internal/autoscan/repository.go internal/autoscan/repository_test.go
git commit -m "feat(autoscan): v2 types and repository (connections, sources, markers)"
```

---

## Task 5: Connection resolution (own or Requests-linked)

**Files:**
- Create: `internal/autoscan/connection.go`
- Create: `internal/autoscan/connection_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestResolveConnectionOwnVsLinked(t *testing.T) {
	reqLookup := fakeRequestIntegrationLookup{"req-1": creds{BaseURL: "http://req:7878", APIKeyRef: "rk"}}
	secrets := fakeSecrets{"rk": "REQKEY", "ownref": "OWNKEY"}
	r := NewConnectionResolver(reqLookup, secrets)

	own, err := r.Resolve(ctx, Connection{BaseURL: "http://own:8989", APIKeyRef: "ownref"})
	if err != nil || own.BaseURL != "http://own:8989" || own.APIKey != "OWNKEY" {
		t.Fatalf("own resolve = %+v, err=%v", own, err)
	}
	id := "req-1"
	linked, err := r.Resolve(ctx, Connection{RequestIntegrationID: &id})
	if err != nil || linked.BaseURL != "http://req:7878" || linked.APIKey != "REQKEY" {
		t.Fatalf("linked resolve = %+v, err=%v", linked, err)
	}
}

func TestResolveConnectionLinkedMissingFallsBack(t *testing.T) {
	r := NewConnectionResolver(fakeRequestIntegrationLookup{}, fakeSecrets{})
	id := "gone"
	if _, err := r.Resolve(ctx, Connection{RequestIntegrationID: &id}); err == nil {
		t.Fatal("expected error when linked Requests integration is missing")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/autoscan/ -run TestResolveConnection`
Expected: FAIL — undefined `NewConnectionResolver`.

- [ ] **Step 3: Implement**

Create `internal/autoscan/connection.go`:

```go
package autoscan

import (
	"context"
	"fmt"
)

// ResolvedConnection is concrete credentials handed to the plugin.
type ResolvedConnection struct {
	BaseURL string
	APIKey  string
}

type RequestIntegrationLookup interface {
	// Returns base URL + encrypted api key ref for a Requests integration.
	Get(ctx context.Context, integrationID string) (baseURL, apiKeyRef string, err error)
}

type SecretResolver interface {
	Get(ctx context.Context, ref string) (string, error)
}

type ConnectionResolver struct {
	requests RequestIntegrationLookup
	secrets  SecretResolver
}

func NewConnectionResolver(r RequestIntegrationLookup, s SecretResolver) *ConnectionResolver {
	return &ConnectionResolver{requests: r, secrets: s}
}

func (cr *ConnectionResolver) Resolve(ctx context.Context, c Connection) (ResolvedConnection, error) {
	baseURL, apiKeyRef := c.BaseURL, c.APIKeyRef
	if c.RequestIntegrationID != nil {
		u, ref, err := cr.requests.Get(ctx, *c.RequestIntegrationID)
		if err != nil {
			return ResolvedConnection{}, fmt.Errorf("autoscan: linked requests integration %q: %w", *c.RequestIntegrationID, err)
		}
		baseURL, apiKeyRef = u, ref
	}
	apiKey := apiKeyRef
	if cr.secrets != nil && apiKeyRef != "" {
		resolved, err := cr.secrets.Get(ctx, apiKeyRef)
		if err != nil {
			return ResolvedConnection{}, fmt.Errorf("autoscan: resolve api key: %w", err)
		}
		if resolved != "" {
			apiKey = resolved
		}
	}
	return ResolvedConnection{BaseURL: baseURL, APIKey: apiKey}, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/autoscan/ -run TestResolveConnection`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/autoscan/connection.go internal/autoscan/connection_test.go
git commit -m "feat(autoscan): resolve connections (own credentials or Requests-linked)"
```

---

## Task 6: Provider seam over the plugin resolver

**Files:**
- Create: `internal/autoscan/provider.go`

- [ ] **Step 1: Define the seam and adapter**

The engine must be testable without a live plugin, so it depends on a narrow interface, with a production adapter over `plugins.Service.ScanSourceClient`. Create `internal/autoscan/provider.go`:

```go
package autoscan

import (
	"context"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// ScanSourceProvider yields changed paths for one source. The engine calls
// PollChanges; production wraps the plugins.Service scan_source resolver.
type ScanSourceProvider interface {
	PollChanges(ctx context.Context, installationID int, capabilityID, marker string, conn ResolvedConnection) (paths []string, nextMarker string, err error)
}

// pluginScanSourceClient is the slice of *pluginhost.ScanSourceClient used here.
type pluginScanSourceClient interface {
	PollChanges(ctx context.Context, req *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error)
}

type scanSourceResolver interface {
	ScanSourceClient(ctx context.Context, installationID int, capabilityID string) (pluginScanSourceClient, error)
}

type pluginProvider struct{ resolver scanSourceResolver }

func NewPluginProvider(resolver scanSourceResolver) ScanSourceProvider {
	return &pluginProvider{resolver: resolver}
}

func (p *pluginProvider) PollChanges(ctx context.Context, installationID int, capabilityID, marker string, conn ResolvedConnection) ([]string, string, error) {
	client, err := p.resolver.ScanSourceClient(ctx, installationID, capabilityID)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.PollChanges(ctx, &pluginv1.PollChangesRequest{CapabilityId: capabilityID, Marker: marker})
	if err != nil {
		return nil, "", err
	}
	return resp.GetChangedPaths(), resp.GetNextMarker(), nil
}
```

Note: the connection (`conn`) is delivered to the plugin out-of-band as runtime config when the source is configured (the plugin reads its own `{base_url, api_key}`); it is passed here so a future provider variant could inject per-call. For v1 the production path configures the plugin instance with the resolved connection at upsert time; document this in `NewPluginProvider`'s comment.

- [ ] **Step 2: Build**

Run: `go build ./internal/autoscan/`
Expected: exits 0.

- [ ] **Step 3: Commit**

```bash
git add internal/autoscan/provider.go
git commit -m "feat(autoscan): scan-source provider seam over the plugin resolver"
```

---

## Task 7: Generalize the engine (`Service.PollOnce`)

**Files:**
- Rewrite: `internal/autoscan/service.go`
- Modify: `internal/autoscan/service_test.go` (generalize the salvaged tests)

- [ ] **Step 1: Adapt the salvaged tests to the provider seam**

The PR #43 `service_test.go` used a `fakeHistory`; replace it with a `fakeProvider` implementing `ScanSourceProvider`, keeping the same assertions (dedupe to one folder; distinct paths same folder → two scans; disabled no-op; provider error keeps marker; enqueue failure releases claims + keeps marker). Add an opaque-marker assertion: the provider's returned `nextMarker` is stored verbatim via `AdvanceMarker`.

```go
type fakeProvider struct {
	paths      map[string][]string // key: capabilityID
	nextMarker string
	err        error
}

func (f *fakeProvider) PollChanges(_ context.Context, _ int, capabilityID, _ string, _ ResolvedConnection) ([]string, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	return f.paths[capabilityID], f.nextMarker, nil
}
```

- [ ] **Step 2: Run to verify the tests fail**

Run: `go test ./internal/autoscan/ -run TestPollOnce`
Expected: FAIL — `Service` signature/`fakeProvider` not yet wired.

- [ ] **Step 3: Rewrite the engine**

Rewrite `internal/autoscan/service.go`. Keep the salvaged resolve→suppress→enqueue loop verbatim (it is provider-agnostic already), but drive it from sources + the provider seam:

```go
func (s *Service) PollOnce(ctx context.Context) error {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	if !settings.Enabled {
		return nil
	}
	sources, err := s.store.ListEnabledSources(ctx)
	if err != nil {
		return err
	}
	ttl := time.Duration(settings.DebounceSeconds) * time.Second

	for _, src := range sources {
		conn, cerr := s.resolveConnection(ctx, src.ConnectionID)
		if cerr != nil {
			slog.WarnContext(ctx, "autoscan: resolve connection failed", "source_id", src.ID, "err", cerr)
			continue
		}
		marker := ""
		if src.Marker != nil {
			marker = *src.Marker
		}
		paths, next, perr := s.provider.PollChanges(ctx, src.InstallationID, src.CapabilityID, marker, conn)
		if perr != nil {
			slog.WarnContext(ctx, "autoscan: poll changes failed", "source_id", src.ID, "err", perr)
			_ = s.store.RecordError(ctx, src.ID, perr.Error())
			continue
		}

		targets, claimed := s.resolveAndClaim(ctx, paths, ttl) // salvaged dedupe→resolve→suppress
		if len(targets) > 0 {
			if eerr := s.queue.EnqueueScans(ctx, targets); eerr != nil {
				s.releaseClaims(ctx, claimed)
				slog.WarnContext(ctx, "autoscan: enqueue failed", "source_id", src.ID, "err", eerr)
				continue // do NOT advance marker
			}
		}
		if aerr := s.store.AdvanceMarker(ctx, src.ID, next); aerr != nil {
			slog.WarnContext(ctx, "autoscan: advance marker failed", "source_id", src.ID, "err", aerr)
		}
	}
	return nil
}
```

Extract the salvaged loop body into `resolveAndClaim`/`releaseClaims` helpers (lifted verbatim from PR #43's `service.go` lines that build `targets`/`claimed`). Keep `uniqueParentDirs`, `Suppressor`, the `fmt.Sprintf("%d|%s", folderID, target.Path)` suppression key, and the `scantrigger.RequestError` quiet-skip.

- [ ] **Step 4: Run to verify the tests pass**

Run: `go test ./internal/autoscan/ -run TestPollOnce`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/autoscan/service.go internal/autoscan/service_test.go
git commit -m "feat(autoscan): generic engine drives sources via scan_source provider"
```

---

## Task 8: Delete arr-specific salvage residue

**Files:**
- Delete: `internal/autoscan/history.go`, `history_test.go`, `rewrite.go`, `rewrite_test.go`, `suggest.go`, `suggest_deps.go`, `suggest_test.go`

- [ ] **Step 1: Remove the files**

```bash
git rm internal/autoscan/history.go internal/autoscan/history_test.go \
       internal/autoscan/rewrite.go internal/autoscan/rewrite_test.go \
       internal/autoscan/suggest.go internal/autoscan/suggest_deps.go internal/autoscan/suggest_test.go
```

- [ ] **Step 2: Build to confirm no remaining references**

Run: `go build ./internal/autoscan/...`
Expected: exits 0 (the engine no longer references the deleted arr logic). If it fails, a reference was missed — remove it.

- [ ] **Step 3: Commit**

```bash
git commit -m "refactor(autoscan): drop arr-specific history/rewrite/suggest (moved to plugin)"
```

---

## Task 9: Admin API rewrite

**Files:**
- Rewrite: `internal/api/handlers/autoscan.go`
- Modify: `internal/api/handlers/autoscan_test.go`

- [ ] **Step 1: Define the endpoints**

Rewrite the handler to back the new model. Endpoints (admin-gated, mounted in Task 10):
- `GET/PUT /admin/autoscan/settings`
- `GET/POST /admin/autoscan/connections`, `PUT/DELETE /admin/autoscan/connections/{id}`
- `GET /admin/autoscan/sources`, `PUT /admin/autoscan/sources/{id}` (enable, interval, connection binding)
- `POST /admin/autoscan/trigger` (async `PollOnce`, 202 — keep the detached-goroutine + channel-tested pattern from the closed PR's `autoscan_test.go`)
- `GET /admin/autoscan/status`

Strip credentials from responses (never emit `api_key_ref`/resolved keys), matching the `_SENSITIVE_METADATA_KEYS` defense-in-depth posture.

- [ ] **Step 2: Port + extend the handler tests**

Carry over the trigger test (the `done`-channel synchronization fix from `d3ee373`), the FK→404 test (now: unknown source/connection → 404), and a secrets-not-leaked test for the connections list.

- [ ] **Step 3: Run the handler tests (libvips container)**

Run (in the libvips-equipped container per Prerequisites #3):
`go test ./internal/api/handlers/ -run Autoscan`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/api/handlers/autoscan.go internal/api/handlers/autoscan_test.go
git commit -m "feat(api): autoscan v2 admin endpoints (settings, connections, sources)"
```

---

## Task 10: Wiring + rate limits + retire old coupling

**Files:**
- Modify: `internal/api/router.go`
- Modify: `cmd/silo/main.go`

- [ ] **Step 1: Rebuild the service wiring**

In `internal/api/router.go` and `cmd/silo/main.go`, construct the v2 service:

```go
autoscanRepo := autoscan.NewRepository(deps.DB)
provider := autoscan.NewPluginProvider(pluginServiceScanSourceAdapter{pluginService})
connResolver := autoscan.NewConnectionResolver(requestIntegrationLookup{requestsRepo}, serverSettingsSecretResolver{settingsRepo})
autoscanSvc := autoscan.NewService(autoscanRepo, provider, connResolver, deps.Resolver, deps.ScanQueue, autoscan.NewRedisSuppressor(deps.RedisClient))
autoscanHandler = handlers.NewAutoscanHandler(autoscanRepo, autoscanSvc)
```

Replace the old route block with the Task 9 routes. Keep the `catalog_*`/admin rate-limit conventions consistent with the security standards (per-user/admin caps on the new endpoints; mirror the request-integrations admin tier).

- [ ] **Step 2: Retarget the poll task**

`internal/taskmanager/tasks/autoscan_poll.go` calls `Service.PollOnce` — unchanged behavior; confirm it compiles against the rebuilt service and uses `Settings.DefaultPollIntervalSeconds` for the interval (per-source overrides are honored inside `PollOnce`).

- [ ] **Step 3: Build the whole tree**

Run (libvips container): `go build ./...`
Expected: exits 0.

- [ ] **Step 4: Commit**

```bash
git add internal/api/router.go cmd/silo/main.go internal/taskmanager/tasks/autoscan_poll.go
git commit -m "feat(autoscan): wire v2 service, routes, and poll task"
```

---

## Task 11: Full sweep + finalize SDK dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Full build + test (libvips container)**

Run in the libvips-equipped container (Prerequisites #3):
```bash
go build ./... && go test ./... && go vet ./...
```
Expected: build OK; all packages `ok`; vet clean. `gofmt -l internal migrations` prints nothing.

- [ ] **Step 2: Finalize the SDK version (after `v0.5.0` is tagged upstream)**

Once `silo-plugin-sdk` PR #2 is merged and `v0.5.0` tagged:
```bash
go mod edit -dropreplace github.com/Silo-Server/silo-plugin-sdk
go get github.com/Silo-Server/silo-plugin-sdk@v0.5.0
go mod tidy
go build ./...
```
Expected: builds against the released tag with no replace directive.

If `v0.5.0` is not yet tagged when this plan is otherwise complete, leave the replace directive in place, commit it, and note in the PR that the dependency must be finalized before merge (do not merge a `replace` pointing at a local path).

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "build: depend on silo-plugin-sdk v0.5.0 (scan_source.v1)"
```

---

## Self-Review notes

- **Spec coverage:** §7 engine (Tasks 6–7), §8 connections incl. reuse-or-own + soft Requests link (Tasks 3,5), §9 data model decoupled from Requests + Autoscan-owned state (Tasks 3–4,9–10), §5/§11 host consumes `scan_source.v1` (Tasks 1–2). §10 (arr plugin) and §9 UI are separate plans.
- **Salvage map honored (§13):** engine/dedupe/suppress kept and generalized (Tasks 6–8); arr history/rewrite/suggest deleted (Task 8); `last_poll_at` → opaque `marker` (Tasks 3–4,7).
- **Out of scope here:** the Autoscan admin **UI** category (Part 2 plan) and the **arr plugin** (separate repo plan). This backend is testable on its own via the repository/engine/handler tests.
- **Risk carried from spec §14:** plugin multiplicity (one installation per arr server vs. many connections per installation) is modeled as one `autoscan_sources` row per `(installation_id, capability_id)`; if the runtime supports multiple capability instances per installation this maps cleanly, otherwise it implies one installation per server. Confirm during Task 1–2 by inspecting `ensureClient`/installation handling.
- **Type consistency:** `ResolvedConnection{BaseURL,APIKey}`, `Connection{BaseURL,APIKeyRef,RequestIntegrationID}`, `Source{InstallationID,CapabilityID,ConnectionID,Marker}`, `ScanSourceProvider.PollChanges(...)`, `Service.PollOnce` used consistently across Tasks 4–10.

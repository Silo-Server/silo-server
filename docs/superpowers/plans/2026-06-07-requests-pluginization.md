# Requests Fulfillment Pluginization — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move request fulfillment out of the Silo host and behind an agnostic `request_router.v1` plugin capability, extracting multi-instance Sonarr/Radarr into a new `silo-plugin-requests-arr` plugin, while keeping the request lifecycle/quota/policy/UX and autoscan's reuse of arr connection rows intact.

**Architecture:** High-seam, whole-request fulfiller. The host computes governance (which qualities a request may receive), hands the request + eligible connections to the plugin, and persists the targets the plugin reports back. The plugin owns instance routing + submission + status checks. Instance config is two-tier: a generic host connection row (`base_url`, `api_key_ref`, …) plus a plugin-declared `plugin_config` JSON blob. Fulfillment is pure-plugin (no in-host fallback).

**Tech Stack:** Go (silo-server host + plugin), `silo-plugin-sdk` (protobuf over hashicorp/go-plugin), Goose SQL migrations, pgx, React/TypeScript + TanStack Query admin UI.

**Repos & branches:**
- `silo-plugin-sdk` — Phase 1. Branch `feat/request-router-capability`. Module `github.com/Silo-Server/silo-plugin-sdk`, proto alias `pluginv1`.
- `silo-plugin-requests-arr` — Phase 2. New repo. Module `github.com/Silo-Server/silo-plugin-requests-arr`.
- `silo` (silo-server) — Phase 3. Branch `feat/requests-pluginization` (already created). Module `github.com/Silo-Server/silo-server`.

Commands assume the relevant repository root is the cwd. Phases are ordered: Phase 1 must publish a tag/pseudo-version before Phase 2/3 can depend on it; Phase 3 deletes host arr code only after Phase 2 exists.

> **libvips note:** silo-server host tests that touch image packages must run in a `golang:1.26 + libvips-dev` container; a bare `go test ./...` silently skips those packages. The packages touched by this plan (`internal/requests`, `internal/autoscan`, `internal/pluginhost`, `internal/plugins`, `internal/api`) do **not** need libvips, so plain `go test ./internal/requests/...` is fine for the steps below.

---

## Contract reference (used across all phases — keep field names identical)

The new `request_router.proto` defines these messages. Every phase references them; do not rename fields.

```proto
service RequestRouter {
  rpc Fulfill(FulfillRequest) returns (FulfillResponse);
  rpc CheckStatus(CheckStatusRequest) returns (CheckStatusResponse);
  rpc ListConfigOptions(ListConfigOptionsRequest) returns (ListConfigOptionsResponse);
  rpc TestConnection(TestConnectionRequest) returns (TestConnectionResponse);
}

message RequestDescriptor {
  string media_type = 1;                 // "movie" | "series"
  string title = 2;
  int32  year = 3;
  map<string, string> external_ids = 4;  // keys: "tmdb", "tvdb", "imdb"
  bool   is_anime = 5;
  int32  requester_user_id = 6;
  string requester_profile_id = 7;
}

message RouterConnection {              // distinct name: ResolvedConnection already exists in scan_source.proto
  string id = 1;
  string base_url = 2;
  string api_key = 3;
  google.protobuf.Struct config = 4;    // the plugin_config blob (root_folder, quality_profile_id, service_kind, …)
}

message FulfillRequest {
  string capability_id = 1;
  RequestDescriptor request = 2;
  repeated string qualities = 3;        // qualities to fulfill NOW (host already filtered by governance + idempotency)
  repeated RouterConnection connections = 4;
}

message FulfillmentTarget {
  string quality = 1;
  string connection_id = 2;
  string external_id = 3;
  string external_status = 4;
  string status = 5;                    // "queued" | "downloading" | "completed" | "failed"
  string message = 6;
}

message FulfillResponse {
  repeated FulfillmentTarget targets = 1;
  string message = 2;                   // set when zero targets (e.g. "no radarr instance configured")
}

message TargetRef {
  string quality = 1;
  string connection_id = 2;
  string external_id = 3;
}

message CheckStatusRequest {
  string capability_id = 1;
  RequestDescriptor request = 2;
  repeated TargetRef targets = 3;
  repeated RouterConnection connections = 4;
}

message TargetStatus {
  string quality = 1;
  string connection_id = 2;
  string status = 3;                    // "queued" | "downloading" | "completed" | "failed"
  string external_status = 4;
  string message = 5;
}

message CheckStatusResponse { repeated TargetStatus statuses = 1; }

message ConfigOption { string value = 1; string label = 2; }
message ConfigOptionList { repeated ConfigOption options = 1; }

message ListConfigOptionsRequest { string capability_id = 1; RouterConnection connection = 2; }
message ListConfigOptionsResponse { map<string, ConfigOptionList> options_by_field = 1; } // key = config field, e.g. "root_folder"

message TestConnectionRequest  { string capability_id = 1; RouterConnection connection = 2; }
message TestConnectionResponse { bool ok = 1; string message = 2; }
```

Generated Go identifiers (from `protoc-gen-go-grpc v1.6.1`, `require_unimplemented_servers=false`): `RequestRouterServer`, `UnimplementedRequestRouterServer`, `RequestRouterClient`, `RegisterRequestRouterServer(s, srv)`, `NewRequestRouterClient(cc)`.

---

# PHASE 1 — SDK: `request_router.v1` capability (`silo-plugin-sdk`)

### Task 1: Add the `request_router.proto` contract

**Files:**
- Create: `proto/silo/plugin/v1/request_router.proto`

- [ ] **Step 1: Create a branch**

```bash
git checkout -b feat/request-router-capability
```

- [ ] **Step 2: Write `proto/silo/plugin/v1/request_router.proto`**

```proto
syntax = "proto3";

package silo.plugin.v1;

option go_package = "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1;pluginv1";

import "google/protobuf/struct.proto";

// RequestRouter lets the host delegate whole-request fulfillment to a backend
// plugin (e.g. multi-instance Sonarr/Radarr, Seerr). The host owns the request
// lifecycle, quota/policy, the allowed-quality governance, and the target
// records; the plugin owns instance routing, submission, and status semantics.
// Connections are passed per call; the plugin stores no credentials.
service RequestRouter {
  rpc Fulfill(FulfillRequest) returns (FulfillResponse);
  rpc CheckStatus(CheckStatusRequest) returns (CheckStatusResponse);
  rpc ListConfigOptions(ListConfigOptionsRequest) returns (ListConfigOptionsResponse);
  rpc TestConnection(TestConnectionRequest) returns (TestConnectionResponse);
}

message RequestDescriptor {
  string media_type = 1;
  string title = 2;
  int32 year = 3;
  map<string, string> external_ids = 4;
  bool is_anime = 5;
  int32 requester_user_id = 6;
  string requester_profile_id = 7;
}

message RouterConnection {
  string id = 1;
  string base_url = 2;
  string api_key = 3;
  google.protobuf.Struct config = 4;
}

message FulfillRequest {
  string capability_id = 1;
  RequestDescriptor request = 2;
  repeated string qualities = 3;
  repeated RouterConnection connections = 4;
}

message FulfillmentTarget {
  string quality = 1;
  string connection_id = 2;
  string external_id = 3;
  string external_status = 4;
  string status = 5;
  string message = 6;
}

message FulfillResponse {
  repeated FulfillmentTarget targets = 1;
  string message = 2;
}

message TargetRef {
  string quality = 1;
  string connection_id = 2;
  string external_id = 3;
}

message CheckStatusRequest {
  string capability_id = 1;
  RequestDescriptor request = 2;
  repeated TargetRef targets = 3;
  repeated RouterConnection connections = 4;
}

message TargetStatus {
  string quality = 1;
  string connection_id = 2;
  string status = 3;
  string external_status = 4;
  string message = 5;
}

message CheckStatusResponse {
  repeated TargetStatus statuses = 1;
}

message ConfigOption {
  string value = 1;
  string label = 2;
}

message ConfigOptionList {
  repeated ConfigOption options = 1;
}

message ListConfigOptionsRequest {
  string capability_id = 1;
  RouterConnection connection = 2;
}

message ListConfigOptionsResponse {
  map<string, ConfigOptionList> options_by_field = 1;
}

message TestConnectionRequest {
  string capability_id = 1;
  RouterConnection connection = 2;
}

message TestConnectionResponse {
  bool ok = 1;
  string message = 2;
}
```

- [ ] **Step 3: Generate Go stubs**

Run: `make proto`
Expected: creates `pkg/pluginproto/silo/plugin/v1/request_router.pb.go` and `pkg/pluginproto/silo/plugin/v1/request_router_grpc.pb.go`, no errors.

- [ ] **Step 4: Verify generated identifiers exist**

Run: `grep -l 'RegisterRequestRouterServer\|NewRequestRouterClient' pkg/pluginproto/silo/plugin/v1/request_router_grpc.pb.go`
Expected: the file path prints (both symbols present).

- [ ] **Step 5: Commit**

```bash
git add proto/silo/plugin/v1/request_router.proto pkg/pluginproto/silo/plugin/v1/request_router.pb.go pkg/pluginproto/silo/plugin/v1/request_router_grpc.pb.go
git commit -m "feat(proto): add request_router.v1 capability contract"
```

---

### Task 2: Wire `RequestRouter` into the runtime server/client scaffolding

**Files:**
- Modify: `pkg/pluginsdk/runtime/runtime.go` (CapabilityServers struct ~25-35; Client accessors ~85-119; GRPCServer registration ~140-157)

- [ ] **Step 1: Add the server field to `CapabilityServers`**

Add `RequestRouter pluginv1.RequestRouterServer` to the struct so it reads:

```go
type CapabilityServers struct {
	Runtime          pluginv1.RuntimeServer
	MetadataProvider pluginv1.MetadataProviderServer
	MarkerProvider   pluginv1.MarkerProviderServer
	MediaAnalyzer    pluginv1.MediaAnalyzerServer
	ScheduledTask    pluginv1.ScheduledTaskServer
	ScanSource       pluginv1.ScanSourceServer
	RequestRouter    pluginv1.RequestRouterServer
	EventConsumer    pluginv1.EventConsumerServer
	AuthProvider     pluginv1.AuthProviderServer
	HttpRoutes       pluginv1.HttpRoutesServer
}
```

- [ ] **Step 2: Add the client accessor**

Next to the existing `func (c *Client) ScanSource() pluginv1.ScanSourceClient { ... }`, add:

```go
func (c *Client) RequestRouter() pluginv1.RequestRouterClient {
	return pluginv1.NewRequestRouterClient(c.conn)
}
```

- [ ] **Step 3: Register the server in `GRPCServer`**

In the `GRPCServer` method, next to the `ScanSource` guard, add:

```go
	if p.Servers.RequestRouter != nil {
		pluginv1.RegisterRequestRouterServer(server, p.Servers.RequestRouter)
	}
```

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: PASS (no errors).

- [ ] **Step 5: Commit**

```bash
git add pkg/pluginsdk/runtime/runtime.go
git commit -m "feat(runtime): expose RequestRouter capability server + client"
```

---

### Task 3: Manifest acceptance test for `request_router.v1`

**Files:**
- Create: `pkg/pluginsdk/manifest/request_router_test.go`

- [ ] **Step 1: Write the failing test**

```go
package manifest_test

import (
	"testing"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
)

func TestLoadAcceptsRequestRouterCapability(t *testing.T) {
	raw := []byte(`{
	  "plugin_id": "silo.example",
	  "version": "1.0.0",
	  "silo_api_version": "v1",
	  "capabilities": [
	    {"type": "request_router.v1", "id": "arr", "display_name": "X", "description": "Y"}
	  ]
	}`)
	m, err := manifest.Load(raw)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if len(m.GetCapabilities()) != 1 {
		t.Fatalf("expected 1 capability, got %d", len(m.GetCapabilities()))
	}
	if got := m.GetCapabilities()[0].GetType(); got != "request_router.v1" {
		t.Fatalf("capability type = %q, want request_router.v1", got)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./pkg/pluginsdk/manifest/ -run TestLoadAcceptsRequestRouterCapability -v`
Expected: PASS. (`request_router.v1` is already in `capability.KnownTypes`, so `Load` accepts it; this test locks that contract.)

- [ ] **Step 3: Commit**

```bash
git add pkg/pluginsdk/manifest/request_router_test.go
git commit -m "test(manifest): lock request_router.v1 capability acceptance"
```

---

### Task 4: Publish the SDK so downstream repos can depend on it

**Files:** none (git tag).

- [ ] **Step 1: Run the full SDK test suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 2: Merge the branch to main and tag**

Push the branch and open/merge a PR per repo convention, then on `main`:

```bash
git tag v0.5.0
git push origin v0.5.0
```

(If the team uses pseudo-versions instead of release tags, skip the tag — Phase 2/3 will `go get github.com/Silo-Server/silo-plugin-sdk@<commit>` to pin the merge commit. Record the resolved version string; Phase 2 Task 5 and Phase 3 Task 12 both need it.)

- [ ] **Step 3: Capture the version string**

Run: `go list -m github.com/Silo-Server/silo-plugin-sdk` (from a repo that depends on it) — note the exact version for the Phase 2/3 `go.mod` updates.

---

# PHASE 2 — New plugin repo: `silo-plugin-requests-arr`

> This phase relocates the host's arr code (`internal/requests/radarr`, `…/sonarr`, `…/arrclient`, and the routing logic from `…/routing.go`) into a standalone plugin. Because a plugin cannot import the host's `internal/requests`, all shared types become **plugin-local** types parsed from the `RouterConnection.config` Struct.

### Task 5: Scaffold the repo

**Files (create):** `go.mod`, `Makefile`, `.gitignore`, `README.md`, `manifest.json`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`

- [ ] **Step 1: Initialize the repo**

```bash
mkdir -p /opt/silo-plugin-requests-arr && cd /opt/silo-plugin-requests-arr
git init
go mod init github.com/Silo-Server/silo-plugin-requests-arr
```

- [ ] **Step 2: Add SDK + go-plugin deps**

Run (use the version from Phase 1 Task 4):
```bash
go get github.com/Silo-Server/silo-plugin-sdk@v0.5.0
go get github.com/hashicorp/go-plugin@v1.8.0
```
Set Go version in `go.mod` to `go 1.26.3`.

- [ ] **Step 3: Create `manifest.json`** (declares the capability + the per-connection config schema; `__CHECKSUM__` is a build-time `sed` target and is overwritten at runtime by the binary self-hash)

```json
{
  "plugin_id": "silo.requests.arr",
  "version": "0.1.0",
  "checksum": "__CHECKSUM__",
  "silo_api_version": "v1",
  "supported_platforms": [
    { "os": "linux", "arch": "amd64" },
    { "os": "linux", "arch": "arm64" },
    { "os": "darwin", "arch": "arm64" }
  ],
  "capabilities": [
    {
      "type": "request_router.v1",
      "id": "arr",
      "display_name": "Sonarr / Radarr",
      "description": "Fulfills movie/series requests against multi-instance Sonarr/Radarr.",
      "config_schema": [
        { "key": "service_kind", "title": "Service", "required": true,
          "json_schema": "{\"type\":\"string\",\"enum\":[\"radarr\",\"sonarr\"]}",
          "admin_form": { "fields": [ { "key": "service_kind", "label": "Service", "control": "ADMIN_FORM_CONTROL_SELECT", "required": true,
            "options": [ { "value": "radarr", "label": "Radarr (movies)" }, { "value": "sonarr", "label": "Sonarr (series)" } ] } ] } },
        { "key": "root_folder", "title": "Root folder",
          "json_schema": "{\"type\":\"string\"}",
          "admin_form": { "fields": [ { "key": "root_folder", "label": "Root folder", "control": "ADMIN_FORM_CONTROL_SELECT" } ] } },
        { "key": "quality_profile_id", "title": "Quality profile",
          "json_schema": "{\"type\":\"integer\"}",
          "admin_form": { "fields": [ { "key": "quality_profile_id", "label": "Quality profile", "control": "ADMIN_FORM_CONTROL_SELECT" } ] } },
        { "key": "tags", "title": "Tags",
          "json_schema": "{\"type\":\"array\",\"items\":{\"type\":\"integer\"}}",
          "admin_form": { "fields": [ { "key": "tags", "label": "Tags", "control": "ADMIN_FORM_CONTROL_TEXT" } ] } },
        { "key": "is_default", "title": "Default (HD/1080p) instance",
          "json_schema": "{\"type\":\"boolean\"}",
          "admin_form": { "fields": [ { "key": "is_default", "label": "Default (HD)", "control": "ADMIN_FORM_CONTROL_SWITCH" } ] } },
        { "key": "is_default_4k", "title": "Default 4K (2160p) instance",
          "json_schema": "{\"type\":\"boolean\"}",
          "admin_form": { "fields": [ { "key": "is_default_4k", "label": "Default 4K", "control": "ADMIN_FORM_CONTROL_SWITCH" } ] } },
        { "key": "anime_enabled", "title": "Enable anime overrides",
          "json_schema": "{\"type\":\"boolean\"}",
          "admin_form": { "fields": [ { "key": "anime_enabled", "label": "Anime overrides", "control": "ADMIN_FORM_CONTROL_SWITCH" } ] } },
        { "key": "anime_root_folder", "title": "Anime root folder",
          "json_schema": "{\"type\":\"string\"}",
          "admin_form": { "fields": [ { "key": "anime_root_folder", "label": "Anime root folder", "control": "ADMIN_FORM_CONTROL_SELECT" } ] } },
        { "key": "anime_quality_profile_id", "title": "Anime quality profile",
          "json_schema": "{\"type\":\"integer\"}",
          "admin_form": { "fields": [ { "key": "anime_quality_profile_id", "label": "Anime quality profile", "control": "ADMIN_FORM_CONTROL_SELECT" } ] } },
        { "key": "anime_tags", "title": "Anime tags",
          "json_schema": "{\"type\":\"array\",\"items\":{\"type\":\"integer\"}}",
          "admin_form": { "fields": [ { "key": "anime_tags", "label": "Anime tags", "control": "ADMIN_FORM_CONTROL_TEXT" } ] } },
        { "key": "search_on_add", "title": "Search on add",
          "json_schema": "{\"type\":\"boolean\"}",
          "admin_form": { "fields": [ { "key": "search_on_add", "label": "Search on add", "control": "ADMIN_FORM_CONTROL_SWITCH" } ] } },
        { "key": "minimum_availability", "title": "Minimum availability (Radarr)",
          "json_schema": "{\"type\":\"string\"}",
          "admin_form": { "fields": [ { "key": "minimum_availability", "label": "Minimum availability", "control": "ADMIN_FORM_CONTROL_TEXT" } ] } },
        { "key": "series_type", "title": "Series type (Sonarr)",
          "json_schema": "{\"type\":\"string\"}",
          "admin_form": { "fields": [ { "key": "series_type", "label": "Series type", "control": "ADMIN_FORM_CONTROL_TEXT" } ] } },
        { "key": "season_folder", "title": "Season folder (Sonarr)",
          "json_schema": "{\"type\":\"boolean\"}",
          "admin_form": { "fields": [ { "key": "season_folder", "label": "Season folder", "control": "ADMIN_FORM_CONTROL_SWITCH" } ] } }
      ]
    }
  ],
  "global_config_schema": []
}
```

- [ ] **Step 4: Copy `Makefile`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.gitignore` from `/opt/silo-plugin-autoscan-arr/`** verbatim, then edit only:
  - `release.yml` → the catalog dispatch `repo` literal: `Silo-Server/silo-plugin-autoscan-arr` → `Silo-Server/silo-plugin-requests-arr`.
  - Everything else (LDFLAGS, PLATFORMS, checksum stamping, SDK-replace guard, GOPRIVATE) is identical and stays.

- [ ] **Step 5: Write `README.md`** (one paragraph: what the plugin does, that credentials/config arrive per-call from the host, build via `make build`).

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "chore: scaffold silo-plugin-requests-arr"
```

---

### Task 6: Relocate arr HTTP logic with plugin-local types

**Files:**
- Create: `internal/arr/types.go` (plugin-local mirror of the host types the arr code needs)
- Create: `internal/arr/client.go` (copy of host `internal/requests/arrclient/client.go`)
- Create: `internal/arr/options.go`, `internal/arr/queue.go`, `internal/arr/resources.go` (copies of host `arrclient/*`)
- Create: `internal/arr/radarr.go` (copy of host `internal/requests/radarr/client.go`)
- Create: `internal/arr/sonarr.go` (copy of host `internal/requests/sonarr/client.go`)
- Create: `internal/arr/routing.go` (copy of host `internal/requests/routing.go`)
- Create: `internal/arr/*_test.go` (copy host `arrclient/queue_test.go`, `radarr/client_test.go`, `sonarr/client_test.go`)

- [ ] **Step 1: Write `internal/arr/types.go`** — the plugin-local types replacing the host's `mediarequests.*`. These mirror the host fields the arr code reads.

```go
// Package arr contains the Sonarr/Radarr fulfillment logic, relocated from the
// silo-server host. It depends only on plugin-local types; config arrives from
// the host per call as a parsed Instance.
package arr

type MediaType string

const (
	MediaTypeMovie  MediaType = "movie"
	MediaTypeSeries MediaType = "series"
)

type Status string

const (
	StatusQueued      Status = "queued"
	StatusDownloading Status = "downloading"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
)

// Request is the per-target view the arr submitters need.
type Request struct {
	MediaType  MediaType
	TMDBID     int
	TVDBID     int
	Title      string
	Year       int
	IsAnime    bool
	ExternalID string // set by CheckStatus probes
}

// Instance is one resolved arr connection: the generic host fields plus the
// plugin_config blob parsed into typed fields.
type Instance struct {
	ID               string
	Kind             string // "radarr" | "sonarr" (from config service_kind)
	BaseURL          string
	APIKey           string
	RootFolder       string
	QualityProfileID *int
	Tags             []int
	IsDefault        bool
	IsDefault4K      bool
	Is4K             bool
	AnimeEnabled     bool
	AnimeRootFolder  string
	AnimeQualityProfileID *int
	AnimeTags        []int
	Options          map[string]any // search_on_add, minimum_availability, series_type, season_folder
}

type FulfillmentResult struct {
	ExternalID     string
	ExternalStatus string
}

type FulfillmentStatus struct {
	Status         Status
	ExternalID     string
	ExternalStatus string
	Message        string
	Failed         bool
}

type IntegrationRootFolder struct {
	Path       string
	FreeSpace  int64
	TotalSpace int64
	Accessible bool
}
type IntegrationQualityProfile struct {
	ID   int
	Name string
}
type IntegrationTag struct {
	ID    int
	Label string
}
type IntegrationOptions struct {
	Kind            string
	RootFolders     []IntegrationRootFolder
	QualityProfiles []IntegrationQualityProfile
	Tags            []IntegrationTag
}
```

- [ ] **Step 2: Copy the host arr files into `internal/arr/`** (single package; flatten the three host packages into one):

```bash
cp /opt/silo/internal/requests/arrclient/client.go    internal/arr/client.go
cp /opt/silo/internal/requests/arrclient/options.go   internal/arr/options.go
cp /opt/silo/internal/requests/arrclient/queue.go     internal/arr/queue.go
cp /opt/silo/internal/requests/arrclient/resources.go internal/arr/resources.go
cp /opt/silo/internal/requests/radarr/client.go       internal/arr/radarr.go
cp /opt/silo/internal/requests/sonarr/client.go       internal/arr/sonarr.go
cp /opt/silo/internal/requests/routing.go             internal/arr/routing.go
cp /opt/silo/internal/requests/arrclient/queue_test.go internal/arr/queue_test.go
cp /opt/silo/internal/requests/radarr/client_test.go   internal/arr/radarr_test.go
cp /opt/silo/internal/requests/sonarr/client_test.go   internal/arr/sonarr_test.go
```

- [ ] **Step 3: Make every copied file compile in the single `arr` package.** Apply these edits across the copied files:
  - Change every `package arrclient` / `package radarr` / `package sonarr` / `package requests` header to `package arr`.
  - Delete the import of `mediarequests "github.com/Silo-Server/silo-server/internal/requests"` and the import of `arrclient "github.com/Silo-Server/silo-server/internal/requests/arrclient"` (now same package).
  - Replace type references: `mediarequests.Integration` → `Instance`; `mediarequests.Request` → `Request`; `mediarequests.FulfillmentResult` → `FulfillmentResult`; `mediarequests.FulfillmentStatus` → `FulfillmentStatus`; `mediarequests.IntegrationOptions` → `IntegrationOptions` (and the sub-types); `mediarequests.MediaTypeMovie/Series` → `MediaTypeMovie/Series`; `mediarequests.Status*` → `Status*`.
  - In `radarr.go`/`sonarr.go`, the receiver type `Client` and constructor `NewClient` already exist in each; rename to avoid collision: radarr's → `RadarrClient` / `NewRadarrClient`, sonarr's → `SonarrClient` / `NewSonarrClient`. Update method receivers accordingly.
  - In `resources.go`, `arrclient.Client` references become local `Client` (the generic HTTP client kept from `arrclient/client.go`).
  - `routing.go`: change the import `github.com/Silo-Server/silo-server/internal/access` usage. The host used `access.CompareQuality(ceiling, access.PlaybackQuality4K)`. The plugin no longer receives a ceiling — the host now sends explicit `qualities`. **Repurpose `routing.go`**: keep `resolveInstance(pt)` (anime overlay) but change `routeTargets` to map an explicit quality set onto instances (see Task 7 Step 2). For this step, just make it compile by deleting the `access` import and the `routeTargets` body (it is rewritten in Task 7); leave `resolveInstance` intact operating on `Instance`.

- [ ] **Step 4: Tidy and build**

Run: `go mod tidy && go build ./internal/arr/`
Expected: PASS.

- [ ] **Step 5: Run the relocated unit tests**

Run: `go test ./internal/arr/ -v`
Expected: PASS (the copied radarr/sonarr/queue tests, adjusted for the renamed clients/types).

- [ ] **Step 6: Commit**

```bash
git add internal/arr/
git commit -m "feat(arr): relocate Sonarr/Radarr fulfillment logic with plugin-local types"
```

---

### Task 7: Implement the `RequestRouter` server

**Files:**
- Create: `internal/router/server.go` (the gRPC server: proto ↔ `arr.*` translation + routing)
- Create: `internal/router/config.go` (`google.protobuf.Struct` → `arr.Instance`)
- Create: `internal/router/server_test.go`

- [ ] **Step 1: Write `internal/router/config.go`** — parse a `RouterConnection` into an `arr.Instance`.

```go
package router

import (
	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-plugin-requests-arr/internal/arr"
)

func instanceFromConnection(c *pluginv1.RouterConnection) arr.Instance {
	cfg := map[string]any{}
	if c.GetConfig() != nil {
		cfg = c.GetConfig().AsMap()
	}
	in := arr.Instance{
		ID:              c.GetId(),
		BaseURL:         c.GetBaseUrl(),
		APIKey:          c.GetApiKey(),
		Kind:            getString(cfg, "service_kind"),
		RootFolder:      getString(cfg, "root_folder"),
		QualityProfileID: getIntPtr(cfg, "quality_profile_id"),
		Tags:            getIntSlice(cfg, "tags"),
		IsDefault:       getBool(cfg, "is_default"),
		IsDefault4K:     getBool(cfg, "is_default_4k"),
		Is4K:            getBool(cfg, "is_4k"),
		AnimeEnabled:    getBool(cfg, "anime_enabled"),
		AnimeRootFolder: getString(cfg, "anime_root_folder"),
		AnimeQualityProfileID: getIntPtr(cfg, "anime_quality_profile_id"),
		AnimeTags:       getIntSlice(cfg, "anime_tags"),
		Options:         map[string]any{},
	}
	for _, k := range []string{"search_on_add", "minimum_availability", "series_type", "season_folder"} {
		if v, ok := cfg[k]; ok {
			in.Options[k] = v
		}
	}
	return in
}

func getString(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
func getBool(m map[string]any, k string) bool {
	if v, ok := m[k].(bool); ok {
		return v
	}
	return false
}
func getIntPtr(m map[string]any, k string) *int {
	// structpb decodes JSON numbers as float64.
	if v, ok := m[k].(float64); ok {
		i := int(v)
		return &i
	}
	return nil
}
func getIntSlice(m map[string]any, k string) []int {
	raw, ok := m[k].([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(raw))
	for _, e := range raw {
		if f, ok := e.(float64); ok {
			out = append(out, int(f))
		}
	}
	return out
}

// optionListToStruct is unused here but kept for symmetry; structpb import guard.
var _ = structpb.NewStruct
```

- [ ] **Step 2: Rewrite `internal/arr/routing.go`'s `routeTargets`** to map an explicit quality set onto instances (governance already done host-side). Add to `internal/arr/routing.go`:

```go
// PlannedTarget is a routing decision for one quality.
type PlannedTarget struct {
	Instance Instance
	Quality  string // "1080p" | "2160p"
	IsAnime  bool
}

// RouteTargets maps the host-requested qualities onto enabled instances of the
// correct service kind: 1080p -> the is_default instance, 2160p -> the
// is_default_4k instance. A quality with no matching instance is omitted.
func RouteTargets(req Request, qualities []string, instances []Instance) []PlannedTarget {
	wantKind := "radarr"
	if req.MediaType == MediaTypeSeries {
		wantKind = "sonarr"
	}
	var hd, uhd *Instance
	for i := range instances {
		in := instances[i]
		if in.Kind != wantKind {
			continue
		}
		if in.IsDefault && hd == nil {
			hd = &instances[i]
		}
		if in.IsDefault4K && uhd == nil {
			uhd = &instances[i]
		}
	}
	var out []PlannedTarget
	for _, q := range qualities {
		switch q {
		case "1080p":
			if hd != nil {
				out = append(out, PlannedTarget{Instance: *hd, Quality: "1080p", IsAnime: req.IsAnime && hd.AnimeEnabled})
			}
		case "2160p":
			if uhd != nil {
				out = append(out, PlannedTarget{Instance: *uhd, Quality: "2160p", IsAnime: req.IsAnime && uhd.AnimeEnabled})
			}
		}
	}
	return out
}
```

Keep the existing `resolveInstance` but change its parameter to the new `PlannedTarget` and its receiver field names (`pt.Instance`, `pt.IsAnime`); it already overlays anime root/profile/tags and sets `Options["series_type"]="anime"` for sonarr.

- [ ] **Step 3: Write `internal/router/server.go`** — implements the four RPCs.

```go
package router

import (
	"context"
	"fmt"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-plugin-requests-arr/internal/arr"
)

type Server struct {
	pluginv1.UnimplementedRequestRouterServer
}

func New() *Server { return &Server{} }

func descriptorToRequest(d *pluginv1.RequestDescriptor) arr.Request {
	ids := d.GetExternalIds()
	return arr.Request{
		MediaType: arr.MediaType(d.GetMediaType()),
		TMDBID:    atoiSafe(ids["tmdb"]),
		TVDBID:    atoiSafe(ids["tvdb"]),
		Title:     d.GetTitle(),
		Year:      int(d.GetYear()),
		IsAnime:   d.GetIsAnime(),
	}
}

func (s *Server) Fulfill(ctx context.Context, req *pluginv1.FulfillRequest) (*pluginv1.FulfillResponse, error) {
	r := descriptorToRequest(req.GetRequest())
	instances := make([]arr.Instance, 0, len(req.GetConnections()))
	for _, c := range req.GetConnections() {
		instances = append(instances, instanceFromConnection(c))
	}
	planned := arr.RouteTargets(r, req.GetQualities(), instances)
	if len(planned) == 0 {
		kind := "radarr"
		if r.MediaType == arr.MediaTypeSeries {
			kind = "sonarr"
		}
		return &pluginv1.FulfillResponse{Message: fmt.Sprintf("no %s instance configured for the requested quality", kind)}, nil
	}
	var targets []*pluginv1.FulfillmentTarget
	for _, pt := range planned {
		resolved := arr.ResolveInstance(pt) // exported resolveInstance (rename in Task 6/7)
		result, err := submit(ctx, r, resolved)
		t := &pluginv1.FulfillmentTarget{Quality: pt.Quality, ConnectionId: resolved.ID}
		if err != nil {
			t.Status = string(arr.StatusFailed)
			t.Message = err.Error()
		} else {
			t.Status = string(arr.StatusQueued)
			t.ExternalId = result.ExternalID
			t.ExternalStatus = result.ExternalStatus
		}
		targets = append(targets, t)
	}
	return &pluginv1.FulfillResponse{Targets: targets}, nil
}

func submit(ctx context.Context, r arr.Request, in arr.Instance) (arr.FulfillmentResult, error) {
	switch r.MediaType {
	case arr.MediaTypeMovie:
		return arr.NewRadarrClient(nil).SubmitMovie(ctx, r, in)
	case arr.MediaTypeSeries:
		return arr.NewSonarrClient(nil).SubmitSeries(ctx, r, in)
	default:
		return arr.FulfillmentResult{}, fmt.Errorf("unsupported media type %q", r.MediaType)
	}
}

func (s *Server) CheckStatus(ctx context.Context, req *pluginv1.CheckStatusRequest) (*pluginv1.CheckStatusResponse, error) {
	byID := map[string]arr.Instance{}
	for _, c := range req.GetConnections() {
		in := instanceFromConnection(c)
		byID[in.ID] = in
	}
	r := descriptorToRequest(req.GetRequest())
	var out []*pluginv1.TargetStatus
	for _, t := range req.GetTargets() {
		in, ok := byID[t.GetConnectionId()]
		if !ok {
			continue
		}
		probe := r
		probe.ExternalID = t.GetExternalId()
		var (
			st  arr.FulfillmentStatus
			err error
		)
		if r.MediaType == arr.MediaTypeMovie {
			st, err = arr.NewRadarrClient(nil).CheckMovieStatus(ctx, probe, in)
		} else {
			st, err = arr.NewSonarrClient(nil).CheckSeriesStatus(ctx, probe, in)
		}
		if err != nil {
			continue
		}
		out = append(out, &pluginv1.TargetStatus{
			Quality: t.GetQuality(), ConnectionId: t.GetConnectionId(),
			Status: string(st.Status), ExternalStatus: st.ExternalStatus, Message: st.Message,
		})
	}
	return &pluginv1.CheckStatusResponse{Statuses: out}, nil
}

func (s *Server) ListConfigOptions(ctx context.Context, req *pluginv1.ListConfigOptionsRequest) (*pluginv1.ListConfigOptionsResponse, error) {
	in := instanceFromConnection(req.GetConnection())
	var opts arr.IntegrationOptions
	var err error
	if in.Kind == "sonarr" {
		opts, err = arr.NewSonarrClient(nil).ListSeriesIntegrationOptions(ctx, in)
	} else {
		opts, err = arr.NewRadarrClient(nil).ListMovieIntegrationOptions(ctx, in)
	}
	if err != nil {
		return nil, err
	}
	rf := &pluginv1.ConfigOptionList{}
	for _, f := range opts.RootFolders {
		rf.Options = append(rf.Options, &pluginv1.ConfigOption{Value: f.Path, Label: f.Path})
	}
	qp := &pluginv1.ConfigOptionList{}
	for _, p := range opts.QualityProfiles {
		qp.Options = append(qp.Options, &pluginv1.ConfigOption{Value: itoa(p.ID), Label: p.Name})
	}
	tg := &pluginv1.ConfigOptionList{}
	for _, t := range opts.Tags {
		tg.Options = append(tg.Options, &pluginv1.ConfigOption{Value: itoa(t.ID), Label: t.Label})
	}
	return &pluginv1.ListConfigOptionsResponse{OptionsByField: map[string]*pluginv1.ConfigOptionList{
		"root_folder":              rf,
		"quality_profile_id":       qp,
		"tags":                     tg,
		"anime_root_folder":        rf,
		"anime_quality_profile_id": qp,
		"anime_tags":               tg,
	}}, nil
}

func (s *Server) TestConnection(ctx context.Context, req *pluginv1.TestConnectionRequest) (*pluginv1.TestConnectionResponse, error) {
	in := instanceFromConnection(req.GetConnection())
	var err error
	if in.Kind == "sonarr" {
		_, err = arr.NewSonarrClient(nil).ListSeriesIntegrationOptions(ctx, in)
	} else {
		_, err = arr.NewRadarrClient(nil).ListMovieIntegrationOptions(ctx, in)
	}
	if err != nil {
		return &pluginv1.TestConnectionResponse{Ok: false, Message: err.Error()}, nil
	}
	return &pluginv1.TestConnectionResponse{Ok: true, Message: "connection successful"}, nil
}
```

Add tiny helpers `atoiSafe`/`itoa` to `config.go` (use `strconv.Atoi` ignoring error → 0, and `strconv.Itoa`). In Task 6 rename `resolveInstance`→`ResolveInstance` and the radarr/sonarr methods `SubmitMovie/SubmitSeries/CheckMovieStatus/CheckSeriesStatus/ListMovieIntegrationOptions/ListSeriesIntegrationOptions` must keep their names and accept `(ctx, arr.Request, arr.Instance)` / return `arr.IntegrationOptions`.

- [ ] **Step 4: Write `internal/router/server_test.go`** — a Fulfill round-trip against an `httptest` Radarr.

```go
package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestFulfillSubmitsMovieToDefaultInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/movie/lookup/tmdb":
			json.NewEncoder(w).Encode(map[string]any{"title": "X", "tmdbId": 42})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/movie":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": 7})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg, _ := structpb.NewStruct(map[string]any{
		"service_kind": "radarr", "is_default": true,
		"root_folder": "/movies", "quality_profile_id": float64(1),
	})
	resp, err := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		CapabilityId: "arr",
		Request:      &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}, Title: "X"},
		Qualities:    []string{"1080p"},
		Connections:  []*pluginv1.RouterConnection{{Id: "c1", BaseUrl: srv.URL, ApiKey: "k", Config: cfg}},
	})
	if err != nil {
		t.Fatalf("Fulfill error: %v", err)
	}
	if len(resp.GetTargets()) != 1 || resp.GetTargets()[0].GetStatus() != "queued" {
		t.Fatalf("unexpected targets: %+v (msg=%q)", resp.GetTargets(), resp.GetMessage())
	}
	if resp.GetTargets()[0].GetExternalId() != "7" {
		t.Fatalf("external id = %q, want 7", resp.GetTargets()[0].GetExternalId())
	}
}
```

- [ ] **Step 5: Run**

Run: `go test ./internal/router/ -run TestFulfillSubmitsMovieToDefaultInstance -v`
Expected: PASS. (If `SubmitMovie`'s exact arr paths differ from the stub, align the stub paths to the verbatim radarr code; adjust and re-run.)

- [ ] **Step 6: Commit**

```bash
git add internal/router/ internal/arr/routing.go
git commit -m "feat(router): implement request_router.v1 RPCs over arr backend"
```

---

### Task 8: Plugin entrypoint `main.go`

**Files:**
- Create: `main.go`
- Create: `main_test.go` (manifest-load smoke test, copied from autoscan-arr `manifest_load_test.go` with the new capability type assertion)

- [ ] **Step 1: Write `main.go`** (structurally identical to autoscan-arr's, swapping the capability server)

```go
// Command silo-plugin-requests-arr implements the Silo request_router.v1
// capability for multi-instance Sonarr/Radarr. Connection config (base URL, API
// key, root folder, quality profile, …) arrives per call in FulfillRequest;
// the plugin stores nothing.
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"

	"github.com/Silo-Server/silo-plugin-requests-arr/internal/router"
)

var version string

//go:embed manifest.json
var manifestJSON []byte

type runtimeServer struct {
	pluginv1.UnimplementedRuntimeServer
	manifest *pluginv1.PluginManifest
}

func (s *runtimeServer) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: s.manifest}, nil
}

func (s *runtimeServer) Configure(context.Context, *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	return &pluginv1.ConfigureResponse{}, nil
}

func main() {
	manifest, err := loadManifest()
	if err != nil {
		panic(err)
	}
	runtime.Serve(runtime.ServeConfig{
		Servers: runtime.CapabilityServers{
			Runtime:       &runtimeServer{manifest: manifest},
			RequestRouter: router.New(),
		},
	})
}

func loadManifest() (*pluginv1.PluginManifest, error) {
	manifest, err := publicmanifest.Load(manifestJSON)
	if err != nil {
		return nil, fmt.Errorf("load embedded manifest: %w", err)
	}
	if version != "" {
		manifest.Version = version
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return nil, fmt.Errorf("read executable %q: %w", exe, err)
	}
	sum := sha256.Sum256(data)
	manifest.Checksum = hex.EncodeToString(sum[:])
	return manifest, nil
}
```

- [ ] **Step 2: Write `main_test.go`**

```go
package main

import "testing"

func TestEmbeddedManifestLoads(t *testing.T) {
	m, err := loadManifest()
	if err != nil {
		t.Fatalf("loadManifest: %v", err)
	}
	if m.GetPluginId() != "silo.requests.arr" {
		t.Fatalf("plugin_id = %q", m.GetPluginId())
	}
	if len(m.GetCapabilities()) != 1 || m.GetCapabilities()[0].GetType() != "request_router.v1" {
		t.Fatalf("expected one request_router.v1 capability, got %+v", m.GetCapabilities())
	}
}
```

- [ ] **Step 3: Build + test the whole module**

Run: `make build && go test ./...`
Expected: `plugin` binary builds; all tests PASS.

- [ ] **Step 4: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: serve request_router.v1 capability from plugin entrypoint"
```

- [ ] **Step 5: Push + tag for catalog**

Push to GitHub, then tag `v0.1.0` to trigger `release.yml` (which stamps the checksum, publishes binaries, and dispatches to the `silo-plugins` catalog).

---

# PHASE 3 — Host refactor (`silo` / silo-server, branch `feat/requests-pluginization`)

### Task 9: Migration — generalize `request_integrations` to a two-tier connection registry

**Files:**
- Create: `migrations/sql/<timestamp>_request_router_connections.sql` (via `make migrate-create`)

- [ ] **Step 1: Create the migration file**

Run: `make migrate-create NAME=request_router_connections`
Expected: prints a new timestamped path under `migrations/sql/`. Edit that file.

- [ ] **Step 2: Write the Up/Down migration** (adds generic columns, folds arr columns into `plugin_config`, preserves ids so `media_request_targets.integration_id` FK and autoscan `request_integration_id` links stay valid)

```sql
-- +goose Up
ALTER TABLE public.request_integrations
    ADD COLUMN IF NOT EXISTS capability_id text NOT NULL DEFAULT 'request_router.v1',
    ADD COLUMN IF NOT EXISTS installation_id integer,
    ADD COLUMN IF NOT EXISTS supported_media_types text[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS plugin_config jsonb NOT NULL DEFAULT '{}'::jsonb;

-- Fold the arr-specific typed columns into the generic plugin_config blob.
-- service_kind comes from the old `kind`; supported_media_types is derived from it.
UPDATE public.request_integrations
SET plugin_config = jsonb_strip_nulls(
        coalesce(options, '{}'::jsonb) || jsonb_build_object(
            'service_kind', kind,
            'root_folder', root_folder,
            'quality_profile_id', quality_profile_id,
            'tags', to_jsonb(tags),
            'is_4k', is_4k,
            'is_default', is_default,
            'is_default_4k', is_default_4k,
            'anime_enabled', anime_enabled,
            'anime_quality_profile_id', anime_quality_profile_id,
            'anime_root_folder', anime_root_folder,
            'anime_tags', to_jsonb(anime_tags)
        )),
    supported_media_types = CASE WHEN kind = 'sonarr' THEN ARRAY['series'] ELSE ARRAY['movie'] END
WHERE plugin_config = '{}'::jsonb;

-- +goose Down
ALTER TABLE public.request_integrations
    DROP COLUMN IF EXISTS capability_id,
    DROP COLUMN IF EXISTS installation_id,
    DROP COLUMN IF EXISTS supported_media_types,
    DROP COLUMN IF EXISTS plugin_config;
```

Note: the old typed columns (`kind`, `root_folder`, `quality_profile_id`, `tags`, `is_4k`, `is_default`, `is_default_4k`, `anime_*`, `options`) are **left in place** for one release so the Down is clean and autoscan's `base_url`/`api_key_ref`/`enabled` reads are untouched; a later cleanup migration drops them once the plugin is proven.

- [ ] **Step 3: Apply and verify**

Run: `docker compose up -d postgres redis && make migrate-up && make migrate-status`
Expected: the new migration shows applied; no errors.

- [ ] **Step 4: Spot-check the blob**

Run: `psql "$DATABASE_URL" -c "SELECT id, capability_id, supported_media_types, plugin_config->>'service_kind' FROM request_integrations LIMIT 5;"`
Expected: existing rows show `capability_id=request_router.v1`, `supported_media_types` matching kind, and `service_kind` populated.

- [ ] **Step 5: Commit**

```bash
git add migrations/sql/*_request_router_connections.sql
git commit -m "feat(db): generalize request_integrations into a two-tier connection registry"
```

---

### Task 10: Generalize the `Integration` type + repository mapping

**Files:**
- Modify: `internal/requests/types.go` (the `Integration` struct, ~256-278)
- Modify: `internal/requests/repository.go` (`integrationColumns` ~455; `scanIntegration` ~766; insert/update)

- [ ] **Step 1: Add generic fields to `Integration`** (keep `BaseURL`/`APIKeyRef`/`Enabled`/`Name`/`ID` exactly — autoscan reads these). Add:

```go
	CapabilityID        string         `json:"capability_id"`
	InstallationID      *int           `json:"installation_id,omitempty"`
	SupportedMediaTypes []string       `json:"supported_media_types"`
	PluginConfig        map[string]any `json:"plugin_config"`
```

Leave the existing arr-typed fields in the struct for now (they remain DB-backed until the cleanup migration); they are simply no longer used by the host fulfillment path.

- [ ] **Step 2: Extend `integrationColumns` and `scanIntegration`** to select/scan the four new columns. Append to the const:

```go
const integrationColumns = `id, kind, name, enabled, base_url, api_key_ref,
	root_folder, quality_profile_id, tags, is_4k, is_default, is_default_4k,
	anime_enabled, anime_quality_profile_id, anime_root_folder, anime_tags,
	options, last_check_at, last_check_status, last_check_error, updated_at,
	capability_id, installation_id, supported_media_types, plugin_config`
```

In `scanIntegration`, after the existing scans, add the four destinations (with `installation_id` and `plugin_config` nullable handling):

```go
	var installationID sql.NullInt64
	var supportedMediaTypes []string
	var pluginConfigRaw []byte
	// ... extend the row.Scan(...) arg list with:
	//   &i.CapabilityID, &installationID, &supportedMediaTypes, &pluginConfigRaw
	// then after scan:
	if installationID.Valid {
		v := int(installationID.Int64)
		i.InstallationID = &v
	}
	i.SupportedMediaTypes = supportedMediaTypes
	if len(pluginConfigRaw) > 0 {
		if err := json.Unmarshal(pluginConfigRaw, &i.PluginConfig); err != nil {
			return Integration{}, fmt.Errorf("unmarshal plugin_config for %s: %w", i.ID, err)
		}
	}
	if i.PluginConfig == nil {
		i.PluginConfig = map[string]any{}
	}
```

- [ ] **Step 3: Update `insertIntegration`/`updateIntegration`** to write `capability_id`, `installation_id`, `supported_media_types`, `plugin_config` (marshal the map to JSON). Mirror the existing parameter-ordering pattern; preserve the `api_key_ref = CASE WHEN $N = '' THEN api_key_ref ELSE $N END` keep-secret behavior.

- [ ] **Step 4: Build + run repository tests**

Run: `go test ./internal/requests/ -run Integration -v`
Expected: PASS (update any fixture expectations for the new fields).

- [ ] **Step 5: Commit**

```bash
git add internal/requests/types.go internal/requests/repository.go
git commit -m "feat(requests): add generic connection fields to Integration + repo mapping"
```

---

### Task 11: `pluginhost` RequestRouter client wrapper + `plugins.Service` resolver

**Files:**
- Modify: `internal/pluginhost/client.go` (add `RequestRouterClient` wrapper + `(*Client).RequestRouter(capabilityID)`)
- Modify: `internal/plugins/service.go` (add `RequestRouterClient(ctx, installationID, capabilityID)`)

- [ ] **Step 1: Add the typed wrapper to `internal/pluginhost/client.go`** (mirror `ScanSourceClient`)

```go
// DefaultRequestRouterTimeout bounds a single request_router RPC. Fulfillment
// hits remote arr instances, so allow generous headroom.
const DefaultRequestRouterTimeout = 60 * time.Second

type RequestRouterClient struct {
	client  pluginv1.RequestRouterClient
	timeout time.Duration
}

func (c *Client) RequestRouter(capabilityID string) (*RequestRouterClient, error) {
	if err := c.requireCapability("request_router.v1", capabilityID); err != nil {
		return nil, err
	}
	return &RequestRouterClient{client: c.rpc.RequestRouter(), timeout: DefaultRequestRouterTimeout}, nil
}

func (c *RequestRouterClient) Fulfill(ctx context.Context, req *pluginv1.FulfillRequest) (*pluginv1.FulfillResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.Fulfill(callCtx, req)
}

func (c *RequestRouterClient) CheckStatus(ctx context.Context, req *pluginv1.CheckStatusRequest) (*pluginv1.CheckStatusResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.CheckStatus(callCtx, req)
}

func (c *RequestRouterClient) ListConfigOptions(ctx context.Context, req *pluginv1.ListConfigOptionsRequest) (*pluginv1.ListConfigOptionsResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.ListConfigOptions(callCtx, req)
}

func (c *RequestRouterClient) TestConnection(ctx context.Context, req *pluginv1.TestConnectionRequest) (*pluginv1.TestConnectionResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.TestConnection(callCtx, req)
}
```

- [ ] **Step 2: Add the resolver to `internal/plugins/service.go`** (mirror `ScanSourceClient`)

```go
func (s *Service) RequestRouterClient(ctx context.Context, installationID int, capabilityID string) (*pluginhost.RequestRouterClient, error) {
	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return client.RequestRouter(capabilityID)
}
```

- [ ] **Step 3: Build**

Run: `go build ./internal/pluginhost/ ./internal/plugins/`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/pluginhost/client.go internal/plugins/service.go
git commit -m "feat(pluginhost): typed RequestRouter capability client + resolver"
```

---

### Task 12: `RequestRouterProvider` (domain seam)

**Files:**
- Modify: `internal/requests/go.mod` dependency — bump `silo-plugin-sdk` to the Phase 1 version: `go get github.com/Silo-Server/silo-plugin-sdk@v0.5.0` (run at repo root).
- Create: `internal/requests/provider.go`
- Create: `internal/requests/provider_test.go`

- [ ] **Step 1: Write `internal/requests/provider.go`** — the provider interface + plugin-backed implementation (mirror `autoscan/provider.go`). It translates domain types ↔ proto.

```go
package requests

import (
	"context"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// RequestRouterProvider fulfills a whole request and checks target status via a
// request_router.v1 plugin. The host owns governance (which qualities) and the
// target records; the provider is the plugin boundary.
type RequestRouterProvider interface {
	Fulfill(ctx context.Context, installationID int, capabilityID string, req Request, qualities []Quality, conns []ResolvedRouterConnection) ([]RouterTarget, string, error)
	CheckStatus(ctx context.Context, installationID int, capabilityID string, req Request, targets []RouterTargetRef, conns []ResolvedRouterConnection) ([]RouterTargetStatus, error)
	ListConfigOptions(ctx context.Context, installationID int, capabilityID string, conn ResolvedRouterConnection) (map[string][]RouterOption, error)
	TestConnection(ctx context.Context, installationID int, capabilityID string, conn ResolvedRouterConnection) (bool, string, error)
}

// ResolvedRouterConnection is a connection with plaintext credentials + config.
type ResolvedRouterConnection struct {
	ID      string
	BaseURL string
	APIKey  string
	Config  map[string]any
}

type RouterTarget struct {
	Quality        Quality
	ConnectionID   string
	ExternalID     string
	ExternalStatus string
	Status         Status
	Message        string
}

type RouterTargetRef struct {
	Quality      Quality
	ConnectionID string
	ExternalID   string
}

type RouterTargetStatus struct {
	Quality        Quality
	ConnectionID   string
	Status         Status
	ExternalStatus string
	Message        string
}

type RouterOption struct {
	Value string
	Label string
}

// RouterClientResolver yields a per-(installation, capability) router client.
type RouterClientResolver interface {
	RequestRouterClient(ctx context.Context, installationID int, capabilityID string) (RouterClient, error)
}

// RouterClient is the slice of *pluginhost.RequestRouterClient used here
// (exported so the api package can adapt the concrete type across packages).
type RouterClient interface {
	Fulfill(ctx context.Context, req *pluginv1.FulfillRequest) (*pluginv1.FulfillResponse, error)
	CheckStatus(ctx context.Context, req *pluginv1.CheckStatusRequest) (*pluginv1.CheckStatusResponse, error)
	ListConfigOptions(ctx context.Context, req *pluginv1.ListConfigOptionsRequest) (*pluginv1.ListConfigOptionsResponse, error)
	TestConnection(ctx context.Context, req *pluginv1.TestConnectionRequest) (*pluginv1.TestConnectionResponse, error)
}

type pluginRouterProvider struct{ resolver RouterClientResolver }

func NewPluginRouterProvider(r RouterClientResolver) RequestRouterProvider {
	return &pluginRouterProvider{resolver: r}
}

func descriptor(req Request) *pluginv1.RequestDescriptor {
	ids := map[string]string{}
	if req.TMDBID != 0 {
		ids["tmdb"] = itoa(req.TMDBID)
	}
	if req.TVDBID != nil {
		ids["tvdb"] = itoa(*req.TVDBID)
	}
	if req.IMDbID != "" {
		ids["imdb"] = req.IMDbID
	}
	year := 0
	if req.Year != nil {
		year = *req.Year
	}
	return &pluginv1.RequestDescriptor{
		MediaType: string(req.MediaType), Title: req.Title, Year: int32(year),
		ExternalIds: ids, IsAnime: req.IsAnime,
		RequesterUserId: int32(req.RequestedByUserID), RequesterProfileId: req.RequestedByProfileID,
	}
}

func protoConns(conns []ResolvedRouterConnection) []*pluginv1.RouterConnection {
	out := make([]*pluginv1.RouterConnection, 0, len(conns))
	for _, c := range conns {
		cfg, _ := structpb.NewStruct(c.Config)
		out = append(out, &pluginv1.RouterConnection{Id: c.ID, BaseUrl: c.BaseURL, ApiKey: c.APIKey, Config: cfg})
	}
	return out
}

func (p *pluginRouterProvider) Fulfill(ctx context.Context, installationID int, capabilityID string, req Request, qualities []Quality, conns []ResolvedRouterConnection) ([]RouterTarget, string, error) {
	client, err := p.resolver.RequestRouterClient(ctx, installationID, capabilityID)
	if err != nil {
		return nil, "", err
	}
	qs := make([]string, 0, len(qualities))
	for _, q := range qualities {
		qs = append(qs, string(q))
	}
	resp, err := client.Fulfill(ctx, &pluginv1.FulfillRequest{
		CapabilityId: capabilityID, Request: descriptor(req), Qualities: qs, Connections: protoConns(conns),
	})
	if err != nil {
		return nil, "", err
	}
	targets := make([]RouterTarget, 0, len(resp.GetTargets()))
	for _, t := range resp.GetTargets() {
		targets = append(targets, RouterTarget{
			Quality: Quality(t.GetQuality()), ConnectionID: t.GetConnectionId(),
			ExternalID: t.GetExternalId(), ExternalStatus: t.GetExternalStatus(),
			Status: Status(t.GetStatus()), Message: t.GetMessage(),
		})
	}
	return targets, resp.GetMessage(), nil
}

// CheckStatus, ListConfigOptions, TestConnection follow the same translate-call-translate
// shape; implement them analogously (TargetRef/TargetStatus/ConfigOption mapping).
```

(Implement `CheckStatus`, `ListConfigOptions`, `TestConnection` bodies in the same file using the obvious mappings; add `itoa` via `strconv.Itoa`.)

- [ ] **Step 2: Write `internal/requests/provider_test.go`** — a fake `RouterClient` asserting the descriptor + qualities are forwarded and the response maps back.

```go
package requests

import (
	"context"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type fakeRouterClient struct{ lastReq *pluginv1.FulfillRequest }

func (f *fakeRouterClient) Fulfill(_ context.Context, req *pluginv1.FulfillRequest) (*pluginv1.FulfillResponse, error) {
	f.lastReq = req
	return &pluginv1.FulfillResponse{Targets: []*pluginv1.FulfillmentTarget{
		{Quality: "1080p", ConnectionId: "c1", ExternalId: "7", Status: "queued"},
	}}, nil
}
func (f *fakeRouterClient) CheckStatus(context.Context, *pluginv1.CheckStatusRequest) (*pluginv1.CheckStatusResponse, error) { return &pluginv1.CheckStatusResponse{}, nil }
func (f *fakeRouterClient) ListConfigOptions(context.Context, *pluginv1.ListConfigOptionsRequest) (*pluginv1.ListConfigOptionsResponse, error) { return &pluginv1.ListConfigOptionsResponse{}, nil }
func (f *fakeRouterClient) TestConnection(context.Context, *pluginv1.TestConnectionRequest) (*pluginv1.TestConnectionResponse, error) { return &pluginv1.TestConnectionResponse{Ok: true}, nil }

type fakeResolver struct{ c RouterClient }

func (r fakeResolver) RequestRouterClient(context.Context, int, string) (RouterClient, error) { return r.c, nil }

func TestPluginRouterProviderFulfillTranslates(t *testing.T) {
	fc := &fakeRouterClient{}
	p := NewPluginRouterProvider(fakeResolver{c: fc})
	year := 2020
	req := Request{MediaType: MediaTypeMovie, TMDBID: 42, Title: "X", Year: &year}
	targets, msg, err := p.Fulfill(context.Background(), 1, "arr", req, []Quality{Quality1080p},
		[]ResolvedRouterConnection{{ID: "c1", BaseURL: "http://r", APIKey: "k", Config: map[string]any{"service_kind": "radarr"}}})
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if msg != "" || len(targets) != 1 || targets[0].Status != StatusQueued || targets[0].ExternalID != "7" {
		t.Fatalf("unexpected result: %+v msg=%q", targets, msg)
	}
	if fc.lastReq.GetRequest().GetExternalIds()["tmdb"] != "42" || len(fc.lastReq.GetQualities()) != 1 {
		t.Fatalf("descriptor/qualities not forwarded: %+v", fc.lastReq)
	}
}
```

- [ ] **Step 3: Run**

Run: `go test ./internal/requests/ -run TestPluginRouterProviderFulfillTranslates -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/requests/provider.go internal/requests/provider_test.go go.mod go.sum
git commit -m "feat(requests): plugin-backed RequestRouterProvider seam"
```

---

### Task 13: Refactor `Service` to use the provider; remove the six adapters

**Files:**
- Modify: `internal/requests/service.go` (struct fields ~65-74; `SetFulfillmentAdapters` ~94-114; `submitApprovedRequest`/`submitPlannedTarget`/`submitTarget` ~1168-1266; `reconcileRequest`/`checkFulfillmentStatus` ~1268-1448; `LoadIntegrationOptions` ~825-884; delete the six adapter interfaces ~34-56)

- [ ] **Step 1: Swap the Service fields.** Replace `movieAdapter`/`seriesAdapter` with `router RequestRouterProvider`. Replace `SetFulfillmentAdapters(movie, series)` with:

```go
func (s *Service) SetRouterProvider(p RequestRouterProvider) { s.router = p }
```

Delete the six adapter interface definitions (`MovieFulfillmentAdapter` … `SeriesIntegrationOptionsAdapter`). Keep `SecretResolver`, `EntitlementResolver`, `requesterCeiling`.

- [ ] **Step 2: Add a host-side governance helper** that computes the allowed quality set (replacing the quality half of the old `routeTargets`):

```go
// allowedQualities returns the qualities a request may receive: 1080p always,
// plus 2160p when force-dual is on or the requester's entitlement ceiling allows 4K.
func (s *Service) allowedQualities(ctx context.Context, req Request, settings Settings) []Quality {
	out := []Quality{Quality1080p}
	ceiling := s.requesterCeiling(ctx, req.RequestedByUserID, req.RequestedByProfileID)
	if settings.ForceDualQuality || access.CompareQuality(ceiling, access.PlaybackQuality4K) >= 0 {
		out = append(out, Quality2160p)
	}
	return out
}
```

- [ ] **Step 3: Add a connection resolver helper** that turns enabled `request_router` integrations into `ResolvedRouterConnection`s (resolving the api-key ref to plaintext, attaching `plugin_config`):

```go
func (s *Service) resolveRouterConnections(ctx context.Context, integrations []Integration) ([]ResolvedRouterConnection, int, string, error) {
	var conns []ResolvedRouterConnection
	installationID, capabilityID := 0, ""
	for _, in := range integrations {
		if !in.Enabled || in.CapabilityID != "request_router.v1" || in.InstallationID == nil {
			continue
		}
		apiKey, err := s.resolveAPIKey(ctx, in)
		if err != nil {
			return nil, 0, "", err
		}
		conns = append(conns, ResolvedRouterConnection{ID: in.ID, BaseURL: in.BaseURL, APIKey: apiKey, Config: in.PluginConfig})
		installationID, capabilityID = *in.InstallationID, in.CapabilityID
	}
	return conns, installationID, capabilityID, nil
}
```

(All enabled router connections are sent on every call; the plugin filters by media type. `installationID`/`capabilityID` identify the plugin to dispatch to — all router connections route through the same installed router plugin; if multiple router plugins are installed, group by `installation_id` — note this as a follow-up and assume one router plugin for the first cut.)

- [ ] **Step 4: Rewrite `submitApprovedRequest`** to use the provider. Replace the `routeTargets`/`submitPlannedTargets` body with: compute `allowedQualities`, subtract qualities that already have a healthy (non-failed) target, delete stale failed targets (preserve the existing idempotency), call `s.router.Fulfill(...)`, then persist returned targets via `store.CreateTarget`/`UpdateTargetStatus`. If `Fulfill` returns zero targets with a message, call `markSubmissionFailed(ctx, req.ID, actor, errors.New(msg))`. If `s.router == nil` or no enabled router connections exist, return `markSubmissionFailed` with `"no fulfillment backend configured"` (the pure-plugin "no backend" block).

```go
func (s *Service) submitApprovedRequest(ctx context.Context, req Request, actor Viewer) (*Request, error) {
	if req.Outcome != OutcomeActive || req.Status != StatusApproved {
		return &req, nil
	}
	if s.router == nil {
		return s.markSubmissionFailed(ctx, req.ID, actor, fmt.Errorf("no fulfillment backend configured"))
	}
	integrations, err := s.store.ListIntegrations(ctx)
	if err != nil {
		return nil, err
	}
	conns, installationID, capabilityID, err := s.resolveRouterConnections(ctx, integrations)
	if err != nil {
		return nil, err
	}
	if len(conns) == 0 {
		return s.markSubmissionFailed(ctx, req.ID, actor, fmt.Errorf("no fulfillment backend configured"))
	}
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	existing, err := s.store.ListTargets(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	healthy := map[Quality]bool{}
	for _, t := range existing {
		if t.Status != StatusFailed {
			healthy[t.Quality] = true
		}
	}
	var want []Quality
	for _, q := range s.allowedQualities(ctx, req, settings) {
		if !healthy[q] {
			want = append(want, q)
		}
	}
	if len(want) == 0 {
		return &req, nil
	}
	// drop stale failed targets for the qualities we are re-submitting
	for _, t := range existing {
		if t.Status == StatusFailed {
			for _, q := range want {
				if t.Quality == q {
					if err := s.store.DeleteTarget(ctx, t.ID); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	targets, msg, err := s.router.Fulfill(ctx, installationID, capabilityID, req, want, conns)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		if msg == "" {
			msg = "fulfillment backend created no targets"
		}
		return s.markSubmissionFailed(ctx, req.ID, actor, errors.New(msg))
	}
	latest := &req
	for _, rt := range targets {
		created, err := s.store.CreateTarget(ctx, Target{
			RequestID: req.ID, IntegrationID: rt.ConnectionID, Quality: rt.Quality,
			IsAnime: req.IsAnime, Status: StatusQueued,
		})
		if err != nil {
			return nil, err
		}
		status := rt.Status
		if status == "" {
			status = StatusQueued
		}
		updated, err := s.store.UpdateTargetStatus(ctx, created.ID, status, rt.ExternalID, rt.ExternalStatus, rt.Message, actor)
		if err != nil {
			return nil, err
		}
		if updated != nil {
			latest = updated
		}
	}
	return latest, nil
}
```

Delete `submitPlannedTarget`, `submitPlannedTargets`, `createFailedTarget`, `submitTarget`, `routeTargets`, `resolveInstance`, `integrationKindForMediaType` (the latter three live in the host `routing.go`, deleted in Task 16). `Retry` (lines 577-614) currently calls `submitPlannedTargets` — repoint it to call `submitApprovedRequest` (after it transitions the request back to approved), since the provider path is now idempotent per quality.

- [ ] **Step 5: Rewrite the reconcile status path.** Replace `checkFulfillmentStatus` (per-integration adapter call) with a single provider `CheckStatus` call. In `reconcileRequest`, build `[]RouterTargetRef` from the request's live targets, build `[]ResolvedRouterConnection` from integrations, call `s.router.CheckStatus(...)`, then update each target by matching `(Quality, ConnectionID)`. Map `RouterTargetStatus.Status` → `Status` (and `failed`→ via `UpdateTargetStatus(... StatusFailed ...)`). Keep the existing `requestAvailable`/`hasLiveTargets` shortcut and the `submitApprovedRequest` re-run while `status==approved`.

- [ ] **Step 6: Rewrite `LoadIntegrationOptions`** to call the provider:

```go
func (s *Service) LoadIntegrationOptions(ctx context.Context, viewer Viewer, integration Integration) (*IntegrationOptions, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	// backfill saved base_url/api_key_ref/installation_id/config from the stored row (as today)
	// ... (keep the existing id-backfill block, adapted to read PluginConfig/InstallationID/CapabilityID)
	apiKey, err := s.resolveAPIKey(ctx, integration)
	if err != nil {
		return nil, err
	}
	if s.router == nil || integration.InstallationID == nil {
		return nil, fmt.Errorf("no fulfillment backend configured")
	}
	conn := ResolvedRouterConnection{ID: integration.ID, BaseURL: integration.BaseURL, APIKey: apiKey, Config: integration.PluginConfig}
	opts, err := s.router.ListConfigOptions(ctx, *integration.InstallationID, integration.CapabilityID, conn)
	if err != nil {
		return nil, err
	}
	return integrationOptionsFromRouter(opts), nil // map root_folder/quality_profile_id/tags option lists into IntegrationOptions
}
```

Add `integrationOptionsFromRouter(map[string][]RouterOption) *IntegrationOptions` translating the `options_by_field` keys (`root_folder`→RootFolders, `quality_profile_id`→QualityProfiles, `tags`→Tags). Keep `IntegrationOptions` as the response DTO so the API/React shape is unchanged.

- [ ] **Step 7: Update `validateInstance`** — it currently rejects `kind != radarr/sonarr`. Generalize: require `CapabilityID == "request_router.v1"` and a non-empty `Name`; move the `is_default/is_4k` invariant checks out (the plugin owns them now) or keep them reading from `PluginConfig` if you want host-side validation. For the first cut, validate only `Name` non-empty and `InstallationID != nil`.

- [ ] **Step 8: Build + run the requests test suite**

Run: `go test ./internal/requests/ -v`
Expected: PASS. Update `service_test.go`/`routing_test.go` fixtures: replace adapter fakes with a fake `RequestRouterProvider`; delete tests that asserted the removed in-host `routeTargets` instance-selection (that logic now lives in the plugin and is tested there). Keep tests for `allowedQualities`, idempotency (healthy target skipped), and the "no backend" block.

- [ ] **Step 9: Commit**

```bash
git add internal/requests/service.go internal/requests/service_test.go internal/requests/routing_test.go
git commit -m "feat(requests): route fulfillment through RequestRouterProvider; host keeps quality governance"
```

---

### Task 14: Delete in-host arr code

**Files:**
- Delete: `internal/requests/radarr/`, `internal/requests/sonarr/`, `internal/requests/arrclient/`, `internal/requests/routing.go` (+ `routing_test.go` if it only covered instance selection), `internal/requests/anime.go` only if unused after the change (it feeds `IsAnime` detection on create — keep it).

- [ ] **Step 1: Remove the directories/files**

```bash
git rm -r internal/requests/radarr internal/requests/sonarr internal/requests/arrclient
git rm internal/requests/routing.go
```

- [ ] **Step 2: Build the whole module**

Run: `go build ./...`
Expected: PASS. (Fix any lingering references — there should be none after Task 13.)

- [ ] **Step 3: Commit**

```bash
git add -A
git commit -m "refactor(requests): remove in-host Sonarr/Radarr fulfillment code"
```

---

### Task 15: Update wiring (both sites) + the `api` adapter

**Files:**
- Create: `internal/api/requests_wiring.go` (adapter + builder, mirror `autoscan_wiring.go`)
- Modify: `internal/api/router.go` (~416-431)
- Modify: `cmd/silo/main.go` (~70-72 imports; ~1441-1459)

- [ ] **Step 1: Write `internal/api/requests_wiring.go`**

```go
package api

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/plugins"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// PluginRequestRouterAdapter adapts plugins.Service to
// mediarequests.RouterClientResolver. The concrete *pluginhost.RequestRouterClient
// satisfies mediarequests.RouterClient, so the adapter names the interface as its
// return type (Go has no return-type covariance).
type PluginRequestRouterAdapter struct {
	Svc *plugins.Service
}

func (a PluginRequestRouterAdapter) RequestRouterClient(ctx context.Context, installationID int, capabilityID string) (mediarequests.RouterClient, error) {
	return a.Svc.RequestRouterClient(ctx, installationID, capabilityID)
}

// AttachRequestRouter wires the plugin-backed router provider onto a requests
// service. Both the HTTP handler and the reconcile task call this so the wiring
// lives in one place.
func AttachRequestRouter(svc *mediarequests.Service, pluginService *plugins.Service) {
	svc.SetRouterProvider(mediarequests.NewPluginRouterProvider(PluginRequestRouterAdapter{pluginService}))
}
```

- [ ] **Step 2: Update `internal/api/router.go`** — delete the `radarr`/`sonarr` imports (lines 50-52) and replace `requestSvc.SetFulfillmentAdapters(radarr.NewClient(nil), sonarr.NewClient(nil))` with `AttachRequestRouter(requestSvc, pluginService)`. (Confirm a `*plugins.Service` is in scope in `router.go`; it is used by autoscan wiring already — reuse the same variable.)

- [ ] **Step 3: Update `cmd/silo/main.go`** — delete the `radarr`/`sonarr` imports (lines 71-72) and replace `requestReconcileSvc.SetFulfillmentAdapters(radarr.NewClient(nil), sonarr.NewClient(nil))` with `api.AttachRequestRouter(requestReconcileSvc, pluginService)` (use the same `*plugins.Service` the autoscan task wiring uses; if it is not yet in scope here, pass it down the same way `BuildAutoscanService` receives it).

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api/requests_wiring.go internal/api/router.go cmd/silo/main.go
git commit -m "feat(api): wire plugin-backed request router at both service sites"
```

---

### Task 16: React admin form — plugin-driven config

**Files:**
- Modify: `web/src/pages/AdminRequests.tsx` (`IntegrationEditor`, form state, `formToIntegration`/`integrationToForm`)
- Modify: `web/src/api/types.ts` (`RequestIntegration` — add `capability_id`, `installation_id`, `supported_media_types`, `plugin_config`)
- Modify: `web/src/hooks/queries/useRequests.ts` only if the options response shape changes (it does not — `IntegrationOptions` is preserved)

- [ ] **Step 1: Extend the `RequestIntegration` TS type** with the four generic fields (`capability_id: string`, `installation_id?: number`, `supported_media_types: string[]`, `plugin_config: Record<string, unknown>`), keeping `base_url`, `api_key_ref?`, `has_api_key?` as-is.

- [ ] **Step 2: Move arr fields into `plugin_config`.** In `formToIntegration`, instead of emitting `root_folder`/`quality_profile_id`/`tags`/`is_4k`/`is_default`/`is_default_4k`/`anime_*` as top-level fields, pack them into `plugin_config` (plus `service_kind` from the existing `kind` select, and `search_on_add`/`minimum_availability`/`series_type`/`season_folder`). In `integrationToForm`, read them back out of `plugin_config`. Set `capability_id: "request_router.v1"` and require the operator to select an installed router plugin (`installation_id`) — add a select populated from the installed-plugins query filtered to `request_router.v1` capabilities. Derive `supported_media_types` from `service_kind` (`radarr`→`["movie"]`, `sonarr`→`["series"]`).

- [ ] **Step 3: Keep the dropdown/test-connection UX.** `useLoadRequestIntegrationOptions` still POSTs to `/admin/request-integrations/:id/options` and the response is still `RequestIntegrationOptions` (root folders / quality profiles / tags). No change needed there — the host now sources those from the plugin's `ListConfigOptions`, but the API shape is identical.

- [ ] **Step 4: Lint + typecheck + build**

Run: `cd web && pnpm run lint && pnpm run format:check && pnpm run build`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/AdminRequests.tsx web/src/api/types.ts
git commit -m "feat(web): plugin-driven request integration config form"
```

---

### Task 17: Autoscan-reuse regression test

**Files:**
- Create/extend: `internal/api/autoscan_wiring_test.go` (or `internal/requests/repository_test.go`)

- [ ] **Step 1: Write the test** asserting that after the Task 9 migration, an `Integration` row still exposes `BaseURL`/`APIKeyRef`/`Enabled`, and `RequestIntegrationLookup.Get` resolves them. Use a real DB (the repo test harness) or a fake `*mediarequests.Repository` returning a row with `Enabled=true`, `BaseURL="http://sonarr:8989"`, `APIKeyRef="ref"`.

```go
func TestRequestIntegrationLookupStillResolvesAfterGeneralization(t *testing.T) {
	// Arrange: a request_integrations row with the generic fields populated and
	// arr-specific config folded into plugin_config.
	// Act: RequestIntegrationLookup{Repo}.Get(ctx, id)
	// Assert: returns base_url + api_key_ref, no error; a disabled row errors.
}
```

Fill in using the existing repo test fixtures (see `repository_test.go` patterns). Cover: enabled row resolves; disabled row → error from `checkRequestIntegrationUsable`.

- [ ] **Step 2: Run**

Run: `go test ./internal/api/ -run TestRequestIntegrationLookup -v` (and the autoscan package: `go test ./internal/autoscan/...`)
Expected: PASS — autoscan reuse intact.

- [ ] **Step 3: Commit**

```bash
git add internal/api/autoscan_wiring_test.go
git commit -m "test(autoscan): lock request-integration reuse after connection generalization"
```

---

### Task 18: Full verification

- [ ] **Step 1: Backend lint + tests**

Run: `make lint && go test ./internal/requests/... ./internal/autoscan/... ./internal/pluginhost/... ./internal/plugins/... ./internal/api/...`
Expected: PASS.

- [ ] **Step 2: Frontend gates**

Run: `cd web && pnpm run lint && pnpm run format:check`
Expected: PASS.

- [ ] **Step 3: Local-path guard**

Run: `make verify-local-paths`
Expected: PASS (no absolute paths leaked into specs/plans).

- [ ] **Step 4: End-to-end smoke (manual, against a live arr + the published plugin)**
  - Install `silo.requests.arr` from the catalog; add a Radarr connection (base_url + api key), pick "Radarr", mark Default (HD), select a root folder + quality profile (verifies `ListConfigOptions` + `TestConnection`).
  - Create + approve a movie request; verify a `media_request_targets` row appears with `status=queued` and an `external_id` (verifies `Fulfill` + persistence).
  - Wait for the reconcile task; verify status advances (verifies `CheckStatus`).
  - With the plugin uninstalled, approve a request; verify it fails with "no fulfillment backend configured" (verifies the pure-plugin block).
  - Confirm an autoscan source linked to the same connection still polls (verifies reuse).

- [ ] **Step 5: Confirm no client follow-up** — the request API (lifecycle/status/targets JSON) is unchanged, so `silo-android`/`silo-apple` need no changes. Note this in the PR description.

- [ ] **Step 6: Open the PR** summarizing the three-repo change, linking the spec, and noting the deferred cleanup migration (dropping the old arr columns) and the Seerr follow-on spec.

---

## Self-Review

- **Spec coverage:** §4 SDK contract → Tasks 1-4. §5.1 two-tier registry → Task 9. §5.2 seam swap → Tasks 12-15. §5.3 host-retained governance → Task 13 (`allowedQualities`, target persistence, reconcile). §5.4 plugin-driven admin form → Tasks 13/16. §6 plugin repo → Tasks 5-8. §7 autoscan reuse → Tasks 9 (columns kept) + 17 (regression test). §8 data flow → Tasks 13 (Fulfill/reconcile) + 16 (admin). §9 errors (no-backend block, partial, zero-target message) → Task 13. §10 testing → Tasks 3, 7, 12, 17, 18. §11 sequencing → phase order. Covered.
- **Type consistency:** proto field names (`RouterConnection`, `FulfillmentTarget{quality,connection_id,external_id,external_status,status,message}`, `qualities`, `options_by_field`) are used identically in Phase 2 (`internal/router/server.go`) and Phase 3 (`internal/requests/provider.go`). Host `Quality` values `"1080p"`/`"2160p"` match the plugin's `RouteTargets` switch and the `media_request_targets_quality_check` constraint. `Status` values `queued/downloading/completed/failed` match `media_request_targets_status_check`.
- **Placeholder scan:** the only "implement analogously" notes (provider's `CheckStatus`/`ListConfigOptions`/`TestConnection`, reconcile status-update loop, the `LoadIntegrationOptions` id-backfill block, the admin form field moves) reference fully-specified shapes defined earlier in the same task — they are mechanical mirrors of code shown verbatim, not undefined work. Relocated arr files (Task 6) are copied verbatim from named host paths, not re-authored.
- **Known risk to watch during execution:** Task 13 Step 3's "one router plugin" assumption — if multiple `request_router.v1` plugins are installed, connections must be grouped by `installation_id` before calling `Fulfill`. Flagged inline as a follow-up; the first cut assumes a single router plugin.

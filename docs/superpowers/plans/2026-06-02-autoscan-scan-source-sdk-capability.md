# `scan_source.v1` SDK Capability — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a new, additive `scan_source.v1` plugin capability to `silo-plugin-sdk` so the Silo host can pull changed paths from a scan-source plugin on a timer.

**Architecture:** Mirror the existing `scheduled_task.v1` capability exactly — a single-RPC, request/response gRPC service defined in a `.proto`, generated into `pkg/pluginproto`, then registered in three Go helper sites (`capability.KnownTypes`, the `runtime.CapabilityServers` struct, and the `runtime.GRPCPlugin` server registration). Nothing existing is modified in shape; only new symbols are added. Tag a new SDK release so the host and the arr plugin can build against it.

**Tech Stack:** Go, Protocol Buffers (proto3), `buf` codegen (`buf.gen.yaml`), `google.golang.org/protobuf`, `google.golang.org/grpc`, the `hashicorp/go-plugin` runtime the SDK wraps.

---

## Prerequisites (read before starting)

This plan targets the **`silo-plugin-sdk`** repository (Go module `github.com/Silo-Server/silo-plugin-sdk`), which is **not currently checked out** in this workspace — only `silo-server` and `silo-plugin-tmdb` are. Before executing:

1. Clone `silo-plugin-sdk` as a writable working copy beside the other repos. Commands below assume **the silo-plugin-sdk repository root is the cwd**.
2. Ensure the proto toolchain is available: the `make proto` target auto-installs `buf`, `protoc-gen-go@v1.36.11`, and `protoc-gen-go-grpc@v1.6.1` into `$(GO_BIN)`, but it requires the **`protoc`** binary to already be on `PATH` (it errors `protoc is required` otherwise). Install `protoc` if absent.
3. This SDK is the **long pole**: the silo-server host plan and the arr-plugin plan both depend on the version tagged at the end of this plan (Task 6). Do not start those until this is tagged.

**Pattern reference (current `scheduled_task.v1`, the template this mirrors):**
- Proto: `proto/silo/plugin/v1/scheduled_task.proto`
- Generated: `pkg/pluginproto/silo/plugin/v1/scheduled_task.pb.go` + `scheduled_task_grpc.pb.go`
- Capability const + allowlist: `pkg/pluginsdk/capability/capability.go`
- Runtime wiring: `pkg/pluginsdk/runtime/runtime.go` (struct field, `Client` accessor, `GRPCServer` registration)
- Manifest acceptance test pattern: `pkg/pluginsdk/manifest/manifest_test.go`

---

## File Structure

| File | Responsibility | Action |
|---|---|---|
| `proto/silo/plugin/v1/scan_source.proto` | The `ScanSource` service + `PollChanges` request/response messages | Create |
| `pkg/pluginproto/silo/plugin/v1/scan_source.pb.go` | Generated message types | Create (codegen) |
| `pkg/pluginproto/silo/plugin/v1/scan_source_grpc.pb.go` | Generated client/server stubs | Create (codegen) |
| `pkg/pluginsdk/capability/capability.go` | `ScanSource` const + add to `KnownTypes` allowlist | Modify |
| `pkg/pluginsdk/capability/capability_test.go` | Assert `ScanSource` is a known type | Create or Modify |
| `pkg/pluginsdk/runtime/runtime.go` | `CapabilityServers.ScanSource` field, `Client.ScanSource()` accessor, `GRPCServer` registration | Modify |
| `pkg/pluginsdk/runtime/runtime_test.go` | Assert a `ScanSource` server registers without error | Create or Modify |
| `pkg/pluginsdk/manifest/manifest_test.go` | Assert a manifest declaring `scan_source.v1` loads | Modify |

---

## Task 1: Define and generate the `scan_source.v1` proto

**Files:**
- Create: `proto/silo/plugin/v1/scan_source.proto`
- Create (via codegen): `pkg/pluginproto/silo/plugin/v1/scan_source.pb.go`, `pkg/pluginproto/silo/plugin/v1/scan_source_grpc.pb.go`

- [ ] **Step 1: Write the proto**

Create `proto/silo/plugin/v1/scan_source.proto` (the `go_package` option must match the sibling protos exactly so generated code lands in package `pluginv1`):

```proto
syntax = "proto3";

package silo.plugin.v1;

option go_package = "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1;pluginv1";

// ScanSource lets the host pull changed filesystem paths from a provider
// (e.g. Sonarr/Radarr, inotify, Ceph). Pull only: the host owns the timer and
// calls PollChanges; the plugin never initiates.
service ScanSource {
  rpc PollChanges(PollChangesRequest) returns (PollChangesResponse);
}

message PollChangesRequest {
  // Which configured scan_source capability instance is being polled.
  string capability_id = 1;
  // Opaque continuation token from the previous PollChanges. Empty on first
  // run, which the provider treats as "start from now" (do not replay history).
  string marker = 2;
}

message PollChangesResponse {
  // Absolute paths already translated into Silo's filesystem namespace (the
  // plugin has applied its own path rewrites). Files or directories.
  repeated string changed_paths = 1;
  // Opaque continuation token. The host stores it verbatim and echoes it back
  // on the next PollChanges; the host never parses it.
  string next_marker = 2;
}
```

- [ ] **Step 2: Generate the Go code**

Run: `make proto`
Expected: exits 0; creates `pkg/pluginproto/silo/plugin/v1/scan_source.pb.go` and `scan_source_grpc.pb.go`. (If it prints `protoc is required`, install `protoc` and re-run.)

- [ ] **Step 3: Verify the generated symbols exist and compile**

Run: `go build ./... && grep -l "func NewScanSourceClient" pkg/pluginproto/silo/plugin/v1/scan_source_grpc.pb.go && grep -l "RegisterScanSourceServer" pkg/pluginproto/silo/plugin/v1/scan_source_grpc.pb.go`
Expected: build succeeds; both `grep`s print the filename (confirming `ScanSourceClient`, `ScanSourceServer`, `NewScanSourceClient`, `RegisterScanSourceServer` were generated — `require_unimplemented_servers=false` in `buf.gen.yaml` means no forced `UnimplementedScanSourceServer` embedding).

- [ ] **Step 4: Commit**

```bash
git add proto/silo/plugin/v1/scan_source.proto pkg/pluginproto/silo/plugin/v1/scan_source.pb.go pkg/pluginproto/silo/plugin/v1/scan_source_grpc.pb.go
git commit -m "feat(proto): add scan_source.v1 capability service"
```

---

## Task 2: Register the capability type in the allowlist

The manifest loader rejects capability types not in `capability.KnownTypes` (see `TestLoadRejectsUnknownCapabilityType`). A manifest declaring `scan_source.v1` cannot load until the type is registered here.

**Files:**
- Modify: `pkg/pluginsdk/capability/capability.go`
- Create or Modify: `pkg/pluginsdk/capability/capability_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/pluginsdk/capability/capability_test.go` (create the file with this `package capability` header if it does not exist):

```go
package capability

import "testing"

func TestScanSourceIsKnownType(t *testing.T) {
	if ScanSource != "scan_source.v1" {
		t.Fatalf("ScanSource const = %q, want %q", ScanSource, "scan_source.v1")
	}
	found := false
	for _, k := range KnownTypes {
		if k == ScanSource {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ScanSource (%q) missing from KnownTypes %v", ScanSource, KnownTypes)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/pluginsdk/capability/ -run TestScanSourceIsKnownType -v`
Expected: FAIL — `undefined: ScanSource` (the const does not exist yet).

- [ ] **Step 3: Add the const and allowlist entry**

In `pkg/pluginsdk/capability/capability.go`, add the const inside the existing `const (...)` block (after `EbookBackend`):

```go
	EbookBackend     = "ebook_backend.v1"
	ScanSource       = "scan_source.v1"
```

and add it to the `KnownTypes` slice (append `ScanSource` to the existing list):

```go
	ScanSource,
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/pluginsdk/capability/ -run TestScanSourceIsKnownType -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/pluginsdk/capability/capability.go pkg/pluginsdk/capability/capability_test.go
git commit -m "feat(capability): register scan_source.v1 as a known capability type"
```

---

## Task 3: Wire the capability into the runtime

Add the `ScanSource` server to `CapabilityServers`, a `Client.ScanSource()` accessor, and the conditional registration in `GRPCServer` — mirroring `ScheduledTask` in `pkg/pluginsdk/runtime/runtime.go`.

**Files:**
- Modify: `pkg/pluginsdk/runtime/runtime.go`
- Create or Modify: `pkg/pluginsdk/runtime/runtime_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/pluginsdk/runtime/runtime_test.go` (create with the `package runtime` header if absent). This test constructs a real `*grpc.Server`, supplies a minimal `ScanSource` server, and asserts `GRPCServer` registers it without error:

```go
package runtime

import (
	"context"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/grpc"
)

type stubScanSource struct{}

func (stubScanSource) PollChanges(context.Context, *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error) {
	return &pluginv1.PollChangesResponse{}, nil
}

func TestGRPCServerRegistersScanSource(t *testing.T) {
	p := &GRPCPlugin{Servers: CapabilityServers{
		Runtime:    stubRuntime{},   // existing stub used by sibling runtime tests
		ScanSource: stubScanSource{},
	}}
	srv := grpc.NewServer()
	if err := p.GRPCServer(nil, srv); err != nil {
		t.Fatalf("GRPCServer with ScanSource = %v, want nil", err)
	}
	if _, ok := srv.GetServiceInfo()["silo.plugin.v1.ScanSource"]; !ok {
		t.Fatalf("ScanSource service not registered; got %v", srv.GetServiceInfo())
	}
}
```

Note: `GRPCServer` requires `Servers.Runtime` to be non-nil (it errors otherwise). Reuse the existing runtime stub the sibling tests use. If no such stub exists in `runtime_test.go`, add a minimal one:

```go
type stubRuntime struct{ pluginv1.RuntimeServer }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/pluginsdk/runtime/ -run TestGRPCServerRegistersScanSource -v`
Expected: FAIL — `unknown field 'ScanSource' in struct literal of type CapabilityServers` (the field does not exist yet).

- [ ] **Step 3: Add the struct field**

In `pkg/pluginsdk/runtime/runtime.go`, add to the `CapabilityServers` struct (after the `ScheduledTask` field):

```go
	ScheduledTask    pluginv1.ScheduledTaskServer
	ScanSource       pluginv1.ScanSourceServer
```

- [ ] **Step 4: Add the `Client` accessor**

After the existing `func (c *Client) ScheduledTask() ...` method, add:

```go
func (c *Client) ScanSource() pluginv1.ScanSourceClient {
	return pluginv1.NewScanSourceClient(c.conn)
}
```

- [ ] **Step 5: Add the conditional registration in `GRPCServer`**

In `func (p *GRPCPlugin) GRPCServer(...)`, after the `ScheduledTask` registration block, add:

```go
	if p.Servers.ScheduledTask != nil {
		pluginv1.RegisterScheduledTaskServer(server, p.Servers.ScheduledTask)
	}
	if p.Servers.ScanSource != nil {
		pluginv1.RegisterScanSourceServer(server, p.Servers.ScanSource)
	}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./pkg/pluginsdk/runtime/ -run TestGRPCServerRegistersScanSource -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/pluginsdk/runtime/runtime.go pkg/pluginsdk/runtime/runtime_test.go
git commit -m "feat(runtime): serve and dial scan_source.v1 capability"
```

---

## Task 4: Prove a `scan_source.v1` manifest loads

Guards the end-to-end path: a plugin manifest declaring the new capability must pass the loader (it would not before Task 2's allowlist entry).

**Files:**
- Modify: `pkg/pluginsdk/manifest/manifest_test.go`

- [ ] **Step 1: Write the failing test**

The test file is package `manifest_test` (external) and loads from a JSON literal via `manifest.Load([]byte)`. Append this test, mirroring `TestLoadAcceptsRequestRouterCapability` exactly with the type swapped to `scan_source.v1`:

```go
func TestLoadAcceptsScanSourceCapability(t *testing.T) {
	raw := []byte(`{
	  "plugin_id": "silo.example",
	  "version": "1.0.0",
	  "silo_api_version": "v1",
	  "capabilities": [
	    {"type": "scan_source.v1", "id": "arr", "display_name": "X", "description": "Y"}
	  ]
	}`)
	m, err := manifest.Load(raw)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if len(m.GetCapabilities()) != 1 {
		t.Fatalf("expected 1 capability, got %d", len(m.GetCapabilities()))
	}
	if got := m.GetCapabilities()[0].GetType(); got != "scan_source.v1" {
		t.Fatalf("capability type = %q, want scan_source.v1", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails (before this task's dependency was in place) / passes now**

Run: `go test ./pkg/pluginsdk/manifest/ -run TestLoadAcceptsScanSourceCapability -v`
Expected: PASS (Task 2 already added `scan_source.v1` to `KnownTypes`). If it FAILS with an unknown-capability-type error, Task 2 was not completed correctly — fix that first.

- [ ] **Step 3: Commit**

```bash
git add pkg/pluginsdk/manifest/manifest_test.go
git commit -m "test(manifest): accept scan_source.v1 capability in manifests"
```

---

## Task 5: Full build + test sweep

**Files:** none (verification only)

- [ ] **Step 1: Build everything**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 2: Run the full SDK test suite**

Run: `go test ./...`
Expected: all packages `ok`. Confirms the additive change broke no existing capability, manifest, or runtime test.

- [ ] **Step 3: Vet + format**

Run: `go vet ./... && gofmt -l pkg proto 2>/dev/null; test -z "$(gofmt -l pkg)"`
Expected: vet clean; `gofmt -l pkg` prints nothing.

---

## Task 6: Tag the release

Downstream repos (silo-server host, arr plugin) consume the SDK as a tagged module version.

**Files:** none (release action)

- [ ] **Step 1: Confirm the version to tag**

Check the latest tag: `git tag --list 'v*' | sort -V | tail -3`. The current consumed version is `v0.4.0`; this additive feature warrants a minor bump to **`v0.5.0`** (no breaking changes).

- [ ] **Step 2: Tag and push**

```bash
git tag v0.5.0
git push origin v0.5.0
```

- [ ] **Step 3: Record the contract for downstream plans**

Note in the host and arr-plugin plans that they must require `github.com/Silo-Server/silo-plugin-sdk v0.5.0` and use:
- `capability.ScanSource` (`"scan_source.v1"`)
- `runtime.CapabilityServers{ ScanSource: <impl> }` (plugin side)
- `client.ScanSource()` → `pluginv1.ScanSourceClient.PollChanges(ctx, &pluginv1.PollChangesRequest{CapabilityId, Marker})` (host side)
- response fields `PollChangesResponse.ChangedPaths []string`, `PollChangesResponse.NextMarker string`

---

## Self-Review notes

- **Spec coverage:** Implements spec §5 (the `scan_source.v1` contract: `PollChanges`, opaque marker, Silo-native paths) and §11 (additive-only; registered in `KnownTypes`; no existing capability modified). Spec §12 build-order step 1 (tag the SDK) is Task 6.
- **Out of scope here:** the host engine (spec §7), connections (§8), the Autoscan category (§9), and the arr plugin (§10) are separate per-repo plans built against the `v0.5.0` tag produced by Task 6.
- **Marker is opaque end-to-end:** the proto carries `marker`/`next_marker` as plain strings with no server-side interpretation, satisfying the "any provider's bookmark" requirement.

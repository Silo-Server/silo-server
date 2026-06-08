# Typed 4K Quality-Tier Signal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the stringly-typed `"2160p"` 4K detection in request_router plugins with an explicit `is4k` field the host stamps into the contract.

**Architecture:** A structured `RequestedQuality{id, is4k}` replaces `FulfillRequest.qualities`'s `repeated string`. The host (the owner of quality governance) stamps `is4k`; the plugins route/branch on it and echo `id` as the unchanged per-target identity string. One coordinated change across SDK → host → arr → seerr.

**Tech Stack:** protobuf (buf regen), Go 1.26, `github.com/Silo-Server/silo-plugin-sdk` (local `replace`).

**Spec:** `docs/superpowers/specs/2026-06-08-typed-quality-tier-signal-design.md`

**Conventions for every task:**
- Go toolchain: `/opt/deployarr/.local/go-sdk/go/bin/go` (the plan writes `go` — always use the full path).
- LOCAL-ONLY: `git commit` only — never push/tag/PR. The `replace => /opt/silo-plugin-sdk` in each plugin's `go.mod` stays.
- **Edit/Write tools work for `/opt/silo` (host) files only.** SDK (`/opt/silo-plugin-sdk`) and plugin (`/opt/silo-plugin-requests-arr`, `/opt/silo-plugin-requests-seerr`) repos are path-guarded — modify their files via **Bash heredocs** (rewrite the whole file or changed region after reading it).
- Build order is mandatory: the SDK proto type must exist before host/plugins compile against it.
- SDK branch `feat/request-router-capability`; host `feat/requests-pluginization`; plugins `master`. Leave branches as-is.

---

## File Structure

```
/opt/silo-plugin-sdk/proto/silo/plugin/v1/request_router.proto   # +RequestedQuality, FulfillRequest.qualities type (Task 1)
/opt/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1/request_router.pb.go  # regenerated (Task 1)

/opt/silo/internal/requests/provider.go        # stamp is4k at the one Fulfill construction site (Task 2)
/opt/silo/internal/requests/provider_test.go   # assert structured qualities (Task 2)

/opt/silo-plugin-requests-arr/internal/arr/routing.go   # RequestedQuality type + route by Is4K (Task 3)
/opt/silo-plugin-requests-arr/internal/arr/types.go     # delete Quality1080p/2160p (+ Quality type if unused) (Task 3)
/opt/silo-plugin-requests-arr/internal/router/server.go # map proto -> arr.RequestedQuality (Task 3)
/opt/silo-plugin-requests-arr/internal/router/server_test.go # Qualities literals -> RequestedQuality (Task 3)

/opt/silo-plugin-requests-seerr/internal/router/server.go      # read q.GetIs4K()/GetId(); delete quality4K (Task 4)
/opt/silo-plugin-requests-seerr/internal/router/server_test.go # Qualities literals -> RequestedQuality (Task 4)
```

---

## Task 1: SDK proto — `RequestedQuality` + regen

**Files:**
- Modify: `/opt/silo-plugin-sdk/proto/silo/plugin/v1/request_router.proto`
- Regenerate: `/opt/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1/request_router.pb.go`

- [ ] **Step 1: Edit the proto** — replace the `FulfillRequest` block and add `RequestedQuality` just before it.

Read `request_router.proto`. The current `FulfillRequest` is:
```proto
message FulfillRequest {
  string capability_id = 1;
  RequestDescriptor request = 2;
  repeated string qualities = 3;
  repeated RouterConnection connections = 4;
}
```
Replace it (via heredoc rewrite of the file, or `sed`-style targeted replacement) with:
```proto
// RequestedQuality is one quality tier the host wants fulfilled. The host owns
// quality governance, so it stamps is4k rather than each plugin re-deriving it
// from the id string. id is the tier identity echoed back as
// FulfillmentTarget.quality (e.g. "1080p" / "2160p").
message RequestedQuality {
  string id = 1;
  bool is4k = 2;
}

message FulfillRequest {
  string capability_id = 1;
  RequestDescriptor request = 2;
  repeated RequestedQuality qualities = 3;
  repeated RouterConnection connections = 4;
}
```
Leave `FulfillmentTarget`, `TargetRef`, `TargetStatus`, and everything else unchanged.

- [ ] **Step 2: Regenerate the Go code**

Run:
```bash
cd /opt/silo-plugin-sdk && PATH="$(pwd)/bin:$PATH" buf generate
```
Expected: regenerates `pkg/pluginproto/.../request_router.pb.go`. (The `make proto` target aborts on a `protoc` precheck that isn't satisfied on this host; `buf generate` with the local `bin/` on PATH is the documented fallback and produces identical output. If `bin/buf` is missing, install it first: `GOBIN="$(pwd)/bin" /opt/deployarr/.local/go-sdk/go/bin/go install github.com/bufbuild/buf/cmd/buf@latest`, then re-run.)

- [ ] **Step 3: Verify the generated types**

Run:
```bash
cd /opt/silo-plugin-sdk && grep -n "type RequestedQuality\|func (x \*RequestedQuality) GetId\|func (x \*RequestedQuality) GetIs4\|func (x \*FulfillRequest) GetQualities" pkg/pluginproto/silo/plugin/v1/request_router.pb.go
```
Expected: `type RequestedQuality struct`, `GetId() string`, the is4k getter, and `GetQualities() []*RequestedQuality` all present. **IMPORTANT — verify the exact is4k identifier:** `protoc-gen-go`'s `GoCamelCase` capitalizes a lowercase letter that follows a digit, so the proto field `is4k` generates the Go field **`Is4K`** and getter **`GetIs4K()`** (NOT `Is4K`/`GetIs4K`). This plan uses `Is4K`/`GetIs4K()` everywhere on that basis; if the grep shows a different identifier, use whatever the generated code actually declares and adjust the later tasks accordingly.

- [ ] **Step 4: Build the SDK**

Run: `cd /opt/silo-plugin-sdk && /opt/deployarr/.local/go-sdk/go/bin/go build ./... && /opt/deployarr/.local/go-sdk/go/bin/go test ./... -count=1`
Expected: builds; existing SDK tests pass (the change is mechanical; no SDK code consumes `FulfillRequest.qualities`).

- [ ] **Step 5: Commit**

```bash
cd /opt/silo-plugin-sdk
git add proto/silo/plugin/v1/request_router.proto pkg/pluginproto/silo/plugin/v1/request_router.pb.go
git commit -m "feat(proto): typed RequestedQuality{id,is4k} for FulfillRequest.qualities"
```

---

## Task 2: Host — stamp `is4k` at the Fulfill construction site

**Files:**
- Modify: `/opt/silo/internal/requests/provider.go` (the `qs := make([]string, …)` loop in `Fulfill`)
- Test: `/opt/silo/internal/requests/provider_test.go`

Host files are under `/opt/silo` — use the Edit tool.

- [ ] **Step 1: Update the failing test first** (TDD — assert the structured qualities)

In `internal/requests/provider_test.go`, the `TestPluginRouterProviderFulfillTranslates` test currently asserts only the count:
```go
	if fc.lastReq.GetRequest().GetExternalIds()["tmdb"] != "42" || len(fc.lastReq.GetQualities()) != 1 {
		t.Fatalf("descriptor/qualities not forwarded: %+v", fc.lastReq)
	}
```
Replace with an assertion on the structured value (the test sends `[]Quality{Quality1080p}`, so the single quality must be `{Id:"1080p", Is4K:false}`):
```go
	gotQ := fc.lastReq.GetQualities()
	if fc.lastReq.GetRequest().GetExternalIds()["tmdb"] != "42" || len(gotQ) != 1 {
		t.Fatalf("descriptor/qualities not forwarded: %+v", fc.lastReq)
	}
	if gotQ[0].GetId() != "1080p" || gotQ[0].GetIs4K() {
		t.Fatalf("1080p should be id=1080p is4k=false, got %+v", gotQ[0])
	}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /opt/silo && /opt/deployarr/.local/go-sdk/go/bin/go test ./internal/requests/ -run TestPluginRouterProviderFulfillTranslates -count=1`
Expected: FAIL — `gotQ[0].GetId undefined` (qualities is still `[]string`, so `GetQualities()` elements have no `GetId`). This compile failure is the red state.

- [ ] **Step 3: Update `provider.go`'s Fulfill construction**

In `internal/requests/provider.go`, replace the `[]string` qualities loop:
```go
	qs := make([]string, 0, len(qualities))
	for _, q := range qualities {
		qs = append(qs, string(q))
	}
```
with the structured loop (the host owns the tier vocabulary; `Quality2160p` is the same-package constant from `internal/requests/types.go`):
```go
	qs := make([]*pluginv1.RequestedQuality, 0, len(qualities))
	for _, q := range qualities {
		qs = append(qs, &pluginv1.RequestedQuality{Id: string(q), Is4K: q == Quality2160p})
	}
```
The `Qualities: qs` field in the `&pluginv1.FulfillRequest{...}` literal is unchanged (it now takes `[]*pluginv1.RequestedQuality`). Nothing else in the function changes — the returned-target loop reads `t.GetQuality()` (the identity), untouched.

- [ ] **Step 4: Run the requests tests**

Run: `cd /opt/silo && /opt/deployarr/.local/go-sdk/go/bin/go build ./internal/requests/ && /opt/deployarr/.local/go-sdk/go/bin/go vet ./internal/requests/ && /opt/deployarr/.local/go-sdk/go/bin/go test ./internal/requests/ -count=1`
Expected: builds; all `internal/requests` tests pass (incl. the updated assertion). `internal/requests` is pure Go (bare-host OK; the libvips container is only needed for the whole-tree/api build, done in Task 5).

- [ ] **Step 5: Commit**

```bash
cd /opt/silo
git add internal/requests/provider.go internal/requests/provider_test.go
git commit -m "feat(requests): stamp is4k per quality (host owns the 4K-tier fact)"
```

---

## Task 3: arr plugin — route by `Is4K`

**Files:**
- Modify: `/opt/silo-plugin-requests-arr/internal/arr/routing.go`
- Modify: `/opt/silo-plugin-requests-arr/internal/arr/types.go` (delete `Quality1080p`/`Quality2160p`, and the `Quality` type if unused)
- Modify: `/opt/silo-plugin-requests-arr/internal/router/server.go`
- Test: `/opt/silo-plugin-requests-arr/internal/arr/routing_test.go` (new or existing) + `internal/router/server_test.go`

- [ ] **Step 1: Write a failing RouteTargets test** (drives routing by `Is4K`, no string literals)

```bash
cat > /opt/silo-plugin-requests-arr/internal/arr/routing_test.go <<'EOF'
package arr

import "testing"

func TestRouteTargetsRoutesByIs4K(t *testing.T) {
	instances := []Instance{
		{ID: "hd", Kind: "radarr", Enabled: true, IsDefault: true},
		{ID: "uhd", Kind: "radarr", Enabled: true, IsDefault4K: true, Is4K: true},
	}
	req := Request{MediaType: MediaTypeMovie}
	planned := RouteTargets(req, []RequestedQuality{
		{ID: "1080p", Is4K: false},
		{ID: "2160p", Is4K: true},
	}, instances)
	if len(planned) != 2 {
		t.Fatalf("want 2 targets, got %d", len(planned))
	}
	byQ := map[string]string{}
	for _, p := range planned {
		byQ[p.Quality] = p.Instance.ID
	}
	if byQ["1080p"] != "hd" || byQ["2160p"] != "uhd" {
		t.Fatalf("routing wrong: %+v", byQ)
	}
}
EOF
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd /opt/silo-plugin-requests-arr && /opt/deployarr/.local/go-sdk/go/bin/go test ./internal/arr/ -run TestRouteTargetsRoutesByIs4K -count=1`
Expected: FAIL — `undefined: RequestedQuality` (the type + the new RouteTargets signature don't exist yet).

- [ ] **Step 3: Rewrite `routing.go`'s type + RouteTargets**

Read `internal/arr/routing.go`. Replace the `PlannedTarget` doc + `RouteTargets` func with this (add the `RequestedQuality` type; route by `Is4K`; keep `ResolveInstance` and everything below it unchanged):
```go
// PlannedTarget is a routing decision: which instance, at which quality, and
// whether anime overlays apply. Quality carries the proto-facing identity
// string ("1080p" / "2160p") so the router can echo it straight back to the host.
type PlannedTarget struct {
	Instance Instance
	Quality  string
	IsAnime  bool
}

// RequestedQuality is one host-requested tier: its identity string plus whether
// it is the 4K tier (the host stamps is4k; the plugin no longer parses the id).
type RequestedQuality struct {
	ID   string
	Is4K bool
}

// RouteTargets maps the host-requested qualities onto configured instances for
// the request's kind (radarr for movies, sonarr otherwise).
//
// For each requested quality it selects the enabled default instance for that
// tier — IsDefault for the HD tier, IsDefault4K when the host marked the tier
// is4k — and emits one PlannedTarget per quality that has a matching instance.
// Qualities with no matching instance are omitted, so an unconfigured tier
// silently yields no target rather than an error.
func RouteTargets(req Request, qualities []RequestedQuality, instances []Instance) []PlannedTarget {
	wantKind := "radarr"
	if req.MediaType != MediaTypeMovie {
		wantKind = "sonarr"
	}

	var targets []PlannedTarget
	for _, q := range qualities {
		var match *Instance
		for i := range instances {
			in := &instances[i]
			if !in.Enabled || in.Kind != wantKind {
				continue
			}
			if q.Is4K {
				if in.IsDefault4K {
					match = in
				}
			} else {
				if in.IsDefault {
					match = in
				}
			}
			if match != nil {
				break
			}
		}
		if match == nil {
			continue
		}
		targets = append(targets, PlannedTarget{
			Instance: *match,
			Quality:  q.ID,
			IsAnime:  req.IsAnime && match.AnimeEnabled,
		})
	}
	return targets
}
```

- [ ] **Step 4: Map the proto input in `server.go`**

Read `internal/router/server.go`. In `Fulfill`, replace the `planned := arr.RouteTargets(r, req.GetQualities(), instances)` line with a mapping from the proto `RequestedQuality` to the arr-local type:
```go
	qualities := make([]arr.RequestedQuality, 0, len(req.GetQualities()))
	for _, q := range req.GetQualities() {
		qualities = append(qualities, arr.RequestedQuality{ID: q.GetId(), Is4K: q.GetIs4K()})
	}
	planned := arr.RouteTargets(r, qualities, instances)
```
The rest of `Fulfill` (the `for _, pt := range planned` loop, `FulfillmentTarget{Quality: pt.Quality, ...}`) is unchanged.

- [ ] **Step 5: Delete the dead tier constants in `types.go`**

Run a grep to confirm `Quality1080p`/`Quality2160p`/the `Quality` type are now unused outside their own declaration:
```bash
cd /opt/silo-plugin-requests-arr && grep -rn "Quality1080p\|Quality2160p\|Quality(" internal/ | grep -v "_test.go"
```
Remove the `Quality1080p`/`Quality2160p` const lines from `internal/arr/types.go`. If the named `Quality` type (`type Quality string`) is now referenced nowhere (the grep above shows only the deleted decls), remove the type too. If anything else still uses it, leave the type and report. (After this, no quality-tier string literal remains in `internal/arr`.)

- [ ] **Step 6: Update `server_test.go`'s Qualities literals**

Read `internal/router/server_test.go`. Every place that builds a `FulfillRequest` with `Qualities: []string{"1080p"}` / `[]string{"1080p","2160p"}` must become `Qualities: []*pluginv1.RequestedQuality{{Id: "1080p", Is4K: false}}` / `{{Id: "1080p", Is4K: false}, {Id: "2160p", Is4K: true}}`. Keep every assertion identical (the emitted `FulfillmentTarget.Quality` is still the id). The `pluginv1` import is already present in that test file.

- [ ] **Step 7: Build, vet, gofmt, test**

Run:
```bash
cd /opt/silo-plugin-requests-arr && /opt/deployarr/.local/go-sdk/go/bin/go build ./... && /opt/deployarr/.local/go-sdk/go/bin/go vet ./... && $(/opt/deployarr/.local/go-sdk/go/bin/go env GOROOT)/bin/gofmt -l . && /opt/deployarr/.local/go-sdk/go/bin/go test ./... -count=1
```
Expected: build/vet clean; `gofmt -l .` empty (run `gofmt -w .` if not); all tests pass (incl. the new `routing_test.go` and the updated `server_test.go`).

- [ ] **Step 8: Commit**

```bash
cd /opt/silo-plugin-requests-arr
git add -A
git commit -m "refactor(arr): route by RequestedQuality.is4k; drop hardcoded tier strings"
```

---

## Task 4: seerr plugin — read `is4k` from the field

**Files:**
- Modify: `/opt/silo-plugin-requests-seerr/internal/router/server.go`
- Test: `/opt/silo-plugin-requests-seerr/internal/router/server_test.go`

- [ ] **Step 1: Update `server.go`'s Fulfill loop + delete `quality4K`**

Read `internal/router/server.go`. Delete the `const quality4K = "2160p"` declaration. In `Fulfill`, replace the inner loop:
```go
		for _, q := range req.GetQualities() {
			is4k := q == quality4K
			if is4k && !conn.Supports4K {
				continue // this connection does not fulfill 4K
			}
			targets = append(targets, s.fulfillOne(ctx, client, conn.ID, q, mediaType, tmdbID, is4k))
		}
```
with:
```go
		for _, q := range req.GetQualities() {
			is4k := q.GetIs4K()
			if is4k && !conn.Supports4K {
				continue // this connection does not fulfill 4K
			}
			targets = append(targets, s.fulfillOne(ctx, client, conn.ID, q.GetId(), mediaType, tmdbID, is4k))
		}
```
`fulfillOne`'s signature (`connID, quality, mediaType string, …, is4k bool`) is unchanged — it now receives `q.GetId()` for the quality and `q.GetIs4K()` for is4k. Its body is unchanged.

- [ ] **Step 2: Update `server_test.go`'s Qualities literals**

Read `internal/router/server_test.go`. Replace every `Qualities: []string{"1080p"}` / `[]string{"1080p","2160p"}` with `Qualities: []*pluginv1.RequestedQuality{{Id: "1080p", Is4K: false}}` / `{{Id: "1080p", Is4K: false}, {Id: "2160p", Is4K: true}}`. Keep all assertions identical (the HD-only, HD+4K-supported, 4K-skipped-when-unsupported cases are now driven by the `Is4K` field; the emitted `FulfillmentTarget.Quality` is still the id). `pluginv1` is already imported in that test.

- [ ] **Step 3: Build, vet, gofmt, test**

Run:
```bash
cd /opt/silo-plugin-requests-seerr && /opt/deployarr/.local/go-sdk/go/bin/go build ./... && /opt/deployarr/.local/go-sdk/go/bin/go vet ./... && $(/opt/deployarr/.local/go-sdk/go/bin/go env GOROOT)/bin/gofmt -l . && /opt/deployarr/.local/go-sdk/go/bin/go test ./... -count=1
```
Expected: build/vet clean; `gofmt -l .` empty; all tests pass. Confirm `quality4K` is gone: `grep -rn "quality4K\|\"2160p\"" internal/` should show no non-test hits (the tier strings now appear only as test `Id` values).

- [ ] **Step 4: Commit**

```bash
cd /opt/silo-plugin-requests-seerr
git add -A
git commit -m "refactor(router): read is4k from RequestedQuality; drop quality4K const"
```

---

## Task 5: Cross-repo verification

- [ ] **Step 1: SDK + both plugins green (bare host)**

Run:
```bash
for r in /opt/silo-plugin-sdk /opt/silo-plugin-requests-seerr /opt/silo-plugin-requests-arr; do
  echo "=== $r ==="
  (cd "$r" && /opt/deployarr/.local/go-sdk/go/bin/go build ./... && /opt/deployarr/.local/go-sdk/go/bin/go vet ./... && $(/opt/deployarr/.local/go-sdk/go/bin/go env GOROOT)/bin/gofmt -l . && /opt/deployarr/.local/go-sdk/go/bin/go test ./... -count=1 2>&1 | tail -6)
done
```
Expected: each builds, vets, gofmt-clean, tests pass.

- [ ] **Step 2: Host whole-tree build + touched tests (libvips container)**

The host requires libvips for the full tree / api packages. Run:
```bash
docker run --rm -v /opt/silo:/opt/silo -v /opt/silo-plugin-sdk:/opt/silo-plugin-sdk -w /opt/silo golang:1.26 \
  bash -c 'export PATH=/usr/local/go/bin:$PATH; apt-get update -qq && apt-get install -y -qq libvips-dev pkg-config >/dev/null 2>&1 && go build -buildvcs=false ./... 2>&1 | tail -10 && echo "be=$?" && go test -buildvcs=false ./internal/requests/... -count=1 2>&1 | tail -10'
```
Expected: whole `go build ./...` succeeds (`be=0`); `internal/requests` tests pass.

- [ ] **Step 3: Confirm the tier vocabulary left the plugins**

Run:
```bash
grep -rn "2160p\|1080p\|Quality1080p\|Quality2160p\|quality4K" /opt/silo-plugin-requests-arr/internal /opt/silo-plugin-requests-seerr/internal | grep -v "_test.go" && echo "STRINGS REMAIN (unexpected)" || echo "no tier strings in plugin non-test code"
```
Expected: "no tier strings in plugin non-test code" (the only remaining `"1080p"`/`"2160p"` are `Id` values inside `_test.go` files, which is fine — tests legitimately name the tiers).

- [ ] **Step 4: Confirm clean trees + zero unexpected host changes**

Run:
```bash
for r in /opt/silo /opt/silo-plugin-sdk /opt/silo-plugin-requests-seerr /opt/silo-plugin-requests-arr; do git -C "$r" status --porcelain && echo "($r clean)"; done
```
Expected: all four working trees clean (everything committed). The host change is intentional (Task 2) and committed; no stray edits.

---

## Notes for the implementer

- **Build order is load-bearing:** Task 1 (SDK regen) must land before Tasks 2–4 compile — they all reference `pluginv1.RequestedQuality` / `GetIs4K()` / `GetQualities() []*RequestedQuality`, which don't exist until the regen.
- **The identity path is untouched.** Only `FulfillRequest.qualities` (the input) changes shape. `FulfillmentTarget.quality` / `TargetRef.quality` / `TargetStatus.quality` stay `string`, the `media_request_targets.quality` DB column + its `CHECK IN ('1080p','2160p')` are unchanged, and the host still validates the echoed identity string against that CHECK. Do not touch the DB or the reconcile/CheckStatus quality handling.
- **The host keeps the tier strings; the plugins shed them.** After this, `Quality1080p`/`Quality2160p` live only in the host (`internal/requests/types.go`) + the DB CHECK; neither plugin contains a quality-tier literal in non-test code. That asymmetry is the intended outcome (the host owns governance).
- **proto regen:** `make proto` aborts on a `protoc` precheck not satisfied here; use `PATH="$(pwd)/bin:$PATH" buf generate` (the local `bin/buf` exists from prior regens this session). Commit the regenerated `.pb.go` alongside the `.proto`.
- **Go identifier for `is4k`:** the generated field/getter is `Is4K` / `GetIs4K()` (GoCamelCase uppercases the letter after a digit), while `id` → `Id` / `GetId()`. Task 1 Step 3 verifies this; every later task uses `Is4K`/`GetIs4K()`.
- If a `server_test.go` builds `Qualities` somewhere this plan didn't enumerate, apply the same `[]string` → `[]*pluginv1.RequestedQuality{{Id:…, Is4K:…}}` transform; the compiler will flag every site.

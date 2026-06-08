# Typed 4K Quality-Tier Signal Design (code-review #9)

**Date:** 2026-06-08
**Status:** Approved (brainstorming)
**Related:** `2026-06-07-requests-pluginization-design.md` (the `request_router.v1` contract), `2026-06-08-plugin-platform-sdk-helpers-design.md` (#10, just landed — both plugins now share the SDK httpclient/ServeManifest). This is the sibling altitude follow-up to #10.

## Goal

Replace the stringly-typed 4K-tier detection in request_router plugins with an explicit `is4k` signal carried in the contract, so plugins no longer hardcode the magic string `"2160p"` and the host — which owns quality governance — is the single source of the "which tier is 4K" fact.

## Why (the problem)

The host computes the allowed quality tiers and sends `FulfillRequest.qualities` as a `repeated string` (e.g. `["1080p","2160p"]`). Each plugin then **re-derives** 4K from the raw string: arr (`internal/arr/routing.go`: `case string(Quality2160p)` → route to the `IsDefault4K` instance) and seerr (`internal/router/server.go`: `quality4K = "2160p"`; `is4k := q == quality4K`). The "4K means this exact string" knowledge is copy-pasted into every plugin.

If the host ever renames or extends the tier vocabulary (`"2160p"` → `"4K"`, or adds a second 4K-class tier like `"2160p-remux"`), every plugin **silently mis-routes** a 4K request to the HD downloader — no compile error, no runtime error, just wrong fulfillment. The host already knows which tier is 4K (it gates on `q == Quality2160p`); it should *tell* the plugin rather than each plugin guessing.

## Key fact: input list vs. identity string are separate

`FulfillRequest.qualities` (the **input** — "what tiers to fulfill") and the per-target `quality` (`FulfillmentTarget.quality` / `TargetRef.quality` / `TargetStatus.quality`, field 1 on each) are **distinct proto fields that never alias**. The per-target `quality` is the load-bearing **identity**: it is the `media_request_targets.quality` DB column (with `CHECK (quality IN ('1080p','2160p'))` + `UNIQUE(request_id, quality)`), the reconcile join key (`target.Quality == st.Quality && target.IntegrationID == st.ConnectionID`), and the value round-tripped into `TargetRef.quality` for CheckStatus.

Therefore the *input* list's shape can change while the *identity* string stays `string` end-to-end. The plugin populates `FulfillmentTarget.quality` from the structured input's `id` field; the whole identity path is untouched. The DB CHECK, reconcile, and CheckStatus all continue to operate on the `string` quality identity.

## Scope

**In scope (one coordinated change across three repos):**
- SDK proto: a new `RequestedQuality` message + change `FulfillRequest.qualities` from `repeated string` to `repeated RequestedQuality` (field 3), regenerate.
- Host (`/opt/silo`): the single FulfillRequest construction site stamps `is4k` per quality.
- arr plugin: route by `is4k` instead of the quality string.
- seerr plugin: read `is4k` from the field instead of matching `"2160p"`.

**Out of scope:**
- The per-target `quality` identity string and everything downstream of it (DB schema/CHECK, reconcile, CheckStatus `TargetRef`/`TargetStatus`) — unchanged.
- `allowedQualities` / entitlement logic — unchanged (it still produces the `[]Quality` of tiers; only the marshalling into the proto changes).
- A general tier enum (HD/UHD/8K/…) — only two tiers exist today; `is4k bool` is the proportionate signal. (Considered and rejected as speculative during brainstorming.)
- Any other capability or plugin.

**Breaking change, intentionally:** changing field 3 from `repeated string` to `repeated RequestedQuality` is wire-incompatible. This is acceptable because nothing is published — the SDK, host, and both plugins are all on local branches, and all four readers are updated in this same change. A clean break beats carrying a deprecated parallel field forever.

**Local-only:** commits only; no push/tag/PR. Each repo's `go.mod replace => /opt/silo-plugin-sdk` stays. SDK on `feat/request-router-capability`; host on `feat/requests-pluginization`; plugins on `master`.

## Component 1 — SDK proto + regen

`/opt/silo-plugin-sdk/proto/silo/plugin/v1/request_router.proto`:

```proto
// RequestedQuality is one quality tier the host wants fulfilled. The host owns
// quality governance, so it stamps is4k rather than each plugin re-deriving it
// from id. id is the tier identity echoed back as FulfillmentTarget.quality.
message RequestedQuality {
  string id = 1;
  bool is4k = 2;
}

message FulfillRequest {
  string capability_id = 1;
  RequestDescriptor request = 2;
  repeated RequestedQuality qualities = 3; // was: repeated string qualities = 3
  repeated RouterConnection connections = 4;
}
```

`FulfillmentTarget.quality`, `TargetRef.quality`, `TargetStatus.quality`, and the rest of the contract are unchanged. Regenerate with the SDK's buf flow (`make proto`, or the `PATH="$(pwd)/bin:$PATH" buf generate` fallback if the protoc precheck aborts — both produce identical output). The generated `FulfillRequest.GetQualities()` becomes `[]*RequestedQuality`; getters `RequestedQuality.GetId() string` / `GetIs4k() bool`.

## Component 2 — Host (`/opt/silo`)

Single construction site: `internal/requests/provider.go` (~lines 131–141), where the host builds `*pluginv1.FulfillRequest`. The `[]Quality` → `[]string` loop becomes a `[]Quality` → `[]*pluginv1.RequestedQuality` loop, stamping `is4k` from the host's tier constant (the one place this fact now lives):

```go
qs := make([]*pluginv1.RequestedQuality, 0, len(qualities))
for _, q := range qualities {
    qs = append(qs, &pluginv1.RequestedQuality{Id: string(q), Is4k: q == Quality2160p})
}
// ... &pluginv1.FulfillRequest{ ..., Qualities: qs, ... }
```

`allowedQualities` (service.go) is unchanged — it still returns `[]Quality{Quality1080p[, Quality2160p]}` by entitlement. `Quality2160p` (`internal/requests/types.go`) stays a host-internal constant; it is now referenced only here (for the `is4k` stamp) and wherever the host validates the echoed identity against the DB CHECK (unchanged). Update the one mechanical assertion in `internal/requests/provider_test.go` (currently `len(...GetQualities()) != 1`) to assert the structured value (e.g. the single quality has `Id == "1080p"` and `Is4k == false`, and a 4K case has `Is4k == true`).

Builds in the `golang:1.26 + libvips-dev` container (the host requires libvips); `internal/requests` itself builds bare-host, but the whole-tree build/`go vet` and the api packages need the container per the project's standing recipe.

## Component 3 — arr plugin (`/opt/silo-plugin-requests-arr`)

Keep `internal/arr` free of `pluginv1` (its current design — `internal/router/server.go` does the `pluginv1` ↔ arr mapping). Introduce a small plugin-local type and route by `is4k`:

- `internal/arr/routing.go`: add `type RequestedQuality struct { ID string; Is4K bool }`. Change `RouteTargets(req Request, qualities []string, instances []Instance)` to `RouteTargets(req Request, qualities []RequestedQuality, instances []Instance)`. Replace the `switch q { case string(Quality1080p): … IsDefault; case string(Quality2160p): … IsDefault4K }` with: `if q.Is4K { match an IsDefault4K instance } else { match an IsDefault instance }`. Set `PlannedTarget.Quality = q.ID` (the identity carried forward verbatim, as today).
- `internal/router/server.go`: map the proto input — `for _, q := range req.GetQualities()` build `[]arr.RequestedQuality{ID: q.GetId(), Is4K: q.GetIs4k()}` and pass to `RouteTargets`. The `FulfillmentTarget.Quality = pt.Quality` echo is unchanged.
- `internal/arr/types.go`: delete `Quality1080p`/`Quality2160p` consts and the `Quality` type if now unused (grep first; they were used only by the routing switch). arr no longer encodes any tier string.

## Component 4 — seerr plugin (`/opt/silo-plugin-requests-seerr`)

`internal/router/server.go`'s Fulfill loop reads the field directly (seerr has no separate routing layer and already imports `pluginv1`):

```go
for _, q := range req.GetQualities() {
    is4k := q.GetIs4k()
    if is4k && !conn.Supports4K {
        continue
    }
    targets = append(targets, s.fulfillOne(ctx, client, conn.ID, q.GetId(), mediaType, tmdbID, is4k))
}
```

Delete the `quality4K = "2160p"` const. `fulfillOne` already takes the quality string (now `q.GetId()`) and `is4k` (now `q.GetIs4k()`) — its body is unchanged. seerr no longer encodes the tier vocabulary.

## Build order

The proto type must exist before any consumer compiles against it:

1. **SDK:** edit `request_router.proto`, regenerate, `go build ./... && go test ./...` green. Commit.
2. **Host:** update `provider.go` + `provider_test.go`; build/test the touched packages (container for the whole tree / api; `internal/requests` bare-host). Commit.
3. **arr:** add `arr.RequestedQuality`, update `RouteTargets` + `server.go`, delete the consts, fix tests; `go build ./... && go vet ./... && go test ./...` green (bare host — arr is pure Go). Commit.
4. **seerr:** update the Fulfill loop, delete `quality4K`, fix tests; build/test green (bare host). Commit.

After all four: SDK + both plugins build/test green against the regenerated proto; the host builds (container) + `internal/requests` tests pass; `git -C` clean in all repos.

## Testing

- **SDK:** the regenerated code compiles; existing SDK tests stay green (the proto change is mechanical). No new SDK test needed beyond compilation (the `RequestedQuality` type is data-only).
- **Host:** `provider_test.go` asserts the constructed `FulfillRequest.Qualities` carries `{Id, Is4k}` correctly — `"1080p"` → `Is4k:false`, and (in a 4K-entitled case) `"2160p"` → `Is4k:true`. The existing requests tests stay green.
- **arr:** a `RouteTargets` test (in `internal/arr`) that passes `[]RequestedQuality{{ID:"1080p",Is4K:false},{ID:"2160p",Is4K:true}}` and asserts the 1080p target routes to the `IsDefault` instance and the 2160p target to the `IsDefault4K` instance — with **no quality string literals in the routing logic**. The `server.go`/Fulfill tests (which build the proto input) update to the structured `RequestedQuality` shape; assertions on `FulfillmentTarget.Quality` (the echoed id) are unchanged.
- **seerr:** the Fulfill tests build `FulfillRequest.Qualities` as `[]*pluginv1.RequestedQuality{{Id:"1080p",Is4k:false},{Id:"2160p",Is4k:true}}` and assert the same outcomes as today (HD-only, HD+4K with `supports_4k`, 4K-skipped when unsupported) — now driven by the `is4k` field, not a string. The `Quality` on emitted targets is still `q.GetId()`.

## Risks & notes

- **Breaking proto change** (field 3 type): mitigated by updating all four readers in lockstep and the fact that nothing is published. The build order ensures the SDK type exists before consumers compile.
- **Identity path untouched:** the per-target `quality` string (DB CHECK `IN ('1080p','2160p')`, `UNIQUE`, reconcile join, CheckStatus) does not change — this design only restructures the Fulfill *input*. A future tier rename would still require updating the DB CHECK + the host's `Quality*` constants, but **plugins would no longer need any change** — which is the point.
- **Future tiers:** `is4k bool` covers HD-vs-4K, the only distinction today. If a third tier class ever appears, `is4k` generalizes to a tier enum on the same `RequestedQuality` message (additive `tier` field) without touching the identity path — but that is explicitly not built now (YAGNI).
- **Coupling reduced:** after this, neither plugin contains any quality-tier string; the tier vocabulary lives solely in the host (`internal/requests/types.go` + the DB CHECK). This is the altitude fix the finding asked for.

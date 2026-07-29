# Transcode Resolution Clamp Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent encoded-video transports from targeting a resolution above the final effective source file.

**Architecture:** Add one pure resolution-normalization helper in the playback API handler and invoke it exactly once after alternate-file selection. Mutate the request's target resolution at that boundary so remote transport, local transport, persistence, activity reporting, and reconstruction consume the same normalized recipe.

**Tech Stack:** Go, `net/http` handler tests, Silo playback session and transcode-node test doubles.

## Global Constraints

- Do not apply the new clamp to video-copy requests; preserve their existing
  downstream recipe normalization.
- Preserve empty or unrecognized requested/source resolutions unchanged.
- Clamp only recognized encoded-video targets above the recognized effective source.
- Do not alter bitrate, codecs, API shapes, status codes, settings, migrations, clients, or production configuration.
- Prove the regression RED before writing production code.

---

### Task 1: Normalize the Effective Transcode Recipe

**Files:**
- Modify: `internal/api/handlers/playback.go`
- Test: `internal/api/handlers/playback_test.go`

**Interfaces:**
- Consumes: `transcodeStartRequest.TargetResolution`, `transcodeStartRequest.TargetCodecVideo`, and `models.MediaFile.Resolution` after alternate-file selection.
- Produces: `clampEncodedTargetResolution(requestedResolution, sourceResolution string) string`.
- Produces: one normalized `req.TargetResolution` consumed unchanged by remote/local transport construction and `buildTranscodeSessionReplacement`.

- [ ] **Step 1: Strengthen the existing remote fallback test**

Change `TestHandleStartTranscode_PreservesRecomputedBaseMethodAfterFallback` to
request 2160p at 10000 kbps. Assert that the captured
`transcodenode.TranscodeStartRequest.TargetResolution` is `1080p`, while the
effective alternate remains file 99 and the resulting playback session stores
`TargetResolution == "1080p"`.

```go
strings.NewReader(`{"session_id":"` + startResp.SessionID +
    `","seek_seconds":0,"target_resolution":"2160p",` +
    `"target_codec_video":"h264","target_codec_audio":"aac",` +
    `"target_bitrate_kbps":10000,"segment_duration":2,` +
    `"subtitle_track_index":-1,"subtitle_burn_in":false}`)

if remoteStartReq.TargetResolution != "1080p" {
    t.Fatalf("remote target resolution = %q, want 1080p", remoteStartReq.TargetResolution)
}
if session.TargetResolution != "1080p" {
    t.Fatalf("session target resolution = %q, want 1080p", session.TargetResolution)
}
```

- [ ] **Step 2: Run the regression and verify RED**

Run:

```bash
go test ./internal/api/handlers -run TestHandleStartTranscode_PreservesRecomputedBaseMethodAfterFallback -count=1
```

Expected: FAIL because the captured remote request still contains `2160p`.

- [ ] **Step 3: Add pure normalization table tests**

Add `TestClampEncodedTargetResolution` beside the handler tests. Cover:

```go
tests := []struct {
    name, requested, source, want string
}{
    {"clamps 2160p to 1080p", "2160p", "1080p", "1080p"},
    {"keeps lower target", "720p", "1080p", "720p"},
    {"keeps equal target", "1080p", "1080p", "1080p"},
    {"keeps empty target", "", "1080p", ""},
    {"keeps unknown target", "source", "1080p", "source"},
    {"keeps target for unknown source", "2160p", "native", "2160p"},
    {"supports low tiers", "480p", "420p", "420p"},
}
```

- [ ] **Step 4: Add the minimal helper and post-selection call**

Add a package-level recognized-height map/helper near `resolutionRank`:

```go
func clampEncodedTargetResolution(requestedResolution, sourceResolution string) string {
    requestedHeight, requestedKnown := transcodeResolutionHeight(requestedResolution)
    sourceHeight, sourceKnown := transcodeResolutionHeight(sourceResolution)
    if !requestedKnown || !sourceKnown || requestedHeight <= sourceHeight {
        return requestedResolution
    }
    return sourceResolution
}
```

After the 4K alternate block and before transport planning:

```go
if !videoCopy {
    req.TargetResolution = clampEncodedTargetResolution(
        req.TargetResolution,
        file.Resolution,
    )
}
```

Use an exhaustive switch for the six supported transcode tiers. Do not modify
the FFmpeg filter builder or web client.

- [ ] **Step 5: Verify focused GREEN**

Run:

```bash
go test ./internal/api/handlers \
  -run 'TestHandleStartTranscode_PreservesRecomputedBaseMethodAfterFallback|TestClampEncodedTargetResolution' \
  -count=1
```

Expected: PASS.

- [ ] **Step 6: Add copy and local-boundary regression coverage**

Add focused tests proving:

- a copy-video request bypasses the new clamp and retains its existing
  downstream empty-resolution recipe;
- a local encoded-video request receives the clamped target in the persisted
  session and generated FFmpeg recipe boundary;
- a requested 720p encode for a 1080p effective file remains 720p.

Use the existing local fake-FFmpeg and session-manager patterns in
`playback_test.go`; do not introduce production test hooks.

- [ ] **Step 7: Run focused and package verification**

Run:

```bash
gofmt -w internal/api/handlers/playback.go internal/api/handlers/playback_test.go
go test ./internal/api/handlers -count=1
go test ./internal/playback -count=1
go test ./internal/api/handlers ./internal/playback -race -count=1
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 8: Commit implementation**

```bash
git add internal/api/handlers/playback.go internal/api/handlers/playback_test.go
git commit -m "fix(playback): prevent transcode resolution upscaling"
```

### Task 2: Independent Review and Final Verification

**Files:**
- Review: `docs/superpowers/specs/2026-07-28-transcode-resolution-clamp-design.md`
- Review: `internal/api/handlers/playback.go`
- Review: `internal/api/handlers/playback_test.go`

**Interfaces:**
- Consumes: committed spec and implementation diff from `upstream/main`.
- Produces: reviewer verdict and a clean, verified branch suitable for a separate pull request.

- [ ] **Step 1: Request focused correctness/security review**

Review the change against these invariants:

- no encoded output exceeds the effective source tier;
- copy and unknown-resolution compatibility is preserved;
- local, offloaded, persisted, and reconstruction paths share one value;
- no target-bitrate or API behavior was broadened;
- tests fail without the production correction.

- [ ] **Step 2: Address Critical or Important findings test-first**

For each accepted behavioral finding, add or strengthen a failing test, observe
RED, make the smallest production correction, and rerun focused GREEN. Do not
bundle unrelated cleanup.

- [ ] **Step 3: Run final verification**

Run:

```bash
go test ./internal/api/handlers ./internal/playback -count=1
go test ./internal/api/handlers ./internal/playback -race -count=1
go test ./... -count=1
make verify-local-paths
git diff --check
git status --short --branch
```

Expected: tests and repository policy checks exit 0; status contains no
uncommitted implementation changes.

- [ ] **Step 4: Prepare handoff**

Summarize the production evidence, exact normalization boundary, RED/GREEN
proof, focused/full verification, reviewer verdict, and deployment caveat.
Do not deploy or mutate production as part of this plan.

# Instant External SRT Switching Server Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a capability-negotiated playback V3 sidecar set containing only readable external SRT/VTT artifacts.

**Architecture:** Extend the additive V3 subtitle decision contract, build a validated sidecar set at the playback-handler boundary, and persist it inside each plan so start, replan, and idempotent replay responses are identical. Serve raw SRT/VTT bytes from the existing authenticated subtitle endpoint.

**Tech Stack:** Go, `net/http`, playback protocol V3, table-driven Go tests.

## Global Constraints

- Keep `/api/v1` additive-only; do not rename, remove, or repurpose existing fields.
- Return `sidecars` only when both `client_features` and `client_playback_context.features` contain `external_text_sidecar_set_v1`.
- Include only readable, non-empty external SRT/SubRip and WebVTT tracks.
- Never fail primary playback because an optional sidecar is unavailable.
- Do not include filesystem paths or subtitle contents in logs.
- Preserve the singular selected `artifact` contract unchanged.
- Commands assume the repository root is the cwd.

---

### Task 1: Add the additive protocol contract

**Files:**
- Modify: `internal/playback/protocol_v3.go`
- Modify: `internal/playback/protocol_v3_test.go`

**Interfaces:**
- Produces: `FeatureExternalTextSidecarSetV3 = "external_text_sidecar_set_v1"`.
- Produces: `SubtitleSidecarV3` with `TrackID string`, `Index int`, `URL string`, `MIMEType string`, `Format string`, and `TimingOriginSeconds float64`.
- Produces: `SubtitleDecisionV3.Sidecars []SubtitleSidecarV3` serialized as `sidecars,omitempty`.

- [ ] **Step 1: Write failing protocol tests**

Add the feature to the expected `ServerFeaturesV3` set and add a JSON round-trip test:

```go
func TestSubtitleDecisionV3SidecarsRoundTrip(t *testing.T) {
	want := SubtitleDecisionV3{Sidecars: []SubtitleSidecarV3{{
		TrackID: "file:42:subtitle:1", Index: 1,
		URL: "/stream/session/subtitles/1.srt?file_id=42",
		MIMEType: "application/x-subrip", Format: "srt",
		TimingOriginSeconds: 12.5,
	}}}
	body, err := json.Marshal(want)
	if err != nil { t.Fatal(err) }
	var got SubtitleDecisionV3
	if err := json.Unmarshal(body, &got); err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(got, want) { t.Fatalf("got %#v, want %#v", got, want) }
}
```

- [ ] **Step 2: Run the focused tests and confirm failure**

Run: `go test ./internal/playback -run 'TestServerFeaturesV3|TestSubtitleDecisionV3Sidecars'`

Expected: compile failure because the feature, type, and field do not exist.

- [ ] **Step 3: Add the feature, type, and optional field**

Add the constant beside the other V3 feature constants, return it from `ServerFeaturesV3`, define `SubtitleSidecarV3` beside `SubtitleArtifactV3`, and add:

```go
Sidecars []SubtitleSidecarV3 `json:"sidecars,omitempty"`
```

to `SubtitleDecisionV3`.

- [ ] **Step 4: Run and pass the focused tests**

Run: `go test ./internal/playback -run 'TestServerFeaturesV3|TestSubtitleDecisionV3Sidecars'`

Expected: PASS.

- [ ] **Step 5: Commit the protocol contract**

```bash
git add internal/playback/protocol_v3.go internal/playback/protocol_v3_test.go
git commit -m "feat(playback): add external text sidecar contract"
```

### Task 2: Serve raw external SRT and VTT artifacts

**Files:**
- Modify: `internal/api/handlers/stream.go`
- Modify: `internal/api/handlers/stream_test.go`

**Interfaces:**
- Consumes: authenticated `/stream/{session}/subtitles/{track}.{format}` routing.
- Produces: raw `.srt` response as `application/x-subrip; charset=utf-8`.
- Produces: raw `.vtt` response as `text/vtt; charset=utf-8`.

- [ ] **Step 1: Add failing handler tests for raw formats**

Create temporary external files with `t.TempDir()` and assert exact body and content type for requests ending in `.srt` and `.vtt`. The SRT fixture must contain:

```text
1
00:00:01,000 --> 00:00:02,000
Hello
```

The VTT fixture must contain:

```text
WEBVTT

00:01.000 --> 00:02.000
Hello
```

- [ ] **Step 2: Run the focused tests and confirm failure**

Run: `go test ./internal/api/handlers -run 'TestHandleSubtitle.*Raw'`

Expected: the `.srt` request currently receives converted WebVTT rather than raw SubRip.

- [ ] **Step 3: Add the minimal raw-serving branches**

In the external-track branch, load with `playback.LoadExternalSubtitleRaw` and call `playback.ServeSubtitle` when the requested format matches the declared eligible format:

```go
format := strings.ToLower(sub.Format)
if (requestedFormat == "srt" && (format == "srt" || format == "subrip")) ||
	(requestedFormat == "vtt" && (format == "vtt" || format == "webvtt")) {
	data, err := playback.LoadExternalSubtitleRaw(sub.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error",
			"Failed to load external subtitle")
		return
	}
	playback.ServeSubtitle(w, data, requestedFormat)
	return
}
```

Keep the existing ASS and conversion branches after this branch.

- [ ] **Step 4: Run and pass focused stream tests**

Run: `go test ./internal/api/handlers -run 'TestHandleSubtitle|TestSubtitleSourceFileID'`

Expected: PASS.

- [ ] **Step 5: Commit raw subtitle serving**

```bash
git add internal/api/handlers/stream.go internal/api/handlers/stream_test.go
git commit -m "feat(stream): serve raw external text subtitles"
```

### Task 3: Build and attach the validated sidecar set

**Files:**
- Modify: `internal/api/handlers/playback_v3.go`
- Modify: `internal/api/handlers/playback_v3_test.go`
- Create: `internal/api/handlers/playback_v3_sidecars_test.go`

**Interfaces:**
- Consumes: `playback.StartRequestV3`, effective `models.MediaFile`, session ID, and plan timeline.
- Produces: `attachExternalTextSidecarsV3(request, sessionID, file, plan)`.
- Produces: deterministic, combined-index-ordered `plan.Subtitle.Sidecars`.

- [ ] **Step 1: Write failing builder tests**

Use temp files for readable SRT, readable VTT, empty SRT, and missing SRT. Build a media file that also has ASS and embedded subtitles. Assert that a request with the feature in both locations returns only the readable SRT/VTT entries, with indexes `0` and `1`, raw extensions/MIME types, stable IDs from `playback.TrackIDV3`, and the plan's `Timeline.StreamOriginSeconds`.

Also assert:

```go
request.ClientFeatures = nil
attachExternalTextSidecarsV3(request, "session", file, plan)
if len(plan.Subtitle.Sidecars) != 0 { t.Fatal("unnegotiated sidecars attached") }
```

Repeat with only the top-level feature and only the context feature to prove both are required.

- [ ] **Step 2: Run the focused tests and confirm failure**

Run: `go test ./internal/api/handlers -run 'TestAttachExternalTextSidecarsV3'`

Expected: compile failure because the helper does not exist.

- [ ] **Step 3: Implement negotiation and per-track validation**

Implement helpers with these signatures:

```go
func supportsExternalTextSidecarSetV3(req playback.StartRequestV3) bool
func attachExternalTextSidecarsV3(req playback.StartRequestV3, sessionID string, file *models.MediaFile, plan *playback.PlanV3)
func externalTextSidecarFormatV3(format string) (extension, mime, normalized string, ok bool)
```

`supportsExternalTextSidecarSetV3` must require `playback.HasFeatureV3` in both feature arrays. `attachExternalTextSidecarsV3` must clear/leave nil when unnegotiated, iterate only `file.ExternalSubtitles`, call `playback.LoadExternalSubtitleRaw`, reject zero-length data, and append:

```go
playback.SubtitleSidecarV3{
	TrackID: playback.TrackIDV3(file.ID, "subtitle", index),
	Index: index,
	URL: fmt.Sprintf("/stream/%s/subtitles/%d%s?file_id=%d", sessionID, index, extension, file.ID),
	MIMEType: mime,
	Format: normalized,
	TimingOriginSeconds: plan.Timeline.StreamOriginSeconds,
}
```

Log only file ID, index, normalized format, and a bounded rejection class.

- [ ] **Step 4: Run and pass builder tests**

Run: `go test ./internal/api/handlers -run 'TestAttachExternalTextSidecarsV3'`

Expected: PASS.

- [ ] **Step 5: Wire start, replan, and replay-safe plan persistence**

Call `attachExternalTextSidecarsV3` immediately after `attachSubtitleArtifactV3` in both start and replan flows, using the normalized start request. Because the resulting plan is saved in `AttemptRecordV3.CurrentPlan`, `decisionResponseFromAttemptV3` must replay the same array without rebuilding it.

- [ ] **Step 6: Add integration assertions**

Extend V3 handler tests to assert:

- a negotiated start response contains `playback_plan.subtitle.sidecars`;
- the persisted attempt contains the same array;
- `decisionResponseFromAttemptV3` returns it unchanged;
- an unnegotiated request omits the JSON field;
- a replan response retains/rebuilds the eligible set for its effective file.

- [ ] **Step 7: Run and pass playback-handler tests**

Run: `go test ./internal/api/handlers -run 'TestHandleStartPlaybackV3|TestHandleReplanPlaybackV3|TestDecisionResponseFromAttemptV3|TestAttachExternalTextSidecarsV3'`

Expected: PASS.

- [ ] **Step 8: Commit the handler integration**

```bash
git add internal/api/handlers/playback_v3.go internal/api/handlers/playback_v3_sidecars_test.go internal/api/handlers/playback_v3_test.go
git commit -m "feat(playback): attach validated external text sidecars"
```

### Task 4: Verify the server repository

**Files:**
- Verify only.

**Interfaces:**
- Produces: a server commit set safe for coordinated Android consumption.

- [ ] **Step 1: Format touched Go files**

Run: `gofmt -w internal/playback/protocol_v3.go internal/playback/protocol_v3_test.go internal/api/handlers/playback_v3.go internal/api/handlers/playback_v3_test.go internal/api/handlers/playback_v3_sidecars_test.go internal/api/handlers/stream.go internal/api/handlers/stream_test.go`

- [ ] **Step 2: Run focused packages**

Run: `go test ./internal/playback ./internal/api/handlers`

Expected: PASS.

- [ ] **Step 3: Run repository gates**

Run: `make test-go && make verify-local-paths`

Expected: PASS.

- [ ] **Step 4: Inspect final state**

Run: `git status --short --branch && git log --oneline -5`

Expected: clean working tree on local `main`, ahead of `upstream/main` only by intentional commits.

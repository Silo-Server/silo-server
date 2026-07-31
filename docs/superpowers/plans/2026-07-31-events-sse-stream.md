# Events SSE Stream Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a read-only Server-Sent Events transport over the existing events hub at `GET /api/v1/events/sse`, so external integrations can observe realtime events without implementing the WebSocket subscribe handshake.

**Architecture:** A new handler method on the existing `EventsHandler`, reusing `events.Hub.Subscribe()`, `allowedChannelsForRole` and `allowsEventForClaims`. Channel selection is a query parameter rather than a handshake. The WebSocket handler, the hub, and event publishing are not modified.

**Tech Stack:** Go, chi router, `net/http` with `http.Flusher`, existing `internal/events` package, `httptest` for tests.

## Global Constraints

- Base branch is `feat/events-sse-stream`, already created from `upstream/main` (d70b291b). Do not create branches or worktrees.
- **Do not modify** `internal/events/hub.go`, `internal/events/types.go`, `HandleWebSocket`, or anything under `internal/api/handlers/events_publish.go`. This adds a transport; it changes no existing behaviour.
- Existing tests in `events_ws_test.go` and `events_ws_user_settings_test.go` must continue to pass untouched.
- New DB changes: none. This feature adds no migration.
- Go stays `gofmt`/`goimports` clean.
- Verify with `make lint` and `make test-go`. CI runs `golangci-lint --new-from-merge-base`, so only lines this branch touches must be lint-clean; pre-existing findings elsewhere are not yours.
- Conventional Commit subjects. Commit after every task.
- Profile-scoped notification channels are OUT OF SCOPE — they depend on the ws-ticket binding which SSE does not use.

---

### Task 1: SSE handler with hello frame and channel filtering

**Files:**

- Create: `internal/api/handlers/events_sse.go`
- Create: `internal/api/handlers/events_sse_test.go`
- Modify: `internal/api/router.go` (beside the existing `r.Get("/events/ws", …)` registration, around line 1998)

**Interfaces:**

- Consumes: `EventsHandler` (existing struct), `h.hub.Subscribe()`, `allowedChannelsForRole(role string) []evt.EventChannel`, `allowsEventForClaims(claims *auth.Claims, boundProfileID string, env evt.Envelope) bool`, `apimw.GetClaims(ctx)`.
- Produces: `func (h *EventsHandler) HandleSSE(w http.ResponseWriter, r *http.Request)`, registered at `GET /api/v1/events/sse`.

Read `internal/api/handlers/events_ws.go` first, particularly `HandleWebSocket` (nil/claims guards, hub subscribe, hello frame, the event loop with its channel-subscription and `allowsEventForClaims` checks). The SSE handler mirrors that structure with an SSE writer in place of the WebSocket connection, and no inbound message handling.

- [ ] **Step 1: Write the failing test**

Create `internal/api/handlers/events_sse_test.go`. Follow the conventions already in `events_ws_test.go` (same package, `httptest`, fake collaborators). Model the claims/context setup on how the existing WebSocket tests build an authenticated request; read that file and reuse its helpers rather than inventing new ones.

```go
package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

func TestHandleSSESetsStreamingHeaders(t *testing.T) {
	hub := evt.NewHub()
	h := &EventsHandler{hub: hub}

	req := newAuthedSSERequest(t, "/api/v1/events/sse?channels=sessions")
	rec := httptest.NewRecorder()

	runSSEHandlerBriefly(t, h, rec, req)

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	// openresty fronts several deployments and buffers the stream without this.
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
}

func TestHandleSSEWritesHelloFrameFirst(t *testing.T) {
	hub := evt.NewHub()
	h := &EventsHandler{hub: hub}

	req := newAuthedSSERequest(t, "/api/v1/events/sse?channels=sessions")
	rec := httptest.NewRecorder()

	runSSEHandlerBriefly(t, h, rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: hello") {
		t.Fatalf("missing hello event, body = %q", body)
	}
	if !strings.Contains(body, `"required_action":"none"`) {
		t.Fatalf("hello should not demand a subscribe action, body = %q", body)
	}
}

func TestHandleSSEWritesSubscribedChannelEvent(t *testing.T) {
	hub := evt.NewHub()
	h := &EventsHandler{hub: hub}

	req := newAuthedSSERequest(t, "/api/v1/events/sse?channels=sessions")
	rec := httptest.NewRecorder()

	body := runSSEHandlerWithPublish(t, h, hub, rec, req, evt.Envelope{
		Channel: evt.ChannelSessions,
		Event:   "sessions.replaced",
	})

	if !strings.Contains(body, "event: sessions") {
		t.Fatalf("subscribed channel event was not written, body = %q", body)
	}
}

func TestHandleSSESuppressesUnsubscribedChannel(t *testing.T) {
	hub := evt.NewHub()
	h := &EventsHandler{hub: hub}

	req := newAuthedSSERequest(t, "/api/v1/events/sse?channels=sessions")
	rec := httptest.NewRecorder()

	body := runSSEHandlerWithPublish(t, h, hub, rec, req, evt.Envelope{
		Channel: evt.ChannelJobs,
		Event:   "jobs.updated",
	})

	if strings.Contains(body, "event: jobs") {
		t.Fatalf("unsubscribed channel leaked into the stream, body = %q", body)
	}
}

func TestHandleSSERejectsUnauthenticated(t *testing.T) {
	hub := evt.NewHub()
	h := &EventsHandler{hub: hub}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/sse", nil)
	rec := httptest.NewRecorder()

	h.HandleSSE(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
```

You must also write the two helpers used above, in the same test file:

- `newAuthedSSERequest(t *testing.T, target string) *http.Request` — builds a GET request whose context carries admin claims. Copy the claims-construction approach from `events_ws_test.go`; do not invent a new auth path.
- `runSSEHandlerBriefly(t, h, rec, req)` and `runSSEHandlerWithPublish(t, h, hub, rec, req, env) string` — run `h.HandleSSE` with a context that is cancelled shortly after (and, for the publish variant, publish the envelope to the hub before cancelling), then return the recorded body. Use `context.WithCancel` plus a short `time.AfterFunc`, or a `context.WithTimeout` of a few dozen milliseconds. `httptest.ResponseRecorder` implements `http.Flusher`, so it works as the SSE sink.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/api/handlers/ -run TestHandleSSE -v`
Expected: FAIL — `h.HandleSSE` undefined.

- [ ] **Step 3: Write the handler**

Create `internal/api/handlers/events_sse.go`:

```go
package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	evt "github.com/Silo-Server/silo-server/internal/events"
)

// ssePingInterval keeps idle connections alive through proxies that would
// otherwise time them out.
const ssePingInterval = 25 * time.Second

// HandleSSE streams hub events as Server-Sent Events.
//
// This is a read-only sibling of HandleWebSocket for consumers that only need
// to observe. Channel selection is a query parameter rather than a handshake:
// SSE is one-way, so there is nothing to negotiate, and no subscribe deadline
// is needed to tell a live client from a stalled one.
//
// Profile-scoped channels are not offered here — they depend on the ws-ticket
// binding, which SSE does not use.
func (h *EventsHandler) HandleSSE(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.hub == nil {
		http.Error(w, "events unavailable", http.StatusServiceUnavailable)
		return
	}

	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	allowed := allowedChannelsForRole(claims.Role)
	subscribed := resolveSSEChannels(r.URL.Query().Get("channels"), allowed)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// openresty fronts several deployments and buffers the stream without this.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	eventsCh, unsubscribe := h.hub.Subscribe()
	defer unsubscribe()

	writeSSEFrame(w, flusher, "hello", evt.EventsHelloMessage{
		Type:              "hello",
		SchemaVersion:     1,
		ConnectionID:      ulid.Make().String(),
		AvailableChannels: allowed,
		RequiredAction:    "none",
	})

	ping := time.NewTicker(ssePingInterval)
	defer ping.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			writeSSEFrame(w, flusher, "ping", map[string]string{
				"type": "ping",
			})
		case env, ok := <-eventsCh:
			if !ok {
				return
			}
			if _, want := subscribed[env.Channel]; !want {
				continue
			}
			if !allowsEventForClaims(claims, "", env) {
				continue
			}
			writeSSEFrame(w, flusher, string(env.Channel), env)
		}
	}
}

// resolveSSEChannels intersects the caller's requested channels with what its
// role allows. An empty request means "everything allowed". A requested channel
// the role forbids is dropped silently rather than failing the whole stream, so
// one forbidden channel does not deny a caller the rest.
func resolveSSEChannels(requested string, allowed []evt.EventChannel) map[evt.EventChannel]struct{} {
	allowedSet := make(map[evt.EventChannel]struct{}, len(allowed))
	for _, ch := range allowed {
		allowedSet[ch] = struct{}{}
	}

	requested = strings.TrimSpace(requested)
	if requested == "" {
		return allowedSet
	}

	selected := make(map[evt.EventChannel]struct{})
	for _, raw := range strings.Split(requested, ",") {
		ch := evt.EventChannel(strings.TrimSpace(raw))
		if ch == "" {
			continue
		}
		if _, ok := allowedSet[ch]; ok {
			selected[ch] = struct{}{}
		}
	}
	return selected
}

// writeSSEFrame writes one named SSE event and flushes it. data is emitted as a
// single compact JSON line, as the SSE framing requires.
func writeSSEFrame(w http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if _, err := w.Write([]byte("event: " + event + "\ndata: " + string(data) + "\n\n")); err != nil {
		return
	}
	flusher.Flush()
}
```

If `EventsHelloMessage`'s fields differ from the above, match the real struct in `internal/events/types.go` — do not change that file.

- [ ] **Step 4: Register the route**

In `internal/api/router.go`, immediately after the existing `r.Get("/events/ws", eventsHandler.HandleWebSocket)` (around line 1998), add:

```go
					r.Get("/events/sse", eventsHandler.HandleSSE)
```

Match the surrounding indentation exactly and keep it inside the same authenticated route group, so it inherits the same auth middleware as the WebSocket route.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/api/handlers/ -run TestHandleSSE -v`
Expected: PASS (all five)

- [ ] **Step 6: Confirm no existing test regressed**

Run: `go test ./internal/api/handlers/`
Expected: PASS. If a pre-existing failure appears, verify it also fails on `upstream/main` before treating it as yours.

- [ ] **Step 7: Format and lint the new code**

Run: `gofmt -l internal/api/handlers/ && goimports -l internal/api/handlers/`
Expected: no output.

- [ ] **Step 8: Commit**

```bash
git add internal/api/handlers/events_sse.go internal/api/handlers/events_sse_test.go internal/api/router.go
git commit -m "feat(events): add server-sent events transport for the events hub"
```

---

### Task 2: Advertise SSE through capability discovery

**Files:**

- Modify: the events capability surface (locate it in Step 1)
- Test: the matching capability test file

**Interfaces:**

- Consumes: `HandleSSE` from Task 1.
- Produces: a capability flag reporting SSE support and its schema version.

`docs/` requires new features to be discoverable through a capability endpoint rather than by version sniffing, so a client can detect SSE support without probing the URL.

- [ ] **Step 1: Locate the right capability surface**

Run:

```bash
grep -rn "capabilit" internal/api/router.go | head -20
grep -rln "Capabilit" internal/api/handlers/ | head -10
```

Candidates seen already: `/device/capability`, `/notifications/capability`, `/collections/capabilities`, `/settings/contract/capabilities`. Choose the one that already describes server-wide or realtime capabilities. If an events-related capability response already exists, extend it. If none fits, extend the broadest server-capability response rather than creating a new endpoint — a new endpoint for one boolean is worse than a new field.

Record which surface you chose and why in your report.

- [ ] **Step 2: Write the failing test**

Add a case to that surface's existing test file asserting the response advertises the SSE stream — for example that the capability payload reports `events_sse` supported with `schema_version: 1`. Mirror the assertions the neighbouring capability tests already use; do not invent a new response shape.

- [ ] **Step 3: Run it and confirm it fails**

Run: `go test ./internal/api/handlers/ -run <YourCapabilityTest> -v`
Expected: FAIL — the field is absent.

- [ ] **Step 4: Add the capability field**

Add the field to the capability response struct and populate it. Follow the naming style already used by its neighbours in the same struct. Per the v1 API rules this is additive only — do not rename, retype, or remove any existing field.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/api/handlers/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add -A internal/api
git commit -m "feat(events): advertise the SSE stream through capability discovery"
```

---

### Task 3: Verification sweep

**Files:** none expected. Fix in the owning task's files if a defect surfaces.

- [ ] **Step 1: Full Go suite**

Run: `make test-go`
Expected: PASS. Compare any failure against `upstream/main` before assuming it is yours — record pre-existing failures rather than fixing unrelated tests.

- [ ] **Step 2: Lint**

Run: `make lint`
Expected: findings only on lines this branch did not touch. CI uses `--new-from-merge-base`, so pre-existing findings are acceptable; findings on your new lines are not.

- [ ] **Step 3: Local-path check**

Run: `make verify-local-paths`
Expected: PASS. The spec and plan under `docs/superpowers/` must not contain absolute local paths.

- [ ] **Step 4: Manual smoke against a running server**

With a local or dev Silo running and a valid bearer token:

```bash
curl -N -H "Authorization: Bearer $SILO_TOKEN" \
  "http://localhost:8090/api/v1/events/sse?channels=sessions"
```

Expected: an immediate `event: hello` frame, then `event: ping` frames at the keepalive interval, and an `event: sessions` frame when playback starts or stops. The connection must stay open — if it closes after a few seconds, the handler is returning early.

Record the observed output in your report.

- [ ] **Step 5: Commit any fixes**

```bash
git add -A
git commit -m "fix(events): address SSE verification findings"
```

---

## Notes for the implementer

- **This adds a transport; it changes no behaviour.** If you find yourself editing `hub.go`, `types.go`, or `HandleWebSocket`, stop — that is out of scope.
- **`allowsEventForClaims` is called with an empty `boundProfileID`** because SSE has no ws-ticket binding. That is intentional and is why profile-scoped channels are excluded.
- **Nothing is pushed and no PR is opened.** Work stays on `feat/events-sse-stream` locally until the project owner decides how to approach upstream.

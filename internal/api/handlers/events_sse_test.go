package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
)

// newAuthedSSERequest builds a GET request whose context carries admin
// claims, following the same apimw.SetClaims pattern used by
// events_ws_user_settings_test.go and session_ws_test.go for the sibling
// websocket handler.
func newAuthedSSERequest(t *testing.T, target string) *http.Request {
	t.Helper()
	return newAuthedSSERequestWithRole(t, target, "admin")
}

// newAuthedSSERequestWithRole is newAuthedSSERequest with an explicit role,
// for tests that need to assert non-admin behavior.
func newAuthedSSERequestWithRole(t *testing.T, target, role string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{
		UserID:    1,
		Role:      role,
		TokenType: auth.TokenTypeAccess,
	})
	return req.WithContext(ctx)
}

// runSSEHandlerBriefly runs h.HandleSSE against a context that is canceled
// shortly after the handler starts, so the hello frame (and streaming
// headers) is written before HandleSSE's blocking loop is torn down.
func runSSEHandlerBriefly(t *testing.T, h *EventsHandler, rec *httptest.ResponseRecorder, req *http.Request) {
	t.Helper()
	ctx, cancel := context.WithTimeout(req.Context(), 50*time.Millisecond)
	defer cancel()
	h.HandleSSE(rec, req.WithContext(ctx))
}

// flushSignal wraps httptest.ResponseRecorder so a test can wait for the Nth
// flush HandleSSE performs, instead of guessing a delay. writeSSEFrame
// flushes exactly once per frame written, so counting flushes counts frames:
// flush #1 is always the hello frame — sent only after h.hub.Subscribe() has
// returned — and each flush after that corresponds to one more ping or event
// frame written. Waiting for a specific count lets a test prove the handler
// has processed everything published before it, rather than assuming so
// after a fixed delay. It still satisfies http.Flusher (required by
// HandleSSE) and, via the embedded *httptest.ResponseRecorder, the same "not
// supported" path through http.NewResponseController that HandleSSE already
// tolerates.
type flushSignal struct {
	*httptest.ResponseRecorder

	mu      sync.Mutex
	count   int
	waiting map[int]chan struct{}
}

func newFlushSignal() *flushSignal {
	return &flushSignal{
		ResponseRecorder: httptest.NewRecorder(),
		waiting:          make(map[int]chan struct{}),
	}
}

func (f *flushSignal) Flush() {
	f.ResponseRecorder.Flush()
	f.mu.Lock()
	f.count++
	for n, ch := range f.waiting {
		if f.count >= n {
			close(ch)
			delete(f.waiting, n)
		}
	}
	f.mu.Unlock()
}

// waitForFlush blocks until at least n flushes have been observed, or fails
// the test after timeout. Calling it with an n already reached returns
// immediately.
func (f *flushSignal) waitForFlush(t *testing.T, n int, timeout time.Duration) {
	t.Helper()

	f.mu.Lock()
	if f.count >= n {
		f.mu.Unlock()
		return
	}
	ch := make(chan struct{})
	f.waiting[n] = ch
	f.mu.Unlock()

	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatalf("HandleSSE did not reach %d flush(es) before the timeout", n)
	}
}

// runSSEHandlerAndAwaitFrames runs h.HandleSSE in a goroutine, waits for its
// first flush (proof that it has already subscribed to the hub), publishes
// each of envs in order, waits until minFlushes total flushes have been
// observed, cancels the context, and waits (with a bounded timeout of its
// own) for the handler to return. It returns the recorded body.
//
// This closes two distinct races that a fixed delay cannot:
//
//  1. Publishing before HandleSSE calls hub.Subscribe() would silently lose
//     the event — hub.Publish only fans out to subscribers that already
//     exist — ruled out by waiting for the first flush before publishing
//     anything.
//  2. Canceling before HandleSSE has actually processed and written the
//     frames a test expects would make an "absence" assertion pass for the
//     wrong reason: the handler never got there, not that it correctly
//     suppressed something. Waiting for minFlushes rather than a fixed delay
//     rules this out too.
//
// A caller asserting an envelope is suppressed must publish, after it, one
// that is genuinely subscribed and will produce a frame, and include that
// frame in minFlushes — otherwise this blocks for the full timeout waiting on
// a flush that will never arrive.
func runSSEHandlerAndAwaitFrames(
	t *testing.T,
	h *EventsHandler,
	hub *evt.Hub,
	rec *flushSignal,
	req *http.Request,
	minFlushes int,
	envs ...evt.Envelope,
) string {
	t.Helper()
	const timeout = 5 * time.Second

	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.HandleSSE(rec, req.WithContext(ctx))
	}()

	rec.waitForFlush(t, 1, timeout)

	for _, env := range envs {
		if err := hub.Publish(context.Background(), env); err != nil {
			t.Fatalf("hub.Publish: %v", err)
		}
	}

	rec.waitForFlush(t, minFlushes, timeout)

	cancel()

	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("HandleSSE did not return after its context was canceled")
	}

	return rec.Body.String()
}

func TestHandleSSESetsStreamingHeaders(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
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
	hub := evt.NewHub("test", &cache.NoopEventBus{})
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
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	h := &EventsHandler{hub: hub}

	req := newAuthedSSERequest(t, "/api/v1/events/sse?channels=sessions")
	rec := newFlushSignal()

	// Populate every internal-only Envelope field alongside the public ones,
	// so this test would genuinely fail if sseEventMessage regressed to
	// marshaling the raw envelope instead of projecting it — which is
	// precisely the bug an earlier review round found and fixed. minFlushes
	// is 2: the hello frame, then this event's frame.
	body := runSSEHandlerAndAwaitFrames(t, h, hub, rec, req, 2, evt.Envelope{
		Channel:        evt.ChannelSessions,
		Event:          "sessions.replaced",
		EventID:        "evt_test_123",
		SourceID:       "node-a",
		UserID:         42,
		ProfileID:      "profile_1",
		AdminOnly:      true,
		TargetPluginID: "plugin.example",
		Data:           json.RawMessage(`{"foo":"bar"}`),
	})

	data := sseFrameData(t, body, "event: sessions")

	var msg evt.EventsEventMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("decoding sessions event payload: %v; data = %s", err, data)
	}
	const wantType = "event"
	if msg.Type != wantType {
		t.Fatalf("type = %q, want %q", msg.Type, wantType)
	}
	if msg.Channel != evt.ChannelSessions {
		t.Fatalf("channel = %q, want %q", msg.Channel, evt.ChannelSessions)
	}
	if msg.Event != "sessions.replaced" {
		t.Fatalf("event = %q, want %q", msg.Event, "sessions.replaced")
	}
	if msg.EventID != "evt_test_123" {
		t.Fatalf("event_id = %q, want %q", msg.EventID, "evt_test_123")
	}
	if msg.Timestamp == "" {
		t.Fatal("timestamp missing")
	}
	if string(msg.Data) != `{"foo":"bar"}` {
		t.Fatalf("data = %s, want %s", msg.Data, `{"foo":"bar"}`)
	}

	// The wire projection (evt.EventsEventMessage) has no field for any of
	// these — decoding into it above already drops them silently — so assert
	// their absence on the raw JSON instead, where a regression to
	// marshaling evt.Envelope directly would actually show up.
	for _, forbidden := range []string{"source_id", "admin_only", "target_plugin_id", "user_id", "profile_id"} {
		if strings.Contains(string(data), `"`+forbidden+`"`) {
			t.Fatalf("internal field %q leaked onto the wire, data = %s", forbidden, data)
		}
	}
}

// sseFrameData extracts the JSON payload of the "data:" line belonging to
// the frame introduced by eventLine (e.g. "event: sessions") from a raw SSE
// response body.
func sseFrameData(t *testing.T, body, eventLine string) []byte {
	t.Helper()
	idx := strings.Index(body, eventLine+"\n")
	if idx == -1 {
		t.Fatalf("frame %q not found, body = %q", eventLine, body)
	}
	rest := body[idx+len(eventLine)+1:]
	const dataPrefix = "data: "
	if !strings.HasPrefix(rest, dataPrefix) {
		t.Fatalf("frame %q missing a data: line, body = %q", eventLine, body)
	}
	rest = rest[len(dataPrefix):]
	end := strings.IndexByte(rest, '\n')
	if end == -1 {
		t.Fatalf("frame %q data line unterminated, body = %q", eventLine, body)
	}
	return []byte(rest[:end])
}

func TestHandleSSESuppressesUnsubscribedChannel(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	h := &EventsHandler{hub: hub}

	req := newAuthedSSERequest(t, "/api/v1/events/sse?channels=sessions")
	rec := newFlushSignal()

	// A suppressed envelope alone never produces a frame, so canceling after
	// a fixed delay could pass this test for the wrong reason — the handler
	// might simply not have reached it yet. Publishing a sentinel on
	// "sessions" (the one channel this caller actually subscribed to) right
	// after it, and waiting for the sentinel's frame (minFlushes: hello +
	// sentinel = 2), proves the handler ran past the suppressed envelope
	// rather than never getting there.
	body := runSSEHandlerAndAwaitFrames(t, h, hub, rec, req, 2,
		evt.Envelope{Channel: evt.ChannelJobs, Event: "jobs.updated"},
		evt.Envelope{Channel: evt.ChannelSessions, Event: "sessions.sentinel"},
	)

	if !strings.Contains(body, "event: sessions") {
		t.Fatalf("sentinel event missing; handler may never have run past the suppressed envelope, body = %q", body)
	}
	if strings.Contains(body, "event: jobs") {
		t.Fatalf("unsubscribed channel leaked into the stream, body = %q", body)
	}
}

func TestHandleSSENonAdminOmitsAdminOnlyChannel(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	h := &EventsHandler{hub: hub}

	// "jobs" is admin-only per allowedChannelsForRole; a plain "user" caller
	// requesting it must not error the request, but must never see it on the
	// wire. This is the endpoint's core authorization guarantee. "catalog" is
	// requested alongside it because a plain user genuinely is allowed that
	// one (the design doc's own rule: "a caller asking for one forbidden
	// channel among several should still get the rest") — it gives this test
	// a real sentinel channel to synchronize on instead of a fixed delay.
	req := newAuthedSSERequestWithRole(t, "/api/v1/events/sse?channels=jobs,catalog", "user")
	rec := newFlushSignal()

	body := runSSEHandlerAndAwaitFrames(t, h, hub, rec, req, 2,
		evt.Envelope{Channel: evt.ChannelJobs, Event: "jobs.updated", AdminOnly: true},
		evt.Envelope{Channel: evt.ChannelCatalog, Event: "catalog.sentinel"},
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (a forbidden channel request must not error)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(body, "event: hello") {
		t.Fatalf("hello frame missing, body = %q", body)
	}
	if !strings.Contains(body, "event: catalog") {
		t.Fatalf("sentinel event missing; handler may never have run past the admin-only envelope, body = %q", body)
	}
	if strings.Contains(body, "event: jobs") {
		t.Fatalf("admin-only channel leaked to a non-admin caller, body = %q", body)
	}
}

func TestHandleSSEMalformedChannelsDenyByDefault(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	h := &EventsHandler{hub: hub}

	// A garbage channels value must not error the request, and must not
	// widen access. This mixes malformed tokens ("bogus", empty entries)
	// with one legitimately requested channel ("sessions") rather than an
	// all-garbage value: TestResolveSSEChannelsGarbageDeniesByDefault below
	// already proves the pure "all garbage yields an empty subscription"
	// claim as a fast, non-concurrent unit test; this end-to-end test proves
	// the handler is actually alive and does not widen a mixed request to
	// the caller's full allowed set — "jobs" is allowed for this admin
	// caller but was never requested, so it must stay silent, while
	// "sessions" (requested and allowed) must arrive and doubles as the
	// sentinel that rules out the "canceled before the handler got there"
	// race.
	req := newAuthedSSERequest(t, "/api/v1/events/sse?channels=,,,bogus,sessions")
	rec := newFlushSignal()

	body := runSSEHandlerAndAwaitFrames(t, h, hub, rec, req, 2,
		evt.Envelope{Channel: evt.ChannelJobs, Event: "jobs.updated"},
		evt.Envelope{Channel: evt.ChannelSessions, Event: "sessions.sentinel"},
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (malformed channels must not error the request)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(body, "event: hello") {
		t.Fatalf("hello frame missing, body = %q", body)
	}
	if !strings.Contains(body, "event: sessions") {
		t.Fatalf("sentinel event missing; handler may never have run past the malformed-channel envelope, body = %q", body)
	}
	if strings.Contains(body, "event: jobs") {
		t.Fatalf("malformed channels widened access to an unrequested channel; body = %q", body)
	}
}

func TestResolveSSEChannelsGarbageDeniesByDefault(t *testing.T) {
	allowed := []evt.EventChannel{evt.ChannelSessions, evt.ChannelJobs}

	got := resolveSSEChannels(",,,bogus,", allowed)
	if len(got) != 0 {
		t.Fatalf("resolveSSEChannels(garbage) = %v, want empty map (deny-by-default)", got)
	}

	// Sanity check the other branch: an empty request means "everything the
	// role allows", not "nothing" — malformed and absent are different.
	got = resolveSSEChannels("", allowed)
	if len(got) != len(allowed) {
		t.Fatalf("resolveSSEChannels(\"\") = %v, want everything allowed (%v)", got, allowed)
	}
}

func TestHandleSSERejectsUnauthenticated(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	h := &EventsHandler{hub: hub}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/sse", nil)
	rec := httptest.NewRecorder()

	h.HandleSSE(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

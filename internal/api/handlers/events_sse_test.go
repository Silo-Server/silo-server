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

// flushSignal wraps httptest.ResponseRecorder so a test can be notified the
// instant HandleSSE performs its first flush, instead of guessing a delay.
// writeSSEFrame flushes after every write, and the very first frame HandleSSE
// writes is the hello frame — sent only after h.hub.Subscribe() returns — so
// "first flush observed" is a race-free proxy for "the handler has already
// subscribed". It still satisfies http.Flusher (required by HandleSSE) and,
// via the embedded *httptest.ResponseRecorder, the same "not supported" path
// through http.NewResponseController that HandleSSE already tolerates.
type flushSignal struct {
	*httptest.ResponseRecorder
	once    sync.Once
	flushed chan struct{}
}

func newFlushSignal() *flushSignal {
	return &flushSignal{
		ResponseRecorder: httptest.NewRecorder(),
		flushed:          make(chan struct{}),
	}
}

func (f *flushSignal) Flush() {
	f.ResponseRecorder.Flush()
	f.once.Do(func() { close(f.flushed) })
}

// runSSEHandlerWithPublish runs h.HandleSSE in a goroutine, waits for its
// first flush (proof that it has already subscribed to the hub), publishes
// env only then, and lets the request context expire so HandleSSE returns.
// It returns the recorded body.
//
// Publishing before HandleSSE calls hub.Subscribe() would silently lose the
// event — hub.Publish only fans out to subscribers that already exist — so
// this must not rely on a fixed delay and hope the handler got there first;
// the flush signal makes the ordering deterministic instead.
func runSSEHandlerWithPublish(
	t *testing.T,
	h *EventsHandler,
	hub *evt.Hub,
	rec *flushSignal,
	req *http.Request,
	env evt.Envelope,
) string {
	t.Helper()
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.HandleSSE(rec, req.WithContext(ctx))
	}()

	select {
	case <-rec.flushed:
		// The hello frame is out, which only happens after hub.Subscribe()
		// has returned — safe to publish now.
	case <-time.After(5 * time.Second):
		t.Fatal("HandleSSE did not flush its hello frame before the timeout")
	}

	if err := hub.Publish(context.Background(), env); err != nil {
		t.Fatalf("hub.Publish: %v", err)
	}

	// The subscribe race is already ruled out above; this just gives the
	// now-subscribed handler's select loop a moment to observe the publish
	// and write it out before the context is canceled underneath it.
	time.AfterFunc(30*time.Millisecond, cancel)
	<-done

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
	// precisely the bug an earlier review round found and fixed.
	body := runSSEHandlerWithPublish(t, h, hub, rec, req, evt.Envelope{
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

	body := runSSEHandlerWithPublish(t, h, hub, rec, req, evt.Envelope{
		Channel: evt.ChannelJobs,
		Event:   "jobs.updated",
	})

	if strings.Contains(body, "event: jobs") {
		t.Fatalf("unsubscribed channel leaked into the stream, body = %q", body)
	}
}

func TestHandleSSENonAdminOmitsAdminOnlyChannel(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	h := &EventsHandler{hub: hub}

	// "jobs" is admin-only per allowedChannelsForRole; a plain "user" caller
	// requesting it must not error the request, but must never see it on the
	// wire. This is the endpoint's core authorization guarantee.
	req := newAuthedSSERequestWithRole(t, "/api/v1/events/sse?channels=jobs", "user")
	rec := newFlushSignal()

	body := runSSEHandlerWithPublish(t, h, hub, rec, req, evt.Envelope{
		Channel:   evt.ChannelJobs,
		Event:     "jobs.updated",
		AdminOnly: true,
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (a forbidden channel request must not error)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(body, "event: hello") {
		t.Fatalf("hello frame missing, body = %q", body)
	}
	if strings.Contains(body, "event: jobs") {
		t.Fatalf("admin-only channel leaked to a non-admin caller, body = %q", body)
	}
}

func TestHandleSSEMalformedChannelsDenyByDefault(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	h := &EventsHandler{hub: hub}

	// A garbage channels value must not error the request, and must not
	// widen access: the safe outcome is an empty subscription (hello and
	// pings only), never "everything allowed".
	req := newAuthedSSERequest(t, "/api/v1/events/sse?channels=,,,bogus,")
	rec := newFlushSignal()

	body := runSSEHandlerWithPublish(t, h, hub, rec, req, evt.Envelope{
		Channel: evt.ChannelSessions,
		Event:   "sessions.replaced",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (malformed channels must not error the request)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(body, "event: hello") {
		t.Fatalf("hello frame missing, body = %q", body)
	}
	if strings.Contains(body, "event: sessions") {
		t.Fatalf("malformed channels widened access instead of denying by default; body = %q", body)
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

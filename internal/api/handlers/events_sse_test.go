package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
// for tests that need to assert non-admin behaviour.
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

// runSSEHandlerBriefly runs h.HandleSSE against a context that is cancelled
// shortly after the handler starts, so the hello frame (and streaming
// headers) is written before HandleSSE's blocking loop is torn down.
func runSSEHandlerBriefly(t *testing.T, h *EventsHandler, rec *httptest.ResponseRecorder, req *http.Request) {
	t.Helper()
	ctx, cancel := context.WithTimeout(req.Context(), 50*time.Millisecond)
	defer cancel()
	h.HandleSSE(rec, req.WithContext(ctx))
}

// runSSEHandlerWithPublish runs h.HandleSSE, publishes env to the hub shortly
// after the handler subscribes, then lets the request context expire so
// HandleSSE returns. It returns the recorded body.
func runSSEHandlerWithPublish(
	t *testing.T,
	h *EventsHandler,
	hub *evt.Hub,
	rec *httptest.ResponseRecorder,
	req *http.Request,
	env evt.Envelope,
) string {
	t.Helper()
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()

	time.AfterFunc(10*time.Millisecond, func() {
		_ = hub.Publish(context.Background(), env)
	})
	time.AfterFunc(50*time.Millisecond, cancel)

	h.HandleSSE(rec, req.WithContext(ctx))
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
	hub := evt.NewHub("test", &cache.NoopEventBus{})
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

func TestHandleSSENonAdminOmitsAdminOnlyChannel(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	h := &EventsHandler{hub: hub}

	// "jobs" is admin-only per allowedChannelsForRole; a plain "user" caller
	// requesting it must not error the request, but must never see it on the
	// wire. This is the endpoint's core authorization guarantee.
	req := newAuthedSSERequestWithRole(t, "/api/v1/events/sse?channels=jobs", "user")
	rec := httptest.NewRecorder()

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
	rec := httptest.NewRecorder()

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

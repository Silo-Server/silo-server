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
	req := httptest.NewRequest(http.MethodGet, target, nil)
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{
		UserID:    1,
		Role:      "admin",
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

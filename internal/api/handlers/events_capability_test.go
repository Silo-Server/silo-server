package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
)

func TestHandleCapabilityReturnsOKForAuthenticatedCaller(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	h := &EventsHandler{hub: hub}

	req := newAuthedSSERequest(t, "/api/v1/events/capability")
	rec := httptest.NewRecorder()

	h.HandleCapability(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandleCapabilityAdvertisesBothTransports(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	h := &EventsHandler{hub: hub}

	req := newAuthedSSERequest(t, "/api/v1/events/capability")
	rec := httptest.NewRecorder()

	h.HandleCapability(rec, req)

	var body struct {
		SchemaVersion int `json:"schema_version"`
		SSE           struct {
			Supported bool   `json:"supported"`
			Path      string `json:"path"`
		} `json:"sse"`
		WebSocket struct {
			Supported bool   `json:"supported"`
			Path      string `json:"path"`
		} `json:"websocket"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if body.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", body.SchemaVersion)
	}
	if !body.SSE.Supported {
		t.Errorf("sse.supported = false, want true")
	}
	if body.SSE.Path != "/api/v1/events/sse" {
		t.Errorf("sse.path = %q, want /api/v1/events/sse", body.SSE.Path)
	}
	if !body.WebSocket.Supported {
		t.Errorf("websocket.supported = false, want true")
	}
	if body.WebSocket.Path != "/api/v1/events/ws" {
		t.Errorf("websocket.path = %q, want /api/v1/events/ws", body.WebSocket.Path)
	}
}

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

func TestEventsCapabilityReportsDeclaredChannelSupport(t *testing.T) {
	handler := &EventsHandler{}
	rec := httptest.NewRecorder()
	handler.HandleCapability(rec, httptest.NewRequest(http.MethodGet, "/events/capability", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got eventsCapabilityResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding capability: %v (%s)", err, rec.Body.String())
	}

	if !got.DeclaredChannels {
		t.Error("declared_channels = false; a client cannot detect ?channels= support")
	}
	if !got.SubscribeFrame {
		t.Error("subscribe_frame = false; the handshake is still supported")
	}
	if got.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", got.SchemaVersion)
	}

	// The advertised grace period must be the one the handler actually
	// enforces, or a client will size its handshake timeout against fiction.
	if want := int(subscribeGracePeriod.Seconds()); got.SubscribeGracePeriodSeconds != want {
		t.Errorf("subscribe_grace_period_seconds = %d, want %d", got.SubscribeGracePeriodSeconds, want)
	}

	if len(got.Channels) != len(evt.AllChannels) {
		t.Errorf("channels = %v, want all %d channels", got.Channels, len(evt.AllChannels))
	}
}

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

	if got.MaxRequestedChannels != maxRequestedChannels {
		t.Errorf("max_requested_channels = %d, want %d", got.MaxRequestedChannels, maxRequestedChannels)
	}

	if len(got.Channels) != len(evt.ClientChannels) {
		t.Errorf("channels = %v, want all %d client channels", got.Channels, len(evt.ClientChannels))
	}
}

// TestEventsCapabilityAdvertisesOnlySubscribableChannels is the point of
// publishing the list at all: a client that requests everything the endpoint
// names must not be refused any of it. The plugins channel is the case — it is
// in evt.AllChannels but granted to no role, admin included, so advertising it
// would send a client to a request that can only fail.
func TestEventsCapabilityAdvertisesOnlySubscribableChannels(t *testing.T) {
	handler := &EventsHandler{}
	rec := httptest.NewRecorder()
	handler.HandleCapability(rec, httptest.NewRequest(http.MethodGet, "/events/capability", nil))

	var got eventsCapabilityResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding capability: %v (%s)", err, rec.Body.String())
	}

	// An admin, on a profile-bound connection, is the most permissive caller
	// there is. Every advertised channel must resolve for them.
	_, accepted, rejected := resolveChannelSelection(
		got.Channels, allowedChannelsForRole("admin"), "profile-1")

	if len(rejected) != 0 {
		t.Errorf("capability advertises channels an admin cannot subscribe to: %v", rejected)
	}
	if len(accepted) != len(got.Channels) {
		t.Errorf("accepted %d of %d advertised channels", len(accepted), len(got.Channels))
	}
}

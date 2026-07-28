package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
)

// receiveUserSettingsEvent drains one envelope from the subscription, which is
// already buffered by the time the handler returns because local fan-out is
// synchronous.
func receiveUserSettingsEvent(t *testing.T, events <-chan evt.Envelope) evt.Envelope {
	t.Helper()
	select {
	case env := <-events:
		return env
	default:
		t.Fatal("no event was published to the hub")
		return evt.Envelope{}
	}
}

func assertUserSettingsEnvelope(t *testing.T, env evt.Envelope, wantKey, wantScope string) {
	t.Helper()
	if env.Channel != evt.ChannelUserSettings {
		t.Errorf("channel = %q, want %q", env.Channel, evt.ChannelUserSettings)
	}
	if env.Event != userSettingsChangedEvent {
		t.Errorf("event = %q, want %q", env.Event, userSettingsChangedEvent)
	}
	if env.UserID != 1 || env.ProfileID != "profile-1" {
		t.Errorf("addressed to user %d profile %q, want 1/profile-1", env.UserID, env.ProfileID)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("payload is not a JSON object: %v", err)
	}
	if string(payload["key"]) != `"`+wantKey+`"` {
		t.Errorf("payload key = %s, want %q", payload["key"], wantKey)
	}
	if string(payload["scope"]) != `"`+wantScope+`"` {
		t.Errorf("payload scope = %s, want %q", payload["scope"], wantScope)
	}
	if string(payload["profile_id"]) != `"profile-1"` {
		t.Errorf("payload profile_id = %s, want \"profile-1\"", payload["profile_id"])
	}
	// The value must never ride along: admins receive every user's user-scoped
	// events, so a value here would leak private settings to admins.
	if raw, present := payload["value"]; present {
		t.Errorf("payload carries a value (%s); it must never leak into events", raw)
	}
}

func TestSetValuePublishesUserSettingsEvent(t *testing.T) {
	handler, _ := newValuesTestHandler(t)
	handler.EventsHub = evt.NewHub("test", &cache.NoopEventBus{})
	events, unsubscribe := handler.EventsHub.Subscribe()
	defer unsubscribe()

	rec := routeValues(t, handler, http.MethodPut, "playback.subtitle_language",
		"scope=profile", []byte(`{"value":"ja"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}

	env := receiveUserSettingsEvent(t, events)
	assertUserSettingsEnvelope(t, env, "playback.subtitle_language", "profile")
}

func TestDeleteValuePublishesUserSettingsEvent(t *testing.T) {
	handler, _ := newValuesTestHandler(t)
	handler.EventsHub = evt.NewHub("test", &cache.NoopEventBus{})
	events, unsubscribe := handler.EventsHub.Subscribe()
	defer unsubscribe()

	if rec := routeValues(t, handler, http.MethodPut, "playback.subtitle_language",
		"scope=profile", []byte(`{"value":"ja"}`)); rec.Code != http.StatusOK {
		t.Fatalf("seeding PUT = %d: %s", rec.Code, rec.Body.String())
	}
	<-events // drain the write's own event

	rec := routeValues(t, handler, http.MethodDelete, "playback.subtitle_language",
		"scope=profile", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}

	env := receiveUserSettingsEvent(t, events)
	assertUserSettingsEnvelope(t, env, "playback.subtitle_language", "profile")
}

// TestFailedMutationsPublishNothing: a refused write and a delete of nothing
// must not tell clients something changed.
func TestFailedMutationsPublishNothing(t *testing.T) {
	handler, _ := newValuesTestHandler(t)
	handler.EventsHub = evt.NewHub("test", &cache.NoopEventBus{})
	events, unsubscribe := handler.EventsHub.Subscribe()
	defer unsubscribe()

	if rec := routeValues(t, handler, http.MethodPut, "playback.subtitle_language",
		"scope=profile", []byte(`{"value":"!!!"}`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid PUT = %d, want 400", rec.Code)
	}
	if rec := routeValues(t, handler, http.MethodDelete, "playback.subtitle_language",
		"scope=profile", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE of nothing = %d, want 404", rec.Code)
	}

	select {
	case env := <-events:
		t.Errorf("a failed mutation published %s on %s", env.Event, env.Channel)
	default:
	}
}

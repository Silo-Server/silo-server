package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
)

// TestEventsWebSocketDeliversUserSettingsToNonAdmins goes through the real
// websocket rather than subscribing on the Hub directly, because that is the
// only place the channel's authorization lives: dropping ChannelUserSettings
// from allowedChannelsForRole answers the subscribe with {code:"forbidden"},
// and dropping it from evt.AllChannels closes the connection as an invalid
// channel — either way the server would keep publishing change events no
// client could ever receive, while every Hub-level test stayed green.
func TestEventsWebSocketDeliversUserSettingsToNonAdmins(t *testing.T) {
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	handler := &EventsHandler{hub: hub}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The router authenticates before the handler runs; a plain (non-admin)
		// user is the role whose devices must hear their own settings change.
		ctx := apimw.SetClaims(r.Context(), &auth.Claims{UserID: 1, Role: "user"})
		handler.HandleWebSocket(w, r.WithContext(ctx))
	}))
	defer server.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dialing events websocket: %v", err)
	}
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	defer func() { _ = conn.Close() }()

	readFrame := func(wantType string) map[string]json.RawMessage {
		t.Helper()
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("setting read deadline: %v", err)
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading %s frame: %v", wantType, err)
		}
		var frame map[string]json.RawMessage
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("frame is not JSON: %v (%s)", err, data)
		}
		if string(frame["type"]) != `"`+wantType+`"` {
			t.Fatalf("frame type = %s, want %q (frame: %s)", frame["type"], wantType, data)
		}
		return frame
	}

	hello := readFrame("hello")
	if !strings.Contains(string(hello["available_channels"]), `"user_settings"`) {
		t.Fatalf("hello does not offer user_settings: %s", hello["available_channels"])
	}

	if err := conn.WriteJSON(evt.EventsSubscribeMessage{
		Type:      "subscribe",
		RequestID: "r1",
		Channels:  []evt.EventChannel{evt.ChannelUserSettings},
	}); err != nil {
		t.Fatalf("sending subscribe: %v", err)
	}

	subscribed := readFrame("subscribed")
	if !strings.Contains(string(subscribed["channels"]), `"user_settings"`) {
		t.Fatalf("subscribe was not accepted: %s", subscribed["channels"])
	}
	if rejected, present := subscribed["rejected"]; present && string(rejected) != "null" && string(rejected) != "[]" {
		t.Fatalf("subscribe was rejected: %s", rejected)
	}

	// The accepted subscription hydrates with a snapshot frame first.
	snapshot := readFrame("snapshot")
	if string(snapshot["channel"]) != `"user_settings"` {
		t.Fatalf("snapshot channel = %s, want user_settings", snapshot["channel"])
	}

	// A change event addressed to this user must reach the connection.
	publishUserSettingsEvent(context.Background(), hub, 1, "profile-1",
		"playback.subtitle_language", "profile")

	event := readFrame("event")
	if string(event["channel"]) != `"user_settings"` {
		t.Errorf("event channel = %s, want user_settings", event["channel"])
	}
	if string(event["event"]) != `"`+userSettingsChangedEvent+`"` {
		t.Errorf("event = %s, want %q", event["event"], userSettingsChangedEvent)
	}
}

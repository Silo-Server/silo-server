package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestPersonalEventRecipients(t *testing.T) {
	for _, role := range []string{models.RoleUser, models.RoleAdmin} {
		for _, tc := range []struct {
			name                          string
			bound, profile                string
			userID                        int
			state, settings, notification bool
		}{
			{"same profile", "a", "a", 7, true, true, true},
			{"sibling profile", "a", "b", 7, false, false, false},
			{"other account", "a", "a", 8, false, false, false},
			{"account settings", "a", "", 7, false, true, false},
			{"unbound profile event", "", "a", 7, false, false, false},
			{"unbound account settings", "", "", 7, false, true, false},
			{"other account settings", "a", "", 8, false, false, false},
			{"missing account", "a", "a", 0, false, false, false},
			{"negative account", "a", "a", -1, false, false, false},
			{"missing identity", "", "", 0, false, false, false},
		} {
			t.Run(role+"/"+tc.name, func(t *testing.T) {
				claims := &auth.Claims{UserID: 7, Role: role}
				for _, channel := range []struct {
					channel evt.EventChannel
					want    bool
				}{
					{evt.ChannelUserState, tc.state},
					{evt.ChannelUserSettings, tc.settings},
					{evt.ChannelNotifications, tc.notification},
				} {
					env := evt.Envelope{Channel: channel.channel, UserID: tc.userID, ProfileID: tc.profile}
					if got := allowsEventForClaims(claims, tc.bound, env); got != channel.want {
						t.Errorf("%s delivered = %v, want %v", channel.channel, got, channel.want)
					}
				}
			})
		}
	}
	for _, channel := range []evt.EventChannel{evt.ChannelUserState, evt.ChannelUserSettings} {
		env := evt.Envelope{Channel: channel, UserID: 7, ProfileID: "a", AdminOnly: true}
		if allowsEventForClaims(nil, "a", env) || allowsEventForClaims(&auth.Claims{UserID: 7, Role: models.RoleUser}, "a", env) {
			t.Errorf("%s bypassed missing claims or admin-only restriction", channel)
		}
		if !allowsEventForClaims(&auth.Claims{UserID: 7, Role: models.RoleAdmin}, "a", env) {
			t.Errorf("%s rejected the intended administrator", channel)
		}
	}
}

// Use real v2 tickets, network sockets, publishers and the local hub. A final
// catalog event fences the ordered stream, so rejection assertions never rely
// on sleeping or on a read timeout. Authority repository behavior is covered
// separately by the socket validator tests.
func TestEventsSocketV2PersonalChannelIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, role, profile string
	}{
		{"profile", models.RoleUser, "a"},
		{"admin profile", models.RoleAdmin, "a"},
		{"unbound", models.RoleUser, ""},
		{"unbound admin", models.RoleAdmin, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := socketTestHandler()
			hub := h.Events.hub
			read := openPersonalEventSocket(t, h, tc.role, tc.profile)

			publishUserStateEvent(t.Context(), hub, 7, "b", "sibling-state", "", "watched", userStateEventState{Played: new(true)})
			publishUserSettingsEvent(t.Context(), hub, 7, "b", "sibling-setting", "profile")
			publishUserStateEvent(t.Context(), hub, 8, "a", "foreign-state", "", "watched", userStateEventState{})
			publishUserSettingsEvent(t.Context(), hub, 8, "a", "foreign-setting", "profile")
			publishUserSettingsEvent(t.Context(), hub, 8, "", "foreign-account-setting", "account")
			publishUserStateEvent(t.Context(), hub, 7, "a", "own-state", "", "watched", userStateEventState{Played: new(true)})
			publishUserSettingsEvent(t.Context(), hub, 7, "a", "own-setting", "profile")
			publishUserSettingsEvent(t.Context(), hub, 7, "", "account-setting", "account")
			for _, env := range []evt.Envelope{
				{Channel: evt.ChannelUserState, Event: "missing-profile", UserID: 7},
				{Channel: evt.ChannelUserState, Event: "missing-account", ProfileID: "a"},
				{Channel: evt.ChannelUserSettings, Event: "missing-account-setting", ProfileID: "a"},
				{Channel: evt.ChannelUserSettings, Event: "negative-account-setting", UserID: -1},
				{Channel: evt.ChannelNotifications, Event: "sibling-notification", UserID: 7, ProfileID: "b"},
				{Channel: evt.ChannelNotifications, Event: "foreign-notification", UserID: 8, ProfileID: "a"},
				{Channel: evt.ChannelNotifications, Event: "own-notification", UserID: 7, ProfileID: "a"},
				{Channel: evt.ChannelHistoryImport, Event: "unsubscribed", UserID: 7},
				{Channel: evt.ChannelCatalog, Event: "fence"},
			} {
				if err := hub.Publish(t.Context(), env); err != nil {
					t.Fatal(err)
				}
			}
			var got []string
			for {
				var frame evt.EventsEventMessage
				if err := json.Unmarshal(read(), &frame); err != nil || frame.Type != "event" {
					t.Fatalf("event: %+v, %v", frame, err)
				}
				if frame.Event == "fence" {
					break
				}
				var payload struct {
					ContentID string `json:"content_id"`
					Key       string `json:"key"`
				}
				if frame.Event == userStateChangedEvent || frame.Event == userSettingsChangedEvent {
					if err := json.Unmarshal(frame.Data, &payload); err != nil {
						t.Fatal(err)
					}
				}
				switch frame.Event {
				case userStateChangedEvent:
					got = append(got, payload.ContentID)
				case userSettingsChangedEvent:
					got = append(got, payload.Key)
				default:
					got = append(got, frame.Event)
				}
			}
			want := []string{"account-setting"}
			if tc.profile != "" {
				want = []string{"own-state", "own-setting", "account-setting", "own-notification"}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("delivered = %v, want %v", got, want)
			}
		})
	}
}

func openPersonalEventSocket(t *testing.T, h *EventsSocketV2, role, profile string) func() []byte {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	ticket, err := h.Tickets.Mint(t.Context(), evt.SocketIdentity{
		UserID: 7, Role: role, SessionID: "synthetic-session", ProfileID: profile,
		AccessExpiresAt: time.Now().Add(time.Minute), AccessFingerprint: "synthetic-scope",
	})
	if err != nil {
		t.Fatal(err)
	}
	dialer := websocket.Dialer{Subprotocols: []string{EventsSocketProtocol, eventsTicketProtocolPrefix + ticket}}
	conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+
		"?channels=user_state,user_settings,notifications,catalog", nil)
	if resp != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	read := func() []byte {
		t.Helper()
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var hello evt.EventsHelloMessage
	if err := json.Unmarshal(read(), &hello); err != nil || hello.Type != "hello" {
		t.Fatalf("hello: %+v, %v", hello, err)
	}
	var subscribed evt.EventsSubscribedMessage
	if err := json.Unmarshal(read(), &subscribed); err != nil || subscribed.Type != "subscribed" {
		t.Fatalf("subscription: %+v, %v", subscribed, err)
	}
	wantChannels := []evt.EventChannel{evt.ChannelUserState, evt.ChannelUserSettings, evt.ChannelCatalog}
	if profile != "" {
		wantChannels = append(wantChannels, evt.ChannelNotifications)
		if len(subscribed.Rejected) != 0 {
			t.Fatalf("unexpected rejections: %+v", subscribed.Rejected)
		}
	} else if len(subscribed.Rejected) != 1 || subscribed.Rejected[0].Channel != evt.ChannelNotifications || subscribed.Rejected[0].Code != "profile_required" {
		t.Fatalf("unbound notification subscription: %+v", subscribed.Rejected)
	}
	for _, channel := range wantChannels {
		if !slices.Contains(subscribed.Channels, channel) {
			t.Fatalf("channel %s was not accepted: %+v", channel, subscribed)
		}
	}
	for range len(wantChannels) {
		var snapshot evt.EventsSnapshotMessage
		if err := json.Unmarshal(read(), &snapshot); err != nil || snapshot.Type != "snapshot" {
			t.Fatalf("snapshot: %+v, %v", snapshot, err)
		}
	}
	return read
}

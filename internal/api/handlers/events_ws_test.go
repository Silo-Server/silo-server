package handlers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
)

// ---------------------------------------------------------------------------
// Fake notificationUnreadCounter for snapshot tests.
// ---------------------------------------------------------------------------

type fakeUnreadCounter struct {
	count int
	err   error
}

func (f *fakeUnreadCounter) UnreadCount(_ context.Context, _ int, _ string, _ bool) (int, error) {
	return f.count, f.err
}

// ---------------------------------------------------------------------------
// allowedChannelsForRole tests
// ---------------------------------------------------------------------------

func TestAllowedChannelsForRole_UserContainsNotificationsAndRequests(t *testing.T) {
	channels := allowedChannelsForRole("user")
	if !slices.Contains(channels, evt.ChannelNotifications) {
		t.Errorf("allowedChannelsForRole(user) missing ChannelNotifications; got %v", channels)
	}
	if !slices.Contains(channels, evt.ChannelRequests) {
		t.Errorf("allowedChannelsForRole(user) missing ChannelRequests; got %v", channels)
	}
}

func TestAllowedChannelsForRole_AdminContainsAllUserChannelsPlusExtras(t *testing.T) {
	userChannels := allowedChannelsForRole("user")
	adminChannels := allowedChannelsForRole("admin")

	for _, ch := range userChannels {
		if !slices.Contains(adminChannels, ch) {
			t.Errorf("allowedChannelsForRole(admin) missing user channel %q", ch)
		}
	}

	adminExtras := []evt.EventChannel{
		evt.ChannelJobs,
		evt.ChannelSessions,
		evt.ChannelTasks,
		evt.ChannelScans,
	}
	for _, ch := range adminExtras {
		if !slices.Contains(adminChannels, ch) {
			t.Errorf("allowedChannelsForRole(admin) missing admin-only channel %q", ch)
		}
	}
}

// ---------------------------------------------------------------------------
// snapshotForChannel tests
// ---------------------------------------------------------------------------

func TestSnapshotForChannel_NotificationsNilService(t *testing.T) {
	h := &EventsHandler{notifications: nil}
	r := httptest.NewRequest("GET", "/events/ws", nil)
	claims := &auth.Claims{UserID: 42, Role: "user"}

	data, err := h.snapshotForChannel(r, claims, evt.ChannelNotifications)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		UnreadCount int `json:"unread_count"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v, raw=%s", err, data)
	}
	if got.UnreadCount != 0 {
		t.Errorf("nil service: want unread_count=0, got %d", got.UnreadCount)
	}
}

func TestSnapshotForChannel_NotificationsWithService(t *testing.T) {
	h := &EventsHandler{notifications: &fakeUnreadCounter{count: 7}}
	r := httptest.NewRequest("GET", "/events/ws", nil)
	r.Header.Set("X-Profile-Id", "profile-abc")
	claims := &auth.Claims{UserID: 5, Role: "user"}

	data, err := h.snapshotForChannel(r, claims, evt.ChannelNotifications)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		UnreadCount int `json:"unread_count"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v, raw=%s", err, data)
	}
	if got.UnreadCount != 7 {
		t.Errorf("want unread_count=7, got %d", got.UnreadCount)
	}
}

func TestSnapshotForChannel_RequestsReturnsEmptyObject(t *testing.T) {
	h := &EventsHandler{}
	r := httptest.NewRequest("GET", "/events/ws", nil)
	claims := &auth.Claims{UserID: 1, Role: "user"}

	data, err := h.snapshotForChannel(r, claims, evt.ChannelRequests)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v, raw=%s", err, data)
	}
	if len(got) != 0 {
		t.Errorf("ChannelRequests snapshot should be empty object, got %v", got)
	}
}

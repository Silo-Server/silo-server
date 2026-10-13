package handlers

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
)

// A remote-playback handoff's events connection carries only its bound
// profile's events and none of the account's household channels.
func TestEventsForRemotePlaybackStayOnTheBoundProfile(t *testing.T) {
	handoff := &auth.Claims{UserID: 7, Role: models.RoleUser, SessionID: "s", ProfileID: "kid", SessionPurpose: models.SessionPurposeRemotePlayback}
	account := &auth.Claims{UserID: 7, Role: models.RoleUser, SessionID: "s"}

	if slices.Contains(allowedChannelsForClaims(handoff), evt.ChannelHistoryImport) {
		t.Fatal("a handoff may subscribe to history imports")
	}
	if !slices.Contains(allowedChannelsForClaims(account), evt.ChannelHistoryImport) {
		t.Fatal("an ordinary sign-in lost history imports")
	}

	for _, tc := range []struct {
		name            string
		env             evt.Envelope
		handoff, others bool
	}{
		{"bound profile's watch state", evt.Envelope{Channel: evt.ChannelUserState, UserID: 7, ProfileID: "kid"}, true, true},
		{"another profile's watch state", evt.Envelope{Channel: evt.ChannelUserState, UserID: 7, ProfileID: "parent"}, false, true},
		{"another profile's settings", evt.Envelope{Channel: evt.ChannelUserSettings, UserID: 7, ProfileID: "parent"}, false, true},
		{"account-wide settings", evt.Envelope{Channel: evt.ChannelUserSettings, UserID: 7}, true, true},
		{"catalog", evt.Envelope{Channel: evt.ChannelCatalog}, true, true},
	} {
		if got := allowsEventForClaims(handoff, "kid", tc.env); got != tc.handoff {
			t.Errorf("%s: handoff receives = %v, want %v", tc.name, got, tc.handoff)
		}
		if got := allowsEventForClaims(account, "", tc.env); got != tc.others {
			t.Errorf("%s: ordinary sign-in receives = %v, want %v", tc.name, got, tc.others)
		}
	}
}

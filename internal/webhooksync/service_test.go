package webhooksync

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/historyimport"
)

func TestShouldApplyPlexWebhookEvent(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"media.scrobble": true,
		"media.stop":     true,
		"media.pause":    true,
		"media.play":     false,
		"media.resume":   false,
		"library.new":    false,
		"":               false,
	}

	for event, want := range cases {
		event := event
		want := want
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			if got := shouldApplyPlexWebhookEvent(event); got != want {
				t.Fatalf("shouldApplyPlexWebhookEvent(%q) = %v, want %v", event, got, want)
			}
		})
	}
}

func TestBuildWebhookURL(t *testing.T) {
	t.Parallel()

	if got := buildWebhookURL("", "abc123"); got != "/api/v1/webhook-sync/webhooks/abc123" {
		t.Fatalf("unexpected relative webhook URL: %q", got)
	}
	if got := buildWebhookURL("https://example.com/", "abc123"); got != "https://example.com/api/v1/webhook-sync/webhooks/abc123" {
		t.Fatalf("unexpected absolute webhook URL: %q", got)
	}
}

func TestFilterDiscoveredAccounts(t *testing.T) {
	t.Parallel()

	accounts := []historyimport.ExternalUser{
		{ID: "1", Name: "Owner", Home: true},
		{ID: "2", Name: "Kid", Restricted: true},
		{ID: "5", Name: "", Home: true},
		{ID: "3", Name: "Friend"},
		{ID: "4", Name: "Mapped Friend"},
	}
	mappings := []ProfileMapping{
		{ExternalUserID: "4"},
	}

	filtered := filterDiscoveredAccounts(accounts, mappings)
	if len(filtered) != 3 {
		t.Fatalf("expected 3 filtered accounts, got %d", len(filtered))
	}
	if filtered[0].ID != "1" || filtered[1].ID != "2" || filtered[2].ID != "4" {
		t.Fatalf("unexpected filtered accounts: %#v", filtered)
	}
}

func TestFilterDiscoveredAccountsFallsBackWhenFlagsMissing(t *testing.T) {
	t.Parallel()

	accounts := []historyimport.ExternalUser{
		{ID: "10", Name: "Unflagged One"},
		{ID: "12", Name: ""},
		{ID: "11", Name: "Unflagged Two"},
	}

	filtered := filterDiscoveredAccounts(accounts, nil)
	if len(filtered) != 2 {
		t.Fatalf("expected only named accounts when no household flags exist, got %d", len(filtered))
	}
	if filtered[0].ID != "10" || filtered[1].ID != "11" {
		t.Fatalf("unexpected fallback accounts: %#v", filtered)
	}
}

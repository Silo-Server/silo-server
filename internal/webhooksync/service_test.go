package webhooksync

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/historyimport"
)

func TestPlexWebhookAction(t *testing.T) {
	t.Parallel()

	cases := []struct {
		event         string
		wantAction    string
		wantCompleted bool
		wantOK        bool
	}{
		{event: "media.scrobble", wantAction: ActionImportProgress, wantCompleted: true, wantOK: true},
		{event: "media.stop", wantAction: ActionImportProgress, wantOK: true},
		{event: "media.pause", wantAction: ActionImportProgress, wantOK: true},
		{event: "media.play", wantAction: ActionPlaybackStarted, wantOK: true},
		{event: "media.resume"},
		{event: "media.rate"},
		{event: "library.new"},
		{event: ""},
	}

	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			t.Parallel()
			action, completed, ok := plexWebhookAction(tc.event)
			if action != tc.wantAction || completed != tc.wantCompleted || ok != tc.wantOK {
				t.Fatalf("plexWebhookAction(%q) = (%q, %v, %v), want (%q, %v, %v)", tc.event, action, completed, ok, tc.wantAction, tc.wantCompleted, tc.wantOK)
			}
		})
	}
}

func TestBuildWebhookURL(t *testing.T) {
	t.Parallel()

	if got := buildWebhookURL("", "abc123"); got != "/api/v2/webhook-sync/webhooks/abc123" {
		t.Fatalf("unexpected relative webhook URL: %q", got)
	}
	if got := buildWebhookURL("https://example.com/", "abc123"); got != "https://example.com/api/v2/webhook-sync/webhooks/abc123" {
		t.Fatalf("unexpected absolute webhook URL: %q", got)
	}
}

func TestResolveWebhookProfileRequiresExplicitMapping(t *testing.T) {
	t.Parallel()

	linkedProfileID := "linked-profile"

	cases := []struct {
		name    string
		mapping *ProfileMapping
		want    string
		wantOK  bool
	}{
		{
			name:   "missing mapping is skipped",
			wantOK: false,
		},
		{
			name:    "unmapped external user is skipped",
			mapping: &ProfileMapping{},
			wantOK:  false,
		},
		{
			name:    "empty profile mapping is skipped",
			mapping: &ProfileMapping{SiloProfileID: ptrString("")},
			wantOK:  false,
		},
		{
			name:    "explicit profile mapping is used",
			mapping: &ProfileMapping{SiloProfileID: &linkedProfileID},
			want:    linkedProfileID,
			wantOK:  true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := resolveWebhookProfileID(tc.mapping)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("resolveWebhookProfileID() = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
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

func ptrString(value string) *string {
	return &value
}

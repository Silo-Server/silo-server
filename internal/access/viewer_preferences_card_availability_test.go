package access

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type serverDefaults map[string]string

func (d serverDefaults) Get(_ context.Context, key string) (string, error) { return d[key], nil }

// The server counts seasons and episodes for a card only when the profile
// draws the availability badge, so this must agree with how clients resolve
// the same two settings: the profile's own choice wins either way, and a
// profile that has not chosen follows the server-wide default.
func TestViewerPreferencesCardAvailability(t *testing.T) {
	const (
		badgeOn  = `{"version":2,"preset":"classic","order":[],"items":{"availability":{"enabled":true,"position":"top-left"}}}`
		badgeOff = `{"version":2,"preset":"classic","order":[],"items":{"availability":{"enabled":false,"position":"top-left"}}}`
		noBadge  = `{"version":2,"preset":"classic","order":[],"items":{"resolution":{"enabled":true,"position":"top-left"}}}`
		flatOn   = `{"availability":{"enabled":true,"position":"top-left"}}`
	)
	cases := []struct {
		name     string
		overlays string // the profile's ui.card_overlays, "" when not stored
		enabled  string // the profile's ui.card_overlays_enabled, "" when not stored
		defaults serverDefaults
		want     bool
	}{
		{name: "nothing stored anywhere"},
		{name: "profile turns the badge on", overlays: badgeOn, want: true},
		{name: "profile document without the badge", overlays: noBadge},
		{name: "profile turned badges off", overlays: badgeOn, enabled: "false"},
		{name: "server turned badges off, profile has not chosen", overlays: badgeOn,
			defaults: serverDefaults{OverlaysEnabledSettingKey: "false"}},
		{name: "profile turned badges on over the server default", overlays: badgeOn, enabled: "true",
			defaults: serverDefaults{OverlaysEnabledSettingKey: "false"}, want: true},
		{name: "server default document turns the badge on",
			defaults: serverDefaults{DefaultCardOverlaysSettingKey: badgeOn}, want: true},
		{name: "profile document wins over the server default", overlays: badgeOff,
			defaults: serverDefaults{DefaultCardOverlaysSettingKey: badgeOn}},
		{name: "first-version server default document",
			defaults: serverDefaults{DefaultCardOverlaysSettingKey: flatOn}, want: true},
		{name: "unreadable server default document",
			defaults: serverDefaults{DefaultCardOverlaysSettingKey: "{"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rows []userstore.SettingValue
			for key, value := range map[string]string{
				settingskeys.UiCardOverlays:        tc.overlays,
				settingskeys.UiCardOverlaysEnabled: tc.enabled,
			} {
				if value == "" {
					continue
				}
				rows = append(rows, userstore.SettingValue{
					SettingIdentity: userstore.SettingIdentity{Key: key, Scope: settingscontract.ScopeProfile, ProfileID: "profile"},
					Value:           json.RawMessage(value),
				})
			}
			got, err := ResolveViewerPreferencesStrict(t.Context(), ViewerPreferenceRows{Rows: rows}, tc.defaults, "profile")
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got.CardAvailability != tc.want {
				t.Fatalf("CardAvailability = %v, want %v", got.CardAvailability, tc.want)
			}
		})
	}
}

package catalog

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// playback.prefer_embedded_subtitles reaches item and watch detail through the
// same resolved block as the other subtitle preferences, so the flag and the
// "was it actually stored" companion have to travel together: every client
// reads the pair, and an unset preference must stay distinguishable from an
// explicit false.
func TestEffectiveSubtitleDefaults_ResolvesPreferEmbeddedSubtitles(t *testing.T) {
	for _, tc := range []struct {
		name       string
		value      string
		wantValue  bool
		wantStored bool
	}{
		{name: "stored true", value: "true", wantValue: true, wantStored: true},
		{name: "stored false", value: "false", wantValue: false, wantStored: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newDetailTestStore(t)
			if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
				Key:       settingskeys.PlaybackPreferEmbeddedSubtitles,
				Scope:     settingscontract.ScopeProfile,
				ProfileID: "profile-1",
			}, json.RawMessage(tc.value)); err != nil {
				t.Fatalf("UpsertSettingValue: %v", err)
			}

			service := &DetailService{}
			service.SetUserStoreProvider(testDetailUserStoreProvider{store: store})

			defaults := service.effectiveSubtitleDefaults(context.Background(), AccessFilter{
				UserID:    1,
				ProfileID: "profile-1",
			}, "", nil)

			if !defaults.HasPreferEmbedded {
				t.Fatalf("HasPreferEmbedded = false, want true for a stored row")
			}
			if defaults.PreferEmbedded != tc.wantValue {
				t.Fatalf("PreferEmbedded = %v, want %v", defaults.PreferEmbedded, tc.wantValue)
			}
		})
	}
}

func TestEffectiveSubtitleDefaults_LeavesPreferEmbeddedUnstoredWhenAbsent(t *testing.T) {
	store := newDetailTestStore(t)

	service := &DetailService{}
	service.SetUserStoreProvider(testDetailUserStoreProvider{store: store})

	defaults := service.effectiveSubtitleDefaults(context.Background(), AccessFilter{
		UserID:    1,
		ProfileID: "profile-1",
	}, "", nil)

	// The contract default is false, but a profile that never chose must not be
	// reported as having chosen it: the detail payload omits the field so a
	// client keeps its own fallback.
	if defaults.HasPreferEmbedded {
		t.Fatal("HasPreferEmbedded = true for a profile with no stored preference")
	}
}

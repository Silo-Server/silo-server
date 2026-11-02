package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func setHomeHideWatchedItems(t *testing.T, store userstore.UserStore, profileID string, hide bool) {
	t.Helper()
	tx, ok := store.(userstore.PreferenceSettingsTransactioner)
	if !ok {
		t.Fatal("test store does not support settings transactions")
	}
	value, _ := json.Marshal(hide)
	if err := tx.WithPreferenceSettingsTransaction(t.Context(), func(writer userstore.PreferenceSettingsWriter) error {
		_, err := writer.UpsertSettingValue(t.Context(), userstore.SettingIdentity{Key: settingskeys.HomeHideWatchedItems, Scope: settingscontract.ScopeProfile, ProfileID: profileID}, value)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// Jellyfin's HidePlayedInLatest is Silo's home.hide_watched_items, so the
// toggle in either client hides watched items from both.
func TestHidePlayedInLatestIsTheHomeHideWatchedSetting(t *testing.T) {
	store := newJellycompatUserStore(t)
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil).WithUserStore(compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	update := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.HandleUpdateConfiguration(rec, viewerRequest("POST", "/", body, "", "", session))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("update %s returned %d %s", body, rec.Code, rec.Body.String())
		}
	}
	source := func() settingscontract.Scope {
		t.Helper()
		contract, err := settingscontract.Load()
		if err != nil {
			t.Fatal(err)
		}
		values, err := settingsresolve.New(contract).Resolve(t.Context(), store, settingsresolve.Context{ProfileID: session.ProfileID}, []string{settingskeys.HomeHideWatchedItems}, nil)
		if err != nil || len(values) != 1 {
			t.Fatalf("resolve home.hide_watched_items: %v (%d values)", err, len(values))
		}
		return values[0].Source
	}

	// Jellyfin Web saves the whole configuration from every settings page.
	// Echoing the default back must not pin it as a profile value.
	update(`{"EnableNextEpisodeAutoPlay":true,"HidePlayedInLatest":false}`)
	if got := source(); got != settingscontract.ScopeDefault {
		t.Fatalf("unchanged HidePlayedInLatest wrote a %s value", got)
	}

	update(`{"HidePlayedInLatest":true}`)
	if !sections.HideWatchedItemsFromHome(t.Context(), store, session.ProfileID) {
		t.Fatal("HidePlayedInLatest=true did not turn on home.hide_watched_items")
	}

	// A change made in Silo reads back to Jellyfin clients.
	setHomeHideWatchedItems(t, store, session.ProfileID, false)
	dto, err := h.resolvedUserDTO(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	if dto.Configuration.HidePlayedInLatest {
		t.Fatal("HidePlayedInLatest still true after home.hide_watched_items was turned off")
	}
}

// Jellyfin Web's home Latest rows send no IsPlayed and rely on the server to
// apply HidePlayedInLatest. An explicit IsPlayed still wins.
func TestLatestHonorsHidePlayedInLatest(t *testing.T) {
	cases := []struct {
		name  string
		query string
		hide  bool
		want  string
	}{
		{"setting off", "", false, ""},
		{"setting on", "", true, "false"},
		{"explicit IsPlayed wins", "&IsPlayed=true", true, "true"},
		{"explicit unplayed filter", "&Filters=IsUnplayed", false, "false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newJellycompatUserStore(t)
			session := &Session{Token: "token", ProfileID: "profile-1", StreamAppUserID: 1}
			if tc.hide {
				setHomeHideWatchedItems(t, store, session.ProfileID, true)
			}
			codec := NewResourceIDCodec()
			content := &rootRoutingContent{}
			h := &ItemsHandler{content: content, userData: &mockUserDataService{}, codec: codec, mapper: newMapper(codec, &config.Config{}), images: NewImageCache(time.Hour, time.Now), storeProvider: compatTestUserStoreProvider{store: store}}

			req := httptest.NewRequest(http.MethodGet, "/Items/Latest?Limit=16"+tc.query, nil)
			req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, session))
			rec := httptest.NewRecorder()
			h.HandleLatest(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("Latest = %d %s", rec.Code, rec.Body.String())
			}
			if !content.browsed {
				t.Fatal("Latest did not browse")
			}
			if got := content.browseParams.Get("is_played"); got != tc.want {
				t.Fatalf("is_played = %q, want %q", got, tc.want)
			}
		})
	}
}

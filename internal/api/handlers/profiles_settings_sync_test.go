package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// These tests pin the seam the settings cutover opened: every server-side
// reader of the profile preferences resolves them from user_setting_values,
// while the shipped clients still write them through POST/PUT /profiles. A
// profile write that does not land in the canonical store never takes effect
// — the stale backfilled row (or the contract default) wins forever.

// updateProfileVia sends PUT /profiles/{id} as profile-1's own session.
func updateProfileVia(t *testing.T, handler *ProfileHandler, profileID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := newAuthorizedProfileRequestWithRole(
		http.MethodPut, "/profiles/"+profileID, body, "user", profileID)
	req = withProfileRouteParam(req, "id", profileID)
	rr := httptest.NewRecorder()
	handler.HandleUpdateProfile(rr, req)
	return rr
}

func storedProfileSetting(t *testing.T, store userstore.UserStore, key, profileID string) *userstore.SettingValue {
	t.Helper()
	value, err := store.GetSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       key,
		Scope:     settingscontract.ScopeProfile,
		ProfileID: profileID,
	})
	if err != nil {
		t.Fatalf("reading canonical %s: %v", key, err)
	}
	return value
}

// TestUpdateProfileSyncsCanonicalMetadataLanguage replays the cutover bug: a
// backfilled canonical row said "fr", the user changes the metadata language
// to "de" through the legacy profile endpoint, and access-scope resolution
// must see "de" — not the stale "fr" the one-time backfill left behind.
func TestUpdateProfileSyncsCanonicalMetadataLanguage(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	// The one-time backfill stored the pre-cutover column value.
	if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       settingskeys.CatalogMetadataLanguage,
		Scope:     settingscontract.ScopeProfile,
		ProfileID: "profile-1",
	}, json.RawMessage(`"fr"`)); err != nil {
		t.Fatalf("seeding backfilled row: %v", err)
	}

	rr := updateProfileVia(t, handler, "profile-1", `{"preferred_metadata_language":"de"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rr.Code, rr.Body.String())
	}

	// The SQLite per-user schema never grew a preferred_metadata_language
	// column, so the canonical row is the only storage this write has — which
	// is exactly why the sync must exist.
	if got := access.PreferredMetadataLanguage(context.Background(), store, "profile-1"); got != "de" {
		t.Errorf("canonical metadata language = %q after profile update, want %q", got, "de")
	}
}

// TestUpdateProfileSyncsCanonicalAudioLanguage is the playback-start half: a
// profile that never had a backfilled row chooses a spoken language, and the
// canonical store — which handleStartPlaybackLegacy resolves — must carry it.
func TestUpdateProfileSyncsCanonicalAudioLanguage(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	rr := updateProfileVia(t, handler, "profile-1", `{"language":"de"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rr.Code, rr.Body.String())
	}

	value := storedProfileSetting(t, store, settingskeys.PlaybackAudioLanguage, "profile-1")
	if value == nil {
		t.Fatal("no canonical playback.audio_language row after the profile update")
	}
	if string(value.Value) != `"de"` {
		t.Errorf("canonical audio language = %s, want \"de\"", value.Value)
	}
}

// TestUpdateProfileClearingLanguageClearsCanonicalRow: the legacy empty
// string means "no preference", spelled canonically as no row at all.
func TestUpdateProfileClearingLanguageClearsCanonicalRow(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	if rr := updateProfileVia(t, handler, "profile-1",
		`{"preferred_metadata_language":"fr"}`); rr.Code != http.StatusOK {
		t.Fatalf("seeding PUT = %d: %s", rr.Code, rr.Body.String())
	}
	if rr := updateProfileVia(t, handler, "profile-1",
		`{"preferred_metadata_language":""}`); rr.Code != http.StatusOK {
		t.Fatalf("clearing PUT = %d: %s", rr.Code, rr.Body.String())
	}

	if value := storedProfileSetting(t, store, settingskeys.CatalogMetadataLanguage, "profile-1"); value != nil {
		t.Errorf("canonical row = %s after clearing, want none", value.Value)
	}
	if got := access.PreferredMetadataLanguage(context.Background(), store, "profile-1"); got != "" {
		t.Errorf("resolved metadata language = %q after clearing, want \"\"", got)
	}
}

// TestUpdateProfileSyncsSubtitlePreferences covers the triple the player's
// subtitle picker still saves through PUT /profiles, resolved canonically by
// catalog detail since the earlier cutover.
func TestUpdateProfileSyncsSubtitlePreferences(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	rr := updateProfileVia(t, handler, "profile-1",
		`{"subtitle_language":"ja","subtitle_mode":"always","show_forced_subtitles":false}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rr.Code, rr.Body.String())
	}

	for key, want := range map[string]string{
		settingskeys.PlaybackSubtitleLanguage:    `"ja"`,
		settingskeys.PlaybackSubtitleMode:        `"always"`,
		settingskeys.PlaybackShowForcedSubtitles: `false`,
	} {
		value := storedProfileSetting(t, store, key, "profile-1")
		if value == nil {
			t.Errorf("no canonical %s row after the profile update", key)
			continue
		}
		if string(value.Value) != want {
			t.Errorf("canonical %s = %s, want %s", key, value.Value, want)
		}
	}
}

// TestUpdateProfileRejectsInvalidLanguageBeforeWriting: a value the canonical
// endpoint would refuse must fail the request as a no-op instead of leaving
// the column and the canonical store disagreeing.
func TestUpdateProfileRejectsInvalidLanguageBeforeWriting(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	rr := updateProfileVia(t, handler, "profile-1", `{"language":"!!!"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT of an invalid tag = %d, want 400: %s", rr.Code, rr.Body.String())
	}

	profile, err := store.GetProfile(context.Background(), "profile-1")
	if err != nil || profile == nil {
		t.Fatalf("reading profile: %v", err)
	}
	if profile.Language != "" {
		t.Errorf("column = %q after a rejected write, want untouched", profile.Language)
	}
	if value := storedProfileSetting(t, store, settingskeys.PlaybackAudioLanguage, "profile-1"); value != nil {
		t.Errorf("canonical row = %s after a rejected write, want none", value.Value)
	}
}

// TestCreateProfileSyncsCanonicalLanguages: a profile born with preferences
// must be resolvable canonically from its first request.
func TestCreateProfileSyncsCanonicalLanguages(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(http.MethodPost, "/profiles",
		`{"name":"Kids","language":"de","preferred_metadata_language":"fr"}`,
		"user", "profile-1")
	rr := httptest.NewRecorder()
	handler.HandleCreateProfile(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rr.Code, rr.Body.String())
	}
	var created profileResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}

	audio := storedProfileSetting(t, store, settingskeys.PlaybackAudioLanguage, created.ID)
	if audio == nil || string(audio.Value) != `"de"` {
		t.Errorf("canonical audio language after create = %v, want \"de\"", audio)
	}
	if got := access.PreferredMetadataLanguage(context.Background(), store, created.ID); got != "fr" {
		t.Errorf("canonical metadata language after create = %q, want %q", got, "fr")
	}
}

// TestUpdateProfilePublishesUserSettingsEvents: the synced rows change what
// other clients resolve, so they get the same refresh signal a
// /settings/values write publishes.
func TestUpdateProfilePublishesUserSettingsEvents(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.EventsHub = evt.NewHub("test", &cache.NoopEventBus{})
	events, unsubscribe := handler.EventsHub.Subscribe()
	defer unsubscribe()

	rr := updateProfileVia(t, handler, "profile-1", `{"preferred_metadata_language":"de"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rr.Code, rr.Body.String())
	}

	env := receiveUserSettingsEvent(t, events)
	assertUserSettingsEnvelope(t, env, settingskeys.CatalogMetadataLanguage, "profile")

	// A field the request did not carry publishes nothing.
	select {
	case extra := <-events:
		t.Errorf("unexpected extra event for %s", extra.Data)
	default:
	}
}

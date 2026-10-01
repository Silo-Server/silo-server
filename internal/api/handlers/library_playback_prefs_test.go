package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func routeLibraryPlaybackPref(
	t *testing.T,
	h *LibraryPlaybackPrefHandler,
	method string,
	libraryID string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	req := valuesRequest(method, "/library-playback-prefs/"+libraryID, body)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("library_id", libraryID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()
	if method == http.MethodPut {
		h.HandleSetLibraryPlaybackPref(rec, req)
	} else {
		h.HandleDeleteLibraryPlaybackPref(rec, req)
	}
	return rec
}

func TestLegacyLibraryPlaybackWritesStayInCanonicalSync(t *testing.T) {
	_, store := newValuesTestHandler(t)
	handler := NewLibraryPlaybackPrefHandler(testUserStoreProvider{store: store})

	rec := routeLibraryPlaybackPref(t, handler, http.MethodPut, "7", []byte(`{
		"audio_language":"ja",
		"subtitle_language":"de",
		"subtitle_mode":"always",
		"show_forced_subtitles":false
	}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}

	want := map[string]string{
		settingskeys.PlaybackAudioLanguage:       `"ja"`,
		settingskeys.PlaybackSubtitleLanguage:    `"de"`,
		settingskeys.PlaybackSubtitleMode:        `"always"`,
		settingskeys.PlaybackShowForcedSubtitles: `false`,
	}
	for key, expected := range want {
		value, err := store.GetSettingValue(context.Background(), userstore.SettingIdentity{
			Key: key, Scope: settingscontract.ScopeProfileLibrary,
			ProfileID: "profile-1", LibraryID: 7,
		})
		if err != nil || value == nil {
			t.Fatalf("reading canonical %s: value=%+v err=%v", key, value, err)
		}
		if string(value.Value) != expected {
			t.Errorf("%s = %s, want %s", key, value.Value, expected)
		}
	}

	// The legacy PUT replaces the combined row. Omitting three fields clears
	// their canonical overrides rather than leaving the backfilled values live.
	rec = routeLibraryPlaybackPref(t, handler, http.MethodPut, "7", []byte(`{"audio_language":"fr"}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("replacement PUT = %d: %s", rec.Code, rec.Body.String())
	}
	for _, key := range []string{
		settingskeys.PlaybackSubtitleLanguage,
		settingskeys.PlaybackSubtitleMode,
		settingskeys.PlaybackShowForcedSubtitles,
	} {
		value, err := store.GetSettingValue(context.Background(), userstore.SettingIdentity{
			Key: key, Scope: settingscontract.ScopeProfileLibrary,
			ProfileID: "profile-1", LibraryID: 7,
		})
		if err != nil || value != nil {
			t.Errorf("omitted %s was not cleared: value=%+v err=%v", key, value, err)
		}
	}

	rec = routeLibraryPlaybackPref(t, handler, http.MethodDelete, "7", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	value, err := store.GetSettingValue(context.Background(), userstore.SettingIdentity{
		Key: settingskeys.PlaybackAudioLanguage, Scope: settingscontract.ScopeProfileLibrary,
		ProfileID: "profile-1", LibraryID: 7,
	})
	if err != nil || value != nil {
		t.Fatalf("DELETE left canonical audio value=%+v err=%v", value, err)
	}
}

// canonicalLibraryValue is the canonical profile_library row for key, or nil.
func canonicalLibraryValue(t *testing.T, store userstore.UserStore, profileID string, libraryID int, key string) *string {
	t.Helper()
	value, err := store.GetSettingValue(context.Background(), userstore.SettingIdentity{
		Key: key, Scope: settingscontract.ScopeProfileLibrary, ProfileID: profileID, LibraryID: libraryID,
	})
	if err != nil {
		t.Fatalf("reading canonical %s: %v", key, err)
	}
	if value == nil {
		return nil
	}
	s := string(value.Value)
	return &s
}

// The v2 partial update merges onto the canonical rows, not the legacy
// composite row: PUT /settings/values (and the web library editor) write the
// canonical row without mirroring it into the legacy row, so a patch of one
// member must keep the newer canonical value of another rather than copy the
// obsolete legacy one back over it.
func TestPatchLibraryPlaybackPreferenceMergesOntoCanonicalRows(t *testing.T) {
	_, store := newValuesTestHandler(t)
	handler := NewLibraryPlaybackPrefHandler(testUserStoreProvider{store: store})
	ctx := context.Background()
	const profileID = "profile-1"

	rec := routeLibraryPlaybackPref(t, handler, http.MethodPut, "7", []byte(`{"audio_language":"ja","subtitle_mode":"always"}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	// A canonical-only write moves subtitle_language to "de" while the
	// legacy row still says nothing about it.
	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key: settingskeys.PlaybackSubtitleLanguage, Scope: settingscontract.ScopeProfileLibrary,
		ProfileID: profileID, LibraryID: 7,
	}, []byte(`"de"`)); err != nil {
		t.Fatalf("canonical write: %v", err)
	}

	if err := handler.PatchLibraryPlaybackPreference(ctx, 1, profileID, 7, LibraryPlaybackPrefPatch{
		AudioLanguage: PrefPatch[string]{Present: true, Value: ptr("fr")},
	}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	if got := canonicalLibraryValue(t, store, profileID, 7, settingskeys.PlaybackAudioLanguage); got == nil || *got != `"fr"` {
		t.Fatalf("audio_language = %v, want \"fr\"", got)
	}
	if got := canonicalLibraryValue(t, store, profileID, 7, settingskeys.PlaybackSubtitleLanguage); got == nil || *got != `"de"` {
		t.Fatalf("subtitle_language reverted to %v, want the newer canonical \"de\"", got)
	}
	if got := canonicalLibraryValue(t, store, profileID, 7, settingskeys.PlaybackSubtitleMode); got == nil || *got != `"always"` {
		t.Fatalf("subtitle_mode = %v, want \"always\"", got)
	}
	// The legacy row is rewritten from the merged set, so it now carries the
	// canonical subtitle_language too.
	row, err := store.GetLibraryPlaybackPreference(ctx, profileID, 7)
	if err != nil || row == nil {
		t.Fatalf("legacy row: %+v err=%v", row, err)
	}
	if row.AudioLanguage != "fr" || !row.HasSubtitleLanguage || row.SubtitleLanguage != "de" || row.SubtitleMode != "always" || row.HasShowForcedSubtitles {
		t.Fatalf("legacy row = %+v", row)
	}

	// Clearing (nil Value) removes only that override; clearing the last
	// override removes the row.
	if err := handler.PatchLibraryPlaybackPreference(ctx, 1, profileID, 7, LibraryPlaybackPrefPatch{
		AudioLanguage: PrefPatch[string]{Present: true},
		SubtitleMode:  PrefPatch[string]{Present: true},
	}); err != nil {
		t.Fatalf("clearing patch: %v", err)
	}
	if got := canonicalLibraryValue(t, store, profileID, 7, settingskeys.PlaybackAudioLanguage); got != nil {
		t.Fatalf("audio_language not cleared: %s", *got)
	}
	if row, _ = store.GetLibraryPlaybackPreference(ctx, profileID, 7); row == nil || !row.HasSubtitleLanguage || row.HasAudioLanguage || row.HasSubtitleMode {
		t.Fatalf("legacy row after clear = %+v", row)
	}
	if err := handler.PatchLibraryPlaybackPreference(ctx, 1, profileID, 7, LibraryPlaybackPrefPatch{
		SubtitleLanguage: PrefPatch[string]{Present: true},
	}); err != nil {
		t.Fatalf("final clear: %v", err)
	}
	if row, _ = store.GetLibraryPlaybackPreference(ctx, profileID, 7); row != nil {
		t.Fatalf("row survived clearing every override: %+v", row)
	}
	if got := canonicalLibraryValue(t, store, profileID, 7, settingskeys.PlaybackSubtitleLanguage); got != nil {
		t.Fatalf("subtitle_language survived: %s", *got)
	}

	// A rejected member never reaches the store.
	err = handler.PatchLibraryPlaybackPreference(ctx, 1, profileID, 7, LibraryPlaybackPrefPatch{
		SubtitleMode: PrefPatch[string]{Present: true, Value: ptr("sometimes")},
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Field != "subtitle_mode" {
		t.Fatalf("invalid subtitle_mode: %v", err)
	}
	// The v1 PUT is unchanged: it still replaces the whole row.
	rec = routeLibraryPlaybackPref(t, handler, http.MethodPut, "7", []byte(`{"audio_language":"ja","subtitle_language":"de"}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	rec = routeLibraryPlaybackPref(t, handler, http.MethodPut, "7", []byte(`{"audio_language":"fr"}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	if got := canonicalLibraryValue(t, store, profileID, 7, settingskeys.PlaybackSubtitleLanguage); got != nil {
		t.Fatalf("v1 PUT kept the omitted member: %s", *got)
	}
}

// The v2 listing is assembled from the canonical profile_library rows: a
// row written through the settings writer shows up with the legacy table
// empty, and a legacy-only row (written before the sync existed) does not.
func TestListLibraryPlaybackPreferencesCanonicalReadsSettingRows(t *testing.T) {
	_, store := newValuesTestHandler(t)
	handler := NewLibraryPlaybackPrefHandler(testUserStoreProvider{store: store})
	ctx := context.Background()
	const profileID = "profile-1"

	canonical := func(libraryID int, key string, value string) {
		t.Helper()
		if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
			Key: key, Scope: settingscontract.ScopeProfileLibrary, ProfileID: profileID, LibraryID: libraryID,
		}, []byte(value)); err != nil {
			t.Fatalf("canonical write %s: %v", key, err)
		}
	}
	canonical(9, settingskeys.PlaybackSubtitleLanguage, `"de"`)
	canonical(9, settingskeys.PlaybackShowForcedSubtitles, `true`)
	canonical(4, settingskeys.PlaybackSubtitleMode, `"off"`)
	// Another profile's row and a profile_library key outside the four are
	// not part of the preference.
	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key: settingskeys.PlaybackAudioLanguage, Scope: settingscontract.ScopeProfileLibrary, ProfileID: "profile-2", LibraryID: 9,
	}, []byte(`"fr"`)); err != nil {
		t.Fatalf("other profile write: %v", err)
	}
	// A legacy-only row: written straight to the composite table, as data
	// predating the canonical sync would be.
	if err := store.UpsertLibraryPlaybackPreference(ctx, userstore.LibraryPlaybackPreference{
		ProfileID: profileID, LibraryID: 2, AudioLanguage: "ja", HasAudioLanguage: true,
	}); err != nil {
		t.Fatalf("legacy write: %v", err)
	}

	got, err := handler.ListLibraryPlaybackPreferencesCanonical(ctx, 1, profileID)
	if err != nil {
		t.Fatalf("canonical list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("canonical list = %+v, want libraries 4 and 9", got)
	}
	if got[0].LibraryID != 4 || !got[0].HasSubtitleMode || got[0].SubtitleMode != "off" ||
		got[0].HasAudioLanguage || got[0].HasSubtitleLanguage || got[0].HasShowForcedSubtitles {
		t.Fatalf("library 4 = %+v", got[0])
	}
	if got[1].LibraryID != 9 || got[1].ProfileID != profileID || !got[1].HasSubtitleLanguage || got[1].SubtitleLanguage != "de" ||
		!got[1].HasShowForcedSubtitles || !got[1].ShowForcedSubtitles || got[1].HasAudioLanguage || got[1].HasSubtitleMode {
		t.Fatalf("library 9 = %+v", got[1])
	}
	for _, pref := range got {
		if pref.UpdatedAt == "" {
			t.Fatalf("library %d has no updated_at", pref.LibraryID)
		}
	}

	// The v1 listing still reads the legacy table unchanged: it sees the
	// legacy-only row and nothing of the canonical-only libraries.
	legacy, err := handler.ListLibraryPlaybackPreferences(ctx, 1, profileID)
	if err != nil {
		t.Fatalf("legacy list: %v", err)
	}
	if len(legacy) != 1 || legacy[0].LibraryID != 2 || legacy[0].AudioLanguage != "ja" {
		t.Fatalf("legacy list = %+v", legacy)
	}

	// A canonical clear drops the library from the list once no key is left.
	for _, key := range []string{settingskeys.PlaybackSubtitleLanguage, settingskeys.PlaybackShowForcedSubtitles} {
		if _, err := store.DeleteSettingValue(ctx, userstore.SettingIdentity{
			Key: key, Scope: settingscontract.ScopeProfileLibrary, ProfileID: profileID, LibraryID: 9,
		}); err != nil {
			t.Fatalf("clearing %s: %v", key, err)
		}
	}
	got, err = handler.ListLibraryPlaybackPreferencesCanonical(ctx, 1, profileID)
	if err != nil || len(got) != 1 || got[0].LibraryID != 4 {
		t.Fatalf("after clear = %+v err=%v", got, err)
	}
}

func libraryPrefHandlerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"user_library_playback_preferences", "user_setting_values"} {
		var name *string
		if err := pool.QueryRow(context.Background(),
			`SELECT to_regclass('public.`+table+`')::text`).Scan(&name); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if name == nil || *name == "" {
			t.Skipf("test database has not applied the %s migration", table)
		}
	}
	return pool
}

// Two concurrent patches of different members both land: the seam reads,
// merges and writes inside one transaction behind the per-user advisory
// lock, so neither can overwrite the other with a stale merge.
func TestPatchLibraryPlaybackPreferenceConcurrentPatchesBothLand(t *testing.T) {
	pool := libraryPrefHandlerPool(t)
	ctx := context.Background()
	var userID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role)
		VALUES ('library-pref-patch-fixture', 'library-pref-patch@invalid.test', '', 'user')
		RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatalf("seed fixture user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	const profileID = "profile-patch"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Patch"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	handler := NewLibraryPlaybackPrefHandler(provider)

	for round := 0; round < 20; round++ {
		if err := handler.DeleteLibraryPlaybackPreference(ctx, userID, profileID, 7); err != nil {
			t.Fatalf("reset: %v", err)
		}
		patches := []LibraryPlaybackPrefPatch{
			{AudioLanguage: PrefPatch[string]{Present: true, Value: ptr("ja")}},
			{SubtitleLanguage: PrefPatch[string]{Present: true, Value: ptr("de")}},
			{ShowForcedSubtitles: PrefPatch[bool]{Present: true, Value: ptr(true)}},
		}
		errs := make(chan error, len(patches))
		start := make(chan struct{})
		for _, patch := range patches {
			go func(patch LibraryPlaybackPrefPatch) {
				<-start
				errs <- handler.PatchLibraryPlaybackPreference(ctx, userID, profileID, 7, patch)
			}(patch)
		}
		close(start)
		for range patches {
			if err := <-errs; err != nil {
				t.Fatalf("round %d: patch: %v", round, err)
			}
		}
		row, err := store.GetLibraryPlaybackPreference(ctx, profileID, 7)
		if err != nil || row == nil {
			t.Fatalf("round %d: legacy row: %+v err=%v", round, row, err)
		}
		if row.AudioLanguage != "ja" || row.SubtitleLanguage != "de" || !row.HasShowForcedSubtitles || !row.ShowForcedSubtitles {
			t.Fatalf("round %d: a patch was lost: %+v", round, row)
		}
		for key, want := range map[string]string{
			settingskeys.PlaybackAudioLanguage:       `"ja"`,
			settingskeys.PlaybackSubtitleLanguage:    `"de"`,
			settingskeys.PlaybackShowForcedSubtitles: `true`,
		} {
			if got := canonicalLibraryValue(t, store, profileID, 7, key); got == nil || *got != want {
				t.Fatalf("round %d: canonical %s = %v, want %s", round, key, got, want)
			}
		}
	}
}

func TestPatchLibraryPlaybackPreferenceMirrorsNormalizedValues(t *testing.T) {
	_, store := newValuesTestHandler(t)
	handler := NewLibraryPlaybackPrefHandler(testUserStoreProvider{store: store})
	for _, input := range []string{" EN-us ", "  "} {
		if err := handler.PatchLibraryPlaybackPreference(t.Context(), 1, "profile-1", 7, LibraryPlaybackPrefPatch{
			AudioLanguage:    PrefPatch[string]{Present: true, Value: new(input)},
			SubtitleLanguage: PrefPatch[string]{Present: true, Value: new(input)},
		}); err != nil {
			t.Fatal(err)
		}
		legacy, err := handler.ListLibraryPlaybackPreferences(t.Context(), 1, "profile-1")
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := handler.ListLibraryPlaybackPreferencesCanonical(t.Context(), 1, "profile-1")
		if err != nil {
			t.Fatal(err)
		}
		if input == "  " {
			if len(legacy) != 0 || len(canonical) != 0 {
				t.Fatalf("cleared values: legacy=%+v canonical=%+v", legacy, canonical)
			}
			continue
		}
		if len(legacy) != 1 || len(canonical) != 1 {
			t.Fatalf("rows: legacy=%+v canonical=%+v", legacy, canonical)
		}
		if legacy[0].AudioLanguage != "en-US" || legacy[0].SubtitleLanguage != "en-US" || canonical[0].AudioLanguage != "en-US" || canonical[0].SubtitleLanguage != "en-US" {
			t.Fatalf("normalization diverged: legacy=%+v canonical=%+v", legacy, canonical)
		}
	}
}

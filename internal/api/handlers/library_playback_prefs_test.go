package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
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

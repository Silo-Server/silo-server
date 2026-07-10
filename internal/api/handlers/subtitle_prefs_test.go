package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestSetSubtitlePreferencePreservesOmittedForcedOverride(t *testing.T) {
	store := newPlaybackTestStore(t)
	if err := store.SetSubtitlePreference(context.Background(), userstore.SubtitlePreference{
		ProfileID:              "profile-1",
		SeriesID:               "series-1",
		SubtitleLanguage:       "en",
		SubtitleTrackIndex:     1,
		SubtitleMode:           "always",
		ShowForcedSubtitles:    false,
		HasShowForcedSubtitles: true,
	}); err != nil {
		t.Fatalf("seed subtitle preference: %v", err)
	}

	handler := NewSubtitlePrefHandler(testUserStoreProvider{store: store})
	req := httptest.NewRequest(http.MethodPut, "/subtitle-prefs/series-1", strings.NewReader(`{
		"subtitle_language":"ja",
		"subtitle_track_index":2,
		"subtitle_mode":"always"
	}`))
	req = req.WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "series_id", "series-1")
	rec := httptest.NewRecorder()

	handler.HandleSetSubtitlePref(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	pref, err := store.GetSubtitlePreference(context.Background(), "profile-1", "series-1")
	if err != nil {
		t.Fatalf("get subtitle preference: %v", err)
	}
	if pref == nil || !pref.HasShowForcedSubtitles || pref.ShowForcedSubtitles {
		t.Fatalf("forced-subtitle override was not preserved: %+v", pref)
	}
	if pref.SubtitleLanguage != "ja" || pref.SubtitleTrackIndex != 2 {
		t.Fatalf("track selection was not updated: %+v", pref)
	}
}

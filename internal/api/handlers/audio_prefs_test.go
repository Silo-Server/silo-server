package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func routeAudioPref(
	t *testing.T,
	h *AudioPrefHandler,
	method string,
	seriesID string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	req := valuesRequest(method, "/audio-prefs/"+seriesID, body)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("series_id", seriesID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()
	if method == http.MethodPut {
		h.HandleSetAudioPref(rec, req)
	} else {
		h.HandleDeleteAudioPref(rec, req)
	}
	return rec
}

func TestLegacyAudioPreferenceKeepsTrackIdentityAndSyncsCanonicalLanguage(t *testing.T) {
	_, store := newValuesTestHandler(t)
	handler := NewAudioPrefHandler(testUserStoreProvider{store: store})

	rec := routeAudioPref(t, handler, http.MethodPut, "series-1", []byte(`{
		"audio_track_index":2,
		"audio_language":"ja",
		"track_signature":{"language":"ja","codec":"aac","channels":2}
	}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	legacy, err := store.GetAudioPreference(context.Background(), "profile-1", "series-1")
	if err != nil || legacy == nil {
		t.Fatalf("reading specialized preference: value=%+v err=%v", legacy, err)
	}
	if legacy.AudioTrackIndex != 2 || legacy.TrackSignature == nil {
		t.Errorf("specialized track identity was lost: %+v", legacy)
	}
	canonicalID := userstore.SettingIdentity{
		Key: settingskeys.PlaybackAudioLanguage, Scope: settingscontract.ScopeProfileSeries,
		ProfileID: "profile-1", SeriesID: "series-1",
	}
	canonical, err := store.GetSettingValue(context.Background(), canonicalID)
	if err != nil || canonical == nil || string(canonical.Value) != `"ja"` {
		t.Fatalf("canonical language = %+v err=%v, want ja", canonical, err)
	}

	// Empty is the legacy spelling of unset. The track identity remains
	// specialized, while the canonical language inherits from the next scope.
	rec = routeAudioPref(t, handler, http.MethodPut, "series-1",
		[]byte(`{"audio_track_index":2,"audio_language":""}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("clearing PUT = %d: %s", rec.Code, rec.Body.String())
	}
	canonical, err = store.GetSettingValue(context.Background(), canonicalID)
	if err != nil || canonical != nil {
		t.Fatalf("empty language left canonical value=%+v err=%v", canonical, err)
	}

	rec = routeAudioPref(t, handler, http.MethodDelete, "series-1", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSetAudioPreferenceStampsUpdatedAtOnSQLite guards the v2 seam against
// the SQLite store: updateAudioPreference sends no UpdatedAt, and a read
// after the write must carry a parseable timestamp, not the empty string.
func TestSetAudioPreferenceStampsUpdatedAtOnSQLite(t *testing.T) {
	store := newPlaybackTestStore(t)
	handler := NewAudioPrefHandler(testUserStoreProvider{store: store})
	ctx := newAuthorizedPlaybackContext()

	if err := handler.SetAudioPreference(ctx, 1, userstore.AudioPreference{
		ProfileID: "profile-1", SeriesID: "series-1", AudioTrackIndex: 2, AudioLanguage: "ja",
	}); err != nil {
		t.Fatalf("SetAudioPreference: %v", err)
	}
	pref, err := handler.GetAudioPreference(ctx, 1, "profile-1", "series-1")
	if err != nil {
		t.Fatalf("GetAudioPreference: %v", err)
	}
	if pref.UpdatedAt == "" {
		t.Fatal("updated_at is empty after a v2-seam write")
	}
	if ts, err := time.Parse(time.RFC3339Nano, pref.UpdatedAt); err != nil || ts.IsZero() {
		t.Fatalf("updated_at %q is not a valid RFC3339 instant: %v", pref.UpdatedAt, err)
	}
}

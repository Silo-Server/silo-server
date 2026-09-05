package handlers

import (
	"context"
	"errors"
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

// TestGetAudioPreferenceCanonicalOverlaysCanonicalLanguage replays the v2
// read finding: /settings/values changes the canonical profile_series
// language without touching the legacy row, and playback resolves the
// canonical value, so the v2 GET must show it over the legacy track identity;
// an absent canonical row is no language preference.
func TestGetAudioPreferenceCanonicalOverlaysCanonicalLanguage(t *testing.T) {
	_, store := newValuesTestHandler(t)
	ctx := t.Context()
	handler := NewAudioPrefHandler(testUserStoreProvider{store: store})
	const legacyStamp = "2026-01-01T00:00:00Z"
	if err := store.SetAudioPreference(ctx, userstore.AudioPreference{
		ProfileID: "profile-1", SeriesID: "series-1", AudioTrackIndex: 2, AudioLanguage: "en",
		TrackSignature: &userstore.AudioTrackSignature{Language: "en", Codec: "aac", Channels: 2},
		UpdatedAt:      legacyStamp,
	}); err != nil {
		t.Fatalf("legacy write: %v", err)
	}
	canonicalID := userstore.SettingIdentity{
		Key: settingskeys.PlaybackAudioLanguage, Scope: settingscontract.ScopeProfileSeries,
		ProfileID: "profile-1", SeriesID: "series-1",
	}
	if _, err := store.UpsertSettingValue(ctx, canonicalID, []byte(`"ja"`)); err != nil {
		t.Fatalf("canonical write: %v", err)
	}

	got, err := handler.GetAudioPreferenceCanonical(ctx, 1, "profile-1", "series-1")
	if err != nil {
		t.Fatalf("canonical read: %v", err)
	}
	if got.AudioLanguage != "ja" {
		t.Fatalf("audio_language = %q, want the canonical ja", got.AudioLanguage)
	}
	if got.AudioTrackIndex != 2 || got.TrackSignature == nil || got.TrackSignature.Codec != "aac" {
		t.Fatalf("track identity was not the legacy row's: %+v", got)
	}
	if got.UpdatedAt == legacyStamp {
		t.Fatalf("updated_at = %q, want the newer canonical stamp", got.UpdatedAt)
	}
	// v1's read is untouched.
	if legacy, err := handler.GetAudioPreference(ctx, 1, "profile-1", "series-1"); err != nil || legacy.AudioLanguage != "en" || legacy.UpdatedAt != legacyStamp {
		t.Fatalf("v1 read = %+v, %v; want the legacy row unchanged", legacy, err)
	}

	if _, err := store.DeleteSettingValue(ctx, canonicalID); err != nil {
		t.Fatalf("clearing canonical row: %v", err)
	}
	got, err = handler.GetAudioPreferenceCanonical(ctx, 1, "profile-1", "series-1")
	if err != nil {
		t.Fatalf("canonical read after clear: %v", err)
	}
	if got.AudioLanguage != "" || got.AudioTrackIndex != 2 || got.UpdatedAt != legacyStamp {
		t.Fatalf("after clearing the canonical row = %+v; want no language over the legacy track", got)
	}

	// No legacy row: 404 as on v1, even with a canonical language row.
	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key: settingskeys.PlaybackAudioLanguage, Scope: settingscontract.ScopeProfileSeries,
		ProfileID: "profile-1", SeriesID: "series-2",
	}, []byte(`"ja"`)); err != nil {
		t.Fatalf("canonical write: %v", err)
	}
	_, err = handler.GetAudioPreferenceCanonical(ctx, 1, "profile-1", "series-2")
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusNotFound || apiErr.Code != "not_found" {
		t.Fatalf("canonical read without a legacy row = %v, want 404 not_found", err)
	}
}

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
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

// resolveSeriesSubtitleSetting resolves one canonical key the way the
// item-detail read path does: profile_series scope first, then the wider
// scopes, then the contract default.
func resolveSeriesSubtitleSetting(t *testing.T, store userstore.UserStore, key, profileID, seriesID string) settingsresolve.Effective {
	t.Helper()
	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("loading settings contract: %v", err)
	}
	resolved, err := settingsresolve.New(contract).Resolve(context.Background(), store,
		settingsresolve.Context{ProfileID: profileID, SeriesIDs: []string{seriesID}},
		[]string{key}, nil)
	if err != nil {
		t.Fatalf("resolving %s: %v", key, err)
	}
	if len(resolved) != 1 {
		t.Fatalf("resolving %s returned %d values, want 1", key, len(resolved))
	}
	return resolved[0]
}

// TestSetSubtitlePrefSyncsCanonicalRows replays the cutover bug: a legacy
// client turns subtitles off for one series through PUT /subtitle-prefs, and
// the item-detail read path — which resolves only the canonical
// profile_series rows — must see the change rather than silently serving the
// old preference.
func TestSetSubtitlePrefSyncsCanonicalRows(t *testing.T) {
	store := newPlaybackTestStore(t)
	handler := NewSubtitlePrefHandler(testUserStoreProvider{store: store})

	req := httptest.NewRequest(http.MethodPut, "/subtitle-prefs/series-1", strings.NewReader(`{
		"subtitle_language":"ja",
		"subtitle_track_index":2,
		"subtitle_mode":"off",
		"show_forced_subtitles":false
	}`))
	req = req.WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "series_id", "series-1")
	rec := httptest.NewRecorder()

	handler.HandleSetSubtitlePref(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	for key, want := range map[string]string{
		settingskeys.PlaybackSubtitleLanguage:    `"ja"`,
		settingskeys.PlaybackSubtitleMode:        `"off"`,
		settingskeys.PlaybackShowForcedSubtitles: `false`,
	} {
		eff := resolveSeriesSubtitleSetting(t, store, key, "profile-1", "series-1")
		if eff.Source != settingscontract.ScopeProfileSeries {
			t.Errorf("%s resolved from %q, want profile_series", key, eff.Source)
			continue
		}
		if string(eff.Value) != want {
			t.Errorf("canonical %s = %s, want %s", key, eff.Value, want)
		}
	}
}

// TestSetSubtitlePrefWithoutForcedClearsCanonicalOverride: a request that
// omits show_forced_subtitles (and finds no legacy override to preserve)
// replaces the whole preference, so a canonical forced row from an earlier
// write must not survive it.
func TestSetSubtitlePrefWithoutForcedClearsCanonicalOverride(t *testing.T) {
	store := newPlaybackTestStore(t)
	if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       settingskeys.PlaybackShowForcedSubtitles,
		Scope:     settingscontract.ScopeProfileSeries,
		ProfileID: "profile-1",
		SeriesID:  "series-1",
	}, json.RawMessage(`false`)); err != nil {
		t.Fatalf("seeding canonical forced row: %v", err)
	}

	handler := NewSubtitlePrefHandler(testUserStoreProvider{store: store})
	req := httptest.NewRequest(http.MethodPut, "/subtitle-prefs/series-1", strings.NewReader(`{
		"subtitle_language":"en",
		"subtitle_track_index":1,
		"subtitle_mode":"always"
	}`))
	req = req.WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "series_id", "series-1")
	rec := httptest.NewRecorder()

	handler.HandleSetSubtitlePref(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	eff := resolveSeriesSubtitleSetting(t, store, settingskeys.PlaybackShowForcedSubtitles, "profile-1", "series-1")
	if eff.Source == settingscontract.ScopeProfileSeries {
		t.Errorf("stale canonical forced row survived: %s from %q", eff.Value, eff.Source)
	}
}

// TestDeleteSubtitlePrefClearsCanonicalRows: clearing the legacy row means "no
// per-series preference", so the canonical profile_series rows must go with it
// and resolution must fall back past the series scope.
func TestDeleteSubtitlePrefClearsCanonicalRows(t *testing.T) {
	store := newPlaybackTestStore(t)
	handler := NewSubtitlePrefHandler(testUserStoreProvider{store: store})

	put := httptest.NewRequest(http.MethodPut, "/subtitle-prefs/series-1", strings.NewReader(`{
		"subtitle_language":"ja",
		"subtitle_track_index":2,
		"subtitle_mode":"off",
		"show_forced_subtitles":false
	}`))
	put = put.WithContext(newAuthorizedPlaybackContext())
	put = withPlaybackRouteParam(put, "series_id", "series-1")
	putRec := httptest.NewRecorder()
	handler.HandleSetSubtitlePref(putRec, put)
	if putRec.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204; body=%s", putRec.Code, putRec.Body.String())
	}

	del := httptest.NewRequest(http.MethodDelete, "/subtitle-prefs/series-1", nil)
	del = del.WithContext(newAuthorizedPlaybackContext())
	del = withPlaybackRouteParam(del, "series_id", "series-1")
	delRec := httptest.NewRecorder()
	handler.HandleDeleteSubtitlePref(delRec, del)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204; body=%s", delRec.Code, delRec.Body.String())
	}

	for _, key := range []string{
		settingskeys.PlaybackSubtitleLanguage,
		settingskeys.PlaybackSubtitleMode,
		settingskeys.PlaybackShowForcedSubtitles,
	} {
		eff := resolveSeriesSubtitleSetting(t, store, key, "profile-1", "series-1")
		if eff.Source == settingscontract.ScopeProfileSeries {
			t.Errorf("canonical %s row survived the delete: %s", key, eff.Value)
		}
	}
}

// TestSetSubtitlePrefRejectsInvalidModeBeforeWriting: a value the canonical
// endpoint would refuse must fail the request as a no-op instead of leaving
// the legacy row and the canonical rows disagreeing.
func TestSetSubtitlePrefRejectsInvalidModeBeforeWriting(t *testing.T) {
	store := newPlaybackTestStore(t)
	handler := NewSubtitlePrefHandler(testUserStoreProvider{store: store})

	req := httptest.NewRequest(http.MethodPut, "/subtitle-prefs/series-1", strings.NewReader(`{
		"subtitle_language":"en",
		"subtitle_mode":"sometimes"
	}`))
	req = req.WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "series_id", "series-1")
	rec := httptest.NewRecorder()

	handler.HandleSetSubtitlePref(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	pref, err := store.GetSubtitlePreference(context.Background(), "profile-1", "series-1")
	if err != nil {
		t.Fatalf("get subtitle preference: %v", err)
	}
	if pref != nil {
		t.Fatalf("legacy row written despite the rejected value: %+v", pref)
	}
}

// TestSetSubtitlePreferenceCanonicalPreservesCanonicalForcedOverride: the v2
// write keeps the forced override the canonical profile_series row holds —
// the row PUT /settings/values writes without touching the legacy table — when
// the body omits show_forced_subtitles, and replaces it when the body carries
// the member. The legacy row is absent throughout, so the legacy lookup alone
// would have cleared the override.
func TestSetSubtitlePreferenceCanonicalPreservesCanonicalForcedOverride(t *testing.T) {
	store := newPlaybackTestStore(t)
	ctx := context.Background()
	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key:       settingskeys.PlaybackShowForcedSubtitles,
		Scope:     settingscontract.ScopeProfileSeries,
		ProfileID: "profile-1",
		SeriesID:  "series-1",
	}, json.RawMessage(`true`)); err != nil {
		t.Fatalf("seeding canonical forced row: %v", err)
	}
	if legacy, err := store.GetSubtitlePreference(ctx, "profile-1", "series-1"); err != nil || legacy != nil {
		t.Fatalf("legacy row before the write = %+v, %v; want none", legacy, err)
	}

	handler := NewSubtitlePrefHandler(testUserStoreProvider{store: store})
	omitted := userstore.SubtitlePreference{
		ProfileID: "profile-1", SeriesID: "series-1",
		SubtitleLanguage: "ja", SubtitleTrackIndex: 2, SubtitleMode: "always",
	}
	if err := handler.SetSubtitlePreferenceCanonical(ctx, 1, omitted); err != nil {
		t.Fatalf("v2 write without show_forced_subtitles: %v", err)
	}
	eff := resolveSeriesSubtitleSetting(t, store, settingskeys.PlaybackShowForcedSubtitles, "profile-1", "series-1")
	if eff.Source != settingscontract.ScopeProfileSeries || string(eff.Value) != `true` {
		t.Fatalf("canonical forced override after omitted member = %s from %q, want true from profile_series", eff.Value, eff.Source)
	}
	legacy, err := store.GetSubtitlePreference(ctx, "profile-1", "series-1")
	if err != nil || legacy == nil {
		t.Fatalf("legacy row after the write = %+v, %v; want one", legacy, err)
	}
	if !legacy.HasShowForcedSubtitles || !legacy.ShowForcedSubtitles || legacy.SubtitleLanguage != "ja" || legacy.SubtitleTrackIndex != 2 {
		t.Fatalf("legacy row after the write = %+v; want forced=true carried over and the new track selection", legacy)
	}

	present := omitted
	present.ShowForcedSubtitles, present.HasShowForcedSubtitles = false, true
	if err := handler.SetSubtitlePreferenceCanonical(ctx, 1, present); err != nil {
		t.Fatalf("v2 write with show_forced_subtitles: %v", err)
	}
	eff = resolveSeriesSubtitleSetting(t, store, settingskeys.PlaybackShowForcedSubtitles, "profile-1", "series-1")
	if eff.Source != settingscontract.ScopeProfileSeries || string(eff.Value) != `false` {
		t.Fatalf("canonical forced override after present member = %s from %q, want false from profile_series", eff.Value, eff.Source)
	}
	legacy, err = store.GetSubtitlePreference(ctx, "profile-1", "series-1")
	if err != nil || legacy == nil || !legacy.HasShowForcedSubtitles || legacy.ShowForcedSubtitles {
		t.Fatalf("legacy row after the present write = %+v, %v; want forced=false", legacy, err)
	}
}

// TestSetSubtitlePreferenceCanonicalWithoutAnyOverrideClearsForced: with no
// canonical row and no legacy row, an omitted show_forced_subtitles means no
// override, as on v1.
func TestSetSubtitlePreferenceCanonicalWithoutAnyOverrideClearsForced(t *testing.T) {
	store := newPlaybackTestStore(t)
	ctx := context.Background()
	handler := NewSubtitlePrefHandler(testUserStoreProvider{store: store})
	if err := handler.SetSubtitlePreferenceCanonical(ctx, 1, userstore.SubtitlePreference{
		ProfileID: "profile-1", SeriesID: "series-1",
		SubtitleLanguage: "en", SubtitleTrackIndex: 1, SubtitleMode: "always",
	}); err != nil {
		t.Fatalf("v2 write: %v", err)
	}
	eff := resolveSeriesSubtitleSetting(t, store, settingskeys.PlaybackShowForcedSubtitles, "profile-1", "series-1")
	if eff.Source == settingscontract.ScopeProfileSeries {
		t.Fatalf("forced override invented: %s from %q", eff.Value, eff.Source)
	}
	legacy, err := store.GetSubtitlePreference(ctx, "profile-1", "series-1")
	if err != nil || legacy == nil || legacy.HasShowForcedSubtitles {
		t.Fatalf("legacy row = %+v, %v; want one without a forced override", legacy, err)
	}
}

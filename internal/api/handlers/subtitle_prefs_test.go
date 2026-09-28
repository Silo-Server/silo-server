package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestSetSubtitlePreferenceCanonicalDoesNotResurrectClearedOverride replays
// the review finding: DELETE /settings/values clears the canonical forced row
// without touching the legacy row, so the legacy row still holds the old flag.
// A v2 write that omits show_forced_subtitles must treat the absent canonical
// row as "no override" and never read the legacy row, or the sync would
// recreate the override the client just cleared.
func TestSetSubtitlePreferenceCanonicalDoesNotResurrectClearedOverride(t *testing.T) {
	store := newPlaybackTestStore(t)
	ctx := context.Background()
	if err := store.SetSubtitlePreference(ctx, userstore.SubtitlePreference{
		ProfileID: "profile-1", SeriesID: "series-1",
		SubtitleLanguage: "en", SubtitleTrackIndex: 1, SubtitleMode: "always",
		ShowForcedSubtitles: true, HasShowForcedSubtitles: true,
	}); err != nil {
		t.Fatalf("seeding legacy row: %v", err)
	}
	if eff := resolveSeriesSubtitleSetting(t, store, settingskeys.PlaybackShowForcedSubtitles, "profile-1", "series-1"); eff.Source == settingscontract.ScopeProfileSeries {
		t.Fatalf("canonical forced row before the write = %s from %q; want none", eff.Value, eff.Source)
	}

	handler := NewSubtitlePrefHandler(testUserStoreProvider{store: store})
	if err := handler.SetSubtitlePreferenceCanonical(ctx, 1, userstore.SubtitlePreference{
		ProfileID: "profile-1", SeriesID: "series-1",
		SubtitleLanguage: "ja", SubtitleTrackIndex: 2, SubtitleMode: "always",
	}); err != nil {
		t.Fatalf("v2 write without show_forced_subtitles: %v", err)
	}
	if eff := resolveSeriesSubtitleSetting(t, store, settingskeys.PlaybackShowForcedSubtitles, "profile-1", "series-1"); eff.Source == settingscontract.ScopeProfileSeries {
		t.Fatalf("cleared forced override resurrected: %s from %q", eff.Value, eff.Source)
	}
	legacy, err := store.GetSubtitlePreference(ctx, "profile-1", "series-1")
	if err != nil || legacy == nil {
		t.Fatalf("legacy row after the write = %+v, %v; want one", legacy, err)
	}
	if legacy.HasShowForcedSubtitles || legacy.SubtitleLanguage != "ja" || legacy.SubtitleTrackIndex != 2 {
		t.Fatalf("legacy row after the write = %+v; want no forced override and the new track selection", legacy)
	}
}

// TestSetSubtitlePreferenceCanonicalStampsUpdatedAtOnSQLite guards the v2
// seam against the SQLite store: updateSubtitlePreference sends no UpdatedAt,
// and a read after the write must carry a parseable timestamp, not the empty
// string.
func TestSetSubtitlePreferenceCanonicalStampsUpdatedAtOnSQLite(t *testing.T) {
	store := newPlaybackTestStore(t)
	handler := NewSubtitlePrefHandler(testUserStoreProvider{store: store})
	ctx := newAuthorizedPlaybackContext()

	if err := handler.SetSubtitlePreferenceCanonical(ctx, 1, userstore.SubtitlePreference{
		ProfileID: "profile-1", SeriesID: "series-1",
		SubtitleLanguage: "ja", SubtitleTrackIndex: 2, SubtitleMode: "always",
	}); err != nil {
		t.Fatalf("SetSubtitlePreferenceCanonical: %v", err)
	}
	pref, err := handler.GetSubtitlePreference(ctx, 1, "profile-1", "series-1")
	if err != nil {
		t.Fatalf("GetSubtitlePreference: %v", err)
	}
	if pref.UpdatedAt == "" {
		t.Fatal("updated_at is empty after a v2-seam write")
	}
	if ts, err := time.Parse(time.RFC3339Nano, pref.UpdatedAt); err != nil || ts.IsZero() {
		t.Fatalf("updated_at %q is not a valid RFC3339 instant: %v", pref.UpdatedAt, err)
	}
}

// TestGetSubtitlePreferenceCanonicalOverlaysCanonicalMembers replays the v2
// read finding: /settings/values changes the canonical profile_series rows
// without touching the legacy row, so the v2 GET must show the canonical
// language, mode and forced override — the ones playback resolves — over the
// legacy row's track identity, and an absent canonical row is an unset member.
func TestGetSubtitlePreferenceCanonicalOverlaysCanonicalMembers(t *testing.T) {
	store := newPlaybackTestStore(t)
	ctx := t.Context()
	handler := NewSubtitlePrefHandler(testUserStoreProvider{store: store})
	const legacyStamp = "2026-01-01T00:00:00Z"
	if err := store.SetSubtitlePreference(ctx, userstore.SubtitlePreference{
		ProfileID: "profile-1", SeriesID: "series-1",
		SubtitleLanguage: "en", SubtitleTrackIndex: 3, SubtitleMode: "auto", ExternalSubtitlePath: "ep.en.srt",
		TrackSignature:      &userstore.SubtitleTrackSignature{Source: "external", Language: "en"},
		ShowForcedSubtitles: true, HasShowForcedSubtitles: true,
		UpdatedAt: legacyStamp,
	}); err != nil {
		t.Fatalf("legacy write: %v", err)
	}
	canonical := func(key, value string) {
		t.Helper()
		if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
			Key: key, Scope: settingscontract.ScopeProfileSeries, ProfileID: "profile-1", SeriesID: "series-1",
		}, json.RawMessage(value)); err != nil {
			t.Fatalf("canonical write %s: %v", key, err)
		}
	}
	// A newer /settings/values write of the language and mode; the forced
	// override the legacy row still carries was cleared canonically (no row).
	canonical(settingskeys.PlaybackSubtitleLanguage, `"ja"`)
	canonical(settingskeys.PlaybackSubtitleMode, `"always"`)

	got, err := handler.GetSubtitlePreferenceCanonical(ctx, 1, "profile-1", "series-1")
	if err != nil {
		t.Fatalf("canonical read: %v", err)
	}
	if got.SubtitleLanguage != "ja" || got.SubtitleMode != "always" {
		t.Fatalf("overlaid members = %q/%q, want ja/always", got.SubtitleLanguage, got.SubtitleMode)
	}
	if got.HasShowForcedSubtitles {
		t.Fatalf("forced override = %+v, want none: the canonical row is absent", got)
	}
	if got.SubtitleTrackIndex != 3 || got.ExternalSubtitlePath != "ep.en.srt" || got.TrackSignature == nil || got.TrackSignature.Language != "en" {
		t.Fatalf("track identity was not the legacy row's: %+v", got)
	}
	if got.UpdatedAt == legacyStamp {
		t.Fatalf("updated_at = %q, want the newer canonical stamp", got.UpdatedAt)
	}
	// v1's read is untouched: it still answers the legacy row verbatim.
	legacy, err := handler.GetSubtitlePreference(ctx, 1, "profile-1", "series-1")
	if err != nil || legacy.SubtitleLanguage != "en" || legacy.SubtitleMode != "auto" || !legacy.HasShowForcedSubtitles || legacy.UpdatedAt != legacyStamp {
		t.Fatalf("v1 read = %+v, %v; want the legacy row unchanged", legacy, err)
	}

	// A canonical forced row overrides the legacy flag in either direction.
	canonical(settingskeys.PlaybackShowForcedSubtitles, `false`)
	got, err = handler.GetSubtitlePreferenceCanonical(ctx, 1, "profile-1", "series-1")
	if err != nil || !got.HasShowForcedSubtitles || got.ShowForcedSubtitles {
		t.Fatalf("forced after canonical false = %+v, %v; want an explicit false override", got, err)
	}

	// Clearing every canonical member leaves the track identity with no
	// language, mode or forced override.
	for _, key := range []string{settingskeys.PlaybackSubtitleLanguage, settingskeys.PlaybackSubtitleMode, settingskeys.PlaybackShowForcedSubtitles} {
		if _, err := store.DeleteSettingValue(ctx, userstore.SettingIdentity{
			Key: key, Scope: settingscontract.ScopeProfileSeries, ProfileID: "profile-1", SeriesID: "series-1",
		}); err != nil {
			t.Fatalf("clearing %s: %v", key, err)
		}
	}
	got, err = handler.GetSubtitlePreferenceCanonical(ctx, 1, "profile-1", "series-1")
	if err != nil {
		t.Fatalf("canonical read after clear: %v", err)
	}
	if got.SubtitleLanguage != "" || got.SubtitleMode != "" || got.HasShowForcedSubtitles || got.SubtitleTrackIndex != 3 {
		t.Fatalf("after clearing canonical rows = %+v; want unset members over the legacy track", got)
	}
	if got.UpdatedAt != legacyStamp {
		t.Fatalf("updated_at after clear = %q, want the legacy stamp %q", got.UpdatedAt, legacyStamp)
	}
}

// TestGetSubtitlePreferenceCanonicalWithoutLegacyRowIs404: the resource is
// the track selection the legacy row holds; canonical rows alone are the
// profile's language preference, not a remembered track, and read as 404 on
// v2 exactly as on v1.
func TestGetSubtitlePreferenceCanonicalWithoutLegacyRowIs404(t *testing.T) {
	store := newPlaybackTestStore(t)
	ctx := t.Context()
	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key: settingskeys.PlaybackSubtitleLanguage, Scope: settingscontract.ScopeProfileSeries, ProfileID: "profile-1", SeriesID: "series-1",
	}, json.RawMessage(`"ja"`)); err != nil {
		t.Fatalf("canonical write: %v", err)
	}
	handler := NewSubtitlePrefHandler(testUserStoreProvider{store: store})
	_, err := handler.GetSubtitlePreferenceCanonical(ctx, 1, "profile-1", "series-1")
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusNotFound || apiErr.Code != "not_found" {
		t.Fatalf("canonical read without a legacy row = %v, want 404 not_found", err)
	}
}

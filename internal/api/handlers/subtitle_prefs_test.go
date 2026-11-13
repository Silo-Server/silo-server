package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

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

// The subtitle preference write mirrors language, mode and the forced
// override to the canonical profile_series rows playback resolves; a value
// the canonical contract refuses fails before anything is written; and the
// delete clears every canonical member.
func TestSubtitlePreferenceCanonicalWriteAndDeleteSync(t *testing.T) {
	store := newPlaybackTestStore(t)
	ctx := t.Context()
	handler := NewSubtitlePrefHandler(testUserStoreProvider{store: store})
	keys := []string{
		settingskeys.PlaybackSubtitleLanguage,
		settingskeys.PlaybackSubtitleMode,
		settingskeys.PlaybackShowForcedSubtitles,
	}

	err := handler.SetSubtitlePreferenceCanonical(ctx, 1, userstore.SubtitlePreference{
		ProfileID: "profile-1", SeriesID: "series-1", SubtitleLanguage: "en", SubtitleMode: "sometimes",
	})
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("invalid mode = %v, want 400", err)
	}
	if pref, err := store.GetSubtitlePreference(ctx, "profile-1", "series-1"); err != nil || pref != nil {
		t.Fatalf("specialized row written despite the rejected mode: %+v, %v", pref, err)
	}
	for _, key := range keys {
		if eff := resolveSeriesSubtitleSetting(t, store, key, "profile-1", "series-1"); eff.Source == settingscontract.ScopeProfileSeries {
			t.Fatalf("canonical %s written despite the rejected mode: %s", key, eff.Value)
		}
	}

	if err := handler.SetSubtitlePreferenceCanonical(ctx, 1, userstore.SubtitlePreference{
		ProfileID: "profile-1", SeriesID: "series-1", SubtitleLanguage: "ja", SubtitleTrackIndex: 2,
		SubtitleMode: "off", ShowForcedSubtitles: false, HasShowForcedSubtitles: true,
	}); err != nil {
		t.Fatalf("SetSubtitlePreferenceCanonical: %v", err)
	}
	want := map[string]string{
		settingskeys.PlaybackSubtitleLanguage:    `"ja"`,
		settingskeys.PlaybackSubtitleMode:        `"off"`,
		settingskeys.PlaybackShowForcedSubtitles: `false`,
	}
	for _, key := range keys {
		eff := resolveSeriesSubtitleSetting(t, store, key, "profile-1", "series-1")
		if eff.Source != settingscontract.ScopeProfileSeries || string(eff.Value) != want[key] {
			t.Errorf("canonical %s = %s from %q, want %s from profile_series", key, eff.Value, eff.Source, want[key])
		}
	}

	if err := handler.DeleteSubtitlePreference(ctx, 1, "profile-1", "series-1"); err != nil {
		t.Fatalf("DeleteSubtitlePreference: %v", err)
	}
	for _, key := range keys {
		if eff := resolveSeriesSubtitleSetting(t, store, key, "profile-1", "series-1"); eff.Source == settingscontract.ScopeProfileSeries {
			t.Errorf("canonical %s row survived the delete: %s", key, eff.Value)
		}
	}
}

// /settings/values can change the canonical members without touching the
// specialized row, so the read overlays language, mode and the forced
// override on the specialized track identity; an absent canonical row means
// no preference for that member.
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
	identity := func(key string) userstore.SettingIdentity {
		return userstore.SettingIdentity{Key: key, Scope: settingscontract.ScopeProfileSeries, ProfileID: "profile-1", SeriesID: "series-1"}
	}
	canonical := func(key, value string) {
		t.Helper()
		if _, err := store.UpsertSettingValue(ctx, identity(key), json.RawMessage(value)); err != nil {
			t.Fatalf("canonical write %s: %v", key, err)
		}
	}
	canonical(settingskeys.PlaybackSubtitleLanguage, `"ja"`)
	canonical(settingskeys.PlaybackSubtitleMode, `"always"`)

	got, err := handler.GetSubtitlePreferenceCanonical(ctx, 1, "profile-1", "series-1")
	if err != nil {
		t.Fatalf("canonical read: %v", err)
	}
	if got.SubtitleLanguage != "ja" || got.SubtitleMode != "always" || got.HasShowForcedSubtitles {
		t.Fatalf("overlaid members = %+v, want ja/always and no forced override", got)
	}
	if got.SubtitleTrackIndex != 3 || got.ExternalSubtitlePath != "ep.en.srt" || got.TrackSignature == nil || got.TrackSignature.Language != "en" {
		t.Fatalf("track identity was not the specialized row's: %+v", got)
	}
	if got.UpdatedAt == legacyStamp {
		t.Fatalf("updated_at = %q, want the newer canonical stamp", got.UpdatedAt)
	}

	canonical(settingskeys.PlaybackShowForcedSubtitles, `false`)
	got, err = handler.GetSubtitlePreferenceCanonical(ctx, 1, "profile-1", "series-1")
	if err != nil || !got.HasShowForcedSubtitles || got.ShowForcedSubtitles {
		t.Fatalf("forced after canonical false = %+v, %v; want an explicit false override", got, err)
	}

	for _, key := range []string{settingskeys.PlaybackSubtitleLanguage, settingskeys.PlaybackSubtitleMode, settingskeys.PlaybackShowForcedSubtitles} {
		if _, err := store.DeleteSettingValue(ctx, identity(key)); err != nil {
			t.Fatalf("clearing %s: %v", key, err)
		}
	}
	got, err = handler.GetSubtitlePreferenceCanonical(ctx, 1, "profile-1", "series-1")
	if err != nil || got.SubtitleLanguage != "" || got.SubtitleMode != "" || got.HasShowForcedSubtitles || got.SubtitleTrackIndex != 3 || got.UpdatedAt != legacyStamp {
		t.Fatalf("after clearing canonical rows = %+v, %v; want unset members over the specialized track", got, err)
	}
}

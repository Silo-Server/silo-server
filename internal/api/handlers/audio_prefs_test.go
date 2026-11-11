package handlers

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The audio preference keeps track identity in the specialized row and
// mirrors the language to the canonical profile_series row playback resolves.
// An empty language clears the canonical row so the language inherits.
func TestSetAudioPreferenceKeepsTrackIdentityAndSyncsCanonicalLanguage(t *testing.T) {
	_, store := newValuesTestHandler(t)
	ctx := t.Context()
	handler := NewAudioPrefHandler(testUserStoreProvider{store: store})
	canonicalID := userstore.SettingIdentity{
		Key: settingskeys.PlaybackAudioLanguage, Scope: settingscontract.ScopeProfileSeries,
		ProfileID: "profile-1", SeriesID: "series-1",
	}

	if err := handler.SetAudioPreference(ctx, 1, userstore.AudioPreference{
		ProfileID: "profile-1", SeriesID: "series-1", AudioTrackIndex: 2, AudioLanguage: "ja",
		TrackSignature: &userstore.AudioTrackSignature{Language: "ja", Codec: "aac", Channels: 2},
	}); err != nil {
		t.Fatalf("SetAudioPreference: %v", err)
	}
	legacy, err := store.GetAudioPreference(ctx, "profile-1", "series-1")
	if err != nil || legacy == nil || legacy.AudioTrackIndex != 2 || legacy.TrackSignature == nil {
		t.Fatalf("specialized row = %+v, %v; want the track identity kept", legacy, err)
	}
	canonical, err := store.GetSettingValue(ctx, canonicalID)
	if err != nil || canonical == nil || string(canonical.Value) != `"ja"` {
		t.Fatalf("canonical language = %+v err=%v, want ja", canonical, err)
	}

	if err := handler.SetAudioPreference(ctx, 1, userstore.AudioPreference{
		ProfileID: "profile-1", SeriesID: "series-1", AudioTrackIndex: 2,
	}); err != nil {
		t.Fatalf("clearing SetAudioPreference: %v", err)
	}
	if canonical, err := store.GetSettingValue(ctx, canonicalID); err != nil || canonical != nil {
		t.Fatalf("empty language left canonical value=%+v err=%v", canonical, err)
	}

	// Delete removes the canonical row too.
	if err := handler.SetAudioPreference(ctx, 1, userstore.AudioPreference{
		ProfileID: "profile-1", SeriesID: "series-1", AudioTrackIndex: 2, AudioLanguage: "ja",
	}); err != nil {
		t.Fatalf("SetAudioPreference: %v", err)
	}
	if err := handler.DeleteAudioPreference(ctx, 1, "profile-1", "series-1"); err != nil {
		t.Fatalf("DeleteAudioPreference: %v", err)
	}
	if canonical, err := store.GetSettingValue(ctx, canonicalID); err != nil || canonical != nil {
		t.Fatalf("delete left canonical value=%+v err=%v", canonical, err)
	}
	if legacy, err := store.GetAudioPreference(ctx, "profile-1", "series-1"); err != nil || legacy != nil {
		t.Fatalf("delete left specialized row %+v err=%v", legacy, err)
	}
}

// /settings/values can change the canonical language without touching the
// specialized row, and playback resolves the canonical value, so the read
// overlays it on the specialized track identity. An absent canonical row is
// no language; a missing specialized row is a 404.
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
	if got.AudioLanguage != "ja" || got.AudioTrackIndex != 2 || got.TrackSignature == nil || got.TrackSignature.Codec != "aac" {
		t.Fatalf("read = %+v; want canonical ja over the specialized track identity", got)
	}
	if got.UpdatedAt == legacyStamp {
		t.Fatalf("updated_at = %q, want the newer canonical stamp", got.UpdatedAt)
	}

	if _, err := store.DeleteSettingValue(ctx, canonicalID); err != nil {
		t.Fatalf("clearing canonical row: %v", err)
	}
	got, err = handler.GetAudioPreferenceCanonical(ctx, 1, "profile-1", "series-1")
	if err != nil || got.AudioLanguage != "" || got.AudioTrackIndex != 2 || got.UpdatedAt != legacyStamp {
		t.Fatalf("after clearing the canonical row = %+v, %v; want no language over the specialized track", got, err)
	}

	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key: settingskeys.PlaybackAudioLanguage, Scope: settingscontract.ScopeProfileSeries,
		ProfileID: "profile-1", SeriesID: "series-2",
	}, []byte(`"ja"`)); err != nil {
		t.Fatalf("canonical write: %v", err)
	}
	_, err = handler.GetAudioPreferenceCanonical(ctx, 1, "profile-1", "series-2")
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusNotFound {
		t.Fatalf("read without a specialized row = %v, want 404", err)
	}
}

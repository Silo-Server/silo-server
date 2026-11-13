package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

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

// A partial update merges onto the canonical profile_library rows: a member
// the patch omits keeps its newer canonical value, a nil value clears only
// that member, values are normalized, and a rejected member writes nothing.
func TestPatchLibraryPlaybackPreferenceMergesOntoCanonicalRows(t *testing.T) {
	_, store := newValuesTestHandler(t)
	handler := NewLibraryPlaybackPrefHandler(testUserStoreProvider{store: store})
	ctx := t.Context()
	const profileID = "profile-1"
	value := func(key string) *string { return canonicalLibraryValue(t, store, profileID, 7, key) }
	patch := func(p LibraryPlaybackPrefPatch) {
		t.Helper()
		if err := handler.PatchLibraryPlaybackPreference(ctx, 1, profileID, 7, p); err != nil {
			t.Fatalf("patch: %v", err)
		}
	}

	patch(LibraryPlaybackPrefPatch{
		AudioLanguage: PrefPatch[string]{Present: true, Value: new(" EN-us ")},
		SubtitleMode:  PrefPatch[string]{Present: true, Value: new("always")},
	})
	if got := value(settingskeys.PlaybackAudioLanguage); got == nil || *got != `"en-US"` {
		t.Fatalf("audio_language = %v, want normalized \"en-US\"", got)
	}
	// A canonical-only write, as PUT /settings/values makes, moves
	// subtitle_language; the next patch must not revert it.
	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key: settingskeys.PlaybackSubtitleLanguage, Scope: settingscontract.ScopeProfileLibrary,
		ProfileID: profileID, LibraryID: 7,
	}, []byte(`"de"`)); err != nil {
		t.Fatalf("canonical write: %v", err)
	}
	patch(LibraryPlaybackPrefPatch{AudioLanguage: PrefPatch[string]{Present: true, Value: new("fr")}})
	if got := value(settingskeys.PlaybackAudioLanguage); got == nil || *got != `"fr"` {
		t.Fatalf("audio_language = %v, want \"fr\"", got)
	}
	if got := value(settingskeys.PlaybackSubtitleLanguage); got == nil || *got != `"de"` {
		t.Fatalf("subtitle_language = %v, want the newer canonical \"de\"", got)
	}
	if got := value(settingskeys.PlaybackSubtitleMode); got == nil || *got != `"always"` {
		t.Fatalf("subtitle_mode = %v, want \"always\"", got)
	}

	patch(LibraryPlaybackPrefPatch{
		AudioLanguage: PrefPatch[string]{Present: true},
		SubtitleMode:  PrefPatch[string]{Present: true},
	})
	if value(settingskeys.PlaybackAudioLanguage) != nil || value(settingskeys.PlaybackSubtitleMode) != nil {
		t.Fatal("cleared members survived")
	}
	if got := value(settingskeys.PlaybackSubtitleLanguage); got == nil || *got != `"de"` {
		t.Fatalf("clearing other members dropped subtitle_language: %v", got)
	}

	err := handler.PatchLibraryPlaybackPreference(ctx, 1, profileID, 7, LibraryPlaybackPrefPatch{
		SubtitleLanguage: PrefPatch[string]{Present: true, Value: new("fr")},
		SubtitleMode:     PrefPatch[string]{Present: true, Value: new("sometimes")},
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Field != "subtitle_mode" {
		t.Fatalf("invalid subtitle_mode: %v", err)
	}
	if got := value(settingskeys.PlaybackSubtitleLanguage); got == nil || *got != `"de"` {
		t.Fatalf("a rejected patch wrote subtitle_language: %v", got)
	}
}

// The listing is assembled from the canonical profile_library rows of the
// requesting profile only; a legacy-only composite row is not part of it, and
// a library drops out once its last canonical key is cleared.
func TestListLibraryPlaybackPreferencesCanonicalReadsSettingRows(t *testing.T) {
	_, store := newValuesTestHandler(t)
	handler := NewLibraryPlaybackPrefHandler(testUserStoreProvider{store: store})
	ctx := t.Context()
	const profileID = "profile-1"
	write := func(profile string, libraryID int, key, value string) {
		t.Helper()
		if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
			Key: key, Scope: settingscontract.ScopeProfileLibrary, ProfileID: profile, LibraryID: libraryID,
		}, []byte(value)); err != nil {
			t.Fatalf("canonical write %s: %v", key, err)
		}
	}
	write(profileID, 9, settingskeys.PlaybackSubtitleLanguage, `"de"`)
	write(profileID, 9, settingskeys.PlaybackShowForcedSubtitles, `true`)
	write(profileID, 4, settingskeys.PlaybackSubtitleMode, `"off"`)
	write("profile-2", 9, settingskeys.PlaybackAudioLanguage, `"fr"`)
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

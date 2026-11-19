package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeAudioPreferences keeps one profile's audio preferences keyed by
// series and answers 404 not_found for a missing one, as the real seam does.
// Its read stands in for the canonical seam entry point
// (GetAudioPreferenceCanonical): the kept row is the overlaid state — legacy
// track identity with the canonical language — never the legacy table alone.
type fakeAudioPreferences struct {
	prefs map[string]userstore.AudioPreference
	last  *userstore.AudioPreference
	err   error
}

func (f *fakeAudioPreferences) GetAudioPreferenceCanonical(_ context.Context, _ int, profileID, seriesID string) (userstore.AudioPreference, error) {
	if f.err != nil {
		return userstore.AudioPreference{}, f.err
	}
	p, ok := f.prefs[profileID+"/"+seriesID]
	if !ok {
		return userstore.AudioPreference{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Audio preference not found"}
	}
	return p, nil
}

func (f *fakeAudioPreferences) SetAudioPreference(_ context.Context, _ int, pref userstore.AudioPreference) error {
	f.last = &pref
	if f.err != nil {
		return f.err
	}
	if f.prefs == nil {
		f.prefs = map[string]userstore.AudioPreference{}
	}
	pref.UpdatedAt = "2026-01-02T03:04:05Z"
	f.prefs[pref.ProfileID+"/"+pref.SeriesID] = pref
	return nil
}

func (f *fakeAudioPreferences) DeleteAudioPreference(_ context.Context, _ int, profileID, seriesID string) error {
	if f.err != nil {
		return f.err
	}
	delete(f.prefs, profileID+"/"+seriesID)
	return nil
}

// fakeLibraryPlaybackPreferences keeps one profile's library overrides and
// refuses a library id outside known with 404 not_found, as the real seam
// does through its library lookup. Its listing stands in for the canonical
// seam entry point (ListLibraryPlaybackPreferencesCanonical): the kept rows
// are the assembled per-library canonical state, never the legacy table.
type fakeLibraryPlaybackPreferences struct {
	known map[int]bool
	prefs []userstore.LibraryPlaybackPreference
	last  *handlers.LibraryPlaybackPrefPatch
	err   error
}

func (f *fakeLibraryPlaybackPreferences) ListLibraryPlaybackPreferencesCanonical(_ context.Context, _ int, profileID string) ([]userstore.LibraryPlaybackPreference, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := []userstore.LibraryPlaybackPreference{}
	for _, p := range f.prefs {
		if p.ProfileID == profileID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeLibraryPlaybackPreferences) library(libraryID int) error {
	if !f.known[libraryID] {
		return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Library not found"}
	}
	return nil
}

// PatchLibraryPlaybackPreference records the patch and applies it to the
// kept row the way the seam does: present members replace or clear their
// override, a row left with none is removed.
func (f *fakeLibraryPlaybackPreferences) PatchLibraryPlaybackPreference(_ context.Context, _ int, profileID string, libraryID int, patch handlers.LibraryPlaybackPrefPatch) error {
	if err := f.library(libraryID); err != nil {
		return err
	}
	f.last = &patch
	if f.err != nil {
		return f.err
	}
	idx := -1
	for i := range f.prefs {
		if f.prefs[i].ProfileID == profileID && f.prefs[i].LibraryID == libraryID {
			idx = i
		}
	}
	row := userstore.LibraryPlaybackPreference{ProfileID: profileID, LibraryID: libraryID}
	if idx >= 0 {
		row = f.prefs[idx]
	}
	apply := func(p handlers.PrefPatch[string], has *bool, value *string) {
		if !p.Present {
			return
		}
		*has, *value = p.Value != nil, ""
		if p.Value != nil {
			*value = *p.Value
		}
	}
	apply(patch.AudioLanguage, &row.HasAudioLanguage, &row.AudioLanguage)
	apply(patch.SubtitleLanguage, &row.HasSubtitleLanguage, &row.SubtitleLanguage)
	apply(patch.SubtitleMode, &row.HasSubtitleMode, &row.SubtitleMode)
	if p := patch.ShowForcedSubtitles; p.Present {
		row.HasShowForcedSubtitles, row.ShowForcedSubtitles = p.Value != nil, p.Value != nil && *p.Value
	}
	if !row.HasAudioLanguage && !row.HasSubtitleLanguage && !row.HasSubtitleMode && !row.HasShowForcedSubtitles {
		if idx >= 0 {
			f.prefs = append(f.prefs[:idx], f.prefs[idx+1:]...)
		}
		return nil
	}
	if idx >= 0 {
		f.prefs[idx] = row
	} else {
		f.prefs = append(f.prefs, row)
	}
	return nil
}

func (f *fakeLibraryPlaybackPreferences) DeleteLibraryPlaybackPreference(_ context.Context, _ int, profileID string, libraryID int) error {
	if err := f.library(libraryID); err != nil {
		return err
	}
	if f.err != nil {
		return f.err
	}
	kept := f.prefs[:0]
	for _, p := range f.prefs {
		if p.ProfileID != profileID || p.LibraryID != libraryID {
			kept = append(kept, p)
		}
	}
	f.prefs = kept
	return nil
}

func fixtureAudioPreference() userstore.AudioPreference {
	return userstore.AudioPreference{
		ProfileID: "p-owner", SeriesID: "series-8f2c1a", AudioTrackIndex: 1, AudioLanguage: "eng",
		TrackSignature: &userstore.AudioTrackSignature{Language: "eng", Title: "English 5.1", Codec: "eac3", Layout: "5.1", Channels: 6, Default: true},
		UpdatedAt:      "2026-01-02T03:04:05Z",
	}
}

func fixtureLibraryPlaybackPreferences() []userstore.LibraryPlaybackPreference {
	return []userstore.LibraryPlaybackPreference{
		{ProfileID: "p-owner", LibraryID: 1, AudioLanguage: "en", HasAudioLanguage: true, SubtitleMode: "always", HasSubtitleMode: true, UpdatedAt: "2026-01-02T03:04:05Z"},
		{ProfileID: "p-owner", LibraryID: 3, ShowForcedSubtitles: true, HasShowForcedSubtitles: true, UpdatedAt: "2026-01-01T00:00:00Z"},
		{ProfileID: "p-other", LibraryID: 1, AudioLanguage: "fr", HasAudioLanguage: true, UpdatedAt: "2026-01-01T00:00:00Z"},
	}
}

// fakeSubtitlePreferences keeps one profile's subtitle preferences keyed by
// profile/series and records the last write it accepted. Its read stands in
// for the canonical seam entry point (GetSubtitlePreferenceCanonical): the
// kept row is the overlaid state — legacy track identity with the canonical
// language, mode and forced override — never the legacy table alone.
type fakeSubtitlePreferences struct {
	prefs map[string]userstore.SubtitlePreference
	last  *userstore.SubtitlePreference
	err   error
}

func (f *fakeSubtitlePreferences) GetSubtitlePreferenceCanonical(_ context.Context, _ int, profileID, seriesID string) (userstore.SubtitlePreference, error) {
	if f.err != nil {
		return userstore.SubtitlePreference{}, f.err
	}
	p, ok := f.prefs[profileID+"/"+seriesID]
	if !ok {
		return userstore.SubtitlePreference{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Subtitle preference not found"}
	}
	return p, nil
}

func (f *fakeSubtitlePreferences) SetSubtitlePreferenceCanonical(_ context.Context, _ int, pref userstore.SubtitlePreference) error {
	if f.err != nil {
		return f.err
	}
	// The seam keeps a stored forced override when the write carries none.
	if existing, ok := f.prefs[pref.ProfileID+"/"+pref.SeriesID]; ok && !pref.HasShowForcedSubtitles && existing.HasShowForcedSubtitles {
		pref.ShowForcedSubtitles, pref.HasShowForcedSubtitles = existing.ShowForcedSubtitles, true
	}
	pref.UpdatedAt = "2026-01-02T03:04:05Z"
	if f.prefs == nil {
		f.prefs = map[string]userstore.SubtitlePreference{}
	}
	f.prefs[pref.ProfileID+"/"+pref.SeriesID] = pref
	f.last = &pref
	return nil
}

func (f *fakeSubtitlePreferences) DeleteSubtitlePreference(_ context.Context, _ int, profileID, seriesID string) error {
	if f.err != nil {
		return f.err
	}
	delete(f.prefs, profileID+"/"+seriesID)
	return nil
}

func fixtureSubtitlePreference() userstore.SubtitlePreference {
	return userstore.SubtitlePreference{
		ProfileID: "p-owner", SeriesID: "series-8f2c1a", SubtitleLanguage: "eng", SubtitleTrackIndex: 0, SubtitleMode: "always",
		TrackSignature:      &userstore.SubtitleTrackSignature{Source: "embedded", Language: "eng", Codec: "subrip", Label: "English (SDH)", HearingImpaired: true},
		ShowForcedSubtitles: true, HasShowForcedSubtitles: true,
		UpdatedAt: "2026-01-02T03:04:05Z",
	}
}

// preferenceDeps is pilotDeps plus the preference seams.
func preferenceDeps(audio *fakeAudioPreferences, library *fakeLibraryPlaybackPreferences) Dependencies {
	deps := pilotDeps(nil, nil)
	deps.SubtitlePreferences = &fakeSubtitlePreferences{prefs: map[string]userstore.SubtitlePreference{"p-owner/series-8f2c1a": fixtureSubtitlePreference()}}
	if audio == nil {
		audio = &fakeAudioPreferences{prefs: map[string]userstore.AudioPreference{"p-owner/series-8f2c1a": fixtureAudioPreference()}}
	}
	if library == nil {
		library = &fakeLibraryPlaybackPreferences{known: map[int]bool{1: true, 2: true, 3: true, 4: true}, prefs: fixtureLibraryPlaybackPreferences()}
	}
	deps.AudioPreferences = audio
	deps.LibraryPlaybackPreferences = library
	return deps
}

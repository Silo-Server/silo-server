package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeAudioPreferences keeps one profile's audio preferences keyed by
// series and answers 404 not_found for a missing one, as the real seam does.
type fakeAudioPreferences struct {
	prefs map[string]userstore.AudioPreference
	last  *userstore.AudioPreference
	err   error
}

func (f *fakeAudioPreferences) GetAudioPreference(_ context.Context, _ int, profileID, seriesID string) (userstore.AudioPreference, error) {
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
// profile/series and records the last write it accepted.
type fakeSubtitlePreferences struct {
	prefs map[string]userstore.SubtitlePreference
	last  *userstore.SubtitlePreference
	err   error
}

func (f *fakeSubtitlePreferences) GetSubtitlePreference(_ context.Context, _ int, profileID, seriesID string) (userstore.SubtitlePreference, error) {
	if f.err != nil {
		return userstore.SubtitlePreference{}, f.err
	}
	p, ok := f.prefs[profileID+"/"+seriesID]
	if !ok {
		return userstore.SubtitlePreference{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Subtitle preference not found"}
	}
	return p, nil
}

func (f *fakeSubtitlePreferences) SetSubtitlePreference(_ context.Context, _ int, pref userstore.SubtitlePreference) error {
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

func TestGetAudioPreference(t *testing.T) {
	h := newTestHandler(t, preferenceDeps(nil, nil))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodGet, "/api/v2/audio-prefs/series-8f2c1a", "", owner)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"profile_id":"p-owner","series_id":"series-8f2c1a","audio_track_index":1,"audio_language":"eng","track_signature":{"language":"eng","title":"English 5.1","codec":"eac3","layout":"5.1","channels":6,"default":true},"updated_at":"2026-01-02T03:04:05.000Z"}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// A series with no preference is 404, as in v1.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/audio-prefs/series-none", "", owner), TypeNotFound)
	// Another profile's preference is not this profile's.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/audio-prefs/series-8f2c1a", "", with(bearer(adminToken), "X-Profile-Id", "p-primary")), TypeNotFound)
}

func TestUpdateAudioPreference(t *testing.T) {
	audio := &fakeAudioPreferences{}
	h := newTestHandler(t, preferenceDeps(audio, nil))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodPut, "/api/v2/audio-prefs/series-8f2c1a", `{"audio_track_index":2,"audio_language":"jpn","track_signature":{"language":"jpn","codec":"aac","channels":2}}`, owner)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got := audio.last
	if got == nil || got.ProfileID != "p-owner" || got.SeriesID != "series-8f2c1a" || got.AudioTrackIndex != 2 || got.AudioLanguage != "jpn" {
		t.Fatalf("stored = %+v", got)
	}
	// The signature crosses the seam member for member; an absent default is false.
	if s := got.TrackSignature; s == nil || s.Language != "jpn" || s.Codec != "aac" || s.Channels != 2 || s.Default || s.Title != "" {
		t.Fatalf("signature = %+v", got.TrackSignature)
	}
	// The read-back is what was stored.
	rec = do(t, h, http.MethodGet, "/api/v2/audio-prefs/series-8f2c1a", "", owner)
	want := `{"profile_id":"p-owner","series_id":"series-8f2c1a","audio_track_index":2,"audio_language":"jpn","track_signature":{"language":"jpn","codec":"aac","channels":2,"default":false},"updated_at":"2026-01-02T03:04:05.000Z"}` + "\n"
	if rec.Code != 200 || rec.Body.String() != want {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Only the index is required; an omitted signature stores none.
	rec = do(t, h, http.MethodPut, "/api/v2/audio-prefs/series-8f2c1a", `{"audio_track_index":0}`, owner)
	if rec.Code != 204 || audio.last.TrackSignature != nil || audio.last.AudioLanguage != "" {
		t.Fatalf("%d %+v", rec.Code, audio.last)
	}
}

func TestUpdateAudioPreferenceValidation(t *testing.T) {
	audio := &fakeAudioPreferences{}
	h := newTestHandler(t, preferenceDeps(audio, nil))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	for _, tc := range []struct{ body, location, code string }{
		{`{"audio_language":"eng"}`, "body.audio_track_index", codeRequired},
		{`{"audio_track_index":-2}`, "body.audio_track_index", codeOutOfRange},
		{`{"audio_track_index":"1"}`, "body.audio_track_index", codeInvalidType},
		{`{"audio_track_index":1,"track_signature":{"bitrate":1}}`, "body.track_signature.bitrate", codeUnknownField},
		{`{"audio_track_index":1,"track_signature":{"channels":"6"}}`, "body.track_signature.channels", codeInvalidType},
		{`{"audio_track_index":1,"series_id":"x"}`, "body.series_id", codeUnknownField},
	} {
		rec := do(t, h, http.MethodPut, "/api/v2/audio-prefs/series-8f2c1a", tc.body, owner)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, body %s", tc.body, rec.Code, rec.Body.String())
			continue
		}
		doc := requireProblem(t, rec, TypeValidationFailed)
		if len(doc.Errors) != 1 || doc.Errors[0].Location != tc.location || doc.Errors[0].Code != tc.code {
			t.Errorf("%s: errors = %+v", tc.body, doc.Errors)
		}
	}
	if audio.last != nil {
		t.Fatalf("a rejected body reached the seam: %+v", audio.last)
	}
	// A language the canonical settings contract refuses is the seam's
	// 400 naming the member, rendered as a 422 at body.audio_language.
	audio.err = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "playback.audio_language: too long", Field: "audio_language"}
	doc := requireProblem(t, do(t, h, http.MethodPut, "/api/v2/audio-prefs/series-8f2c1a", `{"audio_track_index":1,"audio_language":"not-a-language"}`, owner), TypeValidationFailed)
	if len(doc.Errors) != 1 || doc.Errors[0].Location != "body.audio_language" || doc.Errors[0].Code != codeInvalid {
		t.Fatalf("errors = %+v", doc.Errors)
	}
}

func TestDeleteAudioPreference(t *testing.T) {
	h := newTestHandler(t, preferenceDeps(nil, nil))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodDelete, "/api/v2/audio-prefs/series-8f2c1a", "", owner)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/audio-prefs/series-8f2c1a", "", owner), TypeNotFound)
	// Deleting what is not there is still a 204, as in v1.
	if rec := do(t, h, http.MethodDelete, "/api/v2/audio-prefs/series-8f2c1a", "", owner); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestAudioPreferenceDenied(t *testing.T) {
	h := newTestHandler(t, preferenceDeps(nil, nil))
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		body := ""
		if method == http.MethodPut {
			body = `{"audio_track_index":1}`
		}
		requireProblem(t, do(t, h, method, "/api/v2/audio-prefs/series-8f2c1a", body, nil), TypeAuthenticationRequired)
		// Profile scoped: the header is required, and a locked profile needs its token.
		requireProblem(t, do(t, h, method, "/api/v2/audio-prefs/series-8f2c1a", body, bearer(memberToken)), TypeValidationFailed)
		requireProblem(t, do(t, h, method, "/api/v2/audio-prefs/series-8f2c1a", body, with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	}
	// Demo mode refuses the mutations to a non-admin and leaves the read alone.
	demo := preferenceDeps(nil, nil)
	demo.DemoSettings = fakeSettings{demo: true}
	dh := newTestHandler(t, demo)
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	requireProblem(t, do(t, dh, http.MethodPut, "/api/v2/audio-prefs/series-8f2c1a", `{"audio_track_index":1}`, owner), TypePermissionDenied)
	requireProblem(t, do(t, dh, http.MethodDelete, "/api/v2/audio-prefs/series-8f2c1a", "", owner), TypePermissionDenied)
	if rec := do(t, dh, http.MethodGet, "/api/v2/audio-prefs/series-8f2c1a", "", owner); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Without the seam the operations fail closed.
	bare := preferenceDeps(nil, nil)
	bare.AudioPreferences = nil
	requireProblem(t, do(t, newTestHandler(t, bare), http.MethodGet, "/api/v2/audio-prefs/series-8f2c1a", "", owner), TypeDependencyUnavailable)
}

func TestListLibraryPlaybackPreferences(t *testing.T) {
	h := newTestHandler(t, preferenceDeps(nil, nil))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodGet, "/api/v2/library-playback-prefs", "", owner)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	// Only the acting profile's rows; an override the profile has not set is
	// absent, not null; no page member on the bounded collection.
	want := `{"items":[{"profile_id":"p-owner","library_id":"1","audio_language":"en","subtitle_mode":"always","updated_at":"2026-01-02T03:04:05.000Z"},{"profile_id":"p-owner","library_id":"3","show_forced_subtitles":true,"updated_at":"2026-01-01T00:00:00.000Z"}]}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// A profile with none gets an empty array, never null.
	rec = do(t, h, http.MethodGet, "/api/v2/library-playback-prefs", "", with(bearer(adminToken), "X-Profile-Id", "p-primary"))
	if rec.Code != 200 || rec.Body.String() != `{"items":[]}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// The v1 offset/limit parameters are not part of an unpaginated collection.
	doc := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/library-playback-prefs?limit=10", "", owner), TypeValidationFailed)
	if len(doc.Errors) != 1 || doc.Errors[0].Code != codeUnknownParameter {
		t.Fatalf("errors = %+v", doc.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/library-playback-prefs", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/library-playback-prefs", "", nil), TypeAuthenticationRequired)
}

func TestDeleteLibraryPlaybackPreference(t *testing.T) {
	library := &fakeLibraryPlaybackPreferences{known: map[int]bool{1: true, 2: true, 3: true}, prefs: fixtureLibraryPlaybackPreferences()}
	h := newTestHandler(t, preferenceDeps(nil, library))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodDelete, "/api/v2/library-playback-prefs/1", "", owner)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// The other profile's row for the same library is untouched.
	if len(library.prefs) != 2 || library.prefs[1].ProfileID != "p-other" {
		t.Fatalf("prefs = %+v", library.prefs)
	}
	// A library with no override is still a 204; an unknown library is 404.
	if rec := do(t, h, http.MethodDelete, "/api/v2/library-playback-prefs/2", "", owner); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/library-playback-prefs/9", "", owner), TypeNotFound)
	// An identifier no library can carry fails validation at the path.
	for _, id := range []string{"abc", "0", "-1"} {
		doc := requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/library-playback-prefs/"+id, "", owner), TypeValidationFailed)
		if len(doc.Errors) != 1 || doc.Errors[0].Location != locationPathLibraryID {
			t.Fatalf("%s: errors = %+v", id, doc.Errors)
		}
	}
	// Class denials and demo mode.
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/library-playback-prefs/1", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/library-playback-prefs/1", "", with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	demo := preferenceDeps(nil, nil)
	demo.DemoSettings = fakeSettings{demo: true}
	requireProblem(t, do(t, newTestHandler(t, demo), http.MethodDelete, "/api/v2/library-playback-prefs/1", "", owner), TypePermissionDenied)
}

func TestGetSubtitlePreference(t *testing.T) {
	h := newTestHandler(t, preferenceDeps(nil, nil))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodGet, "/api/v2/subtitle-prefs/series-8f2c1a", "", owner)
	want := `{"profile_id":"p-owner","series_id":"series-8f2c1a","subtitle_language":"eng","subtitle_track_index":0,"subtitle_mode":"always","track_signature":{"source":"embedded","language":"eng","codec":"subrip","label":"English (SDH)","forced":false,"hearing_impaired":true},"show_forced_subtitles":true,"updated_at":"2026-01-02T03:04:05.000Z"}` + "\n"
	if rec.Code != 200 || rec.Body.String() != want {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// A series the profile has no preference for is 404, as in v1.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/subtitle-prefs/series-none", "", owner), TypeNotFound)
	// Another profile on the same account does not see the owner's preference.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/subtitle-prefs/series-8f2c1a", "", with(bearer(memberToken), "X-Profile-Id", "p-kid")), TypeNotFound)
}

func TestUpdateSubtitlePreference(t *testing.T) {
	subs := &fakeSubtitlePreferences{}
	deps := preferenceDeps(nil, nil)
	deps.SubtitlePreferences = subs
	h := newTestHandler(t, deps)
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodPut, "/api/v2/subtitle-prefs/series-8f2c1a", `{"subtitle_track_index":2,"subtitle_language":"jpn","subtitle_mode":"always","external_subtitle_path":"ep.jpn.srt","track_signature":{"source":"external","language":"jpn","forced":true},"show_forced_subtitles":false}`, owner)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got := subs.last
	if got == nil || got.ProfileID != "p-owner" || got.SeriesID != "series-8f2c1a" || got.SubtitleTrackIndex != 2 || got.SubtitleLanguage != "jpn" || got.SubtitleMode != "always" || got.ExternalSubtitlePath != "ep.jpn.srt" {
		t.Fatalf("stored = %+v", got)
	}
	// The signature crosses the seam member for member; an absent flag is false.
	if s := got.TrackSignature; s == nil || s.Source != "external" || s.Language != "jpn" || !s.Forced || s.HearingImpaired || s.Codec != "" {
		t.Fatalf("signature = %+v", got.TrackSignature)
	}
	// An explicit false forced override is an override, not an omission.
	if !got.HasShowForcedSubtitles || got.ShowForcedSubtitles {
		t.Fatalf("forced = %+v", got)
	}
	// The read-back is what was stored.
	rec = do(t, h, http.MethodGet, "/api/v2/subtitle-prefs/series-8f2c1a", "", owner)
	want := `{"profile_id":"p-owner","series_id":"series-8f2c1a","subtitle_language":"jpn","subtitle_track_index":2,"external_subtitle_path":"ep.jpn.srt","subtitle_mode":"always","track_signature":{"source":"external","language":"jpn","forced":true,"hearing_impaired":false},"show_forced_subtitles":false,"updated_at":"2026-01-02T03:04:05.000Z"}` + "\n"
	if rec.Code != 200 || rec.Body.String() != want {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Only the index is required; an omitted forced flag crosses the seam as
	// "no override" so the seam can keep the stored one, everything else is none.
	rec = do(t, h, http.MethodPut, "/api/v2/subtitle-prefs/series-8f2c1a", `{"subtitle_track_index":0}`, owner)
	if rec.Code != 204 || subs.last.TrackSignature != nil || subs.last.SubtitleLanguage != "" || subs.last.SubtitleMode != "" {
		t.Fatalf("%d %+v", rec.Code, subs.last)
	}
	if !subs.last.HasShowForcedSubtitles || subs.last.ShowForcedSubtitles {
		t.Fatalf("forced override not kept: %+v", subs.last)
	}
	// -1 is the "subtitles off" sentinel every v1 client stores; it reaches
	// the seam unchanged.
	rec = do(t, h, http.MethodPut, "/api/v2/subtitle-prefs/series-8f2c1a", `{"subtitle_track_index":-1,"subtitle_mode":"off"}`, owner)
	if rec.Code != 204 || subs.last.SubtitleTrackIndex != -1 || subs.last.SubtitleMode != "off" {
		t.Fatalf("%d %+v", rec.Code, subs.last)
	}
}

func TestUpdateSubtitlePreferenceValidation(t *testing.T) {
	subs := &fakeSubtitlePreferences{}
	deps := preferenceDeps(nil, nil)
	deps.SubtitlePreferences = subs
	h := newTestHandler(t, deps)
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	for _, tc := range []struct{ body, location, code string }{
		{`{"subtitle_language":"eng"}`, "body.subtitle_track_index", codeRequired},
		{`{"subtitle_track_index":-2}`, "body.subtitle_track_index", codeOutOfRange},
		{`{"subtitle_track_index":"1"}`, "body.subtitle_track_index", codeInvalidType},
		{`{"subtitle_track_index":1,"track_signature":{"title":"x"}}`, "body.track_signature.title", codeUnknownField},
		{`{"subtitle_track_index":1,"track_signature":{"forced":"yes"}}`, "body.track_signature.forced", codeInvalidType},
		{`{"subtitle_track_index":1,"series_id":"x"}`, "body.series_id", codeUnknownField},
	} {
		rec := do(t, h, http.MethodPut, "/api/v2/subtitle-prefs/series-8f2c1a", tc.body, owner)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, body %s", tc.body, rec.Code, rec.Body.String())
			continue
		}
		doc := requireProblem(t, rec, TypeValidationFailed)
		if len(doc.Errors) != 1 || doc.Errors[0].Location != tc.location || doc.Errors[0].Code != tc.code {
			t.Errorf("%s: errors = %+v", tc.body, doc.Errors)
		}
	}
	if subs.last != nil {
		t.Fatalf("a rejected body reached the seam: %+v", subs.last)
	}
	// A value the canonical settings contract refuses is the seam's 400,
	// which carries no member and renders as malformed_request, as v1's does.
	subs.err = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "playback.subtitle_mode: unsupported"}
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/subtitle-prefs/series-8f2c1a", `{"subtitle_track_index":1,"subtitle_mode":"sometimes"}`, owner), TypeMalformedRequest)
}

func TestDeleteSubtitlePreference(t *testing.T) {
	subs := &fakeSubtitlePreferences{prefs: map[string]userstore.SubtitlePreference{"p-owner/series-8f2c1a": fixtureSubtitlePreference()}}
	deps := preferenceDeps(nil, nil)
	deps.SubtitlePreferences = subs
	h := newTestHandler(t, deps)
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodDelete, "/api/v2/subtitle-prefs/series-8f2c1a", "", owner)
	if rec.Code != 204 || rec.Body.Len() != 0 || len(subs.prefs) != 0 {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), subs.prefs)
	}
	// Deleting a preference that does not exist still succeeds, as in v1.
	if rec := do(t, h, http.MethodDelete, "/api/v2/subtitle-prefs/series-8f2c1a", "", owner); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/subtitle-prefs/series-8f2c1a", "", owner), TypeNotFound)
}

func TestSubtitlePreferenceDenied(t *testing.T) {
	h := newTestHandler(t, preferenceDeps(nil, nil))
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		body := ""
		if method == http.MethodPut {
			body = `{"subtitle_track_index":1}`
		}
		requireProblem(t, do(t, h, method, "/api/v2/subtitle-prefs/series-8f2c1a", body, nil), TypeAuthenticationRequired)
		// Profile scoped: the header is required, and a locked profile needs its token.
		requireProblem(t, do(t, h, method, "/api/v2/subtitle-prefs/series-8f2c1a", body, bearer(memberToken)), TypeValidationFailed)
		requireProblem(t, do(t, h, method, "/api/v2/subtitle-prefs/series-8f2c1a", body, with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	}
	// Demo mode refuses the mutations to a non-admin and leaves the read alone.
	demo := preferenceDeps(nil, nil)
	demo.DemoSettings = fakeSettings{demo: true}
	dh := newTestHandler(t, demo)
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	requireProblem(t, do(t, dh, http.MethodPut, "/api/v2/subtitle-prefs/series-8f2c1a", `{"subtitle_track_index":1}`, owner), TypePermissionDenied)
	requireProblem(t, do(t, dh, http.MethodDelete, "/api/v2/subtitle-prefs/series-8f2c1a", "", owner), TypePermissionDenied)
	if rec := do(t, dh, http.MethodGet, "/api/v2/subtitle-prefs/series-8f2c1a", "", owner); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Without the seam the operations fail closed.
	bare := preferenceDeps(nil, nil)
	bare.SubtitlePreferences = nil
	requireProblem(t, do(t, newTestHandler(t, bare), http.MethodGet, "/api/v2/subtitle-prefs/series-8f2c1a", "", owner), TypeDependencyUnavailable)
}

func TestUpdateLibraryPlaybackPreference(t *testing.T) {
	library := &fakeLibraryPlaybackPreferences{known: map[int]bool{1: true, 2: true, 3: true}, prefs: fixtureLibraryPlaybackPreferences()}
	h := newTestHandler(t, preferenceDeps(nil, library))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	// A partial body: only the present members reach the seam — the handler
	// never reads the stored row itself, the seam merges onto the canonical
	// rows inside its transaction — a value sets one, null clears one.
	rec := do(t, h, http.MethodPatch, "/api/v2/library-playback-prefs/1", `{"subtitle_language":"fr","subtitle_mode":null,"show_forced_subtitles":true}`, owner)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got := library.last
	if got == nil || got.AudioLanguage.Present {
		t.Fatalf("omitted audio_language reached the seam: %+v", got)
	}
	if !got.SubtitleLanguage.Present || got.SubtitleLanguage.Value == nil || *got.SubtitleLanguage.Value != "fr" ||
		!got.ShowForcedSubtitles.Present || got.ShowForcedSubtitles.Value == nil || !*got.ShowForcedSubtitles.Value {
		t.Fatalf("present members not applied: %+v", got)
	}
	// Null on any member is present with no value, never the seam's
	// empty-string spelling, which the store would keep as a row.
	if !got.SubtitleMode.Present || got.SubtitleMode.Value != nil {
		t.Fatalf("null subtitle_mode = %+v", got.SubtitleMode)
	}
	// The row the seam keeps holds the untouched member alongside the patch.
	rows, _ := library.ListLibraryPlaybackPreferencesCanonical(context.Background(), 0, "p-owner")
	if rows[0].LibraryID != 1 || !rows[0].HasAudioLanguage || rows[0].AudioLanguage != "en" || rows[0].SubtitleLanguage != "fr" || rows[0].HasSubtitleMode || !rows[0].ShowForcedSubtitles {
		t.Fatalf("row = %+v", rows[0])
	}
	// "" clears a string member exactly as null does, as the contract
	// documents; the member is present with no value.
	rec = do(t, h, http.MethodPatch, "/api/v2/library-playback-prefs/1", `{"subtitle_language":""}`, owner)
	if rec.Code != 204 || !library.last.SubtitleLanguage.Present || library.last.SubtitleLanguage.Value != nil {
		t.Fatalf("%d %+v", rec.Code, library.last.SubtitleLanguage)
	}
	rows, _ = library.ListLibraryPlaybackPreferencesCanonical(context.Background(), 0, "p-owner")
	if rows[0].HasSubtitleLanguage {
		t.Fatalf("\"\" left the override: %+v", rows[0])
	}
	// Clearing every override — null or "" alike — reaches the seam with
	// every member present and empty, its row-removal form, and the row
	// leaves the list.
	rec = do(t, h, http.MethodPatch, "/api/v2/library-playback-prefs/1", `{"audio_language":"","subtitle_language":null,"subtitle_mode":"","show_forced_subtitles":null}`, owner)
	if rec.Code != 204 || library.last == nil ||
		!library.last.AudioLanguage.Present || library.last.AudioLanguage.Value != nil ||
		!library.last.SubtitleLanguage.Present || library.last.SubtitleLanguage.Value != nil ||
		!library.last.SubtitleMode.Present || library.last.SubtitleMode.Value != nil ||
		!library.last.ShowForcedSubtitles.Present || library.last.ShowForcedSubtitles.Value != nil {
		t.Fatalf("%d %+v", rec.Code, library.last)
	}
	rows, _ = library.ListLibraryPlaybackPreferencesCanonical(context.Background(), 0, "p-owner")
	if len(rows) != 1 || rows[0].LibraryID != 3 {
		t.Fatalf("cleared row still listed: %+v", rows)
	}
	rec = do(t, h, http.MethodPatch, "/api/v2/library-playback-prefs/3", `{"show_forced_subtitles":null}`, owner)
	if rec.Code != 204 || library.last.ShowForcedSubtitles.Value != nil || library.last.AudioLanguage.Present {
		t.Fatalf("%d %+v", rec.Code, library.last)
	}
	// A library with no row yet: only the body's members reach the seam.
	rec = do(t, h, http.MethodPatch, "/api/v2/library-playback-prefs/2", `{"audio_language":"de"}`, owner)
	if rec.Code != 204 || library.last.AudioLanguage.Value == nil || *library.last.AudioLanguage.Value != "de" || library.last.SubtitleLanguage.Present || library.last.SubtitleMode.Present || library.last.ShowForcedSubtitles.Present {
		t.Fatalf("%d %+v", rec.Code, library.last)
	}
	// An empty body is a valid no-op patch.
	if rec := do(t, h, http.MethodPatch, "/api/v2/library-playback-prefs/2", `{}`, owner); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// An unknown library is 404; an identifier no library can carry fails validation at the path.
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/library-playback-prefs/9", `{"audio_language":"de"}`, owner), TypeNotFound)
	doc := requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/library-playback-prefs/abc", `{"audio_language":"de"}`, owner), TypeValidationFailed)
	if len(doc.Errors) != 1 || doc.Errors[0].Location != locationPathLibraryID {
		t.Fatalf("errors = %+v", doc.Errors)
	}
}

func TestUpdateLibraryPlaybackPreferenceValidation(t *testing.T) {
	library := &fakeLibraryPlaybackPreferences{known: map[int]bool{1: true}}
	h := newTestHandler(t, preferenceDeps(nil, library))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	for _, tc := range []struct{ body, location, code string }{
		{`{"show_forced_subtitles":"yes"}`, "body.show_forced_subtitles", codeInvalidType},
		{`{"audio_language":1}`, "body.audio_language", codeInvalidType},
		{`{"library_id":"1"}`, "body.library_id", codeUnknownField},
	} {
		rec := do(t, h, http.MethodPatch, "/api/v2/library-playback-prefs/1", tc.body, owner)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, body %s", tc.body, rec.Code, rec.Body.String())
			continue
		}
		doc := requireProblem(t, rec, TypeValidationFailed)
		if len(doc.Errors) != 1 || doc.Errors[0].Location != tc.location || doc.Errors[0].Code != tc.code {
			t.Errorf("%s: errors = %+v", tc.body, doc.Errors)
		}
	}
	if library.last != nil {
		t.Fatalf("a rejected body reached the seam: %+v", library.last)
	}
	// A member the seam refuses (a subtitle_mode outside auto/always/off, or a
	// value the canonical contract rejects) is its 400 naming the member,
	// rendered as a 422 at body.<member>.
	library.err = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Invalid subtitle_mode", Field: "subtitle_mode"}
	doc := requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/library-playback-prefs/1", `{"subtitle_mode":"sometimes"}`, owner), TypeValidationFailed)
	if len(doc.Errors) != 1 || doc.Errors[0].Location != "body.subtitle_mode" || doc.Errors[0].Code != codeInvalid {
		t.Fatalf("errors = %+v", doc.Errors)
	}
	// Class denials and demo mode.
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/library-playback-prefs/1", `{}`, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/library-playback-prefs/1", `{}`, with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	demo := preferenceDeps(nil, nil)
	demo.DemoSettings = fakeSettings{demo: true}
	requireProblem(t, do(t, newTestHandler(t, demo), http.MethodPatch, "/api/v2/library-playback-prefs/1", `{}`, owner), TypePermissionDenied)
}

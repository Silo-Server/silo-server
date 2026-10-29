package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	contracts "github.com/Silo-Server/silo-server/contracts/api/v2"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/lang"
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

func TestGetPreferencesAcceptsMissingLegacyTimestamp(t *testing.T) {
	for _, timestamp := range []string{"", "not-a-timestamp"} {
		t.Run(timestamp, func(t *testing.T) {
			deps := preferenceDeps(&fakeAudioPreferences{prefs: map[string]userstore.AudioPreference{
				"p-owner/series-old": {ProfileID: "p-owner", SeriesID: "series-old", AudioTrackIndex: 2, UpdatedAt: timestamp},
			}}, nil)
			deps.SubtitlePreferences = &fakeSubtitlePreferences{prefs: map[string]userstore.SubtitlePreference{
				"p-owner/series-old": {ProfileID: "p-owner", SeriesID: "series-old", SubtitleTrackIndex: 3, UpdatedAt: timestamp},
			}}
			h := newTestHandler(t, deps)
			owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")
			for _, route := range []string{"/api/v2/audio-prefs/series-old", "/api/v2/subtitle-prefs/series-old"} {
				rec := do(t, h, http.MethodGet, route, "", owner)
				if timestamp != "" {
					requireProblem(t, rec, TypeInternalError)
					continue
				}
				var body struct {
					UpdatedAt string `json:"updated_at"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if rec.Code != http.StatusOK || body.UpdatedAt != "1970-01-01T00:00:00.000Z" {
					t.Fatalf("%s: %d %s", route, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

// The language tag patterns are hand-copied into many struct tags; they must
// agree with lang.CanonicalTag, which the handlers use. A request pattern
// admits every value the handler accepts, and a response pattern admits what
// the handler stores.
func TestLanguagePatternsAgreeWithCanonicalTag(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Pattern string `json:"pattern"`
					Items   *struct {
						Pattern string `json:"pattern"`
					} `json:"items"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(contracts.OpenAPI, &doc); err != nil {
		t.Fatal(err)
	}
	// Every language or country pattern is one of these, so a copy that drifts
	// fails here rather than refusing a value the handler accepts.
	known := map[string]bool{
		`^(?:[a-z]{2,3}(?:-[0-9A-Za-z]{1,8})*|x(?:-[0-9A-Za-z]{1,8})+)$`:                                                true,
		`^(?:[a-z]{2,3}(?:-[0-9A-Za-z]{1,8})*|x(?:-[0-9A-Za-z]{1,8})+)?$`:                                               true,
		`^[\s\v\x85\p{Z}]*(?:[A-Za-z]{2,3}(?:[-_][0-9A-Za-z]{1,8})*|[XxIi](?:[-_][0-9A-Za-z]{1,8})+)[\s\v\x85\p{Z}]*$`:  true,
		`^[\s\v\x85\p{Z}]*(?:[A-Za-z]{2,3}(?:[-_][0-9A-Za-z]{1,8})*|[XxIi](?:[-_][0-9A-Za-z]{1,8})+)?[\s\v\x85\p{Z}]*$`: true,
		`^(?:[a-z]{2})?$`: true, `^[a-z]{2,3}$`: true, `^[A-Z]{2}$`: true,
	}
	for name, schema := range doc.Components.Schemas {
		for member, property := range schema.Properties {
			p := property.Pattern
			if property.Items != nil {
				p = property.Items.Pattern
			}
			if p != "" && (strings.Contains(member, "language") || strings.Contains(member, "countr")) && !known[p] {
				t.Errorf("%s.%s: unknown language pattern %q", name, member, p)
			}
		}
	}
	pattern := func(schema, member string) *regexp.Regexp {
		p := doc.Components.Schemas[schema].Properties[member].Pattern
		if p == "" {
			t.Fatalf("%s.%s declares no pattern", schema, member)
		}
		return regexp.MustCompile(p)
	}
	request, response := pattern("ProfileUpdate", "language"), pattern("Profile", "language")
	for _, value := range []string{"en", "eng", "EN", " en ", "pt-BR", "pt_br", "zh-Hant-TW", "es-419", "fil", "und",
		"x-silo-original", "i-klingon", "sgn-BE-FR", "ar-EG-u-nu-latn", "", "English", "original", "00", "x",
		// strings.TrimSpace trims Unicode spaces, not only ASCII ones.
		"\u00a0en\u00a0", "\ven", "\u2003en\u3000", "\u0085", "\u00a0"} {
		canonical := lang.CanonicalTag(value)
		if strings.TrimSpace(value) != "" && canonical == "" {
			continue // the handler refuses it; the pattern may too
		}
		if !request.MatchString(value) {
			t.Errorf("request pattern refuses %q, which the handler accepts", value)
		}
		if !response.MatchString(canonical) {
			t.Errorf("response pattern refuses %q, the canonical form of %q", canonical, value)
		}
	}
}

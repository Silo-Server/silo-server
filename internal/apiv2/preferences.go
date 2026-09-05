package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The preferences domain: a profile's per-series and per-library playback
// preferences. Playback matches a remembered track by its signature, so the
// signature schemas keep the store's member names and optionality exactly.

// AudioTrackSignature identifies an audio track independently of its index.
// Every member is optional; absent and the zero value mean the same thing.
type AudioTrackSignature struct {
	Language      string `json:"language,omitempty" doc:"Track language (ISO 639)" example:"eng"`
	Title         string `json:"title,omitempty" doc:"Display title the client showed" example:"English 5.1"`
	EmbeddedTitle string `json:"embedded_title,omitempty" doc:"Title embedded in the container" example:"Surround"`
	Codec         string `json:"codec,omitempty" example:"eac3"`
	Layout        string `json:"layout,omitempty" doc:"Channel layout" example:"5.1"`
	Channels      int    `json:"channels,omitempty" minimum:"0" example:"6"`
	Default       *bool  `json:"default,omitempty" nullable:"false" doc:"Whether the track carried the default flag" example:"true"`
}

// AudioPreference is the acting profile's remembered audio track for a series.
type AudioPreference struct {
	ProfileID       ID                   `json:"profile_id" example:"p-1"`
	SeriesID        ID                   `json:"series_id" doc:"The series content id" example:"series-8f2c1a"`
	AudioTrackIndex int                  `json:"audio_track_index" doc:"Zero-based index of the chosen track, or -1 for no track" example:"1"`
	AudioLanguage   string               `json:"audio_language" doc:"Preferred audio language; empty means no language preference" example:"eng"`
	TrackSignature  *AudioTrackSignature `json:"track_signature,omitempty" nullable:"false" doc:"Absent when the client stored none"`
	UpdatedAt       Instant              `json:"updated_at" example:"2026-01-02T03:04:05.000Z"`
}

// AudioPreferenceUpdate is the updateAudioPreference body. The whole
// preference is replaced: an omitted audio_language or track_signature is
// stored as none.
type AudioPreferenceUpdate struct {
	AudioTrackIndex int                  `json:"audio_track_index" minimum:"-1" doc:"Zero-based index of the chosen track, or -1 for no track" example:"1"`
	AudioLanguage   string               `json:"audio_language,omitempty" doc:"Preferred audio language; empty or omitted means no language preference" example:"eng"`
	TrackSignature  *AudioTrackSignature `json:"track_signature,omitempty" nullable:"false" doc:"How playback recognizes the track when indexes shift"`
}

// AudioPreferenceInput addresses one series' audio preference.
type AudioPreferenceInput struct {
	SeriesID ID `path:"series_id" doc:"The series content id" example:"series-8f2c1a"`
}

// AudioPreferenceUpdateInput is the updateAudioPreference request.
type AudioPreferenceUpdateInput struct {
	SeriesID ID `path:"series_id" doc:"The series content id" example:"series-8f2c1a"`
	Body     AudioPreferenceUpdate
}

// AudioPreferenceOutput is a single-preference response.
type AudioPreferenceOutput struct {
	Body AudioPreference
}

// SubtitleTrackSignature identifies a subtitle track independently of its
// index. Every member is optional; absent and the zero value mean the same
// thing.
type SubtitleTrackSignature struct {
	Source          string `json:"source,omitempty" doc:"Where the track comes from, e.g. embedded or external" example:"embedded"`
	Language        string `json:"language,omitempty" doc:"Track language (ISO 639)" example:"eng"`
	Codec           string `json:"codec,omitempty" example:"subrip"`
	Label           string `json:"label,omitempty" doc:"Display label the client showed" example:"English (SDH)"`
	Forced          *bool  `json:"forced,omitempty" nullable:"false" doc:"Whether the track carried the forced flag" example:"false"`
	HearingImpaired *bool  `json:"hearing_impaired,omitempty" nullable:"false" doc:"Whether the track carried the hearing-impaired flag" example:"true"`
}

// SubtitlePreference is the acting profile's remembered subtitle choice for
// a series.
type SubtitlePreference struct {
	ProfileID            ID                      `json:"profile_id" example:"p-1"`
	SeriesID             ID                      `json:"series_id" doc:"The series content id" example:"series-8f2c1a"`
	SubtitleLanguage     string                  `json:"subtitle_language" doc:"Preferred subtitle language; empty means no language preference" example:"eng"`
	SubtitleTrackIndex   int                     `json:"subtitle_track_index" doc:"Zero-based index of the chosen track, or -1 for subtitles off" example:"0"`
	ExternalSubtitlePath string                  `json:"external_subtitle_path,omitempty" doc:"Path of the chosen external subtitle file; absent for an embedded track" example:"Show.S01E01.eng.srt"`
	SubtitleMode         string                  `json:"subtitle_mode" doc:"Subtitle mode. Canonical values: auto, always, off; empty means no mode preference" example:"always"`
	TrackSignature       *SubtitleTrackSignature `json:"track_signature,omitempty" nullable:"false" doc:"Absent when the client stored none"`
	ShowForcedSubtitles  *bool                   `json:"show_forced_subtitles,omitempty" nullable:"false" doc:"Forced-subtitle override; absent when the profile has none for the series" example:"true"`
	UpdatedAt            Instant                 `json:"updated_at" example:"2026-01-02T03:04:05.000Z"`
}

// SubtitlePreferenceUpdate is the updateSubtitlePreference body. The
// preference is replaced: an omitted language, mode, path or signature is
// stored as none. An omitted show_forced_subtitles keeps the override already
// stored, so a client changing only the track does not reset it.
type SubtitlePreferenceUpdate struct {
	SubtitleTrackIndex   int                     `json:"subtitle_track_index" minimum:"-1" doc:"Zero-based index of the chosen track, or -1 for subtitles off" example:"0"`
	SubtitleLanguage     string                  `json:"subtitle_language,omitempty" doc:"Preferred subtitle language; empty or omitted means no language preference" example:"eng"`
	ExternalSubtitlePath string                  `json:"external_subtitle_path,omitempty" doc:"Path of the chosen external subtitle file, when the track is external" example:"Show.S01E01.eng.srt"`
	SubtitleMode         string                  `json:"subtitle_mode,omitempty" doc:"Subtitle mode: auto, always or off; empty or omitted means no mode preference" example:"always"`
	TrackSignature       *SubtitleTrackSignature `json:"track_signature,omitempty" nullable:"false" doc:"How playback recognizes the track when indexes shift"`
	ShowForcedSubtitles  *bool                   `json:"show_forced_subtitles,omitempty" nullable:"false" doc:"Forced-subtitle override for the series; omitted keeps the stored override" example:"true"`
}

// SubtitlePreferenceInput addresses one series' subtitle preference.
type SubtitlePreferenceInput struct {
	SeriesID ID `path:"series_id" doc:"The series content id" example:"series-8f2c1a"`
}

// SubtitlePreferenceUpdateInput is the updateSubtitlePreference request.
type SubtitlePreferenceUpdateInput struct {
	SeriesID ID `path:"series_id" doc:"The series content id" example:"series-8f2c1a"`
	Body     SubtitlePreferenceUpdate
}

// SubtitlePreferenceOutput is a single-preference response.
type SubtitlePreferenceOutput struct {
	Body SubtitlePreference
}

// LibraryPlaybackPreference is the acting profile's playback overrides for
// one library. A member is absent when the profile has no override for it.
type LibraryPlaybackPreference struct {
	ProfileID           ID      `json:"profile_id" example:"p-1"`
	LibraryID           ID      `json:"library_id" example:"1"`
	AudioLanguage       *string `json:"audio_language,omitempty" nullable:"false" doc:"Audio language override (ISO 639-1); empty means none" example:"en"`
	SubtitleLanguage    *string `json:"subtitle_language,omitempty" nullable:"false" doc:"Subtitle language override (ISO 639-1); empty means none" example:"en"`
	SubtitleMode        *string `json:"subtitle_mode,omitempty" nullable:"false" doc:"Subtitle mode override. Canonical values: auto, always, off; empty means none" example:"auto"`
	ShowForcedSubtitles *bool   `json:"show_forced_subtitles,omitempty" nullable:"false" example:"false"`
	UpdatedAt           Instant `json:"updated_at" example:"2026-01-02T03:04:05.000Z"`
}

// LibraryPlaybackPreferenceCollection is the named envelope the contract
// carries; the per-profile set is bounded by the library count and is not
// paginated.
type LibraryPlaybackPreferenceCollection struct {
	Collection[LibraryPlaybackPreference]
}

// LibraryPlaybackPreferenceCollectionOutput is the
// listLibraryPlaybackPreferences response.
type LibraryPlaybackPreferenceCollectionOutput struct {
	Body LibraryPlaybackPreferenceCollection
}

// LibraryPlaybackPreferenceInput addresses one library's preference.
type LibraryPlaybackPreferenceInput struct {
	LibraryID ID `path:"library_id" doc:"The library" example:"1"`
}

// LibraryPlaybackPreferenceUpdate is the updateLibraryPlaybackPreference
// body: a partial update of the four overrides. An omitted member is
// unchanged; explicit null (or "" on a string member) clears that override;
// a value sets it. Clearing every override removes the library's row.
type LibraryPlaybackPreferenceUpdate struct {
	AudioLanguage       Patch[string] `json:"audio_language,omitzero" doc:"Audio language override (ISO 639-1); null or empty clears it" example:"en"`
	SubtitleLanguage    Patch[string] `json:"subtitle_language,omitzero" doc:"Subtitle language override (ISO 639-1); null or empty clears it" example:"en"`
	SubtitleMode        Patch[string] `json:"subtitle_mode,omitzero" doc:"Subtitle mode override: auto, always or off; null or empty clears it" example:"always"`
	ShowForcedSubtitles Patch[bool]   `json:"show_forced_subtitles,omitzero" doc:"Forced-subtitle override; null clears it" example:"true"`
}

// LibraryPlaybackPreferenceUpdateInput is the updateLibraryPlaybackPreference
// request.
type LibraryPlaybackPreferenceUpdateInput struct {
	LibraryID ID `path:"library_id" doc:"The library" example:"1"`
	Body      LibraryPlaybackPreferenceUpdate
}

const (
	tagPreferences = "preferences"
	// locationLibraryID is where a library_id path failure is reported.
	locationLibraryID = "path.library_id"

	pathAudioPreference           = Prefix + "/audio-prefs/{series_id}"
	pathSubtitlePreference        = Prefix + "/subtitle-prefs/{series_id}"
	pathLibraryPlaybackPreference = Prefix + "/library-playback-prefs"
)

func registerPreferences(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, pathAudioPreference, "getAudioPreference", tagPreferences,
			"Get the acting profile's remembered audio track for a series."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, reg.getAudioPreference)
	Register(reg, Operation{
		Operation: humaOp(http.MethodPut, pathAudioPreference, "updateAudioPreference", tagPreferences,
			"Replace the acting profile's remembered audio track for a series."),
		Class:          ClassProfileScoped,
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.updateAudioPreference)
	Register(reg, Operation{
		Operation: humaOp(http.MethodDelete, pathAudioPreference, "deleteAudioPreference", tagPreferences,
			"Forget the acting profile's remembered audio track for a series."),
		Class:          ClassProfileScoped,
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.deleteAudioPreference)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, pathSubtitlePreference, "getSubtitlePreference", tagPreferences,
			"Get the acting profile's remembered subtitle choice for a series."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, reg.getSubtitlePreference)
	Register(reg, Operation{
		Operation: humaOp(http.MethodPut, pathSubtitlePreference, "updateSubtitlePreference", tagPreferences,
			"Replace the acting profile's remembered subtitle choice for a series."),
		Class:          ClassProfileScoped,
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.updateSubtitlePreference)
	Register(reg, Operation{
		Operation: humaOp(http.MethodDelete, pathSubtitlePreference, "deleteSubtitlePreference", tagPreferences,
			"Forget the acting profile's remembered subtitle choice for a series."),
		Class:          ClassProfileScoped,
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.deleteSubtitlePreference)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, pathLibraryPlaybackPreference, "listLibraryPlaybackPreferences", tagPreferences,
			"List the acting profile's per-library playback overrides."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, reg.listLibraryPlaybackPreferences)
	Register(reg, Operation{
		Operation: humaOp(http.MethodPatch, pathLibraryPlaybackPreference+"/{library_id}", "updateLibraryPlaybackPreference", tagPreferences,
			"Change the acting profile's playback overrides for a library: omitted members are unchanged, null clears one."),
		Class:          ClassProfileScoped,
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.updateLibraryPlaybackPreference)
	Register(reg, Operation{
		Operation: humaOp(http.MethodDelete, pathLibraryPlaybackPreference+"/{library_id}", "deleteLibraryPlaybackPreference", tagPreferences,
			"Remove the acting profile's playback overrides for a library."),
		Class:          ClassProfileScoped,
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.deleteLibraryPlaybackPreference)
}

// getAudioPreference answers as v1 GET /audio-prefs/{series_id}: the stored
// preference, or 404 not_found when the profile has none for the series.
func (reg *Registry) getAudioPreference(ctx context.Context, in *AudioPreferenceInput) (*AudioPreferenceOutput, error) {
	if reg.deps.AudioPreferences == nil {
		return nil, unavailable("preference")
	}
	claims := claimsFrom(ctx)
	profileID := profileFrom(ctx)
	if claims == nil || profileID == "" {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	pref, err := reg.deps.AudioPreferences.GetAudioPreference(ctx, claims.UserID, profileID, string(in.SeriesID))
	if err != nil {
		return nil, preferenceProblem(err)
	}
	out, p := audioPreferenceOf(pref)
	if p != nil {
		return nil, p
	}
	return &AudioPreferenceOutput{Body: out}, nil
}

// updateAudioPreference stores the preference as v1 PUT /audio-prefs/{series_id}
// does and answers 204: the client already holds the document it sent.
func (reg *Registry) updateAudioPreference(ctx context.Context, in *AudioPreferenceUpdateInput) (*struct{}, error) {
	if reg.deps.AudioPreferences == nil {
		return nil, unavailable("preference")
	}
	claims := claimsFrom(ctx)
	profileID := profileFrom(ctx)
	if claims == nil || profileID == "" {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	pref := userstore.AudioPreference{
		ProfileID:       profileID,
		SeriesID:        string(in.SeriesID),
		AudioTrackIndex: in.Body.AudioTrackIndex,
		AudioLanguage:   in.Body.AudioLanguage,
	}
	if s := in.Body.TrackSignature; s != nil {
		pref.TrackSignature = &userstore.AudioTrackSignature{
			Language: s.Language, Title: s.Title, EmbeddedTitle: s.EmbeddedTitle,
			Codec: s.Codec, Layout: s.Layout, Channels: s.Channels,
			Default: s.Default != nil && *s.Default,
		}
	}
	if err := reg.deps.AudioPreferences.SetAudioPreference(ctx, claims.UserID, pref); err != nil {
		return nil, preferenceProblem(err)
	}
	return &struct{}{}, nil
}

// deleteAudioPreference forgets the preference as v1 DELETE
// /audio-prefs/{series_id} does; a series with none is still a 204.
func (reg *Registry) deleteAudioPreference(ctx context.Context, in *AudioPreferenceInput) (*struct{}, error) {
	if reg.deps.AudioPreferences == nil {
		return nil, unavailable("preference")
	}
	claims := claimsFrom(ctx)
	profileID := profileFrom(ctx)
	if claims == nil || profileID == "" {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if err := reg.deps.AudioPreferences.DeleteAudioPreference(ctx, claims.UserID, profileID, string(in.SeriesID)); err != nil {
		return nil, preferenceProblem(err)
	}
	return &struct{}{}, nil
}

// listLibraryPlaybackPreferences answers from the same listing v1 GET
// /library-playback-prefs uses, as a standard unpaginated collection.
func (reg *Registry) listLibraryPlaybackPreferences(ctx context.Context, _ *struct{}) (*LibraryPlaybackPreferenceCollectionOutput, error) {
	if reg.deps.LibraryPlaybackPreferences == nil {
		return nil, unavailable("preference")
	}
	claims := claimsFrom(ctx)
	profileID := profileFrom(ctx)
	if claims == nil || profileID == "" {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	prefs, err := reg.deps.LibraryPlaybackPreferences.ListLibraryPlaybackPreferences(ctx, claims.UserID, profileID)
	if err != nil {
		return nil, preferenceProblem(err)
	}
	items := make([]LibraryPlaybackPreference, 0, len(prefs))
	for _, p := range prefs {
		item, problem := libraryPlaybackPreferenceOf(p)
		if problem != nil {
			return nil, problem
		}
		items = append(items, item)
	}
	return &LibraryPlaybackPreferenceCollectionOutput{Body: LibraryPlaybackPreferenceCollection{Collection: NewCollection(items)}}, nil
}

// deleteLibraryPlaybackPreference removes the overrides as v1 DELETE
// /library-playback-prefs/{library_id} does: an unknown library is 404, a
// library with no overrides is still a 204.
func (reg *Registry) deleteLibraryPlaybackPreference(ctx context.Context, in *LibraryPlaybackPreferenceInput) (*struct{}, error) {
	if reg.deps.LibraryPlaybackPreferences == nil {
		return nil, unavailable("preference")
	}
	claims := claimsFrom(ctx)
	profileID := profileFrom(ctx)
	if claims == nil || profileID == "" {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	libraryID, p := libraryIDOf(in.LibraryID)
	if p != nil {
		return nil, p
	}
	if err := reg.deps.LibraryPlaybackPreferences.DeleteLibraryPlaybackPreference(ctx, claims.UserID, profileID, libraryID); err != nil {
		return nil, preferenceProblem(err)
	}
	return &struct{}{}, nil
}

// libraryIDOf recovers the library key from its opaque path form; anything
// but a positive integer names no library and fails validation.
func libraryIDOf(id ID) (int, *Problem) {
	n, err := intOfID(id)
	if err != nil || n <= 0 {
		return 0, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationLibraryID, Code: codeInvalid, Detail: "expected a library identifier"})
	}
	return n, nil
}

// preferenceProblem maps the v1 decision onto problem types: a rejected
// member is a validation failure naming it; other statuses carry through.
func preferenceProblem(err error) *Problem {
	apiErr, ok := err.(*handlers.APIError) //nolint:errorlint // the seams return the value directly
	if ok && apiErr.Field != "" {
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody + "." + apiErr.Field, Code: codeInvalid, Detail: apiErr.Message})
	}
	return serviceProblem(err)
}

func audioPreferenceOf(p userstore.AudioPreference) (AudioPreference, *Problem) {
	updated, problem := storeInstant(p.UpdatedAt)
	if problem != nil {
		return AudioPreference{}, problem
	}
	out := AudioPreference{
		ProfileID:       ID(p.ProfileID),
		SeriesID:        ID(p.SeriesID),
		AudioTrackIndex: p.AudioTrackIndex,
		AudioLanguage:   p.AudioLanguage,
		UpdatedAt:       updated,
	}
	if s := p.TrackSignature; s != nil {
		out.TrackSignature = &AudioTrackSignature{
			Language: s.Language, Title: s.Title, EmbeddedTitle: s.EmbeddedTitle,
			Codec: s.Codec, Layout: s.Layout, Channels: s.Channels, Default: ptr(s.Default),
		}
	}
	return out, nil
}

func libraryPlaybackPreferenceOf(p userstore.LibraryPlaybackPreference) (LibraryPlaybackPreference, *Problem) {
	updated, problem := storeInstant(p.UpdatedAt)
	if problem != nil {
		return LibraryPlaybackPreference{}, problem
	}
	out := LibraryPlaybackPreference{
		ProfileID: ID(p.ProfileID),
		LibraryID: IDFromInt(int64(p.LibraryID)),
		UpdatedAt: updated,
	}
	if p.HasAudioLanguage {
		out.AudioLanguage = ptr(p.AudioLanguage)
	}
	if p.HasSubtitleLanguage {
		out.SubtitleLanguage = ptr(p.SubtitleLanguage)
	}
	if p.HasSubtitleMode {
		out.SubtitleMode = ptr(p.SubtitleMode)
	}
	if p.HasShowForcedSubtitles {
		out.ShowForcedSubtitles = ptr(p.ShowForcedSubtitles)
	}
	return out, nil
}

// getSubtitlePreference answers as v1 GET /subtitle-prefs/{series_id}: the
// stored preference, or 404 not_found when the profile has none for the series.
func (reg *Registry) getSubtitlePreference(ctx context.Context, in *SubtitlePreferenceInput) (*SubtitlePreferenceOutput, error) {
	if reg.deps.SubtitlePreferences == nil {
		return nil, unavailable("preference")
	}
	claims := claimsFrom(ctx)
	profileID := profileFrom(ctx)
	if claims == nil || profileID == "" {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	pref, err := reg.deps.SubtitlePreferences.GetSubtitlePreference(ctx, claims.UserID, profileID, string(in.SeriesID))
	if err != nil {
		return nil, preferenceProblem(err)
	}
	out, p := subtitlePreferenceOf(pref)
	if p != nil {
		return nil, p
	}
	return &SubtitlePreferenceOutput{Body: out}, nil
}

// updateSubtitlePreference stores the preference as v1 PUT
// /subtitle-prefs/{series_id} does and answers 204: the client already holds
// the document it sent.
func (reg *Registry) updateSubtitlePreference(ctx context.Context, in *SubtitlePreferenceUpdateInput) (*struct{}, error) {
	if reg.deps.SubtitlePreferences == nil {
		return nil, unavailable("preference")
	}
	claims := claimsFrom(ctx)
	profileID := profileFrom(ctx)
	if claims == nil || profileID == "" {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	pref := userstore.SubtitlePreference{
		ProfileID:            profileID,
		SeriesID:             string(in.SeriesID),
		SubtitleLanguage:     in.Body.SubtitleLanguage,
		SubtitleTrackIndex:   in.Body.SubtitleTrackIndex,
		ExternalSubtitlePath: in.Body.ExternalSubtitlePath,
		SubtitleMode:         in.Body.SubtitleMode,
	}
	if s := in.Body.TrackSignature; s != nil {
		pref.TrackSignature = &userstore.SubtitleTrackSignature{
			Source: s.Source, Language: s.Language, Codec: s.Codec, Label: s.Label,
			Forced:          s.Forced != nil && *s.Forced,
			HearingImpaired: s.HearingImpaired != nil && *s.HearingImpaired,
		}
	}
	if f := in.Body.ShowForcedSubtitles; f != nil {
		pref.ShowForcedSubtitles = *f
		pref.HasShowForcedSubtitles = true
	}
	if err := reg.deps.SubtitlePreferences.SetSubtitlePreference(ctx, claims.UserID, pref); err != nil {
		return nil, preferenceProblem(err)
	}
	return &struct{}{}, nil
}

// deleteSubtitlePreference forgets the preference as v1 DELETE
// /subtitle-prefs/{series_id} does; a series with none is still a 204.
func (reg *Registry) deleteSubtitlePreference(ctx context.Context, in *SubtitlePreferenceInput) (*struct{}, error) {
	if reg.deps.SubtitlePreferences == nil {
		return nil, unavailable("preference")
	}
	claims := claimsFrom(ctx)
	profileID := profileFrom(ctx)
	if claims == nil || profileID == "" {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if err := reg.deps.SubtitlePreferences.DeleteSubtitlePreference(ctx, claims.UserID, profileID, string(in.SeriesID)); err != nil {
		return nil, preferenceProblem(err)
	}
	return &struct{}{}, nil
}

// updateLibraryPlaybackPreference is v1 PUT /library-playback-prefs/{library_id}
// as a PATCH: the present members are handed to the seam's partial update,
// which merges them onto the current canonical rows inside one locked
// transaction, so an omitted member keeps whatever /settings/values or the
// web library editor last wrote and two patches of different members both
// land. null and "" both clear a string member (the contract documents both
// spellings), and clearing every override removes the row. It answers 204
// as v1 does; an unknown library is 404.
func (reg *Registry) updateLibraryPlaybackPreference(ctx context.Context, in *LibraryPlaybackPreferenceUpdateInput) (*struct{}, error) {
	if reg.deps.LibraryPlaybackPreferences == nil {
		return nil, unavailable("preference")
	}
	claims := claimsFrom(ctx)
	profileID := profileFrom(ctx)
	if claims == nil || profileID == "" {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	libraryID, p := libraryIDOf(in.LibraryID)
	if p != nil {
		return nil, p
	}
	// A present member replaces the stored override; null or "" is no
	// override at all (a nil member), never the seam's empty-string spelling,
	// which the legacy row would keep and the list would still show.
	clearable := func(p Patch[string]) handlers.PrefPatch[string] {
		out := handlers.PrefPatch[string]{Present: p.Present}
		if p.Present && !p.Null && p.Value != "" {
			out.Value = ptr(p.Value)
		}
		return out
	}
	patch := handlers.LibraryPlaybackPrefPatch{
		AudioLanguage:       clearable(in.Body.AudioLanguage),
		SubtitleLanguage:    clearable(in.Body.SubtitleLanguage),
		SubtitleMode:        clearable(in.Body.SubtitleMode),
		ShowForcedSubtitles: handlers.PrefPatch[bool]{Present: in.Body.ShowForcedSubtitles.Present},
	}
	if f := in.Body.ShowForcedSubtitles; f.Present && !f.Null {
		patch.ShowForcedSubtitles.Value = ptr(f.Value)
	}
	if err := reg.deps.LibraryPlaybackPreferences.PatchLibraryPlaybackPreference(ctx, claims.UserID, profileID, libraryID, patch); err != nil {
		return nil, preferenceProblem(err)
	}
	return &struct{}{}, nil
}

func subtitlePreferenceOf(p userstore.SubtitlePreference) (SubtitlePreference, *Problem) {
	updated, problem := storeInstant(p.UpdatedAt)
	if problem != nil {
		return SubtitlePreference{}, problem
	}
	out := SubtitlePreference{
		ProfileID:            ID(p.ProfileID),
		SeriesID:             ID(p.SeriesID),
		SubtitleLanguage:     p.SubtitleLanguage,
		SubtitleTrackIndex:   p.SubtitleTrackIndex,
		ExternalSubtitlePath: p.ExternalSubtitlePath,
		SubtitleMode:         p.SubtitleMode,
		UpdatedAt:            updated,
	}
	if s := p.TrackSignature; s != nil {
		out.TrackSignature = &SubtitleTrackSignature{
			Source: s.Source, Language: s.Language, Codec: s.Codec, Label: s.Label,
			Forced: ptr(s.Forced), HearingImpaired: ptr(s.HearingImpaired),
		}
	}
	if p.HasShowForcedSubtitles {
		out.ShowForcedSubtitles = ptr(p.ShowForcedSubtitles)
	}
	return out, nil
}

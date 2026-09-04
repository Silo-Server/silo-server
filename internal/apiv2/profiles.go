package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// The profiles domain: household members of a login account.

// Profile is one household member.
type Profile struct {
	ID                         ID      `json:"id" example:"1"`
	Name                       string  `json:"name" example:"Alice"`
	Avatar                     string  `json:"avatar" doc:"Avatar reference; empty when none" example:"preset:fox"`
	AvatarURL                  string  `json:"avatar_url,omitempty" doc:"Where to fetch the avatar; absent when there is none to fetch" example:"/avatars/presets/fox.png"`
	AvatarSource               string  `json:"avatar_source" enum:"none,preset,upload" example:"preset"`
	HasPIN                     bool    `json:"has_pin" example:"false"`
	IsChild                    bool    `json:"is_child" example:"false"`
	IsPrimary                  bool    `json:"is_primary" doc:"The household parent (not the server admin role)" example:"true"`
	MaxContentRating           string  `json:"max_content_rating" doc:"Content-rating ceiling; empty means none" example:"PG-13"`
	QualityPreference          string  `json:"quality_preference" doc:"Canonical values: auto, original; empty when unset. Older profiles may carry other stored values" example:"auto"`
	Language                   string  `json:"language" doc:"Preferred audio language (ISO 639-1); empty inherits" example:"en"`
	PreferredMetadataLanguage  string  `json:"preferred_metadata_language" doc:"Metadata language (ISO 639-1); empty inherits the library's" example:"en"`
	SubtitleLanguage           string  `json:"subtitle_language" doc:"Preferred subtitle language (ISO 639-1); empty inherits" example:"en"`
	SubtitleMode               string  `json:"subtitle_mode" doc:"Canonical values: auto, always, off; empty when unset. Older profiles may carry other stored values" example:"auto"`
	AutoSkipIntro              bool    `json:"auto_skip_intro" example:"true"`
	AutoSkipCredits            bool    `json:"auto_skip_credits" example:"false"`
	AutoSkipRecap              bool    `json:"auto_skip_recap" example:"false"`
	AutoPlayNextPreview        bool    `json:"auto_play_next_preview" example:"false"`
	ShowForcedSubtitles        bool    `json:"show_forced_subtitles" example:"false"`
	LibraryRestrictionsEnabled bool    `json:"library_restrictions_enabled" example:"false"`
	AllowedLibraryIDs          []ID    `json:"allowed_library_ids" doc:"Libraries the profile may see when restrictions are enabled" example:"[\"1\",\"2\"]"`
	MaxPlaybackQuality         string  `json:"max_playback_quality" doc:"Playback ceiling. Canonical values: 1080p, 2160p; empty means none. Older profiles may carry other stored values" example:"1080p"`
	CreatedAt                  Instant `json:"created_at" example:"2026-01-02T03:04:05.000Z"`
	UpdatedAt                  Instant `json:"updated_at" example:"2026-01-02T03:04:05.000Z"`
}

// ProfileUpdate is the updateProfile body. Every member is optional: omitted
// leaves the field unchanged; null clears a nullable one.
type ProfileUpdate struct {
	Name                       *string       `json:"name,omitempty" nullable:"false" minLength:"1" maxLength:"64" doc:"Display name; leading and trailing spaces are trimmed" example:"Alice"`
	Avatar                     Patch[string] `json:"avatar,omitzero" doc:"Preset avatar reference; null removes the avatar" example:"preset:fox"`
	PIN                        Patch[string] `json:"pin,omitzero" doc:"New PIN; null removes the PIN" example:"1234"`
	IsChild                    *bool         `json:"is_child,omitempty" nullable:"false" example:"false"`
	MaxContentRating           Patch[string] `json:"max_content_rating,omitzero" doc:"Content-rating ceiling; null removes it" example:"PG-13"`
	QualityPreference          *string       `json:"quality_preference,omitempty" nullable:"false" enum:"auto,original" example:"auto"`
	Language                   Patch[string] `json:"language,omitzero" doc:"Preferred audio language (ISO 639-1); null inherits" example:"en"`
	PreferredMetadataLanguage  Patch[string] `json:"preferred_metadata_language,omitzero" doc:"Metadata language (ISO 639-1); null inherits the library's" example:"en"`
	SubtitleLanguage           Patch[string] `json:"subtitle_language,omitzero" doc:"Preferred subtitle language (ISO 639-1); null inherits" example:"en"`
	SubtitleMode               *string       `json:"subtitle_mode,omitempty" nullable:"false" enum:"auto,always,off" example:"auto"`
	AutoSkipIntro              *bool         `json:"auto_skip_intro,omitempty" nullable:"false" example:"true"`
	AutoSkipCredits            *bool         `json:"auto_skip_credits,omitempty" nullable:"false" example:"false"`
	AutoSkipRecap              *bool         `json:"auto_skip_recap,omitempty" nullable:"false" example:"false"`
	AutoPlayNextPreview        *bool         `json:"auto_play_next_preview,omitempty" nullable:"false" example:"false"`
	ShowForcedSubtitles        *bool         `json:"show_forced_subtitles,omitempty" nullable:"false" example:"false"`
	LibraryRestrictionsEnabled *bool         `json:"library_restrictions_enabled,omitempty" nullable:"false" example:"false"`
	AllowedLibraryIDs          *[]ID         `json:"allowed_library_ids,omitempty" nullable:"false" doc:"Replaces the allowlist; an empty array allows none" example:"[\"1\",\"2\"]"`
	MaxPlaybackQuality         Patch[string] `json:"max_playback_quality,omitzero" enum:"1080p,2160p" doc:"Playback ceiling; null removes it" example:"1080p"`
}

// ProfileUpdateInput is the updateProfile request.
type ProfileUpdateInput struct {
	ID   ID `path:"id" doc:"The profile to update" example:"1"`
	Body ProfileUpdate
	// RawBody is the document as sent: the framework treats null on an
	// optional member as absence, and the contract does not (null clears,
	// and only a nullable member admits clearing), so the handler checks
	// the raw members itself.
	RawBody []byte
}

// fieldMaxPlaybackQuality is the member the v1 handler rejects by name.
const fieldMaxPlaybackQuality = "max_playback_quality"

// profileUpdateNullable names the members whose null is a clearing value;
// null on any other member is a type failure.
var profileUpdateNullable = map[string]bool{
	"avatar": true, "pin": true, "max_content_rating": true, "language": true,
	"preferred_metadata_language": true, "subtitle_language": true, fieldMaxPlaybackQuality: true,
}

// rejectNonNullableNulls is the contract's omitted-versus-null rule for the
// members that do not admit clearing.
func rejectNonNullableNulls(raw []byte, nullable map[string]bool) *Problem {
	// The framework already judged the syntax and shape; a document that
	// does not decode here has no members to judge.
	var members map[string]json.RawMessage
	_ = json.Unmarshal(raw, &members)
	var errs []ProblemError
	for name, v := range members {
		if bytes.Equal(bytes.TrimSpace(v), jsonNull) && !nullable[name] {
			errs = append(errs, ProblemError{Location: locationBody + "." + name, Code: codeInvalidType, Detail: "null is not a value for this member; omit it to leave it unchanged"})
		}
	}
	if len(errs) == 0 {
		return nil
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Location < errs[j].Location })
	return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").WithErrors(errs...)
}

// ProfileOutput is a single-profile response.
type ProfileOutput struct {
	Body Profile
}

func registerProfiles(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodPatch, Prefix+"/profiles/{id}", "updateProfile", "profiles",
			"Update a household profile; omitted members are unchanged."),
		// Profile scoped without a required header, as v1 PUT /profiles/{id}:
		// an administrator or the verified primary profile manages the
		// household, and any other caller may change only its own active
		// profile's playback preferences. Naturally idempotent: repeating
		// the same PATCH converges on the same state.
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		DemoRestricted:  true,
	}, reg.updateProfile)
}

// updateProfile runs the same authorization and write path as v1 PUT
// /profiles/{id}. A PIN-locked primary profile counts as verified when the
// viewer-access gate resolved it as such (X-Profile-Token), exactly the
// check the v1 handler performs itself.
func (reg *Registry) updateProfile(ctx context.Context, in *ProfileUpdateInput) (*ProfileOutput, error) {
	if reg.deps.Profiles == nil {
		return nil, unavailable("profile")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if p := rejectNonNullableNulls(in.RawBody, profileUpdateNullable); p != nil {
		return nil, p
	}
	req, p := in.Body.toRequest()
	if p != nil {
		return nil, p
	}
	view, err := reg.deps.Profiles.UpdateProfile(ctx, handlers.ProfileUpdateCommand{
		UserID:          claims.UserID,
		ProfileID:       string(in.ID),
		ActiveProfileID: profileFrom(ctx),
		Request:         req,
		VerifyProfile: func(profileID string) error {
			if scope, ok := scopeFrom(ctx); ok && scope.ProfileID == profileID && scope.ProfileVerified {
				return nil
			}
			return access.ErrProfileUnverified
		},
	})
	if err != nil {
		return nil, profileProblem(err)
	}
	profile, p := profileOf(view)
	if p != nil {
		return nil, p
	}
	return &ProfileOutput{Body: profile}, nil
}

// profileProblem maps the v1 decision onto problem types: a rejected member
// is a validation failure naming it, a household-permission failure keeps
// the profile_verification_required type clients branch on.
func profileProblem(err error) *Problem {
	apiErr, ok := err.(*handlers.APIError) //nolint:errorlint // UpdateProfile returns the value directly
	if !ok {
		return serviceProblem(err)
	}
	switch {
	case apiErr.Field != "":
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "body." + apiErr.Field, Code: codeInvalid, Detail: apiErr.Message})
	case apiErr.Status == http.StatusBadRequest:
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody, Code: codeInvalid, Detail: apiErr.Message})
	case apiErr.Status == http.StatusForbidden && apiErr.Code == "profile_management":
		return NewProblem(TypeProfileVerificationRequired, apiErr.Message)
	}
	return serviceProblem(err)
}

// toRequest lowers the presence-aware body onto the v1 request, where "" is
// the clearing value the store already understands.
func (u ProfileUpdate) toRequest() (handlers.ProfileUpdateRequest, *Problem) {
	clear := func(p Patch[string]) *string {
		if !p.Present {
			return nil
		}
		return ptr(p.Value) // Null leaves the zero value: the v1 clearing form
	}
	req := handlers.ProfileUpdateRequest{
		Name:                       u.Name,
		Avatar:                     clear(u.Avatar),
		PIN:                        clear(u.PIN),
		IsChild:                    u.IsChild,
		MaxContentRating:           clear(u.MaxContentRating),
		QualityPreference:          u.QualityPreference,
		Language:                   clear(u.Language),
		PreferredMetadataLanguage:  clear(u.PreferredMetadataLanguage),
		SubtitleLanguage:           clear(u.SubtitleLanguage),
		SubtitleMode:               u.SubtitleMode,
		AutoSkipIntro:              u.AutoSkipIntro,
		AutoSkipCredits:            u.AutoSkipCredits,
		AutoSkipRecap:              u.AutoSkipRecap,
		AutoPlayNextPreview:        u.AutoPlayNextPreview,
		ShowForcedSubtitles:        u.ShowForcedSubtitles,
		LibraryRestrictionsEnabled: u.LibraryRestrictionsEnabled,
		MaxPlaybackQuality:         clear(u.MaxPlaybackQuality),
	}
	if u.AllowedLibraryIDs != nil {
		ids := make([]int, 0, len(*u.AllowedLibraryIDs))
		for _, id := range *u.AllowedLibraryIDs {
			n, err := intOfID(id)
			if err != nil || n <= 0 {
				return req, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
					WithErrors(ProblemError{Location: "body.allowed_library_ids", Code: codeInvalid, Detail: "expected library identifiers"})
			}
			ids = append(ids, n)
		}
		req.AllowedLibraryIDs = &ids
	}
	return req, nil
}

func profileOf(v handlers.ProfileView) (Profile, *Problem) {
	created, p := storeInstant(v.CreatedAt)
	if p != nil {
		return Profile{}, p
	}
	updated, p := storeInstant(v.UpdatedAt)
	if p != nil {
		return Profile{}, p
	}
	return Profile{
		ID:                         ID(v.ID),
		Name:                       v.Name,
		Avatar:                     v.Avatar,
		AvatarURL:                  v.AvatarURL,
		AvatarSource:               v.AvatarSource,
		HasPIN:                     v.HasPIN,
		IsChild:                    v.IsChild,
		IsPrimary:                  v.IsPrimary,
		MaxContentRating:           v.MaxContentRating,
		QualityPreference:          v.QualityPreference,
		Language:                   v.Language,
		PreferredMetadataLanguage:  v.PreferredMetadataLanguage,
		SubtitleLanguage:           v.SubtitleLanguage,
		SubtitleMode:               v.SubtitleMode,
		AutoSkipIntro:              v.AutoSkipIntro,
		AutoSkipCredits:            v.AutoSkipCredits,
		AutoSkipRecap:              v.AutoSkipRecap,
		AutoPlayNextPreview:        v.AutoPlayNextPreview,
		ShowForcedSubtitles:        v.ShowForcedSubtitles,
		LibraryRestrictionsEnabled: v.LibraryRestrictionsEnabled,
		AllowedLibraryIDs:          NonNil(idsOfInts(v.AllowedLibraryIDs)),
		MaxPlaybackQuality:         v.MaxPlaybackQuality,
		CreatedAt:                  created,
		UpdatedAt:                  updated,
	}, nil
}

package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

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
	QualityPreference          string  `json:"quality_preference" doc:"Free-form until the vocabulary is ratified (#135). Canonical values today: auto, original, 720p, 1080p, 2160p, 4k; empty when unset" example:"auto"`
	Language                   string  `json:"language" doc:"Preferred audio language (ISO 639-1); empty inherits" example:"en"`
	PreferredMetadataLanguage  string  `json:"preferred_metadata_language" doc:"Metadata language (ISO 639-1); empty inherits the library's" example:"en"`
	SubtitleLanguage           string  `json:"subtitle_language" doc:"Preferred subtitle language (ISO 639-1); empty inherits" example:"en"`
	SubtitleMode               string  `json:"subtitle_mode" doc:"Free-form until the vocabulary is ratified (#135). Canonical values today: auto, always, off, default, forced_only; empty when unset" example:"auto"`
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
	PIN                        Patch[string] `json:"pin,omitzero" minLength:"1" maxLength:"72" doc:"New PIN, 1 to 72 bytes; null removes the PIN. An empty string is rejected, not a clear" example:"1234"`
	IsChild                    *bool         `json:"is_child,omitempty" nullable:"false" example:"false"`
	MaxContentRating           Patch[string] `json:"max_content_rating,omitzero" doc:"Content-rating ceiling; null removes it" example:"PG-13"`
	QualityPreference          *string       `json:"quality_preference,omitempty" nullable:"false" maxLength:"32" doc:"Free-form until the vocabulary is ratified (#135); v1 never validated it. Canonical values today: auto, original, 720p, 1080p, 2160p, 4k" example:"auto"`
	Language                   Patch[string] `json:"language,omitzero" doc:"Preferred audio language (ISO 639-1); null inherits" example:"en"`
	PreferredMetadataLanguage  Patch[string] `json:"preferred_metadata_language,omitzero" doc:"Metadata language (ISO 639-1); null inherits the library's" example:"en"`
	SubtitleLanguage           Patch[string] `json:"subtitle_language,omitzero" doc:"Preferred subtitle language (ISO 639-1); null inherits" example:"en"`
	SubtitleMode               *string       `json:"subtitle_mode,omitempty" nullable:"false" maxLength:"32" doc:"Free-form until the vocabulary is ratified (#135); v1 never validated it. Canonical values today: auto, always, off, default, forced_only" example:"auto"`
	AutoSkipIntro              *bool         `json:"auto_skip_intro,omitempty" nullable:"false" example:"true"`
	AutoSkipCredits            *bool         `json:"auto_skip_credits,omitempty" nullable:"false" example:"false"`
	AutoSkipRecap              *bool         `json:"auto_skip_recap,omitempty" nullable:"false" example:"false"`
	AutoPlayNextPreview        *bool         `json:"auto_play_next_preview,omitempty" nullable:"false" example:"false"`
	ShowForcedSubtitles        *bool         `json:"show_forced_subtitles,omitempty" nullable:"false" example:"false"`
	LibraryRestrictionsEnabled *bool         `json:"library_restrictions_enabled,omitempty" nullable:"false" example:"false"`
	AllowedLibraryIDs          *[]ID         `json:"allowed_library_ids,omitempty" nullable:"false" doc:"Replaces the allowlist with these unique library identifiers; an empty array allows none" example:"[\"1\",\"2\"]"`
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

// codeProfileManagement is the v1 error code for a household-management
// check the caller did not pass (an unverified PIN-locked primary profile).
const codeProfileManagement = "profile_management"

// locationAllowedLibraryIDs is the problem location of the allowlist member.
const (
	locationAllowedLibraryIDs = locationBody + ".allowed_library_ids"
	locationPIN               = locationBody + ".pin"
)

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

// ProfileCollection is the listProfiles envelope: the whole household, which
// the account's profile limit bounds, so it is not paginated.
type ProfileCollection struct {
	Collection[Profile]
	AvatarUploadEnabled bool `json:"avatar_upload_enabled" doc:"Whether this server accepts avatar uploads (an avatar store is configured)" example:"true"`
}

// ProfileCollectionOutput is the listProfiles response.
type ProfileCollectionOutput struct {
	Body ProfileCollection
}

// ProfileCreate is the createProfile body. Only the name is required; an
// omitted member takes the v1 default (show_forced_subtitles on, everything
// else off or empty). No member admits null: there is nothing to clear on a
// profile that does not exist yet.
type ProfileCreate struct {
	Name                       string  `json:"name" minLength:"1" maxLength:"64" doc:"Display name; leading and trailing spaces are trimmed" example:"Alice"`
	Avatar                     *string `json:"avatar,omitempty" nullable:"false" doc:"Preset avatar reference" example:"preset:fox"`
	PIN                        *string `json:"pin,omitempty" nullable:"false" minLength:"1" maxLength:"72" doc:"PIN, 1 to 72 bytes" example:"1234"`
	IsChild                    *bool   `json:"is_child,omitempty" nullable:"false" example:"false"`
	MaxContentRating           *string `json:"max_content_rating,omitempty" nullable:"false" doc:"Content-rating ceiling" example:"PG-13"`
	QualityPreference          *string `json:"quality_preference,omitempty" nullable:"false" maxLength:"32" doc:"Free-form until the vocabulary is ratified (#135); v1 never validated it. Canonical values today: auto, original, 720p, 1080p, 2160p, 4k" example:"auto"`
	Language                   *string `json:"language,omitempty" nullable:"false" doc:"Preferred audio language (ISO 639-1)" example:"en"`
	PreferredMetadataLanguage  *string `json:"preferred_metadata_language,omitempty" nullable:"false" doc:"Metadata language (ISO 639-1)" example:"en"`
	SubtitleLanguage           *string `json:"subtitle_language,omitempty" nullable:"false" doc:"Preferred subtitle language (ISO 639-1)" example:"en"`
	SubtitleMode               *string `json:"subtitle_mode,omitempty" nullable:"false" maxLength:"32" doc:"Free-form until the vocabulary is ratified (#135); v1 never validated it. Canonical values today: auto, always, off, default, forced_only" example:"auto"`
	AutoSkipIntro              *bool   `json:"auto_skip_intro,omitempty" nullable:"false" example:"true"`
	AutoSkipCredits            *bool   `json:"auto_skip_credits,omitempty" nullable:"false" example:"false"`
	AutoSkipRecap              *bool   `json:"auto_skip_recap,omitempty" nullable:"false" example:"false"`
	AutoPlayNextPreview        *bool   `json:"auto_play_next_preview,omitempty" nullable:"false" example:"false"`
	ShowForcedSubtitles        *bool   `json:"show_forced_subtitles,omitempty" nullable:"false" doc:"Defaults to true when omitted" example:"true"`
	LibraryRestrictionsEnabled *bool   `json:"library_restrictions_enabled,omitempty" nullable:"false" example:"false"`
	AllowedLibraryIDs          *[]ID   `json:"allowed_library_ids,omitempty" nullable:"false" doc:"Unique library identifiers the profile may see when restrictions are enabled" example:"[\"1\",\"2\"]"`
	MaxPlaybackQuality         *string `json:"max_playback_quality,omitempty" nullable:"false" enum:"1080p,2160p" doc:"Playback ceiling" example:"1080p"`
}

// ProfileCreateInput is the createProfile request.
type ProfileCreateInput struct {
	Body ProfileCreate
	// RawBody is the document as sent; see ProfileUpdateInput.
	RawBody []byte
}

// ProfileCreatedOutput is the createProfile response: the profile and where
// it now lives.
type ProfileCreatedOutput struct {
	Location string `header:"Location" doc:"The created profile's resource path"`
	Body     Profile
}

func registerProfiles(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/profiles", "listProfiles", "profiles",
			"List the household profiles on the signed-in account."),
		// Profile scoped without a required header, as v1 GET /profiles: the
		// profile picker calls it before any profile is selected.
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		ServiceBacked:   true,
	}, reg.listProfiles)

	create := humaOp(http.MethodPost, Prefix+"/profiles", "createProfile", "profiles",
		"Create a household profile.")
	create.DefaultStatus = http.StatusCreated
	// v1 CreateProfile answers a taken name (name_conflict) and a full
	// household (profile_limit_reached) with 409.
	create.Errors = []int{http.StatusConflict}
	Register(reg, Operation{
		Operation: create,
		// As v1 POST /profiles: the first profile on an account is
		// bootstrapped by anyone signed in; after that an administrator or
		// the verified primary profile manages the household.
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		// Demo restriction is a v2 addition: v1's demo guard does not list
		// profile mutations (recorded in the ledger row).
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.createProfile)

	update := humaOp(http.MethodPatch, Prefix+"/profiles/{id}", "updateProfile", "profiles",
		"Update a household profile; omitted members are unchanged.")
	// v1 UpdateProfile answers a taken name with 409 name_conflict, which
	// serviceProblem carries through as conflict; the status is this
	// operation's own, not one the class implies.
	update.Errors = []int{http.StatusConflict}
	Register(reg, Operation{
		Operation: update,
		// Profile scoped without a required header, as v1 PUT /profiles/{id}:
		// an administrator or the verified primary profile manages the
		// household, and any other caller may change only its own active
		// profile's playback preferences. Naturally idempotent: repeating
		// the same PATCH converges on the same state.
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		// Demo restriction is a v2 addition: v1's demo guard does not list
		// profile mutations (recorded in the ledger row).
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.updateProfile)

	del := humaOp(http.MethodDelete, Prefix+"/profiles/{id}", "deleteProfile", "profiles",
		"Delete a household profile; the primary profile cannot be deleted.")
	del.DefaultStatus = http.StatusNoContent
	// v1 DeleteProfile answers the primary profile with 409
	// primary_profile_protected.
	del.Errors = []int{http.StatusConflict}
	Register(reg, Operation{
		Operation: del,
		// As v1 DELETE /profiles/{id}: an administrator or the verified
		// primary profile manages the household. Repeating the delete
		// answers 404 (already gone).
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		// Demo restriction is a v2 addition: v1's demo guard does not list
		// profile mutations (recorded in the ledger row).
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.deleteProfile)

	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/profiles/household/sessions", "listHouseholdSessions", "profiles",
			"List the live playback sessions on the signed-in account, for a household manager."),
		// As v1 GET /profiles/household/sessions: an administrator or the
		// verified primary profile; a bounded, unpaginated collection.
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		ServiceBacked:   true,
	}, reg.listHouseholdSessions)

	verify := humaOp(http.MethodPost, Prefix+"/profiles/{id}/verify-pin", "verifyProfilePIN", "profiles",
		"Check a profile's PIN; a match issues the X-Profile-Token that unlocks the profile for this login session.")
	Register(reg, Operation{
		Operation: verify,
		// As v1 POST /profiles/{id}/verify-pin: any signed-in caller on the
		// account may try a PIN; the profile header is not needed because
		// the point is to unlock one. Not demo restricted (a check, not a
		// mutation) and no-store like every v2 response: the token is a
		// credential.
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		ServiceBacked:   true,
	}, reg.verifyProfilePIN)

	upload := humaOp(http.MethodPut, Prefix+"/profiles/{id}/avatar", "uploadProfileAvatar", "profiles",
		"Replace a profile's avatar with an uploaded image (multipart form, part `avatar`: JPEG, PNG or WebP, at most 10 MiB; the whole request, framing included, at most 11 MiB).")
	Register(reg, Operation{
		Operation: upload,
		// As v1 PUT /profiles/{id}/avatar: any signed-in caller on the
		// account (the v1 route runs no household-manager check). The
		// multipart body is bounded by the form parser at the avatar's own
		// limit rather than the JSON default.
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		// Demo restriction is a v2 addition: v1's demo guard does not list
		// profile mutations (recorded in the ledger row).
		DemoRestricted: true,
		ServiceBacked:  true,
		MaxBodyBytes:   maxAvatarFormBytes,
	}, reg.uploadProfileAvatar)

	Register(reg, Operation{
		Operation: humaOp(http.MethodDelete, Prefix+"/profiles/{id}/avatar", "deleteProfileAvatar", "profiles",
			"Remove a profile's uploaded avatar; a preset avatar is left as is."),
		// As v1 DELETE /profiles/{id}/avatar: any signed-in caller on the
		// account; idempotent, a profile with no upload is left unchanged.
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		// Demo restriction is a v2 addition: v1's demo guard does not list
		// profile mutations (recorded in the ledger row).
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.deleteProfileAvatar)
}

// maxAvatarBytes is v1's avatar limit (10 MiB). maxAvatarFormBytes is the
// whole-request cap: the file plus maxAvatarFramingBytes of multipart
// framing (boundaries and the client-chosen part headers, filename
// included), so a valid upload at the file limit is judged by the file
// limit and never by the request cap. The framing allowance is part of
// the documented contract.
const (
	maxAvatarBytes        = 10 << 20
	maxAvatarFramingBytes = 1 << 20
	maxAvatarFormBytes    = maxAvatarBytes + maxAvatarFramingBytes
)

// ProfileIDInput is the request of an operation addressing one profile.
type ProfileIDInput struct {
	ID ID `path:"id" doc:"The profile" example:"1"`
}

// deleteProfile runs the same authorization and delete path as v1 DELETE
// /profiles/{id}.
func (reg *Registry) deleteProfile(ctx context.Context, in *ProfileIDInput) (*struct{}, error) {
	if reg.deps.Profiles == nil {
		return nil, unavailable("profile")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	err := reg.deps.Profiles.DeleteProfile(ctx, handlers.ProfileDeleteCommand{
		UserID:          claims.UserID,
		ProfileID:       string(in.ID),
		ActiveProfileID: profileFrom(ctx),
		VerifyProfile:   scopeVerifier(ctx),
	})
	if err != nil {
		return nil, profileProblem(err)
	}
	return nil, nil
}

// PlaybackSession is one live playback session on the account, as the
// household sessions listing reports it. Every member the reporting node
// may not know is nullable or omitted; the list is a monitoring view, so
// the v1 row's members are carried as they are.
type PlaybackSession struct {
	ID                       ID      `json:"id" doc:"The playback session" example:"ps_7f3a"`
	UserID                   ID      `json:"user_id" example:"1"`
	Username                 string  `json:"username" example:"laura"`
	ProfileID                *ID     `json:"profile_id" nullable:"true" doc:"The profile playing; null when the session carries none" example:"1"`
	ProfileName              string  `json:"profile_name" doc:"Empty when unknown" example:"Laura"`
	MediaFileID              ID      `json:"media_file_id" example:"42"`
	RequestedMediaFileID     ID      `json:"requested_media_file_id" doc:"The file the client asked for before any version substitution" example:"42"`
	ContentID                string  `json:"content_id" doc:"Catalog content id of the playing item; empty when unknown" example:"tt0111161"`
	MediaTitle               string  `json:"media_title" example:"The Shawshank Redemption"`
	MediaType                string  `json:"media_type" doc:"Catalog item type; empty when unknown" example:"movie"`
	SeriesName               string  `json:"series_name" doc:"Empty unless an episode" example:""`
	EpisodeName              string  `json:"episode_name" doc:"Empty unless an episode" example:""`
	SeasonNumber             *int    `json:"season_number" nullable:"true" doc:"null unless an episode" example:"1"`
	EpisodeNumber            *int    `json:"episode_number" nullable:"true" doc:"null unless an episode" example:"1"`
	PosterURL                string  `json:"poster_url" doc:"Where to fetch the poster; empty when there is none" example:"/api/v1/images/poster/42"`
	PlayMethod               string  `json:"play_method" doc:"The negotiated method as the node reported it" example:"direct"`
	ReportingNode            string  `json:"reporting_node" doc:"Identifier of the node serving the stream" example:"api"`
	NodeDisplayName          string  `json:"node_display_name" doc:"Empty when the node has no display name" example:""`
	FileDuration             *int    `json:"file_duration" nullable:"true" doc:"Seconds; null when unknown" example:"8520"`
	StartedAt                Instant `json:"started_at" example:"2026-01-02T03:04:05.000Z"`
	UpdatedAt                Instant `json:"updated_at" example:"2026-01-02T03:04:05.000Z"`
	PositionSeconds          float64 `json:"position_seconds" example:"1234.5"`
	IsPaused                 bool    `json:"is_paused" example:"false"`
	HasPlaybackControl       bool    `json:"has_playback_control" doc:"Whether the serving node accepts remote control of this session" example:"true"`
	ClientIP                 string  `json:"client_ip" doc:"Empty when unknown" example:"192.0.2.10"`
	ClientName               string  `json:"client_name" doc:"Empty when unknown" example:"Silo for Apple TV"`
	ClientVersion            string  `json:"client_version" doc:"Empty when unknown" example:"1.4.0"`
	ClientBuild              string  `json:"client_build" doc:"Empty when unknown" example:"1400"`
	ClientChannel            string  `json:"client_channel" doc:"Empty when unknown" example:"release"`
	ClientLabel              string  `json:"client_label" doc:"Display label derived from the client name and version; empty when unknown" example:"Silo for Apple TV 1.4"`
	ClientLabelFull          string  `json:"client_label_full" doc:"Display label with the exact build; empty when unknown" example:"Silo for Apple TV 1.4.0 (1400)"`
	ClientUserAgent          string  `json:"client_user_agent" doc:"Empty when unknown" example:""`
	AudioTrackIndex          int     `json:"audio_track_index" example:"0"`
	TranscodeAudio           bool    `json:"transcode_audio" example:"false"`
	StreamBitrateKbps        *int    `json:"stream_bitrate_kbps" nullable:"true" doc:"null when unknown" example:"12000"`
	TargetResolution         string  `json:"target_resolution" doc:"Empty when not transcoding" example:""`
	TargetVideoCodec         string  `json:"target_video_codec" doc:"Empty when not transcoding" example:""`
	TargetAudioCodec         string  `json:"target_audio_codec" doc:"Empty when not transcoding" example:""`
	TargetAudioChannels      *int    `json:"target_audio_channels" nullable:"true" doc:"Channels the transcode encodes; null when the reporting node did not know" example:"6"`
	TargetBitrateKbps        *int    `json:"target_bitrate_kbps" nullable:"true" doc:"null when not transcoding" example:"8000"`
	TranscodeHWAccel         string  `json:"transcode_hw_accel" doc:"Confirmed transcode executor; empty when not transcoding" example:""`
	ToneMapMode              string  `json:"tone_map_mode" doc:"Confirmed tone-map executor; empty when none" example:""`
	SourceContainer          string  `json:"source_container" doc:"Empty when unknown" example:"mkv"`
	SourceBitrateKbps        *int    `json:"source_bitrate_kbps" nullable:"true" doc:"null when unknown" example:"24000"`
	SourceVideoCodec         string  `json:"source_video_codec" doc:"Empty when unknown" example:"hevc"`
	SourceVideoResolution    string  `json:"source_video_resolution" doc:"Empty when unknown" example:"2160p"`
	SourceAudioCodec         string  `json:"source_audio_codec" doc:"Empty when unknown" example:"truehd"`
	SourceAudioChannels      *int    `json:"source_audio_channels" nullable:"true" doc:"null when unknown" example:"8"`
	SourceAudioLanguage      string  `json:"source_audio_language" doc:"Empty when unknown" example:"eng"`
	SourceAudioTitle         string  `json:"source_audio_title" doc:"Empty when unknown" example:""`
	SourceAudioLayout        string  `json:"source_audio_layout" doc:"Empty when unknown" example:"7.1"`
	RequestedVideoCodec      string  `json:"requested_video_codec" doc:"Empty when the client did not ask for one" example:""`
	RequestedVideoResolution string  `json:"requested_video_resolution" doc:"Empty when the client did not ask for one" example:""`
	VideoDecision            string  `json:"video_decision" doc:"Empty when unknown" example:"copy"`
	AudioDecision            string  `json:"audio_decision" doc:"Empty when unknown" example:"copy"`
	EffectivePlayMethod      string  `json:"effective_play_method" doc:"Bucketed method: direct, remux, transcode or audio; empty when unknown" example:"direct"`
	IsJellyfinClient         bool    `json:"is_jellyfin_client" example:"false"`
	RoutingWorkload          string  `json:"routing_workload" doc:"Empty when routing is unresolved" example:""`
	RoutingExecution         string  `json:"routing_execution" doc:"Empty when routing is unresolved" example:""`
	RoutingExecutionNodeID   *ID     `json:"routing_execution_node_id" nullable:"true" doc:"null when routing is unresolved" example:"3"`
	RoutingExecutionNodeName string  `json:"routing_execution_node_name" doc:"Empty when routing is unresolved" example:""`
	RoutingEgress            string  `json:"routing_egress" doc:"Empty when routing is unresolved" example:""`
	RoutingEgressNodeID      *ID     `json:"routing_egress_node_id" nullable:"true" doc:"null when routing is unresolved" example:"3"`
	RoutingEgressNodeName    string  `json:"routing_egress_node_name" doc:"Empty when routing is unresolved" example:""`
}

// PlaybackSessionCollection is the listHouseholdSessions envelope: the
// account's live sessions, bounded by the account's stream limit, so it is
// not paginated.
type PlaybackSessionCollection struct {
	Collection[PlaybackSession]
}

// PlaybackSessionCollectionOutput is the listHouseholdSessions response.
type PlaybackSessionCollectionOutput struct {
	Body PlaybackSessionCollection
}

// listHouseholdSessions runs the same authorization and read as v1 GET
// /profiles/household/sessions.
func (reg *Registry) listHouseholdSessions(ctx context.Context, _ *struct{}) (*PlaybackSessionCollectionOutput, error) {
	if reg.deps.Profiles == nil {
		return nil, unavailable("profile")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	rows, err := reg.deps.Profiles.ListHouseholdSessions(ctx, handlers.HouseholdSessionsQuery{
		UserID:          claims.UserID,
		ActiveProfileID: profileFrom(ctx),
		VerifyProfile:   scopeVerifier(ctx),
	})
	if err != nil {
		return nil, profileProblem(err)
	}
	items := make([]PlaybackSession, 0, len(rows))
	for _, row := range rows {
		items = append(items, playbackSessionOf(row))
	}
	return &PlaybackSessionCollectionOutput{Body: PlaybackSessionCollection{Collection: NewCollection(items)}}, nil
}

// optionalID renders an internal string key as an ID, or nil when it is empty.
func optionalID(v string) *ID {
	if v == "" {
		return nil
	}
	id := ID(v)
	return &id
}

func playbackSessionOf(v handlers.PlaybackSessionView) PlaybackSession {
	return PlaybackSession{
		ID: ID(v.SessionID), UserID: IDFromInt(int64(v.UserID)), Username: v.Username,
		ProfileID: optionalID(v.ProfileID), ProfileName: v.ProfileName,
		MediaFileID: IDFromInt(int64(v.MediaFileID)), RequestedMediaFileID: IDFromInt(int64(v.RequestedMediaFileID)),
		ContentID: v.ContentID, MediaTitle: v.MediaTitle, MediaType: v.MediaType,
		SeriesName: v.SeriesName, EpisodeName: v.EpisodeName, SeasonNumber: v.SeasonNumber, EpisodeNumber: v.EpisodeNumber,
		PosterURL: v.PosterURL, PlayMethod: v.PlayMethod, ReportingNode: v.ReportingNode, NodeDisplayName: v.NodeDisplayName,
		FileDuration: v.FileDuration, StartedAt: NewInstant(v.StartedAt), UpdatedAt: NewInstant(v.UpdatedAt),
		PositionSeconds: v.PositionSeconds, IsPaused: v.IsPaused, HasPlaybackControl: v.HasPlaybackControl,
		ClientIP: v.ClientIP, ClientName: v.ClientName, ClientVersion: v.ClientVersion, ClientBuild: v.ClientBuild,
		ClientChannel: v.ClientChannel, ClientLabel: v.ClientLabel, ClientLabelFull: v.ClientLabelFull, ClientUserAgent: v.ClientUserAgent,
		AudioTrackIndex: v.AudioTrackIndex, TranscodeAudio: v.TranscodeAudio, StreamBitrateKbps: v.StreamBitrateKbps,
		TargetResolution: v.TargetResolution, TargetVideoCodec: v.TargetVideoCodec, TargetAudioCodec: v.TargetAudioCodec,
		TargetAudioChannels: v.TargetAudioChannels, TargetBitrateKbps: v.TargetBitrateKbps,
		TranscodeHWAccel: v.TranscodeHWAccel, ToneMapMode: v.ToneMapMode,
		SourceContainer: v.SourceContainer, SourceBitrateKbps: v.SourceBitrateKbps, SourceVideoCodec: v.SourceVideoCodec,
		SourceVideoResolution: v.SourceVideoResolution, SourceAudioCodec: v.SourceAudioCodec, SourceAudioChannels: v.SourceAudioChannels,
		SourceAudioLanguage: v.SourceAudioLanguage, SourceAudioTitle: v.SourceAudioTitle, SourceAudioLayout: v.SourceAudioLayout,
		RequestedVideoCodec: v.RequestedVideoCodec, RequestedVideoResolution: v.RequestedVideoResolution,
		VideoDecision: v.VideoDecision, AudioDecision: v.AudioDecision, EffectivePlayMethod: v.EffectivePlayMethod,
		IsJellyfinClient: v.IsJellyfinClient,
		RoutingWorkload:  v.RoutingWorkload, RoutingExecution: v.RoutingExecution, RoutingExecutionNodeID: idOfIntPtr(v.RoutingExecutionNodeID),
		RoutingExecutionNodeName: v.RoutingExecutionNodeName, RoutingEgress: v.RoutingEgress, RoutingEgressNodeID: idOfIntPtr(v.RoutingEgressNodeID),
		RoutingEgressNodeName: v.RoutingEgressNodeName,
	}
}

func idOfIntPtr(v *int) *ID {
	if v == nil {
		return nil
	}
	id := IDFromInt(int64(*v))
	return &id
}

// ProfilePINCheck is the verifyProfilePIN body.
type ProfilePINCheck struct {
	PIN string `json:"pin" minLength:"1" maxLength:"72" doc:"The PIN to check" example:"1234"`
}

// ProfilePINCheckInput is the verifyProfilePIN request.
type ProfilePINCheckInput struct {
	ID   ID `path:"id" doc:"The profile whose PIN is checked" example:"1"`
	Body ProfilePINCheck
}

// ProfileVerification is the verifyProfilePIN response. A wrong PIN is not
// an error: valid is false and no token is issued.
type ProfileVerification struct {
	Valid        bool            `json:"valid" doc:"Whether the PIN matched" example:"true"`
	ProfileToken string          `json:"profile_token,omitempty" doc:"Send as X-Profile-Token with X-Profile-Id to act as the unlocked profile; bound to this login session. Absent when the PIN did not match" example:"pvt_5f3a9c1e7b2d4e8fa0c6"`
	ExpiresAt    NullableInstant `json:"expires_at" doc:"When the token stops being accepted; null when no token was issued or it does not expire" example:"2026-01-02T15:04:05.000Z"`
}

// ProfileVerificationOutput is the verifyProfilePIN response.
type ProfileVerificationOutput struct {
	Body ProfileVerification
}

// verifyProfilePIN runs the same check and mint as v1 POST
// /profiles/{id}/verify-pin. The token semantics are v1's: bound to the
// caller's login session and the account's policy revision.
func (reg *Registry) verifyProfilePIN(ctx context.Context, in *ProfilePINCheckInput) (*ProfileVerificationOutput, error) {
	if reg.deps.Profiles == nil {
		return nil, unavailable("profile")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if len(in.Body.PIN) > maxPINBytes {
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationPIN, Code: codeOutOfRange, Detail: detailPINTooLong})
	}
	result, err := reg.deps.Profiles.VerifyPIN(ctx, handlers.ProfileVerifyPINCommand{
		UserID: claims.UserID, SessionID: claims.SessionID, ProfileID: string(in.ID), PIN: in.Body.PIN,
	})
	if err != nil {
		return nil, profileProblem(err)
	}
	out := ProfileVerification{Valid: result.Valid, ProfileToken: result.ProfileToken}
	if !result.ExpiresAt.IsZero() {
		out.ExpiresAt = NullableInstant{Valid: true, Time: NewInstant(result.ExpiresAt)}
	}
	return &ProfileVerificationOutput{Body: out}, nil
}

// ProfileAvatarForm is the uploadProfileAvatar multipart form.
type ProfileAvatarForm struct {
	Avatar huma.FormFile `form:"avatar" contentType:"image/jpeg,image/png,image/webp" required:"true" doc:"The image; resized server-side to the avatar variants"`
}

// ProfileAvatarUploadInput is the uploadProfileAvatar request.
type ProfileAvatarUploadInput struct {
	ID      ID `path:"id" doc:"The profile" example:"1"`
	RawBody huma.MultipartFormFiles[ProfileAvatarForm]
}

// uploadProfileAvatar runs the same store, resize and profile write as v1
// PUT /profiles/{id}/avatar. The framework has already parsed the form and
// refused a part outside the declared image types (422 at avatar); the
// service re-checks the type and the 10 MiB file limit.
func (reg *Registry) uploadProfileAvatar(ctx context.Context, in *ProfileAvatarUploadInput) (*ProfileOutput, error) {
	if reg.deps.Profiles == nil {
		return nil, unavailable("profile")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	form := in.RawBody.Data()
	if form == nil || !form.Avatar.IsSet || form.Avatar.File == nil {
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationAvatarPart, Code: codeRequired, Detail: "The avatar part is required."})
	}
	defer func() { _ = form.Avatar.Close() }()
	view, err := reg.deps.Profiles.UploadAvatar(ctx, handlers.ProfileAvatarUpload{
		UserID: claims.UserID, ProfileID: string(in.ID), ContentType: form.Avatar.ContentType, File: form.Avatar.File,
	})
	if err != nil {
		return nil, avatarProblem(err)
	}
	profile, p := profileOf(view)
	if p != nil {
		return nil, p
	}
	return &ProfileOutput{Body: profile}, nil
}

// locationAvatarPart names the multipart part in a validation problem.
const locationAvatarPart = locationBody + ".avatar"

// avatarProblem maps the v1 upload decisions: an unsupported or undecodable
// image is a validation failure at the part, the file limit is the
// payload-too-large problem naming it, a missing upload store is the
// service's own 503.
func avatarProblem(err error) *Problem {
	var apiErr *handlers.APIError
	if !errors.As(err, &apiErr) {
		return serviceProblem(err)
	}
	switch apiErr.Status {
	case http.StatusBadRequest:
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationAvatarPart, Code: codeInvalid, Detail: apiErr.Message})
	case http.StatusRequestEntityTooLarge:
		return NewProblem(TypePayloadTooLarge, fmt.Sprintf("The avatar exceeds the %d-byte limit.", maxAvatarBytes))
	case http.StatusServiceUnavailable:
		return unavailable("avatar upload")
	}
	return serviceProblem(err)
}

// deleteProfileAvatar runs the same clear as v1 DELETE /profiles/{id}/avatar
// and answers 204 rather than the profile (a plain removal).
func (reg *Registry) deleteProfileAvatar(ctx context.Context, in *ProfileIDInput) (*struct{}, error) {
	if reg.deps.Profiles == nil {
		return nil, unavailable("profile")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if _, err := reg.deps.Profiles.DeleteAvatar(ctx, claims.UserID, string(in.ID)); err != nil {
		return nil, profileProblem(err)
	}
	return nil, nil
}

// listProfiles answers from the same household read v1 GET /profiles uses.
func (reg *Registry) listProfiles(ctx context.Context, _ *struct{}) (*ProfileCollectionOutput, error) {
	if reg.deps.Profiles == nil {
		return nil, unavailable("profile")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	view, err := reg.deps.Profiles.ListProfiles(ctx, claims.UserID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]Profile, 0, len(view.Profiles))
	for _, v := range view.Profiles {
		profile, p := profileOf(v)
		if p != nil {
			return nil, p
		}
		items = append(items, profile)
	}
	return &ProfileCollectionOutput{Body: ProfileCollection{
		Collection:          NewCollection(items),
		AvatarUploadEnabled: view.AvatarUploadEnabled,
	}}, nil
}

// createProfile runs the same authorization and write path as v1 POST
// /profiles; the household-manager check verifies a PIN-locked primary
// profile as updateProfile does.
func (reg *Registry) createProfile(ctx context.Context, in *ProfileCreateInput) (*ProfileCreatedOutput, error) {
	if reg.deps.Profiles == nil {
		return nil, unavailable("profile")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	req, p := in.Body.toRequest()
	if p != nil {
		return nil, p
	}
	if len(req.AllowedLibraryIDs) > 0 {
		if p := reg.rejectUnknownLibraries(ctx, &req.AllowedLibraryIDs); p != nil {
			return nil, p
		}
	}
	view, err := reg.deps.Profiles.CreateProfile(ctx, handlers.ProfileCreateCommand{
		UserID:          claims.UserID,
		ActiveProfileID: profileFrom(ctx),
		Request:         req,
		VerifyProfile:   scopeVerifier(ctx),
	})
	if err != nil {
		return nil, profileProblem(err)
	}
	profile, p := profileOf(view)
	if p != nil {
		return nil, p
	}
	return &ProfileCreatedOutput{Location: Prefix + "/profiles/" + string(profile.ID), Body: profile}, nil
}

// scopeVerifier is the v2 household-manager verifier: a PIN-locked primary
// profile counts as verified only when the viewer-access gate verified it by
// X-Profile-Token. An API-key credential is exempt from PIN verification at
// the gate; v1 does not let that exemption stand in for the PIN when
// managing the household, so a scope whose verification was skipped is
// rejected.
func scopeVerifier(ctx context.Context) func(profileID string) error {
	return func(profileID string) error {
		if scope, ok := scopeFrom(ctx); ok && scope.ProfileID == profileID && scope.ProfileVerified && !scope.PINVerificationSkipped {
			return nil
		}
		return access.ErrProfileUnverified
	}
}

// toRequest lowers the create body onto the v1 request, where an omitted
// member is the zero value v1 decodes for an absent one.
func (c ProfileCreate) toRequest() (handlers.ProfileCreateRequest, *Problem) {
	if c.PIN != nil {
		if p := validatePIN(Patch[string]{Present: true, Value: *c.PIN}); p != nil {
			return handlers.ProfileCreateRequest{}, p
		}
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	flag := func(p *bool) bool { return p != nil && *p }
	req := handlers.ProfileCreateRequest{
		Name:                       c.Name,
		Avatar:                     str(c.Avatar),
		PIN:                        str(c.PIN),
		IsChild:                    flag(c.IsChild),
		MaxContentRating:           str(c.MaxContentRating),
		QualityPreference:          str(c.QualityPreference),
		Language:                   str(c.Language),
		PreferredMetadataLanguage:  str(c.PreferredMetadataLanguage),
		SubtitleLanguage:           str(c.SubtitleLanguage),
		SubtitleMode:               str(c.SubtitleMode),
		AutoSkipIntro:              flag(c.AutoSkipIntro),
		AutoSkipCredits:            flag(c.AutoSkipCredits),
		AutoSkipRecap:              flag(c.AutoSkipRecap),
		AutoPlayNextPreview:        flag(c.AutoPlayNextPreview),
		ShowForcedSubtitles:        c.ShowForcedSubtitles,
		LibraryRestrictionsEnabled: flag(c.LibraryRestrictionsEnabled),
		MaxPlaybackQuality:         str(c.MaxPlaybackQuality),
	}
	if c.AllowedLibraryIDs != nil {
		ids, p := libraryIDsOf(*c.AllowedLibraryIDs)
		if p != nil {
			return req, p
		}
		req.AllowedLibraryIDs = ids
	}
	return req, nil
}

// updateProfile runs the same authorization and write path as v1 PUT
// /profiles/{id}; the household-manager check is scopeVerifier.
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
	if p := reg.rejectUnknownLibraries(ctx, req.AllowedLibraryIDs); p != nil {
		return nil, p
	}
	view, err := reg.deps.Profiles.UpdateProfile(ctx, handlers.ProfileUpdateCommand{
		UserID:          claims.UserID,
		ProfileID:       string(in.ID),
		ActiveProfileID: profileFrom(ctx),
		Request:         req,
		VerifyProfile:   scopeVerifier(ctx),
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

// rejectUnknownLibraries refuses an allowlist naming a library that does not
// exist. The store's row-by-row insert would otherwise hit the library
// foreign key (a 500) on Postgres, and persist a dangling id where no key
// is enforced. A nil or empty allowlist has nothing to check.
func (reg *Registry) rejectUnknownLibraries(ctx context.Context, ids *[]int) *Problem {
	if ids == nil || len(*ids) == 0 {
		return nil
	}
	if reg.deps.Libraries == nil {
		return unavailable("library")
	}
	existing, err := reg.deps.Libraries.ExistingIDs(ctx, *ids)
	if err != nil {
		return serviceProblem(err)
	}
	known := make(map[int]bool, len(existing))
	for _, id := range existing {
		known[id] = true
	}
	for _, id := range *ids {
		if !known[id] {
			return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationAllowedLibraryIDs, Code: codeInvalid, Detail: "unknown library identifier: " + strconv.Itoa(id)})
		}
	}
	return nil
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
	case apiErr.Status == http.StatusForbidden && apiErr.Code == codeProfileManagement:
		return NewProblem(TypeProfileVerificationRequired, apiErr.Message)
	}
	return serviceProblem(err)
}

// maxPINBytes is bcrypt's input limit; a longer PIN fails to hash.
const (
	maxPINBytes      = 72
	detailPINTooLong = "PIN must be at most 72 bytes"
)

// validatePIN rejects a present, non-null PIN that would otherwise be lowered
// to the "" clearing sentinel (only null clears) or overflow bcrypt. The
// schema's minLength/maxLength count runes, so the byte bound is enforced
// here.
func validatePIN(p Patch[string]) *Problem {
	if !p.Present || p.Null {
		return nil
	}
	var detail string
	switch {
	case p.Value == "":
		detail = "PIN must not be empty"
	case len(p.Value) > maxPINBytes:
		detail = detailPINTooLong
	default:
		return nil
	}
	return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
		WithErrors(ProblemError{Location: locationPIN, Code: codeOutOfRange, Detail: detail})
}

// toRequest lowers the presence-aware body onto the v1 request, where "" is
// the clearing value the store already understands.
func (u ProfileUpdate) toRequest() (handlers.ProfileUpdateRequest, *Problem) {
	if p := validatePIN(u.PIN); p != nil {
		return handlers.ProfileUpdateRequest{}, p
	}
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
		ids, p := libraryIDsOf(*u.AllowedLibraryIDs)
		if p != nil {
			return req, p
		}
		req.AllowedLibraryIDs = &ids
	}
	return req, nil
}

// libraryIDsOf lowers an allowlist onto store identifiers. The store
// replaces the allowlist row by row under a (user, profile, library) primary
// key, so a repeated identifier is the client's error, not a 500.
func libraryIDsOf(raw []ID) ([]int, *Problem) {
	ids := make([]int, 0, len(raw))
	seen := make(map[int]bool, len(raw))
	for _, id := range raw {
		n, err := intOfID(id)
		if err != nil || n <= 0 {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationAllowedLibraryIDs, Code: codeInvalid, Detail: "expected library identifiers"})
		}
		if seen[n] {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationAllowedLibraryIDs, Code: codeInvalid, Detail: "duplicate library identifier"})
		}
		seen[n] = true
		ids = append(ids, n)
	}
	return ids, nil
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

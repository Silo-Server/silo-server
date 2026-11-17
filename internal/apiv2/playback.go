package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const playbackCacheControl = "no-store"

const (
	opGetPlaybackCapabilities  = "getPlaybackCapabilities"
	opStartPlayback            = "startPlayback"
	opUpdatePlaybackProgress   = "updatePlaybackProgress"
	opStopPlayback             = "stopPlayback"
	opReplanPlayback           = "replanPlayback"
	opReportPlaybackRouteEvent = "reportPlaybackRouteEvent"
	opGetPlaybackInventory     = "getPlaybackInventory"
)

// PlaybackService is the application seam behind the v2 playback operations.
// It is *handlers.PlaybackHandler in production; the same code answers the
// v1 bridge routes.
type PlaybackService interface {
	PlaybackCapabilities(context.Context, int, string) (handlers.PlaybackCapabilitiesView, error)
	StartPlaybackV2(context.Context, handlers.PlaybackCaller, playback.StartRequestV3) (playback.DecisionResponseV3, error)
	ApplyProgressV2(context.Context, handlers.PlaybackCaller, string, handlers.PlaybackProgressCommand) (handlers.PlaybackMutationView, error)
	StopPlaybackV2(context.Context, handlers.PlaybackCaller, string, handlers.PlaybackStopCommand) (handlers.PlaybackMutationView, error)
	ReplanPlaybackV2(context.Context, handlers.PlaybackCaller, string, handlers.PlaybackReplanCommand) (playback.DecisionResponseV3, error)
	ReportRouteEventV2(context.Context, handlers.PlaybackCaller, handlers.PlaybackRouteEventCommand) error
	GetPlaybackInventoryV2(context.Context, handlers.PlaybackCaller, string) (playback.PlaybackInventoryV3, error)
}

type PlaybackCapabilities struct {
	BandwidthProbeURL           string `json:"bandwidth_probe_url,omitempty" format:"uri"`
	BandwidthProbeTargetKbps    *int   `json:"bandwidth_probe_target_kbps,omitempty" nullable:"false" doc:"With connection_speed_v1: the rate a quick speed test has to prove, the highest bitrate the server would plan for this viewer plus 25%. Absent when the server has no bitrates to set it from."`
	BandwidthProbeAutomatic     *bool  `json:"bandwidth_probe_automatic,omitempty" nullable:"false" doc:"With connection_speed_v1: false when the administrator turned automatic speed tests off. Test speed still works."`
	BandwidthProbeMaxAgeSeconds *int   `json:"bandwidth_probe_max_age_seconds,omitempty" nullable:"false" doc:"With connection_speed_v1: how long a speed reading stays current."`
	Capability
	InstallationID   ID                    `json:"installation_id,omitempty"`
	ProtocolVersions []int                 `json:"protocol_versions"`
	Features         []string              `json:"features"`
	Deliveries       []playback.DeliveryV3 `json:"deliveries"`
}
type PlaybackStartBody struct {
	InstallationID             ID                                 `json:"installation_id" minLength:"1"`
	ProtocolVersion            int                                `json:"protocol_version"`
	ClientFeatures             []string                           `json:"client_features"`
	FileID                     ID                                 `json:"file_id"`
	ProfileID                  ID                                 `json:"profile_id"`
	PlaybackAttemptID          string                             `json:"playback_attempt_id"`
	PreviousPlaybackAttemptID  string                             `json:"previous_playback_attempt_id,omitempty" doc:"The attempt whose explicit audio and subtitle selections this restart should carry when the request names neither. Omit it for legacy client-identity carry."`
	QualityPreference          string                             `json:"quality_preference" doc:"auto (the default), a resolution such as 1080p, or a rung label from available_qualities. auto plays the source unless the device cannot, a measured bandwidth_estimate_kbps cannot carry it, or it is above bandwidth_cap_kbps. original is a deprecated alias of auto."`
	AllowAlternateVersions     *bool                              `json:"allow_alternate_versions,omitempty" nullable:"false" doc:"False keeps the requested source file for this attempt, including quality changes and failure recovery. Omitted or true allows alternate versions."`
	SubtitleFidelityPreference playback.SubtitleFidelityV3        `json:"subtitle_fidelity_preference"`
	StartPosition              *float64                           `json:"start_position,omitempty" nullable:"false"`
	ProgressPersistence        playback.ProgressPersistenceV3     `json:"progress_persistence,omitempty" enum:"server,client"`
	AudioTrackID               string                             `json:"audio_track_id,omitempty"`
	AudioTrackIndex            *int                               `json:"audio_track_index,omitempty" nullable:"false"`
	CarriedAudioTrackID        string                             `json:"carried_audio_track_id,omitempty"`
	FileSelection              playback.FileSelectionV3           `json:"file_selection,omitempty" enum:"auto,explicit"`
	OfferedVirtualURI          string                             `json:"offered_virtual_uri,omitempty" doc:"Virtual candidate URI the client offered; not validated as a URI"`
	ForceRelink                bool                               `json:"force_relink,omitempty"`
	SubtitleTrackID            string                             `json:"subtitle_track_id,omitempty"`
	SubtitleTrackIndex         *int                               `json:"subtitle_track_index,omitempty" nullable:"false"`
	Metered                    bool                               `json:"metered,omitempty" doc:"Deprecated; ignored. A client on a metered link sends its cellular quality as quality_preference and bandwidth_cap_kbps instead." deprecated:"true"`
	BandwidthEstimateKbps      *int                               `json:"bandwidth_estimate_kbps,omitempty" nullable:"false" doc:"Measured throughput to this server, such as a bandwidth probe result. auto sends a source whose bitrate fits 80% of it as-is and lowers only one that does not. Omit it rather than send a guess."`
	BandwidthEstimateSource    string                             `json:"bandwidth_estimate_source,omitempty" enum:"probe,observed_rebuffer"`
	ObservedThroughputKbps     *int                               `json:"observed_throughput_kbps,omitempty" nullable:"false" minimum:"100" maximum:"1000000" doc:"What the plan that kept buffering received, measured as plan_summary throughput_kbps. Only with bandwidth_estimate_source observed_rebuffer; the server plans with, and learns, the lower of it and bandwidth_estimate_kbps."`
	LearnedLimit               string                             `json:"learned_limit,omitempty" enum:"ignore,clear"`
	BandwidthCapKbps           *int                               `json:"bandwidth_cap_kbps,omitempty" nullable:"false"`
	Capabilities               playback.ClientCodecCapabilitiesV3 `json:"client_capabilities"`
	ClientPlaybackContext      playback.ClientPlaybackContextV3   `json:"client_playback_context"`
}
type PlaybackBandwidthLimit struct {
	Kbps      int    `json:"kbps"`
	Source    string `json:"source" enum:"observed_rebuffer"`
	ExpiresAt string `json:"expires_at" format:"date-time"`
}

type PlaybackPlan struct {
	BandwidthLimit       *PlaybackBandwidthLimit         `json:"bandwidth_limit,omitempty"`
	ProtocolVersion      int                             `json:"protocol_version"`
	PlanID               string                          `json:"plan_id"`
	PlanAttemptKey       string                          `json:"plan_attempt_key"`
	SessionID            string                          `json:"session_id,omitempty"`
	ExpiresAt            string                          `json:"expires_at,omitempty"`
	Delivery             playback.DeliveryV3             `json:"delivery"`
	Stream               playback.StreamV3               `json:"stream"`
	Timeline             playback.TimelineV3             `json:"timeline"`
	SelectedTracks       playback.SelectedTracksV3       `json:"selected_tracks"`
	AudioTracks          []playback.AudioInventoryItemV3 `json:"audio_tracks,omitempty"`
	EffectiveRecipe      playback.EffectiveRecipeV3      `json:"effective_recipe"`
	Claims               playback.ValidationClaimsV3     `json:"claims"`
	Subtitle             playback.SubtitleDecisionV3     `json:"subtitle"`
	Transformations      []playback.TransformationV3     `json:"transformations"`
	AppliedQuirks        []playback.AppliedQuirkV3       `json:"applied_quirks"`
	RuntimeCorrections   []string                        `json:"runtime_corrections"`
	AvailableQualities   []playback.AvailableQualityV3   `json:"available_qualities" doc:"Fixed quality rungs a quality_change replan may request, in menu order. The source is not listed: quality_preference auto returns to it. Empty when no rung applies."`
	DegradationWarnings  []playback.DegradationWarningV3 `json:"degradation_warnings"`
	DecisionReason       string                          `json:"decision_reason"`
	RequestedMediaFileID ID                              `json:"requested_media_file_id"`
	EffectiveMediaFileID ID                              `json:"effective_media_file_id"`
	// SubstitutedFromFileID names the catalog row the client asked for when the
	// effective release differs from it, so a client can surface an honest
	// substitution notice without diffing the two ids first. It is the
	// requested row id and is omitted when the effective row is the requested
	// one. UI-only, additive: it mirrors PlanV3.SubstitutedFromFileID and is
	// deliberately excluded from plan identity hashing.
	SubstitutedFromFileID ID `json:"substituted_from_file_id,omitempty"`
	// SubstitutionReason is the machine-readable cause that accompanies
	// SubstitutedFromFileID: a dead release, a provider listing failure, a
	// transport failure, a decode rejection, or a release the planner cannot
	// deliver to this client. The set is additive; a client treats an unknown
	// value as a generic substitution rather than failing to render.
	SubstitutionReason     string         `json:"substitution_reason,omitempty" doc:"Machine-readable cause of a version substitution; only present with substituted_from_file_id. A client must treat an unknown value as a generic substitution."`
	Source                 PlaybackSource `json:"source"`
	SubtitleFidelityPolicy string         `json:"subtitle_fidelity_policy"`
	// EffectiveVirtualURI is the provider-neutral virtual:// candidate URI the
	// planner selected and probed when it substituted a real candidate for a
	// neutral catalog row, so a client can adopt the substituted candidate in
	// its version menu. Empty for a non-virtual source. It carries no provider
	// URL, token, or header.
	EffectiveVirtualURI string `json:"effective_virtual_uri,omitempty" doc:"Virtual candidate URI that is playing; not validated as a URI"`
	// DeliveryChange, when present, records that this replan changed the
	// serving route mid-session (the delivery and/or play method differs from
	// the plan the session was previously serving). It mirrors
	// PlanV3.DeliveryChange so a v2 client can tell a planned route change from
	// a silent mid-play swap. UI-only, additive. It is projected into this
	// apiv2-owned DTO (rather than reusing the domain type) so the native v2
	// schema is owned by the transport package, not a shared domain struct.
	DeliveryChange *DeliveryChange `json:"delivery_change,omitempty"`
	// VirtualSourceRevision is an opaque, non-secret revision of the resolved
	// virtual source candidate; it changes on a release rotation and stays fixed
	// while the same candidate is served. Empty for a non-virtual source.
	VirtualSourceRevision string `json:"virtual_source_revision,omitempty"`
	// TracksPending marks a plan whose audio and subtitle inventory is
	// deliberately provisional: the first-byte URL was handed back with the
	// minimal executable recipe (video plus the default audio) and the full
	// track enumeration is still being probed. It mirrors PlanV3.TracksPending
	// so the promised v2 wire contract carries it; a client that negotiated
	// deferred_track_inventory_v1 reads it and keeps the menu loading until the
	// follow-up inventory_updated push (or a poll of InventoryURL) lands. It
	// never changes stream selection.
	TracksPending bool `json:"tracks_pending,omitempty"`
	// AudioSelectionPending marks a plan whose selected audio track is
	// unverified: a cross-release fallback committed the request's ordinal onto
	// a replacement whose audio inventory could not be measured. It mirrors
	// PlanV3.AudioSelectionPending so a v2 client can render the selection as
	// pending instead of confirmed; the plan also carries the
	// audio_track_unverified degradation warning. It never changes stream
	// selection.
	AudioSelectionPending bool `json:"audio_selection_pending,omitempty"`
	// InventoryURL is the relative URL a client fetches (with ETag /
	// If-None-Match) to retrieve the refreshed audio and subtitle inventories
	// without replanning. It mirrors PlanV3.InventoryURL so a deferred v2 client
	// can poll for the pending → terminal inventory transition.
	InventoryURL  string `json:"inventory_url,omitempty" format:"uri-reference"`
	ActiveQuality string `json:"active_quality,omitempty" doc:"The available_qualities label this plan delivers, so a client can mark that menu entry: the label quality_preference named while the menu lists it and the encode keeps its resolution, otherwise the rung whose resolution class and bitrate the encode matches. Absent when the plan delivers no listed entry, such as the source played unchanged or a plain-resolution or auto request whose capped encode falls between two rungs. Present on servers that advertise plan_active_quality_v1."`
}

// DeliveryChange is the v2 projection of a mid-session serving-route change on
// a replan. It is a transport-owned DTO: the native schema name
// DeliveryChangeV3 must be owned by the apiv2 package, so the domain
// playback.DeliveryChangeV3 is projected into it at the handler boundary
// instead of being published directly. The wire shape is identical (it carries
// wire tokens only, no URLs, tokens, or headers).
type DeliveryChange struct {
	// PreviousDelivery is the delivery the session served before this plan, and
	// Delivery is the delivery this plan will serve.
	PreviousDelivery playback.DeliveryV3 `json:"previous_delivery,omitempty"`
	Delivery         playback.DeliveryV3 `json:"delivery,omitempty"`
	// PreviousPlayMethod and PlayMethod are the serve path before and after
	// (direct, remux, or transcode).
	PreviousPlayMethod playback.PlayMethod `json:"previous_play_method,omitempty"`
	PlayMethod         playback.PlayMethod `json:"play_method,omitempty"`
	// DeliveryChanged and PlayMethodChanged name which of the two actually
	// moved, so a client can react to a route-shape change without diffing the
	// tokens itself.
	DeliveryChanged   bool `json:"delivery_changed,omitempty"`
	PlayMethodChanged bool `json:"play_method_changed,omitempty"`
}

type PlaybackSource struct {
	MediaFileID        ID                          `json:"media_file_id"`
	DurationSeconds    *float64                    `json:"duration_seconds,omitempty"`
	Container          string                      `json:"container,omitempty"`
	VideoCodec         string                      `json:"video_codec,omitempty"`
	VideoProfile       string                      `json:"video_profile,omitempty"`
	VideoLevel         int                         `json:"video_level,omitempty"`
	BitDepth           int                         `json:"bit_depth,omitempty"`
	ColorRange         string                      `json:"color_range,omitempty"`
	Width              int                         `json:"width,omitempty"`
	Height             int                         `json:"height,omitempty"`
	FrameRate          float64                     `json:"frame_rate,omitempty"`
	BitrateKbps        int                         `json:"bitrate_kbps,omitempty"`
	DynamicRange       string                      `json:"dynamic_range,omitempty"`
	HDR10Plus          bool                        `json:"hdr10_plus"`
	DVProfile          int                         `json:"dolby_vision_profile,omitempty"`
	DVLevel            int                         `json:"dolby_vision_level,omitempty"`
	DVBLCompatID       int                         `json:"dv_bl_compat_id,omitempty"`
	DVBaseLayerProven  bool                        `json:"dv_base_layer_proven,omitempty"`
	DVEnhancementLayer playback.EnhancementLayerV3 `json:"dv_enhancement_layer"`
	AudioCodec         string                      `json:"audio_codec,omitempty"`
	AudioChannels      int                         `json:"audio_channels,omitempty"`
	AudioLayout        string                      `json:"audio_layout,omitempty"`
	VideoCopyUnsafe    bool                        `json:"video_copy_unsafe,omitempty"`
}

type PlaybackDecision struct {
	ProtocolVersion int                        `json:"protocol_version"`
	ServerFeatures  []string                   `json:"server_features"`
	Outcome         playback.DecisionOutcomeV3 `json:"outcome"`
	SessionID       string                     `json:"session_id,omitempty" doc:"Present only when outcome is playable. A terminal decision (adaptation_unavailable), from start or replan, carries none; read outcome before comparing sessions."`
	PlaybackPlan    *PlaybackPlan              `json:"playback_plan,omitempty"`
	Terminal        *playback.TerminalV3       `json:"terminal,omitempty"`
}

type PlaybackRequestHeaders struct {
	SiloDeviceID string `header:"X-Silo-Device-Id" doc:"The client's stable device identifier; a successful start records it in the profile's device registry (first 128 characters)" example:"iphone-1"`

	UserAgent       string `header:"User-Agent"`
	DeviceID        string `header:"X-Device-ID"`
	ClientName      string `header:"X-Client-Name" doc:"App name; when non-blank, the X-Client-* headers supply the whole client identity and X-Silo-Client* is ignored"`
	ClientVersion   string `header:"X-Client-Version"`
	ClientBuild     string `header:"X-Client-Build"`
	ClientChannel   string `header:"X-Client-Channel"`
	ClientModel     string `header:"X-Client-Model"`
	ClientPlatform  string `header:"X-Client-Platform"`
	ClientOSVersion string `header:"X-Client-OS-Version"`
	VioClient       string `header:"X-Vio-Client" doc:"App name first-party Vio clients send"`
	VioVersion      string `header:"X-Vio-Client-Version" doc:"Marketing version paired with X-Vio-Client"`
	VioBuild        string `header:"X-Vio-Client-Build" doc:"Opaque build identifier paired with X-Vio-Client"`
	VioChannel      string `header:"X-Vio-Client-Channel" doc:"Opaque distribution channel paired with X-Vio-Client"`
	SiloClient      string `header:"X-Silo-Client" doc:"App name the first-party clients send on every request; when non-blank and X-Client-Name is absent or blank, the X-Silo-Client* headers supply the client identity" example:"Silo Android TV"`
	SiloVersion     string `header:"X-Silo-Client-Version" doc:"Marketing version paired with X-Silo-Client" example:"1.0.0"`
	SiloBuild       string `header:"X-Silo-Client-Build" doc:"Opaque build identifier paired with X-Silo-Client" example:"5"`
	SiloChannel     string `header:"X-Silo-Client-Channel" doc:"Opaque distribution channel paired with X-Silo-Client" example:"release"`
}

// clientInfo resolves the caller's app identity from one header set, never a
// mix of both. X-Client-Name is the declared v2 header, so a caller that sends
// it keeps its X-Client-* values; the first-party apps and the web player send
// only X-Vio-Client* / X-Silo-Client*, the app identity headers the v1 bridge reads. Values are
// clamped here, where the request is read, like the bridge does.
func (h PlaybackRequestHeaders) clientInfo() playback.ClientInfo {
	info := playback.ClientInfo{Name: h.ClientName, Version: h.ClientVersion, Build: h.ClientBuild, Channel: h.ClientChannel}
	if strings.TrimSpace(h.ClientName) == "" {
		if strings.TrimSpace(h.VioClient) != "" {
			info = playback.ClientInfo{Name: h.VioClient, Version: h.VioVersion, Build: h.VioBuild, Channel: h.VioChannel}
		} else if strings.TrimSpace(h.SiloClient) != "" {
			info = playback.ClientInfo{Name: h.SiloClient, Version: h.SiloVersion, Build: h.SiloBuild, Channel: h.SiloChannel}
		}
	}
	return info.Normalized()
}

// PlaybackDeviceHeaders declare the client's device on a playback start. A
// successful start records it in the profile's device registry, as a settings
// read does; they do not change the playback device X-Device-ID names.
// Unlike the settings operations they carry no length limits: an over-long
// value is clamped when recorded, and must never refuse playback.
type PlaybackDeviceHeaders struct {
	SiloDeviceName     string `header:"X-Silo-Device-Name" doc:"Optional display name recorded on the device registry (first 120 characters)" example:"Living room"`
	SiloDevicePlatform string `header:"X-Silo-Device-Platform" doc:"Optional platform recorded on the device registry (first 40 characters)" example:"iOS"`
}

type PlaybackStartInput struct {
	PlaybackRequestHeaders
	PlaybackDeviceHeaders
	Body PlaybackStartBody
}
type PlaybackCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         PlaybackCapabilities
}
type PlaybackStartOutput struct {
	Body PlaybackDecision
}
type PlaybackProgressBody struct {
	InstallationID ID      `json:"installation_id" minLength:"1"`
	Sequence       int64   `json:"sequence" minimum:"1"`
	Position       float64 `json:"position" minimum:"0"`
	IsPaused       bool    `json:"is_paused"`
}
type PlaybackStopBody struct {
	InstallationID ID       `json:"installation_id" minLength:"1"`
	StopID         ID       `json:"stop_id" minLength:"1"`
	Sequence       int64    `json:"sequence,omitempty" minimum:"1"`
	Position       *float64 `json:"position,omitempty" nullable:"false" minimum:"0"`
	IsPaused       bool     `json:"is_paused,omitempty"`
}
type PlaybackProgressInput struct {
	PlaybackRequestHeaders
	SessionID ID `path:"session_id" minLength:"1"`
	Body      PlaybackProgressBody
}
type PlaybackStopInput struct {
	PlaybackRequestHeaders
	SessionID ID `path:"session_id" minLength:"1"`
	Body      PlaybackStopBody
}
type PlaybackAccepted struct {
	Sequence int64   `json:"sequence"`
	Position float64 `json:"position"`
	IsPaused bool    `json:"is_paused"`
}

// PlaybackMutation is the receipt for a progress or stop mutation. Outcome is
// applied, stale_sample or replayed for progress; stopped or replayed for
// stop. Accepted is the latest committed sample when one exists.
type PlaybackMutation struct {
	Outcome   string            `json:"outcome"`
	Accepted  *PlaybackAccepted `json:"accepted,omitempty"`
	StopID    ID                `json:"stop_id,omitempty"`
	HistoryID ID                `json:"history_id,omitempty"`
}
type PlaybackMutationOutput struct {
	Body PlaybackMutation
}

// PlaybackReplanBody is the v3 replan request: failure recovery, seek
// re-anchor, and track, quality or output changes for a live session.
type PlaybackReplanBody struct {
	InstallationID          ID                                 `json:"installation_id" minLength:"1"`
	ProtocolVersion         int                                `json:"protocol_version"`
	ClientFeatures          []string                           `json:"client_features,omitempty"`
	Operation               playback.ReplanOperationV3         `json:"operation,omitempty" enum:"failure_recovery,seek_reanchor,seek_failure_recovery,track_change,quality_change,output_change"`
	PlaybackAttemptID       string                             `json:"playback_attempt_id" minLength:"8" maxLength:"128"`
	ReplanRequestID         string                             `json:"replan_request_id" minLength:"8" maxLength:"128" doc:"Client-minted identity of this replan; a retry with the same body replays the durable decision"`
	FailedPlanID            string                             `json:"failed_plan_id" minLength:"8" maxLength:"128"`
	PlanAttemptID           string                             `json:"plan_attempt_id" minLength:"8" maxLength:"128"`
	PlanAttemptKey          string                             `json:"plan_attempt_key" minLength:"8" maxLength:"128"`
	AttemptedPlanKeys       []string                           `json:"attempted_plan_keys" maxItems:"16"`
	LocalMutations          []string                           `json:"local_mutations,omitempty" maxItems:"8"`
	AttemptCount            int                                `json:"attempt_count" minimum:"1" maximum:"8"`
	QualityPreference       string                             `json:"quality_preference" doc:"auto (the default), a resolution such as 1080p, or a rung label from available_qualities. auto plays the source unless the device cannot, a measured bandwidth_estimate_kbps cannot carry it, or it is above bandwidth_cap_kbps. original is a deprecated alias of auto."`
	PositionSeconds         float64                            `json:"position_seconds" minimum:"0"`
	Metered                 bool                               `json:"metered,omitempty" doc:"Deprecated; ignored. A client on a metered link sends its cellular quality as quality_preference and bandwidth_cap_kbps instead." deprecated:"true"`
	AutoFallback            *bool                              `json:"auto_fallback,omitempty" nullable:"false" doc:"Re-negotiates the session's version-fallback intent. Set true when the viewer re-arms Auto mid-session; omitted leaves the start-time intent unchanged. Never authorizes a healthy mid-play switch."`
	BandwidthEstimateKbps   *int                               `json:"bandwidth_estimate_kbps,omitempty" nullable:"false" doc:"Measured throughput to this server, such as a bandwidth probe result. auto sends a source whose bitrate fits 80% of it as-is and lowers only one that does not. Omit it rather than send a guess."`
	BandwidthEstimateSource string                             `json:"bandwidth_estimate_source,omitempty" enum:"probe,observed_rebuffer"`
	ObservedThroughputKbps  *int                               `json:"observed_throughput_kbps,omitempty" nullable:"false" minimum:"100" maximum:"1000000" doc:"What the plan that kept buffering received, measured as plan_summary throughput_kbps. Only with bandwidth_estimate_source observed_rebuffer; the server plans with, and learns, the lower of it and bandwidth_estimate_kbps."`
	LearnedLimit            string                             `json:"learned_limit,omitempty" enum:"ignore,clear"`
	AnswersPlanInvalidation string                             `json:"answers_plan_invalidation,omitempty" maxLength:"64" doc:"Echoes the reason from the plan_invalidated command this replan answers. Correlates a client that negotiated default_audio_reconcile_response_v1's response with the server's own withdrawal; omitting it on such a replan leaves the viewer's selection in place. Not trust-sensitive: at worst it names a correction the server already decided and announced."`
	BandwidthCapKbps        *int                               `json:"bandwidth_cap_kbps,omitempty" nullable:"false"`
	SelectedTracks          playback.SelectedTracksV3          `json:"selected_tracks"`
	Failure                 playback.FailureV3                 `json:"failure,omitzero"`
	Capabilities            playback.ClientCodecCapabilitiesV3 `json:"client_capabilities"`
	ClientPlaybackContext   playback.ClientPlaybackContextV3   `json:"client_playback_context"`
}
type PlaybackReplanInput struct {
	PlaybackRequestHeaders
	SessionID ID `path:"session_id" minLength:"1"`
	Body      PlaybackReplanBody
}
type PlaybackReplanOutput struct {
	Body PlaybackDecision
}

// PlaybackRouteEventBody is the v3 diagnostic route event plus a client-minted
// event identity. Diagnostics never control playback and a 429 means drop.
type PlaybackRouteEventBody struct {
	InstallationID        ID                `json:"installation_id" minLength:"1"`
	EventID               ID                `json:"event_id" minLength:"1" doc:"Client-minted UUID; a retry with the same id after a lost 202 is recorded once"`
	ProtocolVersion       int               `json:"protocol_version"`
	PlaybackAttemptID     string            `json:"playback_attempt_id" minLength:"8" maxLength:"128"`
	SessionID             string            `json:"session_id,omitempty" maxLength:"128"`
	PlanID                string            `json:"plan_id,omitempty" maxLength:"128"`
	PlanAttemptID         string            `json:"plan_attempt_id,omitempty" maxLength:"128"`
	PlanAttemptKey        string            `json:"plan_attempt_key,omitempty" maxLength:"128"`
	Event                 string            `json:"event" enum:"plan_selected,plan_invalidated,plan_failed,first_frame,terminal,stopped,runtime_correction_applied,runtime_correction_succeeded,runtime_correction_failed,seek_reanchor_requested,seek_reanchored,plan_summary"`
	FailureClassification string            `json:"failure_classification,omitempty" maxLength:"64"`
	FallbackReason        string            `json:"fallback_reason,omitempty" maxLength:"64"`
	AppliedQuirkIDs       []string          `json:"applied_quirk_ids,omitempty" maxItems:"16"`
	QuirkRegistryRevision string            `json:"quirk_registry_revision,omitempty" maxLength:"128"`
	OutputContextID       string            `json:"output_context_id,omitempty" maxLength:"128"`
	Diagnostics           map[string]string `json:"diagnostics" maxProperties:"32"`
}
type PlaybackRouteEventInput struct {
	PlaybackRequestHeaders
	Body PlaybackRouteEventBody
}

// PlaybackRouteEventReceipt acknowledges that the identified event was queued
// for recording. It is not proof of a durable write; a lost reply may be
// retried with the same event_id and is recorded once.
type PlaybackRouteEventReceipt struct {
	EventID ID     `json:"event_id"`
	Outcome string `json:"outcome" enum:"accepted"`
}
type PlaybackRouteEventOutput struct {
	Status int
	Body   PlaybackRouteEventReceipt
}

func registerPlayback(reg *Registry) {
	summaries := map[string]string{
		opGetPlaybackCapabilities:  "Discover the playback installation, protocol versions, features and deliveries this server offers the viewer.",
		opStartPlayback:            "Start a playback attempt and receive the protocol-v3 decision. Replaying the same attempt id with the same request returns the stored decision.",
		opUpdatePlaybackProgress:   "Report a sequenced progress sample. A lower sequence is stale; an equal sequence with a different payload conflicts.",
		opStopPlayback:             "Stop the session with a client-minted stop id and an optional final sample. Every later stop for the session replays the stored receipt.",
		opReplanPlayback:           "Replan the session after a route failure, a seek, or a track, quality or output change. The reply is a whole replacement plan.",
		opReportPlaybackRouteEvent: "Record one playback route diagnostic for an attempt this profile owns. Never retried automatically; a 429 means drop the event.",
		opGetPlaybackInventory:     "Retrieve the live audio and subtitle track inventory for an active playback session without restarting playback.",
	}
	op := func(method, path, id string) Operation {
		operation := Operation{Operation: humaOp(method, Prefix+"/playback"+path, id, "playback", summaries[id]), Class: ClassProfileScoped, ServiceBacked: true}
		if method != http.MethodGet {
			operation.RetrySafety = RetrySafetyDomainIdentity
			operation.Errors = []int{http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable}
		}
		switch id {
		case opStartPlayback:
			operation.DefaultStatus = http.StatusCreated
		case opReplanPlayback, opGetPlaybackInventory:
			operation.Errors = append(operation.Errors, http.StatusNotFound)
		case opReportPlaybackRouteEvent:
			operation.RetrySafety = RetrySafetyNonRetryable
			operation.DefaultStatus = http.StatusAccepted
			operation.Errors = []int{http.StatusForbidden, http.StatusUnprocessableEntity, http.StatusTooManyRequests, http.StatusServiceUnavailable}
		}
		return operation
	}
	Register(reg, op(http.MethodGet, "/capabilities", opGetPlaybackCapabilities), func(ctx context.Context, _ *CapabilityInput) (*PlaybackCapabilitiesOutput, error) {
		userID, profileID, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		if reg.deps.Playback == nil {
			return &PlaybackCapabilitiesOutput{CacheControl: playbackCacheControl, Body: PlaybackCapabilities{Capability: Capability{State: StateNotConfigured}, ProtocolVersions: []int{}, Features: []string{}, Deliveries: []playback.DeliveryV3{}}}, nil
		}
		view, err := reg.deps.Playback.PlaybackCapabilities(ctx, userID, profileID)
		if err != nil {
			return nil, playbackProblem(err)
		}
		body := PlaybackCapabilities{InstallationID: ID(view.InstallationID), Capability: Capability{State: view.State, Allowed: new(view.Allowed)}, ProtocolVersions: append([]int{}, view.ProtocolVersions...), Features: append([]string{}, view.Features...), Deliveries: append([]playback.DeliveryV3{}, view.Deliveries...)}
		if playback.HasFeatureV3(view.Features, playback.FeatureConnectionSpeedV1) {
			if r := requestFrom(ctx); r != nil {
				body.BandwidthProbeURL = reg.bandwidthProbeBaseURL(r) + Prefix + "/playback/bandwidth-probe?media_path=true"
			}
			body.BandwidthProbeAutomatic = new(view.BandwidthProbeAutomatic)
			body.BandwidthProbeMaxAgeSeconds = new(view.BandwidthProbeMaxAgeSeconds)
			if view.BandwidthProbeTargetKbps > 0 {
				body.BandwidthProbeTargetKbps = new(view.BandwidthProbeTargetKbps)
			}
		}
		return &PlaybackCapabilitiesOutput{CacheControl: playbackCacheControl, Body: body}, nil
	})
	Register(reg, op(http.MethodPost, "/start", opStartPlayback), func(ctx context.Context, in *PlaybackStartInput) (*PlaybackStartOutput, error) {
		if in.Body.BandwidthEstimateSource != "" && in.Body.BandwidthEstimateKbps == nil {
			return nil, NewProblem(TypeMalformedRequest, "bandwidth_estimate_source requires bandwidth_estimate_kbps")
		}
		caller, p := reg.playbackCaller(ctx, in.PlaybackRequestHeaders, in.Body.InstallationID)
		if p != nil {
			slog.WarnContext(ctx, "playback start rejected", "phase", "caller", "error", p.Title)
			return nil, p
		}
		fileID, p := in.Body.FileID.positive("body.file_id")
		if p != nil {
			slog.WarnContext(ctx, "playback start rejected", "phase", "file_id", "error", "invalid file_id")
			return nil, p
		}
		if string(in.Body.ProfileID) != caller.ProfileID {
			slog.WarnContext(ctx, "playback start rejected", "phase", "profile", "error", "profile mismatch")
			return nil, validationProblem("body.profile_id", "invalid", "Profile must match the authenticated viewer.")
		}
		request := in.Body.domain(fileID)
		validationData, err := json.Marshal(request)
		if err != nil {
			return nil, validationProblem("body", "invalid", "Invalid playback request.")
		}
		var validationRequest playback.StartRequestV3
		if err := json.Unmarshal(validationData, &validationRequest); err != nil {
			return nil, validationProblem("body", "invalid", "Invalid playback request.")
		}
		if _, err := validationRequest.NormalizeAndValidate(); err != nil {
			slog.WarnContext(ctx, "playback start rejected", "phase", "validation", "error", err.Error())
			return nil, validationProblem("body", "invalid", err.Error())
		}
		caller.DeclaredDevice = handlers.NewDeviceMetadata(in.SiloDeviceID, in.SiloDeviceName, in.SiloDevicePlatform)
		response, err := reg.deps.Playback.StartPlaybackV2(ctx, caller, request)
		if err != nil {
			return nil, playbackProblem(err)
		}
		return &PlaybackStartOutput{Body: playbackDecision(response)}, nil
	})
	Register(reg, op(http.MethodPost, "/{session_id}/progress", opUpdatePlaybackProgress), func(ctx context.Context, in *PlaybackProgressInput) (*PlaybackMutationOutput, error) {
		caller, p := reg.playbackCaller(ctx, in.PlaybackRequestHeaders, in.Body.InstallationID)
		if p != nil {
			return nil, p
		}
		view, err := reg.deps.Playback.ApplyProgressV2(ctx, caller, string(in.SessionID), handlers.PlaybackProgressCommand{Sequence: in.Body.Sequence, Position: in.Body.Position, IsPaused: in.Body.IsPaused})
		return playbackMutation(view, err)
	})
	registerPlaybackReplan(reg, op)
	registerPlaybackRouteEvents(reg, op)
	registerPlaybackInventory(reg, op)
	Register(reg, op(http.MethodDelete, "/{session_id}", opStopPlayback), func(ctx context.Context, in *PlaybackStopInput) (*PlaybackMutationOutput, error) {
		caller, p := reg.playbackCaller(ctx, in.PlaybackRequestHeaders, in.Body.InstallationID)
		if p != nil {
			return nil, p
		}
		if !playbackUUID(string(in.Body.StopID)) {
			return nil, validationProblem("body.stop_id", "invalid", "Expected a canonical UUID.")
		}
		if (in.Body.Sequence == 0) != (in.Body.Position == nil) {
			return nil, validationProblem("body", "invalid", "A final sample requires sequence and position together.")
		}
		view, err := reg.deps.Playback.StopPlaybackV2(ctx, caller, string(in.SessionID), handlers.PlaybackStopCommand{StopID: string(in.Body.StopID), Sequence: in.Body.Sequence, Position: in.Body.Position, IsPaused: in.Body.IsPaused})
		return playbackMutation(view, err)
	})
}
func registerPlaybackReplan(reg *Registry, op func(method, path, id string) Operation) {
	Register(reg, op(http.MethodPost, "/{session_id}/replan", opReplanPlayback), func(ctx context.Context, in *PlaybackReplanInput) (*PlaybackReplanOutput, error) {
		if in.Body.BandwidthEstimateSource != "" && in.Body.BandwidthEstimateKbps == nil {
			return nil, NewProblem(TypeMalformedRequest, "bandwidth_estimate_source requires bandwidth_estimate_kbps")
		}
		caller, p := reg.playbackCaller(ctx, in.PlaybackRequestHeaders, in.Body.InstallationID)
		if p != nil {
			return nil, p
		}
		request := in.Body.domain()
		if err := request.Validate(); err != nil {
			return nil, validationProblem("body", "invalid", err.Error())
		}
		// The digest fingerprints the exact accepted body, not the raw bytes,
		// so header-only differences never masquerade as a reused request id.
		canonical, err := json.Marshal(request)
		if err != nil {
			return nil, validationProblem("body", "invalid", "Invalid replan request.")
		}
		response, err := reg.deps.Playback.ReplanPlaybackV2(ctx, caller, string(in.SessionID), handlers.PlaybackReplanCommand{Request: request, Digest: handlers.ReplanDigestV3(canonical)})
		if err != nil {
			return nil, playbackProblem(err)
		}
		return &PlaybackReplanOutput{Body: playbackDecision(response)}, nil
	})
}
func (in PlaybackReplanBody) domain() playback.ReplanRequestV3 {
	return playback.ReplanRequestV3{ProtocolVersion: in.ProtocolVersion, ClientFeatures: in.ClientFeatures, Operation: in.Operation, PlaybackAttemptID: in.PlaybackAttemptID, ReplanRequestID: in.ReplanRequestID, FailedPlanID: in.FailedPlanID, PlanAttemptID: in.PlanAttemptID, PlanAttemptKey: in.PlanAttemptKey, AttemptedPlanKeys: in.AttemptedPlanKeys, LocalMutations: in.LocalMutations, AttemptCount: in.AttemptCount, QualityPreference: in.QualityPreference, PositionSeconds: in.PositionSeconds, Metered: in.Metered, AutoFallback: in.AutoFallback, BandwidthEstimateKbps: in.BandwidthEstimateKbps, BandwidthEstimateSource: in.BandwidthEstimateSource, ObservedThroughputKbps: in.ObservedThroughputKbps, LearnedLimit: in.LearnedLimit, AnswersPlanInvalidation: in.AnswersPlanInvalidation, BandwidthCapKbps: in.BandwidthCapKbps, SelectedTracks: in.SelectedTracks, Failure: in.Failure, Capabilities: in.Capabilities, ClientPlaybackContext: in.ClientPlaybackContext}
}
func registerPlaybackRouteEvents(reg *Registry, op func(method, path, id string) Operation) {
	Register(reg, op(http.MethodPost, "/route-events", opReportPlaybackRouteEvent), func(ctx context.Context, in *PlaybackRouteEventInput) (*PlaybackRouteEventOutput, error) {
		caller, p := reg.playbackCaller(ctx, in.PlaybackRequestHeaders, in.Body.InstallationID)
		if p != nil {
			return nil, p
		}
		if !playbackUUID(string(in.Body.EventID)) {
			return nil, validationProblem("body.event_id", "invalid", "Expected a canonical UUID.")
		}
		b := in.Body
		event := playback.RouteEventV3{ProtocolVersion: b.ProtocolVersion, PlaybackAttemptID: b.PlaybackAttemptID, SessionID: b.SessionID, PlanID: b.PlanID, PlanAttemptID: b.PlanAttemptID, PlanAttemptKey: b.PlanAttemptKey, Event: b.Event, FailureClassification: b.FailureClassification, FallbackReason: b.FallbackReason, AppliedQuirkIDs: b.AppliedQuirkIDs, QuirkRegistryRevision: b.QuirkRegistryRevision, OutputContextID: b.OutputContextID, Diagnostics: b.Diagnostics}
		if err := reg.deps.Playback.ReportRouteEventV2(ctx, caller, handlers.PlaybackRouteEventCommand{EventID: string(b.EventID), Event: event}); err != nil {
			return nil, playbackProblem(err)
		}
		return &PlaybackRouteEventOutput{Status: http.StatusAccepted, Body: PlaybackRouteEventReceipt{EventID: b.EventID, Outcome: adminHistoryAccepted}}, nil
	})
}

type PlaybackInventoryInput struct {
	SessionID   ID     `path:"session_id" minLength:"1" doc:"Playback session identifier"`
	IfNoneMatch string `header:"If-None-Match" doc:"Entity tag of the client's current cached inventory"`
}

type PlaybackInventoryOutput struct {
	CacheControl string `header:"Cache-Control"`
	ETag         string `header:"ETag"`
	Status       int
	Body         playback.PlaybackInventoryV3
}

func registerPlaybackInventory(reg *Registry, op func(method, path, id string) Operation) {
	operation := op(http.MethodGet, "/{session_id}/inventory", opGetPlaybackInventory)
	operation.Errors = append(operation.Errors, http.StatusNotFound)
	Register(reg, operation, func(ctx context.Context, in *PlaybackInventoryInput) (*PlaybackInventoryOutput, error) {
		if reg.deps.Playback == nil {
			return nil, NewProblem(TypeCapabilityNotConfigured, "Playback is not configured.")
		}
		userID, profileID, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		caller := handlers.PlaybackCaller{
			UserID:     userID,
			ProfileID:  profileID,
			RemoteAddr: clientip.FromContext(ctx),
		}
		inv, err := reg.deps.Playback.GetPlaybackInventoryV2(ctx, caller, string(in.SessionID))
		if err != nil {
			return nil, playbackProblem(err)
		}
		for i := range inv.SubtitleInventory {
			inv.SubtitleInventory[i].URL = playbackV2MediaURL(inv.SubtitleInventory[i].URL)
			inv.SubtitleInventory[i].FontBundleURL = playbackV2MediaURL(inv.SubtitleInventory[i].FontBundleURL)
		}
		etag := fmt.Sprintf("%q", inv.InventoryRevision)
		if in.IfNoneMatch != "" && (in.IfNoneMatch == etag || in.IfNoneMatch == inv.InventoryRevision || in.IfNoneMatch == "*") {
			return &PlaybackInventoryOutput{
				Status:       http.StatusNotModified,
				CacheControl: cacheControlPrivateNoCache,
				ETag:         etag,
			}, nil
		}
		return &PlaybackInventoryOutput{
			Status:       http.StatusOK,
			CacheControl: cacheControlPrivateNoCache,
			ETag:         etag,
			Body:         inv,
		}, nil
	})
}

func playbackUUID(raw string) bool {
	id, err := uuid.Parse(raw)
	return err == nil && id != uuid.Nil && id.String() == raw
}
func (reg *Registry) playbackCaller(ctx context.Context, headers PlaybackRequestHeaders, installation ID) (handlers.PlaybackCaller, *Problem) {
	if reg.deps.Playback == nil {
		return handlers.PlaybackCaller{}, NewProblem(TypeCapabilityNotConfigured, "Playback is not configured.")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return handlers.PlaybackCaller{}, p
	}
	if !playbackUUID(string(installation)) {
		return handlers.PlaybackCaller{}, validationProblem("body.installation_id", "invalid", "Expected the installation identifier from capabilities.")
	}
	client := headers.clientInfo()
	return handlers.PlaybackCaller{UserID: userID, ProfileID: profileID, InstallationID: string(installation), DeviceID: headers.DeviceID, ConnectionDeviceID: headers.SiloDeviceID, UserAgent: headers.UserAgent, ClientName: client.Name, ClientVersion: client.Version, ClientBuild: client.Build, ClientChannel: client.Channel, DeviceName: headers.ClientModel, Platform: headers.ClientPlatform, RemoteAddr: clientip.FromContext(ctx)}, nil
}

// playbackProblem maps a service error onto the shared problem catalog: the
// v1 400/426 bodies are v2 validation problems, a code the catalog names at
// the same status keeps that type, and anything else takes the status default.
func playbackProblem(err error) *Problem {
	operation, ok := errors.AsType[*handlers.PlaybackOperationError](err)
	if !ok {
		return NewProblem(TypeDependencyUnavailable, "Playback is temporarily unavailable.")
	}
	problem := NewProblem(playbackProblemType(operation.Status, operation.Code), operation.Message)
	if operation.RetryAfter > 0 {
		problem = problem.WithRetryAfter(operation.RetryAfter)
	}
	return problem
}
func playbackProblemType(status int, code string) ProblemType {
	if status == http.StatusBadRequest || status == http.StatusUpgradeRequired {
		status = http.StatusUnprocessableEntity
	}
	kind := TypeForStatus(status)
	if code == "playback_attempt_reused" {
		kind = TypeIdempotencyConflict
	}
	for _, candidate := range Catalog() {
		if candidate.ID == code && candidate.Status == status {
			return candidate
		}
	}
	return kind
}
func playbackMutation(view handlers.PlaybackMutationView, err error) (*PlaybackMutationOutput, error) {
	if err != nil {
		return nil, playbackProblem(err)
	}
	body := PlaybackMutation{Outcome: view.Outcome, StopID: ID(view.StopID), HistoryID: ID(view.HistoryID)}
	if view.Accepted != nil {
		body.Accepted = &PlaybackAccepted{Sequence: view.Accepted.Sequence, Position: view.Accepted.Position, IsPaused: view.Accepted.IsPaused}
	}
	return &PlaybackMutationOutput{Body: body}, nil
}

// domain projects the v2 body onto the planner's start request. Every declared
// field belongs here: the frozen /api/v1 bridge unmarshals the same body
// straight into the domain type, so a field that survives on v1 but is dropped
// here makes one surface silently behave differently from the other. Dropping
// file_selection alone turned every explicit version pick on /api/v2 into auto,
// which re-arms cross-version substitution, decode rotation and the
// known-failed-candidate retry.
func (in PlaybackStartBody) domain(fileID int) playback.StartRequestV3 {
	return playback.StartRequestV3{
		ProtocolVersion: in.ProtocolVersion, ClientFeatures: in.ClientFeatures, FileID: fileID,
		ProfileID: string(in.ProfileID), PlaybackAttemptID: in.PlaybackAttemptID,
		PreviousPlaybackAttemptID: in.PreviousPlaybackAttemptID,
		QualityPreference:         in.QualityPreference, AllowAlternateVersions: in.AllowAlternateVersions,
		SubtitleFidelityPreference: in.SubtitleFidelityPreference, StartPosition: in.StartPosition,
		ProgressPersistence: in.ProgressPersistence,
		AudioTrackID:        in.AudioTrackID, AudioTrackIndex: in.AudioTrackIndex,
		CarriedAudioTrackID: in.CarriedAudioTrackID,
		SubtitleTrackID:     in.SubtitleTrackID, SubtitleTrackIndex: in.SubtitleTrackIndex,
		FileSelection: in.FileSelection, ForceRelink: in.ForceRelink,
		OfferedVirtualURI:     in.OfferedVirtualURI,
		Metered:               in.Metered,
		BandwidthEstimateKbps: in.BandwidthEstimateKbps, BandwidthEstimateSource: in.BandwidthEstimateSource,
		ObservedThroughputKbps: in.ObservedThroughputKbps, LearnedLimit: in.LearnedLimit,
		BandwidthCapKbps: in.BandwidthCapKbps, Capabilities: in.Capabilities,
		ClientPlaybackContext: in.ClientPlaybackContext,
	}
}
func playbackDecision(in playback.DecisionResponseV3) PlaybackDecision {
	out := PlaybackDecision{ProtocolVersion: in.ProtocolVersion, ServerFeatures: in.ServerFeatures, Outcome: in.Outcome, SessionID: in.SessionID, Terminal: in.Terminal}
	if in.PlaybackPlan != nil {
		p := in.PlaybackPlan
		stream := p.Stream
		stream.URL = playbackV2MediaURL(stream.URL)
		out.PlaybackPlan = &PlaybackPlan{ProtocolVersion: p.ProtocolVersion, PlanID: p.PlanID, PlanAttemptKey: p.PlanAttemptKey, SessionID: p.SessionID, ExpiresAt: p.ExpiresAt, Delivery: p.Delivery, Stream: stream, Timeline: p.Timeline, SelectedTracks: p.SelectedTracks, EffectiveRecipe: p.EffectiveRecipe, BandwidthLimit: playbackBandwidthLimit(p.BandwidthLimit), Claims: p.Claims, Subtitle: playbackV2Subtitle(p.Subtitle), AudioTracks: p.AudioTracks, Transformations: p.Transformations, AppliedQuirks: p.AppliedQuirks, RuntimeCorrections: p.RuntimeCorrections, AvailableQualities: playbackQualities(p.AvailableQualities), DegradationWarnings: p.DegradationWarnings, DecisionReason: p.DecisionReason, RequestedMediaFileID: ID(strconv.Itoa(p.RequestedMediaFileID)), EffectiveMediaFileID: ID(strconv.Itoa(p.EffectiveMediaFileID)), SubstitutedFromFileID: playbackV2FileID(p.SubstitutedFromFileID), SubstitutionReason: p.SubstitutionReason, EffectiveVirtualURI: p.EffectiveVirtualURI, DeliveryChange: playbackV2DeliveryChange(p.DeliveryChange), VirtualSourceRevision: p.VirtualSourceRevision, TracksPending: p.TracksPending, AudioSelectionPending: p.AudioSelectionPending, InventoryURL: playbackV2InventoryURL(p.InventoryURL), Source: playbackSource(p.Source), SubtitleFidelityPolicy: p.SubtitleFidelityPolicy, ActiveQuality: p.ActiveQuality}
	}
	return out
}

// playbackQualities drops the source entry from the v2 menu: auto already
// plays the source whenever the device can, so a separate Original choice
// only duplicated it. The shared plan keeps the entry because v1 serializes
// that plan and is frozen.
func playbackQualities(in []playback.AvailableQualityV3) []playback.AvailableQualityV3 {
	return slices.DeleteFunc(slices.Clone(in), func(quality playback.AvailableQualityV3) bool {
		return quality.Label == playback.QualityOriginalV3
	})
}

// playbackV2FileID renders a catalog row id as an opaque ID, or "" when there
// is no row. The substitution fields use it so an unsubstituted plan omits them
// entirely instead of publishing "0".
func playbackV2FileID(id int) ID {
	if id <= 0 {
		return ""
	}
	return IDFromInt(int64(id))
}

// playbackV2DeliveryChange projects the domain route-change marker into the
// apiv2-owned DTO. It returns nil for an absent marker so the field stays
// omitted on the wire, and carries every field through unchanged so the two
// shapes cannot drift.
func playbackV2DeliveryChange(in *playback.DeliveryChangeV3) *DeliveryChange {
	if in == nil {
		return nil
	}
	return &DeliveryChange{
		PreviousDelivery:   in.PreviousDelivery,
		Delivery:           in.Delivery,
		PreviousPlayMethod: in.PreviousPlayMethod,
		PlayMethod:         in.PlayMethod,
		DeliveryChanged:    in.DeliveryChanged,
		PlayMethodChanged:  in.PlayMethodChanged,
	}
}

func playbackSource(in playback.SourceDescriptorV3) PlaybackSource {
	return PlaybackSource{MediaFileID: ID(strconv.Itoa(in.MediaFileID)), DurationSeconds: in.DurationSeconds, Container: in.Container, VideoCodec: in.VideoCodec, VideoProfile: in.VideoProfile, VideoLevel: in.VideoLevel, BitDepth: in.BitDepth, ColorRange: in.ColorRange, Width: in.Width, Height: in.Height, FrameRate: in.FrameRate, BitrateKbps: in.BitrateKbps, DynamicRange: in.DynamicRange, HDR10Plus: in.HDR10Plus, DVProfile: in.DVProfile, DVLevel: in.DVLevel, DVBLCompatID: in.DVBLCompatID, DVBaseLayerProven: in.DVBaseLayerProven, DVEnhancementLayer: in.DVEnhancementLayer, AudioCodec: in.AudioCodec, AudioChannels: in.AudioChannels, AudioLayout: in.AudioLayout, VideoCopyUnsafe: in.VideoCopyUnsafe}
}

// playbackV2MediaURL projects an API-local v1 media, sidecar or font URL into
// the v2 namespace without touching its signed query. Any other URL (absolute,
// or proxy) is returned unchanged. Domain session-relative routes are API-local.
func playbackV2MediaURL(raw string) string {
	if strings.HasPrefix(raw, "/stream/") || strings.HasPrefix(raw, "/playback/transcode/") {
		return Prefix + raw
	}
	if strings.HasPrefix(raw, "/api/v1/stream/") || strings.HasPrefix(raw, "/api/v1/playback/transcode/") {
		return Prefix + strings.TrimPrefix(raw, "/api/v1")
	}
	return raw
}

// playbackV2InventoryURL projects the plan's inventory URL into the v2
// namespace. The handler already builds it as an /api/v2 path, so this is
// idempotent there; the helper also normalizes a v1-prefixed value so the
// field cannot leak a frozen-surface path. Any other value (empty, absolute) is
// returned unchanged.
func playbackV2InventoryURL(raw string) string {
	if strings.HasPrefix(raw, "/api/v1/playback/") {
		return Prefix + strings.TrimPrefix(raw, "/api/v1")
	}
	return raw
}

// playbackV2Subtitle copies the subtitle decision so the v2 projection never
// mutates the persisted decision, then projects every sidecar and font URL.
func playbackV2Subtitle(in playback.SubtitleDecisionV3) playback.SubtitleDecisionV3 {
	out := in
	// The v2 contract requires an array, including when the source is empty.
	out.Inventory = append([]playback.SubtitleInventoryItemV3{}, in.Inventory...)
	for i := range out.Inventory {
		out.Inventory[i].URL = playbackV2MediaURL(out.Inventory[i].URL)
		out.Inventory[i].FontBundleURL = playbackV2MediaURL(out.Inventory[i].FontBundleURL)
	}
	if in.Artifact != nil {
		artifact := *in.Artifact
		artifact.URL = playbackV2MediaURL(artifact.URL)
		out.Artifact = &artifact
	}
	return out
}

func playbackBandwidthLimit(limit *playback.BandwidthLimitV3) *PlaybackBandwidthLimit {
	if limit == nil {
		return nil
	}
	return &PlaybackBandwidthLimit{Kbps: limit.Kbps, Source: limit.Source, ExpiresAt: limit.ExpiresAt}
}

// bandwidthProbeBaseURL is the origin the speed test URL names: the one the
// client reached, as a reverse proxy forwards it, since r.Host behind a
// TLS-terminating proxy is the internal host the proxy dialed. A client on
// the server's own network keeps the LAN address it used. A proxy that
// rewrites Host to a bare IP address leaves only the address it dialed, which
// a client elsewhere may not reach, unless it is a trusted proxy that names
// the client's host in a single X-Forwarded-Host (clientip.ForwardedHost); then
// the URL is on the configured public URL, when there is one. A network access
// provider's path keeps Host, which can be an overlay IP, so it always keeps
// the origin it reached.
func (reg *Registry) bandwidthProbeBaseURL(r *http.Request) string {
	_, err := netip.ParseAddr((&url.URL{Host: r.Host}).Hostname())
	hostRewritten := err == nil && clientip.ForwardedHost(r) == "" && clientip.HasForwardingHeaders(r.Header) &&
		netaccess.PathFromContext(r.Context()).IsDefault()
	if base := reg.publicBaseURL(); hostRewritten && base != "" {
		return base
	}
	return handlers.RequestBaseURL(r)
}

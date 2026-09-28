package apiv2

import (
	"context"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/streamlocation"
)

type AdminPlaybackSessionService interface {
	AdminPlaybackSessionsAvailable() bool
	ReadAdminPlaybackSessions(context.Context, handlers.PlaybackSessionsQuery, string, int) ([]handlers.AdminPlaybackSessionView, error)
	ReadAdminPlaybackSummary(context.Context, handlers.PlaybackSessionsQuery, int) ([]handlers.AdminPlaybackSessionView, int, error)
}

// AdminPlaybackSession is an observation, not a sequenced control receipt.
type AdminPlaybackSession struct {
	SessionID                string  `json:"session_id"`
	UserID                   ID      `json:"user_id"`
	Username                 string  `json:"username"`
	ProfileID                string  `json:"profile_id"`
	ProfileName              string  `json:"profile_name,omitempty"`
	MediaFileID              ID      `json:"media_file_id"`
	RequestedMediaFileID     ID      `json:"requested_media_file_id"`
	ContentID                string  `json:"content_id,omitempty"`
	MediaTitle               string  `json:"media_title"`
	MediaType                string  `json:"media_type"`
	SeriesName               string  `json:"series_name,omitempty"`
	EpisodeName              string  `json:"episode_name,omitempty"`
	SeasonNumber             *int    `json:"season_number,omitempty"`
	EpisodeNumber            *int    `json:"episode_number,omitempty"`
	PosterURL                string  `json:"poster_url,omitempty"`
	PlayMethod               string  `json:"play_method"`
	ReportingNode            string  `json:"reporting_node"`
	NodeDisplayName          string  `json:"node_display_name,omitempty"`
	FileDuration             *int    `json:"file_duration"`
	StartedAt                Instant `json:"started_at"`
	UpdatedAt                Instant `json:"updated_at"`
	PositionSeconds          float64 `json:"position_seconds"`
	IsPaused                 bool    `json:"is_paused"`
	HasPlaybackControl       bool    `json:"has_playback_control"`
	ClientIP                 string  `json:"client_ip,omitempty"`
	StreamLocation           string  `json:"stream_location" enum:"local,remote" doc:"Local or remote classification used by the server bitrate policy."`
	ClientName               string  `json:"client_name,omitempty"`
	ClientVersion            string  `json:"client_version,omitempty"`
	ClientBuild              string  `json:"client_build,omitempty"`
	ClientChannel            string  `json:"client_channel,omitempty"`
	ClientLabel              string  `json:"client_label,omitempty"`
	ClientLabelFull          string  `json:"client_label_full,omitempty"`
	ClientUserAgent          string  `json:"client_user_agent,omitempty"`
	AudioTrackIndex          int     `json:"audio_track_index"`
	TranscodeAudio           bool    `json:"transcode_audio"`
	StreamBitrateKbps        *int    `json:"stream_bitrate_kbps"`
	TargetResolution         string  `json:"target_resolution,omitempty"`
	TargetVideoCodec         string  `json:"target_video_codec,omitempty"`
	TargetAudioCodec         string  `json:"target_audio_codec,omitempty"`
	TargetAudioChannels      *int    `json:"target_audio_channels,omitempty"`
	TargetBitrateKbps        *int    `json:"target_bitrate_kbps"`
	TranscodeHWAccel         string  `json:"transcode_hw_accel,omitempty"`
	ToneMapMode              string  `json:"tone_map_mode,omitempty"`
	OutputContainer          string  `json:"output_container,omitempty" doc:"Container the serving transport produces: fmp4, mpegts, or the source container for direct play. Absent when the node did not report it; clients must not infer it from play_method or the source."`
	OutputProtocol           string  `json:"output_protocol,omitempty" doc:"Delivery protocol, hls or http, independent of the container. Absent when the node did not report it."`
	SourceContainer          string  `json:"source_container,omitempty"`
	SourceBitrateKbps        *int    `json:"source_bitrate_kbps"`
	SourceVideoCodec         string  `json:"source_video_codec,omitempty"`
	SourceVideoResolution    string  `json:"source_video_resolution,omitempty"`
	SourceAudioCodec         string  `json:"source_audio_codec,omitempty"`
	SourceAudioChannels      *int    `json:"source_audio_channels"`
	SourceAudioLanguage      string  `json:"source_audio_language,omitempty"`
	SourceAudioTitle         string  `json:"source_audio_title,omitempty"`
	SourceAudioLayout        string  `json:"source_audio_layout,omitempty"`
	RequestedVideoCodec      string  `json:"requested_video_codec,omitempty"`
	RequestedVideoResolution string  `json:"requested_video_resolution,omitempty"`
	VideoDecision            string  `json:"video_decision,omitempty"`
	AudioDecision            string  `json:"audio_decision,omitempty"`
	EffectivePlayMethod      string  `json:"effective_play_method,omitempty" doc:"Whole-session method: direct, remux, direct_stream (copied video, converted audio) or transcode; absent when unknown"`
	IsJellyfinClient         bool    `json:"is_jellyfin_client,omitzero"`
	RoutingNetworkProvider   *string `json:"routing_network_provider,omitempty" doc:"Access network selected for playback: empty means default; absent means unknown; otherwise the validated provider identifier."`
	RoutingWorkload          string  `json:"routing_workload,omitempty"`
	RoutingExecution         string  `json:"routing_execution,omitempty"`
	RoutingExecutionNodeID   *ID     `json:"routing_execution_node_id,omitempty"`
	RoutingExecutionNodeName string  `json:"routing_execution_node_name,omitempty"`
	RoutingEgress            string  `json:"routing_egress,omitempty"`
	RoutingEgressNodeID      *ID     `json:"routing_egress_node_id,omitempty"`
	RoutingEgressNodeName    string  `json:"routing_egress_node_name,omitempty"`
}

// directStreamPlayMethod is the native bucket for copied video with converted
// audio. The frozen bridge's shared read model calls it "audio".
const directStreamPlayMethod = "direct_stream"

func nativeEffectivePlayMethod(bridge string) string {
	if bridge == "audio" {
		return directStreamPlayMethod
	}
	return bridge
}

func adminSessionNodeID(id *int) *ID {
	if id == nil {
		return nil
	}
	return new(IDFromInt(int64(*id)))
}
func adminPlaybackSessionOf(v handlers.AdminPlaybackSessionView) AdminPlaybackSession {
	provider := ""
	if v.RoutingNetworkProvider != nil {
		provider = *v.RoutingNetworkProvider
	}
	location := v.StreamLocation
	if location != string(streamlocation.Local) && location != string(streamlocation.Remote) {
		location = string(streamlocation.FromMetadata(v.ClientIP, provider))
	}
	return AdminPlaybackSession{
		SessionID:                v.SessionID,
		UserID:                   IDFromInt(int64(v.UserID)),
		Username:                 v.Username,
		ProfileID:                v.ProfileID,
		ProfileName:              v.ProfileName,
		MediaFileID:              IDFromInt(int64(v.MediaFileID)),
		RequestedMediaFileID:     IDFromInt(int64(v.RequestedMediaFileID)),
		ContentID:                v.ContentID,
		MediaTitle:               v.MediaTitle,
		MediaType:                v.MediaType,
		SeriesName:               v.SeriesName,
		EpisodeName:              v.EpisodeName,
		SeasonNumber:             v.SeasonNumber,
		EpisodeNumber:            v.EpisodeNumber,
		PosterURL:                v.PosterURL,
		PlayMethod:               v.PlayMethod,
		ReportingNode:            v.ReportingNode,
		NodeDisplayName:          v.NodeDisplayName,
		FileDuration:             v.FileDuration,
		StartedAt:                NewInstant(v.StartedAt),
		UpdatedAt:                NewInstant(v.UpdatedAt),
		PositionSeconds:          v.PositionSeconds,
		IsPaused:                 v.IsPaused,
		HasPlaybackControl:       v.HasPlaybackControl,
		ClientIP:                 v.ClientIP,
		StreamLocation:           location,
		ClientName:               v.ClientName,
		ClientVersion:            v.ClientVersion,
		ClientBuild:              v.ClientBuild,
		ClientChannel:            v.ClientChannel,
		ClientLabel:              v.ClientLabel,
		ClientLabelFull:          v.ClientLabelFull,
		ClientUserAgent:          v.ClientUserAgent,
		AudioTrackIndex:          v.AudioTrackIndex,
		TranscodeAudio:           v.TranscodeAudio,
		StreamBitrateKbps:        v.StreamBitrateKbps,
		TargetResolution:         v.TargetResolution,
		TargetVideoCodec:         v.TargetVideoCodec,
		TargetAudioCodec:         v.TargetAudioCodec,
		TargetAudioChannels:      v.TargetAudioChannels,
		TargetBitrateKbps:        v.TargetBitrateKbps,
		TranscodeHWAccel:         v.TranscodeHWAccel,
		ToneMapMode:              v.ToneMapMode,
		OutputContainer:          v.OutputContainer,
		OutputProtocol:           v.OutputProtocol,
		SourceContainer:          v.SourceContainer,
		SourceBitrateKbps:        v.SourceBitrateKbps,
		SourceVideoCodec:         v.SourceVideoCodec,
		SourceVideoResolution:    v.SourceVideoResolution,
		SourceAudioCodec:         v.SourceAudioCodec,
		SourceAudioChannels:      v.SourceAudioChannels,
		SourceAudioLanguage:      v.SourceAudioLanguage,
		SourceAudioTitle:         v.SourceAudioTitle,
		SourceAudioLayout:        v.SourceAudioLayout,
		RequestedVideoCodec:      v.RequestedVideoCodec,
		RequestedVideoResolution: v.RequestedVideoResolution,
		VideoDecision:            v.VideoDecision,
		AudioDecision:            v.AudioDecision,
		EffectivePlayMethod:      nativeEffectivePlayMethod(v.EffectivePlayMethod),
		IsJellyfinClient:         v.IsJellyfinClient,
		RoutingNetworkProvider:   v.RoutingNetworkProvider,
		RoutingWorkload:          v.RoutingWorkload,
		RoutingExecution:         v.RoutingExecution,
		RoutingExecutionNodeID:   adminSessionNodeID(v.RoutingExecutionNodeID),
		RoutingExecutionNodeName: v.RoutingExecutionNodeName,
		RoutingEgress:            v.RoutingEgress,
		RoutingEgressNodeID:      adminSessionNodeID(v.RoutingEgressNodeID),
		RoutingEgressNodeName:    v.RoutingEgressNodeName,
	}
}

type AdminPlaybackSessionsInput struct {
	LimitParam
	Cursor string `query:"cursor" maxLength:"8192"`
	UserID ID     `query:"user_id" doc:"Only live observations belonging to this login account"`
}
type AdminPlaybackSessionsOutput struct {
	Body Collection[AdminPlaybackSession]
}
type AdminPlaybackSessionCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminPlaybackSessionCapabilitiesOutputBody
}

type AdminPlaybackSessionCapabilitiesOutputBody struct {
	Capability
	Available                 bool     `json:"available"`
	UserFilter                bool     `json:"user_filter"`
	Summary                   bool     `json:"summary"`
	NodeObservations          bool     `json:"node_observations"`
	EffectivePlayMethod       bool     `json:"effective_play_method"`
	EffectivePlayMethodValues []string `json:"effective_play_method_values"`
	IsJellyfinClient          bool     `json:"is_jellyfin_client"`
	TranscodeHWAccel          bool     `json:"transcode_hw_accel"`
	ToneMapMode               bool     `json:"tone_map_mode"`
	ToneMapModeValues         []string `json:"tone_map_mode_values"`
	ClientBuild               bool     `json:"client_build"`
	ClientChannel             bool     `json:"client_channel"`
	TargetAudioChannels       bool     `json:"target_audio_channels"`
	NetworkAccessRoute        bool     `json:"network_access_route"`
	StreamLocation            bool     `json:"stream_location"`
	NodeRouting               bool     `json:"node_routing"`
	OutputFormat              bool     `json:"output_format" doc:"Rows may carry output_container and output_protocol"`
}

const opListAdminPlaybackSessions = "listAdminPlaybackSessions"

func registerAdminPlaybackSessions(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := func(path, id string) Operation {
		return Operation{Operation: humaOp("GET", Prefix+"/admin/sessions"+path, id, "admin", "Read live playback observations; these do not confer control authority. Pagination bounds the SQL source and response using session identity."), Class: ClassActingAdmin, ServiceBacked: true}
	}
	Register(reg, op("/capabilities", "getAdminPlaybackSessionCapabilities"), func(_ context.Context, _ *CapabilityInput) (*AdminPlaybackSessionCapabilitiesOutput, error) {
		out := new(AdminPlaybackSessionCapabilitiesOutput)
		out.Body.Available = reg.deps.AdminPlaybackSessions != nil && reg.deps.AdminPlaybackSessions.AdminPlaybackSessionsAvailable()
		out.Body.UserFilter = out.Body.Available
		out.Body.Summary = out.Body.Available
		out.Body.NodeObservations = reg.deps.AdminNodeSessions != nil && reg.deps.AdminNodeSessions.Available()
		f := handlers.AdminPlaybackSessionFeatures()
		out.Body.EffectivePlayMethod = f.EffectivePlayMethod
		out.Body.EffectivePlayMethodValues = make([]string, len(f.EffectivePlayMethodValues))
		for i, method := range f.EffectivePlayMethodValues {
			out.Body.EffectivePlayMethodValues[i] = nativeEffectivePlayMethod(method)
		}
		out.Body.IsJellyfinClient = f.IsJellyfinClient
		out.Body.TranscodeHWAccel = f.TranscodeHWAccel
		out.Body.ToneMapMode = f.ToneMapMode
		out.Body.ToneMapModeValues = f.ToneMapModeValues
		out.Body.ClientBuild = f.ClientBuild
		out.Body.ClientChannel = f.ClientChannel
		out.Body.TargetAudioChannels = f.TargetAudioChannels
		out.Body.NodeRouting = f.NodeRouting
		out.Body.NetworkAccessRoute = true
		out.Body.StreamLocation = true
		out.Body.OutputFormat = true
		return out, nil
	})
	Register(reg, op("", opListAdminPlaybackSessions), func(ctx context.Context, in *AdminPlaybackSessionsInput) (*AdminPlaybackSessionsOutput, error) {
		if reg.deps.AdminPlaybackSessions == nil || !reg.deps.AdminPlaybackSessions.AdminPlaybackSessionsAvailable() {
			return nil, unavailable("administrator playback sessions")
		}
		query, p := adminSessionQuery(in.UserID)
		if p != nil {
			return nil, p
		}
		filter := strconv.Itoa(in.Limit)
		if query.UserID > 0 {
			filter += "/" + strconv.Itoa(query.UserID)
		}
		const sessionSort = "session_id"
		scope := CursorScope{OperationID: opListAdminPlaybackSessions, Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: filter, Sort: sessionSort, Tiebreaker: sessionSort}
		var after string
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
		}
		rows, err := reg.deps.AdminPlaybackSessions.ReadAdminPlaybackSessions(ctx, query, after, in.Limit)
		if err != nil {
			return nil, serviceProblem(err)
		}

		items := make([]AdminPlaybackSession, 0, in.Limit)
		next := ""
		for _, row := range rows {
			if len(items) == in.Limit {
				next, err = cursors.Encode(scope, items[len(items)-1].SessionID)
				if err != nil {
					return nil, serviceProblem(err)
				}
				break
			}
			items = append(items, adminPlaybackSessionOf(row))
		}
		return &AdminPlaybackSessionsOutput{Body: Paginated(items, next)}, nil
	})
	summary := op("/summary", "getAdminPlaybackSummary")
	summary.Description = "Read a bounded sample and total count of live playback observations, optionally filtered by account, without session identifiers or diagnostic metadata."
	Register(reg, summary, func(ctx context.Context, in *AdminPlaybackSummaryInput) (*AdminPlaybackSummaryOutput, error) {
		if reg.deps.AdminPlaybackSessions == nil || !reg.deps.AdminPlaybackSessions.AdminPlaybackSessionsAvailable() {
			return nil, unavailable("administrator playback sessions")
		}
		query, p := adminSessionQuery(in.UserID)
		if p != nil {
			return nil, p
		}
		rows, count, err := reg.deps.AdminPlaybackSessions.ReadAdminPlaybackSummary(ctx, query, in.Limit)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := new(AdminPlaybackSummaryOutput)
		out.Body.Count = count
		out.Body.Items = make([]AdminPlaybackSummaryItem, 0, len(rows))
		for _, row := range rows {
			out.Body.Items = append(out.Body.Items, AdminPlaybackSummaryItem{
				UserID: IDFromInt(int64(row.UserID)), MediaTitle: row.MediaTitle, MediaType: row.MediaType,
				SeriesName: row.SeriesName, EpisodeName: row.EpisodeName, SeasonNumber: row.SeasonNumber,
				EpisodeNumber: row.EpisodeNumber, IsPaused: row.IsPaused,
			})
		}
		return out, nil
	})
}

// A fixed projection keeps diagnostic identifiers and network details out of summary credentials.
type AdminPlaybackSummaryItem struct {
	UserID        ID     `json:"user_id"`
	MediaTitle    string `json:"media_title"`
	MediaType     string `json:"media_type"`
	SeriesName    string `json:"series_name,omitempty"`
	EpisodeName   string `json:"episode_name,omitempty"`
	SeasonNumber  *int   `json:"season_number,omitempty"`
	EpisodeNumber *int   `json:"episode_number,omitempty"`
	IsPaused      bool   `json:"is_paused"`
}
type AdminPlaybackSummaryInput struct {
	UserID ID  `query:"user_id" doc:"Only live observations belonging to this login account; omitted means all accounts"`
	Limit  int `query:"limit" minimum:"1" maximum:"100" default:"20" doc:"Maximum sample size; count includes all matching observations"`
}
type AdminPlaybackSummaryOutput struct {
	Body struct {
		Count int                        `json:"count" minimum:"0"`
		Items []AdminPlaybackSummaryItem `json:"items" maxItems:"100" doc:"Most recently started matching observations; no pagination"`
	}
}

func adminSessionQuery(userID ID) (handlers.PlaybackSessionsQuery, *Problem) {
	query := handlers.PlaybackSessionsQuery{}
	if userID != "" {
		id, err := strconv.Atoi(string(userID))
		if err != nil || id < 1 {
			return query, NewProblem(TypeValidationFailed, "Invalid playback account filter.")
		}
		query.UserID = id
	}
	return query, nil
}

func (c AdminPlaybackSessionCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}

package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// AdminPlaybackHistoryService is the slice of *handlers.AdminHandler
// listAdminPlaybackHistory and getAdminPlaybackHistoryEntry use.
type AdminPlaybackHistoryService interface {
	ListAdminPlaybackHistoryPage(context.Context, handlers.AdminPlaybackHistoryFilter, *handlers.AdminPlaybackHistoryPageKey, int) (handlers.AdminPlaybackHistoryPage, error)
	ReadAdminPlaybackHistoryEntry(context.Context, string) (*handlers.AdminPlaybackHistoryRow, error)
}

// AdminPlaybackHistoryListInput is the listAdminPlaybackHistory query: the
// original filters plus rich analytics facets and cursor paging.
type AdminPlaybackHistoryListInput struct {
	LimitParam
	Cursor           string `query:"cursor" maxLength:"8192" doc:"Opaque cursor from page.next_cursor"`
	UserID           ID     `query:"user_id" doc:"Only attempts by this login account"`
	ProfileID        string `query:"profile_id" maxLength:"1024" doc:"Only attempts by this household profile"`
	MediaItemID      string `query:"media_item_id" maxLength:"1024" doc:"Only attempts of this catalog item"`
	SeriesID         string `query:"series_id" maxLength:"1024" doc:"Only attempts of episodes of this series"`
	LibraryID        ID     `query:"library_id" doc:"Only attempts of media belonging to this library"`
	Completed        string `query:"completed" enum:"all,true,false" default:"all" doc:"Completion filter; all returns every finalized attempt"`
	EndedAfter       string `query:"ended_after" format:"date-time" doc:"Only attempts that ended at or after this instant (inclusive)"`
	EndedBefore      string `query:"ended_before" format:"date-time" doc:"Only attempts that ended strictly before this instant (exclusive), so windows that share a bound never list an attempt twice"`
	StartedAfter     string `query:"started_after" format:"date-time" doc:"Only attempts that started at or after this instant (inclusive)"`
	StartedBefore    string `query:"started_before" format:"date-time" doc:"Only attempts that started strictly before this instant (exclusive)"`
	Search           string `query:"search" maxLength:"256" doc:"Case-insensitive search across media title, series title, account, profile, client, platform, client IP, or session ID"`
	PlayMethod       string `query:"play_method" enum:"all,direct,remux,transcode" default:"all" doc:"Filter by finalized play method"`
	DevicePlatform   string `query:"device_platform" maxLength:"128" doc:"Filter by reported device platform (case-insensitive; 'unknown' matches rows without a platform)"`
	ClientName       string `query:"client_name" maxLength:"256" doc:"Filter by reported client name (case-insensitive; 'unknown' matches rows without a client name)"`
	SourceVideoCodec string `query:"source_video_codec" maxLength:"64" doc:"Filter by source media file video codec (case-insensitive; 'unknown' matches rows without a probed video codec)"`
	SubtitleBurnIn   string `query:"subtitle_burn_in" enum:"all,true,false" default:"all" doc:"Filter by whether the finalized session used subtitle burn-in"`
	StreamLocation   string `query:"stream_location" enum:"all,local,remote" default:"all" doc:"Filter by negotiated stream policy location"`
	AdaptationReason string `query:"adaptation_reason" maxLength:"128" doc:"Filter to attempts carrying this canonical adaptation reason code"`
}

// AdminPlaybackHistoryEntryInput identifies one finalized playback attempt.
type AdminPlaybackHistoryEntryInput struct {
	SessionID string `path:"session_id" maxLength:"256" doc:"Playback session identifier"`
}

// AdminPlaybackHistoryEntryOutput wraps one finalized playback attempt.
type AdminPlaybackHistoryEntryOutput struct {
	Body AdminPlaybackHistoryEntry
}

// AdminPlaybackHistoryEntry is one finalized playback attempt as an
// administrator sees it: the account and profile that played, the item and
// file, the recorded timing, the delivered route facts, and the client and
// device that played it.
type AdminPlaybackHistoryEntry struct {
	SessionID          string   `json:"session_id" doc:"Playback session identifier; unique per attempt"`
	UserID             ID       `json:"user_id"`
	Username           string   `json:"username" doc:"Empty when the account no longer exists"`
	ProfileID          string   `json:"profile_id"`
	ProfileName        string   `json:"profile_name" doc:"Profile display name at the time of playback; the profile id when none was recorded"`
	MediaItemID        string   `json:"media_item_id" doc:"Catalog content id; empty when the attempt was not attributed to an item"`
	MediaFileID        ID       `json:"media_file_id"`
	MediaTitle         string   `json:"media_title" doc:"Episode or item title; empty when the item is no longer in the catalog"`
	MediaType          string   `json:"media_type" doc:"episode, or the catalog item type; empty when unknown"`
	PlayMethod         string   `json:"play_method"`
	StartedAt          Instant  `json:"started_at"`
	EndedAt            Instant  `json:"ended_at"`
	EndPositionSeconds float64  `json:"end_position_seconds" doc:"Position the play ended at, in seconds: how far into the media it got, not how long it was watched. A play resumed partway through ends past its resume point; played_seconds is the watch time"`
	DurationSeconds    *float64 `json:"duration_seconds" nullable:"true" doc:"Media duration when known"`
	Completed          bool     `json:"completed"`
	PlayedSeconds      *float64 `json:"played_seconds" nullable:"true" doc:"Seconds of the media watched by normal playback, excluding pauses, seeks and buffering; null when the play has no watch-time measurement, which includes every play recorded before it existed"`
	SeriesTitle        string   `json:"series_title" doc:"Series title for an episode; empty otherwise"`
	SeasonNumber       *int     `json:"season_number" nullable:"true" doc:"Episode season; null when not an episode"`
	EpisodeNumber      *int     `json:"episode_number" nullable:"true" doc:"Episode number; null when not an episode"`

	Measured             bool                                    `json:"measured" doc:"Whether the play has a watch-time measurement; played_seconds, start_position_seconds, paused_seconds, measured_since and measured_until are null without one"`
	StartPositionSeconds *float64                                `json:"start_position_seconds" nullable:"true" doc:"First position, in seconds, the measurement saw: where the play began or resumed"`
	PausedSeconds        *float64                                `json:"paused_seconds" nullable:"true" doc:"Seconds the play spent paused between samples"`
	MeasuredSince        *Instant                                `json:"measured_since" nullable:"true" doc:"First sample behind the measurement: the start, once its stream was prepared, or the first progress report"`
	MeasuredUntil        *Instant                                `json:"measured_until" nullable:"true" doc:"Last sample behind the measurement: the latest progress report, or the final position the stop carried"`
	ClientIP             *string                                 `json:"client_ip" nullable:"true" doc:"Viewer's last address on the playback session; null when none was recorded. Rows recorded by older servers may hold the address of the request that ended the attempt"`
	DeviceID             *string                                 `json:"device_id" nullable:"true" doc:"Device the client declared in X-Silo-Device-Id when playback started (the profile device registry's identity); a Jellyfin play records the DeviceId of its MediaBrowser authorization. Null when unknown"`
	DevicePlatform       *string                                 `json:"device_platform" nullable:"true" doc:"Platform the client declared in X-Silo-Device-Platform when playback started, never derived from the user agent. Null when unknown, and for a Jellyfin play"`
	ClientName           *string                                 `json:"client_name" nullable:"true" doc:"App name the client reported; null when unknown"`
	ClientVersion        *string                                 `json:"client_version" nullable:"true" doc:"App version the client reported; null when unknown"`
	VideoDecision        *string                                 `json:"video_decision" nullable:"true" enum:"direct,remux,transcode" doc:"How the video stream was delivered when the play ended: direct, remux (copied into a new container), or transcode. Null when unknown"`
	AudioDecision        *string                                 `json:"audio_decision" nullable:"true" enum:"direct,remux,transcode" doc:"How the audio stream was delivered when the play ended, with the video_decision values. Null when unknown"`
	EndReason            *string                                 `json:"end_reason" nullable:"true" enum:"stopped,terminated,expired,aborted,replaced" doc:"Why the attempt ended: stopped by the viewer's client, terminated by an administrator, expired after the client went quiet, aborted by the server, or replaced by a new session for the same play (a Jellyfin client switched play method or version). Null when unknown, which includes every attempt recorded before it existed and a server teardown the client never recovered from"`
	Subtitle             NullableObject[PlaybackSessionSubtitle] `json:"subtitle" doc:"The subtitle the play's plan delivered when it ended, in the live session row's shape; null when it showed none, for a Jellyfin-protocol or Audiobookshelf play, and on rows recorded before the server kept it"`
	LifecycleSeq         *int64                                  `json:"lifecycle_seq" nullable:"true" doc:"Sequence number of the end this row records on the sessions channel: higher than every earlier number for the session, so the row supersedes any live row or session.* event with a lower one. Null on rows recorded before lifecycle numbers existed"`

	RouteFactsRecorded       bool     `json:"route_facts_recorded" doc:"True when delivered route facts were captured at finalization; false on pre-upgrade rows"`
	TargetVideoCodec         *string  `json:"target_video_codec" nullable:"true" doc:"Delivered output video codec when transcoded; null otherwise or on pre-upgrade rows"`
	TargetResolution         *string  `json:"target_resolution" nullable:"true" doc:"Delivered output resolution when transcoded; null otherwise or on pre-upgrade rows"`
	TargetAudioCodec         *string  `json:"target_audio_codec" nullable:"true" doc:"Delivered output audio codec when audio was transcoded; null otherwise or on pre-upgrade rows"`
	TargetAudioChannels      *int     `json:"target_audio_channels" nullable:"true" doc:"Delivered output audio channel count when known; null otherwise"`
	OutputContainer          *string  `json:"output_container" nullable:"true" doc:"Delivered muxer/container (e.g. fmp4, mpegts); null when unreported"`
	OutputProtocol           *string  `json:"output_protocol" nullable:"true" doc:"Delivered streaming protocol (e.g. hls, http); null when unreported"`
	TargetBitrateKbps        *int     `json:"target_bitrate_kbps" nullable:"true" doc:"Target bitrate cap in kbps when transcoded; null otherwise"`
	TranscodeHWAccel         *string  `json:"transcode_hw_accel" nullable:"true" doc:"Effective hardware acceleration backend (vaapi, qsv, nvenc, videotoolbox, none); null when not transcoded or unknown"`
	ToneMapMode              *string  `json:"tone_map_mode" nullable:"true" doc:"Effective HDR-to-SDR tone-mapping executor (hardware, software); null when not tone-mapped"`
	AdaptationReasons        []string `json:"adaptation_reasons" doc:"Canonical reasons why playback adapted away from direct play; empty for direct play or pre-upgrade rows"`
	ReportingNode            *string  `json:"reporting_node" nullable:"true" doc:"API node identity that finalized or synced the session; null on pre-upgrade rows"`
	StreamLocation           *string  `json:"stream_location" nullable:"true" enum:"local,remote" doc:"Negotiated bitrate-policy network classification (local or remote); null when unknown"`
	CompatOrigin             *bool    `json:"compat_origin" nullable:"true" doc:"True when the play originated on the Jellyfin compatibility surface; null on pre-upgrade rows"`
	IsJellyfinClient         bool     `json:"is_jellyfin_client" doc:"True when compat_origin is true or client_name identifies a Jellyfin-ecosystem client"`
	RoutingExecution         *string  `json:"routing_execution" nullable:"true" doc:"Committed execution placement (e.g. local, remote_transcode); null when unreported"`
	RoutingExecutionNodeID   *ID      `json:"routing_execution_node_id" nullable:"true" doc:"Stream node ID that executed transcoding when offloaded; null otherwise"`
	RoutingExecutionNodeName *string  `json:"routing_execution_node_name" nullable:"true" doc:"Display name of the execution stream node when still registered; null otherwise"`
	RoutingEgress            *string  `json:"routing_egress" nullable:"true" doc:"Committed egress placement (e.g. api, proxy, transcode); null when unreported"`
	RoutingEgressNodeID      *ID      `json:"routing_egress_node_id" nullable:"true" doc:"Stream node ID that served media bytes when offloaded; null otherwise"`
	RoutingEgressNodeName    *string  `json:"routing_egress_node_name" nullable:"true" doc:"Display name of the egress stream node when still registered; null otherwise"`

	SourceVideoCodec     *string `json:"source_video_codec" nullable:"true" doc:"Source media file video codec from catalog probe; null if file was removed or unprobed"`
	SourceResolution     *string `json:"source_resolution" nullable:"true" doc:"Source media file resolution label from catalog probe; null if unavailable"`
	SourceAudioCodec     *string `json:"source_audio_codec" nullable:"true" doc:"Source media file primary audio codec from catalog probe; null if unavailable"`
	SourceAudioChannels  *int    `json:"source_audio_channels" nullable:"true" doc:"Source media file primary audio channel count; null if unavailable"`
	SourceContainer      *string `json:"source_container" nullable:"true" doc:"Source media file container format; null if unavailable"`
	SourceBitrateKbps    *int    `json:"source_bitrate_kbps" nullable:"true" doc:"Source media file overall bitrate in kbps; null if unavailable"`
	SourceHDR            *bool   `json:"source_hdr" nullable:"true" doc:"Whether the source media file is HDR; null if unavailable"`
	SourceVideoRangeType *string `json:"source_video_range_type" nullable:"true" doc:"Detailed source video range (e.g. HDR10, DOVI, HLG, SDR); null if unavailable"`
	LibraryID            *ID     `json:"library_id" nullable:"true" doc:"Library ID owning the media file; null if file was removed"`
	LibraryName          *string `json:"library_name" nullable:"true" doc:"Library display name owning the media file; null if file was removed"`
}

// AdminPlaybackHistoryCollection is the named envelope the contract carries.
type AdminPlaybackHistoryCollection struct {
	Collection[AdminPlaybackHistoryEntry]
}
type AdminPlaybackHistoryCollectionOutput struct {
	Body AdminPlaybackHistoryCollection
}

// AdminPlaybackHistoryCapabilitiesOutput is getAdminPlaybackHistoryCapabilities.
type AdminPlaybackHistoryCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminPlaybackHistoryCapabilities
}

// AdminPlaybackHistoryCapabilities names the additions to the playback
// history list a client can rely on.
type AdminPlaybackHistoryCapabilities struct {
	Capability
	Available        bool     `json:"available"`
	EndedBefore      bool     `json:"ended_before" doc:"The list accepts ended_before"`
	WatchMeasurement bool     `json:"watch_measurement" doc:"Entries carry measured, start_position_seconds, paused_seconds, measured_since and measured_until"`
	ClientIP         bool     `json:"client_ip" doc:"Entries carry client_ip"`
	DeviceIdentity   bool     `json:"device_identity" doc:"Entries carry device_id and device_platform"`
	ClientIdentity   bool     `json:"client_identity" doc:"Entries carry client_name and client_version"`
	StreamDecisions  bool     `json:"stream_decisions" doc:"Entries carry video_decision and audio_decision"`
	EndReason        bool     `json:"end_reason" doc:"Entries carry end_reason"`
	EndReasonValues  []string `json:"end_reason_values" doc:"Values end_reason may take"`
	LifecycleSeq     bool     `json:"lifecycle_seq" doc:"Entries carry lifecycle_seq"`
	RouteFacts       bool     `json:"route_facts" doc:"Entries carry delivered route facts, adaptation reasons, and joined source file specs"`
	RichFilters      bool     `json:"rich_filters" doc:"The list accepts search, play_method, device_platform, client_name, source_video_codec, subtitle_burn_in, stream_location, adaptation_reason, library_id, started_after and started_before"`
	SessionDetail    bool     `json:"session_detail" doc:"GET /api/v2/admin/playback-history/{session_id} reads one finalized session entry"`
	SeriesFilter     bool     `json:"series_filter" doc:"The list accepts series_id"`
}

const opListAdminPlaybackHistory = "listAdminPlaybackHistory"
const opGetAdminPlaybackHistoryEntry = "getAdminPlaybackHistoryEntry"

// adminPlaybackHistoryCompletedAll is the completed filter value that lists every attempt.
const adminPlaybackHistoryCompletedAll = "all"

const adminPlaybackHistorySort = "ended_at:desc"
const adminPlaybackHistoryTiebreaker = "session_id:desc"

func registerAdminPlaybackHistory(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	capabilities := Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/playback-history/capabilities", "getAdminPlaybackHistoryCapabilities", "admin", "Read which playback history filters and entry fields this server supports."), Class: ClassActingAdmin}
	Register(reg, capabilities, func(_ context.Context, _ *CapabilityInput) (*AdminPlaybackHistoryCapabilitiesOutput, error) {
		out := new(AdminPlaybackHistoryCapabilitiesOutput)
		available := reg.deps.AdminPlaybackHistory != nil
		out.Body.State = configuredCapabilityState(available)
		out.Body.Available = available
		out.Body.EndedBefore = available
		out.Body.WatchMeasurement = available
		out.Body.ClientIP = available
		out.Body.DeviceIdentity = available
		out.Body.ClientIdentity = available
		out.Body.StreamDecisions = available
		out.Body.EndReason = available
		out.Body.LifecycleSeq = available
		out.Body.RouteFacts = available
		out.Body.RichFilters = available
		out.Body.SessionDetail = available
		out.Body.SeriesFilter = available
		out.Body.EndReasonValues = make([]string, len(handlers.PlaybackEndReasons))
		for i, reason := range handlers.PlaybackEndReasons {
			out.Body.EndReasonValues[i] = string(reason)
		}
		return out, nil
	})
	detailOp := Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/playback-history/{session_id}", opGetAdminPlaybackHistoryEntry, "admin", "Read one finalized playback attempt with its delivered route facts and source file specs."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, detailOp, func(ctx context.Context, in *AdminPlaybackHistoryEntryInput) (*AdminPlaybackHistoryEntryOutput, error) {
		if reg.deps.AdminPlaybackHistory == nil {
			return nil, unavailable("administration")
		}
		sessionID := strings.TrimSpace(in.SessionID)
		if sessionID == "" {
			return nil, NewProblem(TypeValidationFailed, "Session ID is required.")
		}
		row, err := reg.deps.AdminPlaybackHistory.ReadAdminPlaybackHistoryEntry(ctx, sessionID)
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Unable to read playback history entry.")
		}
		if row == nil {
			return nil, NewProblem(TypeNotFound, "Playback history entry not found.")
		}
		return &AdminPlaybackHistoryEntryOutput{Body: adminPlaybackHistoryEntryOf(*row)}, nil
	})
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/playback-history", opListAdminPlaybackHistory, "admin", "List finalized playback attempts across every account and profile, newest ended first. Each page is one consistent read; later pages read the live log."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminPlaybackHistoryListInput) (*AdminPlaybackHistoryCollectionOutput, error) {
		if reg.deps.AdminPlaybackHistory == nil {
			return nil, unavailable("administration")
		}
		filter := handlers.AdminPlaybackHistoryFilter{
			ProfileID:        strings.TrimSpace(in.ProfileID),
			MediaItemID:      strings.TrimSpace(in.MediaItemID),
			SeriesID:         strings.TrimSpace(in.SeriesID),
			Search:           strings.TrimSpace(in.Search),
			DevicePlatform:   strings.TrimSpace(in.DevicePlatform),
			ClientName:       strings.TrimSpace(in.ClientName),
			SourceVideoCodec: strings.TrimSpace(in.SourceVideoCodec),
			AdaptationReason: strings.TrimSpace(in.AdaptationReason),
		}
		if in.UserID != "" {
			n, err := intOfID(in.UserID)
			if err != nil || n <= 0 {
				return nil, NewProblem(TypeValidationFailed, "Invalid playback history account filter.")
			}
			filter.UserID = n
		}
		if in.LibraryID != "" {
			n, err := intOfID(in.LibraryID)
			if err != nil || n <= 0 {
				return nil, NewProblem(TypeValidationFailed, "Invalid playback history library filter.")
			}
			filter.LibraryID = n
		}
		if in.Completed != "" && in.Completed != adminPlaybackHistoryCompletedAll {
			// Huma already restricted the value to the enum.
			filter.Completed = new(in.Completed == strconv.FormatBool(true))
		}
		if in.PlayMethod != "" && in.PlayMethod != adminPlaybackHistoryCompletedAll {
			filter.PlayMethod = in.PlayMethod
		}
		if in.SubtitleBurnIn != "" && in.SubtitleBurnIn != adminPlaybackHistoryCompletedAll {
			filter.SubtitleBurnIn = new(in.SubtitleBurnIn == strconv.FormatBool(true))
		}
		if in.StreamLocation != "" && in.StreamLocation != adminPlaybackHistoryCompletedAll {
			filter.StreamLocation = in.StreamLocation
		}
		var p *Problem
		if filter.EndedAfter, p = adminPlaybackHistoryInstant(in.EndedAfter, "ended_after"); p != nil {
			return nil, p
		}
		if filter.EndedBefore, p = adminPlaybackHistoryInstant(in.EndedBefore, "ended_before"); p != nil {
			return nil, p
		}
		if filter.StartedAfter, p = adminPlaybackHistoryInstant(in.StartedAfter, "started_after"); p != nil {
			return nil, p
		}
		if filter.StartedBefore, p = adminPlaybackHistoryInstant(in.StartedBefore, "started_before"); p != nil {
			return nil, p
		}
		filterJSON, _ := json.Marshal(struct {
			Filter handlers.AdminPlaybackHistoryFilter
			Limit  int
		}{filter, in.Limit})
		scope := CursorScope{OperationID: opListAdminPlaybackHistory, Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: string(filterJSON), Sort: adminPlaybackHistorySort, Tiebreaker: adminPlaybackHistoryTiebreaker}
		var after *handlers.AdminPlaybackHistoryPageKey
		if in.Cursor != "" {
			after = new(handlers.AdminPlaybackHistoryPageKey)
			if p := cursors.Decode(scope, in.Cursor, after); p != nil {
				return nil, p
			}
			if after.SessionID == "" || after.EndedAt.IsZero() {
				return nil, NewProblem(TypeInvalidCursor, "Invalid playback history cursor.")
			}
		}
		result, err := reg.deps.AdminPlaybackHistory.ListAdminPlaybackHistoryPage(ctx, filter, after, in.Limit)
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Unable to list playback history.")
		}
		items := make([]AdminPlaybackHistoryEntry, 0, len(result.Items))
		for _, row := range result.Items {
			items = append(items, adminPlaybackHistoryEntryOf(row))
		}
		next := ""
		if result.HasMore {
			if len(result.Items) == 0 {
				return nil, NewProblem(TypeInternalError, "Unable to page playback history.")
			}
			last := result.Items[len(result.Items)-1]
			next, err = cursors.Encode(scope, handlers.AdminPlaybackHistoryPageKey{EndedAt: last.EndedAt, SessionID: last.SessionID})
			if err != nil {
				return nil, NewProblem(TypeInternalError, "Unable to encode playback history cursor.")
			}
		}
		return &AdminPlaybackHistoryCollectionOutput{Body: AdminPlaybackHistoryCollection{Collection: Paginated(items, next)}}, nil
	})
}

// adminPlaybackHistoryInstant parses an optional RFC 3339 window bound; blank
// is no bound.
func adminPlaybackHistoryInstant(raw, name string) (*time.Time, *Problem) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, nil
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, NewProblem(TypeValidationFailed, "Invalid "+name+".")
	}
	return new(at.UTC()), nil
}

func nullableIDFromIntPtr(v *int) *ID {
	if v == nil || *v <= 0 {
		return nil
	}
	return new(IDFromInt(int64(*v)))
}

func adminPlaybackHistoryEntryOf(row handlers.AdminPlaybackHistoryRow) AdminPlaybackHistoryEntry {
	reasons := row.AdaptationReasons
	if reasons == nil {
		reasons = []string{}
	}
	return AdminPlaybackHistoryEntry{
		SessionID: row.SessionID, UserID: IDFromInt(int64(row.UserID)), Username: row.Username,
		ProfileID: row.ProfileID, ProfileName: row.ProfileName, MediaItemID: row.MediaItemID,
		MediaFileID: IDFromInt(int64(row.MediaFileID)), MediaTitle: row.MediaTitle, MediaType: row.MediaType,
		PlayMethod: row.PlayMethod, StartedAt: NewInstant(row.StartedAt), EndedAt: NewInstant(row.EndedAt),
		EndPositionSeconds: row.EndPositionSeconds, DurationSeconds: row.DurationSeconds, Completed: row.Completed, PlayedSeconds: row.PlayedSeconds,
		SeriesTitle: row.SeriesTitle, SeasonNumber: row.SeasonNumber, EpisodeNumber: row.EpisodeNumber,
		Measured: row.Measured, StartPositionSeconds: row.StartPositionSeconds, PausedSeconds: row.PausedSeconds,
		MeasuredSince: instantPtr(row.MeasuredSince), MeasuredUntil: instantPtr(row.MeasuredUntil),
		ClientIP: row.ClientIP, DeviceID: row.DeviceID, DevicePlatform: row.DevicePlatform,
		ClientName: row.ClientName, ClientVersion: row.ClientVersion,
		VideoDecision: row.VideoDecision, AudioDecision: row.AudioDecision, EndReason: row.EndReason,
		LifecycleSeq: row.LifecycleSeq, Subtitle: playbackSessionSubtitleOf(row.Subtitle),
		RouteFactsRecorded: row.RouteFactsRecorded, TargetVideoCodec: row.TargetVideoCodec,
		TargetResolution: row.TargetResolution, TargetAudioCodec: row.TargetAudioCodec,
		TargetAudioChannels: row.TargetAudioChannels, OutputContainer: row.OutputContainer,
		OutputProtocol: row.OutputProtocol, TargetBitrateKbps: row.TargetBitrateKbps,
		TranscodeHWAccel: row.TranscodeHWAccel, ToneMapMode: row.ToneMapMode,
		AdaptationReasons: reasons, ReportingNode: row.ReportingNode,
		StreamLocation: row.StreamLocation, CompatOrigin: row.CompatOrigin,
		IsJellyfinClient: row.IsJellyfinClient, RoutingExecution: row.RoutingExecution,
		RoutingExecutionNodeID: nullableIDFromIntPtr(row.RoutingExecutionNodeID), RoutingExecutionNodeName: row.RoutingExecutionNodeName,
		RoutingEgress: row.RoutingEgress, RoutingEgressNodeID: nullableIDFromIntPtr(row.RoutingEgressNodeID),
		RoutingEgressNodeName: row.RoutingEgressNodeName,
		SourceVideoCodec:      row.SourceVideoCodec, SourceResolution: row.SourceResolution,
		SourceAudioCodec: row.SourceAudioCodec, SourceAudioChannels: row.SourceAudioChannels,
		SourceContainer: row.SourceContainer, SourceBitrateKbps: row.SourceBitrateKbps,
		SourceHDR: row.SourceHDR, SourceVideoRangeType: row.SourceVideoRangeType,
		LibraryID: nullableIDFromIntPtr(row.LibraryID), LibraryName: row.LibraryName,
	}
}

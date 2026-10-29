package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// AdminPlaybackHistoryService is the slice of *handlers.AdminHandler
// listAdminPlaybackHistory uses.
type AdminPlaybackHistoryService interface {
	ListAdminPlaybackHistoryPage(context.Context, handlers.AdminPlaybackHistoryFilter, *handlers.AdminPlaybackHistoryPageKey, int) (handlers.AdminPlaybackHistoryPage, error)
}

// AdminPlaybackHistoryListInput is the listAdminPlaybackHistory query: the v1
// filters plus cursor paging in place of offset.
type AdminPlaybackHistoryListInput struct {
	LimitParam
	Cursor      string `query:"cursor" maxLength:"8192" doc:"Opaque cursor from page.next_cursor"`
	UserID      ID     `query:"user_id" doc:"Only attempts by this login account"`
	ProfileID   string `query:"profile_id" maxLength:"1024" doc:"Only attempts by this household profile"`
	MediaItemID string `query:"media_item_id" maxLength:"1024" doc:"Only attempts of this catalog item"`
	Completed   string `query:"completed" enum:"all,true,false" default:"all" doc:"Completion filter; all returns every finalized attempt"`
	EndedAfter  string `query:"ended_after" format:"date-time" doc:"Only attempts that ended at or after this instant (inclusive)"`
	EndedBefore string `query:"ended_before" format:"date-time" doc:"Only attempts that ended strictly before this instant (exclusive), so windows that share a bound never list an attempt twice"`
}

// AdminPlaybackHistoryEntry is one finalized playback attempt as an
// administrator sees it: the account and profile that played, the item and
// file, the recorded timing, and the client and device that played it.
type AdminPlaybackHistoryEntry struct {
	SessionID       string   `json:"session_id" doc:"Playback session identifier; unique per attempt"`
	UserID          ID       `json:"user_id"`
	Username        string   `json:"username" doc:"Empty when the account no longer exists"`
	ProfileID       string   `json:"profile_id"`
	ProfileName     string   `json:"profile_name" doc:"Profile display name at the time of playback; the profile id when none was recorded"`
	MediaItemID     string   `json:"media_item_id" doc:"Catalog content id; empty when the attempt was not attributed to an item"`
	MediaFileID     ID       `json:"media_file_id"`
	MediaTitle      string   `json:"media_title" doc:"Episode or item title; empty when the item is no longer in the catalog"`
	MediaType       string   `json:"media_type" doc:"episode, or the catalog item type; empty when unknown"`
	PlayMethod      string   `json:"play_method"`
	StartedAt       Instant  `json:"started_at"`
	EndedAt         Instant  `json:"ended_at"`
	WatchedSeconds  float64  `json:"watched_seconds" doc:"Position the play ended at, in seconds. Not how long it was watched: a play resumed partway through reports the resume point plus what was watched; played_seconds is the watch time"`
	DurationSeconds *float64 `json:"duration_seconds" nullable:"true" doc:"Media duration when known"`
	Completed       bool     `json:"completed"`
	PlayedSeconds   *float64 `json:"played_seconds" nullable:"true" doc:"Seconds of the media watched by normal playback, excluding pauses, seeks and buffering; null when the play has no watch-time measurement, which includes every play recorded before it existed"`
	SeriesTitle     string   `json:"series_title" doc:"Series title for an episode; empty otherwise"`
	SeasonNumber    *int     `json:"season_number" nullable:"true" doc:"Episode season; null when not an episode"`
	EpisodeNumber   *int     `json:"episode_number" nullable:"true" doc:"Episode number; null when not an episode"`

	Measured             bool     `json:"measured" doc:"Whether the play has a watch-time measurement; played_seconds, start_position_seconds, paused_seconds, measured_since and measured_until are null without one"`
	StartPositionSeconds *float64 `json:"start_position_seconds" nullable:"true" doc:"First position, in seconds, the measurement saw: where the play began or resumed"`
	PausedSeconds        *float64 `json:"paused_seconds" nullable:"true" doc:"Seconds the play spent paused between progress reports"`
	MeasuredSince        *Instant `json:"measured_since" nullable:"true" doc:"First progress report behind the measurement"`
	MeasuredUntil        *Instant `json:"measured_until" nullable:"true" doc:"Last progress report behind the measurement"`
	ClientIP             *string  `json:"client_ip" nullable:"true" doc:"Viewer's last address on the playback session; null when none was recorded. Rows recorded by older servers may hold the address of the request that ended the attempt"`
	DeviceID             *string  `json:"device_id" nullable:"true" doc:"Device the client declared in X-Silo-Device-Id when playback started (the profile device registry's identity); a Jellyfin play records the DeviceId of its MediaBrowser authorization. Null when unknown"`
	DevicePlatform       *string  `json:"device_platform" nullable:"true" doc:"Platform the client declared in X-Silo-Device-Platform when playback started, never derived from the user agent. Null when unknown, and for a Jellyfin play"`
	ClientName           *string  `json:"client_name" nullable:"true" doc:"App name the client reported; null when unknown"`
	ClientVersion        *string  `json:"client_version" nullable:"true" doc:"App version the client reported; null when unknown"`
	VideoDecision        *string  `json:"video_decision" nullable:"true" enum:"direct,remux,transcode" doc:"How the video stream was delivered when the play ended: direct, remux (copied into a new container), or transcode. Null when unknown"`
	AudioDecision        *string  `json:"audio_decision" nullable:"true" enum:"direct,remux,transcode" doc:"How the audio stream was delivered when the play ended, with the video_decision values. Null when unknown"`
	EndReason            *string  `json:"end_reason" nullable:"true" enum:"stopped,terminated,expired,aborted,replaced" doc:"Why the attempt ended: stopped by the viewer's client, terminated by an administrator, expired after the client went quiet, aborted by the server, or replaced by a new session for the same play (a Jellyfin client switched play method). Null when unknown, which includes every attempt recorded before it existed and a server teardown the client never recovered from"`
	LifecycleSeq         *int64   `json:"lifecycle_seq" nullable:"true" doc:"Sequence number of the end this row records on the sessions channel: higher than every earlier number for the session, so the row supersedes any live row or session.* event with a lower one. Null on rows recorded before lifecycle numbers existed"`
}

// TransformSchema admits null in the nullable enums: JSON Schema applies the
// enum and the type separately, so an enum of strings alone refuses the null
// an unknown value takes.
func (AdminPlaybackHistoryEntry) TransformSchema(_ huma.Registry, s *huma.Schema) *huma.Schema {
	for _, name := range []string{"video_decision", "audio_decision", "end_reason"} {
		if property := s.Properties[name]; property != nil && !slices.Contains(property.Enum, nil) {
			property.Enum = append(property.Enum, nil)
		}
	}
	return s
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
}

const opListAdminPlaybackHistory = "listAdminPlaybackHistory"

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
		out.Body.EndReasonValues = make([]string, len(handlers.PlaybackEndReasons))
		for i, reason := range handlers.PlaybackEndReasons {
			out.Body.EndReasonValues[i] = string(reason)
		}
		return out, nil
	})
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/playback-history", opListAdminPlaybackHistory, "admin", "List finalized playback attempts across every account and profile, newest ended first. Each page is one consistent read; later pages read the live log."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminPlaybackHistoryListInput) (*AdminPlaybackHistoryCollectionOutput, error) {
		if reg.deps.AdminPlaybackHistory == nil {
			return nil, unavailable("administration")
		}
		filter := handlers.AdminPlaybackHistoryFilter{ProfileID: strings.TrimSpace(in.ProfileID), MediaItemID: strings.TrimSpace(in.MediaItemID)}
		if in.UserID != "" {
			n, err := intOfID(in.UserID)
			if err != nil || n <= 0 {
				return nil, NewProblem(TypeValidationFailed, "Invalid playback history account filter.")
			}
			filter.UserID = n
		}
		if in.Completed != "" && in.Completed != adminPlaybackHistoryCompletedAll {
			// Huma already restricted the value to the enum.
			filter.Completed = new(in.Completed == strconv.FormatBool(true))
		}
		var p *Problem
		if filter.EndedAfter, p = adminPlaybackHistoryInstant(in.EndedAfter, "ended_after"); p != nil {
			return nil, p
		}
		if filter.EndedBefore, p = adminPlaybackHistoryInstant(in.EndedBefore, "ended_before"); p != nil {
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

func adminPlaybackHistoryEntryOf(row handlers.AdminPlaybackHistoryRow) AdminPlaybackHistoryEntry {
	return AdminPlaybackHistoryEntry{
		SessionID: row.SessionID, UserID: IDFromInt(int64(row.UserID)), Username: row.Username,
		ProfileID: row.ProfileID, ProfileName: row.ProfileName, MediaItemID: row.MediaItemID,
		MediaFileID: IDFromInt(int64(row.MediaFileID)), MediaTitle: row.MediaTitle, MediaType: row.MediaType,
		PlayMethod: row.PlayMethod, StartedAt: NewInstant(row.StartedAt), EndedAt: NewInstant(row.EndedAt),
		WatchedSeconds: row.WatchedSeconds, DurationSeconds: row.DurationSeconds, Completed: row.Completed, PlayedSeconds: row.PlayedSeconds,
		SeriesTitle: row.SeriesTitle, SeasonNumber: row.SeasonNumber, EpisodeNumber: row.EpisodeNumber,
		Measured: row.Measured, StartPositionSeconds: row.StartPositionSeconds, PausedSeconds: row.PausedSeconds,
		MeasuredSince: instantPtr(row.MeasuredSince), MeasuredUntil: instantPtr(row.MeasuredUntil),
		ClientIP: row.ClientIP, DeviceID: row.DeviceID, DevicePlatform: row.DevicePlatform,
		ClientName: row.ClientName, ClientVersion: row.ClientVersion,
		VideoDecision: row.VideoDecision, AudioDecision: row.AudioDecision, EndReason: row.EndReason,
		LifecycleSeq: row.LifecycleSeq,
	}
}

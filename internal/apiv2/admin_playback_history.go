package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

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
}

// AdminPlaybackHistoryEntry is one finalized playback attempt as an
// administrator sees it: the account and profile that played, the item and
// file, and the recorded timing. The stored client address is not projected.
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
	WatchedSeconds  float64  `json:"watched_seconds"`
	DurationSeconds *float64 `json:"duration_seconds" nullable:"true" doc:"Media duration when known"`
	Completed       bool     `json:"completed"`
}

// AdminPlaybackHistoryCollection is the named envelope the contract carries.
type AdminPlaybackHistoryCollection struct {
	Collection[AdminPlaybackHistoryEntry]
}
type AdminPlaybackHistoryCollectionOutput struct {
	Body AdminPlaybackHistoryCollection
}

const opListAdminPlaybackHistory = "listAdminPlaybackHistory"

// adminPlaybackHistoryCompletedAll is the completed filter value that lists every attempt.
const adminPlaybackHistoryCompletedAll = "all"

const adminPlaybackHistorySort = "ended_at:desc"
const adminPlaybackHistoryTiebreaker = "session_id:desc"

func registerAdminPlaybackHistory(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
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
			items = append(items, AdminPlaybackHistoryEntry{
				SessionID: row.SessionID, UserID: IDFromInt(int64(row.UserID)), Username: row.Username,
				ProfileID: row.ProfileID, ProfileName: row.ProfileName, MediaItemID: row.MediaItemID,
				MediaFileID: IDFromInt(int64(row.MediaFileID)), MediaTitle: row.MediaTitle, MediaType: row.MediaType,
				PlayMethod: row.PlayMethod, StartedAt: NewInstant(row.StartedAt), EndedAt: NewInstant(row.EndedAt),
				WatchedSeconds: row.WatchedSeconds, DurationSeconds: row.DurationSeconds, Completed: row.Completed,
			})
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

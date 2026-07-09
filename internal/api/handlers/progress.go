package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// progressClockSkew bounds how far ahead of server time a client-supplied
// progress event time may sit before it is clamped to "now".
const progressClockSkew = 2 * time.Minute

// parseClientEventTime parses an RFC3339 client event time. Malformed values
// are an error the caller must reject: treating them as "now" would let a
// stale offline event win LWW as a fresh server-time write.
func parseClientEventTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// clampEventAt bounds a client event time to at most now+skew: a value past the
// window is clamped to now, so a skewed or malicious clock can at most claim
// "now" for its own profile and never lock in a far-future LWW win (invariant 1).
func clampEventAt(client, now time.Time) time.Time {
	if client.IsZero() {
		return now
	}
	if client.After(now.Add(progressClockSkew)) {
		return now
	}
	return client
}

// ProgressLibraryLookup resolves which progress items belong to a library.
type ProgressLibraryLookup interface {
	GetItemsInFolder(ctx context.Context, contentIDs []string, folderID int) (map[string]bool, error)
	// FilterAccessibleContentIDs returns the subset of contentIDs the viewer
	// may access given their library scope and content-rating ceiling.
	FilterAccessibleContentIDs(ctx context.Context, contentIDs []string, allowedFolderIDs, disabledFolderIDs []int, maxContentRating string) (map[string]bool, error)
}

// ProgressHandler handles watch progress and sync endpoints.
type ProgressHandler struct {
	storeProvider           userstore.UserStoreProvider
	LibraryLookup           ProgressLibraryLookup
	SettingsRepo            PlaybackSettingsReader
	EventsHub               *evt.Hub
	profileStaler           ProfileStaler
	profileRefreshRequester ProfileRefreshRequester
}

// NewProgressHandler creates a new ProgressHandler.
func NewProgressHandler(provider userstore.UserStoreProvider) *ProgressHandler {
	return &ProgressHandler{storeProvider: provider}
}

// SetProfileStaler configures an optional staleness trigger for taste profiles.
func (h *ProgressHandler) SetProfileStaler(ps ProfileStaler) {
	h.profileStaler = ps
}

// SetProfileRefreshRequester configures an optional background refresh queue for taste profiles.
func (h *ProgressHandler) SetProfileRefreshRequester(requester ProfileRefreshRequester) {
	h.profileRefreshRequester = requester
}

// --- Request/Response types ---

type progressEntryResponse struct {
	MediaItemID     string  `json:"media_item_id"`
	PositionSeconds float64 `json:"position_seconds"`
	DurationSeconds float64 `json:"duration_seconds"`
	Completed       bool    `json:"completed"`
	UpdatedAt       string  `json:"updated_at"`
}

type progressListResponse struct {
	Progress []progressEntryResponse `json:"progress"`
	// NextCursor is the opaque server token to resume a ?since= delta from.
	NextCursor string `json:"next_cursor,omitempty"`
}

type syncProgressItem struct {
	MediaItemID    string  `json:"media_item_id"`
	Position       float64 `json:"position"`
	Duration       float64 `json:"duration"`
	ForceOverwrite bool    `json:"force_overwrite"`
	// UpdatedAt is the client EVENT time (RFC3339) for an offline-queued item.
	// The server clamps it to now+skew and uses it only as the LWW key.
	UpdatedAt *string `json:"updated_at,omitempty"`
}

type syncProgressRequest struct {
	Items []syncProgressItem `json:"items"`
}

type syncProgressResultItem struct {
	MediaItemID string `json:"media_item_id"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}

type syncProgressResponse struct {
	Results []syncProgressResultItem `json:"results"`
}

// --- Handler methods ---

// HandleListProgress handles GET /progress?status=in_progress&limit=20&offset=0.
func (h *ProgressHandler) HandleListProgress(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	status := r.URL.Query().Get("status")
	since := r.URL.Query().Get("since")
	limit, offset := parsePagination(r)
	libraryID, err := parseLibraryIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library_id")
		return
	}

	// A ?since= cursor switches to server-ordered delta delivery (rows changed
	// elsewhere since the cursor), immune to client clock skew. Absent since →
	// today's status/pagination listing.
	var entries []userstore.WatchProgress
	var nextCursor string
	if since != "" {
		entries, nextCursor, err = store.ListProgressSince(r.Context(), profileID, since)
	} else {
		entries, err = store.ListProgress(r.Context(), profileID, status, limit, offset)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list progress")
		return
	}

	// Drop entries the viewer can't access before they reach the client.
	// Without this, a library-restricted profile receives progress rows for
	// items outside its scope (e.g. an XXX title) and the client then fans out
	// per-item detail fetches that 404 — a dead Continue Watching tile. Only
	// runs for restricted profiles; unrestricted viewers are unaffected.
	if scope, ok := access.GetScope(r.Context()); ok &&
		(scope.AllowedLibraryIDs != nil || len(scope.DisabledLibraryIDs) > 0 || scope.MaxContentRating != "") {
		if h.LibraryLookup == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to apply access filter")
			return
		}
		entries, err = filterProgressEntriesByAccess(r.Context(), entries, scope, h.LibraryLookup)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to apply access filter")
			return
		}
	}

	if libraryID > 0 {
		if h.LibraryLookup == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to apply library filter")
			return
		}
		entries, err = filterProgressEntriesByLibrary(r.Context(), entries, libraryID, h.LibraryLookup)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to apply library filter")
			return
		}
	}

	resp := progressListResponse{
		Progress:   make([]progressEntryResponse, 0, len(entries)),
		NextCursor: nextCursor,
	}
	for _, e := range entries {
		resp.Progress = append(resp.Progress, progressEntryResponse{
			MediaItemID:     e.MediaItemID,
			PositionSeconds: e.PositionSeconds,
			DurationSeconds: e.DurationSeconds,
			Completed:       e.Completed,
			UpdatedAt:       e.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

func parseLibraryIDParam(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("library_id")
	if raw == "" {
		return 0, nil
	}

	libraryID, err := strconv.Atoi(raw)
	if err != nil || libraryID <= 0 {
		return 0, strconv.ErrSyntax
	}

	return libraryID, nil
}

// progressContentIDs collects the media item IDs from a progress slice.
func progressContentIDs(entries []userstore.WatchProgress) []string {
	contentIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		contentIDs = append(contentIDs, entry.MediaItemID)
	}
	return contentIDs
}

// keepAccessibleEntries returns, in order, the entries whose media item ID maps
// to true in accessible.
func keepAccessibleEntries(entries []userstore.WatchProgress, accessible map[string]bool) []userstore.WatchProgress {
	filtered := make([]userstore.WatchProgress, 0, len(entries))
	for _, entry := range entries {
		if accessible[entry.MediaItemID] {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func filterProgressEntriesByLibrary(
	ctx context.Context,
	entries []userstore.WatchProgress,
	libraryID int,
	lookup ProgressLibraryLookup,
) ([]userstore.WatchProgress, error) {
	if len(entries) == 0 {
		return entries, nil
	}

	allowed, err := lookup.GetItemsInFolder(ctx, progressContentIDs(entries), libraryID)
	if err != nil {
		return nil, err
	}

	return keepAccessibleEntries(entries, allowed), nil
}

// filterProgressEntriesByAccess removes progress entries whose item falls
// outside the viewer's access scope (allowed/disabled libraries and the
// content-rating ceiling).
func filterProgressEntriesByAccess(
	ctx context.Context,
	entries []userstore.WatchProgress,
	scope access.Scope,
	lookup ProgressLibraryLookup,
) ([]userstore.WatchProgress, error) {
	if len(entries) == 0 {
		return entries, nil
	}

	accessible, err := lookup.FilterAccessibleContentIDs(ctx, progressContentIDs(entries), scope.AllowedLibraryIDs, scope.DisabledLibraryIDs, scope.MaxContentRating)
	if err != nil {
		return nil, err
	}

	return keepAccessibleEntries(entries, accessible), nil
}

// HandleSyncProgress handles POST /sync/progress.
// It accepts a batch of progress updates and returns per-item results.
func (h *ProgressHandler) HandleSyncProgress(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	var req syncProgressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if len(req.Items) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "At least one progress item is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	var thresholds userstore.ProgressThresholds
	if h.SettingsRepo != nil {
		if v, _ := h.SettingsRepo.Get(r.Context(), "playback.watched_threshold"); v != "" {
			if pct, err := strconv.Atoi(v); err == nil && pct > 0 {
				thresholds.WatchedPct = pct
			}
		}
		if v, _ := h.SettingsRepo.Get(r.Context(), "playback.min_resume_threshold"); v != "" {
			if pct, err := strconv.Atoi(v); err == nil && pct > 0 {
				thresholds.MinResumePct = pct
			}
		}
	}

	results := make([]syncProgressResultItem, 0, len(req.Items))
	hadSuccessfulUpdate := false

	for _, item := range req.Items {
		result := syncProgressResultItem{
			MediaItemID: item.MediaItemID,
		}

		if item.MediaItemID == "" {
			result.Status = "error"
			result.Error = "media_item_id is required"
			results = append(results, result)
			continue
		}

		var updateErr error
		switch {
		case item.UpdatedAt != nil:
			// Offline-queued event: clamp the client event time and merge
			// last-write-wins on the bounded event_at. synced_seq (the cursor) is
			// stamped server-side; completion still comes from the threshold logic,
			// never the timestamp alone.
			client, parseErr := parseClientEventTime(*item.UpdatedAt)
			if parseErr != nil {
				result.Status = "error"
				result.Error = "updated_at must be RFC3339"
				results = append(results, result)
				continue
			}
			now := time.Now()
			eventAt := clampEventAt(client, now)
			if !client.IsZero() && client.After(now.Add(progressClockSkew)) {
				slog.WarnContext(r.Context(), "clamped future-dated progress event time", "component", "api",
					"profile_id", profileID, "media_item_id", item.MediaItemID)
			}
			pos, completed, skip := userstore.ResolveProgressState(item.Position, item.Duration, thresholds)
			if !skip {
				_, updateErr = store.SetProgressIfNewer(r.Context(), profileID, item.MediaItemID, pos, item.Duration, completed, eventAt)
			}
		case item.ForceOverwrite:
			updateErr = store.SetProgress(r.Context(), profileID, item.MediaItemID, item.Position, item.Duration, thresholds)
		default:
			updateErr = store.UpdateProgress(r.Context(), profileID, item.MediaItemID, item.Position, item.Duration, thresholds)
		}

		if updateErr != nil {
			result.Status = "error"
			result.Error = "failed to update progress"
		} else {
			result.Status = "ok"
			hadSuccessfulUpdate = true
		}

		results = append(results, result)
	}

	if hadSuccessfulUpdate {
		triggerProfileRefresh(r.Context(), h.profileStaler, h.profileRefreshRequester, userID, profileID)
		for _, item := range req.Items {
			if item.MediaItemID == "" {
				continue
			}
			publishUserStateEvent(
				r.Context(),
				h.EventsHub,
				userID,
				profileID,
				item.MediaItemID,
				"",
				"progress",
				userStateEventState{},
			)
		}
	}

	writeJSON(w, http.StatusOK, syncProgressResponse{Results: results})
}

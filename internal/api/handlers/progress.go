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
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list progress")
			return
		}
		entries, err = h.filterProgress(r.Context(), entries, libraryID)
	} else {
		entries, err = h.listProgressFrom(r.Context(), store, profileID, ProgressQuery{Status: status, LibraryID: libraryID, Limit: limit, Offset: offset})
	}
	if err != nil {
		writeAPIError(w, err)
		return
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

// ProgressQuery is the status listing's filter and window.
type ProgressQuery struct {
	Status    string
	LibraryID int
	Limit     int
	Offset    int
}

// ListProgress is the status/pagination listing v1 GET /progress (without
// ?since=) and v2 listProgress both serve: the store window, then the viewer
// access and library filters. A failure is an *APIError.
func (h *ProgressHandler) ListProgress(ctx context.Context, userID int, profileID string, q ProgressQuery) ([]userstore.WatchProgress, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	return h.listProgressFrom(ctx, store, profileID, q)
}

// ListProgressPage is the keyset listing v2 listProgress serves: up to limit
// rows the viewer may see, in the requested library, ordered by
// (updated_at DESC, media_item_id DESC) and strictly after the key; the bool
// reports whether at least one more matching row follows. A failure is an
// *APIError.
//
// The store cannot apply the access and library filters, so a store batch of
// limit+1 rows can shrink after filterProgress. The loop keeps fetching the
// next batch (after the last RAW row, filtered or not) until it holds limit+1
// matches or the store runs dry, so has_more is decided on matching rows, not
// on the raw batch. The caller's next cursor is the key of the last EMITTED
// row; because the store resumes strictly after that key, rows filtered out
// between two emitted rows are simply re-read and re-dropped on the next
// page, which costs a little work and never skips or repeats a match. The
// loop is bounded by the profile's own progress rows (its personal data), a
// set that stays small relative to the catalog.
func (h *ProgressHandler) ListProgressPage(ctx context.Context, userID int, profileID string, status string, libraryID int, after *userstore.ProgressKey, limit int) ([]userstore.WatchProgress, bool, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	want := limit + 1
	matches := make([]userstore.WatchProgress, 0, want)
	for len(matches) < want {
		batch, err := store.ListProgressPage(ctx, profileID, status, after, want)
		if err != nil {
			return nil, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to list progress")
		}
		kept, err := h.filterProgress(ctx, batch, libraryID)
		if err != nil {
			return nil, false, err
		}
		matches = append(matches, kept...)
		if len(batch) < want {
			break
		}
		last := batch[len(batch)-1]
		after = &userstore.ProgressKey{UpdatedAt: last.UpdatedAt, MediaItemID: last.MediaItemID}
	}
	if len(matches) > limit {
		return matches[:limit], true, nil
	}
	return matches, false, nil
}

func (h *ProgressHandler) listProgressFrom(ctx context.Context, store userstore.UserStore, profileID string, q ProgressQuery) ([]userstore.WatchProgress, error) {
	entries, err := store.ListProgress(ctx, profileID, q.Status, q.Limit, q.Offset)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list progress")
	}
	return h.filterProgress(ctx, entries, q.LibraryID)
}

// filterProgress drops the rows the viewer may not see, then the rows outside
// the requested library.
func (h *ProgressHandler) filterProgress(ctx context.Context, entries []userstore.WatchProgress, libraryID int) ([]userstore.WatchProgress, error) {
	var err error
	// Drop entries the viewer can't access before they reach the client.
	// Without this, a library-restricted profile receives progress rows for
	// items outside its scope (e.g. an XXX title) and the client then fans out
	// per-item detail fetches that 404 — a dead Continue Watching tile. Only
	// runs for restricted profiles; unrestricted viewers are unaffected.
	if scope, ok := access.GetScope(ctx); ok &&
		(scope.AllowedLibraryIDs != nil || len(scope.DisabledLibraryIDs) > 0 || scope.MaxContentRating != "") {
		if h.LibraryLookup == nil {
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to apply access filter")
		}
		entries, err = filterProgressEntriesByAccess(ctx, entries, scope, h.LibraryLookup)
		if err != nil {
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to apply access filter")
		}
	}

	if libraryID > 0 {
		if h.LibraryLookup == nil {
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to apply library filter")
		}
		entries, err = filterProgressEntriesByLibrary(ctx, entries, libraryID, h.LibraryLookup)
		if err != nil {
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to apply library filter")
		}
	}
	return entries, nil
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

	// The wire string is parsed here so a malformed one keeps its v1 per-item
	// answer; the seam takes the parsed event time.
	updates := make([]ProgressSyncUpdate, 0, len(req.Items))
	for _, item := range req.Items {
		update := ProgressSyncUpdate{MediaItemID: item.MediaItemID, Position: item.Position, Duration: item.Duration, ForceOverwrite: item.ForceOverwrite}
		if item.UpdatedAt != nil {
			client, parseErr := parseClientEventTime(*item.UpdatedAt)
			if parseErr != nil {
				update.invalidUpdatedAt = true
			} else {
				update.UpdatedAt = &client
			}
		}
		updates = append(updates, update)
	}

	results, err := h.SyncProgress(r.Context(), userID, profileID, updates)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, syncProgressResponse{Results: results})
}

// ProgressSyncUpdate is one progress write of a sync batch.
type ProgressSyncUpdate struct {
	MediaItemID    string
	Position       float64
	Duration       float64
	ForceOverwrite bool
	// UpdatedAt is the client event time of an offline-queued item; nil
	// for a live write.
	UpdatedAt *time.Time
	// invalidUpdatedAt marks a v1 wire value that did not parse; the item is
	// answered as an error rather than written.
	invalidUpdatedAt bool
}

// ProgressSyncResultView is the per-item answer of a sync batch.
type ProgressSyncResultView = syncProgressResultItem

// SyncProgress applies a batch of progress writes for the profile and
// answers one result per item, in order. A failed write is an error result,
// never a failed batch; only a store that cannot be opened fails the whole
// call. The v1 handler and the v2 operation share it.
func (h *ProgressHandler) SyncProgress(ctx context.Context, userID int, profileID string, updates []ProgressSyncUpdate) ([]ProgressSyncResultView, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}

	var thresholds userstore.ProgressThresholds
	if h.SettingsRepo != nil {
		if v, _ := h.SettingsRepo.Get(ctx, "playback.watched_threshold"); v != "" {
			if pct, err := strconv.Atoi(v); err == nil && pct > 0 {
				thresholds.WatchedPct = pct
			}
		}
		if v, _ := h.SettingsRepo.Get(ctx, "playback.min_resume_threshold"); v != "" {
			if pct, err := strconv.Atoi(v); err == nil && pct > 0 {
				thresholds.MinResumePct = pct
			}
		}
	}

	results := make([]syncProgressResultItem, 0, len(updates))
	hadSuccessfulUpdate := false

	for _, item := range updates {
		result := syncProgressResultItem{
			MediaItemID: item.MediaItemID,
		}

		if item.MediaItemID == "" {
			result.Status = "error"
			result.Error = "media_item_id is required"
			results = append(results, result)
			continue
		}
		if item.invalidUpdatedAt {
			result.Status = "error"
			result.Error = "updated_at must be RFC3339"
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
			client := item.UpdatedAt.UTC()
			now := time.Now()
			eventAt := clampEventAt(client, now)
			if !client.IsZero() && client.After(now.Add(progressClockSkew)) {
				slog.WarnContext(ctx, "clamped future-dated progress event time", "component", "api",
					"profile_id", profileID, "media_item_id", item.MediaItemID)
			}
			pos, completed, skip := userstore.ResolveProgressState(item.Position, item.Duration, thresholds)
			if !skip {
				_, updateErr = store.SetProgressIfNewer(ctx, profileID, item.MediaItemID, pos, item.Duration, completed, eventAt)
			}
		case item.ForceOverwrite:
			updateErr = store.SetProgress(ctx, profileID, item.MediaItemID, item.Position, item.Duration, thresholds)
		default:
			updateErr = store.UpdateProgress(ctx, profileID, item.MediaItemID, item.Position, item.Duration, thresholds)
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
		triggerProfileRefresh(ctx, h.profileStaler, h.profileRefreshRequester, userID, profileID)
		for _, item := range updates {
			if item.MediaItemID == "" {
				continue
			}
			publishUserStateEvent(
				ctx,
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

	return results, nil
}

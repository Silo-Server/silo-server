package abs

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// extras_handlers.go bundles the ABS endpoints that don't fit naturally
// into the existing per-domain handler files: server discovery (ping /
// healthcheck / init), year-in-review stats, the ebook / e-reader / email
// surface (stub responses until the scanner extends), and the podcast
// endpoints (also stubs — silo's catalog is audiobook-only in v1).
//
// Each handler emits a shape compatible with the official audiobookshelf
// clients (AudioBooth, audiobookshelf-app) so a request never explodes the
// client. Stub endpoints return the well-formed "empty" / "unavailable"
// shape rather than 404/500: clients have been observed to render error
// dialogs on hard failures but to silently degrade on empty arrays.

// ---------------------------------------------------------------------------
// Server discovery
// ---------------------------------------------------------------------------

// handlePing — GET /ping
// Standard ABS heartbeat. The canonical server returns {"success": true};
// AudioBooth uses this for liveness checks on the saved server before
// attempting auth.
func (h *Handler) handlePing(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// handleHealthcheck — GET /healthcheck
// Alias of /ping; some deployments hit this from k8s/docker probes.
func (h *Handler) handleHealthcheck(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleInit — GET /init
// Returns the bootstrap payload ABS clients read to decide whether the
// server needs first-run setup. silo is always "initialized" (there's no
// install wizard); we surface that so clients skip straight to login.
func (h *Handler) handleInit(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"isInit":         true,
		"language":       "en-us",
		"authMethods":    []string{"local"},
		"authFormData":   map[string]any{},
		"serverSettings": map[string]any{},
	})
}

// ---------------------------------------------------------------------------
// Auth-settings — clients fetch this to enumerate available providers
// ---------------------------------------------------------------------------

// handleAuthSettings — GET /auth-settings
// AudioBooth queries this on the "Add server" screen to know whether OIDC
// is enabled. silo is local-auth-only today.
func (h *Handler) handleAuthSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"authActiveAuthMethods":      []string{"local"},
		"authOpenIDIssuerURL":        nil,
		"authOpenIDAuthorizationURL": nil,
		"authPasswordlessSettings":   map[string]any{},
	})
}

// ---------------------------------------------------------------------------
// Year-in-review stats — /me/stats/year/{year}
// ---------------------------------------------------------------------------

// handleYearStats — GET /me/stats/year/{year}
// AudioBooth's `fetchYearStats(year:)` decodes a YearStats struct with 13
// keys (totals + topAuthors / topGenres / mostListenedNarrator /
// mostListenedMonth / numBooksFinished / numBooksListened /
// longestAudiobookFinished / booksWithCovers / finishedBooksWithCovers).
//
// We synthesize the shape from AggregateStats (no per-year rollup table
// yet — that lands when listening_history has at least a year of data).
// Empty arrays are emitted with the JSON key present so the Swift decoder
// doesn't choke on missing fields.
func (h *Handler) handleYearStats(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Year param accepted but currently unused — when listening_history
	// reaches multi-year scale we'll filter AggregateStats by year here.
	_, _ = strconv.Atoi(chi.URLParam(r, "year"))

	totalSeconds := 0
	totalSessions := 0
	if h.deps.PlaybackSessionStore != nil {
		if stats, err := h.deps.PlaybackSessionStore.AggregateStats(r.Context(), a.UserID, a.ProfileID); err == nil {
			totalSeconds = stats.TotalTime
			totalSessions = stats.Items
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"totalListeningSessions":    totalSessions,
		"totalListeningTime":        float64(totalSeconds),
		"totalBookListeningTime":    float64(totalSeconds),
		"totalPodcastListeningTime": 0.0,
		"topAuthors":                []any{},
		"topGenres":                 []any{},
		"mostListenedNarrator":      nil,
		"mostListenedMonth":         nil,
		"numBooksFinished":          0,
		"numBooksListened":          totalSessions,
		"longestAudiobookFinished":  nil,
		"booksWithCovers":           []string{},
		"finishedBooksWithCovers":   []string{},
	})
}

// ---------------------------------------------------------------------------
// Progress — DELETE and episode-progress stub
// ---------------------------------------------------------------------------

// handleDeleteItemProgress — DELETE /me/progress/{libraryItemId}
// Backs the ABS "Reset Progress" action. Idempotent.
func (h *Handler) handleDeleteItemProgress(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.ProgressStore == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	contentID := chi.URLParam(r, "libraryItemId")
	if err := h.deps.ProgressStore.DeleteProgress(r.Context(), a.UserID, a.ProfileID, contentID); err != nil {
		slog.Warn("abs delete progress failed", "err", err, "content", contentID)
		http.Error(w, "delete progress failed", http.StatusInternalServerError)
		return
	}
	h.publish(a.UserID, "user_item_progress_updated", map[string]any{
		"data": map[string]any{"libraryItemId": contentID, "currentTime": 0, "isFinished": false, "progress": 0},
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleSetEpisodeProgress — PATCH /me/progress/{libraryItemId}/{episodeId}
// Podcast episode progress. silo's catalog is audiobook-only in v1; this
// returns the empty progress shape so the client can store offline state
// without raising an error.
func (h *Handler) handleSetEpisodeProgress(w http.ResponseWriter, r *http.Request) {
	if a, ok := absAuthFrom(r); !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"libraryItemId": chi.URLParam(r, "libraryItemId"),
		"episodeId":     chi.URLParam(r, "episodeId"),
		"currentTime":   0.0,
		"duration":      0.0,
		"isFinished":    false,
		"progress":      0.0,
		"lastUpdate":    0,
	})
}

// ---------------------------------------------------------------------------
// Ebooks — stub surface until ebook scanner lands
// ---------------------------------------------------------------------------

// handleEbookFile — GET /items/{id}/ebook/{fileid}
// Streams an ebook file (epub / pdf / mobi / cbz). silo's audiobook
// scanner does not yet enumerate ebook files; until it does this returns
// 404. The shape was intentionally chosen over 501 because the ABS web
// reader treats 404 as "no ebook available for this item" and degrades
// cleanly; 501 surfaces an alarming error banner.
func (h *Handler) handleEbookFile(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "ebook not available", http.StatusNotFound)
}

// handleEbookStatus — PATCH /items/{id}/ebook/{fileid}/status
// Marks an ebook file as read/unread. silo has no ebook catalog yet;
// accept the request and return the empty status object so the client
// optimistic update succeeds.
func (h *Handler) handleEbookStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"libraryItemId":   chi.URLParam(r, "id"),
		"fileId":          chi.URLParam(r, "fileid"),
		"isSupplementary": false,
	})
}

// ---------------------------------------------------------------------------
// E-reader devices + ebook email delivery — stub
// ---------------------------------------------------------------------------

// handleListEreaderDevices — GET /me/ereader-devices
// Returns an empty list — silo has no email infrastructure wired yet.
// The official client UI hides the "Send to e-reader" CTA when the list
// is empty, which is the desired state today.
func (h *Handler) handleListEreaderDevices(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ereaderDevices": []any{}})
}

// handleSendEbookToDevice — POST /emails/send-ebook-to-device
// silo has no SMTP/email integration; surface 503 so the mobile UI can
// show a clear "Email delivery not configured" toast rather than a stuck
// spinner.
func (h *Handler) handleSendEbookToDevice(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "email delivery not configured", http.StatusServiceUnavailable)
}

// ---------------------------------------------------------------------------
// Podcast endpoints — stubs (audiobook-only catalog in v1)
// ---------------------------------------------------------------------------

// handlePodcastFeed — POST /podcasts/feed
// Validates an RSS feed URL and returns the parsed podcast metadata so
// the user can preview before subscribing. silo has no podcast subsystem;
// return an empty preview object so the client renders an "unknown feed"
// state and the user can back out without an error toast.
func (h *Handler) handlePodcastFeed(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"podcast": map[string]any{
		"metadata": map[string]any{"title": "", "author": "", "description": "", "feedUrl": "", "language": ""},
		"episodes": []any{},
	}})
}

// handlePlayEpisode — POST /items/{id}/play/{episodeId}
// Episode-scoped play-session start. Audiobook-only catalog can't
// resolve an episodeId, so 404 keeps the client behavior unambiguous.
func (h *Handler) handlePlayEpisode(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "episode not found", http.StatusNotFound)
}

// handleRecentEpisodes — GET /libraries/{id}/recent-episodes
// Paged list of the newest podcast episodes across the library. silo has
// no episodes; emit the canonical paged-envelope so the home shelf
// renders as "no recent episodes".
func (h *Handler) handleRecentEpisodes(w http.ResponseWriter, r *http.Request) {
	limit, page := readPagedQuery(r, 25)
	writeJSON(w, http.StatusOK, pagedEnvelope(
		[]map[string]any{}, 0, limit, page, "publishedAt", true, "", false, "",
	))
}

// handleSearchPodcast — GET /search/podcast
// Podcast directory discovery. silo doesn't proxy iTunes/PodcastIndex
// today; return an empty results array so the client search UI shows
// "no results" cleanly.
func (h *Handler) handleSearchPodcast(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

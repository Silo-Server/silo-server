package abs

import (
	"context"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
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
		isInitKey:        true,
		languageKey:      languageEnglishUS,
		authMethodsKey:   []string{localAuthMethod},
		authFormDataKey:  map[string]any{},
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
		authActiveMethodsKey:         []string{localAuthMethod},
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
	contentID := chi.URLParam(r, libraryItemIDKey)
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		h.writeAccessResolutionError(w, r, err)
		return
	}
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), contentID, access)
	if err != nil {
		slog.ErrorContext(r.Context(), "abs delete progress item lookup failed", "component", "audiobooks", "err", err, "content", contentID)
		http.Error(w, "item lookup failed", http.StatusInternalServerError)
		return
	}
	if item == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if item.Type == mediaTypeEbook {
		if h.deps.EbookProgressStore == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		err = h.deps.EbookProgressStore.DeleteEbookProgress(r.Context(), a.UserID, a.ProfileID, contentID)
	} else if h.deps.ProgressStore != nil {
		err = h.deps.ProgressStore.DeleteProgress(r.Context(), a.UserID, a.ProfileID, contentID)
	}
	if err != nil {
		slog.WarnContext(r.Context(), "abs delete progress failed", "component", "audiobooks", "err", err, "content", contentID)
		http.Error(w, "delete progress failed", http.StatusInternalServerError)
		return
	}
	h.publish(a.UserID, "user_item_progress_updated", map[string]any{
		dataKey: map[string]any{libraryItemIDKey: contentID, currentTimeKey: 0, isFinishedKey: false, progressKey: 0},
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
		libraryItemIDKey: chi.URLParam(r, libraryItemIDKey),
		episodeIDKey:     chi.URLParam(r, episodeIDKey),
		currentTimeKey:   0.0,
		durationKey:      0.0,
		isFinishedKey:    false,
		progressKey:      0.0,
		lastUpdateKey:    0,
	})
}

// ---------------------------------------------------------------------------
// Ebooks
// ---------------------------------------------------------------------------

// handleEbookFile — GET /items/{id}/ebook[/{fileid}]
// Streams the primary ebook when fileid is omitted, or a selected
// supplementary ebook when it is present.
func (h *Handler) handleEbookFile(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	contentID := chi.URLParam(r, "id")
	fileID := 0
	if rawFileID := chi.URLParam(r, "fileid"); rawFileID != "" {
		var err error
		fileID, err = strconv.Atoi(rawFileID)
		if err != nil || fileID <= 0 {
			http.Error(w, "invalid ebook file", http.StatusBadRequest)
			return
		}
	}
	if contentID == "" {
		http.Error(w, "item required", http.StatusBadRequest)
		return
	}
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		h.writeAccessResolutionError(w, r, err)
		return
	}
	files, err := h.deps.MediaStore.GetMediaFiles(r.Context(), contentID, access)
	if err != nil {
		http.Error(w, "load files: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if fileID == 0 {
		if store, ok := h.deps.MediaStore.(ebookPrimaryStore); ok {
			if primaryID, configured, hasPrimary, err := store.GetPrimaryEbookFileID(r.Context(), contentID); err != nil {
				http.Error(w, "load primary ebook: "+err.Error(), http.StatusInternalServerError)
				return
			} else if configured {
				if !hasPrimary {
					http.Error(w, "ebook not found", http.StatusNotFound)
					return
				}
				fileID = primaryID
			}
		}
	}
	selected := selectEbookFile(files, fileID)
	if selected == nil {
		http.Error(w, "ebook not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", ebookContentType(filepath.Ext(selected.FilePath)))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": filepath.Base(selected.FilePath)}))
	_ = playback.ServeDirectPlay(w, r, selected.FilePath)
}

// selectEbookFile follows ABS primary-file behavior: use an explicitly
// requested supported ebook, otherwise prefer EPUB and fall back to the first
// supported ebook in stable media-file order.
func selectEbookFile(files []*models.MediaFile, fileID int) *models.MediaFile {
	var first *models.MediaFile
	for _, file := range files {
		contentType := ebookContentType(filepath.Ext(file.FilePath))
		if contentType == "" {
			continue
		}
		if fileID != 0 {
			if file.ID == fileID {
				return file
			}
			continue
		}
		if strings.EqualFold(filepath.Ext(file.FilePath), ".epub") {
			return file
		}
		if first == nil {
			first = file
		}
	}
	return first
}

// handleEbookStatus — PATCH /items/{id}/ebook/{fileid}/status
func (h *Handler) handleEbookStatus(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	contentID := chi.URLParam(r, "id")
	fileID, err := strconv.Atoi(chi.URLParam(r, "fileid"))
	if contentID == "" || err != nil || fileID <= 0 {
		http.Error(w, "item and file required", http.StatusBadRequest)
		return
	}
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		h.writeAccessResolutionError(w, r, err)
		return
	}
	files, err := h.deps.MediaStore.GetMediaFiles(r.Context(), contentID, access)
	if err != nil {
		http.Error(w, "load ebook files: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if selectEbookFile(files, fileID) == nil {
		http.Error(w, "invalid ebook file id", http.StatusBadRequest)
		return
	}
	authorizer, ok := h.deps.AccessResolver.(MetadataCurationAuthorizer)
	if !ok {
		http.Error(w, "ebook primary selection unavailable", http.StatusServiceUnavailable)
		return
	}
	allowed, err := authorizer.CanCurateMetadata(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		slog.ErrorContext(r.Context(), "abs ebook primary authorization failed", "component", "audiobooks", "err", err, "user", a.UserID, "item", contentID)
		http.Error(w, "ebook primary authorization failed", http.StatusInternalServerError)
		return
	}
	if !allowed {
		http.Error(w, "metadata curation permission required", http.StatusForbidden)
		return
	}
	store, ok := h.deps.MediaStore.(ebookPrimaryStore)
	if !ok {
		http.Error(w, "ebook primary selection unavailable", http.StatusServiceUnavailable)
		return
	}
	primaryID, configured, hasPrimary, err := store.GetPrimaryEbookFileID(r.Context(), contentID)
	if err != nil {
		http.Error(w, "load primary ebook: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defaultFile := selectEbookFile(files, 0)
	if (configured && hasPrimary && primaryID == fileID) || (!configured && defaultFile != nil && defaultFile.ID == fileID) {
		if err := store.ClearPrimaryEbookFileID(r.Context(), contentID); err != nil {
			http.Error(w, "clear primary ebook: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	err = store.SetPrimaryEbookFileID(r.Context(), contentID, fileID)
	if err != nil {
		http.Error(w, "set primary ebook: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

type ebookPrimaryStore interface {
	GetPrimaryEbookFileID(ctx context.Context, contentID string) (fileID int, configured bool, hasPrimary bool, err error)
	SetPrimaryEbookFileID(ctx context.Context, contentID string, fileID int) error
	ClearPrimaryEbookFileID(ctx context.Context, contentID string) error
}

func ebookContentType(ext string) string {
	switch strings.ToLower(ext) {
	case ".epub":
		return ebookEPUBMimeType
	case ".pdf":
		return "application/pdf"
	case ".mobi", ".azw", ".azw3":
		return "application/x-mobipocket-ebook"
	case ".cbz":
		return "application/vnd.comicbook+zip"
	case ".cbr":
		return "application/vnd.comicbook-rar"
	case ".fb2":
		return "application/x-fictionbook+xml"
	}
	return ""
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
	writeJSON(w, http.StatusOK, map[string]any{podcastKey: map[string]any{
		metadataKey: map[string]any{titleKey: "", "author": "", descriptionKey: "", "feedUrl": "", languageKey: ""},
		"episodes":  []any{},
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

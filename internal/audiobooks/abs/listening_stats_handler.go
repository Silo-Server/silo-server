package abs

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

func (h *Handler) handleListeningStats(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaybackSessionStore == nil {
		writeJSON(w, http.StatusOK, statsToABS(Stats{}))
		return
	}
	stats, err := h.deps.PlaybackSessionStore.AggregateStats(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		slog.Error("abs listening stats failed", "err", err, "user", a.UserID)
		http.Error(w, "stats unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, statsToABS(stats))
}

// handleListeningSessions — GET /me/listening-sessions
//
// Real ABS (MeController.getListeningSessions) returns
// { total, numPages, page, itemsPerPage, sessions } — NOT the generic
// pagedEnvelope shape used by browse endpoints (results/sortBy/filterBy).
// Each entry in `sessions` must match PlaybackSession.toJSON() key-for-key
// (server/objects/PlaybackSession.js upstream) or strict decoders
// (Flutter/Swift) throw keyNotFound on the first missing field.
func (h *Handler) handleListeningSessions(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	itemsPerPage, page := readPagedQuery(r, 10)
	if h.deps.PlaybackSessionStore == nil {
		writeJSON(w, http.StatusOK, listeningSessionsEnvelope([]map[string]any{}, 0, itemsPerPage, page))
		return
	}
	sessions, total, err := h.deps.PlaybackSessionStore.ListClosedSessions(r.Context(), a.UserID, a.ProfileID, itemsPerPage, page*itemsPerPage)
	if err != nil {
		slog.Error("abs listening sessions failed", "err", err, "user", a.UserID)
		http.Error(w, "sessions unavailable", http.StatusInternalServerError)
		return
	}

	// Best-effort batch hydration of mediaMetadata/displayTitle/displayAuthor
	// for every session's content item. A lookup failure (deleted item,
	// access revoked, store error) must never crash the response — the
	// session just falls back to a placeholder mediaMetadata shape built
	// from a stub item so every key strict clients expect is still present.
	items := map[string]*models.MediaItem{}
	if h.deps.MediaStore != nil && len(sessions) > 0 {
		access, aerr := h.accessFilterForAuth(r.Context(), a)
		if aerr != nil {
			slog.Debug("abs listening sessions: resolve access failed", "user", a.UserID, "err", aerr)
		} else {
			contentIDs := make([]string, 0, len(sessions))
			for _, s := range sessions {
				contentIDs = append(contentIDs, s.ContentID)
			}
			hydrated, herr := h.deps.MediaStore.GetAudiobooksByIDs(r.Context(), contentIDs, access)
			if herr != nil {
				slog.Debug("abs listening sessions: hydrate media items failed", "user", a.UserID, "err", herr)
			} else {
				items = hydrated
			}
		}
	}

	baseURL := h.absBaseURL(r)
	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, sessionToABS(s, items[s.ContentID], baseURL))
	}
	writeJSON(w, http.StatusOK, listeningSessionsEnvelope(out, total, itemsPerPage, page))
}

// handleListeningSessionDetail — GET /me/listening-sessions/{sid}
// Returns a single PlaybackSession.toJSON()-shaped object, not the thin
// 5-field object the prior implementation emitted.
func (h *Handler) handleListeningSessionDetail(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaybackSessionStore == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	sid := chi.URLParam(r, "sid")
	sess, err := h.deps.PlaybackSessionStore.GetPlaybackSession(r.Context(), sid)
	if err != nil || sess.UserID != a.UserID {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	var item *models.MediaItem
	if h.deps.MediaStore != nil {
		access, aerr := h.accessFilterForAuth(r.Context(), a)
		if aerr != nil {
			slog.Debug("abs listening session detail: resolve access failed", "user", a.UserID, "err", aerr)
		} else if fetched, ferr := h.deps.MediaStore.GetAudiobookByID(r.Context(), sess.ContentID, access); ferr != nil {
			slog.Debug("abs listening session detail: hydrate media item failed", "user", a.UserID, "content_id", sess.ContentID, "err", ferr)
		} else {
			item = fetched
		}
	}

	writeJSON(w, http.StatusOK, sessionToABS(sess, item, h.absBaseURL(r)))
}

func statsToABS(s Stats) map[string]any {
	dow := map[string]int{}
	for i, sec := range s.DayOfWeek {
		dow[strconv.Itoa(i)] = sec
	}
	days := make([]map[string]any, 0, len(s.Days))
	for _, d := range s.Days {
		days = append(days, map[string]any{"date": d.Date, "seconds": d.Seconds})
	}
	monthly := make([]map[string]any, 0, len(s.Monthly))
	for _, m := range s.Monthly {
		monthly = append(monthly, map[string]any{"month": m.Month, "seconds": m.Seconds})
	}
	return map[string]any{
		"totalTime": s.TotalTime,
		"items":     s.Items,
		"days":      days,
		"dayOfWeek": dow,
		"monthly":   monthly,
	}
}

// listeningSessionsEnvelope builds the exact envelope shape
// MeController.getListeningSessions returns upstream. It intentionally does
// NOT reuse pagedEnvelope: that helper's {results,sortBy,filterBy,minified}
// shape is for browse/list endpoints, while real ABS listening-sessions
// responses only ever carry {total,numPages,page,itemsPerPage,sessions}.
func listeningSessionsEnvelope(sessions []map[string]any, total, itemsPerPage, page int) map[string]any {
	numPages := 0
	if itemsPerPage > 0 {
		numPages = (total + itemsPerPage - 1) / itemsPerPage
	}
	return map[string]any{
		"total":        total,
		"numPages":     numPages,
		"page":         page,
		"itemsPerPage": itemsPerPage,
		"sessions":     sessions,
	}
}

// sessionToABS converts a silo ABSPlaybackSession row into the exact key set
// of PlaybackSession.toJSON() upstream (server/objects/PlaybackSession.js).
// item may be nil (deleted item, access revoked, lookup error) — in that
// case a stub MediaItem is fed through the same mediaMetadata builder the
// /play endpoint uses (buildSiloPlayMediaMetadata), so every key is still
// present with empty/zero values rather than being omitted.
func sessionToABS(s ABSPlaybackSession, item *models.MediaItem, baseURL string) map[string]any {
	if item == nil {
		item = &models.MediaItem{ContentID: s.ContentID}
	}
	mediaMetadata := buildSiloPlayMediaMetadata(item)

	displayAuthor := ""
	if v, ok := mediaMetadata["authorName"].(string); ok {
		displayAuthor = v
	}

	startedAt := s.StartedAt
	updatedAt := s.LastSyncAt
	if updatedAt.IsZero() {
		updatedAt = startedAt
	}
	dateStr := ""
	dayOfWeek := ""
	var startedAtMs, updatedAtMs int64
	if !startedAt.IsZero() {
		dateStr = startedAt.UTC().Format("2006-01-02")
		dayOfWeek = startedAt.UTC().Weekday().String()
		startedAtMs = startedAt.UnixMilli()
	}
	if !updatedAt.IsZero() {
		updatedAtMs = updatedAt.UnixMilli()
	}

	out := map[string]any{
		"id":            s.ID,
		"userId":        s.UserID,
		"libraryId":     VirtualLibraryID,
		"libraryItemId": s.ContentID,
		"bookId":        s.ContentID,
		"episodeId":     nil, // silo is audiobook-only; podcasts are out of scope
		"mediaType":     LibraryMediaType,
		"mediaMetadata": mediaMetadata,
		// Chapters are not loaded for session list/detail responses (would
		// require an extra media-files fetch per session); real ABS clients
		// read chapters from the /play or /sync payload for in-player
		// rendering, so an empty list here is a safe placeholder rather than
		// the full per-file chapter set.
		"chapters":      []map[string]any{},
		"displayTitle":  item.Title,
		"displayAuthor": displayAuthor,
		"coverPath":     baseURL + "/api/items/" + s.ContentID + "/cover",
		// duration: the total book duration isn't tracked on the session row
		// itself; 0 is a safe placeholder (never crashes, only affects the
		// progress-bar denominator on this historical-session view).
		"duration":    0,
		"playMethod":  0, // DIRECTPLAY
		"mediaPlayer": "exo-player",
		"deviceInfo": map[string]any{
			"deviceId":      "unknown",
			"manufacturer":  "Unknown",
			"model":         "Unknown",
			"sdkVersion":    0,
			"clientVersion": "0.0.0",
		},
		"serverVersion": ServerVersion,
		"date":          dateStr,
		"dayOfWeek":     dayOfWeek,
		"timeListening": s.TimeListeningSeconds,
		// startTime: media position when this session began. Not persisted
		// separately from currentTime on ABSPlaybackSession; 0 is safe.
		"startTime":   0,
		"currentTime": s.CurrentPositionSeconds,
		"startedAt":   startedAtMs,
		"updatedAt":   updatedAtMs,
	}
	// Additive extra field (not part of upstream toJSON) kept for backward
	// compatibility with any existing silo-side consumers.
	if s.ClosedAt != nil {
		out["closedAt"] = s.ClosedAt.UnixMilli()
	}
	return out
}

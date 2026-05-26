package abs

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
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

func (h *Handler) handleListeningSessions(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaybackSessionStore == nil {
		writeJSON(w, http.StatusOK, pagedEnvelope([]any{}, 0, 30, 0, "started_at", true, "", false, ""))
		return
	}
	limit, page := readPagedQuery(r, 30)
	sessions, total, err := h.deps.PlaybackSessionStore.ListClosedSessions(r.Context(), a.UserID, a.ProfileID, limit, page*limit)
	if err != nil {
		slog.Error("abs listening sessions failed", "err", err, "user", a.UserID)
		http.Error(w, "sessions unavailable", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, sessionToABS(s))
	}
	writeJSON(w, http.StatusOK, pagedEnvelope(out, total, limit, page, "started_at", true, "", false, ""))
}

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
	writeJSON(w, http.StatusOK, sessionToABS(sess))
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

func sessionToABS(s ABSPlaybackSession) map[string]any {
	out := map[string]any{
		"id":            s.ID,
		"libraryItemId": s.ContentID,
		"userId":        s.UserID,
		"timeListening": s.TimeListeningSeconds,
		"currentTime":   s.CurrentPositionSeconds,
	}
	if s.ClosedAt != nil {
		out["closedAt"] = s.ClosedAt.UnixMilli()
	}
	return out
}

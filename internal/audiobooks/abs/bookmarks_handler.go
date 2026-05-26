package abs

import (
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// bookmarkBody is the JSON body for POST and PATCH
// /me/item/{itemId}/bookmark. Time is a pointer so we can distinguish
// missing (→ 400) from the literal 0.0.
type bookmarkBody struct {
	Title string   `json:"title"`
	Time  *float64 `json:"time"`
}

// handleUpsertBookmark backs both POST (reason="bookmark_created") and
// PATCH (reason="bookmark_updated") /me/item/{itemId}/bookmark. Both
// share the exact same upsert semantics — only the realtime event
// reason differs.
func (h *Handler) handleUpsertBookmark(reason string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, ok := absAuthFrom(r)
		if !ok || a.UserID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if h.deps.BookmarkStore == nil {
			http.Error(w, "bookmark store unavailable", http.StatusServiceUnavailable)
			return
		}

		itemID := chi.URLParam(r, "itemId")
		if itemID == "" {
			http.Error(w, "itemId required", http.StatusBadRequest)
			return
		}

		// 1 MiB body cap — matches handleStandaloneLogin.
		var body bookmarkBody
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		if err := dec.Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if body.Time == nil || math.IsNaN(*body.Time) {
			http.Error(w, "time required", http.StatusBadRequest)
			return
		}

		// Item validation: avoid orphan bookmark rows whose item no
		// longer exists. Skipped on DELETE (see handleDeleteBookmark).
		item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), itemID)
		if err != nil || item == nil {
			http.Error(w, "item not found", http.StatusNotFound)
			return
		}

		bm, err := h.deps.BookmarkStore.Upsert(r.Context(), a.UserID, a.ProfileID, itemID, *body.Time, body.Title)
		if err != nil {
			slog.Error("abs bookmark upsert failed", "err", err, "user", a.UserID, "item", itemID)
			http.Error(w, "bookmark persist failed", http.StatusInternalServerError)
			return
		}

		h.publish(a.UserID, "user_updated", map[string]any{
			"reason":   reason,
			"bookmark": bookmarkToABS(bm),
		})

		writeBookmarkList(w, r, h, a.UserID, a.ProfileID, itemID)
	}
}

// writeBookmarkList re-fetches the item's bookmarks and writes them as
// the JSON response. On list-fetch failure after a successful mutation,
// degrade to 200 + empty list + slog.Warn (the mutation already
// committed; failing the response would mis-report the state).
func writeBookmarkList(w http.ResponseWriter, r *http.Request, h *Handler, userID, profileID, itemID string) {
	rows, err := h.deps.BookmarkStore.List(r.Context(), userID, profileID, itemID)
	if err != nil {
		slog.Warn("abs bookmark list after mutation failed", "err", err, "user", userID, "item", itemID)
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, b := range rows {
		out = append(out, bookmarkToABS(b))
	}
	writeJSON(w, http.StatusOK, out)
}

// parseBookmarkTime parses the {time} URL parameter on DELETE
// /me/item/{itemId}/bookmark/{time}. Returns (0, false) on parse
// failure.
func parseBookmarkTime(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) {
		return 0, false
	}
	return v, true
}

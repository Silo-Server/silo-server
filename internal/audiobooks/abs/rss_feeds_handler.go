package abs

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

var slugRe = regexp.MustCompile(`^[a-z0-9-]{4,64}$`)

type feedOpenBody struct {
	Slug     string `json:"slug"`
	Minified bool   `json:"minified"`
}

func (h *Handler) handleListRSSFeeds(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.RSSFeedStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"feeds": []any{}})
		return
	}
	rows, err := h.deps.RSSFeedStore.ListUserFeeds(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		slog.Error("abs feed list failed", "err", err, "user", a.UserID)
		http.Error(w, "feed list failed", http.StatusInternalServerError)
		return
	}
	base := h.absBaseURL(r)
	out := make([]map[string]any, 0, len(rows))
	for _, f := range rows {
		out = append(out, rssFeedToABS(f, base))
	}
	writeJSON(w, http.StatusOK, map[string]any{"feeds": out})
}

func (h *Handler) handleOpenItemFeed(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.RSSFeedStore == nil {
		http.Error(w, "feed store unavailable", http.StatusServiceUnavailable)
		return
	}
	itemID := chi.URLParam(r, "itemId")
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), itemID)
	if err != nil || item == nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}

	var body feedOpenBody
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)

	slug := strings.ToLower(strings.TrimSpace(body.Slug))
	if slug == "" {
		slug = randomSlug()
	} else if !slugRe.MatchString(slug) {
		http.Error(w, "invalid slug", http.StatusBadRequest)
		return
	}

	f := RSSFeed{
		ID:            ulid.Make().String(),
		UserID:        a.UserID,
		ProfileID:     a.ProfileID,
		LibraryItemID: itemID,
		Slug:          slug,
		Minified:      body.Minified,
	}
	if err := h.deps.RSSFeedStore.CreateFeed(r.Context(), f); err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique") {
			http.Error(w, "slug taken", http.StatusConflict)
			return
		}
		slog.Error("abs feed create failed", "err", err, "user", a.UserID)
		http.Error(w, "feed persist failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.RSSFeedStore.GetFeed(r.Context(), f.ID)
	if errors.Is(err, ErrNotFound) || err != nil {
		f.CreatedAt = time.Now()
		persisted = f
	}
	writeJSON(w, http.StatusOK, rssFeedToABS(persisted, h.absBaseURL(r)))
}

func (h *Handler) handleCloseFeed(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.RSSFeedStore == nil {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	id := chi.URLParam(r, "id")
	f, err := h.deps.RSSFeedStore.GetFeed(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && f.UserID != a.UserID) {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs feed get-for-close failed", "err", err, "id", id)
		http.Error(w, "feed get failed", http.StatusInternalServerError)
		return
	}
	if err := h.deps.RSSFeedStore.CloseFeed(r.Context(), id); err != nil {
		slog.Error("abs feed close failed", "err", err, "id", id)
		http.Error(w, "feed close failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func randomSlug() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(buf)
}

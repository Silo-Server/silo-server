package abs

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) handleAuthorDetail(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := chi.URLParam(r, "id")
	author, err := h.deps.MediaStore.GetAuthorByID(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "author not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs author detail failed", "err", err, "id", id)
		http.Error(w, "author get failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, authorToABS(author))
}

func (h *Handler) handleSeriesDetail(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	idRaw := chi.URLParam(r, "id")
	id, err := url.PathUnescape(idRaw)
	if err != nil {
		id = idRaw
	}
	series, err := h.deps.MediaStore.GetSeriesByName(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "series not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs series detail failed", "err", err, "id", id)
		http.Error(w, "series get failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, seriesToABS(series))
}

func authorToABS(a Author) map[string]any {
	books := make([]map[string]any, 0, len(a.Books))
	for _, b := range a.Books {
		books = append(books, map[string]any{"id": b.ContentID, "media": map[string]any{"metadata": map[string]any{"title": b.Title}}})
	}
	return map[string]any{
		"id":       a.ID,
		"name":     a.Name,
		"numBooks": len(a.Books),
		"books":    books,
	}
}

func seriesToABS(s Series) map[string]any {
	books := make([]map[string]any, 0, len(s.Books))
	for _, b := range s.Books {
		books = append(books, map[string]any{"id": b.ContentID, "media": map[string]any{"metadata": map[string]any{"title": b.Title}}})
	}
	return map[string]any{
		"id":       s.ID,
		"name":     s.Name,
		"numBooks": len(s.Books),
		"books":    books,
	}
}

package abs

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

// playlistBody is the JSON body for POST and PATCH /playlists[/{id}].
// Fields are pointers so PATCH can distinguish "field absent" from
// "field set to empty/false".
type playlistBody struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	CoverItem   *string `json:"cover_item"`
	IsPublic    *bool   `json:"isPublic"`
}

// playlistItemRef is the JSON body for adding/removing a single
// playlist item (and an element of the batch arrays).
type playlistItemRef struct {
	LibraryItemID string `json:"libraryItemId"`
	EpisodeID     string `json:"episodeId"`
}

// handleCreatePlaylist — POST /playlists.
// Body: {name, description?, cover_item?, isPublic?}.
// Returns the created playlist in full-shape (empty items[]).
// Fires playlist_added on success.
func (h *Handler) handleCreatePlaylist(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		http.Error(w, "playlist store unavailable", http.StatusServiceUnavailable)
		return
	}

	var body playlistBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name == nil || *body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	p := Playlist{
		ID:        ulid.Make().String(),
		UserID:    a.UserID,
		ProfileID: a.ProfileID,
		Name:      *body.Name,
	}
	if body.Description != nil {
		p.Description = *body.Description
	}
	if body.CoverItem != nil {
		p.CoverItem = *body.CoverItem
	}
	if body.IsPublic != nil {
		p.IsPublic = *body.IsPublic
	}
	if err := h.deps.PlaylistStore.CreatePlaylist(r.Context(), p); err != nil {
		slog.Error("abs playlist create failed", "err", err, "user", a.UserID)
		http.Error(w, "playlist persist failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), p.ID)
	if errors.Is(err, ErrNotFound) {
		persisted = p
	} else if err != nil {
		persisted = p
	}

	h.publish(a.UserID, "playlist_added", map[string]any{"id": p.ID, "name": p.Name})
	writeJSON(w, http.StatusOK, h.playlistFullShape(r, persisted))
}

// playlistFullShape renders a Playlist in full-shape, hydrating items[]
// via MediaStore for audiobook items (episode items echo bare refs).
func (h *Handler) playlistFullShape(r *http.Request, p Playlist) map[string]any {
	items := h.playlistItems(r, p.ID)
	return playlistToABS(p, items)
}

// playlistItems resolves items in a playlist to wire-shape entries.
// Audiobook items (empty episodeId) hydrate title via MediaStore.
// Episode items are emitted as bare {libraryItemId, episodeId, position}.
func (h *Handler) playlistItems(r *http.Request, playlistID string) []map[string]any {
	if h.deps.PlaylistStore == nil {
		return []map[string]any{}
	}
	rows, err := h.deps.PlaylistStore.ListPlaylistItems(r.Context(), playlistID)
	if err != nil {
		slog.Warn("abs playlist list-items failed", "err", err, "playlist", playlistID)
		return []map[string]any{}
	}
	lib := h.resolveDefaultLibrary(r.Context())
	libID := audiobookLibraryID(lib)
	out := make([]map[string]any, 0, len(rows))
	for _, it := range rows {
		entry := map[string]any{
			"libraryItemId": it.LibraryItemID,
			"position":      it.Position,
		}
		if it.EpisodeID != "" {
			entry["episodeId"] = it.EpisodeID
		} else {
			// Audiobook hydration.
			if item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), it.LibraryItemID); err == nil && item != nil {
				entry["libraryId"] = libID
				entry["title"] = item.Title
			}
		}
		out = append(out, entry)
	}
	return out
}

// playlistURLID is a tiny shim around chi.URLParam(r, "id") to read
// uniformly with the collections handler's chiURLID.
func playlistURLID(r *http.Request) string { return chi.URLParam(r, "id") }

// handleListPlaylists — GET /playlists.
// Returns the caller's playlists wrapped in {"playlists": [...]}.
// List-shape (no items[]).
func (h *Handler) handleListPlaylists(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"playlists": []any{}})
		return
	}
	rows, err := h.deps.PlaylistStore.ListUserPlaylists(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		slog.Error("abs playlist list failed", "err", err, "user", a.UserID)
		http.Error(w, "playlist list failed", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		out = append(out, playlistToABS(p, nil))
	}
	writeJSON(w, http.StatusOK, map[string]any{"playlists": out})
}

// handleGetPlaylist — GET /playlists/{id}.
// Owner gets full-shape; non-owner gets full-shape only when isPublic.
// Otherwise 404 (no existence leak).
func (h *Handler) handleGetPlaylist(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	p, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), playlistURLID(r))
	if errors.Is(err, ErrNotFound) || (err == nil && p.UserID != a.UserID && !p.IsPublic) {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs playlist get failed", "err", err)
		http.Error(w, "playlist get failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, h.playlistFullShape(r, p))
}

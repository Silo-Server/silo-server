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

// handleUpdatePlaylist — PATCH /playlists/{id}.
// Owner-only. Partial body. Fires playlist_updated.
func (h *Handler) handleUpdatePlaylist(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	id := playlistURLID(r)
	p, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && p.UserID != a.UserID) {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs playlist get-for-update failed", "err", err, "id", id)
		http.Error(w, "playlist get failed", http.StatusInternalServerError)
		return
	}

	var body playlistBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name != nil {
		p.Name = *body.Name
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
	if err := h.deps.PlaylistStore.UpdatePlaylist(r.Context(), p); err != nil {
		slog.Error("abs playlist update failed", "err", err, "id", id)
		http.Error(w, "playlist persist failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if err != nil {
		persisted = p
	}
	h.publish(a.UserID, "playlist_updated", map[string]any{"id": id})
	writeJSON(w, http.StatusOK, h.playlistFullShape(r, persisted))
}

// handleAddPlaylistItem — POST /playlists/{id}/item.
// Body: {libraryItemId, episodeId?}.
// Owner-only. Item validation: audiobooks validated via MediaStore
// (404 on unknown); episode items skip validation per spec §7.1 (the
// audiobook-only-hydration policy doesn't reject opaque episode IDs).
// Idempotent on (libraryItemId, episodeId) tuple. Fires playlist_updated.
func (h *Handler) handleAddPlaylistItem(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	id := playlistURLID(r)
	p, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && p.UserID != a.UserID) {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs playlist get-for-add failed", "err", err, "id", id)
		http.Error(w, "playlist get failed", http.StatusInternalServerError)
		return
	}

	var body playlistItemRef
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.LibraryItemID == "" {
		http.Error(w, "libraryItemId required", http.StatusBadRequest)
		return
	}

	// Audiobook items validated; episodes skip validation.
	if body.EpisodeID == "" {
		item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), body.LibraryItemID)
		if err != nil || item == nil {
			http.Error(w, "item not found", http.StatusNotFound)
			return
		}
	}

	if err := h.deps.PlaylistStore.AddPlaylistItem(r.Context(), id, body.LibraryItemID, body.EpisodeID); err != nil {
		slog.Error("abs playlist add-item failed", "err", err, "id", id)
		http.Error(w, "playlist persist failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if err != nil {
		persisted = p
	}
	h.publish(a.UserID, "playlist_updated", map[string]any{"id": id})
	writeJSON(w, http.StatusOK, h.playlistFullShape(r, persisted))
}

// handleDeletePlaylist — DELETE /playlists/{id}.
// Owner-only. Cascade drops abs_playlist_items via FK.
// Fires playlist_removed.
func (h *Handler) handleDeletePlaylist(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	id := playlistURLID(r)
	p, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && p.UserID != a.UserID) {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs playlist get-for-delete failed", "err", err, "id", id)
		http.Error(w, "playlist get failed", http.StatusInternalServerError)
		return
	}
	if err := h.deps.PlaylistStore.DeletePlaylist(r.Context(), id); err != nil {
		slog.Error("abs playlist delete failed", "err", err, "id", id)
		http.Error(w, "playlist delete failed", http.StatusInternalServerError)
		return
	}
	h.publish(a.UserID, "playlist_removed", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

// handleRemovePlaylistItem — DELETE /playlists/{id}/item/{libraryItemId}.
// Owner-only. Removes the item with empty episode_id. Idempotent.
// Fires playlist_updated.
func (h *Handler) handleRemovePlaylistItem(w http.ResponseWriter, r *http.Request) {
	h.removePlaylistItemImpl(w, r, "")
}

// handleRemovePlaylistEpisode — DELETE /playlists/{id}/item/{libraryItemId}/{episodeId}.
// Owner-only. Removes the item keyed on (libraryItemId, episodeId).
// Idempotent. Fires playlist_updated.
func (h *Handler) handleRemovePlaylistEpisode(w http.ResponseWriter, r *http.Request) {
	h.removePlaylistItemImpl(w, r, chi.URLParam(r, "episodeId"))
}

// removePlaylistItemImpl is the shared body for both remove variants.
// episodeIDFromURL is "" for the libraryItemId-only DELETE and the
// {episodeId} URL param for the episode-aware DELETE.
func (h *Handler) removePlaylistItemImpl(w http.ResponseWriter, r *http.Request, episodeIDFromURL string) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	id := playlistURLID(r)
	libItem := chi.URLParam(r, "libraryItemId")
	if libItem == "" {
		http.Error(w, "libraryItemId required", http.StatusBadRequest)
		return
	}

	p, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && p.UserID != a.UserID) {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs playlist get-for-remove failed", "err", err, "id", id)
		http.Error(w, "playlist get failed", http.StatusInternalServerError)
		return
	}

	if err := h.deps.PlaylistStore.RemovePlaylistItem(r.Context(), id, libItem, episodeIDFromURL); err != nil {
		slog.Error("abs playlist remove-item failed", "err", err, "id", id, "item", libItem, "episode", episodeIDFromURL)
		http.Error(w, "playlist delete failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if err != nil {
		persisted = p
	}
	h.publish(a.UserID, "playlist_updated", map[string]any{"id": id})
	writeJSON(w, http.StatusOK, h.playlistFullShape(r, persisted))
}

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

// collectionBody is the JSON body for POST and PATCH /collections[/{id}].
// All fields are optional on PATCH; name is required on POST (checked
// in the handler, not via tag-driven validation).
type collectionBody struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	IsPublic    *bool   `json:"isPublic"`
}

// handleCreateCollection — POST /collections.
// Body: {name, description?, isPublic?}. Returns the created collection
// in full-shape (with an empty books[] array).
func (h *Handler) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.CollectionStore == nil {
		http.Error(w, "collection store unavailable", http.StatusServiceUnavailable)
		return
	}

	var body collectionBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name == nil || *body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	c := Collection{
		ID:        ulid.Make().String(),
		UserID:    a.UserID,
		ProfileID: a.ProfileID,
		Name:      *body.Name,
	}
	if body.Description != nil {
		c.Description = *body.Description
	}
	if body.IsPublic != nil {
		c.IsPublic = *body.IsPublic
	}
	if err := h.deps.CollectionStore.CreateCollection(r.Context(), c); err != nil {
		slog.Error("abs collection create failed", "err", err, "user", a.UserID)
		http.Error(w, "collection persist failed", http.StatusInternalServerError)
		return
	}

	// Re-fetch to pick up server-set timestamps.
	persisted, err := h.deps.CollectionStore.GetCollection(r.Context(), c.ID)
	if errors.Is(err, ErrNotFound) {
		persisted = c
	} else if err != nil {
		slog.Warn("abs collection get-after-create failed", "err", err, "id", c.ID)
		persisted = c
	}
	writeJSON(w, http.StatusOK, h.collectionFullShape(r, persisted))
}

// collectionFullShape renders a Collection in full-shape, hydrating
// books[] via MediaStore. Errors during hydration degrade to bare
// {id, libraryId} entries so the response always reflects DB truth.
func (h *Handler) collectionFullShape(r *http.Request, c Collection) map[string]any {
	books := h.collectionBooks(r, c.ID)
	return collectionToABS(c, books)
}

// collectionBooks resolves the items in a collection to wire-shape book
// entries, hydrating titles/authors via MediaStore. Returns a non-nil
// slice (possibly empty) so collectionToABS emits the books key.
func (h *Handler) collectionBooks(r *http.Request, collectionID string) []map[string]any {
	if h.deps.CollectionStore == nil {
		return []map[string]any{}
	}
	rows, err := h.deps.CollectionStore.ListCollectionItems(r.Context(), collectionID)
	if err != nil {
		slog.Warn("abs collection list-items failed", "err", err, "collection", collectionID)
		return []map[string]any{}
	}
	lib := h.resolveDefaultLibrary(r.Context())
	libID := audiobookLibraryID(lib)
	out := make([]map[string]any, 0, len(rows))
	for _, it := range rows {
		entry := map[string]any{
			"id":        it.LibraryItemID,
			"libraryId": libID,
		}
		if item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), it.LibraryItemID); err == nil && item != nil {
			entry["media"] = map[string]any{
				"metadata": map[string]any{
					"title": item.Title,
				},
			}
		}
		out = append(out, entry)
	}
	return out
}

// handleListCollections — GET /collections.
// Returns the caller's collections wrapped in {"collections": [...]}.
// List-shape (no books[]).
func (h *Handler) handleListCollections(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.CollectionStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"collections": []any{}})
		return
	}
	rows, err := h.deps.CollectionStore.ListUserCollections(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		slog.Error("abs collection list failed", "err", err, "user", a.UserID)
		http.Error(w, "collection list failed", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		out = append(out, collectionToABS(c, nil)) // list-shape: nil books
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": out})
}

// chiURLID is a tiny shim around chi.URLParam(r, "id") so handler call
// sites read uniformly. Inlined where unambiguous.
func chiURLID(r *http.Request) string { return chi.URLParam(r, "id") }

// handleGetCollection — GET /collections/{id}.
// Owner gets full-shape; non-owner gets full-shape only when isPublic.
// Otherwise 404 (no existence leak — indistinguishable from real
// not-found).
func (h *Handler) handleGetCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.CollectionStore == nil {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	c, err := h.deps.CollectionStore.GetCollection(r.Context(), chiURLID(r))
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID && !c.IsPublic) {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs collection get failed", "err", err)
		http.Error(w, "collection get failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, h.collectionFullShape(r, c))
}

// handleUpdateCollection — PATCH /collections/{id}.
// Owner-only. Partial body: only fields explicitly present are
// modified. Non-owner gets 404 (no leak).
func (h *Handler) handleUpdateCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.CollectionStore == nil {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	c, err := h.deps.CollectionStore.GetCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID) {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs collection get-for-update failed", "err", err, "id", id)
		http.Error(w, "collection get failed", http.StatusInternalServerError)
		return
	}

	var body collectionBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name != nil {
		c.Name = *body.Name
	}
	if body.Description != nil {
		c.Description = *body.Description
	}
	if body.IsPublic != nil {
		c.IsPublic = *body.IsPublic
	}
	if err := h.deps.CollectionStore.UpdateCollection(r.Context(), c); err != nil {
		slog.Error("abs collection update failed", "err", err, "id", id)
		http.Error(w, "collection persist failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.CollectionStore.GetCollection(r.Context(), id)
	if err != nil {
		slog.Warn("abs collection get-after-update failed", "err", err, "id", id)
		persisted = c
	}
	writeJSON(w, http.StatusOK, h.collectionFullShape(r, persisted))
}

// handleAddCollectionBook — POST /collections/{id}/book/{bookId}.
// Owner-gated. Validates the item exists via MediaStore (returns 404
// for unknown items). Idempotent: re-adding is a silent no-op.
// Returns the parent collection's full-shape with updated books[].
func (h *Handler) handleAddCollectionBook(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.CollectionStore == nil {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	bookID := chi.URLParam(r, "bookId")
	if bookID == "" {
		http.Error(w, "bookId required", http.StatusBadRequest)
		return
	}

	c, err := h.deps.CollectionStore.GetCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID) {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs collection get-for-add failed", "err", err, "id", id)
		http.Error(w, "collection get failed", http.StatusInternalServerError)
		return
	}

	// Item validation — avoid orphan refs.
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), bookID)
	if err != nil || item == nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}

	if err := h.deps.CollectionStore.AddCollectionItem(r.Context(), id, bookID); err != nil {
		slog.Error("abs collection add-item failed", "err", err, "id", id, "book", bookID)
		http.Error(w, "collection persist failed", http.StatusInternalServerError)
		return
	}

	// Re-fetch to surface updated_at bump.
	persisted, err := h.deps.CollectionStore.GetCollection(r.Context(), id)
	if err != nil {
		persisted = c
	}
	writeJSON(w, http.StatusOK, h.collectionFullShape(r, persisted))
}

// handleRemoveCollectionBook — DELETE /collections/{id}/book/{bookId}.
// Owner-gated. Idempotent: removing a non-member is a no-op.
// Returns the parent collection's full-shape with updated books[].
func (h *Handler) handleRemoveCollectionBook(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.CollectionStore == nil {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	bookID := chi.URLParam(r, "bookId")
	if bookID == "" {
		http.Error(w, "bookId required", http.StatusBadRequest)
		return
	}

	c, err := h.deps.CollectionStore.GetCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID) {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs collection get-for-remove failed", "err", err, "id", id)
		http.Error(w, "collection get failed", http.StatusInternalServerError)
		return
	}

	if err := h.deps.CollectionStore.RemoveCollectionItem(r.Context(), id, bookID); err != nil {
		slog.Error("abs collection remove-item failed", "err", err, "id", id, "book", bookID)
		http.Error(w, "collection delete failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.CollectionStore.GetCollection(r.Context(), id)
	if err != nil {
		persisted = c
	}
	writeJSON(w, http.StatusOK, h.collectionFullShape(r, persisted))
}

// handleDeleteCollection — DELETE /collections/{id}.
// Owner-only. Cascade drops abs_collection_items via FK CASCADE.
// 204 on success; 404 for unknown or non-owned.
func (h *Handler) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.CollectionStore == nil {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	c, err := h.deps.CollectionStore.GetCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID) {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs collection get-for-delete failed", "err", err, "id", id)
		http.Error(w, "collection get failed", http.StatusInternalServerError)
		return
	}
	if err := h.deps.CollectionStore.DeleteCollection(r.Context(), id); err != nil {
		slog.Error("abs collection delete failed", "err", err, "id", id)
		http.Error(w, "collection delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

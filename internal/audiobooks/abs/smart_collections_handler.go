package abs

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/audiobooks/smartcoll"
)

// smartCollectionBody is the JSON body for POST and PATCH
// /me/smart-collections[/{id}]. Pointer fields support partial PATCH.
type smartCollectionBody struct {
	Name        *string                    `json:"name"`
	Description *string                    `json:"description"`
	Color       *string                    `json:"color"`
	IsPublic    *bool                      `json:"isPublic"`
	IsPinned    *bool                      `json:"isPinned"`
	QueryDef    *smartcoll.QueryDefinition `json:"query_def"`
}

// handleCreateSmartCollection — POST /me/smart-collections.
func (h *Handler) handleCreateSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		http.Error(w, "smart collection store unavailable", http.StatusServiceUnavailable)
		return
	}

	var body smartCollectionBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name == nil || *body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	c := SmartCollection{
		ID:        ulid.Make().String(),
		UserID:    a.UserID,
		ProfileID: a.ProfileID,
		Name:      *body.Name,
	}
	if body.Description != nil {
		c.Description = *body.Description
	}
	if body.Color != nil {
		c.Color = *body.Color
	}
	if body.IsPublic != nil {
		c.IsPublic = *body.IsPublic
	}
	if body.IsPinned != nil {
		c.IsPinned = *body.IsPinned
	}

	qd := smartcoll.QueryDefinition{}
	if body.QueryDef != nil {
		qd = *body.QueryDef
	}
	qd = qd.Normalize()
	if err := qd.Validate(true); err != nil {
		http.Error(w, "invalid query_def: "+err.Error(), http.StatusBadRequest)
		return
	}
	qdBytes, err := json.Marshal(qd)
	if err != nil {
		slog.Error("abs smart collection marshal query_def failed", "err", err)
		http.Error(w, "smart collection persist failed", http.StatusInternalServerError)
		return
	}
	c.QueryDef = qdBytes

	if err := h.deps.SmartCollectionStore.CreateSmartCollection(r.Context(), c); err != nil {
		slog.Error("abs smart collection create failed", "err", err, "user", a.UserID)
		http.Error(w, "smart collection persist failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), c.ID)
	if errors.Is(err, ErrNotFound) || err != nil {
		persisted = c
	}
	writeJSON(w, http.StatusOK, smartCollectionToABS(persisted))
}

func (h *Handler) handleListSmartCollections(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	rows, err := h.deps.SmartCollectionStore.ListUserSmartCollections(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		slog.Error("abs smart collection list failed", "err", err, "user", a.UserID)
		http.Error(w, "smart collection list failed", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		out = append(out, smartCollectionToABS(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) handleGetSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	c, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), chiURLID(r))
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID && !c.IsPublic) {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs smart collection get failed", "err", err)
		http.Error(w, "smart collection get failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, smartCollectionToABS(c))
}

func (h *Handler) handleUpdateSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	c, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID) {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs smart collection get-for-update failed", "err", err, "id", id)
		http.Error(w, "smart collection get failed", http.StatusInternalServerError)
		return
	}

	var body smartCollectionBody
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
	if body.Color != nil {
		c.Color = *body.Color
	}
	if body.IsPublic != nil {
		c.IsPublic = *body.IsPublic
	}
	if body.IsPinned != nil {
		c.IsPinned = *body.IsPinned
	}
	if body.QueryDef != nil {
		qd := body.QueryDef.Normalize()
		if err := qd.Validate(true); err != nil {
			http.Error(w, "invalid query_def: "+err.Error(), http.StatusBadRequest)
			return
		}
		qdBytes, mErr := json.Marshal(qd)
		if mErr != nil {
			slog.Error("abs smart collection marshal query_def failed", "err", mErr)
			http.Error(w, "smart collection persist failed", http.StatusInternalServerError)
			return
		}
		c.QueryDef = qdBytes
	}
	if err := h.deps.SmartCollectionStore.UpdateSmartCollection(r.Context(), c); err != nil {
		slog.Error("abs smart collection update failed", "err", err, "id", id)
		http.Error(w, "smart collection persist failed", http.StatusInternalServerError)
		return
	}
	persisted, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), id)
	if err != nil {
		persisted = c
	}
	writeJSON(w, http.StatusOK, smartCollectionToABS(persisted))
}

func (h *Handler) handleDeleteSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	c, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID) {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs smart collection get-for-delete failed", "err", err, "id", id)
		http.Error(w, "smart collection get failed", http.StatusInternalServerError)
		return
	}
	if err := h.deps.SmartCollectionStore.DeleteSmartCollection(r.Context(), id); err != nil {
		slog.Error("abs smart collection delete failed", "err", err, "id", id)
		http.Error(w, "smart collection delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

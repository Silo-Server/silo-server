package abs

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) handleRemoveFromContinueListening(w http.ResponseWriter, r *http.Request) {
	h.setHideFromContinue(w, r, true)
}

func (h *Handler) handleReaddToContinueListening(w http.ResponseWriter, r *http.Request) {
	h.setHideFromContinue(w, r, false)
}

func (h *Handler) setHideFromContinue(w http.ResponseWriter, r *http.Request, hide bool) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	itemID := chi.URLParam(r, "itemId")
	if itemID == "" {
		http.Error(w, "itemId required", http.StatusBadRequest)
		return
	}
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), itemID, access)
	if err != nil {
		slog.Error("abs continue item lookup failed", "err", err, "user", a.UserID, "item", itemID)
		http.Error(w, "item lookup failed", http.StatusInternalServerError)
		return
	}
	if item == nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}
	if h.deps.ProgressStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if err := h.deps.ProgressStore.SetHideFromContinue(r.Context(), a.UserID, a.ProfileID, itemID, hide); err != nil {
		slog.Error("abs continue toggle failed", "err", err, "user", a.UserID, "item", itemID, "hide", hide)
		http.Error(w, "continue toggle failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

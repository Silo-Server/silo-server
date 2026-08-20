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
	itemID := chi.URLParam(r, itemIDParam)
	if itemID == "" {
		http.Error(w, "itemId required", http.StatusBadRequest)
		return
	}
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		h.writeAccessResolutionError(w, r, err)
		return
	}
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), itemID, access)
	if err != nil {
		slog.ErrorContext(r.Context(), "abs continue item lookup failed", "component", "audiobooks", "err", err, "user", a.UserID, "item", itemID)
		http.Error(w, "item lookup failed", http.StatusInternalServerError)
		return
	}
	if item == nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}
	var toggleErr error
	if item.Type == mediaTypeEbook {
		if h.deps.EbookProgressStore == nil {
			http.Error(w, "ebook progress unavailable", http.StatusServiceUnavailable)
			return
		}
		toggleErr = h.deps.EbookProgressStore.SetEbookHidden(r.Context(), a.UserID, a.ProfileID, itemID, hide)
	} else if h.deps.ProgressStore == nil {
		http.Error(w, "progress unavailable", http.StatusServiceUnavailable)
		return
	} else {
		toggleErr = h.deps.ProgressStore.SetHideFromContinue(r.Context(), a.UserID, a.ProfileID, itemID, hide)
	}
	if toggleErr != nil {
		slog.ErrorContext(r.Context(), "abs continue toggle failed", "component", "audiobooks", "err", toggleErr, "user", a.UserID, "item", itemID, "hide", hide)
		http.Error(w, "continue toggle failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

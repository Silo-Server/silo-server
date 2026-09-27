package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// sectionPreviewFetcher is the minimal interface needed by HandlePreview.
// *sections.Fetcher satisfies this interface.
type sectionPreviewFetcher interface {
	FetchOne(ctx context.Context, resolved sections.ResolvedSection, libraryID *int, libraryIDs []int, userID int, profileID string, filter catalog.AccessFilter) (sections.SectionWithItems, error)
}

// previewRequest is the request body for POST /api/admin/sections/preview.
type previewRequest struct {
	SectionType string          `json:"section_type"`
	Config      json.RawMessage `json:"config"`
	ItemLimit   int             `json:"item_limit"`
	LibraryID   *int            `json:"library_id,omitempty"`
	LibraryIDs  []int           `json:"library_ids,omitempty"`
}

// previewResponse is the response body for POST /api/admin/sections/preview.
type previewResponse struct {
	Items      []*models.MediaItem `json:"items"`
	TotalCount int                 `json:"total_count"`
}

// HandlePreview is POST /api/admin/sections/preview. Returns a sample of items without persisting.
func (h *SectionHandler) HandlePreview(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}

	resp, err := h.previewAdminSection(r.Context(), req, requestAccessFilter(r))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

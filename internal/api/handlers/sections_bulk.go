package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/sections"
)

// BulkSectionRepo is the minimal repo surface needed for bulk-create.
// CreateMany must run all inserts in a single transaction.
type BulkSectionRepo interface {
	CreateMany(ctx context.Context, sections []*sections.PageSection) error
}

// SectionBulkHandler handles the bulk-create sections endpoint.
type SectionBulkHandler struct {
	Repo BulkSectionRepo
}

type bulkCreateSectionRequest struct {
	Scope       string          `json:"scope"`
	LibraryIDs  []int           `json:"library_ids"`
	SectionType string          `json:"section_type"`
	Title       string          `json:"title"`
	Featured    bool            `json:"featured"`
	ItemLimit   int             `json:"item_limit"`
	Config      json.RawMessage `json:"config"`
	Enabled     bool            `json:"enabled"`
}

type bulkCreateSectionResponse struct {
	Created int `json:"created"`
}

// HandleBulkCreate handles POST /api/admin/sections/bulk-create.
// It creates the same section across multiple libraries (library scope) or
// a single home-scope section, all within a single transaction.
func (h *SectionBulkHandler) HandleBulkCreate(w http.ResponseWriter, r *http.Request) {
	var req bulkCreateSectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	resp, err := h.BulkCreateAdminSections(r.Context(), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

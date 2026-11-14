package handlers

import (
	"context"
	"encoding/json"

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

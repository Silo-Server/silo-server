package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/sections"
)

// AdminCollectionSection is an admin Home or library page row that shows a
// server collection.
type AdminCollectionSection = sections.LibraryCollectionReference

// AdminCollectionRowCount counts the admin page rows that show one server
// collection.
type AdminCollectionRowCount = sections.LibraryCollectionReferenceCount

// AdminCollectionSections lists the admin page rows that show a server
// collection. Rows profiles added to their own Home are not stored in
// page_sections and are not listed.
func (h *LibraryCollectionHandler) AdminCollectionSections(ctx context.Context, id string) ([]AdminCollectionSection, error) {
	if h.SectionRepo == nil {
		return nil, apiError(http.StatusServiceUnavailable, "service_unavailable", "Section references are not configured")
	}
	if _, err := h.repo.CollectionRevision(ctx, id); err != nil {
		return nil, err
	}
	return h.SectionRepo.ListLibraryCollectionReferences(ctx, id)
}

// AdminCollectionRowCounts counts, in one query, the admin page rows that show
// each of the given server collections. It returns nil when sections are not
// configured, so callers leave the counts out rather than report zero.
func (h *LibraryCollectionHandler) AdminCollectionRowCounts(ctx context.Context, ids []string) (map[string]AdminCollectionRowCount, error) {
	if h.SectionRepo == nil {
		return nil, nil
	}
	return h.SectionRepo.CountLibraryCollectionReferencesByID(ctx, ids)
}

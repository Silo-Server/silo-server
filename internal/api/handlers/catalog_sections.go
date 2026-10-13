package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// ResolveCatalogSection gives Explore all the same row the acting profile sees
// on Home or its library page. Missing rows must not fall back to the admin row:
// the profile may have hidden or removed it.
func (h *SectionHandler) ResolveCatalogSection(ctx context.Context, req catalog.CatalogRequest) (catalog.SectionDefinition, error) {
	var rows []sections.ResolvedSection
	var err error
	switch req.Scope {
	case adminSectionScopeHome:
		rows, _, _, _, err = h.loadResolvedHomeSections(ctx)
	case adminSectionScopeLibrary:
		if err := h.requireViewableLibrary(ctx, req.LibraryID); err != nil {
			if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.Status == http.StatusNotFound {
				return catalog.SectionDefinition{}, catalog.ErrCatalogSourceNotFound
			}
			return catalog.SectionDefinition{}, err
		}
		rows, _, _, err = h.loadResolvedLibrarySections(ctx, req.LibraryID)
	default:
		return catalog.SectionDefinition{}, catalog.ErrInvalidCatalogRequest
	}
	if err != nil {
		return catalog.SectionDefinition{}, err
	}
	for _, row := range rows {
		if row.ID == req.SectionID {
			return catalog.SectionDefinition{SectionType: string(row.SectionType), Title: row.Title,
				ItemLimit: row.ItemLimit, Config: row.Config}, nil
		}
	}
	return catalog.SectionDefinition{}, catalog.ErrCatalogSourceNotFound
}

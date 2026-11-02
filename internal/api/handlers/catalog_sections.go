package handlers

import (
	"context"
	"errors"
	"net/http"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// catalogSectionRow is a row Explore all reads, with the scope its page
// fetches it in.
type catalogSectionRow struct {
	section    sections.ResolvedSection
	libraryID  *int
	libraryIDs []int
	filter     catalog.AccessFilter
	profileID  string
}

// ResolveCatalogSection gives Explore all the same row the acting profile sees
// on Home or its library page. Missing rows must not fall back to the admin row:
// the profile may have hidden or removed it.
func (h *SectionHandler) ResolveCatalogSection(ctx context.Context, req catalog.CatalogRequest) (catalog.SectionDefinition, error) {
	row, err := h.findCatalogSectionRow(ctx, req)
	if err != nil {
		return catalog.SectionDefinition{}, err
	}
	return catalog.SectionDefinition{SectionType: string(row.section.SectionType), Title: row.section.Title,
		ItemLimit: row.section.ItemLimit, Config: row.section.Config,
		FetchItems: func(ctx context.Context, limit int) ([]*models.MediaItem, error) {
			return h.fetchCatalogSectionItems(ctx, row, limit)
		}}, nil
}

// fetchCatalogSectionItems lists a row the catalog cannot express as a query:
// the items the row shows, in its order, then the rest of a fetch of up to
// limit items. Rows that pick a daily subset of a pool sized by their limit
// (hidden gems, forgotten favorites, ...) fetch a different selection at a
// larger limit, so leading with the row keeps what the profile just saw.
func (h *SectionHandler) fetchCatalogSectionItems(ctx context.Context, row catalogSectionRow, limit int) ([]*models.MediaItem, error) {
	// Library pages never hide watched items; Home does per profile.
	hideWatched := row.libraryID == nil && h.homeHidesWatchedItems(ctx)
	shown, err := h.fetchCatalogSectionRow(ctx, row, row.section, hideWatched)
	if err != nil {
		return nil, err
	}
	rowLimit := effectiveHomeItemLimit(row.section.ItemLimit)
	// Fetchers return a short row only when nothing more matches, unless the
	// watched filter shortened it. Rows built per profile on every request
	// (returning shows, Continue Watching) are too costly to build twice.
	if rowLimit >= limit || (!hideWatched && len(shown) < rowLimit) {
		return mergeSectionBrowseItems(shown, nil, limit), nil
	}
	wide := row.section
	wide.ItemLimit = limit
	more, err := h.fetchCatalogSectionRow(ctx, row, wide, hideWatched)
	if err != nil {
		return nil, err
	}
	return mergeSectionBrowseItems(shown, more, limit), nil
}

func (h *SectionHandler) findCatalogSectionRow(ctx context.Context, req catalog.CatalogRequest) (catalogSectionRow, error) {
	switch req.Scope {
	case adminSectionScopeHome:
		rows, libraryIDs, filter, profileID, err := h.loadResolvedHomeSections(ctx)
		if err != nil {
			return catalogSectionRow{}, err
		}
		// A profile in separate Next Up mode gets its Next Up row injected,
		// as Home does when it renders the row.
		rows = h.maybeInjectNextUp(ctx, rows, apimw.GetUserID(ctx))
		for _, s := range rows {
			if s.ID == req.SectionID {
				return catalogSectionRow{section: s, libraryIDs: libraryIDs, filter: filter, profileID: profileID}, nil
			}
		}
	case adminSectionScopeLibrary:
		if err := h.requireViewableLibrary(ctx, req.LibraryID); err != nil {
			if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.Status == http.StatusNotFound {
				return catalogSectionRow{}, catalog.ErrCatalogSourceNotFound
			}
			return catalogSectionRow{}, err
		}
		rows, filter, profileID, err := h.loadResolvedLibrarySections(ctx, req.LibraryID)
		if err != nil {
			return catalogSectionRow{}, err
		}
		for _, s := range rows {
			if s.ID == req.SectionID {
				return catalogSectionRow{section: s, libraryID: new(req.LibraryID), filter: filter, profileID: profileID}, nil
			}
		}
	default:
		return catalogSectionRow{}, catalog.ErrInvalidCatalogRequest
	}
	return catalogSectionRow{}, catalog.ErrCatalogSourceNotFound
}

// fetchCatalogSectionRow fetches s in row's scope the way its page does,
// including Home's hide-watched filter. A deleted collection lists nothing.
func (h *SectionHandler) fetchCatalogSectionRow(ctx context.Context, row catalogSectionRow, s sections.ResolvedSection, hideWatched bool) ([]*models.MediaItem, error) {
	fetch := s
	if hideWatched {
		fetch = homeSectionsForFetch([]sections.ResolvedSection{s}, true)[0]
	}
	withItems, err := h.fetcher.FetchOne(ctx, fetch, row.libraryID, row.libraryIDs, apimw.GetUserID(ctx), row.profileID, row.filter)
	if err != nil {
		if sections.IsMissingCollection(err) {
			return nil, nil
		}
		sections.LogFetchError(ctx, "api", s, err)
		return nil, err
	}
	if !hideWatched {
		return withItems.Items, nil
	}
	filtered, _ := h.filterWatchedHomeSections(ctx, []sections.SectionWithItems{withItems}, []sections.ResolvedSection{s})
	return filtered[0].Items, nil
}

// mergeSectionBrowseItems lists shown, then the items of more it lacks, up to
// limit.
func mergeSectionBrowseItems(shown, more []*models.MediaItem, limit int) []*models.MediaItem {
	out := make([]*models.MediaItem, 0, min(limit, len(shown)+len(more)))
	seen := make(map[string]struct{}, cap(out))
	for _, list := range [][]*models.MediaItem{shown, more} {
		for _, item := range list {
			if len(out) == limit {
				return out
			}
			if item == nil {
				continue
			}
			if _, dup := seen[item.ContentID]; dup {
				continue
			}
			seen[item.ContentID] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}

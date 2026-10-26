package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type catalogIdentityItems interface {
	ResolveIdentities(context.Context, catalog.IdentityRequest, catalog.AccessFilter) (catalog.IdentityResult, error)
	GetByIDsWithAccess(context.Context, []string, catalog.AccessFilter) ([]*models.MediaItem, error)
	GetIdentityProviderIDs(context.Context, []string) (map[string][]*models.MediaItemProviderID, error)
}
type CatalogIdentityMatchView struct {
	Seed int
	Item CollectionItemView
}
type CatalogIdentityView struct {
	Matches []CatalogIdentityMatchView
	HasMore bool
	Next    catalog.IdentityPosition
}

func (h *CatalogHandler) ResolveIdentities(ctx context.Context, v ItemViewer, req catalog.IdentityRequest) (CatalogIdentityView, error) {
	out := CatalogIdentityView{Matches: []CatalogIdentityMatchView{}}
	if h == nil || h.itemsH == nil {
		return out, apiError(http.StatusInternalServerError, "internal_error", "Catalog is not configured")
	}
	repo := h.identityItems
	if repo == nil {
		if h.itemsH.itemRepo == nil {
			return out, apiError(http.StatusInternalServerError, "internal_error", "Catalog is not configured")
		}
		repo = h.itemsH.itemRepo
	}
	result, err := repo.ResolveIdentities(ctx, req, v.Access)
	if err != nil {
		return out, err
	}
	ids := make([]string, 0, len(result.Matches))
	seen := make(map[string]bool, len(result.Matches))
	for _, m := range result.Matches {
		if !seen[m.ContentID] {
			ids = append(ids, m.ContentID)
			seen[m.ContentID] = true
		}
	}
	items, err := repo.GetByIDsWithAccess(ctx, ids, v.Access)
	if err != nil {
		return out, err
	}

	providers, err := repo.GetIdentityProviderIDs(ctx, ids)
	if err != nil {
		return out, err
	}
	byItemID := make(map[string]*models.MediaItem, len(items))
	for _, item := range items {
		if item != nil {
			byItemID[item.ContentID] = item
		}
	}
	confirmed := make([]catalog.IdentityMatch, 0, len(result.Matches))
	safeItems := make([]*models.MediaItem, 0, len(items))
	included := make(map[string]bool, len(items))
	for _, match := range result.Matches {
		if match.Seed < 0 || match.Seed >= len(req.Seeds) {
			continue
		}
		item := byItemID[match.ContentID]
		if !req.Seeds[match.Seed].MatchesItem(item, providers[match.ContentID]) {
			continue
		}
		confirmed = append(confirmed, match)
		if !included[match.ContentID] {
			safeItems = append(safeItems, item)
			included[match.ContentID] = true
		}
	}
	cards := h.catalogItemResponses(ctx, v, safeItems, "", nil, v.Access)

	byID := make(map[string]CollectionItemView, len(cards))
	for _, card := range cards {
		byID[card.ContentID] = card
	}
	for _, match := range confirmed {
		if card, ok := byID[match.ContentID]; ok {
			out.Matches = append(out.Matches, CatalogIdentityMatchView{Seed: match.Seed, Item: card})
		}
	}
	out.HasMore, out.Next = result.HasMore, result.Next
	return out, nil
}

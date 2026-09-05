package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sort"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

// Home viewer seams: the profile-scoped /home reads and the recipe gallery
// the v1 handlers and the v2 operations share. Each returns the view the v1
// handler writes verbatim and, on failure, an *APIError.

// HomeSections answers every section of the home page with its items for
// the profile on the context.
func (h *SectionHandler) HomeSections(ctx context.Context, viewer SectionViewer) (SectionsView, error) {
	resolved, libraryIDs, accessFilter, profileID, err := h.loadResolvedHomeSections(ctx)
	if err != nil {
		return SectionsView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load sections")
	}
	userID := apimw.GetUserID(ctx)
	resolved = h.maybeInjectNextUp(ctx, resolved, userID)
	withItems := h.fetcher.FetchAll(ctx, resolved, nil, libraryIDs, userID, profileID, accessFilter)
	withItems = applyDiversityFilter(withItems)
	withItems = dropEmptySeasonalSections(withItems)
	return h.buildSections(ctx, withItems, nil, viewer.Access, viewer.ImageSize), nil
}

// HomeSectionItems answers one section of the home page with its items.
func (h *SectionHandler) HomeSectionItems(ctx context.Context, sectionID string, viewer SectionViewer) (SectionView, error) {
	resolved, libraryIDs, accessFilter, profileID, err := h.loadResolvedHomeSections(ctx)
	if err != nil {
		return SectionView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load sections")
	}
	userID := apimw.GetUserID(ctx)
	resolved = h.maybeInjectNextUp(ctx, resolved, userID)
	for _, s := range resolved {
		if s.ID != sectionID {
			continue
		}
		withItems, fetchErr := h.fetcher.FetchOne(ctx, s, nil, libraryIDs, userID, profileID, accessFilter)
		if fetchErr != nil {
			slog.ErrorContext(ctx, "fetching section items", "component", "api", "section_id", s.ID, "type", s.SectionType, "error", fetchErr)
			withItems = sections.SectionWithItems{
				ResolvedSection: s,
				Items:           []*models.MediaItem{},
			}
		}
		resp := h.buildSections(ctx, []sections.SectionWithItems{withItems}, nil, viewer.Access, viewer.ImageSize)
		if len(resp.Sections) == 0 {
			return resolvedSectionResponse{
				ID:          withItems.ID,
				SectionType: string(withItems.SectionType),
				Title:       withItems.Title,
				Featured:    withItems.Featured,
				ItemLimit:   withItems.ItemLimit,
				TotalCount:  withItems.TotalCount,
				IsCustom:    withItems.IsCustom,
				Customized:  withItems.Customized,
				Items:       []sectionItemResponse{},
			}, nil
		}
		return resp.Sections[0], nil
	}
	return SectionView{}, apiError(http.StatusNotFound, "not_found", "Section not found")
}

// RecipeCategoryView is one gallery category with its recipes.
type RecipeCategoryView struct {
	Category string
	Recipes  []recipes.RecipeDefinition
}

// Recipes answers the gallery: every visible recipe definition grouped by
// category, categories in key order, Presets never nil.
func (h *RecipeHandler) Recipes() []RecipeCategoryView {
	groups := map[string][]recipes.RecipeDefinition{}
	for _, rec := range recipes.List() {
		def := rec.Definition()
		if def.Hidden {
			continue
		}
		// Guarantee Presets serializes as `[]` rather than `null` so the UI can
		// iterate without a guard. Recipes that take no preset (e.g. custom_filter)
		// still need a present-but-empty array.
		if def.Presets == nil {
			def.Presets = []recipes.GalleryPreset{}
		}
		key := string(def.Category)
		groups[key] = append(groups[key], def)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]RecipeCategoryView, 0, len(keys))
	for _, k := range keys {
		out = append(out, RecipeCategoryView{Category: k, Recipes: groups[k]})
	}
	return out
}

// RecipeCandidates answers the UI-pickable candidates of a parameterized
// recipe type; a type with no candidate source is 404.
func (h *RecipeHandler) RecipeCandidates(ctx context.Context, recipeType string) ([]Candidate, error) {
	src, ok := candidateSources[recipeType]
	if !ok {
		return nil, apiError(http.StatusNotFound, "unknown_recipe", "no candidate source for this recipe type")
	}
	cands, err := src(ctx)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "candidate_error", err.Error())
	}
	return cands, nil
}

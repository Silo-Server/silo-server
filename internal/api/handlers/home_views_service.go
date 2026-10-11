package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Home viewer seams: the profile-scoped /home reads and the recipe gallery
// the v1 handlers and the v2 operations share. Each returns the view the v1
// handler writes verbatim and, on failure, an *APIError.

// HomeSections answers every section of the home page with its items for
// the profile on the context.
func (h *SectionHandler) HomeSections(ctx context.Context, viewer SectionViewer) (SectionsView, error) {
	start := time.Now()
	resolved, libraryIDs, accessFilter, profileID, err := h.loadResolvedHomeSections(ctx)
	if err != nil {
		return SectionsView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load sections")
	}
	resolvedAt := time.Now()
	userID := apimw.GetUserID(ctx)
	resolved = h.maybeInjectNextUp(ctx, resolved, userID)
	nextUpAt := time.Now()
	hideWatched := h.homeHidesWatchedItems(ctx)
	withItems := h.fetcher.FetchAll(ctx, homeSectionsForFetch(resolved, hideWatched), nil, libraryIDs, userID, profileID, accessFilter)
	fetchedAt := time.Now()
	var userStates map[string]*itemUserStateResponse
	if hideWatched {
		// Filter before the cross-section diversity pass so a watched item
		// cut here can't suppress the same title in a later section.
		withItems, userStates = h.filterWatchedHomeSections(ctx, withItems, resolved)
		withItems = dropEmptyWatchedHomeSections(withItems)
	}
	withItems = applyDiversityFilter(withItems)
	withItems = dropEmptySeasonalSections(withItems)
	response := h.buildSectionsWithUserStates(ctx, withItems, nil, viewer.Access, viewer.ImageSize, userStates)
	itemCount := 0
	for _, section := range withItems {
		itemCount += len(section.Items)
	}
	elapsed := time.Since(start)
	level := slog.LevelDebug
	if elapsed >= 500*time.Millisecond {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "home sections timing",
		"total_ms", elapsed.Milliseconds(),
		"resolve_ms", resolvedAt.Sub(start).Milliseconds(),
		"next_up_ms", nextUpAt.Sub(resolvedAt).Milliseconds(),
		"fetch_all_ms", fetchedAt.Sub(nextUpAt).Milliseconds(),
		"build_response_ms", time.Since(fetchedAt).Milliseconds(),
		"section_count", len(withItems), "item_count", itemCount,
	)
	return response, nil
}

// HomeSectionItems answers one section of the home page with its items.
func (h *SectionHandler) HomeSectionItems(ctx context.Context, sectionID string, viewer SectionViewer) (SectionView, error) {
	resolved, libraryIDs, accessFilter, profileID, err := h.loadResolvedHomeSections(ctx)
	if err != nil {
		return SectionView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load sections")
	}
	userID := apimw.GetUserID(ctx)
	resolved = h.maybeInjectNextUp(ctx, resolved, userID)
	hideWatched := h.homeHidesWatchedItems(ctx)
	for _, s := range resolved {
		if s.ID != sectionID {
			continue
		}
		withItems, fetchErr := h.fetcher.FetchOne(ctx, homeSectionsForFetch([]sections.ResolvedSection{s}, hideWatched)[0], nil, libraryIDs, userID, profileID, accessFilter)
		if fetchErr != nil {
			sections.LogFetchError(ctx, "api", s, fetchErr)
			withItems = sections.SectionWithItems{
				ResolvedSection: s,
				Items:           []*models.MediaItem{},
			}
		}
		withItems.ItemLimit = s.ItemLimit
		rows := h.buildHomeSectionRows(ctx, []sections.SectionWithItems{withItems}, []sections.ResolvedSection{s}, hideWatched, viewer)
		if len(rows) == 0 {
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
		return rows[0], nil
	}
	return SectionView{}, apiError(http.StatusNotFound, "not_found", "Section not found")
}

// buildHomeSectionRows turns fetched Home rows into the cards
// HomeSectionItems answers: the hide-watched filter, then user states,
// overlays, playable targets and artwork, batched across the rows. Each row
// comes out as it would on its own; the cross-row diversity filter does not
// run, and a row the filter empties is kept. resolved holds the rows'
// unwidened sections, so each is cut back to its display limit.
func (h *SectionHandler) buildHomeSectionRows(ctx context.Context, rows []sections.SectionWithItems, resolved []sections.ResolvedSection, hideWatched bool, viewer SectionViewer) []SectionView {
	rows = restoreHomeSectionDisplayLimits(rows, resolved)
	var userStates map[string]*itemUserStateResponse
	if hideWatched {
		// The direct section endpoint returns the section even if the
		// filter empties it; only aggregate Home drops such sections.
		rows, userStates = h.filterWatchedHomeSections(ctx, rows, resolved)
	}
	return h.buildSectionsWithUserStates(ctx, rows, nil, viewer.Access, viewer.ImageSize, userStates).Sections
}

// MaxHomeLayoutIncludedSections is the most rows HomeLayoutWithItems
// carries items for. A Home page shows about this many rows above the fold;
// the rest load from the per-row endpoint as the viewer scrolls.
const MaxHomeLayoutIncludedSections = 4

// homeLayoutIncludedRowsBudget bounds how long the home layout waits for the
// rows it was asked to carry, counted from the start of their item fetches.
// The layout gates every Home render, so one slow row must not delay it: a
// row that is not fetched in time is left out, and the client loads it from
// the per-row endpoint instead. A warm resolved-list cache answers well
// inside this budget; a cold rebuild that misses it keeps running detached
// (see the resolved-list cache), so the follow-up per-row request usually
// finds it ready.
const homeLayoutIncludedRowsBudget = 300 * time.Millisecond

// HomeLayoutView is the home layout plus the cards of its first rows.
type HomeLayoutView struct {
	Layout SectionLayoutView
	// IncludedSections are the first rows of the layout, in display order,
	// each as HomeSectionItems answers it. A row that failed or missed the
	// budget is absent. Never nil.
	IncludedSections []SectionView
}

// HomeLayoutWithItems answers the home layout for the profile on the
// context and, when includeItems is positive, the cards of its first
// includeItems rows (at most MaxHomeLayoutIncludedSections). The layout is
// resolved once and the rows are fetched in parallel; rows that error or
// take longer than the budget are omitted rather than failing the layout.
func (h *SectionHandler) HomeLayoutWithItems(ctx context.Context, viewer SectionViewer, includeItems int) (HomeLayoutView, error) {
	return h.homeLayoutWithItems(ctx, viewer, includeItems, homeLayoutIncludedRowsBudget)
}

func (h *SectionHandler) homeLayoutWithItems(ctx context.Context, viewer SectionViewer, includeItems int, budget time.Duration) (HomeLayoutView, error) {
	resolved, libraryIDs, accessFilter, profileID, err := h.loadResolvedHomeSections(ctx)
	if err != nil {
		return HomeLayoutView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load sections")
	}
	userID := apimw.GetUserID(ctx)
	resolved = h.maybeInjectNextUp(ctx, resolved, userID)
	view := HomeLayoutView{Layout: sectionLayoutOf(resolved), IncludedSections: []SectionView{}}
	n := min(includeItems, MaxHomeLayoutIncludedSections, len(resolved))
	if n <= 0 || h.fetcher == nil {
		return view, nil
	}
	included := resolved[:n]
	hideWatched := h.homeHidesWatchedItems(ctx)
	rows := fetchHomeRowsWithin(ctx, homeSectionsForFetch(included, hideWatched), budget,
		func(ctx context.Context, s sections.ResolvedSection) (sections.SectionWithItems, error) {
			return h.fetcher.FetchOne(ctx, s, nil, libraryIDs, userID, profileID, accessFilter)
		})
	if len(rows) == 0 {
		return view, nil
	}
	view.IncludedSections = h.buildHomeSectionRows(ctx, rows, included, hideWatched, viewer)
	return view, nil
}

// homeRowFetch fetches one Home row's items.
type homeRowFetch func(context.Context, sections.ResolvedSection) (sections.SectionWithItems, error)

// fetchHomeRowsWithin fetches every section in parallel and returns, in
// section order, the rows that finished without error before budget
// elapsed (or ctx ended). Late fetches are canceled when it returns.
func fetchHomeRowsWithin(ctx context.Context, resolved []sections.ResolvedSection, budget time.Duration, fetch homeRowFetch) []sections.SectionWithItems {
	type result struct {
		row sections.SectionWithItems
		err error
	}
	fetchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]chan result, len(resolved))
	for i, s := range resolved {
		ch := make(chan result, 1)
		results[i] = ch
		go func() {
			row, err := fetch(fetchCtx, s)
			ch <- result{row: row, err: err}
		}()
	}

	// Only the wait carries the deadline; the fetches see plain cancellation.
	waitCtx, stopWaiting := context.WithTimeout(ctx, budget)
	defer stopWaiting()
	out := make([]sections.SectionWithItems, 0, len(resolved))
	for i, ch := range results {
		var r result
		select {
		case r = <-ch:
		case <-waitCtx.Done():
			// Out of time: still take a row that is already done.
			select {
			case r = <-ch:
			default:
				slog.DebugContext(ctx, "home layout row missed its budget", "component", "api", "section_id", resolved[i].ID, "budget_ms", budget.Milliseconds())
				continue
			}
		}
		if r.err != nil {
			logIncludedHomeRowError(ctx, resolved[i], r.err)
			continue
		}
		out = append(out, r.row)
	}
	return out
}

// logIncludedHomeRowError records a row left out of the home layout because
// its fetch failed. As in sections.LogFetchError, a row whose collection was
// deleted is expected and logs at debug.
func logIncludedHomeRowError(ctx context.Context, s sections.ResolvedSection, err error) {
	level := slog.LevelWarn
	if errors.Is(err, catalog.ErrLibraryCollectionNotFound) || errors.Is(err, userstore.ErrCollectionNotFound) {
		level = slog.LevelDebug
	}
	slog.Log(ctx, level, "home layout row left out after a fetch error", "component", "api", "section_id", s.ID, "type", s.SectionType, "error", err)
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

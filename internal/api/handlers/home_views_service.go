package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sort"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/logredact"
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
	response := h.buildSectionsWithUserStates(ctx, withItems, nil, viewer.Access, viewer.Artwork(), userStates)
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

// HomeSectionItems answers one section of the home page with its items, or an
// error when its items cannot be fetched.
func (h *SectionHandler) HomeSectionItems(ctx context.Context, sectionID string, viewer SectionViewer) (SectionView, error) {
	return h.homeSectionItems(ctx, sectionID, viewer, false)
}

// homeSectionItems is HomeSectionItems. With emptyOnFetchError, a section
// whose items cannot be fetched answers with no items, as the frozen v1
// endpoint always has.
func (h *SectionHandler) homeSectionItems(ctx context.Context, sectionID string, viewer SectionViewer, emptyOnFetchError bool) (SectionView, error) {
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
			// Unlike the aggregate read, which keeps the page up with an
			// empty row, a failed single section answers an error, so a
			// client can tell it from a section with nothing to show. A
			// deleted collection is not a failure and keeps its empty row.
			if !emptyOnFetchError && !sections.IsMissingCollection(fetchErr) {
				return SectionView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load section")
			}
			withItems = sections.SectionWithItems{ResolvedSection: s, Items: []*models.MediaItem{}}
		}
		return h.homeSectionRow(ctx, s, withItems, hideWatched, viewer), nil
	}
	return SectionView{}, apiError(http.StatusNotFound, "not_found", "Section not found")
}

// homeSectionRow turns one fetched Home row into the cards HomeSectionItems
// answers: the hide-watched filter, then user states, overlays, playable
// targets and artwork. The cross-row diversity filter does not run, and a row
// the filter empties is kept. s is the row's unwidened section, so the row is
// cut back to its display limit.
func (h *SectionHandler) homeSectionRow(ctx context.Context, s sections.ResolvedSection, withItems sections.SectionWithItems, hideWatched bool, viewer SectionViewer) SectionView {
	withItems.ItemLimit = s.ItemLimit
	rows := []sections.SectionWithItems{withItems}
	var userStates map[string]*itemUserStateResponse
	if hideWatched {
		// The direct section endpoint returns the section even if the
		// filter empties it; only aggregate Home drops such sections.
		rows, userStates = h.filterWatchedHomeSections(ctx, rows, []sections.ResolvedSection{s})
	}
	built := h.buildSectionsWithUserStates(ctx, rows, nil, viewer.Access, viewer.Artwork(), userStates).Sections
	if len(built) == 0 {
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
		}
	}
	return built[0]
}

// MaxHomeLayoutIncludedSections is the most rows HomeLayoutWithItems
// carries items for. A Home page shows about this many rows above the fold;
// the rest load from the per-row endpoint as the viewer scrolls.
const MaxHomeLayoutIncludedSections = 4

// homeLayoutIncludedRowsBudget bounds how long the home layout waits for the
// rows it was asked to carry, counted from the start of their item fetches
// and covering each row's whole build: the fetch and the card lookups. The
// layout gates every Home render, so one slow row must not delay it: a row
// that is not built in time is left out, and the client loads it from the
// per-row endpoint instead. A warm resolved-list cache answers well inside
// this budget; a cold rebuild that misses it keeps running detached (see the
// resolved-list cache), so the follow-up per-row request usually finds it
// ready.
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
// resolved once and the rows are built in parallel; rows that error or
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
	view.IncludedSections = h.includedHomeRows(ctx, resolved[:n], h.homeHidesWatchedItems(ctx), viewer, budget,
		func(ctx context.Context, s sections.ResolvedSection) (sections.SectionWithItems, error) {
			return h.fetcher.FetchOne(ctx, s, nil, libraryIDs, userID, profileID, accessFilter)
		})
	return view, nil
}

// homeRowFetch fetches one Home row's items.
type homeRowFetch func(context.Context, sections.ResolvedSection) (sections.SectionWithItems, error)

// includedHomeRows builds every section's row in parallel, each fetched and
// then turned into cards as HomeSectionItems does, and returns, in section
// order, the rows built without error before budget elapsed (or ctx ended).
// It never waits past the budget: rows still building are canceled when it
// returns, and each writes only to its own buffered channel, which nothing
// reads after that.
func (h *SectionHandler) includedHomeRows(ctx context.Context, included []sections.ResolvedSection, hideWatched bool, viewer SectionViewer, budget time.Duration, fetch homeRowFetch) []SectionView {
	type result struct {
		row SectionView
		err error
	}
	buildCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]chan result, len(included))
	for i, s := range included {
		ch := make(chan result, 1)
		results[i] = ch
		go func() {
			row, err := h.includedHomeRow(buildCtx, s, hideWatched, viewer, fetch)
			ch <- result{row: row, err: err}
		}()
	}

	// Only the wait carries the deadline; the builds see plain cancellation.
	waitCtx, stopWaiting := context.WithTimeout(ctx, budget)
	defer stopWaiting()
	out := make([]SectionView, 0, len(included))
	for i, ch := range results {
		var r result
		select {
		case r = <-ch:
		case <-waitCtx.Done():
			// Out of time: still take a row that is already built.
			select {
			case r = <-ch:
			default:
				slog.DebugContext(ctx, "home layout row missed its budget", "component", "api", "section_id", included[i].ID, "budget_ms", budget.Milliseconds())
				continue
			}
		}
		if r.err != nil {
			logIncludedHomeRowError(ctx, included[i], r.err)
			continue
		}
		out = append(out, r.row)
	}
	return out
}

// includedHomeRow fetches and builds one row for includedHomeRows. It runs
// on its own goroutine, out of reach of the request's panic recovery, so a
// panic becomes the row's error.
func (h *SectionHandler) includedHomeRow(ctx context.Context, s sections.ResolvedSection, hideWatched bool, viewer SectionViewer, fetch homeRowFetch) (row SectionView, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.ErrorContext(ctx, "home layout row build panicked", "component", "api", "section_id", s.ID,
				"panic", logredact.SanitizeText(fmt.Sprint(rec)), "stack", string(debug.Stack()))
			row, err = SectionView{}, errors.New("home layout row build panicked")
		}
	}()
	withItems, err := fetch(ctx, homeSectionsForFetch([]sections.ResolvedSection{s}, hideWatched)[0])
	if err != nil {
		return SectionView{}, err
	}
	row = h.homeSectionRow(ctx, s, withItems, hideWatched, viewer)
	// The card lookups log and skip a canceled call rather than failing, so
	// a row built after its context ended may be missing state or artwork.
	if err := ctx.Err(); err != nil {
		return SectionView{}, err
	}
	return row, nil
}

// logIncludedHomeRowError records a row left out of the home layout because
// its build failed. As in sections.LogFetchError, a row whose collection was
// deleted is expected and logs at debug, as does a row the request canceled.
func logIncludedHomeRowError(ctx context.Context, s sections.ResolvedSection, err error) {
	level := slog.LevelWarn
	if errors.Is(err, catalog.ErrLibraryCollectionNotFound) || errors.Is(err, userstore.ErrCollectionNotFound) || errors.Is(err, context.Canceled) {
		level = slog.LevelDebug
	}
	slog.Log(ctx, level, "home layout row left out after a build error", "component", "api", "section_id", s.ID, "type", s.SectionType, "error", logredact.SanitizeText(err.Error()))
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

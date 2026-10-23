package handlers

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sort"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	// boxSetSearchCandidates is how many matching collections one search
	// reads at a time, and boxSetSearchPages how many reads it makes at most.
	boxSetSearchCandidates = 50
	boxSetSearchPages      = 4
)

// BoxSetView is a box set as one viewer sees it in one library.
type BoxSetView struct {
	CollectionID      int64
	LibraryID         int
	Title             string
	Overview          string
	PosterURL         string
	PosterThumbhash   string
	BackdropURL       string
	BackdropThumbhash string
	MemberCount       int
	PlayedCount       int
	FirstYear         int
	LastYear          int
}

// BoxSetDetailView is a box set with its movies in collection order.
type BoxSetDetailView struct {
	BoxSetView
	Items []CollectionItemView
}

// SetBoxSetRepository enables box sets on the catalog reads.
func (h *CatalogHandler) SetBoxSetRepository(repo *catalog.BoxSetRepository) {
	h.boxSets = repo
}

// prepareBoxSetBrowse turns a group=box_set browse of one library with box
// sets on into a grouped browse of its box sets. It returns the box sets
// the page may show, keyed by member content ID; nil means the browse is
// not grouped into box sets (no single library, box sets off, a search, or
// no box set the viewer can see).
func (h *CatalogHandler) prepareBoxSetBrowse(ctx context.Context, req *catalog.CatalogRequest, access catalog.AccessFilter) (map[string]catalog.BoxSetGroup, error) {
	if !req.GroupBoxSets || h.boxSets == nil || req.Source != catalog.CatalogSourceQuery || req.SearchQuery != "" {
		return nil, nil
	}
	libraryIDs := playableTargetLibraryIDs(*req)
	if len(libraryIDs) != 1 {
		return nil, nil
	}
	minMovies, ok, err := h.boxSets.LibraryBoxSetMinMovies(ctx, libraryIDs[0])
	if err != nil || !ok {
		return nil, err
	}
	groups, err := h.resolver.BoxSetGroups(ctx, libraryIDs[0], minMovies, access, nil)
	if err != nil || len(groups) == 0 {
		return nil, err
	}
	byMember := make(map[string]catalog.BoxSetGroup)
	collections := make([]int64, 0, len(groups))
	for _, g := range groups {
		collections = append(collections, g.CollectionID)
		for _, contentID := range g.MemberContentIDs {
			byMember[contentID] = g
		}
	}
	req.BoxSetCollections = collections
	req.GroupByWork = true
	return byMember, nil
}

// applyBoxSets replaces each card that stands for a box set with the box
// set. The grouped query keeps one movie per box set, so any member on the
// page is that box set's representative.
func (h *CatalogHandler) applyBoxSets(ctx context.Context, v ItemViewer, libraryID int, items []itemListResponse, byMember map[string]catalog.BoxSetGroup) {
	var groups []catalog.BoxSetGroup
	for _, item := range items {
		if g, ok := byMember[item.ContentID]; ok {
			groups = append(groups, g)
		}
	}
	if len(groups) == 0 {
		return
	}
	views, err := h.boxSetViews(ctx, v, libraryID, groups, false)
	if err != nil {
		slog.WarnContext(ctx, "catalog: box set cards unavailable", "component", "api", "error", err)
		return
	}
	for i := range items {
		g, ok := byMember[items[i].ContentID]
		if !ok {
			continue
		}
		if view, ok := views[g.CollectionID]; ok {
			items[i] = boxSetCatalogItem(items[i], view)
		}
	}
}

// boxSetViews builds the views of groups, which belong to one library,
// keyed by collection ID. A group whose collection is not stored is left
// out. hero sizes the artwork for a detail page's hero rather than a card.
func (h *CatalogHandler) boxSetViews(ctx context.Context, v ItemViewer, libraryID int, groups []catalog.BoxSetGroup, hero bool) (map[int64]BoxSetView, error) {
	ids := make([]int64, 0, len(groups))
	var members []*models.MediaItem
	for _, g := range groups {
		ids = append(ids, g.CollectionID)
		for _, contentID := range g.MemberContentIDs {
			members = append(members, &models.MediaItem{ContentID: contentID, Type: trailerItemTypeMovie})
		}
	}
	collections, err := h.boxSets.Collections(ctx, ids)
	if err != nil {
		return nil, err
	}
	states := h.itemsH.listItemUserStates(ctx, v, members)
	artwork := h.boxSetArtworkURLs(ctx, v.Access, collections, hero)
	views := make(map[int64]BoxSetView, len(groups))
	for _, g := range groups {
		c, ok := collections[g.CollectionID]
		if !ok {
			continue
		}
		played := 0
		for _, contentID := range g.MemberContentIDs {
			if s := states[contentID]; s != nil && s.Played {
				played++
			}
		}
		views[g.CollectionID] = BoxSetView{
			CollectionID:      g.CollectionID,
			LibraryID:         libraryID,
			Title:             c.Name,
			Overview:          c.Overview,
			PosterURL:         artwork[c.PosterPath],
			PosterThumbhash:   c.PosterThumbhash,
			BackdropURL:       artwork[c.BackdropPath],
			BackdropThumbhash: c.BackdropThumbhash,
			MemberCount:       len(g.MemberContentIDs),
			PlayedCount:       played,
			FirstYear:         g.FirstYear,
			LastYear:          g.LastYear,
		}
	}
	return views, nil
}

// boxSetArtworkURLs resolves the collections' artwork, keyed by stored path:
// a cached copy at the requested size, or the provider URL while the image
// is not cached. Cards use the card ladder like item cards; a hero uses the
// sizes item detail uses.
func (h *CatalogHandler) boxSetArtworkURLs(ctx context.Context, access catalog.AccessFilter, collections map[int64]catalog.TMDBMovieCollection, hero bool) map[string]string {
	urls := map[string]string{}
	svc := h.itemsH.detailSvc
	if svc == nil {
		return urls
	}
	var posters, backdrops []string
	for _, c := range collections {
		posters = append(posters, c.PosterPath)
		backdrops = append(backdrops, c.BackdropPath)
	}
	if hero {
		maps.Copy(urls, svc.PresignImageURLs(ctx, posters, artworkkey.ImagePoster, string(access.ImageSizeFor(imagesize.SlotPoster))))
		maps.Copy(urls, svc.PresignImageURLs(ctx, backdrops, artworkkey.ImageBackdrop, string(access.ImageSizeFor(imagesize.SlotBackdrop))))
		return urls
	}
	art := access.Artwork()
	posterSize, backdropSize := art.For(imagesize.SlotPoster), art.For(imagesize.SlotBackdrop)
	posterHint, backdropHint := requestVariantHint("card", posterSize), requestVariantHint("card", backdropSize)
	var batch catalog.ArtworkBatch
	for _, path := range posters {
		batch.Add(sizedCardPath(path, artworkkey.ImagePoster, posterSize), posterHint)
	}
	for _, path := range backdrops {
		batch.Add(sizedCardBackdropPath(path, backdropSize), backdropHint)
	}
	resolved := batch.Resolve(ctx, svc, requestVariantHint("card", art.Size))
	for _, path := range posters {
		if path != "" {
			urls[path] = resolved.Get(sizedCardPath(path, artworkkey.ImagePoster, posterSize), posterHint).URL
		}
	}
	for _, path := range backdrops {
		if path != "" {
			urls[path] = resolved.Get(sizedCardBackdropPath(path, backdropSize), backdropHint).URL
		}
	}
	return urls
}

// boxSetCatalogItem is the card of a box set in place of its representative
// movie. It keeps the movie's sort metrics, which placed it in the list, and
// falls back to the movie's artwork when the collection has none.
func boxSetCatalogItem(representative itemListResponse, view BoxSetView) itemListResponse {
	item := itemListResponse{
		ContentID:         catalog.BoxSetContentID(view.CollectionID),
		Type:              "box_set",
		Title:             view.Title,
		Year:              view.FirstYear,
		Genres:            []string{},
		Keywords:          []string{},
		Status:            representative.Status,
		Overview:          view.Overview,
		PosterURL:         view.PosterURL,
		PosterThumbhash:   view.PosterThumbhash,
		BackdropURL:       view.BackdropURL,
		BackdropThumbhash: view.BackdropThumbhash,
		AddedAt:           representative.AddedAt,
		SortMetrics:       representative.SortMetrics,
		UserState:         &itemUserStateResponse{Played: view.MemberCount > 0 && view.PlayedCount == view.MemberCount},
		BoxSet:            new(view),
	}
	if item.PosterURL == "" {
		item.PosterURL = representative.PosterURL
		item.PosterThumbhash = representative.PosterThumbhash
	}
	if item.BackdropURL == "" {
		item.BackdropURL = representative.BackdropURL
		item.BackdropThumbhash = representative.BackdropThumbhash
	}
	return item
}

// BoxSet returns one box set of a library with its movies the viewer can see
// there, in collection order.
func (h *CatalogHandler) BoxSet(ctx context.Context, v ItemViewer, collectionID int64, libraryID int) (BoxSetDetailView, error) {
	if h == nil || h.boxSets == nil || h.resolver == nil || h.itemsH == nil {
		return BoxSetDetailView{}, apiError(http.StatusInternalServerError, "internal_error", "Box sets are not configured")
	}
	notFound := apiError(http.StatusNotFound, "not_found", "Box set not found")
	minMovies, ok, err := h.boxSets.LibraryBoxSetMinMovies(ctx, libraryID)
	if err != nil {
		return BoxSetDetailView{}, boxSetError(ctx, err)
	}
	if !ok {
		return BoxSetDetailView{}, notFound
	}
	groups, err := h.resolver.BoxSetGroups(ctx, libraryID, minMovies, v.Access, []int64{collectionID})
	if err != nil {
		return BoxSetDetailView{}, boxSetError(ctx, err)
	}
	if len(groups) == 0 {
		return BoxSetDetailView{}, notFound
	}
	views, err := h.boxSetViews(ctx, v, libraryID, groups, true)
	if err != nil {
		return BoxSetDetailView{}, boxSetError(ctx, err)
	}
	view, ok := views[collectionID]
	if !ok {
		return BoxSetDetailView{}, notFound
	}
	members := groups[0].MemberContentIDs
	loaded, err := h.itemsH.itemRepo.GetByIDs(ctx, members)
	if err != nil {
		return BoxSetDetailView{}, boxSetError(ctx, err)
	}
	position := make(map[string]int, len(members))
	for i, contentID := range members {
		position[contentID] = i
	}
	sort.SliceStable(loaded, func(i, j int) bool { return position[loaded[i].ContentID] < position[loaded[j].ContentID] })
	items := h.catalogItemResponses(ctx, v, loaded, "", []int{libraryID}, v.Access)
	return BoxSetDetailView{BoxSetView: view, Items: items}, nil
}

// SearchBoxSets returns the box sets the viewer can see that match query:
// the collection's name contains every word, or at least two of its movies'
// titles the viewer can see do. Only libraries with box sets in search take
// part. A collection in several libraries is listed once, for the library
// holding most of it.
func (h *CatalogHandler) SearchBoxSets(ctx context.Context, v ItemViewer, query string, libraryID, limit int) ([]BoxSetView, error) {
	if h == nil || h.boxSets == nil || h.resolver == nil || h.itemsH == nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Box sets are not configured")
	}
	libraries, err := h.boxSets.BoxSetLibraries(ctx)
	if err != nil {
		return nil, boxSetError(ctx, err)
	}
	libraries = slices.DeleteFunc(libraries, func(l catalog.BoxSetLibrary) bool {
		return !l.InSearch || (libraryID > 0 && l.ID != libraryID)
	})
	libraryIDs := make([]int, 0, len(libraries))
	for _, l := range libraries {
		libraryIDs = append(libraryIDs, l.ID)
	}
	out := []BoxSetView{}
	// Matches are read a page at a time: the viewer may see too few movies of
	// the best-ranked ones.
	for page := range boxSetSearchPages {
		candidates, err := h.resolver.SearchBoxSetCollectionIDs(ctx, query, libraryIDs, v.Access, boxSetSearchCandidates, page*boxSetSearchCandidates)
		if err != nil {
			return nil, boxSetError(ctx, err)
		}
		views, err := h.visibleBoxSets(ctx, v, libraries, candidates)
		if err != nil {
			return nil, err
		}
		for _, id := range candidates {
			if view, ok := views[id]; ok && len(out) < limit {
				out = append(out, view)
			}
		}
		if len(out) >= limit || len(candidates) < boxSetSearchCandidates {
			break
		}
	}
	return out, nil
}

// visibleBoxSets returns the box sets among candidates the viewer can see in
// libraries with box sets in search, keyed by collection. A collection in
// several libraries is listed for the library holding most of it.
func (h *CatalogHandler) visibleBoxSets(ctx context.Context, v ItemViewer, libraries []catalog.BoxSetLibrary, candidates []int64) (map[int64]BoxSetView, error) {
	best := map[int64]BoxSetView{}
	if len(candidates) == 0 {
		return best, nil
	}
	// A collection in several libraries is listed once, under the library
	// with the most of its movies, so only those groups become views.
	type choice struct {
		libraryID int
		group     catalog.BoxSetGroup
	}
	chosen := map[int64]choice{}
	for _, library := range libraries {
		if !library.InSearch {
			continue
		}
		groups, err := h.resolver.BoxSetGroups(ctx, library.ID, library.MinMovies, v.Access, candidates)
		if err != nil {
			return nil, boxSetError(ctx, err)
		}
		for _, g := range groups {
			if current, ok := chosen[g.CollectionID]; !ok || len(g.MemberContentIDs) > len(current.group.MemberContentIDs) {
				chosen[g.CollectionID] = choice{libraryID: library.ID, group: g}
			}
		}
	}
	byLibrary := map[int][]catalog.BoxSetGroup{}
	for _, c := range chosen {
		byLibrary[c.libraryID] = append(byLibrary[c.libraryID], c.group)
	}
	for libraryID, groups := range byLibrary {
		views, err := h.boxSetViews(ctx, v, libraryID, groups, false)
		if err != nil {
			return nil, boxSetError(ctx, err)
		}
		maps.Copy(best, views)
	}
	return best, nil
}

func boxSetError(ctx context.Context, err error) error {
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		return apiErr
	}
	slog.ErrorContext(ctx, "catalog: box set read failed", "component", "api", "error", err)
	return apiError(http.StatusInternalServerError, "internal_error", "Failed to read box sets")
}

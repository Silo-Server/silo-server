package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/usercollections"
)

// Library viewer seams: the profile-scoped /library/{id} reads the v1
// handlers and the v2 operations share. Each returns the view the v1 handler
// writes verbatim and, on failure, an *APIError.

// SectionViewer is what a section read needs to know about its caller
// beyond the identity on the context: the access filter (which carries the
// declared device) and the artwork size asked for.
type SectionViewer struct {
	Access    catalog.AccessFilter
	ImageSize imagesize.Size
}

func sectionViewerFromRequest(r *http.Request) SectionViewer {
	return SectionViewer{Access: requestAccessFilter(r), ImageSize: requestImageSize(r)}
}

// SectionLayoutView is a page's section layout without items.
type SectionLayoutView = homeLayoutResponse

// SectionLayoutEntryView is one section of a layout.
type SectionLayoutEntryView = resolvedSectionLayoutResponse

// SectionsView is a page's sections with their items.
type SectionsView = homeSectionsResponse

// SectionView is one section with its items.
type SectionView = resolvedSectionResponse

// SectionItemView is one catalog item card in a section.
type SectionItemView = sectionItemResponse

// SectionUpcomingEventView is the next airing on a card.
type SectionUpcomingEventView = upcomingEventResponse

// ItemUserStateView is the viewer's flags on an item.
type ItemUserStateView = itemUserStateResponse

// CollectionItemView is one catalog item card in a collection listing.
type CollectionItemView = itemListResponse

// LibraryCollectionTabView is a library's Collections tab.
type LibraryCollectionTabView = libraryTabResponse

// LibraryCollectionView is one curated collection in full.
type LibraryCollectionView = libraryCollectionResponse

// LibraryCollectionTabGroupView is one group of the Collections tab.
type LibraryCollectionTabGroupView = libraryTabGroup

// LibraryCollectionTabEntryView is one collection card on the tab.
type LibraryCollectionTabEntryView = libraryTabCollection

// LibraryCollectionTabUngroupedView is the tab's ungrouped collections.
type LibraryCollectionTabUngroupedView = libraryTabUngrouped

// HomeLayout answers the home page's section layout for the profile on the
// context.
func (h *SectionHandler) HomeLayout(ctx context.Context) (SectionLayoutView, error) {
	resolved, _, _, _, err := h.loadResolvedHomeSections(ctx)
	if err != nil {
		return SectionLayoutView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load sections")
	}
	resolved = h.maybeInjectNextUp(ctx, resolved, apimw.GetUserID(ctx))
	return sectionLayoutOf(resolved), nil
}

// requireViewableLibrary is the gate every profile-scoped section read
// shares: the viewer scope must admit the library and, when a folder
// repository is wired, the library must exist and be enabled. Every refusal
// is the same not_found so a restricted profile cannot probe which libraries
// exist, an unrestricted one is not handed a fabricated default layout for
// an id that was never a library, and a library an administrator disabled
// (including one DeleteLibrary disabled ahead of its asynchronous removal)
// stops serving the moment the flag flips.
func (h *SectionHandler) requireViewableLibrary(ctx context.Context, libraryID int) error {
	return requireViewableLibrary(ctx, h.FolderRepo, libraryID)
}

func requireViewableLibrary(ctx context.Context, folders *catalog.FolderRepository, libraryID int) error {
	if !viewerCanAccessLibrary(ctx, libraryID) {
		return apiError(http.StatusNotFound, "not_found", "Library not found")
	}
	if folders == nil {
		return nil
	}
	folder, err := folders.GetByID(ctx, libraryID)
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			return apiError(http.StatusNotFound, "not_found", "Library not found")
		}
		slog.ErrorContext(ctx, "loading library for profile-scoped read", "component", "api", "library_id", libraryID, "error", err)
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to load library")
	}
	if !folder.Enabled {
		return apiError(http.StatusNotFound, "not_found", "Library not found")
	}
	return nil
}

// LibraryLayout answers the library's section layout for the profile on
// the context.
func (h *SectionHandler) LibraryLayout(ctx context.Context, libraryID int) (SectionLayoutView, error) {
	if err := h.requireViewableLibrary(ctx, libraryID); err != nil {
		return SectionLayoutView{}, err
	}
	resolved, _, _, err := h.loadResolvedLibrarySections(ctx, libraryID)
	if err != nil {
		return SectionLayoutView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load sections")
	}
	return sectionLayoutOf(resolved), nil
}

func sectionLayoutOf(resolved []sections.ResolvedSection) SectionLayoutView {
	resp := SectionLayoutView{Sections: make([]resolvedSectionLayoutResponse, 0, len(resolved))}
	for _, s := range resolved {
		resp.Sections = append(resp.Sections, resolvedSectionLayoutResponse{
			ID:          s.ID,
			SectionType: string(s.SectionType),
			Title:       s.Title,
			Featured:    s.Featured,
			ItemLimit:   s.ItemLimit,
			IsCustom:    s.IsCustom,
			Customized:  s.Customized,
		})
	}
	return resp
}

// LibrarySections answers every section of the library with its items.
func (h *SectionHandler) LibrarySections(ctx context.Context, libraryID int, viewer SectionViewer) (SectionsView, error) {
	if err := h.requireViewableLibrary(ctx, libraryID); err != nil {
		return SectionsView{}, err
	}
	resolved, accessFilter, profileID, err := h.loadResolvedLibrarySections(ctx, libraryID)
	if err != nil {
		return SectionsView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load sections")
	}
	userID := viewer.Access.UserID
	withItems := h.fetcher.FetchAll(ctx, resolved, &libraryID, nil, userID, profileID, accessFilter)
	withItems = applyDiversityFilter(withItems)
	withItems = dropEmptySeasonalSections(withItems)
	return h.buildSections(ctx, withItems, &libraryID, viewer.Access, viewer.ImageSize), nil
}

// LibrarySectionItems answers one section of the library with its items.
func (h *SectionHandler) LibrarySectionItems(ctx context.Context, libraryID int, sectionID string, viewer SectionViewer) (SectionView, error) {
	if err := h.requireViewableLibrary(ctx, libraryID); err != nil {
		return SectionView{}, err
	}
	resolved, accessFilter, profileID, err := h.loadResolvedLibrarySections(ctx, libraryID)
	if err != nil {
		return SectionView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load sections")
	}
	userID := viewer.Access.UserID
	for _, s := range resolved {
		if s.ID != sectionID {
			continue
		}
		withItems, fetchErr := h.fetcher.FetchOne(ctx, s, &libraryID, nil, userID, profileID, accessFilter)
		if fetchErr != nil {
			slog.ErrorContext(ctx, "fetching section items", "component", "api", "section_id", s.ID, "type", s.SectionType, "error", fetchErr)
			withItems = sections.SectionWithItems{
				ResolvedSection: s,
				Items:           []*models.MediaItem{},
			}
		}
		resp := h.buildSections(ctx, []sections.SectionWithItems{withItems}, &libraryID, viewer.Access, viewer.ImageSize)
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

// requireViewableLibrary is the collection reads' share of the gate above:
// the library must be in scope, exist, and be enabled before any
// collection is looked up. Without it an unrestricted viewer could name a
// library id that was never a library and be answered with personal
// collections that carry no explicit library membership, which the store
// treats as visible on every tab.
func (h *LibraryCollectionHandler) requireViewableLibrary(ctx context.Context, libraryID int) error {
	return requireViewableLibrary(ctx, h.FolderRepo, libraryID)
}

// LibraryUserCollections answers the viewer's own collections opted into
// the library's Collections tab. Personal collections are private to their
// owner; this never reveals other users' rows.
func (h *LibraryCollectionHandler) LibraryUserCollections(ctx context.Context, libraryID, userID int, profileID string) ([]usercollections.ServerVisibleCollection, error) {
	if err := h.requireViewableLibrary(ctx, libraryID); err != nil {
		return nil, err
	}
	if h.UserCollectionPool == nil {
		return []usercollections.ServerVisibleCollection{}, nil
	}
	collections, err := usercollections.ListServerVisibleByLibrary(ctx, h.UserCollectionPool, userID, profileID, libraryID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load user collections")
	}
	if collections == nil {
		collections = []usercollections.ServerVisibleCollection{}
	}
	for i := range collections {
		collections[i].PosterURL = h.presignGPURLCtx(ctx, collections[i].PosterPath)
	}
	return collections, nil
}

// LibraryCollectionsTab answers the library's Collections tab: every
// curated collection in full and, when groups are configured, the grouped
// and ungrouped cards including the viewer's opted-in personal collections.
func (h *LibraryCollectionHandler) LibraryCollectionsTab(ctx context.Context, libraryID, userID int, profileID string) (LibraryCollectionTabView, error) {
	if err := h.requireViewableLibrary(ctx, libraryID); err != nil {
		return LibraryCollectionTabView{}, err
	}
	adminCollections, err := h.repo.ListByLibrary(ctx, libraryID, catalog.ListLibraryCollectionsOptions{})
	if err != nil {
		return LibraryCollectionTabView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load collections")
	}
	resp := LibraryCollectionTabView{
		LibraryID:   libraryID,
		Collections: h.libraryCollectionResponsesOf(ctx, adminCollections),
		Groups:      []libraryTabGroup{},
	}
	if h.GroupRepo == nil {
		return resp, nil
	}
	groups, err := h.GroupRepo.ListByLibrary(ctx, libraryID)
	if err != nil {
		return LibraryCollectionTabView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load groups")
	}
	adminCollectionsByGroup := groupLibraryTabCollections(adminCollections)

	var userCollections []usercollections.ServerVisibleCollection
	userCollectionsLoaded := false
	for _, g := range groups {
		var colls []libraryTabCollection
		switch g.Kind {
		case models.GroupKindUserCollections:
			if h.UserCollectionPool == nil || userID == 0 {
				continue
			}
			if !userCollectionsLoaded {
				loadedUserCollections, loadErr := usercollections.ListServerVisibleByLibrary(ctx, h.UserCollectionPool, userID, profileID, libraryID)
				if loadErr != nil {
					return LibraryCollectionTabView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load user collections")
				}
				userCollections = loadedUserCollections
				userCollectionsLoaded = true
			}
			sorted := applyUserCollectionSort(userCollections, g.DefaultSortMode)
			for i := range sorted {
				posterURL := h.presignGPURLCtx(ctx, sorted[i].PosterPath)
				creatorProfileID := sorted[i].CreatorProfileID
				colls = append(colls, libraryTabCollection{
					ID:               sorted[i].ID,
					Title:            sorted[i].Name,
					PosterURL:        posterURL,
					PosterThumbhash:  sorted[i].PosterThumbhash,
					ItemCount:        sorted[i].ItemCount,
					CreatorProfileID: &creatorProfileID,
				})
			}
		default:
			collections := adminCollectionsByGroup[g.ID]
			collections = applyCollectionSort(collections, g.DefaultSortMode)
			for _, c := range collections {
				colls = append(colls, libraryTabCollection{
					ID:              c.ID,
					Title:           c.Title,
					PosterURL:       h.presignGPURLCtx(ctx, c.PosterURL),
					PosterThumbhash: c.PosterThumbhash,
					ItemCount:       c.ItemCount,
					Featured:        c.Featured,
				})
			}
		}
		if len(colls) == 0 {
			continue
		}
		resp.Groups = append(resp.Groups, libraryTabGroup{
			ID:          g.ID,
			Name:        g.Name,
			Kind:        g.Kind,
			SortMode:    g.DefaultSortMode,
			SortOrder:   g.SortOrder,
			Collections: colls,
		})
	}

	ungrouped := adminCollectionsByGroup[groupKeyPtr(nil)]
	if len(ungrouped) > 0 {
		uColls := make([]libraryTabCollection, 0, len(ungrouped))
		for _, c := range ungrouped {
			uColls = append(uColls, libraryTabCollection{
				ID:              c.ID,
				Title:           c.Title,
				PosterURL:       h.presignGPURLCtx(ctx, c.PosterURL),
				PosterThumbhash: c.PosterThumbhash,
				ItemCount:       c.ItemCount,
				Featured:        c.Featured,
			})
		}
		sortOrder, err := h.GroupRepo.GetUngroupedSortOrder(ctx, libraryID)
		if err != nil {
			sortOrder = 9999
		}
		resp.Ungrouped = &libraryTabUngrouped{SortOrder: sortOrder, Collections: uColls}
	}
	return resp, nil
}

func (h *LibraryCollectionHandler) libraryCollectionResponsesOf(ctx context.Context, collections []*models.LibraryCollection) []libraryCollectionResponse {
	out := make([]libraryCollectionResponse, 0, len(collections))
	for _, collection := range collections {
		out = append(out, h.libraryCollectionResponseOf(ctx, collection))
	}
	return out
}

// CollectionItemPage bounds one page of a collection's items: at most
// Limit items starting Offset positions into the collection's order. The
// zero value asks for the whole collection in one answer, which is what v1
// still serves.
type CollectionItemPage struct {
	Limit  int
	Offset int
}

// paged reports whether the caller asked for a page rather than the whole
// collection.
func (p CollectionItemPage) paged() bool {
	return p.Limit > 0
}

// LibraryCollectionItems answers the items of one visible collection of the
// library, in curated order or as the smart query resolves them. With a
// page it answers that window of the order and whether more follow; the
// zero page answers everything and never reports more.
func (h *LibraryCollectionHandler) LibraryCollectionItems(ctx context.Context, libraryID int, collectionID string, access catalog.AccessFilter, page CollectionItemPage) ([]CollectionItemView, bool, error) {
	if err := h.requireViewableLibrary(ctx, libraryID); err != nil {
		return nil, false, err
	}
	collection, err := h.repo.GetByID(ctx, collectionID)
	if err != nil || !collectionSpansLibrary(collection, libraryID) || collection.Visibility != catalog.LibraryCollectionVisibilityVisible {
		return nil, false, apiError(http.StatusNotFound, "not_found", "Collection not found")
	}
	var (
		items   []itemListResponse
		hasMore bool
	)
	if catalog.IsLiveQueryType(collection.CollectionType) {
		items, hasMore, err = h.loadLiveCollectionItems(ctx, collection, access, page)
	} else {
		items, hasMore, err = h.loadOrderedCollectionItems(ctx, collectionID, access, page)
	}
	if err != nil {
		var queryErr smartCollectionQueryError
		if errors.As(err, &queryErr) {
			return nil, false, apiError(http.StatusBadRequest, "bad_request", err.Error())
		}
		return nil, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to load collection items")
	}
	return items, hasMore, nil
}

// collectionSpansLibrary reports whether the collection is a member of the
// library: listed in its full membership (LibraryIDs, the rows of
// library_collection_libraries that ListByLibrary joins on) or carrying it
// as the legacy primary LibraryID. A collection spanning libraries [1,2]
// is listed on both Collections tabs, so its items must be served from
// either library too.
func collectionSpansLibrary(collection *models.LibraryCollection, libraryID int) bool {
	return collection.LibraryID == libraryID || slices.Contains(collection.LibraryIDs, libraryID)
}

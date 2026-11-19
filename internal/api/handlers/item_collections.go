package handlers

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

// ItemCollections answers the "Also in" row of a title's page: the
// collections that store the title and that the viewer may see, as the
// cards a row of collections shows. Smart collections store no titles and
// are never listed; working out whether a title matches each one's rules on
// every page view would cost a query per smart collection.
type ItemCollections struct {
	Cards   *CollectionRowCards
	Items   *catalog.ItemRepository
	BoxSets MovieBoxSetResolver
	// Browser reads a collection's page for the preview.
	Browser CollectionBrowser
}

// CollectionBrowser reads one page of the catalog, as *CatalogHandler does.
type CollectionBrowser interface {
	Browse(ctx context.Context, v ItemViewer, req catalog.CatalogRequest, groupedByWork bool) (CatalogBrowseView, error)
}

// ItemCollectionsView is what a title's page shows of its collections: the
// cards of every collection that holds it and, when only one does, a preview
// of that collection's other titles.
type ItemCollectionsView struct {
	Cards []sections.CollectionCard
	// Preview is, when Cards holds one collection, up to
	// itemCollectionPreviewSize of its other titles the viewer may see, in
	// collection order; otherwise empty.
	Preview []CollectionItemView
}

const itemCollectionPreviewSize = 6

// MovieBoxSetResolver finds the box set a movie's page shows.
type MovieBoxSetResolver interface {
	MovieBoxSet(ctx context.Context, contentID string, libraryID int, access catalog.AccessFilter) (catalog.MovieBoxSet, bool, error)
}

// ItemCollections returns the cards for contentID: the server collections
// first, then the personal ones, each group by title, and the preview of the
// one collection when only one holds it. A title the viewer
// cannot see is a 404 whatever holds it. A server collection synced from the
// TMDB collection whose box set the page already shows is left out, so the
// page does not show the same franchise twice.
func (s *ItemCollections) ItemCollections(ctx context.Context, v ItemViewer, userID int, contentID string) (ItemCollectionsView, error) {
	if s == nil || s.Cards == nil || s.Items == nil {
		return ItemCollectionsView{}, apiError(http.StatusInternalServerError, "internal_error", "Collections are not configured")
	}
	// Collections hold titles, so an episode or season ID is a 404 too.
	titleAccess := v.Access
	titleAccess.ExcludedMediaTypes = append(slices.Clone(v.Access.ExcludedMediaTypes), "episode", "season")
	if err := s.Items.EnsureAccessible(ctx, contentID, titleAccess); err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			return ItemCollectionsView{}, apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		return ItemCollectionsView{}, fmt.Errorf("checking title access: %w", err)
	}
	cards, err := s.cards(ctx, v, userID, contentID)
	if err != nil {
		return ItemCollectionsView{}, err
	}
	out := ItemCollectionsView{Cards: cards}
	if len(cards) == 1 {
		// The preview only adds to a card that already passed every check,
		// so a failed read keeps the card and previews nothing.
		if out.Preview, err = s.preview(ctx, v, contentID, cards[0]); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ItemCollectionsView{}, ctxErr
			}
			slog.WarnContext(ctx, "reading a title's collection preview", "component", "api", "content_id", contentID, "collection_id", cards[0].ID, "error", err)
			out.Preview = nil
		}
	}
	return out, nil
}

func (s *ItemCollections) cards(ctx context.Context, v ItemViewer, userID int, contentID string) ([]sections.CollectionCard, error) {
	refs, err := s.serverRefs(ctx, v, contentID)
	if err != nil {
		return nil, err
	}
	personal, err := s.personalRefs(ctx, userID, contentID)
	if err != nil {
		return nil, err
	}
	cards, err := s.Cards.CollectionCards(ctx, append(refs, personal...), sections.CollectionCardScope{
		UserID: userID, ProfileID: v.ProfileID, Filter: v.Access,
	})
	if err != nil {
		return nil, err
	}
	if cards, err = s.dropOwnerHidden(ctx, v, userID, contentID, cards); err != nil {
		return nil, err
	}
	// Server cards keep the repository's title order; personal ids come
	// unordered, so their cards sort by title here.
	firstPersonal := slices.IndexFunc(cards, func(c sections.CollectionCard) bool { return c.Source == sections.CollectionCardUser })
	if firstPersonal >= 0 {
		slices.SortStableFunc(cards[firstPersonal:], func(a, b sections.CollectionCard) int {
			return cmp.Or(strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)), strings.Compare(a.Title, b.Title))
		})
	}
	return cards, nil
}

// dropOwnerHidden leaves out another profile's collection when its owner
// may not see contentID: that collection's own page, read with the owner's
// access, does not list the title, so the row must not claim it does.
func (s *ItemCollections) dropOwnerHidden(ctx context.Context, v ItemViewer, userID int, contentID string, cards []sections.CollectionCard) ([]sections.CollectionCard, error) {
	var owners catalog.PersonalCollectionAccess
	if s.Cards.Personal != nil {
		owners = s.Cards.Personal.CollectionOwners
	}
	checked := map[string]bool{}
	kept := cards[:0]
	for _, card := range cards {
		if card.Source != sections.CollectionCardUser || card.CreatorProfileID == nil || *card.CreatorProfileID == v.ProfileID {
			kept = append(kept, card)
			continue
		}
		owner := *card.CreatorProfileID
		visible, seen := checked[owner]
		if !seen {
			var err error
			if visible, err = s.ownerSees(ctx, owners, v, userID, owner, contentID); err != nil {
				return nil, err
			}
			checked[owner] = visible
		}
		if visible {
			kept = append(kept, card)
		}
	}
	return kept, nil
}

// ownerSees reports whether contentID is in what both the viewer and the
// collection owner may see.
func (s *ItemCollections) ownerSees(ctx context.Context, owners catalog.PersonalCollectionAccess, v ItemViewer, userID int, owner, contentID string) (bool, error) {
	filter, err := catalog.PersonalCollectionFilter(ctx, owners, v.Access, userID, v.ProfileID, owner)
	if errors.Is(err, catalog.ErrPersonalCollectionOwnerAccess) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch err := s.Items.EnsureAccessible(ctx, contentID, filter); {
	case err == nil:
		return true, nil
	case errors.Is(err, catalog.ErrItemNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("checking a collection owner's access to a title: %w", err)
	}
}

// serverRefs lists the visible server collections that store contentID,
// by title, without the synced copy of the page's box set.
func (s *ItemCollections) serverRefs(ctx context.Context, v ItemViewer, contentID string) ([]recipes.CollectionRef, error) {
	if s.Cards.Library == nil {
		return nil, nil
	}
	containing, err := s.Cards.Library.repo.ListContainingItem(ctx, contentID)
	if err != nil {
		return nil, fmt.Errorf("listing server collections holding a title: %w", err)
	}
	// A collection made smart keeps the rows it stored as a manual one;
	// they are not its titles any more.
	containing = slices.DeleteFunc(containing, func(c *models.LibraryCollection) bool { return catalog.IsLiveQueryType(c.CollectionType) })
	// Only a collection synced from a TMDB collection can copy the box set,
	// so the box set is looked up only when one holds the title.
	var boxSet int64
	if slices.ContainsFunc(containing, func(c *models.LibraryCollection) bool {
		_, synced := catalog.TMDBCollectionSourceID(c.SourceConfig)
		return synced
	}) {
		// Without the box set the synced copy stays listed: a repeated
		// franchise beats losing every card over an optional read.
		if boxSet, err = s.shownBoxSet(ctx, v, contentID); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			slog.WarnContext(ctx, "finding a title's box set for its collections", "component", "api", "content_id", contentID, "error", err)
			boxSet = 0
		}
	}
	refs := make([]recipes.CollectionRef, 0, len(containing))
	for _, c := range containing {
		if synced, ok := catalog.TMDBCollectionSourceID(c.SourceConfig); ok && synced == boxSet {
			continue
		}
		refs = append(refs, recipes.CollectionRef{LibraryCollectionID: c.ID})
	}
	return refs, nil
}

// shownBoxSet is the TMDB collection id of the box set the title's page
// shows, or 0 when it shows none.
func (s *ItemCollections) shownBoxSet(ctx context.Context, v ItemViewer, contentID string) (int64, error) {
	if s.BoxSets == nil {
		return 0, nil
	}
	libraryID := 0
	if v.Access.PresentationLibraryID != nil {
		libraryID = *v.Access.PresentationLibraryID
	}
	found, ok, err := s.BoxSets.MovieBoxSet(ctx, contentID, libraryID, v.Access)
	if err != nil {
		return 0, fmt.Errorf("finding the title's box set: %w", err)
	}
	if !ok {
		return 0, nil
	}
	return found.CollectionID, nil
}

// personalRefs lists the account's collections that store contentID. The
// card builder then keeps only those the acting profile may see.
func (s *ItemCollections) personalRefs(ctx context.Context, userID int, contentID string) ([]recipes.CollectionRef, error) {
	if s.Cards.Personal == nil || userID <= 0 {
		return nil, nil
	}
	ids, err := s.Cards.Personal.collectionsHoldingItem(ctx, userID, contentID)
	if err != nil {
		return nil, fmt.Errorf("listing personal collections holding a title: %w", err)
	}
	refs := make([]recipes.CollectionRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, recipes.CollectionRef{UserCollectionID: id})
	}
	return refs, nil
}

// preview lists the first titles of card's collection other than
// contentID, at most itemCollectionPreviewSize, as the collection's page
// lists them: its sort, its Show only filter, and for another profile's
// collection what its owner may see too. A collection the viewer cannot
// open previews nothing.
func (s *ItemCollections) preview(ctx context.Context, v ItemViewer, contentID string, card sections.CollectionCard) ([]CollectionItemView, error) {
	if s.Browser == nil {
		return nil, nil
	}
	req := catalog.CatalogRequest{
		Source:       catalog.CatalogSourceUserCollection,
		CollectionID: card.ID,
		// One more than shown, for when the title itself is among them.
		Limit:          itemCollectionPreviewSize + 1,
		SkipTotal:      true,
		UseSourceOrder: true,
		// The first page listCatalogItems reads, which pages the
		// collection's members in the database. Without it the read loads
		// and checks every member of the collection to keep seven.
		CursorPaging: true,
	}
	// listCatalogItems reads a collection with no presentation library, so
	// the page's library_id must not reach the preview either.
	v.Access.PresentationLibraryID = nil
	v.Access.ScopeFilesToLibrary = false
	v.Access.SeriesLibraryCoversEpisodes = false
	if card.Source == sections.CollectionCardLibrary {
		req.Source = catalog.CatalogSourceLibraryCollection
		// Explore all opens the collection in the card's library, which
		// keeps its titles in that library; the preview keeps the same ones.
		if card.LibraryID != nil {
			req.Query.LibraryIDs = []int{*card.LibraryID}
		}
	}
	page, err := s.Browser.Browse(ctx, v, req, false)
	if err != nil {
		if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("listing a collection preview: %w", err)
	}
	items := slices.DeleteFunc(page.Items, func(item CollectionItemView) bool { return item.ContentID == contentID })
	return items[:min(len(items), itemCollectionPreviewSize)], nil
}

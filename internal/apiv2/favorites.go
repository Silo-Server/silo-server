package apiv2

import (
	"context"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The favorites domain: the acting profile's favorite catalog items. The
// watchlist domain (watchlist.go) is the same pattern over the other list.

// FavoriteListInput is the listFavorites query.
type FavoriteListInput struct {
	ImageSize string `query:"image_size" enum:"small,medium,large,original" doc:"Artwork variant to presign; absent picks each surface's default" example:"medium"`
	LimitParam
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvIjo1MH0"`
}

// FavoriteItemInput names one catalog item of the profile's favorites.
type FavoriteItemInput struct {
	ItemID ID `path:"item_id" doc:"Catalog item identifier" example:"movie:heat-1995"`
}

// FavoriteCollection is the named envelope the contract carries: the
// favorite items as catalog cards, newest favorite first.
type FavoriteCollection struct {
	Collection[CatalogItem]
}

// FavoriteCollectionOutput is the listFavorites response.
type FavoriteCollectionOutput struct {
	Body FavoriteCollection
}

// FavoriteEntry is one item's membership in the profile's favorites.
type FavoriteEntry struct {
	ItemID  ID      `json:"item_id" doc:"The catalog item" example:"movie:heat-1995"`
	AddedAt Instant `json:"added_at" doc:"When the item became a favorite" example:"2026-01-02T03:04:05.000Z"`
}

// FavoriteEntryOutput is the getFavorite response.
type FavoriteEntryOutput struct {
	Body FavoriteEntry
}

const opListFavorites = "listFavorites"

func registerFavorites(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	viewer := func(op huma.Operation) Operation {
		return Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true}
	}
	mutation := func(op huma.Operation) Operation {
		op.DefaultStatus = http.StatusNoContent
		return Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	}

	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/favorites", opListFavorites, "favorites",
		"List the acting profile's favorites as catalog cards, newest favorite first; items the viewer may not see are omitted.")),
		func(ctx context.Context, in *FavoriteListInput) (*FavoriteCollectionOutput, error) {
			return reg.listFavorites(ctx, cursors, in)
		})
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/favorites/{item_id}", "getFavorite", "favorites",
		"Answer whether the item is one of the acting profile's favorites: the entry, or 404 when it is not (or the viewer may not see it).")),
		reg.getFavorite)
	Register(reg, mutation(humaOp(http.MethodPut, Prefix+"/favorites/{item_id}", "addFavorite", "favorites",
		"Add the item to the acting profile's favorites. Automatic retries are unsafe because provider and refresh effects are not change-gated.")),
		reg.addFavorite)
	Register(reg, mutation(humaOp(http.MethodDelete, Prefix+"/favorites/{item_id}", "deleteFavorite", "favorites",
		"Remove the item from the acting profile's favorites; an absent entry succeeds, but automatic retries can repeat provider and refresh effects.")),
		reg.deleteFavorite)
}

// personalListViewer is the identity a personal-list seam acts as. The v2
// listener does not read a device header, so the access filter carries no
// device id.
func personalListViewer(ctx context.Context, imageSize string) (handlers.PersonalListViewer, *Problem) {
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return handlers.PersonalListViewer{}, p
	}
	size, err := imagesize.Parse(imageSize)
	if err != nil {
		size = imagesize.Unset
	}
	return handlers.PersonalListViewer{UserID: userID, ProfileID: profileID, Access: handlers.AccessFilterFromContext(ctx, ""), ImageSize: size}, nil
}

// personalListScope binds a personal list's cursor to the profile and the
// viewer policy that filters its cards, and to the keyset it pages by.
func personalListScope(ctx context.Context, operationID string, viewer handlers.PersonalListViewer) CursorScope {
	return CursorScope{
		OperationID: operationID,
		Security:    strconv.Itoa(viewer.UserID) + "/" + viewer.ProfileID + "/" + viewerScopeDigest(ctx),
		Sort:        "-added_at,-item_id",
		Tiebreaker:  tiebreakerItemID,
	}
}

// personalListPosition is the favorites and watchlist cursor payload: the
// keyset (added_at, media_item_id) of the last entry the previous page
// emitted, in the store's own string form. The next page resumes strictly
// after it, so an entry added or removed between pages neither repeats nor
// hides a row, and equal timestamps are ordered by the unique item id.
type personalListPosition struct {
	AddedAt     string `json:"a"`
	MediaItemID string `json:"m"`
}

// decodeListKey reads the keyset a personal list's cursor carries; no
// cursor starts at the newest row.
func decodeListKey(cursors *Cursors, scope CursorScope, cursor string) (*userstore.ListKey, *Problem) {
	if cursor == "" {
		return nil, nil
	}
	var pos personalListPosition
	if p := cursors.Decode(scope, cursor, &pos); p != nil {
		return nil, p
	}
	return &userstore.ListKey{AddedAt: pos.AddedAt, MediaItemID: pos.MediaItemID}, nil
}

// listFavorites answers from the same listing v1 GET /favorites uses, paged
// by keyset. A limit+1 probe decides has_more from the raw rows, so an entry
// the catalog no longer has or the viewer may not see never hides the rows
// behind it.
func (reg *Registry) listFavorites(ctx context.Context, cursors *Cursors, in *FavoriteListInput) (*FavoriteCollectionOutput, error) {
	if reg.deps.PersonalLists == nil {
		return nil, unavailable("favorites")
	}
	viewer, p := personalListViewer(ctx, in.ImageSize)
	if p != nil {
		return nil, p
	}
	scope := personalListScope(ctx, opListFavorites, viewer)
	after, p := decodeListKey(cursors, scope, in.Cursor)
	if p != nil {
		return nil, p
	}
	entries, cards, err := reg.deps.PersonalLists.ListFavoritesPage(ctx, viewer, after, in.Limit+1)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items, next, p := personalListPage(cursors, scope, in.Limit, entries, cards, func(e userstore.Favorite) userstore.ListKey {
		return userstore.ListKey{AddedAt: e.AddedAt, MediaItemID: e.MediaItemID}
	})
	if p != nil {
		return nil, p
	}
	return &FavoriteCollectionOutput{Body: FavoriteCollection{Collection: Paginated(items, next)}}, nil
}

// personalListPage renders the cards of a limit+1 probe as the page and
// mints the next cursor, the keyset of the last entry of the page, when a
// probe row followed. The cards preserve entry order and omit unresolved
// entries, so the probe row's card, when it has one, can only be the last
// card.
func personalListPage[E any](cursors *Cursors, scope CursorScope, limit int, entries []E, cards []handlers.CollectionItemView, keyOf func(E) userstore.ListKey) ([]CatalogItem, string, *Problem) {
	next := ""
	if len(entries) > limit {
		probe := keyOf(entries[limit])
		if n := len(cards); n > 0 && cards[n-1].ContentID == probe.MediaItemID {
			cards = cards[:n-1]
		}
		last := keyOf(entries[limit-1])
		var err error
		if next, err = cursors.Encode(scope, personalListPosition{AddedAt: last.AddedAt, MediaItemID: last.MediaItemID}); err != nil {
			return nil, "", NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
	}
	items := make([]CatalogItem, 0, len(cards))
	for _, card := range cards {
		items = append(items, catalogItemOfListing(card))
	}
	return items, next, nil
}

func (reg *Registry) getFavorite(ctx context.Context, in *FavoriteItemInput) (*FavoriteEntryOutput, error) {
	if reg.deps.PersonalLists == nil {
		return nil, unavailable("favorites")
	}
	viewer, p := personalListViewer(ctx, "")
	if p != nil {
		return nil, p
	}
	entry, found, err := reg.deps.PersonalLists.GetFavorite(ctx, viewer, string(in.ItemID))
	if err != nil {
		return nil, serviceProblem(err)
	}
	if !found {
		return nil, NewProblem(TypeNotFound, "The item is not one of the profile's favorites.")
	}
	added, p := storeInstant(entry.AddedAt)
	if p != nil {
		return nil, p
	}
	return &FavoriteEntryOutput{Body: FavoriteEntry{ItemID: ID(entry.MediaItemID), AddedAt: added}}, nil
}

func (reg *Registry) addFavorite(ctx context.Context, in *FavoriteItemInput) (*struct{}, error) {
	if reg.deps.PersonalLists == nil {
		return nil, unavailable("favorites")
	}
	viewer, p := personalListViewer(ctx, "")
	if p != nil {
		return nil, p
	}
	if err := reg.deps.PersonalLists.AddFavorite(ctx, viewer, string(in.ItemID)); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

func (reg *Registry) deleteFavorite(ctx context.Context, in *FavoriteItemInput) (*struct{}, error) {
	if reg.deps.PersonalLists == nil {
		return nil, unavailable("favorites")
	}
	viewer, p := personalListViewer(ctx, "")
	if p != nil {
		return nil, p
	}
	if err := reg.deps.PersonalLists.RemoveFavorite(ctx, viewer, string(in.ItemID)); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

package apiv2

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The watchlist domain: the acting profile's watchlist, the same pattern as
// favorites.go over the other list.

// WatchlistListInput is the listWatchlist query.
type WatchlistListInput struct {
	ImageSize string `query:"image_size" enum:"small,medium,large,original" doc:"Artwork variant to presign; absent picks each surface's default" example:"medium"`
	LimitParam
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvIjo1MH0"`
}

// WatchlistItemInput names one catalog item of the profile's watchlist.
type WatchlistItemInput struct {
	ItemID ID `path:"item_id" doc:"Catalog item identifier" example:"movie:heat-1995"`
}

// WatchlistCollection is the named envelope the contract carries: the
// watchlist items as catalog cards, newest entry first.
type WatchlistCollection struct {
	Collection[CatalogItem]
}

// WatchlistCollectionOutput is the listWatchlist response.
type WatchlistCollectionOutput struct {
	Body WatchlistCollection
}

// WatchlistEntry is one item's membership in the profile's watchlist.
type WatchlistEntry struct {
	ItemID  ID      `json:"item_id" doc:"The catalog item" example:"movie:heat-1995"`
	AddedAt Instant `json:"added_at" doc:"When the item joined the watchlist" example:"2026-01-02T03:04:05.000Z"`
}

// WatchlistEntryOutput is the getWatchlistEntry response.
type WatchlistEntryOutput struct {
	Body WatchlistEntry
}

const opListWatchlist = "listWatchlist"

func registerWatchlist(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	viewer := func(op huma.Operation) Operation {
		return Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true}
	}
	mutation := func(op huma.Operation) Operation {
		op.DefaultStatus = http.StatusNoContent
		return Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true}
	}

	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/watchlist", opListWatchlist, "watchlist",
		"List the acting profile's watchlist as catalog cards, newest entry first; fully-watched series and items the viewer may not see are omitted.")),
		func(ctx context.Context, in *WatchlistListInput) (*WatchlistCollectionOutput, error) {
			return reg.listWatchlist(ctx, cursors, in)
		})
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/watchlist/{item_id}", "getWatchlistEntry", "watchlist",
		"Answer whether the item is on the acting profile's watchlist: the entry, or 404 when it is not (or the viewer may not see it).")),
		reg.getWatchlistEntry)
	Register(reg, mutation(humaOp(http.MethodPut, Prefix+"/watchlist/{item_id}", "addToWatchlist", "watchlist",
		"Add the item to the acting profile's watchlist. Adding an item that is already on it is a no-op, so a retry converges.")),
		reg.addToWatchlist)
	Register(reg, mutation(humaOp(http.MethodDelete, Prefix+"/watchlist/{item_id}", "deleteWatchlistEntry", "watchlist",
		"Remove the item from the acting profile's watchlist; succeeds whether or not it was on it, so a retry converges.")),
		reg.deleteWatchlistEntry)
}

// listWatchlist answers from the same listing v1 GET /watchlist uses. The
// store pages by offset; the cursor carries the offset, and a limit+1 probe
// decides has_more from the raw rows, so a hidden series, an entry the
// catalog no longer has, or one the viewer may not see never hides the rows
// behind it.
func (reg *Registry) listWatchlist(ctx context.Context, cursors *Cursors, in *WatchlistListInput) (*WatchlistCollectionOutput, error) {
	if reg.deps.PersonalLists == nil {
		return nil, unavailable("watchlist")
	}
	viewer, p := personalListViewer(ctx, in.ImageSize)
	if p != nil {
		return nil, p
	}
	scope := personalListScope(ctx, opListWatchlist, viewer)
	offset, p := decodeOffset(cursors, scope, in.Cursor)
	if p != nil {
		return nil, p
	}
	entries, cards, err := reg.deps.PersonalLists.ListWatchlist(ctx, viewer, in.Limit+1, offset)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items, next, p := personalListPage(cursors, scope, in.Limit, offset, entries, cards, func(e userstore.WatchlistEntry) string { return e.MediaItemID })
	if p != nil {
		return nil, p
	}
	return &WatchlistCollectionOutput{Body: WatchlistCollection{Collection: Paginated(items, next)}}, nil
}

func (reg *Registry) getWatchlistEntry(ctx context.Context, in *WatchlistItemInput) (*WatchlistEntryOutput, error) {
	if reg.deps.PersonalLists == nil {
		return nil, unavailable("watchlist")
	}
	viewer, p := personalListViewer(ctx, "")
	if p != nil {
		return nil, p
	}
	entry, found, err := reg.deps.PersonalLists.GetWatchlistEntry(ctx, viewer, string(in.ItemID))
	if err != nil {
		return nil, serviceProblem(err)
	}
	if !found {
		return nil, NewProblem(TypeNotFound, "The item is not on the profile's watchlist.")
	}
	added, p := storeInstant(entry.AddedAt)
	if p != nil {
		return nil, p
	}
	return &WatchlistEntryOutput{Body: WatchlistEntry{ItemID: ID(entry.MediaItemID), AddedAt: added}}, nil
}

func (reg *Registry) addToWatchlist(ctx context.Context, in *WatchlistItemInput) (*struct{}, error) {
	if reg.deps.PersonalLists == nil {
		return nil, unavailable("watchlist")
	}
	viewer, p := personalListViewer(ctx, "")
	if p != nil {
		return nil, p
	}
	if err := reg.deps.PersonalLists.AddToWatchlist(ctx, viewer, string(in.ItemID)); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

func (reg *Registry) deleteWatchlistEntry(ctx context.Context, in *WatchlistItemInput) (*struct{}, error) {
	if reg.deps.PersonalLists == nil {
		return nil, unavailable("watchlist")
	}
	viewer, p := personalListViewer(ctx, "")
	if p != nil {
		return nil, p
	}
	if err := reg.deps.PersonalLists.RemoveFromWatchlist(ctx, viewer, string(in.ItemID)); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

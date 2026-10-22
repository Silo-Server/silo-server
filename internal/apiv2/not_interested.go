package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The not-interested domain: the movies and series the acting profile marked
// Not interested. A mark keeps the title out of the profile's recommendation
// and discovery rows and ranks similar titles lower there; library browsing,
// search, item detail, playback and the profile's favorites and watchlist are
// unchanged. It pages like the favorites domain (favorites.go).

// NotInterestedService is the slice of *handlers.PersonalDataHandler the
// not-interested operations use. Every method acts as the viewer's profile
// and returns an *handlers.APIError on failure.
type NotInterestedService interface {
	// ListNotInterestedPage answers at most limit of the profile's marks
	// ordered by (added_at DESC, media_item_id DESC) strictly after the key
	// (nil = from the newest), and the cards of the marked titles the viewer
	// may see, in the same order.
	ListNotInterestedPage(ctx context.Context, viewer handlers.PersonalListViewer, after *userstore.ListKey, limit int) ([]userstore.NotInterestedEntry, []handlers.CollectionItemView, error)
	// GetNotInterested answers the mark of an item the viewer may see; found
	// is false when the item is not marked.
	GetNotInterested(ctx context.Context, viewer handlers.PersonalListViewer, itemID string) (entry userstore.NotInterestedEntry, found bool, err error)
	MarkNotInterested(ctx context.Context, viewer handlers.PersonalListViewer, itemID string) error
	UnmarkNotInterested(ctx context.Context, viewer handlers.PersonalListViewer, itemID string) error
}

// NotInterestedListInput is the listNotInterested query.
type NotInterestedListInput struct {
	ArtworkQuery
	LimitParam
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvIjo1MH0"`
}

// NotInterestedItemInput names one movie or series.
type NotInterestedItemInput struct {
	ItemID ID `path:"item_id" doc:"Catalog item identifier of a movie or series" example:"movie:heat-1995"`
}

// NotInterestedCollection is the named envelope the contract carries: the
// marked titles as catalog cards, newest mark first.
type NotInterestedCollection struct {
	Collection[CatalogItem]
}

// NotInterestedCollectionOutput is the listNotInterested response.
type NotInterestedCollectionOutput struct {
	Body NotInterestedCollection
}

// NotInterestedEntry is one title's Not interested mark.
type NotInterestedEntry struct {
	ItemID   ID      `json:"item_id" doc:"The movie or series" example:"movie:heat-1995"`
	MarkedAt Instant `json:"marked_at" doc:"When the profile marked it" example:"2026-01-02T03:04:05.000Z"`
}

// NotInterestedEntryOutput is the getNotInterested response.
type NotInterestedEntryOutput struct {
	Body NotInterestedEntry
}

// NotInterestedCapability reports whether profiles can mark titles Not
// interested.
type NotInterestedCapability struct {
	Capability
}

// NotInterestedCapabilityOutput is the getNotInterestedCapability response.
type NotInterestedCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         NotInterestedCapability
}

const opListNotInterested = "listNotInterested"

func registerNotInterested(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)

	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/capabilities/not-interested", "getNotInterestedCapability", "not-interested",
		"Whether the acting profile can mark movies and series Not interested.")), reg.getNotInterestedCapability)
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/not-interested", opListNotInterested, "not-interested",
		"List the movies and series the acting profile marked Not interested as catalog cards, newest mark first; items the viewer may not see are omitted.")),
		func(ctx context.Context, in *NotInterestedListInput) (*NotInterestedCollectionOutput, error) {
			return reg.listNotInterested(ctx, cursors, in)
		})
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/not-interested/{item_id}", "getNotInterested", "not-interested",
		"Answer whether the acting profile marked the item Not interested: the mark, or 404 when it did not (or the viewer may not see the item).")),
		reg.getNotInterested)
	Register(reg, viewerNoContentMutation(humaOp(http.MethodPut, Prefix+"/not-interested/{item_id}", "markNotInterested", "not-interested",
		"Mark a movie or series Not interested for the acting profile: its recommendation and discovery rows leave it out and rank similar titles lower, after a rebuild that starts within seconds. Library browsing, search, item detail, playback, favorites and the watchlist are unchanged. Episodes and seasons are refused with 422; mark the series. Marking a marked title succeeds and changes nothing.")),
		reg.markNotInterested)
	Register(reg, viewerNoContentMutation(humaOp(http.MethodDelete, Prefix+"/not-interested/{item_id}", "unmarkNotInterested", "not-interested",
		"Clear the acting profile's Not interested mark on the item. An absent mark succeeds and changes nothing.")),
		reg.unmarkNotInterested)
}

func (reg *Registry) getNotInterestedCapability(_ context.Context, _ *CapabilityInput) (*NotInterestedCapabilityOutput, error) {
	state := StateAvailable
	if reg.deps.NotInterested == nil {
		state = StateUnsupported
	}
	return &NotInterestedCapabilityOutput{CacheControl: cacheControlPrivateNoCache, Body: NotInterestedCapability{Capability: Capability{State: state}}}, nil
}

func (reg *Registry) listNotInterested(ctx context.Context, cursors *Cursors, in *NotInterestedListInput) (*NotInterestedCollectionOutput, error) {
	if reg.deps.NotInterested == nil {
		return nil, unavailable("not-interested")
	}
	viewer, p := personalListViewer(ctx, in.Artwork())
	if p != nil {
		return nil, p
	}
	scope := personalListScope(ctx, opListNotInterested, viewer)
	after, p := decodeListKey(cursors, scope, in.Cursor)
	if p != nil {
		return nil, p
	}
	entries, cards, err := reg.deps.NotInterested.ListNotInterestedPage(ctx, viewer, after, in.Limit+1)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items, next, p := personalListPage(cursors, scope, in.Limit, entries, cards, reg.ratingSelection(ctx), func(e userstore.NotInterestedEntry) userstore.ListKey {
		return userstore.ListKey{AddedAt: e.AddedAt, MediaItemID: e.MediaItemID}
	})
	if p != nil {
		return nil, p
	}
	return &NotInterestedCollectionOutput{Body: NotInterestedCollection{Collection: Paginated(items, next)}}, nil
}

func (reg *Registry) getNotInterested(ctx context.Context, in *NotInterestedItemInput) (*NotInterestedEntryOutput, error) {
	if reg.deps.NotInterested == nil {
		return nil, unavailable("not-interested")
	}
	viewer, p := personalListViewer(ctx, imagesize.Request{})
	if p != nil {
		return nil, p
	}
	entry, found, err := reg.deps.NotInterested.GetNotInterested(ctx, viewer, string(in.ItemID))
	if err != nil {
		return nil, serviceProblem(err)
	}
	if !found {
		return nil, NewProblem(TypeNotFound, "The profile has not marked the item Not interested.")
	}
	marked, p := storeInstant(entry.AddedAt)
	if p != nil {
		return nil, p
	}
	return &NotInterestedEntryOutput{Body: NotInterestedEntry{ItemID: ID(entry.MediaItemID), MarkedAt: marked}}, nil
}

func (reg *Registry) markNotInterested(ctx context.Context, in *NotInterestedItemInput) (*struct{}, error) {
	if reg.deps.NotInterested == nil {
		return nil, unavailable("not-interested")
	}
	viewer, p := personalListViewer(ctx, imagesize.Request{})
	if p != nil {
		return nil, p
	}
	if err := reg.deps.NotInterested.MarkNotInterested(ctx, viewer, string(in.ItemID)); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

func (reg *Registry) unmarkNotInterested(ctx context.Context, in *NotInterestedItemInput) (*struct{}, error) {
	if reg.deps.NotInterested == nil {
		return nil, unavailable("not-interested")
	}
	viewer, p := personalListViewer(ctx, imagesize.Request{})
	if p != nil {
		return nil, p
	}
	if err := reg.deps.NotInterested.UnmarkNotInterested(ctx, viewer, string(in.ItemID)); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

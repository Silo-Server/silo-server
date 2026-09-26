package apiv2

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const collectionItemTiebreaker = "media_item_id"

type PersonalCollectionItemsInput struct {
	ID ID `path:"id"`
	LimitParam
	Cursor string `query:"cursor"`
}
type LibraryCollectionItemsInput struct {
	ID           ID `path:"id"`
	CollectionID ID `path:"collection_id"`
	LimitParam
	Cursor string `query:"cursor"`
}
type PersonalCollectionItem struct {
	Title        string  `json:"title,omitempty" doc:"Catalog title when the item is available."`
	CollectionID ID      `json:"collection_id"`
	MediaItemID  ID      `json:"media_item_id"`
	Position     int     `json:"position"`
	AddedAt      Instant `json:"added_at"`
}
type PersonalCollectionItemsOutput struct {
	Body Collection[PersonalCollectionItem]
}
type collectionContinuation struct {
	Query    *catalogsvc.QueryCursor          `json:"q,omitempty"`
	Revision int64                            `json:"r"`
	Position userstore.CollectionItemPosition `json:"p"`
}

func registerCollectionPaging(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/collections/{id}/items", "getCollectionItems", "collections", "Page collection items in stable order. Restart after a collection change."), Class: ClassProfileScoped, ServiceBacked: true}, func(ctx context.Context, in *PersonalCollectionItemsInput) (*PersonalCollectionItemsOutput, error) {
		return reg.personalCollectionPage(ctx, cursors, in)
	})
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/library/{id}/collections/{collection_id}/items", "getLibraryCollectionItems", "libraries", "Page visible library collection items in stable order. Restart after a collection change."), Class: ClassProfileScoped, ServiceBacked: true}, func(ctx context.Context, in *LibraryCollectionItemsInput) (*CatalogItemCollectionOutput, error) {
		return reg.libraryCollectionPage(ctx, cursors, in)
	})
}
func collectionPageScope(ctx context.Context, operation, filter string) CursorScope {
	return CursorScope{OperationID: operation, Security: strconv.Itoa(claimsFrom(ctx).UserID) + ":" + profileFrom(ctx) + ":" + viewerScopeDigest(ctx), Filter: filter, Sort: "position", Tiebreaker: collectionItemTiebreaker}
}
func collectionPageOptions(c *Cursors, s CursorScope, cursor string, limit int) (userstore.CollectionItemsPageOptions, *Problem) {
	o := userstore.CollectionItemsPageOptions{Limit: limit}
	if cursor != "" {
		var pos collectionContinuation
		if p := c.Decode(s, cursor, &pos); p != nil {
			return o, p
		}
		if pos.Revision <= 0 || (pos.Position.MediaItemID == "" && pos.Query == nil) {
			return o, NewProblem(TypeInvalidCursor, "The continuation has no collection witness.")
		}
		o.Revision = pos.Revision
		if pos.Query == nil {
			o.After = &pos.Position
		}
	}
	return o, nil
}
func collectionPageNext(c *Cursors, s CursorScope, more bool, revision int64, last *userstore.CollectionItemPosition, query *catalogsvc.QueryCursor) (string, error) {
	if !more {
		return "", nil
	}
	if query != nil {
		return c.Encode(s, collectionContinuation{Revision: revision, Query: query})
	}
	if last == nil {
		return "", NewProblem(TypeInternalError, "The page has no continuation witness.")
	}
	return c.Encode(s, collectionContinuation{Revision: revision, Position: *last})
}
func (reg *Registry) personalCollectionPage(ctx context.Context, c *Cursors, in *PersonalCollectionItemsInput) (*PersonalCollectionItemsOutput, error) {
	s, ok := reg.deps.PersonalCollections.(interface {
		PersonalCollectionItemsPage(context.Context, int, string, string, catalogsvc.AccessFilter, userstore.CollectionItemsPageOptions, *catalogsvc.QueryCursor) (handlers.PersonalCollectionPageView, error)
	})
	if !ok {
		return nil, unavailable("collection paging")
	}
	u, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	scope := collectionPageScope(ctx, "getCollectionItems", string(in.ID))
	opts, p := collectionPageOptions(c, scope, in.Cursor, in.Limit)
	if p != nil {
		return nil, p
	}
	v, e := s.PersonalCollectionItemsPage(ctx, u, profileFrom(ctx), string(in.ID), handlers.AccessFilterFromContext(ctx, ""), opts, collectionQueryPosition(c, scope, in.Cursor))
	if e != nil {
		return nil, collectionProblem(e)
	}
	items := make([]PersonalCollectionItem, 0, len(v.Items))
	for _, i := range v.Items {
		stamp := instantOfStamp(i.AddedAt)
		if stamp == nil {
			return nil, NewProblem(TypeInternalError, "The membership has an invalid timestamp.")
		}
		items = append(items, PersonalCollectionItem{CollectionID: ID(i.CollectionID), MediaItemID: ID(i.MediaItemID), Title: i.Title, Position: i.Position, AddedAt: *stamp})
	}
	next, e := collectionPageNext(c, scope, v.HasMore, v.Revision, v.Last, v.Query)
	if e != nil {
		return nil, e
	}
	return &PersonalCollectionItemsOutput{Body: Paginated(items, next)}, nil
}
func (reg *Registry) libraryCollectionPage(ctx context.Context, c *Cursors, in *LibraryCollectionItemsInput) (*CatalogItemCollectionOutput, error) {
	s, ok := reg.deps.LibraryCollections.(interface {
		LibraryCollectionItemsPage(context.Context, int, string, catalogsvc.AccessFilter, userstore.CollectionItemsPageOptions, *catalogsvc.QueryCursor) (handlers.LibraryCollectionPageView, error)
	})
	if !ok {
		return nil, unavailable("collection paging")
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	scope := collectionPageScope(ctx, "getLibraryCollectionItems", string(in.ID)+":"+string(in.CollectionID))
	opts, p := collectionPageOptions(c, scope, in.Cursor, in.Limit)
	if p != nil {
		return nil, p
	}
	v, e := s.LibraryCollectionItemsPage(ctx, id, string(in.CollectionID), handlers.AccessFilterFromContext(ctx, ""), opts, collectionQueryPosition(c, scope, in.Cursor))
	if e != nil {
		return nil, collectionProblem(e)
	}
	items := make([]CatalogItem, 0, len(v.Items))
	for _, i := range v.Items {
		items = append(items, catalogItemOfListing(i))
	}
	next, e := collectionPageNext(c, scope, v.HasMore, v.Revision, v.Last, v.Query)
	if e != nil {
		return nil, e
	}
	return &CatalogItemCollectionOutput{Body: CatalogItemCollection{Collection: Paginated(items, next)}}, nil
}

func collectionQueryPosition(c *Cursors, s CursorScope, cursor string) *catalogsvc.QueryCursor {
	if cursor == "" {
		return nil
	}
	var p collectionContinuation
	if c.Decode(s, cursor, &p) != nil {
		return nil
	}
	return p.Query
}

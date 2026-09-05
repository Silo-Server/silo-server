package apiv2

import (
	"context"
	"net/http"
	"strconv"
)

// The ratings domain: the acting profile's star ratings of catalog items.

// RatingListInput is the listRatings query.
type RatingListInput struct {
	LimitParam
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvIjo1MH0"`
}

// RatingItemInput names one rated catalog item.
type RatingItemInput struct {
	ItemID ID `path:"item_id" doc:"Catalog item identifier" example:"movie:heat-1995"`
}

// RatingEntry is the profile's rating of one item.
type RatingEntry struct {
	ItemID  ID      `json:"item_id" doc:"The catalog item" example:"movie:heat-1995"`
	Rating  int     `json:"rating" doc:"Stars, 1 to 5" example:"4"`
	RatedAt Instant `json:"rated_at" doc:"When the rating was last set" example:"2026-01-02T03:04:05.000Z"`
}

// RatingCollection is the named envelope the contract carries.
type RatingCollection struct {
	Collection[RatingEntry]
}

// RatingCollectionOutput is the listRatings response.
type RatingCollectionOutput struct {
	Body RatingCollection
}

const opListRatings = "listRatings"

func registerRatings(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/ratings", opListRatings, "ratings",
			"List the acting profile's ratings, most recently rated first."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, func(ctx context.Context, in *RatingListInput) (*RatingCollectionOutput, error) {
		return reg.listRatings(ctx, cursors, in)
	})
	del := humaOp(http.MethodDelete, Prefix+"/ratings/{item_id}", "deleteRating", "ratings",
		"Remove the acting profile's rating of the item; succeeds whether or not one existed, so a retry converges.")
	del.DefaultStatus = http.StatusNoContent
	Register(reg, Operation{Operation: del, Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true}, reg.deleteRating)
}

// listRatings answers from the same listing v1 GET /ratings uses. The store
// pages by offset; the cursor carries the offset and a limit+1 probe decides
// has_more.
func (reg *Registry) listRatings(ctx context.Context, cursors *Cursors, in *RatingListInput) (*RatingCollectionOutput, error) {
	if reg.deps.Ratings == nil {
		return nil, unavailable("ratings")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	scope := CursorScope{OperationID: opListRatings, Security: strconv.Itoa(userID) + "/" + profileID, Sort: "store", Tiebreaker: "store"}
	offset, p := decodeOffset(cursors, scope, in.Cursor)
	if p != nil {
		return nil, p
	}
	ratings, err := reg.deps.Ratings.ListRatings(ctx, userID, profileID, in.Limit+1, offset)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]RatingEntry, 0, len(ratings))
	for _, r := range ratings {
		items = append(items, RatingEntry{ItemID: ID(r.MediaItemID), Rating: r.Rating, RatedAt: NewInstant(r.RatedAt)})
	}
	items, next, p := offsetPage(cursors, scope, len(ratings), in.Limit, offset, items)
	if p != nil {
		return nil, p
	}
	return &RatingCollectionOutput{Body: RatingCollection{Collection: Paginated(items, next)}}, nil
}

func (reg *Registry) deleteRating(ctx context.Context, in *RatingItemInput) (*struct{}, error) {
	if reg.deps.Ratings == nil {
		return nil, unavailable("ratings")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	if err := reg.deps.Ratings.DeleteRating(ctx, userID, profileID, string(in.ItemID)); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

package apiv2

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	mediacatalog "github.com/Silo-Server/silo-server/internal/catalog"
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

// RatingEntryOutput is the getRating response.
type RatingEntryOutput struct {
	Body RatingEntry
}

// RatingSet is the setRating body: the stars to record.
type RatingSet struct {
	Rating int `json:"rating" minimum:"1" maximum:"5" doc:"Stars, 1 to 5" example:"4"`
}

// RatingSetInput names the item and carries the rating to set.
type RatingSetInput struct {
	ItemID ID `path:"item_id" doc:"Catalog item identifier" example:"movie:heat-1995"`
	Body   RatingSet
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
			"List the acting profile's ratings, most recently rated first; items the viewer may not see are omitted."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, func(ctx context.Context, in *RatingListInput) (*RatingCollectionOutput, error) {
		return reg.listRatings(ctx, cursors, in)
	})
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/ratings/{item_id}", "getRating", "ratings",
			"Answer the acting profile's rating of the item, or 404 when the profile has not rated it (or the viewer may not see it)."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, reg.getRating)
	set := humaOp(http.MethodPut, Prefix+"/ratings/{item_id}", "setRating", "ratings",
		"Set the acting profile's rating of the item, replacing any earlier rating; automatic retries repeat the rating timestamp and recommendation refresh.")
	set.DefaultStatus = http.StatusNoContent
	Register(reg, Operation{Operation: set, Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}, reg.setRating)
	del := humaOp(http.MethodDelete, Prefix+"/ratings/{item_id}", "deleteRating", "ratings",
		"Remove the acting profile's rating of the item; an absent rating succeeds, but automatic retries repeat the recommendation refresh.")
	del.DefaultStatus = http.StatusNoContent
	Register(reg, Operation{Operation: del, Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}, reg.deleteRating)
}

// getRating answers the rating of an item the viewer may see; an item
// outside the viewer's access is a 404, rated or not. The v2 listener reads
// no device header, so the access filter carries no device id, as the
// favorites operations do.
func (reg *Registry) getRating(ctx context.Context, in *RatingItemInput) (*RatingEntryOutput, error) {
	if reg.deps.Ratings == nil {
		return nil, unavailable("ratings")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	r, found, err := reg.deps.Ratings.GetRating(ctx, userID, profileID, string(in.ItemID), handlers.AccessFilterFromContext(ctx, ""))
	if err != nil {
		return nil, serviceProblem(err)
	}
	if !found {
		return nil, NewProblem(TypeNotFound, "The profile has not rated the item.")
	}
	return &RatingEntryOutput{Body: RatingEntry{ItemID: ID(r.MediaItemID), Rating: r.Rating, RatedAt: NewInstant(r.RatedAt)}}, nil
}

// setRating records the rating for an item the viewer may see, with the
// same access filter getRating uses.
func (reg *Registry) setRating(ctx context.Context, in *RatingSetInput) (*struct{}, error) {
	if reg.deps.Ratings == nil {
		return nil, unavailable("ratings")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	if err := reg.deps.Ratings.SetRating(ctx, userID, profileID, string(in.ItemID), handlers.AccessFilterFromContext(ctx, ""), in.Body.Rating); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

// ratingPosition is the ratings cursor payload: the keyset (rated_at,
// media_item_id) of the last entry the previous page emitted, rated_at in
// RFC 3339 with nanoseconds so the store's own precision round-trips. The
// next page resumes strictly after it, so a rating set or removed between
// pages neither repeats nor hides a row, and equal timestamps are ordered by
// the unique item id.
type ratingPosition struct {
	RatedAt     string `json:"r"`
	MediaItemID string `json:"m"`
}

// listRatings answers the profile's ratings of items the viewer may see,
// paged by keyset; a limit+1 probe decides has_more. The store filters by
// the viewer's access in the page query, so the probe and the cursor row are
// always visible items and a cursor never names a title the viewer may not
// see. The cursor is
// bound to the viewer policy that filtered the page, as the favorites cursor
// is.
func (reg *Registry) listRatings(ctx context.Context, cursors *Cursors, in *RatingListInput) (*RatingCollectionOutput, error) {
	if reg.deps.Ratings == nil {
		return nil, unavailable("ratings")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	scope := ratingsScope(ctx, userID, profileID)
	var after *mediacatalog.RatingKey
	if in.Cursor != "" {
		var pos ratingPosition
		if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
			return nil, p
		}
		ratedAt, err := time.Parse(time.RFC3339Nano, pos.RatedAt)
		if err != nil {
			return nil, NewProblem(TypeInvalidCursor, "The cursor is malformed, tampered with, or belongs to a different query.")
		}
		after = &mediacatalog.RatingKey{RatedAt: ratedAt, MediaItemID: pos.MediaItemID}
	}
	ratings, err := reg.deps.Ratings.ListRatingsPage(ctx, userID, profileID, handlers.AccessFilterFromContext(ctx, ""), after, in.Limit+1)
	if err != nil {
		return nil, serviceProblem(err)
	}
	next := ""
	if len(ratings) > in.Limit {
		ratings = ratings[:in.Limit]
		last := ratings[len(ratings)-1]
		if next, err = cursors.Encode(scope, ratingPosition{RatedAt: last.RatedAt.UTC().Format(time.RFC3339Nano), MediaItemID: last.MediaItemID}); err != nil {
			return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
	}
	items := make([]RatingEntry, 0, len(ratings))
	for _, r := range ratings {
		items = append(items, RatingEntry{ItemID: ID(r.MediaItemID), Rating: r.Rating, RatedAt: NewInstant(r.RatedAt)})
	}
	return &RatingCollectionOutput{Body: RatingCollection{Collection: Paginated(items, next)}}, nil
}

// ratingsScope binds a ratings cursor to the profile and the viewer policy
// that filters its rows, and to the keyset it pages by.
func ratingsScope(ctx context.Context, userID int, profileID string) CursorScope {
	return CursorScope{
		OperationID: opListRatings,
		Security:    strconv.Itoa(userID) + "/" + profileID + "/" + viewerScopeDigest(ctx),
		Sort:        "-rated_at,-item_id",
		Tiebreaker:  tiebreakerItemID,
	}
}

// deleteRating runs no access check, as v1 does: a profile can drop its own
// rating of an item that has since left its scope, and the 204 is the same
// whether or not a rating existed, so it reveals nothing about the item.
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

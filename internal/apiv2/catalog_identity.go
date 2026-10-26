package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

const opResolveCatalogIdentities = "resolveCatalogIdentities"

type catalogIdentityService interface {
	ResolveIdentities(context.Context, handlers.ItemViewer, catalogpkg.IdentityRequest) (handlers.CatalogIdentityView, error)
}
type CatalogIdentityInput struct {
	ArtworkQuery
	Body CatalogIdentityQuery
}
type CatalogIdentitySeed struct {
	Type   string `json:"type" enum:"movie,series"`
	TmdbID string `json:"tmdb_id,omitempty" maxLength:"64"`
	ImdbID string `json:"imdb_id,omitempty" maxLength:"64"`
	TvdbID string `json:"tvdb_id,omitempty" maxLength:"64"`
}
type CatalogIdentityQuery struct {
	Seeds  []CatalogIdentitySeed `json:"seeds" minItems:"1" maxItems:"48" doc:"Movie or series identities, in caller order. At least one ID is required for each seed."`
	Limit  int                   `json:"limit,omitempty" minimum:"1" maximum:"100" doc:"Candidates examined per page; default 100. Conflicting candidates count toward this bound."`
	Cursor string                `json:"cursor,omitempty"`
}
type CatalogIdentityMatch struct {
	Seed int         `json:"seed" doc:"Zero-based request seed index"`
	Item CatalogItem `json:"item"`
}
type CatalogIdentityCollection struct {
	Matches    []CatalogIdentityMatch `json:"matches" doc:"Authorized native catalog cards; duplicates remain separate. Empty, never null."`
	Complete   bool                   `json:"complete" doc:"All indexed candidates for this batch have been examined across pages. False also applies to empty pages when conflicts consumed the budget. No confirmed match does not prove a title is absent from the library."`
	NextCursor string                 `json:"next_cursor,omitempty" doc:"Continue with the same seeds, limit, artwork and profile."`
}
type CatalogIdentityOutput struct{ Body CatalogIdentityCollection }

func registerCatalogIdentity(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := humaOp(http.MethodPost, Prefix+"/catalog/resolve", opResolveCatalogIdentities, "catalog", "Resolve typed external identities to visible native catalog cards without scanning the library.")
	op.DefaultStatus = http.StatusOK
	operation := viewerOperation(op)
	operation.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, operation, func(ctx context.Context, in *CatalogIdentityInput) (*CatalogIdentityOutput, error) {
		return reg.resolveCatalogIdentities(ctx, cursors, in)
	})
}
func (reg *Registry) resolveCatalogIdentities(ctx context.Context, cursors *Cursors, in *CatalogIdentityInput) (*CatalogIdentityOutput, error) {
	service, ok := reg.deps.CatalogBrowse.(catalogIdentityService)
	if !ok || reg.deps.CatalogAccess == nil {
		return nil, unavailable("catalog identity resolution")
	}
	body := in.Body
	if len(body.Seeds) == 0 || len(body.Seeds) > catalogpkg.MaxIdentitySeeds {
		return nil, NewProblem(TypeValidationFailed, "Expected between 1 and 48 identity seeds.")
	}
	seeds := make([]catalogpkg.IdentitySeed, 0, len(body.Seeds))
	for _, input := range body.Seeds {
		seed := catalogpkg.IdentitySeed{Type: input.Type, TmdbID: input.TmdbID, ImdbID: input.ImdbID, TvdbID: input.TvdbID}
		seeds = append(seeds, seed)
		if !seed.Valid() {
			return nil, NewProblem(TypeValidationFailed, "Each identity seed needs a movie or series type and at least one provider ID.")
		}
	}
	limit := body.Limit
	if limit == 0 {
		limit = 100
	}
	viewer, p := reg.itemViewer(ctx, in.Artwork(), "", "")
	if p != nil {
		return nil, p
	}
	fingerprint, _ := json.Marshal(body.Seeds)
	scope := CursorScope{OperationID: opResolveCatalogIdentities, Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + viewer.ProfileID + "/" + viewerScopeDigest(ctx), Filter: string(fingerprint) + "/" + strconv.Itoa(limit) + "/" + in.ImageSize + in.widthsCursorFilter(), Sort: "seed,content_id", Tiebreaker: tiebreakerItemID}
	req := catalogpkg.IdentityRequest{Seeds: seeds, Limit: limit}
	if body.Cursor != "" {
		if p := cursors.Decode(scope, body.Cursor, &req.After); p != nil {
			return nil, p
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	view, err := service.ResolveIdentities(ctx, viewer, req)
	if err != nil {
		return nil, catalogProblem(err, "body.seeds")
	}
	out := CatalogIdentityCollection{Matches: []CatalogIdentityMatch{}, Complete: !view.HasMore}
	sel := reg.ratingSelection(ctx)
	for _, match := range view.Matches {
		out.Matches = append(out.Matches, CatalogIdentityMatch{Seed: match.Seed, Item: catalogItemOfListing(match.Item, sel)})
	}
	if view.HasMore {
		out.NextCursor, err = cursors.Encode(scope, view.Next)
		if err != nil {
			return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
	}
	return &CatalogIdentityOutput{Body: out}, nil
}

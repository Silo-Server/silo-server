package apiv2

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The title-allowances domain: the household's primary profile allows a
// limited profile specific movies and series above its rating or
// advisory-age limit, one by one or through a personal collection. An
// allowed title is visible and playable for that profile everywhere content
// is reached; a series allowance covers its seasons and episodes. Library
// access still applies. Every operation needs the acting profile to manage
// the household (the verified primary profile).

// TitleAllowanceService is the slice of *handlers.TitleAllowanceHandler the
// title-allowance operations use. Methods return *handlers.APIError on
// failure.
type TitleAllowanceService interface {
	Supported(ctx context.Context, userID int) bool
	ListAllowedTitles(ctx context.Context, actor handlers.AllowanceActor, profileID string) ([]userstore.TitleAllowance, []handlers.CollectionItemView, error)
	SetTitleAllowance(ctx context.Context, actor handlers.AllowanceActor, profileID, itemID string, allowed bool) error
	TitleAllowanceStates(ctx context.Context, actor handlers.AllowanceActor, itemID string) ([]handlers.ProfileAllowanceState, error)
	ListAllowedCollections(ctx context.Context, actor handlers.AllowanceActor, profileID string) ([]userstore.CollectionAllowance, error)
	SetCollectionAllowance(ctx context.Context, actor handlers.AllowanceActor, profileID, collectionID string, allowed bool) error
	AllowCollectionTitles(ctx context.Context, actor handlers.AllowanceActor, profileID, collectionID string) (int, error)
}

// TitleAllowanceCapability reports whether the server keeps title
// allowances for the signed-in account.
type TitleAllowanceCapability struct {
	Capability
}

// TitleAllowanceCapabilityOutput is the getTitleAllowanceCapability response.
type TitleAllowanceCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         TitleAllowanceCapability
}

// AllowedTitlesInput names a profile and the artwork the cards use.
type AllowedTitlesInput struct {
	ArtworkQuery
	ProfileID ID `path:"id" doc:"Profile identifier" example:"1"`
}

// AllowedTitleCollection is the profile's allowed titles as catalog cards,
// newest allowance first. Titles allowed only through a collection are not
// listed; titles the acting profile may not see are omitted.
type AllowedTitleCollection struct {
	Collection[CatalogItem]
}

// AllowedTitleCollectionOutput is the listAllowedTitles response.
type AllowedTitleCollectionOutput struct {
	Body AllowedTitleCollection
}

// AllowedTitleInput names a profile and a movie or series.
type AllowedTitleInput struct {
	ProfileID ID `path:"id" doc:"Profile identifier" example:"1"`
	ItemID    ID `path:"item_id" doc:"Catalog item identifier of a movie or series" example:"movie:heat-1995"`
}

// TitleAllowanceStatesInput names a movie or series.
type TitleAllowanceStatesInput struct {
	ItemID ID `path:"id" doc:"Catalog item identifier of a movie or series" example:"movie:heat-1995"`
}

// TitleAllowanceProfile is one other household profile's standing for a
// title.
type TitleAllowanceProfile struct {
	ProfileID        ID     `json:"profile_id" example:"2"`
	Name             string `json:"name" example:"Emma"`
	MaxContentRating string `json:"max_content_rating" doc:"The profile's content-rating ceiling; empty means none" example:"PG"`
	MaxAdvisoryAge   *int   `json:"max_advisory_age" nullable:"true" doc:"The profile's advisory-age limit; null means none" example:"10"`
	State            string `json:"state" enum:"within_limits,allowed,allowed_by_collection,above_limit,no_library_access" doc:"within_limits: the profile sees the title without an allowance. allowed: the profile has its own allowance for it. allowed_by_collection: an allowed collection holds it. above_limit: the title is above the profile's limits and can be allowed. no_library_access: the profile cannot see the title's library, so an allowance would not help" example:"above_limit"`
}

// TitleAllowanceProfileCollection lists every other profile on the account.
type TitleAllowanceProfileCollection struct {
	Collection[TitleAllowanceProfile]
}

// TitleAllowanceProfileCollectionOutput is the listTitleAllowanceProfiles
// response.
type TitleAllowanceProfileCollectionOutput struct {
	Body TitleAllowanceProfileCollection
}

// AllowedCollectionsInput names a profile.
type AllowedCollectionsInput struct {
	ProfileID ID `path:"id" doc:"Profile identifier" example:"1"`
}

// AllowedCollection is a personal collection allowed for a profile.
type AllowedCollection struct {
	CollectionID ID      `json:"collection_id" example:"0b9b5c1e-5d0e-4c55-8f5c-2a1d2f3e4b5c"`
	Name         string  `json:"name" example:"Family movie night"`
	ItemCount    int     `json:"item_count" example:"24"`
	Live         bool    `json:"live" doc:"Whether the collection allows its titles now: true while it is a manual collection the primary profile created. A collection that stops meeting that allows nothing until it does again" example:"true"`
	AllowedAt    Instant `json:"allowed_at" example:"2026-01-02T03:04:05.000Z"`
}

// AllowedCollectionCollection lists a profile's allowed collections, newest
// first.
type AllowedCollectionCollection struct {
	Collection[AllowedCollection]
}

// AllowedCollectionCollectionOutput is the listAllowedCollections response.
type AllowedCollectionCollectionOutput struct {
	Body AllowedCollectionCollection
}

// AllowedCollectionInput names a profile and a personal collection.
type AllowedCollectionInput struct {
	ProfileID    ID `path:"id" doc:"Profile identifier" example:"1"`
	CollectionID ID `path:"collection_id" doc:"Personal collection identifier" example:"0b9b5c1e-5d0e-4c55-8f5c-2a1d2f3e4b5c"`
}

// AllowCollectionTitlesInput is the allowCollectionTitles request.
type AllowCollectionTitlesInput struct {
	ProfileID ID `path:"id" doc:"Profile identifier" example:"1"`
	Body      struct {
		CollectionID ID `json:"collection_id" doc:"A personal collection the acting profile can see" example:"0b9b5c1e-5d0e-4c55-8f5c-2a1d2f3e4b5c"`
	}
}

// AllowCollectionTitlesResult reports how many title allowances were added.
type AllowCollectionTitlesResult struct {
	Added int `json:"added" doc:"Movies and series newly allowed; titles already allowed are not counted" example:"12"`
}

// AllowCollectionTitlesOutput is the allowCollectionTitles response.
type AllowCollectionTitlesOutput struct {
	Body AllowCollectionTitlesResult
}

func registerTitleAllowances(reg *Registry) {
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/capabilities/title-allowances", "getTitleAllowanceCapability", "profiles",
		"Whether the household's primary profile can allow limited profiles specific titles above their rating limits.")), reg.getTitleAllowanceCapability)

	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/profiles/{id}/allowed-titles", "listAllowedTitles", "profiles",
		"List the movies and series allowed for a profile above its limits, as catalog cards, newest first. Titles allowed only through a collection are not listed. Household managers only.")),
		reg.listAllowedTitles)
	Register(reg, allowanceMutation(humaOp(http.MethodPut, Prefix+"/profiles/{id}/allowed-titles/{item_id}", "allowTitle", "profiles",
		"Allow a profile a movie or series above its rating or advisory-age limit. A series allowance covers its seasons and episodes. The profile must have a limit (422 no_maturity_limits) and reach the title's library (422 no_library_access); episodes and seasons are refused with 422. Allowing an allowed title succeeds and changes nothing. Household managers only.")),
		func(ctx context.Context, in *AllowedTitleInput) (*struct{}, error) {
			return reg.setTitleAllowance(ctx, in, true)
		})
	Register(reg, allowanceMutation(humaOp(http.MethodDelete, Prefix+"/profiles/{id}/allowed-titles/{item_id}", "disallowTitle", "profiles",
		"Remove a profile's allowance for a title. An absent allowance succeeds and changes nothing. A title an allowed collection holds stays allowed. Household managers only.")),
		func(ctx context.Context, in *AllowedTitleInput) (*struct{}, error) {
			return reg.setTitleAllowance(ctx, in, false)
		})
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/items/{id}/title-allowances", "listTitleAllowanceProfiles", "profiles",
		"For a movie or series, the standing of every other profile on the account: within its limits, allowed, or above its limits and allowable. Household managers only.")),
		reg.listTitleAllowanceProfiles)

	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/profiles/{id}/allowed-collections", "listAllowedCollections", "profiles",
		"List the personal collections allowed for a profile, newest first. Household managers only.")),
		reg.listAllowedCollections)
	Register(reg, allowanceMutation(humaOp(http.MethodPut, Prefix+"/profiles/{id}/allowed-collections/{collection_id}", "allowCollection", "profiles",
		"Allow a profile every title in a personal collection, including titles added later. Only a manual collection the acting primary profile created can be allowed (422 collection_not_live); copy other collections' titles with allowCollectionTitles. Household managers only.")),
		func(ctx context.Context, in *AllowedCollectionInput) (*struct{}, error) {
			return reg.setCollectionAllowance(ctx, in, true)
		})
	Register(reg, allowanceMutation(humaOp(http.MethodDelete, Prefix+"/profiles/{id}/allowed-collections/{collection_id}", "disallowCollection", "profiles",
		"Stop allowing a profile a personal collection. An absent allowance succeeds and changes nothing. Household managers only.")),
		func(ctx context.Context, in *AllowedCollectionInput) (*struct{}, error) {
			return reg.setCollectionAllowance(ctx, in, false)
		})
	copyOp := humaOp(http.MethodPost, Prefix+"/profiles/{id}/allowed-titles/copy-collection", "allowCollectionTitles", "profiles",
		"Allow a profile every movie and series a personal collection holds now, as individual title allowances. Titles added to the collection later are not allowed. Household managers only.")
	Register(reg, Operation{
		Operation:      copyOp,
		Class:          ClassProfileScoped,
		DemoRestricted: true,
		RetrySafety:    RetrySafetyNaturalIdempotent,
		ServiceBacked:  true,
		Audited:        true,
	}, reg.allowCollectionTitles)
}

// allowanceMutation is a bodiless 204 allowance write. Repeating it changes
// no row, so it is naturally idempotent.
func allowanceMutation(op huma.Operation) Operation {
	operation := viewerNoContentMutation(op)
	operation.DemoRestricted = true
	operation.Audited = true
	return operation
}

func (reg *Registry) allowanceActor(ctx context.Context, art imagesize.Request) (handlers.AllowanceActor, *Problem) {
	viewer, p := personalListViewer(ctx, art)
	if p != nil {
		return handlers.AllowanceActor{}, p
	}
	return handlers.AllowanceActor{
		UserID:          viewer.UserID,
		ActiveProfileID: viewer.ProfileID,
		VerifyProfile:   verifyHouseholdProfile(ctx),
		Viewer:          viewer,
	}, nil
}

func (reg *Registry) getTitleAllowanceCapability(ctx context.Context, _ *CapabilityInput) (*TitleAllowanceCapabilityOutput, error) {
	state := StateUnsupported
	if reg.deps.TitleAllowances != nil {
		if claims := claimsFrom(ctx); claims != nil && reg.deps.TitleAllowances.Supported(ctx, claims.UserID) {
			state = StateAvailable
		}
	}
	return &TitleAllowanceCapabilityOutput{CacheControl: cacheControlPrivateNoCache, Body: TitleAllowanceCapability{Capability: Capability{State: state}}}, nil
}

func (reg *Registry) listAllowedTitles(ctx context.Context, in *AllowedTitlesInput) (*AllowedTitleCollectionOutput, error) {
	if reg.deps.TitleAllowances == nil {
		return nil, unavailable("title allowances")
	}
	actor, p := reg.allowanceActor(ctx, in.Artwork())
	if p != nil {
		return nil, p
	}
	_, cards, err := reg.deps.TitleAllowances.ListAllowedTitles(ctx, actor, string(in.ProfileID))
	if err != nil {
		return nil, serviceProblem(err)
	}
	sel := reg.ratingSelection(ctx)
	items := make([]CatalogItem, 0, len(cards))
	for _, card := range cards {
		items = append(items, catalogItemOfListing(card, sel))
	}
	return &AllowedTitleCollectionOutput{Body: AllowedTitleCollection{Collection: NewCollection(items)}}, nil
}

func (reg *Registry) setTitleAllowance(ctx context.Context, in *AllowedTitleInput, allowed bool) (*struct{}, error) {
	if reg.deps.TitleAllowances == nil {
		return nil, unavailable("title allowances")
	}
	actor, p := reg.allowanceActor(ctx, imagesize.Request{})
	if p != nil {
		return nil, p
	}
	if err := reg.deps.TitleAllowances.SetTitleAllowance(ctx, actor, string(in.ProfileID), string(in.ItemID), allowed); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

func (reg *Registry) listTitleAllowanceProfiles(ctx context.Context, in *TitleAllowanceStatesInput) (*TitleAllowanceProfileCollectionOutput, error) {
	if reg.deps.TitleAllowances == nil {
		return nil, unavailable("title allowances")
	}
	actor, p := reg.allowanceActor(ctx, imagesize.Request{})
	if p != nil {
		return nil, p
	}
	states, err := reg.deps.TitleAllowances.TitleAllowanceStates(ctx, actor, string(in.ItemID))
	if err != nil {
		return nil, serviceProblem(err)
	}
	out := make([]TitleAllowanceProfile, 0, len(states))
	for _, s := range states {
		out = append(out, TitleAllowanceProfile{
			ProfileID:        ID(s.Profile.ID),
			Name:             s.Profile.Name,
			MaxContentRating: s.Profile.MaxContentRating,
			MaxAdvisoryAge:   advisoryAgeOf(s.Profile.MaxAdvisoryAge),
			State:            s.State,
		})
	}
	return &TitleAllowanceProfileCollectionOutput{Body: TitleAllowanceProfileCollection{Collection: NewCollection(out)}}, nil
}

func (reg *Registry) listAllowedCollections(ctx context.Context, in *AllowedCollectionsInput) (*AllowedCollectionCollectionOutput, error) {
	if reg.deps.TitleAllowances == nil {
		return nil, unavailable("title allowances")
	}
	actor, p := reg.allowanceActor(ctx, imagesize.Request{})
	if p != nil {
		return nil, p
	}
	rows, err := reg.deps.TitleAllowances.ListAllowedCollections(ctx, actor, string(in.ProfileID))
	if err != nil {
		return nil, serviceProblem(err)
	}
	out := make([]AllowedCollection, 0, len(rows))
	for _, row := range rows {
		allowedAt, p := storeInstant(row.CreatedAt)
		if p != nil {
			return nil, p
		}
		out = append(out, AllowedCollection{
			CollectionID: ID(row.CollectionID), Name: row.Name, ItemCount: row.ItemCount, Live: row.Live, AllowedAt: allowedAt,
		})
	}
	return &AllowedCollectionCollectionOutput{Body: AllowedCollectionCollection{Collection: NewCollection(out)}}, nil
}

func (reg *Registry) setCollectionAllowance(ctx context.Context, in *AllowedCollectionInput, allowed bool) (*struct{}, error) {
	if reg.deps.TitleAllowances == nil {
		return nil, unavailable("title allowances")
	}
	actor, p := reg.allowanceActor(ctx, imagesize.Request{})
	if p != nil {
		return nil, p
	}
	if err := reg.deps.TitleAllowances.SetCollectionAllowance(ctx, actor, string(in.ProfileID), string(in.CollectionID), allowed); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

func (reg *Registry) allowCollectionTitles(ctx context.Context, in *AllowCollectionTitlesInput) (*AllowCollectionTitlesOutput, error) {
	if reg.deps.TitleAllowances == nil {
		return nil, unavailable("title allowances")
	}
	actor, p := reg.allowanceActor(ctx, imagesize.Request{})
	if p != nil {
		return nil, p
	}
	added, err := reg.deps.TitleAllowances.AllowCollectionTitles(ctx, actor, string(in.ProfileID), string(in.Body.CollectionID))
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &AllowCollectionTitlesOutput{Body: AllowCollectionTitlesResult{Added: added}}, nil
}

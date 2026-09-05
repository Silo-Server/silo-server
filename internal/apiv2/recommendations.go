package apiv2

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// The recommendations domain: the profile-scoped personalised reads the
// Discover and For You pages draw on. Every recommended item is the shared
// CatalogItem card, rendered by the same seam the discover page uses, so a
// plain list (popular, recently added, because-you-watched) is a
// CatalogItemCollection and a grouped read is a list of RecommendationRow.
// The engine's score and reason members of v1 are not carried: no client
// reads them, and a card already says what the row is for.

// RecommendationRow is one recommendation row with its cards.
type RecommendationRow struct {
	Type  string        `json:"type" doc:"Row type: cluster, similar_users_liked, popular, recently_added, top_rated, genre_sampler" example:"cluster"`
	Title string        `json:"title" doc:"Display label" example:"Because you enjoy Crime"`
	Kind  string        `json:"kind,omitempty" doc:"Section kind for getRecommendationSection; absent when the row has no dedicated page" example:"cluster"`
	Key   string        `json:"key,omitempty" doc:"Section key paired with kind (cluster index or genre name)" example:"0"`
	Items []CatalogItem `json:"items" doc:"Empty, never null"`
}

// RecommendationRowOutput is a single-row response.
type RecommendationRowOutput struct {
	Body RecommendationRow
}

// RecommendationRowCollection is a list of rows.
type RecommendationRowCollection struct {
	Collection[RecommendationRow]
}

// RecommendationRowCollectionOutput is a multi-row response.
type RecommendationRowCollectionOutput struct {
	Body RecommendationRowCollection
}

// RecommendationLimitParam is the page size of a recommendation list.
type RecommendationLimitParam struct {
	Limit int `query:"limit" minimum:"1" maximum:"50" default:"20" doc:"Cards to answer; default 20, maximum 50" example:"20"`
}

// BecauseWatchedInput is the listBecauseWatched request.
type BecauseWatchedInput struct {
	ItemID string `path:"item_id" minLength:"1" doc:"The watched item recommendations are drawn from" example:"movie:heat-1995"`
	RecommendationLimitParam
}

// ForYouInput is the getForYouMain and listForYouRows query.
type ForYouInput struct {
	RecommendationLimitParam
}

// PopularInput is the listPopular query.
type PopularInput struct {
	Days int `query:"days" minimum:"1" default:"30" doc:"Window in days; default 30" example:"30"`
	RecommendationLimitParam
}

// RecentlyAddedInput is the listRecentlyAdded query.
type RecentlyAddedInput struct {
	Days int `query:"days" minimum:"1" default:"14" doc:"Window in days; default 14" example:"14"`
	RecommendationLimitParam
}

// RecommendationSectionInput is the getRecommendationSection request.
type RecommendationSectionInput struct {
	Kind  string `path:"kind" enum:"for-you-main,cluster,similar-users,popular,recently-added,top-rated,genre" doc:"Section kind, as a discover row's kind names it" example:"genre"`
	Key   string `query:"key" doc:"Section key: the cluster index of a cluster section or the genre name of a genre section" example:"Crime"`
	Limit int    `query:"limit" minimum:"1" maximum:"60" default:"60" doc:"Cards to answer; default and maximum 60" example:"60"`
}

func registerRecommendations(reg *Registry) {
	viewer := func(op huma.Operation) Operation {
		return Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true}
	}
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/recommendations/because-watched/{item_id}", "listBecauseWatched", "recommendations",
		"Cards recommended because the acting profile watched the item, minus what it has watched or rated low.")), reg.listBecauseWatched)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/recommendations/discover", "getDiscover", "recommendations",
		"The Discover page: the acting profile's recommendation rows with their cards, blended with upcoming airings.")), reg.getDiscover)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/recommendations/for-you/main", "getForYouMain", "recommendations",
		"The acting profile's main For You row; empty until the engine has a profile for it.")), reg.getForYouMain)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/recommendations/for-you/rows", "listForYouRows", "recommendations",
		"The acting profile's For You rows, one per taste cluster.")), reg.listForYouRows)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/recommendations/popular", "listPopular", "recommendations",
		"The server's most played items over the window, minus what the acting profile has watched.")), reg.listPopular)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/recommendations/recently-added", "listRecentlyAdded", "recommendations",
		"Items added over the window, minus what the acting profile has watched.")), reg.listRecentlyAdded)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/recommendations/section/{kind}", "getRecommendationSection", "recommendations",
		"One recommendation row in full, for a dedicated see-all page; empty when the profile has no such row.")), reg.getRecommendationSection)
}

func (reg *Registry) recommendationsService() (RecommendationService, *Problem) {
	if reg.deps.Recommendations == nil {
		return nil, unavailable("recommendations")
	}
	return reg.deps.Recommendations, nil
}

// recommendationRowOf renders a discover row; the cards are the shared
// section card.
func recommendationRowOf(v handlers.DiscoverRowView) RecommendationRow {
	return RecommendationRow{Type: v.Type, Title: v.Label, Kind: v.SectionKind, Key: v.SectionKey, Items: catalogItemsOfSection(v.Items)}
}

func catalogItemsOfSection(views []handlers.SectionItemView) []CatalogItem {
	items := make([]CatalogItem, 0, len(views))
	for _, v := range views {
		items = append(items, catalogItemOfSection(v))
	}
	return items
}

func (reg *Registry) recommendationCards(ctx context.Context, fetch func(svc RecommendationService, userID int, profileID string) ([]handlers.SectionItemView, error)) (*CatalogItemCollectionOutput, error) {
	svc, p := reg.recommendationsService()
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	views, err := fetch(svc, userID, profileID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &CatalogItemCollectionOutput{Body: CatalogItemCollection{Collection: NewCollection(catalogItemsOfSection(views))}}, nil
}

func (reg *Registry) listBecauseWatched(ctx context.Context, in *BecauseWatchedInput) (*CatalogItemCollectionOutput, error) {
	return reg.recommendationCards(ctx, func(svc RecommendationService, userID int, profileID string) ([]handlers.SectionItemView, error) {
		return svc.BecauseWatchedCards(ctx, userID, profileID, in.ItemID, in.Limit, handlers.ViewerAccessFilter(ctx, ""))
	})
}

func (reg *Registry) listPopular(ctx context.Context, in *PopularInput) (*CatalogItemCollectionOutput, error) {
	return reg.recommendationCards(ctx, func(svc RecommendationService, userID int, profileID string) ([]handlers.SectionItemView, error) {
		return svc.PopularCards(ctx, userID, profileID, in.Days, in.Limit, handlers.ViewerAccessFilter(ctx, ""))
	})
}

func (reg *Registry) listRecentlyAdded(ctx context.Context, in *RecentlyAddedInput) (*CatalogItemCollectionOutput, error) {
	return reg.recommendationCards(ctx, func(svc RecommendationService, userID int, profileID string) ([]handlers.SectionItemView, error) {
		return svc.RecentlyAddedCards(ctx, userID, profileID, in.Days, in.Limit, handlers.ViewerAccessFilter(ctx, ""))
	})
}

func (reg *Registry) getForYouMain(ctx context.Context, in *ForYouInput) (*RecommendationRowOutput, error) {
	svc, p := reg.recommendationsService()
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	row, err := svc.ForYouMainCards(ctx, userID, profileID, in.Limit, handlers.ViewerAccessFilter(ctx, ""))
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &RecommendationRowOutput{Body: recommendationRowOf(row)}, nil
}

func (reg *Registry) recommendationRows(ctx context.Context, fetch func(svc RecommendationService, userID int, profileID string) ([]handlers.DiscoverRowView, error)) (*RecommendationRowCollectionOutput, error) {
	svc, p := reg.recommendationsService()
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	views, err := fetch(svc, userID, profileID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	rows := make([]RecommendationRow, 0, len(views))
	for _, v := range views {
		rows = append(rows, recommendationRowOf(v))
	}
	return &RecommendationRowCollectionOutput{Body: RecommendationRowCollection{Collection: NewCollection(rows)}}, nil
}

func (reg *Registry) listForYouRows(ctx context.Context, in *ForYouInput) (*RecommendationRowCollectionOutput, error) {
	return reg.recommendationRows(ctx, func(svc RecommendationService, userID int, profileID string) ([]handlers.DiscoverRowView, error) {
		return svc.ForYouRowCards(ctx, userID, profileID, in.Limit, handlers.ViewerAccessFilter(ctx, ""))
	})
}

func (reg *Registry) getDiscover(ctx context.Context, _ *struct{}) (*RecommendationRowCollectionOutput, error) {
	return reg.recommendationRows(ctx, func(svc RecommendationService, userID int, profileID string) ([]handlers.DiscoverRowView, error) {
		view, err := svc.Discover(ctx, userID, profileID, handlers.ViewerAccessFilter(ctx, ""))
		if err != nil {
			return nil, err
		}
		return view.Rows, nil
	})
}

func (reg *Registry) getRecommendationSection(ctx context.Context, in *RecommendationSectionInput) (*RecommendationRowOutput, error) {
	svc, p := reg.recommendationsService()
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	if in.Key == "" && (in.Kind == recommendations.SectionKindCluster || in.Kind == recommendations.SectionKindGenre) {
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "query.key", Code: codeRequired, Detail: "a " + in.Kind + " section needs a key"})
	}
	view, err := svc.Section(ctx, userID, profileID, in.Kind, in.Key, in.Limit, handlers.ViewerAccessFilter(ctx, ""))
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &RecommendationRowOutput{Body: RecommendationRow{Type: view.Type, Title: view.Label, Kind: view.Kind, Key: view.Key, Items: catalogItemsOfSection(view.Items)}}, nil
}

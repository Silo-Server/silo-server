package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

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

// SimilarInput is the listSimilar request.
type SimilarInput struct {
	ItemID string `path:"item_id" minLength:"1" doc:"The item similar items are drawn from" example:"movie:heat-1995"`
	RecommendationLimitParam
}

// SimilarUsersInput is the listSimilarUsersLiked query.
type SimilarUsersInput struct {
	RecommendationLimitParam
}

// TasteProfile is the acting profile's taste summary; empty members, not
// an error, until the engine has computed one.
type TasteProfile struct {
	TopGenres         []string       `json:"top_genres" doc:"Strongest genres, best first; empty, never null"`
	FavoriteDirectors []string       `json:"favorite_directors" doc:"Empty, never null"`
	SignalCounts      map[string]int `json:"signal_counts" doc:"How many signals of each kind (rated, favorited, watched, …) fed the profile; empty, never null"`
	UpdatedAt         *Instant       `json:"updated_at,omitempty" doc:"When the profile was last computed; absent until it has been"`
}

// TasteProfileOutput is the getTasteProfile response.
type TasteProfileOutput struct {
	Body TasteProfile
}

// TasteSeedItemsInput is the listTasteSeedItems query.
type TasteSeedItemsInput struct {
	Limit  int    `query:"limit" minimum:"1" maximum:"60" default:"30" doc:"Cards per page; default 30, maximum 60" example:"30"`
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvIjozMH0"`
}

// TasteSeedSubmission is the createTasteSeed body: the items the profile
// picked in the taste-seeding picker.
type TasteSeedSubmission struct {
	ItemIDs []string `json:"item_ids" minItems:"1" maxItems:"200" doc:"Picked catalog identifiers; each is recorded as a favorite" example:"[\"movie:heat-1995\"]"`
}

// TasteSeedSubmitInput is the createTasteSeed request.
type TasteSeedSubmitInput struct {
	Body TasteSeedSubmission
}

// TasteSeedResult reports a taste-seed submission.
type TasteSeedResult struct {
	Added int `json:"added" doc:"Picks recorded as favorites by this submission" example:"3"`
}

// TasteSeedResultOutput is the createTasteSeed response.
type TasteSeedResultOutput struct {
	Body TasteSeedResult
}

// WatchTonightInput is the getWatchTonight query.
type WatchTonightInput struct {
	Limit int `query:"limit" minimum:"1" maximum:"20" default:"5" doc:"Cards to answer; default 5, maximum 20" example:"5"`
}

// WatchTonightItem is one Watch Tonight card: the shared card plus where
// it came from.
type WatchTonightItem struct {
	CatalogItem
	WatchTonightSource string `json:"watch_tonight_source" enum:"continue_watching,next_up,recommendation" doc:"Which pool the card came from" example:"recommendation"`
}

// WatchTonight is the merged Watch Tonight list.
type WatchTonight struct {
	Items  []WatchTonightItem `json:"items" doc:"Best first; empty, never null"`
	IsCold bool               `json:"is_cold" doc:"True when the profile has no taste profile and nothing in progress, so the list is a generic fallback" example:"false"`
}

// WatchTonightOutput is the getWatchTonight response.
type WatchTonightOutput struct {
	Body WatchTonight
}

// WatchTonightCardsInput is the listWatchTonightCards query.
type WatchTonightCardsInput struct {
	Mode       string   `query:"mode" required:"true" enum:"continue,discover" doc:"continue answers in-progress and next-up cards; discover answers taste candidates" example:"discover"`
	Genres     []string `query:"genres,explode" maxItems:"18" doc:"Discover-mode genre filter, repeated (genres=Crime&genres=Thriller); each must be a known genre" example:"Crime"`
	ExcludeIDs []string `query:"exclude_ids,explode" maxItems:"200" doc:"Catalog identifiers already swiped, repeated (exclude_ids=a&exclude_ids=b)" example:"movie:heat-1995"`
	Limit      int      `query:"limit" minimum:"1" maximum:"20" default:"12" doc:"Cards per page; default 12, maximum 20" example:"12"`
}

// WatchTonightCastMember is one cast member on a swipe card.
type WatchTonightCastMember struct {
	Name      string `json:"name" example:"Al Pacino"`
	Character string `json:"character,omitempty" example:"Lt. Vincent Hanna"`
	PhotoURL  string `json:"photo_url,omitempty" doc:"Presigned, short-lived"`
}

// WatchTonightCard is one swipe card: the shared card with its source and
// the cast for the card back.
type WatchTonightCard struct {
	CatalogItem
	WatchTonightSource string                   `json:"watch_tonight_source" enum:"continue_watching,next_up,recommendation" doc:"Which pool the card came from" example:"recommendation"`
	Cast               []WatchTonightCastMember `json:"cast" doc:"Up to four cast members; empty, never null"`
}

// WatchTonightCardPage is one page of swipe cards. Paging is by exclusion,
// not cursor: the client passes the identifiers it has swiped as
// exclude_ids, so has_more is a plain flag.
type WatchTonightCardPage struct {
	Items         []WatchTonightCard `json:"items" doc:"Empty, never null"`
	HasMore       bool               `json:"has_more" doc:"Whether eligible cards remain beyond this page; paging_limited may require restarting the swipe session" example:"true"`
	PagingLimited bool               `json:"paging_limited" doc:"True when eligible cards remain but the 200-card exclusion budget is full; restart with no exclusions" example:"false"`
	IsCold        bool               `json:"is_cold" doc:"True when the cards are a generic fallback rather than taste-driven" example:"false"`
}

// WatchTonightCardPageOutput is the listWatchTonightCards response.
type WatchTonightCardPageOutput struct {
	Body WatchTonightCardPage
}

const (
	watchTonightExclusionLimit = 200
	opListTasteSeedItems       = "listTasteSeedItems"
)

func registerRecommendations(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
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
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/recommendations/similar/{item_id}", "listSimilar", "recommendations",
		"Cards similar to the item by content; not personalised beyond the acting profile's access.")), reg.listSimilar)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/recommendations/similar-users", "listSimilarUsersLiked", "recommendations",
		"Cards profiles with similar taste liked.")), reg.listSimilarUsersLiked)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/recommendations/taste-profile", "getTasteProfile", "recommendations",
		"The acting profile's taste summary; empty members until the engine has computed one.")), reg.getTasteProfile)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/recommendations/taste-seed/items", opListTasteSeedItems, "recommendations",
		"A page of cards for the taste-seeding picker, most recognizable first.")), func(ctx context.Context, in *TasteSeedItemsInput) (*CatalogItemCollectionOutput, error) {
		return reg.listTasteSeedItems(ctx, cursors, in)
	})
	Register(reg, Operation{
		Operation: humaOp(http.MethodPost, Prefix+"/recommendations/taste-seed", "createTasteSeed", "recommendations",
			"Record the picked items as the acting profile's favorites and queue a taste-profile refresh."),
		// Favorites converge and retries report 0 added, but a crash after
		// insertion can lose the separate refresh dispatch.
		Class:          ClassProfileScoped,
		RetrySafety:    RetrySafetyNonRetryable,
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.createTasteSeed)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/recommendations/watch-tonight", "getWatchTonight", "recommendations",
		"The acting profile's Watch Tonight list: in-progress and next-up items merged with taste candidates, best first.")), reg.getWatchTonight)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/recommendations/watch-tonight/cards", "listWatchTonightCards", "recommendations",
		"A page of Watch Tonight swipe cards with cast, in continue or discover mode, minus the identifiers already swiped.")), reg.listWatchTonightCards)
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
		return svc.BecauseWatchedCards(ctx, userID, profileID, in.ItemID, in.Limit, handlers.AccessFilterFromContext(ctx, ""))
	})
}

func (reg *Registry) listPopular(ctx context.Context, in *PopularInput) (*CatalogItemCollectionOutput, error) {
	return reg.recommendationCards(ctx, func(svc RecommendationService, userID int, profileID string) ([]handlers.SectionItemView, error) {
		return svc.PopularCards(ctx, userID, profileID, in.Days, in.Limit, handlers.AccessFilterFromContext(ctx, ""))
	})
}

func (reg *Registry) listRecentlyAdded(ctx context.Context, in *RecentlyAddedInput) (*CatalogItemCollectionOutput, error) {
	return reg.recommendationCards(ctx, func(svc RecommendationService, userID int, profileID string) ([]handlers.SectionItemView, error) {
		return svc.RecentlyAddedCards(ctx, userID, profileID, in.Days, in.Limit, handlers.AccessFilterFromContext(ctx, ""))
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
	row, err := svc.ForYouMainCards(ctx, userID, profileID, in.Limit, handlers.AccessFilterFromContext(ctx, ""))
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
		return svc.ForYouRowCards(ctx, userID, profileID, in.Limit, handlers.AccessFilterFromContext(ctx, ""))
	})
}

func (reg *Registry) getDiscover(ctx context.Context, _ *struct{}) (*RecommendationRowCollectionOutput, error) {
	return reg.recommendationRows(ctx, func(svc RecommendationService, userID int, profileID string) ([]handlers.DiscoverRowView, error) {
		view, err := svc.Discover(ctx, userID, profileID, handlers.AccessFilterFromContext(ctx, ""))
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
	view, err := svc.Section(ctx, userID, profileID, in.Kind, in.Key, in.Limit, handlers.AccessFilterFromContext(ctx, ""))
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &RecommendationRowOutput{Body: RecommendationRow{Type: view.Type, Title: view.Label, Kind: view.Kind, Key: view.Key, Items: catalogItemsOfSection(view.Items)}}, nil
}

func (reg *Registry) listSimilar(ctx context.Context, in *SimilarInput) (*CatalogItemCollectionOutput, error) {
	return reg.recommendationCards(ctx, func(svc RecommendationService, userID int, profileID string) ([]handlers.SectionItemView, error) {
		return svc.SimilarCards(ctx, userID, profileID, in.ItemID, in.Limit, handlers.AccessFilterFromContext(ctx, ""))
	})
}

func (reg *Registry) listSimilarUsersLiked(ctx context.Context, in *SimilarUsersInput) (*CatalogItemCollectionOutput, error) {
	return reg.recommendationCards(ctx, func(svc RecommendationService, userID int, profileID string) ([]handlers.SectionItemView, error) {
		return svc.SimilarUsersCards(ctx, userID, profileID, in.Limit, handlers.AccessFilterFromContext(ctx, ""))
	})
}

func (reg *Registry) getTasteProfile(ctx context.Context, _ *struct{}) (*TasteProfileOutput, error) {
	svc, p := reg.recommendationsService()
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	summary := svc.TasteProfile(ctx, userID, profileID)
	counts := summary.SignalCounts
	if counts == nil {
		counts = map[string]int{}
	}
	return &TasteProfileOutput{Body: TasteProfile{
		TopGenres:         NonNil(summary.TopGenres),
		FavoriteDirectors: NonNil(summary.FavoriteDirectors),
		SignalCounts:      counts,
		UpdatedAt:         instantOfStamp(summary.UpdatedAt),
	}}, nil
}

func (reg *Registry) listTasteSeedItems(ctx context.Context, cursors *Cursors, in *TasteSeedItemsInput) (*CatalogItemCollectionOutput, error) {
	svc, p := reg.recommendationsService()
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	scope := CursorScope{
		OperationID: opListTasteSeedItems,
		Security:    strconv.Itoa(userID) + "/" + profileID + "/" + viewerScopeDigest(ctx),
		Sort:        "engagement",
		Tiebreaker:  "engagement",
	}
	offset, p := decodeOffset(cursors, scope, in.Cursor)
	if p != nil {
		return nil, p
	}
	views, candidates, err := svc.TasteSeedItems(ctx, userID, profileID, handlers.AccessFilterFromContext(ctx, ""), in.Limit, offset)
	if err != nil {
		return nil, serviceProblem(err)
	}
	// v1 offers a next page whenever the candidate window was full, before
	// hydration dropped anything; the cursor carries that same offset.
	next := ""
	if candidates == in.Limit {
		next, err = cursors.Encode(scope, offsetPosition{Offset: offset + in.Limit})
		if err != nil {
			return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
	}
	return &CatalogItemCollectionOutput{Body: CatalogItemCollection{Collection: Paginated(catalogItemsOfSection(views), next)}}, nil
}

func (reg *Registry) createTasteSeed(ctx context.Context, in *TasteSeedSubmitInput) (*TasteSeedResultOutput, error) {
	svc, p := reg.recommendationsService()
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	for i, id := range in.Body.ItemIDs {
		if strings.TrimSpace(id) == "" {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationBody + ".item_ids[" + strconv.Itoa(i) + "]", Code: codeInvalid, Detail: "empty identifier"})
		}
	}
	added, err := svc.SubmitTasteSeed(ctx, userID, profileID, in.Body.ItemIDs)
	if err != nil {
		// The seam answers 503 when the user store is not wired, the
		// same fail-closed dependency answer a missing service gets.
		var apiErr *handlers.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusServiceUnavailable {
			return nil, unavailable("user store")
		}
		return nil, serviceProblem(err)
	}
	return &TasteSeedResultOutput{Body: TasteSeedResult{Added: added}}, nil
}

func (reg *Registry) getWatchTonight(ctx context.Context, in *WatchTonightInput) (*WatchTonightOutput, error) {
	svc, p := reg.recommendationsService()
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	view, err := svc.WatchTonight(ctx, userID, profileID, handlers.AccessFilterFromContext(ctx, ""), in.Limit)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]WatchTonightItem, 0, len(view.Items))
	for _, v := range view.Items {
		items = append(items, WatchTonightItem{CatalogItem: catalogItemOfSection(v.Card()), WatchTonightSource: v.WatchTonightSource})
	}
	return &WatchTonightOutput{Body: WatchTonight{Items: items, IsCold: view.IsCold}}, nil
}

func (reg *Registry) listWatchTonightCards(ctx context.Context, in *WatchTonightCardsInput) (*WatchTonightCardPageOutput, error) {
	svc, p := reg.recommendationsService()
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	genres, p := watchTonightGenres(in.Genres)
	if p != nil {
		return nil, p
	}
	var excludeIDs map[string]struct{}
	for _, id := range in.ExcludeIDs {
		if id = strings.TrimSpace(id); id != "" {
			if excludeIDs == nil {
				excludeIDs = map[string]struct{}{}
			}
			excludeIDs[id] = struct{}{}
		}
	}
	view := svc.WatchTonightCards(ctx, userID, profileID, handlers.AccessFilterFromContext(ctx, ""), in.Mode, genres, excludeIDs, in.Limit)
	remaining := max(0, watchTonightExclusionLimit-len(excludeIDs))
	if len(view.Cards) > remaining {
		view.Cards = view.Cards[:remaining]
		view.HasMore = true
	}
	pagingLimited := view.HasMore && len(view.Cards) >= remaining
	items := make([]WatchTonightCard, 0, len(view.Cards))
	for _, c := range view.Cards {
		cast := make([]WatchTonightCastMember, 0, len(c.Cast))
		for _, m := range c.Cast {
			cast = append(cast, WatchTonightCastMember{Name: m.Name, Character: m.Character, PhotoURL: m.PhotoURL})
		}
		items = append(items, WatchTonightCard{CatalogItem: catalogItemOfSection(c.Card()), WatchTonightSource: c.WatchTonightSource, Cast: cast})
	}
	return &WatchTonightCardPageOutput{Body: WatchTonightCardPage{Items: items, HasMore: view.HasMore, PagingLimited: pagingLimited, IsCold: view.IsCold}}, nil
}

// watchTonightGenres validates the genre filter against the seam's known
// set, answering the deduplicated list in request order. v1 silently
// dropped an unknown genre; v2 names it.
func watchTonightGenres(raw []string) ([]string, *Problem) {
	if len(raw) == 0 {
		return nil, nil
	}
	known := make(map[string]struct{}, len(handlers.KnownWatchTonightGenres))
	for _, g := range handlers.KnownWatchTonightGenres {
		known[g] = struct{}{}
	}
	seen := make(map[string]struct{}, len(raw))
	genres := make([]string, 0, len(raw))
	for i, g := range raw {
		g = strings.TrimSpace(g)
		if _, ok := known[g]; !ok {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: "query.genres[" + strconv.Itoa(i) + "]", Code: codeInvalid, Detail: "not a known genre"})
		}
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		genres = append(genres, g)
	}
	return genres, nil
}

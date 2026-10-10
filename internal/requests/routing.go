package requests

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
)

// Routing: Silo decides which server each quality tier of a request goes to,
// and hands the router plugin only that server. Routes are evaluated per tier
// in order: the first enabled route whose conditions match the request and
// that has a destination for the tier wins, and the media type's fallback
// route (no conditions) comes last. A media type with no routes keeps the
// plugin's own routing (every connection is handed over and the plugin picks).

// The Sonarr/Radarr plugin's config keys and kinds that routing sets or reads.
const (
	configServiceKind  = "service_kind"
	configIsDefault    = "is_default"
	configIsDefault4K  = "is_default_4k"
	configIs4K         = "is_4k"
	configAnimeEnabled = "anime_enabled"
	configSeriesType   = "series_type"
	seriesTypeAnime    = "anime"
	kindRadarr         = "radarr"
	kindSonarr         = "sonarr"
)

// fallbackRouteName names a media type's fallback until an admin renames it.
const fallbackRouteName = "Everything else"

// Route sends the requests it matches to a server per quality tier.
type Route struct {
	ID         string
	MediaType  MediaType
	Position   int
	Name       string
	Enabled    bool
	IsFallback bool
	Conditions RouteConditions
	HD         RouteDestination
	UHD        RouteDestination
	// SkipUHD stops a matching title from getting a 4K copy at all, rather
	// than letting the 4K tier fall through to a later route.
	SkipUHD  bool
	Revision int64
}

// RouteDestination is a server and the settings a route overrides on it. An
// empty IntegrationID means the route has no destination for the tier.
type RouteDestination struct {
	IntegrationID string
	// Overrides replace keys of the server's plugin config for requests this
	// route sends, e.g. root_folder, quality_profile_id, tags, series_type.
	Overrides map[string]any
}

// RouteConditions narrow a route to some requests. Every set field must match
// (AND); a list matches when the request has any of its values (OR), and an
// exclude list when it has none of them. An empty condition set matches
// everything.
type RouteConditions struct {
	Anime             *bool    `json:"anime,omitempty"`
	GenreIDs          []int    `json:"genre_ids,omitempty"`
	KeywordIDs        []int    `json:"keyword_ids,omitempty"`
	OriginalLanguages []string `json:"original_languages,omitempty"`
	OriginCountries   []string `json:"origin_countries,omitempty"`
	// YearFrom and YearTo bound the release (or first-air) year, inclusive;
	// a decade is 1980-1989.
	YearFrom         int   `json:"year_from,omitempty"`
	YearTo           int   `json:"year_to,omitempty"`
	NetworkIDs       []int `json:"network_ids,omitempty"`
	CompanyIDs       []int `json:"company_ids,omitempty"`
	RequesterUserIDs []int `json:"requester_user_ids,omitempty"`

	ExcludeGenreIDs          []int    `json:"exclude_genre_ids,omitempty"`
	ExcludeKeywordIDs        []int    `json:"exclude_keyword_ids,omitempty"`
	ExcludeOriginalLanguages []string `json:"exclude_original_languages,omitempty"`
	ExcludeOriginCountries   []string `json:"exclude_origin_countries,omitempty"`
	ExcludeNetworkIDs        []int    `json:"exclude_network_ids,omitempty"`
	ExcludeCompanyIDs        []int    `json:"exclude_company_ids,omitempty"`
	ExcludeRequesterUserIDs  []int    `json:"exclude_requester_user_ids,omitempty"`
	// MaxContentRating matches titles whose rating is at most this one ("PG"
	// takes G and PG), by minimum age: the US rating, or the title's own
	// country's when it has no US one. A title with neither does not match,
	// as a parental ceiling treats it.
	MaxContentRating string `json:"max_content_rating,omitempty"`
}

// Condition keys, as the JSON fields name them; Unmet reports them.
const (
	condAnime                    = "anime"
	condGenreIDs                 = "genre_ids"
	condKeywordIDs               = "keyword_ids"
	condOriginalLanguages        = "original_languages"
	condOriginCountries          = "origin_countries"
	condYearFrom                 = "year_from"
	condYearTo                   = "year_to"
	condNetworkIDs               = "network_ids"
	condCompanyIDs               = "company_ids"
	condRequesterUserIDs         = "requester_user_ids"
	condExcludeGenreIDs          = "exclude_genre_ids"
	condExcludeKeywordIDs        = "exclude_keyword_ids"
	condExcludeOriginalLanguages = "exclude_original_languages"
	condExcludeOriginCountries   = "exclude_origin_countries"
	condExcludeNetworkIDs        = "exclude_network_ids"
	condExcludeCompanyIDs        = "exclude_company_ids"
	condExcludeRequesterUserIDs  = "exclude_requester_user_ids"
	condMaxContentRating         = "max_content_rating"
)

// Matches reports whether a request satisfies every set condition, judged on
// its stored routing facts.
func (c RouteConditions) Matches(req Request) bool {
	return len(c.Unmet(req)) == 0
}

// Unmet lists the set conditions a request fails, by key, in a fixed order.
// Routing and the admin preview's explanation share it.
func (c RouteConditions) Unmet(req Request) []string {
	f := req.RoutingFacts
	language := []string{f.OriginalLanguage}
	var out []string
	fail := func(failed bool, key string) {
		if failed {
			out = append(out, key)
		}
	}
	fail(c.Anime != nil && *c.Anime != f.Anime, condAnime)
	fail(len(c.GenreIDs) > 0 && !anyInt(c.GenreIDs, f.GenreIDs), condGenreIDs)
	fail(len(c.KeywordIDs) > 0 && !anyInt(c.KeywordIDs, f.KeywordIDs), condKeywordIDs)
	fail(len(c.OriginalLanguages) > 0 && !anyFold(c.OriginalLanguages, language), condOriginalLanguages)
	fail(len(c.OriginCountries) > 0 && !anyFold(c.OriginCountries, f.OriginCountries), condOriginCountries)
	fail(c.YearFrom > 0 && (f.Year == 0 || f.Year < c.YearFrom), condYearFrom)
	fail(c.YearTo > 0 && (f.Year == 0 || f.Year > c.YearTo), condYearTo)
	fail(len(c.NetworkIDs) > 0 && !anyInt(c.NetworkIDs, f.NetworkIDs), condNetworkIDs)
	fail(len(c.CompanyIDs) > 0 && !anyInt(c.CompanyIDs, f.CompanyIDs), condCompanyIDs)
	fail(len(c.RequesterUserIDs) > 0 && !slices.Contains(c.RequesterUserIDs, req.RequestedByUserID), condRequesterUserIDs)
	fail(anyInt(c.ExcludeGenreIDs, f.GenreIDs), condExcludeGenreIDs)
	fail(anyInt(c.ExcludeKeywordIDs, f.KeywordIDs), condExcludeKeywordIDs)
	fail(anyFold(c.ExcludeOriginalLanguages, language), condExcludeOriginalLanguages)
	fail(anyFold(c.ExcludeOriginCountries, f.OriginCountries), condExcludeOriginCountries)
	fail(anyInt(c.ExcludeNetworkIDs, f.NetworkIDs), condExcludeNetworkIDs)
	fail(anyInt(c.ExcludeCompanyIDs, f.CompanyIDs), condExcludeCompanyIDs)
	fail(slices.Contains(c.ExcludeRequesterUserIDs, req.RequestedByUserID) && req.RequestedByUserID != 0, condExcludeRequesterUserIDs)
	fail(c.MaxContentRating != "" && !ratingWithin(f.ContentRating, c.MaxContentRating), condMaxContentRating)
	return out
}

// routesCheckRating reports whether an enabled route of the media type
// matches on content rating.
func routesCheckRating(routes []Route, mediaType MediaType) bool {
	for _, route := range routes {
		if route.Enabled && route.MediaType == mediaType && route.Conditions.MaxContentRating != "" {
			return true
		}
	}
	return false
}

// routesUseConditions reports whether an enabled route of the media type has
// conditions, so a request's routing facts matter.
func routesUseConditions(routes []Route, mediaType MediaType) bool {
	for _, route := range routes {
		if route.Enabled && route.MediaType == mediaType && !conditionsEmpty(route.Conditions) {
			return true
		}
	}
	return false
}

// ratingWithin reports whether a title's rating is at most max, by each
// rating's own minimum age (not the parental-control tiers, which would let
// "TV-Y7 or lower" take TV-PG). An unknown or unrated title is not.
func ratingWithin(rating *string, max string) bool {
	if rating == nil || strings.TrimSpace(*rating) == "" {
		return false
	}
	maxAge, ok := ratingAge(max)
	if !ok {
		return false
	}
	age, ok := ratingAge(*rating)
	return ok && age <= maxAge
}

func ratingAge(rating string) (int, bool) {
	_, age, ok := access.Normalize(rating)
	if !ok || age == nil {
		return 0, false
	}
	return *age, true
}

func anyInt(want, have []int) bool {
	for _, v := range have {
		if slices.Contains(want, v) {
			return true
		}
	}
	return false
}

func anyFold(want, have []string) bool {
	for _, v := range have {
		for _, w := range want {
			if v != "" && strings.EqualFold(v, w) {
				return true
			}
		}
	}
	return false
}

// RouteDecision is where one quality tier of a request goes, and which route
// sent it there. Skip marks a tier a matching route chose not to send at all
// (skip_uhd, or Everything else with no server for the tier): the title gets
// no copy in that tier, whatever force-dual says.
type RouteDecision struct {
	RouteID       string
	RouteName     string
	IntegrationID string
	Overrides     map[string]any
	Skip          bool
}

// orderRoutes sorts a media type's routes into evaluation order: by position,
// the fallback last.
func orderRoutes(routes []Route) []Route {
	out := slices.Clone(routes)
	slices.SortStableFunc(out, func(a, b Route) int {
		if a.IsFallback != b.IsFallback {
			if a.IsFallback {
				return 1
			}
			return -1
		}
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// decideRoutes picks a destination for each quality. A tier no route sends
// anywhere is absent from the result; a tier a route skips is present with
// Skip set.
func decideRoutes(routes []Route, req Request, qualities []Quality) map[Quality]RouteDecision {
	decisions, _ := traceRoutes(routes, req, qualities)
	return decisions
}

// RouteStep is what one route did for one quality tier.
type RouteStep string

const (
	// RouteStepSends: the route matched and sent the tier to its server.
	RouteStepSends RouteStep = "sends"
	// RouteStepSkips: the route matched and made no copy in the tier.
	RouteStepSkips RouteStep = "skips"
	// RouteStepPasses: the route matched but has no server for the tier, so
	// a later route decides.
	RouteStepPasses RouteStep = "passes"
	// RouteStepNoMatch: the route is off, or the request fails a condition.
	RouteStepNoMatch RouteStep = "no_match"
	// RouteStepDecided: an earlier route already decided the tier.
	RouteStepDecided RouteStep = "already_decided"
)

// RouteTrace is one route's part in a decision, for the admin preview.
type RouteTrace struct {
	Route Route
	Unmet []string
	Steps map[Quality]RouteStep
}

// traceRoutes decides like decideRoutes and records, for every route of the
// request's media type in evaluation order, what it did for each tier.
//
// Everything else with no 4K server makes no 4K copy: the tier is skipped,
// even when every request asks for 4K (force-dual), rather than left
// undecided and failed. Everything else with no HD server makes no HD copy
// when the title's 4K copy goes to a server, so a media type whose servers are
// all marked 4K gets its 4K version without a failed HD tier. A title whose 4K
// copy goes nowhere (a requester without 4K) keeps HD undecided, so it fails
// rather than being sent nowhere.
func traceRoutes(routes []Route, req Request, qualities []Quality) (map[Quality]RouteDecision, []RouteTrace) {
	var ordered []Route
	for _, route := range orderRoutes(routes) {
		if route.MediaType == req.MediaType {
			ordered = append(ordered, route)
		}
	}
	decisions := make(map[Quality]RouteDecision, len(qualities))
	traces := make([]RouteTrace, len(ordered))
	for i, route := range ordered {
		traces[i] = RouteTrace{Route: route, Steps: make(map[Quality]RouteStep, len(qualities))}
		if route.Enabled {
			traces[i].Unmet = route.Conditions.Unmet(req)
		}
	}
	for _, q := range qualities {
		for i, route := range ordered {
			trace := &traces[i]
			if !route.Enabled || len(trace.Unmet) > 0 {
				trace.Steps[q] = RouteStepNoMatch
				continue
			}
			if _, done := decisions[q]; done {
				trace.Steps[q] = RouteStepDecided
				continue
			}
			dest := route.HD
			if q == Quality2160p {
				dest = route.UHD
			}
			if q == Quality2160p && (route.SkipUHD || (route.IsFallback && dest.IntegrationID == "")) {
				decisions[q] = RouteDecision{RouteID: route.ID, RouteName: route.Name, Skip: true}
				trace.Steps[q] = RouteStepSkips
				continue
			}
			if dest.IntegrationID == "" {
				trace.Steps[q] = RouteStepPasses
				continue
			}
			decisions[q] = RouteDecision{
				RouteID:       route.ID,
				RouteName:     route.Name,
				IntegrationID: dest.IntegrationID,
				Overrides:     dest.Overrides,
			}
			trace.Steps[q] = RouteStepSends
		}
	}
	if uhd, ok := decisions[Quality2160p]; ok && !uhd.Skip {
		for i := range traces {
			// The fallback passes HD only when it matched, has no HD server,
			// and no route before it decided HD.
			if traces[i].Route.IsFallback && traces[i].Steps[Quality1080p] == RouteStepPasses {
				decisions[Quality1080p] = RouteDecision{RouteID: traces[i].Route.ID, RouteName: traces[i].Route.Name, Skip: true}
				traces[i].Steps[Quality1080p] = RouteStepSkips
				break
			}
		}
	}
	return decisions, traces
}

// routedConnection builds the one connection a routed tier is sent to. The
// route already chose the server, so the connection says so in the Sonarr/
// Radarr plugin's own terms: it is the tier's default, and the plugin's anime
// overlay is off because the route's overrides replace it. A plugin without
// those keys ignores them.
func routedConnection(fc *fulfillContext, d RouteDecision, mediaType MediaType, q Quality) (ResolvedRouterConnection, int, string, error) {
	var in *Integration
	for i := range fc.integrations {
		if fc.integrations[i].ID == d.IntegrationID {
			in = &fc.integrations[i]
			break
		}
	}
	switch {
	case in == nil:
		return ResolvedRouterConnection{}, 0, "", fmt.Errorf("route %q sends to a server that no longer exists", d.RouteName)
	case !in.Enabled:
		return ResolvedRouterConnection{}, 0, "", fmt.Errorf("route %q sends to %q, which is disabled", d.RouteName, in.Name)
	case in.InstallationID == nil || in.CapabilityID == "":
		return ResolvedRouterConnection{}, 0, "", fmt.Errorf("%s (route %q)", msgRouterUnbound, d.RouteName)
	case strings.TrimSpace(in.APIKeyRef) == "":
		return ResolvedRouterConnection{}, 0, "", fmt.Errorf("%s (route %q)", msgRouterNoKey, d.RouteName)
	case !integrationSupportsMediaType(*in, mediaType):
		// The server's media types can change after a route points at it.
		return ResolvedRouterConnection{}, 0, "", fmt.Errorf("route %q sends %s to %q, which does not take them", d.RouteName, mediaTypePlural(mediaType), in.Name)
	}
	// A server can be switched to the other kind after a route points at it.
	if kind, _ := in.PluginConfig[configServiceKind].(string); kind != "" {
		if want := map[MediaType]string{MediaTypeMovie: kindRadarr, MediaTypeSeries: kindSonarr}[mediaType]; want != "" && kind != want {
			return ResolvedRouterConnection{}, 0, "", fmt.Errorf("route %q sends %s to %q, a %s server", d.RouteName, mediaType, in.Name, kind)
		}
	}
	config := maps.Clone(in.PluginConfig)
	if config == nil {
		config = map[string]any{}
	}
	maps.Copy(config, d.Overrides)
	config[configIsDefault] = q == Quality1080p
	config[configIsDefault4K] = q == Quality2160p
	// The server's own 4K flag must not follow an HD copy there.
	config[configIs4K] = q == Quality2160p
	config[configAnimeEnabled] = false
	return ResolvedRouterConnection{
		ID:      in.ID,
		BaseURL: in.BaseURL,
		APIKey:  strings.TrimSpace(in.APIKeyRef),
		Config:  config,
	}, *in.InstallationID, in.CapabilityID, nil
}

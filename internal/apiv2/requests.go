package apiv2

import (
	"cmp"
	"context"
	"errors"
	"math/bits"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// The requests domain: a profile's media requests and the TMDB-backed
// discovery surface they are made from. The bodies stay close to v1 because
// the Android and Apple clients consume them; TMDB identifiers are external
// integers, not Silo IDs, and stay integers.

// RequestMediaState is the request state of one piece of media for the
// acting viewer.
type RequestMediaState struct {
	Status      string `json:"status,omitempty" doc:"Status of the active request, when one exists" example:"pending"`
	State       string `json:"state,omitempty" doc:"User-facing state of the active request, when one exists: pending, approved or processing" example:"pending"`
	Requestable bool   `json:"requestable" doc:"Whether the viewer may request this media now" example:"true"`
	Reason      string `json:"reason,omitempty" doc:"Why the media is not requestable" example:"already_requested"`
	RequestID   ID     `json:"request_id,omitempty" doc:"The active request, when one exists" example:"1834729"`
	Following   bool   `json:"following" doc:"Whether the viewer will be notified when the media becomes available: they requested it or follow it" example:"false"`
	// RequestedByViewer tells a client whether to offer a follow toggle.
	RequestedByViewer bool `json:"requested_by_viewer" doc:"Whether the viewing profile made the active request, so there is nothing to follow" example:"false"`
	// Download is filled on the title detail only: search and discovery do
	// not load each result's targets.
	Download *RequestDownload `json:"download,omitempty" doc:"How far the active request's downloads are, while its download server reports them. Only the title detail (getRequestMediaDetail) carries it"`
}

// RequestDownload is how far downloads are, as the download server last
// reported them.
type RequestDownload struct {
	Phase                 string   `json:"phase" doc:"queued, downloading, paused, stalled, importing or import_blocked. More values may be added: read an unknown one as downloading, without a percentage" example:"downloading"`
	Percent               *int     `json:"percent,omitempty" minimum:"0" maximum:"100" doc:"How much has downloaded, rounded down; absent while the size is unknown" example:"43"`
	BytesTotal            *int64   `json:"bytes_total,omitempty" minimum:"1" doc:"Size of the downloads in bytes; absent while unknown" example:"4294967296"`
	BytesLeft             *int64   `json:"bytes_left,omitempty" minimum:"0" doc:"Bytes still to download; present whenever bytes_total is" example:"2448131358"`
	EstimatedCompletionAt *Instant `json:"estimated_completion_at,omitempty" doc:"When the download server expects the downloads to finish; absent when it cannot tell" example:"2026-01-02T03:16:05.000Z"`
	Downloads             int      `json:"downloads" minimum:"0" doc:"Distinct downloads in flight; a season pack counts once" example:"1"`
	UpdatedAt             Instant  `json:"updated_at" doc:"When the server last heard from the download server. A client may hide figures older than about ten minutes" example:"2026-01-02T03:04:05.000Z"`
}

// RequestMediaResult is one discovery or search card.
type RequestMediaResult struct {
	MediaType        string            `json:"media_type" doc:"movie or series" example:"movie"`
	TMDBID           int               `json:"tmdb_id" doc:"TMDB identifier (external, not a Silo ID)" example:"949"`
	Title            string            `json:"title" example:"Heat"`
	Year             int               `json:"year,omitempty" example:"1995"`
	Overview         string            `json:"overview,omitempty"`
	PosterPath       string            `json:"poster_path,omitempty" doc:"TMDB image path" example:"/abc.jpg"`
	BackdropPath     string            `json:"backdrop_path,omitempty" doc:"TMDB image path"`
	ReleaseDate      string            `json:"release_date,omitempty" doc:"Calendar date, YYYY-MM-DD" example:"1995-12-15"`
	Popularity       float64           `json:"popularity,omitempty"`
	VoteAverage      float64           `json:"vote_average,omitempty"`
	Availability     string            `json:"availability" doc:"missing or available in this server's catalog" example:"missing"`
	LibraryContentID string            `json:"library_content_id,omitempty" doc:"The catalog item when the media is available" example:"movie:heat-1995"`
	Request          RequestMediaState `json:"request"`
}

// RequestMediaCastMember is one cast credit on a detail document.
type RequestMediaCastMember struct {
	Name        string `json:"name" example:"Al Pacino"`
	Character   string `json:"character,omitempty" example:"Vincent Hanna"`
	ProfilePath string `json:"profile_path,omitempty" doc:"TMDB image path"`
	Order       int    `json:"order" example:"0"`
}

// RequestMediaDetail is the full detail document of one piece of media.
type RequestMediaDetail struct {
	MediaType           string                   `json:"media_type" doc:"movie or series" example:"movie"`
	TMDBID              int                      `json:"tmdb_id" doc:"TMDB identifier (external, not a Silo ID)" example:"949"`
	IMDbID              string                   `json:"imdb_id,omitempty" example:"tt0113277"`
	TVDBID              *int                     `json:"tvdb_id,omitempty" doc:"TVDB identifier (external, not a Silo ID)"`
	Title               string                   `json:"title" example:"Heat"`
	OriginalTitle       string                   `json:"original_title,omitempty"`
	Tagline             string                   `json:"tagline,omitempty"`
	Overview            string                   `json:"overview,omitempty"`
	PosterPath          string                   `json:"poster_path,omitempty" doc:"TMDB image path"`
	BackdropPath        string                   `json:"backdrop_path,omitempty" doc:"TMDB image path"`
	ReleaseDate         string                   `json:"release_date,omitempty" doc:"Calendar date, YYYY-MM-DD"`
	Year                int                      `json:"year,omitempty" example:"1995"`
	Runtime             int                      `json:"runtime,omitempty" doc:"Minutes" example:"170"`
	Genres              []string                 `json:"genres" doc:"Empty, never null"`
	VoteAverage         float64                  `json:"vote_average,omitempty"`
	VoteCount           int                      `json:"vote_count,omitempty"`
	Status              string                   `json:"status,omitempty" doc:"TMDB release status" example:"Released"`
	Homepage            string                   `json:"homepage,omitempty"`
	ContentRating       string                   `json:"content_rating,omitempty" example:"R"`
	ProductionCompanies []string                 `json:"production_companies" doc:"Empty, never null"`
	NumberOfSeasons     int                      `json:"number_of_seasons,omitempty"`
	NumberOfEpisodes    int                      `json:"number_of_episodes,omitempty"`
	FirstAirDate        string                   `json:"first_air_date,omitempty" doc:"Calendar date, YYYY-MM-DD"`
	LastAirDate         string                   `json:"last_air_date,omitempty" doc:"Calendar date, YYYY-MM-DD"`
	Networks            []string                 `json:"networks" doc:"Empty, never null"`
	Cast                []RequestMediaCastMember `json:"cast" doc:"Empty, never null"`
	Director            string                   `json:"director,omitempty"`
	Creators            []string                 `json:"creators" doc:"Empty, never null"`
	Recommendations     []RequestMediaResult     `json:"recommendations" doc:"Empty, never null"`
	Availability        string                   `json:"availability" doc:"missing or available in this server's catalog" example:"missing"`
	LibraryContentID    string                   `json:"library_content_id,omitempty" doc:"The catalog item when the media is available"`
	Request             RequestMediaState        `json:"request"`
	Seasons             []RequestMediaSeason     `json:"seasons" doc:"Series: the regular seasons (specials excluded) with library availability and request coverage; empty for movies"`
}

// RequestMediaSeason is one season of a series on its detail document.
type RequestMediaSeason struct {
	SeasonNumber int    `json:"season_number" example:"2"`
	Name         string `json:"name,omitempty" example:"Season 2"`
	EpisodeCount int    `json:"episode_count" doc:"Episodes TMDB lists for the season, aired or not" example:"10"`
	AirDate      string `json:"air_date,omitempty" doc:"Calendar date, YYYY-MM-DD" example:"2025-01-17"`
	PosterPath   string `json:"poster_path,omitempty" doc:"TMDB image path"`
	Availability string `json:"availability" enum:"missing,partial,available" doc:"Whether every aired episode is in the library" example:"partial"`
	Requested    bool   `json:"requested" doc:"The title's active request covers this season"`
}

// RequestSeasonProgress is how far one requested season is.
type RequestSeasonProgress struct {
	SeasonNumber      int `json:"season_number" example:"2"`
	EpisodesAired     int `json:"episodes_aired" doc:"Aired episodes by the library's own metadata; 0 when it has no air dates yet" example:"10"`
	EpisodesAvailable int `json:"episodes_available" doc:"Episodes with a file in an enabled library" example:"4"`
}

// RequestMediaPage is one TMDB result page. Search and browse page by the
// provider's own page number, not by a Silo cursor: the provider owns the
// ordering and the page is not a Silo collection.
type RequestMediaPage struct {
	Page         int                  `json:"page" example:"1"`
	TotalPages   int                  `json:"total_pages" example:"12"`
	TotalResults int                  `json:"total_results" example:"231"`
	Results      []RequestMediaResult `json:"results" doc:"Empty, never null"`
}

// DiscoverSection is one discovery row.
type DiscoverSection struct {
	Key          string               `json:"key" example:"trending_movies"`
	Title        string               `json:"title" example:"Trending Movies"`
	Page         int                  `json:"page" example:"1"`
	TotalPages   int                  `json:"total_pages" example:"500"`
	TotalResults int                  `json:"total_results" example:"10000"`
	Results      []RequestMediaResult `json:"results" doc:"Empty, never null"`
	NextPage     int                  `json:"next_page,omitempty" doc:"The page to request next when rating backfill consumed more than one provider page; absent when page+1 applies or there are no more pages"`
}

// DiscoverSectionCollection is the listDiscoverSections envelope.
type DiscoverSectionCollection struct {
	Collection[DiscoverSection]
}

// DiscoverBrand is a studio, network, or genre card.
type DiscoverBrand struct {
	TMDBID          int     `json:"tmdb_id,omitempty" doc:"TMDB identifier (external, not a Silo ID)" example:"420"`
	Slug            string  `json:"slug" example:"marvel-studios"`
	DisplayName     string  `json:"display_name" example:"Marvel Studios"`
	LogoURL         *string `json:"logo_url,omitempty" nullable:"true"`
	GradientFrom    string  `json:"gradient_from,omitempty" example:"#1a1a2e"`
	GradientTo      string  `json:"gradient_to,omitempty" example:"#16213e"`
	SeriesSupported bool    `json:"series_supported,omitempty" doc:"Whether the brand can be browsed for series"`
}

// DiscoverBrandCollection is the studio, network, and genre list envelope.
type DiscoverBrandCollection struct {
	Collection[DiscoverBrand]
}

// DiscoverBrowsePage is one page of a studio, network, or genre browse.
type DiscoverBrowsePage struct {
	Kind        string               `json:"kind" doc:"studio, network, or genre" example:"studio"`
	Slug        string               `json:"slug" example:"marvel-studios"`
	DisplayName string               `json:"display_name" example:"Marvel Studios"`
	LogoURL     *string              `json:"logo_url,omitempty" nullable:"true"`
	MediaType   string               `json:"media_type" doc:"movie or series" example:"movie"`
	Sort        string               `json:"sort" example:"popularity"`
	Page        int                  `json:"page" example:"1"`
	TotalPages  int                  `json:"total_pages" example:"20"`
	Results     []RequestMediaResult `json:"results" doc:"Empty, never null"`
}

// RequestTarget is one fulfillment of a request against one integration
// instance at one quality. The download server details are for admins only:
// see mediaRequestOf.
type RequestTarget struct {
	ID              ID      `json:"id" example:"42"`
	RequestID       ID      `json:"request_id" example:"1834729"`
	IntegrationID   string  `json:"integration_id,omitempty" doc:"Admins only: the download server holding this target"`
	IntegrationKind string  `json:"integration_kind,omitempty" doc:"Admins only: the download server's kind" example:"radarr"`
	InstanceName    string  `json:"instance_name,omitempty" doc:"Admins only: the download server's name"`
	Quality         string  `json:"quality" example:"1080p"`
	IsAnime         bool    `json:"is_anime"`
	ExternalID      string  `json:"external_id,omitempty" doc:"Admins only: the integration's own identifier"`
	ExternalStatus  string  `json:"external_status,omitempty" doc:"Admins only: the status as the download server reports it"`
	Status          string  `json:"status" example:"queued"`
	LastError       string  `json:"last_error,omitempty" doc:"Admins only: why the download server failed this target"`
	RouteName       string  `json:"route_name,omitempty" doc:"Admins only: the routing rule that sent this target to its server, as named when it was sent"`
	CreatedAt       Instant `json:"created_at" example:"2026-01-02T03:04:05.000Z"`
	UpdatedAt       Instant `json:"updated_at" example:"2026-01-02T03:04:05.000Z"`
	// Download is set while the target's router plugin reports progress.
	Download *RequestDownload `json:"download,omitempty" doc:"How far this target's downloads are, while its download server reports them"`
}

// MediaRequest is one media request.
type MediaRequest struct {
	ID                   ID                      `json:"id" example:"1834729"`
	Provider             string                  `json:"provider" example:"tmdb"`
	MediaType            string                  `json:"media_type" doc:"movie or series" example:"movie"`
	TMDBID               int                     `json:"tmdb_id" doc:"TMDB identifier (external, not a Silo ID)" example:"949"`
	TVDBID               *int                    `json:"tvdb_id,omitempty" doc:"TVDB identifier (external, not a Silo ID)"`
	IMDbID               string                  `json:"imdb_id,omitempty" example:"tt0113277"`
	Title                string                  `json:"title" example:"Heat"`
	Year                 *int                    `json:"year,omitempty" example:"1995"`
	Overview             string                  `json:"overview,omitempty"`
	PosterPath           string                  `json:"poster_path,omitempty" doc:"TMDB image path"`
	BackdropPath         string                  `json:"backdrop_path,omitempty" doc:"TMDB image path"`
	Status               string                  `json:"status" doc:"pending, approved, queued, downloading, completed" example:"pending"`
	Outcome              string                  `json:"outcome" doc:"active, declined, cancelled, failed" example:"active"`                                                                                                                                                    //nolint:misspell // the store's spelling
	State                string                  `json:"state" doc:"The one state to show a user: pending, approved, processing, partially_available (some requested seasons are in the library), available (in the library), declined, cancelled or failed" example:"pending"` //nolint:misspell // the store's spelling
	Seasons              []int                   `json:"seasons" doc:"Series: the requested season numbers; empty means the whole series (requests made through v1 or before season requests)"`
	SeasonProgress       []RequestSeasonProgress `json:"season_progress" doc:"Series season requests: each requested season's episodes, once the series is in the library; empty otherwise"`
	OutcomeReason        string                  `json:"outcome_reason,omitempty" doc:"Why the request was declined or withdrawn, when a reason was given"`
	RequestedByUserID    ID                      `json:"requested_by_user_id,omitempty" example:"1"`
	RequestedByProfileID ID                      `json:"requested_by_profile_id,omitempty" example:"p-owner"`
	IntegrationKind      string                  `json:"integration_kind,omitempty" doc:"Admins only: the download server's kind" example:"radarr"`
	IsAnime              bool                    `json:"is_anime"`
	Targets              []RequestTarget         `json:"targets" doc:"Empty, never null"`
	ExternalID           string                  `json:"external_id,omitempty" doc:"Admins only: the integration's own identifier"`
	ExternalStatus       string                  `json:"external_status,omitempty" doc:"Admins only: the status as the download server reports it"`
	LibraryContentID     string                  `json:"library_content_id,omitempty" doc:"The catalog item once the media is in the library"`
	LastError            string                  `json:"last_error,omitempty" doc:"Admins only: why the last submission to a download server failed. It can name servers and routing rules"`
	CreatedAt            Instant                 `json:"created_at" example:"2026-01-02T03:04:05.000Z"`
	UpdatedAt            Instant                 `json:"updated_at" example:"2026-01-02T03:04:05.000Z"`
	ApprovedAt           *Instant                `json:"approved_at,omitempty"`
	CompletedAt          *Instant                `json:"completed_at,omitempty"`
	Download             *RequestDownload        `json:"download,omitempty" doc:"How far the request's downloads are over all its servers (1080p and 4K together), while any reports them: bytes summed, the phase that needs the most attention, the latest estimate, and the oldest report's time"`
}

// MediaRequestOutput is a single-request response.
type MediaRequestOutput struct {
	Body MediaRequest
}

// MediaRequestCollection is the listMyRequests envelope.
type MediaRequestCollection struct {
	Collection[MediaRequest]
}

// MediaRequestCreate is the createRequest body: the media as the client saw
// it on a discovery or search card.
type MediaRequestCreate struct {
	MediaType    string `json:"media_type" enum:"movie,series" example:"movie"`
	TMDBID       int    `json:"tmdb_id" minimum:"1" doc:"TMDB identifier (external, not a Silo ID)" example:"949"`
	TVDBID       *int   `json:"tvdb_id,omitempty" minimum:"1" doc:"TVDB identifier (external, not a Silo ID)"`
	IMDbID       string `json:"imdb_id,omitempty" example:"tt0113277"`
	Title        string `json:"title" minLength:"1" example:"Heat"`
	Year         *int   `json:"year,omitempty" example:"1995"`
	Overview     string `json:"overview,omitempty"`
	PosterPath   string `json:"poster_path,omitempty" doc:"TMDB image path"`
	BackdropPath string `json:"backdrop_path,omitempty" doc:"TMDB image path"`
	Seasons      []int  `json:"seasons,omitempty" maxItems:"200" doc:"Series only: the season numbers to request, starting at 1 (a season below 1 is refused). Omitted: every aired season not yet complete in the library" example:"[2,3]"`
}

// MediaRequestCreateInput is the createRequest request.
type MediaRequestCreateInput struct {
	Body MediaRequestCreate
}

// MediaRequestListInput is the listMyRequests query.
type MediaRequestListInput struct {
	Status  string `query:"status" enum:"pending,approved,queued,downloading,completed" doc:"Only requests in this status" example:"pending"`
	Outcome string `query:"outcome" enum:"active,declined,cancelled,failed" doc:"Only requests with this outcome" example:"active"` //nolint:misspell // the store's spelling
	Limit   int    `query:"limit" minimum:"1" maximum:"50" default:"50" doc:"Page size; default 50, maximum 50" example:"50"`
	Cursor  string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvIjo1MH0"`
}

// MediaRequestGetInput names one request.
type MediaRequestGetInput struct {
	ID ID `path:"id" minLength:"1" doc:"The request" example:"1834729"`
}

// RequestMediaSearchInput is the searchRequestMedia query.
type RequestMediaSearchInput struct {
	Query     string `query:"q" minLength:"1" required:"true" doc:"Title search text" example:"heat"`
	MediaType string `query:"media_type" enum:"movie,series,all" default:"all" doc:"Restrict results to one media type" example:"all"`
	ProviderPage
}

// ProviderPage is the provider page-number parameter of the TMDB-backed
// operations.
type ProviderPage struct {
	Page int `query:"page" minimum:"1" default:"1" doc:"Provider result page, 1-based" example:"1"`
}

// RequestMediaPageOutput is a provider page response.
type RequestMediaPageOutput struct {
	Body RequestMediaPage
}

// RequestMediaDetailInput names one piece of media.
type RequestMediaDetailInput struct {
	MediaType string `path:"media_type" enum:"movie,series" doc:"The media type" example:"movie"`
	TMDBID    int    `path:"tmdb_id" minimum:"1" doc:"TMDB identifier (external, not a Silo ID)" example:"949"`
}

// RequestMediaDetailOutput is the getRequestMediaDetail response.
type RequestMediaDetailOutput struct {
	Body RequestMediaDetail
}

// DiscoverSectionCollectionOutput is the listDiscoverSections response.
type DiscoverSectionCollectionOutput struct {
	Body DiscoverSectionCollection
}

// DiscoverSectionInput names one discovery row.
type DiscoverSectionInput struct {
	Section string `path:"section" enum:"trending_movies,trending_series,popular_movies,popular_series,upcoming_movies,on_air_series" doc:"The discovery row" example:"trending_movies"`
	ProviderPage
}

// DiscoverSectionOutput is the getDiscoverSection response.
type DiscoverSectionOutput struct {
	Body DiscoverSection
}

// DiscoverBrandCollectionOutput is the studio, network, and genre list
// response.
type DiscoverBrandCollectionOutput struct {
	Body DiscoverBrandCollection
}

// DiscoverBrowseInput is the studio and network browse request.
type DiscoverBrowseInput struct {
	Slug string `path:"slug" minLength:"1" doc:"The brand slug from the list operation" example:"marvel-studios"`
	Sort string `query:"sort" enum:"popularity,vote_average,release_date" default:"popularity" doc:"Result order" example:"popularity"`
	ProviderPage
}

// DiscoverGenreBrowseInput is the genre browse request; a genre is browsed
// for one media type.
type DiscoverGenreBrowseInput struct {
	Slug      string `path:"slug" minLength:"1" doc:"The genre slug from the list operation" example:"action"`
	MediaType string `query:"media_type" enum:"movie,series" required:"true" doc:"The media type to browse" example:"movie"`
	Sort      string `query:"sort" enum:"popularity,vote_average,release_date" default:"popularity" doc:"Result order" example:"popularity"`
	ProviderPage
}

// DiscoverBrowsePageOutput is a browse response.
type DiscoverBrowsePageOutput struct {
	Body DiscoverBrowsePage
}

// Operation ids; the listMyRequests cursor scope is bound to its id.
const (
	opCreateRequest         = "createRequest"
	opListMyRequests        = "listMyRequests"
	opGetRequest            = "getRequest"
	opSearchRequestMedia    = "searchRequestMedia"
	opGetRequestMediaDetail = "getRequestMediaDetail"
	opListDiscoverSections  = "listDiscoverSections"
	opGetDiscoverSection    = "getDiscoverSection"
	opListDiscoverGenres    = "listDiscoverGenres"
	opListDiscoverNetworks  = "listDiscoverNetworks"
	opListDiscoverStudios   = "listDiscoverStudios"
	opBrowseDiscoverGenre   = "browseDiscoverGenre"
	opBrowseDiscoverNetwork = "browseDiscoverNetwork"
	opBrowseDiscoverStudio  = "browseDiscoverStudio"
	opFollowRequestMedia    = "followRequestMedia"
	opUnfollowRequestMedia  = "unfollowRequestMedia"
)

const requestsTag = "requests"

// requestOperationIDs lists the profile-scoped request operations; tests
// check each documents the viewer-access headers.
var requestOperationIDs = []string{
	opCreateRequest, opListMyRequests, opGetRequest, opSearchRequestMedia, opGetRequestMediaDetail,
	opListDiscoverSections, opGetDiscoverSection, opListDiscoverGenres, opListDiscoverNetworks, opListDiscoverStudios,
	opBrowseDiscoverGenre, opBrowseDiscoverNetwork, opBrowseDiscoverStudio,
	opFollowRequestMedia, opUnfollowRequestMedia,
}

func registerRequests(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)

	create := humaOp(http.MethodPost, Prefix+"/requests", opCreateRequest, requestsTag,
		"Request a movie or series for the library.")
	create.DefaultStatus = http.StatusCreated
	// The service refuses media that is already in the library or already
	// has an active request (409) and a requester over quota (429).
	create.Errors = []int{http.StatusConflict, http.StatusTooManyRequests}
	Register(reg, Operation{Operation: create, Class: ClassProfileScoped, DemoRestricted: isMutatingMethod(create.Method), ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}, reg.createRequest)

	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/requests/mine", opListMyRequests, requestsTag,
			"List the account's media requests, newest first."),
		Class: ClassProfileScoped, ServiceBacked: true,
	}, func(ctx context.Context, in *MediaRequestListInput) (*MediaRequestCollectionOutput, error) {
		return reg.listMyRequests(ctx, cursors, in)
	})

	search := humaOp(http.MethodGet, Prefix+"/requests/search", opSearchRequestMedia, requestsTag,
		"Search the request provider for movies and series.")
	search.Errors = []int{http.StatusConflict}
	Register(reg, Operation{Operation: search, Class: ClassProfileScoped, ServiceBacked: true}, reg.searchRequestMedia)

	detail := humaOp(http.MethodGet, Prefix+"/requests/detail/{media_type}/{tmdb_id}", opGetRequestMediaDetail, requestsTag,
		"Get a movie's or series' detail document with its availability and request state.")
	detail.Errors = []int{http.StatusConflict}
	Register(reg, Operation{Operation: detail, Class: ClassProfileScoped, ServiceBacked: true}, reg.getRequestMediaDetail)

	sections := humaOp(http.MethodGet, Prefix+"/requests/discover", opListDiscoverSections, requestsTag,
		"List the discovery rows with their first page.")
	sections.Errors = []int{http.StatusConflict}
	Register(reg, Operation{Operation: sections, Class: ClassProfileScoped, ServiceBacked: true}, reg.listDiscoverSections)

	section := humaOp(http.MethodGet, Prefix+"/requests/discover/{section}", opGetDiscoverSection, requestsTag,
		"Get one page of a discovery row.")
	section.Errors = []int{http.StatusConflict}
	Register(reg, Operation{Operation: section, Class: ClassProfileScoped, ServiceBacked: true}, reg.getDiscoverSection)

	for _, brand := range []struct{ id, segment, summary string }{
		{opListDiscoverGenres, "genres", "List the browsable genres."},
		{opListDiscoverNetworks, "networks", "List the browsable networks."},
		{opListDiscoverStudios, "studios", "List the browsable studios."},
	} {
		op := humaOp(http.MethodGet, Prefix+"/requests/discover/"+brand.segment, brand.id, requestsTag, brand.summary)
		op.Errors = []int{http.StatusConflict}
		id := brand.id
		Register(reg, Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true},
			func(ctx context.Context, _ *struct{}) (*DiscoverBrandCollectionOutput, error) {
				return reg.listDiscoverBrands(ctx, id)
			})
	}

	genre := humaOp(http.MethodGet, Prefix+"/requests/discover/browse/genre/{slug}", opBrowseDiscoverGenre, requestsTag,
		"Browse a genre's movies or series.")
	genre.Errors = []int{http.StatusConflict}
	Register(reg, Operation{Operation: genre, Class: ClassProfileScoped, ServiceBacked: true}, reg.browseDiscoverGenre)

	network := humaOp(http.MethodGet, Prefix+"/requests/discover/browse/network/{slug}", opBrowseDiscoverNetwork, requestsTag,
		"Browse a network's series.")
	network.Errors = []int{http.StatusConflict}
	Register(reg, Operation{Operation: network, Class: ClassProfileScoped, ServiceBacked: true},
		func(ctx context.Context, in *DiscoverBrowseInput) (*DiscoverBrowsePageOutput, error) {
			return reg.browseDiscoverBrand(ctx, in, func(svc MediaRequestService, viewer mediarequests.Viewer) (*mediarequests.DiscoverBrowseResponse, error) {
				return svc.BrowseNetwork(ctx, viewer, in.Slug, in.Sort, in.Page)
			})
		})

	studio := humaOp(http.MethodGet, Prefix+"/requests/discover/browse/studio/{slug}", opBrowseDiscoverStudio, requestsTag,
		"Browse a studio's movies.")
	studio.Errors = []int{http.StatusConflict}
	Register(reg, Operation{Operation: studio, Class: ClassProfileScoped, ServiceBacked: true},
		func(ctx context.Context, in *DiscoverBrowseInput) (*DiscoverBrowsePageOutput, error) {
			return reg.browseDiscoverBrand(ctx, in, func(svc MediaRequestService, viewer mediarequests.Viewer) (*mediarequests.DiscoverBrowseResponse, error) {
				return svc.BrowseStudio(ctx, viewer, in.Slug, in.Sort, in.Page)
			})
		})

	// Following is keyed by title: the viewer follows a title someone else
	// already requested, without learning whose request it is.
	follow := humaOp(http.MethodPut, Prefix+"/requests/follows/{media_type}/{tmdb_id}", opFollowRequestMedia, requestsTag,
		"Get notified when a title that already has an active request becomes available.")
	// 409 when the title has no active request (request it instead).
	follow.Errors = []int{http.StatusNotFound, http.StatusConflict}
	Register(reg, Operation{Operation: follow, Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}, reg.followRequestMedia)

	unfollow := humaOp(http.MethodDelete, Prefix+"/requests/follows/{media_type}/{tmdb_id}", opUnfollowRequestMedia, requestsTag,
		"Stop following a title.")
	unfollow.DefaultStatus = http.StatusNoContent
	Register(reg, Operation{Operation: unfollow, Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}, reg.unfollowRequestMedia)

	get := humaOp(http.MethodGet, Prefix+"/requests/{id}", opGetRequest, requestsTag,
		"Get one of the account's media requests.")
	get.Errors = []int{http.StatusConflict}
	Register(reg, Operation{Operation: get, Class: ClassProfileScoped, ServiceBacked: true}, reg.getRequest)
}

// MediaRequestCollectionOutput is the listMyRequests response.
type MediaRequestCollectionOutput struct {
	Body MediaRequestCollection
}

// requestViewer builds the service viewer from the request context, as v1's
// requestViewer does. The gates already guarantee a claims and a profile.
func (reg *Registry) requestViewer(ctx context.Context) (MediaRequestService, mediarequests.Viewer, *Problem) {
	if reg.deps.Requests == nil {
		return nil, mediarequests.Viewer{}, unavailable("requests")
	}
	claims := claimsFrom(ctx)
	profileID := strings.TrimSpace(profileFrom(ctx))
	if claims == nil || claims.UserID == 0 || profileID == "" {
		return nil, mediarequests.Viewer{}, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	return reg.deps.Requests, mediarequests.Viewer{UserID: claims.UserID, ProfileID: profileID, IsAdmin: claims.Role == models.RoleAdmin}, nil
}

// createRequest has no durable client request identity. An uncertain retry after
// terminal completion can create and submit another request.
func (reg *Registry) createRequest(ctx context.Context, in *MediaRequestCreateInput) (*MediaRequestOutput, error) {
	svc, viewer, p := reg.requestViewer(ctx)
	if p != nil {
		return nil, p
	}
	req, err := svc.CreateRequest(ctx, viewer, mediarequests.CreateRequestInput{
		MediaType:    mediarequests.MediaType(in.Body.MediaType),
		TMDBID:       in.Body.TMDBID,
		TVDBID:       in.Body.TVDBID,
		IMDbID:       in.Body.IMDbID,
		Title:        in.Body.Title,
		Year:         in.Body.Year,
		Overview:     in.Body.Overview,
		PosterPath:   in.Body.PosterPath,
		BackdropPath: in.Body.BackdropPath,
		Seasons:      in.Body.Seasons,
	})
	if err != nil {
		return nil, requestProblem(err)
	}
	return &MediaRequestOutput{Body: mediaRequestOf(req, viewer)}, nil
}

// listMyRequests pages by the last emitted creation time and unique request ID.
func (reg *Registry) listMyRequests(ctx context.Context, cursors *Cursors, in *MediaRequestListInput) (*MediaRequestCollectionOutput, error) {
	svc, viewer, p := reg.requestViewer(ctx)
	if p != nil {
		return nil, p
	}
	scope := CursorScope{
		OperationID: opListMyRequests,
		Security:    strconv.Itoa(viewer.UserID) + "/" + viewer.ProfileID,
		Filter:      in.Status + "|" + in.Outcome,
		Sort:        "-created_at",
		Tiebreaker:  "id",
	}
	var before *mediarequests.RequestPageKey
	if in.Cursor != "" {
		before = new(mediarequests.RequestPageKey)
		if p := cursors.Decode(scope, in.Cursor, before); p != nil {
			return nil, p
		}
		if before.ID == "" || before.CreatedAt.IsZero() {
			return nil, NewProblem(TypeInvalidCursor, "The cursor position is invalid.")
		}
	}
	rows, err := svc.ListMine(ctx, viewer, mediarequests.ListFilter{
		Status:  mediarequests.Status(in.Status),
		Outcome: mediarequests.Outcome(in.Outcome),
		Limit:   in.Limit + 1,
		Before:  before,
	})
	if err != nil {
		return nil, requestProblem(err)
	}
	next := ""
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		last := rows[len(rows)-1]
		next, err = cursors.Encode(scope, mediarequests.RequestPageKey{CreatedAt: last.CreatedAt, ID: last.ID})
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Unable to encode request cursor.")
		}
	}
	items := make([]MediaRequest, 0, len(rows))
	for _, r := range rows {
		items = append(items, mediaRequestOf(r, viewer))
	}
	return &MediaRequestCollectionOutput{Body: MediaRequestCollection{Collection: Paginated(items, next)}}, nil
}

// getRequest is v1 GET /requests/{id}: the requester or an administrator.
func (reg *Registry) getRequest(ctx context.Context, in *MediaRequestGetInput) (*MediaRequestOutput, error) {
	svc, viewer, p := reg.requestViewer(ctx)
	if p != nil {
		return nil, p
	}
	req, err := svc.GetRequest(ctx, viewer, string(in.ID))
	if err != nil {
		return nil, requestProblem(err)
	}
	return &MediaRequestOutput{Body: mediaRequestOf(req, viewer)}, nil
}

// searchRequestMedia is v1 GET /requests/search.
func (reg *Registry) searchRequestMedia(ctx context.Context, in *RequestMediaSearchInput) (*RequestMediaPageOutput, error) {
	svc, viewer, p := reg.requestViewer(ctx)
	if p != nil {
		return nil, p
	}
	if strings.TrimSpace(in.Query) == "" {
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "query.q", Code: codeRequired, Detail: "expected a non-blank search query"})
	}
	page, err := svc.Search(ctx, viewer, in.Query, mediarequests.MediaType(in.MediaType), in.Page)
	if err != nil {
		return nil, requestProblem(err)
	}
	return &RequestMediaPageOutput{Body: requestMediaPageOf(page)}, nil
}

// getRequestMediaDetail is v1 GET /requests/detail/{media_type}/{tmdb_id}.
func (reg *Registry) getRequestMediaDetail(ctx context.Context, in *RequestMediaDetailInput) (*RequestMediaDetailOutput, error) {
	svc, viewer, p := reg.requestViewer(ctx)
	if p != nil {
		return nil, p
	}
	detail, err := svc.GetDetail(ctx, viewer, mediarequests.MediaType(in.MediaType), in.TMDBID)
	if err != nil {
		return nil, requestProblem(err)
	}
	return &RequestMediaDetailOutput{Body: requestMediaDetailOf(detail)}, nil
}

// RequestMediaStateOutput is the followRequestMedia response.
type RequestMediaStateOutput struct {
	Body RequestMediaState
}

func (reg *Registry) followRequestMedia(ctx context.Context, in *RequestMediaDetailInput) (*RequestMediaStateOutput, error) {
	svc, viewer, p := reg.requestViewer(ctx)
	if p != nil {
		return nil, p
	}
	state, err := svc.Follow(ctx, viewer, mediarequests.MediaType(in.MediaType), in.TMDBID)
	if err != nil {
		return nil, requestProblem(err)
	}
	return &RequestMediaStateOutput{Body: requestMediaStateOf(state)}, nil
}

func (reg *Registry) unfollowRequestMedia(ctx context.Context, in *RequestMediaDetailInput) (*struct{}, error) {
	svc, viewer, p := reg.requestViewer(ctx)
	if p != nil {
		return nil, p
	}
	if err := svc.Unfollow(ctx, viewer, mediarequests.MediaType(in.MediaType), in.TMDBID); err != nil {
		return nil, requestProblem(err)
	}
	return nil, nil
}

// listDiscoverSections is v1 GET /requests/discover.
func (reg *Registry) listDiscoverSections(ctx context.Context, _ *struct{}) (*DiscoverSectionCollectionOutput, error) {
	svc, viewer, p := reg.requestViewer(ctx)
	if p != nil {
		return nil, p
	}
	sections, err := svc.DiscoverAll(ctx, viewer)
	if err != nil {
		return nil, requestProblem(err)
	}
	items := make([]DiscoverSection, 0, len(sections))
	for i := range sections {
		items = append(items, discoverSectionOf(&sections[i]))
	}
	return &DiscoverSectionCollectionOutput{Body: DiscoverSectionCollection{Collection: NewCollection(items)}}, nil
}

// getDiscoverSection is v1 GET /requests/discover/{section}.
func (reg *Registry) getDiscoverSection(ctx context.Context, in *DiscoverSectionInput) (*DiscoverSectionOutput, error) {
	svc, viewer, p := reg.requestViewer(ctx)
	if p != nil {
		return nil, p
	}
	section, err := svc.Discover(ctx, viewer, in.Section, in.Page)
	if err != nil {
		return nil, requestProblem(err)
	}
	return &DiscoverSectionOutput{Body: discoverSectionOf(section)}, nil
}

// listDiscoverBrands is v1 GET /requests/discover/{genres,networks,studios}.
func (reg *Registry) listDiscoverBrands(ctx context.Context, opID string) (*DiscoverBrandCollectionOutput, error) {
	svc, viewer, p := reg.requestViewer(ctx)
	if p != nil {
		return nil, p
	}
	var cards []mediarequests.DiscoverBrandCard
	var err error
	switch opID {
	case opListDiscoverGenres:
		cards, err = svc.ListGenres(ctx, viewer)
	case opListDiscoverNetworks:
		cards, err = svc.ListNetworks(ctx, viewer)
	default:
		cards, err = svc.ListStudios(ctx, viewer)
	}
	if err != nil {
		return nil, requestProblem(err)
	}
	items := make([]DiscoverBrand, 0, len(cards))
	for _, c := range cards {
		items = append(items, DiscoverBrand{
			TMDBID: c.TMDBID, Slug: c.Slug, DisplayName: c.DisplayName, LogoURL: c.LogoURL,
			GradientFrom: c.GradientFrom, GradientTo: c.GradientTo, SeriesSupported: c.SeriesSupported,
		})
	}
	return &DiscoverBrandCollectionOutput{Body: DiscoverBrandCollection{Collection: NewCollection(items)}}, nil
}

// browseDiscoverBrand is v1 GET /requests/discover/browse/{network,studio}/{slug}.
func (reg *Registry) browseDiscoverBrand(ctx context.Context, in *DiscoverBrowseInput, browse func(MediaRequestService, mediarequests.Viewer) (*mediarequests.DiscoverBrowseResponse, error)) (*DiscoverBrowsePageOutput, error) {
	svc, viewer, p := reg.requestViewer(ctx)
	if p != nil {
		return nil, p
	}
	if p := requireSlug(in.Slug); p != nil {
		return nil, p
	}
	resp, err := browse(svc, viewer)
	if err != nil {
		return nil, requestProblem(err)
	}
	return &DiscoverBrowsePageOutput{Body: discoverBrowsePageOf(resp)}, nil
}

// browseDiscoverGenre is v1 GET /requests/discover/browse/genre/{slug}.
func (reg *Registry) browseDiscoverGenre(ctx context.Context, in *DiscoverGenreBrowseInput) (*DiscoverBrowsePageOutput, error) {
	svc, viewer, p := reg.requestViewer(ctx)
	if p != nil {
		return nil, p
	}
	if p := requireSlug(in.Slug); p != nil {
		return nil, p
	}
	resp, err := svc.BrowseGenre(ctx, viewer, in.Slug, mediarequests.MediaType(in.MediaType), in.Sort, in.Page)
	if err != nil {
		return nil, requestProblem(err)
	}
	return &DiscoverBrowsePageOutput{Body: discoverBrowsePageOf(resp)}, nil
}

// requireSlug refuses a blank slug before the service sees it (v1 trims and
// then answers not_found); the trimmed value is what v1 passes on.
func requireSlug(slug string) *Problem {
	if strings.TrimSpace(slug) == "" {
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "path.slug", Code: codeRequired, Detail: "expected a non-blank slug"})
	}
	return nil
}

// requestProblem renders a request-service failure with the decisions v1's
// writeRequestServiceError makes, as v2 problems: invalid input and
// provider validation are 422, a disabled feature is 409
// capability_disabled, an exhausted quota is 429, duplicates and state
// conflicts are 409, an unreachable integration is 503
// dependency_unavailable, and anything else is an internal error.
func requestProblem(err error) *Problem {
	if verr, ok := errors.AsType[*mediarequests.ValidationError](err); ok {
		p := NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.")
		if verr.FormError != "" {
			p.Detail = verr.FormError
		}
		errs := make([]ProblemError, 0, len(verr.FieldErrors))
		for field, detail := range verr.FieldErrors {
			errs = append(errs, ProblemError{Location: "body." + field, Code: codeInvalid, Detail: detail})
		}
		slices.SortFunc(errs, func(a, b ProblemError) int { return cmp.Compare(a.Location, b.Location) })
		return p.WithErrors(errs...)
	}
	var quota mediarequests.QuotaError
	switch {
	case errors.As(err, &quota):
		return NewProblem(TypeRateLimited, "Request quota exceeded: "+strconv.Itoa(quota.Used)+" of "+strconv.Itoa(quota.Limit)+" requests used in the last "+strconv.Itoa(quota.WindowDays)+" days.")
	case errors.Is(err, mediarequests.ErrInvalidMediaType):
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "query.media_type", Code: codeInvalid, Detail: "expected movie or series"})
	case errors.Is(err, mediarequests.ErrInvalidInput):
		return NewProblem(TypeValidationFailed, "The request did not pass validation.")
	case errors.Is(err, mediarequests.ErrRequestsDisabled):
		return CapabilityProblem(StateDisabled, "requests")
	case errors.Is(err, mediarequests.ErrUserBlocked):
		return NewProblem(TypePermissionDenied, "This account is blocked from requesting media.")
	case errors.Is(err, mediarequests.ErrAlreadyAvailable):
		return NewProblem(TypeConflict, "The media is already available in the library.")
	case errors.Is(err, mediarequests.ErrAlreadyRequested):
		return NewProblem(TypeConflict, "The media already has an active request.")
	case errors.Is(err, mediarequests.ErrNotRequested):
		return NewProblem(TypeConflict, "The media has no active request to follow; request it instead.")
	case errors.Is(err, mediarequests.ErrForbidden):
		return NewProblem(TypePermissionDenied, "Request access denied.")
	case errors.Is(err, mediarequests.ErrNotFound):
		return NewProblem(TypeNotFound, "The request was not found.")
	case errors.Is(err, mediarequests.ErrInvalidState):
		return NewProblem(TypeConflict, "The request is not in a state that allows this action.")
	case errors.Is(err, mediarequests.ErrIntegrationUnreachable):
		// Detail is a host-written sentence; the underlying cause stays out.
		if unreachable, ok := errors.AsType[*mediarequests.IntegrationUnreachableError](err); ok && unreachable.Detail != "" {
			return NewProblem(TypeDependencyUnavailable, unreachable.Detail)
		}
		return NewProblem(TypeDependencyUnavailable, "The request integration could not be reached.")
	}
	return NewProblem(TypeInternalError, "An unexpected error occurred.")
}

// mediaRequestOf maps a request for the viewer. The download server details
// (which server and routing rule took each target, the server's own ids and
// raw statuses, and the submission and target errors, which can name servers
// and routing rules) go to an admin only. A requester keeps each target's
// quality, status and download progress.
func mediaRequestOf(r *mediarequests.Request, viewer mediarequests.Viewer) MediaRequest {
	out := MediaRequest{
		ID:               ID(r.ID),
		Provider:         r.Provider,
		MediaType:        string(r.MediaType),
		TMDBID:           r.TMDBID,
		TVDBID:           r.TVDBID,
		IMDbID:           r.IMDbID,
		Title:            r.Title,
		Year:             r.Year,
		Overview:         r.Overview,
		PosterPath:       r.PosterPath,
		BackdropPath:     r.BackdropPath,
		Status:           string(r.Status),
		Outcome:          string(r.Outcome),
		State:            string(r.State()),
		Seasons:          NonNil(r.Seasons),
		SeasonProgress:   requestSeasonProgressOf(r.SeasonProgress),
		OutcomeReason:    r.OutcomeReason,
		IsAnime:          r.IsAnime,
		Targets:          make([]RequestTarget, 0, len(r.Targets)),
		LibraryContentID: r.LibraryContentID,
		CreatedAt:        NewInstant(r.CreatedAt),
		UpdatedAt:        NewInstant(r.UpdatedAt),
		ApprovedAt:       instantPtr(r.ApprovedAt),
		CompletedAt:      instantPtr(r.CompletedAt),
		Download:         requestDownloadOf(r.Download()),
	}
	if viewer.IsAdmin {
		out.IntegrationKind, out.ExternalID, out.ExternalStatus, out.LastError = r.IntegrationKind, r.ExternalID, r.ExternalStatus, r.LastError
	}
	if r.RequestedByUserID != 0 {
		out.RequestedByUserID = IDFromInt(int64(r.RequestedByUserID))
	}
	out.RequestedByProfileID = ID(r.RequestedByProfileID)
	for _, t := range r.Targets {
		target := RequestTarget{
			ID: IDFromInt(t.ID), RequestID: ID(t.RequestID), Quality: string(t.Quality), IsAnime: t.IsAnime,
			Status: string(t.Status), CreatedAt: NewInstant(t.CreatedAt), UpdatedAt: NewInstant(t.UpdatedAt),
			Download: requestDownloadOf(t.Download),
		}
		if viewer.IsAdmin {
			target.IntegrationID, target.IntegrationKind, target.InstanceName = t.IntegrationID, t.IntegrationKind, t.InstanceName
			target.ExternalID, target.ExternalStatus, target.LastError, target.RouteName = t.ExternalID, t.ExternalStatus, t.LastError, t.RouteName
		}
		out.Targets = append(out.Targets, target)
	}
	return out
}

// requestDownloadOf maps download progress. The byte counts and percent are
// left out while the size is unknown (a total of 0).
func requestDownloadOf(d *mediarequests.DownloadProgress) *RequestDownload {
	if d == nil {
		return nil
	}
	out := &RequestDownload{
		Phase:                 string(d.Phase),
		EstimatedCompletionAt: instantPtr(d.EstimatedCompletion),
		Downloads:             max(d.Downloads, 0),
		UpdatedAt:             NewInstant(d.UpdatedAt),
	}
	if d.BytesTotal > 0 {
		total, left := d.BytesTotal, min(max(d.BytesLeft, 0), d.BytesTotal)
		percent := downloadPercent(total, left)
		out.BytesTotal, out.BytesLeft, out.Percent = &total, &left, &percent
	}
	return out
}

// downloadPercent is floor((total-left)*100/total) for 0 <= left <= total and
// total > 0. The product is taken in 128 bits, so no total can overflow it.
func downloadPercent(total, left int64) int {
	hi, lo := bits.Mul64(uint64(total-left), 100)
	percent, _ := bits.Div64(hi, lo, uint64(total))
	return int(percent)
}

func requestSeasonProgressOf(progress []mediarequests.SeasonProgress) []RequestSeasonProgress {
	out := make([]RequestSeasonProgress, 0, len(progress))
	for _, p := range progress {
		out = append(out, RequestSeasonProgress{SeasonNumber: p.Season, EpisodesAired: p.Aired, EpisodesAvailable: p.Have})
	}
	return out
}

func requestMediaStateOf(s mediarequests.RequestState) RequestMediaState {
	return RequestMediaState{Status: string(s.Status), Requestable: s.Requestable, Reason: s.Reason, RequestID: ID(s.RequestID), Following: s.Following, RequestedByViewer: s.RequestedByViewer, State: string(s.State), Download: requestDownloadOf(s.Download)}
}

func requestMediaResultsOf(results []mediarequests.MediaResult) []RequestMediaResult {
	out := make([]RequestMediaResult, 0, len(results))
	for _, r := range results {
		out = append(out, RequestMediaResult{
			MediaType: string(r.MediaType), TMDBID: r.TMDBID, Title: r.Title, Year: r.Year, Overview: r.Overview,
			PosterPath: r.PosterPath, BackdropPath: r.BackdropPath, ReleaseDate: r.ReleaseDate,
			Popularity: r.Popularity, VoteAverage: r.VoteAverage, Availability: string(r.Availability),
			LibraryContentID: r.LibraryContentID, Request: requestMediaStateOf(r.Request),
		})
	}
	return out
}

func requestMediaPageOf(p *mediarequests.MediaPage) RequestMediaPage {
	return RequestMediaPage{Page: p.Page, TotalPages: p.TotalPages, TotalResults: p.TotalResults, Results: requestMediaResultsOf(p.Results)}
}

func requestMediaDetailOf(d *mediarequests.MediaDetail) RequestMediaDetail {
	cast := make([]RequestMediaCastMember, 0, len(d.Cast))
	for _, c := range d.Cast {
		cast = append(cast, RequestMediaCastMember{Name: c.Name, Character: c.Character, ProfilePath: c.ProfilePath, Order: c.Order})
	}
	return RequestMediaDetail{
		MediaType: string(d.MediaType), TMDBID: d.TMDBID, IMDbID: d.IMDbID, TVDBID: d.TVDBID,
		Title: d.Title, OriginalTitle: d.OriginalTitle, Tagline: d.Tagline, Overview: d.Overview,
		PosterPath: d.PosterPath, BackdropPath: d.BackdropPath, ReleaseDate: d.ReleaseDate, Year: d.Year,
		Runtime: d.Runtime, Genres: NonNil(d.Genres), VoteAverage: d.VoteAverage, VoteCount: d.VoteCount,
		Status: d.Status, Homepage: d.Homepage, ContentRating: d.ContentRating,
		ProductionCompanies: NonNil(d.ProductionCompanies), NumberOfSeasons: d.NumberOfSeasons,
		NumberOfEpisodes: d.NumberOfEpisodes, FirstAirDate: d.FirstAirDate, LastAirDate: d.LastAirDate,
		Networks: NonNil(d.Networks), Cast: cast, Director: d.Director, Creators: NonNil(d.Creators),
		Recommendations: requestMediaResultsOf(d.Recommendations), Availability: string(d.Availability),
		LibraryContentID: d.LibraryContentID, Request: requestMediaStateOf(d.Request),
		Seasons: requestMediaSeasonsOf(d.Seasons),
	}
}

func requestMediaSeasonsOf(seasons []mediarequests.RequestSeason) []RequestMediaSeason {
	out := make([]RequestMediaSeason, 0, len(seasons))
	for _, s := range seasons {
		out = append(out, RequestMediaSeason{
			SeasonNumber: s.Number, Name: s.Name, EpisodeCount: s.EpisodeCount, AirDate: s.AirDate,
			PosterPath: s.PosterPath, Availability: string(s.Availability), Requested: s.Requested,
		})
	}
	return out
}

func discoverSectionOf(s *mediarequests.DiscoverySection) DiscoverSection {
	return DiscoverSection{
		Key: s.Key, Title: s.Title, Page: s.Page, TotalPages: s.TotalPages, TotalResults: s.TotalResults,
		Results: requestMediaResultsOf(s.Results), NextPage: s.NextPage,
	}
}

func discoverBrowsePageOf(r *mediarequests.DiscoverBrowseResponse) DiscoverBrowsePage {
	return DiscoverBrowsePage{
		Kind: r.Kind, Slug: r.Slug, DisplayName: r.DisplayName, LogoURL: r.LogoURL, MediaType: string(r.MediaType),
		Sort: r.Sort, Page: r.Page, TotalPages: r.TotalPages, Results: requestMediaResultsOf(r.Results),
	}
}

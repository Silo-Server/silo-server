package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// Request routing administration: the ordered rules that decide which server
// each quality tier of a request goes to (docs/architecture/media-requests.md,
// "Routing").

const (
	opListRequestRoutes        = "listRequestRoutes"
	opGetRequestRoute          = "getRequestRoute"
	opCreateRequestRoute       = "createRequestRoute"
	opUpdateRequestRoute       = "updateRequestRoute"
	opDeleteRequestRoute       = "deleteRequestRoute"
	opReorderRequestRoutes     = "reorderRequestRoutes"
	opPreviewRequestRoute      = "previewRequestRoute"
	opSearchRequestRouteTitles = "searchRequestRouteTitles"
	opGetRequestRouting        = "getRequestRouting"
	opUpdateRequestRouting     = "updateRequestRouting"
)

var adminRequestRouteOperationIDs = []string{opListRequestRoutes, opGetRequestRoute, opCreateRequestRoute,
	opUpdateRequestRoute, opDeleteRequestRoute, opReorderRequestRoutes, opPreviewRequestRoute, opSearchRequestRouteTitles,
	opGetRequestRouting, opUpdateRequestRouting}

// adminRequestRoutes is the route administration slice of the request
// service.
type adminRequestRoutes interface {
	ListRoutesAdmin(context.Context, mediarequests.Viewer) ([]mediarequests.Route, error)
	GetRoute(context.Context, mediarequests.Viewer, string) (*mediarequests.Route, error)
	CreateRoute(context.Context, mediarequests.Viewer, mediarequests.Route) (*mediarequests.Route, error)
	UpdateRouteConditional(context.Context, mediarequests.Viewer, mediarequests.Route, int64) (*mediarequests.Route, error)
	DeleteRouteConditional(context.Context, mediarequests.Viewer, string, int64) error
	ReorderRoutes(context.Context, mediarequests.Viewer, mediarequests.MediaType, []string) ([]mediarequests.Route, error)
	PreviewRoute(context.Context, mediarequests.Viewer, mediarequests.MediaType, int, int) (*mediarequests.RoutePreview, error)
	SearchRouteTitles(context.Context, mediarequests.Viewer, mediarequests.MediaType, string) ([]tmdb.MediaResult, error)
	GetRoutingOverview(context.Context, mediarequests.Viewer) (*mediarequests.RoutingOverview, error)
	UpdateRoutingModeConditional(context.Context, mediarequests.Viewer, mediarequests.RoutingMode, int64) (*mediarequests.RoutingOverview, error)
}

// AdminRequestRouting is how requests find their server.
type AdminRequestRouting struct {
	Mode string `json:"mode" enum:"standard,advanced" doc:"standard sends each media type to its one server, and 4K copies to its one server marked 4K, with each server's own settings; the routing rules are kept but paused. advanced routes with the rules."`
	// Standard and StandardUnavailableReason describe Standard whichever
	// mode is on, so a client can show what switching would do.
	Standard                  []AdminRequestStandardDestination `json:"standard" doc:"Where Standard sends each media type that has a server; empty when Standard cannot be used"`
	StandardUnavailableReason string                            `json:"standard_unavailable_reason,omitempty" doc:"Why Standard cannot be used (a media type has more than one server of a kind); absent when it can. Adding or enabling such a server turns Advanced on."`
}

// AdminRequestStandardDestination is where Standard sends one media type.
type AdminRequestStandardDestination struct {
	MediaType        string `json:"media_type" enum:"movie,series"`
	HDIntegrationID  string `json:"hd_integration_id,omitempty" doc:"The media type's one server that is not marked 4K; absent when it has none"`
	UHDIntegrationID string `json:"uhd_integration_id,omitempty" doc:"The media type's one server marked 4K; absent when it has none, and then there is no 4K copy"`
}

type AdminRequestRoutingOutput struct {
	ETag string `header:"ETag"`
	Body AdminRequestRouting
}
type AdminRequestRoutingUpdateInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        struct {
		Mode string `json:"mode" enum:"standard,advanced"`
	}
}

// AdminRequestRouteConditions narrow a route. Every set field must match; a
// list matches when the title has any of its values, and an exclude list when
// it has none of them. Its fields mirror the service's, in the same order.
type AdminRequestRouteConditions struct {
	Anime             *bool    `json:"anime,omitempty" doc:"Match anime (true) or not (false): Japanese animation, and titles TMDB tags anime or an AniDB-based list names"`
	GenreIDs          []int    `json:"genre_ids,omitempty" doc:"TMDB genre IDs"`
	KeywordIDs        []int    `json:"keyword_ids,omitempty" doc:"TMDB keyword IDs"`
	OriginalLanguages []string `json:"original_languages,omitempty" doc:"ISO 639-1 codes of the original language" example:"[\"ja\"]"`
	OriginCountries   []string `json:"origin_countries,omitempty" doc:"ISO 3166-1 country codes" example:"[\"JP\"]"`
	YearFrom          int      `json:"year_from,omitempty" doc:"First release (or first-air) year, inclusive" example:"1980"`
	YearTo            int      `json:"year_to,omitempty" doc:"Last release (or first-air) year, inclusive" example:"1989"`
	NetworkIDs        []int    `json:"network_ids,omitempty" doc:"TMDB network IDs (series)"`
	CompanyIDs        []int    `json:"company_ids,omitempty" doc:"TMDB production company IDs (movies)"`
	RequesterUserIDs  []int    `json:"requester_user_ids,omitempty" doc:"Accounts whose requests the route applies to"`

	ExcludeGenreIDs          []int    `json:"exclude_genre_ids,omitempty" doc:"Match titles with none of these TMDB genre IDs"`
	ExcludeKeywordIDs        []int    `json:"exclude_keyword_ids,omitempty" doc:"Match titles with none of these TMDB keyword IDs"`
	ExcludeOriginalLanguages []string `json:"exclude_original_languages,omitempty" doc:"Match titles whose original language is none of these ISO 639-1 codes" example:"[\"en\"]"`
	ExcludeOriginCountries   []string `json:"exclude_origin_countries,omitempty" doc:"Match titles from none of these ISO 3166-1 countries"`
	ExcludeNetworkIDs        []int    `json:"exclude_network_ids,omitempty" doc:"Match series on none of these TMDB networks"`
	ExcludeCompanyIDs        []int    `json:"exclude_company_ids,omitempty" doc:"Match movies from none of these TMDB companies"`
	ExcludeRequesterUserIDs  []int    `json:"exclude_requester_user_ids,omitempty" doc:"Match requests from none of these accounts"`
	MaxContentRating         string   `json:"max_content_rating,omitempty" doc:"Match titles whose rating is at most this one, by minimum age: the US rating, or the title's own country's when it has none; a title with neither does not match" example:"PG"`
}

// AdminRequestRouteDestination is where a route sends one quality tier.
type AdminRequestRouteDestination struct {
	IntegrationID string         `json:"integration_id,omitempty" doc:"The request server; empty when the route sends nothing for this tier"`
	Overrides     map[string]any `json:"overrides,omitempty" doc:"Server settings this route replaces, keyed like the server's plugin config: root_folder, quality_profile_id, tags, series_type, minimum_availability, ..."`
}

// AdminRequestRoute is one routing rule, or a media type's fallback.
type AdminRequestRoute struct {
	ID         string                       `json:"id" doc:"Opaque route ID; the fallback's is fallback-movie or fallback-series" example:"fallback-movie"`
	MediaType  string                       `json:"media_type" enum:"movie,series"`
	Position   int                          `json:"position" doc:"Evaluation order within the media type; the fallback is always last"`
	Name       string                       `json:"name" example:"Anime"`
	Enabled    bool                         `json:"enabled"`
	IsFallback bool                         `json:"is_fallback" doc:"The media type's Everything else: it has no conditions, comes last and cannot be deleted; with no 4K server it makes no 4K copy"`
	Conditions AdminRequestRouteConditions  `json:"conditions"`
	HD         AdminRequestRouteDestination `json:"hd" doc:"Where the HD (1080p) copy goes"`
	UHD        AdminRequestRouteDestination `json:"uhd" doc:"Where the 4K copy goes"`
	SkipUHD    bool                         `json:"skip_uhd" doc:"Matching titles get no 4K copy at all"`
}

// AdminRequestRouteBody is the editable part of a route. media_type is read
// on create only.
type AdminRequestRouteBody struct {
	MediaType  string                       `json:"media_type,omitempty" enum:"movie,series" doc:"Required on create; ignored on update"`
	Name       string                       `json:"name,omitempty" maxLength:"100"`
	Enabled    bool                         `json:"enabled"`
	Conditions AdminRequestRouteConditions  `json:"conditions"`
	HD         AdminRequestRouteDestination `json:"hd"`
	UHD        AdminRequestRouteDestination `json:"uhd"`
	SkipUHD    bool                         `json:"skip_uhd"`
}

type AdminRequestRouteIDInput struct {
	ID string `path:"id" minLength:"1" doc:"The route" example:"fallback-movie"`
}
type AdminRequestRouteOutput struct {
	ETag string `header:"ETag"`
	Body AdminRequestRoute
}
type AdminRequestRouteCreateInput struct{ Body AdminRequestRouteBody }
type AdminRequestRouteCreateOutput struct {
	Location string `header:"Location"`
	ETag     string `header:"ETag"`
	Body     AdminRequestRoute
}
type AdminRequestRouteUpdateInput struct {
	AdminRequestRouteIDInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminRequestRouteBody
}
type AdminRequestRouteDeleteInput struct {
	AdminRequestRouteIDInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminRequestRouteCollectionOutput struct {
	Body Collection[AdminRequestRoute]
}
type AdminRequestRouteReorderInput struct {
	Body struct {
		MediaType string   `json:"media_type" enum:"movie,series"`
		IDs       []string `json:"ids" maxItems:"100" doc:"Every rule of the media type, fallback excluded, in the new order"`
	}
}

// AdminRequestRoutePreviewInput asks how a title would be routed.
type AdminRequestRoutePreviewInput struct {
	Body struct {
		MediaType       string `json:"media_type" enum:"movie,series"`
		TMDBID          int    `json:"tmdb_id" minimum:"1" doc:"TMDB identifier (external, not a Silo ID)" example:"129"`
		RequesterUserID *ID    `json:"requester_user_id,omitempty" doc:"Route as this account's request; without it, rules for certain accounts do not match"`
	}
}

// AdminRequestRouteFacts is what the routes matched on.
type AdminRequestRouteFacts struct {
	GenreIDs         []int    `json:"genre_ids"`
	KeywordIDs       []int    `json:"keyword_ids"`
	OriginalLanguage string   `json:"original_language,omitempty"`
	OriginCountries  []string `json:"origin_countries"`
	Year             int      `json:"year,omitempty"`
	NetworkIDs       []int    `json:"network_ids"`
	CompanyIDs       []int    `json:"company_ids"`
	Anime            bool     `json:"anime" doc:"Japanese animation, or a title TMDB tags anime or an AniDB-based list names"`
	ContentRating    string   `json:"content_rating,omitempty" doc:"The title's US rating, or its own country's prefixed with the country code (JP:PG12) when it has none; absent when TMDB has neither" example:"TV-14"`
}

// AdminRequestRoutePreviewRule is what one route did in a preview.
type AdminRequestRoutePreviewRule struct {
	RouteID    string   `json:"route_id"`
	RouteName  string   `json:"route_name"`
	IsFallback bool     `json:"is_fallback"`
	Enabled    bool     `json:"enabled"`
	Unmet      []string `json:"unmet_conditions" doc:"The conditions the title fails, by field name (e.g. genre_ids); empty when it matches"`
	HD         string   `json:"hd" enum:"sends,skips,passes,no_match,already_decided" doc:"What the route did for the HD copy"`
	UHD        string   `json:"uhd" enum:"sends,skips,passes,no_match,already_decided" doc:"What the route did for the 4K copy"`
}

// AdminRequestRouteTitle is a title the admin can try the rules on.
type AdminRequestRouteTitle struct {
	TMDBID     int    `json:"tmdb_id" doc:"TMDB identifier (external, not a Silo ID)" example:"129"`
	MediaType  string `json:"media_type" enum:"movie,series"`
	Title      string `json:"title" example:"Spirited Away"`
	Year       int    `json:"year,omitempty" example:"2001"`
	PosterPath string `json:"poster_path,omitempty" doc:"TMDB image path"`
}

type AdminRequestRouteTitleSearchInput struct {
	MediaType string `query:"media_type" enum:"movie,series" required:"true"`
	Q         string `query:"q" minLength:"1" maxLength:"200" required:"true" doc:"Title to search TMDB for" example:"spirited away"`
}

type AdminRequestRouteTitleCollection struct {
	Collection[AdminRequestRouteTitle]
}

type AdminRequestRouteTitleCollectionOutput struct {
	Body AdminRequestRouteTitleCollection
}

// AdminRequestRoutePreviewTier is one tier's outcome.
type AdminRequestRoutePreviewTier struct {
	Quality         string         `json:"quality" enum:"1080p,2160p"`
	RouteID         string         `json:"route_id,omitempty"`
	RouteName       string         `json:"route_name,omitempty"`
	IntegrationID   string         `json:"integration_id,omitempty"`
	IntegrationName string         `json:"integration_name,omitempty"`
	Overrides       map[string]any `json:"overrides,omitempty"`
	Note            string         `json:"note,omitempty" doc:"Why no route sends the tier, or why it would fail"`
}

type AdminRequestRoutePreviewOutput struct {
	Body struct {
		Facts AdminRequestRouteFacts         `json:"facts"`
		Tiers []AdminRequestRoutePreviewTier `json:"tiers"`
		Rules []AdminRequestRoutePreviewRule `json:"rules" doc:"Every route of the media type in evaluation order, with what it did"`
	}
}

func registerAdminRequestRoutes(reg *Registry) {
	op := func(method, path, id, summary string, guard bool) Operation {
		o := Operation{Operation: humaOp(method, Prefix+path, id, "admin", summary), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true, Guarded: guard}
		o.Errors = []int{http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity}
		if method != http.MethodGet {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	Register(reg, op(http.MethodGet, "/admin/request-routes", opListRequestRoutes, "List the request routing rules, in evaluation order per media type.", false), reg.listAdminRequestRoutes)
	Register(reg, op(http.MethodGet, "/admin/request-routes/{id}", opGetRequestRoute, "Get one request routing rule.", false), reg.getAdminRequestRoute)
	create := op(http.MethodPost, "/admin/request-routes", opCreateRequestRoute, "Add a request routing rule after the media type's existing rules.", false)
	create.DefaultStatus = http.StatusCreated
	Register(reg, create, reg.createAdminRequestRoute)
	update := op(http.MethodPut, "/admin/request-routes/{id}", opUpdateRequestRoute, "Replace a request routing rule; saving a media type's fallback creates it.", true)
	Register(reg, update, reg.updateAdminRequestRoute)
	del := op(http.MethodDelete, "/admin/request-routes/{id}", opDeleteRequestRoute, "Delete a request routing rule.", true)
	del.DefaultStatus = http.StatusNoContent
	Register(reg, del, reg.deleteAdminRequestRoute)
	Register(reg, op(http.MethodPost, "/admin/request-routes/order", opReorderRequestRoutes, "Set the evaluation order of a media type's routing rules.", false), reg.reorderAdminRequestRoutes)
	preview := op(http.MethodPost, "/admin/request-routes/preview", opPreviewRequestRoute, "Show which server each quality tier of a title would go to.", false)
	preview.RetrySafety = RetrySafetyNaturalIdempotent
	preview.DemoRestricted = false
	Register(reg, preview, reg.previewAdminRequestRoute)
	Register(reg, op(http.MethodGet, "/admin/request-routes/titles", opSearchRequestRouteTitles, "Search TMDB for titles to try the routing rules on; works while requests are turned off.", false), reg.searchAdminRequestRouteTitles)
	Register(reg, op(http.MethodGet, "/admin/request-routing", opGetRequestRouting, "Get the request routing mode and where Standard routing would send each media type.", false), reg.getAdminRequestRouting)
	Register(reg, op(http.MethodPut, "/admin/request-routing", opUpdateRequestRouting, "Switch request routing between Standard and Advanced; Standard is refused while a media type has more than one server of a kind.", true), reg.updateAdminRequestRouting)
}

func adminRequestRoutingOf(o mediarequests.RoutingOverview) AdminRequestRouting {
	out := AdminRequestRouting{Mode: string(o.Mode), Standard: []AdminRequestStandardDestination{}, StandardUnavailableReason: o.StandardBlocker}
	for _, d := range o.Standard {
		out.Standard = append(out.Standard, AdminRequestStandardDestination{MediaType: string(d.MediaType), HDIntegrationID: d.HDIntegrationID, UHDIntegrationID: d.UHDIntegrationID})
	}
	return out
}

func routingTag(ctx context.Context, o mediarequests.RoutingOverview) EntityTag {
	return adminRequestTag(ctx, "routing", "global", o.Revision)
}

func (reg *Registry) getAdminRequestRouting(ctx context.Context, _ *struct{}) (*AdminRequestRoutingOutput, error) {
	s, p := reg.adminRequestRouteService()
	if p != nil {
		return nil, p
	}
	o, err := s.GetRoutingOverview(ctx, adminRequestViewer(ctx))
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestRoutingOutput{ETag: routingTag(ctx, *o).String(), Body: adminRequestRoutingOf(*o)}, nil
}

func (reg *Registry) updateAdminRequestRouting(ctx context.Context, in *AdminRequestRoutingUpdateInput) (*AdminRequestRoutingOutput, error) {
	s, p := reg.adminRequestRouteService()
	if p != nil {
		return nil, p
	}
	v := adminRequestViewer(ctx)
	current, err := s.GetRoutingOverview(ctx, v)
	if err != nil {
		return nil, requestProblem(err)
	}
	rev, p := adminRequestGuard(AdminRequestPreconditions{in.IfMatch, in.IfNoneMatch}, routingTag(ctx, *current), current.Revision)
	if p != nil {
		return nil, p
	}
	o, err := s.UpdateRoutingModeConditional(ctx, v, mediarequests.RoutingMode(in.Body.Mode), rev)
	if errors.Is(err, mediarequests.ErrStaleRevision) {
		latest, e := s.GetRoutingOverview(ctx, v)
		if e != nil {
			return nil, requestProblem(e)
		}
		return nil, NewProblem(TypePreconditionFailed, "Request routing changed; reload before saving.").WithHeader("ETag", routingTag(ctx, *latest).String())
	}
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestRoutingOutput{ETag: routingTag(ctx, *o).String(), Body: adminRequestRoutingOf(*o)}, nil
}

func (reg *Registry) adminRequestRouteService() (adminRequestRoutes, *Problem) {
	s, ok := reg.deps.AdminRequests.(adminRequestRoutes)
	if !ok {
		return nil, unavailable("request routing")
	}
	return s, nil
}

func adminRequestRouteOf(r mediarequests.Route) AdminRequestRoute {
	c := r.Conditions
	return AdminRequestRoute{
		ID: r.ID, MediaType: string(r.MediaType), Position: r.Position, Name: r.Name, Enabled: r.Enabled,
		IsFallback: r.IsFallback, SkipUHD: r.SkipUHD,
		Conditions: AdminRequestRouteConditions(c),
		HD:         AdminRequestRouteDestination{IntegrationID: r.HD.IntegrationID, Overrides: r.HD.Overrides},
		UHD:        AdminRequestRouteDestination{IntegrationID: r.UHD.IntegrationID, Overrides: r.UHD.Overrides},
	}
}

func (b AdminRequestRouteBody) domain(id string) mediarequests.Route {
	c := b.Conditions
	return mediarequests.Route{
		ID: id, MediaType: mediarequests.MediaType(b.MediaType), Name: b.Name, Enabled: b.Enabled, SkipUHD: b.SkipUHD,
		Conditions: mediarequests.RouteConditions(c),
		HD:         mediarequests.RouteDestination{IntegrationID: b.HD.IntegrationID, Overrides: b.HD.Overrides},
		UHD:        mediarequests.RouteDestination{IntegrationID: b.UHD.IntegrationID, Overrides: b.UHD.Overrides},
	}
}

func routeTag(ctx context.Context, r mediarequests.Route) EntityTag {
	return adminRequestTag(ctx, "route", r.ID, r.Revision)
}

func (reg *Registry) listAdminRequestRoutes(ctx context.Context, _ *struct{}) (*AdminRequestRouteCollectionOutput, error) {
	s, p := reg.adminRequestRouteService()
	if p != nil {
		return nil, p
	}
	routes, err := s.ListRoutesAdmin(ctx, adminRequestViewer(ctx))
	if err != nil {
		return nil, requestProblem(err)
	}
	items := make([]AdminRequestRoute, 0, len(routes))
	for _, r := range routes {
		items = append(items, adminRequestRouteOf(r))
	}
	return &AdminRequestRouteCollectionOutput{Body: NewCollection(items)}, nil
}

func (reg *Registry) getAdminRequestRoute(ctx context.Context, in *AdminRequestRouteIDInput) (*AdminRequestRouteOutput, error) {
	s, p := reg.adminRequestRouteService()
	if p != nil {
		return nil, p
	}
	r, err := s.GetRoute(ctx, adminRequestViewer(ctx), in.ID)
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestRouteOutput{ETag: routeTag(ctx, *r).String(), Body: adminRequestRouteOf(*r)}, nil
}

func (reg *Registry) createAdminRequestRoute(ctx context.Context, in *AdminRequestRouteCreateInput) (*AdminRequestRouteCreateOutput, error) {
	s, p := reg.adminRequestRouteService()
	if p != nil {
		return nil, p
	}
	r, err := s.CreateRoute(ctx, adminRequestViewer(ctx), in.Body.domain(""))
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestRouteCreateOutput{Location: Prefix + "/admin/request-routes/" + r.ID, ETag: routeTag(ctx, *r).String(), Body: adminRequestRouteOf(*r)}, nil
}

// currentRouteRevision reads the route an editor is replacing. A fallback
// that has never been saved reads as revision zero.
func (reg *Registry) currentRouteRevision(ctx context.Context, s adminRequestRoutes, id string) (EntityTag, int64, *Problem) {
	current, err := s.GetRoute(ctx, adminRequestViewer(ctx), id)
	if err != nil {
		return EntityTag{}, 0, requestProblem(err)
	}
	return routeTag(ctx, *current), current.Revision, nil
}

func (reg *Registry) updateAdminRequestRoute(ctx context.Context, in *AdminRequestRouteUpdateInput) (*AdminRequestRouteOutput, error) {
	s, p := reg.adminRequestRouteService()
	if p != nil {
		return nil, p
	}
	tag, revision, p := reg.currentRouteRevision(ctx, s, in.ID)
	if p != nil {
		return nil, p
	}
	rev, p := adminRequestGuard(AdminRequestPreconditions{in.IfMatch, in.IfNoneMatch}, tag, revision)
	if p != nil {
		return nil, p
	}
	r, err := s.UpdateRouteConditional(ctx, adminRequestViewer(ctx), in.Body.domain(in.ID), rev)
	if errors.Is(err, mediarequests.ErrStaleRevision) {
		current, _, p := reg.currentRouteRevision(ctx, s, in.ID)
		if p != nil {
			return nil, p
		}
		return nil, NewProblem(TypePreconditionFailed, "The routing rule changed; reload before saving.").WithHeader("ETag", current.String())
	}
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestRouteOutput{ETag: routeTag(ctx, *r).String(), Body: adminRequestRouteOf(*r)}, nil
}

func (reg *Registry) deleteAdminRequestRoute(ctx context.Context, in *AdminRequestRouteDeleteInput) (*struct{}, error) {
	s, p := reg.adminRequestRouteService()
	if p != nil {
		return nil, p
	}
	current, err := s.GetRoute(ctx, adminRequestViewer(ctx), in.ID)
	if err != nil {
		return nil, requestProblem(err)
	}
	rev, p := adminRequestGuard(AdminRequestPreconditions{in.IfMatch, in.IfNoneMatch}, routeTag(ctx, *current), current.Revision)
	if p != nil {
		return nil, p
	}
	err = s.DeleteRouteConditional(ctx, adminRequestViewer(ctx), in.ID, rev)
	if errors.Is(err, mediarequests.ErrStaleRevision) {
		return nil, NewProblem(TypePreconditionFailed, "The routing rule changed; reload before deleting.")
	}
	if err != nil {
		return nil, requestProblem(err)
	}
	return nil, nil
}

func (reg *Registry) reorderAdminRequestRoutes(ctx context.Context, in *AdminRequestRouteReorderInput) (*AdminRequestRouteCollectionOutput, error) {
	s, p := reg.adminRequestRouteService()
	if p != nil {
		return nil, p
	}
	routes, err := s.ReorderRoutes(ctx, adminRequestViewer(ctx), mediarequests.MediaType(in.Body.MediaType), in.Body.IDs)
	if err != nil {
		return nil, requestProblem(err)
	}
	items := make([]AdminRequestRoute, 0, len(routes))
	for _, r := range routes {
		items = append(items, adminRequestRouteOf(r))
	}
	return &AdminRequestRouteCollectionOutput{Body: NewCollection(items)}, nil
}

func (reg *Registry) previewAdminRequestRoute(ctx context.Context, in *AdminRequestRoutePreviewInput) (*AdminRequestRoutePreviewOutput, error) {
	s, p := reg.adminRequestRouteService()
	if p != nil {
		return nil, p
	}
	requester := 0
	if in.Body.RequesterUserID != nil {
		id, err := strconv.Atoi(string(*in.Body.RequesterUserID))
		if err != nil || id <= 0 {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: "body.requester_user_id", Code: codeInvalid, Detail: "expected an account ID"})
		}
		requester = id
	}
	preview, err := s.PreviewRoute(ctx, adminRequestViewer(ctx), mediarequests.MediaType(in.Body.MediaType), in.Body.TMDBID, requester)
	if err != nil {
		return nil, requestProblem(err)
	}
	out := new(AdminRequestRoutePreviewOutput)
	f := preview.Facts
	out.Body.Facts = AdminRequestRouteFacts{
		GenreIDs: NonNil(f.GenreIDs), KeywordIDs: NonNil(f.KeywordIDs), OriginalLanguage: f.OriginalLanguage,
		OriginCountries: NonNil(f.OriginCountries), Year: f.Year, NetworkIDs: NonNil(f.NetworkIDs),
		CompanyIDs: NonNil(f.CompanyIDs), Anime: f.Anime,
	}
	if f.ContentRating != nil {
		out.Body.Facts.ContentRating = *f.ContentRating
	}
	out.Body.Rules = make([]AdminRequestRoutePreviewRule, 0, len(preview.Rules))
	for _, rule := range preview.Rules {
		out.Body.Rules = append(out.Body.Rules, AdminRequestRoutePreviewRule{
			RouteID: rule.Route.ID, RouteName: rule.Route.Name, IsFallback: rule.Route.IsFallback, Enabled: rule.Route.Enabled,
			Unmet: NonNil(rule.Unmet), HD: string(rule.Steps[mediarequests.Quality1080p]), UHD: string(rule.Steps[mediarequests.Quality2160p]),
		})
	}
	out.Body.Tiers = make([]AdminRequestRoutePreviewTier, 0, len(preview.Tiers))
	for _, t := range preview.Tiers {
		out.Body.Tiers = append(out.Body.Tiers, AdminRequestRoutePreviewTier{
			Quality: string(t.Quality), RouteID: t.RouteID, RouteName: t.RouteName, IntegrationID: t.IntegrationID,
			IntegrationName: t.IntegrationName, Overrides: t.Overrides, Note: t.Reason,
		})
	}
	return out, nil
}

func (reg *Registry) searchAdminRequestRouteTitles(ctx context.Context, in *AdminRequestRouteTitleSearchInput) (*AdminRequestRouteTitleCollectionOutput, error) {
	s, p := reg.adminRequestRouteService()
	if p != nil {
		return nil, p
	}
	results, err := s.SearchRouteTitles(ctx, adminRequestViewer(ctx), mediarequests.MediaType(in.MediaType), in.Q)
	if err != nil {
		return nil, requestProblem(err)
	}
	items := make([]AdminRequestRouteTitle, 0, len(results))
	for _, r := range results {
		items = append(items, AdminRequestRouteTitle{TMDBID: r.ID, MediaType: in.MediaType, Title: r.Title, Year: r.Year, PosterPath: r.PosterPath})
	}
	return &AdminRequestRouteTitleCollectionOutput{Body: AdminRequestRouteTitleCollection{Collection: Paginated(items, "")}}, nil
}

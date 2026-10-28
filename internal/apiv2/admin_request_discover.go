package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/requests/discoverrows"
)

// Requests Discover layout administration: the rows the Discover page shows,
// in order (docs/architecture/request-discover-rows.md).

const (
	opGetRequestDiscoverLayout    = "getRequestDiscoverLayout"
	opUpdateRequestDiscoverLayout = "updateRequestDiscoverLayout"
	opResetRequestDiscoverLayout  = "resetRequestDiscoverLayout"
	opPreviewRequestDiscoverRow   = "previewRequestDiscoverRow"
	opListRequestWatchProviders   = "listRequestWatchProviders"
)

// adminRequestDiscover is the Discover layout slice of the request service.
type adminRequestDiscover interface {
	GetDiscoverLayout(context.Context, mediarequests.Viewer) (*mediarequests.DiscoverLayoutAdmin, error)
	ReplaceDiscoverLayoutConditional(context.Context, mediarequests.Viewer, []discoverrows.Row, int64) (*mediarequests.DiscoverLayoutAdmin, error)
	ResetDiscoverLayoutConditional(context.Context, mediarequests.Viewer, int64) error
	PreviewDiscoverRow(context.Context, mediarequests.Viewer, discoverrows.Row) (*mediarequests.MediaPage, error)
	ListWatchProviders(context.Context, mediarequests.Viewer, mediarequests.MediaType, string) ([]tmdb.WatchProvider, error)
}

// AdminDiscoverChart names a TMDB chart.
type AdminDiscoverChart struct {
	Preset     string `json:"preset" enum:"trending,popular,top_rated,now_playing,upcoming,airing_today,on_the_air" doc:"now_playing and upcoming are movie charts; airing_today and on_the_air are series charts" example:"trending"`
	MediaType  string `json:"media_type" enum:"movie,series,all" doc:"all mixes movies and series, trending only" example:"movie"`
	TimeWindow string `json:"time_window,omitempty" enum:"day,week" doc:"Trending only" example:"week"`
}

// AdminDiscoverFilter is a TMDB /discover query. Genres must all match; the
// other lists match any of their values.
type AdminDiscoverFilter struct {
	MediaType        string   `json:"media_type" enum:"movie,series" example:"movie"`
	SortBy           string   `json:"sort_by" doc:"A TMDB discover sort, such as popularity.desc, vote_average.desc, primary_release_date.desc (movies) or first_air_date.desc (series). A vote_average sort without vote_count_gte counts titles with at least 100 votes" example:"popularity.desc"`
	GenreIDs         []int    `json:"genre_ids,omitempty" maxItems:"20" doc:"TMDB genre IDs; a title must have all of them"`
	ExcludeGenreIDs  []int    `json:"exclude_genre_ids,omitempty" maxItems:"20" doc:"TMDB genre IDs; a title must have none of them"`
	KeywordIDs       []int    `json:"keyword_ids,omitempty" maxItems:"20" doc:"TMDB keyword IDs"`
	CompanyIDs       []int    `json:"company_ids,omitempty" maxItems:"20" doc:"TMDB production company IDs"`
	NetworkIDs       []int    `json:"network_ids,omitempty" maxItems:"20" doc:"TMDB network IDs (series)"`
	OriginalLanguage string   `json:"original_language,omitempty" doc:"ISO 639-1 code" example:"ja"`
	OriginCountries  []string `json:"origin_countries,omitempty" maxItems:"20" doc:"ISO 3166-1 alpha-2 codes" example:"[\"JP\"]"`
	YearFrom         int      `json:"year_from,omitempty" doc:"First release (or first-air) year, inclusive" example:"1980"`
	YearTo           int      `json:"year_to,omitempty" doc:"Last release (or first-air) year, inclusive" example:"1989"`
	VoteAverageGte   float64  `json:"vote_average_gte,omitempty" minimum:"0" maximum:"10" doc:"Lowest TMDB score"`
	VoteCountGte     int      `json:"vote_count_gte,omitempty" minimum:"0" doc:"Fewest TMDB votes"`
	RuntimeGte       int      `json:"runtime_gte,omitempty" minimum:"0" doc:"Shortest runtime, minutes"`
	RuntimeLte       int      `json:"runtime_lte,omitempty" minimum:"0" doc:"Longest runtime, minutes"`
	CertificationLte string   `json:"certification_lte,omitempty" doc:"Highest US rating: G, PG, PG-13, R or NC-17 for movies; TV-Y, TV-Y7, TV-G, TV-PG, TV-14 or TV-MA for series. A viewer's own rating limit still applies" example:"PG"`
	WatchProviderIDs []int    `json:"watch_provider_ids,omitempty" maxItems:"20" doc:"Streaming services from listRequestWatchProviders"`
	WatchRegion      string   `json:"watch_region,omitempty" doc:"ISO 3166-1 alpha-2 code of where the watch providers stream; required with them" example:"US"`
}

// AdminDiscoverList is a public list by URL.
type AdminDiscoverList struct {
	URL string `json:"url" maxLength:"2048" format:"uri-reference" doc:"A themoviedb.org list page (or its number) for tmdb_list; an mdblist.com list page for mdblist. Stored canonical" example:"https://mdblist.com/lists/garycrawfordgc/top-watched-movies-of-the-week"`
}

// AdminDiscoverRowBody is one row of the Discover layout as an admin edits
// it. Exactly the spec that matches type is set: chart, discover, or list
// (tmdb_list and mdblist); a brand row sets none.
type AdminDiscoverRowBody struct {
	ID            string               `json:"id,omitempty" maxLength:"40" doc:"The row's ID; empty on a new row, which gets one. The six built-in rows keep their IDs and charts; a brand row's ID is its type" example:"trending_movies"`
	Type          string               `json:"type" enum:"tmdb_chart,tmdb_discover,tmdb_list,mdblist,studios,networks,genres" example:"tmdb_chart"`
	Title         string               `json:"title" maxLength:"80" example:"Trending Movies"`
	Enabled       bool                 `json:"enabled"`
	Limit         int                  `json:"limit,omitempty" minimum:"0" maximum:"100" doc:"How many titles the row shows on the page; its see all page keeps paging. 0 means 20 on a title row; always 0 on a brand row" example:"20"`
	HideAvailable bool                 `json:"hide_available,omitempty" doc:"Leave out titles the library already has"`
	Chart         *AdminDiscoverChart  `json:"chart,omitempty"`
	Discover      *AdminDiscoverFilter `json:"discover,omitempty"`
	List          *AdminDiscoverList   `json:"list,omitempty"`
}

// AdminDiscoverSource is how a list row's stored copy stands.
type AdminDiscoverSource struct {
	State       string   `json:"state" enum:"pending,ready,empty,error" doc:"pending: not fetched yet; ready: current as of refreshed_at; empty: the list holds no movies or series; error: the last fetch failed, and any earlier copy is still shown"`
	Entries     int      `json:"entries" doc:"Titles in the stored copy"`
	RefreshedAt *Instant `json:"refreshed_at,omitempty" doc:"When the stored copy was fetched"`
	Error       string   `json:"error,omitempty" doc:"Why the last fetch failed"`
}

// AdminDiscoverRow is one row of the stored layout.
type AdminDiscoverRow struct {
	AdminDiscoverRowBody
	Source *AdminDiscoverSource `json:"source,omitempty" doc:"List rows only: their stored copy, which the server refreshes about every 12 hours"`
}

// AdminDiscoverLayout is the Discover page's layout.
type AdminDiscoverLayout struct {
	Customized bool               `json:"customized" doc:"False while the server shows its default layout, which follows server updates"`
	Rows       []AdminDiscoverRow `json:"rows" doc:"In page order. Empty, never null"`
	UpdatedAt  *Instant           `json:"updated_at,omitempty"`
	MaxRows    int                `json:"max_rows" doc:"The most rows a layout holds" example:"30"`
}

type AdminDiscoverLayoutOutput struct {
	ETag string `header:"ETag"`
	Body AdminDiscoverLayout
}

type AdminDiscoverLayoutUpdateInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        struct {
		Rows []AdminDiscoverRowBody `json:"rows" maxItems:"30" doc:"The whole layout, in page order"`
	}
}

type AdminDiscoverLayoutResetInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}

type AdminDiscoverRowPreviewInput struct {
	Body AdminDiscoverRowBody
}

// AdminWatchProvider is a streaming service a discover row can filter on.
type AdminWatchProvider struct {
	ID       int    `json:"id" doc:"TMDB watch provider ID" example:"8"`
	Name     string `json:"name" example:"Netflix"`
	LogoPath string `json:"logo_path,omitempty" doc:"TMDB image path" example:"/pbpMk2JmcoNnQwx5JGpXngfoWtp.jpg"`
}

type AdminWatchProviderCollection struct {
	Collection[AdminWatchProvider]
}

type AdminWatchProviderCollectionOutput struct {
	Body AdminWatchProviderCollection
}

type AdminWatchProviderInput struct {
	MediaType string `query:"media_type" enum:"movie,series" default:"movie" example:"movie"`
	Region    string `query:"region" pattern:"^[A-Za-z]{2}$" default:"US" doc:"ISO 3166-1 alpha-2 code" example:"US"`
}

func registerAdminRequestDiscover(reg *Registry) {
	op := func(method, path, id, summary string, guard bool) Operation {
		o := Operation{Operation: humaOp(method, Prefix+path, id, "admin", summary), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true, Guarded: guard}
		o.Errors = []int{http.StatusUnprocessableEntity}
		if method != http.MethodGet {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	Register(reg, op(http.MethodGet, "/admin/request-discover/layout", opGetRequestDiscoverLayout, "Get the Requests Discover page's layout; works while requests are turned off.", false), reg.getAdminDiscoverLayout)
	Register(reg, op(http.MethodPut, "/admin/request-discover/layout", opUpdateRequestDiscoverLayout, "Replace the Requests Discover page's layout.", true), reg.updateAdminDiscoverLayout)
	reset := op(http.MethodDelete, "/admin/request-discover/layout", opResetRequestDiscoverLayout, "Go back to the default Discover layout.", true)
	reset.DefaultStatus = http.StatusNoContent
	Register(reg, reset, reg.resetAdminDiscoverLayout)
	preview := op(http.MethodPost, "/admin/request-discover/preview", opPreviewRequestDiscoverRow, "Show the first page of a Discover row before saving it; a list row's list is fetched live.", false)
	preview.RetrySafety = RetrySafetyNaturalIdempotent
	preview.DemoRestricted = false
	Register(reg, preview, reg.previewAdminDiscoverRow)
	Register(reg, op(http.MethodGet, "/admin/request-discover/watch-providers", opListRequestWatchProviders, "List the streaming services a Discover row can filter on in a region.", false), reg.listAdminWatchProviders)
}

func (reg *Registry) adminRequestDiscoverService() (adminRequestDiscover, *Problem) {
	s, ok := reg.deps.AdminRequests.(adminRequestDiscover)
	if !ok {
		return nil, unavailable("request discover layout")
	}
	return s, nil
}

func discoverLayoutTag(ctx context.Context, l *mediarequests.DiscoverLayoutAdmin) EntityTag {
	return adminRequestTag(ctx, "discover-layout", "global", l.Revision)
}

func adminDiscoverLayoutOutput(ctx context.Context, l *mediarequests.DiscoverLayoutAdmin) *AdminDiscoverLayoutOutput {
	out := AdminDiscoverLayout{Customized: l.Customized, Rows: make([]AdminDiscoverRow, 0, len(l.Rows)), UpdatedAt: adminSyncInstant(l.UpdatedAt), MaxRows: discoverrows.MaxRows}
	for _, row := range l.Rows {
		wire := AdminDiscoverRow{AdminDiscoverRowBody: adminDiscoverRowOf(row)}
		if status, ok := l.Sources[row.ID]; ok {
			wire.Source = &AdminDiscoverSource{State: status.State, Entries: status.Entries, RefreshedAt: adminSyncInstant(status.RefreshedAt), Error: status.Error}
		}
		out.Rows = append(out.Rows, wire)
	}
	return &AdminDiscoverLayoutOutput{ETag: discoverLayoutTag(ctx, l).String(), Body: out}
}

// The wire specs mirror discoverrows' field for field, so they convert
// directly; a field added to one side and not the other stops compiling.
func adminDiscoverRowOf(r discoverrows.Row) AdminDiscoverRowBody {
	out := AdminDiscoverRowBody{ID: r.ID, Type: string(r.Type), Title: r.Title, Enabled: r.Enabled, Limit: r.Limit, HideAvailable: r.HideAvailable}
	if r.Chart != nil {
		out.Chart = new(AdminDiscoverChart(*r.Chart))
	}
	if r.Discover != nil {
		out.Discover = new(AdminDiscoverFilter(*r.Discover))
	}
	if r.List != nil {
		out.List = new(AdminDiscoverList(*r.List))
	}
	return out
}

func (b AdminDiscoverRowBody) domain() discoverrows.Row {
	out := discoverrows.Row{ID: b.ID, Type: discoverrows.Type(b.Type), Title: b.Title, Enabled: b.Enabled, Limit: b.Limit, HideAvailable: b.HideAvailable}
	if b.Chart != nil {
		out.Chart = new(discoverrows.ChartSpec(*b.Chart))
	}
	if b.Discover != nil {
		out.Discover = new(discoverrows.DiscoverSpec(*b.Discover))
	}
	if b.List != nil {
		out.List = new(discoverrows.ListSpec(*b.List))
	}
	return out
}

func (reg *Registry) getAdminDiscoverLayout(ctx context.Context, _ *struct{}) (*AdminDiscoverLayoutOutput, error) {
	s, p := reg.adminRequestDiscoverService()
	if p != nil {
		return nil, p
	}
	l, err := s.GetDiscoverLayout(ctx, adminRequestViewer(ctx))
	if err != nil {
		return nil, requestProblem(err)
	}
	return adminDiscoverLayoutOutput(ctx, l), nil
}

// guardDiscoverLayout checks the editor's preconditions against the stored
// layout and returns the revision the write must still match.
func (reg *Registry) guardDiscoverLayout(ctx context.Context, s adminRequestDiscover, v mediarequests.Viewer, headers AdminRequestPreconditions) (int64, *Problem) {
	current, err := s.GetDiscoverLayout(ctx, v)
	if err != nil {
		return 0, requestProblem(err)
	}
	return adminRequestGuard(headers, discoverLayoutTag(ctx, current), current.Revision)
}

// staleDiscoverLayout is the 412 for a write over a layout that moved on.
func (reg *Registry) staleDiscoverLayout(ctx context.Context, s adminRequestDiscover, v mediarequests.Viewer) error {
	latest, err := s.GetDiscoverLayout(ctx, v)
	if err != nil {
		return requestProblem(err)
	}
	return NewProblem(TypePreconditionFailed, "The Discover layout changed; reload before saving.").WithHeader("ETag", discoverLayoutTag(ctx, latest).String())
}

func (reg *Registry) updateAdminDiscoverLayout(ctx context.Context, in *AdminDiscoverLayoutUpdateInput) (*AdminDiscoverLayoutOutput, error) {
	s, p := reg.adminRequestDiscoverService()
	if p != nil {
		return nil, p
	}
	v := adminRequestViewer(ctx)
	rev, p := reg.guardDiscoverLayout(ctx, s, v, AdminRequestPreconditions{in.IfMatch, in.IfNoneMatch})
	if p != nil {
		return nil, p
	}
	rows := make([]discoverrows.Row, 0, len(in.Body.Rows))
	for _, r := range in.Body.Rows {
		rows = append(rows, r.domain())
	}
	l, err := s.ReplaceDiscoverLayoutConditional(ctx, v, rows, rev)
	if errors.Is(err, mediarequests.ErrStaleRevision) {
		return nil, reg.staleDiscoverLayout(ctx, s, v)
	}
	if err != nil {
		return nil, requestProblem(err)
	}
	return adminDiscoverLayoutOutput(ctx, l), nil
}

func (reg *Registry) resetAdminDiscoverLayout(ctx context.Context, in *AdminDiscoverLayoutResetInput) (*struct{}, error) {
	s, p := reg.adminRequestDiscoverService()
	if p != nil {
		return nil, p
	}
	v := adminRequestViewer(ctx)
	rev, p := reg.guardDiscoverLayout(ctx, s, v, AdminRequestPreconditions{in.IfMatch, in.IfNoneMatch})
	if p != nil {
		return nil, p
	}
	err := s.ResetDiscoverLayoutConditional(ctx, v, rev)
	if errors.Is(err, mediarequests.ErrStaleRevision) {
		return nil, reg.staleDiscoverLayout(ctx, s, v)
	}
	if err != nil {
		return nil, requestProblem(err)
	}
	return nil, nil
}

func (reg *Registry) previewAdminDiscoverRow(ctx context.Context, in *AdminDiscoverRowPreviewInput) (*RequestMediaPageOutput, error) {
	s, p := reg.adminRequestDiscoverService()
	if p != nil {
		return nil, p
	}
	page, err := s.PreviewDiscoverRow(ctx, adminRequestViewer(ctx), in.Body.domain())
	if err != nil {
		return nil, requestProblem(err)
	}
	return &RequestMediaPageOutput{Body: requestMediaPageOf(page)}, nil
}

func (reg *Registry) listAdminWatchProviders(ctx context.Context, in *AdminWatchProviderInput) (*AdminWatchProviderCollectionOutput, error) {
	s, p := reg.adminRequestDiscoverService()
	if p != nil {
		return nil, p
	}
	providers, err := s.ListWatchProviders(ctx, adminRequestViewer(ctx), mediarequests.MediaType(in.MediaType), in.Region)
	if err != nil {
		return nil, requestProblem(err)
	}
	items := make([]AdminWatchProvider, 0, len(providers))
	for _, wp := range providers {
		items = append(items, AdminWatchProvider{ID: wp.ID, Name: wp.Name, LogoPath: wp.LogoPath})
	}
	return &AdminWatchProviderCollectionOutput{Body: AdminWatchProviderCollection{Collection: NewCollection(items)}}, nil
}

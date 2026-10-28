package apiv2

import (
	"context"
)

// The Discover page's rows as the admin laid them out: the row order a
// client draws (listDiscoverSections' row_order) and a page of any title row
// (getDiscoverRow). See docs/architecture/request-discover-rows.md.

// DiscoverRowRef places one row of the Discover page.
type DiscoverRowRef struct {
	Key   string `json:"key" doc:"The row's stable ID: an items key for a title row; studios, networks or genres for a brand row" example:"trending_movies"`
	Type  string `json:"type" doc:"What the row shows: tmdb_chart, tmdb_discover, tmdb_list or mdblist (title rows, in items), or studios, networks or genres (brand rows). More values may be added" example:"tmdb_chart"`
	Title string `json:"title" example:"Trending Movies"`
}

// DiscoverRowInput names one row of the Discover layout.
type DiscoverRowInput struct {
	RowID string `path:"row_id" pattern:"^[a-z0-9_]{1,40}$" doc:"The row's key from listDiscoverSections" example:"trending_movies"`
	ProviderPage
}

// getDiscoverSection is v1 GET /requests/discover/{section}, read through the
// admin's layout: a built-in row the admin turned off is not found.
func (reg *Registry) getDiscoverSection(ctx context.Context, in *DiscoverSectionInput) (*DiscoverSectionOutput, error) {
	return reg.discoverRow(ctx, in.Section, in.Page)
}

// getDiscoverRow serves any title row of the layout.
func (reg *Registry) getDiscoverRow(ctx context.Context, in *DiscoverRowInput) (*DiscoverSectionOutput, error) {
	return reg.discoverRow(ctx, in.RowID, in.Page)
}

func (reg *Registry) discoverRow(ctx context.Context, rowID string, page int) (*DiscoverSectionOutput, error) {
	svc, viewer, p := reg.requestViewer(ctx)
	if p != nil {
		return nil, p
	}
	section, err := svc.DiscoverRow(ctx, viewer, rowID, page)
	if err != nil {
		return nil, requestProblem(err)
	}
	out := discoverSectionOf(section)
	reg.markInWatchlist(ctx, watchlistMarksOf(out.Results))
	return &DiscoverSectionOutput{Body: out}, nil
}

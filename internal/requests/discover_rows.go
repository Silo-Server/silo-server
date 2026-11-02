package requests

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/requests/discoverrows"
)

// DiscoverSnapshots reads list rows' stored copies and how they stand.
// *discoverrows.SnapshotRepository implements it.
type DiscoverSnapshots interface {
	discoverrows.SnapshotReader
	Statuses(ctx context.Context, keys []string) (map[string]discoverrows.SourceStatus, error)
}

// DiscoverListRefresher fetches list rows' lists. *discoverrows.Refresher
// implements it.
type DiscoverListRefresher interface {
	RefreshRows(ctx context.Context, rows []discoverrows.Row)
	FetchCards(ctx context.Context, row discoverrows.Row, limit int) ([]discoverrows.Card, error)
}

// SetDiscoverLists wires where list rows read their stored titles and what
// fetches them when an admin saves the layout. Without it list rows are
// empty.
func (s *Service) SetDiscoverLists(snapshots DiscoverSnapshots, refresher DiscoverListRefresher) {
	s.discoverSnapshots, s.discoverRefresher = snapshots, refresher
}

func (s *Service) discoverEnv() discoverrows.Env {
	return discoverrows.Env{TMDB: s.tmdb, Snapshots: s.discoverSnapshots}
}

// DiscoverRowRef places one enabled row of the layout. Brand rows (studios,
// networks, genres) appear only here: their cards come from the brand list
// operations.
type DiscoverRowRef struct {
	Key   string
	Type  discoverrows.Type
	Title string
}

// DiscoverLayoutPage is the Discover page for one viewer: every enabled row
// in order, and the first page of each title row that has titles.
type DiscoverLayoutPage struct {
	Order    []DiscoverRowRef
	Sections []DiscoverySection
}

// layoutRows returns the rows the page shows, in order: the admin's layout,
// or the default one.
func (s *Service) layoutRows(ctx context.Context) ([]discoverrows.Row, error) {
	store, ok := s.store.(DiscoverLayoutStore)
	if !ok {
		return discoverrows.DefaultRows(), nil
	}
	layout, err := store.GetDiscoverLayout(ctx)
	if err != nil {
		return nil, err
	}
	return layout.Rows, nil
}

// DiscoverRows renders the Discover page for a viewer. A title row that fails
// is left out and logged, so one broken list or filter does not take the
// page down; the call fails only when every title row did.
func (s *Service) DiscoverRows(ctx context.Context, viewer Viewer) (*DiscoverLayoutPage, error) {
	if s == nil || s.store == nil || s.tmdb == nil {
		return nil, errRequestServiceNotConfigured
	}
	ctx = withPolicyCache(ctx)
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return nil, err
	}
	ceiling, err := s.viewerContentCeiling(ctx, viewer)
	if err != nil {
		return nil, err
	}
	rows, err := s.layoutRows(ctx)
	if err != nil {
		return nil, err
	}

	out := &DiscoverLayoutPage{}
	type titleRow struct {
		row    discoverrows.Row
		source discoverrows.Source
	}
	var titleRows []titleRow
	for _, row := range rows {
		kind, ok := discoverrows.KindOf(row.Type)
		if !ok || !row.Enabled {
			continue
		}
		out.Order = append(out.Order, DiscoverRowRef{Key: row.ID, Type: row.Type, Title: row.Title})
		if source := kind.Source(s.discoverEnv(), row); source != nil {
			titleRows = append(titleRows, titleRow{row: row, source: source})
		}
	}

	sections := make([]*DiscoverySection, len(titleRows))
	errs := make([]error, len(titleRows))
	group := new(errgroup.Group)
	group.SetLimit(externalIDHydrationConcurrency)
	for i, tr := range titleRows {
		group.Go(func() error {
			// A row's failure is kept, not returned, so the others finish.
			sections[i], errs[i] = s.firstRowPage(ctx, viewer, tr.row, tr.source, ceiling)
			return nil
		})
	}
	_ = group.Wait()

	failed := 0
	for i, section := range sections {
		if errs[i] != nil {
			failed++
			slog.WarnContext(ctx, "requests: discover row failed", "component", "requests", "row", titleRows[i].row.ID, "type", titleRows[i].row.Type, "error", errs[i])
			continue
		}
		// Shipped clients cannot draw a row without titles.
		if len(section.Results) > 0 {
			out.Sections = append(out.Sections, *section)
		}
	}
	if failed > 0 && failed == len(titleRows) {
		return nil, errs[0]
	}
	return out, nil
}

// firstRowPage renders a title row for the Discover page: at most its limit
// of titles. A trimmed row loses its resume cursor; its see all page starts
// again at page 1.
func (s *Service) firstRowPage(ctx context.Context, viewer Viewer, row discoverrows.Row, source discoverrows.Source, ceiling string) (*DiscoverySection, error) {
	budget := (row.Limit + sectionResultsPerPage - 1) / sectionResultsPerPage
	if ceiling != "" || row.HideAvailable {
		budget++
	}
	page, nextPage, err := s.fillRowPage(ctx, viewer, rowPageRequest{
		source: source, page: 1, want: row.Limit, budget: budget,
		ceiling: ceiling, hideAvailable: row.HideAvailable, endCursor: true,
	})
	if err != nil {
		return nil, err
	}
	if len(page.Results) > row.Limit {
		page.Results, nextPage = page.Results[:row.Limit], 0
	}
	return discoverySectionOf(row, page, nextPage), nil
}

// DiscoverRow returns one page of an enabled title row of the layout, for
// its see all page. page follows the source's page numbers; resume at the
// returned NextPage when set.
func (s *Service) DiscoverRow(ctx context.Context, viewer Viewer, rowID string, page int) (*DiscoverySection, error) {
	if s == nil || s.store == nil || s.tmdb == nil {
		return nil, errRequestServiceNotConfigured
	}
	ctx = withPolicyCache(ctx)
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return nil, err
	}
	rows, err := s.layoutRows(ctx)
	if err != nil {
		return nil, err
	}
	rowID = strings.TrimSpace(rowID)
	i := slices.IndexFunc(rows, func(r discoverrows.Row) bool { return r.ID == rowID && r.Enabled })
	if i < 0 {
		return nil, ErrNotFound
	}
	row := rows[i]
	kind, ok := discoverrows.KindOf(row.Type)
	if !ok {
		return nil, ErrNotFound
	}
	source := kind.Source(s.discoverEnv(), row)
	if source == nil {
		return nil, ErrNotFound
	}
	ceiling, err := s.viewerContentCeiling(ctx, viewer)
	if err != nil {
		return nil, err
	}
	result, nextPage, err := s.fillRowPage(ctx, viewer, rowPageRequest{
		source: source, page: page, want: sectionResultsPerPage, budget: sectionBackfillBudgetSingle,
		ceiling: ceiling, hideAvailable: row.HideAvailable, endCursor: true,
	})
	if err != nil {
		return nil, err
	}
	return discoverySectionOf(row, result, nextPage), nil
}

// Discover is the frozen /api/v1 discovery row: one of the six built-in rows
// as the default layout has them, whatever the admin's layout says.
func (s *Service) Discover(ctx context.Context, viewer Viewer, section string, page int) (*DiscoverySection, error) {
	if s == nil || s.store == nil || s.tmdb == nil {
		return nil, errRequestServiceNotConfigured
	}
	ctx = withPolicyCache(ctx)
	ceiling, err := s.viewerContentCeiling(ctx, viewer)
	if err != nil {
		return nil, err
	}
	return s.discoverLegacy(ctx, viewer, section, page, sectionBackfillBudgetSingle, ceiling)
}

// DiscoverAll is the frozen /api/v1 discovery page: the six built-in rows as
// the default layout has them.
func (s *Service) DiscoverAll(ctx context.Context, viewer Viewer) ([]DiscoverySection, error) {
	if s == nil || s.store == nil || s.tmdb == nil {
		return nil, errRequestServiceNotConfigured
	}
	ctx = withPolicyCache(ctx)
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return nil, err
	}
	ceiling, err := s.viewerContentCeiling(ctx, viewer)
	if err != nil {
		return nil, err
	}
	keys := discoverrows.LegacyIDs()
	sections := make([]DiscoverySection, len(keys))
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(externalIDHydrationConcurrency)
	for i, key := range keys {
		group.Go(func() error {
			section, err := s.discoverLegacy(gctx, viewer, key, 1, sectionBackfillBudgetAggregate, ceiling)
			if err != nil {
				return err
			}
			sections[i] = *section
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return sections, nil
}

// discoverLegacy renders one built-in row for an already-resolved ceiling,
// as /api/v1 always has: every survivor of the consumed pages, untrimmed.
func (s *Service) discoverLegacy(ctx context.Context, viewer Viewer, section string, page, budget int, ceiling string) (*DiscoverySection, error) {
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return nil, err
	}
	row, ok := discoverrows.LegacyRow(strings.TrimSpace(section))
	if !ok {
		return nil, fmt.Errorf("%w: invalid discovery section", ErrInvalidInput)
	}
	kind, _ := discoverrows.KindOf(row.Type)
	result, nextPage, err := s.fillRowPage(ctx, viewer, rowPageRequest{
		source: kind.Source(s.discoverEnv(), row), page: page, want: sectionResultsPerPage, budget: budget, ceiling: ceiling,
	})
	if err != nil {
		return nil, err
	}
	return discoverySectionOf(row, result, nextPage), nil
}

func discoverySectionOf(row discoverrows.Row, page *MediaPage, nextPage int) *DiscoverySection {
	return &DiscoverySection{
		Key: row.ID, Title: row.Title, Page: page.Page, TotalPages: page.TotalPages,
		TotalResults: page.TotalResults, Results: page.Results, NextPage: nextPage,
	}
}

// rowPageRequest asks fillRowPage for one page of a row.
type rowPageRequest struct {
	source discoverrows.Source
	// page is the source page to start at; want is how many titles to fill;
	// budget bounds the source pages read.
	page, want, budget int
	// ceiling is the viewer's rating ceiling; "" means unrestricted.
	ceiling       string
	hideAvailable bool
	// endCursor keeps a cursor past the last page when a read of several
	// pages reached the end, so a client that never saw one does not fall
	// back to page+1 and read those pages again. /api/v1 answers without it.
	endCursor bool
}

// fillRowPage reads a row's source from req.page on, laying the viewer's
// availability and request state over each source page, until req.want
// titles survive the viewer's rating ceiling (and, with hideAvailable, the
// library check) or req.budget source pages are spent. It returns every
// survivor of the consumed pages and the page to resume at: 0 for an
// unfiltered row that read one page, where page+1 applies, and 0 once the
// source is exhausted (past the last page with req.endCursor). Totals stay
// the source's unfiltered counts; see the Row backfill comment.
func (s *Service) fillRowPage(ctx context.Context, viewer Viewer, req rowPageRequest) (*MediaPage, int, error) {
	page := max(req.page, 1)
	budget := max(req.budget, 1)
	in := discoverrows.PageInput{ViewerCertification: func(tmdbMediaType string) string {
		return certificationCeilingFor(req.ceiling, tmdbMediaType)
	}}
	out := &MediaPage{Page: page, Results: []MediaResult{}}
	// Charts shift between fetches and lists can name a title twice, so
	// dedupe within the request.
	seen := map[certKey]bool{}
	totalPages := page // until the source tells us the real count, assume the current page exists
	consumed, nextPage := 0, 0
	for next := page; consumed < budget && next <= totalPages && next <= sectionBackfillMaxPage; next++ {
		// Enough survived: stop before spending another page. Titles on
		// unconsumed pages are not lost; nextPage points at the first one.
		if len(out.Results) >= req.want {
			break
		}
		in.Page = next
		raw, err := req.source.Page(ctx, in)
		if err != nil {
			return nil, 0, err
		}
		if raw == nil {
			break
		}
		consumed++
		nextPage = next + 1
		if raw.TotalPages > 0 {
			totalPages = raw.TotalPages
			out.TotalResults = raw.TotalResults
		}
		enriched, err := s.enrichPageWithCeiling(ctx, viewer, raw, req.ceiling)
		if err != nil {
			return nil, 0, err
		}
		for _, item := range enriched.Results {
			key := certKey{mediaType: item.MediaType, id: item.TMDBID}
			if seen[key] || (req.hideAvailable && item.Availability == AvailabilityAvailable) {
				continue
			}
			seen[key] = true
			out.Results = append(out.Results, item)
		}
	}
	out.TotalPages = totalPages
	exhausted := nextPage > totalPages || nextPage > sectionBackfillMaxPage
	switch {
	case consumed <= 1 && req.ceiling == "" && !req.hideAvailable:
		nextPage = 0
	case exhausted && (!req.endCursor || consumed <= 1):
		nextPage = 0
	}
	return out, nextPage, nil
}

var (
	errRequestServiceNotConfigured = errors.New("request service is not configured")
	// errNoDiscoverLists is returned where a list row needs the list wiring.
	errNoDiscoverLists = errors.New("requests: discover lists are not configured")
)

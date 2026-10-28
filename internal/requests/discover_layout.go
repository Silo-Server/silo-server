package requests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	"github.com/Silo-Server/silo-server/internal/requests/discoverrows"
)

// DiscoverLayout is the Discover page's layout. Customized is false, and
// Revision 0, when none is stored and the default rows apply.
type DiscoverLayout struct {
	Rows       []discoverrows.Row
	Customized bool
	Revision   int64
	UpdatedAt  *time.Time
}

// DiscoverLayoutStore reads and writes the Discover layout. The PostgreSQL
// repository implements it; a store without it serves the default layout.
type DiscoverLayoutStore interface {
	GetDiscoverLayout(ctx context.Context) (DiscoverLayout, error)
	// ReplaceDiscoverLayoutConditional stores rows when the revision still
	// matches expected (0 when none is stored, -1 to overwrite).
	ReplaceDiscoverLayoutConditional(ctx context.Context, rows []discoverrows.Row, expected int64) (DiscoverLayout, error)
	// ResetDiscoverLayoutConditional drops the stored layout, so the default
	// rows apply again.
	ResetDiscoverLayoutConditional(ctx context.Context, expected int64) error
}

func (r *Repository) GetDiscoverLayout(ctx context.Context) (DiscoverLayout, error) {
	return scanDiscoverLayout(r.pool.QueryRow(ctx, `SELECT rows, revision, updated_at FROM request_discover_layout WHERE id`))
}

// DiscoverLayoutRows serves the list refresher.
func (r *Repository) DiscoverLayoutRows(ctx context.Context) ([]discoverrows.Row, error) {
	layout, err := r.GetDiscoverLayout(ctx)
	return layout.Rows, err
}

func scanDiscoverLayout(row pgx.Row) (DiscoverLayout, error) {
	var raw []byte
	var out DiscoverLayout
	var updated time.Time
	err := row.Scan(&raw, &out.Revision, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return DiscoverLayout{Rows: discoverrows.DefaultRows()}, nil
	}
	if err != nil {
		return DiscoverLayout{}, fmt.Errorf("get discover layout: %w", err)
	}
	if err := json.Unmarshal(raw, &out.Rows); err != nil {
		return DiscoverLayout{}, fmt.Errorf("decode discover layout: %w", err)
	}
	out.Customized, out.UpdatedAt = true, &updated
	return out, nil
}

func (r *Repository) ReplaceDiscoverLayoutConditional(ctx context.Context, rows []discoverrows.Row, expected int64) (DiscoverLayout, error) {
	raw, err := json.Marshal(rows)
	if err != nil {
		return DiscoverLayout{}, fmt.Errorf("encode discover layout: %w", err)
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return DiscoverLayout{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockRevision(ctx, tx, `SELECT revision FROM request_discover_layout WHERE id FOR UPDATE`, nil, expected, true); err != nil {
		return DiscoverLayout{}, err
	}
	// A missing row cannot be locked; the conflict predicate turns a
	// concurrent first save into a stale write instead of an overwrite.
	out, err := scanDiscoverLayout(tx.QueryRow(ctx, `
		INSERT INTO request_discover_layout (id, rows, updated_at) VALUES (true, $1, now())
		ON CONFLICT (id) DO UPDATE SET rows = EXCLUDED.rows, updated_at = now()
		WHERE $2::bigint = -1 OR request_discover_layout.revision = $2
		RETURNING rows, revision, updated_at`, raw, expected))
	if err != nil {
		return DiscoverLayout{}, err
	}
	if !out.Customized {
		return DiscoverLayout{}, ErrStaleRevision
	}
	return out, tx.Commit(ctx)
}

func (r *Repository) ResetDiscoverLayoutConditional(ctx context.Context, expected int64) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockRevision(ctx, tx, `SELECT revision FROM request_discover_layout WHERE id FOR UPDATE`, nil, expected, true); err != nil {
		return err
	}
	// A missing row cannot be locked: a reset of the default layout must not
	// delete a first save that committed after the check, so the delete
	// carries the revision and a row that appeared makes the reset stale.
	tag, err := tx.Exec(ctx, `DELETE FROM request_discover_layout WHERE id AND ($1::bigint = -1 OR revision = $1)`, expected)
	if err != nil {
		return fmt.Errorf("reset discover layout: %w", err)
	}
	if tag.RowsAffected() == 0 && expected != -1 {
		var appeared bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM request_discover_layout)`).Scan(&appeared); err != nil {
			return fmt.Errorf("reset discover layout: %w", err)
		}
		if appeared {
			return ErrStaleRevision
		}
	}
	return tx.Commit(ctx)
}

// DiscoverLayoutAdmin is the layout an admin edits, with how each list row's
// stored copy stands, by row ID.
type DiscoverLayoutAdmin struct {
	DiscoverLayout
	Sources map[string]discoverrows.SourceStatus
}

func (s *Service) discoverLayoutStore() (DiscoverLayoutStore, error) {
	store, ok := s.store.(DiscoverLayoutStore)
	if !ok {
		return nil, errors.New("request store does not support discover layouts")
	}
	return store, nil
}

// GetDiscoverLayout returns the layout for the admin editor. It works while
// requests are turned off, so the page can be set up first.
func (s *Service) GetDiscoverLayout(ctx context.Context, v Viewer) (*DiscoverLayoutAdmin, error) {
	if !v.IsAdmin {
		return nil, ErrForbidden
	}
	store, err := s.discoverLayoutStore()
	if err != nil {
		return nil, err
	}
	layout, err := store.GetDiscoverLayout(ctx)
	if err != nil {
		return nil, err
	}
	return s.discoverLayoutAdmin(ctx, layout)
}

func (s *Service) discoverLayoutAdmin(ctx context.Context, layout DiscoverLayout) (*DiscoverLayoutAdmin, error) {
	out := &DiscoverLayoutAdmin{DiscoverLayout: layout, Sources: map[string]discoverrows.SourceStatus{}}
	keys := map[string]string{}
	for _, row := range layout.Rows {
		if key := discoverrows.SourceKey(row); key != "" {
			keys[row.ID] = key
		}
	}
	if len(keys) == 0 || s.discoverSnapshots == nil {
		return out, nil
	}
	statuses, err := s.discoverSnapshots.Statuses(ctx, slices.Collect(maps.Values(keys)))
	if err != nil {
		return nil, err
	}
	for id, key := range keys {
		out.Sources[id] = statuses[key]
	}
	return out, nil
}

// ReplaceDiscoverLayoutConditional checks and stores a whole layout, then
// fetches the lists it shows that are missing or stale.
func (s *Service) ReplaceDiscoverLayoutConditional(ctx context.Context, v Viewer, rows []discoverrows.Row, expected int64) (*DiscoverLayoutAdmin, error) {
	if !v.IsAdmin {
		return nil, ErrForbidden
	}
	store, err := s.discoverLayoutStore()
	if err != nil {
		return nil, err
	}
	normalized, fieldErrs := discoverrows.NormalizeLayout(rows)
	if len(fieldErrs) > 0 {
		return nil, &ValidationError{FieldErrors: fieldErrs}
	}
	layout, err := store.ReplaceDiscoverLayoutConditional(ctx, normalized, expected)
	if err != nil {
		return nil, err
	}
	if s.discoverRefresher != nil {
		go func() {
			// Detached from the request, which ends before a list arrives;
			// the scheduled run is the recovery path when this node stops.
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
			defer cancel()
			s.discoverRefresher.RefreshRows(rctx, layout.Rows)
		}()
	}
	return s.discoverLayoutAdmin(ctx, layout)
}

// ResetDiscoverLayoutConditional drops the admin's layout so the default one
// applies again.
func (s *Service) ResetDiscoverLayoutConditional(ctx context.Context, v Viewer, expected int64) error {
	if !v.IsAdmin {
		return ErrForbidden
	}
	store, err := s.discoverLayoutStore()
	if err != nil {
		return err
	}
	return store.ResetDiscoverLayoutConditional(ctx, expected)
}

// PreviewDiscoverRow shows the first page of a row that need not be saved,
// as the admin would see it. A list row's list is fetched live.
func (s *Service) PreviewDiscoverRow(ctx context.Context, v Viewer, row discoverrows.Row) (*MediaPage, error) {
	if !v.IsAdmin {
		return nil, ErrForbidden
	}
	normalized, fieldErrs := discoverrows.NormalizeRow(row)
	if len(fieldErrs) > 0 {
		return nil, &ValidationError{FieldErrors: fieldErrs}
	}
	if normalized.Type.IsBrand() {
		return nil, &ValidationError{FieldErrors: map[string]string{"type": "a brand row has no titles to preview"}}
	}
	var source discoverrows.Source
	if normalized.List != nil {
		if s.discoverRefresher == nil {
			return nil, errNoDiscoverLists
		}
		cards, err := s.discoverRefresher.FetchCards(ctx, normalized, discoverrows.PageSize)
		if err != nil {
			return nil, err
		}
		source = discoverrows.CardsSource(cards)
	} else {
		kind, _ := discoverrows.KindOf(normalized.Type)
		source = kind.Source(s.discoverEnv(), normalized)
	}
	ctx = withPolicyCache(ctx)
	page, _, err := s.fillRowPage(ctx, v, rowPageRequest{
		source: source, page: 1, want: sectionResultsPerPage, budget: sectionBackfillBudgetAggregate,
		hideAvailable: normalized.HideAvailable,
	})
	return page, err
}

// TMDBWatchProviderClient lists streaming services. Detected by type
// assertion, like TMDBExternalIDClient.
type TMDBWatchProviderClient interface {
	WatchProviders(ctx context.Context, mediaType, region string) ([]tmdb.WatchProvider, error)
}

// ListWatchProviders lists the streaming services a discover row can filter
// on in a region, for the admin editor.
func (s *Service) ListWatchProviders(ctx context.Context, v Viewer, mediaType MediaType, region string) ([]tmdb.WatchProvider, error) {
	if !v.IsAdmin {
		return nil, ErrForbidden
	}
	client, ok := s.tmdb.(TMDBWatchProviderClient)
	if !ok {
		return nil, errors.New("requests: tmdb client cannot list watch providers")
	}
	mediaType, err := normalizeMediaType(mediaType)
	if err != nil {
		return nil, err
	}
	return client.WatchProviders(ctx, string(mediaType), strings.ToUpper(strings.TrimSpace(region)))
}

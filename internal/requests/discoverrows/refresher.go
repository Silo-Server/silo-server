package discoverrows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

const (
	// MaxListEntries bounds a stored list. A row shows at most MaxLimit
	// titles and its see all page pages through the rest; a list fetch
	// costs a TMDB call per MDBList entry.
	MaxListEntries = 200
	// listMaxAge is how old a list may get before the scheduled run fetches
	// it again.
	listMaxAge = 12 * time.Hour
	// listErrorRetry spaces scheduled retries of a list whose fetch failed.
	listErrorRetry = 30 * time.Minute
	// listRefreshLease bounds one fetch, card lookups included.
	listRefreshLease = 10 * time.Minute
	// listRetention is how long a list no row names is kept.
	listRetention = 7 * 24 * time.Hour
	// cardLookupConcurrency bounds an MDBList fetch's TMDB card lookups; the
	// client's rate limit sets the pace.
	cardLookupConcurrency = 4
)

// LayoutReader reads the Discover layout the refresher keeps lists for.
type LayoutReader interface {
	// DiscoverLayoutRows returns the stored layout's rows, or the default
	// rows when none is stored.
	DiscoverLayoutRows(ctx context.Context) ([]Row, error)
}

// ListTMDB is the slice of the TMDB client list fetches use.
type ListTMDB interface {
	GetListMedia(ctx context.Context, id, limit int) ([]tmdb.MediaResult, error)
	MediaCard(ctx context.Context, mediaType string, id int) (*tmdb.MediaResult, error)
	FindByExternalIDCached(ctx context.Context, source, externalID string) ([]tmdb.MediaResult, error)
}

type snapshotStore interface {
	SnapshotReader
	TryClaimRefresh(ctx context.Context, key string, lease time.Duration) (time.Time, bool, error)
	SaveSuccess(ctx context.Context, key string, cards []Card, status string, claimAt, refreshedAt time.Time) error
	RecordFailure(ctx context.Context, key, message string, claimAt time.Time) error
	DeleteUnreferenced(ctx context.Context, keep []string, olderThan time.Duration) (int64, error)
}

// mdblistShow is MDBList's media type for a series.
const mdblistShow = "show"

// MDBListEntry is one entry of an MDBList /json feed. ID is the TMDB ID.
type MDBListEntry struct {
	ID        json.Number `json:"id"`
	IMDbID    string      `json:"imdb_id"`
	MediaType string      `json:"mediatype"`
}

// Refresher keeps the stored copies of the lists the layout's rows show. A
// TaskManager task drives RunOnce; a layout save calls RefreshRows so a new
// list fills right away. Any node may run either: the lease decides who
// fetches.
type Refresher struct {
	Layout    LayoutReader
	Snapshots snapshotStore
	TMDB      ListTMDB
	// FetchMDBList reads an MDBList feed with the query parameters an order
	// adds (nil for the list's own order); nil reads the public /json feed.
	FetchMDBList func(ctx context.Context, url string, limit int, query url.Values) ([]MDBListEntry, error)
	Now          func() time.Time
	logger       *slog.Logger
}

// NewRefresher creates a Refresher over MDBList's public feed.
func NewRefresher(layout LayoutReader, snapshots *SnapshotRepository, client ListTMDB) *Refresher {
	return &Refresher{
		Layout: layout, Snapshots: snapshots, TMDB: client,
		Now:    time.Now,
		logger: slog.Default().With("component", "requests.discover_lists"),
	}
}

func (r *Refresher) log() *slog.Logger {
	if r.logger != nil {
		return r.logger
	}
	return slog.Default()
}

// RefreshResult is the JSON summary attached to the task run.
type RefreshResult struct {
	Lists     int   `json:"lists"`
	Refreshed int   `json:"refreshed"`
	Fresh     int   `json:"fresh"`
	Failed    int   `json:"failed"`
	Skipped   int   `json:"skipped"`
	Removed   int64 `json:"removed"`
}

const (
	outcomeRefreshed = "refreshed"
	outcomeFresh     = "fresh"
	outcomeSkipped   = "skipped"
	outcomeFailed    = "failed"
)

// RunOnce fetches every enabled list row's list that is older than
// listMaxAge, then drops lists no row has named for listRetention.
func (r *Refresher) RunOnce(ctx context.Context) (json.RawMessage, error) {
	rows, err := r.Layout.DiscoverLayoutRows(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading discover layout: %w", err)
	}
	var result RefreshResult
	refreshed := map[string]bool{}
	var keep []string
	for _, row := range rows {
		key := SourceKey(row)
		if key == "" {
			continue
		}
		keep = append(keep, key)
		if !row.Enabled || refreshed[key] {
			continue
		}
		refreshed[key] = true
		result.Lists++
		switch r.refresh(ctx, key, row, true) {
		case outcomeRefreshed:
			result.Refreshed++
		case outcomeFresh:
			result.Fresh++
		case outcomeSkipped:
			result.Skipped++
		default:
			result.Failed++
		}
	}
	removed, err := r.Snapshots.DeleteUnreferenced(ctx, keep, listRetention)
	if err != nil {
		r.log().ErrorContext(ctx, "discover lists: removing unused lists failed", "error", err)
	}
	result.Removed = removed
	data, _ := json.Marshal(result)
	return data, nil
}

// RefreshRows fetches the lists saved rows show that are missing, stale or
// failing. Best effort: the scheduled run is the recovery path.
func (r *Refresher) RefreshRows(ctx context.Context, rows []Row) {
	seen := map[string]bool{}
	for _, row := range rows {
		if key := SourceKey(row); key != "" && row.Enabled && !seen[key] {
			seen[key] = true
			r.refresh(ctx, key, row, false)
		}
	}
}

// FetchCards fetches a list row's first limit titles live, for a preview of
// a row not saved yet.
func (r *Refresher) FetchCards(ctx context.Context, row Row, limit int) ([]Card, error) {
	return r.fetch(ctx, row, limit)
}

func (r *Refresher) refresh(ctx context.Context, key string, row Row, backOff bool) string {
	snapshot, found, err := r.Snapshots.Get(ctx, key)
	if err != nil {
		r.log().ErrorContext(ctx, "discover lists: reading stored list failed", "source", key, "error", err)
		return outcomeFailed
	}
	now := r.Now()
	if found && snapshot.LastStatus != snapshotError && snapshot.RefreshedAt != nil && now.Sub(*snapshot.RefreshedAt) < listMaxAge {
		return outcomeFresh
	}
	if backOff && found && snapshot.LastStatus == snapshotError && snapshot.LastAttemptAt != nil && now.Sub(*snapshot.LastAttemptAt) < listErrorRetry {
		return outcomeSkipped
	}
	claimAt, claimed, err := r.Snapshots.TryClaimRefresh(ctx, key, listRefreshLease)
	if err != nil {
		r.log().ErrorContext(ctx, "discover lists: claim failed", "source", key, "error", err)
		return outcomeFailed
	}
	if !claimed {
		return outcomeSkipped
	}
	fetchCtx, cancel := context.WithTimeout(ctx, listRefreshLease)
	cards, fetchErr := r.fetch(fetchCtx, row, MaxListEntries)
	cancel()
	if fetchErr != nil {
		r.log().WarnContext(ctx, "discover lists: fetch failed", "source", key, "error", fetchErr)
		r.recordFailure(ctx, key, fetchErr.Error(), claimAt)
		return outcomeFailed
	}
	status := snapshotOK
	if len(cards) == 0 {
		// An empty answer over a list we have is more likely a glitch than a
		// list emptied overnight: keep the copy and try again later.
		if found && len(snapshot.Cards) > 0 {
			r.recordFailure(ctx, key, "the list came back empty", claimAt)
			return outcomeFailed
		}
		status = snapshotEmpty
	}
	// The save outlives a canceled run, so the claim does not dangle.
	if err := r.Snapshots.SaveSuccess(context.WithoutCancel(ctx), key, cards, status, claimAt, r.Now()); err != nil {
		if errors.Is(err, ErrLeaseLost) {
			return outcomeSkipped
		}
		r.log().ErrorContext(ctx, "discover lists: save failed", "source", key, "error", err)
		return outcomeFailed
	}
	return outcomeRefreshed
}

// recordFailure records a failed fetch, keeping the stored cards. A lost
// lease means another worker owns the list now, so there is nothing to
// record.
func (r *Refresher) recordFailure(ctx context.Context, key, message string, claimAt time.Time) {
	if err := r.Snapshots.RecordFailure(context.WithoutCancel(ctx), key, message, claimAt); err != nil && !errors.Is(err, ErrLeaseLost) {
		r.log().ErrorContext(ctx, "discover lists: recording failure failed", "source", key, "error", err)
	}
}

func (r *Refresher) fetch(ctx context.Context, row Row, limit int) ([]Card, error) {
	if row.List == nil {
		return nil, fmt.Errorf("row %s has no list", row.ID)
	}
	switch row.Type {
	case TypeTMDBList:
		id, err := collectionutil.ParseTMDBListURL(row.List.URL)
		if err != nil {
			return nil, err
		}
		results, err := r.TMDB.GetListMedia(ctx, id, limit)
		if err != nil {
			return nil, err
		}
		cards := make([]Card, 0, len(results))
		for _, res := range results {
			cards = append(cards, cardOf(res))
		}
		return cards, nil
	case TypeMDBList:
		entries, err := r.fetchMDBList(ctx, row.List.URL, limit, mdblistFeedOrders[row.List.Order])
		if err != nil {
			return nil, err
		}
		return r.mdblistCards(ctx, entries)
	default:
		return nil, fmt.Errorf("row %s is not a list row", row.ID)
	}
}

func (r *Refresher) fetchMDBList(ctx context.Context, listURL string, limit int, query url.Values) ([]MDBListEntry, error) {
	if r.FetchMDBList != nil {
		return r.FetchMDBList(ctx, listURL, limit, query)
	}
	return collectionutil.FetchMDBListJSONQuery[MDBListEntry](ctx, collectionutil.MDBListHTTPClient(nil), listURL, limit, query)
}

// mdblistCards looks each entry up on TMDB, keeping list order. MDBList
// carries no artwork, so every entry costs a call: by TMDB ID, or by IMDb ID
// for an entry without one. A title TMDB no longer has is skipped; any other
// failure fails the fetch, so the last good copy stays.
func (r *Refresher) mdblistCards(ctx context.Context, entries []MDBListEntry) ([]Card, error) {
	cards := make([]*Card, len(entries))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cardLookupConcurrency)
	// A list can name a title twice; look each one up once, at its first place.
	scheduled := map[string]bool{}
	for i, entry := range entries {
		mediaType := ""
		switch entry.MediaType {
		case MediaMovie:
			mediaType = MediaMovie
		case mdblistShow:
			mediaType = MediaSeries
		default:
			continue
		}
		lookup := mediaType + "/" + entry.ID.String() + "/" + entry.IMDbID
		if scheduled[lookup] {
			continue
		}
		scheduled[lookup] = true
		g.Go(func() error {
			card, err := r.lookupCard(gctx, mediaType, entry)
			if errors.Is(err, tmdb.ErrNotFound) {
				return nil
			}
			cards[i] = card
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	type titleKey struct {
		mediaType string
		id        int
	}
	out := make([]Card, 0, len(cards))
	seen := map[titleKey]bool{}
	for _, c := range cards {
		if c == nil {
			continue
		}
		key := titleKey{c.MediaType, c.ID}
		if !seen[key] {
			seen[key] = true
			out = append(out, *c)
		}
	}
	return out, nil
}

func (r *Refresher) lookupCard(ctx context.Context, mediaType string, entry MDBListEntry) (*Card, error) {
	if id, err := entry.ID.Int64(); err == nil && id > 0 {
		res, err := r.TMDB.MediaCard(ctx, mediaType, int(id))
		if err != nil {
			return nil, err
		}
		card := cardOf(*res)
		return &card, nil
	}
	if entry.IMDbID == "" {
		return nil, nil
	}
	results, err := r.TMDB.FindByExternalIDCached(ctx, tmdb.ExternalSourceIMDb, entry.IMDbID)
	if err != nil {
		return nil, err
	}
	for _, res := range results {
		if res.MediaType == mediaType {
			card := cardOf(res)
			return &card, nil
		}
	}
	return nil, nil
}

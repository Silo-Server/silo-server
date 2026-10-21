package sections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

const (
	// seasonalListLookahead is how far ahead the refresher fetches lists, so
	// a row has its list the moment its window opens.
	seasonalListLookahead = 14 * 24 * time.Hour
	// seasonalListMaxAge is how old a list may get before the scheduled run
	// fetches it again.
	seasonalListMaxAge = 20 * time.Hour
	// seasonalListMaxEntries bounds one list. The row shows a few dozen titles
	// at most; the rest only widens the library match.
	seasonalListMaxEntries   = 2000
	seasonalListRefreshLease = 5 * time.Minute
)

// List entry media types, as MDBList names them.
const (
	listEntryMovie = "movie"
	listEntryShow  = "show"
)

// seasonalListEntry is one list title by external ID, in list order.
type seasonalListEntry struct {
	TMDB string `json:"tmdb,omitempty"`
	IMDb string `json:"imdb,omitempty"`
	TVDB string `json:"tvdb,omitempty"`
	// Type is "movie" or "show".
	Type string `json:"type"`
}

// SeasonalListSnapshot is the stored copy of one seasonal MDBList list.
type SeasonalListSnapshot struct {
	ListURL     string
	Entries     []seasonalListEntry
	RefreshedAt *time.Time
	LastStatus  string
	LastError   string
}

// ErrSeasonalListLeaseLost means another worker took over the refresh before
// this one saved its result.
var ErrSeasonalListLeaseLost = errors.New("seasonal list refresh lease lost")

// SeasonalListRepository persists seasonal_list_snapshots.
type SeasonalListRepository struct {
	pool *pgxpool.Pool
}

// NewSeasonalListRepository creates a SeasonalListRepository.
func NewSeasonalListRepository(pool *pgxpool.Pool) *SeasonalListRepository {
	return &SeasonalListRepository{pool: pool}
}

// Get returns the stored list. found is false before the first refresh.
// Snapshots are keyed by the list's canonical /json URL, so the same list
// typed two ways shares one row.
func (r *SeasonalListRepository) Get(ctx context.Context, listURL string) (SeasonalListSnapshot, bool, error) {
	key, err := collectionutil.CanonicalMDBListURL(listURL)
	if err != nil {
		return SeasonalListSnapshot{}, false, err
	}
	var s SeasonalListSnapshot
	var raw []byte
	err = r.pool.QueryRow(ctx, `
		SELECT list_url, entries, refreshed_at, last_status, last_error
		FROM seasonal_list_snapshots WHERE list_url = $1`, key).
		Scan(&s.ListURL, &raw, &s.RefreshedAt, &s.LastStatus, &s.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return SeasonalListSnapshot{}, false, nil
	}
	if err != nil {
		return SeasonalListSnapshot{}, false, fmt.Errorf("getting seasonal list: %w", err)
	}
	if err := json.Unmarshal(raw, &s.Entries); err != nil {
		return SeasonalListSnapshot{}, false, fmt.Errorf("decoding seasonal list entries: %w", err)
	}
	return s, true, nil
}

// TryClaimRefresh claims one list for a bounded lease, the same way trending
// snapshots are claimed: no pool connection is held while the list downloads,
// and a crashed worker's claim expires.
func (r *SeasonalListRepository) TryClaimRefresh(ctx context.Context, listURL string, lease time.Duration) (time.Time, bool, error) {
	var claimedAt time.Time
	err := r.pool.QueryRow(ctx, `
		WITH lease_clock AS (SELECT clock_timestamp() AS claimed_at)
		INSERT INTO seasonal_list_snapshots (list_url, last_attempt_at, last_status, last_error)
		SELECT $1, claimed_at, 'refreshing', '' FROM lease_clock
		ON CONFLICT (list_url) DO UPDATE SET
			last_attempt_at = EXCLUDED.last_attempt_at,
			last_status     = 'refreshing',
			last_error      = ''
		WHERE (seasonal_list_snapshots.last_attempt_at IS NULL
		       OR seasonal_list_snapshots.last_attempt_at <= EXCLUDED.last_attempt_at)
		  AND (seasonal_list_snapshots.last_status <> 'refreshing'
		       OR seasonal_list_snapshots.last_attempt_at <=
		          EXCLUDED.last_attempt_at - ($2::bigint * INTERVAL '1 millisecond'))
		RETURNING last_attempt_at`, listURL, max(lease.Milliseconds(), 1)).Scan(&claimedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("claiming seasonal list refresh: %w", err)
	}
	return claimedAt, true, nil
}

// SaveSuccess replaces the stored entries after a completed download.
func (r *SeasonalListRepository) SaveSuccess(ctx context.Context, listURL string, entries []seasonalListEntry, status string, claimAt, refreshedAt time.Time) error {
	if entries == nil {
		entries = []seasonalListEntry{}
	}
	raw, _ := json.Marshal(entries) // plain strings; cannot fail
	tag, err := r.pool.Exec(ctx, `
		UPDATE seasonal_list_snapshots SET
			entries = $2, entry_count = $3, refreshed_at = $4,
			last_status = $5, last_error = ''
		WHERE list_url = $1 AND last_status = 'refreshing' AND last_attempt_at = $6`,
		listURL, raw, len(entries), refreshedAt, status, claimAt)
	if err != nil {
		return fmt.Errorf("saving seasonal list: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrSeasonalListLeaseLost
	}
	return nil
}

// RecordFailure records a failed download and keeps the last good entries.
func (r *SeasonalListRepository) RecordFailure(ctx context.Context, listURL, message string, claimAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE seasonal_list_snapshots SET last_status = 'error', last_error = $2
		WHERE list_url = $1 AND last_status = 'refreshing' AND last_attempt_at = $3`,
		listURL, message, claimAt)
	if err != nil {
		return fmt.Errorf("recording seasonal list failure: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrSeasonalListLeaseLost
	}
	return nil
}

type seasonalListStore interface {
	Get(ctx context.Context, listURL string) (SeasonalListSnapshot, bool, error)
	TryClaimRefresh(ctx context.Context, listURL string, lease time.Duration) (time.Time, bool, error)
	SaveSuccess(ctx context.Context, listURL string, entries []seasonalListEntry, status string, claimAt, refreshedAt time.Time) error
	RecordFailure(ctx context.Context, listURL, message string, claimAt time.Time) error
}

type seasonalConfigLister interface {
	ListSeasonalConfigs(ctx context.Context) ([]json.RawMessage, error)
}

// mdblistListItem is one entry of an MDBList /json feed.
type mdblistListItem struct {
	ID        json.Number `json:"id"`
	IMDbID    string      `json:"imdb_id"`
	TVDBID    json.Number `json:"tvdbid"`
	MediaType string      `json:"mediatype"`
}

// SeasonalListRefresher downloads the MDBList lists seasonal rows show, in
// IMDb popularity order, ahead of their windows. A TaskManager task drives
// RunOnce; section saves call RefreshConfig so a new row fills right away.
type SeasonalListRefresher struct {
	Sections  seasonalConfigLister
	Snapshots seasonalListStore
	// Fetch downloads a list; nil uses MDBList's public /json feed.
	Fetch  func(ctx context.Context, listURL string) ([]seasonalListEntry, error)
	Clock  recipes.Clock
	logger *slog.Logger
}

// NewSeasonalListRefresher creates a refresher backed by MDBList.
func NewSeasonalListRefresher(sectionLister seasonalConfigLister, snapshots seasonalListStore) *SeasonalListRefresher {
	return &SeasonalListRefresher{
		Sections:  sectionLister,
		Snapshots: snapshots,
		Clock:     recipes.RealClock{},
		logger:    slog.Default().With("component", "sections.seasonal_lists"),
	}
}

// SeasonalListRefreshResult is the JSON summary attached to the task run.
type SeasonalListRefreshResult struct {
	Lists     int `json:"lists"`
	Refreshed int `json:"refreshed"`
	Fresh     int `json:"fresh"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
}

func (r *SeasonalListRefresher) now() time.Time {
	if r.Clock != nil {
		return r.Clock.Now()
	}
	return time.Now()
}

func (r *SeasonalListRefresher) log() *slog.Logger {
	if r.logger != nil {
		return r.logger
	}
	return slog.Default()
}

// RunOnce refreshes every list an enabled seasonal row shows now or within
// the lookahead and that is older than seasonalListMaxAge.
func (r *SeasonalListRefresher) RunOnce(ctx context.Context) (json.RawMessage, error) {
	configs, listErr := r.Sections.ListSeasonalConfigs(ctx)
	urls := seasonalListURLs(configs, r.now())
	result := SeasonalListRefreshResult{Lists: len(urls)}
	for _, listURL := range urls {
		switch r.refreshIfStale(ctx, listURL) {
		case seasonalListRefreshed:
			result.Refreshed++
		case seasonalListFresh:
			result.Fresh++
		case seasonalListSkipped:
			result.Skipped++
		default:
			result.Failed++
		}
	}
	data, _ := json.Marshal(result)
	if listErr != nil {
		return data, fmt.Errorf("listing seasonal sections: %w", listErr)
	}
	return data, nil
}

// RefreshConfig fetches the lists one saved row needs and does not have yet.
// Best effort: the scheduled run is the recovery path.
func (r *SeasonalListRefresher) RefreshConfig(ctx context.Context, config json.RawMessage) {
	for _, listURL := range seasonalListURLs([]json.RawMessage{config}, r.now()) {
		r.refreshIfStale(ctx, listURL)
	}
}

// seasonalListURLs returns the canonical, deduplicated list URLs the configs
// show between now and the lookahead.
func seasonalListURLs(configs []json.RawMessage, now time.Time) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range configs {
		var p recipes.SeasonalThemedParams
		if len(raw) == 0 || json.Unmarshal(raw, &p) != nil {
			continue
		}
		for _, listURL := range recipes.SeasonalListURLsAround(p, now, seasonalListLookahead) {
			key, err := collectionutil.CanonicalMDBListURL(listURL)
			if err != nil || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

const (
	seasonalListRefreshed = "refreshed"
	seasonalListFresh     = "fresh"
	seasonalListSkipped   = "skipped"
	seasonalListFailed    = "failed"
)

func (r *SeasonalListRefresher) refreshIfStale(ctx context.Context, listURL string) string {
	snapshot, found, err := r.Snapshots.Get(ctx, listURL)
	if err != nil {
		r.log().ErrorContext(ctx, "seasonal list refresh: reading snapshot failed", "list_url", listURL, "error", err)
		return seasonalListFailed
	}
	if found && snapshot.RefreshedAt != nil && r.now().Sub(*snapshot.RefreshedAt) < seasonalListMaxAge {
		return seasonalListFresh
	}
	claimAt, claimed, err := r.Snapshots.TryClaimRefresh(ctx, listURL, seasonalListRefreshLease)
	if err != nil {
		r.log().ErrorContext(ctx, "seasonal list refresh: claim failed", "list_url", listURL, "error", err)
		return seasonalListFailed
	}
	if !claimed {
		return seasonalListSkipped
	}
	entries, fetchErr := r.fetch(ctx, listURL)
	if fetchErr != nil {
		r.log().WarnContext(ctx, "seasonal list refresh: download failed", "list_url", listURL, "error", fetchErr)
		r.recordFailure(ctx, listURL, fetchErr.Error(), claimAt)
		return seasonalListFailed
	}
	// The same status values as trending snapshots.
	status := trendingRefreshOK
	if len(entries) == 0 {
		// An empty answer over a list we have is more likely a glitch than a
		// list emptied overnight: keep the copy and try again next run.
		if found && len(snapshot.Entries) > 0 {
			r.recordFailure(ctx, listURL, "list came back empty", claimAt)
			return seasonalListFailed
		}
		status = trendingRefreshEmpty
	}
	if err := r.Snapshots.SaveSuccess(ctx, listURL, entries, status, claimAt, r.now()); err != nil {
		if errors.Is(err, ErrSeasonalListLeaseLost) {
			return seasonalListSkipped
		}
		r.log().ErrorContext(ctx, "seasonal list refresh: save failed", "list_url", listURL, "error", err)
		return seasonalListFailed
	}
	return seasonalListRefreshed
}

// recordFailure records a failed refresh, keeping the stored entries. A lost
// lease means another worker owns the list now, so there is nothing to record.
func (r *SeasonalListRefresher) recordFailure(ctx context.Context, listURL, message string, claimAt time.Time) {
	if err := r.Snapshots.RecordFailure(ctx, listURL, message, claimAt); err != nil && !errors.Is(err, ErrSeasonalListLeaseLost) {
		r.log().ErrorContext(ctx, "seasonal list refresh: recording failure failed", "list_url", listURL, "error", err)
	}
}

func (r *SeasonalListRefresher) fetch(ctx context.Context, listURL string) ([]seasonalListEntry, error) {
	if r.Fetch != nil {
		return r.Fetch(ctx, listURL)
	}
	items, err := collectionutil.FetchMDBListJSONQuery[mdblistListItem](
		ctx, collectionutil.MDBListHTTPClient(nil), listURL, seasonalListMaxEntries, collectionutil.MDBListIMDbPopularOrder)
	if err != nil {
		return nil, err
	}
	return seasonalEntriesFromMDBList(items), nil
}

func seasonalEntriesFromMDBList(items []mdblistListItem) []seasonalListEntry {
	entries := make([]seasonalListEntry, 0, len(items))
	for _, item := range items {
		entry := seasonalListEntry{IMDb: item.IMDbID, Type: item.MediaType}
		if id := item.ID.String(); id != "" && id != "0" {
			entry.TMDB = id
		}
		if id := item.TVDBID.String(); id != "" && id != "0" {
			entry.TVDB = id
		}
		if entry.Type != listEntryMovie && entry.Type != listEntryShow {
			continue
		}
		if entry.TMDB == "" && entry.IMDb == "" && entry.TVDB == "" {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

// resolveSeasonalEntries maps list entries to library content IDs in list
// order, skipping titles the library does not have. As in the trending
// resolver, a show matches by TVDB first, then TMDB, then IMDb; a movie by
// TMDB, then IMDb, then TVDB.
func resolveSeasonalEntries(ctx context.Context, resolver externalIDResolver, entries []seasonalListEntry) ([]string, error) {
	var movies, series catalog.ExternalIDBatch
	for _, e := range entries {
		batch := &movies
		if e.Type == listEntryShow {
			batch = &series
		}
		if e.TMDB != "" {
			batch.TMDBIDs = append(batch.TMDBIDs, e.TMDB)
		}
		if e.IMDb != "" {
			batch.IMDbIDs = append(batch.IMDbIDs, e.IMDb)
		}
		if e.TVDB != "" {
			batch.TVDBIDs = append(batch.TVDBIDs, e.TVDB)
		}
	}
	dedupeExternalIDs(&movies)
	dedupeExternalIDs(&series)
	movieLookup, err := resolver.GetByExternalIDs(ctx, movies, "movie")
	if err != nil {
		return nil, fmt.Errorf("resolving seasonal movies: %w", err)
	}
	seriesLookup, err := resolver.GetByExternalIDs(ctx, series, "series")
	if err != nil {
		return nil, fmt.Errorf("resolving seasonal series: %w", err)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		var id string
		if e.Type == listEntryShow {
			id = firstNonEmpty(seriesLookup.ByTVDB[e.TVDB], seriesLookup.ByTMDB[e.TMDB], seriesLookup.ByIMDb[e.IMDb])
		} else {
			id = firstNonEmpty(movieLookup.ByTMDB[e.TMDB], movieLookup.ByIMDb[e.IMDb], movieLookup.ByTVDB[e.TVDB])
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// dedupeExternalIDs drops repeated IDs from a lookup batch; a list can name
// one title twice. Order does not matter to the lookup.
func dedupeExternalIDs(batch *catalog.ExternalIDBatch) {
	for _, ids := range []*[]string{&batch.TMDBIDs, &batch.IMDbIDs, &batch.TVDBIDs} {
		slices.Sort(*ids)
		*ids = slices.Compact(*ids)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

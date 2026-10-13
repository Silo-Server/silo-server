// Package animeids keeps a copy of an AniDB-based anime list, by the TVDB and
// IMDb IDs TMDB also reports, so request routing can tell anime TMDB does not
// tag as such. The list is Kometa's Anime-IDs (MIT), rebuilt daily from AniDB
// and the Anime-Lists mappings. A scheduled task replaces the stored copy; the
// request path only reads it, so a slow or unreachable GitHub never holds a
// request up.
package animeids

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SourceURL is the published list.
const SourceURL = "https://raw.githubusercontent.com/Kometa-Team/Anime-IDs/master/anime_ids.json"

const (
	sourceHost = "raw.githubusercontent.com"
	// maxBody bounds the download; the list is under 2 MB today.
	maxBody = 16 << 20
	// minEntries is the fewest listed IDs a download must hold to replace
	// the stored copy; fewer means a truncated or broken file. The list
	// yields about 6,000 today.
	minEntries = 3000
	// lease bounds how long one server's claim keeps the others from
	// downloading; a crashed server's claim expires with it.
	lease = 10 * time.Minute
)

// Store answers whether the list names a title.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore reads the stored list.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Listed reports whether the list names a title: a series by its TVDB or IMDb
// ID, a movie by its IMDb ID. TVDB numbers movies apart from series, so a
// movie's TVDB ID is not compared. An empty list names nothing.
func (s *Store) Listed(ctx context.Context, movie bool, tvdbID int, imdbID string) (bool, error) {
	imdbID = strings.TrimSpace(imdbID)
	tvdb := ""
	if tvdbID > 0 && !movie {
		tvdb = strconv.Itoa(tvdbID)
	}
	if tvdb == "" && imdbID == "" {
		return false, nil
	}
	var listed bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM anime_ids
			WHERE (source = 'tvdb' AND external_id = $1) OR (source = 'imdb' AND external_id = $2)
		)`, tvdb, imdbID).Scan(&listed)
	if err != nil {
		return false, fmt.Errorf("look up anime list: %w", err)
	}
	return listed, nil
}

// Result is what one refresh did.
type Result struct {
	// Skipped: another server holds the refresh.
	Skipped bool `json:"skipped,omitempty"`
	// Unchanged: the published list has not changed since the last copy.
	Unchanged bool `json:"unchanged,omitempty"`
	Entries   int  `json:"entries,omitempty"`
}

// Refresher downloads the list and replaces the stored copy.
type Refresher struct {
	pool   *pgxpool.Pool
	client *http.Client
	url    string
}

// NewRefresher downloads from SourceURL.
func NewRefresher(pool *pgxpool.Pool) *Refresher {
	return &Refresher{pool: pool, client: newClient(), url: SourceURL}
}

// newClient only follows redirects to the list's own host.
func newClient() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("anime list: too many redirects")
			}
			if req.URL.Scheme != "https" || req.URL.Hostname() != sourceHost {
				return fmt.Errorf("anime list: refusing redirect to %s", req.URL.Redacted())
			}
			return nil
		},
	}
}

// Refresh downloads the list and replaces the stored copy, unless another
// server is already doing so or the list has not changed. A failed download
// or a broken file keeps the stored copy and records why.
func (r *Refresher) Refresh(ctx context.Context) (Result, error) {
	c, claimed, err := r.claim(ctx)
	if err != nil || !claimed {
		return Result{Skipped: !claimed}, err
	}
	ids, newETag, unchanged, err := r.download(ctx, c.etag)
	if err == nil && unchanged {
		return Result{Unchanged: true}, r.finish(ctx, c, "unchanged", "")
	}
	if err == nil {
		err = r.replace(ctx, c, ids, newETag)
	}
	if err != nil {
		if recordErr := r.finish(context.WithoutCancel(ctx), c, "error", err.Error()); recordErr != nil {
			return Result{}, errors.Join(err, recordErr)
		}
		return Result{}, err
	}
	return Result{Entries: len(ids)}, nil
}

// claimToken is this server's claim: when it was taken, which is what the
// server's later writes must still find, and the ETag to send.
type claimToken struct {
	at   time.Time
	etag string
}

// errClaimLost means another server took the refresh over after this one's
// lease ran out; this one's result is dropped.
var errClaimLost = errors.New("anime list refresh: another server took the refresh over")

// claim takes the refresh for this server. The stored ETag is only sent when
// rows are stored, so a list that lost its rows is downloaded again.
func (r *Refresher) claim(ctx context.Context) (claimToken, bool, error) {
	var c claimToken
	err := r.pool.QueryRow(ctx, `
		INSERT INTO anime_ids_refresh (id, last_attempt_at, last_status, last_error)
		VALUES (true, clock_timestamp(), 'refreshing', '')
		ON CONFLICT (id) DO UPDATE SET
			last_attempt_at = EXCLUDED.last_attempt_at,
			last_status = 'refreshing',
			last_error = ''
		WHERE anime_ids_refresh.last_status <> 'refreshing'
		   OR anime_ids_refresh.last_attempt_at <= clock_timestamp() - ($1::bigint * INTERVAL '1 millisecond')
		RETURNING last_attempt_at, CASE WHEN EXISTS (SELECT 1 FROM anime_ids) THEN etag ELSE '' END`,
		lease.Milliseconds()).Scan(&c.at, &c.etag)
	if errors.Is(err, pgx.ErrNoRows) {
		return claimToken{}, false, nil
	}
	if err != nil {
		return claimToken{}, false, fmt.Errorf("claim anime list refresh: %w", err)
	}
	return c, true, nil
}

// finish records a refresh that stored nothing new, if the claim is still
// this server's.
func (r *Refresher) finish(ctx context.Context, c claimToken, status, message string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE anime_ids_refresh SET last_status = $1, last_error = $2,
			refreshed_at = CASE WHEN $1 = 'unchanged' THEN clock_timestamp() ELSE refreshed_at END
		WHERE id AND last_attempt_at = $3`, status, message, c.at)
	if err != nil {
		return fmt.Errorf("record anime list refresh: %w", err)
	}
	return nil
}

func (r *Refresher) download(ctx context.Context, etag string) (ids []listedID, newETag string, unchanged bool, err error) {
	u, err := url.Parse(r.url)
	if err != nil {
		return nil, "", false, fmt.Errorf("anime list url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("User-Agent", "silo-anime-ids")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, "", false, fmt.Errorf("download anime list: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, etag, true, nil
	case http.StatusOK:
	default:
		return nil, "", false, fmt.Errorf("download anime list: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, "", false, fmt.Errorf("download anime list: %w", err)
	}
	if len(body) > maxBody {
		return nil, "", false, errors.New("download anime list: larger than expected")
	}
	ids, err = parse(body)
	if err != nil {
		return nil, "", false, err
	}
	return ids, resp.Header.Get("ETag"), false, nil
}

// The sources a listed ID comes from, as the anime_ids table stores them.
const (
	sourceTVDB = "tvdb"
	sourceIMDb = "imdb"
)

type listedID struct {
	source, id string
}

// parse reads the list: AniDB IDs keyed to their TVDB series ID and IMDb IDs
// (several, comma-separated, for a multi-part film).
func parse(body []byte) ([]listedID, error) {
	var raw map[string]struct {
		TVDBID *int   `json:"tvdb_id"`
		IMDbID string `json:"imdb_id"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("read anime list: %w", err)
	}
	seen := map[listedID]bool{}
	var out []listedID
	add := func(id listedID) {
		if id.id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, entry := range raw {
		if entry.TVDBID != nil && *entry.TVDBID > 0 {
			add(listedID{sourceTVDB, strconv.Itoa(*entry.TVDBID)})
		}
		for imdb := range strings.SplitSeq(entry.IMDbID, ",") {
			if imdb = strings.TrimSpace(imdb); strings.HasPrefix(imdb, "tt") {
				add(listedID{sourceIMDb, imdb})
			}
		}
	}
	if len(out) < minEntries {
		return nil, fmt.Errorf("read anime list: only %d IDs, expected at least %d", len(out), minEntries)
	}
	return out, nil
}

// replace swaps the stored copy for the new one in one transaction, so a
// reader sees the old list or the new one, never a partial one.
func (r *Refresher) replace(ctx context.Context, c claimToken, ids []listedID, etag string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock the claim row: a server taking over after this one's lease ran
	// out waits, then finds its claim replaced by this one's result.
	var one int
	err = tx.QueryRow(ctx, `SELECT 1 FROM anime_ids_refresh WHERE id AND last_attempt_at = $1 FOR UPDATE`, c.at).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return errClaimLost
	}
	if err != nil {
		return fmt.Errorf("replace anime list: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM anime_ids`); err != nil {
		return fmt.Errorf("replace anime list: %w", err)
	}
	rows := make([][]any, len(ids))
	for i, id := range ids {
		rows[i] = []any{id.source, id.id}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"anime_ids"}, []string{"source", "external_id"}, pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("replace anime list: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE anime_ids_refresh SET last_status = 'ok', last_error = '', etag = $1,
			entry_count = $2, refreshed_at = clock_timestamp()
		WHERE id`, etag, len(ids)); err != nil {
		return fmt.Errorf("record anime list refresh: %w", err)
	}
	return tx.Commit(ctx)
}

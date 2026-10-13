package watchlist

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/idgen"
)

// titlesRepo holds the SQL for watchlist_titles, watchlist_title_aliases and
// user_watchlist_titles.
//
// Lock order: every write that can delete a title row, or attach an entry to
// one, first locks that title row. An add locks it with SELECT … FOR UPDATE
// (or creates it), a remove or promotion locks it before deleting the entry and
// then deletes the title only if no entry remains, and a merge locks both
// titles in id order. So an add never attaches to a title a concurrent remove
// is deleting. The orphan check runs as its own statement after the lock, so
// it reads a snapshot taken after any add it waited on had committed.
type titlesRepo struct {
	pool *pgxpool.Pool
}

// maxAddAttempts bounds the retries of an add that lost a race to create the
// same title.
const maxAddAttempts = 3

const titleColumns = `t.id, t.media_type, t.tmdb_id, t.imdb_id, coalesce(t.tvdb_id, 0), t.title, coalesce(t.year, 0),
	t.release_date, t.poster_path, t.certification, t.vote_average::float8, t.state, t.not_found_count,
	t.last_not_found_at, t.checked_at, t.next_check_at, t.created_at, t.updated_at,
	coalesce((SELECT array_agg(fa.provider_id::int ORDER BY fa.provider_id::int)
	          FROM watchlist_title_aliases fa
	          WHERE fa.title_id = t.id AND fa.provider = 'tmdb' AND fa.provider_id <> t.tmdb_id::text), '{}')`

func scanTitle(row pgx.Row, extra ...any) (Title, error) {
	var t Title
	var state string
	dest := append([]any{
		&t.ID, &t.MediaType, &t.TMDBID, &t.IMDbID, &t.TVDBID, &t.Title, &t.Year,
		&t.ReleaseDate, &t.PosterPath, &t.Certification, &t.VoteAverage, &state, &t.NotFoundCount,
		&t.LastNotFoundAt, &t.CheckedAt, &t.NextCheckAt, &t.CreatedAt, &t.UpdatedAt, &t.FormerTMDBIDs,
	}, extra...)
	if err := row.Scan(dest...); err != nil {
		return Title{}, err
	}
	t.State = TitleState(state)
	return t, nil
}

// titleByTMDB returns the title holding the TMDB ID as a current or former
// alias, or nil. A title no entry references is left for the orphan sweep and
// not returned: nothing keeps its snapshot current.
func (r *titlesRepo) titleByTMDB(ctx context.Context, mediaType string, tmdbID int) (*Title, error) {
	t, err := scanTitle(r.pool.QueryRow(ctx, `
		SELECT `+titleColumns+`
		FROM watchlist_title_aliases a
		JOIN watchlist_titles t ON t.id = a.title_id
		WHERE a.media_type = $1 AND a.provider = 'tmdb' AND a.provider_id = $2
		  AND EXISTS (SELECT 1 FROM user_watchlist_titles e WHERE e.title_id = t.id)`,
		mediaType, strconv.Itoa(tmdbID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading watchlist title by tmdb id: %w", err)
	}
	return &t, nil
}

func (r *titlesRepo) titleByID(ctx context.Context, id int64) (*Title, error) {
	t, err := scanTitle(r.pool.QueryRow(ctx, `SELECT `+titleColumns+` FROM watchlist_titles t WHERE t.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading watchlist title: %w", err)
	}
	return &t, nil
}

// lockTitleByTMDB locks the title holding the TMDB alias, or reports that no
// title holds it.
//
// A merge that deletes the title this waited on moves its aliases to the
// survivor, but the waiting row lock does not follow them: it comes back
// empty. So an empty result is trusted only once a new statement, with a new
// snapshot, finds no alias either; otherwise the lock is retried against the
// alias's current title.
func lockTitleByTMDB(ctx context.Context, tx pgx.Tx, mediaType string, tmdbID int) (int64, bool, error) {
	alias := strconv.Itoa(tmdbID)
	for attempt := 0; attempt < maxAddAttempts; attempt++ {
		var id int64
		err := tx.QueryRow(ctx, `
			SELECT t.id
			FROM watchlist_title_aliases a
			JOIN watchlist_titles t ON t.id = a.title_id
			WHERE a.media_type = $1 AND a.provider = 'tmdb' AND a.provider_id = $2
			FOR UPDATE OF t`, mediaType, alias).Scan(&id)
		if err == nil {
			return id, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, false, fmt.Errorf("locking watchlist title: %w", err)
		}
		var held bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM watchlist_title_aliases
				WHERE media_type = $1 AND provider = 'tmdb' AND provider_id = $2)`, mediaType, alias).Scan(&held); err != nil {
			return 0, false, fmt.Errorf("checking watchlist title alias: %w", err)
		}
		if !held {
			return 0, false, nil
		}
	}
	return 0, false, fmt.Errorf("locking watchlist title: its tmdb alias kept moving")
}

func lockTitleByID(ctx context.Context, tx pgx.Tx, id int64) (bool, error) {
	var locked int64
	err := tx.QueryRow(ctx, `SELECT id FROM watchlist_titles WHERE id = $1 FOR UPDATE`, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("locking watchlist title: %w", err)
	}
	return true, nil
}

// insertAliases records a title's current provider IDs as aliases. An ID that
// already belongs to another title is skipped: observing a live title never
// merges two titles.
func insertAliases(ctx context.Context, tx pgx.Tx, titleID int64, mediaType string, tmdbID int, imdbID string, tvdbID int) error {
	providers := []string{contentid.ProviderTMDB}
	values := []string{strconv.Itoa(tmdbID)}
	if imdbID != "" {
		providers = append(providers, contentid.ProviderIMDB)
		values = append(values, imdbID)
	}
	if tvdbID > 0 {
		providers = append(providers, contentid.ProviderTVDB)
		values = append(values, strconv.Itoa(tvdbID))
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO watchlist_title_aliases (title_id, media_type, provider, provider_id)
		SELECT $1, $2, p.provider, p.provider_id
		FROM unnest($3::text[], $4::text[]) AS p(provider, provider_id)
		ON CONFLICT DO NOTHING`, titleID, mediaType, providers, values); err != nil {
		return fmt.Errorf("recording watchlist title aliases: %w", err)
	}
	return keepOwnedIDsLocked(ctx, tx, titleID)
}

// keepOwnedIDsLocked keeps the title's IMDb and TVDB fields to IDs it owns as
// aliases. An ID another title already holds was skipped as an alias; left in
// the field, library matching and request presence would read it and could
// match the other title's copy. The field falls back to an ID of that
// provider the title does own (the previous one, since aliases accumulate),
// else it is emptied.
func keepOwnedIDsLocked(ctx context.Context, tx pgx.Tx, titleID int64) error {
	if _, err := tx.Exec(ctx, `
		UPDATE watchlist_titles t
		SET imdb_id = CASE
		        WHEN t.imdb_id = '' OR EXISTS (
		            SELECT 1 FROM watchlist_title_aliases a
		            WHERE a.title_id = t.id AND a.provider = 'imdb' AND a.provider_id = t.imdb_id)
		        THEN t.imdb_id
		        ELSE coalesce((
		            SELECT a.provider_id FROM watchlist_title_aliases a
		            WHERE a.title_id = t.id AND a.provider = 'imdb'
		            ORDER BY a.provider_id LIMIT 1), '')
		    END,
		    tvdb_id = CASE
		        WHEN t.tvdb_id IS NULL OR EXISTS (
		            SELECT 1 FROM watchlist_title_aliases a
		            WHERE a.title_id = t.id AND a.provider = 'tvdb' AND a.provider_id = t.tvdb_id::text)
		        THEN t.tvdb_id
		        ELSE (
		            SELECT a.provider_id::int FROM watchlist_title_aliases a
		            WHERE a.title_id = t.id AND a.provider = 'tvdb'
		            ORDER BY a.provider_id LIMIT 1)
		    END
		WHERE t.id = $1`, titleID); err != nil {
		return fmt.Errorf("keeping watchlist title ids to its aliases: %w", err)
	}
	return nil
}

// add attaches an entry for the snapshot's title to the profile, creating the
// title when no title holds its TMDB ID. An existing entry keeps its added_at;
// inserted reports whether this call created the entry.
func (r *titlesRepo) add(ctx context.Context, userID int, profileID string, snap Snapshot, addedAt, now time.Time) (Entry, bool, error) {
	for attempt := 0; ; attempt++ {
		entry, inserted, raced, err := r.tryAdd(ctx, userID, profileID, snap, addedAt, now)
		if err != nil {
			return Entry{}, false, err
		}
		if !raced {
			return entry, inserted, nil
		}
		if attempt+1 >= maxAddAttempts {
			return Entry{}, false, fmt.Errorf("adding watchlist title: lost the race to create it %d times", maxAddAttempts)
		}
	}
}

func (r *titlesRepo) tryAdd(ctx context.Context, userID int, profileID string, snap Snapshot, addedAt, now time.Time) (entry Entry, inserted, raced bool, err error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Entry{}, false, false, fmt.Errorf("beginning watchlist title add: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	titleID, found, err := lockTitleByTMDB(ctx, tx, snap.MediaType, snap.TMDBID)
	if err != nil {
		return Entry{}, false, false, err
	}
	if found {
		// A title no entry references waits for the orphan sweep and nothing
		// kept its snapshot current; take the caller's instead.
		var orphan bool
		if err := tx.QueryRow(ctx, `
			SELECT NOT EXISTS (SELECT 1 FROM user_watchlist_titles WHERE title_id = $1)`, titleID).Scan(&orphan); err != nil {
			return Entry{}, false, false, fmt.Errorf("checking watchlist title entries: %w", err)
		}
		if orphan {
			// Held through a former TMDB ID, the snapshot doesn't apply; the
			// next list read checks the title instead.
			if _, err := tx.Exec(ctx, `
				UPDATE watchlist_titles SET next_check_at = least(next_check_at, $2) WHERE id = $1`, titleID, now); err != nil {
				return Entry{}, false, false, fmt.Errorf("scheduling watchlist title check: %w", err)
			}
			if err := refreshLocked(ctx, tx, titleID, snap, now); err != nil {
				return Entry{}, false, false, err
			}
		}
	}
	if !found {
		idText, err := idgen.NextID()
		if err != nil {
			return Entry{}, false, false, fmt.Errorf("minting watchlist title id: %w", err)
		}
		titleID, err = strconv.ParseInt(idText, 10, 64)
		if err != nil {
			return Entry{}, false, false, fmt.Errorf("parsing watchlist title id: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO watchlist_titles (id, media_type, tmdb_id, imdb_id, tvdb_id, title, year, release_date,
				poster_path, certification, vote_average, checked_at, next_check_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, nullif($5, 0), $6, nullif($7, 0), $8, $9, $10, round($11::numeric, 1), $12, $13, $12, $12)`,
			titleID, snap.MediaType, snap.TMDBID, snap.IMDbID, snap.TVDBID, snap.Title, snap.Year, snap.ReleaseDate,
			snap.PosterPath, snap.Certification, snap.VoteAverage, now, nextCheckAfterSuccess(now, snap.ReleaseDate)); err != nil {
			return Entry{}, false, false, fmt.Errorf("creating watchlist title: %w", err)
		}
		// The TMDB alias is the title's identity. If another add created the
		// title first, start over and attach to that one.
		tag, err := tx.Exec(ctx, `
			INSERT INTO watchlist_title_aliases (title_id, media_type, provider, provider_id)
			VALUES ($1, $2, 'tmdb', $3)
			ON CONFLICT DO NOTHING`, titleID, snap.MediaType, strconv.Itoa(snap.TMDBID))
		if err != nil {
			return Entry{}, false, false, fmt.Errorf("recording watchlist title tmdb alias: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return Entry{}, false, true, nil
		}
		if err := insertAliases(ctx, tx, titleID, snap.MediaType, snap.TMDBID, snap.IMDbID, snap.TVDBID); err != nil {
			return Entry{}, false, false, err
		}
	}

	var entryAddedAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO user_watchlist_titles (user_id, profile_id, title_id, added_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, profile_id, title_id) DO NOTHING
		RETURNING added_at`, userID, profileID, titleID, addedAt).Scan(&entryAddedAt)
	switch {
	case err == nil:
		inserted = true
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(ctx, `
			SELECT added_at FROM user_watchlist_titles
			WHERE user_id = $1 AND profile_id = $2 AND title_id = $3`, userID, profileID, titleID).Scan(&entryAddedAt); err != nil {
			return Entry{}, false, false, fmt.Errorf("reading watchlist title entry: %w", err)
		}
	default:
		return Entry{}, false, false, fmt.Errorf("adding watchlist title entry: %w", err)
	}
	title, err := scanTitle(tx.QueryRow(ctx, `SELECT `+titleColumns+` FROM watchlist_titles t WHERE t.id = $1`, titleID))
	if err != nil {
		return Entry{}, false, false, fmt.Errorf("reading watchlist title: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Entry{}, false, false, fmt.Errorf("committing watchlist title add: %w", err)
	}
	return Entry{Title: title, AddedAt: entryAddedAt}, inserted, false, nil
}

// removeByTMDB removes the profile's entry for the title holding the TMDB ID
// (current or former), deleting the title when no entry remains. It returns
// the title as it was, or nil when no title holds the ID.
func (r *titlesRepo) removeByTMDB(ctx context.Context, userID int, profileID, mediaType string, tmdbID int) (*Title, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("beginning watchlist title remove: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	titleID, found, err := lockTitleByTMDB(ctx, tx, mediaType, tmdbID)
	if err != nil || !found {
		return nil, false, err
	}
	title, err := scanTitle(tx.QueryRow(ctx, `SELECT `+titleColumns+` FROM watchlist_titles t WHERE t.id = $1`, titleID))
	if err != nil {
		return nil, false, fmt.Errorf("reading watchlist title: %w", err)
	}
	removed, err := deleteEntryLocked(ctx, tx, userID, profileID, titleID)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("committing watchlist title remove: %w", err)
	}
	return &title, removed, nil
}

// promoteEntry moves the profile's entry on one title to the library
// watchlist. With the title locked, and only while the entry still exists, it
// runs add (the library watchlist write), then deletes the entry and the title
// if it is orphaned. A remove of the same title waits on the lock, so it either
// runs first and nothing is added, or runs after the library entry exists and
// can take it off. moved is true only for the caller whose DELETE removed the
// entry, which is what makes promotion side effects fire once.
//
// add writes through another connection. A node that dies after it rolls the
// delete back, so the entry stays and the next read finishes the move.
func (r *titlesRepo) promoteEntry(ctx context.Context, userID int, profileID string, titleID int64, add func(context.Context) error) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("beginning watchlist title promotion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	locked, err := lockTitleByID(ctx, tx, titleID)
	if err != nil || !locked {
		return false, err
	}
	var present bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM user_watchlist_titles
			WHERE user_id = $1 AND profile_id = $2 AND title_id = $3)`, userID, profileID, titleID).Scan(&present); err != nil {
		return false, fmt.Errorf("reading promoted watchlist title entry: %w", err)
	}
	if !present {
		return false, nil
	}
	if err := add(ctx); err != nil {
		return false, err
	}
	removed, err := deleteEntryLocked(ctx, tx, userID, profileID, titleID)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing watchlist title promotion: %w", err)
	}
	return removed, nil
}

// entryExists reports whether the profile still has an entry on the title.
func (r *titlesRepo) entryExists(ctx context.Context, userID int, profileID string, titleID int64) (bool, error) {
	var present bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM user_watchlist_titles
			WHERE user_id = $1 AND profile_id = $2 AND title_id = $3)`, userID, profileID, titleID).Scan(&present); err != nil {
		return false, fmt.Errorf("reading watchlist title entry: %w", err)
	}
	return present, nil
}

// deleteEntryLocked runs with the title row already locked.
func deleteEntryLocked(ctx context.Context, tx pgx.Tx, userID int, profileID string, titleID int64) (bool, error) {
	tag, err := tx.Exec(ctx, `
		DELETE FROM user_watchlist_titles
		WHERE user_id = $1 AND profile_id = $2 AND title_id = $3`, userID, profileID, titleID)
	if err != nil {
		return false, fmt.Errorf("deleting watchlist title entry: %w", err)
	}
	if err := deleteOrphanLocked(ctx, tx, titleID); err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func deleteOrphanLocked(ctx context.Context, tx pgx.Tx, titleID int64) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM watchlist_titles t
		WHERE t.id = $1
		  AND NOT EXISTS (SELECT 1 FROM user_watchlist_titles e WHERE e.title_id = t.id)`, titleID); err != nil {
		return fmt.Errorf("deleting orphaned watchlist title: %w", err)
	}
	return nil
}

// purgeProfile removes every entry of a deleted profile and the titles left
// without entries, locking the titles in id order first.
func (r *titlesRepo) purgeProfile(ctx context.Context, userID int, profileID string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning watchlist title purge: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT t.id FROM watchlist_titles t
		WHERE t.id IN (SELECT title_id FROM user_watchlist_titles WHERE user_id = $1 AND profile_id = $2)
		ORDER BY t.id
		FOR UPDATE`, userID, profileID)
	if err != nil {
		return fmt.Errorf("locking purged watchlist titles: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("locking purged watchlist titles: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_watchlist_titles WHERE user_id = $1 AND profile_id = $2`, userID, profileID); err != nil {
		return fmt.Errorf("purging watchlist title entries: %w", err)
	}
	if len(ids) > 0 {
		if _, err := tx.Exec(ctx, `
			DELETE FROM watchlist_titles t
			WHERE t.id = ANY($1)
			  AND NOT EXISTS (SELECT 1 FROM user_watchlist_titles e WHERE e.title_id = t.id)`, ids); err != nil {
			return fmt.Errorf("deleting orphaned watchlist titles: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing watchlist title purge: %w", err)
	}
	return r.sweepOrphans(ctx)
}

// orphanSweepLimit bounds one sweep; the next profile or user delete
// continues it.
const orphanSweepLimit = 1000

// sweepOrphans deletes titles no entry references. Entries deleted without
// the title lock leave these behind: the Postgres user store's profile
// delete, and user deletion through the users foreign key. Reads skip such
// titles until then, and an add that attaches to one refreshes its snapshot. Titles an add
// holds are skipped, and the orphan check runs again after the locks are
// taken, so an entry committed meanwhile keeps its title.
func (r *titlesRepo) sweepOrphans(ctx context.Context) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning watchlist title sweep: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT t.id FROM watchlist_titles t
		WHERE NOT EXISTS (SELECT 1 FROM user_watchlist_titles e WHERE e.title_id = t.id)
		ORDER BY t.id
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, orphanSweepLimit)
	if err != nil {
		return fmt.Errorf("locking orphaned watchlist titles: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("locking orphaned watchlist titles: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM watchlist_titles t
		WHERE t.id = ANY($1)
		  AND NOT EXISTS (SELECT 1 FROM user_watchlist_titles e WHERE e.title_id = t.id)`, ids); err != nil {
		return fmt.Errorf("deleting orphaned watchlist titles: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing watchlist title sweep: %w", err)
	}
	return nil
}

// listPage returns the profile's entries newest first, after the keyset
// position when one is given.
func (r *titlesRepo) listPage(ctx context.Context, userID int, profileID string, after *PageKey, limit int) ([]Entry, error) {
	var afterAt *time.Time
	var afterID int64
	if after != nil {
		afterAt, afterID = &after.AddedAt, after.TitleID
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+titleColumns+`, e.added_at
		FROM user_watchlist_titles e
		JOIN watchlist_titles t ON t.id = e.title_id
		WHERE e.user_id = $1 AND e.profile_id = $2
		  AND ($3::timestamptz IS NULL OR (e.added_at, e.title_id) < ($3::timestamptz, $4::bigint))
		ORDER BY e.added_at DESC, e.title_id DESC
		LIMIT $5`, userID, profileID, afterAt, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing watchlist titles: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		title, err := scanTitle(rows, &e.AddedAt)
		if err != nil {
			return nil, fmt.Errorf("scanning watchlist title: %w", err)
		}
		e.Title = title
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing watchlist titles: %w", err)
	}
	return out, nil
}

// onWatchlist reports which of the keys the profile has an entry for, through
// any TMDB ID the entry's title has held.
func (r *titlesRepo) onWatchlist(ctx context.Context, userID int, profileID string, keys []TitleKey) (map[TitleKey]bool, error) {
	out := make(map[TitleKey]bool)
	if len(keys) == 0 {
		return out, nil
	}
	mediaTypes := make([]string, len(keys))
	tmdbIDs := make([]string, len(keys))
	for i, k := range keys {
		mediaTypes[i], tmdbIDs[i] = k.MediaType, strconv.Itoa(k.TMDBID)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT a.media_type, a.provider_id
		FROM unnest($3::text[], $4::text[]) AS k(media_type, tmdb_id)
		JOIN watchlist_title_aliases a
		  ON a.media_type = k.media_type AND a.provider = 'tmdb' AND a.provider_id = k.tmdb_id
		JOIN user_watchlist_titles e
		  ON e.title_id = a.title_id AND e.user_id = $1 AND e.profile_id = $2`,
		userID, profileID, mediaTypes, tmdbIDs)
	if err != nil {
		return nil, fmt.Errorf("checking watchlist titles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mediaType, tmdbID string
		if err := rows.Scan(&mediaType, &tmdbID); err != nil {
			return nil, fmt.Errorf("scanning watchlist title check: %w", err)
		}
		id, err := strconv.Atoi(tmdbID)
		if err != nil {
			continue
		}
		out[TitleKey{MediaType: mediaType, TMDBID: id}] = true
	}
	return out, rows.Err()
}

// titleAlias is one alias row.
type titleAlias struct {
	TitleID    int64
	MediaType  string
	Provider   string
	ProviderID string
}

func collectAliases(rows pgx.Rows, err error) ([]titleAlias, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []titleAlias
	for rows.Next() {
		var a titleAlias
		if err := rows.Scan(&a.TitleID, &a.MediaType, &a.Provider, &a.ProviderID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// profileEntries returns every entry of the profile with its title's aliases.
// A profile with no entries costs one index probe.
func (r *titlesRepo) profileEntries(ctx context.Context, userID int, profileID string) (map[int64]time.Time, []titleAlias, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT e.title_id, e.added_at, a.media_type, a.provider, a.provider_id
		FROM user_watchlist_titles e
		JOIN watchlist_title_aliases a ON a.title_id = e.title_id
		WHERE e.user_id = $1 AND e.profile_id = $2`, userID, profileID)
	if err != nil {
		return nil, nil, fmt.Errorf("reading watchlist title entries: %w", err)
	}
	defer rows.Close()
	addedAt := make(map[int64]time.Time)
	var aliases []titleAlias
	for rows.Next() {
		var a titleAlias
		var at time.Time
		if err := rows.Scan(&a.TitleID, &at, &a.MediaType, &a.Provider, &a.ProviderID); err != nil {
			return nil, nil, fmt.Errorf("scanning watchlist title entry: %w", err)
		}
		addedAt[a.TitleID] = at
		aliases = append(aliases, a)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("reading watchlist title entries: %w", err)
	}
	return addedAt, aliases, nil
}

// profileEntriesMatching returns the profile's entries whose title holds any
// of the given provider IDs, with all of those titles' aliases.
func (r *titlesRepo) profileEntriesMatching(ctx context.Context, userID int, profileID, mediaType string, providers, providerIDs []string) (map[int64]time.Time, []titleAlias, error) {
	rows, err := r.pool.Query(ctx, `
		WITH matched AS (
			SELECT DISTINCT e.title_id, e.added_at
			FROM unnest($4::text[], $5::text[]) AS v(provider, provider_id)
			JOIN watchlist_title_aliases a
			  ON a.media_type = $3 AND a.provider = v.provider AND a.provider_id = v.provider_id
			JOIN user_watchlist_titles e
			  ON e.title_id = a.title_id AND e.user_id = $1 AND e.profile_id = $2
		)
		SELECT m.title_id, m.added_at, a.media_type, a.provider, a.provider_id
		FROM matched m
		JOIN watchlist_title_aliases a ON a.title_id = m.title_id`,
		userID, profileID, mediaType, providers, providerIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("matching watchlist title entries: %w", err)
	}
	defer rows.Close()
	addedAt := make(map[int64]time.Time)
	var aliases []titleAlias
	for rows.Next() {
		var a titleAlias
		var at time.Time
		if err := rows.Scan(&a.TitleID, &at, &a.MediaType, &a.Provider, &a.ProviderID); err != nil {
			return nil, nil, fmt.Errorf("scanning watchlist title entry: %w", err)
		}
		addedAt[a.TitleID] = at
		aliases = append(aliases, a)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("matching watchlist title entries: %w", err)
	}
	return addedAt, aliases, nil
}

func (r *titlesRepo) aliasesOf(ctx context.Context, titleID int64) ([]titleAlias, error) {
	aliases, err := collectAliases(r.pool.Query(ctx, `
		SELECT title_id, media_type, provider, provider_id
		FROM watchlist_title_aliases
		WHERE title_id = $1
		ORDER BY provider, provider_id`, titleID))
	if err != nil {
		return nil, fmt.Errorf("reading watchlist title aliases: %w", err)
	}
	return aliases, nil
}

// aliasOwner returns the title holding a provider ID, or 0.
func (r *titlesRepo) aliasOwner(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, mediaType, provider, providerID string) (int64, error) {
	var owner int64
	err := q.QueryRow(ctx, `
		SELECT title_id FROM watchlist_title_aliases
		WHERE media_type = $1 AND provider = $2 AND provider_id = $3`, mediaType, provider, providerID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading watchlist title alias owner: %w", err)
	}
	return owner, nil
}

// claimDue claims overdue titles for a check by pushing next_check_at out by
// the claim lease. Only one caller wins each title; a claim whose checker
// died expires with the lease and a later read retries.
func (r *titlesRepo) claimDue(ctx context.Context, ids []int64, now time.Time, lease time.Duration) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		UPDATE watchlist_titles
		SET next_check_at = $2::timestamptz + $3::interval
		WHERE id = ANY($1) AND next_check_at <= $2::timestamptz
		RETURNING id`, ids, now, lease)
	if err != nil {
		return nil, fmt.Errorf("claiming watchlist title checks: %w", err)
	}
	claimed, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("claiming watchlist title checks: %w", err)
	}
	return claimed, nil
}

// applyDetail stores a successful check or observation: the current IDs, the
// display snapshot, an active state and the next check time. New IDs become
// aliases unless another title holds them.
func (r *titlesRepo) applyDetail(ctx context.Context, titleID int64, snap Snapshot, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning watchlist title refresh: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := refreshLocked(ctx, tx, titleID, snap, now); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing watchlist title refresh: %w", err)
	}
	return nil
}

// refreshLocked stores a TMDB detail's snapshot on the title if it still has
// the snapshot's TMDB ID, and records its new IDs as aliases.
func refreshLocked(ctx context.Context, tx pgx.Tx, titleID int64, snap Snapshot, now time.Time) error {
	var mediaType string
	err := tx.QueryRow(ctx, `
		UPDATE watchlist_titles
		SET imdb_id = $2, tvdb_id = nullif($3, 0), title = $4, year = nullif($5, 0), release_date = $6,
		    poster_path = $7, certification = $8, vote_average = round($9::numeric, 1),
		    state = 'active', not_found_count = 0, last_not_found_at = NULL,
		    checked_at = $10, next_check_at = $11, updated_at = $10
		WHERE id = $1 AND tmdb_id = $12
		RETURNING media_type`,
		titleID, snap.IMDbID, snap.TVDBID, snap.Title, snap.Year, snap.ReleaseDate, snap.PosterPath,
		snap.Certification, snap.VoteAverage, now, nextCheckAfterSuccess(now, snap.ReleaseDate), snap.TMDBID).Scan(&mediaType)
	if errors.Is(err, pgx.ErrNoRows) {
		// The title was repointed or merged away since it was read.
		return nil
	}
	if err != nil {
		return fmt.Errorf("refreshing watchlist title: %w", err)
	}
	return insertAliases(ctx, tx, titleID, mediaType, snap.TMDBID, snap.IMDbID, snap.TVDBID)
}

// recordNotFound stores a TMDB 404 for the title's current ID.
func (r *titlesRepo) recordNotFound(ctx context.Context, titleID int64, tmdbID int, count int, at, nextCheck time.Time) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE watchlist_titles
		SET not_found_count = $3, last_not_found_at = $4, next_check_at = $5, updated_at = $4
		WHERE id = $1 AND tmdb_id = $2`, titleID, tmdbID, count, at, nextCheck); err != nil {
		return fmt.Errorf("recording watchlist title not found: %w", err)
	}
	return nil
}

// setState records a recovery outcome that needs the user (needs_review) or
// found nothing (removed), with the confirmed 404.
func (r *titlesRepo) setState(ctx context.Context, titleID int64, tmdbID int, state TitleState, notFoundCount int, now, nextCheck time.Time) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE watchlist_titles
		SET state = $3, not_found_count = $4, last_not_found_at = $5, checked_at = $5, next_check_at = $6, updated_at = $5
		WHERE id = $1 AND tmdb_id = $2`, titleID, tmdbID, string(state), notFoundCount, now, nextCheck); err != nil {
		return fmt.Errorf("setting watchlist title state: %w", err)
	}
	return nil
}

// backoff pushes the next check out after a failure that says nothing about
// the title (TMDB unreachable), leaving its state alone.
func (r *titlesRepo) backoff(ctx context.Context, titleID int64, nextCheck time.Time) error {
	if _, err := r.pool.Exec(ctx, `UPDATE watchlist_titles SET next_check_at = $2 WHERE id = $1`, titleID, nextCheck); err != nil {
		return fmt.Errorf("backing off watchlist title check: %w", err)
	}
	return nil
}

// repoint moves a title whose TMDB ID was deleted to the ID recovery found,
// keeping the old ID as an alias. When another title already holds the new ID
// the dead title is merged into it instead. It returns the surviving title.
func (r *titlesRepo) repoint(ctx context.Context, titleID int64, oldTMDBID, newTMDBID int, now time.Time) (int64, error) {
	title, err := r.titleByID(ctx, titleID)
	if err != nil || title == nil {
		return 0, err
	}
	newAlias := strconv.Itoa(newTMDBID)
	for attempt := 0; attempt < maxAddAttempts; attempt++ {
		owner, err := r.aliasOwner(ctx, r.pool, title.MediaType, contentid.ProviderTMDB, newAlias)
		if err != nil {
			return 0, err
		}
		if owner != 0 && owner != titleID {
			merged, retry, err := r.merge(ctx, titleID, owner, title.MediaType, newAlias)
			if err != nil || !retry {
				return merged, err
			}
			continue
		}
		done, err := r.tryRepoint(ctx, titleID, title.MediaType, oldTMDBID, newTMDBID, now)
		if err != nil {
			return 0, err
		}
		if done {
			return titleID, nil
		}
	}
	return 0, fmt.Errorf("repointing watchlist title: alias kept changing")
}

func (r *titlesRepo) tryRepoint(ctx context.Context, titleID int64, mediaType string, oldTMDBID, newTMDBID int, now time.Time) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("beginning watchlist title repoint: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if locked, err := lockTitleByID(ctx, tx, titleID); err != nil || !locked {
		return true, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO watchlist_title_aliases (title_id, media_type, provider, provider_id)
		VALUES ($1, $2, 'tmdb', $3)
		ON CONFLICT (media_type, provider, provider_id) DO UPDATE SET title_id = watchlist_title_aliases.title_id
		WHERE watchlist_title_aliases.title_id = $1`, titleID, mediaType, strconv.Itoa(newTMDBID))
	if err != nil {
		return false, fmt.Errorf("recording repointed tmdb alias: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Another title took the ID meanwhile; the caller merges.
		return false, nil
	}
	// The next check refreshes the snapshot from the new ID right away.
	if _, err := tx.Exec(ctx, `
		UPDATE watchlist_titles
		SET tmdb_id = $3, state = 'active', not_found_count = 0, last_not_found_at = NULL,
		    next_check_at = $4, updated_at = $4
		WHERE id = $1 AND tmdb_id = $2`, titleID, oldTMDBID, newTMDBID, now); err != nil {
		return false, fmt.Errorf("repointing watchlist title: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing watchlist title repoint: %w", err)
	}
	return true, nil
}

// merge folds the loser title into the survivor: entries move keeping the
// earliest added_at, aliases move, and the loser is deleted. Both rows are
// locked in id order. retry is true when the alias that justified the merge
// no longer belongs to the survivor.
func (r *titlesRepo) merge(ctx context.Context, loser, survivor int64, mediaType, sharedTMDBAlias string) (int64, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("beginning watchlist title merge: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT id FROM watchlist_titles WHERE id = ANY($1) ORDER BY id FOR UPDATE`, []int64{loser, survivor})
	if err != nil {
		return 0, false, fmt.Errorf("locking merged watchlist titles: %w", err)
	}
	locked, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, false, fmt.Errorf("locking merged watchlist titles: %w", err)
	}
	if len(locked) != 2 {
		// One side is gone. A vanished loser needs nothing more; a vanished
		// survivor means the alias is free again.
		return survivor, slicesContains(locked, loser), nil
	}
	owner, err := r.aliasOwner(ctx, tx, mediaType, contentid.ProviderTMDB, sharedTMDBAlias)
	if err != nil {
		return 0, false, err
	}
	if owner != survivor {
		return 0, true, nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_watchlist_titles (user_id, profile_id, title_id, added_at)
		SELECT user_id, profile_id, $2, added_at FROM user_watchlist_titles WHERE title_id = $1
		ON CONFLICT (user_id, profile_id, title_id)
		DO UPDATE SET added_at = LEAST(user_watchlist_titles.added_at, EXCLUDED.added_at)`, loser, survivor); err != nil {
		return 0, false, fmt.Errorf("moving merged watchlist title entries: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE watchlist_title_aliases SET title_id = $2 WHERE title_id = $1`, loser, survivor); err != nil {
		return 0, false, fmt.Errorf("moving merged watchlist title aliases: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM watchlist_titles WHERE id = $1`, loser); err != nil {
		return 0, false, fmt.Errorf("deleting merged watchlist title: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("committing watchlist title merge: %w", err)
	}
	return survivor, false, nil
}

func slicesContains(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

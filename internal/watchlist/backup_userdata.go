package watchlist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// BackupData is a profile's favorites and watchlist as backups and profile
// exports carry them (userdata.Favorites): favorite and watchlisted library
// items, and watchlisted titles the library does not have, by their TMDB,
// IMDb, and TVDB ids. Entries are added where the profile lacks them;
// restored library watchlist entries sort as locally added ones do. A title
// is attached the way the watchlist adds one: to the title here holding its
// TMDB id, current or former, or a new title from the carried snapshot,
// which the watchlist's checks refresh from TMDB as for any other title.
type BackupData struct{}

func (BackupData) ID() string { return userdata.Favorites }
func (BackupData) Tables() []string {
	return []string{"user_favorites", "user_watchlist", "user_watchlist_titles"}
}

type favoritesDocument struct {
	Favorites []keyedEntry   `json:"favorites"`
	Watchlist []keyedEntry   `json:"watchlist"`
	Titles    []watchedTitle `json:"titles"`
}

// keyedEntry is a library item a profile marked at a time.
type keyedEntry struct {
	Item string    `json:"item"`
	At   time.Time `json:"at"`
}

// watchedTitle is a watchlisted title outside the library, by its portable
// provider ids, with the display snapshot it was stored with.
type watchedTitle struct {
	MediaType     string     `json:"media_type"`
	TMDBID        int        `json:"tmdb_id"`
	IMDbID        string     `json:"imdb_id"`
	TVDBID        int        `json:"tvdb_id"`
	Title         string     `json:"title"`
	Year          int        `json:"year"`
	ReleaseDate   *time.Time `json:"release_date"`
	PosterPath    string     `json:"poster_path"`
	Certification string     `json:"certification"`
	VoteAverage   *float64   `json:"vote_average"`
	AddedAt       time.Time  `json:"added_at"`
}

func (w watchedTitle) snapshot() Snapshot {
	return Snapshot{MediaType: w.MediaType, TMDBID: w.TMDBID, IMDbID: w.IMDbID, TVDBID: w.TVDBID, Title: w.Title, Year: w.Year,
		ReleaseDate: w.ReleaseDate, PosterPath: w.PosterPath, Certification: w.Certification, VoteAverage: w.VoteAverage}
}

func exportKeyed(ctx context.Context, tx pgx.Tx, sql string, owner userdata.Owner, keep userdata.Keep) ([]keyedEntry, error) {
	rows, err := tx.Query(ctx, sql, owner.UserID, owner.ProfileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []keyedEntry{}
	for rows.Next() {
		var e keyedEntry
		if err := rows.Scan(&e.Item, &e.At); err != nil {
			return nil, err
		}
		if !keep.Has(e.Item) {
			continue
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (BackupData) Export(ctx context.Context, tx pgx.Tx, owner userdata.Owner, keep userdata.Keep) (userdata.Export, error) {
	favorites, err := exportKeyed(ctx, tx, `SELECT media_item_id, added_at FROM user_favorites WHERE user_id = $1 AND profile_id = $2 ORDER BY added_at, media_item_id`, owner, keep)
	if err != nil {
		return userdata.Export{}, fmt.Errorf("reading favorites: %w", err)
	}
	watchlist, err := exportKeyed(ctx, tx, `SELECT media_item_id, added_at FROM user_watchlist WHERE user_id = $1 AND profile_id = $2 ORDER BY sort_index NULLS LAST, added_at, media_item_id`, owner, keep)
	if err != nil {
		return userdata.Export{}, fmt.Errorf("reading the watchlist: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT t.media_type, t.tmdb_id, t.imdb_id, COALESCE(t.tvdb_id, 0), t.title, COALESCE(t.year, 0), t.release_date, t.poster_path,
			t.certification, t.vote_average::float8, e.added_at
		FROM user_watchlist_titles e JOIN watchlist_titles t ON t.id = e.title_id
		WHERE e.user_id = $1 AND e.profile_id = $2 ORDER BY e.added_at, t.id`, owner.UserID, owner.ProfileID)
	if err != nil {
		return userdata.Export{}, fmt.Errorf("reading watchlist titles: %w", err)
	}
	defer rows.Close()
	titles := []watchedTitle{}
	for rows.Next() {
		var w watchedTitle
		if err := rows.Scan(&w.MediaType, &w.TMDBID, &w.IMDbID, &w.TVDBID, &w.Title, &w.Year, &w.ReleaseDate, &w.PosterPath,
			&w.Certification, &w.VoteAverage, &w.AddedAt); err != nil {
			return userdata.Export{}, err
		}
		// A profile's export leaves out titles its watchlist hides now.
		if !keep.HasTitle(w.Certification) {
			continue
		}
		titles = append(titles, w)
	}
	if err := rows.Err(); err != nil {
		return userdata.Export{}, err
	}
	var items []string
	for _, list := range [][]keyedEntry{favorites, watchlist} {
		for _, e := range list {
			items = append(items, e.Item)
		}
	}
	data, err := json.Marshal(favoritesDocument{Favorites: favorites, Watchlist: watchlist, Titles: titles})
	return userdata.Export{Data: data, Count: len(favorites) + len(watchlist) + len(titles), Items: items}, err
}

func decodeFavorites(data json.RawMessage) (favoritesDocument, userdata.Summary, error) {
	var doc favoritesDocument
	var sum userdata.Summary
	if err := userdata.DecodeStrict(data, &doc); err != nil {
		return doc, sum, err
	}
	if len(doc.Titles) > maxImportedTitles {
		return doc, sum, fmt.Errorf("more than %d watchlist titles", maxImportedTitles)
	}
	sum.Count = len(doc.Favorites) + len(doc.Watchlist) + len(doc.Titles)
	if err := userdata.CheckEntries("favorites", sum.Count); err != nil {
		return doc, sum, err
	}
	for kind, list := range map[string][]keyedEntry{"favorites": doc.Favorites, "the watchlist": doc.Watchlist} {
		seen := userdata.Distinct{}
		for _, e := range list {
			if !userdata.ValidContentID(e.Item) {
				return doc, sum, fmt.Errorf("favorites name an invalid item %q", e.Item)
			}
			if err := seen.Add(kind, e.Item); err != nil {
				return doc, sum, err
			}
			sum.Items = append(sum.Items, e.Item)
		}
	}
	titles := userdata.Distinct{}
	for _, w := range doc.Titles {
		if err := titles.Add("watchlist titles", w.MediaType+":"+strconv.Itoa(w.TMDBID)); err != nil {
			return doc, sum, err
		}
		if (w.MediaType != mediaTypeMovie && w.MediaType != mediaTypeSeries) || w.TMDBID <= 0 || w.TVDBID < 0 ||
			!userdata.ValidText(w.Title, 1024) || !userdata.ValidText(w.IMDbID, 64) || !userdata.ValidText(w.PosterPath, 1024) ||
			!userdata.ValidText(w.Certification, 64) {
			return doc, sum, errors.New("a watchlist title is not valid")
		}
	}
	return doc, sum, nil
}

func (BackupData) Validate(data json.RawMessage) (userdata.Summary, error) {
	_, sum, err := decodeFavorites(data)
	return sum, err
}

// Prepare makes every title the documents name exist and locks it, across
// every profile of the import, before any watchlist row is written
// (EnsureTitlesInTx). Promotion holds a title's lock while another
// connection writes the library watchlist entry; an import that wrote that
// entry and then waited on the title would close a cycle across three
// connections that Postgres cannot see.
//
// An untrusted document (a profile's own export) creates and links no
// title, so it locks none (importTitles).
func (BackupData) Prepare(ctx context.Context, tx pgx.Tx, docs []userdata.Prepared) error {
	var snaps []Snapshot
	untrusted := false
	now := time.Now().UTC()
	for _, d := range docs {
		doc, _, err := decodeFavorites(d.Data)
		if err != nil {
			return err
		}
		for _, w := range doc.Titles {
			snaps = append(snaps, w.snapshot())
		}
		untrusted = untrusted || d.Untrusted
	}
	if untrusted {
		return nil
	}
	return EnsureTitlesInTx(ctx, tx, snaps, now)
}

// maxImportedTitles bounds the watchlist titles outside the library one
// document carries: a profile's watchlist holds a few hundred.
const maxImportedTitles = 10_000

func (BackupData) Import(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, _, err := decodeFavorites(data)
	if err != nil {
		return userdata.Result{}, err
	}
	var res userdata.Result
	// Titles first: their row locks come before any watchlist row (Prepare).
	if err := importTitles(ctx, tx, target, doc.Titles, &res); err != nil {
		return res, err
	}
	if err := importKeyed(ctx, tx, target, `INSERT INTO user_favorites (user_id, profile_id, media_item_id, added_at) SELECT $1, $2, u.item, u.at FROM unnest($3::text[], $4::timestamptz[]) AS u(item, at) ON CONFLICT DO NOTHING`, doc.Favorites, &res); err != nil {
		return res, fmt.Errorf("restoring favorites: %w", err)
	}
	if err := importKeyed(ctx, tx, target, `INSERT INTO user_watchlist (user_id, profile_id, media_item_id, added_at) SELECT $1, $2, u.item, u.at FROM unnest($3::text[], $4::timestamptz[]) AS u(item, at) ON CONFLICT DO NOTHING`, doc.Watchlist, &res); err != nil {
		return res, fmt.Errorf("restoring the watchlist: %w", err)
	}
	return res, nil
}

// importTitles restores the watchlist titles that are not in the library.
// A trusted document (a server restore) adds each. An untrusted one (a
// profile's own export) adds none: a title the profile already lists is
// unchanged and every other is left out for the same reason, so the outcome
// depends only on the profile's own watchlist, never on whether another
// account has listed the title.
func importTitles(ctx context.Context, tx pgx.Tx, target userdata.Target, titles []watchedTitle, res *userdata.Result) error {
	missing := 0
	var listed map[string]bool
	if target.Untrusted && len(titles) > 0 {
		// The profile's titles are read once, not once per title.
		var err error
		if listed, err = ProfileTitlesInTx(ctx, tx, target.UserID, target.ProfileID); err != nil {
			return fmt.Errorf("reading the profile's watchlist: %w", err)
		}
	}
	for _, w := range titles {
		if target.Untrusted {
			if listed[w.snapshot().TitleKey()] {
				res.Unchanged++
			} else {
				missing++
			}
			continue
		}
		added, err := AddSnapshotInTx(ctx, tx, target.UserID, target.ProfileID, w.snapshot(), w.AddedAt, target.Now)
		if err != nil {
			return fmt.Errorf("restoring a watchlist title: %w", err)
		}
		if added {
			res.Added++
		} else {
			res.Unchanged++
		}
	}
	if missing > 0 {
		res.LeftOut += missing
		res.Notes = append(res.Notes, fmt.Sprintf("%d watchlist titles that are not in this server's library were left out; add them again from search.", missing))
	}
	return nil
}

// importKeyed inserts, in one statement, each mapped entry the profile
// does not have. sql takes user id, profile id, an array of items, and an
// array of times, and must do nothing on conflict.
func importKeyed(ctx context.Context, tx pgx.Tx, target userdata.Target, sql string, entries []keyedEntry, res *userdata.Result) error {
	var items []string
	var at []time.Time
	for _, e := range entries {
		local, ok, err := target.Items.Resolve(ctx, e.Item)
		if err != nil {
			return err
		}
		if !ok {
			res.LeftOut++
			continue
		}
		when := e.At
		if when.IsZero() {
			when = target.Now
		}
		items, at = append(items, local), append(at, when)
	}
	if len(items) == 0 {
		return nil
	}
	tag, err := tx.Exec(ctx, sql, target.UserID, target.ProfileID, items, at)
	added := int(tag.RowsAffected())
	res.Added += added
	res.Unchanged += len(items) - added
	return err
}

// Pending counts what Import would add: watchlist titles the profile has
// no entry for (by media type and TMDB id), and favorites and watchlist
// items it lacks.
func (BackupData) Pending(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, _, err := decodeFavorites(data)
	if err != nil {
		return userdata.Result{}, err
	}
	var res userdata.Result
	if len(doc.Titles) > 0 {
		seen := map[string]bool{}
		var types, ids []string
		for _, w := range doc.Titles {
			key := w.MediaType + "\x00" + strconv.Itoa(w.TMDBID)
			if seen[key] {
				res.Unchanged++
				continue
			}
			seen[key] = true
			types, ids = append(types, w.MediaType), append(ids, strconv.Itoa(w.TMDBID))
		}
		var missing int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM unnest($3::text[], $4::text[]) AS x(media_type, tmdb)
			WHERE NOT EXISTS (SELECT 1 FROM watchlist_title_aliases a
				JOIN user_watchlist_titles u ON u.title_id = a.title_id AND u.user_id = $1 AND u.profile_id = $2
				WHERE a.media_type = x.media_type AND a.provider = 'tmdb' AND a.provider_id = x.tmdb)`,
			target.UserID, target.ProfileID, types, ids).Scan(&missing); err != nil {
			return res, fmt.Errorf("comparing watchlist titles: %w", err)
		}
		res.Added, res.Unchanged = res.Added+missing, res.Unchanged+len(types)-missing
	}
	for _, list := range []struct {
		table   string
		entries []keyedEntry
	}{{"user_favorites", doc.Favorites}, {"user_watchlist", doc.Watchlist}} {
		ids := make([]string, 0, len(list.entries))
		for _, e := range list.entries {
			ids = append(ids, e.Item)
		}
		if err := userdata.PendingMissing(ctx, tx, target, &res, `
			SELECT x.item FROM unnest($3::text[]) AS x(item)
			WHERE NOT EXISTS (SELECT 1 FROM `+list.table+` f WHERE f.user_id = $1 AND f.profile_id = $2 AND f.media_item_id = x.item)`,
			ids); err != nil {
			return res, fmt.Errorf("comparing %s: %w", list.table, err)
		}
	}
	return res, nil
}

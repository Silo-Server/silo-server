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

func exportKeyed(ctx context.Context, tx pgx.Tx, sql string, owner userdata.Owner) ([]keyedEntry, error) {
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
		out = append(out, e)
	}
	return out, rows.Err()
}

func (BackupData) Export(ctx context.Context, tx pgx.Tx, owner userdata.Owner) (userdata.Export, error) {
	favorites, err := exportKeyed(ctx, tx, `SELECT media_item_id, added_at FROM user_favorites WHERE user_id = $1 AND profile_id = $2 ORDER BY added_at, media_item_id`, owner)
	if err != nil {
		return userdata.Export{}, fmt.Errorf("reading favorites: %w", err)
	}
	watchlist, err := exportKeyed(ctx, tx, `SELECT media_item_id, added_at FROM user_watchlist WHERE user_id = $1 AND profile_id = $2 ORDER BY sort_index NULLS LAST, added_at, media_item_id`, owner)
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
	sum.Count = len(doc.Favorites) + len(doc.Watchlist) + len(doc.Titles)
	if err := userdata.CheckEntries("favorites", sum.Count); err != nil {
		return doc, sum, err
	}
	for _, list := range [][]keyedEntry{doc.Favorites, doc.Watchlist} {
		for _, e := range list {
			if !userdata.ValidContentID(e.Item) {
				return doc, sum, fmt.Errorf("favorites name an invalid item %q", e.Item)
			}
			sum.Items = append(sum.Items, e.Item)
		}
	}
	for _, w := range doc.Titles {
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
func (BackupData) Prepare(ctx context.Context, tx pgx.Tx, docs []userdata.Prepared) error {
	var snaps []Snapshot
	now := time.Now().UTC()
	for _, d := range docs {
		doc, _, err := decodeFavorites(d.Data)
		if err != nil {
			return err
		}
		for _, w := range doc.Titles {
			snaps = append(snaps, w.snapshot())
		}
	}
	return EnsureTitlesInTx(ctx, tx, snaps, now)
}

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
	if err := importKeyed(ctx, tx, target, `INSERT INTO user_favorites (user_id, profile_id, media_item_id, added_at) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, doc.Favorites, &res); err != nil {
		return res, fmt.Errorf("restoring favorites: %w", err)
	}
	if err := importKeyed(ctx, tx, target, `INSERT INTO user_watchlist (user_id, profile_id, media_item_id, added_at) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, doc.Watchlist, &res); err != nil {
		return res, fmt.Errorf("restoring the watchlist: %w", err)
	}
	return res, nil
}

func importTitles(ctx context.Context, tx pgx.Tx, target userdata.Target, titles []watchedTitle, res *userdata.Result) error {
	for _, w := range titles {
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
	return nil
}

// importKeyed inserts each mapped entry the profile does not have. sql takes
// user id, profile id, item, and time, and must do nothing on conflict.
func importKeyed(ctx context.Context, tx pgx.Tx, target userdata.Target, sql string, entries []keyedEntry, res *userdata.Result) error {
	for _, e := range entries {
		local, ok, err := target.Items.Resolve(ctx, e.Item)
		if err != nil {
			return err
		}
		if !ok {
			res.LeftOut++
			continue
		}
		at := e.At
		if at.IsZero() {
			at = target.Now
		}
		tag, err := tx.Exec(ctx, sql, target.UserID, target.ProfileID, local, at)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			res.Added++
		} else {
			res.Unchanged++
		}
	}
	return nil
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

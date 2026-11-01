package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// The Postgres user store owns two of the data types backups and profile
// exports carry: dismissals and preferences.

// keyedEntry is an item a profile marked at a time: a favorite, a watchlist
// entry, a "not interested".
type keyedEntry struct {
	Item string    `json:"item"`
	At   time.Time `json:"at"`
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

func validateKeyed(kind string, entries []keyedEntry, sum *userdata.Summary) error {
	for _, e := range entries {
		if !userdata.ValidContentID(e.Item) {
			return fmt.Errorf("%s names an invalid item %q", kind, e.Item)
		}
		sum.Items = append(sum.Items, e.Item)
	}
	sum.Count += len(entries)
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

func exportDocument(doc any, count int, items []string) (userdata.Export, error) {
	data, err := json.Marshal(doc)
	return userdata.Export{Data: data, Count: count, Items: items}, err
}

func keyedItems(lists ...[]keyedEntry) []string {
	var out []string
	for _, list := range lists {
		for _, e := range list {
			out = append(out, e.Item)
		}
	}
	return out
}

// ---- Dismissals ----

// The keyed dismissal tables.
const (
	tableNotInterested = "user_not_interested"
	tableDroppedSeries = "user_dropped_series"
)

// DismissalsBackupData is what a profile dismissed (userdata.Dismissals):
// home row dismissals, "not interested", and dropped series. Entries are
// added where the profile lacks them.
type DismissalsBackupData struct{}

func (DismissalsBackupData) ID() string { return userdata.Dismissals }
func (DismissalsBackupData) Tables() []string {
	return []string{"user_home_item_dismissals", tableNotInterested, tableDroppedSeries}
}

type dismissalsDocument struct {
	Home          []homeDismissal `json:"home"`
	NotInterested []keyedEntry    `json:"not_interested"`
	DroppedSeries []keyedEntry    `json:"dropped_series"`
}

type homeDismissal struct {
	Surface           string     `json:"surface"`
	Item              string     `json:"item"`
	Series            *string    `json:"series"`
	ProgressUpdatedAt *time.Time `json:"progress_updated_at"`
	DismissedAt       time.Time  `json:"dismissed_at"`
}

func (DismissalsBackupData) Export(ctx context.Context, tx pgx.Tx, owner userdata.Owner) (userdata.Export, error) {
	rows, err := tx.Query(ctx, `
		SELECT surface, media_item_id, series_id, progress_updated_at, dismissed_at
		FROM user_home_item_dismissals WHERE user_id = $1 AND profile_id = $2 ORDER BY surface, media_item_id`, owner.UserID, owner.ProfileID)
	if err != nil {
		return userdata.Export{}, fmt.Errorf("reading home dismissals: %w", err)
	}
	doc := dismissalsDocument{Home: []homeDismissal{}}
	var items []string
	for rows.Next() {
		var d homeDismissal
		if err := rows.Scan(&d.Surface, &d.Item, &d.Series, &d.ProgressUpdatedAt, &d.DismissedAt); err != nil {
			rows.Close()
			return userdata.Export{}, err
		}
		doc.Home = append(doc.Home, d)
		items = append(items, d.Item)
		if d.Series != nil {
			items = append(items, *d.Series)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return userdata.Export{}, err
	}
	if doc.NotInterested, err = exportKeyed(ctx, tx, `SELECT media_item_id, added_at FROM user_not_interested WHERE user_id = $1 AND profile_id = $2 ORDER BY media_item_id`, owner); err != nil {
		return userdata.Export{}, fmt.Errorf("reading not interested: %w", err)
	}
	if doc.DroppedSeries, err = exportKeyed(ctx, tx, `SELECT series_id, dropped_at FROM user_dropped_series WHERE user_id = $1 AND profile_id = $2 ORDER BY series_id`, owner); err != nil {
		return userdata.Export{}, fmt.Errorf("reading dropped series: %w", err)
	}
	items = append(items, keyedItems(doc.NotInterested, doc.DroppedSeries)...)
	return exportDocument(doc, len(doc.Home)+len(doc.NotInterested)+len(doc.DroppedSeries), items)
}

func decodeDismissals(data json.RawMessage) (dismissalsDocument, userdata.Summary, error) {
	var doc dismissalsDocument
	var sum userdata.Summary
	if err := userdata.DecodeStrict(data, &doc); err != nil {
		return doc, sum, err
	}
	if err := userdata.CheckEntries("dismissals", len(doc.Home)+len(doc.NotInterested)+len(doc.DroppedSeries)); err != nil {
		return doc, sum, err
	}
	for _, d := range doc.Home {
		if (d.Surface != "continue_watching" && d.Surface != "next_up") || !userdata.ValidContentID(d.Item) || (d.Series != nil && !userdata.ValidContentID(*d.Series)) {
			return doc, sum, fmt.Errorf("a home dismissal of %q is not valid", d.Item)
		}
		sum.Items = append(sum.Items, d.Item)
		if d.Series != nil {
			sum.Items = append(sum.Items, *d.Series)
		}
	}
	sum.Count += len(doc.Home)
	if err := validateKeyed("not interested", doc.NotInterested, &sum); err != nil {
		return doc, sum, err
	}
	err := validateKeyed("dropped series", doc.DroppedSeries, &sum)
	return doc, sum, err
}

func (DismissalsBackupData) Validate(data json.RawMessage) (userdata.Summary, error) {
	_, sum, err := decodeDismissals(data)
	return sum, err
}

func (DismissalsBackupData) Import(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, _, err := decodeDismissals(data)
	if err != nil {
		return userdata.Result{}, err
	}
	var res userdata.Result
	for _, d := range doc.Home {
		local, ok, err := target.Items.Resolve(ctx, d.Item)
		if err != nil {
			return res, err
		}
		if !ok {
			res.LeftOut++
			continue
		}
		var series *string
		if d.Series != nil {
			if mapped, ok, err := target.Items.Resolve(ctx, *d.Series); err != nil {
				return res, err
			} else if ok {
				series = &mapped
			}
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO user_home_item_dismissals (user_id, profile_id, surface, media_item_id, series_id, progress_updated_at, dismissed_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`,
			target.UserID, target.ProfileID, d.Surface, local, series, d.ProgressUpdatedAt, d.DismissedAt)
		if err != nil {
			return res, fmt.Errorf("restoring a home dismissal: %w", err)
		}
		if tag.RowsAffected() > 0 {
			res.Added++
		} else {
			res.Unchanged++
		}
	}
	if err := importKeyed(ctx, tx, target, `INSERT INTO user_not_interested (user_id, profile_id, media_item_id, added_at) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, doc.NotInterested, &res); err != nil {
		return res, fmt.Errorf("restoring not interested: %w", err)
	}
	if err := importKeyed(ctx, tx, target, `INSERT INTO user_dropped_series (user_id, profile_id, series_id, dropped_at) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, doc.DroppedSeries, &res); err != nil {
		return res, fmt.Errorf("restoring dropped series: %w", err)
	}
	return res, nil
}

// Pending counts the dismissals Import would add: home dismissals (by
// surface and item), "not interested" items, and dropped series the
// profile lacks.
func (DismissalsBackupData) Pending(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, _, err := decodeDismissals(data)
	if err != nil {
		return userdata.Result{}, err
	}
	var res userdata.Result
	seen := map[string]bool{}
	var surfaces, items []string
	for _, d := range doc.Home {
		key := d.Surface + "\x00" + d.Item
		if seen[key] {
			res.Unchanged++
			continue
		}
		seen[key] = true
		surfaces, items = append(surfaces, d.Surface), append(items, d.Item)
	}
	if len(items) > 0 {
		rows, err := tx.Query(ctx, `
			SELECT x.item FROM unnest($3::text[], $4::text[]) AS x(surface, item)
			WHERE NOT EXISTS (SELECT 1 FROM user_home_item_dismissals d
				WHERE d.user_id = $1 AND d.profile_id = $2 AND d.surface = x.surface AND d.media_item_id = x.item)`,
			target.UserID, target.ProfileID, surfaces, items)
		if err != nil {
			return res, fmt.Errorf("comparing home dismissals: %w", err)
		}
		missing, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return res, fmt.Errorf("comparing home dismissals: %w", err)
		}
		res.Unchanged += len(items) - len(missing)
		if err := userdata.SettleChanged(ctx, target.Items, &res, missing, nil); err != nil {
			return res, err
		}
	}
	for _, list := range []struct {
		table, column string
		entries       []keyedEntry
	}{{tableNotInterested, "media_item_id", doc.NotInterested}, {tableDroppedSeries, "series_id", doc.DroppedSeries}} {
		if err := userdata.PendingMissing(ctx, tx, target, &res, `
			SELECT x.item FROM unnest($3::text[]) AS x(item)
			WHERE NOT EXISTS (SELECT 1 FROM `+list.table+` d WHERE d.user_id = $1 AND d.profile_id = $2 AND d.`+list.column+` = x.item)`,
			keyedItems(list.entries)); err != nil {
			return res, fmt.Errorf("comparing %s: %w", list.table, err)
		}
	}
	return res, nil
}

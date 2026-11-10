package pgstore

// What restoring a profile's preferences would change, counted without
// writing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// Pending counts what Import would do, by its rules, writing nothing and
// taking no lock: profile columns, settings, and track, library, series,
// and collection sort preferences that are not here or differ, and page
// layouts whose overrides here differ.
func (d PreferencesBackupData) Pending(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, sum, err := d.decode(data)
	if err != nil {
		return userdata.Result{}, err
	}
	// Resolve every series the document names at once; the comparisons
	// below then look each up without a query of its own.
	if _, err := userdata.ResolveAll(ctx, target.Items, sum.Items); err != nil {
		return userdata.Result{}, err
	}
	var res userdata.Result
	// differing names what would change, for a debug log: column names,
	// setting keys, and kinds of preference, never values.
	var differing []string
	if p := doc.Profile; p != nil {
		current, err := readProfilePreferences(ctx, tx, target.Owner)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			res.Unchanged++
		case err != nil:
			return res, err
		default:
			if columns := current.differing(*p); len(columns) > 0 {
				differing = append(differing, columns...)
				res.Updated++
			} else {
				res.Unchanged++
			}
		}
	}
	stored, err := storedSettings(ctx, tx, target.Owner)
	if err != nil {
		return res, err
	}
	for _, s := range doc.Settings {
		plan, err := planSetting(ctx, tx, target, s, stored)
		if err != nil {
			return res, err
		}
		switch plan.outcome {
		case settingNew:
			res.Added++
		case settingChanged:
			res.Updated++
		case settingSame:
			res.Unchanged++
		default:
			res.LeftOut++
		}
		if plan.outcome == settingNew || plan.outcome == settingChanged {
			differing = append(differing, "setting "+s.Key)
		}
	}
	before := res.Changes()
	if err := pendingTrackPreferences(ctx, tx, target, doc, &res); err != nil {
		return res, err
	}
	if res.Changes() > before {
		differing = append(differing, "track, library, series, or sort preferences")
	}
	before = res.Changes()
	if err := pendingSectionOverrides(ctx, tx, target, doc.SectionOverrides, &res); err != nil {
		return res, err
	}
	if res.Changes() > before {
		differing = append(differing, "page layouts")
	}
	if len(differing) > 0 {
		slog.DebugContext(ctx, "a profile's preferences differ from the backup's",
			"user_id", target.UserID, "profile_id", target.ProfileID, "differing", differing)
	}
	return res, nil
}

func pendingTrackPreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, doc preferencesDocument, res *userdata.Result) error {
	if err := pendingAudioPreferences(ctx, tx, target, doc.Audio, res); err != nil {
		return err
	}
	if err := pendingSubtitlePreferences(ctx, tx, target, doc.Subtitles, res); err != nil {
		return err
	}
	if err := pendingLibraryPreferences(ctx, tx, target, doc.LibraryPlayback, res); err != nil {
		return err
	}
	if err := pendingSeriesPreferences(ctx, tx, target, doc.SeriesPlayback, res); err != nil {
		return err
	}
	return pendingCollectionSorts(ctx, tx, target, doc.CollectionSort, res)
}

// pendingCollectionSorts compares the sorts with this profile's in one read.
func pendingCollectionSorts(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []collectionSort, res *userdata.Result) error {
	rows, err := localSorts(ctx, target, entries, res)
	if err != nil {
		return err
	}
	kinds, collections, fields, orders := make([]string, len(rows)), make([]string, len(rows)), make([]string, len(rows)), make([]string, len(rows))
	for i, c := range rows {
		kinds[i], collections[i], fields[i], orders[i] = c.Kind, c.Collection, c.Field, c.Order
	}
	if err := comparePendingSet(ctx, tx, res, len(rows), `
		SELECT count(*) FILTER (WHERE p.collection_id IS NULL),
			count(*) FILTER (WHERE p.collection_id IS NOT NULL AND (p.sort_field, p.sort_order) IS DISTINCT FROM (u.field, u.sort_order))
		FROM unnest($3::text[], $4::text[], $5::text[], $6::text[]) AS u(kind, collection, field, sort_order)
		LEFT JOIN user_collection_sort_preferences p ON p.user_id = $1 AND p.profile_id = $2
			AND p.collection_kind = u.kind AND p.collection_id = u.collection`,
		target.UserID, target.ProfileID, kinds, collections, fields, orders); err != nil {
		return fmt.Errorf("comparing collection sort preferences: %w", err)
	}
	return nil
}

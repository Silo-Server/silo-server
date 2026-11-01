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
func (PreferencesBackupData) Pending(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, sum, err := decodePreferences(data)
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

// comparePending reads whether a row exists and whether its values differ
// from the backup's (sql returns one row of differs, or none), and counts
// what an upsert guarded by "values differ" would do.
func comparePending(ctx context.Context, tx pgx.Tx, res *userdata.Result, sql string, args ...any) error {
	var differs bool
	err := tx.QueryRow(ctx, sql, args...).Scan(&differs)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		res.Added++
	case err != nil:
		return err
	case differs:
		res.Updated++
	default:
		res.Unchanged++
	}
	return nil
}

func pendingTrackPreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, doc preferencesDocument, res *userdata.Result) error {
	for _, a := range doc.Audio {
		series, ok, err := resolveSeries(ctx, target, a.Series, res)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := comparePending(ctx, tx, res, `
			SELECT (audio_track_index, audio_language, audio_track_signature) IS DISTINCT FROM ($4, $5, $6::jsonb)
			FROM user_audio_preferences WHERE user_id = $1 AND profile_id = $2 AND series_id = $3`,
			target.UserID, target.ProfileID, series, a.TrackIndex, a.Language, trackSignature(a.Signature)); err != nil {
			return fmt.Errorf("comparing an audio preference: %w", err)
		}
	}
	for _, s := range doc.Subtitles {
		series, ok, err := resolveSeries(ctx, target, s.Series, res)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := comparePending(ctx, tx, res, `
			SELECT (subtitle_language, subtitle_track_index, external_subtitle_path, subtitle_mode, show_forced_subtitles, subtitle_track_signature)
				IS DISTINCT FROM ($4, $5, $6, $7, $8, $9::jsonb)
			FROM user_subtitle_preferences WHERE user_id = $1 AND profile_id = $2 AND series_id = $3`,
			target.UserID, target.ProfileID, series, s.Language, s.TrackIndex, s.localPath(target), s.Mode, s.ShowForcedSubtitles, trackSignature(s.Signature)); err != nil {
			return fmt.Errorf("comparing a subtitle preference: %w", err)
		}
	}
	for _, l := range doc.LibraryPlayback {
		library, ok := target.MapLibrary(l.Library)
		if !ok {
			res.LeftOut++
			continue
		}
		if err := comparePending(ctx, tx, res, `
			SELECT (audio_language, subtitle_language, subtitle_mode, show_forced_subtitles) IS DISTINCT FROM ($4, $5, $6, $7)
			FROM user_library_playback_preferences WHERE user_id = $1 AND profile_id = $2 AND library_id = $3`,
			target.UserID, target.ProfileID, library, l.AudioLanguage, l.SubtitleLanguage, l.SubtitleMode, l.ShowForcedSubtitles); err != nil {
			return fmt.Errorf("comparing a library playback preference: %w", err)
		}
	}
	for _, sp := range doc.SeriesPlayback {
		series, ok, err := resolveSeries(ctx, target, sp.Series, res)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := comparePending(ctx, tx, res, `
			SELECT (resolution, hdr, codec_video, file_id, edition_key) IS DISTINCT FROM ($4, $5, $6, $7, $8)
			FROM user_series_playback_preferences WHERE user_id = $1 AND profile_id = $2 AND series_id = $3`,
			target.UserID, target.ProfileID, series, sp.Resolution, sp.HDR, sp.CodecVideo, sp.localFileID(target), sp.EditionKey); err != nil {
			return fmt.Errorf("comparing a series playback preference: %w", err)
		}
	}
	for _, c := range doc.CollectionSort {
		collection, ok := c.localCollection(target)
		if !ok {
			res.LeftOut++
			continue
		}
		if err := comparePending(ctx, tx, res, `
			SELECT (sort_field, sort_order) IS DISTINCT FROM ($5, $6)
			FROM user_collection_sort_preferences WHERE user_id = $1 AND profile_id = $2 AND collection_kind = $3 AND collection_id = $4`,
			target.UserID, target.ProfileID, c.Kind, collection, c.Field, c.Order); err != nil {
			return fmt.Errorf("comparing a collection sort preference: %w", err)
		}
	}
	return nil
}

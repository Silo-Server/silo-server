package pgstore

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Each list of track, playback, and sort preferences is written, and
// compared for a preview, in one statement, whatever its length: an
// import's statements do not grow with the document.

func importTrackPreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, doc preferencesDocument, res *userdata.Result) error {
	if err := importAudioPreferences(ctx, tx, target, doc.Audio, res); err != nil {
		return err
	}
	if err := importSubtitlePreferences(ctx, tx, target, doc.Subtitles, res); err != nil {
		return err
	}
	if err := importLibraryPreferences(ctx, tx, target, doc.LibraryPlayback, res); err != nil {
		return err
	}
	if err := importSeriesPreferences(ctx, tx, target, doc.SeriesPlayback, res); err != nil {
		return err
	}
	return importCollectionSorts(ctx, tx, target, doc.CollectionSort, res)
}

// lastByKey keeps, of the rows with one key, the last, in the order keys
// first appear; two backup ids can map to one id here, and one statement
// cannot write a row twice. The rows it drops count as unchanged.
func lastByKey[T any](rows []T, key func(T) string, res *userdata.Result) []T {
	at := make(map[string]int, len(rows))
	out := rows[:0:0]
	for _, r := range rows {
		k := key(r)
		if i, ok := at[k]; ok {
			out[i] = r
			res.Unchanged++
			continue
		}
		at[k] = len(out)
		out = append(out, r)
	}
	return out
}

// upsertSet runs a set-based upsert of n rows, whose conflict update is
// guarded by "values differ" and that returns (xmax = 0) for each row it
// wrote: a row it does not return was unchanged.
func upsertSet(ctx context.Context, tx pgx.Tx, res *userdata.Result, n int, sql string, args ...any) error {
	if n == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	written, err := pgx.CollectRows(rows, pgx.RowTo[bool])
	if err != nil {
		return err
	}
	for _, inserted := range written {
		if inserted {
			res.Added++
		} else {
			res.Updated++
		}
	}
	res.Unchanged += n - len(written)
	return nil
}

type seriesRow[T any] struct {
	series string
	entry  T
}

// bySeries pairs the entries whose series is here with it, one per series.
func bySeries[T any](ctx context.Context, target userdata.Target, entries []T, series func(T) string, res *userdata.Result) ([]seriesRow[T], error) {
	var rows []seriesRow[T]
	for _, e := range entries {
		local, ok, err := resolveSeries(ctx, target, series(e), res)
		if err != nil {
			return nil, err
		}
		if ok {
			rows = append(rows, seriesRow[T]{local, e})
		}
	}
	return lastByKey(rows, func(r seriesRow[T]) string { return r.series }, res), nil
}

// The column arrays of each list's set-based statements, built once for
// the import and its preview: one entry per row here.
type audioColumns struct {
	series, signatures []string
	indexes            []*int
	languages          []*string
	at                 []time.Time
}

func audioRows(ctx context.Context, target userdata.Target, entries []audioPreference, res *userdata.Result) (audioColumns, error) {
	rows, err := bySeries(ctx, target, entries, func(a audioPreference) string { return a.Series }, res)
	var c audioColumns
	for _, r := range rows {
		a := r.entry
		c.series, c.indexes, c.languages = append(c.series, r.series), append(c.indexes, a.TrackIndex), append(c.languages, a.Language)
		c.signatures, c.at = append(c.signatures, trackSignature(a.Signature)), append(c.at, a.UpdatedAt)
	}
	return c, err
}

func importAudioPreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []audioPreference, res *userdata.Result) error {
	c, err := audioRows(ctx, target, entries, res)
	if err != nil {
		return err
	}
	if err := upsertSet(ctx, tx, res, len(c.series), `
		INSERT INTO user_audio_preferences (user_id, profile_id, series_id, audio_track_index, audio_language, audio_track_signature, updated_at)
		SELECT $1, $2, u.series, u.idx, u.lang, u.sig::jsonb, u.at
		FROM unnest($3::text[], $4::int[], $5::text[], $6::text[], $7::timestamptz[]) AS u(series, idx, lang, sig, at)
		ON CONFLICT (user_id, profile_id, series_id) DO UPDATE SET audio_track_index = EXCLUDED.audio_track_index,
			audio_language = EXCLUDED.audio_language, audio_track_signature = EXCLUDED.audio_track_signature, updated_at = EXCLUDED.updated_at
		WHERE (user_audio_preferences.audio_track_index, user_audio_preferences.audio_language, user_audio_preferences.audio_track_signature)
			IS DISTINCT FROM (EXCLUDED.audio_track_index, EXCLUDED.audio_language, EXCLUDED.audio_track_signature)
		RETURNING (xmax = 0)`,
		target.UserID, target.ProfileID, c.series, c.indexes, c.languages, c.signatures, c.at); err != nil {
		return fmt.Errorf("restoring audio preferences: %w", err)
	}
	return nil
}

func pendingAudioPreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []audioPreference, res *userdata.Result) error {
	c, err := audioRows(ctx, target, entries, res)
	if err != nil {
		return err
	}
	if err := comparePendingSet(ctx, tx, res, len(c.series), `
		SELECT count(*) FILTER (WHERE p.series_id IS NULL),
			count(*) FILTER (WHERE p.series_id IS NOT NULL
				AND (p.audio_track_index, p.audio_language, p.audio_track_signature) IS DISTINCT FROM (u.idx, u.lang, u.sig::jsonb))
		FROM unnest($3::text[], $4::int[], $5::text[], $6::text[]) AS u(series, idx, lang, sig)
		LEFT JOIN user_audio_preferences p ON p.user_id = $1 AND p.profile_id = $2 AND p.series_id = u.series`,
		target.UserID, target.ProfileID, c.series, c.indexes, c.languages, c.signatures); err != nil {
		return fmt.Errorf("comparing audio preferences: %w", err)
	}
	return nil
}

type subtitleColumns struct {
	series, languages, paths, modes, signatures []string
	indexes                                     []int
	forced                                      []*bool
	at                                          []time.Time
}

func subtitleRows(ctx context.Context, target userdata.Target, entries []subtitlePreference, res *userdata.Result) (subtitleColumns, error) {
	rows, err := bySeries(ctx, target, entries, func(s subtitlePreference) string { return s.Series }, res)
	var c subtitleColumns
	for _, r := range rows {
		s := r.entry
		c.series, c.languages, c.indexes = append(c.series, r.series), append(c.languages, s.Language), append(c.indexes, s.TrackIndex)
		c.paths, c.modes, c.forced = append(c.paths, s.localPath(target)), append(c.modes, s.Mode), append(c.forced, s.ShowForcedSubtitles)
		c.signatures, c.at = append(c.signatures, trackSignature(s.Signature)), append(c.at, s.UpdatedAt)
	}
	return c, err
}

func importSubtitlePreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []subtitlePreference, res *userdata.Result) error {
	c, err := subtitleRows(ctx, target, entries, res)
	if err != nil {
		return err
	}
	if err := upsertSet(ctx, tx, res, len(c.series), `
		INSERT INTO user_subtitle_preferences (user_id, profile_id, series_id, subtitle_language, subtitle_track_index, external_subtitle_path,
			subtitle_mode, show_forced_subtitles, subtitle_track_signature, updated_at)
		SELECT $1, $2, u.series, u.lang, u.idx, u.path, u.mode, u.forced, u.sig::jsonb, u.at
		FROM unnest($3::text[], $4::text[], $5::int[], $6::text[], $7::text[], $8::bool[], $9::text[], $10::timestamptz[])
			AS u(series, lang, idx, path, mode, forced, sig, at)
		ON CONFLICT (user_id, profile_id, series_id) DO UPDATE SET subtitle_language = EXCLUDED.subtitle_language,
			subtitle_track_index = EXCLUDED.subtitle_track_index, external_subtitle_path = EXCLUDED.external_subtitle_path,
			subtitle_mode = EXCLUDED.subtitle_mode, show_forced_subtitles = EXCLUDED.show_forced_subtitles,
			subtitle_track_signature = EXCLUDED.subtitle_track_signature, updated_at = EXCLUDED.updated_at
		WHERE (user_subtitle_preferences.subtitle_language, user_subtitle_preferences.subtitle_track_index, user_subtitle_preferences.external_subtitle_path,
			user_subtitle_preferences.subtitle_mode, user_subtitle_preferences.show_forced_subtitles, user_subtitle_preferences.subtitle_track_signature)
			IS DISTINCT FROM (EXCLUDED.subtitle_language, EXCLUDED.subtitle_track_index, EXCLUDED.external_subtitle_path,
			EXCLUDED.subtitle_mode, EXCLUDED.show_forced_subtitles, EXCLUDED.subtitle_track_signature)
		RETURNING (xmax = 0)`,
		target.UserID, target.ProfileID, c.series, c.languages, c.indexes, c.paths, c.modes, c.forced, c.signatures, c.at); err != nil {
		return fmt.Errorf("restoring subtitle preferences: %w", err)
	}
	return nil
}

func pendingSubtitlePreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []subtitlePreference, res *userdata.Result) error {
	c, err := subtitleRows(ctx, target, entries, res)
	if err != nil {
		return err
	}
	if err := comparePendingSet(ctx, tx, res, len(c.series), `
		SELECT count(*) FILTER (WHERE p.series_id IS NULL),
			count(*) FILTER (WHERE p.series_id IS NOT NULL
				AND (p.subtitle_language, p.subtitle_track_index, p.external_subtitle_path, p.subtitle_mode, p.show_forced_subtitles, p.subtitle_track_signature)
				IS DISTINCT FROM (u.lang, u.idx, u.path, u.mode, u.forced, u.sig::jsonb))
		FROM unnest($3::text[], $4::text[], $5::int[], $6::text[], $7::text[], $8::bool[], $9::text[]) AS u(series, lang, idx, path, mode, forced, sig)
		LEFT JOIN user_subtitle_preferences p ON p.user_id = $1 AND p.profile_id = $2 AND p.series_id = u.series`,
		target.UserID, target.ProfileID, c.series, c.languages, c.indexes, c.paths, c.modes, c.forced, c.signatures); err != nil {
		return fmt.Errorf("comparing subtitle preferences: %w", err)
	}
	return nil
}

type libraryColumns struct {
	libraries              []int
	audio, subtitles, mode []*string
	forced                 []*bool
	at                     []time.Time
}

func libraryRows(target userdata.Target, entries []libraryPreference, res *userdata.Result) libraryColumns {
	type row struct {
		library int
		entry   libraryPreference
	}
	var rows []row
	for _, l := range entries {
		if library, ok := target.MapLibrary(l.Library); ok {
			rows = append(rows, row{library, l})
		} else {
			res.LeftOut++
		}
	}
	var c libraryColumns
	for _, r := range lastByKey(rows, func(r row) string { return strconv.Itoa(r.library) }, res) {
		l := r.entry
		c.libraries, c.audio, c.subtitles = append(c.libraries, r.library), append(c.audio, l.AudioLanguage), append(c.subtitles, l.SubtitleLanguage)
		c.mode, c.forced, c.at = append(c.mode, l.SubtitleMode), append(c.forced, l.ShowForcedSubtitles), append(c.at, l.UpdatedAt)
	}
	return c
}

func importLibraryPreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []libraryPreference, res *userdata.Result) error {
	c := libraryRows(target, entries, res)
	if err := upsertSet(ctx, tx, res, len(c.libraries), `
		INSERT INTO user_library_playback_preferences (user_id, profile_id, library_id, audio_language, subtitle_language, subtitle_mode,
			show_forced_subtitles, updated_at)
		SELECT $1, $2, u.library, u.audio, u.subtitle, u.mode, u.forced, u.at
		FROM unnest($3::int[], $4::text[], $5::text[], $6::text[], $7::bool[], $8::timestamptz[]) AS u(library, audio, subtitle, mode, forced, at)
		ON CONFLICT (user_id, profile_id, library_id) DO UPDATE SET audio_language = EXCLUDED.audio_language,
			subtitle_language = EXCLUDED.subtitle_language, subtitle_mode = EXCLUDED.subtitle_mode,
			show_forced_subtitles = EXCLUDED.show_forced_subtitles, updated_at = EXCLUDED.updated_at
		WHERE (user_library_playback_preferences.audio_language, user_library_playback_preferences.subtitle_language,
			user_library_playback_preferences.subtitle_mode, user_library_playback_preferences.show_forced_subtitles)
			IS DISTINCT FROM (EXCLUDED.audio_language, EXCLUDED.subtitle_language, EXCLUDED.subtitle_mode, EXCLUDED.show_forced_subtitles)
		RETURNING (xmax = 0)`,
		target.UserID, target.ProfileID, c.libraries, c.audio, c.subtitles, c.mode, c.forced, c.at); err != nil {
		return fmt.Errorf("restoring library playback preferences: %w", err)
	}
	return nil
}

func pendingLibraryPreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []libraryPreference, res *userdata.Result) error {
	c := libraryRows(target, entries, res)
	if err := comparePendingSet(ctx, tx, res, len(c.libraries), `
		SELECT count(*) FILTER (WHERE p.library_id IS NULL),
			count(*) FILTER (WHERE p.library_id IS NOT NULL
				AND (p.audio_language, p.subtitle_language, p.subtitle_mode, p.show_forced_subtitles) IS DISTINCT FROM (u.audio, u.subtitle, u.mode, u.forced))
		FROM unnest($3::int[], $4::text[], $5::text[], $6::text[], $7::bool[]) AS u(library, audio, subtitle, mode, forced)
		LEFT JOIN user_library_playback_preferences p ON p.user_id = $1 AND p.profile_id = $2 AND p.library_id = u.library`,
		target.UserID, target.ProfileID, c.libraries, c.audio, c.subtitles, c.mode, c.forced); err != nil {
		return fmt.Errorf("comparing library playback preferences: %w", err)
	}
	return nil
}

type seriesColumns struct {
	series                        []string
	resolutions, codecs, editions []*string
	hdr                           []bool
	files                         []*int64
	at                            []time.Time
}

func seriesRows(ctx context.Context, target userdata.Target, entries []seriesPreference, res *userdata.Result) (seriesColumns, error) {
	rows, err := bySeries(ctx, target, entries, func(s seriesPreference) string { return s.Series }, res)
	var c seriesColumns
	for _, r := range rows {
		s := r.entry
		c.series, c.resolutions, c.hdr = append(c.series, r.series), append(c.resolutions, s.Resolution), append(c.hdr, s.HDR)
		c.codecs, c.files, c.editions = append(c.codecs, s.CodecVideo), append(c.files, s.localFileID(target)), append(c.editions, s.EditionKey)
		c.at = append(c.at, s.UpdatedAt)
	}
	return c, err
}

func importSeriesPreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []seriesPreference, res *userdata.Result) error {
	c, err := seriesRows(ctx, target, entries, res)
	if err != nil {
		return err
	}
	if err := upsertSet(ctx, tx, res, len(c.series), `
		INSERT INTO user_series_playback_preferences (user_id, profile_id, series_id, resolution, hdr, codec_video, file_id, edition_key, updated_at)
		SELECT $1, $2, u.series, u.resolution, u.hdr, u.codec, u.file, u.edition, u.at
		FROM unnest($3::text[], $4::text[], $5::bool[], $6::text[], $7::bigint[], $8::text[], $9::timestamptz[])
			AS u(series, resolution, hdr, codec, file, edition, at)
		ON CONFLICT (user_id, profile_id, series_id) DO UPDATE SET resolution = EXCLUDED.resolution, hdr = EXCLUDED.hdr,
			codec_video = EXCLUDED.codec_video, file_id = EXCLUDED.file_id, edition_key = EXCLUDED.edition_key, updated_at = EXCLUDED.updated_at
		WHERE (user_series_playback_preferences.resolution, user_series_playback_preferences.hdr, user_series_playback_preferences.codec_video,
			user_series_playback_preferences.file_id, user_series_playback_preferences.edition_key)
			IS DISTINCT FROM (EXCLUDED.resolution, EXCLUDED.hdr, EXCLUDED.codec_video, EXCLUDED.file_id, EXCLUDED.edition_key)
		RETURNING (xmax = 0)`,
		target.UserID, target.ProfileID, c.series, c.resolutions, c.hdr, c.codecs, c.files, c.editions, c.at); err != nil {
		return fmt.Errorf("restoring series playback preferences: %w", err)
	}
	return nil
}

func pendingSeriesPreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []seriesPreference, res *userdata.Result) error {
	c, err := seriesRows(ctx, target, entries, res)
	if err != nil {
		return err
	}
	if err := comparePendingSet(ctx, tx, res, len(c.series), `
		SELECT count(*) FILTER (WHERE p.series_id IS NULL),
			count(*) FILTER (WHERE p.series_id IS NOT NULL
				AND (p.resolution, p.hdr, p.codec_video, p.file_id, p.edition_key) IS DISTINCT FROM (u.resolution, u.hdr, u.codec, u.file, u.edition))
		FROM unnest($3::text[], $4::text[], $5::bool[], $6::text[], $7::bigint[], $8::text[]) AS u(series, resolution, hdr, codec, file, edition)
		LEFT JOIN user_series_playback_preferences p ON p.user_id = $1 AND p.profile_id = $2 AND p.series_id = u.series`,
		target.UserID, target.ProfileID, c.series, c.resolutions, c.hdr, c.codecs, c.files, c.editions); err != nil {
		return fmt.Errorf("comparing series playback preferences: %w", err)
	}
	return nil
}

// comparePendingSet counts what upsertSet would do with n rows: sql
// returns how many of them are not here and how many differ.
func comparePendingSet(ctx context.Context, tx pgx.Tx, res *userdata.Result, n int, sql string, args ...any) error {
	if n == 0 {
		return nil
	}
	var added, updated int
	if err := tx.QueryRow(ctx, sql, args...).Scan(&added, &updated); err != nil {
		return err
	}
	res.Added += added
	res.Updated += updated
	res.Unchanged += n - added - updated
	return nil
}

// localSorts maps each sort to its collection here, keeping those that
// have one. The decoder stored every sort under the identity the sort
// editor stores it under (catalog.NormalizeSortPreferenceRef): a personal
// list's under PersonalSortPreferenceCollectionID, which needs no mapping,
// and a library-kind sort under an admin collection's id (a
// library_collections row, as the editor checks), mapped through the
// restore's collection mapping in one call.
func localSorts(ctx context.Context, target userdata.Target, entries []collectionSort, res *userdata.Result) ([]collectionSort, error) {
	var ids []string
	for _, c := range entries {
		if c.Kind == userstore.CollectionKindLibrary {
			ids = append(ids, c.Collection)
		}
	}
	mapped, err := target.MapCollections(ctx, userdata.LibraryCollectionField, ids)
	if err != nil {
		return nil, err
	}
	out := make([]collectionSort, 0, len(entries))
	for _, c := range entries {
		if c.Kind == userstore.CollectionKindLibrary {
			local, ok := mapped[c.Collection]
			if !ok {
				res.LeftOut++
				continue
			}
			c.Collection = local
		}
		out = append(out, c)
	}
	return lastByKey(out, func(c collectionSort) string { return c.Kind + "\x00" + c.Collection }, res), nil
}

func importCollectionSorts(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []collectionSort, res *userdata.Result) error {
	rows, err := localSorts(ctx, target, entries, res)
	if err != nil {
		return err
	}
	n := len(rows)
	kinds, collections, fields, orders, at := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]time.Time, n)
	for i, c := range rows {
		kinds[i], collections[i], fields[i], orders[i], at[i] = c.Kind, c.Collection, c.Field, c.Order, c.UpdatedAt
	}
	if err := upsertSet(ctx, tx, res, n, `
		INSERT INTO user_collection_sort_preferences (user_id, profile_id, collection_kind, collection_id, sort_field, sort_order, updated_at)
		SELECT $1, $2, u.kind, u.collection, u.field, u.sort_order, u.at
		FROM unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::timestamptz[]) AS u(kind, collection, field, sort_order, at)
		ON CONFLICT (user_id, profile_id, collection_kind, collection_id) DO UPDATE SET sort_field = EXCLUDED.sort_field,
			sort_order = EXCLUDED.sort_order, updated_at = EXCLUDED.updated_at
		WHERE (user_collection_sort_preferences.sort_field, user_collection_sort_preferences.sort_order)
			IS DISTINCT FROM (EXCLUDED.sort_field, EXCLUDED.sort_order)
		RETURNING (xmax = 0)`,
		target.UserID, target.ProfileID, kinds, collections, fields, orders, at); err != nil {
		return fmt.Errorf("restoring collection sort preferences: %w", err)
	}
	return nil
}

// upsertSettingValues writes the planned setting values as
// upsertSettingValue writes one, in one statement per scope. Two plans for
// one identity (two backup series mapped to one here) write the last.
func upsertSettingValues(ctx context.Context, tx pgx.Tx, userID int, plans []settingPlan) error {
	byScope := map[settingscontract.Scope][]settingPlan{}
	var scopes []settingscontract.Scope
	for _, p := range plans {
		if err := p.id.Validate(); err != nil {
			return err
		}
		if err := userstore.ValidateSettingValueJSON(p.value); err != nil {
			return err
		}
		if _, ok := byScope[p.id.Scope]; !ok {
			scopes = append(scopes, p.id.Scope)
		}
		byScope[p.id.Scope] = append(byScope[p.id.Scope], p)
	}
	var ignored userdata.Result
	for _, scope := range scopes {
		target, ok := settingConflictTargets[scope]
		if !ok {
			return fmt.Errorf("%w: %q has no storage identity", userstore.ErrInvalidSettingIdentity, scope)
		}
		list := lastByKey(byScope[scope], func(p settingPlan) string {
			return settingKey(p.id.Key, string(p.id.Scope), string(p.id.ClientFamily), p.id.LibraryID, p.id.SeriesID)
		}, &ignored)
		n := len(list)
		keys, values := make([]string, n), make([]string, n)
		profiles, clients, devices, series, libraries := make([]*string, n), make([]*string, n), make([]*string, n), make([]*string, n), make([]*int, n)
		for i, p := range list {
			keys[i], values[i] = p.id.Key, string(p.value)
			profiles[i], clients[i], devices[i] = nullableText(p.id.ProfileID), nullableText(string(p.id.ClientFamily)), nullableText(p.id.DeviceID)
			libraries[i], series[i] = nullableInt(p.id.LibraryID), nullableText(p.id.SeriesID)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
			INSERT INTO user_setting_values (user_id, key, scope, profile_id, client_family, device_id, library_id, series_id, value)
			SELECT $1, u.key, $2, u.profile, u.client, u.device, u.library, u.series, u.value::jsonb
			FROM unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::int[], $8::text[], $9::text[])
				AS u(key, profile, client, device, library, series, value)
			ON CONFLICT %s DO UPDATE SET value = excluded.value, revision = user_setting_values.revision + 1, updated_at = now()`, target),
			userID, string(scope), keys, profiles, clients, devices, libraries, series, values); err != nil {
			return fmt.Errorf("restoring %s setting values: %w", scope, err)
		}
	}
	return nil
}

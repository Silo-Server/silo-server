package backup

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// stricterFlag is a security, access, sharing, or outbound switch inside a
// table a full restore otherwise restores as the backup has it. The restore
// keeps the stricter of the backup's and the live server's value, so it
// never turns back on what an admin, or a user, turned off since the
// backup: a disabled account or provider, a narrowed permission, a child
// profile, an unshared collection, a paused integration.
type stricterFlag struct {
	table, column string
	// stricter is the value that wins: false for a switch that grants or
	// sends something, true for one that requires or restricts.
	stricter bool
	// id is the SQL expression, over a row, that identifies it (t.id
	// when empty), and key one that names it besides: a restored row is the
	// live one only when both match. The carry runs only for the deployment
	// the backup came from, where the ids are the same rows.
	id, key string
	// scope limits the live rows that apply; everyRow for all.
	scope string
	// describe names a kept row in the restore's output.
	describe string
	// absentOff also sets the stricter value on a restored row the live
	// server no longer has (removed after the backup), described by
	// describeAbsent: it comes back, but never switched on. It is set for
	// every flag whose stricter value is off, unless rollsBack says why
	// such a row comes back as it was.
	absentOff      bool
	describeAbsent string
	rollsBack      string
}

// whyAccountsRollBack is why account switches roll back with the account:
// a full restore returns every account as the backup has it, one deleted
// since included, and says so before the swap (AccountRollback).
const whyAccountsRollBack = "accounts roll back whole, as the restore reports before the swap"

// columnEnabled is the switch column most flagged tables name.
const columnEnabled = "enabled"

// Tables more than one flag names.
const (
	tableLibraries           = "media_folders"
	tablePersonalCollections = "user_personal_collections"
)

// The keys a row is matched by.
const (
	accountKey = `lower(coalesce(t.username, t.email, ''))`
	pluginKey  = `t.plugin_id`
	noKey      = `''`
	profileKey = `t.user_id::text`
)

// flag builds a stricterFlag matched by id alone (a same-deployment carry),
// described by what plus the row's id.
func flag(table, column string, stricter bool, what string) stricterFlag {
	return stricterFlag{table: table, column: column, stricter: stricter, key: noKey, scope: everyRow,
		describe: fmt.Sprintf(`format('%s %%s kept as this server has it (%s)', t.id)`, what, column)}
}

// stricterFlags are the switches the restore keeps at their stricter value.
// TestEveryTableIsClassifiedDB requires each to be a boolean column of a
// table the restore otherwise restores as the backup has it, and every
// other boolean column of such a table to be in restoredFlagColumns with
// the reason it is not one. Switches in credential tables come from the
// live server already (secretTables).
var stricterFlags = func() []stricterFlag {
	flags := []stricterFlag{
		{table: tableUsers, column: columnEnabled, stricter: false, key: accountKey, scope: everyRow,
			describe: `format('account %s kept disabled', t.username)`, rollsBack: whyAccountsRollBack},
		{table: tableUsers, column: "break_glass", stricter: false, key: accountKey, scope: everyRow,
			describe: `format('account %s kept without break-glass sign-in', t.username)`, rollsBack: whyAccountsRollBack},
		{table: tableUsers, column: "local_password_login_enabled", stricter: false, key: accountKey, scope: everyRow,
			describe: `format('account %s kept without password sign-in', t.username)`, rollsBack: whyAccountsRollBack},
		{table: tableUsers, column: "password_change_required", stricter: true, key: accountKey, scope: everyRow,
			describe: `format('account %s still must change its password', t.username)`},
		// Plugins run as the server: one an admin turned off stays off (its
		// carried sign-in links then sign no one in), and one removed since
		// the backup comes back off. Nothing a restore does turns one on.
		{table: "plugin_installations", column: columnEnabled, stricter: false, key: pluginKey, scope: everyRow,
			describe: `format('plugin %s kept off', t.plugin_id)`, absentOff: true,
			describeAbsent: `format('plugin %s restored off: removed after the backup', t.plugin_id)`},
		{table: "plugin_task_bindings", column: columnEnabled, stricter: false, key: `t.plugin_installation_id::text || ':' || t.capability_id`, scope: everyRow,
			describe: `format('plugin task %s kept off', t.capability_id)`},
		// A plugin catalog an admin turned off stays off, and one removed
		// since comes back off, so nothing installs or updates from it.
		// A library an admin turned off since stays hidden, and one deleted
		// since comes back disabled, for an admin to turn on deliberately:
		// an account with every library would otherwise see it at once.
		{table: tableLibraries, column: columnEnabled, stricter: false, key: noKey, scope: everyRow,
			describe: `format('library %s kept disabled', t.name)`, absentOff: true,
			describeAbsent: `format('library %s restored disabled: removed after the backup', t.name)`},
		// Autoscan accepts webhooks with a carried token: off, globally or
		// for one source, stays off.
		{table: "autoscan_settings", column: columnEnabled, stricter: false, key: noKey, scope: everyRow,
			describe: `'autoscan kept off'`},
		flag("autoscan_sources", columnEnabled, false, "autoscan source"),
		// Outbound integrations, with credentials restored beside them.
		flag("history_import_sources", columnEnabled, false, "history import source"),
		flag("request_integrations", columnEnabled, false, "request integration"),
		flag("request_routes", columnEnabled, false, "request route"),
		{table: "subtitle_provider_config", column: columnEnabled, stricter: false, id: "t.provider_name", key: noKey, scope: everyRow,
			describe: `format('subtitle provider %s kept off', t.provider_name)`},
		{table: "marker_provider_config", column: "contribute_enabled", stricter: false, id: "t.provider", key: noKey, scope: everyRow,
			describe: `format('marker sharing with %s kept off', t.provider)`},
		{table: "marker_provider_config", column: "contribute_auto_local", stricter: false, id: "t.provider", key: noKey, scope: everyRow,
			describe: `format('automatic marker sharing with %s kept off', t.provider)`},
		// What users may ask the server to request from the request apps.
		flag("request_settings", "requests_enabled", false, "requests"),
		flag("request_settings", "global_auto_approval_enabled", false, "requests"),
		flag("request_settings", "watchlist_requests", false, "requests"),
		// A child profile, and a profile's restrictions, never loosen.
		{table: tableUserProfiles, column: "is_child", stricter: true, key: profileKey, scope: everyRow,
			describe: `format('profile %s kept a child profile', t.name)`},
		{table: tableUserProfiles, column: "library_restrictions_enabled", stricter: true, key: profileKey, scope: everyRow,
			describe: `format('profile %s kept its library restriction', t.name)`},
		{table: tableUserProfiles, column: "require_advisory_age", stricter: true, key: profileKey, scope: everyRow,
			describe: `format('profile %s kept its advisory-age requirement', t.name)`},
		// A collection its owner stopped sharing stays private, and one
		// deleted since comes back unshared.
		{table: tablePersonalCollections, column: "is_shared", stricter: false, key: noKey, scope: everyRow,
			describe: `format('personal collection %s kept unshared', t.id)`, absentOff: true,
			describeAbsent: `format('personal collection %s restored unshared: removed after the backup', t.id)`},
		{table: tablePersonalCollections, column: "include_in_server_collections", stricter: false, key: noKey, scope: everyRow,
			describe: `format('personal collection %s kept out of server collections', t.id)`, absentOff: true,
			describeAbsent: `format('personal collection %s restored out of server collections: removed after the backup', t.id)`},
	}
	// Per-account and per-group permissions never widen.
	for _, column := range []string{"download_allowed", "download_transcode_allowed", "transcode_allowed", "audio_transcode_allowed", "requests_allowed"} {
		what := strings.ReplaceAll(column, "_", " ")
		flags = append(flags,
			stricterFlag{table: tableUsers, column: column, stricter: false, key: accountKey, scope: everyRow,
				describe: fmt.Sprintf(`format('account %%s kept without %s', t.username)`, what), rollsBack: whyAccountsRollBack},
			stricterFlag{table: "access_groups", column: column, stricter: false, key: `t.name`, scope: everyRow,
				describe: fmt.Sprintf(`format('access group %%s kept without %s', t.name)`, what)})
	}
	// A profile that turned a notification off keeps it off.
	for _, column := range []string{columnEnabled, "notify_favorites", "notify_watchlist", "notify_continue_watching", "notify_next_up"} {
		flags = append(flags, stricterFlag{table: "notification_preferences", column: column, stricter: false,
			id: `t.user_id::text || ':' || t.profile_id`, key: noKey, scope: everyRow,
			describe: fmt.Sprintf(`format('notifications of profile %%s kept off (%s)', t.profile_id)`, column)})
	}
	// A row removed since the backup comes back with an off switch off,
	// never on, unless the flag says why it rolls back as it was.
	for i := range flags {
		f := &flags[i]
		if f.stricter || f.rollsBack != "" {
			continue
		}
		f.absentOff = true
		if f.describeAbsent == "" {
			f.describeAbsent = fmt.Sprintf(`format('%s %%s restored with %s off: removed after the backup', %s)`, f.table, f.column, f.idOf())
		}
	}
	return flags
}()

// Reasons a boolean column is restored as the backup has it.
const (
	whyFacts      = "facts about media or a file"
	whyHistory    = "history and progress"
	whyQueue      = "work queue state"
	whyPlayback   = "a profile's playback preference"
	whyProcessing = "how a library is processed"
	whyShown      = "presentation"
	whyKey        = "the singleton's key"
	whyDerived    = "derived, and recomputed"
	whyMigration  = "a migration's record"
	whyRouting    = "routing between request apps the integration switches gate"
)

// restoredFlagColumns are the boolean columns of restored tables that are
// not stricterFlags, each with why the backup's value is the one to keep.
var restoredFlagColumns = map[string]string{
	"access_groups.is_default":                                "which group new accounts join; the invitation carry compares the policy it grants",
	"admin_playback_history.completed":                        whyHistory,
	"admin_playback_history.measured":                         whyHistory,
	"autoscan_settings.id":                                    whyKey,
	"download_artifacts.progress_unavailable":                 whyFacts,
	"download_artifacts.tone_map_dv_bl_compat_id_present":     whyFacts,
	"download_artifacts.tone_map_dv_bl_present":               whyFacts,
	"download_artifacts.tone_map_dv_config_present":           whyFacts,
	"download_artifacts.tone_map_dv_rpu_present":              whyFacts,
	"download_artifacts.tone_map_preflight_required":          whyFacts,
	"download_subscriptions.active":                           "a user's own automatic downloads to their own devices",
	"download_subscriptions.delete_watched":                   "a user's own download preference",
	"downloaded_subtitles.hearing_impaired":                   whyFacts,
	"ebook_enrichment_state.requeue_requested":                whyQueue,
	"embedded_subtitle_tracks.has_override_tags":              whyFacts,
	"episode_catalog_entries.has_dolby_vision":                whyFacts,
	"episode_catalog_entries.has_hdr":                         whyFacts,
	"episode_catalog_entries.has_non_dolby_vision":            whyFacts,
	"episode_catalog_entries.has_non_hdr":                     whyFacts,
	"item_videos.is_official":                                 whyFacts,
	"library_collections.featured":                            whyShown,
	"library_collections.poster_auto_generated":               whyShown,
	"library_collections.poster_from_template":                whyShown,
	"library_provider_chains.enabled":                         "orders metadata providers; each provider's plugin switch and credentials gate it",
	"local_metadata_default_on_chain_backup.enabled":          whyMigration,
	"local_metadata_default_on_chain_backup.up_enabled":       whyMigration,
	"marker_contributions.claim_active":                       whyQueue,
	"marker_provider_config.fetch_enabled":                    "fetching markers sends nothing of the server's",
	"media_files.hdr":                                         whyFacts,
	"media_files.multiple_pps":                                whyFacts,
	"media_folders.allow_empty_cleanup_once":                  "a one-shot scan instruction",
	"media_folders.auto_translate_metadata":                   whyProcessing,
	"media_folders.box_sets_enabled":                          whyShown,
	"media_folders.box_sets_in_search":                        whyShown,
	"media_folders.chapter_thumbnails_enabled":                whyProcessing,
	"media_folders.intro_detection_enabled":                   whyProcessing,
	"media_folders.realtime_monitoring":                       whyProcessing,
	"media_folders.trickplay_enabled":                         whyProcessing,
	"media_group_locations.is_primary":                        whyFacts,
	"media_items.episode_metadata_incomplete":                 whyFacts,
	"media_request_targets.is_anime":                          whyFacts,
	"media_requests.is_anime":                                 whyFacts,
	"movie_match_queue.lease_forced_rerun":                    whyQueue,
	"movie_match_queue.rerun_requested":                       whyQueue,
	"page_sections.enabled":                                   whyShown,
	"page_sections.featured":                                  whyShown,
	"page_sections.web_only":                                  whyShown,
	"profile_series_interest.continue_watching":               whyDerived,
	"profile_series_interest.favorite":                        whyDerived,
	"profile_series_interest.next_up_candidate":               whyDerived,
	"profile_series_interest.watchlist":                       whyDerived,
	"request_routes.is_fallback":                              whyRouting,
	"request_routes.skip_uhd":                                 whyRouting,
	"request_routing.id":                                      whyKey,
	"request_settings.force_dual_quality":                     "what a request asks for, not whether one may be made",
	"request_settings.id":                                     whyKey,
	"series_root_match_queue.lease_forced_rerun":              whyQueue,
	"series_root_match_queue.rerun_requested":                 whyQueue,
	"user_downloads.transcoded":                               whyFacts,
	"user_library_playback_preferences.show_forced_subtitles": whyPlayback,
	"user_login_identifiers.legacy_duplicate":                 whyMigration,
	"user_personal_collections.native":                        "a collection's origin",
	"user_profiles.auto_play_next_preview":                    whyPlayback,
	"user_profiles.auto_skip_credits":                         whyPlayback,
	"user_profiles.auto_skip_intro":                           whyPlayback,
	"user_profiles.auto_skip_recap":                           whyPlayback,
	"user_profiles.is_primary":                                "the household parent; exactly one per account",
	"user_profiles.remove_watched_from_watchlist":             "a watchlist preference",
	"user_profiles.show_forced_subtitles":                     whyPlayback,
	"user_series_playback_preferences.hdr":                    whyPlayback,
	"user_subtitle_preferences.show_forced_subtitles":         whyPlayback,
	"user_watch_history.completed":                            whyHistory,
	"user_watch_progress.completed":                           whyHistory,
	"user_watch_progress.hide_from_continue":                  "a Home preference",
	"user_watch_progress.last_hdr":                            whyHistory,
	"users.is_owner":                                          "exactly one owner; the credential carry refuses keys of an account with more authority",
}

// liveFlags is what readStricterFlags reads for one flag: the live rows at
// its stricter value, and, for an absentOff flag, every live row, each a
// JSON array of {id, key}.
type liveFlags struct {
	stricter, present string
}

// idOf is a flag's row id expression.
func (f stricterFlag) idOf() string {
	if f.id != "" {
		return f.id
	}
	return "t.id::text"
}

// readStricterFlags reads each flag's live rows in the live snapshot tx.
func readStricterFlags(ctx context.Context, tx pgx.Tx) ([]liveFlags, error) {
	out := make([]liveFlags, len(stricterFlags))
	for i, f := range stricterFlags {
		rows := fmt.Sprintf(`SELECT coalesce(json_agg(json_build_object('id', %s, 'key', %s)), '[]')::text FROM %s t`, f.idOf(), f.key, f.table)
		if err := tx.QueryRow(ctx, rows+fmt.Sprintf(` WHERE t.%s = $1 AND %s`, f.column, f.scope), f.stricter).Scan(&out[i].stricter); err != nil {
			return nil, fmt.Errorf("%s.%s: %w", f.table, f.column, err)
		}
		out[i].present = "[]"
		if f.absentOff {
			if err := tx.QueryRow(ctx, rows).Scan(&out[i].present); err != nil {
				return nil, fmt.Errorf("%s: %w", f.table, err)
			}
		}
	}
	return out, nil
}

// keepStricterFlags sets, in the restored database, each flag of a row
// that is also live at its stricter value, and returns what it kept.
func keepStricterFlags(ctx context.Context, tx pgx.Tx, live []liveFlags) ([]string, error) {
	var kept []string
	for i, f := range stricterFlags {
		// A backup taken before the column existed has nothing to keep;
		// the restored server adds it with its default when it migrates.
		var has bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_attribute
			WHERE attrelid = to_regclass($1) AND attname = $2 AND attnum > 0 AND NOT attisdropped)`, f.table, f.column).Scan(&has); err != nil {
			return nil, err
		}
		if !has {
			continue
		}
		sql := fmt.Sprintf(`UPDATE %[1]s t SET %[2]s = $2 FROM json_to_recordset($1::json) AS l(id text, key text)
			WHERE %[5]s = l.id AND %[3]s = l.key AND t.%[2]s IS DISTINCT FROM $2 RETURNING %[4]s`, f.table, f.column, f.key, f.describe, f.idOf())
		names, err := collectKept(ctx, tx, sql, live[i].stricter, f.stricter)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", f.table, f.column, err)
		}
		kept = append(kept, names...)
		if !f.absentOff {
			continue
		}
		sql = fmt.Sprintf(`UPDATE %[1]s t SET %[2]s = $2 WHERE t.%[2]s IS DISTINCT FROM $2 AND NOT EXISTS (
			SELECT 1 FROM json_to_recordset($1::json) AS l(id text, key text) WHERE %[5]s = l.id AND %[3]s = l.key) RETURNING %[4]s`,
			f.table, f.column, f.key, f.describeAbsent, f.idOf())
		if names, err = collectKept(ctx, tx, sql, live[i].present, f.stricter); err != nil {
			return nil, fmt.Errorf("%s.%s: %w", f.table, f.column, err)
		}
		kept = append(kept, names...)
	}
	return kept, nil
}

// collectKept runs an UPDATE … RETURNING a description of each row.
func collectKept(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

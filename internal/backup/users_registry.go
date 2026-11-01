package backup

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/watchlist"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

// userDataTypes are the per-profile data types a restore set and a profile
// export carry, each implemented by the package that owns its tables, in
// the order an import applies them.
var userDataTypes = []userdata.Type{
	watchstate.BackupData{},
	catalog.RatingsBackupData{},
	watchlist.BackupData{},
	usercollections.BackupData{},
	pgstore.DismissalsBackupData{},
	pgstore.PreferencesBackupData{},
	notifications.BackupData{},
}

func userDataType(id string) (userdata.Type, bool) { return userdata.Find(userDataTypes, id) }

// UserDataTypeIDs lists the data type ids in import order.
func UserDataTypeIDs() []string {
	out := make([]string, len(userDataTypes))
	for i, t := range userDataTypes {
		out[i] = t.ID()
	}
	return out
}

// accountFieldTables are the tables the account and profile fields of a
// user document read and a restore writes: identity, login, and access.
// user_login_identifiers is kept by a trigger on users.
var accountFieldTables = strings.Fields(`users user_login_identifiers user_profiles user_profile_allowed_libraries
	backup_restored_accounts backup_restored_profiles`)

// tableDownloads is the downloads table, named once.
const tableDownloads = "downloads"

// Reasons shared by several excluded tables.
const (
	excludedPush           = "push registrations"
	excludedDownloads      = "offline downloads"
	excludedPlayback       = "playback sessions"
	excludedPlaybackLogs   = "playback logs"
	excludedLogs           = "logs"
	excludedSignIn         = "external sign-in state"
	excludedRecommendation = "derived; rebuilt by recommendations"
	excludedCollage        = "derived collages, regenerated on read"
	excludedRevision       = "revision counters of this server"
	excludedBooks          = "beta Books data, outside the restore"
	excludedHistoryImport  = "history-import credentials"
	excludedDashboard      = "administrator dashboard layout"
	excludedDiscord        = "a Discord link is an external sign-in"
)

// excludedUserTables are the per-user tables neither a restore set nor a
// profile export carries, with the reason. The coverage test fails for any
// table with a user_id or profile_id column that is neither claimed by a
// data type or the account and profile fields nor listed here.
var excludedUserTables = map[string]string{
	// Sessions, credentials, devices, and links to outside accounts.
	"auth_sessions":                    "login sessions",
	"api_keys":                         "API keys",
	"oauth_completions":                excludedSignIn,
	"oauth_link_tickets":               excludedSignIn,
	"oauth_pending_links":              "external sign-in links",
	"plugin_auth_identities":           "external sign-in links",
	"password_reset_tokens":            "credentials",
	"user_devices":                     "device registrations",
	"user_device_settings":             "settings bound to device registrations",
	"push_devices":                     excludedPush,
	"apple_push_installations":         excludedPush,
	"android_push_installations":       excludedPush,
	"web_push_subscriptions":           excludedPush,
	"watch_provider_connections":       "watch-provider credentials",
	"watch_provider_auth_sessions":     "watch-provider credentials",
	"history_import_connect_sessions":  excludedHistoryImport,
	"history_import_plex_sessions":     excludedHistoryImport,
	"history_import_runs":              "history-import run records",
	"webhook_sync_connections":         "webhook credentials",
	"notification_webhooks":            "webhook destinations and secrets",
	"notification_discord_prefs":       excludedDiscord,
	"notification_discord_link_state":  excludedDiscord,
	"notification_email_prefs":         "an email destination must be verified on this server",
	"notification_email_verifications": "email verification state",
	"userdb_import_receipts":           "SQLite import receipts of this server",
	// Downloads, playback, and logs.
	tableDownloads:                       excludedDownloads,
	"user_downloads":                     excludedDownloads,
	"download_subscriptions":             excludedDownloads,
	"download_storage_events":            excludedDownloads,
	"playback_connections":               excludedPlayback,
	"playback_route_events":              excludedPlaybackLogs,
	"playback_session_lifecycle":         excludedPlaybackLogs,
	"playback_sessions_sync":             excludedPlayback,
	"playback_shuffles":                  excludedPlayback,
	"playback_v3_attempts":               excludedPlaybackLogs,
	"jellycompat_playback_sessions":      excludedPlayback,
	"jellycompat_sessions":               "login sessions",
	"admin_playback_history":             excludedPlaybackLogs,
	"activity_log":                       excludedLogs,
	"api_client_usage_hourly":            "API usage summaries of activity logs",
	"operational_logs":                   excludedLogs,
	"policy_decisions":                   excludedLogs,
	"marker_edit_audit":                  excludedLogs,
	"client_diagnostic_reports":          excludedLogs,
	"notification_deliveries":            "notification delivery log",
	"notification_inbox_clocks":          "notification delivery state",
	"watch_together_suggestion_receipts": "watch-together receipts",
	// Derived state rebuilt from the data that is carried.
	"user_aggregates":                          "derived; rebuilt by the reconciler",
	"user_last_activity":                       "derived from activity",
	"progress_bootstrap_snapshots":             "derived from watch progress",
	"user_progress_sync_state":                 "client progress-sync state",
	"recommendation_cache":                     excludedRecommendation,
	"user_taste_clusters":                      excludedRecommendation,
	"user_taste_profiles":                      excludedRecommendation,
	"profile_series_interest":                  "derived from favorites, watchlist, and progress",
	"user_personal_collection_poster_variants": excludedCollage,
	"user_personal_collection_smart_collages":  excludedCollage,
	"user_collection_revisions":                excludedRevision,
	"user_collection_order_revisions":          excludedRevision,
	"user_setting_mutations":                   "setting write idempotency records",
	"user_setting_migration_rejects":           "settings migration records",
	// Data that names this server, or that the 1.0 restore does not cover.
	"jellycompat_displayprefs":         "Jellyfin client display state",
	"user_profile_onboarding":          "a restored profile repeats onboarding tours",
	"media_request_follows":            "media requests are not restored",
	"request_user_limits":              "request limits are administered per server",
	"admin_dashboard_layouts":          excludedDashboard,
	"admin_dashboard_layout_revisions": excludedDashboard,
	"abs_bookmarks":                    excludedBooks,
	"abs_playback_sessions":            excludedBooks,
	"abs_rss_feeds":                    excludedBooks,
	"abs_sessions":                     excludedBooks,
	"ebook_reader_annotations":         excludedBooks,
	"ebook_reader_config":              excludedBooks,
	"ebook_reader_progress":            excludedBooks,
}

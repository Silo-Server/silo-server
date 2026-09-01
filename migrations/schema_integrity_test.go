package migrations

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// The three migrations in this release are load-bearing in ways a reader cannot
// see from the file name: the ON DELETE action chosen per table decides whether
// deleting an account destroys or merely detaches a row, each ADD CONSTRAINT is
// only appliable because a sweep for orphans runs first, and the two backfill
// values decide what a pre-existing NULL becomes forever. These tests pin all of
// that as exact text, so changing an action, dropping a sweep, or reordering the
// pair fails here instead of on someone's database.

const (
	fkLeadingIndexesMigration = "sql/20260901161657_fk_leading_indexes.sql"
	userFKIntegrityMigration  = "sql/20260901161700_user_fk_integrity.sql"
	usersNotNullMigration     = "sql/20260901161702_users_role_enabled_not_null.sql"
)

var sqlLineComment = regexp.MustCompile(`(?m)--.*$`)

// readMigrationSections returns the Up and Down halves of a migration with line
// comments removed and whitespace collapsed, so assertions can quote statements
// as single-line SQL and compare positions without prose interfering.
func readMigrationSections(t *testing.T, name string) (up, down string) {
	t.Helper()
	raw, err := FS.ReadFile(name)
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	// Strip goose annotations first; they are themselves SQL comments.
	body := string(raw)
	parts := strings.SplitN(body, "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatalf("migration %s missing goose Down section", name)
	}
	normalize := func(s string) string {
		return strings.Join(strings.Fields(sqlLineComment.ReplaceAllString(s, "")), " ")
	}
	return normalize(parts[0]), normalize(parts[1])
}

// sweepKind is how a table's pre-existing orphans are dealt with before its
// foreign key can be validated.
type sweepKind int

const (
	// sweepDeleteNotNull deletes every row whose non-nullable user column points
	// at an account that no longer exists.
	sweepDeleteNotNull sweepKind = iota
	// sweepDeleteNullable deletes orphans but skips rows where the column is
	// NULL, because the column is nullable and NULL is a legal value.
	sweepDeleteNullable
	// sweepUpdateSetNull keeps the row and nulls the dangling attribution.
	sweepUpdateSetNull
)

type userFKContract struct {
	table      string
	column     string
	constraint string
	onDelete   string
	sweep      sweepKind
}

// The complete contract of 20260901161700_user_fk_integrity: seventeen
// constraints, the ON DELETE action each one carries, and the sweep that has to
// precede it.
var userFKContracts = []userFKContract{
	{"push_devices", "user_id", "push_devices_user_id_fkey", "CASCADE", sweepDeleteNotNull},
	{"web_push_subscriptions", "user_id", "web_push_subscriptions_user_id_fkey", "CASCADE", sweepDeleteNotNull},
	{"notification_webhooks", "user_id", "notification_webhooks_user_id_fkey", "CASCADE", sweepDeleteNotNull},
	{"notification_email_prefs", "user_id", "notification_email_prefs_user_id_fkey", "CASCADE", sweepDeleteNotNull},
	{"notification_discord_prefs", "user_id", "notification_discord_prefs_user_id_fkey", "CASCADE", sweepDeleteNotNull},
	{"notification_discord_link_state", "user_id", "notification_discord_link_state_user_id_fkey", "CASCADE", sweepDeleteNotNull},
	{"notification_deliveries", "user_id", "notification_deliveries_user_id_fkey", "CASCADE", sweepDeleteNotNull},
	{"profile_series_interest", "user_id", "profile_series_interest_user_id_fkey", "CASCADE", sweepDeleteNotNull},
	{"user_audio_preferences", "user_id", "user_audio_preferences_user_id_fkey", "CASCADE", sweepDeleteNotNull},
	{"recommendation_cache", "user_id", "recommendation_cache_user_id_fkey", "CASCADE", sweepDeleteNotNull},
	{"playback_sessions_sync", "user_id", "playback_sessions_sync_user_id_fkey", "CASCADE", sweepDeleteNotNull},
	{"jellycompat_sessions", "streamapp_user_id", "jellycompat_sessions_streamapp_user_id_fkey", "CASCADE", sweepDeleteNotNull},
	{"admin_jobs", "created_by_user_id", "admin_jobs_created_by_user_id_fkey", "CASCADE", sweepDeleteNotNull},
	// Nullable but CASCADE on purpose: the column is what makes the session an
	// impersonation, so nulling it would quietly convert a live impersonation
	// into an ordinary session for the impersonated user.
	{"auth_sessions", "impersonator_user_id", "auth_sessions_impersonator_user_id_fkey", "CASCADE", sweepDeleteNullable},
	{"activity_log", "impersonator_user_id", "activity_log_impersonator_user_id_fkey", "SET NULL", sweepUpdateSetNull},
	{"subtitle_ai_jobs", "requested_by", "subtitle_ai_jobs_requested_by_fkey", "SET NULL", sweepUpdateSetNull},
	{"metadata_translation_jobs", "requested_by", "metadata_translation_jobs_requested_by_fkey", "SET NULL", sweepUpdateSetNull},
}

func (c userFKContract) addConstraintSQL() string {
	return fmt.Sprintf(
		"ALTER TABLE public.%s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES public.users(id) ON DELETE %s;",
		c.table, c.constraint, c.column, c.onDelete,
	)
}

func (c userFKContract) dropConstraintSQL() string {
	return fmt.Sprintf("ALTER TABLE public.%s DROP CONSTRAINT IF EXISTS %s;", c.table, c.constraint)
}

func (c userFKContract) sweepSQL() string {
	missingParent := fmt.Sprintf("NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.%s)", c.column)
	switch c.sweep {
	case sweepDeleteNotNull:
		return fmt.Sprintf("DELETE FROM public.%s t WHERE %s;", c.table, missingParent)
	case sweepDeleteNullable:
		return fmt.Sprintf("DELETE FROM public.%s t WHERE t.%s IS NOT NULL AND %s;", c.table, c.column, missingParent)
	case sweepUpdateSetNull:
		return fmt.Sprintf("UPDATE public.%s t SET %s = NULL WHERE t.%s IS NOT NULL AND %s;",
			c.table, c.column, c.column, missingParent)
	default:
		return ""
	}
}

func TestUserFKIntegrityMigrationContract(t *testing.T) {
	up, down := readMigrationSections(t, userFKIntegrityMigration)

	if got, want := strings.Count(up, "ADD CONSTRAINT"), len(userFKContracts); got != want {
		t.Fatalf("up adds %d constraints, contract lists %d: update this test alongside the migration", got, want)
	}
	if got, want := strings.Count(down, "DROP CONSTRAINT"), len(userFKContracts); got != want {
		t.Fatalf("down drops %d constraints, contract lists %d", got, want)
	}

	for _, c := range userFKContracts {
		add := c.addConstraintSQL()
		addAt := strings.Index(up, add)
		if addAt < 0 {
			t.Errorf("%s: up missing exact constraint %q", c.table, add)
			continue
		}

		// The sweep is what makes ADD CONSTRAINT appliable on an upgraded
		// database that already holds orphans, and its shape encodes whether
		// those rows are destroyed or merely detached. Both the text and the
		// ordering are part of the contract.
		sweep := c.sweepSQL()
		sweepAt := strings.Index(up, sweep)
		if sweepAt < 0 {
			t.Errorf("%s: up missing exact orphan sweep %q", c.table, sweep)
			continue
		}
		if sweepAt > addAt {
			t.Errorf("%s: orphan sweep runs after ADD CONSTRAINT; the constraint cannot validate", c.table)
		}

		if drop := c.dropConstraintSQL(); !strings.Contains(down, drop) {
			t.Errorf("%s: down missing %q", c.table, drop)
		}
	}

	// A SET NULL action on a column the sweep deletes from (or vice versa) would
	// be internally inconsistent, so pin the pairing itself.
	for _, c := range userFKContracts {
		if c.onDelete == "SET NULL" && c.sweep != sweepUpdateSetNull {
			t.Errorf("%s: SET NULL constraint must pair with an UPDATE ... SET NULL sweep", c.table)
		}
		if c.sweep == sweepUpdateSetNull && c.onDelete != "SET NULL" {
			t.Errorf("%s: UPDATE ... SET NULL sweep must pair with a SET NULL constraint", c.table)
		}
	}

	// The migration runs in one transaction on purpose; NO TRANSACTION would
	// leave a half-applied set of constraints behind after a failure.
	raw, err := FS.ReadFile(userFKIntegrityMigration)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if strings.Contains(string(raw), "+goose NO TRANSACTION") {
		t.Fatal("user FK integrity migration must stay transactional")
	}
}

func TestUsersRoleEnabledNotNullMigrationContract(t *testing.T) {
	up, down := readMigrationSections(t, usersNotNullMigration)

	// The backfill values are the durable decision here: they are what every
	// pre-existing NULL becomes, irreversibly. 'user' is the fail-closed role.
	for _, step := range []struct{ backfill, setNotNull string }{
		{
			backfill:   "UPDATE public.users SET role = 'user' WHERE role IS NULL;",
			setNotNull: "ALTER TABLE public.users ALTER COLUMN role SET NOT NULL;",
		},
		{
			backfill:   "UPDATE public.users SET enabled = true WHERE enabled IS NULL;",
			setNotNull: "ALTER TABLE public.users ALTER COLUMN enabled SET NOT NULL;",
		},
	} {
		backfillAt := strings.Index(up, step.backfill)
		if backfillAt < 0 {
			t.Errorf("up missing exact backfill %q", step.backfill)
			continue
		}
		notNullAt := strings.Index(up, step.setNotNull)
		if notNullAt < 0 {
			t.Errorf("up missing %q", step.setNotNull)
			continue
		}
		if backfillAt > notNullAt {
			t.Errorf("backfill %q runs after SET NOT NULL; the ALTER would fail on any NULL row", step.backfill)
		}
	}

	for _, want := range []string{
		"ALTER TABLE public.users ALTER COLUMN enabled DROP NOT NULL;",
		"ALTER TABLE public.users ALTER COLUMN role DROP NOT NULL;",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("down missing %q", want)
		}
	}

	// role deliberately gains no DEFAULT: an insert that forgets the column must
	// fail rather than silently create an ordinary account.
	if strings.Contains(up, "ALTER COLUMN role SET DEFAULT") {
		t.Error("up must not give users.role a DEFAULT")
	}
}

// fkLeadingIndexes is the complete set this release creates. Two of them --
// idx_page_sections_library_id and
// idx_watch_provider_scrobble_sessions_connection_id -- exist because the only
// index leading on those FK columns is partial, and a partial index does not
// cover the rows a cascade visits. Removing either reintroduces a sequential
// scan per parent delete, so the list is pinned.
var fkLeadingIndexes = []string{
	"idx_abs_playback_sessions_content_id",
	"idx_abs_playback_sessions_media_file_id",
	"idx_abs_rss_feeds_library_item_id",
	"idx_admin_jobs_created_by_user_id",
	"idx_auth_sessions_user_id",
	"idx_autoscan_connections_request_integration_id",
	"idx_autoscan_sources_connection_id",
	"idx_autoscan_webhook_deliveries_source_id",
	"idx_device_login_requests_approved_by_user_id",
	"idx_device_login_requests_auth_session_id",
	"idx_downloaded_subtitles_downloaded_by",
	"idx_downloads_media_file_id",
	"idx_intro_season_analysis_state_media_folder_id",
	"idx_invitations_accepted_user_id",
	"idx_invitations_access_group_id",
	"idx_invitations_invited_by",
	"idx_invite_codes_created_by",
	"idx_jellycompat_sessions_streamapp_user_id",
	"idx_library_collection_items_media_item_id",
	"idx_library_collection_libraries_group_id",
	"idx_library_provider_chains_plugin_installation_id",
	"idx_literary_work_match_decisions_created_by",
	"idx_literary_works_primary_cover_content_id",
	"idx_marker_edit_audit_api_key_id",
	"idx_marker_edit_audit_impersonator_user_id",
	"idx_media_group_overrides_created_by_user_id",
	"idx_media_group_overrides_updated_by_user_id",
	"idx_media_identity_overrides_created_by_user_id",
	"idx_media_identity_overrides_updated_by_user_id",
	"idx_media_request_events_actor_user_id",
	"idx_media_request_targets_integration_id",
	"idx_media_root_overrides_created_by_user_id",
	"idx_media_root_overrides_updated_by_user_id",
	"idx_metadata_translation_jobs_requested_by",
	"idx_notification_deliveries_release_event_id",
	"idx_notification_discord_link_state_user_id",
	"idx_notification_webhooks_user_id",
	"idx_page_sections_library_id",
	"idx_playback_route_events_user_id",
	"idx_playback_sessions_sync_user_id",
	"idx_playback_v3_attempts_effective_media_file_id",
	"idx_playback_v3_attempts_requested_media_file_id",
	"idx_playback_v3_attempts_user_id",
	"idx_plugin_auth_identities_user_id",
	"idx_plugin_installations_repository_id",
	"idx_policy_document_versions_created_by_user_id",
	"idx_policy_documents_active_version_id",
	"idx_profile_series_interest_user_id",
	"idx_push_devices_user_id",
	"idx_subtitle_ai_jobs_requested_by",
	"idx_user_profile_allowed_libraries_library_id",
	"idx_watch_provider_auth_sessions_user_id",
	"idx_watch_provider_connections_user_id",
	"idx_watch_provider_scrobble_sessions_connection_id",
	"idx_watch_together_rooms_host_user_id",
	"idx_watch_together_suggestions_suggester_user_id",
	"idx_web_push_subscriptions_user_id",
}

func TestFKLeadingIndexesMigrationContract(t *testing.T) {
	raw, err := FS.ReadFile(fkLeadingIndexesMigration)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(raw)

	// CONCURRENTLY cannot run inside a transaction, and the recovery block that
	// drops invalid leftovers is only reachable because of it.
	if !strings.HasPrefix(migration, "-- +goose NO TRANSACTION") {
		t.Fatal("index migration must declare NO TRANSACTION")
	}

	parts := strings.SplitN(migration, "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("migration missing goose Down section")
	}
	up, down := parts[0], parts[1]

	// The recovery list is the array of names the DO block drops if an earlier
	// CONCURRENTLY build left them invalid; an index missing from it can never
	// be rebuilt, because CREATE INDEX IF NOT EXISTS skips the invalid leftover.
	_, recovery, ok := strings.Cut(up, "ARRAY[")
	if !ok {
		t.Fatal("up missing the invalid-leftover recovery array")
	}
	recovery, _, ok = strings.Cut(recovery, "])")
	if !ok {
		t.Fatal("invalid-leftover recovery array is unterminated")
	}

	for _, name := range fkLeadingIndexes {
		if !strings.Contains(recovery, "'"+name+"'") {
			t.Errorf("%s missing from the invalid-leftover recovery list", name)
		}
		if !strings.Contains(up, "CREATE INDEX CONCURRENTLY IF NOT EXISTS "+name+"\n") {
			t.Errorf("%s is not created concurrently", name)
		}
		if !strings.Contains(down, "DROP INDEX CONCURRENTLY IF EXISTS public."+name+";") {
			t.Errorf("%s is not dropped by the down migration", name)
		}
	}

	// Counts, so an index added to the migration without being added here (or
	// removed from one of the three lists) fails rather than passing silently.
	for _, count := range []struct {
		what string
		got  int
	}{
		{"recovery-list entries", strings.Count(recovery, "'idx_")},
		{"CREATE INDEX statements", strings.Count(up, "CREATE INDEX CONCURRENTLY IF NOT EXISTS ")},
		{"DROP INDEX statements", strings.Count(down, "DROP INDEX CONCURRENTLY IF EXISTS public.")},
	} {
		if count.got != len(fkLeadingIndexes) {
			t.Errorf("%s: got %d, want %d", count.what, count.got, len(fkLeadingIndexes))
		}
	}

	// Every index here exists to serve a foreign key check, which only uses an
	// index whose predicate provably holds for the rows it is proving absent. A
	// WHERE clause on any of them would silently defeat the migration.
	for _, chunk := range strings.Split(up, "CREATE INDEX CONCURRENTLY IF NOT EXISTS ")[1:] {
		statement, _, _ := strings.Cut(chunk, ";")
		if strings.Contains(strings.ToUpper(statement), "WHERE") {
			t.Errorf("FK leading index must not be partial: %q", strings.Join(strings.Fields(statement), " "))
		}
	}
}

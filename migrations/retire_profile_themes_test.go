package migrations

import (
	"slices"
	"testing"
)

const retireProfileThemesMigration = "20260926233851_retire_profile_themes"

// The migration deletes every stored profile theme choice at every scope, and
// nothing else: other appearance settings and every server setting, including
// the admin theme overrides and the retired branding rows /api/v1 still
// reports, stay.
func TestRetireProfileThemesMigrationDeletesOnlyRetiredRowsPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE server_settings (key text PRIMARY KEY, value text NOT NULL);
CREATE TABLE user_setting_values (
    id         bigserial PRIMARY KEY,
    user_id    integer NOT NULL,
    key        text NOT NULL,
    scope      text NOT NULL,
    profile_id text,
    device_id  text,
    value      jsonb NOT NULL
);
INSERT INTO server_settings (key, value) VALUES
    ('branding.default_theme', 'cinema-light'),
    ('branding.wordmark_light_ref', 'aaaa.webp'),
    ('branding.mark_light_ref', 'bbbb.webp'),
    ('branding.wordmark_ref', 'cccc.webp'),
    ('branding.accent_color', '#5bc39d'),
    ('ui.admin_theme_vars', '{"--primary":"#fff"}'),
    ('ui.admin_custom_css', 'body{}'),
    ('theme.catalog_url', 'https://example.test/catalog.json');
INSERT INTO user_setting_values (user_id, key, scope, profile_id, device_id, value) VALUES
    (1, 'ui.theme', 'profile', 'p1', NULL, '"cinema-light"'),
    (1, 'ui.theme', 'profile_device', 'p1', 'd1', '"cobalt-studio"'),
    (1, 'ui.custom_theme_vars', 'profile', 'p1', NULL, '{"--primary":"#000"}'),
    (1, 'ui.custom_css', 'profile', 'p1', NULL, '"body{}"'),
    (2, 'ui.theme', 'profile', 'p2', NULL, '"oxblood-noir"'),
    (1, 'ui.text_scale', 'profile', 'p1', NULL, '"large"'),
    (1, 'ui.high_contrast', 'profile_device', 'p1', 'd1', 'true'),
    (2, 'playback.preferred_quality', 'profile', 'p2', NULL, '"1080p"');`)

	keys := func(query string) []string {
		t.Helper()
		rows, err := tx.Query(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				t.Fatal(err)
			}
			got = append(got, key)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}

	migrationExec(t, tx, adminMigrationSQL(t, retireProfileThemesMigration, schema, false))

	wantValues := []string{"playback.preferred_quality", "ui.high_contrast", "ui.text_scale"}
	if got := keys("SELECT key FROM user_setting_values ORDER BY key"); !slices.Equal(got, wantValues) {
		t.Fatalf("user_setting_values keys = %v, want %v", got, wantValues)
	}
	// server_settings is untouched: the frozen /api/v1 branding response still
	// reports the retired default theme and light-logo references.
	wantSettings := []string{"branding.accent_color", "branding.default_theme", "branding.mark_light_ref", "branding.wordmark_light_ref", "branding.wordmark_ref", "theme.catalog_url", "ui.admin_custom_css", "ui.admin_theme_vars"}
	if got := keys("SELECT key FROM server_settings ORDER BY key"); !slices.Equal(got, wantSettings) {
		t.Fatalf("server_settings keys = %v, want %v", got, wantSettings)
	}

	// The rollback is a no-op, and re-running the migration changes nothing.
	migrationExec(t, tx, adminMigrationSQL(t, retireProfileThemesMigration, schema, true))
	migrationExec(t, tx, adminMigrationSQL(t, retireProfileThemesMigration, schema, false))
	if got := keys("SELECT key FROM user_setting_values ORDER BY key"); !slices.Equal(got, wantValues) {
		t.Fatalf("re-run user_setting_values keys = %v, want %v", got, wantValues)
	}
}

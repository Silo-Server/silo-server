package migrations

import "testing"

// Only the old default's rows, home rows of the trending type that still
// hold its text, get the key; Down leaves every key in place.
func TestTrendingOnServerTitleKeyMigrationPostgres(t *testing.T) {
	const migration = "20261109145651_key_trending_on_server_section_title"
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `CREATE TABLE page_sections (
    id text PRIMARY KEY,
    scope text NOT NULL,
    library_id integer,
    section_type text NOT NULL,
    title text NOT NULL,
    title_key text NOT NULL DEFAULT ''
);
INSERT INTO page_sections (id, scope, library_id, section_type, title, title_key) VALUES
    ('old-default', 'home', NULL, 'trending_on_server', 'Trending on Server', ''),
    ('renamed', 'home', NULL, 'trending_on_server', 'Hot Right Now', ''),
    ('other-type', 'home', NULL, 'popular_on_server', 'Trending on Server', ''),
    ('library-page', 'library', 7, 'trending_on_server', 'Trending on Server', ''),
    ('already-keyed', 'home', NULL, 'trending_on_server', 'Trending on Server', 'home.section.trendingOnServer')`)

	keys := func() map[string]string {
		t.Helper()
		rows, err := tx.Query(t.Context(), `SELECT id, title_key FROM page_sections`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var id, key string
			if err := rows.Scan(&id, &key); err != nil {
				t.Fatal(err)
			}
			got[id] = key
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}
	const key = "home.section.trendingOnServer"
	want := map[string]string{"old-default": key, "renamed": "", "other-type": "", "library-page": "", "already-keyed": key}

	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))
	for id, k := range keys() {
		if k != want[id] {
			t.Errorf("after up, %s key = %q, want %q", id, k, want[id])
		}
	}
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, true))
	for id, k := range keys() {
		if k != want[id] {
			t.Errorf("after down, %s key = %q, want %q", id, k, want[id])
		}
	}
}

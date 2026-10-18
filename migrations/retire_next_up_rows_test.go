package migrations

import (
	"slices"
	"testing"
)

const retireNextUpRowsMigration = "20261018213122_retire_next_up_rows"

// The migration deletes the Next Up rows admins and profiles added, keeps
// every other row and override as stored, including one with no recipe
// fields, and deletes a profile's page setting it leaves empty.
func TestRetireNextUpRowsMigrationPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE page_sections (id text PRIMARY KEY, section_type text NOT NULL);
CREATE TABLE user_settings (user_id integer NOT NULL, key text NOT NULL, value text NOT NULL);

INSERT INTO page_sections VALUES ('admin-next-up', 'next_up'), ('admin-continue', 'continue_watching');
INSERT INTO user_settings (user_id, key, value) VALUES
    (1, 'section_overrides:home:', '[{"ID":"deck","SectionID":"","UserSectionType":"next_up"},{"ID":"gems","SectionID":"","UserSectionType":"hidden_gems"},{"ID":"moved","SectionID":"admin-next-up","Position":3},{"ID":"bare","Hidden":true}]'),
    (1, 'section_overrides:library:1', '[{"ID":"legacy","SectionType":"next_up","UserSectionType":""}]'),
    (2, 'section_overrides:home:', '[ {"ID":"untouched", "SectionID":"", "UserSectionType":"recently_added", "UserTitle":"next_up"} ]'),
    (2, 'section_overrides:library:2', 'not json, mentions next_up'),
    (2, 'ui.next_up_mode', 'separate');`)

	values := func(query string) []string {
		t.Helper()
		rows, err := tx.Query(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
			got = append(got, value)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}
	check := func(label, query string, want ...string) {
		t.Helper()
		if got := values(query); !slices.Equal(got, want) {
			t.Fatalf("%s = %q, want %q", label, got, want)
		}
	}
	verify := func() {
		t.Helper()
		check("page sections", `SELECT id FROM page_sections ORDER BY id`, "admin-continue")
		check("override IDs",
			`SELECT user_id || ' ' || key || ' ' || string_agg(e->>'ID', ',' ORDER BY ord)
			   FROM user_settings, jsonb_array_elements(value::jsonb) WITH ORDINALITY AS a(e, ord)
			  WHERE key LIKE 'section\_overrides:%' AND value LIKE '[%'
			  GROUP BY user_id, key ORDER BY user_id, key`,
			"1 section_overrides:home: gems,moved,bare",
			"2 section_overrides:home: untouched")
		check("unchanged values",
			`SELECT value FROM user_settings WHERE user_id = 2 ORDER BY key`,
			`[ {"ID":"untouched", "SectionID":"", "UserSectionType":"recently_added", "UserTitle":"next_up"} ]`,
			"not json, mentions next_up",
			"separate")
	}

	migrationExec(t, tx, adminMigrationSQL(t, retireNextUpRowsMigration, schema, false))
	verify()

	// The rollback restores nothing, and re-running the migration changes
	// nothing.
	migrationExec(t, tx, adminMigrationSQL(t, retireNextUpRowsMigration, schema, true))
	migrationExec(t, tx, adminMigrationSQL(t, retireNextUpRowsMigration, schema, false))
	verify()
}

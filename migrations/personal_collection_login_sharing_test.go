package migrations

import (
	"maps"
	"slices"
	"testing"
)

const personalCollectionLoginSharingMigration = "20261003235347_personal_collection_login_sharing"

// The #1615 migration keeps a collection shared only when its allow list
// covers every profile of its login, marks native rows, flattens each
// creator's order (group order, then position, ungrouped last) and deletes
// the groups. Audiobookshelf rows, which never had allow-list rows, stay
// untouched and non-native.
func TestPersonalCollectionLoginSharingPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE user_profiles (user_id integer NOT NULL, id text NOT NULL, PRIMARY KEY (user_id, id));
CREATE TABLE user_collection_groups (
    user_id integer NOT NULL,
    id text NOT NULL,
    name text NOT NULL,
    sort_order integer NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, id)
);
CREATE TABLE user_personal_collections (
    user_id integer NOT NULL,
    id text NOT NULL,
    creator_profile_id text NOT NULL,
    collection_type text NOT NULL DEFAULT 'manual',
    is_shared boolean NOT NULL DEFAULT false,
    sort_order integer NOT NULL DEFAULT 0,
    group_id text,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, id),
    FOREIGN KEY (user_id, group_id) REFERENCES user_collection_groups (user_id, id) ON DELETE SET NULL (group_id)
);
CREATE TABLE user_personal_collection_profiles (
    user_id integer NOT NULL,
    collection_id text NOT NULL,
    profile_id text NOT NULL,
    PRIMARY KEY (user_id, collection_id, profile_id)
);
INSERT INTO user_profiles VALUES (1, 'p1'), (1, 'p2'), (1, 'p3'), (2, 'q1');
INSERT INTO user_collection_groups VALUES
    (1, 'g_b', 'B', 0), (1, 'g_a', 'A', 0), (1, 'g_c', 'C', 1), (2, 'g_q', 'Q', 0);
INSERT INTO user_personal_collections (user_id, id, creator_profile_id, collection_type, is_shared, sort_order, group_id, created_at) VALUES
    (1, 'all',          'p1', 'manual',   true,  0, 'g_b', '2020-01-01'),
    (1, 'subset',       'p1', 'smart',    true,  5, 'g_a', '2020-01-02'),
    (1, 'stale',        'p1', 'manual',   true,  2, 'g_a', '2020-01-03'),
    (1, 'private',      'p1', 'manual',   false, 0, NULL,  '2020-01-04'),
    (1, 'private_late', 'p1', 'mdblist',  false, 1, NULL,  '2020-01-05'),
    (1, 'creator_only', 'p2', 'manual',   true,  7, NULL,  '2020-01-06'),
    (1, 'implied',      'p3', 'manual',   true,  0, NULL,  '2020-01-06'),
    (1, 'p2_grouped',   'p2', 'manual',   false, 3, 'g_c', '2020-01-07'),
    (1, 'abs_public',   'p1', 'playlist', true,  9, NULL,  '2020-01-08'),
    (1, 'abs_private',  'p2', 'smart',    false, 4, NULL,  '2020-01-09'),
    (2, 'solo',         'q1', 'manual',   true,  3, 'g_q', '2020-01-10');
INSERT INTO user_personal_collection_profiles VALUES
    (1, 'all', 'p1'), (1, 'all', 'p2'), (1, 'all', 'p3'),
    (1, 'subset', 'p1'), (1, 'subset', 'p2'),
    (1, 'stale', 'p1'), (1, 'stale', 'p2'), (1, 'stale', 'p3'), (1, 'stale', 'p_deleted'),
    (1, 'private', 'p1'),
    (1, 'private_late', 'p1'),
    (1, 'creator_only', 'p2'),
    (1, 'implied', 'p1'), (1, 'implied', 'p2'),
    (1, 'p2_grouped', 'p2'),
    (2, 'solo', 'q1');`)

	type row struct {
		Native, Shared bool
		SortOrder      int
		Grouped        bool
	}
	read := func() map[string]row {
		t.Helper()
		rows, err := tx.Query(t.Context(), `SELECT id, native, is_shared, sort_order, group_id IS NOT NULL FROM user_personal_collections`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]row{}
		for rows.Next() {
			var id string
			var r row
			if err := rows.Scan(&id, &r.Native, &r.Shared, &r.SortOrder, &r.Grouped); err != nil {
				t.Fatal(err)
			}
			got[id] = r
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}

	up := adminMigrationSQL(t, personalCollectionLoginSharingMigration, schema, false)
	migrationExec(t, tx, up)

	want := map[string]row{
		// p1: group A (stale, subset), group B (all), then ungrouped.
		"stale":        {Native: true, Shared: true, SortOrder: 0},
		"subset":       {Native: true, Shared: false, SortOrder: 1},
		"all":          {Native: true, Shared: true, SortOrder: 2},
		"private":      {Native: true, Shared: false, SortOrder: 3},
		"private_late": {Native: true, Shared: false, SortOrder: 4},
		// p2: group C first, then ungrouped. Shared with nobody else: private.
		"p2_grouped":   {Native: true, Shared: false, SortOrder: 0},
		"creator_only": {Native: true, Shared: false, SortOrder: 1},
		// Every other profile is listed; the creator is implied.
		"implied": {Native: true, Shared: true, SortOrder: 0},
		// Audiobookshelf rows keep every stored value.
		"abs_public":  {Native: false, Shared: true, SortOrder: 9},
		"abs_private": {Native: false, Shared: false, SortOrder: 4},
		// The only profile of its login covers the whole login: stays shared.
		"solo": {Native: true, Shared: true, SortOrder: 0},
	}
	if got := read(); !maps.Equal(got, want) {
		t.Fatalf("after up:\n got  %+v\n want %+v", got, want)
	}
	var groups int
	if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM user_collection_groups`).Scan(&groups); err != nil {
		t.Fatal(err)
	}
	if groups != 0 {
		t.Fatalf("collection groups after up = %d, want 0", groups)
	}

	// Down rewrites the allow lists the previous code reads from is_shared.
	migrationExec(t, tx, adminMigrationSQL(t, personalCollectionLoginSharingMigration, schema, true))
	audience := func(id string) []string {
		t.Helper()
		rows, err := tx.Query(t.Context(), `SELECT profile_id FROM user_personal_collection_profiles WHERE collection_id = $1 ORDER BY profile_id`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				t.Fatal(err)
			}
			out = append(out, p)
		}
		return out
	}
	for id, wantAudience := range map[string][]string{
		"all":          {"p1", "p2", "p3"},
		"stale":        {"p1", "p2", "p3"},
		"subset":       {"p1"},
		"creator_only": {"p2"},
		"implied":      {"p1", "p2", "p3"},
		"private":      {"p1"},
		"abs_public":   nil,
		"solo":         {"q1"},
	} {
		if got := audience(id); !slices.Equal(got, wantAudience) {
			t.Fatalf("allow list of %s after down = %v, want %v", id, got, wantAudience)
		}
	}
	var nativeColumns int
	if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'user_personal_collections' AND column_name = 'native'`, schema).Scan(&nativeColumns); err != nil {
		t.Fatal(err)
	}
	if nativeColumns != 0 {
		t.Fatal("native column survived the down migration")
	}

	// Up again reaches the same sharing state.
	migrationExec(t, tx, up)
	got := read()
	for id, w := range want {
		if got[id].Native != w.Native || got[id].Shared != w.Shared {
			t.Fatalf("after down and up, %s = %+v, want native=%t shared=%t", id, got[id], w.Native, w.Shared)
		}
	}
}

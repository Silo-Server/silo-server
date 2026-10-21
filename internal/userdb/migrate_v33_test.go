package userdb

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
)

// v33 rewrites the section overrides that point at Trakt: Trakt trending
// becomes TMDB weekly trending, a row the profile added for it is hidden, a
// collection row drops its Trakt marker, and every other override, including
// one whose config does not parse, is left as it was.
func TestMigrateToV33RetiresTraktSectionOverrides(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO profile_section_overrides (id, profile_id, scope, section_id, hidden, config, is_user_added, user_section_type, user_config, created_at, updated_at) VALUES
    ('own-trending', 'p1', 'home', NULL, 0, NULL, 1, 'trending_discover', '{"source":"trakt","window":"day"}', 'now', 'now'),
    ('linked-trending', 'p1', 'home', 'admin-trending', 0, '{"source":"trakt"}', 0, NULL, NULL, 'now', 'now'),
    ('linked-collection', 'p1', 'home', 'admin-collection', 0, '{"library_collection_id":"c1","source_provider":"trakt"}', 0, NULL, NULL, 'now', 'now'),
    ('tmdb-trending', 'p1', 'home', NULL, 0, NULL, 1, 'trending_discover', '{"source":"tmdb","window":"day"}', 'now', 'now'),
    ('unparsed', 'p1', 'home', NULL, 0, NULL, 1, 'custom_filter', '{"title":"trakt', 'now', 'now');
PRAGMA user_version = 32;`); err != nil {
		t.Fatalf("seed v32 store: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if version, err := userVersion(db); err != nil || version != schemaVersion {
		t.Fatalf("user_version = %d, %v; want %d", version, err, schemaVersion)
	}

	type row struct {
		hidden             bool
		config, userConfig sql.NullString
	}
	read := func(id string) row {
		t.Helper()
		var r row
		if err := db.QueryRow(`SELECT hidden, config, user_config FROM profile_section_overrides WHERE id = ?`, id).Scan(&r.hidden, &r.config, &r.userConfig); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return r
	}
	object := func(raw sql.NullString) map[string]any {
		t.Helper()
		var v map[string]any
		if err := json.Unmarshal([]byte(raw.String), &v); err != nil {
			t.Fatalf("config %q: %v", raw.String, err)
		}
		return v
	}

	own := read("own-trending")
	if !own.hidden || own.config.Valid || !reflect.DeepEqual(object(own.userConfig), map[string]any{"source": "tmdb", "window": "week"}) {
		t.Fatalf("own trending row = %+v, want hidden TMDB weekly", own)
	}
	linked := read("linked-trending")
	if linked.hidden || !reflect.DeepEqual(object(linked.config), map[string]any{"source": "tmdb", "window": "week"}) {
		t.Fatalf("linked trending row = %+v, want visible TMDB weekly", linked)
	}
	collection := read("linked-collection")
	if collection.hidden || !reflect.DeepEqual(object(collection.config), map[string]any{"library_collection_id": "c1"}) {
		t.Fatalf("collection row = %+v, want the Trakt marker dropped", collection)
	}
	if tmdb := read("tmdb-trending"); tmdb.hidden || tmdb.userConfig.String != `{"source":"tmdb","window":"day"}` {
		t.Fatalf("TMDB row = %+v, want unchanged", tmdb)
	}
	if unparsed := read("unparsed"); unparsed.hidden || unparsed.userConfig.String != `{"title":"trakt` {
		t.Fatalf("unparsed row = %+v, want unchanged", unparsed)
	}
}

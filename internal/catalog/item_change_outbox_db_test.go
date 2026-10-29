package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// drainItemChanges claims until the outbox is empty and returns the changes
// whose content IDs start with prefix. Other tests share the outbox, so their
// changes are claimed and dropped.
func drainItemChanges(t *testing.T, outbox *ItemChangeOutbox, prefix string) []string {
	t.Helper()
	var out []string
	for range 100 {
		changes, more, err := outbox.Claim(t.Context(), 500, 0, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range changes {
			if strings.HasPrefix(c.ContentID, prefix) {
				out = append(out, fmt.Sprintf("%s %s@%d", c.Change, strings.TrimPrefix(c.ContentID, prefix), c.LibraryID))
			}
		}
		if len(changes) == 0 && !more {
			return out
		}
	}
	t.Fatal("outbox never drained")
	return nil
}

// TestCatalogItemChangeOutboxDB pins what the triggers queue for each kind of
// catalog write and how Claim coalesces and spaces out what it publishes.
func TestCatalogItemChangeOutboxDB(t *testing.T) {
	pool := searchOptimizationPool(t, nil)
	ctx := t.Context()
	outbox := NewItemChangeOutbox(pool)
	p := fmt.Sprintf("ico%d-", time.Now().UnixNano())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	folder := func(kind string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type,name) VALUES ($1,$2) RETURNING id`, kind, p).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	movies, tv, books := folder("movies"), folder("series"), folder("audiobooks")
	t.Cleanup(func() { cleanupItemChangeFixture(pool, p, []int{movies, tv, books}) })

	drained := func(name string, want ...string) {
		t.Helper()
		if got := drainItemChanges(t, outbox, p); !slices.Equal(got, want) {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
	at := func(id string, library int) string { return fmt.Sprintf("%s@%d", id, library) }

	// A first scan: memberships and files coalesce into one add per item. A
	// book, an extra and a series-level file are not listed, so queue nothing,
	// and a membership the scan writes twice adds once.
	exec(`INSERT INTO media_items (content_id,type,title) VALUES ($1,'movie','M'),($2,'series','S'),($3,'audiobook','B')`, p+"m", p+"s", p+"b")
	exec(`INSERT INTO episodes (content_id,series_id,season_number,episode_number) VALUES ($1,$2,1,1)`, p+"e", p+"s")
	exec(`INSERT INTO media_extras (content_id,parent_id,kind) VALUES ($1,$2,'trailer')`, p+"x", p+"m")
	exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$4),($2,$5),($3,$6)`, p+"m", p+"s", p+"b", movies, tv, books)
	exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, p+"m", movies)
	exec(`INSERT INTO episode_libraries (episode_id,media_folder_id) VALUES ($1,$2)`, p+"e", tv)
	exec(`INSERT INTO media_files (content_id,episode_id,media_folder_id,file_path,extra_id) VALUES
		($1,NULL,$5,$6||'m1.mkv',NULL), ($1,NULL,$5,$6||'m2.mkv',NULL), ($1,NULL,$5,$6||'t.mkv',$4),
		($2,NULL,$7,$6||'s.mkv',NULL), ($2,$3,$7,$6||'e.mkv',NULL), ($8,NULL,$9,$6||'b.m4b',NULL)`,
		p+"m", p+"s", p+"e", p+"x", movies, "/"+p, tv, p+"b", books)
	drained("first scan", "added "+at("m", movies), "added "+at("s", tv), "added "+at("e", tv))

	// Routine rescans touch rows without changing what they show.
	exec(`UPDATE media_files SET probe_updated_at = now(), file_size = file_size, codec_video = codec_video WHERE file_path LIKE $1`, "/"+p+"%")
	exec(`UPDATE media_item_libraries SET first_seen_at = first_seen_at - interval '1 day' WHERE content_id LIKE $1`, p+"%")
	exec(`UPDATE episode_libraries SET first_seen_at = first_seen_at - interval '1 day' WHERE episode_id LIKE $1`, p+"%")
	drained("routine writes")

	// A re-probe and a file going missing are one files_changed; a file that
	// is already missing changing again is nothing.
	exec(`UPDATE media_files SET codec_video = 'hevc' WHERE file_path = $1`, "/"+p+"m1.mkv")
	exec(`UPDATE media_files SET missing_since = now() WHERE file_path = $1`, "/"+p+"m2.mkv")
	exec(`UPDATE media_files SET missing_since = now() - interval '1 hour', codec_video = 'av1' WHERE file_path = $1`, "/"+p+"m2.mkv")
	exec(`UPDATE media_files SET file_size = 42 WHERE file_path = $1`, "/"+p+"e.mkv")
	drained("file changes", "files_changed "+at("m", movies), "files_changed "+at("e", tv))

	// Re-keying an item cascades to its membership: the old key is removed
	// and the new one added.
	exec(`UPDATE media_items SET content_id = $2 WHERE content_id = $1`, p+"m", p+"m2")
	drained("key move", "removed "+at("m", movies), "added "+at("m2", movies))

	// A membership dropped and restored before a drain is an add, since the
	// new row may carry a new first-seen time; one added and dropped is no
	// change; one only dropped is a removal, also after the episode's own
	// files changed.
	exec(`DELETE FROM media_item_libraries WHERE content_id = $1`, p+"s")
	exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, p+"s", tv)
	exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, p+"m2", tv)
	exec(`DELETE FROM media_item_libraries WHERE content_id = $1 AND media_folder_id = $2`, p+"m2", tv)
	exec(`UPDATE media_files SET resolution = '2160p' WHERE file_path = $1`, "/"+p+"e.mkv")
	exec(`DELETE FROM episode_libraries WHERE episode_id = $1`, p+"e")
	drained("flap and removal", "added "+at("s", tv), "removed "+at("e", tv))

	// Drains are spaced out across servers: right after the drain above, one
	// within the interval claims nothing, and a later one gets what waited.
	exec(`DELETE FROM media_item_libraries WHERE content_id = $1`, p+"s")
	changes, _, err := outbox.Claim(ctx, 500, time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Errorf("a claim within the interval got %v", changes)
	}
	drained("after the interval", "removed "+at("s", tv))

	// One claim publishes at most limit keys, the bound on what a connection
	// buffers per drain, and reports that more are waiting.
	for i := range 5 {
		exec(`INSERT INTO catalog_item_change_events (content_id,library_id,change) VALUES ($1,$2,'added')`, fmt.Sprintf("%scap%d", p, i), movies)
	}
	changes, more, err := outbox.Claim(ctx, 2, 0, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) > 2 || !more {
		t.Errorf("a claim of 2 returned %d changes, more=%v", len(changes), more)
	}
	drainItemChanges(t, outbox, p)

	// Changes older than the retention are dropped unpublished.
	exec(`INSERT INTO catalog_item_change_events (content_id,library_id,change,created_at) VALUES ($1,$2,'added',now() - interval '2 days')`, p+"old", movies)
	drained("retention")
}

func cleanupItemChangeFixture(pool *pgxpool.Pool, prefix string, folders []int) {
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = ANY($1)`, folders)
	_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, folders)
	_, _ = pool.Exec(ctx, `DELETE FROM catalog_item_change_events WHERE content_id LIKE $1`, prefix+"%")
}

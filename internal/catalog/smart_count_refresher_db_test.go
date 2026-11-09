package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A scan that adds or removes a library's titles refreshes the stored count of
// each smart collection drawing from that library, including one drawing from
// every library, and leaves the others alone (#3697).
func TestSmartCountRefreshFollowsScannedLibraryDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	prefix := fmt.Sprintf("smart-count-scan-%d", time.Now().UnixNano())
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}

	var scanned, other int
	for i, target := range []*int{&scanned, &other} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("%s-%d", prefix, i)).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	repo := NewLibraryCollectionRepository(pool)
	ids := map[string]string{}
	defer func() {
		cleanup := context.Background()
		for _, id := range ids {
			_ = repo.Delete(cleanup, id)
			_, _ = pool.Exec(cleanup, `DELETE FROM library_collection_revisions WHERE collection_id=$1`, id)
		}
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id=ANY($1)`, []int{scanned, other})
	}()
	for name, query := range map[string]string{
		"scanned": fmt.Sprintf(`{"library_ids":[%d],"media_scope":"movie","match":"all","groups":[]}`, scanned),
		"other":   fmt.Sprintf(`{"library_ids":[%d],"media_scope":"movie","match":"all","groups":[]}`, other),
		"every":   `{"media_scope":"movie","match":"all","groups":[]}`,
	} {
		c, err := repo.Create(ctx, CreateLibraryCollectionInput{LibraryID: scanned, Slug: prefix + "-" + name, Title: name, CollectionType: "smart", Visibility: "visible", QueryDefinition: json.RawMessage(query)})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = c.ID
	}
	// -1 marks a count the refresh has not written.
	for _, id := range ids {
		exec(`UPDATE library_collections SET item_count_cached=-1 WHERE id=$1`, id)
	}
	counts := func() map[string]int {
		t.Helper()
		got := map[string]int{}
		for name, id := range ids {
			var n int
			if err := pool.QueryRow(ctx, `SELECT item_count_cached FROM library_collections WHERE id=$1`, id).Scan(&n); err != nil {
				t.Fatal(err)
			}
			got[name] = n
		}
		return got
	}

	refresher := &SmartCountRefresher{Pool: pool, Executor: &QueryExecutor{Pool: pool}}
	added := prefix + "-added"
	exec(`INSERT INTO media_items(content_id,type,title,status,genres) VALUES($1,'movie',$1,'released','{}')`, added)
	exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, added, scanned)
	if _, errs := refresher.RefreshLibraries(ctx, []int{scanned}); errs != 0 {
		t.Fatalf("refresh after the title was added had %d errors", errs)
	}
	if got := counts(); got["scanned"] != 1 || got["every"] < 1 || got["other"] != -1 {
		t.Fatalf("after adding a title, counts are %v; want scanned 1, every at least 1, other untouched (-1)", got)
	}

	exec(`DELETE FROM media_item_libraries WHERE content_id=$1`, added)
	if _, errs := refresher.RefreshLibraries(ctx, []int{scanned}); errs != 0 {
		t.Fatalf("refresh after the title was removed had %d errors", errs)
	}
	if got := counts(); got["scanned"] != 0 || got["other"] != -1 {
		t.Fatalf("after removing the title, counts are %v; want scanned 0, other untouched (-1)", got)
	}
}

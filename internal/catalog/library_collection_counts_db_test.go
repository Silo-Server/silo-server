package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
)

// TestViewerCollectionCountsStatementBudgetDB pins what counting server
// collections for a viewer costs: nothing for a viewer whose access narrows
// nothing, and otherwise one statement for every stored collection plus at
// most collectionCountConcurrency for the smart ones, and as many again to
// recount without the viewer's access the smart ones it can open nothing in,
// however many are listed.
func TestViewerCollectionCountsStatementBudgetDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	tracer := &statementCountTracer{}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	prefix := fmt.Sprintf("viewer-count-budget-%d", time.Now().UnixNano())
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM library_collections WHERE slug LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id=$1`, library)
	})
	var members []string
	for i, age := range []int{7, 17} {
		id := fmt.Sprintf("%s-%d", prefix, i)
		members = append(members, id)
		if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age) VALUES($1,'movie',$1,'released','{}','x',$2)`, id, age); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, library); err != nil {
			t.Fatal(err)
		}
	}

	repo := NewLibraryCollectionRepository(pool)
	const storedCount, smartCount = 6, 9
	for i := range storedCount + smartCount {
		input := CreateLibraryCollectionInput{LibraryID: library, LibraryIDs: []int{library}, Slug: fmt.Sprintf("%s-%d", prefix, i), Title: prefix, CollectionType: "manual", Visibility: "visible"}
		if i >= storedCount {
			// Distinct definitions, so no two share a read.
			input.CollectionType = "smart"
			input.QueryDefinition = json.RawMessage(fmt.Sprintf(`{"media_scope":"movie","match":"all","groups":[],"limit":%d}`, 100+i))
		}
		c, err := repo.Create(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if i < storedCount {
			for pos, id := range members {
				if err := repo.AddItem(ctx, c.ID, id, pos); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// Read back as a listing reads them, with their stored counts.
	collections, err := repo.ListByLibrary(ctx, library, ListLibraryCollectionsOptions{})
	if err != nil || len(collections) != storedCount+smartCount {
		t.Fatalf("listed %d collections (%v), want %d", len(collections), err, storedCount+smartCount)
	}

	for _, tc := range []struct {
		name      string
		access    AccessFilter
		maxStmts  int64
		wantCount int
		wantHides bool
	}{
		{"unrestricted", AccessFilter{}, 0, -1, false},
		{"rating limited", AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}, 1 + collectionCountConcurrency, 1, false},
		{"every member hidden", AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "G"}}, 1 + 2*collectionCountConcurrency, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracer.statements.Store(0)
			counts, err := repo.ViewerCollectionCounts(ctx, collections, tc.access)
			if err != nil {
				t.Fatal(err)
			}
			if got := tracer.statements.Load(); got > tc.maxStmts {
				t.Errorf("counting %d collections ran %d statements, want at most %d", len(collections), got, tc.maxStmts)
			}
			if tc.wantCount < 0 {
				if counts != nil {
					t.Errorf("counts = %v, want none", counts)
				}
				return
			}
			want := ViewerCollectionCount{Count: tc.wantCount, HidesMembers: tc.wantHides}
			for _, c := range collections {
				if got, ok := counts[c.ID]; !ok || got != want {
					t.Errorf("collection %s (%s) counts %+v, want %+v", c.ID, c.CollectionType, got, want)
				}
			}
		})
	}
}

package catalog

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCatalogSectionCursorDB(t *testing.T) {
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	prefix := fmt.Sprintf("section-cursor-%d", time.Now().UnixNano())
	var lib int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('ebooks',$1,true) RETURNING id`, prefix).Scan(&lib); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM page_sections WHERE library_id=$1`, lib)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM literary_works WHERE work_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, lib)
	}()
	ids := make([]string, 9)
	workIDs := map[string]string{}
	exec(`INSERT INTO literary_works(work_id,canonical_title,normalized_title) VALUES($1,'Shared','shared')`, prefix+"-work")
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%d", prefix, i)
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age,created_at,release_date,first_air_date) VALUES($1,'ebook',$2,'released','{}',$3,$5,'2025-01-01'::timestamptz + ($4::int/3) * interval '1 day',CASE WHEN $4::int<6 THEN '2025-02-01'::date ELSE NULL END,CASE WHEN $4::int=7 THEN '2025-03-01' ELSE '' END)`, ids[i], fmt.Sprintf("Title %d", 8-i), map[bool]string{true: "R", false: "PG"}[i == 0], i, map[bool]int{true: 17, false: 8}[i == 0])
		// Deliberately reverse library arrival: generic added_at would silently
		// change the legacy section order, which sorts catalog created_at.
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id,first_seen_at) VALUES($1,$2,'2025-03-01'::timestamptz - $3::int * interval '1 day')`, ids[i], lib, i)
		workIDs[ids[i]] = ids[i]
		if i >= 1 && i <= 3 {
			exec(`INSERT INTO literary_work_items(work_id,content_id,format_type,link_source) VALUES($1,$2,'ebook','manual')`, prefix+"-work", ids[i])
			workIDs[ids[i]] = prefix + "-work"
		}
	}
	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool))
	access := AccessFilter{AllowedLibraryIDs: []int{lib}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
	seed := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("seed", 3600))
	for _, spec := range []struct{ kind, sort string }{{"recently_added", "added_at"}, {"recently_released", "release_date"}, {"random", "random"}} {
		sectionID := prefix + "-" + spec.kind
		exec(`INSERT INTO page_sections(id,scope,library_id,section_type,title,config) VALUES($1,'library',$2,$3,'Section','{"filter_type":"ebook"}')`, sectionID, lib, spec.kind)
		for _, grouped := range []bool{false, true} {
			for _, skip := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/group=%t/skip=%t", spec.kind, grouped, skip), func(t *testing.T) {
					section := catalogPageSection{ID: sectionID, Scope: "library", LibraryID: &lib, SectionType: spec.kind, Config: []byte(`{"filter_type":"ebook"}`)}
					legacy, err := resolver.resolveSectionBrowseSource(ctx, CatalogRequest{Limit: 100, SnapshotAt: &seed}, access, section, spec.sort, "desc")
					if err != nil {
						t.Fatal(err)
					}
					var want []string
					seen := map[string]bool{}
					for _, item := range legacy.Items {
						key := item.ContentID
						if grouped {
							key = workIDs[key]
						}
						if !seen[key] {
							seen[key] = true
							want = append(want, item.ContentID)
						}
					}
					req := CatalogRequest{Source: CatalogSourceSection, Scope: "library", LibraryID: lib, SectionID: sectionID, CursorPaging: true, GroupByWork: grouped, Limit: 2, SnapshotAt: &seed, SkipTotal: skip}
					var got []string
					for range 10 {
						// Constructing a fresh resolver proves the saved seed is sufficient:
						// later requests do not depend on process-local random state.
						fresh := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool))
						page, err := fresh.Resolve(ctx, req, access)
						if err != nil {
							t.Fatal(err)
						}
						if !page.SnapshotAt.Equal(seed) {
							t.Fatal("changed section seed")
						}
						if page.TotalExact == skip || (!skip && page.Total != len(want)) {
							t.Fatalf("count: %+v want %d", page, len(want))
						}
						for _, item := range page.Items {
							got = append(got, item.ContentID)
						}
						if !page.HasMore {
							break
						}
						if page.Next == nil {
							t.Fatal("missing section keyset")
						}
						req.After = page.Next
						req.SnapshotAt = &page.SnapshotAt
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("changed legacy order: got %v want %v", got, want)
					}
					req.After = nil
					req.Seek = new(2)
					page, err := resolver.Resolve(ctx, req, access)
					if err != nil {
						t.Fatal(err)
					}
					if len(page.Items) != 2 || page.Items[0].ContentID != want[2] || page.Items[1].ContentID != want[3] {
						t.Fatalf("wrong section jump: %+v want %v", page, want[2:4])
					}
				})
			}
		}
	}
	t.Run("generated random seed and access", func(t *testing.T) {
		req := CatalogRequest{Source: CatalogSourceSection, Scope: "library", LibraryID: lib, SectionID: prefix + "-random", CursorPaging: true, Limit: 2}
		first, err := resolver.Resolve(ctx, req, access)
		if err != nil {
			t.Fatal(err)
		}
		if first.SnapshotAt.IsZero() {
			t.Fatal("missing seed")
		}
		req.SnapshotAt = &first.SnapshotAt
		again, err := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).Resolve(ctx, req, access)
		if err != nil {
			t.Fatal(err)
		}
		if first.Items[0].ContentID != again.Items[0].ContentID || first.Items[1].ContentID != again.Items[1].ContentID {
			t.Fatal("saved random seed changed first page")
		}
		denied := access
		denied.DisabledLibraryIDs = []int{lib}
		page, err := resolver.Resolve(ctx, req, denied)
		if err != nil || len(page.Items) != 0 || page.HasMore || page.Total != 0 {
			t.Fatalf("disabled library: %+v %v", page, err)
		}
	})
}

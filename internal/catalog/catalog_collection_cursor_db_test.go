package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCatalogManualCollectionCursorDB(t *testing.T) {
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
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies','catalog cursor',true) RETURNING id`).Scan(&library); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, library) }()
	prefix := fmt.Sprintf("catalog-collection-cursor-%d", time.Now().UnixNano())
	repo := NewLibraryCollectionRepository(pool)
	c, err := repo.Create(ctx, CreateLibraryCollectionInput{LibraryID: library, Slug: prefix, Title: "Cursor"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = repo.Delete(context.Background(), c.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id=$1`, c.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	}()
	var inputs []LibraryCollectionItemInput
	// The first batch contains only hidden members. The page must continue into
	// the next bounded membership window rather than declare the source empty.
	for i := range 204 {
		id := fmt.Sprintf("%s-%03d", prefix, i)
		if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age) VALUES($1,'movie',$1,'released','{}',$2,$3)`, id, map[bool]string{true: "R", false: "PG"}[i < 200], map[bool]int{true: 17, false: 8}[i < 200]); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, library); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, LibraryCollectionItemInput{MediaItemID: id})
	}
	if _, err := pool.Exec(ctx, `UPDATE media_items SET title='ZZZ Exclusive Collection Needle' WHERE content_id=$1`, inputs[203].MediaItemID); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceItems(ctx, c.ID, inputs); err != nil {
		t.Fatal(err)
	}
	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool))
	req := CatalogRequest{Source: CatalogSourceLibraryCollection, CollectionID: c.ID, CursorPaging: true, UseSourceOrder: true, Limit: 2}
	access := AccessFilter{AllowedLibraryIDs: []int{library}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
	page, err := resolver.Resolve(ctx, req, access)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ContentID != inputs[200].MediaItemID || !page.HasMore || page.Next == nil || page.CursorScope == nil {
		t.Fatalf("first accessible window: %+v", page)
	}
	req.After = page.Next
	second, err := resolver.Resolve(ctx, req, access)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 2 || second.Items[0].ContentID != inputs[202].MediaItemID || second.HasMore || second.CursorScope == nil {
		t.Fatalf("second window: %+v", second)
	}

	t.Run("seek past changed visibility retains collection scope", func(t *testing.T) {
		initial := req
		initial.After = nil
		snapshot := time.Now().UTC()
		initial.SnapshotAt = &snapshot
		observed, err := resolver.Resolve(ctx, initial, access)
		if err != nil {
			t.Fatal(err)
		}
		if observed.Total != 4 || observed.CursorScope == nil {
			t.Fatalf("observed page: %+v", observed)
		}
		revision := observed.CursorScope.Collection.Revision
		if _, err := pool.Exec(ctx, `UPDATE media_items SET content_rating='R', content_rating_age=17 WHERE content_id=ANY($1)`, []string{inputs[202].MediaItemID, inputs[203].MediaItemID}); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = pool.Exec(context.Background(), `UPDATE media_items SET content_rating='PG', content_rating_age=8 WHERE content_id=ANY($1)`, []string{inputs[202].MediaItemID, inputs[203].MediaItemID})
		}()
		current, err := repo.CollectionRevision(ctx, c.ID)
		if err != nil || current != revision {
			t.Fatalf("visibility must not mutate collection revision: %d %d %v", revision, current, err)
		}
		initial.After = observed.CursorScope
		initial.Seek = new(observed.Total - 1)
		empty, err := resolver.Resolve(ctx, initial, access)
		if err != nil {
			t.Fatal(err)
		}
		if empty.Items == nil || len(empty.Items) != 0 || empty.HasMore || empty.Next != nil || empty.CursorScope == nil || empty.CursorScope.Collection.Revision != revision || !empty.SnapshotAt.Equal(snapshot) {
			t.Fatalf("stale visible seek must return scoped empty page: %+v", empty)
		}
	})
	t.Run("seek at and beyond query cap is empty", func(t *testing.T) {
		capped := req
		capped.After = nil
		capped.Query = QueryDefinition{Sort: QuerySort{Field: "title", Order: querySortAsc}, Limit: new(2)}
		for _, position := range []int{2, 3} {
			capped.Seek = new(position)
			page, err := resolver.Resolve(ctx, capped, access)
			if err != nil {
				t.Fatal(err)
			}
			if page.Items == nil || len(page.Items) != 0 || page.HasMore || page.Next != nil || page.CursorScope == nil {
				t.Fatalf("capped seek %d: %+v", position, page)
			}
		}
		for _, position := range []int{-1, 10000001} {
			capped.Seek = new(position)
			if _, err := resolver.Resolve(ctx, capped, access); err == nil {
				t.Fatalf("invalid seek %d accepted", position)
			}
		}
	})
	t.Run("revision brackets source reads", func(t *testing.T) {
		initial := req
		initial.After = nil
		for _, phase := range []string{"before page", "after page", "rollback"} {
			t.Run(phase, func(t *testing.T) {
				revision, err := collectionFence(ctx, initial, repo.CollectionRevision)
				if err != nil {
					t.Fatal(err)
				}
				mutate := func() {
					t.Helper()
					if _, err := pool.Exec(ctx, `UPDATE library_collections SET title=title || '.' WHERE id=$1`, c.ID); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "before page" {
					mutate()
				}
				result, err := resolver.resolveLibraryMembershipQueryCursor(ctx, initial, access)
				if err != nil {
					t.Fatal(err)
				}
				if phase == "after page" {
					mutate()
				}
				if phase == "rollback" {
					tx, err := pool.Begin(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := tx.Exec(ctx, `UPDATE library_collections SET title=title || '.' WHERE id=$1`, c.ID); err != nil {
						_ = tx.Rollback(ctx)
						t.Fatal(err)
					}
					if err := tx.Rollback(ctx); err != nil {
						t.Fatal(err)
					}
				}
				_, err = finishCollectionCursor(ctx, initial, revision, repo.CollectionRevision, result, nil)
				if phase == "rollback" {
					if err != nil {
						t.Fatalf("rolled-back write invalidated cursor: %v", err)
					}
				} else if !errors.Is(err, ErrCatalogCursorChanged) {
					t.Fatalf("accepted mixed revision page: %v", err)
				}
			})
		}
	})
	t.Run("source order jump across hidden members", func(t *testing.T) {
		jumpedReq := req
		jumpedReq.After = nil
		jumpedReq.Seek = new(2)
		jumped, err := resolver.Resolve(ctx, jumpedReq, access)
		if err != nil {
			t.Fatal(err)
		}
		if len(jumped.Items) != 2 || jumped.Items[0].ContentID != inputs[202].MediaItemID {
			t.Fatalf("jump not relative to visible rows: %+v", jumped)
		}
	})
	t.Run("collection search and smart display", func(t *testing.T) {
		search := req
		search.After = nil
		search.SearchQuery = "exclusive collection needle"
		search.Query.Sort = QuerySort{Field: "title", Order: "desc"}
		found, err := resolver.Resolve(ctx, search, access)
		if err != nil {
			t.Fatal(err)
		}
		if len(found.Items) != 1 || found.Items[0].ContentID != inputs[203].MediaItemID {
			t.Fatalf("search ignored: %+v", found)
		}
		base := QueryDefinition{LibraryIDs: []int{library}}
		display := `{"match":"all","groups":[{"match":"all","rules":[{"field":"type","op":"is","value":"series"}]}]}`
		displayReq := req
		displayReq.After = nil
		empty, err := resolver.resolveSmartCollectionCursorWithDisplay(ctx, displayReq, access, base, display)
		if err != nil {
			t.Fatal(err)
		}
		if len(empty.Items) != 0 {
			t.Fatalf("display filter ignored: %+v", empty)
		}
	})
	t.Run("manual SQL sort and filter", func(t *testing.T) {
		overlay := req
		overlay.After = nil
		overlay.Query = QueryDefinition{Sort: QuerySort{Field: "title", Order: "desc"}, Groups: []QueryGroup{{Rules: []QueryRule{{Field: "content_rating", Op: "is", Value: "PG"}}}}}
		first, err := resolver.Resolve(ctx, overlay, access)
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Items) != 2 || first.Items[0].ContentID != inputs[203].MediaItemID || first.Total != 4 {
			t.Fatalf("sorted filter first: %+v", first)
		}
		overlay.After = first.Next
		next, err := resolver.Resolve(ctx, overlay, access)
		if err != nil {
			t.Fatal(err)
		}
		if len(next.Items) != 2 || next.Items[0].ContentID != inputs[201].MediaItemID || next.HasMore {
			t.Fatalf("sorted filter next: %+v", next)
		}
	})
	t.Run("smart SQL overlay preserves capped source", func(t *testing.T) {
		base := QueryDefinition{LibraryIDs: []int{library}, Sort: QuerySort{Field: "title", Order: "asc"}, Limit: new(3)}
		overlay := req
		overlay.After = nil
		overlay.Query = QueryDefinition{MediaScope: "movie", Sort: QuerySort{Field: "title", Order: "desc"}}
		first, err := resolver.resolveSmartCollectionCursor(ctx, overlay, access, base)
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Items) != 2 || first.Items[0].ContentID != inputs[202].MediaItemID || first.Total != 3 {
			t.Fatalf("smart first: %+v", first)
		}
		overlay.After = first.Next
		next, err := resolver.resolveSmartCollectionCursor(ctx, overlay, access, base)
		if err != nil {
			t.Fatal(err)
		}
		if len(next.Items) != 1 || next.Items[0].ContentID != inputs[200].MediaItemID || next.HasMore {
			t.Fatalf("smart next: %+v", next)
		}
	})

	t.Run("SQLite manual selected-store paging", func(t *testing.T) {
		sqlitePool := userdb.NewUserDBPool(userdb.PoolConfig{DataDir: t.TempDir(), MaxOpen: 2})
		provider := userdb.NewSQLiteProvider(sqlitePool)
		defer func() { _ = provider.Close() }()
		store, err := provider.ForUser(ctx, 87654)
		if err != nil {
			t.Fatal(err)
		}
		profile := "sqlite-collection-profile"
		if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: "SQLite", IsPrimary: true}); err != nil {
			t.Fatal(err)
		}
		collection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: profile, Name: "SQLite manual"})
		if err != nil {
			t.Fatal(err)
		}
		for i := range 4 {
			if err := store.AddCollectionItem(ctx, collection.ID, inputs[200+i].MediaItemID, i*10); err != nil {
				t.Fatal(err)
			}
		}
		sqliteResolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).WithUserStoreProvider(provider)
		sqliteReq := CatalogRequest{Source: CatalogSourceUserCollection, CollectionID: collection.ID, CursorPaging: true, UseSourceOrder: true, Limit: 2}
		viewer := access
		viewer.UserID = 87654
		viewer.ProfileID = profile
		first, err := sqliteResolver.Resolve(ctx, sqliteReq, viewer)
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Items) != 2 || first.Items[0].ContentID != inputs[200].MediaItemID || first.Next == nil {
			t.Fatalf("SQLite first: %+v", first)
		}
		sqliteReq.After = first.Next
		second, err := sqliteResolver.Resolve(ctx, sqliteReq, viewer)
		if err != nil {
			t.Fatal(err)
		}
		if len(second.Items) != 2 || second.Items[0].ContentID != inputs[202].MediaItemID || second.HasMore {
			t.Fatalf("SQLite second: %+v", second)
		}
		sqliteReq.After = nil
		sqliteReq.Query = QueryDefinition{Groups: []QueryGroup{{Rules: []QueryRule{{Field: "favorited", Op: "is", Value: true}}}}}
		if _, err := sqliteResolver.Resolve(ctx, sqliteReq, viewer); !errors.Is(err, ErrCatalogStorageUnsupported) {
			t.Fatalf("SQLite personalized SQL silently accepted: %v", err)
		}
		sqliteReq.Source = CatalogSourceLibraryCollection
		sqliteReq.CollectionID = c.ID
		if _, err := sqliteResolver.resolveSmartCollectionCursor(ctx, sqliteReq, viewer, QueryDefinition{LibraryIDs: []int{library}}); !errors.Is(err, ErrCatalogStorageUnsupported) {
			t.Fatalf("SQLite smart library overlay read unrelated PG viewer state: %v", err)
		}

	})
	t.Run("personal PG source order sort jump and access", func(t *testing.T) {
		var userID int
		if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) }()
		profile := "10000000-0000-4000-8000-000000000006"
		if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'collection')`, profile, userID); err != nil {
			t.Fatal(err)
		}
		provider := pgstore.NewPostgresProvider(pool)
		store, err := provider.ForUser(ctx, userID)
		if err != nil {
			t.Fatal(err)
		}
		personal, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: profile, Name: "Personal"})
		if err != nil {
			t.Fatal(err)
		}
		for i := range 4 {
			if err := store.AddCollectionItem(ctx, personal.ID, inputs[200+i].MediaItemID, i*10); err != nil {
				t.Fatal(err)
			}
		}
		personalResolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).WithUserStoreProvider(provider)
		personalReq := CatalogRequest{Source: CatalogSourceUserCollection, CollectionID: personal.ID, CursorPaging: true, UseSourceOrder: true, Limit: 2}
		viewer := access
		viewer.UserID = userID
		viewer.ProfileID = profile
		first, err := personalResolver.Resolve(ctx, personalReq, viewer)
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Items) != 2 || first.Items[0].ContentID != inputs[200].MediaItemID || first.Next == nil {
			t.Fatalf("personal order: %+v", first)
		}
		personalReq.After = first.CursorScope
		personalReq.Seek = new(2)
		jumped, err := personalResolver.Resolve(ctx, personalReq, viewer)
		if err != nil {
			t.Fatal(err)
		}
		if len(jumped.Items) != 2 || jumped.Items[0].ContentID != inputs[202].MediaItemID {
			t.Fatalf("personal sparse-position jump: %+v", jumped)
		}
		personalReq.After = nil
		personalReq.Seek = nil
		personalReq.Query.Sort = QuerySort{Field: "title", Order: "desc"}
		sorted, err := personalResolver.Resolve(ctx, personalReq, viewer)
		if err != nil {
			t.Fatal(err)
		}
		if len(sorted.Items) != 2 || sorted.Items[0].ContentID != inputs[203].MediaItemID {
			t.Fatalf("personal sort: %+v", sorted)
		}

		base := QueryDefinition{LibraryIDs: []int{library}, Sort: QuerySort{Field: "title", Order: "asc"}, Limit: new(2)}
		libraryOverlay := CatalogRequest{Source: CatalogSourceLibraryCollection, CollectionID: c.ID, CursorPaging: true, Limit: 2, Query: QueryDefinition{Sort: QuerySort{Field: "progress", Order: "desc"}}}
		if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,position_seconds,duration_seconds,completed) VALUES($1,$2,$3,25,100,false),($1,$2,$4,75,100,false),($1,$2,$5,99,100,false)`, userID, profile, inputs[200].MediaItemID, inputs[201].MediaItemID, inputs[203].MediaItemID); err != nil {
			t.Fatal(err)
		}
		progressSorted, err := personalResolver.resolveSmartCollectionCursor(ctx, libraryOverlay, viewer, base)
		if err != nil {
			t.Fatal(err)
		}
		if len(progressSorted.Items) != 2 || progressSorted.Items[0].ContentID != inputs[201].MediaItemID || progressSorted.Items[1].ContentID != inputs[200].MediaItemID {
			t.Fatalf("library base scope or personalized overlay changed: %+v", progressSorted)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO user_favorites(user_id,profile_id,media_item_id) VALUES($1,$2,$3)`, userID, profile, inputs[201].MediaItemID); err != nil {
			t.Fatal(err)
		}
		libraryOverlay.Query.Groups = []QueryGroup{{Rules: []QueryRule{{Field: "favorited", Op: "is", Value: true}}}}
		filtered, err := personalResolver.resolveSmartCollectionCursor(ctx, libraryOverlay, viewer, base)
		if err != nil {
			t.Fatal(err)
		}
		if len(filtered.Items) != 1 || filtered.Items[0].ContentID != inputs[201].MediaItemID {
			t.Fatalf("library favorite overlay lost viewer: %+v", filtered)
		}
		libraryOverlay.Query = QueryDefinition{}
		libraryOverlay.SnapshotAt = new(time.Now().Add(-time.Hour))
		fenced, err := personalResolver.resolveSmartCollectionCursor(ctx, libraryOverlay, viewer, base)
		if err != nil {
			t.Fatal(err)
		}
		if len(fenced.Items) != 0 {
			t.Fatal("unoverlaid smart source dropped insertion fence")
		}
		viewer.ProfileID = "other"
		if _, err := personalResolver.Resolve(ctx, personalReq, viewer); !errors.Is(err, ErrCatalogSourceNotFound) {
			t.Fatalf("another profile accessed personal collection: %v", err)
		}
	})
	if _, err := pool.Exec(ctx, `UPDATE library_collection_items SET position=position+1 WHERE collection_id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(ctx, req, access); !errors.Is(err, ErrCatalogCursorChanged) {
		t.Fatalf("changed membership: %v", err)
	}
}

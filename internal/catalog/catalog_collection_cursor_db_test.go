package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// Curated order keeps ties, filters and search across continuations.
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
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	}()
	access := AccessFilter{AllowedLibraryIDs: []int{library}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
	ids, positions := seedCollectionTies(t, pool, prefix, library)

	t.Run("personal collection", func(t *testing.T) {
		checkPersonalCollectionTies(t, pool, prefix, access, ids, positions)
	})
	t.Run("library collection", func(t *testing.T) {
		repo := NewLibraryCollectionRepository(pool)
		tied, err := repo.Create(ctx, CreateLibraryCollectionInput{LibraryID: library, Slug: prefix + "-tie", Title: "Ties"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = repo.Delete(context.Background(), tied.ID)
			_, _ = pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id=$1`, tied.ID)
		}()
		members := make([]LibraryCollectionItemInput, len(ids))
		for i := range ids {
			members[len(ids)-1-i] = LibraryCollectionItemInput{MediaItemID: ids[i]}
		}
		if err := repo.ReplaceItems(ctx, tied.ID, members); err != nil {
			t.Fatal(err)
		}
		for i, id := range ids {
			if _, err := pool.Exec(ctx, `UPDATE library_collection_items SET position=$3 WHERE collection_id=$1 AND media_item_id=$2`, tied.ID, id, positions[i]); err != nil {
				t.Fatal(err)
			}
		}
		resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool))
		libraryReq := CatalogRequest{Source: CatalogSourceLibraryCollection, CollectionID: tied.ID, CursorPaging: true, UseSourceOrder: true, Limit: 1}
		want := []string{ids[4], ids[0], ids[1], ids[2], ids[3]}
		walkCollectionPages(t, resolver, libraryReq, access, want)
		grouped := libraryReq
		grouped.GroupByWork = true
		walkCollectionPages(t, resolver, grouped, access, want)
	})
}

// seedCollectionTies inserts six movies. tie-5 is hidden by the viewer's
// rating limit, and tie-2 by the personal collection's display filter once the
// viewer has watched it. tie-0..tie-3 share a position, so content ID breaks
// the tie.
func seedCollectionTies(t *testing.T, pool *pgxpool.Pool, prefix string, library int) ([]string, []int) {
	t.Helper()
	positions := []int{5, 5, 5, 5, 1, 1}
	ids := make([]string, len(positions))
	for i := range positions {
		ids[i] = fmt.Sprintf("%s-tie-%d", prefix, i)
		rating, age := "PG", 8
		if i == 5 {
			rating, age = "R", 17
		}
		title := "Tie " + ids[i]
		if i == 3 {
			title = "Tie Needle Three"
		}
		if _, err := pool.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age) VALUES($1,'movie',$2,'released','{}',$3,$4)`, ids[i], title, rating, age); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, ids[i], library); err != nil {
			t.Fatal(err)
		}
	}
	return ids, positions
}

func checkPersonalCollectionTies(t *testing.T, pool *pgxpool.Pool, prefix string, access AccessFilter, ids []string, positions []int) {
	t.Helper()
	ctx := t.Context()
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix+"-tie").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) }()
	profile := "10000000-0000-4000-8000-000000000007"
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'ties')`, profile, userID); err != nil {
		t.Fatal(err)
	}
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	personal, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID:       profile,
		Name:                   "Ties",
		DisplayQueryDefinition: `{"match":"all","groups":[{"match":"all","rules":[{"field":"watched","op":"is","value":false}]}]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Add in reverse so insertion order cannot stand in for the tie breaker.
	for i := len(ids) - 1; i >= 0; i-- {
		if err := store.AddCollectionItem(ctx, personal.ID, ids[i], positions[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,position_seconds,duration_seconds,completed) VALUES($1,$2,$3,100,100,true)`, userID, profile, ids[2]); err != nil {
		t.Fatal(err)
	}
	personalResolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).WithUserStoreProvider(provider)
	viewer := access
	viewer.UserID = userID
	viewer.ProfileID = profile
	personalReq := CatalogRequest{Source: CatalogSourceUserCollection, CollectionID: personal.ID, CursorPaging: true, UseSourceOrder: true, Limit: 1}
	walkCollectionPages(t, personalResolver, personalReq, viewer, []string{ids[4], ids[0], ids[1], ids[3]})

	jump := personalReq
	jump.Seek = new(2)
	jumped, err := personalResolver.Resolve(ctx, jump, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(jumped.Items) != 1 || jumped.Items[0].ContentID != ids[1] {
		t.Fatalf("personal tie jump: %+v", jumped)
	}
	search := personalReq
	search.SearchQuery = "needle three"
	walkCollectionPages(t, personalResolver, search, viewer, []string{ids[3]})
}

func walkCollectionPages(t *testing.T, r *CatalogResolver, req CatalogRequest, viewer AccessFilter, want []string) {
	t.Helper()
	var got []string
	for range len(want) + 1 {
		page, err := r.Resolve(t.Context(), req, viewer)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != len(want) {
			t.Fatalf("total = %d, want %d", page.Total, len(want))
		}
		for _, item := range page.Items {
			got = append(got, item.ContentID)
		}
		if !page.HasMore {
			break
		}
		req.After = page.Next
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("pages = %v, want %v", got, want)
	}
}

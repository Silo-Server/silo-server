package catalog

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestQueryWorkGroupsDB(t *testing.T) {
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
	prefix := fmt.Sprintf("work-cursor-%d", time.Now().UnixNano())
	var lib, uid int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('ebooks',$1,true) RETURNING id`, prefix).Scan(&lib); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	profile := prefix + "-profile"
	exec(`INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'one')`, profile, uid)
	person := time.Now().UnixNano()
	exec(`INSERT INTO people(id,name) VALUES($1,$2)`, person, prefix)
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, uid)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM literary_works WHERE work_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM people WHERE id=$1`, person)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, lib)
	}()
	for i := range 3 {
		exec(`INSERT INTO literary_works(work_id,canonical_title,normalized_title) VALUES($1,'Work','work')`, fmt.Sprintf("%s-work%d", prefix, i))
	}
	ids := make([]string, 208)
	workKeys := map[string]string{}
	var members []LibraryCollectionItemInput
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%03d", prefix, i)
		mediaType := "ebook"
		if i == 207 {
			mediaType = "comic"
		}
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age,rating_imdb,created_at) VALUES($1,$2,'Tied title','released','{}',$3,$5,$4,'2025-01-01'::timestamptz)`, ids[i], mediaType, map[bool]string{true: "R", false: "PG"}[i == 0], map[bool]any{true: nil, false: float64(i % 3)}[i%4 == 0], map[bool]int{true: 17, false: 8}[i == 0])
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, ids[i], lib)
		work := 0
		if i == 205 {
			work = 1
		}
		if i == 206 {
			work = 2
		}
		workID := fmt.Sprintf("%s-work%d", prefix, work)
		format := mediaType
		exec(`INSERT INTO literary_work_items(work_id,content_id,format_type,link_source) VALUES($1,$2,$3,'manual')`, workID, ids[i], format)
		workKeys[ids[i]] = workID
		if mediaType == "comic" {
			workKeys[ids[i]] = ids[i]
		}
		exec(`INSERT INTO user_favorites(user_id,profile_id,media_item_id,added_at) VALUES($1,$2,$3,'2025-01-01'::timestamptz)`, uid, profile, ids[i])
		exec(`INSERT INTO item_people(id,content_id,person_id,kind) VALUES($1,$2,$3,7)`, person+int64(i)+1, ids[i], person)
		members = append(members, LibraryCollectionItemInput{MediaItemID: ids[i]})
	}
	access := AccessFilter{UserID: uid, ProfileID: profile, AllowedLibraryIDs: []int{lib}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
	raw := &QueryExecutor{Pool: pool}
	grouped := &QueryExecutor{Pool: pool, GroupByWork: true}
	for _, sort := range []QuerySort{{Field: "title", Order: "asc"}, {Field: "title", Order: "desc"}, {Field: "rating_imdb", Order: "desc"}, {Field: "rating_imdb", Order: "asc"}, {Field: "date_viewed", Order: "desc"}} {
		t.Run(sort.Field+sort.Order, func(t *testing.T) {
			def := QueryDefinition{Sort: sort}
			seen := map[string]bool{}
			var want []string
			var after *QueryCursor
			for range 5 {
				page, err := raw.PreviewCursorPage(ctx, def, access, 80, after, false)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range page.Items {
					key := workKeys[item.ContentID]
					if !seen[key] {
						seen[key] = true
						want = append(want, item.ContentID)
					}
				}
				if !page.HasMore {
					break
				}
				after = page.Next
			}
			after = nil
			var got []string
			for range 6 {
				page, err := grouped.PreviewCursorPage(ctx, def, access, 1, after, true)
				if err != nil {
					t.Fatal(err)
				}
				if page.Total != len(want) {
					t.Fatalf("group count=%d want %d", page.Total, len(want))
				}
				for _, item := range page.Items {
					got = append(got, item.ContentID)
				}
				if !page.HasMore {
					break
				}
				after = page.Next
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("representatives %v want %v", got, want)
			}
			seek, err := grouped.SeekCursor(ctx, def, access, 2)
			if err != nil {
				t.Fatal(err)
			}
			page, err := grouped.PreviewCursorPage(ctx, def, access, 2, seek, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 2 || page.Items[0].ContentID != want[2] {
				t.Fatalf("group seek used raw index: %+v", page)
			}
		})
	}
	t.Run("raw source cap precedes grouping", func(t *testing.T) {
		def := QueryDefinition{Sort: QuerySort{Field: "title", Order: "asc"}, Limit: new(100)}
		page, err := grouped.PreviewCursorPage(ctx, def, access, 2, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].ContentID != ids[1] || page.Total != 1 || page.HasMore {
			t.Fatalf("cap counted groups instead of editions: %+v", page)
		}
	})
	collectionRepo := NewLibraryCollectionRepository(pool)
	collection, err := collectionRepo.Create(ctx, CreateLibraryCollectionInput{LibraryID: lib, Slug: prefix, Title: "Works"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = collectionRepo.Delete(context.Background(), collection.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id=$1`, collection.ID)
	}()
	if err := collectionRepo.ReplaceItems(ctx, collection.ID, members); err != nil {
		t.Fatal(err)
	}
	provider := pgstore.NewPostgresProvider(pool)
	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).WithUserStoreProvider(provider)
	store, err := provider.ForUser(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	personal, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: profile, Name: "Works"})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if err := store.AddCollectionItem(ctx, personal.ID, id, i); err != nil {
			t.Fatal(err)
		}
	}
	for _, source := range []CatalogSource{CatalogSourceQuery, CatalogSourceFavorites, CatalogSourcePerson, CatalogSourceLibraryCollection, CatalogSourceUserCollection} {
		t.Run("resolver/"+string(source), func(t *testing.T) {
			req := CatalogRequest{Source: source, PersonID: person, CollectionID: collection.ID, CursorPaging: true, GroupByWork: true, Limit: 2, Query: QueryDefinition{Sort: QuerySort{Field: "title", Order: "asc"}}}
			if source == CatalogSourceUserCollection {
				req.CollectionID = personal.ID
			}
			page, err := resolver.Resolve(ctx, req, access)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 2 || page.Items[0].ContentID != ids[1] || page.Items[1].ContentID != ids[205] || page.Next == nil || !page.HasMore {
				t.Fatalf("first source representatives: %+v", page)
			}
			req.After = page.Next
			page, err = resolver.Resolve(ctx, req, access)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 2 || page.Items[0].ContentID != ids[206] || page.Items[1].ContentID != ids[207] || page.HasMore {
				t.Fatalf("later edition repeated: %+v", page)
			}
		})
	}
	t.Run("smart cap precedes work grouping", func(t *testing.T) {
		smart, err := collectionRepo.Create(ctx, CreateLibraryCollectionInput{LibraryID: lib, Slug: prefix + "-smart", Title: "Smart works", CollectionType: "smart", QueryDefinition: []byte(`{"sort":{"field":"title","order":"asc"},"limit":100}`)})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = collectionRepo.Delete(context.Background(), smart.ID)
			_, _ = pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id=$1`, smart.ID)
		}()
		req := CatalogRequest{Source: CatalogSourceLibraryCollection, CollectionID: smart.ID, CursorPaging: true, GroupByWork: true, Limit: 2}
		page, err := resolver.Resolve(ctx, req, access)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Total != 1 || page.HasMore {
			t.Fatalf("smart source cap escaped: %+v", page)
		}
	})

}

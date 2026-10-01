package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pagingIntegrationFixture struct {
	pool                     *pgxpool.Pool
	account, library, hidden int
	ids                      []string
}

func newPagingIntegrationFixture(t *testing.T) pagingIntegrationFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := pagingIntegrationFixture{pool: pool}
	suffix := time.Now().UnixNano()
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("paging-adapter-%d", suffix)).Scan(&f.account); err != nil {
		t.Fatal(err)
	}
	for i, target := range []*int{&f.library, &f.hidden} {
		if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("paging-adapter-%d-%d", suffix, i)).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 5 {
		id := fmt.Sprintf("paging-adapter-%d-%02d", suffix, i)
		f.ids = append(f.ids, id)
		f.exec(t, `INSERT INTO media_items(content_id,type,title) VALUES($1,'movie','Same title')`, id)
		lib := f.library
		if i == 0 {
			lib = f.hidden
		}
		f.exec(t, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, lib)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id=ANY($1)`, f.ids)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=ANY($1)`, []int{f.library, f.hidden})
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, f.account)
		_, _ = pool.Exec(ctx, `DELETE FROM user_collection_revisions WHERE user_id=$1`, f.account)
	})
	return f
}
func (f pagingIntegrationFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

type pagingIntegrationProvider struct {
	userstore.UserStoreProvider
	account int
	store   userstore.UserStore
}

func (p pagingIntegrationProvider) ForUser(_ context.Context, id int) (userstore.UserStore, error) {
	if id != p.account {
		return nil, errors.New("wrong account")
	}
	return p.store, nil
}

func TestPersonalCollectionPagingRealStoresDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	for _, backend := range []string{"postgres", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var provider userstore.UserStoreProvider
			if backend == "postgres" {
				provider = pgstore.NewPostgresProvider(f.pool)
			} else {
				db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "user.db"), f.account)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				provider = pagingIntegrationProvider{account: f.account, store: userdb.NewSQLiteUserStore(db.DB)}
			}
			store, err := provider.ForUser(t.Context(), f.account)
			if err != nil {
				t.Fatal(err)
			}
			c, err := store.CreateCollection(t.Context(), userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Paging", CollectionType: "manual"})
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range f.ids {
				if err := store.AddCollectionItem(t.Context(), c.ID, id, 0); err != nil {
					t.Fatal(err)
				}
			}
			h := NewCollectionHandler(provider)
			h.Executor = &catalog.QueryExecutor{Pool: f.pool}
			filter := catalog.AccessFilter{AllowedLibraryIDs: []int{f.library}}
			page, err := h.PersonalCollectionItemsPage(t.Context(), f.account, "owner", c.ID, filter, userstore.CollectionItemsPageOptions{Limit: 1}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 0 || !page.HasMore || page.Last == nil || page.Last.MediaItemID != f.ids[0] {
				t.Fatalf("filtered first page lost continuation: %+v", page)
			}
			opts := userstore.CollectionItemsPageOptions{Limit: 2, After: page.Last, Revision: page.Revision}
			var got []string
			for {
				next, err := h.PersonalCollectionItemsPage(t.Context(), f.account, "owner", c.ID, filter, opts, nil)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range next.Items {
					if item.Title != "Same title" {
						t.Fatalf("missing accessible catalog title: %+v", item)
					}
					if item.AddedAt == "" {
						t.Fatal("missing membership time")
					}
					got = append(got, item.MediaItemID)
				}
				if !next.HasMore {
					break
				}
				opts.After = next.Last
			}
			if !reflect.DeepEqual(got, f.ids[1:]) {
				t.Fatalf("tie continuation: %v", got)
			}
			_, err = h.PersonalCollectionItemsPage(t.Context(), f.account, "uninvited", c.ID, filter, userstore.CollectionItemsPageOptions{Limit: 1}, nil)
			assertPagingAPIStatus(t, err, 404)
			if backend == "postgres" {
				_, err = h.PersonalCollectionItemsPage(t.Context(), f.account+1000000, "owner", c.ID, filter, userstore.CollectionItemsPageOptions{Limit: 1}, nil)
				assertPagingAPIStatus(t, err, 404)
			}
			for _, mutation := range []func() error{
				func() error { return store.RemoveCollectionItem(t.Context(), c.ID, f.ids[4]) },
				func() error { return store.AddCollectionItem(t.Context(), c.ID, f.ids[4], 2) },
				func() error {
					return store.UpdateCollection(t.Context(), userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: "owner", Name: new("renamed")})
				},
			} {
				first, err := h.PersonalCollectionItemsPage(t.Context(), f.account, "owner", c.ID, filter, userstore.CollectionItemsPageOptions{Limit: 1}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := mutation(); err != nil {
					t.Fatal(err)
				}
				_, err = h.PersonalCollectionItemsPage(t.Context(), f.account, "owner", c.ID, filter, userstore.CollectionItemsPageOptions{Limit: 1, Revision: first.Revision, After: first.Last}, nil)
				assertPagingAPIStatus(t, err, 409)
			}
			if err := store.DeleteCollection(t.Context(), c.ID); err != nil {
				t.Fatal(err)
			}
			_, err = h.PersonalCollectionItemsPage(t.Context(), f.account, "owner", c.ID, filter, opts, nil)
			assertPagingAPIStatus(t, err, 404)
		})
	}
}
func assertPagingAPIStatus(t *testing.T, err error, status int) {
	t.Helper()
	e, ok := errors.AsType[*APIError](err)
	if !ok || e.Status != status {
		t.Fatalf("error %v, want status %d", err, status)
	}
}

// The mutation occurs exactly after the handler's initial revision fence. This
// uses the real SQLite store to prove a definition/access read cannot be paired
// with a newer membership snapshot in the separate PostgreSQL catalog.
type changingPagingStore struct {
	userstore.UserStore
	userstore.CollectionItemsPager
	change func()
}

func (s *changingPagingStore) GetCollection(ctx context.Context, id string) (*userstore.Collection, error) {
	c, e := s.UserStore.GetCollection(ctx, id)
	if s.change != nil {
		change := s.change
		s.change = nil
		change()
	}
	return c, e
}
func TestSQLitePersonalPagingCrossStoreFenceDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "user.db"), f.account)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store := userdb.NewSQLiteUserStore(db.DB)
	c, err := store.CreateCollection(t.Context(), userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Before", CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddCollectionItem(t.Context(), c.ID, f.ids[1], 0); err != nil {
		t.Fatal(err)
	}
	wrapped := &changingPagingStore{UserStore: store, CollectionItemsPager: store, change: func() {
		if err := store.UpdateCollection(t.Context(), userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: "owner", Name: new("After")}); err != nil {
			t.Fatal(err)
		}
	}}
	h := NewCollectionHandler(pagingIntegrationProvider{account: f.account, store: wrapped})
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}
	_, err = h.PersonalCollectionItemsPage(t.Context(), f.account, "owner", c.ID, catalog.AccessFilter{AllowedLibraryIDs: []int{f.library}}, userstore.CollectionItemsPageOptions{Limit: 1}, nil)
	assertPagingAPIStatus(t, err, 409)
}

func TestSmartLibraryCollectionPagingAdapterDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	repo := catalog.NewLibraryCollectionRepository(f.pool)
	def := fmt.Sprintf(`{"library_ids":[%d,%d],"media_scope":"movie","sort":{"field":"title","order":"asc"},"limit":3}`, f.library, f.hidden)
	c, err := repo.Create(t.Context(), catalog.CreateLibraryCollectionInput{LibraryID: f.library, Title: "Smart", Slug: fmt.Sprintf("smart-%d", time.Now().UnixNano()), CollectionType: "smart", QueryDefinition: []byte(def)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = repo.Delete(context.Background(), c.ID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id=$1`, c.ID)
	})
	h := NewLibraryCollectionHandler(repo, nil, catalog.NewItemRepository(f.pool), nil)
	h.FolderRepo = catalog.NewFolderRepository(f.pool)
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}
	ctx := access.SetScope(t.Context(), access.Scope{AllowedLibraryIDs: []int{f.library}, LibrariesRestricted: true})
	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{f.library, f.hidden}}
	first, err := h.LibraryCollectionItemsPage(ctx, f.library, c.ID, filter, userstore.CollectionItemsPageOptions{Limit: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || !first.HasMore || first.Query == nil {
		t.Fatalf("first smart page: %+v", first)
	}
	next, err := h.LibraryCollectionItemsPage(ctx, f.library, c.ID, filter, userstore.CollectionItemsPageOptions{Limit: 2, Revision: first.Revision}, first.Query)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Items) != 1 || next.HasMore {
		t.Fatalf("smart cap was not respected: %+v", next)
	}
	var got []string
	for _, item := range append(first.Items, next.Items...) {
		got = append(got, item.ContentID)
	}
	if !reflect.DeepEqual(got, f.ids[1:4]) {
		t.Fatalf("smart library constraint/tuple: %v", got)
	}
	denied := access.SetScope(t.Context(), access.Scope{AllowedLibraryIDs: []int{f.hidden}, LibrariesRestricted: true})
	_, err = h.LibraryCollectionItemsPage(denied, f.library, c.ID, filter, userstore.CollectionItemsPageOptions{Limit: 1}, nil)
	assertPagingAPIStatus(t, err, 404)
	if err := repo.Update(t.Context(), catalog.UpdateLibraryCollectionInput{ID: c.ID, Title: new("changed")}); err != nil {
		t.Fatal(err)
	}
	_, err = h.LibraryCollectionItemsPage(ctx, f.library, c.ID, filter, userstore.CollectionItemsPageOptions{Limit: 2, Revision: first.Revision}, first.Query)
	assertPagingAPIStatus(t, err, 409)
}

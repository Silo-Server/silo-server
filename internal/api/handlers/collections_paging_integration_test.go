package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
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

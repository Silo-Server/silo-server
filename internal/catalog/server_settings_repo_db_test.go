package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestServerSettingsRepoGetManyReadsPresentKeys(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	prefix := fmt.Sprintf("test.get_many.%d.", time.Now().UnixNano())
	first, second, missing := prefix+"first", prefix+"second", prefix+"missing"
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM server_settings WHERE key = ANY($1)`, []string{first, second}); err != nil {
			t.Logf("clean up test settings: %v", err)
		}
	})
	repo := NewServerSettingsRepo(pool)
	if err := repo.SetMany(ctx, map[string]string{first: "one", second: ""}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetMany(ctx, first, second, missing)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[first] != "one" || got[second] != "" {
		t.Fatalf("GetMany = %v, want only the two stored keys", got)
	}
	if _, ok := got[missing]; ok {
		t.Fatalf("GetMany returned a key without a row: %v", got)
	}
}

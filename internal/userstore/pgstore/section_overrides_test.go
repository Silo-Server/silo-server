package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestListAllSectionOverridesMatchesLiteralSettingPrefix(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("section-prefix-%d", time.Now().UnixNano()),
	).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	want := []userstore.SectionOverride{{ID: "wanted", ProfileID: "profile", Scope: "home"}}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	store := newStore(pool, userID)
	if err := store.SetSetting(ctx, sectionOverridesKey("home", ""), string(encoded)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(ctx, "sectionXoverrides:home:", "not section override JSON"); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListAllSectionOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != want[0].ID {
		t.Fatalf("overrides = %+v, want %+v", got, want)
	}
}

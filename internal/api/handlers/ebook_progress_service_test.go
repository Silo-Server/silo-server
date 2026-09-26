package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func ebookProgressTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("ebook_progress_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(t.Context(), `CREATE TABLE ebook_reader_progress (
 user_id integer NOT NULL, profile_id text NOT NULL, content_id text NOT NULL,
 file_id integer NOT NULL, location text NOT NULL, progress double precision NOT NULL,
 updated_at timestamptz NOT NULL, PRIMARY KEY(user_id,profile_id,content_id))`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestEbookProgressEventsPostgres(t *testing.T) {
	pool := ebookProgressTestPool(t)
	store := NewPGEbookReaderProgressStore(pool)
	handler := NewEbookReaderHandler(&MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "book", Container: "epub", BaseType: "ebook"}},
		ItemAccess:   stubItemAccessChecker{},
	})
	handler.ProgressStore = store
	ctx := t.Context()
	event := EbookReaderProgress{UserID: 1, ProfileID: "one", ContentID: "book", FileID: 42, Location: "new", Progress: 0.4, UpdatedAt: time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)}
	filter := catalog.AccessFilter{UserID: 1, ProfileID: "one"}
	got, err := handler.SaveReaderProgress(ctx, event, filter)
	if err != nil || got.Location != "new" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, delta := range []time.Duration{0, -time.Minute} {
		old := event
		old.Location = "stale"
		old.Progress = 0.1
		old.UpdatedAt = event.UpdatedAt.Add(delta)
		got, err = handler.SaveReaderProgress(ctx, old, filter)
		if err != nil || got.Location != "new" || got.Progress != 0.4 || !got.UpdatedAt.Equal(event.UpdatedAt) {
			t.Fatalf("stale event: %+v %v", got, err)
		}
	}
	// Multiple connections race different client times; the stored event must
	// be the maximum regardless of statement/commit ordering.
	start := make(chan struct{})
	results := make(chan error, 12)
	for i := range 12 {
		go func() {
			<-start
			next := event
			next.UpdatedAt = event.UpdatedAt.Add(time.Duration(i+1) * time.Second)
			next.Location = fmt.Sprint(i + 1)
			results <- store.UpsertNewer(ctx, next)
		}()
	}
	close(start)
	for range 12 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	got, err = store.Get(ctx, 1, "one", "book")
	if err != nil || got.Location != "12" {
		t.Fatalf("concurrent winner: %+v %v", got, err)
	}
	finished := event
	finished.UpdatedAt = event.UpdatedAt.Add(time.Minute)
	finished.Progress = 1
	if err := store.UpsertNewer(ctx, finished); err != nil {
		t.Fatal(err)
	}
	finished.UpdatedAt = finished.UpdatedAt.Add(time.Second)
	finished.Progress = 0.1
	finished.Location = "reopened"
	got, err = handler.SaveReaderProgress(ctx, finished, filter)
	if err != nil || got.Progress != 1 || got.Location != "reopened" {
		t.Fatalf("finished invariant: %+v %v", got, err)
	}
	other := event
	other.ProfileID = "two"
	if err := store.UpsertNewer(ctx, other); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx, 1, "two", "book")
	if err != nil || got.Location != "new" || got.Progress != 0.4 {
		t.Fatalf("profile isolation: %+v %v", got, err)
	}
	// V1 retains its unconditional server-time writes and legacy response.
	legacy := event
	legacy.UpdatedAt = time.Time{}
	legacy.Location = "legacy"
	got, err = handler.SaveReaderProgress(ctx, legacy, filter)
	if err != nil || got.Location != "legacy" || got.UpdatedAt.IsZero() {
		t.Fatalf("legacy: %+v %v", got, err)
	}
}

func TestEbookProgressAuthorizationBeforeWrite(t *testing.T) {
	store := &fakeEbookProgressStore{}
	handler := NewEbookReaderHandler(&MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "book", Container: "epub", BaseType: "ebook"}},
		ItemAccess:   stubItemAccessChecker{err: catalog.ErrItemNotFound},
	})
	handler.ProgressStore = store
	event := EbookReaderProgress{UserID: 1, ProfileID: "one", ContentID: "book", FileID: 42, Location: "here", Progress: 0.2, UpdatedAt: time.Now().Add(-time.Hour)}
	if _, err := handler.SaveReaderProgress(t.Context(), event, catalog.AccessFilter{}); !errors.Is(err, catalog.ErrItemNotFound) || store.upserted != nil {
		t.Fatalf("access denial: %v", err)
	}
	handler.FileAuthorizer.ItemAccess = stubItemAccessChecker{}
	event.ContentID = "another-book"
	if _, err := handler.SaveReaderProgress(t.Context(), event, catalog.AccessFilter{}); !errors.Is(err, catalog.ErrItemNotFound) || store.upserted != nil {
		t.Fatalf("mismatched file: %v", err)
	}
	event.ContentID = "book"
	event.UpdatedAt = time.Now().Add(time.Hour)
	_, err := handler.SaveReaderProgress(t.Context(), event, catalog.AccessFilter{})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Field != "updated_at" || store.upserted != nil {
		t.Fatalf("future event: %v", err)
	}
}

package sections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// sectionLogRecorder captures every log record written while a test runs.
type sectionLogRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (r *sectionLogRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *sectionLogRecorder) Handle(_ context.Context, record slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record.Clone())
	return nil
}
func (r *sectionLogRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *sectionLogRecorder) WithGroup(string) slog.Handler      { return r }

// messages lists the messages logged at exactly level.
func (r *sectionLogRecorder) messages(level slog.Level) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, record := range r.records {
		if record.Level == level {
			out = append(out, record.Message)
		}
	}
	return out
}

func (r *sectionLogRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = nil
}

// recordSectionLogs routes the default logger to a recorder for the test. Tests
// that use it must not run in parallel.
func recordSectionLogs(t *testing.T) *sectionLogRecorder {
	t.Helper()
	recorder := &sectionLogRecorder{}
	previous := slog.Default()
	slog.SetDefault(slog.New(recorder))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return recorder
}

type failingSectionStoreProvider struct{ userstore.UserStoreProvider }

func (failingSectionStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return nil, errors.New("user store unavailable")
}

// An unrelated failure still renders the row empty and still logs an ERROR:
// only a deleted collection is expected.
func TestFetchAllLogsUnrelatedSectionErrorsAtError(t *testing.T) {
	logs := recordSectionLogs(t)
	fetcher := &Fetcher{StoreProvider: failingSectionStoreProvider{}}
	row := ResolvedSection{ID: "row", SectionType: SectionCollection, Config: json.RawMessage(`{"user_collection_id":"c1"}`)}

	got := fetcher.FetchAll(t.Context(), []ResolvedSection{row}, nil, nil, 1, "owner", catalog.AccessFilter{})

	if len(got) != 1 || got[0].ID != "row" || got[0].Items == nil || len(got[0].Items) != 0 {
		t.Fatalf("FetchAll = %+v, want the row with no items", got)
	}
	if errs := logs.messages(slog.LevelError); len(errs) != 1 || errs[0] != "fetching section items" {
		t.Fatalf("ERROR records = %q, want one fetching section items record", errs)
	}
}

// TestFetchAllLogsDeletedCollectionsAtDebugDB pins that a Home or library row
// still naming a deleted server or personal collection loads as an empty row
// without an ERROR record, and that a private collection's row on another
// profile's Home shows nothing (#193 S1).
func TestFetchAllLogsDeletedCollectionsAtDebugDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	resetResolvedListCacheForTest()
	t.Cleanup(resetResolvedListCacheForTest)
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	prefix := fmt.Sprintf("missing-collection-%d", time.Now().UnixNano())
	var account, library int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id=$1`, library)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id=$1`, account)
		_, _ = pool.Exec(cleanup, `DELETE FROM user_collection_revisions WHERE user_id=$1`, account)
		_, _ = pool.Exec(cleanup, `DELETE FROM user_collection_order_revisions WHERE user_id=$1`, account)
	})
	movie := prefix + "-movie"
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title) VALUES($1,'movie','Movie')`, movie); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, movie, library); err != nil {
		t.Fatal(err)
	}

	repo := catalog.NewLibraryCollectionRepository(pool)
	server, err := repo.Create(ctx, catalog.CreateLibraryCollectionInput{LibraryIDs: []int{library}, Title: "Deleted", Slug: prefix, CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id=$1`, server.ID)
	})

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"owner", "other"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Deleted", CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCollection(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}
	private, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Private", CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddCollectionItem(ctx, private.ID, movie, 0); err != nil {
		t.Fatal(err)
	}

	fetcher := NewFetcher(pool)
	fetcher.CollectionRepo = repo
	fetcher.StoreProvider = provider
	row := func(id, config string) ResolvedSection {
		return ResolvedSection{ID: id, SectionType: SectionCollection, Title: id, ItemLimit: 20, Config: json.RawMessage(config)}
	}
	assertEmptyRows := func(t *testing.T, got []SectionWithItems, ids ...string) {
		t.Helper()
		if len(got) != len(ids) {
			t.Fatalf("FetchAll returned %d rows, want %d", len(got), len(ids))
		}
		for i, id := range ids {
			if got[i].ID != id || len(got[i].Items) != 0 || got[i].TotalCount != 0 {
				t.Errorf("row %d = %s with %d items (total %d), want empty %s", i, got[i].ID, len(got[i].Items), got[i].TotalCount, id)
			}
		}
	}
	logs := recordSectionLogs(t)

	t.Run("deleted collections log at debug", func(t *testing.T) {
		logs.reset()
		got := fetcher.FetchAll(ctx, []ResolvedSection{
			row("server", fmt.Sprintf(`{"library_collection_id":%q}`, server.ID)),
			row("personal", fmt.Sprintf(`{"user_collection_id":%q}`, deleted.ID)),
		}, nil, []int{library}, account, "owner", catalog.AccessFilter{AllowedLibraryIDs: []int{library}})
		assertEmptyRows(t, got, "server", "personal")
		if errs := logs.messages(slog.LevelError); len(errs) != 0 {
			t.Fatalf("ERROR records = %q, want none", errs)
		}
		if debug := logs.messages(slog.LevelDebug); len(debug) != 2 {
			t.Fatalf("DEBUG records = %q, want one per deleted collection", debug)
		}
	})

	t.Run("a private collection's row on another profile shows nothing", func(t *testing.T) {
		logs.reset()
		privateRow := row("private", fmt.Sprintf(`{"user_collection_id":%q}`, private.ID))
		got := fetcher.FetchAll(ctx, []ResolvedSection{privateRow}, nil, []int{library}, account, "other", catalog.AccessFilter{AllowedLibraryIDs: []int{library}})
		assertEmptyRows(t, got, "private")
		if errs := logs.messages(slog.LevelError); len(errs) != 0 {
			t.Fatalf("ERROR records = %q, want none", errs)
		}
		// The owner still sees the title, so the row above is empty because
		// of the profile, not the fixture.
		owned := fetcher.FetchAll(ctx, []ResolvedSection{privateRow}, nil, []int{library}, account, "owner", catalog.AccessFilter{AllowedLibraryIDs: []int{library}})
		if len(owned) != 1 || len(owned[0].Items) != 1 || owned[0].Items[0].ContentID != movie {
			t.Fatalf("owner's private row = %+v, want %s", owned, movie)
		}
	})
}

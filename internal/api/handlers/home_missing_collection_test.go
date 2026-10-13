package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// levelLogRecorder captures the level and message of every log record written
// while a test runs.
type levelLogRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (r *levelLogRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *levelLogRecorder) Handle(_ context.Context, record slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record.Clone())
	return nil
}
func (r *levelLogRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *levelLogRecorder) WithGroup(string) slog.Handler      { return r }

func (r *levelLogRecorder) take(level slog.Level) []string {
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

func (r *levelLogRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = nil
}

// brokenCollectionStore fails to read one collection the way a database outage
// would, so the test can tell that failure apart from a deleted collection.
type brokenCollectionStore struct {
	userstore.UserStore
	brokenID string
}

func (s brokenCollectionStore) GetCollection(ctx context.Context, id string) (*userstore.Collection, error) {
	if id == s.brokenID {
		return nil, errors.New("connection reset by peer")
	}
	return s.UserStore.GetCollection(ctx, id)
}

// TestSectionRowOfDeletedCollectionLogsAtDebugDB pins that the single-row Home
// and library reads answer an empty row, with a debug record instead of an
// ERROR, when the row's server or personal collection was deleted. Any other
// failure still logs an ERROR, and a private collection's row on another
// profile's Home shows nothing (#193 S1).
func TestSectionRowOfDeletedCollectionLogsAtDebugDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()

	libraryCollections := catalog.NewLibraryCollectionRepository(f.pool)
	server, err := libraryCollections.Create(ctx, catalog.CreateLibraryCollectionInput{
		LibraryIDs: []int{f.library}, Title: "Deleted", Slug: fmt.Sprintf("deleted-row-%d", f.account), CollectionType: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := libraryCollections.Delete(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id=$1`, server.ID)
	})

	store, err := pgstore.NewPostgresProvider(f.pool).ForUser(ctx, f.account)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"owner", "other"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	create := func(name string) *userstore.Collection {
		t.Helper()
		c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: name, CollectionType: "manual"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddCollectionItem(ctx, c.ID, f.ids[1], 0); err != nil {
			t.Fatal(err)
		}
		return c
	}
	deleted := create("Deleted")
	if err := store.DeleteCollection(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}
	broken := create("Broken")
	private := create("Private")

	saveRows := func(profileID, scope, scopeID string, configs map[string]string) {
		t.Helper()
		rows := make([]userstore.SectionOverride, 0, len(configs))
		position := 1000
		for id, config := range configs {
			position++
			limit := 20
			rows = append(rows, userstore.SectionOverride{
				ID: id, Scope: scope, Position: &position, ItemLimit: &limit, IsUserAdded: true,
				UserSectionType: string(sections.SectionCollection), UserTitle: id, UserConfig: config,
			})
		}
		if err := store.SaveSectionOverrides(ctx, profileID, scope, scopeID, rows); err != nil {
			t.Fatal(err)
		}
	}
	serverRow := fmt.Sprintf(`{"library_collection_id":%q}`, server.ID)
	saveRows("owner", "home", "", map[string]string{
		"row-server":   serverRow,
		"row-personal": fmt.Sprintf(`{"user_collection_id":%q}`, deleted.ID),
		"row-broken":   fmt.Sprintf(`{"user_collection_id":%q}`, broken.ID),
	})
	saveRows("owner", "library", strconv.Itoa(f.library), map[string]string{"row-server": serverRow})
	saveRows("other", "home", "", map[string]string{"row-private": fmt.Sprintf(`{"user_collection_id":%q}`, private.ID)})

	provider := pagingIntegrationProvider{account: f.account, store: brokenCollectionStore{UserStore: store, brokenID: broken.ID}}
	fetcher := sections.NewFetcher(f.pool)
	fetcher.CollectionRepo = libraryCollections
	fetcher.StoreProvider = provider
	h := NewSectionHandler(sections.NewRepository(f.pool), fetcher)
	h.StoreProvider = provider
	viewerContext := func(profileID string) context.Context {
		reqCtx := apimw.SetClaims(ctx, &auth.Claims{UserID: f.account})
		return apimw.SetProfileID(reqCtx, profileID)
	}
	viewer := SectionViewer{Access: catalog.AccessFilter{UserID: f.account}}

	logs := &levelLogRecorder{}
	previous := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(previous) })

	assertEmpty := func(t *testing.T, row SectionView, err error, id string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if row.ID != id || row.Items == nil || len(row.Items) != 0 || row.TotalCount != 0 {
			t.Fatalf("%s = %+v, want the row with no items", id, row)
		}
	}
	for _, tc := range []struct {
		name, profile, id string
		library           bool
		wantError         bool
		wantDebug         int
	}{
		{name: "deleted server collection on Home", profile: "owner", id: "row-server", wantDebug: 1},
		{name: "deleted personal collection on Home", profile: "owner", id: "row-personal", wantDebug: 1},
		{name: "deleted server collection in a library", profile: "owner", id: "row-server", library: true, wantDebug: 1},
		{name: "unrelated failure", profile: "owner", id: "row-broken", wantError: true},
		{name: "private collection on another profile", profile: "other", id: "row-private"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.reset()
			var row SectionView
			if tc.library {
				row, err = h.LibrarySectionItems(viewerContext(tc.profile), f.library, tc.id, viewer)
			} else {
				row, err = h.HomeSectionItems(viewerContext(tc.profile), tc.id, viewer)
			}
			assertEmpty(t, row, err, tc.id)
			errs := logs.take(slog.LevelError)
			if tc.wantError && (len(errs) != 1 || errs[0] != "fetching section items") {
				t.Fatalf("ERROR records = %q, want one fetching section items record", errs)
			}
			if !tc.wantError && len(errs) != 0 {
				t.Fatalf("ERROR records = %q, want none", errs)
			}
			if debug := logs.take(slog.LevelDebug); len(debug) != tc.wantDebug {
				t.Fatalf("DEBUG records = %q, want %d", debug, tc.wantDebug)
			}
		})
	}
}

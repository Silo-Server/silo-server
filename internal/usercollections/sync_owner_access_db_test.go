package usercollections

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

type staticTMDBList []catalog.TMDBCollectionEntry

func (l staticTMDBList) GetList(context.Context, int, int) ([]catalog.TMDBCollectionEntry, error) {
	return l, nil
}

// TestImportFillsItsLimitWithTitlesItsOwnerCanAccessDB pins #1612: an
// imported personal collection fills its item limit only with titles its
// owner profile can access (allowed libraries and rating limit, not the
// libraries it hides from browsing), counts only those titles in its sync
// summary, and follows the owner's access on the next sync.
func TestImportFillsItsLimitWithTitlesItsOwnerCanAccessDB(t *testing.T) {
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
	exec := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}

	suffix := time.Now().UnixNano()
	var account, open, closed int
	username := fmt.Sprintf("import-owner-access-%d", suffix)
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role,email,password_hash) VALUES($1,'user',$2,'') RETURNING id`,
		username, username+"@example.test").Scan(&account); err != nil {
		t.Fatal(err)
	}
	for i, target := range []*int{&open, &closed} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`,
			fmt.Sprintf("import-owner-access-%d-%d", suffix, i)).Scan(target); err != nil {
			t.Fatal(err)
		}
	}

	// The source list, in order. Entry 3 names a title the server lacks.
	// A PG ceiling admits ages 0 and 8, not 17.
	type title struct {
		library int
		age     int
	}
	source := []*title{{closed, 0}, {open, 17}, {open, 0}, nil, {open, 8}, {open, 0}, {open, 0}}
	tmdbBase := int(suffix % 1_000_000_000 * 10)
	var entries staticTMDBList
	contentIDs := make([]string, len(source))
	for i, ti := range source {
		tmdb := tmdbBase + i
		entries = append(entries, catalog.TMDBCollectionEntry{ID: tmdb, MediaType: "movie"})
		if ti == nil {
			continue
		}
		contentIDs[i] = fmt.Sprintf("import-owner-access-%d-%d", suffix, i)
		exec(t, `INSERT INTO media_items(content_id,type,title,tmdb_id,content_rating_age) VALUES($1,'movie',$1,$2,$3)`,
			contentIDs[i], strconv.Itoa(tmdb), ti.age)
		exec(t, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, contentIDs[i], ti.library)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id=ANY($1)`, contentIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=ANY($1)`, []int{open, closed})
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, account)
		_, _ = pool.Exec(ctx, `DELETE FROM user_collection_revisions WHERE user_id=$1`, account)
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
	setOwner := func(t *testing.T, rating string, libraries []int) {
		t.Helper()
		restricted := libraries != nil
		if err := store.UpdateProfile(ctx, "owner", userstore.UpdateProfileInput{
			MaxContentRating: &rating, LibraryRestrictionsEnabled: &restricted, AllowedLibraryIDs: &libraries,
		}); err != nil {
			t.Fatal(err)
		}
	}
	hide := func(t *testing.T, libraries string) {
		t.Helper()
		if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
			Key: settingskeys.UiDisabledLibraryIds, Scope: settingscontract.ScopeProfile, ProfileID: "owner",
		}, json.RawMessage(libraries)); err != nil {
			t.Fatal(err)
		}
	}

	engine, err := policy.NewEngine(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolver := policy.NewViewerResolver(auth.NewUserRepository(pool), provider, nil, policy.NewPDP(engine), access.NewGroupStore(pool))
	svc := NewService(provider, catalog.NewItemRepository(pool), catalog.NewLibraryItemRepository(pool), NewOwnerAccess(resolver), nil, slog.New(slog.DiscardHandler))
	svc.TMDBLists = entries

	collection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: "owner", Name: "Imported", CollectionType: "tmdb", IsShared: true,
		QueryDefinition: "{}",
		SourceConfig:    `{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/310","limit":3}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	// sync runs the scheduled entry point, which knows only the account and
	// collection, and checks the stored members and summary.
	sync := func(t *testing.T, wantMessage string, wantUnmatched int, want ...int) {
		t.Helper()
		result, err := svc.SyncCollection(ctx, account, collection.ID)
		if err != nil {
			t.Fatal(err)
		}
		items, err := store.ListCollectionItems(ctx, collection.ID)
		if err != nil {
			t.Fatal(err)
		}
		var got, wantIDs []string
		for _, item := range items {
			got = append(got, item.MediaItemID)
		}
		for _, i := range want {
			wantIDs = append(wantIDs, contentIDs[i])
		}
		if !slices.Equal(got, wantIDs) {
			t.Errorf("members = %v, want %v", got, wantIDs)
		}
		stored, err := store.GetCollection(ctx, collection.ID)
		if err != nil {
			t.Fatal(err)
		}
		if result.Message != wantMessage || stored.LastSyncMessage != wantMessage || stored.LastSyncStatus != "success" {
			t.Errorf("summary = %q (stored %q, %s), want %q", result.Message, stored.LastSyncMessage, stored.LastSyncStatus, wantMessage)
		}
		if result.ItemsMatched != len(want) || result.ItemsUnmatched != wantUnmatched || stored.ItemCount != len(want) {
			t.Errorf("matched %d, unmatched %d, stored count %d; want %d, %d, %d",
				result.ItemsMatched, result.ItemsUnmatched, stored.ItemCount, len(want), wantUnmatched, len(want))
		}
	}

	t.Run("a restricted owner's limit skips titles outside its access", func(t *testing.T) {
		setOwner(t, "PG", []int{open})
		// Entries 0 (closed library) and 1 (rated 17) count as unmatched,
		// like entry 3, which the server lacks.
		sync(t, "Matched 3 of 7 entries (item limit reached after 6 scanned)", 3, 2, 4, 5)
	})
	t.Run("the next sync follows the owner's wider access", func(t *testing.T) {
		setOwner(t, "", nil)
		sync(t, "Matched 3 of 7 entries (item limit reached after 3 scanned)", 0, 0, 1, 2)
	})
	t.Run("the owner's hidden libraries still count as access", func(t *testing.T) {
		setOwner(t, "", []int{open, closed})
		hide(t, fmt.Sprintf(`[%d]`, closed))
		t.Cleanup(func() { hide(t, `[]`) })
		sync(t, "Matched 3 of 7 entries (item limit reached after 3 scanned)", 0, 0, 1, 2)
	})
	t.Run("chosen libraries and the owner's access both apply", func(t *testing.T) {
		setOwner(t, "PG", nil)
		if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{
			ID: collection.ID, RequestProfileID: "owner",
			SourceConfig: new(fmt.Sprintf(`{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/310","limit":3,"library_ids":[%d]}`, open)),
		}); err != nil {
			t.Fatal(err)
		}
		sync(t, "Matched 3 of 7 entries (item limit reached after 6 scanned)", 3, 2, 4, 5)
	})
	t.Run("a chosen library the owner can't access adds nothing", func(t *testing.T) {
		// Entry 2 is in both libraries. The owner reaches it through the open
		// one, but the collection asks for the closed one only, so it stays out.
		exec(t, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, contentIDs[2], closed)
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM media_item_libraries WHERE content_id=$1 AND media_folder_id=$2`, contentIDs[2], closed)
		})
		setOwner(t, "", []int{open})
		if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{
			ID: collection.ID, RequestProfileID: "owner",
			SourceConfig: new(fmt.Sprintf(`{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/310","limit":3,"library_ids":[%d]}`, closed)),
		}); err != nil {
			t.Fatal(err)
		}
		result, err := svc.SyncCollection(ctx, account, collection.ID)
		if err != nil {
			t.Fatal(err)
		}
		items, err := store.ListCollectionItems(ctx, collection.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 0 || result.ItemsMatched != 0 {
			t.Fatalf("members = %+v (matched %d), want none", items, result.ItemsMatched)
		}
	})
}

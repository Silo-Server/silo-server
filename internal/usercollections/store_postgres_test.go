package usercollections

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newStoreTestPool connects to the database named by SILO_TEST_DATABASE_URL,
// skipping when it is unset or has not applied the personal-collection
// migrations. Mirrors the jellycompat compat-pool harness.
func newStoreTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.user_personal_collections')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check user_personal_collections table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied the personal collection migrations")
	}
	return pool
}

const (
	storeTestOwnerProfile = "store-test-profile-owner"
	storeTestOtherProfile = "store-test-profile-other"
	storeTestIDPrefix     = "store-test-"
)

// storeTestFixture owns a unique user; deleting it cascades to its collections.
type storeTestFixture struct {
	pool   *pgxpool.Pool
	userID int
}

func newStoreTestFixture(t *testing.T) *storeTestFixture {
	t.Helper()
	pool := newStoreTestPool(t)
	ctx := context.Background()

	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, storeTestIDPrefix+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})
	return &storeTestFixture{pool: pool, userID: userID}
}

type storeTestCollection struct {
	id    string
	name  string
	optIn bool
	// creator defaults to storeTestOwnerProfile. shared shows the collection to
	// every profile on the login; notNative marks an Audiobookshelf row.
	creator      string
	shared       bool
	notNative    bool
	collType     string
	queryDef     string
	displayQuery string
	sourceConfig string
}

func (f *storeTestFixture) insert(t *testing.T, c storeTestCollection) {
	t.Helper()
	ctx := context.Background()
	if c.collType == "" {
		c.collType = "manual"
	}
	if c.sourceConfig == "" {
		c.sourceConfig = "{}"
	}
	if c.queryDef == "" {
		c.queryDef = "{}" // NOT NULL; display_query_definition stays NULL when unset
	}
	if c.creator == "" {
		c.creator = storeTestOwnerProfile
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO user_personal_collections
		   (id, user_id, profile_id, creator_profile_id, name, description, collection_type,
		    include_in_server_collections, source_config, query_definition, display_query_definition,
		    is_shared, native)
		 VALUES ($1, $2, $3, $3, $4, '', $5, $6, $7::jsonb,
		         $8::jsonb, NULLIF($9, '')::jsonb, $10, $11)`,
		c.id, f.userID, c.creator, c.name, c.collType, c.optIn,
		c.sourceConfig, c.queryDef, c.displayQuery, c.shared, !c.notNative,
	); err != nil {
		t.Fatalf("insert collection %s: %v", c.id, err)
	}
}

// TestStoreGetEnforcesOwnershipOptInAndProfile exercises the privacy predicate
// against a real database: an opted-in native collection on the asking login
// resolves when the asking profile created it or its creator shares it.
func TestStoreGetEnforcesOwnershipOptInAndProfile(t *testing.T) {
	t.Parallel()
	f := newStoreTestFixture(t)
	store := NewStore(f.pool)
	ctx := context.Background()

	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "visible", name: "Visible", optIn: true,
		collType:     "mdblist",
		queryDef:     `{"match":"all"}`,
		displayQuery: `{"match":"all","groups":[{"match":"all","rules":[{"field":"watched","op":"equals","value":false}]}]}`,
	})
	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "not-opted-in", name: "Not Opted In", optIn: false,
	})
	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "other-profile", name: "Other Profile", optIn: true,
		creator: storeTestOtherProfile,
	})
	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "shared-by-other", name: "Shared By Other", optIn: true,
		creator: storeTestOtherProfile, shared: true,
	})
	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "audiobookshelf", name: "Audiobookshelf", optIn: true,
		notNative: true,
	})
	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "hidden-library", name: "Hidden Library", optIn: true,
		sourceConfig: `{"library_ids":[9]}`,
	})

	sum := sha256.Sum256([]byte(storeTestIDPrefix + "visible"))
	got, err := store.Get(ctx, f.userID, storeTestOwnerProfile, hex.EncodeToString(sum[:14]), []int{7})
	if err != nil {
		t.Fatalf("get visible collection: %v", err)
	}
	if got == nil {
		t.Fatal("expected the opted-in, shared collection to resolve")
	}
	if got.Name != "Visible" || got.CollectionType != "mdblist" {
		t.Fatalf("unexpected collection: %+v", *got)
	}
	sum = sha256.Sum256([]byte(storeTestIDPrefix + "shared-by-other"))
	if got, err := store.Get(ctx, f.userID, storeTestOwnerProfile, hex.EncodeToString(sum[:14]), []int{7}); err != nil || got == nil {
		t.Fatalf("another profile's shared collection must resolve on the login: got=%v err=%v", got, err)
	}

	for _, tc := range []struct {
		name      string
		userID    int
		profileID string
		id        string
	}{
		{"not opted in", f.userID, storeTestOwnerProfile, storeTestIDPrefix + "not-opted-in"},
		{"another profile's private collection", f.userID, storeTestOwnerProfile, storeTestIDPrefix + "other-profile"},
		{"audiobookshelf row", f.userID, storeTestOwnerProfile, storeTestIDPrefix + "audiobookshelf"},
		{"owned by another user", f.userID + 10_000, storeTestOwnerProfile, storeTestIDPrefix + "visible"},
		{"shared on another login", f.userID + 10_000, storeTestOwnerProfile, storeTestIDPrefix + "shared-by-other"},
		{"scoped outside visible libraries", f.userID, storeTestOwnerProfile, storeTestIDPrefix + "hidden-library"},
		{"unknown id", f.userID, storeTestOwnerProfile, storeTestIDPrefix + "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sum := sha256.Sum256([]byte(tc.id))
			got, err := store.Get(ctx, tc.userID, tc.profileID, hex.EncodeToString(sum[:14]), []int{7})
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got != nil {
				t.Fatalf("expected no collection, got %+v", *got)
			}
		})
	}
}

func TestStoreAnyVisible(t *testing.T) {
	t.Parallel()
	f := newStoreTestFixture(t)
	store := NewStore(f.pool)
	ctx := context.Background()

	if visible, err := store.AnyVisible(ctx, f.userID, storeTestOtherProfile, []int{7}); err != nil || visible {
		t.Fatalf("expected no collections for an unshared profile, got visible=%v err=%v", visible, err)
	}
	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "hidden-probe", name: "Hidden Probe", optIn: true,
		creator: storeTestOtherProfile, sourceConfig: `{"library_ids":[9]}`,
	})
	if visible, err := store.AnyVisible(ctx, f.userID, storeTestOtherProfile, []int{7}); err != nil || visible {
		t.Fatalf("expected hidden-library collection not to enable the view, got visible=%v err=%v", visible, err)
	}

	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "probe", name: "Probe", optIn: true,
		creator: storeTestOtherProfile,
	})

	if visible, err := store.AnyVisible(ctx, f.userID, storeTestOtherProfile, []int{7}); err != nil || !visible {
		t.Fatalf("expected the shared collection to be probed, got visible=%v err=%v", visible, err)
	}
	if visible, err := store.AnyVisible(ctx, f.userID+10_000, storeTestOtherProfile, []int{7}); err != nil || visible {
		t.Fatalf("expected another user's probe to be empty, got visible=%v err=%v", visible, err)
	}
}

func TestStoreCompatKeys(t *testing.T) {
	t.Parallel()
	f := newStoreTestFixture(t)
	store := NewStore(f.pool)
	for _, id := range []string{
		"01K3M9K0R7D6Y9T7F1P6W2H8ZX", uuid.NewString(), "legacy-collection", "123", `test-å-\-集合`,
	} {
		t.Run(id, func(t *testing.T) {
			f.insert(t, storeTestCollection{id: id, name: "Test collection", optIn: true})
			sum := sha256.Sum256([]byte(id))
			key := hex.EncodeToString(sum[:14])
			got, err := store.Get(t.Context(), f.userID, storeTestOwnerProfile, key, nil)
			if err != nil || got == nil || got.ID != id {
				t.Fatalf("lookup key must resolve the unchanged native ID: got=%+v err=%v", got, err)
			}
			candidates, err := store.ImageCandidates(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, c := range candidates {
				found = found || c.ID == id
			}
			if !found {
				t.Fatal("signed-image candidates did not resolve the native ID")
			}
		})
	}
}

// TestStoreListLibraryScope pins the library predicate: scoped rows must
// overlap the visible library set, and library-agnostic rows show under any of
// them.
func TestStoreListLibraryScope(t *testing.T) {
	t.Parallel()
	f := newStoreTestFixture(t)
	store := NewStore(f.pool)
	ctx := context.Background()

	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "lib-7", name: "Scoped To 7", optIn: true,
		sourceConfig: `{"library_ids":[7]}`,
	})
	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "agnostic", name: "Agnostic", optIn: true,
	})
	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "not-opted-in-list", name: "Not Opted In", optIn: false,
	})
	for _, kind := range []string{"mdblist", "smart"} {
		f.insert(t, storeTestCollection{
			id: storeTestIDPrefix + kind + "-invalid-scope", name: "Out Of Range", optIn: true,
			collType:     kind,
			sourceConfig: `{"library_ids":[2147483648,"7"]}`,
			queryDef:     `{"library_ids":[2147483648,"7"]}`,
		})
	}

	names := func(t *testing.T, libraryIDs []int) []string {
		t.Helper()
		rows, err := store.List(ctx, f.userID, storeTestOwnerProfile, libraryIDs)
		if err != nil {
			t.Fatalf("list libraries %v: %v", libraryIDs, err)
		}
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.Name)
		}
		return out
	}
	if got := names(t, []int{7, 8}); len(got) != 2 {
		t.Fatalf("the visible library set should return both collections, got %v", got)
	}
	if got := names(t, []int{7}); len(got) != 2 {
		t.Fatalf("library 7 should list its own plus the agnostic one, got %v", got)
	}
	if got := names(t, []int{8}); len(got) != 1 || got[0] != "Agnostic" {
		t.Fatalf("library 8 should list only the agnostic collection, got %v", got)
	}
	// A negative ID is reachable from the request path (Atoi accepts it) and must
	// stay a plain non-matching library, never a wildcard.
	if got := names(t, []int{-1}); len(got) != 1 || got[0] != "Agnostic" {
		t.Fatalf("a negative library must not widen the query, got %v", got)
	}
	if got, err := ListServerVisibleByLibrary(ctx, f.pool, f.userID, storeTestOwnerProfile, 7); err != nil || len(got) != 2 {
		t.Fatalf("native library listing must ignore invalid scopes: got=%v err=%v", got, err)
	}
}

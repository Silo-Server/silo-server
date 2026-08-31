package usercollections

import (
	"context"
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
	id           string
	name         string
	optIn        bool
	sharedWith   []string
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
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO user_personal_collections
		   (id, user_id, profile_id, creator_profile_id, name, description, collection_type,
		    include_in_server_collections, source_config, query_definition, display_query_definition)
		 VALUES ($1, $2, $3, $3, $4, '', $5, $6, $7::jsonb,
		         $8::jsonb, NULLIF($9, '')::jsonb)`,
		c.id, f.userID, storeTestOwnerProfile, c.name, c.collType, c.optIn,
		c.sourceConfig, c.queryDef, c.displayQuery,
	); err != nil {
		t.Fatalf("insert collection %s: %v", c.id, err)
	}
	for _, profileID := range c.sharedWith {
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO user_personal_collection_profiles (user_id, collection_id, profile_id)
			 VALUES ($1, $2, $3)`,
			f.userID, c.id, profileID,
		); err != nil {
			t.Fatalf("share collection %s with %s: %v", c.id, profileID, err)
		}
	}
}

// TestStoreGetEnforcesOwnershipOptInAndProfile exercises the privacy predicate
// against a real database: only an opted-in collection shared with the asking
// profile, and owned by the asking user, resolves.
func TestStoreGetEnforcesOwnershipOptInAndProfile(t *testing.T) {
	t.Parallel()
	f := newStoreTestFixture(t)
	store := NewStore(f.pool)
	ctx := context.Background()

	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "visible", name: "Visible", optIn: true,
		sharedWith:   []string{storeTestOwnerProfile},
		collType:     "mdblist",
		queryDef:     `{"match":"all"}`,
		displayQuery: `{"match":"all","groups":[{"match":"all","rules":[{"field":"watched","op":"equals","value":false}]}]}`,
	})
	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "not-opted-in", name: "Not Opted In", optIn: false,
		sharedWith: []string{storeTestOwnerProfile},
	})
	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "other-profile", name: "Other Profile", optIn: true,
		sharedWith: []string{storeTestOtherProfile},
	})
	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "hidden-library", name: "Hidden Library", optIn: true,
		sharedWith: []string{storeTestOwnerProfile}, sourceConfig: `{"library_ids":[9]}`,
	})

	got, err := store.Get(ctx, f.userID, storeTestOwnerProfile, storeTestIDPrefix+"visible", []int{7})
	if err != nil {
		t.Fatalf("get visible collection: %v", err)
	}
	if got == nil {
		t.Fatal("expected the opted-in, shared collection to resolve")
	}
	if got.Name != "Visible" || got.CollectionType != "mdblist" {
		t.Fatalf("unexpected collection: %+v", *got)
	}

	for _, tc := range []struct {
		name      string
		userID    int
		profileID string
		id        string
	}{
		{"not opted in", f.userID, storeTestOwnerProfile, storeTestIDPrefix + "not-opted-in"},
		{"shared with another profile", f.userID, storeTestOwnerProfile, storeTestIDPrefix + "other-profile"},
		{"owned by another user", f.userID + 10_000, storeTestOwnerProfile, storeTestIDPrefix + "visible"},
		{"scoped outside visible libraries", f.userID, storeTestOwnerProfile, storeTestIDPrefix + "hidden-library"},
		{"unknown id", f.userID, storeTestOwnerProfile, storeTestIDPrefix + "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.Get(ctx, tc.userID, tc.profileID, tc.id, []int{7})
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
		sharedWith: []string{storeTestOtherProfile}, sourceConfig: `{"library_ids":[9]}`,
	})
	if visible, err := store.AnyVisible(ctx, f.userID, storeTestOtherProfile, []int{7}); err != nil || visible {
		t.Fatalf("expected hidden-library collection not to enable the view, got visible=%v err=%v", visible, err)
	}

	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "probe", name: "Probe", optIn: true,
		sharedWith: []string{storeTestOtherProfile},
	})

	if visible, err := store.AnyVisible(ctx, f.userID, storeTestOtherProfile, []int{7}); err != nil || !visible {
		t.Fatalf("expected the shared collection to be probed, got visible=%v err=%v", visible, err)
	}
	if visible, err := store.AnyVisible(ctx, f.userID+10_000, storeTestOtherProfile, []int{7}); err != nil || visible {
		t.Fatalf("expected another user's probe to be empty, got visible=%v err=%v", visible, err)
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
		sharedWith: []string{storeTestOwnerProfile}, sourceConfig: `{"library_ids":[7]}`,
	})
	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "agnostic", name: "Agnostic", optIn: true,
		sharedWith: []string{storeTestOwnerProfile},
	})
	f.insert(t, storeTestCollection{
		id: storeTestIDPrefix + "not-opted-in-list", name: "Not Opted In", optIn: false,
		sharedWith: []string{storeTestOwnerProfile},
	})
	for _, kind := range []string{"mdblist", "smart"} {
		f.insert(t, storeTestCollection{
			id: storeTestIDPrefix + kind + "-invalid-scope", name: "Out Of Range", optIn: true,
			sharedWith: []string{storeTestOwnerProfile}, collType: kind,
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

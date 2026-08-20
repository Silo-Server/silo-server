package catalog

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// These tests run the default-sort / saved-override flow against a real
// Postgres: real SQL ordering through the query executor, and a real
// PostgresUserStore writing the override row. They cover the reported
// behavior end to end — a collection's creator picks a default order, a viewer
// re-sorts, and the viewer's choice survives a fresh read.
//
// Set SILO_TEST_DATABASE_URL to a migrated database to run them.

func collectionSortTestPool(t *testing.T) *pgxpool.Pool {
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
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.user_collection_sort_preferences')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check user_collection_sort_preferences table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied the collection sort preference migration")
	}
	return pool
}

// seedSortableItem inserts one sortable item. Callers choose title/year pairs
// that make a title sort and a year sort disagree, so an order assertion cannot
// pass by coincidence.
func seedSortableItem(t *testing.T, pool *pgxpool.Pool, contentID, title string, year int) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, sort_title, year, genres)
		VALUES ($1, 'movie', $2, $2, $3, '{}'::text[])
	`, contentID, title, year); err != nil {
		t.Fatalf("seed media item %s: %v", contentID, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
}

func seedSortTestUser(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx := context.Background()
	var userID int
	// A bare fixture row: the override table has an FK to users(id) and the
	// test needs something to point at. No credentials are set.
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role)
		VALUES ('collection-sort-fixture', 'collection-sort-fixture@invalid.test', '', 'user')
		RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatalf("seed fixture user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	return userID
}

func itemsForSort(contentIDs ...string) []*models.MediaItem {
	items := make([]*models.MediaItem, 0, len(contentIDs))
	for _, id := range contentIDs {
		items = append(items, &models.MediaItem{ContentID: id})
	}
	return items
}

func orderedIDs(items []*models.MediaItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ContentID)
	}
	return ids
}

func assertOrder(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// TestCollectionDefaultSortAndOverrideEndToEnd walks the whole reported
// scenario against real Postgres, for both collection kinds.
func TestCollectionDefaultSortAndOverrideEndToEnd(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := context.Background()

	// Years are shuffled against titles on purpose: title-asc gives A,B,C but
	// year-desc gives B,C,A. If the two agreed, an assertion on the override's
	// order would also pass when the override was ignored entirely.
	seedSortableItem(t, pool, "cs-a", "Alpha", 2001)
	seedSortableItem(t, pool, "cs-b", "Bravo", 2003)
	seedSortableItem(t, pool, "cs-c", "Charlie", 2002)

	// Source order is deliberately neither sorted order.
	sourceOrder := itemsForSort("cs-c", "cs-a", "cs-b")
	limited, total, err := QueryCollectionItemsBySort(
		ctx,
		pool,
		[]string{"cs-c", "cs-a", "cs-b"},
		QuerySort{Field: "title", Order: "asc"},
		AccessFilter{},
		2,
		"",
	)
	if err != nil {
		t.Fatalf("querying limited sorted collection: %v", err)
	}
	assertOrder(t, orderedIDs(limited), "cs-a", "cs-b")
	if total != 3 {
		t.Fatalf("limited sorted collection total = %d, want 3", total)
	}

	userID := seedSortTestUser(t, pool)
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-e2e", Name: "E2E sort profile"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteProfile(context.Background(), "profile-e2e") })

	resolver := &CatalogResolver{storeProvider: provider}
	access := AccessFilter{UserID: userID, ProfileID: "profile-e2e"}

	for _, kind := range []string{userstore.CollectionKindLibrary, userstore.CollectionKindUser} {
		t.Run(kind, func(t *testing.T) {
			collectionID := "cs-collection-" + kind
			t.Cleanup(func() {
				_ = store.ClearCollectionSortPreference(ctx, access.ProfileID, kind, collectionID)
			})

			// 1. No default, no override — the collection keeps source order.
			if _, ok := resolver.EffectiveCollectionSort(ctx, access, kind, collectionID, []byte(`{}`)); ok {
				t.Fatal("a collection with no default reported a sort")
			}

			// 2. The creator sets a default of Title (A→Z).
			defaultSort := []byte(`{"field":"title","order":"asc"}`)
			qs, ok := resolver.EffectiveCollectionSort(ctx, access, kind, collectionID, defaultSort)
			if !ok {
				t.Fatal("configured default sort was not applied")
			}
			ordered, err := OrderCollectionItemsBySort(ctx, pool, sourceOrder, qs, access)
			if err != nil {
				t.Fatalf("ordering by default sort: %v", err)
			}
			assertOrder(t, orderedIDs(ordered), "cs-a", "cs-b", "cs-c")

			// 3. The viewer re-sorts to Year (newest first) and it is saved.
			if err := store.SetCollectionSortPreference(ctx, userstore.CollectionSortPreference{
				ProfileID:      access.ProfileID,
				CollectionKind: kind,
				CollectionID:   collectionID,
				SortField:      "year",
				SortOrder:      "desc",
			}); err != nil {
				t.Fatalf("saving override: %v", err)
			}

			// 4. A fresh resolution — the "leave and come back" case — reads the
			// override back out of the database and beats the creator's default.
			freshResolver := &CatalogResolver{storeProvider: pgstore.NewPostgresProvider(pool)}
			qs, ok = freshResolver.EffectiveCollectionSort(ctx, access, kind, collectionID, defaultSort)
			if !ok {
				t.Fatal("saved override was not applied on a fresh read")
			}
			if qs.Field != "year" || qs.Order != "desc" {
				t.Fatalf("effective sort = %q/%q, want year/desc", qs.Field, qs.Order)
			}
			ordered, err = OrderCollectionItemsBySort(ctx, pool, sourceOrder, qs, access)
			if err != nil {
				t.Fatalf("ordering by override: %v", err)
			}
			// Distinct from both the default's A,B,C and source order's C,A,B.
			assertOrder(t, orderedIDs(ordered), "cs-b", "cs-c", "cs-a")
			assertOrder(t, orderedIDs(sourceOrder), "cs-c", "cs-a", "cs-b")

			// 5. The viewer pins back to the collection's own order. That is a
			// real choice and must suppress the creator's default, not fall
			// through to it.
			if err := store.SetCollectionSortPreference(ctx, userstore.CollectionSortPreference{
				ProfileID:      access.ProfileID,
				CollectionKind: kind,
				CollectionID:   collectionID,
				SortField:      "",
				SortOrder:      "",
			}); err != nil {
				t.Fatalf("pinning to source order: %v", err)
			}
			if _, ok := resolver.EffectiveCollectionSort(ctx, access, kind, collectionID, defaultSort); ok {
				t.Fatal("source-order override fell through to the creator's default")
			}

			// 6. Clearing the override returns the viewer to the default.
			if err := store.ClearCollectionSortPreference(ctx, access.ProfileID, kind, collectionID); err != nil {
				t.Fatalf("clearing override: %v", err)
			}
			qs, ok = resolver.EffectiveCollectionSort(ctx, access, kind, collectionID, defaultSort)
			if !ok || qs.Field != "title" {
				t.Fatalf("after clearing, effective sort = %q (ok=%v), want title", qs.Field, ok)
			}
		})
	}
}

// TestCollectionSortOverrideIsPerProfile guards the isolation that makes a
// server-wide collection safe to re-sort: one profile's choice must not change
// what another profile sees.
func TestCollectionSortOverrideIsPerProfile(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := context.Background()

	userID := seedSortTestUser(t, pool)
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	for _, profile := range []userstore.Profile{
		{ID: "profile-one", Name: "Profile One"},
		{ID: "profile-two", Name: "Profile Two"},
	} {
		if err := store.CreateProfile(ctx, profile); err != nil {
			t.Fatalf("seed profile %s: %v", profile.ID, err)
		}
	}
	t.Cleanup(func() {
		_ = store.DeleteProfile(context.Background(), "profile-two")
		_ = store.DeleteProfile(context.Background(), "profile-one")
	})
	resolver := &CatalogResolver{storeProvider: provider}

	const collectionID = "cs-shared-collection"
	defaultSort := []byte(`{"field":"title","order":"asc"}`)
	kind := userstore.CollectionKindLibrary

	if err := store.SetCollectionSortPreference(ctx, userstore.CollectionSortPreference{
		ProfileID:      "profile-one",
		CollectionKind: kind,
		CollectionID:   collectionID,
		SortField:      "year",
		SortOrder:      "desc",
	}); err != nil {
		t.Fatalf("saving override: %v", err)
	}
	t.Cleanup(func() {
		_ = store.ClearCollectionSortPreference(context.Background(), "profile-one", kind, collectionID)
	})

	one, ok := resolver.EffectiveCollectionSort(ctx, AccessFilter{UserID: userID, ProfileID: "profile-one"}, kind, collectionID, defaultSort)
	if !ok || one.Field != "year" {
		t.Fatalf("profile-one sort = %q (ok=%v), want year", one.Field, ok)
	}

	two, ok := resolver.EffectiveCollectionSort(ctx, AccessFilter{UserID: userID, ProfileID: "profile-two"}, kind, collectionID, defaultSort)
	if !ok || two.Field != "title" {
		t.Fatalf("profile-two sort = %q (ok=%v), want the collection default title", two.Field, ok)
	}
}

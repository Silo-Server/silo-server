package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func seedNativeCollectionItem(t *testing.T, f libraryCASFixture, suffix string) string {
	t.Helper()
	id := fmt.Sprintf("native-member-%d-%s", f.libraryID, suffix)
	seedSortableItem(t, f.pool, id, id, 2020)
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, f.libraryID); err != nil {
		t.Fatal(err)
	}
	return id
}

// The definition transaction holds the parent while the native operation
// starts behind it. Validation must observe the committed definition after
// acquiring that lock, not the definition previously seen by an HTTP service.
func TestLibraryCollectionNativeMembershipDefinitionRaceDB(t *testing.T) {
	for _, operation := range []string{"add-type", "add-scope", "remove-type", "reorder-wildcard-type"} {
		t.Run(operation, func(t *testing.T) {
			f := newLibraryCASFixture(t)
			other := newLibraryCASFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			first := seedNativeCollectionItem(t, f, "first")
			second := seedNativeCollectionItem(t, f, "second")
			if operation == "remove-type" || operation == "reorder-wildcard-type" {
				if err := f.repo.AddItemIfAbsent(ctx, f.collectionID, first, 10); err != nil {
					t.Fatal(err)
				}
				if err := f.repo.AddItemIfAbsent(ctx, f.collectionID, second, 20); err != nil {
					t.Fatal(err)
				}
			}
			before, err := f.repo.ListItems(ctx, f.collectionID)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if err := lockLibraryCollectionParent(ctx, tx, f.collectionID); err != nil {
				t.Fatal(err)
			}
			if operation == "add-scope" {
				if _, err := tx.Exec(ctx, `UPDATE library_collection_libraries SET library_id=$2 WHERE collection_id=$1`, f.collectionID, other.libraryID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := tx.Exec(ctx, `UPDATE library_collections SET collection_type='smart' WHERE id=$1`, f.collectionID); err != nil {
					t.Fatal(err)
				}
			}
			name := fmt.Sprintf("native-member-race-%d", f.libraryID)
			cfg, err := pgxpool.ParseConfig(os.Getenv("SILO_TEST_DATABASE_URL"))
			if err != nil {
				t.Fatal(err)
			}
			cfg.ConnConfig.RuntimeParams["application_name"] = name
			cfg.MaxConns = 1
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			repo := NewLibraryCollectionRepository(pool)
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "add-type", "add-scope":
					done <- repo.AddItemIfAbsent(ctx, f.collectionID, first, 0)
				case "remove-type":
					done <- repo.RemoveManualItem(ctx, f.collectionID, first)
				case "reorder-wildcard-type":
					done <- repo.ReorderItemsIfRevision(ctx, f.collectionID, []string{second, first}, -1)
				}
			}()
			waitLibraryCollectionBlocked(t, ctx, f.pool, name, "transactionid")
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			want := ErrLibraryCollectionNotManual
			if operation == "add-scope" {
				want = ErrLibraryCollectionItemNotFound
			}
			if err := <-done; !errors.Is(err, want) {
				t.Fatalf("native operation accepted changed definition: got %v want %v", err, want)
			}
			after, err := f.repo.ListItems(ctx, f.collectionID)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Fatalf("rejected operation changed membership count %d -> %d", len(before), len(after))
			}
			for i := range before {
				if before[i].MediaItemID != after[i].MediaItemID || before[i].Position != after[i].Position {
					t.Fatal("rejected operation changed item order")
				}
			}
		})
	}
}

func TestLibraryCollectionNativeMembershipEligibilityDB(t *testing.T) {
	f := newLibraryCASFixture(t)
	other := newLibraryCASFixture(t)
	ctx := t.Context()
	allowed := seedNativeCollectionItem(t, f, "allowed")
	outside := seedNativeCollectionItem(t, other, "outside")
	if _, err := f.pool.Exec(ctx, `UPDATE library_collections SET visibility='hidden' WHERE id=$1`, f.collectionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE media_folders SET enabled=false WHERE id=$1`, f.libraryID); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.AddItemIfAbsent(ctx, f.collectionID, allowed, 13); err != nil {
		t.Fatalf("admin hidden membership: %v", err)
	}
	revision := f.revision(t, false)
	if err := f.repo.AddItemIfAbsent(ctx, f.collectionID, allowed, 0); err != nil {
		t.Fatal(err)
	}
	if got := f.revision(t, false); got != revision {
		t.Fatal("duplicate native add changed revision")
	}
	for _, id := range []string{outside, "missing-native-item"} {
		if err := f.repo.AddItemIfAbsent(ctx, f.collectionID, id, 0); !errors.Is(err, ErrLibraryCollectionItemNotFound) {
			t.Fatalf("ineligible item %s: %v", id, err)
		}
	}
	if got := f.revision(t, false); got != revision {
		t.Fatal("rejected native add changed revision")
	}
	if err := f.repo.RemoveManualItem(ctx, f.collectionID, allowed); err != nil {
		t.Fatal(err)
	}
	// Preserve the service's historical fallback when explicit scope rows are
	// absent but the parent still identifies a legacy library.
	if _, err := f.pool.Exec(ctx, `DELETE FROM library_collection_libraries WHERE collection_id=$1`, f.collectionID); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.AddItemIfAbsent(ctx, f.collectionID, allowed, 9); err != nil {
		t.Fatalf("legacy library fallback: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE library_collections SET collection_type='smart' WHERE id=$1`, f.collectionID); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.RemoveManualItem(ctx, f.collectionID, allowed); !errors.Is(err, ErrLibraryCollectionNotManual) {
		t.Fatalf("native remove accepted smart: %v", err)
	}
	// Legacy repository operations retain their caller-validated behavior.
	if err := f.repo.AddItem(ctx, f.collectionID, allowed, 4); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.ReorderItems(ctx, f.collectionID, []string{allowed}); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.RemoveItem(ctx, f.collectionID, allowed); err != nil {
		t.Fatal(err)
	}
	if items, err := f.repo.ListItems(ctx, f.collectionID); err != nil || len(items) != 0 {
		t.Fatalf("legacy removal: %+v %v", items, err)
	}
}

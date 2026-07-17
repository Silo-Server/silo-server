package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestReconcileFolderMembershipProtectsUnreachableRoots verifies the
// dead-root exemption in the orphan purge: memberships are still removed for
// content with no non-missing files (that is what hides the item from
// browse/home, which gate on media_item_libraries), but the media_items row
// of an item whose files sit under a protected (unreachable) root must
// survive so user collections, metadata edits, and artwork are not destroyed
// by a temporary outage. An orphan with no protected files is still purged.
func TestReconcileFolderMembershipProtectsUnreachableRoots(t *testing.T) {
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

	suffix := time.Now().UnixNano()
	deadRoot := fmt.Sprintf("/drp-dead-%d", suffix)
	goneItem := fmt.Sprintf("drp-gone-%d", suffix)
	protectedItem := fmt.Sprintf("drp-protected-%d", suffix)

	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('movies', 'DRP Test', true) RETURNING id`,
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{goneItem, protectedItem})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	for _, contentID := range []string{goneItem, protectedItem} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres)
			VALUES ($1, 'movie', 'DRP Item', 'matched', '{}'::text[])
		`, contentID); err != nil {
			t.Fatalf("seed item %s: %v", contentID, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
			VALUES ($1, $2, NOW())
		`, contentID, folderID); err != nil {
			t.Fatalf("seed membership %s: %v", contentID, err)
		}
	}
	missingSince := time.Now().UTC().Add(-72 * time.Hour)
	seedFile := func(contentID, path string) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, missing_since)
			VALUES ($1, $2, $3, 1024, $4)
		`, contentID, folderID, path, missingSince); err != nil {
			t.Fatalf("seed file %s: %v", path, err)
		}
	}
	// The protected item's only file lives under the unreachable root; the
	// other item's file was under a root that is reachable (its absence is a
	// genuine deletion).
	seedFile(protectedItem, deadRoot+"/Alpha (2020)/Alpha (2020).mkv")
	seedFile(goneItem, fmt.Sprintf("/drp-live-%d/Beta (2021)/Beta (2021).mkv", suffix))

	repo := NewLibraryItemRepository(pool)
	removed, deletedItems, _, err := repo.ReconcileFolderMembership(ctx, folderID, []string{deadRoot})
	if err != nil {
		t.Fatalf("ReconcileFolderMembership: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed memberships = %d, want 2 (hiding still requires removal)", removed)
	}
	if deletedItems != 1 {
		t.Fatalf("deleted items = %d, want 1 (only the unprotected orphan)", deletedItems)
	}

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM media_items WHERE content_id = $1)`, protectedItem,
	).Scan(&exists); err != nil {
		t.Fatalf("check protected item: %v", err)
	}
	if !exists {
		t.Fatal("protected item was purged despite its files sitting under an unreachable root")
	}
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM media_items WHERE content_id = $1)`, goneItem,
	).Scan(&exists); err != nil {
		t.Fatalf("check gone item: %v", err)
	}
	if exists {
		t.Fatal("unprotected orphan item survived; purge regressed")
	}
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM media_item_libraries WHERE media_folder_id = $1)`, folderID,
	).Scan(&exists); err != nil {
		t.Fatalf("check memberships: %v", err)
	}
	if exists {
		t.Fatal("memberships remain; items with only missing files must be hidden")
	}

	// Once the root is reachable again, reconciliation must revisit the item
	// even though its membership was removed during the protected pass.
	removed, deletedItems, _, err = repo.ReconcileFolderMembership(ctx, folderID, nil)
	if err != nil {
		t.Fatalf("ReconcileFolderMembership after recovery: %v", err)
	}
	if removed != 0 || deletedItems != 1 {
		t.Fatalf("recovery reconciliation removed/deleted = %d/%d, want 0/1", removed, deletedItems)
	}
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM media_items WHERE content_id = $1)`, protectedItem,
	).Scan(&exists); err != nil {
		t.Fatalf("check recovered orphan: %v", err)
	}
	if exists {
		t.Fatal("previously protected orphan survived after its root recovered")
	}
}

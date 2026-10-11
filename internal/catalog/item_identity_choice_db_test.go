package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// An Identify mark survives later writes of the item row, and a content-id
// rename carries it along.
func TestIdentityChoiceSurvivesUpsertAndRenameDB(t *testing.T) {
	pool := newAdvisoryAgeTestPool(t)
	ctx := context.Background()
	repo := NewItemRepository(pool)
	contentID := fmt.Sprintf("identity-choice-%d", time.Now().UnixNano())
	renamed := contentID + "-renamed"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{contentID, renamed})
	})
	item := &models.MediaItem{ContentID: contentID, Type: "movie", Title: "Collateral", SortTitle: "collateral", Year: 2004, Status: "matched"}
	if err := repo.Upsert(ctx, item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	chosen, err := repo.IdentityChosen(ctx, contentID)
	if err != nil || chosen {
		t.Fatalf("IdentityChosen before mark = %v, %v; want false", chosen, err)
	}
	if err := repo.MarkIdentityChosen(ctx, contentID); err != nil {
		t.Fatalf("MarkIdentityChosen: %v", err)
	}
	item.Title = "Collateral (refreshed)"
	if err := repo.Upsert(ctx, item); err != nil {
		t.Fatalf("Upsert after mark: %v", err)
	}
	if chosen, err := repo.IdentityChosen(ctx, contentID); err != nil || !chosen {
		t.Fatalf("IdentityChosen after upsert = %v, %v; want true", chosen, err)
	}

	if _, err := pool.Exec(ctx, `SELECT silo_rename_content_ids($1, $2)`, []string{contentID}, []string{renamed}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if chosen, err := repo.IdentityChosen(ctx, renamed); err != nil || !chosen {
		t.Fatalf("IdentityChosen after rename = %v, %v; want true", chosen, err)
	}
	if chosen, err := repo.IdentityChosen(ctx, "missing-"+contentID); err != nil || chosen {
		t.Fatalf("IdentityChosen for a missing item = %v, %v; want false", chosen, err)
	}
}

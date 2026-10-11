package metadata

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Merging an item an admin identified onto its canonical row keeps the
// Identify choice on the survivor (the canonicalization step that merges the
// item row's own columns).
func TestCanonicalizeKeepsIdentityChoicePostgres(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	suffix := uuid.NewString()
	source, canonical := "identity-choice-source-"+suffix, "identity-choice-canonical-"+suffix
	if _, err := tx.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, identity_chosen_at)
		VALUES ($1, 'movie', 'Source', 'matched', NOW()), ($2, 'movie', 'Canonical', 'matched', NULL)`, source, canonical); err != nil {
		t.Fatalf("seed items: %v", err)
	}
	ran := false
	for _, step := range mediaItemMergeSteps {
		if step.name != "merge legacy provider columns" {
			continue
		}
		if _, err := tx.Exec(ctx, step.sql, mergeStepArgs(step.sql, source, canonical)...); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		ran = true
	}
	if !ran {
		t.Fatal(`merge step "merge legacy provider columns" not found`)
	}
	var chosen bool
	if err := tx.QueryRow(ctx, `SELECT identity_chosen_at IS NOT NULL FROM media_items WHERE content_id = $1`, canonical).Scan(&chosen); err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if !chosen {
		t.Error("canonical item lost the Identify choice of the item merged onto it")
	}
}

// An Identify's mark commits in the transaction that writes the identity:
// when that write fails, as on a provider-ID conflict, no mark is left.
func TestPersistMarksIdentityWithTheIdentityWritePostgres(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()
	itemRepo := catalog.NewItemRepository(pool)
	providerRepo := catalog.NewProviderIDRepository(pool)
	svc := &MetadataService{dbPool: pool}

	suffix := uuid.NewString()
	owner, chosen := "identity-tx-owner-"+suffix, "identity-tx-chosen-"+suffix
	tmdbID := "9" + strings.ReplaceAll(suffix[:8], "-", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{owner, chosen})
	})
	for _, id := range []string{owner, chosen} {
		if err := itemRepo.Upsert(ctx, &models.MediaItem{ContentID: id, Type: "movie", Title: id, SortTitle: id, Status: "matched"}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	if err := providerRepo.ReplaceByContentID(ctx, owner, map[string]string{"tmdb": tmdbID}); err != nil {
		t.Fatalf("seed provider id: %v", err)
	}
	isChosen := func(id string) bool {
		t.Helper()
		var marked bool
		if err := pool.QueryRow(ctx, `SELECT identity_chosen_at IS NOT NULL FROM media_items WHERE content_id = $1`, id).Scan(&marked); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return marked
	}

	item := &models.MediaItem{ContentID: chosen, Type: "movie", Title: "Chosen", SortTitle: "chosen", Status: "matched"}
	if err := svc.persistItemAndProviderIDsOnceTx(ctx, itemRepo, providerRepo, item, map[string]string{"tmdb": tmdbID}, true); err == nil {
		t.Fatal("persist succeeded although another item owns the provider ID")
	}
	if isChosen(chosen) {
		t.Error("a failed identity write left the Identify mark")
	}
	if err := svc.persistItemAndProviderIDsOnceTx(ctx, itemRepo, providerRepo, item, map[string]string{"imdb": "tt" + tmdbID}, true); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if !isChosen(chosen) {
		t.Error("a committed identity write did not record the Identify mark")
	}
}

// An Identify that moves the item before writing its identity, by re-anchor
// rename, by merge onto an existing row, or by merge onto the item that owns
// its provider IDs, records the choice in the move's own transaction, so a
// failed identity write after it cannot leave the moved item unmarked. Other
// moves leave no mark.
func TestIdentifyMoveMarksTheMovedItemPostgres(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	svc := identifyMoveService(pool)
	ctx := context.Background()

	t.Run("rename", func(t *testing.T) {
		checkIdentifyMoveMarks(t, pool, false, func(from, to, _ string, markIdentity bool) error {
			return svc.renameContentID(ctx, from, to, false, markIdentity)
		})
	})
	t.Run("merge", func(t *testing.T) {
		checkIdentifyMoveMarks(t, pool, true, func(from, to, _ string, markIdentity bool) error {
			return svc.rebindItemOnto(ctx, from, to, true, markIdentity)
		})
	})
	t.Run("rebind by provider IDs", func(t *testing.T) {
		checkIdentifyMoveMarks(t, pool, true, func(from, to, tmdbID string, markIdentity bool) error {
			got, err := svc.rebindItemByProviderIDsLocked(ctx, from, map[string]string{"tmdb": tmdbID}, "movie", true, markIdentity)
			if err == nil && got != to {
				return fmt.Errorf("rebound to %q, want %q", got, to)
			}
			return err
		})
	})
}

// The provider-ID conflict merge records an Identify choice on the survivor
// the same way.
func TestIdentifyConflictMergeMarksTheSurvivorPostgres(t *testing.T) {
	t.Skip("duplicate merges fail at the personal collection step until #2299 is fixed")
	pool := chainBuiltinTestPool(t)
	checkIdentifyMoveMarks(t, pool, true, func(from, to, _ string, markIdentity bool) error {
		_, err := canonicalizeProviderIDDuplicateInto(context.Background(), pool, from, to, true, markIdentity)
		return err
	})
}

func identifyMoveService(pool *pgxpool.Pool) *MetadataService {
	return &MetadataService{dbPool: pool, itemRepo: catalog.NewItemRepository(pool), providerIDRepo: catalog.NewProviderIDRepository(pool)}
}

// checkIdentifyMoveMarks runs move from a seeded item to `to`, once as an
// Identify and once not, and checks that only the Identify marks `to`. With
// merge, `to` already exists and owns the tmdb ID passed to move.
func checkIdentifyMoveMarks(t *testing.T, pool *pgxpool.Pool, merge bool, move func(from, to, tmdbID string, markIdentity bool) error) {
	t.Helper()
	ctx := context.Background()
	itemRepo := catalog.NewItemRepository(pool)
	providerRepo := catalog.NewProviderIDRepository(pool)
	for _, markIdentity := range []bool{true, false} {
		suffix := uuid.NewString()
		from, to := "identity-move-from-"+suffix, "identity-move-to-"+suffix
		tmdbID := "8" + strings.ReplaceAll(suffix[:8], "-", "")
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{from, to})
		})
		seed := []string{from}
		if merge {
			seed = append(seed, to)
		}
		for _, id := range seed {
			if err := itemRepo.Upsert(ctx, &models.MediaItem{ContentID: id, Type: "movie", Title: id, SortTitle: id, Status: "matched"}); err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
		}
		if merge {
			if err := providerRepo.ReplaceByContentID(ctx, to, map[string]string{"tmdb": tmdbID}); err != nil {
				t.Fatalf("seed provider id: %v", err)
			}
		}
		if err := move(from, to, tmdbID, markIdentity); err != nil {
			t.Fatalf("move (identify=%v): %v", markIdentity, err)
		}
		chosen, err := itemRepo.IdentityChosen(ctx, to)
		if err != nil {
			t.Fatalf("IdentityChosen: %v", err)
		}
		if chosen != markIdentity {
			t.Errorf("identify=%v: moved item marked = %v", markIdentity, chosen)
		}
	}
}

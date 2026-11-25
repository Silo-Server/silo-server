package pgstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// A profile set that removes one profile, a delete of another profile that
// shares collections with it, and a reorder of the removed profile's
// collections can run at once without a deadlock: the set takes the delete's
// locks in the delete's order, then the household's rows, then the account
// row. Catches a lock-order change in WithProfileSetTransaction.
func TestPostgresProfileSetConcurrentWithDeleteAndReorderDB(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	const removedSyncID = "6f1c2a4e-9b7d-4c3a-8e5f-0a1b2c3d4e5f"
	const keptSyncID = "0f8fad5b-d9cb-469f-a165-70867728950e"

	for round := range 15 {
		userID := seedTestUser(t, pool, fmt.Sprintf("conf-setdelete-%d", round))
		store := newStore(pool, userID)
		for _, id := range []string{"main", "p", "q"} {
			if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
				t.Fatalf("CreateProfile %s: %v", id, err)
			}
		}
		if _, err := pool.Exec(ctx, `UPDATE user_profiles SET sync_id = $2, sync_position = 1 WHERE user_id = $1 AND id = 'p'`,
			userID, removedSyncID); err != nil {
			t.Fatalf("seed sync id: %v", err)
		}
		collections := map[string][]string{}
		for _, owner := range []string{"p", "q"} {
			for i := range 3 {
				c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: owner, Name: fmt.Sprintf("%s %d", owner, i)})
				if err != nil {
					t.Fatalf("CreateCollection: %v", err)
				}
				collections[owner] = append([]string{c.ID}, collections[owner]...)
			}
		}
		for viewer, owner := range map[string]string{"p": "q", "q": "p"} {
			if _, err := pool.Exec(ctx, `INSERT INTO user_personal_collection_profiles (user_id, collection_id, profile_id) VALUES ($1, $2, $3)`,
				userID, collections[owner][0], viewer); err != nil {
				t.Fatalf("seed visibility: %v", err)
			}
		}

		var wg sync.WaitGroup
		var setErr, deleteErr, reorderErr error
		wg.Go(func() {
			setErr = newStore(pool, userID).WithProfileSetTransaction(ctx, []string{keptSyncID}, func(w userstore.ProfileSetWriter) error {
				for _, id := range w.Removable() {
					if _, err := w.DeleteProfile(ctx, id); err != nil {
						return err
					}
				}
				if _, err := w.SetSyncFields(ctx, "main", keptSyncID, 0); err != nil {
					return err
				}
				return w.Accept(ctx, 1, make([]byte, 32))
			})
		})
		wg.Go(func() { _, deleteErr = newStore(pool, userID).DeleteProfileAudited(ctx, "q", nil) })
		wg.Go(func() { reorderErr = newStore(pool, userID).ReorderCollections(ctx, "p", collections["p"]) })
		wg.Wait()
		if setErr != nil || deleteErr != nil {
			t.Fatalf("round %d: set = %v, delete = %v", round, setErr, deleteErr)
		}
		var pgErr *pgconn.PgError
		if errors.As(reorderErr, &pgErr) && pgErr.Code == "40P01" {
			t.Fatalf("round %d: ReorderCollections deadlocked: %v", round, reorderErr)
		}
		var left int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_profiles WHERE user_id = $1 AND id IN ('p', 'q')`, userID).Scan(&left); err != nil || left != 0 {
			t.Fatalf("round %d: %d of the removed profiles left (%v)", round, left, err)
		}
	}
}

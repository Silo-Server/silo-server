package pgstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestPostgresUpdateCollectionSyncStatePreservesConcurrentSchedule(t *testing.T) {
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

	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role) VALUES ($1, 'admin') RETURNING id`,
		fmt.Sprintf("collection-sync-state-%d", time.Now().UnixNano()),
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})

	const (
		collectionID     = "sync-state-concurrency"
		capturedSchedule = "0 * * * *"
		editedSchedule   = "0 */6 * * *"
	)
	editedNext := time.Date(2030, time.January, 2, 6, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_personal_collections
		 (id, user_id, profile_id, name, collection_type, sync_schedule, next_sync_at)
		 VALUES ($1, $2, 'profile-1', 'Concurrency test', 'mdblist', $3, $4)`,
		collectionID, userID, editedSchedule, editedNext,
	); err != nil {
		t.Fatalf("seed collection: %v", err)
	}

	store := newStore(pool, userID)
	staleNext := editedNext.Add(24 * time.Hour)
	if err := store.UpdateCollectionSyncState(ctx, userstore.UpdateCollectionSyncStateInput{
		ID:                   collectionID,
		Status:               "success",
		ItemCount:            3,
		LastSyncAt:           time.Now().UTC(),
		ExpectedSyncSchedule: stringPointer(capturedSchedule),
		NextSyncAt:           &staleNext,
	}); err != nil {
		t.Fatalf("update with stale schedule: %v", err)
	}

	var gotNext time.Time
	if err := pool.QueryRow(ctx,
		`SELECT next_sync_at FROM user_personal_collections WHERE user_id = $1 AND id = $2`,
		userID, collectionID,
	).Scan(&gotNext); err != nil {
		t.Fatalf("read preserved next sync: %v", err)
	}
	if !gotNext.Equal(editedNext) {
		t.Fatalf("next_sync_at after concurrent edit = %v, want %v", gotNext, editedNext)
	}

	currentNext := editedNext.Add(48 * time.Hour)
	if err := store.UpdateCollectionSyncState(ctx, userstore.UpdateCollectionSyncStateInput{
		ID:                   collectionID,
		Status:               "success",
		ItemCount:            4,
		LastSyncAt:           time.Now().UTC(),
		ExpectedSyncSchedule: stringPointer(editedSchedule),
		NextSyncAt:           &currentNext,
	}); err != nil {
		t.Fatalf("update with current schedule: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT next_sync_at FROM user_personal_collections WHERE user_id = $1 AND id = $2`,
		userID, collectionID,
	).Scan(&gotNext); err != nil {
		t.Fatalf("read advanced next sync: %v", err)
	}
	if !gotNext.Equal(currentNext) {
		t.Fatalf("next_sync_at with current schedule = %v, want %v", gotNext, currentNext)
	}
}

func stringPointer(value string) *string { return &value }

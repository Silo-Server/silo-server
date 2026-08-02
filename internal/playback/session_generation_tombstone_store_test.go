package playback_test

import (
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestPGSessionGenerationTombstoneStorePersistsAcrossInstancesAndHonorsExpiry(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run playback database tests")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)

	store1 := playback.NewPGSessionGenerationTombstoneStore(pool)
	store2 := playback.NewPGSessionGenerationTombstoneStore(pool)
	sessionID := "tombstone-store-" + uuid.NewString()
	generation := uuid.NewString()
	now := time.Now().UTC()

	if err := store1.RecordEndedSessionGeneration(t.Context(), sessionID, generation, now.Add(time.Hour)); err != nil {
		t.Fatalf("record tombstone: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM playback_session_generation_tombstones WHERE session_id=$1`, sessionID)
	})
	ended, err := store2.WasSessionGenerationEnded(t.Context(), sessionID, generation, now)
	if err != nil {
		t.Fatalf("read tombstone from second store: %v", err)
	}
	if !ended {
		t.Fatal("durable tombstone was not visible to a new store instance")
	}

	expiredGeneration := uuid.NewString()
	if err := store1.RecordEndedSessionGeneration(t.Context(), sessionID, expiredGeneration, now.Add(-time.Second)); err != nil {
		t.Fatalf("record expired tombstone: %v", err)
	}
	ended, err = store2.WasSessionGenerationEnded(t.Context(), sessionID, expiredGeneration, now)
	if err != nil {
		t.Fatalf("read expired tombstone: %v", err)
	}
	if ended {
		t.Fatal("expired tombstone still blocks reconstruction")
	}

	legacySessionID := "legacy-tombstone-store-" + uuid.NewString()
	if err := store1.RecordEndedSessionGeneration(t.Context(), legacySessionID, "", now.Add(time.Hour)); err != nil {
		t.Fatalf("record legacy tombstone: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM playback_session_generation_tombstones WHERE session_id=$1`, legacySessionID)
	})
	var persisted string
	if err := pool.QueryRow(t.Context(), `SELECT session_generation::text FROM playback_session_generation_tombstones WHERE session_id=$1`, legacySessionID).Scan(&persisted); err != nil {
		t.Fatalf("read legacy tombstone sentinel: %v", err)
	}
	if persisted != playback.LegacySessionGenerationSentinel {
		t.Fatalf("legacy tombstone generation = %q, want sentinel", persisted)
	}
	if ended, err := store2.WasSessionGenerationEnded(t.Context(), legacySessionID, "", now); err != nil || !ended {
		t.Fatalf("legacy tombstone lookup = %v, err=%v", ended, err)
	}
}

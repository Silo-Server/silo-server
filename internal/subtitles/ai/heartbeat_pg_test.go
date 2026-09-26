package ai

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSubtitleAIHeartbeatPostgresTerminalSignal(t *testing.T) {
	dsn := os.Getenv("SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL must name a disposable PostgreSQL test database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(t.Context(), `CREATE TEMP TABLE subtitle_ai_jobs(id bigint PRIMARY KEY,status text,heartbeat_at timestamptz);
 INSERT INTO subtitle_ai_jobs VALUES(1,'pending','2000-01-01'),(2,'running','2000-01-01'),(3,'completed','2000-01-01'),(4,'failed','2000-01-01'),(5,'cancelled','2000-01-01')`)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPgJobRepository(pool)
	for id := int64(1); id <= 6; id++ {
		var before time.Time
		if id <= 5 {
			if err := pool.QueryRow(t.Context(), `SELECT heartbeat_at FROM subtitle_ai_jobs WHERE id=$1`, id).Scan(&before); err != nil {
				t.Fatal(err)
			}
		}
		err := repo.Heartbeat(t.Context(), id)
		terminal := id >= 3
		if errors.Is(err, jobrunner.ErrJobTerminal) != terminal || (!terminal && err != nil) {
			t.Fatalf("job %d heartbeat=%v", id, err)
		}
		if id == 6 {
			continue
		}
		var heartbeat time.Time
		if err := pool.QueryRow(t.Context(), `SELECT heartbeat_at FROM subtitle_ai_jobs WHERE id=$1`, id).Scan(&heartbeat); err != nil {
			t.Fatal(err)
		}
		if unchanged := heartbeat.Equal(before); unchanged != terminal {
			t.Fatalf("job %d heartbeat=%v", id, heartbeat)
		}
	}
	pool.Close()
	if err := repo.Heartbeat(t.Context(), 1); err == nil || errors.Is(err, jobrunner.ErrJobTerminal) {
		t.Fatalf("unavailable database classified as terminal: %v", err)
	}
}

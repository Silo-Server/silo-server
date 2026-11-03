package downloads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func statusEventTestRepo(t *testing.T) *Repository {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("download_events_%d", time.Now().UnixNano())
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(t.Context(), `CREATE TABLE downloads(
 id text PRIMARY KEY,user_id integer NOT NULL,profile_id text,device_id text,media_file_id integer NOT NULL,content_id text NOT NULL,episode_id text,batch_id text,
 kind text NOT NULL,status text NOT NULL,format text NOT NULL,quality text NOT NULL,effective_quality text NOT NULL,target_bitrate_kbps integer NOT NULL,revision integer NOT NULL,
 artifact_id text,file_size bigint NOT NULL,bytes_sent bigint NOT NULL,error_message text NOT NULL,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,completed_at timestamptz,
 revoked_at timestamptz,revoked_by integer,revoked_reason text NOT NULL DEFAULT '')`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260906025444_download_status_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	if _, err := pool.Exec(t.Context(), strings.ReplaceAll(up, "public.", "")); err != nil {
		t.Fatal(err)
	}
	return NewRepository(pool)
}

func TestDownloadStatusEventValidation(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		event StatusEvent
		valid bool
	}{
		{"missing time", StatusEvent{Status: StatusCompleted, Revision: 1}, false},
		{"missing revision", StatusEvent{Status: StatusCompleted, UpdatedAt: now.Add(-time.Minute)}, false},
		{"past event", StatusEvent{Status: StatusCompleted, Revision: 1, UpdatedAt: now.Add(-time.Minute)}, true},
		// A device clock slightly ahead of the server's still reports.
		{"one second ahead", StatusEvent{Status: StatusCompleted, Revision: 1, UpdatedAt: now.Add(time.Second)}, true},
		{"two minutes ahead", StatusEvent{Status: StatusCompleted, Revision: 1, UpdatedAt: now.Add(2 * time.Minute)}, true},
		{"past the skew window", StatusEvent{Status: StatusCompleted, Revision: 1, UpdatedAt: now.Add(2*time.Minute + time.Second)}, false},
		{"an hour ahead", StatusEvent{Status: StatusCompleted, Revision: 1, UpdatedAt: now.Add(time.Hour)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.event.validate(now)
			if tc.valid && err != nil {
				t.Fatalf("valid event refused: %v", err)
			}
			if !tc.valid && !errors.Is(err, ErrInvalidStatusEvent) {
				t.Fatalf("invalid event: err = %v, want ErrInvalidStatusEvent", err)
			}
		})
	}

	service := &Service{}
	if _, err := service.ReportStatus(t.Context(), 1, "one", "device", "id", StatusEvent{Status: StatusReady, Revision: 1, UpdatedAt: time.Now().Add(-time.Minute)}); !errors.Is(err, ErrInvalidStatus) {
		t.Fatal(err)
	}
	if _, err := service.ReportStatus(t.Context(), 1, "one", "", "id", StatusEvent{}); !errors.Is(err, ErrProfileRequired) {
		t.Fatal(err)
	}
}

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
 artifact_id text,file_size bigint NOT NULL,bytes_sent bigint NOT NULL,error_message text NOT NULL,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,completed_at timestamptz)`)
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

func TestDownloadStatusEventsPostgres(t *testing.T) {
	repo := statusEventTestRepo(t)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	original := &Download{ID: "entry", UserID: 1, ProfileID: "one", DeviceID: "device", ContentID: "movie", MediaFileID: 42, Kind: KindQueued, Status: StatusReady, CreatedAt: base, UpdatedAt: base}
	if err := repo.Create(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	service := &Service{repo: repo}
	event := StatusEvent{Status: StatusCompleted, UpdatedAt: base.Add(time.Minute), Revision: 1}
	current, err := service.ReportStatus(t.Context(), 1, "one", "device", "entry", event)
	if err != nil || current.Status != StatusCompleted || !current.CompletedAt.Equal(event.UpdatedAt) {
		t.Fatalf("%+v %v", current, err)
	}
	for _, at := range []time.Time{event.UpdatedAt, event.UpdatedAt.Add(-time.Second)} {
		got, err := service.ReportStatus(t.Context(), 1, "one", "device", "entry", StatusEvent{Status: StatusDownloading, UpdatedAt: at, Revision: 1})
		if err != nil || got.Status != StatusCompleted || got.CompletedAt == nil {
			t.Fatalf("rewound: %+v %v", got, err)
		}
	}
	for _, scope := range []struct {
		user            int
		profile, device string
	}{{2, "one", "device"}, {1, "two", "device"}, {1, "one", "other"}} {
		if _, err := service.ReportStatus(t.Context(), scope.user, scope.profile, scope.device, "entry", event); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	}
	// Concurrent events converge to the latest event, regardless of arrival order.
	start := make(chan struct{})
	results := make(chan error, 10)
	for i := range 10 {
		go func() {
			<-start
			_, err := service.ReportStatus(t.Context(), 1, "one", "device", "entry", StatusEvent{Status: StatusDownloading, UpdatedAt: base.Add(time.Duration(i+2) * time.Minute), Revision: 1})
			results <- err
		}()
	}
	close(start)
	for range 10 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	current, err = repo.GetByID(t.Context(), "entry")
	if err != nil || !current.StatusEventAt.Equal(base.Add(11*time.Minute)) {
		t.Fatalf("%+v %v", current, err)
	}
	// A frozen v1 report still updates the same lifecycle, and fences older v2 events.
	legacyAt := time.Now().UTC()
	if err := repo.UpdateManagedStatus(t.Context(), "entry", 1, "one", "device", StatusCompleted, &legacyAt); err != nil {
		t.Fatal(err)
	}
	got, err := service.ReportStatus(t.Context(), 1, "one", "device", "entry", StatusEvent{Status: StatusDownloading, UpdatedAt: base.Add(12 * time.Minute), Revision: 1})
	if err != nil || got.Status != StatusCompleted {
		t.Fatalf("legacy rewound: %+v %v", got, err)
	}
	replacement := *current
	replacement.MediaFileID = 43
	replacement.Status = StatusReady
	replacement.Format = FormatOriginal
	replacement.Quality = QualityOriginal
	replacement.EffectiveQuality = QualityOriginal
	replaced, err := repo.ReplaceManagedEntry(t.Context(), current, &replacement)
	if err != nil || replaced.Revision != 2 || replaced.StatusEventAt != nil {
		t.Fatalf("replacement: %+v %v", replaced, err)
	}
	if _, err := service.ReportStatus(t.Context(), 1, "one", "device", "entry", event); !errors.Is(err, ErrStatusConflict) {
		t.Fatal(err)
	}
	event.Revision = 2
	got, err = service.ReportStatus(t.Context(), 1, "one", "device", "entry", event)
	if err != nil || got.Status != StatusCompleted {
		t.Fatalf("new revision: %+v %v", got, err)
	}
	if _, err := repo.pool.Exec(t.Context(), `UPDATE downloads SET status='preparing' WHERE id='entry'`); err != nil {
		t.Fatal(err)
	}
	event.UpdatedAt = event.UpdatedAt.Add(time.Minute)
	if _, err := service.ReportStatus(t.Context(), 1, "one", "device", "entry", event); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestDownloadStatusEventValidation(t *testing.T) {
	service := &Service{}
	for _, event := range []StatusEvent{
		{Status: StatusCompleted, Revision: 1},
		{Status: StatusCompleted, Revision: 1, UpdatedAt: time.Now().Add(time.Hour)},
		{Status: StatusCompleted, UpdatedAt: time.Now().Add(-time.Minute)},
		{Status: StatusReady, Revision: 1, UpdatedAt: time.Now().Add(-time.Minute)},
	} {
		if _, err := service.ReportStatus(t.Context(), 1, "one", "device", "id", event); err == nil {
			t.Fatal("invalid event accepted")
		}
	}
	if _, err := service.ReportStatus(t.Context(), 1, "one", "", "id", StatusEvent{}); !errors.Is(err, ErrProfileRequired) {
		t.Fatal(err)
	}
}

func TestDownloadRegistryPagesPostgres(t *testing.T) {
	repo := statusEventTestRepo(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	for _, row := range []*Download{
		{ID: "b", UserID: 1, ProfileID: "one", DeviceID: "device"},
		{ID: "a", UserID: 1, ProfileID: "one", DeviceID: "device"},
		{ID: "c", UserID: 1, ProfileID: "two", DeviceID: "device"},
		{ID: "d", UserID: 1, ProfileID: "one", DeviceID: "other"},
		{ID: "ephemeral", UserID: 1},
		{ID: "foreign", UserID: 2},
	} {
		row.ContentID = "movie"
		row.MediaFileID = 42
		row.Kind = KindQueued
		row.Status = StatusReady
		row.CreatedAt = at
		row.UpdatedAt = at
		if err := repo.Create(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{repo: repo}
	first, err := service.ListPage(t.Context(), 1, "one", "device", nil, 1)
	if err != nil || len(first) != 1 || first[0].ID != "b" {
		t.Fatalf("%+v %v", first, err)
	}
	next, err := service.ListPage(t.Context(), 1, "one", "device", &RegistryPosition{CreatedAt: first[0].CreatedAt, ID: first[0].ID}, 1)
	if err != nil || len(next) != 1 || next[0].ID != "a" {
		t.Fatalf("%+v %v", next, err)
	}
	ephemeral, err := service.ListPage(t.Context(), 1, "one", "", nil, 50)
	if err != nil || len(ephemeral) != 1 || ephemeral[0].ID != "ephemeral" {
		t.Fatalf("%+v %v", ephemeral, err)
	}
	if _, err := service.ListPage(t.Context(), 1, "", "device", nil, 50); !errors.Is(err, ErrProfileRequired) {
		t.Fatal(err)
	}
	for _, limit := range []int{0, 102} {
		if _, err := service.ListPage(t.Context(), 1, "one", "device", nil, limit); err == nil {
			t.Fatal("unbounded page accepted")
		}
	}
}

func TestDownloadStatusMigrationFencesBridgeRows(t *testing.T) {
	repo := statusEventTestRepo(t)
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	row := &Download{ID: "bridge", UserID: 1, ProfileID: "one", DeviceID: "device", ContentID: "movie", MediaFileID: 42, Kind: KindQueued, Status: StatusCompleted, CreatedAt: at, UpdatedAt: at, CompletedAt: &at}
	if err := repo.Create(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260906025444_download_status_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(migration), "-- +goose Down")
	if !ok {
		t.Fatal("missing down migration")
	}
	// Exercise the exact migration in an isolated schema with a bridge row present.
	for _, sql := range []string{down, up} {
		if _, err := repo.pool.Exec(t.Context(), strings.ReplaceAll(sql, "public.", "")); err != nil {
			t.Fatal(err)
		}
	}
	current, err := repo.GetByID(t.Context(), "bridge")
	if err != nil || current.StatusEventAt == nil || !current.StatusEventAt.Equal(at) {
		t.Fatalf("%+v %v", current, err)
	}
	got, err := repo.ReportStatus(t.Context(), 1, "one", "device", "bridge", StatusEvent{Status: StatusDownloading, UpdatedAt: at.Add(-time.Second), Revision: 1})
	if err != nil || got.Status != StatusCompleted {
		t.Fatalf("bridge state rewound: %+v %v", got, err)
	}
}

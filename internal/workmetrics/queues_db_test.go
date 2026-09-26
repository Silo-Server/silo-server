package workmetrics

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Exercise the migrated status-fencing trigger as well as the sampler: recipe
// variants must contribute to the same two bounded queue states, while terminal
// artifacts must contribute neither depth nor oldest-request timestamps.
func TestQueueSamplerIncludesDownloadRecipeVariants(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL requires an isolated migrated database")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := &QueueSampler{pool: pool, samples: make(map[string]queueSample)}
	s.sample(t.Context())
	before := s.samples[workloadDownloads]
	if !before.available {
		t.Fatal("download queue query failed before seeding")
	}

	var folder, file int
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders (type,name,enabled) VALUES ('series','observability-download-queue-test',true) RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folder) }()
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_files (media_folder_id,file_path) VALUES ($1,$2) RETURNING id`, folder, fmt.Sprintf("/observability-download-queue-test/%d.mkv", folder)).Scan(&file); err != nil {
		t.Fatal(err)
	}
	baseTime := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	wantOldest := make(map[string]time.Time)
	for _, recipe := range []string{"plain", "tone_map", "audio_v2"} {
		for i, status := range []string{stateQueued, "running", "ready", "failed"} {
			at := baseTime.Add(time.Duration(i) * time.Hour)
			if status == "ready" || status == "failed" {
				at = baseTime.Add(-24 * time.Hour)
			} else {
				wantOldest[status] = at
			}
			id := fmt.Sprintf("observability-download-%d-%s-%s", file, recipe, status)
			var actualStatus string
			if err := pool.QueryRow(t.Context(), `INSERT INTO download_artifacts
				(id,media_file_id,format,params_hash,status,created_at,tone_map_policy,tone_map_mode,tone_map_source_kind,tone_map_recipe_version,tone_map_source_revision,audio_recipe_version)
				VALUES ($1,$2,'transcode',$1,$3,$4,
				 CASE WHEN $5='tone_map' THEN 'software_only' ELSE 'none' END,
				 CASE WHEN $5='tone_map' THEN 'software' ELSE '' END,
				 CASE WHEN $5='tone_map' THEN 'pq' ELSE '' END,
				 CASE WHEN $5='tone_map' THEN '1' ELSE '' END,
				 CASE WHEN $5='tone_map' THEN 'synthetic-test-revision' ELSE '' END,
				 CASE WHEN $5='audio_v2' THEN '2' ELSE '' END)
				RETURNING status`, id, file, status, at, recipe).Scan(&actualStatus); err != nil {
				t.Fatal(err)
			}
			wantStatus := status
			if recipe != "plain" && status != "failed" {
				wantStatus = recipe + "_" + status
			}
			if actualStatus != wantStatus {
				t.Fatalf("fixture trigger returned %q, want %q", actualStatus, wantStatus)
			}
		}
	}

	s.sample(t.Context())
	after := s.samples[workloadDownloads]
	if !after.available {
		t.Fatal("download queue query failed after seeding")
	}
	if len(after.states) != 2 {
		t.Fatalf("unbounded download queue states: %+v", after.states)
	}
	for _, state := range []string{stateQueued, "running"} {
		old, got := before.states[state], after.states[state]
		if got.count != old.count+3 {
			t.Errorf("%s count = %v, want %v", state, got.count, old.count+3)
		}
		want := wantOldest[state]
		if old.oldest != nil && old.oldest.Before(want) {
			want = *old.oldest
		}
		if got.oldest == nil || !got.oldest.Equal(want) {
			t.Errorf("%s oldest = %v, want %v", state, got.oldest, want)
		}
	}
}

func TestQueueSamplerReadsMigratedSchemaAndFailureIsUnavailable(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL requires an isolated migrated database")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := &QueueSampler{pool: pool, samples: make(map[string]queueSample)}
	s.sample(t.Context())
	for _, q := range queueQueries {
		if !s.samples[q.name].available {
			t.Errorf("queue query failed for migrated schema: %s", q.name)
		}
	}
	var folder int
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders (type,name,enabled) VALUES ('series','observability-test',true) RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folder) }()
	id := "observability-queue-" + time.Now().Format("150405.000000000")
	if _, err := pool.Exec(t.Context(), `INSERT INTO scan_runs(id,media_folder_id,mode,status,requested_at) VALUES ($1,$2,'library','accepted',now()-interval '2 minutes')`, id, folder); err != nil {
		t.Fatal(err)
	}
	s.sample(t.Context())
	if v := s.samples["scan"].states["queued"]; v.count < 1 || v.oldest == nil || time.Since(*v.oldest) < time.Minute {
		t.Fatalf("queued state missing: %+v", v)
	}
	failedPool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	failedPool.Close()
	s.pool = failedPool
	s.sample(t.Context())
	for _, q := range queueQueries {
		if sample := s.samples[q.name]; sample.available || sample.failures != 1 {
			t.Errorf("failed sampler retained availability: %s %+v", q.name, sample)
		}
	}
}

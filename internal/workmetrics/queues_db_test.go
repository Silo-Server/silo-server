package workmetrics

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
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

func insertTestImageCacheJob(t *testing.T, pool *pgxpool.Pool, targetID, status string, createdAt, nextAttemptAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `INSERT INTO metadata_image_cache_jobs
		(target_type, target_content_id, target_language, series_id, source_path, provider_id, provider_content_id, content_type, image_type, status, created_at, next_attempt_at)
		VALUES ('item', $1, '', $1, 'https://example.invalid/poster.jpg', 'test', $1, 'movie', 'poster', $2, $3, $4)`,
		targetID, status, createdAt, nextAttemptAt); err != nil {
		t.Fatal(err)
	}
}

// Future-deferred retries in metadata_image_cache_jobs stay counted in queued
// depth without setting an oldest queued timestamp, while due queued rows
// report min(next_attempt_at) and running rows keep reporting min(created_at).
func TestQueueSamplerImagesReportsDueQueuedAgeDB(t *testing.T) {
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
	registry := prometheus.NewRegistry()
	registry.MustRegister(s)
	s.sample(t.Context())
	before := s.samples[workloadImages]
	if !before.available {
		t.Fatal("images queue query failed before seeding")
	}

	prefix := fmt.Sprintf("observability-images-%d-", time.Now().UnixNano())
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM metadata_image_cache_jobs WHERE target_content_id LIKE $1`, prefix+"%")
	}()

	now := time.Now().UTC().Truncate(time.Microsecond)
	runningCreated := now.Add(-3 * time.Hour)
	insertTestImageCacheJob(t, pool, prefix+"deferred", stateQueued, now.Add(-48*time.Hour), now.Add(7*24*time.Hour))
	insertTestImageCacheJob(t, pool, prefix+"running", stateRunning, runningCreated, now.Add(-2*time.Hour))
	insertTestImageCacheJob(t, pool, prefix+"succeeded", "succeeded", now.Add(-72*time.Hour), now.Add(-72*time.Hour))
	insertTestImageCacheJob(t, pool, prefix+"failed", "failed", now.Add(-72*time.Hour), now.Add(-72*time.Hour))

	s.sample(t.Context())
	afterDeferred := s.samples[workloadImages]
	if !afterDeferred.available {
		t.Fatal("images queue query failed after seeding deferred rows")
	}
	if len(afterDeferred.states) != 2 {
		t.Fatalf("unbounded images queue states: %+v", afterDeferred.states)
	}
	if got := afterDeferred.states[stateQueued]; got.count != before.states[stateQueued].count+1 {
		t.Errorf("deferred queued count = %v, want %v", got.count, before.states[stateQueued].count+1)
	}
	if before.states[stateQueued].oldest == nil {
		if got := afterDeferred.states[stateQueued].oldest; got != nil {
			t.Errorf("deferred-only queued oldest = %v, want nil", got)
		}
		if got, ok := findQueueGauge(t, registry, "silo_queue_oldest_requested_timestamp_seconds", workloadImages, stateQueued); ok {
			t.Errorf("deferred-only queued oldest metric unexpectedly exported: %v", got)
		}
	}
	if got, ok := findQueueGauge(t, registry, "silo_queue_items", workloadImages, stateQueued); !ok || got != before.states[stateQueued].count+1 {
		t.Errorf("deferred queued depth metric = (%v, %v), want (%v, true)", got, ok, before.states[stateQueued].count+1)
	}
	wantRunning := runningCreated
	if prev := before.states[stateRunning].oldest; prev != nil && prev.Before(wantRunning) {
		wantRunning = *prev
	}
	if got := afterDeferred.states[stateRunning]; got.count != before.states[stateRunning].count+1 || got.oldest == nil || !got.oldest.Equal(wantRunning) {
		t.Errorf("running state = %+v, want count=%v oldest=%v", got, before.states[stateRunning].count+1, wantRunning)
	}

	dueOlderRetry := now.Add(-20 * time.Minute)
	dueOldestDue := now.Add(-45 * time.Minute)
	insertTestImageCacheJob(t, pool, prefix+"due-older-retry", stateQueued, now.Add(-24*time.Hour), dueOlderRetry)
	insertTestImageCacheJob(t, pool, prefix+"due-oldest-due", stateQueued, now.Add(-2*time.Hour), dueOldestDue)

	s.sample(t.Context())
	afterDue := s.samples[workloadImages]
	if !afterDue.available {
		t.Fatal("images queue query failed after seeding due rows")
	}
	wantDue := dueOldestDue
	if prev := before.states[stateQueued].oldest; prev != nil && prev.Before(wantDue) {
		wantDue = *prev
	}
	if got := afterDue.states[stateQueued]; got.count != before.states[stateQueued].count+3 || got.oldest == nil || !got.oldest.Equal(wantDue) {
		t.Errorf("due queued state = %+v, want count=%v oldest=%v", got, before.states[stateQueued].count+3, wantDue)
	}
	if got, ok := findQueueGauge(t, registry, "silo_queue_oldest_requested_timestamp_seconds", workloadImages, stateQueued); !ok || got != float64(wantDue.Unix()) {
		t.Errorf("due queued oldest metric = (%v, %v), want (%v, true)", got, ok, float64(wantDue.Unix()))
	}
}

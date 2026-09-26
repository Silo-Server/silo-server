package workmetrics

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// These are shared database snapshots, replicated by API processes. Use max by
// (cluster,queue,state), never sum across instances. Queries exclude terminal
// history, have deadlines, and execute only in the background.
var queueQueries = []struct{ name, sql string }{
	{workloadScan, `SELECT status, count(*), min(requested_at) FROM scan_runs WHERE status IN ('accepted','running') GROUP BY status`},
	{"admin", `SELECT status, count(*), min(requested_at) FROM admin_jobs WHERE status IN ('queued','running') GROUP BY status`},
	{"history_import", `SELECT status, count(*), min(created_at) FROM history_import_runs WHERE status IN ('queued','running') GROUP BY status`},
	{workloadDownloads, `SELECT CASE WHEN status IN ('queued','tone_map_queued','audio_v2_queued') THEN 'queued' ELSE 'running' END, count(*), min(created_at) FROM download_artifacts WHERE status IN ('queued','running','tone_map_queued','tone_map_running','audio_v2_queued','audio_v2_running') GROUP BY 1`},
	{"subtitles", `SELECT status, count(*), min(created_at) FROM subtitle_ai_jobs WHERE status IN ('pending','running') GROUP BY status`},
	{workloadSearch, `SELECT 'queued', count(*), min(created_at) FROM catalog_search_index_events WHERE processed_at IS NULL`},
	{workloadNotifications, `SELECT 'queued', count(*), min(created_at) FROM release_events WHERE processed_at IS NULL`},
	{"matching_movies", `SELECT CASE WHEN lease_token = '' THEN 'queued' ELSE 'running' END, count(*), min(first_queued_at) FROM movie_match_queue WHERE state = 'pending' GROUP BY 1`},
	{"matching_series", `SELECT CASE WHEN lease_token = '' THEN 'queued' ELSE 'running' END, count(*), min(first_queued_at) FROM series_root_match_queue WHERE state = 'pending' GROUP BY 1`},
	{workloadImages, `SELECT status, count(*), min(created_at) FROM metadata_image_cache_jobs WHERE status IN ('queued','running') GROUP BY status`},
	{"metadata_translation", `SELECT status, count(*), min(created_at) FROM metadata_translation_jobs WHERE status IN ('pending','running') GROUP BY status`},
	{"autoscan_intake", `SELECT CASE WHEN locked_by = '' THEN 'queued' ELSE 'running' END, count(*), min(received_at) FROM autoscan_webhook_deliveries GROUP BY 1`},
	{"webhook_delivery", `SELECT 'queued', count(*), min(attempted_at) FROM webhook_delivery_attempts WHERE outcome IN ('pending','retrying')`},
	{"web_push_delivery", `SELECT 'queued', count(*), min(attempted_at) FROM web_push_delivery_attempts WHERE outcome IN ('pending','retrying')`},
	{"native_push_delivery", `SELECT 'queued', count(*), min(created_at) FROM push_delivery_attempts WHERE outcome IN ('pending','retrying')`},
}
var queueDepthDesc = prometheus.NewDesc("silo_queue_items", "Shared durable queue items; aggregate replicas with max, not sum. Queued includes delayed retries.", []string{labelQueue, "state"}, nil)
var queueOldestDesc = prometheus.NewDesc("silo_queue_oldest_requested_timestamp_seconds", "Oldest request timestamp by shared queue state; absent when empty or unavailable.", []string{labelQueue, "state"}, nil)
var queueSampleDesc = prometheus.NewDesc("silo_queue_sample_timestamp_seconds", "Last successful shared queue sample. Samples older than 90 seconds are omitted.", []string{labelQueue}, nil)
var queueAvailableDesc = prometheus.NewDesc("silo_queue_sample_available", "Whether the last queue query succeeded and its sample is fresh.", []string{labelQueue}, nil)
var queueErrorsDesc = prometheus.NewDesc("silo_queue_sample_errors_total", "Background queue query failures; no raw error strings are exported.", []string{labelQueue}, nil)
var queueDurationDesc = prometheus.NewDesc("silo_queue_sample_duration_seconds", "Wall time of the last background queue query including pool wait.", []string{labelQueue}, nil)

type queueState struct {
	count  float64
	oldest *time.Time
}

const stateRunning = "running"

type queueSample struct {
	states    map[string]queueState
	at        time.Time
	available bool
	failures  float64
	duration  float64
}
type QueueSampler struct {
	pool    *pgxpool.Pool
	mu      sync.RWMutex
	samples map[string]queueSample
}

// StartQueueSampler registers one collector for this application's shared DB.
// It does not query during construction or scraping. Cancellation unregisters it.
func StartQueueSampler(ctx context.Context, pool *pgxpool.Pool) error {
	s := &QueueSampler{pool: pool, samples: make(map[string]queueSample)}
	if err := prometheus.Register(s); err != nil {
		return err
	}
	go func() {
		defer prometheus.Unregister(s)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			s.sample(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}
func (s *QueueSampler) sample(ctx context.Context) {
	for _, query := range queueQueries {
		if ctx.Err() != nil {
			return
		}
		begin := time.Now()
		queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		states := map[string]queueState{stateQueued: {}, stateRunning: {}}
		rows, err := s.pool.Query(queryCtx, query.sql)
		if err == nil {
			for rows.Next() {
				var state string
				var count int64
				var oldest *time.Time
				if err = rows.Scan(&state, &count, &oldest); err != nil {
					break
				}
				if state == "pending" || state == "accepted" {
					state = stateQueued
				}
				if state != stateQueued && state != stateRunning {
					continue
				}
				states[state] = queueState{count: float64(count), oldest: oldest}
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
		cancel()
		s.mu.Lock()
		previous := s.samples[query.name]
		previous.duration = time.Since(begin).Seconds()
		previous.available = err == nil
		if err != nil {
			previous.failures++
		} else {
			previous.states = states
			previous.at = time.Now()
		}
		s.samples[query.name] = previous
		s.mu.Unlock()
	}
}
func (s *QueueSampler) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{queueDepthDesc, queueOldestDesc, queueSampleDesc, queueAvailableDesc, queueErrorsDesc, queueDurationDesc} {
		ch <- d
	}
}
func (s *QueueSampler) Collect(ch chan<- prometheus.Metric) {
	s.mu.RLock()
	snapshot := make(map[string]queueSample, len(s.samples))
	for k, v := range s.samples {
		snapshot[k] = v
	}
	s.mu.RUnlock()
	for _, query := range queueQueries {
		name := query.name
		sample := snapshot[name]
		available := sample.available && time.Since(sample.at) <= 90*time.Second
		value := 0.0
		if available {
			value = 1
		}
		ch <- prometheus.MustNewConstMetric(queueAvailableDesc, prometheus.GaugeValue, value, name)
		ch <- prometheus.MustNewConstMetric(queueErrorsDesc, prometheus.CounterValue, sample.failures, name)
		if !sample.at.IsZero() {
			ch <- prometheus.MustNewConstMetric(queueSampleDesc, prometheus.GaugeValue, float64(sample.at.Unix()), name)
		}
		if sample.duration > 0 {
			ch <- prometheus.MustNewConstMetric(queueDurationDesc, prometheus.GaugeValue, sample.duration, name)
		}
		if !available {
			continue
		}
		for state, v := range sample.states {
			ch <- prometheus.MustNewConstMetric(queueDepthDesc, prometheus.GaugeValue, v.count, name, state)
			if v.oldest != nil {
				ch <- prometheus.MustNewConstMetric(queueOldestDesc, prometheus.GaugeValue, float64(v.oldest.Unix()), name, state)
			}
		}
	}
}

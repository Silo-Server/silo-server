// Package workmetrics records bounded, per-process workload observations.
// Durable queues retain authority over job state; these metrics describe attempts.
package workmetrics

import (
	"context"
	"runtime/pprof"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/telemetry"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	workloadScan          = "scan"
	workloadDownloads     = "downloads"
	workloadSearch        = "search"
	workloadNotifications = "notifications"
	workloadImages        = "images"
	labelQueue            = "queue"
	stateQueued           = "queued"
	labelWorkload         = "workload"
	outcomeSuccess        = "success"
	outcomeError          = "error"
	outcomeFailed         = "failed"
	workloadPlugin        = "plugin"
	legacyCanceled        = "cancelled" //nolint:misspell // Existing durable job wire status.
)

var active = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "silo_work_active", Help: "Work attempts currently executing in this process; sum across instances."}, []string{labelWorkload})
var completed = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_work_attempts_total", Help: "Finished execution attempts; unknown means no confirmed terminal outcome."}, []string{labelWorkload, "outcome"})
var duration = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "silo_work_duration_seconds", Help: "Wall time per execution attempt.", Buckets: []float64{.01, .1, 1, 5, 15, 60, 300, 900, 3600}}, []string{labelWorkload})
var queueWait = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "silo_work_queue_wait_seconds", Help: "Age from durable request time to attempt start; includes retries.", Buckets: []float64{.1, 1, 5, 15, 60, 300, 900, 3600, 86400}}, []string{labelWorkload})
var progress = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_work_progress_updates_total", Help: "Observed progress updates; heartbeats are excluded."}, []string{labelWorkload})
var lastProgress = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "silo_work_last_progress_timestamp_seconds", Help: "Last observed progress or attempt start in this process. Aggregate progress cannot prove that every concurrent job advances."}, []string{labelWorkload})
var recoveries = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_work_recoveries_total", Help: "Durable stale claims recovered by this process."}, []string{labelWorkload})

// Category folds dynamic plugin task keys and future tasks into finite categories.
// Do not add identities (provider slugs, job IDs, paths) to this vocabulary.
func Category(key string) string {
	switch key {
	case workloadScan, "scan_libraries", "autoscan_poll", "autoscan_webhook_retry":
		return workloadScan
	case "match_media", "matcher":
		return "matching"
	case "cache_metadata_images", "backfill_metadata_images", "reconcile_artwork_cache", "verify_artwork_delivery", "cleanup_artwork_revisions", workloadImages:
		return workloadImages
	case "sync_catalog_search_index", "rebuild_catalog_search_index", workloadSearch:
		return workloadSearch
	case "detect_intro_markers", "contribute_markers", "intro":
		return "intro"
	case "chapter_thumbnail_backfill", "chapters":
		return "chapters"
	case "encode_download_artifacts", workloadDownloads:
		return workloadDownloads
	case "sync_watch_providers", "watch_sync":
		return "watch_sync"
	case workloadNotifications, "notification_delivery", "history_import", "recommendations", "ai", "subtitles", workloadPlugin, "admin", "transcode", "remux", "probe":
		return key
	case "refresh_metadata", "sync_manga_metadata", "sync_audiobook_metadata", "sync_ebook_metadata", "backfill_ebook_metadata", "sync_podcast_feeds", "metadata":
		return "metadata"
	case "refresh_trending_discover":
		return "recommendations"
	case "seed_content_availability", "rebuild_release_interest":
		return workloadNotifications
	case "reconcile_requests", "reconcile_watch_history", "cleanup_activity_log", "cleanup_policy_decision_log", "cleanup_client_diagnostics", "cleanup_auth_sessions", "cleanup_operational_log", "cleanup_task_history", "cleanup_catalog_search_index_events", "cleanup_orphaned_media_items", "setting_mutations_retention", "notifications_retention", "backfill_media_item_aliases", "repair_provider_id_integrity", "sync_collections", "sync_user_collections", "check_plugin_updates":
		return "maintenance"
	default:
		if strings.HasPrefix(key, "plugin:") || strings.HasPrefix(key, "plugin_") {
			return workloadPlugin
		}
		return "other"
	}
}

func Outcome(value string) string {
	switch value {
	case outcomeSuccess, "completed", "complete":
		return outcomeSuccess
	case outcomeError, outcomeFailed:
		return "error"
	case "canceled", legacyCanceled:
		return "canceled"
	case "timeout":
		return "timeout"
	default:
		return "unknown"
	}
}

type runKey struct{}

type Run struct {
	parent   context.Context
	category string
	start    time.Time
	span     trace.Span
	once     sync.Once
}

// Start links an initiating request when present, then starts an independent
// workload trace. No long-lived request span or durable identity is retained.
func Start(ctx context.Context, workload string, queuedAt time.Time) (context.Context, *Run) {
	parentCtx := ctx
	category := Category(workload)
	opts := []trace.SpanStartOption{trace.WithNewRoot(), trace.WithAttributes(attribute.String(labelWorkload, category))}
	if parent := trace.SpanContextFromContext(ctx); parent.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: parent}))
	}
	ctx, span := otel.Tracer("silo/work").Start(telemetry.PublicContext(ctx), "work."+category, opts...)
	run := &Run{parent: parentCtx, category: category, start: time.Now(), span: span}
	active.WithLabelValues(category).Inc()
	if !queuedAt.IsZero() && !queuedAt.After(run.start) {
		queueWait.WithLabelValues(category).Observe(run.start.Sub(queuedAt).Seconds())
	}
	lastProgress.WithLabelValues(category).SetToCurrentTime()
	return context.WithValue(pprof.WithLabels(ctx, pprof.Labels(labelWorkload, category)), runKey{}, run), run
}

// Do makes the bounded workload label available to CPU/goroutine profiling.
func Do(ctx context.Context, fn func(context.Context)) { pprof.Do(ctx, pprof.Labels(), fn) }
func Progress(workload string) {
	category := Category(workload)
	progress.WithLabelValues(category).Inc()
	lastProgress.WithLabelValues(category).SetToCurrentTime()
}
func Recovered(workload string, count int64) {
	if count > 0 {
		recoveries.WithLabelValues(Category(workload)).Add(float64(count))
	}
}
func (r *Run) Finish(outcome string) {
	r.once.Do(func() {
		outcome = Outcome(outcome)
		active.WithLabelValues(r.category).Dec()
		completed.WithLabelValues(r.category, outcome).Inc()
		duration.WithLabelValues(r.category).Observe(time.Since(r.start).Seconds())
		if outcome != outcomeSuccess {
			r.span.SetStatus(codes.Error, outcome)
		}
		r.span.SetAttributes(attribute.String("outcome", outcome))
		r.span.End()
	})
}

// FinishContext records an outcome only for a currently observed attempt.
// Owners call it after a confirmed durable transition, never from read APIs.
func FinishContext(ctx context.Context, outcome string) {
	if run, ok := ctx.Value(runKey{}).(*Run); ok {
		run.Finish(outcome)
	}
}

// Profile applies the workload labels to the calling goroutine and restores
// its initiating context's labels on return. Defer the result in that same
// goroutine; Finish may be called by another goroutine and cannot restore labels.
func Profile(ctx context.Context) func() {
	run, ok := ctx.Value(runKey{}).(*Run)
	if !ok {
		return func() {}
	}
	pprof.SetGoroutineLabels(ctx)
	return func() { pprof.SetGoroutineLabels(run.parent) }
}

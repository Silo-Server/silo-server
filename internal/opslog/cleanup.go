package opslog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	keyEnabled                = "opslog.enabled"
	keyCaptureLevel           = "opslog.capture_level"
	keyRetentionDays          = "opslog.retention_days"
	keyCleanupInterval        = "opslog.cleanup_interval_minutes"
	keyMaxRows                = "opslog.max_rows"
	keyMaxSizeMB              = "opslog.max_size_mb"
	keyBucketPolicies         = "opslog.bucket_policies"
	defaultEnabled            = "true"
	defaultCaptureLevel       = "info"
	defaultRetentionStr       = "7"
	defaultRetention          = 7
	defaultCleanupIntervalStr = "15"
	defaultCleanupInterval    = 15
	defaultMaxRowsStr         = "1000000"
	defaultMaxRows            = int64(1_000_000)
	defaultMaxSizeMBStr       = "1024"
	defaultMaxSizeMB          = int64(1024)
	cleanupBatchSize          = 10000
)

var defaultBucketPolicies = []BucketPolicy{
	{Component: "metadata", Level: "info", RetentionLimit: RetentionLimit{RetentionDays: 1, MaxRows: 100_000, MaxSizeMB: 128}},
	{Component: "scanner", Level: "info", RetentionLimit: RetentionLimit{RetentionDays: 2, MaxRows: 150_000, MaxSizeMB: 192}},
	{Component: "metadata", Level: "warn", RetentionLimit: RetentionLimit{RetentionDays: 7, MaxRows: 250_000, MaxSizeMB: 256}},
	{Component: "scanner", Level: "warn", RetentionLimit: RetentionLimit{RetentionDays: 7, MaxRows: 250_000, MaxSizeMB: 256}},
}

type SettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

type PartitionManager interface {
	EnsureFuturePartitions(ctx context.Context) error
	DropExpiredPartitions(ctx context.Context, cutoff time.Time) ([]string, error)
	DeleteExpiredRowsFromDefault(ctx context.Context, cutoff time.Time) (int64, error)
}

type RetentionLimit struct {
	RetentionDays int   `json:"retention_days,omitempty"`
	MaxRows       int64 `json:"max_rows,omitempty"`
	MaxSizeMB     int64 `json:"max_size_mb,omitempty"`
}

type BucketPolicy struct {
	Component string `json:"component"`
	Level     string `json:"level"`
	RetentionLimit
}

type RetentionPolicy struct {
	Global  RetentionLimit `json:"global"`
	Buckets []BucketPolicy `json:"buckets"`
}

func SeedDefaults(ctx context.Context, store SettingsStore) error {
	bucketDefaults, err := json.Marshal(defaultBucketPolicies)
	if err != nil {
		return fmt.Errorf("marshal opslog bucket defaults: %w", err)
	}

	defaults := map[string]string{
		keyEnabled:         defaultEnabled,
		keyCaptureLevel:    defaultCaptureLevel,
		keyRetentionDays:   defaultRetentionStr,
		keyCleanupInterval: defaultCleanupIntervalStr,
		keyMaxRows:         defaultMaxRowsStr,
		keyMaxSizeMB:       defaultMaxSizeMBStr,
		keyBucketPolicies:  string(bucketDefaults),
	}
	for key, value := range defaults {
		existing, err := store.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("seed opslog defaults for %s: %w", key, err)
		}
		if existing != "" {
			continue
		}
		if err := store.Set(ctx, key, value); err != nil {
			return fmt.Errorf("seed opslog default %s: %w", key, err)
		}
	}
	return nil
}

func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		Global: RetentionLimit{
			RetentionDays: defaultRetention,
			MaxRows:       defaultMaxRows,
			MaxSizeMB:     defaultMaxSizeMB,
		},
		Buckets: append([]BucketPolicy(nil), defaultBucketPolicies...),
	}
}

func LoadRetentionPolicy(ctx context.Context, store SettingsStore) (RetentionPolicy, error) {
	policy := DefaultRetentionPolicy()

	if raw, err := store.Get(ctx, keyRetentionDays); err == nil && raw != "" {
		if days := parseInt(raw); days > 0 {
			policy.Global.RetentionDays = days
		}
	}
	if raw, err := store.Get(ctx, keyMaxRows); err == nil && raw != "" {
		if rows := parseInt64(raw); rows > 0 {
			policy.Global.MaxRows = rows
		}
	}
	if raw, err := store.Get(ctx, keyMaxSizeMB); err == nil && raw != "" {
		if sizeMB := parseInt64(raw); sizeMB > 0 {
			policy.Global.MaxSizeMB = sizeMB
		}
	}

	rawBuckets, err := store.Get(ctx, keyBucketPolicies)
	if err != nil || strings.TrimSpace(rawBuckets) == "" {
		return policy, nil
	}

	var buckets []BucketPolicy
	if err := json.Unmarshal([]byte(rawBuckets), &buckets); err != nil {
		return policy, fmt.Errorf("decode %s: %w", keyBucketPolicies, err)
	}

	normalized := make([]BucketPolicy, 0, len(buckets))
	for _, bucket := range buckets {
		component := strings.TrimSpace(bucket.Component)
		level := strings.ToLower(strings.TrimSpace(bucket.Level))
		if component == "" || level == "" {
			continue
		}
		bucket.Component = component
		bucket.Level = level
		normalized = append(normalized, bucket)
	}
	policy.Buckets = normalized
	return policy, nil
}

func RunCleanup(ctx context.Context, pool *pgxpool.Pool, store SettingsStore, pm PartitionManager) {
	CleanupOnce(ctx, pool, store, pm)

	ticker := time.NewTicker(LoadCleanupInterval(ctx, store))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			CleanupOnce(ctx, pool, store, pm)
		}
	}
}

// LoadCleanupInterval returns the configured operational log cleanup interval.
func LoadCleanupInterval(ctx context.Context, store SettingsStore) time.Duration {
	minutes := defaultCleanupInterval
	if store == nil {
		return time.Duration(minutes) * time.Minute
	}
	if raw, err := store.Get(ctx, keyCleanupInterval); err == nil && raw != "" {
		if parsed := parseInt(raw); parsed > 0 {
			minutes = parsed
		}
	}
	return time.Duration(minutes) * time.Minute
}

// CleanupOnce runs a single operational log retention pass.
func CleanupOnce(ctx context.Context, pool *pgxpool.Pool, store SettingsStore, pm PartitionManager) int64 {
	policy, err := LoadRetentionPolicy(ctx, store)
	if err != nil {
		slog.WarnContext(ctx, "opslog cleanup policy error", "component", "opslog", "error", err)
		policy = DefaultRetentionPolicy()
	}

	if pm != nil {
		if err := pm.EnsureFuturePartitions(ctx); err != nil {
			slog.WarnContext(ctx, "opslog ensure future partitions error", "component", "opslog", "error", err)
		}
	}

	totalDeleted := int64(0)
	for _, bucket := range policy.Buckets {
		totalDeleted += pruneByAge(ctx, pool, bucket.RetentionDays, bucket.Component, bucket.Level)
		totalDeleted += pruneByRowCap(ctx, pool, bucket.MaxRows, bucket.Component, bucket.Level)
		totalDeleted += pruneBySizeCap(ctx, pool, bucket.MaxSizeMB, bucket.Component, bucket.Level)
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -policy.Global.RetentionDays)
	if pm != nil {
		partitionCleanupFailed := false
		if dropped, err := pm.DropExpiredPartitions(ctx, cutoff); err != nil {
			slog.WarnContext(ctx, "opslog partition cleanup error", "component", "opslog", "error", err)
			partitionCleanupFailed = true
		} else if len(dropped) > 0 {
			slog.InfoContext(ctx, "opslog dropped expired partitions", "component", "opslog", "partitions", dropped)
		}
		if deleted, err := pm.DeleteExpiredRowsFromDefault(ctx, cutoff); err != nil {
			slog.WarnContext(ctx, "opslog default partition cleanup error", "component", "opslog", "error", err)
			partitionCleanupFailed = true
		} else {
			totalDeleted += deleted
			if deleted > 0 {
				slog.InfoContext(ctx, "opslog default partition cleanup completed", "component", "opslog", "deleted", deleted)
			}
		}
		if partitionCleanupFailed {
			slog.WarnContext(ctx, "opslog partition cleanup degraded, falling back to row deletes", "component", "opslog", "retention_days", policy.Global.RetentionDays)
			totalDeleted += pruneByAgeBefore(ctx, pool, cutoff, "", "")
		}
	} else {
		totalDeleted += pruneByAgeBefore(ctx, pool, cutoff, "", "")
	}
	totalDeleted += pruneByRowCap(ctx, pool, policy.Global.MaxRows, "", "")
	totalDeleted += pruneBySizeCap(ctx, pool, policy.Global.MaxSizeMB, "", "")

	if totalDeleted > 0 {
		slog.InfoContext(ctx,
			"opslog cleanup completed", "component", "opslog",
			"deleted", totalDeleted,
			"retention_days", policy.Global.RetentionDays,
			"max_rows", policy.Global.MaxRows,
			"max_size_mb", policy.Global.MaxSizeMB,
		)
	}
	return totalDeleted
}

func pruneByAge(ctx context.Context, pool *pgxpool.Pool, days int, component, level string) int64 {
	if days <= 0 {
		return 0
	}

	return pruneByAgeBefore(ctx, pool, time.Now().UTC().AddDate(0, 0, -days), component, level)
}

func pruneByAgeBefore(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time, component, level string) int64 {
	totalDeleted := int64(0)
	for {
		filters, args, nextArg := scopeFilterArgs(component, level)
		query := fmt.Sprintf(`
			DELETE FROM operational_logs
			WHERE id IN (
				SELECT id
				FROM operational_logs
				WHERE timestamp < $%d%s
				LIMIT $%d
			)
		`, nextArg, filters, nextArg+1)
		args = append(args, cutoff, cleanupBatchSize)

		result, err := pool.Exec(ctx, query, args...)
		if err != nil {
			// target_component/target_level describe the retention-policy scope
			// being pruned; the plain "component" key is reserved for routing
			// the log line itself (see Handler.Handle).
			slog.WarnContext(ctx, "opslog age cleanup error", "component", "opslog", "error", err, "target_component", component, "target_level", level)
			return totalDeleted
		}
		deleted := result.RowsAffected()
		totalDeleted += deleted
		if deleted < int64(cleanupBatchSize) {
			return totalDeleted
		}
	}
}

func pruneByRowCap(ctx context.Context, pool *pgxpool.Pool, maxRows int64, component, level string) int64 {
	if maxRows <= 0 {
		return 0
	}

	// Find the timestamp of the row at position maxRows — everything older
	// is beyond the cap. This single OFFSET query is index-backed and much
	// cheaper than using OFFSET inside every DELETE batch.
	filters, args, nextArg := scopeFilterArgs(component, level)
	args = append(args, maxRows)
	var cutoff time.Time
	err := pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT "timestamp" FROM operational_logs
		WHERE 1=1%s
		ORDER BY "timestamp" DESC, id DESC
		OFFSET $%d LIMIT 1`, filters, nextArg), args...,
	).Scan(&cutoff)
	if err != nil {
		// No row at offset = fewer than maxRows rows, nothing to prune.
		return 0
	}

	// Delete everything at or before the cutoff in batches.
	// Note: rows sharing the cutoff timestamp are all deleted, which may
	// remove slightly more than maxRows. This is acceptable for cleanup.
	totalDeleted := int64(0)
	for {
		filters2, args2, nextArg2 := scopeFilterArgs(component, level)
		args2 = append(args2, cutoff, cleanupBatchSize)
		result, err := pool.Exec(ctx, fmt.Sprintf(`
			DELETE FROM operational_logs
			WHERE id IN (
				SELECT id FROM operational_logs
				WHERE "timestamp" <= $%d%s
				LIMIT $%d
			)`, nextArg2, filters2, nextArg2+1), args2...)
		if err != nil {
			slog.WarnContext(ctx, "opslog row-cap cleanup error", "component", "opslog", "error", err, "target_component", component, "target_level", level)
			return totalDeleted
		}
		deleted := result.RowsAffected()
		totalDeleted += deleted
		if deleted < int64(cleanupBatchSize) {
			return totalDeleted
		}
	}
}

func pruneBySizeCap(ctx context.Context, pool *pgxpool.Pool, maxSizeMB int64, component, level string) int64 {
	if maxSizeMB <= 0 {
		return 0
	}
	maxBytes := maxSizeMB * 1024 * 1024

	// Step 1: Count rows in scope.
	filters, args, _ := scopeFilterArgs(component, level)
	var rowCount int64
	err := pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM operational_logs WHERE 1=1%s`, filters),
		args...,
	).Scan(&rowCount)
	if err != nil || rowCount == 0 {
		return 0
	}

	// Step 2: Sample average row size from 100 newest rows in scope.
	// Use ROW(...) with explicit columns to force detoasting, matching how
	// the original window function measured size.
	filters2, args2, _ := scopeFilterArgs(component, level)
	var avgSize int64
	err = pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(AVG(pg_column_size(ROW(
			id, "timestamp", level, component, message,
			request_id, user_id, session_id, playback_session_id,
			client_ip, node_id, attrs
		)))::bigint, 256)
		FROM (
			SELECT * FROM operational_logs
			WHERE 1=1%s
			ORDER BY "timestamp" DESC
			LIMIT 100
		) sub`, filters2), args2...,
	).Scan(&avgSize)
	if err != nil || avgSize <= 0 {
		return 0
	}

	if rowCount*avgSize <= maxBytes {
		return 0
	}

	// Step 3: Find timestamp cutoff — keep (maxBytes / avgSize) newest rows.
	keepRows := maxBytes / avgSize
	if keepRows >= rowCount {
		return 0
	}

	filters3, args3, nextArg := scopeFilterArgs(component, level)
	args3 = append(args3, keepRows)
	var cutoff time.Time
	err = pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT "timestamp" FROM operational_logs
		WHERE 1=1%s
		ORDER BY "timestamp" DESC, id DESC
		OFFSET $%d LIMIT 1`, filters3, nextArg), args3...,
	).Scan(&cutoff)
	if err != nil {
		return 0
	}

	// Step 4: Delete in batches at or below the cutoff.
	// Note: rows sharing the cutoff timestamp are all deleted, which may
	// remove slightly more than the size cap. This is acceptable for cleanup.
	totalDeleted := int64(0)
	for {
		filters4, args4, nextArg4 := scopeFilterArgs(component, level)
		args4 = append(args4, cutoff, cleanupBatchSize)
		result, err := pool.Exec(ctx, fmt.Sprintf(`
			DELETE FROM operational_logs
			WHERE id IN (
				SELECT id FROM operational_logs
				WHERE "timestamp" <= $%d%s
				LIMIT $%d
			)`, nextArg4, filters4, nextArg4+1), args4...)
		if err != nil {
			slog.WarnContext(ctx, "opslog size-cap cleanup error", "component", "opslog", "error", err, "target_component", component, "target_level", level)
			return totalDeleted
		}
		deleted := result.RowsAffected()
		totalDeleted += deleted
		if deleted < int64(cleanupBatchSize) {
			return totalDeleted
		}
	}
}

func scopeFilterArgs(component, level string) (string, []any, int) {
	filters := ""
	args := make([]any, 0, 2)
	nextArg := 1
	if component != "" {
		filters += fmt.Sprintf(" AND component = $%d", nextArg)
		args = append(args, component)
		nextArg++
	}
	if level != "" {
		filters += fmt.Sprintf(" AND level = $%d", nextArg)
		args = append(args, strings.ToLower(level))
		nextArg++
	}
	return filters, args, nextArg
}

func parseInt(s string) int {
	var v int
	fmt.Sscanf(s, "%d", &v)
	return v
}

func parseInt64(s string) int64 {
	var v int64
	fmt.Sscanf(s, "%d", &v)
	return v
}

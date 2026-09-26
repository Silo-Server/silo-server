package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	ObjectPrefix          = diagnosticObjectPrefix + "/"
	DefaultReceivingGrace = time.Hour
)

type CleanupRepository interface {
	DeleteByID(ctx context.Context, id string) (*Report, error)
	MarkFailed(ctx context.Context, id string) error
	RetentionCandidates(ctx context.Context, olderThan time.Time, perUserByteCap int64) ([]Report, error)
	StaleReceiving(ctx context.Context, grace time.Duration) ([]Report, error)
	LiveBlobKeys(ctx context.Context, keys []string) (map[string]ReportState, error)
}

type CleanupOptions struct {
	Now                 func() time.Time
	StaleReceivingGrace time.Duration
	Logger              *slog.Logger
}

type CleanupResult struct {
	RetentionReportsDeleted int `json:"retention_reports_deleted"`
	StaleReportsDeleted     int `json:"stale_reports_deleted"`
	OrphanObjectsDeleted    int `json:"orphan_objects_deleted"`
}

func (r CleanupResult) ReportsDeleted() int {
	return r.RetentionReportsDeleted + r.StaleReportsDeleted
}

func CleanupOnce(
	ctx context.Context,
	repo CleanupRepository,
	settingsStore SettingsStore,
	store ObjectStore,
	logger *slog.Logger,
) (CleanupResult, error) {
	settings, err := LoadSettings(ctx, settingsStore)
	if err != nil {
		return CleanupResult{}, fmt.Errorf("load diagnostics cleanup settings: %w", err)
	}
	return CleanupReports(ctx, repo, store, settings, CleanupOptions{Logger: logger})
}

func CleanupReports(
	ctx context.Context,
	repo CleanupRepository,
	store ObjectStore,
	settings Settings,
	opts CleanupOptions,
) (CleanupResult, error) {
	if repo == nil {
		return CleanupResult{}, nil
	}
	now := func() time.Time { return time.Now().UTC() }
	if opts.Now != nil {
		now = opts.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	grace := opts.StaleReceivingGrace
	if grace <= 0 {
		grace = DefaultReceivingGrace
	}

	cutoff := time.Time{}
	if settings.RetentionDays > 0 {
		cutoff = now().Add(-time.Duration(settings.RetentionDays) * 24 * time.Hour)
	}

	// A single poisoned report (e.g. a permission error on one S3 object) must
	// not abort the whole run, or every scheduled pass re-hits it first and
	// blocks retention, stale reconciliation, and orphan cleanup indefinitely.
	// Log-and-continue per report; aggregate per-item failures and still surface
	// genuine query/iteration errors.
	var result CleanupResult
	var errs []error

	candidates, err := repo.RetentionCandidates(ctx, cutoff, settings.MaxBytesPerUser)
	if err != nil {
		errs = append(errs, fmt.Errorf("load diagnostic retention candidates: %w", err))
		return result, errors.Join(errs...)
	}
	for _, report := range candidates {
		// Row first: a DB failure after the object is gone would leave a visible
		// ready report whose bundle 404s on download. If the blob delete then
		// fails the row is already gone, so log it for the orphan pass below to
		// reap rather than aborting the run (see the admin DeleteReport path).
		if _, err := repo.DeleteByID(ctx, report.ID); err != nil && !IsReportNotFound(err) {
			errs = append(errs, fmt.Errorf("delete diagnostic retention row %s: %w", report.ID, err))
			continue
		}
		if err := deleteReportObjects(ctx, store, &report, logger); err != nil {
			logDeferredBlobDeletion(ctx, logger, store, &report, err)
		}
		result.RetentionReportsDeleted++
	}

	stale, err := repo.StaleReceiving(ctx, grace)
	if err != nil {
		errs = append(errs, fmt.Errorf("load stale diagnostic reports: %w", err))
		return result, errors.Join(errs...)
	}
	for _, report := range stale {
		// Same rows-first ordering as retention: mark/delete the row before the
		// blob so a mid-cleanup DB failure never leaves a row pointing at a
		// missing object, and let orphan cleanup reap a blob that fails to delete.
		if report.State == StateReceiving {
			if err := repo.MarkFailed(ctx, report.ID); err != nil && !IsReportNotFound(err) {
				errs = append(errs, fmt.Errorf("mark stale diagnostic report failed %s: %w", report.ID, err))
				continue
			}
		}
		if _, err := repo.DeleteByID(ctx, report.ID); err != nil && !IsReportNotFound(err) {
			errs = append(errs, fmt.Errorf("delete stale diagnostic row %s: %w", report.ID, err))
			continue
		}
		if err := deleteReportObjects(ctx, store, &report, logger); err != nil {
			logDeferredBlobDeletion(ctx, logger, store, &report, err)
		}
		result.StaleReportsDeleted++
	}

	if store == nil || strings.TrimSpace(store.Bucket()) == "" {
		return result, errors.Join(errs...)
	}
	keys, err := store.ListObjects(ctx, ObjectPrefix)
	if err != nil {
		errs = append(errs, fmt.Errorf("list diagnostic objects: %w", err))
		return result, errors.Join(errs...)
	}
	keys = slices.DeleteFunc(keys, func(key string) bool { return !isReportObjectKey(key) })
	if len(keys) == 0 {
		return result, errors.Join(errs...)
	}
	liveKeys, err := repo.LiveBlobKeys(ctx, keys)
	if err != nil {
		errs = append(errs, fmt.Errorf("load live diagnostic blob keys: %w", err))
		return result, errors.Join(errs...)
	}
	for _, key := range keys {
		if _, ok := liveKeys[key]; ok {
			continue
		}
		if err := deleteObjectIfPresent(ctx, store, store.Bucket(), key); err != nil {
			errs = append(errs, fmt.Errorf("delete orphan diagnostic object %s: %w", key, err))
			continue
		}
		logger.InfoContext(ctx, "diagnostic orphan object deleted",
			"component", "diagnostics",
			"key", key,
		)
		result.OrphanObjectsDeleted++
	}
	return result, errors.Join(errs...)
}

// isReportObjectKey reports whether key has the exact shape reportObjectKey
// writes. On a local backend with no private bucket, diagnostics shares one
// root with artwork, whose keys begin with an unvalidated provider slug. Orphan
// cleanup deletes whatever it lists, so it must only consider keys that could
// be a bundle, never everything under the prefix.
func isReportObjectKey(key string) bool {
	parts := strings.Split(key, "/")
	if len(parts) != 3 || parts[0] != diagnosticObjectPrefix || !strings.HasSuffix(parts[2], ".tar.gz") || parts[2] == ".tar.gz" {
		return false
	}
	userID, err := strconv.Atoi(parts[1])
	return err == nil && userID > 0 && strconv.Itoa(userID) == parts[1]
}

func deleteReportObjects(ctx context.Context, store ObjectStore, report *Report, logger *slog.Logger) error {
	if report == nil {
		return nil
	}
	keys := reportObjectKeys(*report)
	if store == nil || strings.TrimSpace(store.Bucket()) == "" {
		if len(keys) == 0 {
			return nil
		}
		logSkippedObjectDeletion(ctx, logger, report.ID, keys)
		return nil
	}
	bucket := stringValue(report.BlobBucket)
	if bucket == "" {
		bucket = store.Bucket()
	}
	for _, key := range keys {
		if err := deleteObjectIfPresent(ctx, store, bucket, key); err != nil {
			return err
		}
	}
	return nil
}

func logSkippedObjectDeletion(ctx context.Context, logger *slog.Logger, reportID string, keys []string) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.WarnContext(ctx, "diagnostic report blob deletion skipped because storage is unavailable",
		"component", "diagnostics",
		"report_id", reportID,
		"keys", keys,
	)
}

// logDeferredBlobDeletion records a report blob that could not be deleted after
// its DB row was already removed. The row is the source of truth, so the object
// is now an orphan for the orphan-cleanup pass (this run or a later one) to
// reap; we log the bucket and keys rather than fail so one unreachable object
// can't block row deletion. Shared by the retention/stale loops and the admin
// DeleteReport path.
func logDeferredBlobDeletion(ctx context.Context, logger *slog.Logger, store ObjectStore, report *Report, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.ErrorContext(ctx, "diagnostic report blob deletion failed after row deletion",
		"component", "diagnostics",
		"report_id", report.ID,
		"bucket", reportBlobBucket(report, store),
		"keys", reportObjectKeys(*report),
		"error", err,
	)
}

func deleteObjectIfPresent(ctx context.Context, store ObjectStore, bucket, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	if err := store.DeleteObject(ctx, bucket, key); err != nil && !IsObjectNotFound(err) {
		return err
	}
	return nil
}

func reportObjectKeys(report Report) []string {
	keys := make([]string, 0, 2)
	if key := stringValue(report.BlobKey); key != "" {
		keys = append(keys, key)
	}
	if report.ID != "" && report.UserID > 0 {
		deterministic := reportObjectKey(report.UserID, report.ID)
		seen := false
		for _, key := range keys {
			if key == deterministic {
				seen = true
				break
			}
		}
		if !seen {
			keys = append(keys, deterministic)
		}
	}
	return keys
}

func IsReportNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

const ReportDownloadContentType = BundleContentType

var ErrReportNotReady = errors.New("diagnostic report is not ready")

func (s *Service) ListForAdmin(ctx context.Context, filters ListFilters) (ListResult, error) {
	if s.reports == nil {
		return ListResult{}, ErrReportStoreUnavailable
	}
	return s.reports.ListForAdmin(ctx, filters)
}

// reportIDMisses reports whether a caller-supplied report id can be rejected
// without a query. client_diagnostic_reports.id is a `uuid` column, so a
// malformed id can only ever miss; handing it to Postgres raises SQLSTATE 22P02
// and would surface to the client as a 500 instead of a 404.
func reportIDMisses(id string) bool {
	// The exact value is bound to the uuid column, so padding counts as a miss.
	return uuid.Validate(id) != nil
}

func (s *Service) GetReport(ctx context.Context, id string) (*Report, error) {
	if s.reports == nil {
		return nil, ErrReportStoreUnavailable
	}
	if reportIDMisses(id) {
		return nil, ErrNotFound
	}
	return s.reports.GetByID(ctx, id)
}

func (s *Service) PresignReportDownload(ctx context.Context, report *Report, expiry time.Duration) (string, error) {
	bucket, key, err := s.readyReportBlobLocation(report)
	if err != nil {
		return "", err
	}
	if expiry <= 0 {
		expiry = 15 * time.Minute
	}
	return s.store.PresignGetURL(ctx, bucket, key, expiry)
}

func (s *Service) EffectiveReportDownloadTTL(requested time.Duration) time.Duration {
	if requested <= 0 {
		return requested
	}
	effectiveStore, ok := s.store.(interface {
		EffectivePresignTTL(time.Duration) time.Duration
	})
	if !ok {
		return requested
	}
	effective := effectiveStore.EffectivePresignTTL(requested)
	if effective <= 0 {
		return requested
	}
	return effective
}

func (s *Service) OpenReportDownload(ctx context.Context, report *Report) (io.ReadCloser, error) {
	bucket, key, err := s.readyReportBlobLocation(report)
	if err != nil {
		return nil, err
	}
	return s.store.GetObject(ctx, bucket, key)
}

func (s *Service) DeleteReport(ctx context.Context, id string) (*Report, error) {
	if s.reports == nil {
		return nil, ErrReportStoreUnavailable
	}
	// Delete the row before the blob: a DB failure after the object is gone
	// would otherwise leave a visible report whose bundle can no longer be
	// downloaded. DeleteByID returns the deleted row (and ErrNotFound when
	// absent), so the blob location is captured from it. If the blob delete
	// fails the row is already gone, so log the bucket/key for an operator (and
	// the orphan reconciler) to reap instead of failing the delete.
	if reportIDMisses(id) {
		return nil, ErrNotFound
	}
	deleted, err := s.reports.DeleteByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := deleteReportObjects(ctx, s.store, deleted, s.logger); err != nil {
		logDeferredBlobDeletion(ctx, s.logger, s.store, deleted, err)
	}
	return deleted, nil
}

func reportBlobBucket(report *Report, store ObjectStore) string {
	bucket := stringValue(report.BlobBucket)
	if bucket == "" && store != nil {
		bucket = store.Bucket()
	}
	return bucket
}

func (s *Service) readyReportBlobLocation(report *Report) (string, string, error) {
	if report == nil {
		return "", "", ErrNotFound
	}
	if report.State != StateReady {
		return "", "", ErrReportNotReady
	}
	if s.store == nil || strings.TrimSpace(s.store.Bucket()) == "" {
		return "", "", ErrStorageUnavailable
	}

	bucket := stringValue(report.BlobBucket)
	if bucket == "" {
		bucket = s.store.Bucket()
	}
	key := stringValue(report.BlobKey)
	if key == "" {
		return "", "", fmt.Errorf("%w: diagnostic report has no blob key", ErrStorageUnavailable)
	}
	return bucket, key, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

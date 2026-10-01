package diagnostics

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestCleanupReportsDeletesRowBeforeBlobAndToleratesMissingObject(t *testing.T) {
	ops := []string{}
	repo := &fakeCleanupRepo{
		retention: []Report{
			testCleanupReport("r1", 7, StateReady, "diagnostics/7/r1.tar.gz"),
			testCleanupReport("r2", 7, StateReady, "diagnostics/7/r2.tar.gz"),
		},
		ops: &ops,
	}
	store := &fakeCleanupStore{
		bucket:      "private",
		missingKeys: map[string]bool{"diagnostics/7/r1.tar.gz": true},
		ops:         &ops,
	}

	result, err := CleanupReports(context.Background(), repo, store, Settings{
		RetentionDays:   30,
		MaxBytesPerUser: DefaultMaxBytesPerUser,
	}, CleanupOptions{
		Now:    func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("CleanupReports: %v", err)
	}
	if result.RetentionReportsDeleted != 2 {
		t.Fatalf("RetentionReportsDeleted = %d, want 2", result.RetentionReportsDeleted)
	}
	wantOps := []string{
		"delete-row:r1",
		"delete-object:diagnostics/7/r1.tar.gz",
		"delete-row:r2",
		"delete-object:diagnostics/7/r2.tar.gz",
	}
	assertStrings(t, ops, wantOps)
}

func TestCleanupReportsTreatsBlobFailureAsNonFatal(t *testing.T) {
	ops := []string{}
	repo := &fakeCleanupRepo{
		retention: []Report{
			testCleanupReport("r1", 7, StateReady, "diagnostics/7/r1.tar.gz"),
			testCleanupReport("r2", 7, StateReady, "diagnostics/7/r2.tar.gz"),
		},
		ops: &ops,
	}
	store := &fakeCleanupStore{
		bucket:     "private",
		deleteErrs: map[string]error{"diagnostics/7/r1.tar.gz": errors.New("s3 access denied")},
		ops:        &ops,
	}

	result, err := CleanupReports(context.Background(), repo, store, Settings{
		RetentionDays:   30,
		MaxBytesPerUser: DefaultMaxBytesPerUser,
	}, CleanupOptions{
		Now:    func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	// r1's row is deleted first, so its blob delete failing does not abort the
	// run or roll back the row: it is logged for orphan cleanup to reap, both
	// rows count as deleted, and no error is surfaced.
	if err != nil {
		t.Fatalf("CleanupReports: %v", err)
	}
	if result.RetentionReportsDeleted != 2 {
		t.Fatalf("RetentionReportsDeleted = %d, want 2", result.RetentionReportsDeleted)
	}
	assertStrings(t, ops, []string{
		"delete-row:r1",
		"delete-object:diagnostics/7/r1.tar.gz",
		"delete-row:r2",
		"delete-object:diagnostics/7/r2.tar.gz",
	})
}

func TestCleanupReportsCleansStaleReceiving(t *testing.T) {
	ops := []string{}
	repo := &fakeCleanupRepo{
		stale: []Report{
			testCleanupReport("r1", 7, StateReceiving, ""),
		},
		ops: &ops,
	}
	store := &fakeCleanupStore{bucket: "private", ops: &ops}

	result, err := CleanupReports(context.Background(), repo, store, Settings{
		RetentionDays:   30,
		MaxBytesPerUser: DefaultMaxBytesPerUser,
	}, CleanupOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("CleanupReports: %v", err)
	}
	if result.StaleReportsDeleted != 1 {
		t.Fatalf("StaleReportsDeleted = %d, want 1", result.StaleReportsDeleted)
	}
	assertStrings(t, ops, []string{
		"mark-failed:r1",
		"delete-row:r1",
		"delete-object:diagnostics/7/r1.tar.gz",
	})
}

func TestCleanupReportsDeletesRowsWhenStorageUnavailable(t *testing.T) {
	ops := []string{}
	repo := &fakeCleanupRepo{
		retention: []Report{
			testCleanupReport("r1", 7, StateReady, "diagnostics/7/r1.tar.gz"),
		},
		stale: []Report{
			testCleanupReport("r2", 7, StateReceiving, "diagnostics/7/r2.tar.gz"),
		},
		ops: &ops,
	}

	result, err := CleanupReports(context.Background(), repo, nil, Settings{
		RetentionDays:   30,
		MaxBytesPerUser: DefaultMaxBytesPerUser,
	}, CleanupOptions{
		Now:    func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("CleanupReports: %v", err)
	}
	if result.RetentionReportsDeleted != 1 || result.StaleReportsDeleted != 1 {
		t.Fatalf("result = %#v, want one retention and one stale deletion", result)
	}
	assertStrings(t, ops, []string{
		"delete-row:r1",
		"mark-failed:r2",
		"delete-row:r2",
	})
}

func TestCleanupReportsDeletesUnmatchedObjects(t *testing.T) {
	ops := []string{}
	repo := &fakeCleanupRepo{
		live: map[string]ReportState{
			"diagnostics/7/ready.tar.gz":     StateReady,
			"diagnostics/7/receiving.tar.gz": StateReceiving,
		},
		ops: &ops,
	}
	store := &fakeCleanupStore{
		bucket: "private",
		list: []string{
			"diagnostics/7/ready.tar.gz",
			"diagnostics/7/receiving.tar.gz",
			"diagnostics/7/orphan.tar.gz",
			"diagnostics/7/failed.tar.gz",
		},
		ops: &ops,
	}

	result, err := CleanupReports(context.Background(), repo, store, Settings{
		RetentionDays:   30,
		MaxBytesPerUser: DefaultMaxBytesPerUser,
	}, CleanupOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("CleanupReports: %v", err)
	}
	if result.OrphanObjectsDeleted != 2 {
		t.Fatalf("OrphanObjectsDeleted = %d, want 2", result.OrphanObjectsDeleted)
	}
	wantDeleted := []string{"diagnostics/7/orphan.tar.gz", "diagnostics/7/failed.tar.gz"}
	if !sameStrings(store.deleted, wantDeleted) {
		t.Fatalf("deleted objects = %v, want %v", store.deleted, wantDeleted)
	}
}

// On a local backend with no private bucket, diagnostics shares a root with
// artwork, whose first key segment is an unvalidated provider slug. A metadata
// plugin named "diagnostics" must not have its artwork swept as orphans.
func TestCleanupReportsOnlyDeletesBundleShapedOrphans(t *testing.T) {
	ops := []string{}
	repo := &fakeCleanupRepo{live: map[string]ReportState{}, ops: &ops}
	store := &fakeCleanupStore{
		bucket: "local",
		list: []string{
			"diagnostics/movie/42/poster/original.r1.jpg",
			"diagnostics/7/notes.txt",
			"diagnostics/seven/orphan.tar.gz",
			"diagnostics/07/orphan.tar.gz",
			"diagnostics/7/.tar.gz",
			"diagnostics/7/orphan.tar.gz",
		},
		ops: &ops,
	}

	result, err := CleanupReports(context.Background(), repo, store, Settings{
		RetentionDays:   30,
		MaxBytesPerUser: DefaultMaxBytesPerUser,
	}, CleanupOptions{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("CleanupReports: %v", err)
	}
	if want := []string{"diagnostics/7/orphan.tar.gz"}; !sameStrings(store.deleted, want) || result.OrphanObjectsDeleted != 1 {
		t.Fatalf("deleted objects = %v (count %d), want %v", store.deleted, result.OrphanObjectsDeleted, want)
	}
}

func testCleanupReport(id string, userID int, state ReportState, blobKey string) Report {
	report := Report{ID: id, UserID: userID, State: state}
	if blobKey != "" {
		report.BlobKey = &blobKey
	}
	return report
}

type fakeCleanupRepo struct {
	retention []Report
	stale     []Report
	live      map[string]ReportState
	ops       *[]string
}

func (f *fakeCleanupRepo) DeleteByID(_ context.Context, id string) (*Report, error) {
	f.record("delete-row:" + id)
	return &Report{ID: id}, nil
}

func (f *fakeCleanupRepo) MarkFailed(_ context.Context, id string) error {
	f.record("mark-failed:" + id)
	return nil
}

func (f *fakeCleanupRepo) RetentionCandidates(context.Context, time.Time, int64) ([]Report, error) {
	return append([]Report(nil), f.retention...), nil
}

func (f *fakeCleanupRepo) StaleReceiving(context.Context, time.Duration) ([]Report, error) {
	return append([]Report(nil), f.stale...), nil
}

func (f *fakeCleanupRepo) LiveBlobKeys(_ context.Context, keys []string) (map[string]ReportState, error) {
	live := make(map[string]ReportState)
	for _, key := range keys {
		if state, ok := f.live[key]; ok {
			live[key] = state
		}
	}
	return live, nil
}

func (f *fakeCleanupRepo) record(op string) {
	if f.ops != nil {
		*f.ops = append(*f.ops, op)
	}
}

type fakeCleanupStore struct {
	bucket      string
	list        []string
	missingKeys map[string]bool
	deleteErrs  map[string]error
	deleted     []string
	ops         *[]string
}

func (f *fakeCleanupStore) PutStream(context.Context, string, string, io.Reader, string) error {
	return nil
}

func (f *fakeCleanupStore) GetObject(context.Context, string, string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeCleanupStore) DeleteObject(_ context.Context, _ string, key string) error {
	f.record("delete-object:" + key)
	f.deleted = append(f.deleted, key)
	if f.missingKeys[key] {
		return ErrObjectNotFound
	}
	if err := f.deleteErrs[key]; err != nil {
		return err
	}
	return nil
}

func (f *fakeCleanupStore) ListObjects(context.Context, string) ([]string, error) {
	return append([]string(nil), f.list...), nil
}

func (f *fakeCleanupStore) PresignGetURL(context.Context, string, string, time.Duration) (string, error) {
	return "", errors.New("not implemented")
}

func (f *fakeCleanupStore) Bucket() string {
	return f.bucket
}

func (f *fakeCleanupStore) record(op string) {
	if f.ops != nil {
		*f.ops = append(*f.ops, op)
	}
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if !sameStrings(got, want) {
		t.Fatalf("ops = %v, want %v", got, want)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

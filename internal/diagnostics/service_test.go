package diagnostics

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/diagnostics/contract"
)

func TestServiceIngestStoresReadyReport(t *testing.T) {
	bundle, manifest, info := testDiagnosticsUpload(t, "server-1", DefaultConsentNoticeVer, "prof_1")
	repo := &fakeDiagnosticReportStore{}
	store := &fakeDiagnosticObjectStore{bucket: "private"}
	svc := newTestDiagnosticsService(repo, store)

	result, err := svc.Ingest(context.Background(), 42, nil, manifest, bytes.NewReader(bundle))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if result.ReportID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("ReportID = %q, want reserved report ID", result.ReportID)
	}
	if result.ShortID != "SILO-ABCDEF123456" {
		t.Fatalf("ShortID = %q, want SILO-ABCDEF123456", result.ShortID)
	}
	if len(repo.ready) != 1 || repo.ready[0] != result.ReportID {
		t.Fatalf("ready reports = %v, want [%s]", repo.ready, result.ReportID)
	}
	if len(repo.readyBlobs) != 1 {
		t.Fatalf("ready blobs = %v, want one", repo.readyBlobs)
	}
	wantKey := "diagnostics/42/11111111-1111-1111-1111-111111111111.tar.gz"
	readyBlob := repo.readyBlobs[0]
	if readyBlob.Bucket != "private" || readyBlob.Key != wantKey {
		t.Fatalf("ready blob location = %s/%s, want private/%s", readyBlob.Bucket, readyBlob.Key, wantKey)
	}
	if readyBlob.SHA256 != info.SHA256 {
		t.Fatalf("ready blob sha256 = %q, want %q", readyBlob.SHA256, info.SHA256)
	}
	if len(store.puts) != 1 || store.puts[0] != wantKey {
		t.Fatalf("stored keys = %v, want [%s]", store.puts, wantKey)
	}
	if !bytes.Equal(store.data, bundle) {
		t.Fatal("stored bundle does not match uploaded bundle")
	}
	if len(repo.failed) != 0 || len(store.deleted) != 0 {
		t.Fatalf("unexpected compensation: failed=%v deleted=%v", repo.failed, store.deleted)
	}
}

func TestServiceIngestRejectsDestinationMismatch(t *testing.T) {
	bundle, info := testDiagnosticsBundle(t)
	repo := &fakeDiagnosticReportStore{}
	store := &fakeDiagnosticObjectStore{bucket: "private"}
	svc := newTestDiagnosticsService(repo, store)

	manifest := testManifestJSON(t, "other-server", DefaultConsentNoticeVer, info)
	_, err := svc.Ingest(context.Background(), 42, nil, manifest, bytes.NewReader(bundle))
	if !errors.Is(err, ErrDestinationMismatch) {
		t.Fatalf("Ingest error = %v, want ErrDestinationMismatch", err)
	}
	if repo.insertCalls != 0 {
		t.Fatalf("InsertReceiving calls = %d, want 0", repo.insertCalls)
	}
	if len(store.puts) != 0 {
		t.Fatalf("PutStream calls = %d, want 0", len(store.puts))
	}
}

func TestServiceIngestRejectsStaleConsent(t *testing.T) {
	bundle, info := testDiagnosticsBundle(t)
	repo := &fakeDiagnosticReportStore{}
	store := &fakeDiagnosticObjectStore{bucket: "private"}
	svc := newTestDiagnosticsService(repo, store)

	manifest := testManifestJSON(t, "server-1", DefaultConsentNoticeVer+1, info)
	_, err := svc.Ingest(context.Background(), 42, nil, manifest, bytes.NewReader(bundle))
	if !errors.Is(err, ErrStaleConsent) {
		t.Fatalf("Ingest error = %v, want ErrStaleConsent", err)
	}
	if repo.insertCalls != 0 {
		t.Fatalf("InsertReceiving calls = %d, want 0", repo.insertCalls)
	}
	if len(store.puts) != 0 {
		t.Fatalf("PutStream calls = %d, want 0", len(store.puts))
	}
}

func TestServiceIngestArchiveMismatchCompensates(t *testing.T) {
	bundle, info := testDiagnosticsBundle(t)
	info.SHA256 = strings.Repeat("0", 64)
	repo := &fakeDiagnosticReportStore{}
	store := &fakeDiagnosticObjectStore{bucket: "private"}
	svc := newTestDiagnosticsService(repo, store)

	manifest := testManifestJSON(t, "server-1", DefaultConsentNoticeVer, info)
	_, err := svc.Ingest(context.Background(), 42, nil, manifest, bytes.NewReader(bundle))
	if !errors.Is(err, ErrArchiveMismatch) {
		t.Fatalf("Ingest error = %v, want ErrArchiveMismatch", err)
	}
	if repo.insertCalls != 1 {
		t.Fatalf("InsertReceiving calls = %d, want 1", repo.insertCalls)
	}
	if len(store.puts) != 1 {
		t.Fatalf("PutStream calls = %d, want 1", len(store.puts))
	}
	wantKey := "diagnostics/42/11111111-1111-1111-1111-111111111111.tar.gz"
	if len(store.deleted) != 1 || store.deleted[0] != wantKey {
		t.Fatalf("deleted keys = %v, want [%s]", store.deleted, wantKey)
	}
	if len(repo.failed) != 1 || repo.failed[0] != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("failed reports = %v, want reserved report id", repo.failed)
	}
	if len(repo.ready) != 0 {
		t.Fatalf("ready reports = %v, want none", repo.ready)
	}
}

func TestServiceIngestRejectsEmbeddedManifestMismatch(t *testing.T) {
	// The tar embeds a manifest.json that disagrees with the part-1 manifest
	// (different os_version) while the archive size/sha fields still match the
	// stored bundle, so this is caught only by the embedded-manifest check.
	embedded := diagnosticsManifestBody("server-1", DefaultConsentNoticeVer, "prof_1", "15", nil)
	bundle := buildManifestBundle(t, embedded)
	info, err := ValidateBundle(bytes.NewReader(bundle), BundleLimits{})
	if err != nil {
		t.Fatalf("ValidateBundle: %v", err)
	}
	manifest := diagnosticsManifestBody("server-1", DefaultConsentNoticeVer, "prof_1", "99", &info)
	if _, err := contract.ValidateManifest(manifest); err != nil {
		t.Fatalf("test manifest invalid: %v\n%s", err, manifest)
	}

	repo := &fakeDiagnosticReportStore{}
	store := &fakeDiagnosticObjectStore{bucket: "private"}
	svc := newTestDiagnosticsService(repo, store)

	_, err = svc.Ingest(context.Background(), 42, nil, manifest, bytes.NewReader(bundle))
	if !errors.Is(err, ErrArchiveMismatch) {
		t.Fatalf("Ingest error = %v, want ErrArchiveMismatch", err)
	}
	wantReportID := "11111111-1111-1111-1111-111111111111"
	if len(repo.failed) != 1 || repo.failed[0] != wantReportID {
		t.Fatalf("failed reports = %v, want [%s]", repo.failed, wantReportID)
	}
	if len(store.deleted) != 1 {
		t.Fatalf("deleted keys = %v, want compensation delete", store.deleted)
	}
	if len(repo.ready) != 0 {
		t.Fatalf("ready reports = %v, want none", repo.ready)
	}
}

func TestEmbeddedManifestMatchesPreservesLargeIntegers(t *testing.T) {
	// The received (part-1) and embedded manifests differ only in
	// log_summary.lines by 1, at a magnitude above 2^53 where float64 cannot
	// represent adjacent integers. Decoding with UseNumber must keep them distinct
	// so a bundle whose embedded manifest was tampered is rejected, not accepted.
	received := []byte(`{"log_summary":{"lines":9007199254740993},"archive":{"bytes":1}}`)
	embedded := []byte(`{"log_summary":{"lines":9007199254740992}}`)
	if embeddedManifestMatches(received, embedded) {
		t.Fatal("embeddedManifestMatches = true for manifests differing by a large integer, want false")
	}

	// Sanity: with the archive object stripped from the received manifest, an
	// otherwise identical large integer still compares equal.
	sameReceived := []byte(`{"log_summary":{"lines":9007199254740993},"archive":{"bytes":1}}`)
	sameEmbedded := []byte(`{"log_summary":{"lines":9007199254740993}}`)
	if !embeddedManifestMatches(sameReceived, sameEmbedded) {
		t.Fatal("embeddedManifestMatches = false for identical manifests, want true")
	}
}

func TestEmbeddedManifestMatchesRejectsTrailingData(t *testing.T) {
	// A stray closing delimiter after an otherwise-matching object must be
	// rejected. Decoder.More() returns false here (it only tracks array/object
	// iteration), so the decode-to-EOF check is what catches it; without it a
	// bundle whose embedded manifest is not a single clean JSON object would
	// still compare equal to the accepted part-1 manifest.
	received := []byte(`{"log_summary":{"lines":1},"archive":{"bytes":1}}`)
	for _, trailer := range []string{"}", "]", "garbage", `{"extra":1}`} {
		embedded := []byte(`{"log_summary":{"lines":1}}` + trailer)
		if embeddedManifestMatches(received, embedded) {
			t.Fatalf("embeddedManifestMatches = true for embedded manifest with trailing %q, want false", trailer)
		}
	}

	// Trailing whitespace after the object is not trailing data; it must still
	// compare equal.
	embeddedWithWhitespace := []byte("{\"log_summary\":{\"lines\":1}}\n  ")
	if !embeddedManifestMatches(received, embeddedWithWhitespace) {
		t.Fatal("embeddedManifestMatches = false for embedded manifest with trailing whitespace, want true")
	}
}

func TestServiceIngestReturnsStorageErrorForMidStreamPutFailure(t *testing.T) {
	bundle, info := testDiagnosticsBundle(t)
	putErr := errors.New("s3: connection reset by peer")
	repo := &fakeDiagnosticReportStore{}
	store := &fakeDiagnosticObjectStore{
		bucket:       "private",
		putErr:       putErr,
		putReadBytes: 8,
	}
	svc := newTestDiagnosticsService(repo, store)

	manifest := testManifestJSON(t, "server-1", DefaultConsentNoticeVer, info)
	_, err := svc.Ingest(context.Background(), 42, nil, manifest, bytes.NewReader(bundle))
	if !errors.Is(err, ErrStorageUnavailable) || !errors.Is(err, putErr) {
		t.Fatalf("Ingest error = %v, want retryable storage error wrapping put failure", err)
	}
	if errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("Ingest error = %v, must not be classified as ErrInvalidBundle", err)
	}
	wantReportID := "11111111-1111-1111-1111-111111111111"
	if len(repo.failed) != 1 || repo.failed[0] != wantReportID {
		t.Fatalf("failed reports = %v, want [%s]", repo.failed, wantReportID)
	}
	wantKey := "diagnostics/42/" + wantReportID + ".tar.gz"
	if len(store.deleted) != 1 || store.deleted[0] != wantKey {
		t.Fatalf("deleted keys = %v, want [%s]", store.deleted, wantKey)
	}
	if len(repo.ready) != 0 {
		t.Fatalf("ready reports = %v, want none", repo.ready)
	}
}

func TestServiceIngestCompensatesWithDetachedContext(t *testing.T) {
	bundle, info := testDiagnosticsBundle(t)
	repo := &fakeDiagnosticReportStore{}
	store := &fakeDiagnosticObjectStore{
		bucket:       "private",
		putErr:       errors.New("s3: connection reset by peer"),
		putReadBytes: 8,
	}
	svc := newTestDiagnosticsService(repo, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manifest := testManifestJSON(t, "server-1", DefaultConsentNoticeVer, info)
	_, err := svc.Ingest(ctx, 42, nil, manifest, bytes.NewReader(bundle))
	if err == nil {
		t.Fatal("Ingest error = nil, want storage error")
	}
	if len(store.deleteCtxErrs) != 1 || store.deleteCtxErrs[0] != nil {
		t.Fatalf("delete ctx errors = %v, want [nil]", store.deleteCtxErrs)
	}
	if len(repo.markFailedCtxErrs) != 1 || repo.markFailedCtxErrs[0] != nil {
		t.Fatalf("mark failed ctx errors = %v, want [nil]", repo.markFailedCtxErrs)
	}
}

func TestServiceIngestStoresOnlyValidatedProfileAttribution(t *testing.T) {
	// Header-only attribution (manifest carries no profile_id): the header is
	// the single source, so it is validated and stored.
	bundle, manifest, _ := testDiagnosticsUpload(t, "server-1", DefaultConsentNoticeVer, "")
	repo := &fakeDiagnosticReportStore{}
	store := &fakeDiagnosticObjectStore{bucket: "private"}
	svc := newTestDiagnosticsService(repo, store)
	svc.SetProfileAttributionValidator(ProfileAttributionValidatorFunc(func(_ context.Context, userID int, profileID string) (bool, error) {
		return userID == 42 && profileID == "prof_header", nil
	}))

	headerProfileID := "prof_header"
	if _, err := svc.Ingest(context.Background(), 42, &headerProfileID, manifest, bytes.NewReader(bundle)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if repo.insertInput.ProfileID == nil || *repo.insertInput.ProfileID != "prof_header" {
		t.Fatalf("ProfileID = %v, want prof_header", repo.insertInput.ProfileID)
	}

	// Manifest-only attribution that fails validation is dropped, not stored.
	bundle, manifest, _ = testDiagnosticsUpload(t, "server-1", DefaultConsentNoticeVer, "prof_1")
	repo = &fakeDiagnosticReportStore{}
	store = &fakeDiagnosticObjectStore{bucket: "private"}
	svc = newTestDiagnosticsService(repo, store)
	svc.SetProfileAttributionValidator(ProfileAttributionValidatorFunc(func(context.Context, int, string) (bool, error) {
		return false, nil
	}))
	if _, err := svc.Ingest(context.Background(), 42, nil, manifest, bytes.NewReader(bundle)); err != nil {
		t.Fatalf("Ingest with unvalidated profile: %v", err)
	}
	if repo.insertInput.ProfileID != nil {
		t.Fatalf("ProfileID = %v, want nil for unvalidated attribution", *repo.insertInput.ProfileID)
	}
}

func TestServiceIngestRejectsMismatchedProfileID(t *testing.T) {
	// The manifest was captured under prof_manifest but the request carries a
	// different X-Profile-Id (prof_header) because the client switched profiles
	// between capture and upload. The two sources must not be silently
	// reconciled; the upload is rejected and no report row is created.
	bundle, manifest, _ := testDiagnosticsUpload(t, "server-1", DefaultConsentNoticeVer, "prof_manifest")
	repo := &fakeDiagnosticReportStore{}
	store := &fakeDiagnosticObjectStore{bucket: "private"}
	svc := newTestDiagnosticsService(repo, store)
	// Even a validator that would accept any profile must not paper over the
	// mismatch: resolution fails before attribution validation runs.
	svc.SetProfileAttributionValidator(ProfileAttributionValidatorFunc(func(context.Context, int, string) (bool, error) {
		return true, nil
	}))

	headerProfileID := "prof_header"
	_, err := svc.Ingest(context.Background(), 42, &headerProfileID, manifest, bytes.NewReader(bundle))
	if !errors.Is(err, ErrProfileMismatch) {
		t.Fatalf("Ingest error = %v, want ErrProfileMismatch", err)
	}
	if repo.insertInput.ProfileID != nil {
		t.Fatalf("ProfileID = %v, want no insert on profile mismatch", *repo.insertInput.ProfileID)
	}
	if len(repo.ready) != 0 || len(store.puts) != 0 {
		t.Fatalf("no report should be stored on mismatch: ready=%v puts=%v", repo.ready, store.puts)
	}
}

func TestServiceIngestAcceptsMatchingProfileID(t *testing.T) {
	// Header and manifest agree: attribution resolves to that single value.
	bundle, manifest, _ := testDiagnosticsUpload(t, "server-1", DefaultConsentNoticeVer, "prof_1")
	repo := &fakeDiagnosticReportStore{}
	store := &fakeDiagnosticObjectStore{bucket: "private"}
	svc := newTestDiagnosticsService(repo, store)
	svc.SetProfileAttributionValidator(ProfileAttributionValidatorFunc(func(_ context.Context, userID int, profileID string) (bool, error) {
		return userID == 42 && profileID == "prof_1", nil
	}))

	headerProfileID := "prof_1"
	if _, err := svc.Ingest(context.Background(), 42, &headerProfileID, manifest, bytes.NewReader(bundle)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if repo.insertInput.ProfileID == nil || *repo.insertInput.ProfileID != "prof_1" {
		t.Fatalf("ProfileID = %v, want prof_1", repo.insertInput.ProfileID)
	}
}

func TestNewProfileAttributionValidatorRejectsChildProfiles(t *testing.T) {
	tests := []struct {
		name      string
		found     bool
		isChild   bool
		lookErr   error
		want      bool
		wantErr   bool
		wantChild bool
	}{
		{name: "non-child profile attributed", found: true, isChild: false, want: true},
		{name: "child profile forbidden", found: true, isChild: true, wantErr: true, wantChild: true},
		{name: "missing profile dropped", found: false, isChild: false, want: false},
		{name: "lookup error propagated", lookErr: errors.New("boom"), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			validator := NewProfileAttributionValidator(func(_ context.Context, userID int, profileID string) (bool, bool, error) {
				if userID != 42 || profileID != "prof_1" {
					t.Fatalf("lookup called with userID=%d profileID=%q", userID, profileID)
				}
				return tc.found, tc.isChild, tc.lookErr
			})
			got, err := validator.ProfileBelongsToUser(context.Background(), 42, "prof_1")
			if tc.wantErr {
				if err == nil {
					t.Fatal("ProfileBelongsToUser error = nil, want error")
				}
				if tc.wantChild && !errors.Is(err, ErrChildProfileForbidden) {
					t.Fatalf("ProfileBelongsToUser error = %v, want ErrChildProfileForbidden", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ProfileBelongsToUser: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ProfileBelongsToUser = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServiceIngestRejectsChildProfileAttribution(t *testing.T) {
	// A nonconforming client attributes the report to a child profile. The
	// validator rejects it with ErrChildProfileForbidden, so the upload is
	// refused outright rather than silently stored without attribution.
	bundle, manifest, _ := testDiagnosticsUpload(t, "server-1", DefaultConsentNoticeVer, "prof_child")
	repo := &fakeDiagnosticReportStore{}
	store := &fakeDiagnosticObjectStore{bucket: "private"}
	svc := newTestDiagnosticsService(repo, store)
	svc.SetProfileAttributionValidator(ProfileAttributionValidatorFunc(func(context.Context, int, string) (bool, error) {
		return false, ErrChildProfileForbidden
	}))

	_, err := svc.Ingest(context.Background(), 42, nil, manifest, bytes.NewReader(bundle))
	if !errors.Is(err, ErrChildProfileForbidden) {
		t.Fatalf("Ingest error = %v, want ErrChildProfileForbidden", err)
	}
	if repo.insertInput.ProfileID != nil {
		t.Fatalf("ProfileID = %v, want no insert on child profile", *repo.insertInput.ProfileID)
	}
	if len(repo.ready) != 0 || len(store.puts) != 0 {
		t.Fatalf("no report should be stored on child rejection: ready=%v puts=%v", repo.ready, store.puts)
	}
}

func TestServiceIngestAcceptsManifestWithoutProfileID(t *testing.T) {
	bundle, manifest, _ := testDiagnosticsUpload(t, "server-1", DefaultConsentNoticeVer, "")
	repo := &fakeDiagnosticReportStore{}
	store := &fakeDiagnosticObjectStore{bucket: "private"}
	svc := newTestDiagnosticsService(repo, store)

	if _, err := svc.Ingest(context.Background(), 42, nil, manifest, bytes.NewReader(bundle)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if repo.insertInput.ProfileID != nil {
		t.Fatalf("ProfileID = %v, want nil", *repo.insertInput.ProfileID)
	}
}

func TestServiceDeleteReportDeletesRowWhenStorageUnavailable(t *testing.T) {
	report := testReadyDiagnosticReport("22222222-2222-2222-2222-222222222222", 42)
	repo := &fakeDiagnosticReportStore{
		getReport:    &report,
		deleteReport: &report,
	}
	svc := newTestDiagnosticsService(repo, nil)

	deleted, err := svc.DeleteReport(context.Background(), report.ID)
	if err != nil {
		t.Fatalf("DeleteReport: %v", err)
	}
	if deleted == nil || deleted.ID != report.ID {
		t.Fatalf("deleted report = %#v, want %s", deleted, report.ID)
	}
	if len(repo.deleted) != 1 || repo.deleted[0] != report.ID {
		t.Fatalf("deleted rows = %v, want [%s]", repo.deleted, report.ID)
	}
}

func TestServiceDeleteReportKeepsBlobWhenRowDeletionFails(t *testing.T) {
	report := testReadyDiagnosticReport("22222222-2222-2222-2222-222222222222", 42)
	repo := &fakeDiagnosticReportStore{
		getReport: &report,
		deleteErr: errors.New("db down"),
	}
	store := &fakeDiagnosticObjectStore{bucket: "private"}
	svc := newTestDiagnosticsService(repo, store)

	if _, err := svc.DeleteReport(context.Background(), report.ID); err == nil {
		t.Fatal("DeleteReport error = nil, want row deletion failure")
	}
	// The blob must survive a failed row deletion so a retry can still recover
	// the original artifact.
	if len(store.deleted) != 0 {
		t.Fatalf("blob deleted despite row deletion failure: %v", store.deleted)
	}
}

func TestServiceDeleteReportSucceedsWhenBlobDeletionFails(t *testing.T) {
	report := testReadyDiagnosticReport("22222222-2222-2222-2222-222222222222", 42)
	repo := &fakeDiagnosticReportStore{
		getReport:    &report,
		deleteReport: &report,
	}
	store := &fakeDiagnosticObjectStore{bucket: "private", deleteErr: errors.New("s3 down")}
	svc := newTestDiagnosticsService(repo, store)

	deleted, err := svc.DeleteReport(context.Background(), report.ID)
	if err != nil {
		t.Fatalf("DeleteReport error = %v, want success after row deletion", err)
	}
	if deleted == nil || deleted.ID != report.ID {
		t.Fatalf("deleted report = %#v, want %s", deleted, report.ID)
	}
	if len(repo.deleted) != 1 || repo.deleted[0] != report.ID {
		t.Fatalf("deleted rows = %v, want [%s]", repo.deleted, report.ID)
	}
	// The row is gone; the orphaned blob delete was attempted (and failed), and
	// the reconciler will reap it later.
	if len(store.deleted) == 0 {
		t.Fatal("blob deletion was not attempted after row removal")
	}
}

func newTestDiagnosticsService(repo ReportStore, store ObjectStore) *Service {
	settings := newMemorySettingsStore(map[string]string{
		KeyUploadsEnabled:       "true",
		KeyMaxBundleBytes:       "10485760",
		KeyMaxUncompressedBytes: "67108864",
		KeyMaxReportsPerUserDay: "20",
		KeyRetentionDays:        "30",
		KeyMaxBytesPerUser:      "209715200",
		KeyConsentNoticeVersion: "1",
		KeyServerInstanceID:     "server-1",
	})
	svc := NewService(repo, settings, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.now = func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) }
	return svc
}

func testDiagnosticsBundle(t *testing.T) ([]byte, BundleInfo) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	payload := []byte(`{}`)
	if err := tw.WriteHeader(&tar.Header{
		Name: "manifest.json",
		Mode: 0o600,
		Size: int64(len(payload)),
	}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatalf("write tar payload: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	data := buf.Bytes()
	info, err := ValidateBundle(bytes.NewReader(data), BundleLimits{})
	if err != nil {
		t.Fatalf("ValidateBundle: %v", err)
	}
	return data, info
}

func testManifestJSON(t *testing.T, serverID string, noticeVersion int, archive BundleInfo) []byte {
	t.Helper()
	return testManifestJSONWithProfile(t, serverID, noticeVersion, archive, "prof_1")
}

func testManifestJSONWithProfile(t *testing.T, serverID string, noticeVersion int, archive BundleInfo, profileID string) []byte {
	t.Helper()
	data := diagnosticsManifestBody(serverID, noticeVersion, profileID, "15", &archive)
	if _, err := contract.ValidateManifest(data); err != nil {
		t.Fatalf("test manifest invalid: %v\n%s", err, string(data))
	}
	return data
}

// diagnosticsManifestBody builds a manifest JSON body. When info is nil the
// `archive` object is omitted, producing the embedded manifest.json contract
// form (part-1 manifest minus `archive`).
func diagnosticsManifestBody(serverID string, noticeVersion int, profileID, osVersion string, info *BundleInfo) []byte {
	profileField := ""
	if profileID != "" {
		profileField = `,
			"profile_id": "` + profileID + `"`
	}
	archiveField := ""
	if info != nil {
		archiveField = `,
		"archive": {
			"entries": ["manifest.json"],
			"bytes": ` + strconv.FormatInt(info.CompressedBytes, 10) + `,
			"uncompressed_bytes": ` + strconv.FormatInt(info.UncompressedBytes, 10) + `,
			"sha256": "` + info.SHA256 + `"
		}`
	}
	return []byte(`{
		"schema_version": 1,
		"report": {
			"type": "manual",
			"captured_at": "2026-07-20T12:00:00Z",
			"capture_session_id": "run_test",
			"app_version": "1.0.0",
			"app_build": "100",
			"platform": "android",
			"os_version": "` + osVersion + `"` + profileField + `
		},
		"destination": { "server_instance_id": "` + serverID + `" },
		"consent": { "mode": "manual", "notice_version": ` + strconv.Itoa(noticeVersion) + ` },
		"device_summary": {
			"manufacturer": "Google",
			"model": "Pixel",
			"os": "15",
			"form_factor": "phone"
		},
		"playback_session_ids": ["ps_1"],
		"log_summary": {
			"lines": 1,
			"bytes_gz": 1,
			"dropped_lines": 0,
			"categories": ["crash"],
			"debug_logging": true
		}` + archiveField + `
	}`)
}

// testDiagnosticsUpload builds a coordinated (bundle, manifest) pair: the tar's
// embedded manifest.json is the part-1 manifest minus `archive`, and the part-1
// manifest's archive fields match the built bundle — the shape Ingest accepts.
func testDiagnosticsUpload(t *testing.T, serverID string, noticeVersion int, profileID string) ([]byte, []byte, BundleInfo) {
	t.Helper()
	embedded := diagnosticsManifestBody(serverID, noticeVersion, profileID, "15", nil)
	bundle := buildManifestBundle(t, embedded)
	info, err := ValidateBundle(bytes.NewReader(bundle), BundleLimits{})
	if err != nil {
		t.Fatalf("ValidateBundle: %v", err)
	}
	manifest := diagnosticsManifestBody(serverID, noticeVersion, profileID, "15", &info)
	if _, err := contract.ValidateManifest(manifest); err != nil {
		t.Fatalf("test manifest invalid: %v\n%s", err, manifest)
	}
	return bundle, manifest, info
}

func buildManifestBundle(t *testing.T, manifestBody []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "manifest.json",
		Mode: 0o600,
		Size: int64(len(manifestBody)),
	}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(manifestBody); err != nil {
		t.Fatalf("write tar payload: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

type fakeDiagnosticReportStore struct {
	insertCalls       int
	insertInput       InsertReceivingInput
	insertErr         error
	ready             []string
	readyBlobs        []BlobInfo
	failed            []string
	markFailedCtxErrs []error
	getReport         *Report
	getErr            error
	deleteReport      *Report
	deleteErr         error
	deleted           []string
}

func (f *fakeDiagnosticReportStore) InsertReceiving(_ context.Context, input InsertReceivingInput) (InsertReceivingResult, error) {
	f.insertCalls++
	f.insertInput = input
	if f.insertErr != nil {
		return InsertReceivingResult{}, f.insertErr
	}
	return InsertReceivingResult{
		ID:      "11111111-1111-1111-1111-111111111111",
		ShortID: "SILO-ABCDEF123456",
	}, nil
}

func (f *fakeDiagnosticReportStore) MarkReady(_ context.Context, id string, blob BlobInfo) error {
	f.ready = append(f.ready, id)
	f.readyBlobs = append(f.readyBlobs, blob)
	return nil
}

func (f *fakeDiagnosticReportStore) MarkFailed(ctx context.Context, id string) error {
	f.failed = append(f.failed, id)
	f.markFailedCtxErrs = append(f.markFailedCtxErrs, ctx.Err())
	return nil
}

func (f *fakeDiagnosticReportStore) GetByID(context.Context, string) (*Report, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getReport != nil {
		report := *f.getReport
		return &report, nil
	}
	return nil, ErrNotFound
}

func (f *fakeDiagnosticReportStore) ListForAdmin(context.Context, ListFilters) (ListResult, error) {
	return ListResult{}, nil
}

func (f *fakeDiagnosticReportStore) DeleteByID(_ context.Context, id string) (*Report, error) {
	f.deleted = append(f.deleted, id)
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	if f.deleteReport != nil {
		report := *f.deleteReport
		return &report, nil
	}
	return nil, ErrNotFound
}

func (f *fakeDiagnosticReportStore) RetentionCandidates(context.Context, time.Time, int64) ([]Report, error) {
	return nil, nil
}

func (f *fakeDiagnosticReportStore) StaleReceiving(context.Context, time.Duration) ([]Report, error) {
	return nil, nil
}

type fakeDiagnosticObjectStore struct {
	bucket        string
	puts          []string
	deleted       []string
	deleteCtxErrs []error
	deleteErr     error
	data          []byte
	putErr        error
	putReadBytes  int64
}

func (f *fakeDiagnosticObjectStore) PutStream(_ context.Context, _ string, key string, r io.Reader, _ string) error {
	if f.putErr != nil {
		if f.putReadBytes > 0 {
			_, _ = io.CopyN(io.Discard, r, f.putReadBytes)
		}
		f.puts = append(f.puts, key)
		return f.putErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.puts = append(f.puts, key)
	f.data = append(f.data[:0], data...)
	return nil
}

func (f *fakeDiagnosticObjectStore) GetObject(context.Context, string, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.data)), nil
}

func (f *fakeDiagnosticObjectStore) DeleteObject(ctx context.Context, _ string, key string) error {
	f.deleted = append(f.deleted, key)
	f.deleteCtxErrs = append(f.deleteCtxErrs, ctx.Err())
	return f.deleteErr
}

func (f *fakeDiagnosticObjectStore) ListObjects(context.Context, string) ([]string, error) {
	return nil, nil
}

func (f *fakeDiagnosticObjectStore) PresignGetURL(context.Context, string, string, time.Duration) (string, error) {
	return "", nil
}

func (f *fakeDiagnosticObjectStore) Bucket() string {
	return f.bucket
}

func testReadyDiagnosticReport(id string, userID int) Report {
	bucket := "private"
	key := reportObjectKey(userID, id)
	bytes := int64(128)
	uncompressed := int64(256)
	sha := strings.Repeat("a", 64)
	return Report{
		ID:                id,
		ShortID:           "SILO-ABCDEF123456",
		UserID:            userID,
		State:             StateReady,
		BlobBucket:        &bucket,
		BlobKey:           &key,
		BlobBytes:         &bytes,
		UncompressedBytes: &uncompressed,
		BlobSHA256:        &sha,
	}
}

func TestServiceStatusAdvertisesAcceptedCrashSources(t *testing.T) {
	svc := newTestDiagnosticsService(&fakeDiagnosticReportStore{}, &fakeDiagnosticObjectStore{bucket: "private"})

	status, err := svc.Status(context.Background(), 42)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := []string{"ueh", "exit_info", "metrickit", "exit_sentinel", "watchdog"}
	if !slices.Equal(status.AcceptedCrashSources, want) {
		t.Fatalf("AcceptedCrashSources = %v, want %v", status.AcceptedCrashSources, want)
	}
	status.AcceptedCrashSources[0] = "changed"
	if contract.CrashSources()[0] != "ueh" {
		t.Fatal("status shares the contract's crash source list")
	}
}

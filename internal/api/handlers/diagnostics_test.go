package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

func readyDiagnosticsReport() *diagnostics.Report {
	blobBucket := "private"
	blobKey := "diagnostics/7/report-1.tar.gz"
	blobBytes := int64(13)
	return &diagnostics.Report{
		ID:         "report-1",
		ShortID:    "SILO-ABCDEF123456",
		UserID:     7,
		State:      diagnostics.StateReady,
		ReportType: "crash",
		Platform:   "android-tv",
		BlobBucket: &blobBucket,
		BlobKey:    &blobKey,
		BlobBytes:  &blobBytes,
	}
}

type fakeDiagnosticsService struct {
	mu              sync.Mutex
	status          diagnostics.Status
	statusErr       error
	statusCalls     int
	ingestCalls     int
	ingestErr       error
	lastManifest    []byte
	lastBundleBytes int64
	started         chan struct{}
	block           chan struct{}
	listCalls       int
	listFilters     diagnostics.ListFilters
	listResult      diagnostics.ListResult
	listErr         error
	getReport       *diagnostics.Report
	getErr          error
	presignURL      string
	presignErr      error
	presignCalls    int
	presignExpiry   time.Duration
	effectiveTTL    time.Duration
	openCalls       int
	openData        []byte
	openErr         error
	deleteReport    *diagnostics.Report
	deleteErr       error
}

func newFakeDiagnosticsService() *fakeDiagnosticsService {
	return &fakeDiagnosticsService{
		status: diagnostics.Status{
			Status:                 diagnostics.StatusAvailable,
			ServerInstanceID:       "server-1",
			AcceptedSchemaVersions: []int{1},
			MaxBundleBytes:         diagnostics.DefaultMaxBundleBytes,
			MaxManifestBytes:       diagnostics.MaxManifestBytes,
			RetentionDays:          diagnostics.DefaultRetentionDays,
			ConsentNoticeVersion:   diagnostics.DefaultConsentNoticeVer,
		},
	}
}

func (f *fakeDiagnosticsService) Status(context.Context, int) (diagnostics.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	if f.statusErr != nil {
		return diagnostics.Status{}, f.statusErr
	}
	return f.status, nil
}

func (f *fakeDiagnosticsService) setStatusErr(err error) {
	f.mu.Lock()
	f.statusErr = err
	f.mu.Unlock()
}

func (f *fakeDiagnosticsService) Ingest(_ context.Context, _ int, _ *string, manifestJSON []byte, bundle io.Reader) (diagnostics.IngestResult, error) {
	f.mu.Lock()
	f.ingestCalls++
	f.lastManifest = append([]byte(nil), manifestJSON...)
	started := f.started
	block := f.block
	err := f.ingestErr
	f.mu.Unlock()

	if started != nil {
		close(started)
	}
	if block != nil {
		<-block
	}
	if err != nil {
		return diagnostics.IngestResult{}, err
	}
	consumed, err := io.Copy(io.Discard, bundle)
	if err != nil {
		return diagnostics.IngestResult{}, err
	}
	f.mu.Lock()
	f.lastBundleBytes = consumed
	f.mu.Unlock()
	return diagnostics.IngestResult{
		ReportID: "11111111-1111-1111-1111-111111111111",
		ShortID:  "SILO-ABCDEF123456",
	}, nil
}

func (f *fakeDiagnosticsService) ListForAdmin(_ context.Context, filters diagnostics.ListFilters) (diagnostics.ListResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	f.listFilters = filters
	if f.listErr != nil {
		return diagnostics.ListResult{}, f.listErr
	}
	return f.listResult, nil
}

func (f *fakeDiagnosticsService) GetReport(context.Context, string) (*diagnostics.Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getReport != nil {
		return f.getReport, nil
	}
	return readyDiagnosticsReport(), nil
}

func (f *fakeDiagnosticsService) PresignReportDownload(_ context.Context, _ *diagnostics.Report, expiry time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presignCalls++
	f.presignExpiry = expiry
	if f.presignErr != nil {
		return "", f.presignErr
	}
	return f.presignURL, nil
}

func (f *fakeDiagnosticsService) EffectiveReportDownloadTTL(requested time.Duration) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.effectiveTTL > 0 {
		return f.effectiveTTL
	}
	return requested
}

func (f *fakeDiagnosticsService) OpenReportDownload(context.Context, *diagnostics.Report) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openCalls++
	if f.openErr != nil {
		return nil, f.openErr
	}
	return io.NopCloser(bytes.NewReader(f.openData)), nil
}

func (f *fakeDiagnosticsService) DeleteReport(context.Context, string) (*diagnostics.Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	if f.deleteReport != nil {
		return f.deleteReport, nil
	}
	return readyDiagnosticsReport(), nil
}

type fakeDiagnosticsEnablementStore struct {
	bucket string
	ops    []string
}

func (f *fakeDiagnosticsEnablementStore) PutStream(_ context.Context, _ string, key string, r io.Reader, _ string) error {
	if _, err := io.ReadAll(r); err != nil {
		return err
	}
	f.ops = append(f.ops, "put:"+key)
	return nil
}

func (f *fakeDiagnosticsEnablementStore) DeleteObject(_ context.Context, _ string, key string) error {
	f.ops = append(f.ops, "delete:"+key)
	return nil
}

func (f *fakeDiagnosticsEnablementStore) Bucket() string {
	return f.bucket
}

func sameStringSlice(a, b []string) bool {
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

func TestOpenAdminDiagnosticDownloadAlwaysStreams(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.getReport = readyDiagnosticsReport()
	service.presignURL = "https://storage.example.test/report"
	service.openData = []byte("bundle")
	download, err := NewDiagnosticsHandler(service).OpenAdminDiagnosticDownload(t.Context(), "report-1")
	if err != nil {
		t.Fatal(err)
	}
	defer download.Body.Close()
	body, err := io.ReadAll(download.Body)
	if err != nil || string(body) != "bundle" || service.presignCalls != 0 || service.openCalls != 1 || download.Size == nil || download.Filename == "" {
		t.Fatal(download, err, service.presignCalls, service.openCalls)
	}
	service.getReport.State = diagnostics.StateReceiving
	_, err = NewDiagnosticsHandler(service).OpenAdminDiagnosticDownload(t.Context(), "report-1")
	if !errors.Is(err, diagnostics.ErrReportNotReady) || service.openCalls != 1 {
		t.Fatal(err, service.openCalls)
	}
}

func TestAdminDiagnosticReadServicesReuseStore(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.getReport = readyDiagnosticsReport()
	service.listResult = diagnostics.ListResult{Reports: []diagnostics.Report{*service.getReport}, NextCursor: "next"}
	handler := NewDiagnosticsHandler(service)
	filters := diagnostics.ListFilters{UserID: new(7), Limit: 5, Cursor: "position"}
	page, err := handler.ListAdminDiagnosticReports(t.Context(), filters)
	if err != nil || len(page.Reports) != 1 || service.listFilters.Cursor != "position" || service.listFilters.Limit != 5 {
		t.Fatal(page, err, service.listFilters)
	}
	report, err := handler.GetAdminDiagnosticReport(t.Context(), "report-1")
	if err != nil || report != service.getReport {
		t.Fatal(report, err)
	}
}

func TestDeleteAdminDiagnosticReportTreatsAbsentAsSuccess(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := NewDiagnosticsHandler(service)
	if err := handler.DeleteAdminDiagnosticReport(t.Context(), "report-1"); err != nil {
		t.Fatal(err)
	}
	service.deleteErr = diagnostics.ErrNotFound
	if err := handler.DeleteAdminDiagnosticReport(t.Context(), "report-1"); err != nil {
		t.Fatal(err)
	}
	service.deleteErr = errors.New("database failed")
	if err := handler.DeleteAdminDiagnosticReport(t.Context(), "report-1"); err == nil {
		t.Fatal("database error suppressed")
	}
}

func adminSettingFailure(t *testing.T, err error) *APIError {
	t.Helper()
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok {
		t.Fatalf("err = %T %v, want *APIError", err, err)
	}
	return apiErr
}

// Turning diagnostics uploads on must prove the private store accepts a write
// first; without one, the setting is refused and nothing is stored.
func TestUpdateAdminSettingDiagnosticsUploadsRequireStorageProbe(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	handler := &AdminHandler{SettingsRepo: settings}

	_, err := handler.UpdateAdminSetting(context.Background(), diagnostics.KeyUploadsEnabled, "true", nil)
	if apiErr := adminSettingFailure(t, err); apiErr.Status != http.StatusBadRequest || apiErr.Code != errCodeStorageUnavailable {
		t.Fatalf("enable without storage = %d %q, want 400 %s", apiErr.Status, apiErr.Code, errCodeStorageUnavailable)
	}
	if got := settings.values[diagnostics.KeyUploadsEnabled]; got != "" {
		t.Fatalf("setting persisted = %q, want nothing", got)
	}

	store := &fakeDiagnosticsEnablementStore{bucket: "private"}
	handler.DiagnosticsStore = store
	if _, err := handler.UpdateAdminSetting(context.Background(), diagnostics.KeyUploadsEnabled, "true", nil); err != nil {
		t.Fatalf("enable with storage: %v", err)
	}
	if got := settings.values[diagnostics.KeyUploadsEnabled]; got != "true" {
		t.Fatalf("setting persisted = %q, want true", got)
	}
	if !sameStringSlice(store.ops, []string{"put:diagnostics/.probe", "delete:diagnostics/.probe"}) {
		t.Fatalf("probe ops = %v, want put then delete of the probe", store.ops)
	}
}

// The diagnostics limits are range-checked on their own and against each
// other; a rejected value names its key and is never stored.
func TestUpdateAdminSettingDiagnosticsLimitsRejectOutOfRange(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		value   string
		initial map[string]string
	}{
		{name: "bundle below minimum", key: diagnostics.KeyMaxBundleBytes, value: "1048575"},
		{name: "bundle above maximum", key: diagnostics.KeyMaxBundleBytes, value: "268435457", initial: map[string]string{diagnostics.KeyMaxUncompressedBytes: "1073741824"}},
		{name: "bundle above uncompressed", key: diagnostics.KeyMaxBundleBytes, value: "67108865", initial: map[string]string{diagnostics.KeyMaxUncompressedBytes: "67108864"}},
		{name: "uncompressed below bundle", key: diagnostics.KeyMaxUncompressedBytes, value: "10485759", initial: map[string]string{diagnostics.KeyMaxBundleBytes: "10485760"}},
		{name: "bundle above per-user cap", key: diagnostics.KeyMaxBundleBytes, value: "104857600", initial: map[string]string{diagnostics.KeyMaxUncompressedBytes: "1073741824", diagnostics.KeyMaxBytesPerUser: "10485760"}},
		{name: "per-user below bundle", key: diagnostics.KeyMaxBytesPerUser, value: "10485760", initial: map[string]string{diagnostics.KeyMaxBundleBytes: "104857600", diagnostics.KeyMaxUncompressedBytes: "1073741824"}},
		{name: "uncompressed above maximum", key: diagnostics.KeyMaxUncompressedBytes, value: "1073741825"},
		{name: "reports below minimum", key: diagnostics.KeyMaxReportsPerUserDay, value: "0"},
		{name: "reports above maximum", key: diagnostics.KeyMaxReportsPerUserDay, value: "1001"},
		{name: "retention below minimum", key: diagnostics.KeyRetentionDays, value: "0"},
		{name: "retention above maximum", key: diagnostics.KeyRetentionDays, value: "366"},
		{name: "user bytes below minimum", key: diagnostics.KeyMaxBytesPerUser, value: "10485759"},
		{name: "user bytes above maximum", key: diagnostics.KeyMaxBytesPerUser, value: "10737418241"},
		{name: "not an integer", key: diagnostics.KeyRetentionDays, value: "thirty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: tc.initial}
			_, err := (&AdminHandler{SettingsRepo: settings}).UpdateAdminSetting(context.Background(), tc.key, tc.value, nil)
			apiErr := adminSettingFailure(t, err)
			if apiErr.Status != http.StatusBadRequest || apiErr.Code != "bad_request" || !strings.Contains(apiErr.Message, tc.key) {
				t.Fatalf("error = %d %q %q, want 400 bad_request naming %s", apiErr.Status, apiErr.Code, apiErr.Message, tc.key)
			}
			if _, stored := settings.values[tc.key]; stored {
				t.Fatalf("invalid value was stored: %#v", settings.values)
			}
		})
	}
}

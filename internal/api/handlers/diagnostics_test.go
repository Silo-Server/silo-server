package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

func TestDiagnosticsUploadHappyPath(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := NewDiagnosticsHandler(service)

	rec := httptest.NewRecorder()
	handler.HandleUpload(rec, newDiagnosticsUploadRequest(t, []diagnosticsPart{
		{name: "manifest", contentType: "application/json", body: []byte(`{"ok":true}`)},
		{name: "bundle", contentType: "application/gzip", body: []byte("bundle")},
	}, accessClaims()))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var resp diagnostics.IngestResult
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ShortID != "SILO-ABCDEF123456" {
		t.Fatalf("short_id = %q, want SILO-ABCDEF123456", resp.ShortID)
	}
	if service.ingestCalls != 1 {
		t.Fatalf("ingest calls = %d, want 1", service.ingestCalls)
	}
}

func TestDiagnosticsUploadManifestTooBig(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := NewDiagnosticsHandler(service)
	logs := captureDiagnosticsLogs(handler)

	rec := httptest.NewRecorder()
	handler.HandleUpload(rec, newDiagnosticsUploadRequest(t, []diagnosticsPart{
		{name: "manifest", contentType: "application/json", body: bytes.Repeat([]byte("x"), int(diagnostics.MaxManifestBytes)+1)},
		{name: "bundle", contentType: "application/gzip", body: []byte("bundle")},
	}, accessClaims()))

	assertDiagnosticsError(t, rec, http.StatusRequestEntityTooLarge, "too_large")
	if service.ingestCalls != 0 {
		t.Fatalf("ingest calls = %d, want 0", service.ingestCalls)
	}
	assertDiagnosticsRejectionLog(t, logs, "too_large", 42)
}

func TestDiagnosticsUploadWrongPartOrder(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := NewDiagnosticsHandler(service)
	logs := captureDiagnosticsLogs(handler)

	rec := httptest.NewRecorder()
	handler.HandleUpload(rec, newDiagnosticsUploadRequest(t, []diagnosticsPart{
		{name: "bundle", contentType: "application/gzip", body: []byte("bundle")},
		{name: "manifest", contentType: "application/json", body: []byte(`{"ok":true}`)},
	}, accessClaims()))

	assertDiagnosticsError(t, rec, http.StatusBadRequest, "invalid_bundle")
	if service.ingestCalls != 0 {
		t.Fatalf("ingest calls = %d, want 0", service.ingestCalls)
	}
	assertDiagnosticsRejectionLog(t, logs, "invalid_bundle", 42)
}

func TestDiagnosticsUploadWrongFirstPartNotDrained(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := NewDiagnosticsHandler(service)

	// A large, wrongly-named first part must be rejected without draining its
	// body: an invalid upload must not be allowed to stream its whole payload
	// (holding the in-flight slot) before it gets a 400.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="bundle"`)
	header.Set("Content-Type", "application/gzip")
	w, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("create multipart part: %v", err)
	}
	if _, err := w.Write(bytes.Repeat([]byte("x"), 1<<20)); err != nil {
		t.Fatalf("write multipart part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	counter := &countingReader{r: &body}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/diagnostics/reports", counter)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer token")
	req = req.WithContext(apimw.SetClaims(req.Context(), accessClaims()))

	rec := httptest.NewRecorder()
	handler.HandleUpload(rec, req)

	assertDiagnosticsError(t, rec, http.StatusBadRequest, "invalid_bundle")
	if service.ingestCalls != 0 {
		t.Fatalf("ingest calls = %d, want 0", service.ingestCalls)
	}
	// Only the small multipart framing/headers should have been read; the 1 MiB
	// wrong-first-part body must not have been drained.
	if counter.n > 128*1024 {
		t.Fatalf("read %d bytes from request body, want the wrong first part rejected without draining (< 128 KiB)", counter.n)
	}
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func TestDiagnosticsUploadBodyTooLargeLogsRejection(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.status.MaxBundleBytes = 1
	handler := NewDiagnosticsHandler(service)
	logs := captureDiagnosticsLogs(handler)

	body := strings.NewReader(strings.Repeat("x\n", int(diagnosticsMultipartOverheadBytes/2)+2))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/diagnostics/reports", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=diagnostics-boundary")
	req.Header.Set("Authorization", "Bearer token")
	req = req.WithContext(apimw.SetClaims(req.Context(), accessClaims()))
	rec := httptest.NewRecorder()
	handler.HandleUpload(rec, req)

	assertDiagnosticsError(t, rec, http.StatusRequestEntityTooLarge, "too_large")
	if service.ingestCalls != 0 {
		t.Fatalf("ingest calls = %d, want 0", service.ingestCalls)
	}
	assertDiagnosticsRejectionLog(t, logs, "too_large", 42)
}

func TestDiagnosticsUploadMissingPart(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := NewDiagnosticsHandler(service)

	rec := httptest.NewRecorder()
	handler.HandleUpload(rec, newDiagnosticsUploadRequest(t, []diagnosticsPart{
		{name: "manifest", contentType: "application/json", body: []byte(`{"ok":true}`)},
	}, accessClaims()))

	assertDiagnosticsError(t, rec, http.StatusBadRequest, "invalid_bundle")
	if service.ingestCalls != 0 {
		t.Fatalf("ingest calls = %d, want 0", service.ingestCalls)
	}
}

func TestDiagnosticsUploadDisabled(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.status.Status = diagnostics.StatusDisabled
	handler := NewDiagnosticsHandler(service)

	rec := httptest.NewRecorder()
	handler.HandleUpload(rec, newDiagnosticsUploadRequest(t, validDiagnosticsParts(), accessClaims()))

	assertDiagnosticsError(t, rec, http.StatusForbidden, "disabled")
	if service.ingestCalls != 0 {
		t.Fatalf("ingest calls = %d, want 0", service.ingestCalls)
	}
}

func TestDiagnosticsUploadStorageUnavailable(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.status.Status = diagnostics.StatusStorageUnavailable
	handler := NewDiagnosticsHandler(service)

	rec := httptest.NewRecorder()
	handler.HandleUpload(rec, newDiagnosticsUploadRequest(t, validDiagnosticsParts(), accessClaims()))

	assertDiagnosticsError(t, rec, http.StatusServiceUnavailable, "storage_unavailable")
	if service.ingestCalls != 0 {
		t.Fatalf("ingest calls = %d, want 0", service.ingestCalls)
	}
}

func TestDiagnosticsUploadQuotaExceededSetsRetryAfter(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.ingestErr = &diagnostics.QuotaError{Kind: diagnostics.QuotaKindReportsPerDay, Limit: 20}
	handler := NewDiagnosticsHandler(service)

	rec := httptest.NewRecorder()
	handler.HandleUpload(rec, newDiagnosticsUploadRequest(t, validDiagnosticsParts(), accessClaims()))

	assertDiagnosticsError(t, rec, http.StatusTooManyRequests, "quota_exceeded")
	if got := rec.Header().Get("Retry-After"); got != diagnosticsQuotaRetryAfter {
		t.Fatalf("Retry-After = %q, want %q", got, diagnosticsQuotaRetryAfter)
	}
}

func TestDiagnosticsUploadRejectsAPIKey(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := NewDiagnosticsHandler(service)
	logs := captureDiagnosticsLogs(handler)

	rec := httptest.NewRecorder()
	req := newDiagnosticsUploadRequest(t, validDiagnosticsParts(), &auth.Claims{
		UserID:    42,
		TokenType: auth.TokenTypeAPIKey,
	})
	req.Header.Set("Authorization", "Bearer sa_test")
	handler.HandleUpload(rec, req)

	assertDiagnosticsError(t, rec, http.StatusForbidden, "api_key_not_allowed")
	if service.statusCalls != 0 {
		t.Fatalf("status calls = %d, want 0", service.statusCalls)
	}
	assertDiagnosticsRejectionLog(t, logs, "api_key_not_allowed", 42)
}

func TestDiagnosticsUploadBusy(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.started = make(chan struct{})
	service.block = make(chan struct{})
	handler := NewDiagnosticsHandler(service)
	logs := captureDiagnosticsLogs(handler)

	firstReq := newDiagnosticsUploadRequest(t, validDiagnosticsParts(), accessClaims())
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		handler.HandleUpload(rec, firstReq)
		firstDone <- rec
	}()
	<-service.started

	rec := httptest.NewRecorder()
	handler.HandleUpload(rec, newDiagnosticsUploadRequest(t, validDiagnosticsParts(), accessClaims()))

	assertDiagnosticsError(t, rec, http.StatusServiceUnavailable, "busy")
	if got := rec.Header().Get("Retry-After"); got != diagnosticsBusyRetryAfter {
		t.Fatalf("Retry-After = %q, want %q", got, diagnosticsBusyRetryAfter)
	}
	assertDiagnosticsRejectionLog(t, logs, "busy", 42)

	close(service.block)
	first := <-firstDone
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want %d; body=%s", first.Code, http.StatusCreated, first.Body.String())
	}
}

func TestDiagnosticsAdminListReportsParsesFilters(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := NewDiagnosticsHandler(service)
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/diagnostics/reports?user_id=42&platform=ios&report_type=crash&from=2026-07-19T10:00:00Z&to=2026-07-20T10:00:00Z&short_id=abcdef123456&limit=25&cursor=next",
		nil,
	)
	rec := httptest.NewRecorder()

	handler.HandleAdminListReports(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if service.listCalls != 1 {
		t.Fatalf("list calls = %d, want 1", service.listCalls)
	}
	filters := service.listFilters
	if filters.UserID == nil || *filters.UserID != 42 {
		t.Fatalf("UserID = %v, want 42", filters.UserID)
	}
	if filters.Platform != "ios" || filters.ReportType != "crash" {
		t.Fatalf("filters = %#v, want platform ios report_type crash", filters)
	}
	if filters.From == nil || !filters.From.Equal(time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("From = %v, want expected timestamp", filters.From)
	}
	if filters.To == nil || !filters.To.Equal(time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("To = %v, want expected timestamp", filters.To)
	}
	if filters.ShortID != "SILO-ABCDEF123456" || filters.Limit != 25 || filters.Cursor != "next" {
		t.Fatalf("filters = %#v, want normalized short id, limit, cursor", filters)
	}
}

func TestDiagnosticsAdminDownloadReturnsPresignedURLWhenAvailable(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.getReport = readyDiagnosticsReport()
	service.presignURL = "https://storage.example.test/report"
	service.effectiveTTL = 5 * time.Minute
	handler := NewDiagnosticsHandler(service)

	before := time.Now().UTC()
	rec := httptest.NewRecorder()
	adminDiagnosticsRouter(handler).ServeHTTP(rec, adminDiagnosticsRequest(http.MethodGet, "/diagnostics/reports/report-1/download", adminClaims()))
	after := time.Now().UTC()

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp diagnosticsDownloadURLResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.DownloadURL != service.presignURL {
		t.Fatalf("download_url = %q, want %q", resp.DownloadURL, service.presignURL)
	}
	if service.presignExpiry != service.effectiveTTL {
		t.Fatalf("presign expiry = %s, want %s", service.presignExpiry, service.effectiveTTL)
	}
	if resp.ExpiresAt.Before(before.Add(service.effectiveTTL)) || resp.ExpiresAt.After(after.Add(service.effectiveTTL)) {
		t.Fatalf("expires_at = %s, want within effective TTL window", resp.ExpiresAt)
	}
	if service.openCalls != 0 {
		t.Fatalf("open calls = %d, want 0", service.openCalls)
	}
}

func TestDiagnosticsAdminDownloadStreamsWhenProxyRequested(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.getReport = readyDiagnosticsReport()
	service.presignURL = "https://storage.example.test/report"
	service.openData = []byte("gzipped report")
	handler := NewDiagnosticsHandler(service)

	rec := httptest.NewRecorder()
	adminDiagnosticsRouter(handler).ServeHTTP(rec, adminDiagnosticsRequest(http.MethodGet, "/diagnostics/reports/report-1/download?proxy=1", adminClaims()))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if service.presignCalls != 0 {
		t.Fatalf("presign calls = %d, want 0", service.presignCalls)
	}
	if service.openCalls != 1 {
		t.Fatalf("open calls = %d, want 1", service.openCalls)
	}
	if rec.Body.String() != "gzipped report" {
		t.Fatalf("body = %q, want streamed data", rec.Body.String())
	}
}

func TestDiagnosticsAdminDownloadStreamsWhenPresignUnavailable(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.getReport = readyDiagnosticsReport()
	service.presignErr = errors.New("presign unavailable")
	service.openData = []byte("gzipped report")
	handler := NewDiagnosticsHandler(service)

	rec := httptest.NewRecorder()
	adminDiagnosticsRouter(handler).ServeHTTP(rec, adminDiagnosticsRequest(http.MethodGet, "/diagnostics/reports/report-1/download", adminClaims()))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != diagnostics.ReportDownloadContentType {
		t.Fatalf("Content-Type = %q, want %q", got, diagnostics.ReportDownloadContentType)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "silo-diagnostics-SILO-ABCDEF123456.tar.gz") {
		t.Fatalf("Content-Disposition = %q, want diagnostic filename", rec.Header().Get("Content-Disposition"))
	}
	if rec.Body.String() != "gzipped report" {
		t.Fatalf("body = %q, want streamed data", rec.Body.String())
	}
}

func TestDiagnosticsAdminListReportsRejectsMalformedQuery(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := NewDiagnosticsHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/diagnostics/reports?user_id=abc", nil)
	rec := httptest.NewRecorder()

	handler.HandleAdminListReports(rec, req)

	assertDiagnosticsError(t, rec, http.StatusBadRequest, "bad_request")
	if service.listCalls != 0 {
		t.Fatalf("list calls = %d, want 0", service.listCalls)
	}
}

func TestDiagnosticsAdminListReportsRejectsMalformedCursor(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.listErr = diagnostics.ErrInvalidCursor
	handler := NewDiagnosticsHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/diagnostics/reports?cursor=bad", nil)
	rec := httptest.NewRecorder()

	handler.HandleAdminListReports(rec, req)

	assertDiagnosticsError(t, rec, http.StatusBadRequest, "bad_request")
}

func TestDiagnosticsAdminDeleteEmitsAuditEvent(t *testing.T) {
	service := newFakeDiagnosticsService()
	service.deleteReport = readyDiagnosticsReport()
	var logs bytes.Buffer
	handler := NewDiagnosticsHandler(service)
	handler.logger = slog.New(slog.NewJSONHandler(&logs, nil))

	rec := httptest.NewRecorder()
	adminDiagnosticsRouter(handler).ServeHTTP(rec, adminDiagnosticsRequest(http.MethodDelete, "/diagnostics/reports/report-1", adminClaims()))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	logLine := logs.String()
	if !strings.Contains(logLine, `"msg":"diagnostic report deleted"`) ||
		!strings.Contains(logLine, `"admin_user_id":7`) ||
		!strings.Contains(logLine, `"report_id":"report-1"`) {
		t.Fatalf("audit log = %s", logLine)
	}
}

func TestDiagnosticsAdminRoutesRejectNonActingAdmin(t *testing.T) {
	service := newFakeDiagnosticsService()
	handler := NewDiagnosticsHandler(service)
	router := chi.NewRouter()
	router.Use(apimw.RequireActingAdmin(nil))
	RegisterAdminDiagnosticsRoutes(router, handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminDiagnosticsRequest(http.MethodGet, "/diagnostics/reports", &auth.Claims{
		UserID:    8,
		Role:      "user",
		TokenType: auth.TokenTypeAccess,
	}))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if service.listCalls != 0 {
		t.Fatalf("list calls = %d, want 0", service.listCalls)
	}
}

func TestDiagnosticsUploadsEnablementRequiresStorageProbe(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	handler := &AdminHandler{SettingsRepo: settings}
	router := chi.NewRouter()
	router.Put("/admin/settings/{key}", handler.HandleUpdateSetting)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPut,
		"/admin/settings/"+diagnostics.KeyUploadsEnabled,
		strings.NewReader(`{"value":"true"}`),
	))

	assertDiagnosticsError(t, rec, http.StatusBadRequest, "storage_unavailable")
	if settings.values[diagnostics.KeyUploadsEnabled] != "" {
		t.Fatalf("setting persisted = %q, want empty", settings.values[diagnostics.KeyUploadsEnabled])
	}

	store := &fakeDiagnosticsEnablementStore{bucket: "private"}
	handler.DiagnosticsStore = store
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPut,
		"/admin/settings/"+diagnostics.KeyUploadsEnabled,
		strings.NewReader(`{"value":"true"}`),
	))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if settings.values[diagnostics.KeyUploadsEnabled] != "true" {
		t.Fatalf("setting persisted = %q, want true", settings.values[diagnostics.KeyUploadsEnabled])
	}
	if !sameStringSlice(store.ops, []string{"put:diagnostics/.probe", "delete:diagnostics/.probe"}) {
		t.Fatalf("probe ops = %v, want put/delete probe", store.ops)
	}
}

func TestDiagnosticsNumericSettingsAcceptBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		value   string
		initial map[string]string
	}{
		{name: "bundle minimum", key: diagnostics.KeyMaxBundleBytes, value: "1048576", initial: map[string]string{diagnostics.KeyMaxUncompressedBytes: "1073741824"}},
		{name: "bundle maximum", key: diagnostics.KeyMaxBundleBytes, value: "268435456", initial: map[string]string{diagnostics.KeyMaxUncompressedBytes: "1073741824", diagnostics.KeyMaxBytesPerUser: "10737418240"}},
		{name: "uncompressed bundle floor", key: diagnostics.KeyMaxUncompressedBytes, value: "1048576", initial: map[string]string{diagnostics.KeyMaxBundleBytes: "1048576"}},
		{name: "uncompressed maximum", key: diagnostics.KeyMaxUncompressedBytes, value: "1073741824", initial: map[string]string{diagnostics.KeyMaxBundleBytes: "1048576"}},
		{name: "reports minimum", key: diagnostics.KeyMaxReportsPerUserDay, value: "1"},
		{name: "reports maximum", key: diagnostics.KeyMaxReportsPerUserDay, value: "1000"},
		{name: "retention minimum", key: diagnostics.KeyRetentionDays, value: "1"},
		{name: "retention maximum", key: diagnostics.KeyRetentionDays, value: "365"},
		{name: "user bytes minimum", key: diagnostics.KeyMaxBytesPerUser, value: "10485760"},
		{name: "user bytes maximum", key: diagnostics.KeyMaxBytesPerUser, value: "10737418240"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: tc.initial}
			rec := updateDiagnosticsSetting(t, &AdminHandler{SettingsRepo: settings}, tc.key, tc.value)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			if settings.values[tc.key] != tc.value {
				t.Fatalf("stored value = %q, want %q", settings.values[tc.key], tc.value)
			}
		})
	}
}

func TestDiagnosticsNumericSettingsRejectOutOfRangeValues(t *testing.T) {
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
			rec := updateDiagnosticsSetting(t, &AdminHandler{SettingsRepo: settings}, tc.key, tc.value)
			body := rec.Body.String()
			assertDiagnosticsError(t, rec, http.StatusBadRequest, "bad_request")
			if _, stored := settings.values[tc.key]; stored {
				t.Fatalf("invalid value was stored: %#v", settings.values)
			}
			if !strings.Contains(body, tc.key) {
				t.Fatalf("error body = %s, want clear error naming %s", body, tc.key)
			}
		})
	}
}

type diagnosticsPart struct {
	name        string
	contentType string
	body        []byte
}

func validDiagnosticsParts() []diagnosticsPart {
	return []diagnosticsPart{
		{name: "manifest", contentType: "application/json", body: []byte(`{"ok":true}`)},
		{name: "bundle", contentType: "application/gzip", body: []byte("bundle")},
	}
}

func newDiagnosticsUploadRequest(t *testing.T, parts []diagnosticsPart, claims *auth.Claims) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, part := range parts {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="`+part.name+`"`)
		header.Set("Content-Type", part.contentType)
		w, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("create multipart part: %v", err)
		}
		if _, err := w.Write(part.body); err != nil {
			t.Fatalf("write multipart part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/diagnostics/reports", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer token")
	if claims != nil {
		req = req.WithContext(apimw.SetClaims(req.Context(), claims))
	}
	return req
}

func accessClaims() *auth.Claims {
	return &auth.Claims{
		UserID:    42,
		TokenType: auth.TokenTypeAccess,
	}
}

func assertDiagnosticsError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, status, rec.Body.String())
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error != code {
		t.Fatalf("error = %q, want %q", resp.Error, code)
	}
}

func captureDiagnosticsLogs(handler *DiagnosticsHandler) *bytes.Buffer {
	var logs bytes.Buffer
	handler.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	return &logs
}

func assertDiagnosticsRejectionLog(t *testing.T, logs *bytes.Buffer, reason string, userID int) {
	t.Helper()
	line := logs.String()
	for _, want := range []string{
		`"msg":"diagnostic report rejected"`,
		`"component":"diagnostics"`,
		`"result":"rejected"`,
		`"reason":"` + reason + `"`,
		`"user_id":` + strconv.Itoa(userID),
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("rejection log = %s, want %s", line, want)
		}
	}
}

func updateDiagnosticsSetting(t *testing.T, handler *AdminHandler, key, value string) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	router.Put("/admin/settings/{key}", handler.HandleUpdateSetting)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPut,
		"/admin/settings/"+key,
		strings.NewReader(`{"value":"`+value+`"}`),
	))
	return rec
}

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

func adminDiagnosticsRouter(handler *DiagnosticsHandler) chi.Router {
	router := chi.NewRouter()
	RegisterAdminDiagnosticsRoutes(router, handler)
	return router
}

func adminDiagnosticsRequest(method, target string, claims *auth.Claims) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	if claims != nil {
		req = req.WithContext(apimw.SetClaims(req.Context(), claims))
	}
	return req
}

func adminClaims() *auth.Claims {
	return &auth.Claims{
		UserID:    7,
		Role:      "admin",
		TokenType: auth.TokenTypeAccess,
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

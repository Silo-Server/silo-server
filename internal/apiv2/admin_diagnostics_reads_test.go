package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

type diagnosticReadsStub struct {
	calls   int
	filters diagnostics.ListFilters
	result  diagnostics.ListResult
	report  *diagnostics.Report
	err     error
}

func (s *diagnosticReadsStub) ListAdminDiagnosticReports(_ context.Context, f diagnostics.ListFilters) (diagnostics.ListResult, error) {
	s.calls++
	s.filters = f
	return s.result, s.err
}
func (s *diagnosticReadsStub) GetAdminDiagnosticReport(_ context.Context, _ string) (*diagnostics.Report, error) {
	s.calls++
	return s.report, s.err
}
func TestAdminDiagnosticReadsCursorAndProjection(t *testing.T) {
	report := diagnostics.Report{ID: "report-1", UserID: 7, ShortID: "SILO-ABCDEF123456", State: diagnostics.StateReady, CapturedAt: time.Date(2026, 9, 1, 1, 2, 3, 123456789, time.UTC), ReceivedAt: time.Date(2026, 9, 2, 1, 2, 3, 987654321, time.UTC), ReportType: "manual", Platform: "ios", BlobBucket: new("private-bucket"), BlobKey: new("private-key"), Manifest: json.RawMessage(`{"schema_version":1,"extension":"retained"}`)}
	s := &diagnosticReadsStub{result: diagnostics.ListResult{Reports: []diagnostics.Report{report}, NextCursor: "store-position"}, report: &report}
	deps := requestDeps(fixtureRequests())
	deps.AdminDiagnosticReads = s
	h := NewHandler(deps)
	path := Prefix + "/admin/diagnostics/reports"
	if r := do(t, h, http.MethodGet, path, "", nil); r.Code != 401 || s.calls != 0 {
		t.Fatal(r.Code, s.calls)
	}
	r := do(t, h, http.MethodGet, path+"?user_id=7&limit=1&short_id=abcdef123456", "", actingRequestAdmin)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	for _, want := range []string{`"user_id":"7"`, `"captured_at":"2026-09-01T01:02:03.123Z"`, `"playback_session_ids":[]`} {
		if !strings.Contains(r.Body.String(), want) {
			t.Fatal(r.Body.String())
		}
	}
	for _, secret := range []string{"private-bucket", "private-key", "manifest", "retained"} {
		if strings.Contains(r.Body.String(), secret) {
			t.Fatal(r.Body.String())
		}
	}
	var page struct {
		Page struct {
			NextCursor string `json:"next_cursor"`
		}
	}
	if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Page.NextCursor == "" || page.Page.NextCursor == "store-position" || s.filters.UserID == nil || *s.filters.UserID != 7 || s.filters.ShortID != "SILO-ABCDEF123456" {
		t.Fatal(page, s.filters)
	}
	cursor := url.QueryEscape(page.Page.NextCursor)
	r = do(t, h, http.MethodGet, path+"?user_id=7&limit=1&short_id=SILO-ABCDEF123456&cursor="+cursor, "", actingRequestAdmin)
	if r.Code != 200 || s.filters.Cursor != "store-position" {
		t.Fatal(r.Code, r.Body.String(), s.filters)
	}
	calls := s.calls
	for _, query := range []string{"user_id=8&limit=1&short_id=abcdef123456", "user_id=7&limit=2&short_id=abcdef123456", "user_id=7&limit=1&short_id=abcdef123456&platform=ios"} {
		r = do(t, h, http.MethodGet, path+"?"+query+"&cursor="+cursor, "", actingRequestAdmin)
		if r.Code != 400 || s.calls != calls {
			t.Fatal(r.Code, r.Body.String(), s.calls)
		}
	}
	r = do(t, h, http.MethodGet, path+"/report-1", "", actingRequestAdmin)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"extension":"retained"`) || strings.Contains(r.Body.String(), "private-key") {
		t.Fatal(r.Code, r.Body.String())
	}
}
func TestAdminDiagnosticReadsValidationAndMissing(t *testing.T) {
	s := &diagnosticReadsStub{}
	deps := requestDeps(fixtureRequests())
	deps.AdminDiagnosticReads = s
	h := NewHandler(deps)
	path := Prefix + "/admin/diagnostics/reports"
	for _, q := range []string{"limit=201", "user_id=0", "short_id=invalid", "from=2026-09-03T00:00:00Z&to=2026-09-01T00:00:00Z", "from=bad"} {
		r := do(t, h, http.MethodGet, path+"?"+q, "", actingRequestAdmin)
		if r.Code != 422 || s.calls != 0 {
			t.Fatal(q, r.Code, r.Body.String(), s.calls)
		}
	}
	r := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"items":[]`) {
		t.Fatal(r.Code, r.Body.String())
	}
	r = do(t, h, http.MethodGet, path+"/missing", "", actingRequestAdmin)
	if r.Code != 404 {
		t.Fatal(r.Code, r.Body.String())
	}
	s.err = diagnostics.ErrReportStoreUnavailable
	r = do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if r.Code != 503 {
		t.Fatal(r.Code, r.Body.String())
	}
}

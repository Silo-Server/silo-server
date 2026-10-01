package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

// unusedReportStore fails the test if any query runs: a malformed report id has
// to be answered before Postgres sees it, otherwise the `uuid` id column raises
// SQLSTATE 22P02 and the admin route answers 500 instead of 404.
type unusedReportStore struct{ t *testing.T }

func (s unusedReportStore) fail(op string) {
	s.t.Helper()
	s.t.Fatalf("report store %s was called for a malformed id", op)
}

func (s unusedReportStore) InsertReceiving(context.Context, diagnostics.InsertReceivingInput) (diagnostics.InsertReceivingResult, error) {
	s.fail("InsertReceiving")
	return diagnostics.InsertReceivingResult{}, nil
}
func (s unusedReportStore) MarkReady(context.Context, string, diagnostics.BlobInfo) error {
	s.fail("MarkReady")
	return nil
}
func (s unusedReportStore) MarkFailed(context.Context, string) error {
	s.fail("MarkFailed")
	return nil
}
func (s unusedReportStore) GetByID(context.Context, string) (*diagnostics.Report, error) {
	s.fail("GetByID")
	return nil, nil
}
func (s unusedReportStore) ListForAdmin(context.Context, diagnostics.ListFilters) (diagnostics.ListResult, error) {
	s.fail("ListForAdmin")
	return diagnostics.ListResult{}, nil
}
func (s unusedReportStore) DeleteByID(context.Context, string) (*diagnostics.Report, error) {
	s.fail("DeleteByID")
	return nil, nil
}
func (s unusedReportStore) RetentionCandidates(context.Context, time.Time, int64) ([]diagnostics.Report, error) {
	s.fail("RetentionCandidates")
	return nil, nil
}
func (s unusedReportStore) StaleReceiving(context.Context, time.Duration) ([]diagnostics.Report, error) {
	s.fail("StaleReceiving")
	return nil, nil
}

func TestAdminDiagnosticsRoutesRejectMalformedReportID(t *testing.T) {
	service := diagnostics.NewService(unusedReportStore{t: t}, nil, nil, slog.Default())
	router := chi.NewRouter()
	RegisterAdminDiagnosticsRoutes(router, NewDiagnosticsHandler(service))

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"read", http.MethodGet, "/diagnostics/reports/not-a-uuid"},
		{"download", http.MethodGet, "/diagnostics/reports/not-a-uuid/download"},
		{"delete", http.MethodDelete, "/diagnostics/reports/not-a-uuid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s status = %d, want 404 (body %s)", tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
	}
}

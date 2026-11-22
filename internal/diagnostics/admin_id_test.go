package diagnostics

import (
	"context"
	"errors"
	"testing"
)

// A report id that is not a UUID cannot address a client_diagnostic_reports row
// (the id column is `uuid`), so the service has to answer "not found" without
// letting Postgres raise SQLSTATE 22P02 and turn the miss into a 500.
func TestGetReportRejectsMalformedIDWithoutQuerying(t *testing.T) {
	for _, id := range []string{"not-a-uuid", "11111111-1111-1111-1111-11111111111"} {
		repo := &fakeDiagnosticReportStore{getReport: &Report{ID: "11111111-1111-1111-1111-111111111111"}}
		svc := newTestDiagnosticsService(repo, &fakeDiagnosticObjectStore{bucket: "private"})

		report, err := svc.GetReport(context.Background(), id)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetReport(%q) error = %v, want ErrNotFound", id, err)
		}
		if report != nil {
			t.Fatalf("GetReport(%q) returned a report; the store should not have been consulted", id)
		}
	}
}

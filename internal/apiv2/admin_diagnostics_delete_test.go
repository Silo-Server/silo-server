package apiv2

import (
	"context"
	"net/http"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

type diagnosticDeleteStub struct {
	calls int
	user  int
	id    string
	err   error
}

func (s *diagnosticDeleteStub) DeleteAdminDiagnosticReport(ctx context.Context, id string) error {
	s.calls++
	s.user = apimw.GetUserID(ctx)
	s.id = id
	return s.err
}
func TestAdminDiagnosticDelete(t *testing.T) {
	s := &diagnosticDeleteStub{}
	deps := requestDeps(fixtureRequests())
	deps.AdminDiagnosticDeletes = s
	h := NewHandler(deps)
	path := Prefix + "/admin/diagnostics/reports/report-1"
	if r := do(t, h, http.MethodDelete, path, "", nil); r.Code != 401 || s.calls != 0 {
		t.Fatal(r.Code, s.calls)
	}
	for range 2 {
		r := do(t, h, http.MethodDelete, path, "", actingRequestAdmin)
		if r.Code != 204 || r.Body.Len() != 0 || s.user == 0 || s.id != "report-1" {
			t.Fatal(r.Code, r.Body.String(), s)
		}
	}
	if s.calls != 2 {
		t.Fatal(s.calls)
	}
	s.err = diagnostics.ErrReportStoreUnavailable
	r := do(t, h, http.MethodDelete, path, "", actingRequestAdmin)
	if r.Code != 503 {
		t.Fatal(r.Code, r.Body.String())
	}
}

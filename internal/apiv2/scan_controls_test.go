package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type scanControlFixture struct {
	calls   int
	id      *int
	path    string
	failure error
}

func (*scanControlFixture) ScanControlAvailable() bool { return true }
func (f *scanControlFixture) StartLibraryScan(ctx context.Context, id *int, path string) (handlers.ScanAdmission, error) {
	f.calls++
	f.id = id
	f.path = path
	if claimsFrom(ctx) == nil {
		return handlers.ScanAdmission{}, &handlers.APIError{Status: 500, Message: "lost authority"}
	}
	return handlers.ScanAdmission{Status: "accepted", Mode: "file", LibraryID: 42}, f.failure
}
func (f *scanControlFixture) CancelLibraryScans(ctx context.Context, id int) (handlers.ScanCancellation, error) {
	f.calls++
	f.id = new(id)
	return handlers.ScanCancellation{Canceled: 2, LibraryID: id}, f.failure
}
func TestScanControlsGateAndProjection(t *testing.T) {
	f := new(scanControlFixture)
	deps := parityDeps(false)
	deps.ScanControls = f
	h := NewHandler(deps)
	admin := with(bearer(adminToken), "X-Profile-Id", "p-primary")
	for _, path := range []string{Prefix + "/scan", Prefix + "/scan/cancel"} {
		requireProblem(t, do(t, h, "POST", path, `{"library_id":"42"}`, bearer(memberToken)), TypePermissionDenied)
	}
	if f.calls != 0 {
		t.Fatal("non-admin scan dispatch")
	}
	rec := do(t, h, "POST", Prefix+"/scan", `{"library_id":"42","path":"/synthetic/item.mkv"}`, admin)
	if rec.Code != 202 || f.calls != 1 || f.id == nil || *f.id != 42 || f.path != "/synthetic/item.mkv" || !strings.Contains(rec.Body.String(), `"library_id":"42"`) {
		t.Fatal(rec.Code, rec.Body.String(), f)
	}
	rec = do(t, h, "POST", Prefix+"/scan/cancel", `{"library_id":"42"}`, admin)
	if rec.Code != 200 || f.calls != 2 || !strings.Contains(rec.Body.String(), `"cancelled":2`) { //nolint:misspell // Assert the established wire spelling.
		t.Fatal(rec.Code, rec.Body.String(), f)
	}
	requireProblem(t, do(t, h, "POST", Prefix+"/scan/cancel", `{"library_id":"0"}`, admin), TypeValidationFailed)
	f.failure = &handlers.APIError{Status: 503, Message: "Scanner not available"}
	requireProblem(t, do(t, h, "POST", Prefix+"/scan", `{"path":"/synthetic"}`, admin), TypeDependencyUnavailable)
	deps.ScanControls = nil
	requireProblem(t, do(t, NewHandler(deps), "POST", Prefix+"/scan", `{}`, admin), TypeDependencyUnavailable)
}
func TestScanControlsDiscoveryAndDemoExemption(t *testing.T) {
	deps := parityDeps(false)
	admin := with(bearer(adminToken), "X-Profile-Id", "p-primary")
	rec := do(t, NewHandler(deps), http.MethodGet, Prefix+"/scan/capabilities", "", admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), StateNotConfigured) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	f := new(scanControlFixture)
	deps = parityDeps(true)
	deps.ScanControls = f
	h := NewHandler(deps)
	for _, path := range []string{Prefix + "/scan", Prefix + "/scan/cancel"} {
		problem := requireProblem(t, do(t, h, http.MethodPost, path, `{"library_id":"42"}`, bearer(memberToken)), TypePermissionDenied)
		if problem.Detail != "This action is not available in demo mode." {
			t.Fatalf("%s denied outside the demo guard: %+v", path, problem)
		}
	}
	if f.calls != 0 {
		t.Fatal("demo scan dispatched")
	}
	for _, tc := range []struct {
		path   string
		status int
	}{{Prefix + "/scan", http.StatusAccepted}, {Prefix + "/scan/cancel", http.StatusOK}} {
		rec := do(t, h, http.MethodPost, tc.path, `{"library_id":"42"}`, admin)
		if rec.Code != tc.status {
			t.Fatalf("acting admin demo exemption: %s returned %d: %s", tc.path, rec.Code, rec.Body.String())
		}
	}
	if f.calls != 2 {
		t.Fatalf("acting admin demo dispatches = %d, want 2", f.calls)
	}
}

func TestScanStartDisabledLibraryConflictIsDeclared(t *testing.T) {
	deps := parityDeps(false)
	fixture := &scanControlFixture{failure: &handlers.APIError{Status: http.StatusConflict, Message: "Library is disabled"}}
	deps.ScanControls = fixture
	admin := with(bearer(adminToken), "X-Profile-Id", "p-primary")
	response := do(t, NewHandler(deps), http.MethodPost, Prefix+"/scan", `{"library_id":"42"}`, admin)
	requireProblem(t, response, TypeConflict)
	if response.Code != http.StatusConflict || fixture.calls != 1 {
		t.Fatal(response.Code, fixture.calls)
	}
	generated, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]struct {
			Post struct{ Responses map[string]json.RawMessage }
		}
	}
	if err := json.Unmarshal(generated, &document); err != nil {
		t.Fatal(err)
	}
	if _, exists := document.Paths[Prefix+"/scan"].Post.Responses["409"]; !exists {
		t.Fatal("disabled-library409 missing from scan contract")
	}
}

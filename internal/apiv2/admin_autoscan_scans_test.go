package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type fakeAdminAutoscanScans struct {
	filters []autoscan.ScanListFilter
	err     error
}

func (f *fakeAdminAutoscanScans) ReadAdminAutoscanScans(_ context.Context, in autoscan.ScanListFilter) ([]autoscan.ScanWithEvent, int, error) {
	f.filters = append(f.filters, in)
	if in.Offset > 0 {
		return nil, 1, f.err
	}
	return []autoscan.ScanWithEvent{{ScanRunSummary: autoscan.ScanRunSummary{ID: "scan-a", MediaFolderID: 7, Mode: "file", Status: "completed", RequestedAt: new(time.Date(2026, 9, 1, 0, 0, 0, 123456789, time.UTC))}, AutoscanEventID: new(int64(9007199254740993))}}, 1, f.err
}
func TestAdminAutoscanScans(t *testing.T) {
	f := new(fakeAdminAutoscanScans)
	deps := pilotDeps(nil, nil)
	deps.AdminAutoscanScans = f
	h := NewHandler(deps)
	path := Prefix + "/admin/autoscan/scans"
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, "GET", path+"?status=invalid", "", bearer(adminToken)), TypeValidationFailed)
	if len(f.filters) != 0 {
		t.Fatal("invalid read dispatched")
	}
	rec := do(t, h, "GET", path+"?limit=1&q=needle", "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"autoscan_event_id":"9007199254740993"`) || !strings.Contains(rec.Body.String(), "2026-09-01T00:00:00.123Z") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var page AdminAutoscanScansPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Page == nil || !page.Page.HasMore || len(page.Items) != 1 || page.Total != 1 {
		t.Fatal(page)
	}
	next := path + "?limit=1&q=needle&cursor=" + url.QueryEscape(page.Page.NextCursor)
	rec = do(t, h, "GET", next, "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) || f.filters[1].Offset != 1 {
		t.Fatal(rec.Code, rec.Body.String(), f.filters)
	}
	before := len(f.filters)
	requireProblem(t, do(t, h, "GET", strings.Replace(next, "q=needle", "q=other", 1), "", bearer(adminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, "GET", strings.Replace(next, "limit=1", "limit=2", 1), "", bearer(adminToken)), TypeInvalidCursor)
	if len(f.filters) != before {
		t.Fatal("bad cursor read source")
	}
	f.err = errors.New("private-store")
	rec = do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "private-store") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	deps.AdminAutoscanScans = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

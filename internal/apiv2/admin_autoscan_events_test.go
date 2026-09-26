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

type fakeAdminAutoscanEvents struct {
	filters []autoscan.EventListFilter
	err     error
}

func (f *fakeAdminAutoscanEvents) ReadAdminAutoscanEvents(_ context.Context, in autoscan.EventListFilter) ([]autoscan.EventWithRuns, int, error) {
	f.filters = append(f.filters, in)
	if in.Offset > 0 {
		return nil, 1, f.err
	}
	return []autoscan.EventWithRuns{{Event: autoscan.Event{ID: 9007199254740993, StartedAt: time.Date(2026, 9, 1, 0, 0, 0, 123456789, time.UTC), CompletedAt: time.Date(2026, 9, 1, 0, 0, 1, 123456789, time.UTC), Status: autoscan.EventStatusSuccess}, Runs: []autoscan.ScanRunSummary{{ID: "run-a", MediaFolderID: 7, Mode: "file", Status: "completed"}}}}, 1, f.err
}
func TestAdminAutoscanEvents(t *testing.T) {
	f := new(fakeAdminAutoscanEvents)
	deps := pilotDeps(nil, nil)
	deps.AdminAutoscanEvents = f
	h := NewHandler(deps)
	path := Prefix + "/admin/autoscan/events"
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, "GET", path+"?status=invalid", "", bearer(adminToken)), TypeValidationFailed)
	if len(f.filters) != 0 {
		t.Fatal("invalid read dispatched")
	}
	rec := do(t, h, "GET", path+"?limit=1&source_id=source-a&q=needle", "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"9007199254740993"`) || !strings.Contains(rec.Body.String(), "2026-09-01T00:00:00.123Z") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var page AdminAutoscanEventsPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Page == nil || !page.Page.HasMore || len(page.Items) != 1 || page.Total != 1 {
		t.Fatal(page)
	}
	next := path + "?limit=1&source_id=source-a&q=needle&cursor=" + url.QueryEscape(page.Page.NextCursor)
	rec = do(t, h, "GET", next, "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) || f.filters[1].Offset != 1 {
		t.Fatal(rec.Code, rec.Body.String(), f.filters)
	}
	before := len(f.filters)
	requireProblem(t, do(t, h, "GET", strings.Replace(next, "source_id=source-a", "source_id=source-b", 1), "", bearer(adminToken)), TypeInvalidCursor)
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
	deps.AdminAutoscanEvents = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

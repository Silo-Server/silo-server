package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/activitylog"
)

type fakeAuditLogs struct {
	calls  []activitylog.ListOptions
	result activitylog.ListResult
	err    error
}

func (f *fakeAuditLogs) List(_ context.Context, opts activitylog.ListOptions) (activitylog.ListResult, error) {
	f.calls = append(f.calls, opts)
	return f.result, f.err
}
func TestAdminAuditLogsCursorAndProjection(t *testing.T) {
	stamp := time.Date(2026, 9, 6, 1, 2, 3, 123456789, time.FixedZone("offset", 3600))
	f := &fakeAuditLogs{result: activitylog.ListResult{Entries: []activitylog.AuditEntry{{ID: 9007199254740993, Timestamp: stamp, Method: "GET", Path: "/synthetic", StatusCode: 200, UserID: new(7), ImpersonatorUserID: new(9)}}, NextCursor: "source-preserving-nanosecond-position"}}
	deps := pilotDeps(nil, nil)
	deps.CursorSecret = []byte("synthetic-audit-log-cursor-secret")
	deps.AdminAuditLogs = f
	h := NewHandler(deps)
	path := Prefix + "/admin/logs/audit"
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if len(f.calls) != 0 {
		t.Fatal("unauthorized source read")
	}
	query := "?limit=1&method=get&status_code=200&path_prefix=/synthetic&client_ip=192.0.2.7/24&user_id=7&from=2026-09-06T00:00:00.123456789Z"
	rec := do(t, h, "GET", path+query, "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var page struct {
		Items []AdminAuditLog `json:"items"`
		Page  struct {
			NextCursor string `json:"next_cursor"`
			HasMore    bool   `json:"has_more"`
		} `json:"page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "9007199254740993" || page.Items[0].Timestamp.String() != "2026-09-06T00:02:03.123Z" || *page.Items[0].UserID != "7" || *page.Items[0].ImpersonatorUserID != "9" || !page.Page.HasMore {
		t.Fatal(rec.Body.String())
	}
	if f.calls[0].Method != "GET" || f.calls[0].ClientIP != "192.0.2.7/24" || *f.calls[0].StatusCode != 200 || f.calls[0].From.Nanosecond() != 123456789 {
		t.Fatal(f.calls[0])
	}
	token := url.QueryEscape(page.Page.NextCursor)
	f.result = activitylog.ListResult{}
	rec = do(t, h, "GET", path+query+"&cursor="+token, "", bearer(adminToken))
	if rec.Code != 200 || f.calls[1].Cursor != "source-preserving-nanosecond-position" || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, changed := range []string{strings.Replace(query, "limit=1", "limit=2", 1), strings.Replace(query, "/synthetic", "/other", 1), strings.Replace(query, "status_code=200", "status_code=500", 1), strings.Replace(query, "user_id=7", "user_id=8", 1)} {
		requireProblem(t, do(t, h, "GET", path+changed+"&cursor="+token, "", bearer(adminToken)), TypeInvalidCursor)
	}
	if len(f.calls) != 2 {
		t.Fatal("changed cursor scope reached source")
	}
	requireProblem(t, do(t, h, "GET", path+"?limit=201", "", bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, "GET", path+"?from=not-a-time", "", bearer(adminToken)), TypeValidationFailed)
	for _, suffix := range []string{"?client_ip=invalid", "?client_ip=fe80::1%25eth0", "?status_code=oops", "?user_id=0"} {
		requireProblem(t, do(t, h, "GET", path+suffix, "", bearer(adminToken)), TypeValidationFailed)
	}
	f.result = activitylog.ListResult{Entries: []activitylog.AuditEntry{{ID: 0, Timestamp: stamp}}}
	requireProblem(t, do(t, h, "GET", path, "", bearer(adminToken)), TypeInternalError)
	f.err = errors.New("synthetic private database detail")
	rec = do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "synthetic private") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	deps.AdminAuditLogs = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/opslog"
)

type fakeOperationalLogs struct {
	calls  []opslog.ListOptions
	result opslog.ListResult
	err    error
}

func (f *fakeOperationalLogs) List(_ context.Context, opts opslog.ListOptions) (opslog.ListResult, error) {
	f.calls = append(f.calls, opts)
	return f.result, f.err
}
func TestAdminOperationalLogsCursorAndProjection(t *testing.T) {
	stamp := time.Date(2026, 9, 6, 1, 2, 3, 123456789, time.FixedZone("offset", 3600))
	f := &fakeOperationalLogs{result: opslog.ListResult{Entries: []opslog.EntryRow{{ID: 9007199254740993, Timestamp: stamp, Level: "error", Message: "Synthetic message", UserID: new(7), Attrs: map[string]any{"nested": map[string]any{"attempt": 2}}}}, NextCursor: "source-preserving-nanosecond-position"}}
	deps := pilotDeps(nil, nil)
	deps.CursorSecret = []byte("synthetic-operational-log-cursor-secret")
	deps.AdminOperationalLogs = f
	h := NewHandler(deps)
	path := Prefix + "/admin/logs/app"
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if len(f.calls) != 0 {
		t.Fatal("unauthorized source read")
	}
	query := "?limit=1&level=warn,ERROR,error&component=ffmpeg&user_id=7&from=2026-09-06T00:00:00.123456789Z"
	rec := do(t, h, "GET", path+query, "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var page struct {
		Items []AdminOperationalLog `json:"items"`
		Page  struct {
			NextCursor string `json:"next_cursor"`
			HasMore    bool   `json:"has_more"`
		} `json:"page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "9007199254740993" || page.Items[0].Timestamp.String() != "2026-09-06T00:02:03.123Z" || *page.Items[0].UserID != "7" || !page.Page.HasMore {
		t.Fatal(rec.Body.String())
	}
	if strings.Join(f.calls[0].Levels, ",") != "error,warn" || f.calls[0].From.Nanosecond() != 123456789 {
		t.Fatal(f.calls[0])
	}
	token := url.QueryEscape(page.Page.NextCursor)
	f.result = opslog.ListResult{}
	rec = do(t, h, "GET", path+query+"&cursor="+token, "", bearer(adminToken))
	if rec.Code != 200 || f.calls[1].Cursor != "source-preserving-nanosecond-position" || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, changed := range []string{strings.Replace(query, "limit=1", "limit=2", 1), strings.Replace(query, "ffmpeg", "scanner", 1)} {
		requireProblem(t, do(t, h, "GET", path+changed+"&cursor="+token, "", bearer(adminToken)), TypeInvalidCursor)
	}
	if len(f.calls) != 2 {
		t.Fatal("changed cursor scope reached source")
	}
	requireProblem(t, do(t, h, "GET", path+"?limit=201", "", bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, "GET", path+"?from=not-a-time", "", bearer(adminToken)), TypeValidationFailed)
	f.err = errors.New("synthetic private database detail")
	rec = do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "synthetic private") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	deps.AdminOperationalLogs = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

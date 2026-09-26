package apiv2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
)

type adminServerStatusStub struct {
	status handlers.AdminServerStatusSnapshot
	calls  int
	user   int
}

func (s *adminServerStatusStub) ReadAdminServerStatus(ctx context.Context) handlers.AdminServerStatusSnapshot {
	s.calls++
	s.user = apimw.GetUserID(ctx)
	return s.status
}
func TestAdminServerStatusRead(t *testing.T) {
	at := time.Date(2026, 9, 1, 1, 2, 3, 987654321, time.FixedZone("test", 3600))
	s := &adminServerStatusStub{status: handlers.AdminServerStatusSnapshot{StartedAt: at, RestartRequired: true, RestartRequiredAt: &at, RestartRequiredReasons: []string{"setting:server.listen"}, RestartMarkCount: 2, RestartRequested: true, RestartRequestedAt: &at}}
	s.status.Health.Postgres.Configured = true
	s.status.Health.Postgres.OK = new(false)
	s.status.Health.Postgres.LatencyMS = new(2.25)
	s.status.Health.Errors24h = 4
	s.status.ArtworkStorage.StatusKnown = true
	deps := requestDeps(fixtureRequests())
	deps.AdminServerStatus = s
	h := NewHandler(deps)
	path := Prefix + "/admin/server/status"
	r := do(t, h, "GET", path, "", nil)
	if r.Code != 401 || s.calls != 0 {
		t.Fatal(r.Code, s.calls)
	}
	r = do(t, h, "GET", path, "", actingRequestAdmin)
	if r.Code != 200 || s.calls != 1 || s.user == 0 {
		t.Fatal(r.Code, r.Body.String(), s.calls, s.user)
	}
	for _, want := range []string{`"started_at":"2026-09-01T00:02:03.987Z"`, `"restart_mark_count":2`, `"restart_required_reasons":["setting:server.listen"]`, `"postgres":{"configured":true,"ok":false,"latency_ms":2.25}`, `"redis":{"configured":false}`, `"status_known":true`} {
		if !strings.Contains(r.Body.String(), want) {
			t.Fatal(r.Body.String(), want)
		}
	}
	if s.status.StartedAt != at {
		t.Fatal("mutated tracker snapshot")
	}
	missing := NewHandler(requestDeps(fixtureRequests()))
	r = do(t, missing, "GET", path, "", actingRequestAdmin)
	if r.Code != 503 {
		t.Fatal(r.Code, r.Body.String())
	}
}

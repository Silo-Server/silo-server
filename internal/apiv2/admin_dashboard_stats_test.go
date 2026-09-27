package apiv2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminDashboardStats struct {
	calls   int
	refresh bool
	stats   handlers.AdminStats
}

func (f *fakeAdminDashboardStats) ReadAdminStats(_ context.Context, refresh bool) (handlers.AdminStats, error) {
	f.calls++
	f.refresh = refresh
	return f.stats, nil
}
func TestAdminDashboardStats(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeAdminDashboardStats)
	deps.AdminDashboardStats = f
	h := NewHandler(deps)
	path := Prefix + "/admin/stats"
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("unauthorized service call")
	}
	rec := do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"watch_providers":[]`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	at := time.Date(2026, 9, 1, 2, 0, 0, 123456789, time.FixedZone("offset", 7200))
	f.stats = handlers.AdminStats{TotalUsers: 3, TotalStorageBytes: 123456789, WatchProviders: []handlers.WatchProviderStats{{Provider: "removed", Registered: false, ConnectedProfiles: 2, LastSyncCompletedAt: &at, SyncRuns24h: 4}}}
	rec = do(t, h, "GET", path+"?refresh=true", "", bearer(adminToken))
	for _, want := range []string{`"total_users":3`, `"registered":false`, `"connected_profiles":2`, `2026-09-01T00:00:00.123Z`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatal(want, rec.Code, rec.Body.String())
		}
	}
	if !f.refresh || !f.stats.WatchProviders[0].LastSyncCompletedAt.Equal(at) {
		t.Fatal("refresh forwarding/cache preservation")
	}
	requireProblem(t, do(t, h, "GET", path+"?refresh=garbage", "", bearer(adminToken)), TypeValidationFailed)
	deps.AdminDashboardStats = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

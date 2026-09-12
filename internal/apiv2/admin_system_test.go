package apiv2

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodemetrics"
)

type fakeAdminResourceSampler struct {
	snapshot nodemetrics.Snapshot
	reads    int
}

func (f *fakeAdminResourceSampler) Snapshot() nodemetrics.Snapshot { f.reads++; return f.snapshot }

func TestAdminSystemResourcesAuthorizationAndProjection(t *testing.T) {
	f := &fakeAdminResourceSampler{snapshot: nodemetrics.Snapshot{Available: true, SampledAt: time.Date(2026, 9, 5, 1, 2, 3, 123456789, time.FixedZone("test", 3600)), System: &nodemetrics.SystemStats{CPUPct: 17}, GPU: []nodemetrics.GPUStats{{Device: "gpu-test", Source: "fdinfo", VideoBusyPct: new(0)}}}}
	deps := requestDeps(fixtureRequests())
	deps.AdminResourceSampler = f
	h := NewHandler(deps)
	path := Prefix + "/admin/system/resources"
	denied := do(t, h, http.MethodGet, path, "", nil)
	if denied.Code != http.StatusUnauthorized || f.reads != 0 {
		t.Fatalf("unauthorized sample: %d reads %d", denied.Code, f.reads)
	}
	read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if read.Code != http.StatusOK {
		t.Fatal(read.Code, read.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(read.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["sampled_at"] != "2026-09-05T00:02:03.123Z" {
		t.Fatal(body)
	}
	if !strings.Contains(read.Body.String(), `"disks":[]`) || !strings.Contains(read.Body.String(), `"video_busy_pct":0`) || strings.Contains(read.Body.String(), `"total_busy_pct"`) {
		t.Fatal(read.Body.String())
	}
	if f.reads != 1 || f.snapshot.System.Disks != nil {
		t.Fatal("read mutated snapshot or sampled more than once")
	}
}
func TestAdminSystemResourcesUnsampled(t *testing.T) {
	h := NewHandler(requestDeps(fixtureRequests()))
	read := do(t, h, http.MethodGet, Prefix+"/admin/system/resources", "", actingRequestAdmin)
	if read.Code != 200 || !strings.Contains(read.Body.String(), `"available":false`) || !strings.Contains(read.Body.String(), `"gpu":[]`) || strings.Contains(read.Body.String(), `"sampled_at"`) {
		t.Fatal(read.Code, read.Body.String())
	}
}
func TestAdminSystemBuild(t *testing.T) {
	h := NewHandler(requestDeps(fixtureRequests()))
	read := do(t, h, http.MethodGet, Prefix+"/admin/system/build", "", actingRequestAdmin)
	if read.Code != 200 || !strings.Contains(read.Body.String(), `"build_number":`) {
		t.Fatal(read.Code, read.Body.String())
	}
	for _, raw := range []string{"", "2026-09-05T01:02:03.123456Z", "invalid"} {
		value, err := buildInstant(raw)
		switch raw {
		case "":
			if value != nil || err != nil {
				t.Fatal(value, err)
			}
		case "invalid":
			if err == nil {
				t.Fatal("invalid timestamp accepted")
			}
		default:
			if err != nil || value.String() != "2026-09-05T01:02:03.123Z" {
				t.Fatal(value, err)
			}
		}
	}
}

func TestAdminResourceCapabilitiesAndScope(t *testing.T) {
	f := &fakeAdminResourceSampler{snapshot: nodemetrics.Snapshot{Available: true}}
	deps := requestDeps(fixtureRequests())
	deps.AdminResourceSampler = f
	h := NewHandler(deps)
	for _, path := range []string{Prefix + "/admin/system/resources", Prefix + "/admin/system/resources/capabilities"} {
		requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
		requireProblem(t, do(t, h, "GET", path, "", with(bearer(adminToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	}
	if f.reads != 0 {
		t.Fatal("denied callers read sampler")
	}
	path := Prefix + "/admin/system/resources/capabilities"
	rec := do(t, h, "GET", path, "", actingRequestAdmin)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") == "" || !strings.Contains(rec.Body.String(), `"allowed":true`) || !strings.Contains(rec.Body.String(), `"instance_attribution":true`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if f.reads != 0 {
		t.Fatal("capability discovery sampled resource health")
	}
	cached := do(t, h, "GET", path, "", with(actingRequestAdmin, "If-None-Match", rec.Header().Get("ETag")))
	if cached.Code != http.StatusNotModified {
		t.Fatal(cached.Code, cached.Body.String())
	}
}

func TestAdminSystemAttributionPreservesUnknownAndSource(t *testing.T) {
	f := &fakeAdminResourceSampler{snapshot: nodemetrics.Snapshot{Available: true, SampledAt: time.Now(), Attribution: &nodemetrics.ResourceAttribution{
		InstanceID: "instance-test", SampleIntervalSeconds: 5,
		CPU:     nodemetrics.ResourceSource{Scope: "cgroup", Source: "cgroup_v2", Available: false},
		Process: &nodemetrics.ProcessStats{ResidentBytes: new(uint64(4096)), HeapLiveBytes: new(uint64(0))},
	}}}
	deps := requestDeps(fixtureRequests())
	deps.AdminResourceSampler = f
	read := do(t, NewHandler(deps), "GET", Prefix+"/admin/system/resources", "", actingRequestAdmin)
	if read.Code != http.StatusOK {
		t.Fatal(read.Code, read.Body.String())
	}
	for _, want := range []string{`"stale":false`, `"instance_id":"instance-test"`, `"resident_bytes":4096`, `"heap_live_bytes":0`, `"scope":"cgroup"`} {
		if !strings.Contains(read.Body.String(), want) {
			t.Fatal("missing", want, read.Body.String())
		}
	}
	if strings.Contains(read.Body.String(), `"read_bytes"`) {
		t.Fatal("fabricated missing reading", read.Body.String())
	}
}

func TestAdminResourceScopedKeysStayDenied(t *testing.T) {
	deps := requestDeps(fixtureRequests())
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{}, fakeSessions{}, fakeAPIKeys{keys: map[string]*models.APIKey{
		"sa_users": {ID: 1, UserID: 2, Scopes: []string{auth.ScopeAdminUsers}},
		"sa_admin": {ID: 2, UserID: 2},
	}}, fakeUsers{users: map[int]*models.User{2: {ID: 2, Role: "admin", Enabled: true}}})
	h := NewHandler(deps)
	for _, path := range []string{Prefix + "/admin/system/resources", Prefix + "/admin/system/resources/capabilities"} {
		requireProblem(t, do(t, h, "GET", path, "", bearer("sa_users")), TypePermissionDenied)
		if rec := do(t, h, "GET", path, "", bearer("sa_admin")); rec.Code != http.StatusOK {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
}

package apiv2

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type fakeAdminHardware struct{ calls int }

func (f *fakeAdminHardware) ReadHardwareAcceleration(http.ResponseWriter, *http.Request) handlers.HWAccelInventory {
	f.calls++
	return handlers.HWAccelInventory{HWAccelInfo: playback.HWAccelInfo{Resolved: "qsv", Source: "local", DetectedBackends: []playback.DetectedBackend{{Backend: "qsv", Verified: false, Skipped: true, Reason: "No accessible candidate"}}}, Nodes: []handlers.NodeHWAccel{{NodeURL: "https://user:secret@example.invalid/node?token=secret", Error: "private upstream secret"}}}
}
func TestAdminHardwareAccelerationRead(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeAdminHardware)
	deps.AdminHardwareAcceleration = f
	h := NewHandler(deps)
	path := Prefix + "/admin/system/hw-accel"
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("refusal invoked inventory")
	}
	rec := do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"resolved":"qsv"`, `"verified":false`, `"skipped":true`, `"source":"local"`, `"render_devices":[]`, `"render_device_details":[]`, `"node_url":"https://example.invalid/node"`, `"error":"Node capability probe failed."`} {
		if !strings.Contains(body, want) {
			t.Fatal(want, body)
		}
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "private upstream") {
		t.Fatal(body)
	}
	deps.AdminHardwareAcceleration = nil
	rec = do(t, NewHandler(deps), "GET", path, "", bearer(adminToken))
	if rec.Code != 503 {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

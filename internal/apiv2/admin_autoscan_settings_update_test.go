package apiv2

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type fakeAutoscanSettingsWrite struct {
	calls int
	err   error
	state string
}

func (f *fakeAutoscanSettingsWrite) UpdateAdminAutoscanSettings(_ context.Context, in autoscan.Settings) (handlers.AdminAutoscanSettingsWriteView, error) {
	f.calls++
	return handlers.AdminAutoscanSettingsWriteView{Settings: in, RescheduleState: f.state}, f.err
}
func TestAdminAutoscanSettingsUpdate(t *testing.T) {
	f := &fakeAutoscanSettingsWrite{state: "failed"}
	deps := pilotDeps(nil, nil)
	deps.AdminAutoscanSettingsUpdates = f
	h := NewHandler(deps)
	path := Prefix + "/admin/autoscan/settings"
	body := `{"enabled":true,"default_poll_interval_seconds":120,"debounce_seconds":0}`
	requireProblem(t, do(t, h, "PUT", path, body, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "PUT", path, body, bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, "PUT", path, `{"enabled":true,"default_poll_interval_seconds":0,"debounce_seconds":0}`, bearer(adminToken)), TypeValidationFailed)
	if f.calls != 0 {
		t.Fatal("invalid request dispatched")
	}
	rec := do(t, h, "PUT", path, body, bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"reschedule_state":"failed"`) || !strings.Contains(rec.Body.String(), `"enabled":true`) || f.calls != 1 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, test := range []struct {
		err    error
		status int
	}{{handlers.ErrAdminAutoscanSettingsWriteInvalid, 422}, {handlers.ErrAdminAutoscanSettingsWriteUnavailable, 503}, {errors.New("private-store"), 500}} {
		f.err = test.err
		rec = do(t, h, "PUT", path, body, bearer(adminToken))
		if rec.Code != test.status || strings.Contains(rec.Body.String(), "private-store") {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	deps.AdminAutoscanSettingsUpdates = nil
	requireProblem(t, do(t, NewHandler(deps), "PUT", path, body, bearer(adminToken)), TypeDependencyUnavailable)
}

package apiv2

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeOrderedPush struct {
	cmd   notifications.AndroidPushCommand
	calls int
	err   error
}

func (*fakeOrderedPush) OrderedAndroidPushAvailable() bool { return true }
func (f *fakeOrderedPush) ApplyAndroidPush(_ context.Context, cmd notifications.AndroidPushCommand) (notifications.AndroidPushReceipt, error) {
	f.cmd = cmd
	f.calls++
	return notifications.AndroidPushReceipt{Generation: cmd.Generation, RegistrationID: "registration", ServerDeviceID: "server-device", PushMode: notifications.PushModePrivatePush, Removed: cmd.Remove}, f.err
}
func TestOrderedAndroidPushTransport(t *testing.T) {
	f := new(fakeOrderedPush)
	deps := pilotDeps(nil, nil)
	deps.OrderedAndroidPush = f
	h := NewHandler(deps)
	headers := profileOwner()
	headers["X-Push-Installation-Key"] = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	headers["X-Push-Generation"] = "9007199254740993"
	body := `{"device_id":"install","platform":"android","token":"` + strings.Repeat("a", 64) + `","push_mode":"private_push"}`
	rec := do(t, h, http.MethodPost, Prefix+"/notifications/push/devices", body, headers)
	if rec.Code != 200 {
		t.Fatalf("register=%d %s", rec.Code, rec.Body.String())
	}
	var receipt AndroidPushRegistrationReceipt
	if err := json.Unmarshal(rec.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Generation != "9007199254740993" || f.cmd.UserID != 1 || f.cmd.ProfileID != "p-owner" || f.cmd.DeviceID != "install" || f.cmd.Remove {
		t.Fatalf("receipt=%+v", receipt)
	}
	if strings.Contains(rec.Body.String(), headers["X-Push-Installation-Key"]) || strings.Contains(rec.Body.String(), strings.Repeat("a", 64)) {
		t.Fatal("credential leaked")
	}
	rec = do(t, h, http.MethodDelete, Prefix+"/notifications/push/devices/install", "", headers)
	if rec.Code != 204 || rec.Body.Len() != 0 || !f.cmd.Remove || f.cmd.Token != "" {
		t.Fatalf("remove=%d %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{"", "0", "01", "+1", "9223372036854775808"} {
		headers["X-Push-Generation"] = bad
		calls := f.calls
		rec = do(t, h, http.MethodPost, Prefix+"/notifications/push/devices", body, headers)
		if rec.Code != 422 || f.calls != calls {
			t.Fatalf("generation %q=%d calls=%d", bad, rec.Code, f.calls)
		}
	}
	headers["X-Push-Generation"] = "1"
	for _, tc := range []struct {
		err    error
		status int
	}{{notifications.ErrPushGenerationConflict, 409}, {notifications.ErrPushInstallationProof, 403}, {notifications.ErrPushDeviceUnavailable, 503}} {
		f.err = tc.err
		rec = do(t, h, http.MethodPost, Prefix+"/notifications/push/devices", body, headers)
		if rec.Code != tc.status {
			t.Fatalf("mapped error=%d want=%d", rec.Code, tc.status)
		}
	}
}

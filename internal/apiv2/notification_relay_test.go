package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeNotificationRelay struct {
	registers, clears int
	url               string
	err               error
}

func (f *fakeNotificationRelay) RegisterNotificationRelay(_ context.Context, url string) (handlers.NotificationRelayView, error) {
	f.registers++
	f.url = url
	return handlers.NotificationRelayView{RelayURL: "https://relay.example.test", DeploymentID: "deployment-1", KeyPrefix: "prefix", ExpiresAt: fixedTime()}, f.err
}
func (f *fakeNotificationRelay) ClearNotificationRelay(context.Context) error {
	f.clears++
	return f.err
}

func TestNotificationRelayTransport(t *testing.T) {
	fake := new(fakeNotificationRelay)
	deps := pilotDeps(nil, nil)
	deps.NotificationRelay = fake
	h := NewHandler(deps)
	path := Prefix + "/admin/notifications/push/relay"
	body := `{"relay_url":"https://relay.example.test"}`
	requireProblem(t, do(t, h, "POST", path+"/register", body, profileOwner()), TypePermissionDenied)
	if fake.registers != 0 {
		t.Fatal("non-admin registered relay")
	}
	rec := do(t, h, "POST", path+"/register", body, bearer(adminToken))
	if rec.Code != 200 || fake.registers != 1 || fake.url != "https://relay.example.test" || !strings.Contains(rec.Body.String(), `"api_key_configured":true`) || strings.Contains(rec.Body.String(), `"api_key":`) {
		t.Fatalf("%d %s; %+v", rec.Code, rec.Body.String(), fake)
	}
	for _, invalid := range []string{`{"relay_url":null}`, `{"relay_url":"x","api_key":"secret"}`} {
		requireProblem(t, do(t, h, "POST", path+"/register", invalid, bearer(adminToken)), TypeValidationFailed)
	}
	if fake.registers != 1 {
		t.Fatal("invalid registration dispatched")
	}
	for _, status := range []int{429, 503, 502} {
		fake.err = &handlers.APIError{Status: status, Code: "relay_error", Message: "Relay refused request", RetryAfter: 7}
		rec = do(t, h, "POST", path+"/register", body, bearer(adminToken))
		want := status
		if status == 502 {
			want = 500
		}
		if rec.Code != want || rec.Header().Get("Retry-After") != "7" {
			t.Fatalf("%d %s %v", rec.Code, rec.Body.String(), rec.Header())
		}
	}
	fake.err = nil
	rec = do(t, h, "DELETE", path, "", bearer(adminToken))
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || fake.clears != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func notificationRelayFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "notification_relay_register", operationID: "registerAdminNotificationRelay", method: "POST", path: Prefix + "/admin/notifications/push/relay/register", body: `{"relay_url":"https://relay.example.test"}`, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/NotificationRelayRegistration", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "Synthetic relay registration returns credential metadata without its reusable secret."},
		{name: "notification_relay_clear", operationID: "clearAdminNotificationRelay", method: "DELETE", path: Prefix + "/admin/notifications/push/relay", headers: bearer(adminToken), status: 204, assertHeaders: []string{"Cache-Control"}, scenario: "An explicit administrator command clears the local relay credential."},
	}
}

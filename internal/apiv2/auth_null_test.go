package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
)

type authNullSessionSpy struct {
	SessionService
	calls int
}

func (s *authNullSessionSpy) Login(ctx context.Context, in handlers.LoginInput) (handlers.TokenPairView, error) {
	s.calls++
	return s.SessionService.Login(ctx, in)
}
func (s *authNullSessionSpy) SetupInitialUser(ctx context.Context, in handlers.RegistrationInput) (handlers.TokenPairView, error) {
	s.calls++
	return s.SessionService.SetupInitialUser(ctx, in)
}
func (s *authNullSessionSpy) Signup(ctx context.Context, in handlers.RegistrationInput) (handlers.TokenPairView, error) {
	s.calls++
	return s.SessionService.Signup(ctx, in)
}

type authNullDeviceSpy struct {
	DeviceLoginService
	calls int
}

func (s *authNullDeviceSpy) StartDeviceLogin(ctx context.Context, in auth.DeviceLoginStartInput) (*auth.DeviceLoginStartResult, error) {
	s.calls++
	return s.DeviceLoginService.StartDeviceLogin(ctx, in)
}
func (s *authNullDeviceSpy) ApproveDeviceLogin(ctx context.Context, in auth.DeviceLoginLookupInput, id int) (handlers.DeviceLoginDecision, error) {
	s.calls++
	return handlers.DeviceLoginDecision{Status: "approved"}, nil
}
func (s *authNullDeviceSpy) ApproveDeviceHandoff(ctx context.Context, in auth.DeviceLoginLookupInput, id int, profile string) (handlers.DeviceLoginDecision, error) {
	s.calls++
	return handlers.DeviceLoginDecision{Status: "approved"}, nil
}
func (s *authNullDeviceSpy) DenyDeviceLogin(ctx context.Context, in auth.DeviceLoginLookupInput, id int) (handlers.DeviceLoginDecision, error) {
	s.calls++
	return handlers.DeviceLoginDecision{Status: "denied"}, nil
}

func TestOrdinaryAuthRejectsOptionalNullBeforeEffects(t *testing.T) {
	for _, tc := range []struct {
		path, body string
		fields     []string
		status     int
	}{
		{"login", `{"username":"laura","password":"pw"}`, []string{"provider"}, 200},
		{"setup", `{"username":"alice","email":"alice@example.test","password":"password123"}`, []string{"create_default_profile", "default_profile_name"}, 201},
		{"signup", `{"username":"alice","email":"alice@example.test","password":"password123","invite_code":"WELCOME-2026"}`, []string{"create_default_profile", "default_profile_name"}, 201},
		{"device/start", `{}`, []string{"device_name", "device_platform", "client_purpose", "temporary"}, 201},
		{"device/approve", `{"token":"br-pending","code":"ABCD-1234"}`, []string{"token", "code"}, 200},
		{"device/approve-handoff", `{"token":"br-remote","code":"ABCD-1234"}`, []string{"token", "code"}, 200},
		{"device/deny", `{"token":"br-pending","code":"ABCD-1234"}`, []string{"token", "code"}, 200},
	} {
		t.Run(tc.path, func(t *testing.T) {
			deps := pilotDeps(nil, nil)
			sessions := &authNullSessionSpy{SessionService: deps.Sessions}
			devices := &authNullDeviceSpy{DeviceLoginService: deps.Devices}
			deps.Sessions = sessions
			deps.Devices = devices
			h := newTestHandler(t, deps)
			headers := with(bearer(memberToken), "X-Profile-Id", "p-owner")
			for _, field := range tc.fields {
				t.Run(field, func(t *testing.T) {
					var body map[string]any
					if err := json.Unmarshal([]byte(tc.body), &body); err != nil {
						t.Fatal(err)
					}
					body[field] = nil
					raw, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					before := sessions.calls + devices.calls
					rec := do(t, h, http.MethodPost, Prefix+"/auth/"+tc.path, string(raw), headers)
					if sessions.calls+devices.calls != before {
						t.Fatalf("null %s reached service: status=%d body=%s", field, rec.Code, rec.Body.String())
					}
					p := requireProblem(t, rec, TypeValidationFailed)
					if len(p.Errors) != 1 || p.Errors[0].Location != "body."+field {
						t.Fatalf("errors=%+v", p.Errors)
					}
				})
			}
			before := sessions.calls + devices.calls
			rec := do(t, h, http.MethodPost, Prefix+"/auth/"+tc.path, tc.body, headers)
			if rec.Code != tc.status || sessions.calls+devices.calls != before+1 {
				t.Fatalf("valid omission/control: status=%d calls=%d body=%s", rec.Code, sessions.calls+devices.calls-before, rec.Body.String())
			}
		})
	}
}

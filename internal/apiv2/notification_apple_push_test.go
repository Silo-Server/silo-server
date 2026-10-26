package apiv2

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/golang-jwt/jwt/v5"
)

type fakeOrderedApple struct {
	calls    int
	cmd      notifications.ApplePushCommand
	identity evt.SocketIdentity
	err      error
}

func (*fakeOrderedApple) OrderedApplePushAvailable() bool { return true }
func (f *fakeOrderedApple) RegisterApplePush(_ context.Context, cmd notifications.ApplePushCommand, identity evt.SocketIdentity) (notifications.ApplePushReceipt, string, time.Time, error) {
	f.calls++
	f.cmd = cmd
	f.identity = identity
	return notifications.ApplePushReceipt{Generation: cmd.Generation, RegistrationID: "registration", ServerDeviceID: "server-device", Enabled: true, PushMode: cmd.PushMode}, "synthetic-display", fixedTime().Add(30 * 24 * time.Hour), f.err
}
func TestOrderedApplePushTransport(t *testing.T) {
	f := new(fakeOrderedApple)
	deps := pilotDeps(nil, nil)
	deps.OrderedApplePush = f
	deps.ServerIdentity = fakeServerIdentity{id: fixtureServerID}
	claims := &auth.Claims{UserID: 1, Role: "user", SessionID: "session", TokenType: auth.TokenTypeAccess, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{map[string]*auth.Claims{memberToken: claims}}, fakeSessions{map[string]string{"session": "user"}}, nil, nil)
	h := NewHandler(deps)
	headers := profileOwner()
	headers["X-Push-Installation-Key"] = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	headers["X-Push-Generation"] = "9007199254740993"
	body := `{"device_id":"install","apns_token":"` + strings.Repeat("a", 64) + `","apns_environment":"production","apns_topic":"org.siloserver.silo","push_mode":"private_push"}`
	path := Prefix + "/devices/push/apple"
	r := do(t, h, http.MethodPost, path, body, headers)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"generation":"9007199254740993"`) || !strings.Contains(r.Body.String(), `"display_token":"synthetic-display"`) || !strings.Contains(r.Body.String(), `"server_id":"`+fixtureServerID+`"`) || f.identity.SessionID != "session" || f.cmd.ProfileID != "p-owner" || f.cmd.UserID != 1 {
		t.Fatalf("%d %s %+v", r.Code, r.Body.String(), f)
	}
	if r.Header().Get("Cache-Control") != "no-store" || strings.Contains(r.Body.String(), headers["X-Push-Installation-Key"]) || strings.Contains(r.Body.String(), strings.Repeat("a", 64)) {
		t.Fatal("credential caching/leak")
	}
	for _, bad := range []string{"", "0", "01", "+1", "9223372036854775808"} {
		headers["X-Push-Generation"] = bad
		before := f.calls
		r = do(t, h, http.MethodPost, path, body, headers)
		if r.Code != 422 || f.calls != before {
			t.Fatalf("generation=%q status=%d", bad, r.Code)
		}
	}
	headers["X-Push-Generation"] = "1"
	before := f.calls
	r = do(t, h, http.MethodPost, path, body, bearer(memberToken))
	if r.Code != 422 || f.calls != before {
		t.Fatal("profileless", r.Code)
	}
	claims.ExpiresAt = nil
	r = do(t, h, http.MethodPost, path, body, headers)
	if r.Code != 403 || f.calls != before {
		t.Fatal("unbounded", r.Code)
	}
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Minute))
	claims.TokenType = auth.TokenTypeApplePushDisplay
	r = do(t, h, http.MethodPost, path, body, headers)
	if r.Code != 401 || f.calls != before {
		t.Fatal("display credential accepted", r.Code)
	}
	claims.TokenType = auth.TokenTypeAccess
	claims.SessionID = ""
	r = do(t, h, http.MethodPost, path, body, headers)
	if r.Code != 401 || f.calls != before {
		t.Fatal("sessionless access accepted", r.Code)
	}
	claims.SessionID = "session"

	for _, tc := range []struct {
		err    error
		status int
	}{{notifications.ErrPushGenerationConflict, 409}, {notifications.ErrPushInstallationProof, 403}, {notifications.ErrPushDeviceInvalid, 422}, {notifications.ErrPushDeviceUnavailable, 503}} {
		f.err = tc.err
		r = do(t, h, http.MethodPost, path, body, headers)
		if r.Code != tc.status {
			t.Fatalf("error=%v status=%d", tc.err, r.Code)
		}
	}
	deps.OrderedApplePush = nil
	h = NewHandler(deps)
	r = do(t, h, http.MethodGet, path+"/capabilities", "", headers)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"registration_available":false`) {
		t.Fatal("absent capability", r.Code)
	}
	r = do(t, h, http.MethodPost, path, body, headers)
	if r.Code != 503 {
		t.Fatal("absent registration", r.Code)
	}
}

type fakeApplePushRetirement struct {
	calls int
	cmd   notifications.ApplePushRetirement
	err   error
}

func (*fakeApplePushRetirement) ApplePushRetirementAvailable() bool { return true }
func (f *fakeApplePushRetirement) RetireApplePush(_ context.Context, cmd notifications.ApplePushRetirement) (notifications.ApplePushReceipt, error) {
	f.calls++
	f.cmd = cmd
	return notifications.ApplePushReceipt{Generation: cmd.Generation, Removed: true}, f.err
}

// The app calls retirement after it has discarded its login session, so the
// route must answer on the installation key alone.
func TestApplePushRetirementTransportNeedsNoSession(t *testing.T) {
	f := new(fakeApplePushRetirement)
	deps := pilotDeps(nil, nil)
	deps.ApplePushRetirement = f
	h := NewHandler(deps)
	key := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	headers := map[string]string{"X-Push-Installation-Key": key, "X-Push-Generation": "9007199254740993"}
	path, body := Prefix+"/devices/push/apple/retire", `{"device_id":"install"}`
	r := do(t, h, http.MethodPost, path, body, headers)
	if r.Code != 200 || strings.TrimSpace(r.Body.String()) != `{"generation":"9007199254740993","removed":true}` || f.cmd != (notifications.ApplePushRetirement{DeviceID: "install", InstallationKey: key, Generation: 9007199254740993}) {
		t.Fatalf("%d %s %+v", r.Code, r.Body.String(), f.cmd)
	}
	for _, bad := range []string{"0", "01", "9223372036854775808"} {
		before := f.calls
		r = do(t, h, http.MethodPost, path, body, with(headers, "X-Push-Generation", bad))
		if r.Code != 422 || f.calls != before {
			t.Fatalf("generation=%q status=%d", bad, r.Code)
		}
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{notifications.ErrPushGenerationConflict, 409}, {notifications.ErrPushInstallationProof, 403}, {notifications.ErrPushDeviceInvalid, 422}, {notifications.ErrPushDeviceUnavailable, 503}} {
		f.err = tc.err
		if r = do(t, h, http.MethodPost, path, body, headers); r.Code != tc.status {
			t.Fatalf("error=%v status=%d", tc.err, r.Code)
		}
	}
	deps.ApplePushRetirement = nil
	if r = do(t, NewHandler(deps), http.MethodPost, path, body, headers); r.Code != 503 {
		t.Fatal("absent retirement", r.Code)
	}
}

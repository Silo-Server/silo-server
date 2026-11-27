package apiv2

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/demomode"
)

// TestDemoLogin: demoLogin answers a token pair that is never cached, and a
// server that is not a public demo answers the capability problem matching
// getDemoLoginCapability, so a client can tell "not offered here" from a
// failure. Catches demo sign-in being refused with a status a client treats
// as an outage, or a token pair a shared cache could keep.
func TestDemoLogin(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.Sessions = &fakeSessionService{}
	rec := do(t, newTestHandler(t, deps), http.MethodPost, Prefix+"/auth/demo", `{}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	var body TokenPair
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AccessToken != "demo-acc" || body.RefreshToken != "demo-ref" || body.User.Username != "demo" {
		t.Fatalf("unexpected body: %+v", body)
	}

	for _, tc := range []struct {
		name string
		err  error
		want ProblemType
	}{
		{name: "demo mode off", err: auth.ErrDemoLoginDisabled, want: TypeCapabilityDisabled},
		{name: "no demo account", err: auth.ErrDemoLoginNotConfigured, want: TypeCapabilityNotConfigured},
		{name: "store failure", err: errors.New("db down"), want: TypeInternalError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := pilotDeps(nil, nil)
			deps.Sessions = &fakeSessionService{demoLoginErr: tc.err}
			requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, Prefix+"/auth/demo", `{}`, nil), tc.want)
		})
	}
}

// TestDemoLoginRateLimitBucket: demo sign-ins spend their own per-address
// budget, so demo visitors behind one address do not lock out password
// sign-ins from it.
func TestDemoLoginRateLimitBucket(t *testing.T) {
	var seen []string
	deps := pilotDeps(nil, nil)
	deps.Sessions = &fakeSessionService{}
	deps.BucketRateLimit = func(bucket string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = append(seen, bucket)
				next.ServeHTTP(w, r)
			})
		}
	}
	do(t, newTestHandler(t, deps), http.MethodPost, Prefix+"/auth/demo", `{}`, nil)
	if len(seen) != 1 || seen[0] != demoLoginBucket {
		t.Fatalf("buckets = %v, want [%s]", seen, demoLoginBucket)
	}
}

// TestDemoLoginCapability: only a public demo, with demo mode on and a demo
// account named, says available; every other server says it does not offer
// demo sign-in, as does one whose settings can't be read. Catches a client
// offering "Try the demo" on an ordinary server.
func TestDemoLoginCapability(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings apimw.DemoSettingsReader
		want     string
	}{
		{name: "ordinary server", settings: brandingSettings{}, want: StateDisabled},
		{name: "demo account named without demo mode", settings: brandingSettings{demomode.UsernameKey: "demo"}, want: StateDisabled},
		{name: "demo mode without an account", settings: brandingSettings{demomode.SettingKey: demomode.OnValue, demomode.UsernameKey: " "}, want: StateNotConfigured},
		{name: "public demo", settings: brandingSettings{demomode.SettingKey: demomode.OnValue, demomode.UsernameKey: "demo"}, want: StateAvailable},
		{name: "unreadable settings", settings: failingSettings{}, want: StateDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := pilotDeps(nil, nil)
			deps.DemoSettings = tc.settings
			rec := do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/auth/demo/capability", "", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d %s, want 200", rec.Code, rec.Body.String())
			}
			var body DemoLoginCapability
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.State != tc.want {
				t.Fatalf("state = %q, want %q", body.State, tc.want)
			}
		})
	}
}

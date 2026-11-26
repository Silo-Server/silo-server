package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/demomode"
)

const demoRefusalDetail = "This action is not available in demo mode."

// isDemoRefusal reports whether rec is the demo-mode refusal rather than any
// other answer (a handler's own refusal, a missing service, success).
func isDemoRefusal(rec *httptest.ResponseRecorder) bool {
	if rec.Code != http.StatusForbidden {
		return false
	}
	var p problemDoc
	return json.Unmarshal(rec.Body.Bytes(), &p) == nil && p.Detail == demoRefusalDetail
}

// TestDemoModeAccountChanges: demo visitors share one non-administrator
// account, so none of them may change how it signs in or end another
// visitor's session. Catches an account-changing operation that loses its
// demo restriction, an administrator who becomes restricted, and demo mode
// leaking into a server that has it off.
func TestDemoModeAccountChanges(t *testing.T) {
	const password = `{"current_password":"pw","new_password":"margin fossil quench"}`
	restricted := []struct {
		name, method, path, body string
	}{
		{"change password", http.MethodPost, "/account/password", password},
		{"unlink identity", http.MethodDelete, "/account/identities/4", ""},
		{"link directory identity", http.MethodPost, "/account/identities/link-credentials", `{"installation_id":"3","password":"pw","username":"u","directory_password":"d"}`},
		{"link network identity", http.MethodPost, "/account/identities/link-network", `{"installation_id":"3","password":"pw"}`},
		{"link ticket", http.MethodPost, "/account/identities/link-ticket", `{"installation_id":"3","password":"pw"}`},
		{"link start", http.MethodPost, "/account/identities/link-start", `{"link_ticket":"t","next":"/settings"}`},
		{"link complete", http.MethodPost, "/account/identities/link-complete", `{"code":"c","code_verifier":"v"}`},
		// s9 is another session of the member's account (memberToken is s1).
		{"end another session", http.MethodDelete, "/auth/sessions/s9", ""},
	}
	headers := map[string]map[string]string{
		"member": with(bearer(memberToken), "X-Profile-Id", "p-primary"),
		"admin":  with(bearer(adminToken), "X-Profile-Id", "p-primary"),
	}
	handler := func(demo bool) http.Handler {
		deps := externalSignInDeps(&fakeExternalSignIn{allowUnlink: true})
		deps.Sessions = &fakeSessionService{}
		deps.DemoSettings = fakeSettings{demo: demo}
		return newTestHandler(t, deps)
	}
	on, off := handler(true), handler(false)
	for _, tc := range restricted {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, on, tc.method, Prefix+tc.path, tc.body, headers["member"])
			if !isDemoRefusal(rec) {
				t.Fatalf("demo visitor: %d %s", rec.Code, rec.Body.String())
			}
			requireProblem(t, rec, TypePermissionDenied)
			if rec := do(t, on, tc.method, Prefix+tc.path, tc.body, headers["admin"]); isDemoRefusal(rec) {
				t.Fatalf("administrator refused in demo mode: %s", rec.Body.String())
			}
			if rec := do(t, off, tc.method, Prefix+tc.path, tc.body, headers["member"]); isDemoRefusal(rec) {
				t.Fatalf("refused with demo mode off: %s", rec.Body.String())
			}
		})
	}

	// What a visitor may still do: end the session making the request, sign
	// out, and approve a device sign-in (how a visitor tries the TV app).
	allowed := []struct {
		name, method, path, body string
	}{
		{"end own session", http.MethodDelete, "/auth/sessions/s1", ""},
		{"sign out", http.MethodPost, "/auth/logout", ""},
		{"approve device sign-in", http.MethodPost, "/auth/device/approve", `{"user_code":"ABCD-EFGH"}`},
	}
	for _, tc := range allowed {
		t.Run(tc.name, func(t *testing.T) {
			if rec := do(t, on, tc.method, Prefix+tc.path, tc.body, headers["member"]); isDemoRefusal(rec) {
				t.Fatalf("demo visitor refused: %s", rec.Body.String())
			}
		})
	}
	sessions := &fakeSessionService{}
	deps := pilotDeps(nil, nil)
	deps.Sessions, deps.DemoSettings = sessions, fakeSettings{demo: true}
	h := newTestHandler(t, deps)
	if rec := do(t, h, http.MethodDelete, Prefix+"/auth/sessions/s1", "", bearer(memberToken)); rec.Code != http.StatusNoContent || len(sessions.revoked) != 1 {
		t.Fatalf("own session: %d %s %v", rec.Code, rec.Body.String(), sessions.revoked)
	}
	// Whose session it is comes first: an unknown session, or another
	// account's (s2), is 404 in demo mode exactly as with it off.
	for _, id := range []string{"unknown", "s2"} {
		requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/auth/sessions/"+id, "", bearer(memberToken)), TypeNotFound)
	}
}

// failingSettings cannot read any setting, as when the store is unreachable.
type failingSettings struct{}

func (failingSettings) Get(context.Context, string) (string, error) {
	return "", errors.New("settings store unreachable")
}

// flakySettings answers demo.enabled with value once, then fails every read.
type flakySettings struct {
	value string
	reads int
}

func (f *flakySettings) Get(context.Context, string) (string, error) {
	f.reads++
	if f.reads > 1 {
		return "", errors.New("settings store unreachable")
	}
	return f.value, nil
}

// TestDemoModeRemembersLastRead: through the shared demomode.Setting, a
// failed read answers the last value read. Catches a database hiccup turning
// an ordinary server's restricted operations into 500s and its capabilities
// to no, or a demo server's demo refusal into a 500.
func TestDemoModeRemembersLastRead(t *testing.T) {
	member := with(bearer(memberToken), "X-Profile-Id", "p-primary")
	const password = `{"current_password":"pw","new_password":"margin fossil quench"}`
	handler := func(first string) http.Handler {
		deps := externalSignInDeps(&fakeExternalSignIn{allowUnlink: true})
		deps.Accounts = passwordChanger{deps.Accounts.(fakeAccounts)}
		deps.Sessions = &fakeSessionService{}
		setting := demomode.New(&flakySettings{value: first})
		if _, err := setting.Get(context.Background(), demomode.SettingKey); err != nil {
			t.Fatal(err)
		}
		deps.DemoSettings = setting
		return newTestHandler(t, deps)
	}
	off := handler("false")
	// Past the gate: the fake's own primary-profile rule answers, not a 500
	// or the demo refusal.
	if rec := do(t, off, http.MethodPost, Prefix+"/account/password", password, member); rec.Code == http.StatusInternalServerError || isDemoRefusal(rec) {
		t.Fatalf("not a demo, read failed: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(t, off, http.MethodGet, Prefix+"/auth/sessions/capabilities", "", member)
	var body map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &body) != nil || body["revoke_other_sessions"] != true {
		t.Fatalf("not a demo, read failed: capability %s", rec.Body.String())
	}
	rec = do(t, handler(demomode.OnValue), http.MethodPost, Prefix+"/account/password", password, member)
	if !isDemoRefusal(rec) {
		t.Fatalf("demo, read failed: %d %s", rec.Code, rec.Body.String())
	}
}

// TestDemoModeUnreadableSetting: when the demo setting can't be read, a
// non-administrator's restricted change is refused as an internal failure
// (the server may not be a demo, so not with the demo refusal), the caller's
// own session can still end, and capabilities answer no. Catches the check
// failing open, which on a demo server would let every visitor through.
func TestDemoModeUnreadableSetting(t *testing.T) {
	deps := externalSignInDeps(&fakeExternalSignIn{allowUnlink: true})
	deps.Accounts = passwordChanger{deps.Accounts.(fakeAccounts)}
	sessions := &fakeSessionService{}
	deps.Sessions = sessions
	deps.DemoSettings = failingSettings{}
	h := newTestHandler(t, deps)
	member := with(bearer(memberToken), "X-Profile-Id", "p-primary")
	for _, tc := range []struct{ name, method, path, body string }{
		{"gate", http.MethodPost, "/account/password", `{"current_password":"pw","new_password":"margin fossil quench"}`},
		{"another session", http.MethodDelete, "/auth/sessions/s9", ""},
	} {
		rec := do(t, h, tc.method, Prefix+tc.path, tc.body, member)
		if isDemoRefusal(rec) {
			t.Fatalf("%s: answered as a demo server: %s", tc.name, rec.Body.String())
		}
		requireProblem(t, rec, TypeInternalError)
	}
	if rec := do(t, h, http.MethodDelete, Prefix+"/auth/sessions/s1", "", member); rec.Code != http.StatusNoContent || !slices.Equal(sessions.revoked, []string{"s1"}) {
		t.Fatalf("own session: %d %s %v", rec.Code, rec.Body.String(), sessions.revoked)
	}
	for _, tc := range []struct{ path, name string }{
		{"/account/password/capability", "allowed"},
		{"/auth/sessions/capabilities", "revoke_other_sessions"},
		{"/account/identities", "can_link"},
	} {
		rec := do(t, h, http.MethodGet, Prefix+tc.path, "", member)
		var body map[string]any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body[tc.name] != false {
			t.Fatalf("%s %s: %d %s", tc.path, tc.name, rec.Code, rec.Body.String())
		}
	}
	// An administrator is never restricted, so the setting is not needed.
	if rec := do(t, h, http.MethodPost, Prefix+"/account/password", `{"current_password":"pw","new_password":"margin fossil quench"}`, with(bearer(adminToken), "X-Profile-Id", "p-primary")); rec.Code != http.StatusNoContent {
		t.Fatalf("administrator: %d %s", rec.Code, rec.Body.String())
	}
}

// passwordChanger grants every caller the password change, so only demo
// mode can take it away.
type passwordChanger struct{ fakeAccounts }

func (a passwordChanger) AccountPasswordCapability(ctx context.Context, claims *auth.Claims, profileID string) (handlers.AccountPasswordCapabilityView, error) {
	view, err := a.fakeAccounts.AccountPasswordCapability(ctx, claims, profileID)
	view.ChangePassword = true
	return view, err
}

// TestDemoModeAccountCapabilities: the answers a client reads before offering
// these actions say no to a demo visitor, so the web hides them instead of
// offering a form that fails. Catches a capability that keeps saying yes, and
// one that demo mode changes for an administrator.
func TestDemoModeAccountCapabilities(t *testing.T) {
	member := with(bearer(memberToken), "X-Profile-Id", "p-primary")
	admin := with(bearer(adminToken), "X-Profile-Id", "p-primary")
	handler := func(demo bool) http.Handler {
		deps := externalSignInDeps(&fakeExternalSignIn{allowUnlink: true})
		deps.Accounts = passwordChanger{deps.Accounts.(fakeAccounts)}
		deps.Sessions = &fakeSessionService{}
		deps.DemoSettings = fakeSettings{demo: demo}
		return newTestHandler(t, deps)
	}
	field := func(h http.Handler, path string, hdr map[string]string, name string) bool {
		t.Helper()
		rec := do(t, h, http.MethodGet, Prefix+path, "", hdr)
		var body map[string]any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		v, ok := body[name].(bool)
		if !ok {
			t.Fatalf("%s: no boolean %s in %s", path, name, rec.Body.String())
		}
		return v
	}
	on, off := handler(true), handler(false)
	for _, tc := range []struct{ path, name string }{
		{"/account/password/capability", "allowed"},
		{"/auth/sessions/capabilities", "revoke_other_sessions"},
		{"/account/identities", "can_link"},
		{"/account/identities", "can_unlink"},
	} {
		t.Run(tc.path+" "+tc.name, func(t *testing.T) {
			if field(on, tc.path, member, tc.name) {
				t.Fatal("demo visitor offered the action")
			}
			if !field(off, tc.path, member, tc.name) {
				t.Fatal("not offered with demo mode off")
			}
			if field(on, tc.path, admin, tc.name) != field(off, tc.path, admin, tc.name) {
				t.Fatal("demo mode changed the administrator's answer")
			}
		})
	}
}

// demoAllowedAccountOperations lists the mutations in the account domains
// that demo mode deliberately leaves open to a visitor, with the reason.
// Every other such mutation is DemoRestricted.
var demoAllowedAccountOperations = map[string]string{
	"approveDeviceLogin":   "signing a TV in to the demo account is how a visitor tries the TV app",
	"approveDeviceHandoff": "the same device sign-in, for the visitor's profile",
	"denyDeviceLogin":      "refuses a pending device sign-in whose code the caller holds; changes no access",
	"endImpersonation":     "only an administrator's View as user session can use it, and it returns to that administrator",
	"logout":               "ends only the session making the request",
	"deleteSession":        "the handler refuses any session but the caller's own in demo mode",
	"createPluginLaunch":   "issues a plugin cookie for the caller's own session; changes nothing",
	"verifyProfilePIN":     "a wrong guess spends the profile's attempt budget, but visitors can't set a PIN, so the demo has none to lock",
}

// demoAccountDomains are the tags of operations that change the account, how
// it signs in, or who holds a session on it.
var demoAccountDomains = map[string]bool{"account": true, "api-keys": true, "auth": true, "device-login": true, "notifications": true, "profiles": true}

// TestDemoModeCoversAccountOperations fails when an account-changing
// operation a non-administrator can call is added without deciding its
// demo-mode behavior: restrict it, or list it above with the reason.
func TestDemoModeCoversAccountOperations(t *testing.T) {
	doc := generatedDocument(t)
	seen := map[string]bool{}
	for _, item := range doc["paths"].(map[string]any) {
		for method, raw := range item.(map[string]any) {
			op, ok := raw.(map[string]any)
			if !ok || !isMutatingMethod(strings.ToUpper(method)) {
				continue
			}
			class, _ := op[extClass].(string)
			tags, _ := op["tags"].([]any)
			if Class(class) == ClassPublic || Class(class) == ClassActingAdmin || len(tags) != 1 || !demoAccountDomains[tags[0].(string)] {
				continue
			}
			id, _ := op["operationId"].(string)
			seen[id] = true
			restricted, _ := op[extDemoRestricted].(bool)
			_, allowed := demoAllowedAccountOperations[id]
			switch {
			case restricted && allowed:
				t.Errorf("%s is demo restricted but listed as allowed", id)
			case !restricted && !allowed:
				t.Errorf("%s changes the account but neither declares DemoRestricted nor is listed in demoAllowedAccountOperations", id)
			}
		}
	}
	for id := range demoAllowedAccountOperations {
		if !seen[id] {
			t.Errorf("allowance %s matches no account operation; remove it", id)
		}
	}
}

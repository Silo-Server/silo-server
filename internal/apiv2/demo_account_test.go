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
		// A key enrolled before demo mode was turned on: revoking it ends
		// every session it opened.
		{"revoke device key", http.MethodDelete, "/auth/keys/devices/0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11", ""},
		{"deny waiting device", http.MethodPost, "/auth/keys/pending/0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11/deny", "{}"},
		{"revoke device pre-approval", http.MethodDelete, "/auth/device/preapprovals/6f1c2a1e-8d3b-4f0e-9a7c-2b5d8e1f3a4c", ""},
	}
	headers := map[string]map[string]string{
		"member": with(bearer(memberToken), "X-Profile-Id", "p-primary"),
		"admin":  with(bearer(adminToken), "X-Profile-Id", "p-primary"),
	}
	// These rows reach a fake that succeeds, so the administrator, and the
	// member with demo mode off, must get 204 and not merely another refusal.
	// The two key rows also count calls on the app key fake.
	succeeds := map[string]bool{"revoke device key": true, "deny waiting device": true, "revoke device pre-approval": true}
	keyRow := map[string]bool{"revoke device key": true, "deny waiting device": true}
	keys := &fakeAppKeys{}
	handler := func(demo bool) http.Handler {
		deps := externalSignInDeps(&fakeExternalSignIn{allowUnlink: true})
		deps.Sessions = &fakeSessionService{}
		deps.AppKeys = keys
		deps.DemoSettings = fakeSettings{demo: demo}
		return newTestHandler(t, deps)
	}
	on, off := handler(true), handler(false)
	for _, tc := range restricted {
		t.Run(tc.name, func(t *testing.T) {
			// answered checks one non-demo answer: 204 with the key service
			// run once for a row that succeeds, anything but the demo
			// refusal otherwise.
			answered := func(who string, h http.Handler, hdr map[string]string) {
				t.Helper()
				before := keys.calls
				rec := do(t, h, tc.method, Prefix+tc.path, tc.body, hdr)
				if isDemoRefusal(rec) || (succeeds[tc.name] && rec.Code != http.StatusNoContent) {
					t.Fatalf("%s: %d %s", who, rec.Code, rec.Body.String())
				}
				if keyRow[tc.name] && keys.calls != before+1 {
					t.Fatalf("%s: key service ran %d times", who, keys.calls-before)
				}
			}
			before := keys.calls
			rec := do(t, on, tc.method, Prefix+tc.path, tc.body, headers["member"])
			if !isDemoRefusal(rec) {
				t.Fatalf("demo visitor: %d %s", rec.Code, rec.Body.String())
			}
			requireProblem(t, rec, TypePermissionDenied)
			if keys.calls != before {
				t.Fatalf("demo visitor: key service ran %d times", keys.calls-before)
			}
			answered("administrator in demo mode", on, headers["admin"])
			answered("member with demo mode off", off, headers["member"])
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
	// appKeysOn answers capability_disabled in demo mode, administrators
	// included, so these four never run on a demo server.
	"enrollAppKeys":        "the handler refuses every caller while demo mode is on (appKeysOn)",
	"replacePersonKey":     "the handler refuses every caller while demo mode is on (appKeysOn)",
	"createAppCode":        "the handler refuses every caller while demo mode is on (appKeysOn)",
	"approvePendingAppKey": "the handler refuses every caller while demo mode is on (appKeysOn)",
}

// demoAccountDomains are the tags of operations that change the account, how
// it signs in, or who holds a session on it.
var demoAccountDomains = map[string]bool{"account": true, "api-keys": true, "app-keys": true, "auth": true, "device-login": true, "notifications": true, "profiles": true}

// TestDemoModeCoversAccountOperations fails when an account-changing
// operation a non-administrator can call is added without deciding its
// demo-mode behavior: restrict it, or list it above with the reason.
func TestDemoModeCoversAccountOperations(t *testing.T) {
	seen := map[string]bool{}
	for _, op := range documentOperations(t) {
		if !isMutatingMethod(op.Method) || op.Class == ClassPublic || op.Class == ClassActingAdmin || !demoAccountDomains[op.Tag] {
			continue
		}
		seen[op.ID] = true
		restricted, _ := op.Raw[extDemoRestricted].(bool)
		_, allowed := demoAllowedAccountOperations[op.ID]
		switch {
		case restricted && allowed:
			t.Errorf("%s is demo restricted but listed as allowed", op.ID)
		case !restricted && !allowed:
			t.Errorf("%s changes the account but neither declares DemoRestricted nor is listed in demoAllowedAccountOperations", op.ID)
		}
	}
	for id := range demoAllowedAccountOperations {
		if !seen[id] {
			t.Errorf("allowance %s matches no account operation; remove it", id)
		}
	}
}

// documentOperation is one operation of the generated document, with the
// declarations the coverage tests judge.
type documentOperation struct {
	Method, Path, ID, Tag string
	Class                 Class
	// Raw is the operation object as the document has it.
	Raw map[string]any
}

// documentOperations walks the generated document: every operation, sorted
// by path then method, so a coverage test reads the declarations the
// registry published rather than the structs it was given.
func documentOperations(t *testing.T) []documentOperation {
	t.Helper()
	var out []documentOperation
	for path, item := range generatedDocument(t)["paths"].(map[string]any) {
		for method, raw := range item.(map[string]any) {
			op, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			class, _ := op[extClass].(string)
			id, _ := op["operationId"].(string)
			tags, _ := op["tags"].([]any)
			tag := ""
			if len(tags) == 1 {
				tag, _ = tags[0].(string)
			}
			out = append(out, documentOperation{Method: strings.ToUpper(method), Path: path, ID: id, Tag: tag, Class: Class(class), Raw: op})
		}
	}
	slices.SortFunc(out, func(a, b documentOperation) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Method, b.Method)
	})
	return out
}

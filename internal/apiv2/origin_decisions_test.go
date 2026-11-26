package apiv2

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/policy"
)

// documentedDecision reads an operation's published decision for a session
// another app's key opened, or nil.
func documentedDecision(t *testing.T, op documentOperation) *SessionDecision {
	t.Helper()
	raw, ok := op.Raw[extOriginDecisions]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var d OriginDecisions
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("%s: %s: %v", op.ID, extOriginDecisions, err)
	}
	return d.GrantedApp
}

// judgedClass reports whether a session another app's key opened reaches
// an operation of the class through a gate that reads its decision.
func judgedClass(c Class) bool {
	return c == ClassAuthenticated || c == ClassProfileScoped || c == ClassPermissionGated
}

// TestEveryOperationDecidesForGrantedApps fails by name on an operation a
// session another app's key opened can reach, read or write, that states no
// decision for it, on a decision without its reason, and on an own-only
// decision that does not name the handler check enforcing it. Catches a new
// operation that ships without anyone deciding what such a session may do
// with it.
func TestEveryOperationDecidesForGrantedApps(t *testing.T) {
	for _, op := range documentOperations(t) {
		d := documentedDecision(t, op)
		if !judgedClass(op.Class) {
			if d != nil {
				t.Errorf("%s (%s) states a decision no gate reads", op.ID, op.Class)
			}
			continue
		}
		switch {
		case d == nil:
			t.Errorf("%s %s (%s) states no decision for a session another app's key opened: set Operation.Origins", op.Method, op.Path, op.ID)
		case strings.TrimSpace(d.Reason) == "":
			t.Errorf("%s: %s decision without a reason", op.ID, d.Verdict)
		case d.Verdict == VerdictOwnOnly && strings.TrimSpace(d.Check) == "":
			t.Errorf("%s: own-only decision names no handler check", op.ID)
		}
	}
}

// pathParam matches a path template parameter.
var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// TestRefusedOperationsAnswerGrantedAppsFirst: for every operation the
// document says a session another app's key opened is refused, a request
// from such a session answers 403 permission_denied with the refusal's
// detail, and the same request from the account's password session does not
// (it may answer anything else: 400, 404, 503). The requests are built from
// the document alone, so a refusal that is declared but not enforced, or one
// that leaks onto the person's own sessions, fails here by name.
func TestRefusedOperationsAnswerGrantedAppsFirst(t *testing.T) {
	deps := parityDeps(false)
	pass := func(next http.Handler) http.Handler { return next }
	deps.PermissionGates[policy.PermissionMetadataCuration] = pass
	h := newTestHandler(t, deps)
	refused := 0
	for _, op := range documentOperations(t) {
		d := documentedDecision(t, op)
		if !judgedClass(op.Class) || d == nil || d.Verdict != VerdictRefused {
			continue
		}
		refused++
		t.Run(op.ID, func(t *testing.T) {
			path := pathParam.ReplaceAllString(op.Path, "1")
			body := ""
			if _, ok := op.Raw["requestBody"]; ok {
				body = "{}"
			}
			headers := func(token string) map[string]string {
				return with(bearer(token), "X-Profile-Id", "p-owner")
			}
			p := requireProblem(t, do(t, h, op.Method, path, body, headers(grantedAppToken)), TypePermissionDenied)
			if p.Detail != handlers.SessionNotAllowedDetail {
				t.Fatalf("another app's session: detail %q", p.Detail)
			}
			rec := do(t, h, op.Method, path, body, headers(memberToken))
			var own problemDoc
			if rec.Code == http.StatusForbidden && json.Unmarshal(rec.Body.Bytes(), &own) == nil && own.Detail == handlers.SessionNotAllowedDetail {
				t.Fatalf("password session refused as another app: %s", rec.Body.String())
			}
		})
	}
	if refused == 0 {
		t.Fatal("no refused operation in the document")
	}
}

// TestOriginDecisionDeclarations: Register refuses a decision without its
// reason, an own-only decision that names no check, a check on another
// verdict, and a decision on a class no such session reaches through a gate.
// Catches a mark that publishes a verdict nobody can audit or that no gate
// ever reads.
func TestOriginDecisionDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   Operation
	}{
		{"no reason", Operation{Class: ClassProfileScoped, Origins: grantedApp(decideAllowed(""))}},
		{"own only without check", Operation{Class: ClassProfileScoped, Origins: grantedApp(decideOwnOnly("", "its own"))}},
		{"check on refusal", Operation{Class: ClassProfileScoped, Origins: grantedApp(&SessionDecision{Verdict: VerdictRefused, Reason: "r", Check: "c"})}},
		{"unknown verdict", Operation{Class: ClassProfileScoped, Origins: grantedApp(&SessionDecision{Verdict: "maybe", Reason: "r"})}},
		{"public", Operation{Class: ClassPublic, Origins: grantedApp(decideAllowed("r"))}},
		{"acting admin", Operation{Class: ClassActingAdmin, Origins: grantedApp(decideRefused("r"))}},
	} {
		if checkOriginDecisions(tc.op) == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
	if err := checkOriginDecisions(Operation{Class: ClassAuthenticated, Origins: grantedApp(decideOwnOnly("check", "its own"))}); err != nil {
		t.Errorf("own only with check: %v", err)
	}
}

// TestRefusedFamiliesNeverReachTheirService: one refused operation or more
// per family, sent by a session another app's key opened, is answered 403
// permission_denied and the family's service is never called; the same
// request from the account's password session reaches it. Each service is a
// tripwire (an interface value with no implementation) that panics when
// called, which the router answers 500. Catches a refusal that moves into, or
// behind, the handler, so the service runs before the session is judged.
func TestRefusedFamiliesNeverReachTheirService(t *testing.T) {
	for _, tc := range []struct {
		family, method, path, body string
		wire                       func(*Dependencies)
	}{
		{"webhook sync", http.MethodPost, "/webhook-sync/connections", `{"provider":"plex","server_name":"Plex","default_profile_id":"p-owner"}`, func(d *Dependencies) {
			d.WebhookSync = struct{ WebhookSyncService }{}
			d.WebhookSyncManagement = struct{ WebhookSyncManagementService }{}
		}},
		{"webhook sync list", http.MethodGet, "/webhook-sync/connections", "", func(d *Dependencies) {
			d.WebhookSync = struct{ WebhookSyncService }{}
			d.WebhookSyncManagement = struct{ WebhookSyncManagementService }{}
		}},
		{"notification webhooks", http.MethodPost, "/notifications/webhooks", `{"name":"hook","url":"https://example.test/hook"}`, func(d *Dependencies) {
			d.NotificationDestinationCreate = struct {
				NotificationDestinationCreateService
			}{}
		}},
		{"notification webhook list", http.MethodGet, "/notifications/webhooks", "", func(d *Dependencies) {
			d.NotificationDestinations = struct{ NotificationDestinationService }{}
		}},
		{"plugin notification destinations", http.MethodPost, "/notifications/plugin-destinations", `{"plugin_id":"p","capability_id":"c","name":"n"}`, func(d *Dependencies) {
			d.NotificationPluginDestinations = struct {
				NotificationPluginDestinationService
			}{}
		}},
		{"notification email address", http.MethodPut, "/notifications/email-preferences/address", `{"verification_id":"6f1c2a1e-8d3b-4f0e-9a7c-2b5d8e1f3a4c","email":"a@example.test"}`, func(d *Dependencies) {
			d.NotificationEmailVerification = struct {
				NotificationEmailVerificationService
			}{}
		}},
		{"discord link", http.MethodPost, "/notifications/discord/link/init", `{}`, func(d *Dependencies) {
			d.NotificationDiscordManagement = struct {
				NotificationDiscordManagementService
			}{}
		}},
		{"watch-provider link", http.MethodPost, "/watch-providers/trakt/auth/api-key", `{"api_key":"synthetic-token"}`, func(d *Dependencies) {
			d.WatchProviders = struct{ WatchProviderService }{}
		}},
		{"history-import link", http.MethodPost, "/history-imports/plex/auth/pin", "", func(d *Dependencies) {
			d.HistoryImports = struct{ HistoryImportService }{}
		}},
		{"sign-in provider unlink", http.MethodDelete, "/account/identities/4", "", func(d *Dependencies) {
			d.ExternalSignIn = struct{ ExternalSignInService }{}
		}},
		{"password change", http.MethodPost, "/account/password", `{"current_password":"pw","new_password":"margin fossil quench hollow"}`, func(d *Dependencies) {
			d.Accounts = struct{ AccountService }{}
		}},
		{"personal API key list", http.MethodGet, "/api-keys", "", func(d *Dependencies) {
			d.PersonalAPIKeys = struct{ PersonalAPIKeyService }{}
		}},
		{"personal API key revoke", http.MethodDelete, "/api-keys/1", "", func(d *Dependencies) {
			d.PersonalAPIKeys = struct{ PersonalAPIKeyService }{}
		}},
		{"profile create", http.MethodPost, "/profiles", `{"name":"Kid"}`, func(d *Dependencies) { d.Profiles = struct{ ProfileService }{} }},
		{"profile change", http.MethodPatch, "/profiles/p-kid", `{}`, func(d *Dependencies) { d.Profiles = struct{ ProfileService }{} }},
		{"profile delete", http.MethodDelete, "/profiles/p-kid", "", func(d *Dependencies) { d.Profiles = struct{ ProfileService }{} }},
		{"device registry", http.MethodGet, "/devices", "", func(d *Dependencies) { d.DeviceSettings = struct{ DeviceSettingsService }{} }},
		{"downloads list", http.MethodGet, "/downloads", "", func(d *Dependencies) { d.Downloads = struct{ DownloadRegistryService }{} }},
		{"downloads create", http.MethodPost, "/downloads", `{"content_id":"movie","media_file_id":"42"}`, func(d *Dependencies) { d.DownloadCreation = struct{ DownloadCreationService }{} }},
	} {
		t.Run(tc.family, func(t *testing.T) {
			deps := parityDeps(false)
			tc.wire(&deps)
			h := newTestHandler(t, deps)
			headers := func(token string) map[string]string { return with(bearer(token), "X-Profile-Id", "p-owner") }
			p := requireProblem(t, do(t, h, tc.method, Prefix+tc.path, tc.body, headers(grantedAppToken)), TypePermissionDenied)
			if p.Detail != handlers.SessionNotAllowedDetail {
				t.Fatalf("another app's session: detail %q", p.Detail)
			}
			if rec := do(t, h, tc.method, Prefix+tc.path, tc.body, headers(memberToken)); rec.Code != http.StatusInternalServerError {
				t.Fatalf("password session did not reach the service: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

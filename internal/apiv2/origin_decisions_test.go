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

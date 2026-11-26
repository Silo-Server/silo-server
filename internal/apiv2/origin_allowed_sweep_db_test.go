package apiv2

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestAllowedOperationsAnswerGrantedAppsDB: every operation the document
// marks allowed, sent by a session another app's key opened (a real grant
// session, through the real key service and session lookup), never answers
// 403 permission_denied: not the gate's, and not a handler's. The requests
// are built from the document with placeholder ids and minimal bodies, so
// most reach a fake or answer a validation problem; what the sweep catches
// is a refusal of such a session that contradicts the published decision.
// It sees the refusals that run before or without a stubbed service (the
// gate, the key and session rules on the real key service); a refusal inside
// a service the harness fakes is not reached. Catches an operation marked
// allowed whose handler refuses such a session anyway.
func TestAllowedOperationsAnswerGrantedAppsDB(t *testing.T) {
	// Services whose handlers check the session after asking whether the
	// service is wired are given one, so the check is reached.
	d := newKeyRoutesDB(t, func(deps *Dependencies) { deps.ProfileSet = &fakeProfileSets{} })
	for _, op := range documentOperations(t) {
		dec := documentedDecision(t, op)
		if !judgedClass(op.Class) || dec == nil || dec.Verdict != VerdictAllowed {
			continue
		}
		t.Run(op.ID, func(t *testing.T) {
			path := pathParam.ReplaceAllString(op.Path, "1")
			body := ""
			if _, ok := op.Raw["requestBody"]; ok {
				body = "{}"
			}
			rec := do(t, d.handler, op.Method, path, body, with(bearer("tok-app"), "X-Profile-Id", "p-owner"))
			if rec.Code != http.StatusForbidden {
				return
			}
			var p problemDoc
			if json.Unmarshal(rec.Body.Bytes(), &p) == nil && strings.HasSuffix(p.Type, "/"+TypePermissionDenied.ID) {
				t.Fatalf("allowed, but such a session is refused: %s", rec.Body.String())
			}
		})
	}
}

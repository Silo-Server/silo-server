package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/policy"
)

type fakeAdminPolicy struct {
	nextCursor                                     string
	listOptions                                    policy.ListOptions
	document                                       policy.Document
	version                                        policy.Version
	writes, reads                                  int
	expected                                       int64
	mismatch, deleted, editorDisabled, applyFailed bool
}

func newFakeAdminPolicy() *fakeAdminPolicy {
	return &fakeAdminPolicy{document: policy.Document{ID: 1, Revision: 1, Domain: "scope", Enabled: true, Name: "Fixture policy", CreatedAt: fixedTime(), UpdatedAt: fixedTime()}, version: policy.Version{ID: 2, DocumentID: 1, VersionNumber: 1, RegoSource: "package silo_custom.scope\nimport rego.v1\noverride(base, _) := base", SourceSHA256: strings.Repeat("a", 64), CompiledOK: true, CreatedAt: fixedTime()}}
}
func (f *fakeAdminPolicy) AdminPolicyReady(editor, _, _ bool) error {
	if editor && f.editorDisabled {
		return &handlers.APIError{Status: 403, Message: "Policy editor is disabled"}
	}
	return nil
}
func (f *fakeAdminPolicy) ListAdminPolicyDocuments(_ context.Context, after int64, _ int) ([]policy.Document, error) {
	if after >= f.document.ID {
		return nil, nil
	}
	return []policy.Document{f.document}, nil
}
func (f *fakeAdminPolicy) GetAdminPolicyDocument(_ context.Context, id int64) (policy.DocumentSnapshot, error) {
	f.reads++
	if f.deleted || id != 1 {
		return policy.DocumentSnapshot{}, policy.ErrDocumentNotFound
	}
	out := policy.DocumentSnapshot{Document: f.document}
	if f.document.ActiveVersionID != nil {
		out.ActiveVersion = new(f.version)
	}
	return out, nil
}
func (f *fakeAdminPolicy) CreateAdminPolicyDocument(_ context.Context, domain, name string) (policy.Document, error) {
	f.writes++
	f.document.Domain = domain
	f.document.Name = name
	return f.document, nil
}
func (f *fakeAdminPolicy) ListAdminPolicyVersions(_ context.Context, _ int64, before int, _ int) ([]policy.Version, error) {
	if before > 0 && before <= f.version.VersionNumber {
		return nil, nil
	}
	return []policy.Version{f.version}, nil
}
func (f *fakeAdminPolicy) GetAdminPolicyVersion(_ context.Context, id, version int64) (policy.Version, error) {
	if id != 1 || version != 2 {
		return policy.Version{}, policy.ErrVersionNotFound
	}
	return f.version, nil
}
func (f *fakeAdminPolicy) CreateAdminPolicyVersion(_ context.Context, _ int64, source, comment string) (policy.Version, error) {
	f.writes++
	f.document.Revision++
	v := f.version
	v.ID = 3
	v.VersionNumber = 2
	v.RegoSource = source
	v.Comment = new(comment)
	if source == "invalid" {
		v.CompiledOK = false
		v.CompileError = new("Invalid policy source")
	}
	return v, nil
}
func (f *fakeAdminPolicy) mutate(expected int64) error {
	f.writes++
	f.expected = expected
	if f.mismatch {
		f.document.Revision++
		return &policy.DocumentRevisionMismatchError{Expected: expected, Actual: f.document.Revision}
	}
	f.document.Revision++
	return nil
}
func (f *fakeAdminPolicy) apply() policy.DocumentApplyResult {
	out := policy.DocumentApplyResult{Persisted: true, DocumentMutation: policy.DocumentMutation{Document: f.document, Generation: 7}, Application: policy.ApplyStatus{Generation: 7}}
	if f.applyFailed {
		out.Application.LocalReloadErr = errors.New("private database address")
		out.Application.PublishErr = errors.New("private event address")
		out.Application.Generation = 6
	}
	return out
}
func (f *fakeAdminPolicy) ActivateAdminPolicyVersion(_ context.Context, _, version, expected int64) (policy.DocumentApplyResult, error) {
	if err := f.mutate(expected); err != nil {
		return policy.DocumentApplyResult{}, err
	}
	f.document.ActiveVersionID = new(version)
	return f.apply(), nil
}
func (f *fakeAdminPolicy) SetAdminPolicyEnabled(_ context.Context, _ int64, enabled bool, expected int64) (policy.DocumentApplyResult, error) {
	if err := f.mutate(expected); err != nil {
		return policy.DocumentApplyResult{}, err
	}
	f.document.Enabled = enabled
	return f.apply(), nil
}
func (f *fakeAdminPolicy) DeleteAdminPolicyDocument(_ context.Context, _, expected int64) error {
	if err := f.mutate(expected); err != nil {
		return err
	}
	f.deleted = true
	return nil
}
func (f *fakeAdminPolicy) SimulateAdminPolicy(context.Context, policy.SimulateRequest) (policy.SimulateResult, error) {
	return policy.SimulateResult{Decision: []byte(`{"allow":true}`), EvalTimeNS: 100, Generation: 7}, nil
}
func (f *fakeAdminPolicy) ListAdminPolicyDecisions(_ context.Context, in policy.ListOptions) (policy.ListResult, error) {
	f.listOptions = in
	if in.Cursor == "bad" {
		return policy.ListResult{}, errors.New("invalid cursor")
	}
	return policy.ListResult{Entries: []policy.Entry{}, NextCursor: f.nextCursor}, nil
}
func (f *fakeAdminPolicy) GetAdminPolicyDecision(context.Context, int64) (policy.Entry, error) {
	return policy.Entry{ID: 3, Timestamp: fixedTime(), DecisionName: policy.DecisionPermission, PolicyGeneration: 7, Allowed: new(true), InputSample: []byte(`{"resource":"fixture"}`), ResultSample: []byte(`{"allow":true}`)}, nil
}
func adminPolicyTestHandler(t *testing.T, f *fakeAdminPolicy) http.Handler {
	t.Helper()
	deps, _ := libraryDeps(t)
	deps.AdminPolicy = f
	return newTestHandler(t, deps)
}

func TestAdminPolicyCapturedGuards(t *testing.T) {
	for _, tc := range []struct {
		method, suffix, body string
		status               int
	}{{"PATCH", "", `{"enabled":false}`, 200}, {"PUT", "/active-version", `{"version_id":"2"}`, 200}, {"DELETE", "", "", 204}} {
		t.Run(tc.method, func(t *testing.T) {
			f := newFakeAdminPolicy()
			h := adminPolicyTestHandler(t, f)
			path := Prefix + "/admin/policy/documents/1"
			read := do(t, h, "GET", path, "", bearer(adminToken))
			tag := read.Header().Get("ETag")
			if read.Code != 200 || tag == "" || strings.HasPrefix(tag, "W/") {
				t.Fatalf("read %d %s", read.Code, read.Body)
			}
			again := do(t, h, "GET", path, "", bearer(adminToken))
			if again.Body.String() != read.Body.String() || again.Header().Get("ETag") != tag {
				t.Fatal("unstable canonical representation")
			}
			requireProblem(t, do(t, h, tc.method, path+tc.suffix, tc.body, bearer(adminToken)), TypePreconditionRequired)
			requireProblem(t, do(t, h, tc.method, path+tc.suffix, tc.body, with(bearer(adminToken), "If-Match", `"stale"`)), TypePreconditionFailed)
			if f.writes != 0 {
				t.Fatal("failed precondition reached writer")
			}
			f.mismatch = true
			before := f.reads
			race := do(t, h, tc.method, path+tc.suffix, tc.body, with(bearer(adminToken), "If-Match", tag))
			requireProblem(t, race, TypePreconditionFailed)
			if f.expected != 1 || f.writes != 1 || f.reads != before+1 || race.Header().Get("ETag") == tag {
				t.Fatal("lost captured guard, reread after conflict, or retried")
			}
			f.mismatch = false
			fresh := do(t, h, tc.method, path+tc.suffix, tc.body, with(bearer(adminToken), "If-Match", " * "))
			if fresh.Code != tc.status || f.expected != policy.AnyDocumentRevision {
				t.Fatalf("wildcard %d %s expected %d", fresh.Code, fresh.Body, f.expected)
			}
		})
	}
}
func TestAdminPolicyCommittedApplyFailure(t *testing.T) {
	f := newFakeAdminPolicy()
	f.applyFailed = true
	h := adminPolicyTestHandler(t, f)
	path := Prefix + "/admin/policy/documents/1"
	tag := do(t, h, "GET", path, "", bearer(adminToken)).Header().Get("ETag")
	out := do(t, h, "PATCH", path, `{"enabled":true}`, with(bearer(adminToken), "If-Match", tag))
	if out.Code != 200 || out.Header().Get("ETag") == tag {
		t.Fatalf("apply %d %s", out.Code, out.Body)
	}
	for _, want := range []string{`"persisted":true`, `"persisted_generation":7`, `"local_applied":false`, `"loaded_generation":6`, `"publication_failed":true`} {
		if !strings.Contains(out.Body.String(), want) {
			t.Fatalf("missing %s in %s", want, out.Body)
		}
	}
	if strings.Contains(out.Body.String(), "private") {
		t.Fatal("internal apply error leaked")
	}
	requireProblem(t, do(t, h, "PATCH", path, `{"enabled":true}`, with(bearer(adminToken), "If-Match", tag)), TypePreconditionFailed)
	if f.writes != 1 {
		t.Fatal("replayed committed mutation")
	}
}
func TestAdminPolicyBodiesAndDraftPersistence(t *testing.T) {
	f := newFakeAdminPolicy()
	h := adminPolicyTestHandler(t, f)
	path := Prefix + "/admin/policy/documents/1"
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":false,"unknown":1}`} {
		requireProblem(t, do(t, h, "PATCH", path, body, with(bearer(adminToken), "If-Match", "*")), TypeValidationFailed)
	}
	if f.writes != 0 {
		t.Fatal("invalid body reached writer")
	}
	saved := do(t, h, "POST", path+"/versions", `{"source":"invalid"}`, bearer(adminToken))
	if saved.Code != 201 || saved.Header().Get("Location") != path+"/versions/3" || !strings.Contains(saved.Body.String(), `"compiled_ok":false`) || !strings.Contains(saved.Body.String(), `"id":"3"`) {
		t.Fatalf("saved draft %d %s", saved.Code, saved.Body)
	}
	requireProblem(t, do(t, h, "GET", Prefix+"/admin/policy/decisions?cursor=bad", "", bearer(adminToken)), TypeInvalidCursor)
	for _, query := range []string{"limit=201", "allowed=maybe", "user_id=0", "from=2026-09-05T00:00:00Z&to=2026-09-04T00:00:00Z"} {
		requireProblem(t, do(t, h, "GET", Prefix+"/admin/policy/decisions?"+query, "", bearer(adminToken)), TypeValidationFailed)
	}
	page := do(t, h, "GET", Prefix+"/admin/policy/decisions", "", bearer(adminToken))
	if page.Code != 200 || !strings.Contains(page.Body.String(), `"items":[]`) || !strings.Contains(page.Body.String(), `"has_more":false`) {
		t.Fatalf("page %d %s", page.Code, page.Body)
	}
}
func TestAdminPolicyAuthorizationAndEditorGate(t *testing.T) {
	f := newFakeAdminPolicy()
	h := adminPolicyTestHandler(t, f)
	for _, path := range []string{"/vendor", "/documents", "/documents/1", "/documents/1/versions", "/documents/1/versions/2", "/decisions", "/decisions/3"} {
		requireProblem(t, do(t, h, "GET", Prefix+"/admin/policy"+path, "", nil), TypeAuthenticationRequired)
		requireProblem(t, do(t, h, "GET", Prefix+"/admin/policy"+path, "", bearer(memberToken)), TypePermissionDenied)
	}
	f.editorDisabled = true
	requireProblem(t, do(t, h, "GET", Prefix+"/admin/policy/documents", "", bearer(adminToken)), TypePermissionDenied)
	out := do(t, h, "GET", Prefix+"/admin/policy/decisions", "", bearer(adminToken))
	if out.Code != 200 {
		t.Fatalf("decision viewer incorrectly editor-gated: %d %s", out.Code, out.Body)
	}
}

func TestAdminPolicyDecisionCursorBinding(t *testing.T) {
	f := newFakeAdminPolicy()
	f.nextCursor = "legacy-position"
	h := adminPolicyTestHandler(t, f)
	path := Prefix + "/admin/policy/decisions?allowed=true&decision_name=permission"
	out := do(t, h, "GET", path, "", bearer(adminToken))
	if out.Code != 200 {
		t.Fatalf("page %d %s", out.Code, out.Body)
	}
	var page Collection[AdminPolicyDecision]
	if err := json.Unmarshal(out.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Page == nil || !page.Page.HasMore || page.Page.NextCursor == "legacy-position" {
		t.Fatal("missing signed continuation")
	}
	cursor := page.Page.NextCursor
	next := do(t, h, "GET", path+"&cursor="+cursor, "", bearer(adminToken))
	if next.Code != 200 || f.listOptions.Cursor != "legacy-position" {
		t.Fatalf("continuation %d %s", next.Code, next.Body)
	}
	requireProblem(t, do(t, h, "GET", path+"&cursor="+cursor, "", bearer(otherAdminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, "GET", strings.Replace(path, "allowed=true", "allowed=false", 1)+"&cursor="+cursor, "", bearer(adminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, "GET", path+"&cursor="+cursor+"x", "", bearer(adminToken)), TypeInvalidCursor)
}
func TestAdminPolicyMutationAuthorization(t *testing.T) {
	f := newFakeAdminPolicy()
	h := adminPolicyTestHandler(t, f)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/documents", `{"domain":"scope","name":"Denied"}`},
		{"POST", "/documents/1/versions", `{"source":"invalid"}`},
		{"PATCH", "/documents/1", `{"enabled":false}`},
		{"PUT", "/documents/1/active-version", `{"version_id":"2"}`},
		{"DELETE", "/documents/1", ""},
		{"POST", "/validate", `{"domain":"scope","source":"invalid"}`},
		{"POST", "/simulate", `{"domain":"scope","input":{}}`},
	} {
		requireProblem(t, do(t, h, tc.method, Prefix+"/admin/policy"+tc.path, tc.body, with(bearer(memberToken), "If-Match", "*")), TypePermissionDenied)
	}
	if f.writes != 0 {
		t.Fatal("non-administrator reached policy writer")
	}
}

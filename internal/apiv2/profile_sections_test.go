package apiv2

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func sectionDeps(svc *fakeProfileSections) Dependencies {
	deps := pilotDeps(nil, nil)
	if svc != nil {
		deps.ProfileSections = svc
	}
	return deps
}

// TestProfileSectionConfigPresence is the absent-versus-empty rule for the
// config members: the web editors round-trip GET through the
// full-replacement PUT, so a saved {} must survive as {} or an unrelated hide
// or reorder would silently drop it and let the section fall back to its
// inherited config.
func TestProfileSectionConfigPresence(t *testing.T) {
	svc := &fakeProfileSections{rows: []userstore.SectionOverride{
		{ID: "o-empty", SectionID: "s-continue", Config: `{}`},
		{ID: "o-user-empty", IsUserAdded: true, UserSectionType: "hidden_gems", Config: `{"library_ids":[3]}`, UserConfig: `{}`},
		{ID: "o-none", SectionID: "s-recent"},
	}}
	h := newTestHandler(t, sectionDeps(svc))
	auth := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	// A stored {} is present and empty on the read; no stored config omits
	// the member.
	rec := do(t, h, http.MethodGet, "/api/v2/profile/sections", "", auth)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var got struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Items) != 3 {
		t.Fatalf("%v %s", err, rec.Body.String())
	}
	if c, ok := got.Items[0]["config"]; !ok || string(c) != `{}` {
		t.Fatalf("o-empty config = %q, present %v", c, ok)
	}
	if _, ok := got.Items[0]["user_config"]; ok {
		t.Fatalf("o-empty user_config present: %s", rec.Body.String())
	}
	if c, ok := got.Items[1]["user_config"]; !ok || string(c) != `{}` {
		t.Fatalf("o-user-empty user_config = %q, present %v", c, ok)
	}
	if c := got.Items[1]["config"]; string(c) != `{"library_ids":[3]}` {
		t.Fatalf("o-user-empty config = %q", c)
	}
	for _, member := range []string{"config", "user_config"} {
		if _, ok := got.Items[2][member]; ok {
			t.Fatalf("o-none %s present: %s", member, rec.Body.String())
		}
	}

	// On the write, {} reaches v1 as {} (an explicitly empty document) and
	// an omitted member reaches it as absent.
	body := `{"overrides":[` +
		`{"id":"o-empty","section_id":"s-continue","config":{}},` +
		`{"id":"o-user-empty","is_user_added":true,"user_section_type":"hidden_gems","config":{"library_ids":[3]},"user_config":{}},` +
		`{"id":"o-none","section_id":"s-recent"}` +
		`]}`
	rec = do(t, h, http.MethodPut, "/api/v2/profile/sections", body, auth)
	if rec.Code != 204 || len(svc.lastWrites) != 3 {
		t.Fatalf("%d %s writes = %+v", rec.Code, rec.Body.String(), svc.lastWrites)
	}
	if w := svc.lastWrites[0]; string(w.Config) != `{}` || w.UserConfig != nil {
		t.Fatalf("o-empty write = config %q user_config %q", w.Config, w.UserConfig)
	}
	if w := svc.lastWrites[1]; string(w.Config) != `{"library_ids":[3]}` || string(w.UserConfig) != `{}` {
		t.Fatalf("o-user-empty write = config %q user_config %q", w.Config, w.UserConfig)
	}
	if w := svc.lastWrites[2]; w.Config != nil || w.UserConfig != nil {
		t.Fatalf("o-none write = config %q user_config %q", w.Config, w.UserConfig)
	}
}

func TestProfileSectionScopeValidation(t *testing.T) {
	h := newTestHandler(t, sectionDeps(nil))
	auth := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	for _, tc := range []struct{ query, location, code string }{
		// v1 stored whatever scope and library_id it was sent; v2 makes
		// them agree so a saved set is always addressable.
		{"?scope=library", locationLibraryID, codeRequired},
		{"?scope=home&library_id=3", locationLibraryID, codeInvalid},
		{"?scope=library&library_id=x", locationLibraryID, codeInvalid},
		{"?scope=global", locationScope, codeInvalidEnum},
		{"?offset=1", "query.offset", codeUnknownParameter},
	} {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			p := requireProblem(t, do(t, h, method, "/api/v2/profile/sections"+tc.query, "", auth), TypeValidationFailed)
			if len(p.Errors) != 1 || p.Errors[0].Location != tc.location || p.Errors[0].Code != tc.code {
				t.Errorf("%s %s: errors = %+v", method, tc.query, p.Errors)
			}
		}
	}
	// The profile header is required: these operations act on the acting
	// profile's own set.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/profile/sections", "", bearer(memberToken)), TypeValidationFailed)
}

func TestProfileSectionsDenied(t *testing.T) {
	h := newTestHandler(t, sectionDeps(nil))
	for _, op := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v2/profile/sections", ""},
		{http.MethodPut, "/api/v2/profile/sections", `{"overrides":[]}`},
		{http.MethodDelete, "/api/v2/profile/sections", ""},
		{http.MethodGet, "/api/v2/profile/sections/settings", ""},
		{http.MethodGet, "/api/v2/profile/sections/flags", ""},
	} {
		requireProblem(t, do(t, h, op.method, op.path, op.body, nil), TypeAuthenticationRequired)
		requireProblem(t, do(t, h, op.method, op.path, op.body, with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
		requireProblem(t, do(t, h, op.method, op.path, op.body, with(bearer(memberToken), "X-Profile-Id", "p-other")), TypeNotFound)
	}
	// Demo mode refuses the mutations to a non-admin and leaves the reads.
	demo := sectionDeps(nil)
	demo.DemoSettings = fakeSettings{demo: true}
	hd := newTestHandler(t, demo)
	auth := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	requireProblem(t, do(t, hd, http.MethodPut, "/api/v2/profile/sections", `{"overrides":[]}`, auth), TypePermissionDenied)
	requireProblem(t, do(t, hd, http.MethodDelete, "/api/v2/profile/sections", "", auth), TypePermissionDenied)
	if rec := do(t, hd, http.MethodGet, "/api/v2/profile/sections", "", auth); rec.Code != 200 {
		t.Fatalf("demo read: %d %s", rec.Code, rec.Body.String())
	}
	// A missing service fails closed.
	unwired := sectionDeps(nil)
	unwired.ProfileSections = nil
	hu := newTestHandler(t, unwired)
	requireProblem(t, do(t, hu, http.MethodGet, "/api/v2/profile/sections", "", auth), TypeDependencyUnavailable)
	// The flag is a constant now and needs no service.
	if rec := do(t, hu, http.MethodGet, "/api/v2/profile/sections/flags", "", auth); rec.Code != 200 {
		t.Fatalf("unwired flags: %d %s", rec.Code, rec.Body.String())
	}
}

package apiv2

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func sectionDeps(svc *fakeProfileSections) Dependencies {
	deps := pilotDeps(nil, nil)
	if svc != nil {
		deps.ProfileSections = svc
	}
	return deps
}

func TestListProfileSectionOverrides(t *testing.T) {
	svc := &fakeProfileSections{rows: fixtureSectionOverrides()}
	h := newTestHandler(t, sectionDeps(svc))
	rec := do(t, h, http.MethodGet, "/api/v2/profile/sections", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	// snake_case on the read, as on the write (v1 answered PascalCase);
	// nullable overrides are explicit null; the store's timestamps are
	// instants or null.
	want := `{"items":[` +
		`{"id":"o-1","section_id":"s-continue","position":2,"hidden":true,"removed":false,"section_type":"","title":"Keep watching","featured":false,"item_limit":10,"is_user_added":false,"user_section_type":"","user_title":"","created_at":"2026-01-02T03:04:05.000Z","updated_at":"2026-01-02T03:04:05.000Z"},` +
		`{"id":"o-2","section_id":"","position":null,"hidden":false,"removed":false,"section_type":"","title":"","featured":null,"item_limit":null,"is_user_added":true,"user_section_type":"hidden_gems","user_config":{"library_ids":[3]},"user_title":"Hidden gems","created_at":null,"updated_at":null}` +
		`]}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if q := svc.lastQuery; q.UserID != 1 || q.ProfileID != "p-owner" || q.Scope != "home" || q.LibraryID != "" {
		t.Fatalf("query = %+v", q)
	}

	// A library page is addressed by scope and library_id together.
	rec = do(t, h, http.MethodGet, "/api/v2/profile/sections?scope=library&library_id=3", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != 200 || svc.lastQuery.Scope != "library" || svc.lastQuery.LibraryID != "3" {
		t.Fatalf("%d %+v", rec.Code, svc.lastQuery)
	}

	// An empty set is an empty array, never null.
	svc.rows = nil
	rec = do(t, h, http.MethodGet, "/api/v2/profile/sections", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Body.String() != `{"items":[]}`+"\n" {
		t.Fatalf("body = %s", rec.Body.String())
	}
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

func TestReplaceProfileSectionOverrides(t *testing.T) {
	svc := &fakeProfileSections{}
	h := newTestHandler(t, sectionDeps(svc))
	auth := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	body := `{"overrides":[` +
		`{"id":"o-1","section_id":"s-continue","position":2,"hidden":true,"item_limit":null},` +
		`{"is_user_added":true,"user_section_type":"hidden_gems","user_config":{"library_ids":[3]},"user_title":"Hidden gems"}` +
		`]}`
	rec := do(t, h, http.MethodPut, "/api/v2/profile/sections?scope=library&library_id=3", body, auth)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if q := svc.lastQuery; q.UserID != 1 || q.ProfileID != "p-owner" || q.Scope != "library" || q.LibraryID != "3" {
		t.Fatalf("query = %+v", q)
	}
	if len(svc.lastWrites) != 2 {
		t.Fatalf("writes = %+v", svc.lastWrites)
	}
	first, second := svc.lastWrites[0], svc.lastWrites[1]
	if first.ID != "o-1" || first.SectionID != "s-continue" || first.Position == nil || *first.Position != 2 || !first.Hidden || first.ItemLimit != nil || first.Featured != nil || len(first.Config) != 0 {
		t.Fatalf("first = %+v", first)
	}
	if !second.IsUserAdded || second.UserSectionType != "hidden_gems" || string(second.UserConfig) != `{"library_ids":[3]}` || second.UserTitle != "Hidden gems" || second.SectionID != "" {
		t.Fatalf("second = %+v", second)
	}

	// The whole set is replaced: an empty array clears it.
	rec = do(t, h, http.MethodPut, "/api/v2/profile/sections", `{"overrides":[]}`, auth)
	if rec.Code != 204 || len(svc.lastWrites) != 0 || svc.lastWrites == nil {
		t.Fatalf("%d writes = %#v", rec.Code, svc.lastWrites)
	}

	for _, tc := range []struct{ body, location, code string }{
		{`{}`, locationOverrides, codeRequired},
		{`{"overrides":null}`, locationOverrides, codeInvalidType},
		{`{"overrides":[{"hidden":null}]}`, locationOverrides + "[0].hidden", codeInvalidType},
		{`{"overrides":[{"position":-1}]}`, locationOverrides + "[0].position", codeOutOfRange},
		{`{"overrides":[{"config":"x"}]}`, locationOverrides + "[0].config", codeInvalidType},
		{`{"overrides":[{"nickname":"x"}]}`, locationOverrides + "[0].nickname", codeUnknownField},
	} {
		p := requireProblem(t, do(t, h, http.MethodPut, "/api/v2/profile/sections", tc.body, auth), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != tc.location || p.Errors[0].Code != tc.code {
			t.Errorf("%s: errors = %+v", tc.body, p.Errors)
		}
	}
}

func TestReplaceProfileSectionOverridesDecisions(t *testing.T) {
	auth := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	body := `{"overrides":[{"is_user_added":true,"user_section_type":"admin_curated_list"}]}`
	for _, tc := range []struct {
		err      error
		want     ProblemType
		location string
	}{
		// v1's recipe gate: an unregistered recipe or a config the recipe
		// refuses is a validation failure at the override set.
		{&handlers.APIError{Status: http.StatusBadRequest, Code: "unknown_recipe", Message: "section_type not registered: nope"}, TypeValidationFailed, locationOverrides},
		{&handlers.APIError{Status: http.StatusBadRequest, Code: "invalid_config", Message: "library_ids: at least one"}, TypeValidationFailed, locationOverrides},
		// An admin-only recipe on a server that does not allow custom sections.
		{&handlers.APIError{Status: http.StatusForbidden, Code: "custom_disabled", Message: "this server does not allow profiles to build custom sections"}, TypePermissionDenied, ""},
		{&handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to save overrides"}, TypeInternalError, ""},
	} {
		h := newTestHandler(t, sectionDeps(&fakeProfileSections{err: tc.err}))
		p := requireProblem(t, do(t, h, http.MethodPut, "/api/v2/profile/sections", body, auth), tc.want)
		if tc.location != "" && (len(p.Errors) != 1 || p.Errors[0].Location != tc.location) {
			t.Errorf("%v: errors = %+v", tc.err, p.Errors)
		}
		if tc.want == TypeInternalError && strings.Contains(p.Detail, "Failed") {
			t.Errorf("internal detail leaked: %q", p.Detail)
		}
	}
}

func TestResetProfileSectionOverrides(t *testing.T) {
	svc := &fakeProfileSections{}
	h := newTestHandler(t, sectionDeps(svc))
	rec := do(t, h, http.MethodDelete, "/api/v2/profile/sections?scope=library&library_id=7", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != 204 || !svc.reset || svc.lastQuery.LibraryID != "7" || svc.lastQuery.ProfileID != "p-owner" {
		t.Fatalf("%d %+v", rec.Code, svc.lastQuery)
	}
}

func TestGetProfileSectionSettings(t *testing.T) {
	svc := &fakeProfileSections{}
	h := newTestHandler(t, sectionDeps(svc))
	rec := do(t, h, http.MethodGet, "/api/v2/profile/sections/settings", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"items":[` +
		`{"id":"s-continue","section_type":"continue_watching","title":"Continue Watching","featured":false,"item_limit":20,"hidden":true,"is_custom":false,"customized":true,"position":0},` +
		`{"id":"u-gems","section_type":"hidden_gems","title":"Hidden gems","featured":false,"item_limit":12,"hidden":false,"is_custom":true,"customized":false,"position":1,"config":{"library_ids":[3]}}` +
		`]}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// The viewer-access filter comes from the scope the gate resolved.
	if svc.lastQuery.ProfileID != "p-owner" || svc.lastFilter.UserID != 1 || svc.lastFilter.ProfileID != "p-owner" {
		t.Fatalf("query = %+v filter = %+v", svc.lastQuery, svc.lastFilter)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/profile/sections/settings?scope=library&library_id=3", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != 200 || svc.lastQuery.LibraryID != "3" {
		t.Fatalf("%d %+v", rec.Code, svc.lastQuery)
	}
}

func TestGetProfileSectionFlags(t *testing.T) {
	h := newTestHandler(t, sectionDeps(nil))
	rec := do(t, h, http.MethodGet, "/api/v2/profile/sections/flags", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != 200 || rec.Body.String() != `{"allow_profile_custom_sections":true}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var flags map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &flags); err != nil {
		t.Fatal(err)
	}
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
	unwired.ProfileSections, unwired.SectionFlags = nil, nil
	hu := newTestHandler(t, unwired)
	requireProblem(t, do(t, hu, http.MethodGet, "/api/v2/profile/sections", "", auth), TypeDependencyUnavailable)
	requireProblem(t, do(t, hu, http.MethodGet, "/api/v2/profile/sections/flags", "", auth), TypeDependencyUnavailable)
}

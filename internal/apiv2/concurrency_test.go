package apiv2

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These probes exercise conditional-header handling through the real router.

func guardedHandler(t *testing.T) (http.Handler, *guardedProbeStore) {
	t.Helper()
	store := newGuardedProbeStore()
	return NewHandler(Dependencies{testRegister: registerGuardedProbes(store)}), store
}

// TestGuardedProbeEmptyIfMatchIs412: RFC 9110 5.6.1 lets a #-list be empty,
// and an empty list matches no tag. Only an absent field is 428.
func TestGuardedProbeEmptyIfMatchIs412(t *testing.T) {
	h, store := guardedHandler(t)
	for _, field := range []string{"", " ", ","} {
		rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"If-Match": field})
		requireProblem(t, rec, TypePreconditionFailed)
		if rec.Header().Get("ETag") == "" {
			t.Fatalf("If-Match %q: 412 lacks the current ETag", field)
		}
	}
	if row, _ := store.Get("a"); row.Name != "alpha" || row.Version != 1 {
		t.Fatalf("resource changed: %+v", row)
	}
}

func TestGuardedProbeConditionalRead(t *testing.T) {
	h, _ := guardedHandler(t)
	current := RenderETag(guardedProbeScope, "a", 1)
	rec := do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != current.String() || !strings.Contains(rec.Body.String(), `"alpha"`) {
		t.Fatalf("plain read: %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	for _, field := range []string{current.String(), "W/" + current.String(), RenderETag(guardedProbeScope, "a", 7).String() + ", " + current.String(), "*"} {
		rec = do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", map[string]string{"If-None-Match": field})
		if rec.Code != http.StatusNotModified {
			t.Fatalf("If-None-Match %q: status %d body %s", field, rec.Code, rec.Body.String())
		}
		if rec.Body.Len() != 0 || rec.Header().Get("ETag") != current.String() || rec.Header().Get("Content-Type") != "" {
			t.Fatalf("If-None-Match %q: body %q etag %q ct %q", field, rec.Body.String(), rec.Header().Get("ETag"), rec.Header().Get("Content-Type"))
		}
		if requestIDHeader(rec) == "" {
			t.Fatal("304 lacks X-Request-ID")
		}
	}
	rec = do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", map[string]string{"If-None-Match": RenderETag(guardedProbeScope, "a", 7).String()})
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 || rec.Header().Get("ETag") != current.String() {
		t.Fatalf("non-matching read: %d %q", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", map[string]string{"If-None-Match": "not-a-tag"})
	p := requireProblem(t, rec, TypeMalformedRequest)
	if strings.Contains(rec.Body.String(), "not-a-tag") {
		t.Fatalf("value echoed: %s", rec.Body.String())
	}
	if len(p.Errors) != 1 || p.Errors[0].Location != "header.If-None-Match" {
		t.Fatalf("errors = %+v", p.Errors)
	}
}

func TestGuardedProbeMalformedIfMatchIs400WithoutEcho(t *testing.T) {
	h, store := guardedHandler(t)
	for _, field := range []string{"secret-value", `"a", *`, `"unterminated`, `"a" "b"`} {
		rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"If-Match": field})
		p := requireProblem(t, rec, TypeMalformedRequest)
		if strings.Contains(rec.Body.String(), "secret-value") || strings.Contains(rec.Body.String(), "unterminated") {
			t.Fatalf("If-Match %q echoed: %s", field, rec.Body.String())
		}
		if len(p.Errors) != 1 || p.Errors[0].Location != "header.If-Match" || p.Errors[0].Code != "invalid_entity_tag" {
			t.Fatalf("errors = %+v", p.Errors)
		}
		if rec.Header().Get("ETag") != "" {
			t.Fatal("400 must not carry an ETag")
		}
	}
	if row, _ := store.Get("a"); row.Name != "alpha" {
		t.Fatalf("row = %+v", row)
	}
}

// TestGuardedProbeIfNoneMatchAfterIfMatch: RFC 9110 13.2.2 evaluates
// If-None-Match after If-Match succeeds; on a mutation a match (or "*") is
// 412 with the current ETag, and the resource is untouched.
func TestGuardedProbeIfNoneMatchAfterIfMatch(t *testing.T) {
	h, store := guardedHandler(t)
	current := RenderETag(guardedProbeScope, "a", 1).String()
	for _, none := range []string{current, "W/" + current, "*", `"other", ` + current} {
		rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"If-Match": current, "If-None-Match": none})
		requireProblem(t, rec, TypePreconditionFailed)
		if got := rec.Header().Get("ETag"); got != current {
			t.Fatalf("If-None-Match %q: 412 ETag = %q", none, got)
		}
		if row, _ := store.Get("a"); row.Name != "alpha" || row.Version != 1 {
			t.Fatalf("If-None-Match %q: resource changed: %+v", none, row)
		}
	}
	// A non-matching If-None-Match lets the mutation through.
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"If-Match": current, "If-None-Match": RenderETag(guardedProbeScope, "a", 99).String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("non-matching If-None-Match: status %d body %s", rec.Code, rec.Body.String())
	}
	// A stale If-Match is reported before If-None-Match is looked at.
	rec = do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"gamma"}`, map[string]string{"If-Match": current, "If-None-Match": "*"})
	p := requireProblem(t, rec, TypePreconditionFailed)
	if strings.Contains(p.Detail, "If-None-Match") {
		t.Fatalf("If-Match should be evaluated first: %q", p.Detail)
	}
	// The same order holds on a guarded DELETE: a matching If-None-Match
	// (or "*") after a current If-Match is 412 and the resource survives.
	h, store = guardedHandler(t)
	for _, none := range []string{current, "*"} {
		rec = do(t, h, http.MethodDelete, "/api/v2/probe/guarded/a", "", map[string]string{"If-Match": current, "If-None-Match": none})
		requireProblem(t, rec, TypePreconditionFailed)
		if _, exists := store.Get("a"); !exists {
			t.Fatalf("If-None-Match %q: resource deleted despite the second precondition", none)
		}
	}
	// Malformed If-None-Match after a passing If-Match is 400.
	rec = do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"gamma"}`, map[string]string{"If-Match": "*", "If-None-Match": "not-a-tag"})
	requireProblem(t, rec, TypeMalformedRequest)
}

// TestConditionalReadEvaluatesIfMatchFirst: a stale If-Match on a read is 412
// with the current ETag before If-None-Match is looked at; a current one lets
// If-None-Match decide; a malformed one is 400.
func TestConditionalReadEvaluatesIfMatchFirst(t *testing.T) {
	h, _ := guardedHandler(t)
	current := RenderETag(guardedProbeScope, "a", 1).String()
	stale := RenderETag(guardedProbeScope, "a", 0).String()
	rec := do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", map[string]string{"If-Match": stale, "If-None-Match": current})
	requireProblem(t, rec, TypePreconditionFailed)
	if got := rec.Header().Get("ETag"); got != current {
		t.Fatalf("412 ETag = %q", got)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", map[string]string{"If-Match": current, "If-None-Match": current})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("current If-Match then matching If-None-Match: %d", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", map[string]string{"If-Match": "*"})
	if rec.Code != http.StatusOK {
		t.Fatalf("If-Match: * on a read: %d", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", map[string]string{"If-Match": "not-a-tag"})
	requireProblem(t, rec, TypeMalformedRequest)
}

// TestCreateOnlyEvaluatesIfMatchFirst: on a create-only PUT, If-Match against
// a missing id is 412 with no ETag (even "*"), a stale If-Match against an
// existing id is 412 with its ETag and no overwrite, and a current one lets
// If-None-Match decide.
func TestCreateOnlyEvaluatesIfMatchFirst(t *testing.T) {
	h, store := guardedHandler(t)
	for _, field := range []string{"*", RenderETag(guardedProbeScope, "new", 1).String()} {
		rec := do(t, h, http.MethodPut, "/api/v2/probe/created/new", `{"name":"x"}`, map[string]string{"If-Match": field})
		requireProblem(t, rec, TypePreconditionFailed)
		if rec.Header().Get("ETag") != "" {
			t.Fatalf("If-Match %q on a missing id: 412 must carry no ETag", field)
		}
		if _, exists := store.Get("new"); exists {
			t.Fatalf("If-Match %q on a missing id created the resource", field)
		}
	}
	stale := RenderETag(guardedProbeScope, "a", 0).String()
	rec := do(t, h, http.MethodPut, "/api/v2/probe/created/a", `{"name":"clobber"}`, map[string]string{"If-Match": stale})
	requireProblem(t, rec, TypePreconditionFailed)
	if got := rec.Header().Get("ETag"); got != RenderETag(guardedProbeScope, "a", 1).String() {
		t.Fatalf("412 ETag = %q", got)
	}
	if row, _ := store.Get("a"); row.Name != "alpha" {
		t.Fatalf("stale If-Match overwrote: %+v", row)
	}
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/a", `{"name":"replaced"}`, map[string]string{"If-Match": RenderETag(guardedProbeScope, "a", 1).String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("current If-Match then replace: %d %s", rec.Code, rec.Body.String())
	}
}

// Repeated header lines are one list (RFC 9110 5.3): the router joins them
// before Huma binds the input, so a tag on the second line still matches.
func TestGuardedProbeRepeatedHeaderLinesAreOneList(t *testing.T) {
	h, store := guardedHandler(t)
	current := RenderETag(guardedProbeScope, "a", 1)
	stale := RenderETag(guardedProbeScope, "a", 0)

	r := httptest.NewRequest(http.MethodPut, "/api/v2/probe/guarded/a", strings.NewReader(`{"name":"beta"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Add("If-Match", stale.String())
	r.Header.Add("If-Match", current.String())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("two If-Match lines: status %d body %s", rec.Code, rec.Body.String())
	}
	if row, _ := store.Get("a"); row.Name != "beta" || row.Version != 2 {
		t.Fatalf("resource not updated: %+v", row)
	}

	r = httptest.NewRequest(http.MethodGet, "/api/v2/probe/guarded/a", nil)
	r.Header.Add("If-None-Match", stale.String())
	r.Header.Add("If-None-Match", RenderETag(guardedProbeScope, "a", 2).String())
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("two If-None-Match lines: status %d body %s", rec.Code, rec.Body.String())
	}
}

// The create-only probe: If-None-Match: * creates a new resource, refuses an
// existing one with 412 and its ETag, and an absent field replaces.
func TestCreateOnlyProbe(t *testing.T) {
	h, store := guardedHandler(t)
	rec := do(t, h, http.MethodPut, "/api/v2/probe/created/new", `{"name":"fresh"}`, map[string]string{"If-None-Match": "*"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status %d body %s", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("ETag"), RenderETag(guardedProbeScope, "new", 1).String(); got != want {
		t.Fatalf("create ETag = %q, want %q", got, want)
	}
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/new", `{"name":"again"}`, map[string]string{"If-None-Match": "*"})
	requireProblem(t, rec, TypePreconditionFailed)
	if got, want := rec.Header().Get("ETag"), RenderETag(guardedProbeScope, "new", 1).String(); got != want {
		t.Fatalf("412 ETag = %q, want %q", got, want)
	}
	if row, _ := store.Get("new"); row.Name != "fresh" || row.Version != 1 {
		t.Fatalf("resource changed on refused create: %+v", row)
	}
	// A weak match against the existing tag also refuses.
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/new", `{"name":"again"}`, map[string]string{"If-None-Match": "W/" + RenderETag(guardedProbeScope, "new", 1).String()})
	requireProblem(t, rec, TypePreconditionFailed)
	// No field: an ordinary replace.
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/new", `{"name":"replaced"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: status %d body %s", rec.Code, rec.Body.String())
	}
	if row, _ := store.Get("new"); row.Name != "replaced" || row.Version != 2 {
		t.Fatalf("resource not replaced: %+v", row)
	}
	// No field, racing a concurrent writer: the request supplied no
	// precondition, so nothing can fail it; the replace lands on top.
	store.Upsert("new", "intruder")
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/new", `{"name":"after-race"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("unconditional replace after a race: status %d body %s", rec.Code, rec.Body.String())
	}
	if row, _ := store.Get("new"); row.Name != "after-race" || row.Version != 4 {
		t.Fatalf("resource after race: %+v", row)
	}
	// Malformed: 400 without echo.
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/new", `{"name":"x"}`, map[string]string{"If-None-Match": "not-a-tag"})
	p := requireProblem(t, rec, TypeMalformedRequest)
	if strings.Contains(p.Detail, "not-a-tag") {
		t.Fatalf("malformed value echoed: %q", p.Detail)
	}
}

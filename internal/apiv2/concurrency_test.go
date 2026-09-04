package apiv2

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The guarded probe through the real router: every outcome the contract's
// "Optimistic concurrency" section ratifies, in the order a handler evaluates
// them (load, precondition, domain state, compare-and-update).

func guardedHandler(t *testing.T) (http.Handler, *guardedProbeStore) {
	t.Helper()
	store := newGuardedProbeStore()
	return NewHandler(Dependencies{testRegister: registerGuardedProbes(store)}), store
}

func TestGuardedProbeMissingIfMatchIs428(t *testing.T) {
	h, store := guardedHandler(t)
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, nil)
	p := requireProblem(t, rec, TypePreconditionRequired)
	if !strings.Contains(p.Detail, "If-Match") {
		t.Fatalf("detail does not name If-Match: %q", p.Detail)
	}
	if rec.Header().Get("ETag") != "" {
		t.Fatal("428 must not carry an ETag; the client fetches the representation first")
	}
	if row, _ := store.Get("a"); row.Name != "alpha" || row.Version != 1 {
		t.Fatalf("resource changed: %+v", row)
	}
}

func TestGuardedProbeStaleIfMatchIs412WithETag(t *testing.T) {
	h, store := guardedHandler(t)
	current := RenderETag(1, guardedProbeScope)
	stale := RenderETag(0, guardedProbeScope)
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"If-Match": stale.String()})
	p := requireProblem(t, rec, TypePreconditionFailed)
	if got := rec.Header().Get("ETag"); got != current.String() {
		t.Fatalf("ETag = %q, want current %q", got, current.String())
	}
	if p.Instance != "urn:silo:request:"+requestIDHeader(rec) {
		t.Fatalf("instance %q", p.Instance)
	}
	if row, _ := store.Get("a"); row.Name != "alpha" || row.Version != 1 {
		t.Fatalf("resource changed on 412: %+v", row)
	}
}

func TestGuardedProbeExactMatchUpdatesAndReturnsNewETag(t *testing.T) {
	h, store := guardedHandler(t)
	before := RenderETag(1, guardedProbeScope)
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"If-Match": before.String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	after := RenderETag(2, guardedProbeScope)
	if got := rec.Header().Get("ETag"); got != after.String() || got == before.String() {
		t.Fatalf("ETag = %q, want %q", got, after.String())
	}
	if row, _ := store.Get("a"); row.Name != "beta" || row.Version != 2 {
		t.Fatalf("row = %+v", row)
	}
	// The old tag is now stale.
	rec = do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"gamma"}`, map[string]string{"If-Match": before.String()})
	requireProblem(t, rec, TypePreconditionFailed)
	if got := rec.Header().Get("ETag"); got != after.String() {
		t.Fatalf("412 ETag = %q, want %q", got, after.String())
	}
}

func TestGuardedProbeWildcardOverwrites(t *testing.T) {
	h, store := guardedHandler(t)
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"If-Match": "*"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if row, _ := store.Get("a"); row.Name != "beta" {
		t.Fatalf("row = %+v", row)
	}
}

func TestGuardedProbeWeakTagNeverMatches(t *testing.T) {
	h, store := guardedHandler(t)
	current := RenderETag(1, guardedProbeScope)
	weak := EntityTag{Opaque: current.Opaque, Weak: true}
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"If-Match": weak.String()})
	requireProblem(t, rec, TypePreconditionFailed)
	if got := rec.Header().Get("ETag"); got != current.String() {
		t.Fatalf("ETag = %q", got)
	}
	if row, _ := store.Get("a"); row.Name != "alpha" {
		t.Fatalf("row = %+v", row)
	}
}

func TestGuardedProbeUnknownIdIs404BeforePrecondition(t *testing.T) {
	h, _ := guardedHandler(t)
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/missing", `{"name":"beta"}`, nil)
	requireProblem(t, rec, TypeNotFound)
	rec = do(t, h, http.MethodDelete, "/api/v2/probe/guarded/missing", "", nil)
	requireProblem(t, rec, TypeNotFound)
}

func TestGuardedProbeConflictAfterValidPrecondition(t *testing.T) {
	h, store := guardedHandler(t)
	current := RenderETag(1, guardedProbeScope)
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/reserved", `{"name":"beta"}`, map[string]string{"If-Match": current.String()})
	requireProblem(t, rec, TypeConflict)
	if row, _ := store.Get("reserved"); row.Name != guardedProbeReservedName || row.Version != 1 {
		t.Fatalf("row = %+v", row)
	}
	// Without the precondition the same request is 428, not 409: the
	// precondition is evaluated before domain state.
	rec = do(t, h, http.MethodPut, "/api/v2/probe/guarded/reserved", `{"name":"beta"}`, nil)
	requireProblem(t, rec, TypePreconditionRequired)
}

func TestGuardedProbeDeleteStaleIs412AndKeepsResource(t *testing.T) {
	h, store := guardedHandler(t)
	current := RenderETag(1, guardedProbeScope)
	rec := do(t, h, http.MethodDelete, "/api/v2/probe/guarded/a", "", map[string]string{"If-Match": RenderETag(9, guardedProbeScope).String()})
	requireProblem(t, rec, TypePreconditionFailed)
	if got := rec.Header().Get("ETag"); got != current.String() {
		t.Fatalf("ETag = %q", got)
	}
	if _, ok := store.Get("a"); !ok {
		t.Fatal("resource deleted on 412")
	}
	rec = do(t, h, http.MethodDelete, "/api/v2/probe/guarded/a", "", nil)
	requireProblem(t, rec, TypePreconditionRequired)
	rec = do(t, h, http.MethodDelete, "/api/v2/probe/guarded/a", "", map[string]string{"If-Match": current.String()})
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
		t.Fatalf("delete: %d %q ct %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	if _, ok := store.Get("a"); ok {
		t.Fatal("resource survived a matching delete")
	}
	rec = do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", nil)
	requireProblem(t, rec, TypeNotFound)
}

func TestGuardedProbeConditionalRead(t *testing.T) {
	h, _ := guardedHandler(t)
	current := RenderETag(1, guardedProbeScope)
	rec := do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != current.String() || !strings.Contains(rec.Body.String(), `"alpha"`) {
		t.Fatalf("plain read: %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	for _, field := range []string{current.String(), "W/" + current.String(), RenderETag(7, guardedProbeScope).String() + ", " + current.String(), "*"} {
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
	rec = do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", map[string]string{"If-None-Match": RenderETag(7, guardedProbeScope).String()})
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

// TestGuardedProbeLostRaceIs412: the compare-and-update's ErrStaleVersion
// (another writer got in between load and write) maps to the same 412 with
// the version that won.
func TestGuardedProbeLostRaceIs412(t *testing.T) {
	store := newGuardedProbeStore()
	h := NewHandler(Dependencies{testRegister: registerGuardedProbes(store)})
	current := RenderETag(1, guardedProbeScope)
	// Simulate the race: the store advances after the handler would have
	// loaded version 1 by handing it a tag for version 1 while the row is
	// already at version 2.
	if _, err := store.Update("a", 1, "racer"); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"If-Match": current.String()})
	requireProblem(t, rec, TypePreconditionFailed)
	if got := rec.Header().Get("ETag"); got != RenderETag(2, guardedProbeScope).String() {
		t.Fatalf("ETag = %q", got)
	}
	if row, _ := store.Get("a"); row.Name != "racer" {
		t.Fatalf("row = %+v", row)
	}
}

// TestStaleVersionSentinelFromStore: the store's compare-and-update returns
// the sentinel, not a bespoke error.
func TestStaleVersionSentinelFromStore(t *testing.T) {
	store := newGuardedProbeStore()
	if _, err := store.Update("a", 5, "x"); err != ErrStaleVersion { //nolint:errorlint // sentinel identity is the contract
		t.Fatalf("Update: %v", err)
	}
	if err := store.Delete("a", 5); err != ErrStaleVersion { //nolint:errorlint // sentinel identity is the contract
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Update("missing", 1, "x"); err != ErrStaleVersion { //nolint:errorlint // sentinel identity is the contract
		t.Fatalf("Update missing: %v", err)
	}
}

// Repeated header lines are one list (RFC 9110 5.3): the router joins them
// before Huma binds the input, so a tag on the second line still matches.
func TestGuardedProbeRepeatedHeaderLinesAreOneList(t *testing.T) {
	h, store := guardedHandler(t)
	current := RenderETag(1, guardedProbeScope)
	stale := RenderETag(0, guardedProbeScope)

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
	r.Header.Add("If-None-Match", RenderETag(2, guardedProbeScope).String())
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
	if got, want := rec.Header().Get("ETag"), RenderETag(1, guardedProbeScope).String(); got != want {
		t.Fatalf("create ETag = %q, want %q", got, want)
	}
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/new", `{"name":"again"}`, map[string]string{"If-None-Match": "*"})
	requireProblem(t, rec, TypePreconditionFailed)
	if got, want := rec.Header().Get("ETag"), RenderETag(1, guardedProbeScope).String(); got != want {
		t.Fatalf("412 ETag = %q, want %q", got, want)
	}
	if row, _ := store.Get("new"); row.Name != "fresh" || row.Version != 1 {
		t.Fatalf("resource changed on refused create: %+v", row)
	}
	// A weak match against the existing tag also refuses.
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/new", `{"name":"again"}`, map[string]string{"If-None-Match": "W/" + RenderETag(1, guardedProbeScope).String()})
	requireProblem(t, rec, TypePreconditionFailed)
	// No field: an ordinary replace.
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/new", `{"name":"replaced"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: status %d body %s", rec.Code, rec.Body.String())
	}
	if row, _ := store.Get("new"); row.Name != "replaced" || row.Version != 2 {
		t.Fatalf("resource not replaced: %+v", row)
	}
	// Malformed: 400 without echo.
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/new", `{"name":"x"}`, map[string]string{"If-None-Match": "not-a-tag"})
	p := requireProblem(t, rec, TypeMalformedRequest)
	if strings.Contains(p.Detail, "not-a-tag") {
		t.Fatalf("malformed value echoed: %q", p.Detail)
	}
}

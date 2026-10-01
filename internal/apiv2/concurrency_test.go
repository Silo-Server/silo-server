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

func TestGuardedProbeStaleIfMatchIs412WithETag(t *testing.T) {
	h, store := guardedHandler(t)
	current := RenderETag(guardedProbeScope, "a", 1)
	stale := RenderETag(guardedProbeScope, "a", 0)
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
	before := RenderETag(guardedProbeScope, "a", 1)
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"If-Match": before.String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	after := RenderETag(guardedProbeScope, "a", 2)
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
	current := RenderETag(guardedProbeScope, "a", 1)
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
	current := RenderETag(guardedProbeScope, "reserved", 1)
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
	current := RenderETag(guardedProbeScope, "a", 1)
	rec := do(t, h, http.MethodDelete, "/api/v2/probe/guarded/a", "", map[string]string{"If-Match": RenderETag(guardedProbeScope, "a", 9).String()})
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

// TestGuardedProbeLostRaceIs412: the compare-and-update's ErrStaleVersion
// (another writer got in between load and write) maps to the same 412 with
// the version that won.
func TestGuardedProbeLostRaceIs412(t *testing.T) {
	store := newGuardedProbeStore()
	h := NewHandler(Dependencies{testRegister: registerGuardedProbes(store)})
	current := RenderETag(guardedProbeScope, "a", 1)
	// Simulate the race: the store advances after the handler would have
	// loaded version 1 by handing it a tag for version 1 while the row is
	// already at version 2.
	if _, err := store.Update("a", 1, "racer"); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"If-Match": current.String()})
	requireProblem(t, rec, TypePreconditionFailed)
	if got := rec.Header().Get("ETag"); got != RenderETag(guardedProbeScope, "a", 2).String() {
		t.Fatalf("ETag = %q", got)
	}
	if row, _ := store.Get("a"); row.Name != "racer" {
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

// TestGuardedProbeWildcardRetryRechecksIfNoneMatchAndDomain: under
// If-Match: * a lost race is retried, but the retry judges If-None-Match
// and the domain rule again against the latest row, so a writer that
// installs the tag If-None-Match names, or the reserved state, turns the
// retry into 412 or 409 instead of an overwrite.
func TestGuardedProbeWildcardRetryRechecksIfNoneMatchAndDomain(t *testing.T) {
	// If-None-Match names version 2; the racer moves the row to version 2.
	store := newGuardedProbeStore()
	h := NewHandler(Dependencies{testRegister: registerGuardedProbes(store)})
	store.raceNextGets(1, func() { store.Upsert("a", "racer") })
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"If-Match": "*", "If-None-Match": RenderETag(guardedProbeScope, "a", 2).String()})
	requireProblem(t, rec, TypePreconditionFailed)
	if got := rec.Header().Get("ETag"); got != RenderETag(guardedProbeScope, "a", 2).String() {
		t.Fatalf("412 ETag = %q", got)
	}
	if row, _ := store.Get("a"); row.Name != "racer" || row.Version != 2 {
		t.Fatalf("row overwritten despite If-None-Match: %+v", row)
	}
	// The racer turns the row into the reserved state.
	store = newGuardedProbeStore()
	h = NewHandler(Dependencies{testRegister: registerGuardedProbes(store)})
	store.raceNextGets(1, func() { store.Upsert("a", guardedProbeReservedName) })
	rec = do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"If-Match": "*"})
	requireProblem(t, rec, TypeConflict)
	if row, _ := store.Get("a"); row.Name != guardedProbeReservedName {
		t.Fatalf("reserved row overwritten: %+v", row)
	}
}

// TestGuardedProbeWildcardDeleteRetryRechecksIfNoneMatch: a DELETE under
// If-Match: * that loses its compare-and-delete judges If-None-Match again
// against the latest row, so a writer that installs the named tag turns the
// retry into 412 and the resource survives.
func TestGuardedProbeWildcardDeleteRetryRechecksIfNoneMatch(t *testing.T) {
	store := newGuardedProbeStore()
	h := NewHandler(Dependencies{testRegister: registerGuardedProbes(store)})
	store.raceNextGets(1, func() { store.Upsert("a", "racer") })
	rec := do(t, h, http.MethodDelete, "/api/v2/probe/guarded/a", "", map[string]string{"If-Match": "*", "If-None-Match": RenderETag(guardedProbeScope, "a", 2).String()})
	requireProblem(t, rec, TypePreconditionFailed)
	if got := rec.Header().Get("ETag"); got != RenderETag(guardedProbeScope, "a", 2).String() {
		t.Fatalf("412 ETag = %q", got)
	}
	if row, exists := store.Get("a"); !exists || row.Version != 2 {
		t.Fatalf("resource deleted despite If-None-Match: %+v %v", row, exists)
	}
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

// TestCreateOnlyIfMatchOnlyStaysOnCASPath: a create-only PUT that supplies
// If-Match but no If-None-Match is not an unconditional replace: a writer
// landing after the load makes an exact tag 412 with the newer ETag, makes
// "*" after a delete 412 with no recreate, and lets "*" after an update
// re-apply against the latest version.
func TestCreateOnlyIfMatchOnlyStaysOnCASPath(t *testing.T) {
	current := RenderETag(guardedProbeScope, "a", 1).String()
	store := newGuardedProbeStore()
	h := NewHandler(Dependencies{testRegister: registerGuardedProbes(store)})
	store.raceNextGets(1, func() { store.Upsert("a", "racer") })
	rec := do(t, h, http.MethodPut, "/api/v2/probe/created/a", `{"name":"mine"}`, map[string]string{"If-Match": current})
	requireProblem(t, rec, TypePreconditionFailed)
	if got := rec.Header().Get("ETag"); got != RenderETag(guardedProbeScope, "a", 2).String() {
		t.Fatalf("412 ETag = %q", got)
	}
	if row, _ := store.Get("a"); row.Name != "racer" {
		t.Fatalf("exact If-Match overwrote after a race: %+v", row)
	}
	store = newGuardedProbeStore()
	h = NewHandler(Dependencies{testRegister: registerGuardedProbes(store)})
	store.raceNextGets(1, func() {
		if err := store.Delete("a", 1); err != nil {
			t.Errorf("race setup: %v", err)
		}
	})
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/a", `{"name":"mine"}`, map[string]string{"If-Match": "*"})
	requireProblem(t, rec, TypePreconditionFailed)
	if rec.Header().Get("ETag") != "" {
		t.Fatal("412 after a winning delete must carry no ETag")
	}
	if _, exists := store.Get("a"); exists {
		t.Fatal("If-Match: * recreated a deleted resource")
	}
	store = newGuardedProbeStore()
	h = NewHandler(Dependencies{testRegister: registerGuardedProbes(store)})
	store.raceNextGets(1, func() { store.Upsert("a", "racer") })
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/a", `{"name":"mine"}`, map[string]string{"If-Match": "*"})
	if rec.Code != http.StatusOK {
		t.Fatalf("If-Match: * after an update: %d %s", rec.Code, rec.Body.String())
	}
	if row, _ := store.Get("a"); row.Name != "mine" || row.Version != 3 {
		t.Fatalf("row = %+v", row)
	}
}

// TestGuardedProbeWildcardSurvivesLostRace: "*" is a deliberate overwrite,
// so a writer that lands between the load and the compare-and-update does
// not turn it into a 412 while the resource still exists.
func TestGuardedProbeWildcardSurvivesLostRace(t *testing.T) {
	store := newGuardedProbeStore()
	h := NewHandler(Dependencies{testRegister: registerGuardedProbes(store)})
	store.raceNextGets(1, func() {
		if _, err := store.Update("a", 1, "racer"); err != nil {
			t.Errorf("race setup: %v", err)
		}
	})
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"If-Match": "*"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if row, _ := store.Get("a"); row.Name != "beta" || row.Version != 3 {
		t.Fatalf("row = %+v", row)
	}
	if got := rec.Header().Get("ETag"); got != RenderETag(guardedProbeScope, "a", 3).String() {
		t.Fatalf("ETag = %q", got)
	}
	// An exact tag in the same race is still 412: the caller named a version.
	store.raceNextGets(1, func() {
		if _, err := store.Update("a", 3, "racer"); err != nil {
			t.Errorf("race setup: %v", err)
		}
	})
	rec = do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"gamma"}`, map[string]string{"If-Match": RenderETag(guardedProbeScope, "a", 3).String()})
	requireProblem(t, rec, TypePreconditionFailed)
	if got := rec.Header().Get("ETag"); got != RenderETag(guardedProbeScope, "a", 4).String() {
		t.Fatalf("412 ETag = %q", got)
	}
}

// TestGuardedProbeWildcardDeleteSurvivesRepeatedRaces: "*" on a DELETE keeps
// reloading and retrying while the resource exists, however many writers
// land in the window.
func TestGuardedProbeWildcardDeleteSurvivesRepeatedRaces(t *testing.T) {
	store := newGuardedProbeStore()
	h := NewHandler(Dependencies{testRegister: registerGuardedProbes(store)})
	// Each Get (the handler's load, then each reload after a lost race)
	// is followed by another writer bumping the version.
	store.raceNextGets(3, func() {
		// Bump unconditionally: Upsert never consumes a race slot the way
		// a Get inside the hook would.
		store.Upsert("a", "racer")
	})
	rec := do(t, h, http.MethodDelete, "/api/v2/probe/guarded/a", "", map[string]string{"If-Match": "*"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, exists := store.Get("a"); exists {
		t.Fatal("resource survived a wildcard delete")
	}
}

// TestCreateOnlyProbeReEvaluatesAfterRace: a create-only PUT that loses the
// compare-and-update evaluates If-None-Match again against the latest state
// and retries while the condition holds; it is 412 only when the latest
// state falsifies the condition, and a winning delete leaves no ETag.
func TestCreateOnlyProbeReEvaluatesAfterRace(t *testing.T) {
	// Non-matching tag: another writer bumps the row, but the tag still does
	// not match the new version, so the replace lands.
	store := newGuardedProbeStore()
	h := NewHandler(Dependencies{testRegister: registerGuardedProbes(store)})
	store.raceNextGets(1, func() {
		if _, err := store.Update("a", 1, "racer"); err != nil {
			t.Errorf("race setup: %v", err)
		}
	})
	rec := do(t, h, http.MethodPut, "/api/v2/probe/created/a", `{"name":"after-race"}`, map[string]string{"If-None-Match": RenderETag(guardedProbeScope, "a", 99).String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("non-matching tag after a race: status %d body %s", rec.Code, rec.Body.String())
	}
	if row, _ := store.Get("a"); row.Name != "after-race" || row.Version != 3 {
		t.Fatalf("row = %+v", row)
	}
	// "*" on a missing id: another writer creates it first, so the latest
	// state falsifies the condition and the answer is 412 with its ETag.
	store = newGuardedProbeStore()
	h = NewHandler(Dependencies{testRegister: registerGuardedProbes(store)})
	store.raceNextGets(1, func() {
		if _, err := store.Create("new", "intruder"); err != nil {
			t.Errorf("race setup: %v", err)
		}
	})
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/new", `{"name":"mine"}`, map[string]string{"If-None-Match": "*"})
	requireProblem(t, rec, TypePreconditionFailed)
	if got := rec.Header().Get("ETag"); got != RenderETag(guardedProbeScope, "new", 1).String() {
		t.Fatalf("412 ETag = %q", got)
	}
	// Non-matching tag on an existing id: the winner deletes the row, so
	// the condition holds against the empty state and the create lands.
	store = newGuardedProbeStore()
	h = NewHandler(Dependencies{testRegister: registerGuardedProbes(store)})
	store.raceNextGets(1, func() {
		if err := store.Delete("a", 1); err != nil {
			t.Errorf("race setup: %v", err)
		}
	})
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/a", `{"name":"recreated"}`, map[string]string{"If-None-Match": RenderETag(guardedProbeScope, "a", 99).String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("create after a winning delete: status %d body %s", rec.Code, rec.Body.String())
	}
	if row, _ := store.Get("a"); row.Name != "recreated" || row.Version != 2 {
		t.Fatalf("row = %+v", row)
	}
}

// TestETagNeverValidatesAnotherResource: two resources at the same version
// have different validators, so a tag copied from one neither authorizes a
// mutation of the other under If-Match nor answers 304 for it under
// If-None-Match.
func TestETagNeverValidatesAnotherResource(t *testing.T) {
	h, store := guardedHandler(t)
	store.Upsert("b", "bravo")
	rowA, _ := store.Get("a")
	rowB, _ := store.Get("b")
	if rowA.Version != rowB.Version {
		t.Fatalf("setup: versions differ %d vs %d", rowA.Version, rowB.Version)
	}
	tagA := RenderETag(guardedProbeScope, "a", rowA.Version)
	if tagA == RenderETag(guardedProbeScope, "b", rowB.Version) {
		t.Fatal("two resources at one version share a validator")
	}
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/b", `{"name":"clobber"}`, map[string]string{"If-Match": tagA.String()})
	requireProblem(t, rec, TypePreconditionFailed)
	if row, _ := store.Get("b"); row.Name != "bravo" {
		t.Fatalf("a's tag mutated b: %+v", row)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/probe/guarded/b", "", map[string]string{"If-None-Match": tagA.String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("a's tag answered %d for b", rec.Code)
	}
}

// TestETagNeverRevalidatesAcrossRecreate: a validator minted for a resource
// that was then deleted must not match the resource later recreated at the
// same id, or a stale If-Match could overwrite the new one and a stale
// If-None-Match could answer 304 for a different representation.
func TestETagNeverRevalidatesAcrossRecreate(t *testing.T) {
	h, store := guardedHandler(t)
	old := RenderETag(guardedProbeScope, "a", 1).String()
	rec := do(t, h, http.MethodDelete, "/api/v2/probe/guarded/a", "", map[string]string{"If-Match": old})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPut, "/api/v2/probe/created/a", `{"name":"reborn"}`, map[string]string{"If-None-Match": "*"})
	if rec.Code != http.StatusOK {
		t.Fatalf("recreate: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got == old {
		t.Fatalf("recreated resource reuses the deleted resource's ETag %q", got)
	}
	rec = do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"clobber"}`, map[string]string{"If-Match": old})
	requireProblem(t, rec, TypePreconditionFailed)
	if row, _ := store.Get("a"); row.Name != "reborn" {
		t.Fatalf("stale tag overwrote the recreated resource: %+v", row)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", map[string]string{"If-None-Match": old})
	if rec.Code != http.StatusOK {
		t.Fatalf("stale If-None-Match answered %d for a different representation", rec.Code)
	}
}

// TestGuardedProbeRaceWinnerDeletedOmitsETag: when the compare-and-update
// loses to a delete, the 412 advertises no validator, because there is no
// current representation; "*" cannot re-apply either, since the resource is
// gone.
func TestGuardedProbeRaceWinnerDeletedOmitsETag(t *testing.T) {
	for _, field := range []string{RenderETag(guardedProbeScope, "a", 1).String(), "*"} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			store := newGuardedProbeStore()
			h := NewHandler(Dependencies{testRegister: registerGuardedProbes(store)})
			store.raceNextGets(1, func() {
				if err := store.Delete("a", 1); err != nil {
					t.Errorf("race setup: %v", err)
				}
			})
			body := ""
			if method == http.MethodPut {
				body = `{"name":"beta"}`
			}
			rec := do(t, h, method, "/api/v2/probe/guarded/a", body, map[string]string{"If-Match": field})
			requireProblem(t, rec, TypePreconditionFailed)
			if got := rec.Header().Get("ETag"); got != "" {
				t.Fatalf("%s If-Match %q: 412 after the winner deleted the row advertises ETag %q", method, field, got)
			}
		}
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

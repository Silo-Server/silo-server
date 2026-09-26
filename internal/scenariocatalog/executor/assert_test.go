package executor

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func jsonResponse(t *testing.T, body string) response {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("bad test body %q: %v", body, err)
	}
	return response{Status: 200, Headers: http.Header{}, Raw: []byte(body), Doc: doc, IsJSON: true}
}

func assertion(t *testing.T, spec string) scenariocatalog.BodyAssertion {
	t.Helper()
	var a scenariocatalog.BodyAssertion
	if err := json.Unmarshal([]byte(spec), &a); err != nil {
		t.Fatalf("bad assertion %s: %v", spec, err)
	}
	return a
}

func expectBody(t *testing.T, specs ...string) scenariocatalog.Expect {
	t.Helper()
	exp := scenariocatalog.Expect{Status: 200}
	for _, s := range specs {
		exp.Body = append(exp.Body, assertion(t, s))
	}
	return exp
}

func wantFailure(t *testing.T, failures []string, substr string) {
	t.Helper()
	for _, f := range failures {
		if strings.Contains(f, substr) {
			return
		}
	}
	t.Fatalf("no failure mentioning %q in %v", substr, failures)
}

// TestCollectionPredicatesAreNotVacuous pins the rule that every, none,
// sorted, and unique_by refuse an array too short to test unless the same
// scenario pins that array's size.
func TestCollectionPredicatesAreNotVacuous(t *testing.T) {
	sorted := `{"pointer": "/items", "op": "sorted", "value": {"by": "/id", "direction": "asc"}}`
	unique := `{"pointer": "/items", "op": "unique_by", "value": "/id"}`
	every := `{"pointer": "/items", "op": "every", "value": {"pointer": "/id", "op": "type", "value": "integer"}}`
	none := `{"pointer": "/items", "op": "none", "value": {"pointer": "/id", "op": "equals", "value": 7}}`

	empty := jsonResponse(t, `{"items": []}`)
	for _, spec := range []string{sorted, unique, every, none} {
		failures := check(expectBody(t, spec), empty)
		wantFailure(t, failures, "is vacuous")
	}
	one := jsonResponse(t, `{"items": [{"id": 1}]}`)
	for _, spec := range []string{sorted, unique} {
		wantFailure(t, check(expectBody(t, spec), one), "is vacuous")
	}
	for _, spec := range []string{every, none} {
		if failures := check(expectBody(t, spec), one); len(failures) != 0 {
			t.Fatalf("%s over one element should pass: %v", spec, failures)
		}
	}

	// A same-pointer size predicate says the short array is the point.
	guard := `{"pointer": "/items", "op": "empty"}`
	for _, spec := range []string{sorted, unique, every, none} {
		if failures := check(expectBody(t, guard, spec), empty); len(failures) != 0 {
			t.Fatalf("%s with an empty guard should pass on []: %v", spec, failures)
		}
	}
	// A size predicate on another pointer does not count.
	other := `{"pointer": "/other", "op": "empty"}`
	wantFailure(t, check(expectBody(t, other, sorted), jsonResponse(t, `{"items": [], "other": []}`)), "is vacuous")

	// non_empty pins one element, which satisfies every/none but not an
	// ordering or uniqueness over a single element; length/min_length do.
	nonEmpty := `{"pointer": "/items", "op": "non_empty"}`
	for _, spec := range []string{sorted, unique} {
		wantFailure(t, check(expectBody(t, nonEmpty, spec), one), "is vacuous")
	}
	for _, spec := range []string{every, none} {
		for _, f := range check(expectBody(t, nonEmpty, spec), empty) {
			if strings.Contains(f, "is vacuous") {
				t.Fatalf("%s with a non_empty guard should not be reported vacuous on []: %v", spec, f)
			}
		}
	}
	for _, guard := range []string{`{"pointer": "/items", "op": "length", "value": 1}`, `{"pointer": "/items", "op": "min_length", "value": 1}`} {
		for _, spec := range []string{sorted, unique} {
			if failures := check(expectBody(t, guard, spec), one); len(failures) != 0 {
				t.Fatalf("%s with guard %s should pass on one element: %v", spec, guard, failures)
			}
		}
	}

	// With enough elements the predicates do their real work.
	two := jsonResponse(t, `{"items": [{"id": 2}, {"id": 1}]}`)
	wantFailure(t, check(expectBody(t, sorted), two), "not sorted")
	dup := jsonResponse(t, `{"items": [{"id": 1}, {"id": 1}]}`)
	wantFailure(t, check(expectBody(t, unique), dup), "share")
	hit := jsonResponse(t, `{"items": [{"id": 1}, {"id": 7}]}`)
	wantFailure(t, check(expectBody(t, none), hit), "satisfies")
	if failures := check(expectBody(t, none), two); len(failures) != 0 {
		t.Fatalf("none should pass when no element matches: %v", failures)
	}
}

// TestBodyKindIsEnforced pins that the default json body kind rejects a
// non-JSON body and that text exposes the raw bytes and rejects an empty
// body.
func TestBodyKindIsEnforced(t *testing.T) {
	html := response{Status: 200, Headers: http.Header{}, Raw: []byte("<html>oops</html>"), Doc: "<html>oops</html>"}
	wantFailure(t, check(scenariocatalog.Expect{Status: 200}, html), "not JSON")
	wantFailure(t, check(scenariocatalog.Expect{Status: 200, BodyKind: "json"}, html), "not JSON")
	emptyBody := response{Status: 200, Headers: http.Header{}, Doc: ""}
	wantFailure(t, check(scenariocatalog.Expect{Status: 200}, emptyBody), "not JSON")
	wantFailure(t, check(scenariocatalog.Expect{Status: 200, BodyKind: "text"}, emptyBody), "empty, want text")
	if failures := check(scenariocatalog.Expect{Status: 200, BodyKind: "any"}, emptyBody); len(failures) != 0 {
		t.Fatalf("any should accept an empty body: %v", failures)
	}

	// A text body that happens to be valid JSON is still compared as text.
	number := jsonResponse(t, `42`)
	exp := scenariocatalog.Expect{Status: 200, BodyKind: "text", Body: []scenariocatalog.BodyAssertion{assertion(t, `{"pointer": "", "op": "equals", "value": "42"}`)}}
	if failures := check(exp, number); len(failures) != 0 {
		t.Fatalf("text body should be the raw string: %v", failures)
	}
}

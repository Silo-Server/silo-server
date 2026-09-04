package apiv2

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestParseEntityTag(t *testing.T) {
	cases := []struct {
		in   string
		want EntityTag
		bad  bool
	}{
		{in: `"abc"`, want: EntityTag{Opaque: "abc"}},
		{in: `W/"abc"`, want: EntityTag{Opaque: "abc", Weak: true}},
		{in: `""`, want: EntityTag{}},
		{in: "\"\xc3\xa9\"", want: EntityTag{Opaque: "\xc3\xa9"}}, // obs-text bytes
		{in: `"!#$%&'()*+-./:;<=>?@[\]^_{|}~"`, want: EntityTag{Opaque: `!#$%&'()*+-./:;<=>?@[\]^_{|}~`}},
		{in: `abc`, bad: true},                        // unquoted token
		{in: `"abc"x`, bad: true},                     // trailing garbage
		{in: `"abc" `, bad: true},                     // whitespace is the list parser's job
		{in: `"a"b"`, bad: true},                      // DQUOTE inside
		{in: `"a b"`, bad: true},                      // SP is not etagc
		{in: `"a,b"`, want: EntityTag{Opaque: "a,b"}}, // comma is etagc: quotes delimit list elements
		{in: "\"a\tb\"", bad: true},                   // HTAB is not etagc
		{in: "\"a\x7fb\"", bad: true},                 // DEL is not etagc
		{in: `w/"abc"`, bad: true},                    // weak prefix is case-sensitive (%s"W/")
		{in: `W/abc`, bad: true},                      // weak prefix without quotes
		{in: `"`, bad: true},                          // lone quote
		{in: ``, bad: true},                           // empty
		{in: `*`, bad: true},                          // wildcard is not an entity-tag
		{in: `W/`, bad: true},                         // weak prefix alone
		{in: `"abc`, bad: true},                       // unterminated
		{in: ` "abc"`, bad: true},                     // leading whitespace
		{in: `"abc","def"`, bad: true},                // a list is not one tag
		{in: "W/\"\xff\"", want: EntityTag{Opaque: "\xff", Weak: true}},
	}
	for _, c := range cases {
		got, err := ParseEntityTag(c.in)
		if c.bad {
			if !errors.Is(err, ErrMalformedEntityTag) {
				t.Errorf("ParseEntityTag(%q) = %+v, %v; want ErrMalformedEntityTag", c.in, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseEntityTag(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
}

func TestParseETagList(t *testing.T) {
	cases := []struct {
		in   string
		want []EntityTag
		star bool
		bad  bool
	}{
		{in: `*`, star: true},
		{in: ` * `, star: true},
		{in: `"a"`, want: []EntityTag{{Opaque: "a"}}},
		{in: `"a", "b"`, want: []EntityTag{{Opaque: "a"}, {Opaque: "b"}}},
		{in: `"a",W/"b"`, want: []EntityTag{{Opaque: "a"}, {Opaque: "b", Weak: true}}},
		{in: "\t\"a\"\t,  \t\"b\"  ", want: []EntityTag{{Opaque: "a"}, {Opaque: "b"}}}, // OWS
		{in: `,"a",,"b",`, want: []EntityTag{{Opaque: "a"}, {Opaque: "b"}}},            // empty elements
		{in: `, ,`, want: nil},                                                         // all empty: empty list
		{in: ``, want: nil},
		{in: `"a", *`, bad: true}, // "*" only as the whole field
		{in: `*, "a"`, bad: true},
		{in: `**`, bad: true},
		{in: `"a", b`, bad: true},  // unquoted token in a list
		{in: `"a" "b"`, bad: true}, // missing comma
		{in: `"a";"b"`, bad: true},
		{in: `"a" x`, bad: true}, // trailing garbage
		{in: `"a"", "b"`, bad: true},
		{in: `"a,b", W/"c,d"`, want: []EntityTag{{Opaque: "a,b"}, {Opaque: "c,d", Weak: true}}}, // commas inside quotes
	}
	for _, c := range cases {
		tags, star, err := ParseETagList(c.in)
		if c.bad {
			if !errors.Is(err, ErrMalformedEntityTag) {
				t.Errorf("ParseETagList(%q) = %+v, %v, %v; want ErrMalformedEntityTag", c.in, tags, star, err)
			}
			continue
		}
		if err != nil || star != c.star || len(tags) != len(c.want) {
			t.Errorf("ParseETagList(%q) = %+v, %v, %v; want %+v, %v", c.in, tags, star, err, c.want, c.star)
			continue
		}
		for i := range tags {
			if tags[i] != c.want[i] {
				t.Errorf("ParseETagList(%q)[%d] = %+v, want %+v", c.in, i, tags[i], c.want[i])
			}
		}
	}
}

func TestEntityTagComparison(t *testing.T) {
	s := EntityTag{Opaque: "1"}
	w := EntityTag{Opaque: "1", Weak: true}
	other := EntityTag{Opaque: "2"}
	if !StrongMatch(s, s) || StrongMatch(s, w) || StrongMatch(w, w) || StrongMatch(s, other) {
		t.Fatal("strong comparison: only two strong tags with equal text match")
	}
	if !WeakMatch(s, s) || !WeakMatch(s, w) || !WeakMatch(w, w) || WeakMatch(s, other) {
		t.Fatal("weak comparison: equal text matches regardless of weakness")
	}
}

func TestRenderETag(t *testing.T) {
	a := RenderETag(7, "group.editor")
	if a.Weak || a.IsZero() {
		t.Fatalf("rendered tag is not a strong, non-empty tag: %+v", a)
	}
	if got := a.String(); !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Fatalf("String() = %q, want quoted", got)
	}
	back, err := ParseEntityTag(a.String())
	if err != nil || back != a {
		t.Fatalf("rendered tag does not round-trip through the parser: %+v %v", back, err)
	}
	for i := 0; i < len(a.Opaque); i++ {
		if !isETagChar(a.Opaque[i]) {
			t.Fatalf("opaque text %q holds a non-etagc byte", a.Opaque)
		}
	}
	if a != RenderETag(7, "group.editor") {
		t.Fatal("rendering is not stable")
	}
	if a == RenderETag(8, "group.editor") {
		t.Fatal("two versions of one scope share a tag")
	}
	if a == RenderETag(7, "group.admin") {
		t.Fatal("two scopes of one version share a tag")
	}
	if strings.Contains(a.Opaque, "7") && strings.Count(a.Opaque, "7") == 1 && strings.HasSuffix(a.Opaque, ".7") {
		t.Fatalf("opaque text %q exposes the version as plain decimal", a.Opaque)
	}
	seen := map[EntityTag]bool{}
	for v := int64(0); v < 1000; v++ {
		tag := RenderETag(v, "s")
		if seen[tag] {
			t.Fatalf("version %d collides", v)
		}
		seen[tag] = true
	}
}

func TestEvaluateIfMatch(t *testing.T) {
	current := RenderETag(3, "probe")
	stale := RenderETag(2, "probe")
	weakCurrent := EntityTag{Opaque: current.Opaque, Weak: true}
	cases := []struct {
		name     string
		ifMatch  string
		want     ProblemType
		wantETag bool
	}{
		{name: "exact strong match", ifMatch: current.String()},
		{name: "match within a list", ifMatch: stale.String() + ", " + current.String()},
		{name: "wildcard", ifMatch: "*"},
		{name: "absent is 428", ifMatch: "", want: TypePreconditionRequired},
		{name: "stale is 412 with ETag", ifMatch: stale.String(), want: TypePreconditionFailed, wantETag: true},
		{name: "weak tag never matches", ifMatch: weakCurrent.String(), want: TypePreconditionFailed, wantETag: true},
		{name: "unparseable is 400", ifMatch: "abc", want: TypeMalformedRequest},
		{name: "wildcard in a list is 400", ifMatch: `*, ` + current.String(), want: TypeMalformedRequest},
		{name: "all-empty list matches nothing", ifMatch: ", ,", want: TypePreconditionFailed, wantETag: true},
	}
	for _, c := range cases {
		p := EvaluateIfMatch(c.ifMatch, current)
		if c.want.ID == "" {
			if p != nil {
				t.Errorf("%s: got problem %v, want nil", c.name, p)
			}
			continue
		}
		if p == nil || p.Type != c.want.URI() || p.Status != c.want.Status {
			t.Errorf("%s: got %v, want %s", c.name, p, c.want.ID)
			continue
		}
		if got := p.GetHeaders().Get("ETag"); (got != "") != c.wantETag || (c.wantETag && got != current.String()) {
			t.Errorf("%s: ETag header %q, want present=%v", c.name, got, c.wantETag)
		}
		if strings.Contains(p.Detail, c.ifMatch) && c.ifMatch != "" && c.want.ID == TypeMalformedRequest.ID {
			t.Errorf("%s: detail echoes the rejected value: %q", c.name, p.Detail)
		}
		if c.want.ID == TypePreconditionRequired.ID && !strings.Contains(p.Detail, "If-Match") {
			t.Errorf("%s: detail does not name If-Match: %q", c.name, p.Detail)
		}
		if c.want.ID == TypeMalformedRequest.ID && (len(p.Errors) != 1 || p.Errors[0].Location != "header.If-Match") {
			t.Errorf("%s: errors = %+v, want one at header.If-Match", c.name, p.Errors)
		}
	}
}

func TestEvaluateIfNoneMatch(t *testing.T) {
	current := RenderETag(3, "probe")
	weak := EntityTag{Opaque: current.Opaque, Weak: true}
	other := RenderETag(4, "probe")
	for _, c := range []struct {
		name    string
		field   string
		matched bool
		want    ProblemType
	}{
		{name: "absent", field: ""},
		{name: "strong match", field: current.String(), matched: true},
		{name: "weak match", field: weak.String(), matched: true},
		{name: "in a list", field: other.String() + ", " + current.String(), matched: true},
		{name: "wildcard", field: "*", matched: true},
		{name: "no match", field: other.String()},
		{name: "malformed", field: "abc", want: TypeMalformedRequest},
	} {
		matched, p := EvaluateIfNoneMatch(c.field, current)
		if c.want.ID != "" {
			if p == nil || p.Type != c.want.URI() || matched {
				t.Errorf("%s: got %v %v, want %s", c.name, matched, p, c.want.ID)
			} else if p.Errors[0].Location != "header.If-None-Match" {
				t.Errorf("%s: location %q", c.name, p.Errors[0].Location)
			}
			continue
		}
		if p != nil || matched != c.matched {
			t.Errorf("%s: got %v %v, want matched=%v", c.name, matched, p, c.matched)
		}
	}
}

func TestEvaluateCreateOnly(t *testing.T) {
	existing := RenderETag(1, "probe")
	if p := EvaluateCreateOnly("*", nil); p != nil {
		t.Fatalf("no resource: %v", p)
	}
	if p := EvaluateCreateOnly("", &existing); p != nil {
		t.Fatalf("absent field: %v", p)
	}
	p := EvaluateCreateOnly("*", &existing)
	if p == nil || p.Status != http.StatusPreconditionFailed || p.GetHeaders().Get("ETag") != existing.String() {
		t.Fatalf("wildcard against an existing resource: %v", p)
	}
	if p := EvaluateCreateOnly(existing.String(), &existing); p == nil || p.Status != http.StatusPreconditionFailed {
		t.Fatalf("matching tag against an existing resource: %v", p)
	}
	if p := EvaluateCreateOnly(RenderETag(2, "probe").String(), &existing); p != nil {
		t.Fatalf("non-matching tag: %v", p)
	}
	if p := EvaluateCreateOnly("abc", nil); p == nil || p.Status != http.StatusBadRequest {
		t.Fatalf("malformed is 400 even with no resource: %v", p)
	}
}

func TestStaleVersionProblem(t *testing.T) {
	if !errors.Is(ErrStaleVersion, ErrStaleVersion) || ErrStaleVersion.Error() == "" {
		t.Fatal("sentinel")
	}
	cur := RenderETag(9, "probe")
	p := StaleVersionProblem(cur)
	if p.Status != http.StatusPreconditionFailed || p.GetHeaders().Get("ETag") != cur.String() {
		t.Fatalf("with current: %v %v", p, p.GetHeaders())
	}
	if p := StaleVersionProblem(EntityTag{}); p.GetHeaders().Get("ETag") != "" {
		t.Fatal("zero current tag must not render an ETag header")
	}
}

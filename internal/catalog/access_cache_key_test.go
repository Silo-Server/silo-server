package catalog

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

// scopeKey renders one filter's access-scope cache key component.
func scopeKey(filter AccessFilter) string {
	var b strings.Builder
	filter.WriteAccessScopeCacheKey(&b)
	return b.String()
}

// TestAccessScopeCacheKeyCollapsesEquivalentCeilings pins that the key holds
// what the ceiling RESOLVES to rather than the string a caller happened to
// send. The jellycompat browse paths fold a client's free-text
// MaxOfficialRating into MaxContentRating, so keying the raw string would let
// one client mint an unbounded number of process-global entries that all hold
// the same list.
func TestAccessScopeCacheKeyCollapsesEquivalentCeilings(t *testing.T) {
	want := scopeKey(AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}})
	for _, spelling := range []string{"pg13", "PG 13", "pg-13", "US:PG-13", "Rated PG-13 for language"} {
		if got := scopeKey(AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: spelling}}); got != want {
			t.Errorf("ceiling %q keyed as %q, want the same entry as PG-13 (%q)", spelling, got, want)
		}
	}

	// A bare age is a different ceiling from the US tier that contains it: a
	// "13" ceiling admits ages up to 13, while "PG-13" admits its whole US
	// tier up to 14. Different predicates must never share an entry.
	if scopeKey(AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "13"}}) == want {
		t.Error("a bare age 13 ceiling must not share the PG-13 tier's entry")
	}
}

// TestAccessScopeCacheKeySeparatesCeilingStates keeps the three states
// ApplyMaturityLimits branches on — no ceiling, a ceiling that resolves
// to no age, and a resolved age — in distinct cache entries.
func TestAccessScopeCacheKeySeparatesCeilingStates(t *testing.T) {
	none := scopeKey(AccessFilter{})
	blocked := scopeKey(AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "NR"}})
	age := scopeKey(AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "FSK 16"}})

	if none == blocked {
		t.Error("an unusable ceiling blocks everything and must not share the unrestricted entry")
	}
	if blocked == age || none == age {
		t.Error("a resolved ceiling must have its own entry")
	}

	// Every ceiling the server cannot read renders the same "1 = 0", so they
	// share one entry holding one empty list.
	for _, unusable := range []string{"Unrated", "SPG", " "} {
		if got := scopeKey(AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: unusable}}); got != blocked {
			t.Errorf("ceiling %q keyed as %q, want the blocked entry %q", unusable, got, blocked)
		}
	}
	// A whitespace-only ceiling blocks; it must not key as unrestricted.
	if scopeKey(AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: " "}}) == none {
		t.Error("a whitespace-only ceiling must not share the unrestricted entry")
	}
}

// TestAccessScopeCacheKeySeparatesUnratedSetting keeps the unrated-content
// setting part of the ceiling: the same ceiling admits different rows under
// each value, so a list warmed under one must not be served under the other.
func TestAccessScopeCacheKeySeparatesUnratedSetting(t *testing.T) {
	hide := scopeKey(AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}})
	allow := scopeKey(AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13", AllowUnratedContent: true}})
	if hide == allow {
		t.Error("access.unrated_content changes which rows the ceiling admits and must key separately")
	}
}

// TestAccessScopeCacheKeyCoversEveryMaturityLimit guards the list above against
// drift: each maturity limit changes which rows a viewer sees, so setting any
// one of them, including one added later, must change the key.
func TestAccessScopeCacheKeyCoversEveryMaturityLimit(t *testing.T) {
	base := scopeKey(AccessFilter{})
	for _, limits := range nonZeroMaturityLimits(t) {
		if scopeKey(AccessFilter{MaturityLimits: limits.value}) == base {
			t.Errorf("setting MaturityLimits.%s does not change the access-scope cache key", limits.field)
		}
	}
	// Different advisory limits admit different rows.
	if scopeKey(AccessFilter{MaturityLimits: access.MaturityLimits{MaxAdvisoryAge: 10}}) ==
		scopeKey(AccessFilter{MaturityLimits: access.MaturityLimits{MaxAdvisoryAge: 13}}) {
		t.Error("advisory limits 10 and 13 must key separately")
	}
}

type namedMaturityLimits struct {
	field string
	value access.MaturityLimits
}

// nonZeroMaturityLimits returns one MaturityLimits per field with only that
// field set to a restricting value.
func nonZeroMaturityLimits(t *testing.T) []namedMaturityLimits {
	t.Helper()
	typ := reflect.TypeOf(access.MaturityLimits{})
	out := make([]namedMaturityLimits, 0, typ.NumField())
	for i := range typ.NumField() {
		var limits access.MaturityLimits
		field := reflect.ValueOf(&limits).Elem().Field(i)
		switch field.Kind() {
		case reflect.String:
			field.SetString("PG")
		case reflect.Bool:
			field.SetBool(true)
		case reflect.Int:
			field.SetInt(10)
		default:
			t.Fatalf("MaturityLimits.%s has kind %s; teach this test a restricting value", typ.Field(i).Name, field.Kind())
		}
		out = append(out, namedMaturityLimits{field: typ.Field(i).Name, value: limits})
	}
	return out
}

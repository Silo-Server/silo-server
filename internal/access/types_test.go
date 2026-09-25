package access

import (
	"encoding/json"
	"testing"
)

// The JSON of a Scope is hashed into access fingerprints (event-socket tickets,
// progress bootstrap snapshots). Moving the maturity fields into the embedded
// MaturityLimits must not change that JSON for a scope without an advisory-age
// limit, or a deploy would invalidate every in-flight fingerprint at once.
func TestScopeJSONKeepsMaturityKeysFlat(t *testing.T) {
	encoded, err := json.Marshal(Scope{UserID: 1, MaturityLimits: MaturityLimits{MaxContentRating: "PG", AllowUnratedContent: true}})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["MaxContentRating"] != "PG" || fields["AllowUnratedContent"] != true {
		t.Fatalf("maturity fields are not flattened: %s", encoded)
	}
	for _, key := range []string{"MaturityLimits", "MaxAdvisoryAge"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("scope without an advisory limit encodes %q: %s", key, encoded)
		}
	}

	limited, err := json.Marshal(Scope{UserID: 1, MaturityLimits: MaturityLimits{MaxAdvisoryAge: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if string(limited) == string(mustMarshal(t, Scope{UserID: 1})) {
		t.Fatal("an advisory-age limit must change the scope JSON")
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestHidesUnadvised pins the one place the strict flag is read: it needs a
// limit to mean anything, so a stored flag on a profile with no limit hides
// nothing.
func TestHidesUnadvised(t *testing.T) {
	for _, tt := range []struct {
		limits MaturityLimits
		want   bool
	}{
		{MaturityLimits{}, false},
		{MaturityLimits{MaxAdvisoryAge: 10}, false},
		{MaturityLimits{RequireAdvisoryAge: true}, false},
		{MaturityLimits{MaxAdvisoryAge: 10, RequireAdvisoryAge: true}, true},
	} {
		if got := tt.limits.HidesUnadvised(); got != tt.want {
			t.Errorf("%+v.HidesUnadvised() = %v, want %v", tt.limits, got, tt.want)
		}
	}
	// Active is unchanged by the flag alone: it tightens a limit, it is not one.
	if (MaturityLimits{RequireAdvisoryAge: true}).Active() {
		t.Error("a require flag with no limit must not make the limits active")
	}
}

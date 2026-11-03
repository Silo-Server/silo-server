package catalog

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

// TestApplyMaturityLimitsAdvisoryAge pins the advisory predicate: it admits a
// title with no advisory age, binds after the ceiling, and never binds when an
// unusable ceiling already blocks everything.
func TestApplyMaturityLimitsAdvisoryAge(t *testing.T) {
	apply := func(limits access.MaturityLimits) ([]string, []any, int) {
		var conditions []string
		var args []any
		argIdx := 3
		ApplyMaturityLimits("ece", AccessFilter{MaturityLimits: limits}, &conditions, &args, &argIdx)
		return conditions, args, argIdx
	}

	conditions, args, next := apply(access.MaturityLimits{MaxAdvisoryAge: 10})
	if !slices.Equal(conditions, []string{"(ece.advisory_age IS NULL OR ece.advisory_age <= $3)"}) || !slices.Equal(args, []any{10}) || next != 4 {
		t.Fatalf("advisory only: %v %v %d", conditions, args, next)
	}

	conditions, args, next = apply(access.MaturityLimits{MaxContentRating: "PG", MaxAdvisoryAge: 10})
	want := []string{
		"(ece.content_rating_age IS NOT NULL AND ece.content_rating_age <= $3)",
		"(ece.advisory_age IS NULL OR ece.advisory_age <= $4)",
	}
	if !slices.Equal(conditions, want) || !slices.Equal(args, []any{8, 10}) || next != 5 {
		t.Fatalf("ceiling and advisory: %v %v %d", conditions, args, next)
	}

	// access.unrated_content governs the ceiling only; a title with no
	// advisory age always passes the advisory limit.
	conditions, _, _ = apply(access.MaturityLimits{MaxAdvisoryAge: 10, AllowUnratedContent: true})
	if !slices.Equal(conditions, []string{"(ece.advisory_age IS NULL OR ece.advisory_age <= $3)"}) {
		t.Fatalf("unrated setting changed the advisory predicate: %v", conditions)
	}

	// Requiring an advisory age turns the limit fail-closed: a title with no
	// advisory age is hidden, the same shape a ceiling gives an unrated title.
	conditions, args, next = apply(access.MaturityLimits{MaxAdvisoryAge: 10, RequireAdvisoryAge: true})
	if !slices.Equal(conditions, []string{"(ece.advisory_age IS NOT NULL AND ece.advisory_age <= $3)"}) || !slices.Equal(args, []any{10}) || next != 4 {
		t.Fatalf("required advisory: %v %v %d", conditions, args, next)
	}
	// The flag means nothing without a limit: no predicate, no bind.
	conditions, args, next = apply(access.MaturityLimits{RequireAdvisoryAge: true})
	if len(conditions) != 0 || len(args) != 0 || next != 3 {
		t.Fatalf("required advisory without a limit: %v %v %d", conditions, args, next)
	}

	conditions, args, next = apply(access.MaturityLimits{MaxContentRating: "NR", MaxAdvisoryAge: 10})
	if !slices.Equal(conditions, []string{"1 = 0"}) || len(args) != 0 || next != 3 {
		t.Fatalf("blocked ceiling: %v %v %d", conditions, args, next)
	}
}

package smartcoll

import "testing"

func TestNormalize_DefaultsMatchToAll(t *testing.T) {
	q := QueryDefinition{}
	n := q.Normalize()
	if n.Match != "all" {
		t.Errorf("Match = %q, want all", n.Match)
	}
}

func TestNormalize_LowercaseAndTrimsFields(t *testing.T) {
	q := QueryDefinition{
		Match: "  ALL ",
		Groups: []QueryGroup{{Match: "  Any ", Rules: []QueryRule{{Field: "  Title ", Op: "  IS ", Value: "x"}}}},
	}
	n := q.Normalize()
	if n.Match != "all" || n.Groups[0].Match != "any" {
		t.Errorf("match normalization broken: %+v", n)
	}
	if n.Groups[0].Rules[0].Field != "title" || n.Groups[0].Rules[0].Op != "is" {
		t.Errorf("rule normalization broken: %+v", n.Groups[0].Rules[0])
	}
}

func TestNormalize_AppliesFieldAliases(t *testing.T) {
	for raw, want := range map[string]string{"authors": "author", "narrators": "narrator", "genres": "genre"} {
		q := QueryDefinition{Groups: []QueryGroup{{Rules: []QueryRule{{Field: raw, Op: "is", Value: "x"}}}}}
		n := q.Normalize()
		if n.Groups[0].Rules[0].Field != want {
			t.Errorf("alias %q -> %q, got %q", raw, want, n.Groups[0].Rules[0].Field)
		}
	}
}

func TestNormalize_DedupesAndSortsLibraryIDs(t *testing.T) {
	q := QueryDefinition{LibraryIDs: []int64{3, 1, 2, 1, 3}}
	n := q.Normalize()
	want := []int64{1, 2, 3}
	if len(n.LibraryIDs) != 3 {
		t.Fatalf("LibraryIDs = %v, want %v", n.LibraryIDs, want)
	}
	for i, id := range want {
		if n.LibraryIDs[i] != id {
			t.Errorf("LibraryIDs[%d] = %d, want %d", i, n.LibraryIDs[i], id)
		}
	}
}

func TestNormalizeSort_DefaultsField(t *testing.T) {
	s := NormalizeSort(QuerySort{})
	if s.Field != "added_at" {
		t.Errorf("default sort.field = %q, want added_at", s.Field)
	}
}

func TestNormalizeSort_DefaultOrderPerField(t *testing.T) {
	if NormalizeSort(QuerySort{Field: "title"}).Order != "asc" {
		t.Errorf("default order for 'title' should be 'asc'")
	}
	if NormalizeSort(QuerySort{Field: "added_at"}).Order != "desc" {
		t.Errorf("default order for 'added_at' should be 'desc'")
	}
}

func TestValidate_RejectsUnknownField(t *testing.T) {
	q := QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "nonsense", Op: "is", Value: 1}}}}}
	if err := q.Validate(true); err == nil {
		t.Errorf("expected error for unknown field")
	}
}

func TestValidate_RejectsInvalidOpForField(t *testing.T) {
	q := QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "title", Op: "between", Value: []any{1, 2}}}}}}
	if err := q.Validate(true); err == nil {
		t.Errorf("expected error for invalid op on title")
	}
}

func TestValidate_PersonalizedWithoutScope(t *testing.T) {
	q := QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "finished", Op: "is", Value: true}}}}}
	if err := q.Validate(false); err == nil {
		t.Errorf("expected error for personalized without scope")
	}
}

func TestValidate_PersonalizedWithScope(t *testing.T) {
	q := QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "finished", Op: "is", Value: true}}}}}
	if err := q.Validate(true); err != nil {
		t.Errorf("expected no error with scope, got %v", err)
	}
}

func TestValidate_RejectsBadMatch(t *testing.T) {
	q := QueryDefinition{Match: "maybe"}
	if err := q.Validate(true); err == nil {
		t.Errorf("expected error for bad top-level match")
	}
}

func TestValidate_RejectsBadSort(t *testing.T) {
	q := QueryDefinition{Match: "all", Sort: QuerySort{Field: "nonsense"}}
	if err := q.Validate(true); err == nil {
		t.Errorf("expected error for unknown sort field")
	}
}

func TestValidate_RejectsNegativeLimit(t *testing.T) {
	limit := -1
	q := QueryDefinition{Match: "all", Limit: &limit}
	if err := q.Validate(true); err == nil {
		t.Errorf("expected error for negative limit")
	}
}

package smartcoll

import "testing"

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

package pgstore

import "testing"

func TestCompactMediaItemIDsReturnsEmptySliceForNilInput(t *testing.T) {
	ids := compactMediaItemIDs(nil)
	if ids == nil {
		t.Fatal("compactMediaItemIDs(nil) returned nil, want empty slice for pgx array binding")
	}
	if len(ids) != 0 {
		t.Fatalf("len = %d, want 0", len(ids))
	}
}

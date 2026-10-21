package handlers

import "testing"

// Imports accept any positive item limit: a synced collection has no item cap
// of its own.
func TestValidateOptionalLimit(t *testing.T) {
	if err := validateOptionalLimit(nil); err != nil {
		t.Fatalf("validateOptionalLimit(nil) = %v, want nil", err)
	}
	for _, limit := range []int{1, 500, 501, 10000} {
		if err := validateOptionalLimit(&limit); err != nil {
			t.Fatalf("validateOptionalLimit(%d) = %v, want nil", limit, err)
		}
	}
	for _, limit := range []int{0, -1} {
		if err := validateOptionalLimit(&limit); err == nil {
			t.Fatalf("validateOptionalLimit(%d) accepted a non-positive limit", limit)
		}
	}
}

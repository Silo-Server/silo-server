package contractledger

import (
	"testing"
)

// TestEverySectionIsAssignedAndNonEmpty pins the Phase 4 delivery units: every
// ledger row names a section, and the assignment script reproduces the
// committed file byte for byte (run scripts/apiv2-ledger/assign_sections.py
// --check in CI through make verify-migration-ledger).
func TestEverySectionIsAssignedAndNonEmpty(t *testing.T) {
	l, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, e := range l.Entries {
		if e.Section == "" {
			t.Errorf("entry without a section: %s", e.key())
			continue
		}
		counts[e.Section]++
	}
	if len(counts) == 0 {
		t.Fatal("no sections")
	}
	// A section PR must stay reviewable: the plan's sizing rule caps a section
	// well below a whole wave. Raise deliberately, with a split, not by nudging.
	const maxRows = 40
	for name, n := range counts {
		if n > maxRows {
			t.Errorf("section %q has %d rows; split it (limit %d)", name, n, maxRows)
		}
	}
}

package access

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
)

// TestGroupAuditRecordsProfileLimit checks that changing only a group's
// profile limit leaves an audit change for it.
func TestGroupAuditRecordsProfileLimit(t *testing.T) {
	before := &Group{ID: 2, Name: "Guests", MaxProfiles: 5}
	after := &Group{ID: 2, Name: "Guests", MaxProfiles: 2}
	changes := auditmutation.Changes(groupAuditValues(before), groupAuditValues(after))
	if len(changes) != 1 || changes[0].Field != "max_profiles" {
		t.Fatalf("changes = %+v, want one max_profiles change", changes)
	}
	if changes[0].Before == nil || *changes[0].Before != "5" || changes[0].After == nil || *changes[0].After != "2" {
		t.Fatalf("max_profiles change = %+v, want 5 -> 2", changes[0])
	}
}

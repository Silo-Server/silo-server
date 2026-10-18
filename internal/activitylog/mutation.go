package activitylog

import "github.com/Silo-Server/silo-server/internal/auditmutation"

type Change = auditmutation.Change

// WithoutDetails projects the frozen v1 audit wire shape.
func WithoutDetails(entry AuditEntry) AuditEntry {
	entry.Action, entry.TargetType, entry.TargetID, entry.Changes = "", "", "", nil
	return entry
}

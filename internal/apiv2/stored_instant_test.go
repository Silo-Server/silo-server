package apiv2

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStoredInstantOnlyOmitsAbsentValues(t *testing.T) {
	if got, p := storedInstant(""); got != nil || p != nil {
		t.Fatalf("absent timestamp = %v, %v", got, p)
	}
	for _, raw := range []string{"2026-01-02T03:04:05.678Z", "2026-01-02 03:04:05.678+00", "2026-01-02 03:04:05.678+00:00", "2026-01-02 03:04:05.678"} {
		got, p := storedInstant(raw)
		if p != nil || got == nil {
			t.Fatalf("valid stored timestamp %q = %v, %v", raw, got, p)
		}
		wire, err := json.Marshal(got)
		if err != nil || string(wire) != `"2026-01-02T03:04:05.678Z"` {
			t.Fatalf("timestamp %q encoded as %s: %v", raw, wire, err)
		}
	}
	for _, raw := range []string{"PRIVATE_CORRUPT_TIMESTAMP", " ", "2026-02-30T00:00:00Z"} {
		got, p := storedInstant(raw)
		if got != nil || p == nil || p.Status != 500 || strings.Contains(p.Detail, "PRIVATE_") {
			t.Fatalf("corrupt stored timestamp %q = %v, %v", raw, got, p)
		}
	}
}

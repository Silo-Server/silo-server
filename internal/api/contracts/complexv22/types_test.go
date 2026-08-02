package complexv22

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTerminateRequestUsesGenerationBoundWireFields(t *testing.T) {
	payload, err := json.Marshal(TerminateRequest{SessionGeneration: "g1", SnapshotID: "s1", ReasonCode: "global_stream_limit", IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"session_generation", "snapshot_id", "reason_code", "idempotency_key"} {
		if !strings.Contains(string(payload), `"`+field+`"`) {
			t.Fatalf("missing %s", field)
		}
	}
}

func TestCapabilitiesAreCanonical(t *testing.T) {
	want := []string{"branding.v1", "sessions.snapshot.v2", "sessions.terminate.v1", "users.identity.v1"}
	if strings.Join(Capabilities, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected capabilities: %v", Capabilities)
	}
}

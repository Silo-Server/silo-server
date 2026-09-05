package contractledger

import (
	"encoding/json"
	"strings"
	"testing"

	apiv2 "github.com/Silo-Server/silo-server/contracts/api/v2"
)

// TestRatifiedOperationsExistInSpec pins the ledger to the committed OpenAPI
// artifact: every ratified row that names a v2 operation must name one the
// spec registers at exactly that method and path, so a ledger row can never
// point clients at an operation the server does not serve.
func TestRatifiedOperationsExistInSpec(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(apiv2.OpenAPI, &spec); err != nil {
		t.Fatal(err)
	}
	type target struct{ method, path string }
	ops := map[string]target{}
	for path, methods := range spec.Paths {
		for method, op := range methods {
			if op.OperationID != "" {
				ops[op.OperationID] = target{strings.ToUpper(method), path}
			}
		}
	}
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	ratified := 0
	for _, e := range ledger.Entries {
		if e.V2.OperationID == nil {
			continue
		}
		if e.ReviewState != ReviewRatified {
			t.Errorf("%s: names v2 operation %q while still %s", e.key(), *e.V2.OperationID, e.ReviewState)
		}
		ratified++
		want, ok := ops[*e.V2.OperationID]
		if !ok {
			t.Errorf("%s: v2 operation %q is not in openapi.json", e.key(), *e.V2.OperationID)
			continue
		}
		if e.V2.Method == nil || e.V2.Path == nil || *e.V2.Method != want.method || *e.V2.Path != want.path {
			t.Errorf("%s: v2 %s %s does not match openapi.json %s %s for %q", e.key(), deref(e.V2.Method), deref(e.V2.Path), want.method, want.path, *e.V2.OperationID)
		}
	}
	if ratified == 0 {
		t.Fatal("no ratified rows name a v2 operation; the pilot slice should")
	}
}

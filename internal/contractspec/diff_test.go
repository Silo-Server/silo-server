package contractspec

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	contracts "github.com/Silo-Server/silo-server/contracts/api/v2"
)

// TestDiffToolIsPinned: oasdiff is a go.mod requirement at an exact release,
// with no replace directive and no pseudo-version, so go.sum's checksum is
// what CI verifies (go mod verify) rather than a downloaded binary.
func TestDiffToolIsPinned(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Path}} {{.Version}} {{if .Replace}}replaced{{end}}", "github.com/oasdiff/oasdiff").Output()
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(out))
	fields := strings.Fields(line)
	if len(fields) != 2 {
		t.Fatalf("oasdiff must be required at an exact version with no replace: %q", line)
	}
	if strings.Contains(fields[1], "-0.") || strings.Count(fields[1], "-") > 0 {
		t.Fatalf("oasdiff must be a tagged release, not a pseudo-version: %q", fields[1])
	}
	sum, err := os.ReadFile("../../go.sum")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sum), "github.com/oasdiff/oasdiff "+fields[1]+" h1:") {
		t.Fatalf("go.sum carries no checksum for oasdiff %s", fields[1])
	}
}

// TestCommittedArtifactIsSelfCompatible: the diff of a document with itself
// is empty, and the committed artifact loads through the diff tool at all.
func TestCommittedArtifactIsSelfCompatible(t *testing.T) {
	changes, err := Diff(contracts.OpenAPI, contracts.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("self-diff is not empty: %+v", changes)
	}
}

// seededBreaking removes getSystemInfo's contract_digest field and drops the
// openapi.json operation: two breaking changes on two operations.
func seededBreaking(t *testing.T) []byte {
	t.Helper()
	return mutate(t, func(doc map[string]any) {
		props := schemas(doc)["SystemInfo"].(map[string]any)["properties"].(map[string]any)
		delete(props, "contract_digest")
		req := schemas(doc)["SystemInfo"].(map[string]any)["required"].([]any)
		var kept []any
		for _, r := range req {
			if r != "contract_digest" {
				kept = append(kept, r)
			}
		}
		schemas(doc)["SystemInfo"].(map[string]any)["required"] = kept
		delete(doc["paths"].(map[string]any), "/api/v2/openapi.json")
	})
}

// TestSeededBreakingChangeIsDetected proves the pinned tool detects the
// seeded fixture; a tool upgrade that stops detecting it fails here.
func TestSeededBreakingChangeIsDetected(t *testing.T) {
	changes, err := Diff(contracts.OpenAPI, seededBreaking(t))
	if err != nil {
		t.Fatal(err)
	}
	var breaking []Change
	for _, c := range changes {
		if c.Breaking {
			breaking = append(breaking, c)
		}
	}
	if len(breaking) == 0 {
		t.Fatalf("seeded fixture produced no breaking change: %+v", changes)
	}
	ids := map[string]bool{}
	for _, c := range breaking {
		ids[c.ID] = true
		if c.Fingerprint == "" || len(c.Fingerprint) != 64 {
			t.Errorf("change without fingerprint: %+v", c)
		}
	}
	if !ids["api-removed-without-deprecation"] && !ids["api-path-removed-without-deprecation"] {
		t.Errorf("operation removal not reported as breaking; ids: %v", ids)
	}
	if !ids["response-required-property-removed"] && !ids["response-property-removed"] {
		t.Errorf("required response property removal not reported as breaking; ids: %v", ids)
	}
	// Pre-lock without approvals: fail. With the exact entries: pass. Locked:
	// fail even with approvals.
	if err := Policy(changes, &ApprovalsFile{}, false); !errors.Is(err, ErrBreaking) {
		t.Fatalf("unapproved breaking changes passed: %v", err)
	}
	var approvals ApprovalsFile
	for _, c := range breaking {
		approvals.Approvals = append(approvals.Approvals, Approval{OperationID: c.OperationID, ChangeID: c.ID, Fingerprint: c.Fingerprint, Reason: "seeded test approval, exact", ApprovedIn: "#0"})
	}
	if err := Policy(changes, &approvals, false); err != nil {
		t.Fatalf("exactly approved changes failed: %v", err)
	}
	if err := Policy(changes, &approvals, true); !errors.Is(err, ErrBreaking) {
		t.Fatalf("locked contract accepted an approval: %v", err)
	}
	// A wrong operation id or change id on an otherwise matching fingerprint
	// does not approve.
	wrong := ApprovalsFile{Approvals: []Approval{{OperationID: "someOtherOp", ChangeID: breaking[0].ID, Fingerprint: breaking[0].Fingerprint}}}
	if err := Policy(changes[:0], &wrong, false); err == nil {
		t.Fatal("stale approval was accepted")
	}
	if err := Policy(changes, &wrong, false); !errors.Is(err, ErrBreaking) {
		t.Fatalf("mismatched approval was accepted: %v", err)
	}
}

// TestCommittedApprovalsLoad: the committed allowlist validates.
func TestCommittedApprovalsLoad(t *testing.T) {
	if _, err := LoadApprovals(contracts.FS); err != nil {
		t.Fatal(err)
	}
}

// TestApprovalEntryMustBeExact: a prose-only entry, a wildcard, and other
// inexact entries are refused by the schema or the loader.
func TestApprovalEntryMustBeExact(t *testing.T) {
	schemaBytes, err := contracts.FS.ReadFile(ApprovalsSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	good := map[string]any{
		"operation_id": "getSystemInfo",
		"change_id":    "response-required-property-removed",
		"fingerprint":  strings.Repeat("ab", 32),
		"reason":       "Clients coordinated in the linked issue.",
		"approved_in":  "#123",
	}
	cases := map[string]map[string]any{
		"prose only":            {"reason": "Approved by the maintainers; see the PR."},
		"wildcard operation":    with(good, "operation_id", "*"),
		"glob operation":        with(good, "operation_id", "get*"),
		"wildcard change":       with(good, "change_id", "*"),
		"wildcard fingerprint":  with(good, "fingerprint", "*"),
		"short fingerprint":     with(good, "fingerprint", "abcd"),
		"empty reason":          with(good, "reason", ""),
		"unknown member":        with(good, "pattern", "/api/v2/**"),
		"missing fingerprint":   without(good, "fingerprint"),
		"missing approved_in":   without(good, "approved_in"),
		"approved_in not a ref": with(good, "approved_in", "later"),
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"approvals": []any{entry}})
			if _, err := ParseApprovals(raw, schemaBytes); err == nil {
				t.Fatalf("entry was accepted: %s", raw)
			}
		})
	}
	raw, _ := json.Marshal(map[string]any{"approvals": []any{good}})
	if _, err := ParseApprovals(raw, schemaBytes); err != nil {
		t.Fatalf("exact entry refused: %v", err)
	}
	raw, _ = json.Marshal(map[string]any{"approvals": []any{good, good}})
	if _, err := ParseApprovals(raw, schemaBytes); err == nil {
		t.Fatal("duplicate fingerprint accepted")
	}
}

func with(m map[string]any, k string, v any) map[string]any {
	out := map[string]any{}
	for kk, vv := range m {
		out[kk] = vv
	}
	out[k] = v
	return out
}

func without(m map[string]any, k string) map[string]any {
	out := with(m, k, nil)
	delete(out, k)
	return out
}

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	contracts "github.com/Silo-Server/silo-server/contracts/api/v2"
	"github.com/Silo-Server/silo-server/internal/contractspec"
)

// The command owns file, approval and lock-marker handling. A small real
// OpenAPI document exercises its semantic diff without repeating the committed
// artifact coverage in internal/contractspec.
const commandOpenAPI = `{
  "openapi": "3.1.0",
  "info": {"title": "Command policy fixture", "version": "2"},
  "paths": {
    "/api/v2/system/info": {
      "get": {"operationId": "getSystemInfo", "responses": {"200": {"description": "System info"}}}
    },
    "/api/v2/openapi.json": {
      "get": {"operationId": "getOpenAPIDocument", "responses": {"200": {"description": "OpenAPI document"}}}
    }
  }
}`

// TestRunPolicy drives the command end to end on a seeded breaking revision:
// pre-lock without approval fails, with the exact approval passes, and the
// LOCKED marker fails regardless of approvals.
func TestRunPolicy(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.json")
	if err := os.WriteFile(base, []byte(commandOpenAPI), 0o600); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(commandOpenAPI), &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc["paths"].(map[string]any), "/api/v2/openapi.json")
	revBytes, _ := json.Marshal(doc)
	revision := filepath.Join(dir, "revision.json")
	if err := os.WriteFile(revision, revBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	contractsDir := filepath.Join(dir, "contracts")
	if err := os.Mkdir(contractsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	schema, _ := contracts.FS.ReadFile(contractspec.ApprovalsSchemaPath)
	if err := os.WriteFile(filepath.Join(contractsDir, contractspec.ApprovalsSchemaPath), schema, 0o600); err != nil {
		t.Fatal(err)
	}
	writeApprovals := func(entries []contractspec.Approval) {
		if entries == nil {
			entries = []contractspec.Approval{}
		}
		raw, _ := json.Marshal(contractspec.ApprovalsFile{Approvals: entries})
		if err := os.WriteFile(filepath.Join(contractsDir, contractspec.ApprovalsPath), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeApprovals(nil)
	if err := run(base, revision, contractsDir); !errors.Is(err, contractspec.ErrBreaking) {
		t.Fatalf("unapproved break passed: %v", err)
	}
	changes, err := contractspec.Diff([]byte(commandOpenAPI), revBytes)
	if err != nil {
		t.Fatal(err)
	}
	var approvals []contractspec.Approval
	for _, c := range changes {
		if c.Breaking {
			approvals = append(approvals, contractspec.Approval{OperationID: c.OperationID, ChangeID: c.ID, Fingerprint: c.Fingerprint, Reason: "Seeded approval for the command test.", ApprovedIn: "#0"})
		}
	}
	writeApprovals(approvals)
	if err := run(base, revision, contractsDir); err != nil {
		t.Fatalf("approved break failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contractsDir, contractspec.LockMarkerPath), []byte("1.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(base, revision, contractsDir); !errors.Is(err, contractspec.ErrBreaking) {
		t.Fatalf("locked contract accepted an approved break: %v", err)
	}
	// No base: nothing to compare.
	if err := run(filepath.Join(dir, "missing.json"), revision, contractsDir); err != nil {
		t.Fatal(err)
	}
}

// TestRunIdenticalDocumentsIgnoresApprovals: a push to the base branch diffs
// the commit against itself, so the approvals the merged pull request
// carried must not be reported as stale there.
func TestRunIdenticalDocumentsIgnoresApprovals(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "openapi.json")
	if err := os.WriteFile(doc, []byte(commandOpenAPI), 0o600); err != nil {
		t.Fatal(err)
	}
	contractsDir := filepath.Join(dir, "contracts")
	if err := os.Mkdir(contractsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	schema, _ := contracts.FS.ReadFile(contractspec.ApprovalsSchemaPath)
	if err := os.WriteFile(filepath.Join(contractsDir, contractspec.ApprovalsSchemaPath), schema, 0o600); err != nil {
		t.Fatal(err)
	}
	approvals := contractspec.ApprovalsFile{Approvals: []contractspec.Approval{{
		OperationID: "getSystemInfo",
		ChangeID:    "response-property-removed",
		Fingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Reason:      "Carried over from a merged pull request.",
		ApprovedIn:  "#0",
	}}}
	raw, _ := json.Marshal(approvals)
	if err := os.WriteFile(filepath.Join(contractsDir, contractspec.ApprovalsPath), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(doc, doc, contractsDir); err != nil {
		t.Fatalf("identical documents with a carried approval failed: %v", err)
	}
	// The same approval against a real, unrelated diff is still stale.
	var mutated map[string]any
	if err := json.Unmarshal([]byte(commandOpenAPI), &mutated); err != nil {
		t.Fatal(err)
	}
	mutated["info"].(map[string]any)["title"] = "changed"
	revBytes, _ := json.Marshal(mutated)
	revision := filepath.Join(dir, "revision.json")
	if err := os.WriteFile(revision, revBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(doc, revision, contractsDir); !errors.Is(err, contractspec.ErrBreaking) {
		t.Fatalf("stale approval against a real diff passed: %v", err)
	}
}

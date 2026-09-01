package contractledger

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	apiv2 "github.com/Silo-Server/silo-server/contracts/api/v2"
)

// TestLedgerMatchesInventory is the CI gate: the committed ledger must satisfy
// its schema and cover the route inventory exactly.
func TestLedgerMatchesInventory(t *testing.T) {
	if err := Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestEveryEntryIsProposedUntilRatified(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Entries) == 0 {
		t.Fatal("ledger has no entries")
	}
	for _, e := range ledger.Entries {
		switch e.ReviewState {
		case "proposed", "ratified", "rejected":
		default:
			t.Errorf("%s: unexpected review_state %q", e.key(), e.ReviewState)
		}
		if e.Tier != 1 && e.Tier != 2 {
			t.Errorf("%s: tier %d", e.key(), e.Tier)
		}
	}
}

func mutatedFS(t *testing.T, mutate func(doc map[string]any)) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	for _, name := range []string{inventoryPath, ledgerPath, schemaPath} {
		data, err := apiv2.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		fsys[name] = &fstest.MapFile{Data: data}
	}
	var doc map[string]any
	if err := json.Unmarshal(fsys[ledgerPath].Data, &doc); err != nil {
		t.Fatal(err)
	}
	mutate(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	fsys[ledgerPath] = &fstest.MapFile{Data: out}
	return fsys
}

func entries(t *testing.T, doc map[string]any) []any {
	t.Helper()
	es, ok := doc["entries"].([]any)
	if !ok || len(es) == 0 {
		t.Fatal("ledger entries missing")
	}
	return es
}

func TestGateFailsWhenAnInventoryRowHasNoEntry(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		es := entries(t, doc)
		doc["entries"] = es[1:]
		doc["totals"].(map[string]any)["entries"] = len(es) - 1
	})
	err := verify(fsys)
	if err == nil || !strings.Contains(err.Error(), "inventory row has no ledger entry") {
		t.Fatalf("expected missing-entry failure, got %v", err)
	}
}

func TestGateFailsWhenAnEntryHasNoInventoryRow(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		es := entries(t, doc)
		extra := map[string]any{}
		for k, v := range es[0].(map[string]any) {
			extra[k] = v
		}
		extra["path"] = "/api/v1/never-registered"
		doc["entries"] = append(es, extra)
		doc["totals"].(map[string]any)["entries"] = len(es) + 1
	})
	err := verify(fsys)
	if err == nil || !strings.Contains(err.Error(), "ledger entry has no inventory row") {
		t.Fatalf("expected orphan-entry failure, got %v", err)
	}
}

func TestGateFailsWhenHandlerDrifts(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		es := entries(t, doc)
		es[0].(map[string]any)["handler"] = "someone.Else"
	})
	err := verify(fsys)
	if err == nil || !strings.Contains(err.Error(), "handler drift") {
		t.Fatalf("expected handler-drift failure, got %v", err)
	}
}

func TestSchemaRejectsAnUnknownDisposition(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		es := entries(t, doc)
		es[0].(map[string]any)["disposition"] = "keep"
	})
	err := verify(fsys)
	if err == nil || !strings.Contains(err.Error(), "violates") {
		t.Fatalf("expected schema failure, got %v", err)
	}
}

func TestSchemaRejectsAnUnknownField(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		es := entries(t, doc)
		es[0].(map[string]any)["owner"] = "nobody"
	})
	err := verify(fsys)
	if err == nil || !strings.Contains(err.Error(), "violates") {
		t.Fatalf("expected schema failure, got %v", err)
	}
}

func TestSchemaRejectsUnusedRowWithCallSites(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		es := entries(t, doc)
		e := es[0].(map[string]any)
		e["consumers"] = []any{"unused"}
		e["consumer_call_sites"] = []any{map[string]any{"repo": "web", "file": "src/x.ts", "line": 1, "types": []any{}, "match": "manual"}}
	})
	err := verify(fsys)
	if err == nil || !strings.Contains(err.Error(), "violates") {
		t.Fatalf("expected schema failure, got %v", err)
	}
}

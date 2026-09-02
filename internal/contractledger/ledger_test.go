package contractledger

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		case ReviewProposed, ReviewRatified, ReviewRejected:
		default:
			t.Errorf("%s: unexpected review_state %q", e.key(), e.ReviewState)
		}
		if e.Tier != 1 && e.Tier != 2 {
			t.Errorf("%s: tier %d", e.key(), e.Tier)
		}
	}
}

// TestRemovedRowsAreTierTwo pins the tier rule stated in the ledger header:
// a removed route has no v2 behavior to baseline, so it never sits in tier 1.
func TestRemovedRowsAreTierTwo(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ledger.Entries {
		if e.Disposition == DispositionRemoved && e.Tier != removedTier {
			t.Errorf("%s: removed row is tier %d", e.key(), e.Tier)
		}
	}
}

// TestRemovalsAndRedesignsNameAnOwner pins the plan's Phase 1 gate line
// "every proposed removal/redesign has an owner and rationale".
func TestRemovalsAndRedesignsNameAnOwner(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ledger.Entries {
		switch e.Disposition {
		case DispositionRemoved, DispositionRedesigned, DispositionReplaced:
			if e.Owner == nil || *e.Owner == "" {
				t.Errorf("%s: %s row has no owner", e.key(), e.Disposition)
			}
		}
	}
}

func mutatedFS(t *testing.T, mutate func(doc map[string]any)) fstest.MapFS {
	t.Helper()
	return mutatedFSWithInventory(t, mutate, nil)
}

// mutatedFSWithInventory copies the embedded artifacts into a MapFS and applies
// the given mutations to the ledger and, when non-nil, to the inventory.
func mutatedFSWithInventory(t *testing.T, mutateLedger func(doc map[string]any), mutateInventory func(doc map[string]any)) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	for _, name := range []string{inventoryPath, ledgerPath, schemaPath} {
		data, err := apiv2.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		fsys[name] = &fstest.MapFile{Data: data}
	}
	apply := func(name string, mutate func(doc map[string]any)) {
		if mutate == nil {
			return
		}
		var doc map[string]any
		if err := json.Unmarshal(fsys[name].Data, &doc); err != nil {
			t.Fatal(err)
		}
		mutate(doc)
		out, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		fsys[name] = &fstest.MapFile{Data: out}
	}
	apply(ledgerPath, mutateLedger)
	apply(inventoryPath, mutateInventory)
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

// entryWhere returns the first ledger entry the predicate accepts.
func entryWhere(t *testing.T, doc map[string]any, pred func(e map[string]any) bool) map[string]any {
	t.Helper()
	for _, raw := range entries(t, doc) {
		e := raw.(map[string]any)
		if pred(e) {
			return e
		}
	}
	t.Fatal("no ledger entry matched the predicate")
	return nil
}

func expectFailure(t *testing.T, fsys fstest.MapFS, want string) {
	t.Helper()
	err := verify(fsys)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected failure containing %q, got %v", want, err)
	}
}

func TestGateFailsWhenAnInventoryRowHasNoEntry(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		es := entries(t, doc)
		doc["entries"] = es[1:]
		doc["totals"].(map[string]any)["entries"] = len(es) - 1
	})
	expectFailure(t, fsys, "inventory row has no ledger entry")
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
	expectFailure(t, fsys, "ledger entry has no inventory row")
}

// TestGateReportsSetProblemsBeforeOrderAndOnlyFirstOrderMismatch pins the
// output shape: a single removed row yields the missing-entry line, then one
// order line, not hundreds of cascading order lines.
func TestGateReportsSetProblemsBeforeOrderAndOnlyFirstOrderMismatch(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		es := entries(t, doc)
		doc["entries"] = append(es[:5:5], es[6:]...)
		doc["totals"].(map[string]any)["entries"] = len(es) - 1
	})
	err := verify(fsys)
	if err == nil {
		t.Fatal("expected failure")
	}
	lines := strings.Split(err.Error(), "\n")
	var missing, order int
	for i, l := range lines {
		switch {
		case strings.Contains(l, "inventory row has no ledger entry"):
			missing = i
		case strings.Contains(l, "must follow inventory order"):
			order++
			if i < missing {
				t.Errorf("order line printed before the set problem:\n%s", err)
			}
		}
	}
	if missing == 0 {
		t.Errorf("missing-entry line absent:\n%s", err)
	}
	if order != 1 {
		t.Errorf("want exactly one order line, got %d:\n%s", order, err)
	}
}

func TestGateFailsWhenHandlerDrifts(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		es := entries(t, doc)
		es[0].(map[string]any)["handler"] = "someone.Else"
	})
	expectFailure(t, fsys, "handler drift")
}

// TestGateFailsWhenAnyCopiedFieldDrifts seeds one drift per copied field, in
// the ledger, and expects the field to be named.
func TestGateFailsWhenAnyCopiedFieldDrifts(t *testing.T) {
	isAPIJSON := func(e map[string]any) bool {
		return e["listener"] == "api" && e["response_media_kind"] == "json" && e["auth_class"] == "acting_admin" && e["conditional"] == true
	}
	cases := []struct {
		field string
		value any
	}{
		{"namespace", "legacy_unversioned"},
		{"handler_kind", "func"},
		{"source_file", "internal/nowhere.go"},
		{"route_group", "/somewhere"},
		{"middleware_chain", 999},
		{"auth_class", "public"},
		{"auth_traits", []any{}},
		{"conditional", false},
		{"conditions", []any{"never"}},
		{"delegates_to", "proxy"},
		{"request_kind", "multipart"},
		{"response_media_kind", "binary"},
		{"streams", true},
		{"upgrades_websocket", true},
		{"profile_required", true},
		{"admin_required", false},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			fsys := mutatedFS(t, func(doc map[string]any) {
				e := entryWhere(t, doc, isAPIJSON)
				e[tc.field] = tc.value
			})
			expectFailure(t, fsys, tc.field+" drift")
		})
	}
}

// TestGateFailsWhenInventoryChangesUnderTheLedger is the regeneration case:
// the inventory moves and the ledger is left behind.
func TestGateFailsWhenInventoryChangesUnderTheLedger(t *testing.T) {
	fsys := mutatedFSWithInventory(t, nil, func(doc map[string]any) {
		for _, raw := range doc["routes"].([]any) {
			r := raw.(map[string]any)
			if r["auth_class"] == "acting_admin" {
				r["auth_class"] = "public"
				r["auth_traits"] = []any{}
				return
			}
		}
		t.Fatal("no acting_admin inventory row")
	})
	expectFailure(t, fsys, "auth_class drift")
}

// TestDynamicProxyOverrideIsTheOnlyMediaKindOverride checks the documented
// override in both directions: allowed under dynamic_plugin_proxy, refused on
// any other row.
func TestDynamicProxyOverrideIsTheOnlyMediaKindOverride(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	var proxies int
	for _, e := range ledger.Entries {
		if e.DispositionRule == dynamicProxyRule {
			proxies++
			if e.RequestKind != dynamicProxyKind || e.ResponseMediaKind != dynamicProxyKind {
				t.Errorf("%s: dynamic_plugin_proxy row has kinds %s/%s", e.key(), e.RequestKind, e.ResponseMediaKind)
			}
		}
	}
	if proxies == 0 {
		t.Fatal("no dynamic_plugin_proxy rows")
	}
	fsys := mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool { return e["disposition_rule"] == "default_ported" })
		e["request_kind"] = dynamicProxyKind
		e["response_media_kind"] = dynamicProxyKind
	})
	expectFailure(t, fsys, "request_kind drift")
}

func TestGateFailsWhenARatifiedRemovalHasAPendingOwner(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool { return e["disposition"] == DispositionRemoved })
		e["review_state"] = ReviewRatified
		e["owner"] = "pending:#135/execution-input-1"
	})
	expectFailure(t, fsys, "ratified without a named owner")
}

// TestGateFailsWhenARatifiedRemovalHasAPlaceholderOwner covers the placeholder
// spellings that satisfy the schema's owner pattern but name nobody.
func TestGateFailsWhenARatifiedRemovalHasAPlaceholderOwner(t *testing.T) {
	// "n/a" and a blank owner already fail the schema's owner pattern; the
	// spellings below pass it and are caught by the review rule.
	for _, owner := range []string{"TBD", "tbd", "todo", "Unknown", "none", "pending", "Pending review"} {
		t.Run(strconv.Quote(owner), func(t *testing.T) {
			fsys := mutatedFS(t, func(doc map[string]any) {
				e := entryWhere(t, doc, func(e map[string]any) bool { return e["disposition"] == DispositionRemoved })
				e["review_state"] = ReviewRatified
				e["owner"] = owner
			})
			expectFailure(t, fsys, "ratified without a named owner")
		})
	}
	// A real name passes the owner rule.
	fsys := mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool { return e["disposition"] == DispositionRemoved })
		e["review_state"] = ReviewRatified
		e["owner"] = "quick104"
	})
	if err := verify(fsys); err != nil && strings.Contains(err.Error(), "ratified without a named owner") {
		t.Fatalf("named owner refused: %v", err)
	}
}

// TestGateFailsWhenARemovedRowIsTierOne pins the tier rule inside the gate
// itself (make verify-migration-ledger), not only in a separate test.
func TestGateFailsWhenARemovedRowIsTierOne(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool { return e["disposition"] == DispositionRemoved })
		e["tier"] = 1
	})
	expectFailure(t, fsys, "removed row is tier 1")
}

// TestGateFailsWhenANonProxyRowClaimsTheDynamicProxyRule: the media-kind
// override is anchored to the inventory handler, so a row cannot opt out of
// drift detection by declaring the rule.
func TestGateFailsWhenANonProxyRowClaimsTheDynamicProxyRule(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool {
			return e["disposition_rule"] == "default_ported" && e["listener"] == "api"
		})
		e["disposition"] = DispositionDocumentedExcluded
		e["disposition_rule"] = dynamicProxyRule
		e["request_kind"] = dynamicProxyKind
		e["response_media_kind"] = dynamicProxyKind
		e["v2"] = map[string]any{"method": nil, "path": nil, "operation_id": nil}
	})
	expectFailure(t, fsys, "dynamic_plugin_proxy rule on a non-proxy handler")
	expectFailure(t, fsys, "request_kind drift")
}

// TestDynamicProxyRowsAreThePluginProxyHandlers pins the anchoring in the
// other direction: every row claiming the rule is one of the two plugin-proxy
// literal handlers.
func TestDynamicProxyRowsAreThePluginProxyHandlers(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ledger.Entries {
		if e.DispositionRule != dynamicProxyRule {
			continue
		}
		if e.Handler != pluginAssetsProxyHandler && e.Handler != pluginPagesProxyHandler {
			t.Errorf("%s: dynamic_plugin_proxy on handler %q", e.key(), e.Handler)
		}
	}
}

// TestCallSiteTypesAreBalanced catches a truncated generic such as
// Partial<T recorded from a nested api<Partial<T>>(...) call.
func TestCallSiteTypesAreBalanced(t *testing.T) {
	doc := ledgerDoc(t)
	for _, raw := range entries(t, doc) {
		e := raw.(map[string]any)
		for _, rs := range e["consumer_call_sites"].([]any) {
			site := rs.(map[string]any)
			for _, tv := range site["types"].([]any) {
				typ := tv.(string)
				if strings.Count(typ, "<") != strings.Count(typ, ">") || strings.Count(typ, "[") != strings.Count(typ, "]") {
					t.Errorf("%s %s %s: %s:%v has unbalanced type %q", e["listener"], e["method"], e["path"], site["file"], site["line"], typ)
				}
			}
		}
	}
}

func ledgerDoc(t *testing.T) map[string]any {
	t.Helper()
	data, err := apiv2.FS.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestSiblingCallSitesResolveAgainstPinnedTrees re-resolves every apple and
// android call site against the commit recorded in source_trees, so a site
// that no longer exists at the pinned tree is reported as wrong, while drift
// of the sibling's origin/main after the pin is not a failure here. The
// sibling checkouts are expected next to this repository's main checkout and
// are read only through git plumbing; the test skips when either checkout or
// the pinned commit is absent (CI has no sibling checkouts).
func TestSiblingCallSitesResolveAgainstPinnedTrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	commonDir, err := exec.Command("git", "rev-parse", "--git-common-dir").Output()
	if err != nil {
		t.Skipf("not in a git checkout: %v", err)
	}
	parent := filepath.Join(strings.TrimSpace(string(commonDir)), "..", "..")
	if !filepath.IsAbs(parent) {
		parent = filepath.Join(mustGetwd(t), parent)
	}
	repos := map[string]string{"apple": "silo-apple", "android": "silo-android"}
	for repo, name := range repos {
		sha := ledger.SourceTrees[name]
		if sha == "" {
			t.Fatalf("source_trees has no pin for %s", name)
		}
		dir := filepath.Join(parent, name)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			t.Skipf("%s checkout not present next to this repository", name)
		}
		if err := exec.Command("git", "-C", dir, "cat-file", "-e", sha+"^{commit}").Run(); err != nil {
			t.Skipf("%s: pinned commit %s not fetched", name, sha)
		}
		for _, e := range ledger.Entries {
			for _, site := range e.ConsumerCallSites {
				if site.Repo != repo {
					continue
				}
				out, err := exec.Command("git", "-C", dir, "show", sha+":"+site.File).Output()
				if err != nil {
					t.Errorf("%s: %s %s:%d does not exist at pinned tree %s (stale against pinned tree)", e.key(), repo, site.File, site.Line, sha[:8])
					continue
				}
				if n := strings.Count(string(out), "\n") + 1; site.Line > n {
					t.Errorf("%s: %s %s:%d is past the end of the file (%d lines) at pinned tree %s", e.key(), repo, site.File, site.Line, n, sha[:8])
				}
			}
		}
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func TestSchemaRejectsRemovalWithoutOwner(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool { return e["disposition"] == DispositionRemoved })
		e["owner"] = nil
	})
	expectFailure(t, fsys, "violates")
}

func TestSchemaRejectsRuleThatDoesNotFitDisposition(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool { return e["disposition"] == DispositionRemoved })
		e["disposition_rule"] = "contract_root_probes"
	})
	expectFailure(t, fsys, "violates")
}

func TestSchemaRejectsRemovedRowWithV2Target(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool { return e["disposition"] == DispositionRemoved })
		e["v2"] = map[string]any{"method": "GET", "path": "/api/v2/x", "operation_id": "getX"}
	})
	expectFailure(t, fsys, "violates")
}

func TestSchemaRejectsRatifiedPortWithoutV2Target(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool { return e["disposition"] == DispositionPorted })
		e["review_state"] = ReviewRatified
	})
	expectFailure(t, fsys, "violates")
}

func TestSchemaRejectsPartiallyNullV2(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool { return e["disposition"] == DispositionPorted })
		e["v2"] = map[string]any{"method": "GET", "path": nil, "operation_id": nil}
	})
	expectFailure(t, fsys, "violates")
}

// TestSchemaRejectsMisRootedCallSite covers each repo's root rule with the
// path shape that was wrong in an earlier revision (an export-directory
// prefix) and a plainly foreign root.
func TestSchemaRejectsMisRootedCallSite(t *testing.T) {
	cases := map[string]string{
		"android": "android/shared/src/commonMain/kotlin/X.kt",
		"apple":   "silo-apple/iosApp/iosApp/X.swift",
		"web":     "web/src/x.ts",
		"server":  "silo-server/internal/x.go",
	}
	for repo, file := range cases {
		t.Run(repo, func(t *testing.T) {
			fsys := mutatedFS(t, func(doc map[string]any) {
				e := entryWhere(t, doc, func(e map[string]any) bool { return e["consumers"].([]any)[0] != "unused" })
				e["consumer_call_sites"] = append(e["consumer_call_sites"].([]any), map[string]any{"repo": repo, "file": file, "line": 1, "types": []any{}, "match": "manual"})
			})
			expectFailure(t, fsys, "violates")
		})
	}
}

func TestSchemaRejectsAnUnknownDisposition(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		es := entries(t, doc)
		es[0].(map[string]any)["disposition"] = "keep"
	})
	expectFailure(t, fsys, "violates")
}

func TestSchemaRejectsAnUnknownRule(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		es := entries(t, doc)
		es[0].(map[string]any)["disposition_rule"] = "frontend_shell"
	})
	expectFailure(t, fsys, "violates")
}

func TestSchemaRejectsAnUnknownField(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		es := entries(t, doc)
		es[0].(map[string]any)["reviewer"] = "nobody"
	})
	expectFailure(t, fsys, "violates")
}

func TestSchemaRejectsUnusedRowWithCallSites(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		es := entries(t, doc)
		e := es[0].(map[string]any)
		e["consumers"] = []any{"unused"}
		e["consumer_call_sites"] = []any{map[string]any{"repo": "web", "file": "src/x.ts", "line": 1, "types": []any{}, "match": "manual"}}
	})
	expectFailure(t, fsys, "violates")
}

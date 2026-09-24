package contractledger

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	apiv2 "github.com/Silo-Server/silo-server/contracts/api/v2"
	apiv2registry "github.com/Silo-Server/silo-server/internal/apiv2"
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

// siteContextLines is how far from a credited line the route's last static
// path segment may sit: a call site's credited line is usually the request
// expression, but the path literal can be an argument a few lines away.
const siteContextLines = 4

// TestSiblingCallSitesResolveAgainstPinnedTrees re-resolves every apple and
// android call site against the commit recorded in source_trees: the file
// must exist at the pinned tree, the line must be inside it, and the route's
// last static path segment must appear within siteContextLines of the
// credited line, so a wrong pin whose files still exist is reported instead
// of passing. A site whose path literal is a constant declared elsewhere
// records path_literal_line, and the segment is looked for there instead; a
// match=follower site is the resolver or allowlist for a server-supplied URL
// and never spells the path, so only its file and line are checked. Drift of
// the sibling's origin/main after the pin is not a failure here. The sibling
// checkouts are expected next to this repository's main checkout and are
// read only through git plumbing. The test skips only when a checkout is
// absent (CI has none); a checkout that is present but lacks the pinned
// commit is a failure, so `make verify-migration-ledger` cannot print ok
// over an unfetched pin.
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
			t.Errorf("%s: pinned commit %s is not fetchable in %s; run `git -C %s fetch origin`", name, sha, dir, dir)
			continue
		}
		for _, e := range ledger.Entries {
			segments := []string{lastStaticSegment(e.Path)}
			// A client that has already moved to the row's v2 operation spells
			// the v2 path at the site, which is what match_consumers.py credits
			// back to this v1 row (its v2-alias rule). Accept either segment so
			// such a site is verified rather than reported as mis-credited.
			if e.V2.Path != nil {
				if s := lastStaticSegment(*e.V2.Path); s != "" && s != segments[0] {
					segments = append(segments, s)
				}
			}
			for _, site := range e.ConsumerCallSites {
				if site.Repo != repo {
					continue
				}
				out, err := exec.Command("git", "-C", dir, "show", sha+":"+site.File).Output()
				if err != nil {
					t.Errorf("%s: %s %s:%d does not exist at pinned tree %s (stale against pinned tree)", e.key(), repo, site.File, site.Line, sha[:8])
					continue
				}
				if problem := checkSiteAtPinnedTree(strings.Split(string(out), "\n"), site, segments...); problem != "" {
					t.Errorf("%s: %s %s at pinned tree %s", e.key(), repo, problem, sha[:8])
				}
			}
		}
	}
}

// checkSiteAtPinnedTree applies the line and content assertions to one site
// given the file's lines at the pinned tree, and returns "" when the site
// holds up. segments are the path segments any one of which satisfies the
// content assertion: the route's last static segment and, for a row that maps
// to a v2 operation, that operation's. No segment, or only empty ones (a path
// such as "/"), disables the content assertion.
func checkSiteAtPinnedTree(lines []string, site CallSite, segments ...string) string {
	if site.Line > len(lines) {
		return fmt.Sprintf("%s:%d is past the end of the file (%d lines)", site.File, site.Line, len(lines))
	}
	if site.PathLiteralLine > len(lines) {
		return fmt.Sprintf("%s:%d path_literal_line %d is past the end of the file (%d lines)", site.File, site.Line, site.PathLiteralLine, len(lines))
	}
	wanted := make([]string, 0, len(segments))
	for _, s := range segments {
		if s != "" {
			wanted = append(wanted, s)
		}
	}
	if len(wanted) == 0 || site.Match == MatchFollower {
		return ""
	}
	// The segment is expected next to the request expression, unless the
	// path is a constant declared elsewhere in the file.
	at := site.Line
	if site.PathLiteralLine != 0 {
		at = site.PathLiteralLine
	}
	quoted := make([]string, 0, len(wanted))
	for _, s := range wanted {
		if mentionsNear(lines, at, siteContextLines, s) {
			return ""
		}
		quoted = append(quoted, strconv.Quote(s))
	}
	return fmt.Sprintf("does not mention %s within %d lines of %s:%d (stale or mis-credited)",
		strings.Join(quoted, " or "), siteContextLines, site.File, at)
}

// lastStaticSegment returns the last path segment that is not a {param} or a
// trailing wildcard, or "" when the path has no static segment (such as "/").
func lastStaticSegment(path string) string {
	segments := strings.Split(path, "/")
	for i := len(segments) - 1; i >= 0; i-- {
		s := segments[i]
		if s == "" || s == "*" || strings.HasPrefix(s, "{") {
			continue
		}
		return s
	}
	return ""
}

// mentionsNear reports whether needle occurs on 1-based line or within
// context lines on either side of it, clamped to the file.
func mentionsNear(lines []string, line, context int, needle string) bool {
	for i := max(line-context, 1); i <= min(line+context, len(lines)); i++ {
		if strings.Contains(lines[i-1], needle) {
			return true
		}
	}
	return false
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

// TestSiteContentAssertionIsEnforced pins checkSiteAtPinnedTree on a
// synthetic file so the pinned-tree rules are exercised without the sibling
// checkouts: a non-follower site whose credited line does not mention the
// segment fails, path_literal_line moves the check to the declaration, and a
// follower site is exempt from the content check but not the line checks.
func TestSiteContentAssertionIsEnforced(t *testing.T) {
	// Line 2 declares the path; line 8 is the request that names the constant
	// and sits more than siteContextLines away. The search is a substring
	// match, so nothing between lines 3 and 12 may contain "sync" (not even
	// "async").
	lines := []string{
		"enum Wire {",
		`    static let endpoint = "/api/v1/notifications/sync"`,
		"}",
		"",
		"",
		"",
		"func run() throws {",
		"    let response = try HTTPClient.shared.get(Wire.endpoint)",
		"    _ = response",
		"}",
	}
	const segment = "sync"
	cases := []struct {
		name string
		site CallSite
		want string // substring of the problem, or "" for no problem
	}{
		{"mechanical site at the literal", CallSite{File: "a.swift", Line: 2, Match: MatchMechanical}, ""},
		{"mechanical site at a call that does not mention the segment", CallSite{File: "a.swift", Line: 8, Match: MatchMechanical}, `does not mention "sync"`},
		{"manual site at a call that does not mention the segment", CallSite{File: "a.swift", Line: 8, Match: MatchManual}, `does not mention "sync"`},
		{"path_literal_line redirects the content check", CallSite{File: "a.swift", Line: 8, PathLiteralLine: 2, Match: MatchMechanical}, ""},
		{"path_literal_line that does not mention the segment", CallSite{File: "a.swift", Line: 8, PathLiteralLine: 7, Match: MatchMechanical}, `does not mention "sync"`},
		{"path_literal_line past the end of the file", CallSite{File: "a.swift", Line: 8, PathLiteralLine: 99, Match: MatchMechanical}, "path_literal_line 99 is past the end"},
		{"follower site is exempt from the content check", CallSite{File: "a.swift", Line: 8, Match: MatchFollower}, ""},
		{"follower site past the end of the file", CallSite{File: "a.swift", Line: 99, Match: MatchFollower}, "is past the end of the file"},
		{"no static segment disables the content check", CallSite{File: "a.swift", Line: 8, Match: MatchMechanical}, ""},
		{"a v2 alias segment satisfies the content check", CallSite{File: "a.swift", Line: 2, Match: MatchMechanical}, ""},
		{"neither the route nor its v2 alias segment is mentioned", CallSite{File: "a.swift", Line: 8, Match: MatchMechanical}, `does not mention "notifications" or "sync"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			segs := []string{segment}
			switch tc.name {
			case "no static segment disables the content check":
				segs = []string{""}
			case "a v2 alias segment satisfies the content check":
				// The route's own segment is absent from the file; the segment
				// of the v2 operation the row maps to is on line 2.
				segs = []string{"status", "sync"}
			case "neither the route nor its v2 alias segment is mentioned":
				segs = []string{"notifications", segment}
			}
			got := checkSiteAtPinnedTree(lines, tc.site, segs...)
			if tc.want == "" && got != "" {
				t.Fatalf("unexpected problem: %s", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("want problem containing %q, got %q", tc.want, got)
			}
		})
	}
}

// TestLastStaticSegment pins the segment the content assertion looks for.
func TestLastStaticSegment(t *testing.T) {
	cases := map[string]string{
		"/":                           "",
		"/api/":                       "api",
		"/api/v1/notifications/sync":  "sync",
		"/api/v1/stream/{session_id}": "stream",
		"/api/v1/stream/{session_id}/subtitles/{track}":       "subtitles",
		"/api/v1/plugins/{installation_id}/*":                 "plugins",
		"/api/v1/playback/transcode/{session_id}/master.m3u8": "master.m3u8",
	}
	for path, want := range cases {
		if got := lastStaticSegment(path); got != want {
			t.Errorf("lastStaticSegment(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestFollowerSitesAreTheStreamURLResolvers pins which sites may carry
// match=follower: only the Apple and Android stream-URL validator/resolver
// on the stream and transcode routes, so the exemption from the content
// assertion cannot quietly spread to sites that ought to spell their path.
func TestFollowerSitesAreTheStreamURLResolvers(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	type location struct {
		repo, file string
		line       int
	}
	allowed := map[location]bool{
		{"apple", "iosApp/iosApp/Screens/Player/StreamRequest.swift", 205}:                                               true,
		{"android", "android-shared/src/androidMain/kotlin/org/siloserver/silo/common/player/SiloPlayerFactory.kt", 731}: true,
	}
	var followers int
	for _, e := range ledger.Entries {
		for _, site := range e.ConsumerCallSites {
			if site.Match != MatchFollower {
				continue
			}
			followers++
			if !strings.HasPrefix(e.Path, "/api/v1/stream/") && !strings.HasPrefix(e.Path, "/api/v1/playback/transcode/") {
				t.Errorf("%s: follower site %s %s:%d on a route that is not a stream or transcode URL", e.key(), site.Repo, site.File, site.Line)
			}
			if !allowed[location{site.Repo, site.File, site.Line}] {
				t.Errorf("%s: unexpected follower site %s %s:%d", e.key(), site.Repo, site.File, site.Line)
			}
		}
	}
	if followers != 14 {
		t.Errorf("want 14 follower sites (2 resolvers on 7 routes), got %d", followers)
	}
}

// TestPathLiteralLineSitesNameTheirLiteral pins the annotated sites: every
// path_literal_line differs from the call line and sits on a mechanical site.
func TestPathLiteralLineSitesNameTheirLiteral(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	var annotated int
	for _, e := range ledger.Entries {
		for _, site := range e.ConsumerCallSites {
			if site.PathLiteralLine == 0 {
				continue
			}
			annotated++
			if site.PathLiteralLine == site.Line {
				t.Errorf("%s: %s %s:%d path_literal_line equals the call line; drop it", e.key(), site.Repo, site.File, site.Line)
			}
			if site.Match != MatchMechanical {
				t.Errorf("%s: %s %s:%d path_literal_line on a %s site", e.key(), site.Repo, site.File, site.Line, site.Match)
			}
		}
	}
	if annotated == 0 {
		t.Fatal("no path_literal_line sites; expected the three constant-declared paths")
	}
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
		e["v2"] = map[string]any{"method": nil, "path": nil, "operation_id": nil}
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

func TestSchemaRejectsAnUnknownMatchKind(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool { return e["consumers"].([]any)[0] != "unused" })
		e["consumer_call_sites"] = append(e["consumer_call_sites"].([]any), map[string]any{"repo": "web", "file": "src/x.ts", "line": 1, "types": []any{}, "match": "guessed"})
	})
	expectFailure(t, fsys, "violates")
}

// TestSchemaAcceptsFollowerAndPathLiteralLine checks the two optional shapes
// pass the schema, and that path_literal_line must be a positive integer.
func TestSchemaAcceptsFollowerAndPathLiteralLine(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool { return e["consumers"].([]any)[0] != "unused" })
		e["consumer_call_sites"] = append(e["consumer_call_sites"].([]any),
			map[string]any{"repo": "web", "file": "src/x.ts", "line": 1, "types": []any{}, "match": "follower"},
			map[string]any{"repo": "web", "file": "src/y.ts", "line": 9, "path_literal_line": 2, "types": []any{}, "match": "mechanical"},
		)
	})
	if err := verify(fsys); err != nil && strings.Contains(err.Error(), "violates") {
		t.Fatalf("follower site or path_literal_line refused by the schema: %v", err)
	}
	fsys = mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool { return e["consumers"].([]any)[0] != "unused" })
		e["consumer_call_sites"] = append(e["consumer_call_sites"].([]any),
			map[string]any{"repo": "web", "file": "src/y.ts", "line": 9, "path_literal_line": 0, "types": []any{}, "match": "mechanical"},
		)
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

// TestConcurrencyMarkingIsRestricted pins where the curated concurrency
// field may appear: ported rows at either tier with a guardable method, and only the
// if_match value.
func TestConcurrencyMarkingIsRestricted(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	marked := 0
	for _, e := range ledger.Entries {
		if e.Concurrency == "" {
			continue
		}
		marked++
		if e.Concurrency != ConcurrencyIfMatch || e.Disposition != DispositionPorted || !isGuardableMethod(concurrencyMethod(e)) {
			t.Errorf("%s: concurrency %q on tier %d %s %s", e.key(), e.Concurrency, e.Tier, e.Disposition, e.Method)
		}
	}
	if marked == 0 {
		t.Fatal("the contract's initial if_match set is not marked")
	}
	isMarked := func(e map[string]any) bool { c, _ := e["concurrency"].(string); return c != "" }
	notMarked := func(e map[string]any) bool { return !isMarked(e) }
	expectFailure(t, mutatedFS(t, func(doc map[string]any) {
		entryWhere(t, doc, isMarked)["concurrency"] = "domain"
	}), "value must be 'if_match'") // the schema refuses it before the review rule runs
	expectFailure(t, mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, isMarked)
		e["disposition"] = DispositionCompatibilityOnly
		e["disposition_rule"] = "maintainer_decision"
		// Drop the retry classification too, so the concurrency placement rule
		// is what fails rather than the schema's retry_safety placement.
		delete(e, "retry_safety")
		delete(e, "retry_safety_note")
	}), "concurrency")
	expectFailure(t, mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool {
			return notMarked(e) && e["method"] == "GET" && e["tier"] == float64(1) && e["disposition"] == "ported"
		})
		e["concurrency"] = ConcurrencyIfMatch
	}), "concurrency")
	expectFailure(t, mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool {
			return notMarked(e) && e["method"] == "POST" && e["tier"] == float64(1) && e["disposition"] == "ported"
		})
		e["concurrency"] = ConcurrencyIfMatch
	}), "concurrency")
}

// guardedWithoutLegacyRow names the guarded v2 operations that port no
// legacy route and so have no ledger row to mark, each with the reason. It
// is empty today; the reconcile test refuses an unmapped guarded operation
// that is not listed here.
var guardedWithoutLegacyRow = map[string]string{}

// TestGuardedOperationsAreMarkedIfMatch reconciles the v2 registry with the
// ledger: every operation registered Guarded must have each legacy row that
// maps to it marked if_match. The test runs against production registrations
// so an implemented guarded operation cannot omit its ledger marking.
func TestGuardedOperationsAreMarkedIfMatch(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	declared := apiv2registry.DeclaredOperations()
	if len(declared) == 0 {
		t.Fatal("the v2 registry declares nothing")
	}
	for _, problem := range concurrencyMismatches(ledger.Entries, declared, guardedWithoutLegacyRow) {
		t.Error(problem)
	}
}

// TestConcurrencyMismatchesFire proves every reconcile branch with a
// synthetic registry and ledger, including mismatches absent from production.
func TestConcurrencyMismatchesFire(t *testing.T) {
	opID := func(s string) *string { return &s }
	v2 := func(id string) V2Target {
		return V2Target{OperationID: opID(id), Method: opID(http.MethodPut), Path: opID("/api/v2/things/{id}")}
	}
	entries := []Entry{
		{copied: copied{Method: http.MethodPut, Path: "/api/v1/things/{id}"}, Tier: 1, Disposition: DispositionPorted, Concurrency: ConcurrencyIfMatch, V2: v2("updateThing")},
		{copied: copied{Method: http.MethodDelete, Path: "/api/v1/things/{id}"}, Tier: 1, Disposition: DispositionPorted, V2: v2("deleteThing")},
		// A redesigned row may name a guarded v2 operation; the review rule
		// keeps the marking off it, so the reconcile must not demand one.
		{copied: copied{Method: http.MethodPut, Path: "/api/v1/old-things/{id}"}, Tier: 1, Disposition: DispositionRedesigned, V2: v2("replaceThing")},
	}
	guarded := func(id string) apiv2registry.Declared {
		return apiv2registry.Declared{Method: http.MethodPut, Path: "/api/v2/things/{id}", OperationID: id, Guarded: true}
	}
	cases := []struct {
		name     string
		declared []apiv2registry.Declared
		exempt   map[string]string
		want     string
	}{
		{"agree", []apiv2registry.Declared{guarded("updateThing")}, nil, ""},
		{"mapped row unmarked", []apiv2registry.Declared{guarded("updateThing"), guarded("deleteThing")}, nil, "is not marked concurrency"},
		{"guarded op with no row", []apiv2registry.Declared{guarded("updateThing"), guarded("newThing")}, nil, "maps to no legacy row"},
		{"guarded op with no row, exempt with reason", []apiv2registry.Declared{guarded("updateThing"), guarded("newThing")}, map[string]string{"newThing": "v2-only resource"}, ""},
		{"guarded op with no row, exempt without reason", []apiv2registry.Declared{guarded("updateThing"), guarded("newThing")}, map[string]string{"newThing": ""}, "maps to no legacy row"},
		{"redesigned row mapped to a guarded op needs no marking", []apiv2registry.Declared{guarded("updateThing"), guarded("replaceThing")}, nil, ""},
		{"marked row, op not guarded", []apiv2registry.Declared{{Method: http.MethodPut, Path: "/api/v2/things/{id}", OperationID: "updateThing"}}, nil, "is not declared Guarded"},
		{"target method disagrees", []apiv2registry.Declared{{Method: http.MethodPatch, Path: "/api/v2/things/{id}", OperationID: "updateThing", Guarded: true}}, nil, "disagree with the registry"},
		{"target path disagrees", []apiv2registry.Declared{{Method: http.MethodPut, Path: "/api/v2/other/{id}", OperationID: "updateThing", Guarded: true}}, nil, "disagree with the registry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := concurrencyMismatches(entries, tc.declared, tc.exempt)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected problems: %v", got)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0], tc.want) {
				t.Fatalf("want one problem containing %q, got %v", tc.want, got)
			}
		})
	}
}

// v2TargetMismatches checks that every ledger row naming a v2 operation the
// registry declares agrees with the declaration on method and path too, so
// a gate keyed by operation id cannot certify a mapping whose other two
// target fields are stale or mistyped.
func v2TargetMismatches(entries []Entry, declared []apiv2registry.Declared) []string {
	byID := map[string]apiv2registry.Declared{}
	for _, op := range declared {
		byID[op.OperationID] = op
	}
	var problems []string
	for _, e := range entries {
		if e.V2.OperationID == nil {
			continue
		}
		op, ok := byID[*e.V2.OperationID]
		if !ok {
			continue
		}
		if e.V2.Method == nil || *e.V2.Method != op.Method || e.V2.Path == nil || *e.V2.Path != op.Path {
			problems = append(problems, fmt.Sprintf("%s: v2 target names operation %s but its method/path (%s %s) disagree with the registry (%s %s)", e.key(), op.OperationID, deref(e.V2.Method), deref(e.V2.Path), op.Method, op.Path))
		}
	}
	return problems
}

// concurrencyMismatches reconciles the ledger's if_match markings with the
// v2 registry in both directions. exempt names guarded operations that port
// no legacy route, each with a reason.
func concurrencyMismatches(entries []Entry, declared []apiv2registry.Declared, exempt map[string]string) []string {
	byOperation := map[string][]Entry{}
	for _, e := range entries {
		if e.V2.OperationID != nil {
			byOperation[*e.V2.OperationID] = append(byOperation[*e.V2.OperationID], e)
		}
	}
	problems := v2TargetMismatches(entries, declared)
	guarded := map[string]bool{}
	for _, op := range declared {
		if !op.Guarded {
			continue
		}
		guarded[op.OperationID] = true
		rows := byOperation[op.OperationID]
		if len(rows) == 0 {
			// Without a mapped row the marking check would pass vacuously. A
			// port records its v2 operation on the legacy row; a v2-only
			// guarded operation with no legacy ancestor names itself in
			// guardedWithoutLegacyRow with the reason.
			if reason := exempt[op.OperationID]; reason != "" {
				continue
			}
			problems = append(problems, fmt.Sprintf("guarded v2 operation %s maps to no legacy row; set v2.operation_id on the row it ports, or record it in guardedWithoutLegacyRow with a reason", op.OperationID))
			continue
		}
		for _, e := range rows {
			// Only a row eligible for the marking (ported at either tier, guardable
			// method) is required to carry it: a redesigned or replaced row
			// may name a guarded v2 operation while the review rule keeps
			// the concurrency field off it, and the two rules must not
			// contradict each other.
			if !eligibleForConcurrency(e) {
				continue
			}
			if e.Concurrency != ConcurrencyIfMatch {
				problems = append(problems, fmt.Sprintf("%s maps to guarded v2 operation %s but is not marked concurrency %s", e.key(), op.OperationID, ConcurrencyIfMatch))
			}
		}
	}
	// The other direction: a row the ledger marks if_match that already names
	// its v2 operation must resolve to a registration declared Guarded, so a
	// port cannot drop the decision by omitting the declaration.
	for _, e := range entries {
		if e.Concurrency != ConcurrencyIfMatch || e.V2.OperationID == nil {
			continue
		}
		if !guarded[*e.V2.OperationID] {
			problems = append(problems, fmt.Sprintf("%s is marked concurrency %s but its v2 operation %s is not declared Guarded", e.key(), ConcurrencyIfMatch, *e.V2.OperationID))
		}
	}
	return problems
}

// TestRetrySafetyPlacement pins where the curated retry_safety field may
// appear: allowed on ported mutations at either tier, required for tier 1
// and mapped v2 operations, one of the seven values, and accompanied by a note for
// idempotency_key and non_retryable.
func TestRetrySafetyPlacement(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ledger.Entries {
		if e.RetrySafety == "" {
			continue
		}
		if !retrySafetyValues[e.RetrySafety] || !eligibleForRetrySafety(e) {
			t.Errorf("%s: retry_safety %q on tier %d %s %s", e.key(), e.RetrySafety, e.Tier, e.Disposition, e.Method)
		}
	}
	// Every tier-1 ported mutation row is classified; there is no allow-list
	// any more, so the ledger itself carries no counter-example.
	for _, e := range ledger.Entries {
		if requiresRetrySafety(e) && e.RetrySafety == "" {
			t.Errorf("%s: tier-1 ported mutation row without retry_safety", e.key())
		}
	}
	// The required direction, exercised directly against a synthetic row.
	unlisted := Entry{Tier: 1, Disposition: DispositionPorted}
	unlisted.Method = http.MethodPost
	unlisted.RouteGroup = "/api/v1/not-a-real-group"
	if got := retrySafetyRules(unlisted.key(), unlisted); len(got) != 1 || !strings.Contains(got[0], "no retry_safety") {
		t.Errorf("unlisted mutation row without retry_safety: got %v", got)
	}
	unlisted.RetrySafety = RetrySafetyNaturalIdempotent
	if got := retrySafetyRules(unlisted.key(), unlisted); len(got) != 0 {
		t.Errorf("classified row: got %v", got)
	}

	isGET1 := func(e map[string]any) bool {
		return e["method"] == "GET" && e["tier"] == float64(1) && e["disposition"] == "ported"
	}
	isMutation1 := func(e map[string]any) bool {
		m, _ := e["method"].(string)
		return isMutatingMethod(m) && e["tier"] == float64(1) && e["disposition"] == "ported"
	}
	isExcluded := func(e map[string]any) bool { return e["disposition"] != "ported" }
	expectFailure(t, mutatedFS(t, func(doc map[string]any) {
		entryWhere(t, doc, isGET1)["retry_safety"] = RetrySafetyNaturalIdempotent
	}), "retry_safety")
	expectFailure(t, mutatedFS(t, func(doc map[string]any) {
		entryWhere(t, doc, isExcluded)["retry_safety"] = RetrySafetyNaturalIdempotent
	}), "retry_safety")
	expectFailure(t, mutatedFS(t, func(doc map[string]any) {
		entryWhere(t, doc, isMutation1)["retry_safety"] = "retry_later"
	}), "retry_safety")
	expectFailure(t, mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, isMutation1)
		e["retry_safety"] = RetrySafetyIdempotencyKey
		delete(e, "retry_safety_note")
	}), "retry_safety_note")
	expectFailure(t, mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, isMutation1)
		e["retry_safety"] = RetrySafetyNonRetryable
		delete(e, "retry_safety_note")
	}), "retry_safety_note")
	expectFailure(t, mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, isMutation1)
		e["retry_safety"] = RetrySafetyNaturalIdempotent
		e["retry_safety_note"] = strings.Repeat("x", retrySafetyNoteMaxLen+1)
	}), "retry_safety_note")
	expectFailure(t, mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, isGET1)
		e["retry_safety_note"] = "a note without a value"
	}), "retry_safety")
	// Go-side rules, reached directly because the schema refuses the same
	// documents first.
	mutation := Entry{Tier: 1, Disposition: DispositionPorted}
	mutation.Method = http.MethodDelete
	mutation.RouteGroup = "/api/v1/not-a-real-group"
	for name, e := range map[string]func(Entry) Entry{
		"unknown value":         func(e Entry) Entry { e.RetrySafety = "retry_later"; return e },
		"key without note":      func(e Entry) Entry { e.RetrySafety = RetrySafetyIdempotencyKey; return e },
		"non-retryable no note": func(e Entry) Entry { e.RetrySafety = RetrySafetyNonRetryable; return e },
		"note without value":    func(e Entry) Entry { e.Tier = 2; e.RetrySafetyNote = "x"; return e },
		"read row":              func(e Entry) Entry { e.Method = "GET"; e.RetrySafety = RetrySafetyNaturalIdempotent; return e },
		"excluded row": func(e Entry) Entry {
			e.Disposition = DispositionRedesigned
			e.RetrySafety = RetrySafetyNaturalIdempotent
			return e
		},
		"long note": func(e Entry) Entry {
			e.RetrySafety = RetrySafetyNaturalIdempotent
			e.RetrySafetyNote = strings.Repeat("x", retrySafetyNoteMaxLen+1)
			return e
		},
	} {
		if got := retrySafetyRules(mutation.key(), e(mutation)); len(got) == 0 {
			t.Errorf("%s: no finding", name)
		}
	}
}

// TestDeclaredRetrySafetyMatchesTheLedger reconciles the v2 registry with the
// ledger: every mutating operation the registry declares must map (through
// v2.operation_id) to at least one classified legacy row, and each such row's
// retry_safety must equal the declaration. Today the registry's mutating set
// is empty (only probes register mutations, and they live in test files), so
// the loop is a no-op; the test still runs against the real registry so the
// first section PR that ports a mutation cannot skip the classification.
func TestDeclaredRetrySafetyMatchesTheLedger(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	declared := apiv2registry.DeclaredOperations()
	if len(declared) == 0 {
		t.Fatal("the v2 registry declares nothing")
	}
	for _, problem := range retrySafetyMismatches(ledger.Entries, declared, mutationWithoutLegacyRow) {
		t.Error(problem)
	}
}

// TestRetrySafetyMismatchesFire proves the reconcile branches with a synthetic
// registry and ledger, since the live registry declares no mutation yet.
func TestRetrySafetyMismatchesFire(t *testing.T) {
	opID := func(s string) *string { return &s }
	entries := []Entry{
		{copied: copied{Method: http.MethodPut, Path: "/api/v1/things/{id}"}, Tier: 1, Disposition: DispositionPorted, RetrySafety: "natural_idempotent", V2: V2Target{OperationID: opID("updateThing")}},
		{copied: copied{Method: http.MethodPost, Path: "/api/v1/things"}, Tier: 1, Disposition: DispositionPorted, RetrySafety: "unique_constraint", V2: V2Target{OperationID: opID("createThing")}},
		// A redesigned row may name a v2 mutation; the schema keeps
		// retry_safety off it, so the reconcile must not compare it.
		{copied: copied{Method: http.MethodPost, Path: "/api/v1/old-things/launch"}, Tier: 1, Disposition: DispositionRedesigned, V2: V2Target{OperationID: opID("launchThing")}},
	}
	update := apiv2registry.Declared{Method: http.MethodPut, OperationID: "updateThing", RetrySafety: "natural_idempotent"}
	create := apiv2registry.Declared{Method: http.MethodPost, OperationID: "createThing", RetrySafety: "unique_constraint"}
	with := func(extra ...apiv2registry.Declared) []apiv2registry.Declared {
		return append([]apiv2registry.Declared{update, create}, extra...)
	}
	cases := []struct {
		name     string
		declared []apiv2registry.Declared
		exempt   map[string]string
		want     string
	}{
		{"agree", with(), nil, ""},
		{"read declares", with(apiv2registry.Declared{Method: http.MethodGet, OperationID: "getThing", RetrySafety: "natural_idempotent"}), nil, "read operation getThing declares retry safety"},
		{"unknown value", []apiv2registry.Declared{{Method: http.MethodPut, OperationID: "updateThing", RetrySafety: "bogus"}, create}, nil, "not one of the ledger's values"},
		{"no legacy row", with(apiv2registry.Declared{Method: http.MethodDelete, OperationID: "deleteThing", RetrySafety: "natural_idempotent"}), nil, "maps to no legacy row"},
		{"no legacy row, exempt with reason", with(apiv2registry.Declared{Method: http.MethodDelete, OperationID: "deleteThing", RetrySafety: "natural_idempotent"}), map[string]string{"deleteThing": "v2-only resource"}, ""},
		{"no legacy row, exempt without reason", with(apiv2registry.Declared{Method: http.MethodDelete, OperationID: "deleteThing", RetrySafety: "natural_idempotent"}), map[string]string{"deleteThing": ""}, "maps to no legacy row"},
		{"disagrees", []apiv2registry.Declared{update, {Method: http.MethodPost, OperationID: "createThing", RetrySafety: "domain_identity"}}, nil, `ledger retry_safety "unique_constraint", registry declares "domain_identity"`},
		{"row names an undeclared operation", []apiv2registry.Declared{update}, nil, "which the registry does not declare"},
		{"row names a read", []apiv2registry.Declared{update, {Method: http.MethodGet, OperationID: "createThing"}}, nil, "carries no retry classification"},
		{"redesigned row mapped to a mutation needs no classification", with(apiv2registry.Declared{Method: http.MethodPost, OperationID: "launchThing", RetrySafety: "domain_identity"}), nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := retrySafetyMismatches(entries, tc.declared, tc.exempt)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected problems: %v", got)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0], tc.want) {
				t.Fatalf("want one problem containing %q, got %v", tc.want, got)
			}
		})
	}
}

// retrySafetyMismatches compares every operation the v2 registry declares
// against the legacy rows mapped to it through v2.operation_id.
// mutationWithoutLegacyRow names the mutating v2 operations that port no
// legacy route and so have no ledger row carrying a retry_safety value, each
// with the reason. The reconcile test refuses an unmapped
// mutation that is not listed here, the same rule guardedWithoutLegacyRow
// applies to concurrency.
var mutationWithoutLegacyRow = map[string]string{
	"cancelAdminJob":                       "V2-only cancellation command for managed background jobs. The job state machine makes repeated requests converge on the same terminal cancellation state.",
	"createAdminStorageTransition":         "V2-only managed artwork storage transition. The active-transition constraint rejects concurrent or replayed starts instead of creating duplicate transition work.",
	"fallbackWatchTogetherSource":          "V2-only coordinated source fallback. The room selection revision and failed file identify one transition under the room lock; replay returns the current snapshot without another source change.",
	"createAdminLogsSocketTicket":          "V2-only administrator log stream handshake delegation: v1 accepted the bearer token in the socket URL directly. Repeated minting grants the same bounded authority through expiring single-use tickets; the legacy log stream GET retains its own mapping.",
	"createWatchTogetherSocketTicket":      "V2-only room handshake delegation: v1 accepted URL login and room credentials directly. Repeated minting grants the same bounded authority through expiring single-use tickets; the legacy room socket GET retains its own mapping.",
	"createPlaybackControlSocketTicket":    "V2-only playback control handshake delegation: v1 accepted a URL bearer token on the socket directly. Repeated minting grants the same owner-bound authority through expiring single-use tickets; the legacy control socket GET retains its own mapping.",
	"createProgressBootstrapSnapshot":      "V2-only full-replacement snapshot admission; request_id identifies one transactional result through expiry plus 24-hour replay retention. This does not port incremental v1 sync reads or progress uploads.",
	"uploadAdminCollectionPoster":          "V2 separates administrator poster upload from legacy multipart definition create and update; those legacy operations retain their own mappings.",
	"uploadAdminCollectionBackdrop":        "V2 separates administrator backdrop upload from legacy multipart definition create and update; those legacy operations retain their own mappings.",
	"uploadCollectionPoster":               "V2 separates poster upload from legacy multipart collection create and update; their legacy rows remain mapped separately.",
	"restartAdminPluginInstallation":       "V2-only resident plugin restart: v1 had no supervised plugin processes, so no legacy route stops and relaunches one. Repeating the restart converges on one running process.",
	"connectNetworkAccess":                 "V2-only network access provider command: v1 had no overlay network providers. Repeating the connect converges on one connected instance per host.",
	"disconnectNetworkAccess":              "V2-only network access provider command: v1 had no overlay network providers. Repeating the disconnect converges on disconnected.",
	"stageWatchTogetherRoomItem":           "V2-only lobby staging: v1 selection always starts playback. Staging replaces the lobby's staged item and is a no-op for identical content; nothing plays until the separate start command.",
	"startWatchTogetherRoomPlayback":       "V2-only start of a staged lobby item: v1 has no lobby/start split. An already playing room answers with its current snapshot, so a duplicate press cannot restart playback.",
	"stopWatchTogetherRoomPlayback":        "V2-only stop that keeps the room: v1 only ends a room. A room that is not playing answers with its current snapshot, so repeating the call cannot disturb the lobby it produced.",
	"updateWatchTogetherRoomSelectionMode": "V2-only lobby mode switch: v1 fixes selection_mode at creation. Repeating the same mode is a no-op; the switch drops the staged item, which is the documented meaning of the value rather than a side effect of retrying.",
	"queryWatchTogetherMemberState":        "V2-only POST-shaped read: the content id set (up to 200) exceeds what a query string carries. It changes no state; repeating it returns the current classification.",
}

// retrySafetyMismatches compares every operation the v2 registry declares
// against the legacy rows mapped to it through v2.operation_id. exempt names
// mutations that port no legacy route, each with a reason.
func retrySafetyMismatches(entries []Entry, declared []apiv2registry.Declared, exempt map[string]string) []string {
	byOperation := map[string][]Entry{}
	for _, e := range entries {
		if e.V2.OperationID != nil {
			byOperation[*e.V2.OperationID] = append(byOperation[*e.V2.OperationID], e)
		}
	}
	// The reverse direction: a classified row that names a v2 operation
	// must resolve to a declared mutating operation, or the ledger would
	// certify a port and its classification that no registration enforces.
	declaredByID := map[string]apiv2registry.Declared{}
	for _, op := range declared {
		declaredByID[op.OperationID] = op
	}
	var problems []string
	for _, e := range entries {
		if e.RetrySafety == "" || e.V2.OperationID == nil {
			continue
		}
		op, ok := declaredByID[*e.V2.OperationID]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s: retry_safety %s names v2 operation %s, which the registry does not declare", e.key(), e.RetrySafety, *e.V2.OperationID))
		case !isMutatingMethod(op.Method):
			problems = append(problems, fmt.Sprintf("%s: retry_safety %s names v2 operation %s, which is a %s and carries no retry classification", e.key(), e.RetrySafety, op.OperationID, op.Method))
		}
	}
	for _, op := range declared {
		if !isMutatingMethod(op.Method) {
			if op.RetrySafety != "" {
				problems = append(problems, fmt.Sprintf("read operation %s declares retry safety %q", op.OperationID, op.RetrySafety))
			}
			continue
		}
		if !retrySafetyValues[string(op.RetrySafety)] {
			problems = append(problems, fmt.Sprintf("mutating operation %s declares retry safety %q, not one of the ledger's values", op.OperationID, op.RetrySafety))
			continue
		}
		rows := byOperation[op.OperationID]
		if len(rows) == 0 {
			if reason := exempt[op.OperationID]; reason != "" {
				continue
			}
			problems = append(problems, fmt.Sprintf("mutating operation %s maps to no legacy row; a ported mutation records its v2 operation and retry_safety in the ledger, and a v2-only mutation names itself in mutationWithoutLegacyRow with a reason", op.OperationID))
		}
		for _, e := range rows {
			// Only a row eligible for the classification (ported
			// mutation) is compared: a redesigned or replaced row may name
			// a v2 mutation while the schema keeps retry_safety off it, and
			// the two rules must not contradict each other.
			if !eligibleForRetrySafety(e) {
				continue
			}
			if e.RetrySafety != string(op.RetrySafety) {
				problems = append(problems, fmt.Sprintf("%s: ledger retry_safety %q, registry declares %q for %s", e.key(), e.RetrySafety, op.RetrySafety, op.OperationID))
			}
		}
	}
	return problems
}

func TestTier2RetrySafety(t *testing.T) {
	// Adding metadata must not alter tier assignments or classify untouched rows.
	fsys := mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool {
			m, _ := e["method"].(string)
			return e["tier"] == float64(2) && e["disposition"] == "ported" && isMutatingMethod(m)
		})
		e["retry_safety"] = RetrySafetyNaturalIdempotent
	})
	if err := verify(fsys); err != nil {
		t.Fatal(err)
	}
	e := Entry{copied: copied{Method: http.MethodPost}, Tier: 2, Disposition: DispositionPorted}
	if got := retrySafetyRules(e.key(), e); len(got) != 0 {
		t.Fatal(got)
	}
	e.V2.OperationID = new("createTier2")
	if got := retrySafetyRules(e.key(), e); len(got) != 1 {
		t.Fatalf("mapped row missing classification: %v", got)
	}
	e.RetrySafety = RetrySafetyUniqueConstraint
	op := apiv2registry.Declared{Method: http.MethodPost, OperationID: "createTier2", RetrySafety: apiv2registry.RetrySafetyUniqueConstraint}
	for _, tc := range []struct {
		name     string
		row      Entry
		declared []apiv2registry.Declared
		want     bool
	}{
		{"matching", e, []apiv2registry.Declared{op}, false},
		{"undeclared target", e, nil, true},
		{"target is read", e, []apiv2registry.Declared{{Method: http.MethodGet, OperationID: op.OperationID}}, true},
		{"registry differs", e, []apiv2registry.Declared{{Method: http.MethodPost, OperationID: op.OperationID, RetrySafety: apiv2registry.RetrySafetyNaturalIdempotent}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := retrySafetyMismatches([]Entry{tc.row}, tc.declared, nil)
			if (len(got) > 0) != tc.want {
				t.Fatal(got)
			}
		})
	}
	e.RetrySafety = ""
	if got := retrySafetyMismatches([]Entry{e}, []apiv2registry.Declared{op}, nil); len(got) == 0 {
		t.Fatal("mapped tier-2 row without metadata accepted")
	}
}

func TestTier2RetrySchemaPreservesNotesAndExclusions(t *testing.T) {
	for _, value := range []string{RetrySafetyNonRetryable, RetrySafetyIdempotencyKey} {
		expectFailure(t, mutatedFS(t, func(doc map[string]any) {
			e := entryWhere(t, doc, func(e map[string]any) bool {
				m, _ := e["method"].(string)
				return e["tier"] == float64(2) && e["disposition"] == "ported" && isMutatingMethod(m)
			})
			e["retry_safety"] = value
			delete(e, "retry_safety_note")
		}), "retry_safety_note")
	}
	expectFailure(t, mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool {
			return e["disposition"] == DispositionDocumentedExcluded
		})
		e["retry_safety"] = RetrySafetyNaturalIdempotent
	}), "retry_safety")
}

func TestExternalWebhookRetryClassification(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range ledger.Entries {
		if e.Path == "/api/v1/webhook-sync/webhooks/{secret}" {
			found = true
			if !requiresRetrySafety(e) || e.RetrySafety != RetrySafetyNonRetryable || e.RetrySafetyNote == "" || e.V2.OperationID == nil || *e.V2.OperationID != "receiveExternalWebhook" {
				t.Fatalf("mapped external ingress lost its non-retryable contract: %+v", e)
			}
			// Unmapped tier-2 ingress still has no native retry requirement.
			e.V2 = V2Target{}
			e.RetrySafety = ""
			e.RetrySafetyNote = ""
			if requiresRetrySafety(e) {
				t.Fatal("unmapped external ingress requires native retry metadata")
			}
		}
	}
	if !found {
		t.Fatal("external webhook ingress absent")
	}
}

func TestTier2ConcurrencyPlacementAndAgreement(t *testing.T) {
	fsys := mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool {
			method, _ := e["method"].(string)
			return e["tier"] == float64(2) && e["disposition"] == DispositionPorted && isGuardableMethod(method)
		})
		e["concurrency"] = ConcurrencyIfMatch
	})
	if err := verify(fsys); err != nil {
		t.Fatalf("tier-2 ported concurrency refused: %v", err)
	}
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			e := Entry{copied: copied{Method: method, Path: "/api/v1/tier2/{id}"}, Tier: 2, Disposition: DispositionPorted, Concurrency: ConcurrencyIfMatch,
				RetrySafety: RetrySafetyNaturalIdempotent, ReviewState: ReviewRatified,
				V2: V2Target{OperationID: new("updateTier2"), Method: new(method), Path: new("/api/v2/tier2/{id}")}}
			if got := reviewRules(e.key(), e, inventoryRoute{}); len(got) != 0 {
				t.Fatalf("ported row without named owner refused: %v", got)
			}
			op := apiv2registry.Declared{Method: method, Path: *e.V2.Path, OperationID: *e.V2.OperationID, Guarded: true}
			if got := concurrencyMismatches([]Entry{e}, []apiv2registry.Declared{op}, nil); len(got) != 0 {
				t.Fatal(got)
			}
			unmarked := e
			unmarked.Concurrency = ""
			if got := concurrencyMismatches([]Entry{unmarked}, []apiv2registry.Declared{op}, nil); len(got) != 1 || !strings.Contains(got[0], "is not marked concurrency") {
				t.Fatalf("missing tier-2 marking accepted: %v", got)
			}
			wrongMethod := op
			wrongMethod.Method = http.MethodGet
			if got := concurrencyMismatches([]Entry{e}, []apiv2registry.Declared{wrongMethod}, nil); len(got) != 1 || !strings.Contains(got[0], "disagree with the registry") {
				t.Fatalf("tier-2 target method mismatch accepted: %v", got)
			}
			op.Guarded = false
			if got := concurrencyMismatches([]Entry{e}, []apiv2registry.Declared{op}, nil); len(got) != 1 || !strings.Contains(got[0], "is not declared Guarded") {
				t.Fatalf("unguarded tier-2 operation accepted: %v", got)
			}
		})
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		t.Run("reject "+method, func(t *testing.T) {
			e := Entry{copied: copied{Method: method}, Tier: 2, Disposition: DispositionPorted, Concurrency: ConcurrencyIfMatch}
			if eligibleForConcurrency(e) {
				t.Fatal("unguardable method eligible")
			}
			if got := reviewRules(e.key(), e, inventoryRoute{}); len(got) == 0 {
				t.Fatal("unguardable method accepted")
			}
			expectFailure(t, mutatedFS(t, func(doc map[string]any) {
				row := entryWhere(t, doc, func(e map[string]any) bool { return e["tier"] == float64(2) && e["disposition"] == DispositionPorted })
				row["method"] = method
				row["v2"] = map[string]any{"method": nil, "path": nil, "operation_id": nil}
				row["concurrency"] = ConcurrencyIfMatch
				delete(row, "retry_safety")
				delete(row, "retry_safety_note")
			}), "concurrency")
		})
	}
	expectFailure(t, mutatedFS(t, func(doc map[string]any) {
		row := entryWhere(t, doc, func(e map[string]any) bool { return e["tier"] == float64(2) && e["disposition"] != DispositionPorted })
		row["concurrency"] = ConcurrencyIfMatch
	}), "concurrency")
}

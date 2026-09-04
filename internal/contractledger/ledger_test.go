package contractledger

import (
	"encoding/json"
	"fmt"
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
			segment := lastStaticSegment(e.Path)
			for _, site := range e.ConsumerCallSites {
				if site.Repo != repo {
					continue
				}
				out, err := exec.Command("git", "-C", dir, "show", sha+":"+site.File).Output()
				if err != nil {
					t.Errorf("%s: %s %s:%d does not exist at pinned tree %s (stale against pinned tree)", e.key(), repo, site.File, site.Line, sha[:8])
					continue
				}
				if problem := checkSiteAtPinnedTree(strings.Split(string(out), "\n"), site, segment); problem != "" {
					t.Errorf("%s: %s %s at pinned tree %s", e.key(), repo, problem, sha[:8])
				}
			}
		}
	}
}

// checkSiteAtPinnedTree applies the line and content assertions to one site
// given the file's lines at the pinned tree, and returns "" when the site
// holds up. segment is the route's last static path segment ("" when the
// path has none, which disables the content assertion).
func checkSiteAtPinnedTree(lines []string, site CallSite, segment string) string {
	if site.Line > len(lines) {
		return fmt.Sprintf("%s:%d is past the end of the file (%d lines)", site.File, site.Line, len(lines))
	}
	if site.PathLiteralLine > len(lines) {
		return fmt.Sprintf("%s:%d path_literal_line %d is past the end of the file (%d lines)", site.File, site.Line, site.PathLiteralLine, len(lines))
	}
	if segment == "" || site.Match == MatchFollower {
		return ""
	}
	// The segment is expected next to the request expression, unless the
	// path is a constant declared elsewhere in the file.
	at := site.Line
	if site.PathLiteralLine != 0 {
		at = site.PathLiteralLine
	}
	if !mentionsNear(lines, at, siteContextLines, segment) {
		return fmt.Sprintf("does not mention %q within %d lines of %s:%d (stale or mis-credited)", segment, siteContextLines, site.File, at)
	}
	return ""
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seg := segment
			if tc.name == "no static segment disables the content check" {
				seg = ""
			}
			got := checkSiteAtPinnedTree(lines, tc.site, seg)
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
// field may appear: tier-1 ported rows with a mutating method, and only the
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
		if e.Concurrency != ConcurrencyIfMatch || e.Tier != 1 || e.Disposition != DispositionPorted || !isMutatingMethod(e.Method) {
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
	}), "only for tier-1 ported rows")
	expectFailure(t, mutatedFS(t, func(doc map[string]any) {
		e := entryWhere(t, doc, func(e map[string]any) bool {
			return notMarked(e) && e["method"] == "GET" && e["tier"] == float64(1) && e["disposition"] == "ported"
		})
		e["concurrency"] = ConcurrencyIfMatch
	}), "only for a mutating method")
}

// TestGuardedOperationsAreMarkedIfMatch reconciles the v2 registry with the
// ledger: every operation registered Guarded must have each legacy row that
// maps to it marked if_match. The Guarded set is empty until the first
// section PR guards a resource; the test still runs against the real
// registry so that PR cannot forget the marking.
func TestGuardedOperationsAreMarkedIfMatch(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	byOperation := map[string][]Entry{}
	for _, e := range ledger.Entries {
		if e.V2.OperationID != nil {
			byOperation[*e.V2.OperationID] = append(byOperation[*e.V2.OperationID], e)
		}
	}
	declared := apiv2registry.DeclaredOperations()
	if len(declared) == 0 {
		t.Fatal("the v2 registry declares nothing")
	}
	for _, op := range declared {
		if !op.Guarded {
			continue
		}
		for _, e := range byOperation[op.OperationID] {
			if e.Concurrency != ConcurrencyIfMatch {
				t.Errorf("%s maps to guarded v2 operation %s but is not marked concurrency %s", e.key(), op.OperationID, ConcurrencyIfMatch)
			}
		}
	}
}

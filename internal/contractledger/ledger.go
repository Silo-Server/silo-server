// Package contractledger validates the pre-1.0-native-to-v2 migration ledger
// (contracts/api/v2/migration.json) against its JSON Schema and reconciles it
// with the legacy native route inventory (contracts/api/v2/route-inventory.json).
//
// The gate is one entry per inventory row and one inventory row per entry, and
// every field the ledger copies from the inventory must still agree with it.
// Both artifacts are embedded, so a drift between them fails the build's test
// run rather than surfacing later as a missing or stale migration decision.
package contractledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	apiv2 "github.com/Silo-Server/silo-server/contracts/api/v2"
)

const (
	inventoryPath = "route-inventory.json"
	ledgerPath    = "migration.json"
	schemaPath    = "migration.schema.json"

	// dynamicProxyRule is the one disposition rule under which the ledger may
	// record a media kind that differs from the inventory: a dynamic plugin
	// proxy has no static request or response kind, so both are dynamic_proxy.
	// The rule is anchored to the inventory side: only a row whose inventory
	// handler is one of the two plugin-proxy literals may claim it.
	dynamicProxyRule = "dynamic_plugin_proxy"
	dynamicProxyKind = "dynamic_proxy"

	pluginAssetsProxyHandler = "literal:api:/api/v1/plugin-assets/{installation_id}/*"
	pluginPagesProxyHandler  = "literal:api:/api/v1/plugins/{installation_id}/*"
	pluginAssetsProxyPrefix  = "/api/v1/plugin-assets/{installation_id}/"
	pluginPagesProxyPrefix   = "/api/v1/plugins/{installation_id}/"

	// ConcurrencyIfMatch is the one concurrency marking the ledger knows: the
	// row's v2 operation requires If-Match (apiv2.Operation.Guarded). It may
	// appear only on a tier-1 ported row with a mutating method.
	ConcurrencyIfMatch = "if_match"

	// RetrySafety values (migration.schema.json entry.retry_safety), one per
	// bullet of the contract's "Mutation retry safety" section. The value is
	// required on every tier-1 ported row with a mutating method and
	// forbidden on every other row; internal/apiv2 declares the same set as
	// apiv2.RetrySafety and the reconcile test compares them.
	RetrySafetyNaturalIdempotent = "natural_idempotent"
	RetrySafetyUniqueConstraint  = "unique_constraint"
	RetrySafetyDomainIdentity    = "domain_identity"
	RetrySafetyCoalescing        = "coalescing"
	RetrySafetyDurableDispatch   = "durable_dispatch"
	RetrySafetyIdempotencyKey    = "idempotency_key"
	RetrySafetyNonRetryable      = "non_retryable"

	// retrySafetyNoteMaxLen mirrors the schema's maxLength so the Go rule and
	// the schema refuse the same note.
	retrySafetyNoteMaxLen = 300

	// adminRouteGroup is the one route group the temporary retry_safety
	// allow-list splits by path (see retrySafetyExempt).
	adminRouteGroup = "/api/v1/admin"

	// removedTier is the only tier a removed row may hold: there is no v2
	// behavior to baseline, so it never sits in tier 1.
	removedTier = 2
)

// Disposition values (migration.schema.json $defs.disposition).
const (
	DispositionPorted             = "ported"
	DispositionRedesigned         = "redesigned"
	DispositionReplaced           = "replaced"
	DispositionRemoved            = "removed"
	DispositionCompatibilityOnly  = "compatibility_only"
	DispositionDocumentedExcluded = "documented_exclusion"
)

// Review states (migration.schema.json $defs.reviewState).
const (
	ReviewProposed = "proposed"
	ReviewRatified = "ratified"
	ReviewRejected = "rejected"
)

// Call-site match kinds (migration.schema.json $defs.matchKind).
const (
	// MatchMechanical: scripts/apiv2-ledger/extract_consumers.py resolved the
	// route's path at the site.
	MatchMechanical = "mechanical"
	// MatchManual: a maintainer resolved a path the scripts cannot follow.
	MatchManual = "manual"
	// MatchFollower: the client fetches a server-supplied URL and the site is
	// the resolver or allowlist that admits it, so the route's path is not
	// spelled anywhere near the site.
	MatchFollower = "follower"
)

// ownerPlaceholders are owner spellings that name nobody. Named reviewers are
// not recorded yet (issue #135, execution input 1) and removed/redesigned/
// replaced rows carry "pending:#135/execution-input-1" until they are. Values
// are compared case-insensitively after trimming, and anything starting with
// "pending" is a placeholder too, so a row cannot be ratified while its owner
// is any of these.
var ownerPlaceholders = map[string]bool{
	"":        true,
	"tbd":     true,
	"todo":    true,
	"unknown": true,
	"none":    true,
	"n/a":     true,
}

// isPlaceholderOwner reports whether an owner value fails to name a reviewer.
func isPlaceholderOwner(owner *string) bool {
	if owner == nil {
		return true
	}
	v := strings.ToLower(strings.TrimSpace(*owner))
	return ownerPlaceholders[v] || strings.HasPrefix(v, "pending")
}

// isPluginProxyRoute reports whether an inventory row is one of the dynamic
// plugin proxy registrations, by handler first and by path as a fallback.
func isPluginProxyRoute(r inventoryRoute) bool {
	switch r.Handler {
	case pluginAssetsProxyHandler, pluginPagesProxyHandler:
		return true
	}
	return strings.HasPrefix(r.Path, pluginAssetsProxyPrefix) || strings.HasPrefix(r.Path, pluginPagesProxyPrefix)
}

// Key identifies one registered route variant. The inventory registers a few
// method+path pairs more than once under different middleware or conditions,
// so RegistrationIndex disambiguates those in registration order.
type Key struct {
	Listener          string
	Method            string
	Path              string
	RegistrationIndex int
}

func (k Key) String() string {
	return fmt.Sprintf("%s %s %s #%d", k.Listener, k.Method, k.Path, k.RegistrationIndex)
}

// copied holds the fields a ledger entry copies verbatim from its inventory
// row. It is embedded in both Entry and inventoryRoute so reconciliation can
// compare the two field by field.
type copied struct {
	Listener          string   `json:"listener"`
	Namespace         string   `json:"namespace"`
	Method            string   `json:"method"`
	Path              string   `json:"path"`
	Handler           string   `json:"handler"`
	HandlerKind       string   `json:"handler_kind"`
	SourceFile        string   `json:"source_file"`
	RouteGroup        string   `json:"route_group"`
	MiddlewareChain   int      `json:"middleware_chain"`
	AuthClass         string   `json:"auth_class"`
	AuthTraits        []string `json:"auth_traits"`
	Conditional       bool     `json:"conditional"`
	Conditions        []string `json:"conditions"`
	DelegatesTo       *string  `json:"delegates_to"`
	RequestKind       string   `json:"request_kind"`
	ResponseMediaKind string   `json:"response_media_kind"`
	Streams           bool     `json:"streams"`
	UpgradesWebsocket bool     `json:"upgrades_websocket"`
}

// Entry is the subset of a ledger entry the gate reasons about. The schema
// governs the full shape; this struct carries the copied fields plus what the
// disposition and review rules need.
type Entry struct {
	copied
	RegistrationIndex int        `json:"registration_index"`
	ProfileRequired   bool       `json:"profile_required"`
	AdminRequired     bool       `json:"admin_required"`
	ConsumerCallSites []CallSite `json:"consumer_call_sites"`
	Section           string     `json:"section"`
	Disposition       string     `json:"disposition"`
	DispositionRule   string     `json:"disposition_rule"`
	Owner             *string    `json:"owner"`
	ReviewState       string     `json:"review_state"`
	Tier              int        `json:"tier"`
	V2                V2Target   `json:"v2"`
	// Concurrency is the optional curated optimistic-concurrency marking:
	// ConcurrencyIfMatch on a row whose v2 operation is registered Guarded.
	Concurrency string `json:"concurrency,omitempty"`
	// RetrySafety is the curated mutation retry-safety strategy (one of the
	// RetrySafety* constants); RetrySafetyNote explains a non-obvious choice
	// and is required for idempotency_key and non_retryable.
	RetrySafety     string `json:"retry_safety,omitempty"`
	RetrySafetyNote string `json:"retry_safety_note,omitempty"`
}

// retrySafetyValues is the closed set a row may carry.
var retrySafetyValues = map[string]bool{
	RetrySafetyNaturalIdempotent: true,
	RetrySafetyUniqueConstraint:  true,
	RetrySafetyDomainIdentity:    true,
	RetrySafetyCoalescing:        true,
	RetrySafetyDurableDispatch:   true,
	RetrySafetyIdempotencyKey:    true,
	RetrySafetyNonRetryable:      true,
}

// retrySafetyUnclassifiedGroups is TEMPORARY: the route groups whose tier-1
// ported mutation rows may still lack retry_safety while the classification
// pass (branch api-v2/wave1-idempotency and its follow-up) works through the
// ledger group by group. A group leaves this list in the same commit that
// classifies its rows. TODO(api-v2/wave1-idempotency): empty this list and
// delete it together with retrySafetyExempt.
var retrySafetyUnclassifiedGroups = map[string]bool{}

// retrySafetyExempt reports whether an unclassified row is tolerated by the
// temporary allow-list. The /api/v1/admin group is split: its api-keys and
// devices rows were classified with wave 1, so only its other paths are
// exempt.
func retrySafetyExempt(e Entry) bool {
	if !retrySafetyUnclassifiedGroups[e.RouteGroup] {
		return false
	}
	if e.RouteGroup == adminRouteGroup {
		return !strings.Contains(e.Path, "/api-keys") && !strings.Contains(e.Path, "/devices")
	}
	return true
}

// requiresRetrySafety reports whether a row must carry retry_safety: a
// tier-1 ported row with a mutating method.
func requiresRetrySafety(e Entry) bool {
	return e.Tier == 1 && e.Disposition == DispositionPorted && isMutatingMethod(e.Method)
}

// V2Target is the v2 operation an entry maps to. All three are nil until the
// port is ratified (or method+path for a proposed redesign); the schema
// enforces the combinations.
type V2Target struct {
	Method      *string `json:"method"`
	Path        *string `json:"path"`
	OperationID *string `json:"operation_id"`
}

// CallSite is one consumer location. File is repository-root-relative for
// Repo (web sites are relative to web/); apple and android sites refer to the
// commit pinned in Ledger.SourceTrees.
type CallSite struct {
	Repo string `json:"repo"`
	File string `json:"file"`
	// Line is the request expression (the call), 1-based.
	Line int `json:"line"`
	// PathLiteralLine is where the route's path literal is declared when it
	// is a constant rather than an argument at Line; zero when absent.
	PathLiteralLine int      `json:"path_literal_line"`
	Types           []string `json:"types"`
	Match           string   `json:"match"`
}

func (e Entry) key() Key {
	return Key{Listener: e.Listener, Method: e.Method, Path: e.Path, RegistrationIndex: e.RegistrationIndex}
}

// Ledger is the parsed migration ledger.
type Ledger struct {
	SchemaVersion int `json:"schema_version"`
	// SourceTrees pins the sibling commits the consumer call sites were
	// extracted from (repo name -> commit SHA), so a site can be re-resolved
	// against the exact tree the line numbers refer to.
	SourceTrees map[string]string `json:"source_trees"`
	Totals      struct {
		Entries int `json:"entries"`
	} `json:"totals"`
	Entries []Entry `json:"entries"`
}

type inventoryRoute struct {
	copied
}

type inventory struct {
	Routes []inventoryRoute `json:"routes"`
}

// Load parses and schema-validates the embedded ledger.
func Load() (*Ledger, error) {
	return load(apiv2.FS)
}

// Verify runs the full gate against the embedded artifacts: schema validity,
// then bidirectional reconciliation with the route inventory, then the
// review rules the schema cannot express. It returns every discrepancy at once
// so a maintainer can fix the ledger in one pass.
func Verify() error {
	return verify(apiv2.FS)
}

func load(fsys fs.FS) (*Ledger, error) {
	schemaBytes, err := fs.ReadFile(fsys, schemaPath)
	if err != nil {
		return nil, fmt.Errorf("contractledger: read schema: %w", err)
	}
	ledgerBytes, err := fs.ReadFile(fsys, ledgerPath)
	if err != nil {
		return nil, fmt.Errorf("contractledger: read ledger: %w", err)
	}

	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return nil, fmt.Errorf("contractledger: parse schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(schemaPath, schemaDoc); err != nil {
		return nil, fmt.Errorf("contractledger: add schema resource: %w", err)
	}
	schema, err := compiler.Compile(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("contractledger: compile schema: %w", err)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(ledgerBytes))
	if err != nil {
		return nil, fmt.Errorf("contractledger: parse ledger: %w", err)
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("contractledger: ledger violates %s: %w", schemaPath, err)
	}

	var ledger Ledger
	dec := json.NewDecoder(bytes.NewReader(ledgerBytes))
	if err := dec.Decode(&ledger); err != nil {
		return nil, fmt.Errorf("contractledger: decode ledger: %w", err)
	}
	if ledger.Totals.Entries != len(ledger.Entries) {
		return nil, fmt.Errorf("contractledger: totals.entries is %d but %d entries are present", ledger.Totals.Entries, len(ledger.Entries))
	}
	return &ledger, nil
}

func verify(fsys fs.FS) error {
	ledger, err := load(fsys)
	if err != nil {
		return err
	}
	inventoryBytes, err := fs.ReadFile(fsys, inventoryPath)
	if err != nil {
		return fmt.Errorf("contractledger: read inventory: %w", err)
	}
	var inv inventory
	if err := json.Unmarshal(inventoryBytes, &inv); err != nil {
		return fmt.Errorf("contractledger: decode inventory: %w", err)
	}

	// Registration index is positional within (listener, method, path) in
	// inventory order, matching how the ledger was generated.
	seen := map[Key]int{}
	invByKey := make(map[Key]inventoryRoute, len(inv.Routes))
	invOrder := make([]Key, 0, len(inv.Routes))
	for _, r := range inv.Routes {
		base := Key{Listener: r.Listener, Method: r.Method, Path: r.Path}
		k := base
		k.RegistrationIndex = seen[base]
		seen[base]++
		invByKey[k] = r
		invOrder = append(invOrder, k)
	}

	// Set checks first: missing, orphan, duplicate, field drift, review
	// rules. They name the root cause; the order check that follows is a
	// consequence of them whenever a row is missing or extra.
	var problems []string
	ledgerByKey := make(map[Key]Entry, len(ledger.Entries))
	for _, e := range ledger.Entries {
		k := e.key()
		if _, dup := ledgerByKey[k]; dup {
			problems = append(problems, fmt.Sprintf("duplicate ledger entry for %s", k))
			continue
		}
		ledgerByKey[k] = e
	}
	for _, k := range invOrder {
		e, ok := ledgerByKey[k]
		if !ok {
			problems = append(problems, fmt.Sprintf("inventory row has no ledger entry: %s", k))
			continue
		}
		problems = append(problems, fieldDrift(k, e, invByKey[k])...)
		problems = append(problems, reviewRules(k, e, invByKey[k])...)
	}
	for k := range ledgerByKey {
		if _, ok := invByKey[k]; !ok {
			problems = append(problems, fmt.Sprintf("ledger entry has no inventory row: %s", k))
		}
	}
	sort.Strings(problems)

	// Order check: report the first mismatch only. Once one row is out of
	// place every later row is too, and the set checks above already name the
	// missing or extra row that usually causes it.
	for i, e := range ledger.Entries {
		if i >= len(invOrder) {
			break
		}
		if k := e.key(); invOrder[i] != k {
			problems = append(problems, fmt.Sprintf("ledger entry %d is %s; inventory row %d is %s (ledger must follow inventory order; later mismatches cascade from this one and are not listed)", i, k, i, invOrder[i]))
			break
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New("contractledger: ledger and route inventory disagree:\n  " + strings.Join(problems, "\n  "))
}

// fieldDrift compares every copied field of a ledger entry with its inventory
// row after normalization and reports one line per differing field.
func fieldDrift(k Key, e Entry, r inventoryRoute) []string {
	want := normalize(r.copied)
	got := normalize(e.copied)

	// The only permitted override: dynamic plugin proxy rows record both media
	// kinds as dynamic_proxy regardless of what the inventory heuristics saw.
	// The override is keyed on the inventory row, not on the ledger's own
	// claim; reviewRules reports a non-proxy row that claims the rule.
	if e.DispositionRule == dynamicProxyRule && isPluginProxyRoute(r) && got.RequestKind == dynamicProxyKind && got.ResponseMediaKind == dynamicProxyKind {
		want.RequestKind, want.ResponseMediaKind = dynamicProxyKind, dynamicProxyKind
	}

	var out []string
	drift := func(field string, ledger, inventory any) {
		out = append(out, fmt.Sprintf("%s drift for %s: ledger %v, inventory %v", field, k, ledger, inventory))
	}
	if got.Namespace != want.Namespace {
		drift("namespace", got.Namespace, want.Namespace)
	}
	if got.Handler != want.Handler {
		drift("handler", fmt.Sprintf("%q", got.Handler), fmt.Sprintf("%q", want.Handler))
	}
	if got.HandlerKind != want.HandlerKind {
		drift("handler_kind", got.HandlerKind, want.HandlerKind)
	}
	if got.SourceFile != want.SourceFile {
		drift("source_file", got.SourceFile, want.SourceFile)
	}
	if got.RouteGroup != want.RouteGroup {
		drift("route_group", got.RouteGroup, want.RouteGroup)
	}
	if got.MiddlewareChain != want.MiddlewareChain {
		drift("middleware_chain", got.MiddlewareChain, want.MiddlewareChain)
	}
	if got.AuthClass != want.AuthClass {
		drift("auth_class", got.AuthClass, want.AuthClass)
	}
	if !reflect.DeepEqual(got.AuthTraits, want.AuthTraits) {
		drift("auth_traits", got.AuthTraits, want.AuthTraits)
	}
	if got.Conditional != want.Conditional {
		drift("conditional", got.Conditional, want.Conditional)
	}
	if !reflect.DeepEqual(got.Conditions, want.Conditions) {
		drift("conditions", got.Conditions, want.Conditions)
	}
	if !reflect.DeepEqual(got.DelegatesTo, want.DelegatesTo) {
		drift("delegates_to", deref(got.DelegatesTo), deref(want.DelegatesTo))
	}
	if got.RequestKind != want.RequestKind {
		drift("request_kind", got.RequestKind, want.RequestKind)
	}
	if got.ResponseMediaKind != want.ResponseMediaKind {
		drift("response_media_kind", got.ResponseMediaKind, want.ResponseMediaKind)
	}
	if got.Streams != want.Streams {
		drift("streams", got.Streams, want.Streams)
	}
	if got.UpgradesWebsocket != want.UpgradesWebsocket {
		drift("upgrades_websocket", got.UpgradesWebsocket, want.UpgradesWebsocket)
	}

	// Derived fields: the ledger's booleans are projections of auth_traits.
	if want := hasTrait(want.AuthTraits, "profile_required"); e.ProfileRequired != want {
		drift("profile_required", e.ProfileRequired, want)
	}
	if want := hasTrait(want.AuthTraits, "acting_admin"); e.AdminRequired != want {
		drift("admin_required", e.AdminRequired, want)
	}
	return out
}

// reviewRules enforces the cross-field rules the schema cannot express:
// a ratified removal, redesign, or replacement names a real owner; a removed
// row is tier 2; and only a plugin-proxy inventory row may claim the
// dynamic_plugin_proxy rule.
func reviewRules(k Key, e Entry, r inventoryRoute) []string {
	var out []string
	switch e.Disposition {
	case DispositionRemoved, DispositionRedesigned, DispositionReplaced:
		if e.ReviewState == ReviewRatified && isPlaceholderOwner(e.Owner) {
			out = append(out, fmt.Sprintf("ratified without a named owner: %s (owner %s)", k, deref(e.Owner)))
		}
	}
	if e.Disposition == DispositionRemoved && e.Tier != removedTier {
		out = append(out, fmt.Sprintf("removed row is tier %d, must be tier %d: %s", e.Tier, removedTier, k))
	}
	if e.DispositionRule == dynamicProxyRule && !isPluginProxyRoute(r) {
		out = append(out, fmt.Sprintf("dynamic_plugin_proxy rule on a non-proxy handler: %s (inventory handler %q)", k, r.Handler))
	}
	if e.Concurrency != "" {
		switch {
		case e.Concurrency != ConcurrencyIfMatch:
			out = append(out, fmt.Sprintf("unknown concurrency marking %q: %s", e.Concurrency, k))
		case e.Tier != 1 || e.Disposition != DispositionPorted:
			out = append(out, fmt.Sprintf("concurrency %s is only for tier-1 ported rows; row is tier %d %s: %s", e.Concurrency, e.Tier, e.Disposition, k))
		case !isGuardableMethod(e.Method):
			out = append(out, fmt.Sprintf("concurrency %s is only for a method a Guarded v2 operation may use (PUT, PATCH, DELETE), not %s: %s", e.Concurrency, e.Method, k))
		}
	}
	out = append(out, retrySafetyRules(k, e)...)
	return out
}

// retrySafetyRules enforces the classification's placement: required on a
// tier-1 ported mutation row (outside the temporary allow-list), forbidden
// elsewhere, a known value, and a note where the value needs one.
func retrySafetyRules(k Key, e Entry) []string {
	var out []string
	switch {
	case e.RetrySafety == "" && requiresRetrySafety(e) && !retrySafetyExempt(e):
		out = append(out, fmt.Sprintf("tier-1 ported mutation row has no retry_safety: %s", k))
	case e.RetrySafety == "":
		if e.RetrySafetyNote != "" {
			out = append(out, fmt.Sprintf("retry_safety_note without retry_safety: %s", k))
		}
	case !retrySafetyValues[e.RetrySafety]:
		out = append(out, fmt.Sprintf("unknown retry_safety %q: %s", e.RetrySafety, k))
	case !requiresRetrySafety(e):
		out = append(out, fmt.Sprintf("retry_safety %s is only for tier-1 ported rows with a mutating method; row is tier %d %s %s: %s", e.RetrySafety, e.Tier, e.Disposition, e.Method, k))
	case (e.RetrySafety == RetrySafetyIdempotencyKey || e.RetrySafety == RetrySafetyNonRetryable) && e.RetrySafetyNote == "":
		out = append(out, fmt.Sprintf("retry_safety %s requires a retry_safety_note: %s", e.RetrySafety, k))
	}
	if len(e.RetrySafetyNote) > retrySafetyNoteMaxLen {
		out = append(out, fmt.Sprintf("retry_safety_note longer than %d characters: %s", retrySafetyNoteMaxLen, k))
	}
	return out
}

// eligibleForConcurrency reports whether the review rule above allows a row
// to carry concurrency=if_match: tier-1, ported, and a method a Guarded v2
// operation may use. The reconcile against the registry requires the
// marking only on such rows, so a redesigned or replaced row that names a
// guarded v2 operation is not caught between the two rules.
func eligibleForConcurrency(e Entry) bool {
	return e.Tier == 1 && e.Disposition == DispositionPorted && isGuardableMethod(e.Method)
}

// isGuardableMethod mirrors apiv2.checkOperation: only PUT, PATCH and DELETE
// may be registered Guarded, so only their rows may be marked if_match.
func isGuardableMethod(method string) bool {
	switch method {
	case http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// isMutatingMethod is the retry_safety placement rule: every tier-1 ported
// row with one of these methods carries a classification.
func isMutatingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// normalize maps inventory spellings onto ledger spellings so the comparison
// is exact: the inventory's empty delegates_to is the ledger's null, and nil
// and empty slices are the same absence.
func normalize(c copied) copied {
	if c.DelegatesTo != nil && *c.DelegatesTo == "" {
		c.DelegatesTo = nil
	}
	if c.AuthTraits == nil {
		c.AuthTraits = []string{}
	}
	if c.Conditions == nil {
		c.Conditions = []string{}
	}
	return c
}

func hasTrait(traits []string, want string) bool {
	for _, t := range traits {
		if t == want {
			return true
		}
	}
	return false
}

func deref(s *string) string {
	if s == nil {
		return "null"
	}
	return *s
}

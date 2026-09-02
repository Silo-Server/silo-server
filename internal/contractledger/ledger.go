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
	Disposition       string     `json:"disposition"`
	DispositionRule   string     `json:"disposition_rule"`
	Owner             *string    `json:"owner"`
	ReviewState       string     `json:"review_state"`
	Tier              int        `json:"tier"`
}

// CallSite is one consumer location. File is repository-root-relative for
// Repo (web sites are relative to web/); apple and android sites refer to the
// commit pinned in Ledger.SourceTrees.
type CallSite struct {
	Repo  string   `json:"repo"`
	File  string   `json:"file"`
	Line  int      `json:"line"`
	Types []string `json:"types"`
	Match string   `json:"match"`
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
	return out
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

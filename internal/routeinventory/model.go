// Package routeinventory builds the machine-readable inventory of every route
// the legacy native HTTP listeners register.
//
// The inventory is produced from the registration source, not from one runtime
// dependency graph. A "maximal dependencies" runtime walk can only show the
// routes that particular wiring happened to construct; conditional
// registration (`if deps.PluginHTTPProxy != nil { ... }`) makes that a silent
// omission rather than a visible gap. The analyzer in this package enumerates
// registrations statically and refuses to emit an inventory it cannot fully
// account for, so a route that is missing from the output is a build failure
// rather than an absence nobody notices.
//
// See docs/architecture/api-contract.md for the program this feeds.
package routeinventory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SchemaVersion is bumped whenever the row shape changes incompatibly.
const SchemaVersion = 1

// Namespace records which URL namespace a route belongs to. The distinction
// matters at 1.0: the `/api/v1` namespace gets a tombstone, while
// version-neutral paths are retired individually.
const (
	NamespaceAPIV1        = "api_v1"
	NamespaceUnversioned  = "legacy_unversioned"
	NamespaceOperational  = "operational" // root /metrics style probes
	unknownClassification = "unknown"
)

// Route is one method+path variant registered on one listener.
//
// Every field is derived from the registration source. Listener, method, path,
// conditions, middleware, handler expression and method origin are read
// directly off the registration and are never inferred; success statuses and
// error codes cannot be derived statically at all and are explicitly null (see
// Inventory.DeferredFields). Between those two groups sit the fields named in
// Inventory.HeuristicFields, which are inferred from handler bodies by name
// matching and can be wrong.
type Route struct {
	Listener  string `json:"listener"`
	Namespace string `json:"namespace"`
	Method    string `json:"method"`
	Path      string `json:"path"`

	// RouteGroup is the concatenated chi Route() scope the registration sits
	// in — the router subtree that owns it, not merely the path prefix.
	RouteGroup string `json:"route_group"`

	// Handler is the resolved handler identity (`(*handlers.AuthHandler).HandleLogin`)
	// when the analyzer can resolve the receiver's type from the registration
	// source, and HandlerExpr verbatim otherwise. HandlerExpr is always the
	// exact source expression, so the pair is never a guess.
	Handler         string `json:"handler"`
	HandlerExpr     string `json:"handler_expr"`
	HandlerKind     string `json:"handler_kind"`
	HandlerResolved bool   `json:"handler_resolved"`

	// MiddlewareChain indexes Inventory.MiddlewareChains, which holds the
	// ordered chain the request passes through: base stack first, then each
	// enclosing Group/Route Use(), then any inline With() at the registration
	// site. The chains are shared because a change to the base stack should
	// produce an eight-line diff, not a seven-hundred-row one.
	MiddlewareChain int      `json:"middleware_chain"`
	AuthClass       string   `json:"auth_class"`
	AuthTraits      []string `json:"auth_traits"`

	// Conditions are the enclosing `if` conditions, outermost first. Empty
	// means the route is registered unconditionally. This is the field that
	// makes conditionally-wired routes visible.
	Conditions  []string `json:"conditions"`
	Conditional bool     `json:"conditional"`

	// RequestKind and ResponseMediaKind are heuristic; see
	// Inventory.HeuristicFields.
	RequestKind       string `json:"request_kind"`
	ResponseMediaKind string `json:"response_media_kind"`

	// DelegatesTo names the listener whose surface this registration hands off
	// to, empty for a leaf route. The root listener's `/api/` registration is a
	// delegation: the operations behind it are the API listener's own rows, not
	// one wildcard handler.
	DelegatesTo string `json:"delegates_to"`

	// Streams is true when the registration wraps the handler in the
	// stream-telemetry media observer, which is the repository's own
	// declaration that the route carries media bytes.
	Streams bool `json:"streams"`
	// UpgradesWebSocket is heuristic; see Inventory.HeuristicFields.
	UpgradesWebSocket bool `json:"upgrades_websocket"`

	// MethodOrigin records how the method variant was produced: "explicit" for
	// r.Get/r.Post/r.Method, "handle_all" for r.Handle/r.HandleFunc, which chi
	// registers for every method and reports as "*" when walking the tree.
	MethodOrigin string `json:"method_origin"`

	// Deferred: not statically derivable, resolved in a later inventory stage.
	SuccessStatuses []int    `json:"success_statuses"`
	ErrorCodes      []string `json:"error_codes"`

	SourceFile string `json:"source_file"`

	// chain is the rendered middleware chain before it is interned into
	// Inventory.MiddlewareChains. It is unexported so it never reaches the
	// artifact twice.
	chain []string
}

// Listener summarizes one HTTP listener in the inventory.
type Listener struct {
	ID          string `json:"id"`
	Entrypoint  string `json:"entrypoint"`
	Description string `json:"description"`
	RouteCount  int    `json:"route_count"`
}

// Totals are the aggregate counts, kept in the artifact so a reviewer sees the
// shape of a diff without recounting rows.
type Totals struct {
	Routes            int `json:"routes"`
	ConditionalRoutes int `json:"conditional_routes"`
	StreamingRoutes   int `json:"streaming_routes"`
	WebSocketRoutes   int `json:"websocket_routes"`
}

// MiddlewareChain is one shared, ordered middleware chain. An entry rendered
// `expr [when cond]` runs only under that extra condition — the route itself is
// registered regardless.
type MiddlewareChain struct {
	ID         int      `json:"id"`
	Middleware []string `json:"middleware"`
}

// Inventory is the committed artifact.
type Inventory struct {
	SchemaVersion  int      `json:"schema_version"`
	Generator      string   `json:"generator"`
	Description    string   `json:"description"`
	DeferredFields []string `json:"deferred_fields"`
	// HeuristicFields are the row fields inferred from handler bodies rather
	// than read off the registration. They are named here so no reader mistakes
	// them for the same grade of evidence as the rest of the row.
	HeuristicFields  []string          `json:"heuristic_fields"`
	Exclusions       []string          `json:"excluded_listeners"`
	Listeners        []Listener        `json:"listeners"`
	Totals           Totals            `json:"totals"`
	MiddlewareChains []MiddlewareChain `json:"middleware_chains"`
	Routes           []Route           `json:"routes"`
}

// MiddlewareFor returns the chain a route resolves to.
func (inv *Inventory) MiddlewareFor(route Route) []string {
	if route.MiddlewareChain < 0 || route.MiddlewareChain >= len(inv.MiddlewareChains) {
		return nil
	}
	return inv.MiddlewareChains[route.MiddlewareChain].Middleware
}

// Key is the total ordering key for a row. Two rows sharing a key are a
// generator bug, not a legitimate duplicate: chi itself panics on a duplicate
// method+path within one wiring.
func (r Route) Key() string {
	return strings.Join([]string{
		r.Listener, r.Path, r.Method, strings.Join(r.Conditions, "&&"), r.HandlerExpr,
	}, "\x00")
}

// Sort puts the inventory into its canonical order: listeners in declaration
// order, then path, method, conditions, handler.
func (inv *Inventory) Sort(listenerOrder []string) {
	rank := make(map[string]int, len(listenerOrder))
	for i, id := range listenerOrder {
		rank[id] = i
	}
	sort.SliceStable(inv.Routes, func(i, j int) bool {
		a, b := inv.Routes[i], inv.Routes[j]
		if rank[a.Listener] != rank[b.Listener] {
			return rank[a.Listener] < rank[b.Listener]
		}
		return a.Key() < b.Key()
	})
}

// Validate rejects duplicate rows and empty required fields.
func (inv *Inventory) Validate() error {
	seen := make(map[string]struct{}, len(inv.Routes))
	for _, route := range inv.Routes {
		if route.Listener == "" || route.Method == "" || route.Path == "" {
			return fmt.Errorf("incomplete inventory row: %+v", route)
		}
		key := route.Key()
		if _, dup := seen[key]; dup {
			return fmt.Errorf("duplicate inventory row: %s %s on %s", route.Method, route.Path, route.Listener)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// MarshalJSON renders the artifact deterministically: stable key order from the
// struct definitions, two-space indentation, HTML escaping off so path
// templates are byte-identical to the source, and a trailing newline.
func (inv *Inventory) MarshalIndented() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(inv); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Load parses a committed inventory artifact.
func Load(data []byte) (*Inventory, error) {
	var inv Inventory
	if err := json.Unmarshal(data, &inv); err != nil {
		return nil, err
	}
	return &inv, nil
}

// RuntimeKey is the identity a chi tree walk can observe: listener, method and
// path. It is deliberately weaker than Key — the runtime cross-check proves
// that everything a real router registers appears in the inventory, and cannot
// see conditions or handler expressions.
func RuntimeKey(listener, method, path string) string {
	return listener + " " + method + " " + path
}

// RuntimeKeys returns the observable identities the inventory claims for one
// listener.
func (inv *Inventory) RuntimeKeys(listener string) map[string]struct{} {
	keys := make(map[string]struct{}, len(inv.Routes))
	for _, route := range inv.Routes {
		if route.Listener != listener {
			continue
		}
		keys[RuntimeKey(route.Listener, route.Method, route.Path)] = struct{}{}
	}
	return keys
}

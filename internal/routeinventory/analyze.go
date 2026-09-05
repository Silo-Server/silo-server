package routeinventory

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Router method names the analyzer matches on. They are spelled once so a
// typo cannot make one call site model a method the others do not.
const (
	methodHandle     = "Handle"
	methodHandleFunc = "HandleFunc"
	methodWith       = "With"
	methodMethod     = "Method"
	methodServeHTTP  = "ServeHTTP"
	methodRoutes     = "Routes"
	// methodHandler is http.ServeMux's read-only Handler lookup.
	methodHandler = "Handler"
)

// metricsPath is the one operational path the namespace classifier singles out.
const metricsPath = "/metrics"

// MethodOrigin values: how a row's method variant was produced.
const (
	originExplicit  = "explicit"
	originHandleAll = "handle_all"
)

// Listener IDs. They are the join key between the artifact and the
// per-listener reconciliation tests.
const (
	ListenerRoot          = "root"
	ListenerAPI           = "api"
	ListenerAPIV2         = "api_v2"
	ListenerProxy         = "proxy"
	ListenerTranscodeNode = "transcode_node"
)

// Listener kinds select the router model the walk applies to an entry point.
const (
	ListenerKindChi      = "chi"
	ListenerKindServeMux = "servemux"
)

// handleAllMethods is what chi registers for Handle/HandleFunc (its mALL set),
// reported as "*" when walking the tree. The inventory enumerates the variants
// instead of hiding nine operations behind one wildcard row.
var handleAllMethods = []string{
	http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead,
	http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace,
}

// verbMethods maps chi's per-verb registration helpers to their HTTP method.
var verbMethods = map[string]string{
	"Connect": http.MethodConnect,
	"Delete":  http.MethodDelete,
	"Get":     http.MethodGet,
	"Head":    http.MethodHead,
	"Options": http.MethodOptions,
	"Patch":   http.MethodPatch,
	"Post":    http.MethodPost,
	"Put":     http.MethodPut,
	"Trace":   http.MethodTrace,
}

// readOnlyRouterMethods are chi.Router methods that inspect a router without
// registering anything. They are listed so the analyzer can accept them
// explicitly rather than by falling through to a permissive default.
var readOnlyRouterMethods = map[string]bool{
	methodRoutes: true, "Middlewares": true, "Match": true, methodServeHTTP: true, "Find": true,
}

// ListenerSpec names one HTTP listener, the entry function that hands its
// handler out, and the constructor that builds the router behind it.
type ListenerSpec struct {
	ID          string
	Description string
	// Kind is ListenerKindChi (the zero value) or ListenerKindServeMux.
	Kind string
	Dir  string // repo-relative package directory
	Recv string // receiver type name, empty for a package-level function
	// Func is the entry function: what the process serves. It must return a
	// sealed http.Handler built from Constructor and nothing else (seal.go).
	Func string
	// Constructor is the unexported function or method (on the same Recv)
	// that builds the router. The walk starts here; it must return the
	// router type and be called from Func only.
	Constructor string
	// Delegates maps a parameter name of the entry function to the listener ID
	// whose surface that parameter carries. A root listener that hands /api/ to
	// the API router registers a delegation, not a leaf route, and the row says
	// so instead of pretending the whole namespace is one handler.
	Delegates map[string]string
	// DelegatedBy names the package directory of the chi listener that hands
	// a subtree to this listener by registering its entry function's result
	// as a handler (`r.Handle("/api/v2/*", apiv2.NewHandler(...))`). The
	// registration is recorded as a delegation row on the registering
	// listener; this listener's own operations are described elsewhere
	// (the committed OpenAPI artifact) rather than enumerated as rows. The
	// entry function must be called in the registration itself: a handler
	// bound to a local first is refused, because nothing else proves the
	// registered value is that listener.
	DelegatedBy string
	// RouterConsumer names the one function ("import/path.Func") allowed to
	// receive this listener's router from inside its constructor: the Huma
	// adapter constructor that registers the listener's operations. It is
	// permitted exactly once, and only with the constructor's own root
	// router at the constructor's top level — never a router derived by
	// Group(), Route() or With(), never inside a closure, a helper function
	// or a condition, because the prefix and middleware such a router
	// carries are invisible to the OpenAPI artifact that describes the
	// operations registered through it.
	RouterConsumer string
}

// Entrypoint renders the listener's entry function for the artifact.
func (l ListenerSpec) Entrypoint() string {
	if l.Recv == "" {
		return l.Dir + "." + l.Func
	}
	return l.Dir + ".(*" + l.Recv + ")." + l.Func
}

func (l ListenerSpec) kind() string {
	if l.Kind == "" {
		return ListenerKindChi
	}
	return l.Kind
}

// RouterExclusion declares a router construction that deliberately lives
// outside the native inventory. The exclusion names one function in one file:
// a file-wide exclusion would silently cover every router a later change adds
// to that file. Every excluded construction needs a reason in the artifact so
// "not inventoried" is a recorded decision rather than an oversight.
type RouterExclusion struct {
	File   string
	Func   string
	Reason string
}

func (e RouterExclusion) key() string { return e.File + "#" + e.Func }

// Config drives one inventory build.
type Config struct {
	Root      string
	Listeners []ListenerSpec
	// AuditDirs are the package directories the walk follows helpers into and
	// audits for router-taking helpers nothing reaches. Every listener
	// directory must appear here; add the packages that register routes on a
	// listener's behalf. The whole module is type-checked and swept for stray
	// constructions regardless (see sweep.go).
	AuditDirs  []string
	Exclusions []RouterExclusion
}

// Analyzer enumerates route registrations from source.
type Analyzer struct {
	cfg Config
	set *sourceSet

	routes []Route

	enteredFuncs map[*ast.FuncDecl]bool
	enteredLits  map[*ast.FuncLit]bool

	// rootConstructed guards the current listener walk against a second router
	// construction. The first one is the value the entry point returns and is
	// therefore anchored at "/"; a second one is only reachable through an
	// attachment the walk cannot see, so its rows would claim wrong paths.
	rootConstructed bool
	// routerConsumed guards the current listener walk against a second
	// hand-off to its RouterConsumer.
	routerConsumed bool
	// rootScope is the scope of the router the current listener's constructor
	// built. Every Group/Route/With scope is a clone, so identity with this
	// one is what "the constructor's own root router" means.
	rootScope *routerScope

	classifier *classifier
}

// Analyze builds the inventory or fails. It never returns a partial result:
// an inventory that silently drops a route it could not understand is worse
// than no inventory at all.
func Analyze(cfg Config) (*Inventory, error) {
	declared := make(map[string]bool, len(cfg.Listeners))
	for _, listener := range cfg.Listeners {
		declared[listener.ID] = true
	}
	for _, listener := range cfg.Listeners {
		for _, param := range sortedKeys(listener.Delegates) {
			if !declared[listener.Delegates[param]] {
				return nil, fmt.Errorf("listener %s: parameter %s delegates to undeclared listener %q",
					listener.ID, param, listener.Delegates[param])
			}
		}
	}
	dirs := append([]string{}, cfg.AuditDirs...)
	for _, listener := range cfg.Listeners {
		dirs = append(dirs, listener.Dir)
	}
	set, err := loadSources(cfg.Root, dirs)
	if err != nil {
		return nil, err
	}
	a := &Analyzer{
		cfg:          cfg,
		set:          set,
		enteredFuncs: map[*ast.FuncDecl]bool{},
		enteredLits:  map[*ast.FuncLit]bool{},
		classifier:   newClassifier(set),
	}
	for _, listener := range cfg.Listeners {
		if err := a.walkListener(listener); err != nil {
			return nil, err
		}
	}
	if err := a.audit(); err != nil {
		return nil, err
	}
	if err := a.auditExportedRouterReturns(); err != nil {
		return nil, err
	}
	if err := a.sweep(); err != nil {
		return nil, err
	}
	return a.build()
}

func (a *Analyzer) build() (*Inventory, error) {
	order := make([]string, 0, len(a.cfg.Listeners))
	counts := map[string]int{}
	totals := Totals{Routes: len(a.routes)}
	for _, route := range a.routes {
		counts[route.Listener]++
		if route.Conditional {
			totals.ConditionalRoutes++
		}
		if route.Streams {
			totals.StreamingRoutes++
		}
		if route.UpgradesWebSocket {
			totals.WebSocketRoutes++
		}
	}
	listeners := make([]Listener, 0, len(a.cfg.Listeners))
	for _, spec := range a.cfg.Listeners {
		order = append(order, spec.ID)
		listeners = append(listeners, Listener{
			ID:          spec.ID,
			Entrypoint:  spec.Entrypoint(),
			Description: spec.Description,
			RouteCount:  counts[spec.ID],
		})
	}
	exclusions := make([]string, 0, len(a.cfg.Exclusions))
	for _, exclusion := range a.cfg.Exclusions {
		exclusions = append(exclusions, exclusion.File+"#"+exclusion.Func+": "+exclusion.Reason)
	}
	sort.Strings(exclusions)

	inv := &Inventory{
		SchemaVersion: SchemaVersion,
		Generator:     "cmd/route-inventory",
		Description: "Every method+path variant the legacy native HTTP listeners register, " +
			"enumerated from registration source so conditionally wired routes cannot be omitted.",
		DeferredFields: []string{
			"success_statuses: not statically derivable from registration source; resolved in a later inventory stage",
			"error_codes: not statically derivable from registration source; resolved in a later inventory stage",
		},
		// Everything else in a row is read directly off the registration; these
		// three are inferred from handler bodies by name and substring matching
		// and can be wrong. They are listed so a later stage knows which fields
		// still have to be confirmed against the handlers themselves.
		HeuristicFields: []string{
			"request_kind: inferred from decode/parse calls in the handler body, matched by callee name and by the " +
				"substring \"body\" in the decoded argument",
			"response_media_kind: inferred from Content-Type string literals and from writer-call names " +
				"(json.NewEncoder, writeJSON, respondJSON, writeError, ServeContent, ServeFile)",
			"upgrades_websocket: inferred from an Upgrade/Accept call whose callee names a websocket package or " +
				"contains the substring \"grade\"",
		},
		Exclusions:       exclusions,
		Listeners:        listeners,
		Totals:           totals,
		MiddlewareChains: internChains(a.routes),
		Routes:           a.routes,
	}
	inv.Sort(order)
	if err := inv.Validate(); err != nil {
		return nil, err
	}
	return inv, nil
}

// internChains collapses the repeated middleware chains into a shared,
// lexicographically ordered table and rewrites each route to reference it. IDs
// come from the sorted chain text, so they do not move when an unrelated route
// is added.
func internChains(routes []Route) []MiddlewareChain {
	unique := map[string][]string{}
	for _, route := range routes {
		unique[strings.Join(route.chain, "\x00")] = route.chain
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	ids := make(map[string]int, len(keys))
	chains := make([]MiddlewareChain, 0, len(keys))
	for index, key := range keys {
		ids[key] = index
		middleware := unique[key]
		if middleware == nil {
			middleware = []string{}
		}
		chains = append(chains, MiddlewareChain{ID: index, Middleware: middleware})
	}
	for i := range routes {
		routes[i].MiddlewareChain = ids[strings.Join(routes[i].chain, "\x00")]
	}
	return chains
}

// ---------------------------------------------------------------------------
// Walk
// ---------------------------------------------------------------------------

// mwEntry is one middleware in a router's chain, together with the conditions
// that were active where it was installed. A middleware added under a narrower
// condition than the route itself (a demo guard behind `demoGuard != nil`) is
// rendered with that gate, so the inventory does not claim a route is always
// guarded when it is not.
type mwEntry struct {
	expr  string
	conds []string
}

// routerScope is the accumulated state of one chi router value: where it is
// mounted and what middleware runs before its handlers.
type routerScope struct {
	prefix string // full path prefix
	group  string // Route() chain only
	mw     []mwEntry
}

func (s *routerScope) clone() *routerScope {
	return &routerScope{prefix: s.prefix, group: s.group, mw: append([]mwEntry{}, s.mw...)}
}

// renderMiddleware flattens a chain against the conditions the route itself
// carries.
func renderMiddleware(chain []mwEntry, routeConds []string) []string {
	active := make(map[string]bool, len(routeConds))
	for _, cond := range routeConds {
		active[cond] = true
	}
	out := make([]string, 0, len(chain))
	for _, entry := range chain {
		var extra []string
		for _, cond := range entry.conds {
			if !active[cond] {
				extra = append(extra, cond)
			}
		}
		if len(extra) == 0 {
			out = append(out, entry.expr)
			continue
		}
		out = append(out, entry.expr+" [when "+strings.Join(extra, " && ")+"]")
	}
	return out
}

// walkEnv is the state of one function body being walked. Router and mux
// values are keyed by the variable object the type checker resolved, so a
// shadowing declaration is a different variable and never inherits a scope.
type walkEnv struct {
	pkg      *pkgSource
	listener ListenerSpec
	routers  map[*types.Var]*routerScope
	// muxes are the bound http.ServeMux locals of a servemux listener.
	muxes map[*types.Var]bool
	// delegates maps an entry parameter to the listener ID it carries.
	delegates map[*types.Var]string
	// sealed maps a local bound to a delegated listener's sealed entry-function
	// result to that listener's ID, so registering the variable instead of the
	// call is refused rather than recorded as a leaf handler.
	sealed map[*types.Var]string
	conds  []string
	depth  int
	entry  bool
	// inRouterLit is true inside a Group(), Route() or With(...).Group()
	// closure: the router in scope there is derived, not the constructor's own.
	inRouterLit bool
}

func (e *walkEnv) info() *types.Info { return e.pkg.info() }

func (e *walkEnv) child() *walkEnv {
	routers := make(map[*types.Var]*routerScope, len(e.routers))
	for obj, scope := range e.routers {
		routers[obj] = scope
	}
	muxes := make(map[*types.Var]bool, len(e.muxes))
	for obj := range e.muxes {
		muxes[obj] = true
	}
	sealed := make(map[*types.Var]string, len(e.sealed))
	for obj, id := range e.sealed {
		sealed[obj] = id
	}
	return &walkEnv{
		pkg: e.pkg, listener: e.listener,
		routers: routers, muxes: muxes, delegates: e.delegates, sealed: sealed,
		conds: append([]string{}, e.conds...),
		depth: e.depth, entry: e.entry, inRouterLit: e.inRouterLit,
	}
}

// varOf resolves an identifier expression to the variable it uses.
func (e *walkEnv) varOf(expr ast.Expr) *types.Var {
	ident, ok := unwrapParen(expr).(*ast.Ident)
	if !ok {
		return nil
	}
	obj, _ := e.info().Uses[ident].(*types.Var)
	return obj
}

func (a *Analyzer) walkListener(spec ListenerSpec) error {
	pkg := a.set.packages[spec.Dir]
	if pkg == nil {
		return fmt.Errorf("listener %s: package %s not loaded", spec.ID, spec.Dir)
	}
	// The walk starts at the constructor. The entry function that seals its
	// result is checked separately, with type information, in seal.go.
	decl, err := a.checkSeal(spec, pkg)
	if err != nil {
		return err
	}
	a.enteredFuncs[decl] = true
	a.rootConstructed = false
	a.routerConsumed = false
	a.rootScope = nil

	env := &walkEnv{
		pkg:       pkg,
		listener:  spec,
		routers:   map[*types.Var]*routerScope{},
		muxes:     map[*types.Var]bool{},
		delegates: map[*types.Var]string{},
		sealed:    map[*types.Var]string{},
		entry:     true,
	}
	if spec.kind() == ListenerKindServeMux {
		for _, p := range flattenParams(decl.Type.Params) {
			id := spec.Delegates[p.name]
			if id == "" || p.ident == nil {
				continue
			}
			if obj, ok := pkg.info().Defs[p.ident].(*types.Var); ok {
				env.delegates[obj] = id
			}
		}
		return a.walkMuxStmts(decl.Body.List, env)
	}
	return a.walkStmts(decl.Body.List, env)
}

// ---------------------------------------------------------------------------
// http.ServeMux walk
// ---------------------------------------------------------------------------

// walkMuxStmts enumerates an http.ServeMux entry point. The model is
// deliberately narrower than the chi one: a mux is bound by http.NewServeMux(),
// registered on with Handle/HandleFunc, and returned. Any other use of the mux
// value is refused, so the root listener cannot grow a registration the
// inventory does not see.
func (a *Analyzer) walkMuxStmts(stmts []ast.Stmt, env *walkEnv) error {
	for _, stmt := range stmts {
		if err := a.walkMuxStmt(stmt, env); err != nil {
			return err
		}
	}
	return nil
}

func (a *Analyzer) walkMuxStmt(stmt ast.Stmt, env *walkEnv) error {
	switch typed := stmt.(type) {
	case *ast.ExprStmt:
		call, ok := unwrapParen(typed.X).(*ast.CallExpr)
		if !ok {
			return a.leakCheck(typed, env)
		}
		sel, ok := unwrapParen(call.Fun).(*ast.SelectorExpr)
		if !ok {
			return a.leakCheck(typed, env)
		}
		if obj := env.varOf(sel.X); obj == nil || !env.muxes[obj] {
			return a.leakCheck(typed, env)
		}
		return a.handleMuxMethod(call, sel, env)

	case *ast.BlockStmt:
		return a.walkMuxStmts(typed.List, env.child())

	case *ast.IfStmt:
		if typed.Init != nil {
			if err := a.leakCheck(typed.Init, env); err != nil {
				return err
			}
		}
		if err := a.leakCheck(typed.Cond, env); err != nil {
			return err
		}
		cond := a.set.exprText(typed.Cond)
		body := env.child()
		body.conds = append(body.conds, cond)
		if err := a.walkMuxStmts(typed.Body.List, body); err != nil {
			return err
		}
		if typed.Else == nil {
			return nil
		}
		alt := env.child()
		alt.conds = append(alt.conds, "!("+cond+")")
		return a.walkMuxStmt(typed.Else, alt)

	case *ast.AssignStmt:
		return a.walkBinding(typed, a.bindingsOfAssign(typed, env), env, ListenerKindServeMux)

	case *ast.DeclStmt:
		bindings, ok := a.bindingsOfDecl(typed, env)
		if !ok {
			return a.leakCheck(typed, env)
		}
		return a.walkBinding(typed, bindings, env, ListenerKindServeMux)

	case *ast.ReturnStmt:
		if len(typed.Results) == 1 {
			if obj := env.varOf(typed.Results[0]); obj != nil && env.muxes[obj] {
				return nil
			}
		}
		return a.leakCheck(typed, env)

	default:
		return a.leakCheck(stmt, env)
	}
}

// serveMuxReadOnlyMethods inspect a mux without registering anything.
var serveMuxReadOnlyMethods = map[string]bool{methodServeHTTP: true, methodHandler: true}

func (a *Analyzer) handleMuxMethod(call *ast.CallExpr, sel *ast.SelectorExpr, env *walkEnv) error {
	name := sel.Sel.Name
	switch {
	case name == methodHandle || name == methodHandleFunc:
		pattern, err := a.stringArg(call, 0)
		if err != nil {
			return err
		}
		return a.emitMux(call, env, pattern, argAt(call, 1))
	case serveMuxReadOnlyMethods[name]:
		return a.leakCheckArgs(call, env)
	}
	return a.errorf(call, "unknown http.ServeMux method %q", name)
}

// emitMux records one ServeMux registration. A ServeMux pattern matches every
// method unless it names one, so the row set is expanded the same way a chi
// Handle registration is.
func (a *Analyzer) emitMux(call *ast.CallExpr, env *walkEnv, pattern string, handler ast.Expr) error {
	if handler == nil {
		return a.errorf(call, "registration has no handler argument")
	}
	if err := a.leakCheck(handler, env); err != nil {
		return err
	}
	methods, path, origin, err := splitServeMuxPattern(pattern)
	if err != nil {
		return a.errorf(call, "%v", err)
	}
	sourceFile := a.set.sourceFile(call)

	delegate := ""
	if obj := env.varOf(handler); obj != nil {
		delegate = env.delegates[obj]
	}

	for _, method := range methods {
		info := a.classifier.describe(handler, method, path, env)
		route := Route{
			Listener:          env.listener.ID,
			Namespace:         namespaceFor(path),
			Method:            method,
			Path:              path,
			RouteGroup:        "/",
			Handler:           info.identity,
			HandlerExpr:       info.expr,
			HandlerKind:       info.kind,
			HandlerResolved:   info.resolved,
			chain:             []string{},
			Conditions:        append([]string{}, env.conds...),
			Conditional:       len(env.conds) > 0,
			RequestKind:       info.requestKind,
			ResponseMediaKind: info.responseKind,
			Streams:           info.streams,
			UpgradesWebSocket: info.websocket,
			MethodOrigin:      origin,
			SourceFile:        sourceFile,
			DelegatesTo:       delegate,
		}
		if route.Conditions == nil {
			route.Conditions = []string{}
		}
		route.AuthClass, route.AuthTraits = classifyAuth(route.chain)
		if delegate != "" {
			// The delegated surface's own rows carry its auth; claiming this
			// row is public would be a claim about routes it does not own.
			route.Handler = "listener:" + delegate
			route.HandlerKind = handlerKindDelegation
			route.HandlerResolved = true
			route.RequestKind = unknownClassification
			route.ResponseMediaKind = unknownClassification
			route.AuthClass = authDelegated
			route.AuthTraits = []string{authDelegated}
		}
		a.routes = append(a.routes, route)
	}
	return nil
}

// splitServeMuxPattern reads Go 1.22 ServeMux pattern syntax. A method-less
// pattern answers on every method, which is the shape the inventory models.
//
// A pattern that names a method is refused rather than recorded. `GET /path`
// does not register GET alone: net/http answers HEAD on it as well, and it also
// makes every other method 405 rather than 404 on that path. Emitting the GET
// row the pattern literally spells would narrow the surface silently, and
// emitting a synthesized HEAD row would model one part of the method-aware
// pattern semantics while leaving the rest unmodeled. Nothing in the repository
// registers such a pattern today, so refusing costs nothing and keeps "a route
// cannot exist without a row" literally true. Model it before using it.
//
// A host-qualified pattern is refused for the same reason.
func splitServeMuxPattern(pattern string) ([]string, string, string, error) {
	fields := strings.Fields(pattern)
	switch {
	case len(fields) == 2:
		return nil, "", "", fmt.Errorf("method-aware ServeMux pattern %q is not modeled by the route inventory: "+
			"a %q pattern also answers HEAD and turns other methods into 405, "+
			"so a single row would understate it. Register the pattern without a method, or add explicit support",
			pattern, fields[0])
	case len(fields) != 1:
		return nil, "", "", fmt.Errorf("ServeMux pattern %q is not a single path", pattern)
	}
	path := fields[0]
	if !strings.HasPrefix(path, "/") {
		return nil, "", "", fmt.Errorf("host-qualified ServeMux pattern %q is not modeled by the route inventory", pattern)
	}
	return handleAllMethods, path, originHandleAll, nil
}

// ---------------------------------------------------------------------------
// chi walk
// ---------------------------------------------------------------------------

func (a *Analyzer) walkStmts(stmts []ast.Stmt, env *walkEnv) error {
	for _, stmt := range stmts {
		if err := a.walkStmt(stmt, env); err != nil {
			return err
		}
	}
	return nil
}

func (a *Analyzer) walkStmt(stmt ast.Stmt, env *walkEnv) error {
	switch typed := stmt.(type) {
	case *ast.ExprStmt:
		call, ok := unwrapParen(typed.X).(*ast.CallExpr)
		if !ok {
			return a.leakCheck(typed, env)
		}
		handled, err := a.handleCall(call, env)
		if err != nil {
			return err
		}
		if handled {
			return nil
		}
		return a.leakCheck(typed, env)

	case *ast.BlockStmt:
		return a.walkStmts(typed.List, env.child())

	case *ast.IfStmt:
		if typed.Init != nil {
			if err := a.leakCheck(typed.Init, env); err != nil {
				return err
			}
		}
		if err := a.leakCheck(typed.Cond, env); err != nil {
			return err
		}
		cond := a.set.exprText(typed.Cond)
		body := env.child()
		body.conds = append(body.conds, cond)
		if err := a.walkStmts(typed.Body.List, body); err != nil {
			return err
		}
		if typed.Else == nil {
			return nil
		}
		alt := env.child()
		alt.conds = append(alt.conds, "!("+cond+")")
		return a.walkStmt(typed.Else, alt)

	case *ast.AssignStmt:
		if len(typed.Lhs) == 1 && len(typed.Rhs) == 1 {
			if call, ok := unwrapParen(typed.Rhs[0]).(*ast.CallExpr); ok && a.isRouterConsumer(call, env) {
				if err := a.consumeRouter(call, env); err != nil {
					return err
				}
				return a.leakCheck(typed.Lhs[0], env)
			}
		}
		return a.walkBinding(typed, a.bindingsOfAssign(typed, env), env, ListenerKindChi)

	case *ast.DeclStmt:
		bindings, ok := a.bindingsOfDecl(typed, env)
		if !ok {
			return a.leakCheck(typed, env)
		}
		return a.walkBinding(typed, bindings, env, ListenerKindChi)

	case *ast.ReturnStmt:
		if env.entry && env.depth == 0 && len(typed.Results) == 1 {
			if obj := env.varOf(typed.Results[0]); obj != nil && env.routers[obj] != nil {
				return nil
			}
		}
		return a.leakCheck(typed, env)

	default:
		// Every other construct is legitimate application code as long as no
		// router value flows through it. A router inside a loop, switch,
		// goroutine, or channel send is exactly the shape that would let a
		// registration escape enumeration, so it is refused rather than
		// approximated.
		return a.leakCheck(stmt, env)
	}
}

// ---------------------------------------------------------------------------
// Bindings
// ---------------------------------------------------------------------------

// valueBinding is one name a statement brings into scope, with the initializer
// it was bound with. Bindings are read out of `:=`/`=` assignments and `var`
// declarations alike, so a router cannot enter scope through a spelling the
// walk never inspects.
type valueBinding struct {
	obj   *types.Var // nil when the target is not a plain identifier
	value ast.Expr   // nil for a declaration with no initializer
	// declared is the spelled type of a `var x T` binding, nil when inferred.
	declared types.Type
	// attributed is false when a single initializer feeds several names
	// (`a, b := f()`), so no name can be tied to what it produced.
	attributed bool
}

// bindingsOfAssign flattens an assignment into its bindings.
func (a *Analyzer) bindingsOfAssign(stmt *ast.AssignStmt, env *walkEnv) []valueBinding {
	if len(stmt.Lhs) == len(stmt.Rhs) {
		out := make([]valueBinding, 0, len(stmt.Lhs))
		for i, lhs := range stmt.Lhs {
			out = append(out, valueBinding{obj: a.bindingTarget(lhs, env), value: stmt.Rhs[i], attributed: true})
		}
		return out
	}
	// `a, b := f()`: one initializer, several names. Nothing can be attributed.
	out := make([]valueBinding, 0, len(stmt.Rhs))
	for _, rhs := range stmt.Rhs {
		out = append(out, valueBinding{value: rhs})
	}
	return out
}

// bindingTarget resolves the variable an assignment target names, whether the
// statement defines it (`:=`, `var`) or assigns to an existing one (`=`).
func (a *Analyzer) bindingTarget(lhs ast.Expr, env *walkEnv) *types.Var {
	ident, ok := unwrapParen(lhs).(*ast.Ident)
	if !ok {
		return nil
	}
	if obj, ok := env.info().Defs[ident].(*types.Var); ok {
		return obj
	}
	obj, _ := env.info().Uses[ident].(*types.Var)
	return obj
}

// bindingsOfDecl flattens a `var` declaration statement into its bindings. It
// reports false for any other declaration.
func (a *Analyzer) bindingsOfDecl(stmt *ast.DeclStmt, env *walkEnv) ([]valueBinding, bool) {
	decl, ok := stmt.Decl.(*ast.GenDecl)
	if !ok || decl.Tok != token.VAR {
		return nil, false
	}
	var out []valueBinding
	for _, spec := range decl.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		var declared types.Type
		if value.Type != nil {
			declared = env.info().TypeOf(value.Type)
		}
		switch {
		case len(value.Values) == 0:
			// `var r chi.Router` — declared, uninitialized, and still a name a
			// registration could be made through.
			for _, name := range value.Names {
				out = append(out, valueBinding{obj: a.bindingTarget(name, env), declared: declared, attributed: true})
			}
		case len(value.Names) == len(value.Values):
			for i, name := range value.Names {
				out = append(out, valueBinding{
					obj: a.bindingTarget(name, env), value: value.Values[i], declared: declared, attributed: true,
				})
			}
		default:
			for _, initializer := range value.Values {
				out = append(out, valueBinding{value: initializer, declared: declared})
			}
		}
	}
	return out, true
}

// walkBinding models every way a router or mux value can be bound. A binding
// the walk cannot tie to exactly one name is refused rather than approximated:
// a router nobody can name is a router nobody can prove the attachment of.
func (a *Analyzer) walkBinding(stmt ast.Stmt, bindings []valueBinding, env *walkEnv, kind string) error {
	for _, binding := range bindings {
		a.noteSealedListener(binding, env)
	}
	interesting := false
	for _, binding := range bindings {
		if a.set.routerKind(binding.declared) != ctorNone || a.valueRouterKind(binding.value, env) != ctorNone {
			interesting = true
			break
		}
	}
	if !interesting {
		return a.leakCheck(stmt, env)
	}
	for _, binding := range bindings {
		if err := a.bindOne(stmt, binding, env, kind); err != nil {
			return err
		}
	}
	return nil
}

// valueRouterKind is the router kind an initializer produces, or ctorNone.
func (a *Analyzer) valueRouterKind(value ast.Expr, env *walkEnv) ctorKind {
	if value == nil {
		return ctorNone
	}
	return a.set.tupleRouterKind(env.info().TypeOf(value))
}

func (a *Analyzer) bindOne(stmt ast.Stmt, binding valueBinding, env *walkEnv, kind string) error {
	produced := a.valueRouterKind(binding.value, env)
	declared := a.set.routerKind(binding.declared)
	if produced == ctorNone && declared == ctorNone {
		// A neighboring binding in the same statement; it still must not carry
		// an already-bound router into an unmodeled construct.
		if binding.value == nil {
			return nil
		}
		return a.leakCheck(binding.value, env)
	}
	if declared != ctorNone {
		return a.errorf(stmt, "%s value is bound through an explicit type declaration; "+
			"the route inventory models only `name := constructor()`, so it cannot prove what this value is "+
			"or where it attaches", declared)
	}
	value := unwrapParen(binding.value)
	call, isCall := value.(*ast.CallExpr)
	if !isCall || isBuiltinNew(call, env.info()) {
		// new(T), &T{}, T{} — a zero router is fully functional, so these are
		// constructions as much as the constructor call is. Anything else
		// (a conversion, a function-typed value) is refused in the same breath.
		return a.errorf(stmt, "%s is built by literal or new() in the %s listener entry point; "+
			"the route inventory recognizes only chi.NewRouter(), chi.NewMux() and http.NewServeMux(), "+
			"so this value would carry registrations it never sees", produced, env.listener.ID)
	}
	if isRouterCtor(calleeFunc(call, env.info())) == ctorNone {
		return a.errorf(stmt, "%s is produced by %s, which the route inventory does not model; "+
			"it recognizes only chi.NewRouter(), chi.NewMux() and http.NewServeMux(), "+
			"so this value would carry registrations it never sees", produced, a.set.exprText(value))
	}
	if !binding.attributed || binding.obj == nil {
		return a.errorf(stmt, "%s is constructed in a binding form the route inventory does not model; "+
			"bind each router with a single `name := constructor()` so the walk can prove where it attaches", produced)
	}
	switch kind {
	case ListenerKindServeMux:
		return a.bindMuxValue(stmt, binding, produced, env)
	default:
		return a.bindChiValue(stmt, binding, produced, env)
	}
}

func (a *Analyzer) bindChiValue(stmt ast.Stmt, binding valueBinding, ctor ctorKind, env *walkEnv) error {
	switch ctor {
	case ctorChiRouter:
		if !env.entry {
			return a.errorf(stmt, "chi router constructed outside a declared listener entry point")
		}
		if a.rootConstructed {
			return a.errorf(stmt, "a second chi router is constructed in the %s listener entry point; "+
				"the inventory cannot prove where it is attached, so its routes would be recorded at "+
				"unprefixed paths and the attachment itself would never be leak-checked. "+
				"Register on the listener's own router, or add explicit support for the attachment",
				env.listener.ID)
		}
		a.rootConstructed = true
		a.rootScope = &routerScope{}
		env.routers[binding.obj] = a.rootScope
		return nil
	default:
		return a.errorf(stmt, "an http.ServeMux is constructed inside the %s chi listener entry point; "+
			"the inventory does not model what is registered on it", env.listener.ID)
	}
}

func (a *Analyzer) bindMuxValue(stmt ast.Stmt, binding valueBinding, ctor ctorKind, env *walkEnv) error {
	switch ctor {
	case ctorServeMux:
		if len(env.muxes) > 0 {
			return a.errorf(stmt, "a second http.ServeMux is constructed in the %s listener entry point; "+
				"the inventory cannot prove which one the listener serves", env.listener.ID)
		}
		env.muxes[binding.obj] = true
		return nil
	default:
		return a.errorf(stmt, "a chi router is constructed inside the %s http.ServeMux listener entry point; "+
			"the inventory does not model what is registered on it", env.listener.ID)
	}
}

// resolveRouter maps an expression to the router scope it denotes, following
// With() chains. It never invents a scope: an expression it does not model
// returns false and the caller refuses the construct. A With() argument that
// captures the router is refused outright, like any other handler argument.
func (a *Analyzer) resolveRouter(expr ast.Expr, env *walkEnv) (*routerScope, bool, error) {
	switch typed := unwrapParen(expr).(type) {
	case *ast.Ident:
		obj := env.varOf(typed)
		if obj == nil {
			return nil, false, nil
		}
		scope, ok := env.routers[obj]
		return scope, ok, nil
	case *ast.CallExpr:
		sel, ok := unwrapParen(typed.Fun).(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != methodWith {
			return nil, false, nil
		}
		base, ok, err := a.resolveRouter(sel.X, env)
		if !ok || err != nil {
			return nil, false, err
		}
		derived := base.clone()
		for _, arg := range typed.Args {
			if err := a.leakCheck(arg, env); err != nil {
				return nil, false, err
			}
			derived.mw = append(derived.mw, mwEntry{expr: a.set.exprText(arg), conds: append([]string{}, env.conds...)})
		}
		return derived, true, nil
	}
	return nil, false, nil
}

// leakCheckArgs leak-checks every argument of a call.
func (a *Analyzer) leakCheckArgs(call *ast.CallExpr, env *walkEnv) error {
	for _, arg := range call.Args {
		if err := a.leakCheck(arg, env); err != nil {
			return err
		}
	}
	return nil
}

func (a *Analyzer) handleCall(call *ast.CallExpr, env *walkEnv) (bool, error) {
	if sel, ok := unwrapParen(call.Fun).(*ast.SelectorExpr); ok {
		scope, isRouter, err := a.resolveRouter(sel.X, env)
		if err != nil {
			return true, err
		}
		if isRouter {
			return true, a.handleRouterMethod(call, sel, scope, env)
		}
	}
	// A router handed to the listener's declared consumer: recorded, not followed.
	if a.isRouterConsumer(call, env) {
		return true, a.consumeRouter(call, env)
	}
	// A router handed to another function: follow it, or fail.
	if a.callPassesRouter(call, env) {
		return true, a.followHelper(call, env)
	}
	return false, nil
}

// isRouterConsumer reports whether call is the listener's RouterConsumer.
func (a *Analyzer) isRouterConsumer(call *ast.CallExpr, env *walkEnv) bool {
	if env.listener.RouterConsumer == "" {
		return false
	}
	fn := calleeFunc(call, env.info())
	if fn == nil || fn.Pkg() == nil || fn.Signature().Recv() != nil {
		return false
	}
	return fn.Pkg().Path()+"."+fn.Name() == env.listener.RouterConsumer
}

// consumeRouter admits the one hand-off of the listener's router to its
// RouterConsumer: once, at the constructor's top level, with every other
// argument leak-checked as usual.
func (a *Analyzer) consumeRouter(call *ast.CallExpr, env *walkEnv) error {
	var received *routerScope
	for _, arg := range call.Args {
		if obj := env.varOf(arg); obj != nil && env.routers[obj] != nil {
			if received == nil {
				received = env.routers[obj]
			}
			continue
		}
		if err := a.leakCheck(arg, env); err != nil {
			return err
		}
	}
	if received == nil {
		return a.errorf(call, "%s does not receive the %s listener's router", env.listener.RouterConsumer, env.listener.ID)
	}
	// The consumer registers operations the inventory does not enumerate, so
	// it may only receive the constructor's own root router: on a router
	// derived by Group(), Route() or With(), or inside a helper or a
	// condition, the prefix and middleware those operations would inherit are
	// invisible to the artifact that describes them.
	if !env.entry || env.depth != 0 || len(env.conds) > 0 || env.inRouterLit || received != a.rootScope {
		return a.errorf(call, "%s must receive the %s listener's own root router at the top level of its "+
			"constructor: not a router derived by Group(), Route() or With(), not inside a closure, a helper "+
			"function or a condition", env.listener.RouterConsumer, env.listener.ID)
	}
	if a.routerConsumed {
		return a.errorf(call, "%s is called more than once in the %s listener constructor; "+
			"the router may be handed to its consumer exactly once", env.listener.RouterConsumer, env.listener.ID)
	}
	a.routerConsumed = true
	return nil
}

func (a *Analyzer) callPassesRouter(call *ast.CallExpr, env *walkEnv) bool {
	for _, arg := range call.Args {
		if obj := env.varOf(arg); obj != nil && env.routers[obj] != nil {
			return true
		}
	}
	return false
}

// followHelper walks a helper that receives a tracked router. Only a helper
// declared in an audited package (Config.AuditDirs) is followed, whichever of
// those packages it lives in: those are the packages whose unreached
// router-taking helpers are refused, so following one cannot leave a sibling
// unexamined. A helper outside them is refused.
func (a *Analyzer) followHelper(call *ast.CallExpr, env *walkEnv) error {
	decl := a.set.funcDecls[calleeFunc(call, env.info())]
	declPkg := a.set.declPkg[decl]
	if decl == nil || a.set.packages[declPkg.Dir] == nil {
		return a.errorf(call, "a chi router is passed to %s, which the route inventory cannot follow; "+
			"register routes inside an enumerated listener or an analyzed helper", a.set.exprText(call.Fun))
	}
	if a.enteredFuncs[decl] {
		return a.errorf(call, "route registration helper %s is reached more than once; "+
			"the inventory would duplicate or lose its routes", decl.Name.Name)
	}
	a.enteredFuncs[decl] = true

	child := &walkEnv{
		pkg:       declPkg,
		listener:  env.listener,
		routers:   map[*types.Var]*routerScope{},
		muxes:     map[*types.Var]bool{},
		delegates: env.delegates,
		conds:     append([]string{}, env.conds...),
		depth:     env.depth + 1,
	}
	params := flattenParams(decl.Type.Params)
	if len(params) != len(call.Args) {
		return a.errorf(call, "cannot map arguments onto %s (variadic or mismatched signature)", decl.Name.Name)
	}
	for i, arg := range call.Args {
		obj := env.varOf(arg)
		if obj == nil {
			if err := a.leakCheck(arg, env); err != nil {
				return err
			}
			continue
		}
		scope, bound := env.routers[obj]
		if !bound {
			continue
		}
		if a.set.routerKind(declPkg.info().TypeOf(params[i].typ)) != ctorChiRouter {
			return a.errorf(call, "argument %d of %s receives a chi router but is not declared chi.Router", i, decl.Name.Name)
		}
		if params[i].ident == nil {
			continue
		}
		if paramObj, ok := declPkg.info().Defs[params[i].ident].(*types.Var); ok {
			child.routers[paramObj] = scope
		}
	}
	if decl.Body == nil {
		return a.errorf(call, "route registration helper %s has no body", decl.Name.Name)
	}
	return a.walkStmts(decl.Body.List, child)
}

type param struct {
	name  string
	ident *ast.Ident // nil for an unnamed parameter
	typ   ast.Expr
}

func flattenParams(fields *ast.FieldList) []param {
	var out []param
	if fields == nil {
		return out
	}
	for _, field := range fields.List {
		if len(field.Names) == 0 {
			out = append(out, param{name: "_", typ: field.Type})
			continue
		}
		for _, name := range field.Names {
			out = append(out, param{name: name.Name, ident: name, typ: field.Type})
		}
	}
	return out
}

func (a *Analyzer) handleRouterMethod(call *ast.CallExpr, sel *ast.SelectorExpr, scope *routerScope, env *walkEnv) error {
	name := sel.Sel.Name
	switch {
	case name == "Route":
		pattern, err := a.stringArg(call, 0)
		if err != nil {
			return err
		}
		lit, err := a.funcLitArg(call, 1)
		if err != nil {
			return err
		}
		child := scope.clone()
		child.prefix = joinPattern(scope.prefix, pattern)
		child.group = joinPattern(scope.group, pattern)
		return a.walkRouterLit(lit, child, env)

	case name == "Group":
		lit, err := a.funcLitArg(call, 0)
		if err != nil {
			return err
		}
		return a.walkRouterLit(lit, scope.clone(), env)

	case name == "Use":
		// A middleware expression that captures the router — a closure or an
		// immediately invoked function that registers inside — would register
		// routes the walk never sees, so every argument is leak-checked the
		// same way a handler argument is.
		for _, arg := range call.Args {
			if err := a.leakCheck(arg, env); err != nil {
				return err
			}
			scope.mw = append(scope.mw, mwEntry{expr: a.set.exprText(arg), conds: append([]string{}, env.conds...)})
		}
		return nil

	case name == methodWith:
		return a.errorf(call, "With() result is discarded; it registers nothing")

	case name == "Mount":
		return a.errorf(call, "Mount() is not modeled by the route inventory; "+
			"the mounted handler's routes would be invisible. Add explicit support before mounting")

	case name == "NotFound" || name == "MethodNotAllowed":
		// Fallback handlers, not addressable method+path operations; the
		// handler expression still may not capture the router.
		return a.leakCheckArgs(call, env)

	case readOnlyRouterMethods[name]:
		return a.leakCheckArgs(call, env)

	case verbMethods[name] != "":
		pattern, err := a.stringArg(call, 0)
		if err != nil {
			return err
		}
		return a.emit(call, env, scope, []string{verbMethods[name]}, originExplicit, pattern, argAt(call, 1))

	case name == methodMethod || name == "MethodFunc":
		method, err := a.methodArg(call, 0, env)
		if err != nil {
			return err
		}
		pattern, err := a.stringArg(call, 1)
		if err != nil {
			return err
		}
		return a.emit(call, env, scope, []string{method}, originExplicit, pattern, argAt(call, 2))

	case name == methodHandle || name == methodHandleFunc:
		pattern, err := a.stringArg(call, 0)
		if err != nil {
			return err
		}
		return a.emit(call, env, scope, handleAllMethods, originHandleAll, pattern, argAt(call, 1))
	}
	return a.errorf(call, "unknown chi router method %q", name)
}

func (a *Analyzer) walkRouterLit(lit *ast.FuncLit, scope *routerScope, env *walkEnv) error {
	params := flattenParams(lit.Type.Params)
	if len(params) != 1 || params[0].ident == nil || a.set.routerKind(env.info().TypeOf(params[0].typ)) != ctorChiRouter {
		return a.errorf(lit, "router closure must take exactly one chi.Router parameter")
	}
	a.enteredLits[lit] = true
	child := env.child()
	child.inRouterLit = true
	if obj, ok := env.info().Defs[params[0].ident].(*types.Var); ok {
		child.routers[obj] = scope
	}
	return a.walkStmts(lit.Body.List, child)
}

// argAt returns one call argument with its redundant parentheses removed, so
// `("/path")` reads as the literal it is rather than as an unmodeled expression.
func argAt(call *ast.CallExpr, index int) ast.Expr {
	if index >= len(call.Args) {
		return nil
	}
	return unwrapParen(call.Args[index])
}

func (a *Analyzer) stringArg(call *ast.CallExpr, index int) (string, error) {
	expr := argAt(call, index)
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", a.errorf(call, "route pattern must be a string literal, got %s", a.set.exprText(expr))
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", a.errorf(call, "unquote route pattern: %v", err)
	}
	return value, nil
}

// methodArg resolves r.Method's first argument: a string constant, which
// covers a literal and an http.MethodX name alike. Anything else is refused.
func (a *Analyzer) methodArg(call *ast.CallExpr, index int, env *walkEnv) (string, error) {
	expr := argAt(call, index)
	if tv, ok := env.info().Types[expr]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return strings.ToUpper(constant.StringVal(tv.Value)), nil
	}
	return "", a.errorf(call, "HTTP method must be a literal or an http.MethodX constant, got %s", a.set.exprText(expr))
}

func (a *Analyzer) funcLitArg(call *ast.CallExpr, index int) (*ast.FuncLit, error) {
	expr := argAt(call, index)
	lit, ok := expr.(*ast.FuncLit)
	if !ok {
		return nil, a.errorf(call, "router sub-scope must be an inline closure, got %s", a.set.exprText(expr))
	}
	return lit, nil
}

func (a *Analyzer) emit(call *ast.CallExpr, env *walkEnv, scope *routerScope, methods []string, origin, pattern string, handler ast.Expr) error {
	if handler == nil {
		return a.errorf(call, "registration has no handler argument")
	}
	// A router handed in as the handler is a mount in disguise: the routes
	// behind it would hide under one wildcard row.
	if err := a.leakCheck(handler, env); err != nil {
		return err
	}
	fullPath := joinPattern(scope.prefix, pattern)
	group := scope.group
	if group == "" {
		group = "/"
	}
	sourceFile := a.set.sourceFile(call)
	delegate := a.delegatedListener(handler, env)
	if delegate == "" {
		if id := a.sealedListenerValue(handler, env); id != "" {
			return a.errorf(handler, "sealed listener handler must be built at the registration site: "+
				"%s holds the %s listener's handler, so this registration would be recorded as a leaf route "+
				"instead of a delegation. Call its entry function in the registration itself",
				a.set.exprText(handler), id)
		}
	}

	for _, method := range methods {
		info := a.classifier.describe(handler, method, fullPath, env)
		mw := renderMiddleware(scope.mw, env.conds)
		route := Route{
			Listener:          env.listener.ID,
			Namespace:         namespaceFor(fullPath),
			Method:            method,
			Path:              fullPath,
			RouteGroup:        group,
			Handler:           info.identity,
			HandlerExpr:       info.expr,
			HandlerKind:       info.kind,
			HandlerResolved:   info.resolved,
			chain:             mw,
			Conditions:        append([]string{}, env.conds...),
			Conditional:       len(env.conds) > 0,
			RequestKind:       info.requestKind,
			ResponseMediaKind: info.responseKind,
			Streams:           info.streams,
			UpgradesWebSocket: info.websocket,
			MethodOrigin:      origin,
			SourceFile:        sourceFile,
			DelegatesTo:       delegate,
		}
		if route.chain == nil {
			route.chain = []string{}
		}
		if route.Conditions == nil {
			route.Conditions = []string{}
		}
		route.AuthClass, route.AuthTraits = classifyAuth(route.chain)
		if delegate != "" {
			// The delegated listener's own surface carries its auth; the row
			// records the hand-off, not a claim about the operations behind it.
			route.Namespace = NamespaceAPIV2
			route.Handler = "listener:" + delegate
			route.HandlerKind = handlerKindDelegation
			route.HandlerResolved = true
			route.RequestKind = unknownClassification
			route.ResponseMediaKind = unknownClassification
			route.AuthClass = authDelegated
			route.AuthTraits = []string{authDelegated}
		}
		a.routes = append(a.routes, route)
	}
	return nil
}

// noteSealedListener records a local bound to a delegated listener's sealed
// entry-function result. The value is an http.Handler like any other, so only
// the walk knows what it holds; emit refuses a registration that uses it.
func (a *Analyzer) noteSealedListener(binding valueBinding, env *walkEnv) {
	if !binding.attributed || binding.obj == nil || binding.value == nil || env.sealed == nil {
		return
	}
	if id := a.sealedListenerValue(binding.value, env); id != "" {
		env.sealed[binding.obj] = id
	}
}

// sealedListenerValue reports the delegated listener an expression holds
// without being the entry call at a registration site: a variable the walk
// saw bound to a sealed value, an alias of such a variable, or a call that
// receives a sealed value as an argument (a wrapper or middleware around the
// sealed handler). The mark propagates through those shapes so a registration
// using any of them is refused rather than recorded as a leaf route.
func (a *Analyzer) sealedListenerValue(expr ast.Expr, env *walkEnv) string {
	switch e := unwrapParen(expr).(type) {
	case *ast.Ident:
		if obj := env.varOf(e); obj != nil {
			return env.sealed[obj]
		}
	case *ast.CallExpr:
		if id := a.delegatedListenerOfCall(e, env); id != "" {
			return id
		}
		for _, arg := range e.Args {
			if id := a.sealedListenerValue(arg, env); id != "" {
				return id
			}
		}
	}
	return ""
}

// delegatedListener reports the listener a handler expression delegates to:
// the expression is a direct call to the entry function of a listener whose
// DelegatedBy names the current listener's package. Anything else is a leaf
// handler. Only a direct call counts: the sealed handler must be built at the
// registration site, so no other value of that type can stand in for it.
func (a *Analyzer) delegatedListener(handler ast.Expr, env *walkEnv) string {
	call, ok := unwrapParen(handler).(*ast.CallExpr)
	if !ok {
		return ""
	}
	return a.delegatedListenerOfCall(call, env)
}

// delegatedListenerOfCall is delegatedListener for a call expression that has
// already been unwrapped.
func (a *Analyzer) delegatedListenerOfCall(call *ast.CallExpr, env *walkEnv) string {
	fn := calleeFunc(call, env.info())
	if fn == nil || fn.Pkg() == nil {
		return ""
	}
	for _, spec := range a.cfg.Listeners {
		if spec.DelegatedBy != env.listener.Dir || spec.Recv != "" {
			continue
		}
		pkg := a.set.packages[spec.Dir]
		if pkg == nil || fn.Pkg() != pkg.Pkg.Types || fn.Name() != spec.Func {
			continue
		}
		return spec.ID
	}
	return ""
}

func namespaceFor(path string) string {
	switch {
	case path == "/api/v1" || strings.HasPrefix(path, "/api/v1/"):
		return NamespaceAPIV1
	case path == "/api/v2" || strings.HasPrefix(path, "/api/v2/"):
		return NamespaceAPIV2
	case path == metricsPath:
		return NamespaceOperational
	default:
		return NamespaceUnversioned
	}
}

func joinPattern(prefix, pattern string) string {
	joined := prefix + pattern
	for strings.Contains(joined, "//") {
		joined = strings.ReplaceAll(joined, "//", "/")
	}
	if joined == "" {
		return "/"
	}
	return joined
}

// ---------------------------------------------------------------------------
// Leak check
// ---------------------------------------------------------------------------

// leakCheck refuses any router-typed value inside a construct the walk did not
// model: a bound router or mux escaping into it, a router produced inside it
// by any expression, or a router-typed variable the walk never bound. It is
// the property that makes the inventory complete: a registration can only
// happen through a router value, and every one of them is either walked or
// reported here. The check is on resolved types, so the spelling of the value
// does not matter.
func (a *Analyzer) leakCheck(node ast.Node, env *walkEnv) error {
	var found error
	ast.Inspect(node, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		expr, ok := n.(ast.Expr)
		if !ok {
			return true
		}
		if _, isKey := n.(*ast.KeyValueExpr); isKey {
			return true
		}
		tv, known := env.info().Types[expr]
		if !known {
			// Package names, field selectors and the like carry no type.
			return true
		}
		if tv.IsType() {
			// A type expression (`chi.Router` in a closure signature) is not a value.
			return false
		}
		kind := a.set.tupleRouterKind(tv.Type)
		if kind == ctorNone {
			return true
		}
		if obj := env.varOf(expr); obj != nil {
			if env.routers[obj] != nil || env.muxes[obj] {
				found = a.errorf(expr, "router value %q escapes into a construct the route inventory does not model; "+
					"routes registered through it would not appear in the inventory", obj.Name())
				return false
			}
			found = a.errorf(expr, "router value %q was never bound by the route inventory's walk; "+
				"routes registered through it would not appear in the inventory", obj.Name())
			return false
		}
		found = a.errorf(expr, "%s is produced by %s in a construct the route inventory does not model; "+
			"routes registered through it would not appear in the inventory", kind, a.set.exprText(expr))
		return false
	})
	return found
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// audit proves that no router-taking helper in the audited packages escaped
// the walk. Without it, adding a route-registering helper that nothing calls
// from an enumerated entry point would leave the inventory quietly short. The
// unreached-helper rule applies to the audited packages only; the
// repository-wide rules live in the sweep (see sweep.go).
func (a *Analyzer) audit() error {
	for _, dir := range sortedKeys(a.set.packages) {
		pkg := a.set.packages[dir]
		for _, file := range pkg.Files {
			if err := a.auditFile(pkg, file); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *Analyzer) auditFile(pkg *pkgSource, file *ast.File) error {
	var err error
	ast.Inspect(file, func(node ast.Node) bool {
		if err != nil {
			return false
		}
		switch typed := node.(type) {
		case *ast.FuncDecl:
			if a.enteredFuncs[typed] || !a.hasRouterParam(typed.Type, pkg) {
				return true
			}
			err = a.errorf(typed, "%s.%s takes a router but is never reached from a declared listener entry point; "+
				"any route it registers would be missing from the inventory", pkg.Pkg.Name, typed.Name.Name)
			return false
		case *ast.FuncLit:
			if a.enteredLits[typed] || !a.hasRouterParam(typed.Type, pkg) {
				return true
			}
			err = a.errorf(typed, "a closure taking a router in %s is never reached from a declared listener entry point", pkg.Pkg.Name)
			return false
		}
		return true
	})
	return err
}

func (a *Analyzer) hasRouterParam(sig *ast.FuncType, pkg *pkgSource) bool {
	for _, p := range flattenParams(sig.Params) {
		if a.set.routerKind(pkg.info().TypeOf(p.typ)) != ctorNone {
			return true
		}
	}
	return false
}

func sortedKeys[V any](in map[string]V) []string {
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func (a *Analyzer) errorf(node ast.Node, format string, args ...any) error {
	pos := a.set.position(node)
	return fmt.Errorf("%s:%d: %s", a.set.relPath(pos.Filename), pos.Line, fmt.Sprintf(format, args...))
}

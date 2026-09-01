package routeinventory

import (
	"fmt"
	"go/ast"
	"go/token"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// chiImportPath is the router library the native listeners use. The analyzer
// only trusts type syntax that names this package.
const chiImportPath = "github.com/go-chi/chi/v5"

// chiNewRouter and chiNewMux are the two constructors a chi router can come
// from. chi.NewMux returns the same *chi.Mux as chi.NewRouter, so anything that
// recognizes one has to recognize the other; a walk that knew only NewRouter
// would drop every route registered on a NewMux router without a word.
const (
	chiNewRouter = "NewRouter"
	chiNewMux    = "NewMux"
)

// httpImportPath and serveMuxNew identify the standard-library router the
// process root listener is built from.
const (
	httpImportPath = "net/http"
	serveMuxNew    = "NewServeMux"
)

// Router method names the analyzer matches on. They are spelled once so a
// typo cannot make one call site model a method the others do not.
const (
	methodHandle     = "Handle"
	methodHandleFunc = "HandleFunc"
	methodWith       = "With"
	methodServeHTTP  = "ServeHTTP"
	methodRoutes     = "Routes"
	// methodHandler is http.ServeMux's read-only Handler lookup; it is also the
	// name of the http.Server field a listener handler may be assigned to.
	methodHandler = "Handler"
)

// metricsPath is the one operational path the namespace classifier singles out.
const metricsPath = "/metrics"

// MethodOrigin values: how a row's method variant was produced.
const (
	originExplicit  = "explicit"
	originHandleAll = "handle_all"
)

// isChiConstructor reports whether a chi package function returns a router.
func isChiConstructor(name string) bool {
	return name == chiNewRouter || name == chiNewMux
}

// unwrapParen strips redundant parentheses. Every place the analyzer asserts a
// *ast.CallExpr, a *ast.SelectorExpr or an *ast.Ident goes through it:
// `(chi.NewRouter())` constructs exactly what `chi.NewRouter()` does, and a
// walk that recognized only the bare spelling would bind a router it never
// models — dropping the routes registered on it without a word.
func unwrapParen(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}

// ctorKind is what the analyzer decides an expression constructs.
type ctorKind int

const (
	ctorNone ctorKind = iota
	// ctorChiRouter is chi.NewRouter() or chi.NewMux().
	ctorChiRouter
	// ctorServeMux is http.NewServeMux().
	ctorServeMux
	// ctorChiUnmodeled is any other call into the chi package. chi gains
	// constructors over time and a value from one of them may well be a router.
	ctorChiUnmodeled
)

func (k ctorKind) String() string {
	switch k {
	case ctorChiRouter:
		return "a chi router"
	case ctorServeMux:
		return "an http.ServeMux"
	case ctorChiUnmodeled:
		return "a value from an unmodeled chi constructor"
	}
	return "nothing"
}

// callCtorKind classifies an expression as a router construction, resolving the
// package identifier through the file's imports so a same-named local function
// cannot trigger it.
func callCtorKind(expr ast.Expr, file *ast.File) ctorKind {
	if expr == nil {
		return ctorNone
	}
	call, ok := unwrapParen(expr).(*ast.CallExpr)
	if !ok {
		return ctorNone
	}
	sel, ok := unwrapParen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return ctorNone
	}
	ident, ok := unwrapParen(sel.X).(*ast.Ident)
	if !ok {
		return ctorNone
	}
	switch importPathFor(file, ident.Name) {
	case chiImportPath:
		if isChiConstructor(sel.Sel.Name) {
			return ctorChiRouter
		}
		return ctorChiUnmodeled
	case httpImportPath:
		if sel.Sel.Name == serveMuxNew {
			return ctorServeMux
		}
	}
	return ctorNone
}

// literalCtorKind classifies the non-call spellings of a router construction:
// `new(http.ServeMux)`, `&http.ServeMux{}`, `http.ServeMux{}` and the chi.Mux
// equivalents. A zero http.ServeMux is fully functional, so these build a
// router as surely as the constructor does. They are recognized only to be
// refused: the inventory models one construction shape, and a second spelling
// would be a second place for a router to come from.
func literalCtorKind(expr ast.Expr, file *ast.File) ctorKind {
	if expr == nil {
		return ctorNone
	}
	switch typed := unwrapParen(expr).(type) {
	case *ast.UnaryExpr:
		if typed.Op == token.AND {
			return literalCtorKind(typed.X, file)
		}
	case *ast.CompositeLit:
		return routerTypeKind(typed.Type, file)
	case *ast.CallExpr:
		if ident, ok := unwrapParen(typed.Fun).(*ast.Ident); ok && ident.Name == "new" && len(typed.Args) == 1 &&
			importPathFor(file, "new") == "" {
			return routerTypeKind(typed.Args[0], file)
		}
	}
	return ctorNone
}

// routerTypeKind maps a router type expression to the constructor kind that
// builds it.
func routerTypeKind(typ ast.Expr, file *ast.File) ctorKind {
	switch {
	case typ == nil:
		return ctorNone
	case isChiRouterType(typ, file):
		return ctorChiRouter
	case isServeMuxType(typ, file):
		return ctorServeMux
	}
	return ctorNone
}

// Listener IDs. They are the join key between the artifact and the
// per-listener reconciliation tests.
const (
	ListenerRoot          = "root"
	ListenerAPI           = "api"
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

// ListenerSpec names one HTTP listener and the function that builds its router.
type ListenerSpec struct {
	ID          string
	Description string
	// Kind is ListenerKindChi (the zero value) or ListenerKindServeMux.
	Kind string
	Dir  string // repo-relative package directory
	Recv string // receiver type name, empty for a package-level function
	Func string
	// Delegates maps a parameter name of the entry function to the listener ID
	// whose surface that parameter carries. A root listener that hands /api/ to
	// the API router registers a delegation, not a leaf route, and the row says
	// so instead of pretending the whole namespace is one handler.
	Delegates map[string]string
}

// Entrypoint renders the listener's entry function for the artifact.
func (l ListenerSpec) Entrypoint() string {
	if l.Recv == "" {
		return l.Dir + "." + l.Func
	}
	return l.Dir + ".(*" + l.Recv + ")." + l.Func
}

// declName is the enclosing declaration's name as the construction audit spells
// it: `Func` for a package-level function, `Recv.Func` for a method.
func (l ListenerSpec) declName() string {
	if l.Recv == "" {
		return l.Func
	}
	return l.Recv + "." + l.Func
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
	Root       string
	ModulePath string
	Listeners  []ListenerSpec
	// AuditDirs are the package directories parsed and audited. Every listener
	// directory must appear here; add the packages that register routes on a
	// listener's behalf.
	AuditDirs []string
	// ScanRoots are the directory trees swept for routers constructed outside
	// the enumerated listeners, and for registrations made on a listener's
	// return value after construction. "." sweeps the whole repository, which
	// is what keeps a router in a tree nobody enumerated from going unnoticed.
	ScanRoots  []string
	Exclusions []RouterExclusion
}

// Analyzer enumerates route registrations from source.
type Analyzer struct {
	cfg  Config
	fset *token.FileSet
	set  *sourceSet

	routes []Route

	enteredFuncs map[*ast.FuncDecl]bool
	enteredLits  map[*ast.FuncLit]bool
	entryFuncs   map[*ast.FuncDecl]ListenerSpec

	// rootConstructed guards the current listener walk against a second router
	// construction. The first one is the value the entry point returns and is
	// therefore anchored at "/"; a second one is only reachable through an
	// attachment the walk cannot see, so its rows would claim wrong paths.
	rootConstructed bool

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
	fset := token.NewFileSet()
	dirs := append([]string{}, cfg.AuditDirs...)
	for _, listener := range cfg.Listeners {
		dirs = append(dirs, listener.Dir)
	}
	set, err := loadSources(fset, cfg.Root, cfg.ModulePath, dirs)
	if err != nil {
		return nil, err
	}
	a := &Analyzer{
		cfg:          cfg,
		fset:         fset,
		set:          set,
		enteredFuncs: map[*ast.FuncDecl]bool{},
		enteredLits:  map[*ast.FuncLit]bool{},
		entryFuncs:   map[*ast.FuncDecl]ListenerSpec{},
		classifier:   newClassifier(set),
	}
	a.classifier.owner = a
	for _, listener := range cfg.Listeners {
		if err := a.walkListener(listener); err != nil {
			return nil, err
		}
	}
	if err := a.audit(); err != nil {
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

type walkEnv struct {
	pkg      *pkgSource
	file     *ast.File
	listener ListenerSpec
	routers  map[string]*routerScope
	// chiValues are locals initialized from a chi package call the walk does
	// not model. They are tracked so a method call on one is refused instead of
	// being read as ordinary application code.
	chiValues map[string]string
	// muxes are the bound http.ServeMux locals of a servemux listener.
	muxes map[string]bool
	// delegates maps an in-scope name to the listener ID it carries.
	delegates map[string]string
	conds     []string
	varTypes  map[string]string
	depth     int
	entry     bool
}

func (e *walkEnv) child() *walkEnv {
	routers := make(map[string]*routerScope, len(e.routers))
	for name, scope := range e.routers {
		routers[name] = scope
	}
	chiValues := make(map[string]string, len(e.chiValues))
	for name, expr := range e.chiValues {
		chiValues[name] = expr
	}
	muxes := make(map[string]bool, len(e.muxes))
	for name := range e.muxes {
		muxes[name] = true
	}
	return &walkEnv{
		pkg: e.pkg, file: e.file, listener: e.listener,
		routers: routers, chiValues: chiValues, muxes: muxes, delegates: e.delegates,
		conds:    append([]string{}, e.conds...),
		varTypes: e.varTypes, depth: e.depth, entry: e.entry,
	}
}

// boundNames is every name in scope that denotes a router or mux value. The
// leak check refuses any use of one it did not model.
func (e *walkEnv) boundNames() map[string]bool {
	names := make(map[string]bool, len(e.routers)+len(e.muxes))
	for name := range e.routers {
		names[name] = true
	}
	for name := range e.muxes {
		names[name] = true
	}
	return names
}

func (a *Analyzer) walkListener(spec ListenerSpec) error {
	pkg := a.set.packages[spec.Dir]
	if pkg == nil {
		return fmt.Errorf("listener %s: package %s not loaded", spec.ID, spec.Dir)
	}
	key := spec.Func
	decl := pkg.funcs[key]
	if spec.Recv != "" {
		decl = pkg.methods[spec.Recv+"."+spec.Func]
	}
	if decl == nil {
		return fmt.Errorf("listener %s: entry function %s not found in %s", spec.ID, spec.Entrypoint(), spec.Dir)
	}
	a.entryFuncs[decl] = spec
	a.enteredFuncs[decl] = true
	a.rootConstructed = false

	file := pkg.fileOf[decl]
	env := &walkEnv{
		pkg:       pkg,
		file:      file,
		listener:  spec,
		routers:   map[string]*routerScope{},
		chiValues: map[string]string{},
		muxes:     map[string]bool{},
		delegates: map[string]string{},
		varTypes:  a.collectVarTypes(pkg, file, decl),
		entry:     true,
	}
	if decl.Body == nil {
		return fmt.Errorf("listener %s: entry function %s has no body", spec.ID, spec.Entrypoint())
	}
	if spec.kind() == ListenerKindServeMux {
		for _, p := range flattenParams(decl.Type.Params) {
			if id := spec.Delegates[p.name]; id != "" {
				env.delegates[p.name] = id
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
		ident, ok := unwrapParen(sel.X).(*ast.Ident)
		if !ok {
			return a.leakCheck(typed, env)
		}
		if expr, unmodeled := env.chiValues[ident.Name]; unmodeled {
			return a.errorf(call, "%s comes from %s, a chi constructor the route inventory does not model; "+
				"a method call on it could register routes the inventory cannot see", ident.Name, expr)
		}
		if !env.muxes[ident.Name] {
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
		return a.walkBinding(typed, bindingsOfAssign(typed), env, ListenerKindServeMux)

	case *ast.DeclStmt:
		bindings, ok := bindingsOfDecl(typed)
		if !ok {
			return a.leakCheck(typed, env)
		}
		return a.walkBinding(typed, bindings, env, ListenerKindServeMux)

	case *ast.ReturnStmt:
		if len(typed.Results) == 1 && env.muxes[identName(typed.Results[0])] {
			return nil
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
		return nil
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
	sourceFile := env.pkg.FileNames[env.pkg.fileFor(call)]

	delegate := ""
	if ident, ok := handler.(*ast.Ident); ok {
		delegate = env.delegates[ident.Name]
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
		return a.walkBinding(typed, bindingsOfAssign(typed), env, ListenerKindChi)

	case *ast.DeclStmt:
		bindings, ok := bindingsOfDecl(typed)
		if !ok {
			return a.leakCheck(typed, env)
		}
		return a.walkBinding(typed, bindings, env, ListenerKindChi)

	case *ast.ReturnStmt:
		if env.entry && env.depth == 0 && a.returnsBoundRouter(typed, env) {
			return nil
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
// and declared type it was bound with. Bindings are read out of `:=`/`=`
// assignments and `var` declarations alike, so a router cannot enter scope
// through a spelling the walk never inspects.
type valueBinding struct {
	name  string   // "" when the target is not a plain identifier
	value ast.Expr // nil for a declaration with no initializer
	typ   ast.Expr // declared type, nil when it is inferred
	// attributed is false when a single initializer feeds several names
	// (`a, b := f()`), so no name can be tied to what it produced.
	attributed bool
}

// bindingsOfAssign flattens an assignment into its bindings.
func bindingsOfAssign(stmt *ast.AssignStmt) []valueBinding {
	if len(stmt.Lhs) == len(stmt.Rhs) {
		out := make([]valueBinding, 0, len(stmt.Lhs))
		for i, lhs := range stmt.Lhs {
			out = append(out, valueBinding{name: identName(lhs), value: stmt.Rhs[i], attributed: true})
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

// bindingsOfDecl flattens a `var` declaration statement into its bindings. It
// reports false for any other declaration.
func bindingsOfDecl(stmt *ast.DeclStmt) ([]valueBinding, bool) {
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
		switch {
		case len(value.Values) == 0:
			// `var r chi.Router` — declared, uninitialized, and still a name a
			// registration could be made through.
			for _, name := range value.Names {
				out = append(out, valueBinding{name: name.Name, typ: value.Type, attributed: true})
			}
		case len(value.Names) == len(value.Values):
			for i, name := range value.Names {
				out = append(out, valueBinding{
					name: name.Name, value: value.Values[i], typ: value.Type, attributed: true,
				})
			}
		default:
			for _, initializer := range value.Values {
				out = append(out, valueBinding{value: initializer, typ: value.Type})
			}
		}
	}
	return out, true
}

func identName(expr ast.Expr) string {
	ident, ok := unwrapParen(expr).(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name
}

// walkBinding models every way a router or mux value can be bound. A binding
// the walk cannot tie to exactly one name is refused rather than approximated:
// a router nobody can name is a router nobody can prove the attachment of.
func (a *Analyzer) walkBinding(stmt ast.Stmt, bindings []valueBinding, env *walkEnv, kind string) error {
	interesting := false
	for _, binding := range bindings {
		if callCtorKind(binding.value, env.file) != ctorNone || literalCtorKind(binding.value, env.file) != ctorNone ||
			a.routerTypeName(binding.typ, env) != "" {
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

// routerTypeName names the router type a type expression denotes, or "".
func (a *Analyzer) routerTypeName(typ ast.Expr, env *walkEnv) string {
	switch {
	case typ == nil:
		return ""
	case isChiRouterType(typ, env.file):
		return "chi router"
	case isServeMuxType(typ, env.file):
		return "http.ServeMux"
	}
	return ""
}

func (a *Analyzer) bindOne(stmt ast.Stmt, binding valueBinding, env *walkEnv, kind string) error {
	ctor := callCtorKind(binding.value, env.file)
	declared := a.routerTypeName(binding.typ, env)
	if literal := literalCtorKind(binding.value, env.file); literal != ctorNone {
		return a.errorf(stmt, "%s is built by literal or new() in the %s listener entry point; "+
			"the route inventory recognizes only chi.NewRouter(), chi.NewMux() and http.NewServeMux(), "+
			"so this value would carry registrations it never sees", literal, env.listener.ID)
	}
	if ctor == ctorNone && declared == "" {
		// A neighboring binding in the same statement; it still must not carry
		// an already-bound router into an unmodeled construct.
		if binding.value == nil {
			return nil
		}
		return a.leakCheck(binding.value, env)
	}
	if declared != "" {
		return a.errorf(stmt, "a %s value is bound through an explicit type declaration; "+
			"the route inventory models only `name := constructor()`, so it cannot prove what this value is "+
			"or where it attaches", declared)
	}
	if !binding.attributed || binding.name == "" {
		return a.errorf(stmt, "%s is constructed in a binding form the route inventory does not model; "+
			"bind each router with a single `name := constructor()` so the walk can prove where it attaches", ctor)
	}
	switch kind {
	case ListenerKindServeMux:
		return a.bindMuxValue(stmt, binding, ctor, env)
	default:
		return a.bindChiValue(stmt, binding, ctor, env)
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
		env.routers[binding.name] = &routerScope{}
		return nil
	case ctorServeMux:
		return a.errorf(stmt, "an http.ServeMux is constructed inside the %s chi listener entry point; "+
			"the inventory does not model what is registered on it", env.listener.ID)
	default:
		// A chi constructor the walk does not model may still return a router,
		// so the value is tracked and any method call on it is refused.
		env.chiValues[binding.name] = a.set.exprText(unwrapParen(binding.value))
		return nil
	}
}

func (a *Analyzer) bindMuxValue(stmt ast.Stmt, binding valueBinding, ctor ctorKind, env *walkEnv) error {
	switch ctor {
	case ctorServeMux:
		if len(env.muxes) > 0 {
			return a.errorf(stmt, "a second http.ServeMux is constructed in the %s listener entry point; "+
				"the inventory cannot prove which one the listener serves", env.listener.ID)
		}
		env.muxes[binding.name] = true
		return nil
	case ctorChiRouter:
		return a.errorf(stmt, "a chi router is constructed inside the %s http.ServeMux listener entry point; "+
			"the inventory does not model what is registered on it", env.listener.ID)
	default:
		env.chiValues[binding.name] = a.set.exprText(unwrapParen(binding.value))
		return nil
	}
}

func (a *Analyzer) returnsBoundRouter(stmt *ast.ReturnStmt, env *walkEnv) bool {
	if len(stmt.Results) != 1 {
		return false
	}
	name := identName(stmt.Results[0])
	if name == "" {
		return false
	}
	_, bound := env.routers[name]
	return bound
}

// resolveRouter maps an expression to the router scope it denotes, following
// With() chains. It never invents a scope: an expression it does not model
// returns false and the caller refuses the construct.
func (a *Analyzer) resolveRouter(expr ast.Expr, env *walkEnv) (*routerScope, bool, error) {
	switch typed := unwrapParen(expr).(type) {
	case *ast.Ident:
		scope, ok := env.routers[typed.Name]
		return scope, ok, nil
	case *ast.CallExpr:
		sel, ok := unwrapParen(typed.Fun).(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != methodWith {
			return nil, false, nil
		}
		base, ok, err := a.resolveRouter(sel.X, env)
		if err != nil || !ok {
			return nil, ok, err
		}
		derived := base.clone()
		for _, arg := range typed.Args {
			derived.mw = append(derived.mw, mwEntry{expr: a.set.exprText(arg), conds: append([]string{}, env.conds...)})
		}
		return derived, true, nil
	}
	return nil, false, nil
}

func (a *Analyzer) handleCall(call *ast.CallExpr, env *walkEnv) (bool, error) {
	if sel, ok := unwrapParen(call.Fun).(*ast.SelectorExpr); ok {
		if ident, ok := unwrapParen(sel.X).(*ast.Ident); ok {
			if expr, unmodeled := env.chiValues[ident.Name]; unmodeled {
				return true, a.errorf(call, "%s comes from %s, a chi constructor the route inventory does not model; "+
					"a method call on it could register routes the inventory cannot see", ident.Name, expr)
			}
		}
		scope, isRouter, err := a.resolveRouter(sel.X, env)
		if err != nil {
			return false, err
		}
		if isRouter {
			return true, a.handleRouterMethod(call, sel, scope, env)
		}
	}
	// A router handed to another function: follow it, or fail.
	if a.callPassesRouter(call, env) {
		return true, a.followHelper(call, env)
	}
	return false, nil
}

func (a *Analyzer) callPassesRouter(call *ast.CallExpr, env *walkEnv) bool {
	for _, arg := range call.Args {
		if _, bound := env.routers[identName(arg)]; bound {
			return true
		}
	}
	return false
}

func (a *Analyzer) followHelper(call *ast.CallExpr, env *walkEnv) error {
	decl, declPkg := a.resolveFuncDecl(call.Fun, env)
	if decl == nil {
		return a.errorf(call, "a chi router is passed to %s, which the route inventory cannot follow; "+
			"register routes inside an enumerated listener or an analyzed helper", a.set.exprText(call.Fun))
	}
	if a.enteredFuncs[decl] {
		return a.errorf(call, "route registration helper %s is reached more than once; "+
			"the inventory would duplicate or lose its routes", decl.Name.Name)
	}
	a.enteredFuncs[decl] = true

	declFile := declPkg.fileOf[decl]
	child := &walkEnv{
		pkg:       declPkg,
		file:      declFile,
		listener:  env.listener,
		routers:   map[string]*routerScope{},
		chiValues: map[string]string{},
		muxes:     map[string]bool{},
		delegates: env.delegates,
		conds:     append([]string{}, env.conds...),
		varTypes:  a.collectVarTypes(declPkg, declFile, decl),
		depth:     env.depth + 1,
	}
	params := flattenParams(decl.Type.Params)
	if len(params) != len(call.Args) {
		return a.errorf(call, "cannot map arguments onto %s (variadic or mismatched signature)", decl.Name.Name)
	}
	for i, arg := range call.Args {
		name := identName(arg)
		if name == "" {
			if err := a.leakCheck(arg, env); err != nil {
				return err
			}
			continue
		}
		scope, bound := env.routers[name]
		if !bound {
			continue
		}
		if !isChiRouterType(params[i].typ, declFile) {
			return a.errorf(call, "argument %d of %s receives a chi router but is not declared chi.Router", i, decl.Name.Name)
		}
		child.routers[params[i].name] = scope
	}
	if decl.Body == nil {
		return a.errorf(call, "route registration helper %s has no body", decl.Name.Name)
	}
	return a.walkStmts(decl.Body.List, child)
}

type param struct {
	name string
	typ  ast.Expr
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
			out = append(out, param{name: name.Name, typ: field.Type})
		}
	}
	return out
}

func (a *Analyzer) resolveFuncDecl(fun ast.Expr, env *walkEnv) (*ast.FuncDecl, *pkgSource) {
	switch typed := unwrapParen(fun).(type) {
	case *ast.Ident:
		if decl := env.pkg.funcs[typed.Name]; decl != nil {
			return decl, env.pkg
		}
	case *ast.SelectorExpr:
		ident, ok := unwrapParen(typed.X).(*ast.Ident)
		if !ok {
			return nil, nil
		}
		importPath := importPathFor(env.file, ident.Name)
		if importPath == "" {
			return nil, nil
		}
		pkg := a.set.byImport[importPath]
		if pkg == nil {
			return nil, nil
		}
		if decl := pkg.funcs[typed.Sel.Name]; decl != nil {
			return decl, pkg
		}
	}
	return nil, nil
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
		for _, arg := range call.Args {
			scope.mw = append(scope.mw, mwEntry{expr: a.set.exprText(arg), conds: append([]string{}, env.conds...)})
		}
		return nil

	case name == methodWith:
		return a.errorf(call, "With() result is discarded; it registers nothing")

	case name == "Mount":
		return a.errorf(call, "Mount() is not modeled by the route inventory; "+
			"the mounted handler's routes would be invisible. Add explicit support before mounting")

	case name == "NotFound" || name == "MethodNotAllowed":
		// Fallback handlers, not addressable method+path operations.
		return nil

	case readOnlyRouterMethods[name]:
		return nil

	case verbMethods[name] != "":
		pattern, err := a.stringArg(call, 0)
		if err != nil {
			return err
		}
		return a.emit(call, env, scope, []string{verbMethods[name]}, originExplicit, pattern, argAt(call, 1))

	case name == "Method" || name == "MethodFunc":
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
	if len(params) != 1 || !isChiRouterType(params[0].typ, env.file) {
		return a.errorf(lit, "router closure must take exactly one chi.Router parameter")
	}
	a.enteredLits[lit] = true
	child := env.child()
	child.routers[params[0].name] = scope
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

// methodArg resolves r.Method's first argument: a string literal or an
// http.MethodX constant. Anything else is refused.
func (a *Analyzer) methodArg(call *ast.CallExpr, index int, env *walkEnv) (string, error) {
	expr := argAt(call, index)
	if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return "", a.errorf(call, "unquote method: %v", err)
		}
		return strings.ToUpper(value), nil
	}
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		if ident, ok := sel.X.(*ast.Ident); ok && importPathFor(env.file, ident.Name) == "net/http" {
			if method, found := strings.CutPrefix(sel.Sel.Name, "Method"); found && method != "" {
				return strings.ToUpper(method), nil
			}
		}
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
	sourceFile := env.pkg.FileNames[env.pkg.fileFor(call)]

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
		}
		if route.chain == nil {
			route.chain = []string{}
		}
		if route.Conditions == nil {
			route.Conditions = []string{}
		}
		route.AuthClass, route.AuthTraits = classifyAuth(route.chain)
		a.routes = append(a.routes, route)
	}
	return nil
}

func namespaceFor(path string) string {
	switch {
	case path == "/api/v1" || strings.HasPrefix(path, "/api/v1/"):
		return NamespaceAPIV1
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

// leakCheck refuses any use of a bound router or mux value the walk did not
// model, and any router value the walk cannot account for at all — one
// constructed inside an unmodeled construct, or recovered from a type
// assertion. It is the property that makes the inventory complete: a
// registration can only happen through such a value, and every one of them is
// either walked or reported here.
func (a *Analyzer) leakCheck(node ast.Node, env *walkEnv) error {
	bound := env.boundNames()
	shadow := map[string]bool{}
	var found ast.Node
	var foundName string
	var foundReason string

	var visit func(ast.Node, map[string]bool)
	visit = func(n ast.Node, shadowed map[string]bool) {
		if n == nil || found != nil {
			return
		}
		switch typed := n.(type) {
		case *ast.Ident:
			if bound[typed.Name] && !shadowed[typed.Name] {
				found, foundName = typed, typed.Name
			}
			return
		case *ast.CallExpr, *ast.CompositeLit:
			// A construction the walk never bound: whatever is registered on the
			// result cannot reach the inventory.
			expr, isExpr := n.(ast.Expr)
			if !isExpr {
				return
			}
			kind := callCtorKind(expr, env.file)
			if kind == ctorNone {
				kind = literalCtorKind(expr, env.file)
			}
			switch kind {
			case ctorChiRouter:
				found, foundReason = typed, "a chi router is constructed"
				return
			case ctorServeMux:
				found, foundReason = typed, "an http.ServeMux is constructed"
				return
			}
		case *ast.TypeAssertExpr:
			if name := a.routerTypeName(typed.Type, env); name != "" {
				found, foundReason = typed, "a "+name+" value is recovered by type assertion"
				return
			}
		case *ast.SelectorExpr:
			// A constructor referenced as a function value builds a router
			// wherever it is eventually called, which is nowhere the walk looks.
			if kind := ctorFunctionValue(typed, env.file); kind != "" {
				found, foundReason = typed, kind+" is referenced as a function value"
				return
			}
			// Only the base of a selector can be a router value.
			visit(typed.X, shadowed)
			return
		case *ast.FuncLit:
			inner := cloneShadow(shadowed)
			for _, p := range flattenParams(typed.Type.Params) {
				inner[p.name] = true
			}
			for _, p := range flattenParams(typed.Type.Results) {
				inner[p.name] = true
			}
			ast.Inspect(typed.Body, func(child ast.Node) bool {
				if child == nil || found != nil {
					return false
				}
				if child == typed.Body {
					return true
				}
				visit(child, inner)
				return false
			})
			return
		case *ast.AssignStmt:
			if typed.Tok == token.DEFINE {
				// `r := ...` introduces a new binding; only the right-hand side
				// can still refer to the router.
				for _, rhs := range typed.Rhs {
					visit(rhs, shadowed)
				}
				return
			}
		case *ast.RangeStmt:
			inner := cloneShadow(shadowed)
			if typed.Tok == token.DEFINE {
				for _, key := range []ast.Expr{typed.Key, typed.Value} {
					if ident, ok := key.(*ast.Ident); ok {
						inner[ident.Name] = true
					}
				}
			}
			visit(typed.X, shadowed)
			if typed.Body != nil {
				for _, stmt := range typed.Body.List {
					visit(stmt, inner)
				}
			}
			return
		}
		ast.Inspect(n, func(child ast.Node) bool {
			if child == nil || found != nil {
				return false
			}
			if child == n {
				return true
			}
			visit(child, shadowed)
			return false
		})
	}
	visit(node, shadow)

	switch {
	case found != nil && foundReason != "":
		return a.errorf(found, "%s in a construct the route inventory does not model; "+
			"routes registered through it would not appear in the inventory", foundReason)
	case found != nil:
		return a.errorf(found, "router value %q escapes into a construct the route inventory does not model; "+
			"routes registered through it would not appear in the inventory", foundName)
	}
	return nil
}

func cloneShadow(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// audit proves that no chi router value in the analyzed packages escaped the
// walk. Without it, adding a route-registering helper that nothing calls from
// an enumerated entry point would leave the inventory quietly short. The
// unreached-helper rule below applies to the audited packages only; the
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
	return a.auditScannedTrees()
}

func (a *Analyzer) auditFile(pkg *pkgSource, file *ast.File) error {
	var err error
	ast.Inspect(file, func(node ast.Node) bool {
		if err != nil {
			return false
		}
		switch typed := node.(type) {
		case *ast.FuncDecl:
			if !hasChiRouterParam(typed.Type, file) || a.enteredFuncs[typed] {
				return true
			}
			err = a.errorf(typed, "%s.%s takes a chi.Router but is never reached from a declared listener entry point; "+
				"any route it registers would be missing from the inventory", pkg.Name, typed.Name.Name)
			return false
		case *ast.FuncLit:
			if !hasChiRouterParam(typed.Type, file) || a.enteredLits[typed] {
				return true
			}
			err = a.errorf(typed, "a closure taking chi.Router in %s is never reached from a declared listener entry point", pkg.Name)
			return false
		}
		return true
	})
	return err
}

func sortedKeys[V any](in map[string]V) []string {
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Type syntax helpers
// ---------------------------------------------------------------------------

// isServeMuxType reports whether a type expression names http.ServeMux.
func isServeMuxType(expr ast.Expr, file *ast.File) bool {
	switch typed := unwrapParen(expr).(type) {
	case *ast.StarExpr:
		return isServeMuxType(typed.X, file)
	case *ast.SelectorExpr:
		ident, ok := unwrapParen(typed.X).(*ast.Ident)
		return ok && typed.Sel.Name == "ServeMux" && importPathFor(file, ident.Name) == httpImportPath
	}
	return false
}

func isChiRouterType(expr ast.Expr, file *ast.File) bool {
	switch typed := unwrapParen(expr).(type) {
	case *ast.StarExpr:
		return isChiRouterType(typed.X, file)
	case *ast.SelectorExpr:
		ident, ok := unwrapParen(typed.X).(*ast.Ident)
		if !ok {
			return false
		}
		if importPathFor(file, ident.Name) != chiImportPath {
			return false
		}
		switch typed.Sel.Name {
		case "Router", methodRoutes, "Mux":
			return true
		}
	}
	return false
}

func hasChiRouterParam(sig *ast.FuncType, file *ast.File) bool {
	for _, p := range flattenParams(sig.Params) {
		if isChiRouterType(p.typ, file) {
			return true
		}
	}
	return false
}

func (a *Analyzer) errorf(node ast.Node, format string, args ...any) error {
	pos := a.set.position(node)
	rel := pos.Filename
	if trimmed, err := filepath.Rel(a.cfg.Root, pos.Filename); err == nil {
		rel = filepath.ToSlash(trimmed)
	}
	return fmt.Errorf("%s:%d: %s", rel, pos.Line, fmt.Sprintf(format, args...))
}

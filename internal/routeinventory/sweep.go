package routeinventory

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// The sweep is the repository-wide half of the completeness guarantee. The walk
// enumerates what the declared listeners register; the sweep proves that no
// route can exist anywhere else. It parses every non-test Go file under the
// scan roots and refuses, rather than models, each way a router value could be
// built or reached outside the walk:
//
//   - a router or mux constructed outside a listener entry function or a
//     recorded exclusion, whether by its constructor call, by a composite
//     literal, by new(), or by a variable or field declared with its type;
//   - a constructor referenced as a function value instead of being called,
//     and a dot-import of chi or net/http, either of which would let a
//     construction be spelled in a way the sweep does not recognize;
//   - a named type, alias, struct field, interface embedding or element type
//     spelled with a router type, which would give a router a second name;
//   - anything that would serve http.DefaultServeMux: http.Handle and
//     http.HandleFunc, a reference to http.DefaultServeMux, a nil handler
//     passed to http.ListenAndServe and its siblings, an http.Server built
//     without a non-nil Handler, and an import of net/http/pprof or expvar;
//   - a listener handler, once its entry point has returned it, used in any
//     way other than the handful the sweep vouches for (see funcScope.call).
//
// Registration-capable values the sweep cannot rule out this way are the
// listener handlers themselves: they are tracked as "carriers" through every
// binding form the walk understands, per function and through package-level
// variables, and every use of one that is not explicitly allowed is refused.
// A handler-typed parameter of a same-directory function that receives a
// carrier is audited with that parameter seeded as a carrier, so the
// http.Handler vouch is backed by inspection of the callee rather than by the
// interface's method set alone.

// sweptFile is one parsed non-test Go file under the scan roots.
type sweptFile struct {
	rel  string
	dir  string
	fset *token.FileSet
	file *ast.File
}

func (f *sweptFile) at(node ast.Node) string {
	return fmt.Sprintf("%s:%d", f.rel, f.fset.Position(node.Pos()).Line)
}

// sweptFunc is a package-level function the sweep can resolve by name.
type sweptFunc struct {
	file *sweptFile
	decl *ast.FuncDecl
}

func scopedKey(dir, name string) string { return dir + "#" + name }

// sweep holds the cross-file state of one audit: the parsed files, the
// package-level functions and variables they declare, and the package-level
// variables found to carry a listener handler.
type sweep struct {
	cfg             Config
	excluded        map[string]bool
	entryDecls      map[string]bool
	listenerImports map[string]ListenerSpec

	files []*sweptFile
	funcs map[string]sweptFunc // package-level functions by dir#name
	// pkgVars are the package-level variable names by dir#name. Assigning a
	// carrier to one of them makes it a carrier everywhere in its package.
	pkgVars           map[string]bool
	pkgCarriers       map[string]bool
	pkgListenerValues map[string]bool
	// grew is set when a package-level carrier is discovered; the audit runs
	// again so functions inspected before the discovery see it.
	grew bool

	vouched  map[*ast.FuncDecl]bool
	findings []string
}

// auditScannedTrees sweeps every Go file in the scanned trees. See the package
// comment at the top of this file for what it refuses.
func (a *Analyzer) auditScannedTrees() error {
	s := &sweep{
		cfg:               a.cfg,
		excluded:          map[string]bool{},
		entryDecls:        map[string]bool{},
		listenerImports:   map[string]ListenerSpec{},
		funcs:             map[string]sweptFunc{},
		pkgVars:           map[string]bool{},
		pkgCarriers:       map[string]bool{},
		pkgListenerValues: map[string]bool{},
	}
	for _, exclusion := range a.cfg.Exclusions {
		s.excluded[exclusion.key()] = true
	}
	// A construction is allowed inside its own listener's entry declaration,
	// wherever in the listener package that declaration lives. Directory-wide
	// or file-wide allowances are deliberately not offered: they would cover
	// routers a later change adds beside the entry point.
	for _, listener := range a.cfg.Listeners {
		s.entryDecls[scopedKey(listener.Dir, listener.declName())] = true
		s.listenerImports[path.Join(a.cfg.ModulePath, listener.Dir)] = listener
	}
	if err := s.load(); err != nil {
		return err
	}
	s.collectDecls()
	s.run()
	if len(s.findings) == 0 {
		return nil
	}
	sort.Strings(s.findings)
	unique := s.findings[:1]
	for _, finding := range s.findings[1:] {
		if finding != unique[len(unique)-1] {
			unique = append(unique, finding)
		}
	}
	return errors.New(strings.Join(unique, "; "))
}

// load parses every non-test Go file in the scan roots once.
func (s *sweep) load() error {
	for _, root := range s.cfg.ScanRoots {
		walkRoot := filepath.Join(s.cfg.Root, filepath.FromSlash(root))
		err := filepath.WalkDir(walkRoot, func(target string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if target != walkRoot && skipScanDir(entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(target, ".go") || strings.HasSuffix(target, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(s.cfg.Root, target)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			fset := token.NewFileSet()
			file, parseErr := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
			if parseErr != nil {
				return fmt.Errorf("parse %s: %w", rel, parseErr)
			}
			s.files = append(s.files, &sweptFile{rel: rel, dir: path.Dir(rel), fset: fset, file: file})
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// skipScanDir keeps the sweep inside the module's own source. Hidden trees hold
// tooling and nested git worktrees, node_modules and vendor are other people's
// code, and testdata is not built.
func skipScanDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "testdata"
}

// collectDecls indexes the package-level functions and variables of every
// swept file, so a call in one file can be judged against a signature declared
// in another and a package variable assigned in one function is known to all.
func (s *sweep) collectDecls() {
	for _, f := range s.files {
		for _, decl := range f.file.Decls {
			switch typed := decl.(type) {
			case *ast.FuncDecl:
				if typed.Recv == nil {
					s.funcs[scopedKey(f.dir, typed.Name.Name)] = sweptFunc{file: f, decl: typed}
				}
			case *ast.GenDecl:
				if typed.Tok != token.VAR {
					continue
				}
				for _, spec := range typed.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range value.Names {
						s.pkgVars[scopedKey(f.dir, name.Name)] = true
					}
				}
			}
		}
	}
}

// run audits every file. Package-level carriers can be discovered late — a
// function near the end of a package may be the one that stores a listener
// handler into a package variable — so the whole audit repeats until no new
// package-level carrier appears. The loop is bounded by the number of
// package-level variables.
func (s *sweep) run() {
	for {
		s.grew = false
		s.findings = s.findings[:0]
		s.vouched = map[*ast.FuncDecl]bool{}
		for _, f := range s.files {
			s.auditFile(f)
		}
		if !s.grew {
			return
		}
	}
}

func (s *sweep) auditFile(f *sweptFile) {
	s.checkImports(f)
	for _, decl := range f.file.Decls {
		switch typed := decl.(type) {
		case *ast.GenDecl:
			// Package scope belongs to no function: no entry-point or exclusion
			// allowance can apply, and a construction here is registered on
			// somewhere the walk never looks.
			scope := s.newScope(f, "package scope", false, true)
			scope.runOn(typed)
		case *ast.FuncDecl:
			name := typed.Name.Name
			if typed.Recv != nil && len(typed.Recv.List) > 0 {
				name = recvTypeName(typed.Recv.List[0].Type) + "." + name
			}
			allowed := s.entryDecls[scopedKey(f.dir, name)] || s.excluded[scopedKey(f.rel, name)]
			scope := s.newScope(f, name, allowed, false)
			if typed.Body != nil {
				scope.runOn(typed.Body)
			}
		}
	}
}

// checkImports refuses the imports that would let a construction or a
// registration be spelled in a form the sweep does not recognize.
func (s *sweep) checkImports(f *sweptFile) {
	for _, spec := range f.file.Imports {
		raw := strings.Trim(spec.Path.Value, `"`)
		switch raw {
		case "net/http/pprof", "expvar":
			s.report(f.at(spec), "%s imports %s, which registers on http.DefaultServeMux at init; "+
				"nothing may serve that mux, so the import is refused", f.rel, raw)
		}
		if spec.Name == nil || spec.Name.Name != "." {
			continue
		}
		if raw == chiImportPath || raw == httpImportPath {
			s.report(f.at(spec), "%s dot-imports %s, so its constructors could be called unqualified "+
				"where the route inventory would not recognize them", f.rel, raw)
		}
	}
}

func (s *sweep) report(at, format string, args ...any) {
	s.findings = append(s.findings, at+": "+fmt.Sprintf(format, args...))
}

// takesHandlerOnly resolves a same-directory package-level callee whose
// parameter at index is declared http.Handler or http.HandlerFunc. A
// cross-package or method callee is deliberately not resolved: the audit
// would be guessing, and the point of the rule is to stop guessing.
func (s *sweep) takesHandlerOnly(f *sweptFile, fun ast.Expr, index int) (sweptFunc, bool) {
	ident, ok := unwrapParen(fun).(*ast.Ident)
	if !ok {
		return sweptFunc{}, false
	}
	callee, known := s.funcs[scopedKey(f.dir, ident.Name)]
	if !known {
		return sweptFunc{}, false
	}
	params := flattenParams(callee.decl.Type.Params)
	if index >= len(params) || !isHTTPHandlerType(params[index].typ, callee.file.file) {
		return sweptFunc{}, false
	}
	return callee, true
}

// vouch audits a callee that received a listener handler through an
// http.Handler parameter, with every handler-typed parameter seeded as a
// carrier. Without this the vouch would rest on the interface's method set,
// and Go lets the callee assert the value back to the router type.
func (s *sweep) vouch(callee sweptFunc) {
	if s.vouched[callee.decl] || callee.decl.Body == nil {
		return
	}
	s.vouched[callee.decl] = true
	scope := s.newScope(callee.file, callee.decl.Name.Name, false, false)
	for _, p := range flattenParams(callee.decl.Type.Params) {
		if isHTTPHandlerType(p.typ, callee.file.file) && p.name != "_" {
			scope.carriers[p.name] = true
		}
	}
	scope.runOn(callee.decl.Body)
}

// handlerConsumingCalls are the standard-library calls a listener handler may
// be passed to. Each only serves the handler it is given. The handler is the
// last argument of every one of them.
var handlerConsumingCalls = map[string]bool{
	"net/http.ListenAndServe":    true,
	"net/http.ListenAndServeTLS": true,
	"net/http.Serve":             true,
	"net/http.ServeTLS":          true,
}

// carrierReadOnlyMethods are the methods that may be called on a listener
// handler after its entry point returned it: ServeHTTP, which every handler
// has, and the inspection methods of chi and http.ServeMux. Everything else is
// refused — a registration method by name, and any other method because the
// audit cannot know what it does.
var carrierReadOnlyMethods = map[string]bool{
	methodServeHTTP: true, methodRoutes: true, "Middlewares": true, "Match": true, "Find": true, methodHandler: true,
}

// registrationMethods are the router methods that can add a route. A call to
// one of them on a value the walk did not enumerate is a registration the
// inventory cannot see.
func isRegistrationMethod(name string) bool {
	switch name {
	case methodHandle, methodHandleFunc, "Method", "MethodFunc", "Route", "Group", "Mount", "Use", methodWith,
		"NotFound", "MethodNotAllowed":
		return true
	}
	return verbMethods[name] != ""
}

// funcScope is the audit of one function body (or of one package-scope
// declaration). Carrier names are scoped to it; package-level carriers are
// consulted through the sweep.
type funcScope struct {
	s        *sweep
	f        *sweptFile
	name     string
	allowed  bool // constructor calls are allowed (entry point or exclusion)
	pkgScope bool
	// carriers are the names bound to a listener handler in this function.
	carriers map[string]bool
	// listenerValues are the names bound to a value from a listener package,
	// so `x.Handler()` on one of them is that listener's entry point.
	listenerValues map[string]bool
	// shadowed are closure parameters that hide an outer name for the extent
	// of the closure body.
	shadowed map[string]bool
	// audit is false while carriers are being collected and true for the pass
	// that reports findings.
	audit bool
	grew  bool
}

func (s *sweep) newScope(f *sweptFile, name string, allowed, pkgScope bool) *funcScope {
	return &funcScope{
		s: s, f: f, name: name, allowed: allowed, pkgScope: pkgScope,
		carriers:       map[string]bool{},
		listenerValues: map[string]bool{},
		shadowed:       map[string]bool{},
	}
}

// runOn collects carriers to a fixed point, then audits. Collection is
// flow-insensitive on purpose: a closure declared before the assignment that
// makes a name a carrier still runs after it.
func (fs *funcScope) runOn(node ast.Node) {
	fs.audit = false
	for {
		fs.grew = false
		fs.visit(node)
		if !fs.grew {
			break
		}
	}
	fs.audit = true
	fs.visit(node)
}

func (fs *funcScope) report(node ast.Node, format string, args ...any) {
	if !fs.audit {
		return
	}
	fs.s.report(fs.f.at(node), "%s: %s", fs.name, fmt.Sprintf(format, args...))
}

func (fs *funcScope) isCarrier(name string) bool {
	if name == "" || name == "_" || fs.shadowed[name] {
		return false
	}
	return fs.carriers[name] || fs.s.pkgCarriers[scopedKey(fs.f.dir, name)]
}

func (fs *funcScope) isListenerValue(name string) bool {
	if name == "" || fs.shadowed[name] {
		return false
	}
	return fs.listenerValues[name] || fs.s.pkgListenerValues[scopedKey(fs.f.dir, name)]
}

// carries reports whether an expression evaluates to a listener handler: a
// carrier name, or a call to a listener entry point. A type assertion no
// longer carries — it is refused outright, because the type it recovers is
// what a registration needs.
func (fs *funcScope) carries(expr ast.Expr) bool {
	switch typed := unwrapParen(expr).(type) {
	case *ast.Ident:
		return fs.isCarrier(typed.Name)
	case *ast.CallExpr:
		return fs.isEntrypointCall(typed)
	}
	return false
}

// mark records a name as a carrier. An assignment to a package-level variable
// of the file's package makes that variable a carrier for every function in
// the package, in every file.
func (fs *funcScope) mark(name string, tok token.Token) {
	if name == "" || name == "_" {
		return
	}
	if !fs.carriers[name] {
		fs.carriers[name] = true
		fs.grew = true
	}
	if (tok == token.ASSIGN || fs.pkgScope) && fs.s.pkgVars[scopedKey(fs.f.dir, name)] {
		key := scopedKey(fs.f.dir, name)
		if !fs.s.pkgCarriers[key] {
			fs.s.pkgCarriers[key] = true
			fs.s.grew = true
		}
	}
}

func (fs *funcScope) markListenerValue(name string, tok token.Token) {
	if name == "" || name == "_" {
		return
	}
	if !fs.listenerValues[name] {
		fs.listenerValues[name] = true
		fs.grew = true
	}
	if (tok == token.ASSIGN || fs.pkgScope) && fs.s.pkgVars[scopedKey(fs.f.dir, name)] {
		key := scopedKey(fs.f.dir, name)
		if !fs.s.pkgListenerValues[key] {
			fs.s.pkgListenerValues[key] = true
			fs.s.grew = true
		}
	}
}

// isEntrypointCall recognizes a call to a declared listener entry function:
// unqualified in the listener's own package, package-qualified from another,
// or a method on a value that came from the listener package.
func (fs *funcScope) isEntrypointCall(call *ast.CallExpr) bool {
	fun := unwrapParen(call.Fun)
	if ident, ok := fun.(*ast.Ident); ok {
		for _, listener := range fs.s.cfg.Listeners {
			if listener.Recv == "" && listener.Func == ident.Name && listener.Dir == fs.f.dir {
				return true
			}
		}
		return false
	}
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	for _, listener := range fs.s.cfg.Listeners {
		if sel.Sel.Name != listener.Func {
			continue
		}
		if ident, ok := unwrapParen(sel.X).(*ast.Ident); ok {
			if listener.Recv == "" && importPathFor(fs.f.file, ident.Name) == path.Join(fs.s.cfg.ModulePath, listener.Dir) {
				return true
			}
			if listener.Recv != "" && fs.isListenerValue(ident.Name) {
				return true
			}
		}
		if listener.Recv != "" && fs.callsListenerPackage(sel.X) {
			return true
		}
	}
	return false
}

// callsListenerPackage reports whether an expression calls into one of the
// listener packages, e.g. `proxy.NewServer(...)`.
func (fs *funcScope) callsListenerPackage(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(node ast.Node) bool {
		if found {
			return false
		}
		sel, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, isListener := fs.s.listenerImports[importPathFor(fs.f.file, ident.Name)]; isListener {
			found = true
			return false
		}
		return true
	})
	return found
}

// visit is the one traversal both passes use. Every node kind that can bind,
// move, construct or register on a router value has its own case; everything
// else is descended into generically, so a carrier reaching any construct
// without a case is reported by the Ident case rather than passed over.
func (fs *funcScope) visit(node ast.Node) {
	if node == nil {
		return
	}
	switch typed := node.(type) {
	case *ast.GenDecl:
		for _, spec := range typed.Specs {
			fs.visit(spec)
		}
	case *ast.ImportSpec:
		// Handled per file by checkImports.
	case *ast.TypeSpec:
		fs.checkTypeSpec(typed)
	case *ast.ValueSpec:
		fs.valueSpec(typed)
	case *ast.DeclStmt:
		fs.visit(typed.Decl)
	case *ast.AssignStmt:
		fs.assign(typed)
	case *ast.ReturnStmt:
		for _, result := range typed.Results {
			if fs.carries(result) {
				fs.report(result, "returns a listener handler; the route inventory tracks a handler only "+
					"through local and package-level variables, so a route registered on the returned value "+
					"would exist with no inventory row")
				continue
			}
			fs.visit(result)
		}
	case *ast.CallExpr:
		fs.call(typed)
	case *ast.TypeAssertExpr:
		if fs.carries(typed.X) {
			fs.report(typed, "type-asserts a listener handler after its entry point returned it; "+
				"the recovered type is what a registration needs, so the assertion is refused. "+
				"Register inside the listener entry point")
			return
		}
		fs.visit(typed.X)
	case *ast.CompositeLit:
		fs.composite(typed)
	case *ast.KeyValueExpr:
		// A struct literal's key is a field name, not a value reference.
		if _, isIdent := typed.Key.(*ast.Ident); !isIdent {
			fs.visit(typed.Key)
		}
		fs.visit(typed.Value)
	case *ast.SelectorExpr:
		fs.selector(typed)
	case *ast.Ident:
		if fs.isCarrier(typed.Name) {
			fs.report(typed, "listener handler %q escapes into a construct the route inventory does not model; "+
				"a route registered through it would exist with no inventory row", typed.Name)
		}
	case *ast.FuncLit:
		saved := fs.shadowed
		fs.shadowed = cloneShadow(saved)
		for _, p := range append(flattenParams(typed.Type.Params), flattenParams(typed.Type.Results)...) {
			fs.shadowed[p.name] = true
		}
		fs.visit(typed.Body)
		fs.shadowed = saved
	default:
		ast.Inspect(node, func(child ast.Node) bool {
			if child == nil {
				return false
			}
			if child == node {
				return true
			}
			fs.visit(child)
			return false
		})
	}
}

// checkTypeSpec refuses a declared type that would give a router a second
// name: an alias or defined type of a router type, a struct field, an
// interface embedding, or a composite element of one. A method signature
// inside an interface is allowed: it names a router only as a parameter, the
// same way a function does, and functions are governed by the audit of the
// listener packages.
func (fs *funcScope) checkTypeSpec(spec *ast.TypeSpec) {
	if mention := routerTypeMention(spec.Type, fs.f.file); mention != "" {
		fs.report(spec, "type %s is declared with %s; a second name for a router type would let a router "+
			"escape the route inventory's recognition", spec.Name.Name, mention)
	}
	if mention := serverValueMention(spec.Type, fs.f.file); mention != "" {
		fs.report(spec, "type %s is declared with %s; its zero value serves http.DefaultServeMux",
			spec.Name.Name, mention)
	}
}

// valueSpec handles `var` declarations in a function and at package scope.
func (fs *funcScope) valueSpec(spec *ast.ValueSpec) {
	if spec.Type != nil {
		if mention := routerTypeMention(spec.Type, fs.f.file); mention != "" {
			where := "in " + fs.name
			if fs.pkgScope {
				where = "at package scope"
			}
			fs.report(spec, "a variable is declared with %s %s, outside the modeled walk; "+
				"a router bound this way cannot be proved to attach anywhere", mention, where)
			return
		}
		if mention := serverValueMention(spec.Type, fs.f.file); mention != "" {
			fs.report(spec, "a variable is declared as %s; its zero value serves http.DefaultServeMux", mention)
			return
		}
	}
	switch {
	case len(spec.Values) == 0:
	case len(spec.Values) == len(spec.Names):
		for i, name := range spec.Names {
			fs.bind(name, name.Name, spec.Values[i], token.ASSIGN)
		}
	default:
		// `var a, b = f()`: one initializer, several names.
		fs.multiBind(spec.Values[0])
	}
}

func (fs *funcScope) assign(stmt *ast.AssignStmt) {
	if len(stmt.Lhs) > 1 && len(stmt.Rhs) == 1 {
		fs.multiBind(stmt.Rhs[0])
		for _, lhs := range stmt.Lhs {
			fs.target(lhs, nil)
		}
		return
	}
	for i, binding := range bindingsOfAssign(stmt) {
		lhs := stmt.Lhs[i]
		if ident, ok := unwrapParen(lhs).(*ast.Ident); ok {
			fs.bind(lhs, ident.Name, binding.value, stmt.Tok)
			continue
		}
		if fs.carries(binding.value) {
			fs.report(stmt, "stores a listener handler into %s, which the route inventory does not follow; "+
				"a route registered through it would exist with no inventory row", renderTarget(lhs))
			continue
		}
		fs.visit(binding.value)
		fs.target(lhs, binding.value)
	}
}

// multiBind is `a, b := f()` and `var a, b = f()`: one value feeding several
// names. A listener handler cannot be tied to one of them, so it is refused.
func (fs *funcScope) multiBind(value ast.Expr) {
	if fs.carries(value) {
		fs.report(value, "binds a listener handler through a multi-value assignment the route inventory "+
			"does not model; bind it to a single name")
		return
	}
	fs.visit(value)
}

// bind records what one name is bound to.
func (fs *funcScope) bind(at ast.Node, name string, value ast.Expr, tok token.Token) {
	if value == nil {
		return
	}
	if fs.carries(value) {
		fs.mark(name, tok)
		if call, ok := unwrapParen(value).(*ast.CallExpr); ok {
			fs.entrypointArgs(call)
		}
		return
	}
	if call, ok := unwrapParen(value).(*ast.CallExpr); ok && fs.callsListenerPackage(call.Fun) {
		fs.markListenerValue(name, tok)
	}
	fs.visit(value)
}

// target inspects a non-identifier assignment target. Setting an http.Server's
// Handler to nil makes it serve http.DefaultServeMux.
func (fs *funcScope) target(lhs ast.Expr, value ast.Expr) {
	if sel, ok := unwrapParen(lhs).(*ast.SelectorExpr); ok && sel.Sel.Name == methodHandler && isNilIdent(value) {
		fs.report(lhs, "sets a Handler field to nil; an http.Server with a nil handler serves http.DefaultServeMux")
		return
	}
	fs.visit(lhs)
}

// entrypointArgs inspects the arguments of a listener entry-point call. A
// carrier may be passed straight in — that is how the root listener receives
// the API router — and everything else is inspected as usual.
func (fs *funcScope) entrypointArgs(call *ast.CallExpr) {
	for _, arg := range call.Args {
		if fs.carries(arg) {
			continue
		}
		fs.visit(arg)
	}
	if sel, ok := unwrapParen(call.Fun).(*ast.SelectorExpr); ok {
		fs.visit(sel.X)
	}
}

// call is where a listener handler may legitimately go, and where a router may
// be constructed or registered on. It vouches for exactly these shapes: a
// listener entry point, a standard-library serving call, and a same-directory
// function whose parameter is declared http.Handler — whose body is then
// audited with that parameter as a carrier.
func (fs *funcScope) call(call *ast.CallExpr) {
	fun := unwrapParen(call.Fun)

	// A method call on a listener handler.
	if sel, ok := fun.(*ast.SelectorExpr); ok && fs.carries(sel.X) {
		name := sel.Sel.Name
		switch {
		case isRegistrationMethod(name):
			fs.report(call, "calls %s on a listener handler after its entry point returned it; "+
				"the route would exist with no inventory row. Register it inside the listener entry point", name)
		case carrierReadOnlyMethods[name]:
		default:
			fs.report(call, "calls %s on a listener handler after its entry point returned it; "+
				"the route inventory allows only ServeHTTP and the read-only router methods on one", name)
		}
		if inner, ok := unwrapParen(sel.X).(*ast.CallExpr); ok {
			fs.entrypointArgs(inner)
		}
		for _, arg := range call.Args {
			fs.visit(arg)
		}
		return
	}

	// Constructions.
	if kind := constructorKind(call, fs.f.file); kind != "" && !fs.allowed {
		if fs.pkgScope {
			fs.report(call, "%s constructed at package scope, which belongs to no listener entry point; "+
				"build it inside a declared listener entry function", kind)
		} else {
			fs.report(call, "%s constructed in %s outside the inventoried listeners; "+
				"add it as a listener or record it as an explicit exclusion for that function", kind, fs.name)
		}
	}
	if kind := literalCtorKind(call, fs.f.file); kind != ctorNone {
		fs.report(call, "%s is built with new() rather than its constructor; the route inventory recognizes "+
			"only chi.NewRouter(), chi.NewMux() and http.NewServeMux()", kind)
	}

	qualified := qualifiedCallee(fun, fs.f.file)
	switch qualified {
	case "net/http.Handle", "net/http.HandleFunc":
		fs.report(call, "%s registers on http.DefaultServeMux, which no listener enumerates", fs.name)
	case "net/http.Server", "net/http.ServeMux":
		// Conversions do not construct; nothing to do beyond the arguments.
	}

	switch {
	case handlerConsumingCalls[qualified]:
		if len(call.Args) == 0 {
			break
		}
		last := call.Args[len(call.Args)-1]
		if isNilIdent(last) {
			fs.report(call, "serves a nil handler, which is http.DefaultServeMux; no listener enumerates it")
		}
		for _, arg := range call.Args {
			if fs.carries(arg) {
				continue
			}
			fs.visit(arg)
		}
		return
	case fs.isEntrypointCall(call):
		// Reached only when the call is not the value of a binding, an
		// argument of a serving call, or the Handler of an http.Server: those
		// paths handle it without coming here.
		fs.report(call, "calls a listener entry point in a position the route inventory does not follow; "+
			"bind its result to a name, or pass it straight to a serving call")
		fs.entrypointArgs(call)
		return
	}

	for index, arg := range call.Args {
		if !fs.carries(arg) {
			fs.visit(arg)
			continue
		}
		if callee, ok := fs.s.takesHandlerOnly(fs.f, fun, index); ok {
			if fs.audit {
				fs.s.vouch(callee)
			}
			continue
		}
		fs.report(call, "passes a listener handler to %s, which the route inventory cannot see into; "+
			"any route registered through it would exist with no inventory row. "+
			"Register it inside the listener entry point, or take the value as an http.Handler "+
			"in a package-level function of the same directory", renderCallee(fun))
	}
	if sel, ok := fun.(*ast.SelectorExpr); ok {
		// The selector itself names the callee; only its base can carry a
		// value or hide a construction.
		fs.visit(sel.X)
		return
	}
	if _, isIdent := fun.(*ast.Ident); isIdent {
		// A carrier called as a function would be a HandlerFunc invoked
		// directly; it is refused like any other unmodeled use.
		fs.visit(fun)
		return
	}
	fs.visit(fun)
}

// composite refuses a router built by literal and an http.Server that would
// serve http.DefaultServeMux, and allows a listener handler exactly one
// literal position: the Handler field of an http.Server.
func (fs *funcScope) composite(lit *ast.CompositeLit) {
	if lit.Type != nil {
		if isServeMuxType(lit.Type, fs.f.file) {
			fs.report(lit, "an http.ServeMux is built by composite literal; the route inventory recognizes "+
				"only http.NewServeMux()")
			return
		}
		if isChiRouterType(lit.Type, fs.f.file) {
			fs.report(lit, "a chi router is built by composite literal; the route inventory recognizes "+
				"only chi.NewRouter() and chi.NewMux()")
			return
		}
		if isHTTPServerType(lit.Type, fs.f.file) {
			fs.serverLiteral(lit)
			return
		}
	}
	for _, elt := range lit.Elts {
		fs.visit(elt)
	}
}

func (fs *funcScope) serverLiteral(lit *ast.CompositeLit) {
	hasHandler := false
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			fs.report(lit, "http.Server is built with a positional literal; the route inventory requires "+
				"a keyed literal with a non-nil Handler")
			return
		}
		key, _ := kv.Key.(*ast.Ident)
		if key == nil || key.Name != methodHandler {
			fs.visit(kv.Value)
			continue
		}
		hasHandler = true
		if isNilIdent(kv.Value) {
			fs.report(kv, "http.Server is built with Handler: nil, which serves http.DefaultServeMux")
			continue
		}
		if fs.carries(kv.Value) {
			continue
		}
		fs.visit(kv.Value)
	}
	if !hasHandler {
		fs.report(lit, "http.Server is built without a Handler, so it serves http.DefaultServeMux; "+
			"no listener enumerates that mux")
	}
}

// selector handles a selector that is not the callee of a call: a method value
// or field of a listener handler, a reference to http.DefaultServeMux, or a
// constructor used as a function value.
func (fs *funcScope) selector(sel *ast.SelectorExpr) {
	if fs.carries(sel.X) {
		fs.report(sel, "takes %s of a listener handler as a value; the route inventory follows only "+
			"direct method calls on one", sel.Sel.Name)
		return
	}
	switch qualifiedCallee(sel, fs.f.file) {
	case "net/http.DefaultServeMux":
		fs.report(sel, "refers to http.DefaultServeMux, which no listener enumerates")
		return
	}
	if kind := ctorFunctionValue(sel, fs.f.file); kind != "" {
		fs.report(sel, "%s is used as a function value rather than called; a router built through it "+
			"would not be recognized as a construction", kind)
		return
	}
	fs.visit(sel.X)
}

// ctorFunctionValue names the constructor a selector refers to when it is not
// in call position.
func ctorFunctionValue(sel *ast.SelectorExpr, file *ast.File) string {
	ident, ok := unwrapParen(sel.X).(*ast.Ident)
	if !ok {
		return ""
	}
	switch importPathFor(file, ident.Name) {
	case chiImportPath:
		if isChiConstructor(sel.Sel.Name) {
			return "chi." + sel.Sel.Name
		}
	case httpImportPath:
		if sel.Sel.Name == serveMuxNew {
			return "http." + serveMuxNew
		}
	}
	return ""
}

// constructorKind names the router a call constructs, or "" when the call
// builds no router. It resolves the package identifier through the file's
// imports, so a comment or a same-named method cannot trigger it.
func constructorKind(call *ast.CallExpr, file *ast.File) string {
	sel, ok := unwrapParen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	switch callCtorKind(call, file) {
	case ctorChiRouter:
		return "chi." + sel.Sel.Name + "()"
	case ctorServeMux:
		return "http.NewServeMux()"
	}
	return ""
}

func isHTTPHandlerType(expr ast.Expr, file *ast.File) bool {
	sel, ok := unwrapParen(expr).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := unwrapParen(sel.X).(*ast.Ident)
	if !ok || importPathFor(file, ident.Name) != httpImportPath {
		return false
	}
	return sel.Sel.Name == methodHandler || sel.Sel.Name == "HandlerFunc"
}

// isHTTPServerType reports whether a type expression names http.Server, with
// or without a pointer.
func isHTTPServerType(expr ast.Expr, file *ast.File) bool {
	switch typed := unwrapParen(expr).(type) {
	case *ast.StarExpr:
		return isHTTPServerType(typed.X, file)
	case *ast.SelectorExpr:
		ident, ok := unwrapParen(typed.X).(*ast.Ident)
		return ok && typed.Sel.Name == "Server" && importPathFor(file, ident.Name) == httpImportPath
	}
	return false
}

// routerTypeMention names the router type a type expression spells anywhere
// in its structure, or "". Interface method signatures are not descended
// into; see checkTypeSpec.
func routerTypeMention(expr ast.Expr, file *ast.File) string {
	switch typed := unwrapParen(expr).(type) {
	case *ast.StarExpr:
		return routerTypeMention(typed.X, file)
	case *ast.ArrayType:
		return routerTypeMention(typed.Elt, file)
	case *ast.Ellipsis:
		return routerTypeMention(typed.Elt, file)
	case *ast.MapType:
		if m := routerTypeMention(typed.Key, file); m != "" {
			return m
		}
		return routerTypeMention(typed.Value, file)
	case *ast.ChanType:
		return routerTypeMention(typed.Value, file)
	case *ast.FuncType:
		for _, p := range append(flattenParams(typed.Params), flattenParams(typed.Results)...) {
			if m := routerTypeMention(p.typ, file); m != "" {
				return m
			}
		}
	case *ast.StructType:
		for _, field := range typed.Fields.List {
			if m := routerTypeMention(field.Type, file); m != "" {
				return m
			}
		}
	case *ast.InterfaceType:
		for _, field := range typed.Methods.List {
			if len(field.Names) > 0 {
				continue
			}
			if m := routerTypeMention(field.Type, file); m != "" {
				return m
			}
		}
	case *ast.SelectorExpr:
		switch {
		case isChiRouterType(typed, file):
			return "a chi router type"
		case isServeMuxType(typed, file):
			return "http.ServeMux"
		}
	}
	return ""
}

// serverValueMention names a non-pointer http.Server in a type expression: a
// value whose zero state has a nil Handler and therefore serves
// http.DefaultServeMux.
func serverValueMention(expr ast.Expr, file *ast.File) string {
	switch typed := unwrapParen(expr).(type) {
	case *ast.ArrayType:
		return serverValueMention(typed.Elt, file)
	case *ast.StructType:
		for _, field := range typed.Fields.List {
			if m := serverValueMention(field.Type, file); m != "" {
				return m
			}
		}
	case *ast.SelectorExpr:
		if isHTTPServerType(typed, file) {
			return "an http.Server value"
		}
	}
	return ""
}

func isNilIdent(expr ast.Expr) bool {
	ident, ok := unwrapParen(expr).(*ast.Ident)
	return ok && ident.Name == "nil"
}

// renderCallee names a call target for a finding without needing the analyzer's
// shared printer, which is scoped to the parsed listener packages rather than
// to the swept trees.
func renderCallee(fun ast.Expr) string {
	switch typed := unwrapParen(fun).(type) {
	case *ast.Ident:
		return typed.Name + "()"
	case *ast.SelectorExpr:
		if ident, ok := unwrapParen(typed.X).(*ast.Ident); ok {
			return ident.Name + "." + typed.Sel.Name + "()"
		}
		return typed.Sel.Name + "()"
	}
	return "an unnamed call"
}

func renderTarget(expr ast.Expr) string {
	switch typed := unwrapParen(expr).(type) {
	case *ast.SelectorExpr:
		return "field " + typed.Sel.Name
	case *ast.IndexExpr:
		return "an index expression"
	case *ast.StarExpr:
		return "a dereference"
	}
	return "a non-identifier target"
}

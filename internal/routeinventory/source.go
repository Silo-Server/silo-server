package routeinventory

import (
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// chiImportPath is the router library the native listeners use.
const chiImportPath = "github.com/go-chi/chi/v5"

// httpImportPath is the standard-library package the process root listener's
// router comes from.
const httpImportPath = "net/http"

// pkgSource is one type-checked package of the module. Only non-test files are
// loaded: tests may build throwaway routers, and a test route is not part of
// the shipped surface.
type pkgSource struct {
	Pkg       *packages.Package
	Dir       string // repo-relative
	Files     []*ast.File
	FileNames map[*ast.File]string // repo-relative

	funcs   map[string]*ast.FuncDecl // package-level funcs by name
	methods map[string]*ast.FuncDecl // methods by "RecvType.Name"
}

func (p *pkgSource) info() *types.Info { return p.Pkg.TypesInfo }

// sourceSet is every package under the repository root, type-checked in one
// go/packages load, plus the subset the walk and the classifier look inside.
type sourceSet struct {
	fset       *token.FileSet
	root       string
	modulePath string
	// all is every package in the module; the sweep covers all of them.
	all []*pkgSource
	// packages and byImport are the analyzed packages: the listener packages
	// and the audit directories. The walk follows helpers and the classifier
	// reads handler bodies only inside them.
	packages map[string]*pkgSource // keyed by repo-relative dir
	byImport map[string]*pkgSource

	funcDecls map[*types.Func]*ast.FuncDecl
	declPkg   map[*ast.FuncDecl]*pkgSource
	// callees are the identifiers in call position, so a constructor
	// referenced as a function value can be told from one that is called.
	callees map[*ast.Ident]bool

	// registration holds, by method name, the signatures a type must carry
	// one of to count as a router: chi.Router's registration methods and
	// http.ServeMux's Handle/HandleFunc, read off the loaded packages.
	registration map[string][]*types.Signature
	// kinds memoizes routerKind; the leak check asks about every expression.
	kinds map[types.Type]ctorKind
}

// loadSources type-checks the whole module under root and indexes it. It fails
// on any package error: the inventory is read off type information, so a tree
// that does not compile is a tree it cannot see into.
func loadSources(root string, analyzed []string) (*sourceSet, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	// The router packages are loaded by name beside the module so the
	// registration signatures (routerKind's method-set check) come from the
	// packages themselves, whether or not anything in the module imports them.
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.LoadSyntax | packages.NeedModule,
		Dir:  root,
		Fset: fset,
	}, "./...", chiImportPath, httpImportPath)
	if err != nil {
		return nil, fmt.Errorf("load packages under %s: %w", root, err)
	}
	set := &sourceSet{
		fset:         fset,
		root:         root,
		packages:     map[string]*pkgSource{},
		byImport:     map[string]*pkgSource{},
		funcDecls:    map[*types.Func]*ast.FuncDecl{},
		declPkg:      map[*ast.FuncDecl]*pkgSource{},
		callees:      map[*ast.Ident]bool{},
		registration: map[string][]*types.Signature{},
		kinds:        map[types.Type]ctorKind{},
	}
	var problems []string
	var routerPkgs []*types.Package
	for _, pkg := range pkgs {
		if pkg.PkgPath == chiImportPath || pkg.PkgPath == httpImportPath {
			// A module that does not depend on chi has nothing chi-shaped to
			// recognize, and net/http is always present; neither is swept.
			if pkg.Types != nil {
				routerPkgs = append(routerPkgs, pkg.Types)
			}
			continue
		}
		if len(pkg.GoFiles) == 0 && len(pkg.Syntax) == 0 {
			// A directory whose files are all excluded by build constraints
			// (the ruleguard rules in lintrules/) lists as a package with an
			// error and no source. There is nothing in it to sweep.
			continue
		}
		for _, problem := range pkg.Errors {
			problems = append(problems, problem.Error())
		}
		if len(pkg.Syntax) == 0 {
			continue
		}
		if set.modulePath == "" && pkg.Module != nil {
			set.modulePath = pkg.Module.Path
		}
		set.all = append(set.all, set.index(pkg))
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		if len(problems) > 5 {
			problems = append(problems[:5], fmt.Sprintf("... and %d more", len(problems)-5))
		}
		return nil, fmt.Errorf("the source tree under %s does not type-check, and the route inventory needs a "+
			"compiling tree: %s", root, strings.Join(problems, "; "))
	}
	if len(set.all) == 0 {
		return nil, fmt.Errorf("no Go packages under %s", root)
	}
	sort.Slice(set.all, func(i, j int) bool { return set.all[i].Dir < set.all[j].Dir })
	set.indexRegistrationMethods(routerPkgs)

	byDir := map[string]*pkgSource{}
	for _, src := range set.all {
		byDir[src.Dir] = src
	}
	for _, dir := range analyzed {
		src := byDir[dir]
		if src == nil {
			return nil, fmt.Errorf("package %s has no non-test Go files", dir)
		}
		set.packages[dir] = src
		set.byImport[src.Pkg.PkgPath] = src
	}
	return set, nil
}

func (s *sourceSet) index(pkg *packages.Package) *pkgSource {
	src := &pkgSource{
		Pkg:       pkg,
		FileNames: map[*ast.File]string{},
		funcs:     map[string]*ast.FuncDecl{},
		methods:   map[string]*ast.FuncDecl{},
	}
	for _, file := range pkg.Syntax {
		rel := s.relPath(s.fset.File(file.Pos()).Name())
		if src.Dir == "" {
			src.Dir = filepath.ToSlash(filepath.Dir(rel))
		}
		src.Files = append(src.Files, file)
		src.FileNames[file] = rel
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			s.declPkg[fn] = src
			if obj, ok := pkg.TypesInfo.Defs[fn.Name].(*types.Func); ok {
				s.funcDecls[obj] = fn
			}
			if fn.Recv == nil || len(fn.Recv.List) == 0 {
				src.funcs[fn.Name.Name] = fn
				continue
			}
			src.methods[recvTypeName(fn.Recv.List[0].Type)+"."+fn.Name.Name] = fn
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if ident := calleeIdent(call); ident != nil {
					s.callees[ident] = true
				}
			}
			return true
		})
	}
	return src
}

func (s *sourceSet) relPath(filename string) string {
	rel, err := filepath.Rel(s.root, filename)
	if err != nil {
		return filepath.ToSlash(filename)
	}
	return filepath.ToSlash(rel)
}

func recvTypeName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.StarExpr:
		return recvTypeName(typed.X)
	case *ast.IndexExpr:
		return recvTypeName(typed.X)
	case *ast.IndexListExpr:
		return recvTypeName(typed.X)
	case *ast.Ident:
		return typed.Name
	}
	return ""
}

// exprText renders an expression as normalized single-line source. Two
// registrations that read the same in the source produce the same text
// regardless of how the author wrapped the call across lines.
func (s *sourceSet) exprText(expr ast.Expr) string {
	var sb strings.Builder
	if err := printer.Fprint(&sb, s.fset, expr); err != nil {
		return "<unprintable>"
	}
	return normalizeSpace(sb.String())
}

func normalizeSpace(in string) string {
	return strings.Join(strings.Fields(in), " ")
}

func (s *sourceSet) position(node ast.Node) token.Position {
	return s.fset.Position(node.Pos())
}

// sourceFile is the repo-relative file a node lives in.
func (s *sourceSet) sourceFile(node ast.Node) string {
	return s.relPath(s.position(node).Filename)
}

// typeIdentity renders a type with fully qualified package paths, e.g.
// `*github.com/x/y/internal/api/handlers.AuthHandler`.
func typeIdentity(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return p.Path() })
}

// typeLabel renders a type the way source spells it (`http.ServeMux`).
func typeLabel(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return p.Name() })
}

// ---------------------------------------------------------------------------
// Router types
// ---------------------------------------------------------------------------

// ctorKind is what kind of router a type denotes or an expression constructs.
type ctorKind int

const (
	ctorNone ctorKind = iota
	// ctorChiRouter is chi.Router, chi.Routes or chi.Mux; as a constructor,
	// chi.NewRouter() or chi.NewMux().
	ctorChiRouter
	// ctorServeMux is http.ServeMux; as a constructor, http.NewServeMux().
	ctorServeMux
)

func (k ctorKind) String() string {
	switch k {
	case ctorChiRouter:
		return "a chi router"
	case ctorServeMux:
		return "an http.ServeMux"
	}
	return "nothing"
}

// routerTypeKind classifies a type as a router type, looking through aliases
// and one pointer. A value of any of them can have routes registered on it.
func routerTypeKind(t types.Type) ctorKind {
	if t == nil {
		return ctorNone
	}
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return ctorNone
	}
	switch named.Obj().Pkg().Path() {
	case chiImportPath:
		switch named.Obj().Name() {
		case "Router", methodRoutes, "Mux":
			return ctorChiRouter
		}
	case httpImportPath:
		if named.Obj().Name() == "ServeMux" {
			return ctorServeMux
		}
	}
	return ctorNone
}

// registrationMethods are the router methods that add to or reshape the served
// surface. A type that carries one of them with the signature chi.Router or
// http.ServeMux gives it is a registration surface whatever it is called: an
// interface embedding chi.Router, a structural interface spelling one method,
// or a wrapper struct. Read-only methods (Routes, Match, ...) are not here: a
// value that can only be walked registers nothing.
var registrationMethods = map[string]bool{
	methodHandle: true, methodHandleFunc: true, "Method": true, "MethodFunc": true,
	"Connect": true, "Delete": true, "Get": true, "Head": true, "Options": true,
	"Patch": true, "Post": true, "Put": true, "Trace": true,
	"Route": true, "Group": true, "Mount": true, "Use": true, methodWith: true,
	"NotFound": true, "MethodNotAllowed": true,
}

// indexRegistrationMethods reads the registration signatures off the chi and
// net/http packages themselves, so the method-set check compares against what
// those packages declare rather than a spelling kept here.
func (s *sourceSet) indexRegistrationMethods(routerPkgs []*types.Package) {
	for _, pkg := range routerPkgs {
		switch pkg.Path() {
		case chiImportPath:
			if obj, ok := pkg.Scope().Lookup("Router").(*types.TypeName); ok {
				s.addRegistrationMethods(obj.Type())
			}
		case httpImportPath:
			if obj, ok := pkg.Scope().Lookup("ServeMux").(*types.TypeName); ok {
				s.addRegistrationMethods(types.NewPointer(obj.Type()))
			}
		}
	}
}

func (s *sourceSet) addRegistrationMethods(t types.Type) {
	set := types.NewMethodSet(t)
	for i := 0; i < set.Len(); i++ {
		fn, ok := set.At(i).Obj().(*types.Func)
		if !ok || !registrationMethods[fn.Name()] {
			continue
		}
		s.registration[fn.Name()] = append(s.registration[fn.Name()], fn.Signature())
	}
}

// routerKind classifies a type as a router, or ctorNone. It is routerTypeKind
// — chi.Router, chi.Routes, chi.Mux, http.ServeMux, through aliases and one
// pointer — widened by a method-set check: any type whose method set carries a
// registration method with the signature chi.Router or http.ServeMux gives it
// can have routes registered on it, whatever it is named. That is what makes
// an alias, a defined interface type, an interface embedding chi.Router and a
// structural interface all the same thing to the walk, the sweep and the
// sealing checks.
func (s *sourceSet) routerKind(t types.Type) ctorKind {
	if t == nil {
		return ctorNone
	}
	if kind, ok := s.kinds[t]; ok {
		return kind
	}
	kind := routerTypeKind(t)
	if kind == ctorNone {
		kind = s.registrationKind(t)
	}
	s.kinds[t] = kind
	return kind
}

// registrationKind is the method-set half of routerKind: chi-shaped when t
// carries any chi registration method, ServeMux-shaped when it carries only
// Handle/HandleFunc. A type parameter has no method set of its own to judge;
// the sweep refuses the instantiation instead.
func (s *sourceSet) registrationKind(t types.Type) ctorKind {
	if _, isParam := types.Unalias(t).(*types.TypeParam); isParam {
		return ctorNone
	}
	kind := ctorNone
	for name, signatures := range s.registration {
		obj, _, _ := types.LookupFieldOrMethod(t, true, nil, name)
		fn, ok := obj.(*types.Func)
		if !ok {
			continue
		}
		for _, want := range signatures {
			if !types.Identical(fn.Signature(), want) {
				continue
			}
			if name != methodHandle && name != methodHandleFunc {
				return ctorChiRouter
			}
			kind = ctorServeMux
		}
	}
	return kind
}

// tupleRouterKind is routerTypeKind over a possibly multi-valued expression
// type: a call returning `(*chi.Mux, error)` produces a router too.
func (s *sourceSet) tupleRouterKind(t types.Type) ctorKind {
	tuple, ok := t.(*types.Tuple)
	if !ok {
		return s.routerKind(t)
	}
	for i := 0; i < tuple.Len(); i++ {
		if kind := s.routerKind(tuple.At(i).Type()); kind != ctorNone {
			return kind
		}
	}
	return ctorNone
}

// producesRouter reports whether an expression of type t brings a router into
// existence: the expression is itself a router value, or its value holds a
// concrete mux by value somewhere inside (a struct field, an array element,
// the elements of a slice, map or channel). A zero http.ServeMux is fully
// functional, so a container of them is a container of routers. A pointer or
// interface inside a container is nil until something else constructs the
// router, and that something is refused on its own.
func (s *sourceSet) producesRouter(t types.Type) bool {
	if s.tupleRouterKind(t) != ctorNone {
		return true
	}
	if tuple, ok := t.(*types.Tuple); ok {
		for i := 0; i < tuple.Len(); i++ {
			if s.holdsRouterValue(tuple.At(i).Type(), map[types.Type]bool{}) {
				return true
			}
		}
		return false
	}
	return s.holdsRouterValue(t, map[types.Type]bool{})
}

func (s *sourceSet) holdsRouterValue(t types.Type, seen map[types.Type]bool) bool {
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	if seen[t] {
		return false
	}
	seen[t] = true
	switch typed := t.(type) {
	case *types.Named:
		if _, isInterface := typed.Underlying().(*types.Interface); !isInterface && routerTypeKind(typed) != ctorNone {
			return true
		}
		return s.holdsRouterValue(typed.Underlying(), seen)
	case *types.Struct:
		for i := 0; i < typed.NumFields(); i++ {
			if s.holdsRouterValue(typed.Field(i).Type(), seen) {
				return true
			}
		}
	case *types.Array:
		return s.holdsRouterValue(typed.Elem(), seen)
	case *types.Slice:
		return s.holdsRouterValue(typed.Elem(), seen)
	case *types.Map:
		return s.holdsRouterValue(typed.Elem(), seen)
	case *types.Chan:
		return s.holdsRouterValue(typed.Elem(), seen)
	}
	return false
}

// The recognized constructor names. chi.NewMux returns the same *chi.Mux as
// chi.NewRouter, so anything that recognizes one has to recognize the other.
// They are spelled here, separately from the Silo entry-function names in
// config.go, so a coincidence of spelling cannot make one constant mean two
// things.
const (
	chiNewRouter = "NewRouter"
	chiNewMux    = "NewMux"
	serveMuxNew  = "NewServeMux"
)

// isRouterCtor reports whether fn is one of the three recognized constructors.
func isRouterCtor(fn *types.Func) ctorKind {
	if fn == nil || fn.Pkg() == nil {
		return ctorNone
	}
	switch fn.Pkg().Path() {
	case chiImportPath:
		if fn.Name() == chiNewRouter || fn.Name() == chiNewMux {
			return ctorChiRouter
		}
	case httpImportPath:
		if fn.Name() == serveMuxNew {
			return ctorServeMux
		}
	}
	return ctorNone
}

// isHTTPHandlerType reports whether t is exactly net/http.Handler.
func isHTTPHandlerType(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == httpImportPath && named.Obj().Name() == methodHandler
}

// calleeIdent is the identifier naming a call's function, or nil for a call
// through an arbitrary expression.
func calleeIdent(call *ast.CallExpr) *ast.Ident {
	switch fun := unwrapParen(call.Fun).(type) {
	case *ast.Ident:
		return fun
	case *ast.SelectorExpr:
		return fun.Sel
	case *ast.IndexExpr:
		return calleeIdent(&ast.CallExpr{Fun: fun.X})
	case *ast.IndexListExpr:
		return calleeIdent(&ast.CallExpr{Fun: fun.X})
	}
	return nil
}

// calleeFunc resolves the function a call invokes, or nil for a conversion,
// a builtin, or a call through a function-typed value.
func calleeFunc(call *ast.CallExpr, info *types.Info) *types.Func {
	ident := calleeIdent(call)
	if ident == nil {
		return nil
	}
	fn, _ := info.Uses[ident].(*types.Func)
	return fn
}

// isBuiltinNew reports whether a call is the builtin new().
func isBuiltinNew(call *ast.CallExpr, info *types.Info) bool {
	ident, ok := unwrapParen(call.Fun).(*ast.Ident)
	if !ok {
		return false
	}
	builtin, ok := info.Uses[ident].(*types.Builtin)
	return ok && builtin.Name() == "new"
}

// qualifiedCallee renders `pkgimportpath.Func` when the callee is a plain
// package-level call, so `json.NewDecoder` cannot be confused with a method
// named NewDecoder on some other type.
func qualifiedCallee(fun ast.Expr, info *types.Info) string {
	sel, ok := unwrapParen(fun).(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	ident, ok := unwrapParen(sel.X).(*ast.Ident)
	if !ok {
		return ""
	}
	pkgName, ok := info.Uses[ident].(*types.PkgName)
	if !ok {
		return ""
	}
	return pkgName.Imported().Path() + "." + sel.Sel.Name
}

// unwrapParen strips redundant parentheses. Every place the analyzer asserts a
// *ast.CallExpr, a *ast.SelectorExpr or an *ast.Ident goes through it:
// `(chi.NewRouter())` constructs exactly what `chi.NewRouter()` does.
func unwrapParen(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}

func isNilValue(expr ast.Expr, info *types.Info) bool {
	tv, ok := info.Types[expr]
	return ok && tv.IsNil()
}

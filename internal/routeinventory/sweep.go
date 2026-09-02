package routeinventory

import (
	"errors"
	"go/ast"
	"go/types"
	"sort"
	"strings"
)

// The sweep is the module-wide half of the completeness guarantee. The walk
// enumerates what the declared listeners register and seal.go proves nothing
// can get a listener's router back; the sweep proves that no router can come
// into existence or be recovered anywhere else. It runs over the type-checked
// syntax of every non-test package in the module and refuses:
//
//   - any expression that produces a router — chi.Router, chi.Routes,
//     *chi.Mux or *http.ServeMux, or a value holding a zero-valued mux inside
//     it — outside a declared listener entry function, a helper the walk
//     followed, or a recorded exclusion. The check is on the resolved type,
//     so the constructor call, new(), a composite literal, make(), a generic
//     instantiation, a call through a function value and a variable declared
//     with the concrete type are all the same production. Two shapes are not
//     productions: a method on a router that derives another router (Route,
//     Group, With) only narrows one that already exists, and a call to a
//     non-generic function declared in this module returns a router its own
//     body produced, where it is judged;
//   - a router constructor referenced as a function value rather than called;
//   - anything that would serve http.DefaultServeMux: http.Handle and
//     http.HandleFunc, a reference to http.DefaultServeMux, a nil handler
//     passed to http.ListenAndServe and its siblings, an http.Server literal
//     without a non-nil Handler, and an import of net/http/pprof or expvar
//     (chi's middleware package links pprof in, so that mux is never empty).
//
//   - any attempt to recover a router from a value: a type assertion or type
//     switch case whose type is a router — by name or by method set, so an
//     alias, a defined type, an embedding or structural interface count — a
//     generic instantiated with such a type, and reflect.Value.MethodByName.
//     Every listener entry function returns a sealed handler (seal.go), so
//     none of these can succeed on a listener; they are refused so that a
//     router that is not a listener cannot be recovered either. The ruleguard
//     rule in lintrules/ reports the same shapes in the editor.

// handlerConsumingCalls are the net/http functions whose last argument is the
// handler to serve; nil selects http.DefaultServeMux.
var handlerConsumingCalls = map[string]bool{
	"ListenAndServe": true, "ListenAndServeTLS": true, "Serve": true, "ServeTLS": true,
}

// defaultMuxRegistrars are the net/http functions that register on
// http.DefaultServeMux.
var defaultMuxRegistrars = map[string]bool{methodHandle: true, methodHandleFunc: true}

// defaultMuxImports register on http.DefaultServeMux at init time.
var defaultMuxImports = map[string]bool{"net/http/pprof": true, "expvar": true}

type sweeper struct {
	a        *Analyzer
	pkg      *pkgSource
	excluded map[string]bool
	findings []string
}

// sweep runs the module-wide refusals. See the comment at the top of this file.
func (a *Analyzer) sweep() error {
	s := &sweeper{a: a, excluded: map[string]bool{}}
	for _, exclusion := range a.cfg.Exclusions {
		s.excluded[exclusion.key()] = true
	}
	for _, pkg := range a.set.all {
		s.pkg = pkg
		for _, file := range pkg.Files {
			s.file(file)
		}
	}
	if len(s.findings) == 0 {
		return nil
	}
	sort.Strings(s.findings)
	return errors.New(strings.Join(s.findings, "; "))
}

func (s *sweeper) report(node ast.Node, format string, args ...any) {
	s.findings = append(s.findings, s.a.errorf(node, format, args...).Error())
}

func (s *sweeper) info() *types.Info { return s.pkg.info() }

func (s *sweeper) file(file *ast.File) {
	rel := s.pkg.FileNames[file]
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		if defaultMuxImports[path] {
			s.report(spec, "%s imports %s, which registers on http.DefaultServeMux at init; "+
				"nothing may serve that mux, so the import is refused", rel, path)
		}
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			// Package scope belongs to no function: no entry-point or exclusion
			// allowance can apply, and a router built here is registered on
			// somewhere the walk never looks.
			s.visit(decl, "at package scope", false)
			continue
		}
		name := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			name = recvTypeName(fn.Recv.List[0].Type) + "." + name
		}
		allowed := s.a.enteredFuncs[fn] || s.excluded[rel+"#"+name]
		s.visit(fn, "in "+name, allowed)
	}
}

// visit applies the refusals to one declaration. Productions are allowed
// inside an entry function or a followed helper (the walk has already judged
// every statement there) and inside a recorded exclusion; the DefaultServeMux
// and function-value rules apply everywhere.
func (s *sweeper) visit(decl ast.Node, where string, allowed bool) {
	ast.Inspect(decl, func(n ast.Node) bool {
		switch typed := n.(type) {
		case *ast.Ident:
			s.ident(typed)
		case *ast.CallExpr:
			s.call(typed)
			if !allowed {
				s.production(typed, where)
			}
		case *ast.CompositeLit:
			s.composite(typed)
			if !allowed {
				s.production(typed, where)
			}
		case *ast.ValueSpec:
			if typed.Type == nil || allowed {
				break
			}
			if t := s.info().TypeOf(typed.Type); s.a.set.producesRouter(t) {
				s.report(typed, "a variable is declared with %s %s; its zero value is a working router "+
					"that no listener enumerates", typeLabel(t), where)
			}
		case *ast.TypeAssertExpr:
			if typed.Type != nil {
				s.recovery(typed, typed.Type, "type-asserts to")
			}
		case *ast.TypeSwitchStmt:
			for _, stmt := range typed.Body.List {
				clause, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range clause.List {
					s.recovery(expr, expr, "switches on")
				}
			}
		}
		return true
	})
}

// recovery refuses a type assertion or switch case to a router type.
func (s *sweeper) recovery(at ast.Node, typeExpr ast.Expr, verb string) {
	t := s.info().TypeOf(typeExpr)
	if _, isParam := types.Unalias(t).(*types.TypeParam); isParam {
		s.report(at, "%s a type parameter; instantiated with a router type it would recover a router "+
			"no listener enumerates", verb)
		return
	}
	if kind := s.a.set.routerKind(t); kind != ctorNone {
		s.report(at, "%s %s, which is %s by its method set; a router recovered from a value is a "+
			"registration surface the inventory cannot see", verb, typeLabel(t), kind)
	}
}

// ident refuses the identifiers that matter wherever they appear: a router
// constructor used as a function value, http.DefaultServeMux, a generic
// instantiated with a router type, and reflect.Value.MethodByName.
func (s *sweeper) ident(ident *ast.Ident) {
	if inst, ok := s.info().Instances[ident]; ok && inst.TypeArgs != nil {
		for i := 0; i < inst.TypeArgs.Len(); i++ {
			if kind := s.a.set.routerKind(inst.TypeArgs.At(i)); kind != ctorNone {
				s.report(ident, "%s is instantiated with %s, which is %s by its method set; a generic "+
					"can assert a value to its type argument and recover a router", ident.Name,
					typeLabel(inst.TypeArgs.At(i)), kind)
			}
		}
	}
	switch obj := s.info().Uses[ident].(type) {
	case *types.Func:
		if isRouterCtor(obj) != ctorNone && !s.a.set.callees[ident] {
			s.report(ident, "%s.%s is used as a function value rather than called; a router built through it "+
				"would not be recognized as a construction", obj.Pkg().Name(), obj.Name())
		}
		if obj.Pkg() != nil && obj.Pkg().Path() == "reflect" && obj.Name() == "MethodByName" {
			s.report(ident, "reflect.Value.MethodByName can call a registration method on a router the "+
				"inventory cannot see")
		}
	case *types.Var:
		if obj.Pkg() != nil && obj.Pkg().Path() == httpImportPath && obj.Name() == "DefaultServeMux" {
			s.report(ident, "refers to http.DefaultServeMux, which no listener enumerates")
		}
	}
}

// call refuses the net/http calls that reach http.DefaultServeMux.
func (s *sweeper) call(call *ast.CallExpr) {
	fn := calleeFunc(call, s.info())
	if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != httpImportPath || fn.Signature().Recv() != nil {
		return
	}
	switch {
	case defaultMuxRegistrars[fn.Name()]:
		s.report(call, "http.%s registers on http.DefaultServeMux, which no listener enumerates", fn.Name())
	case handlerConsumingCalls[fn.Name()] && len(call.Args) > 0 && isNilValue(call.Args[len(call.Args)-1], s.info()):
		s.report(call, "serves a nil handler, which is http.DefaultServeMux; no listener enumerates it")
	}
}

// composite refuses an http.Server literal that would serve
// http.DefaultServeMux: one without a Handler element, or with Handler: nil.
func (s *sweeper) composite(lit *ast.CompositeLit) {
	named, ok := types.Unalias(s.info().TypeOf(lit)).(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != httpImportPath || named.Obj().Name() != "Server" {
		return
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			s.report(lit, "http.Server is built with a positional literal; the route inventory requires "+
				"a keyed literal with a non-nil Handler")
			return
		}
		if key, _ := kv.Key.(*ast.Ident); key != nil && key.Name == methodHandler {
			if isNilValue(kv.Value, s.info()) {
				s.report(kv, "http.Server is built with Handler: nil, which serves http.DefaultServeMux")
			}
			return
		}
	}
	s.report(lit, "http.Server is built without a Handler, so it serves http.DefaultServeMux; "+
		"no listener enumerates that mux")
}

// production refuses an expression that brings a router into existence.
func (s *sweeper) production(expr ast.Expr, where string) {
	tv, ok := s.info().Types[expr]
	if !ok || tv.IsType() || !s.a.set.producesRouter(tv.Type) {
		return
	}
	if call, ok := expr.(*ast.CallExpr); ok && s.derivesRouter(call) {
		return
	}
	if strings.HasPrefix(where, "at ") {
		s.report(expr, "%s constructed %s, which belongs to no listener entry point; "+
			"build it inside a declared listener entry function", s.a.set.exprText(expr), where)
		return
	}
	s.report(expr, "%s constructed %s outside the inventoried listeners; "+
		"add it as a listener or record it as an explicit exclusion for that function", s.a.set.exprText(expr), where)
}

// derivesRouter reports whether a call returns a router that already existed:
// a method on a router (Route, Group, With narrow their receiver), or a call to
// a non-generic function or method declared in this module, whose body is
// swept on its own. A generic instantiation is not exempt: `new(T)` inside it
// is typed *T, so the router only becomes visible at the call.
func (s *sweeper) derivesRouter(call *ast.CallExpr) bool {
	fn := calleeFunc(call, s.info())
	if fn == nil {
		return false
	}
	if recv := fn.Signature().Recv(); recv != nil && routerTypeKind(recv.Type()) != ctorNone {
		return true
	}
	if s.a.set.funcDecls[fn] == nil {
		return false
	}
	_, instantiated := s.info().Instances[calleeIdent(call)]
	return !instantiated
}

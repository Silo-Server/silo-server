package routeinventory

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

// The sealing invariant is the structural half of the completeness guarantee:
// after a listener entry function returns, nothing can register on its router,
// because nothing can get the router back. Each entry function returns a value
// of a sealed type — an unexported struct holding the router in an unexported
// field, with ServeHTTP as its only method — built from an unexported
// constructor that the entry function alone calls. The walk starts at the
// constructor. This file checks the shape with type information rather than
// by name, so a type assertion, an alias, a defined or embedding interface, a
// type switch, a generic instantiation and a reflect call all fail the same
// way: the dynamic type of the returned value is never a router.

// checkSeal verifies the sealing shape of one listener and returns the
// constructor declaration the walk starts from.
func (a *Analyzer) checkSeal(spec ListenerSpec, pkg *pkgSource) (*ast.FuncDecl, error) {
	entry := pkg.lookupFunc(spec.Recv, spec.Func)
	if entry == nil {
		return nil, fmt.Errorf("listener %s: entry function %s not found in %s", spec.ID, spec.Entrypoint(), spec.Dir)
	}
	info := pkg.info()
	results := flattenParams(entry.Type.Results)
	if len(results) != 1 || !isHTTPHandlerType(info.TypeOf(results[0].typ)) {
		return nil, a.errorf(entry, "listener %s: entry function %s must return exactly one http.Handler; "+
			"a router-typed result would let a caller register routes after the walk is over", spec.ID, spec.Func)
	}
	if spec.Constructor == "" {
		return nil, fmt.Errorf("listener %s: no constructor declared; the walk has nowhere to start", spec.ID)
	}
	if token.IsExported(spec.Constructor) {
		return nil, fmt.Errorf("listener %s: constructor %s is exported; it must be unexported so only "+
			"the sealing entry function %s can reach the router", spec.ID, spec.Constructor, spec.Func)
	}
	ctor := pkg.lookupFunc(spec.Recv, spec.Constructor)
	if ctor == nil {
		return nil, fmt.Errorf("listener %s: constructor %s not found in %s", spec.ID, spec.Constructor, spec.Dir)
	}
	if ctor.Body == nil {
		return nil, a.errorf(ctor, "listener %s: constructor %s has no body", spec.ID, spec.Constructor)
	}
	ctorObj, _ := info.Defs[ctor.Name].(*types.Func)
	if ctorObj == nil {
		return nil, a.errorf(ctor, "listener %s: constructor %s has no type information", spec.ID, spec.Constructor)
	}
	want := ctorChiRouter
	if spec.kind() == ListenerKindServeMux {
		want = ctorServeMux
	}
	ctorResults := ctorObj.Signature().Results()
	if ctorResults.Len() != 1 || a.set.routerKind(ctorResults.At(0).Type()) != want {
		return nil, a.errorf(ctor, "listener %s: constructor %s must return exactly one %s value; "+
			"got %s", spec.ID, spec.Constructor, want, typeLabel(ctorObj.Signature().Results()))
	}

	if err := a.checkSealedReturn(spec, pkg, entry, ctorObj); err != nil {
		return nil, err
	}
	if err := a.checkConstructorCallers(spec, pkg, entry, ctorObj); err != nil {
		return nil, err
	}
	return ctor, nil
}

// checkSealedReturn requires the entry body to be exactly
// `return sealed{field: ctor(...)}`: one return of one composite literal of a
// sealed type, whose single element is a call to the constructor.
func (a *Analyzer) checkSealedReturn(spec ListenerSpec, pkg *pkgSource, entry *ast.FuncDecl, ctorObj *types.Func) error {
	const shape = "its body must be exactly `return sealedType{field: constructor(...)}`"
	if entry.Body == nil || len(entry.Body.List) != 1 {
		return a.errorf(entry, "listener %s: entry function %s does not seal its router; %s", spec.ID, spec.Func, shape)
	}
	ret, ok := entry.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return a.errorf(entry, "listener %s: entry function %s does not seal its router; %s", spec.ID, spec.Func, shape)
	}
	lit, ok := unwrapParen(ret.Results[0]).(*ast.CompositeLit)
	if !ok || len(lit.Elts) != 1 {
		return a.errorf(ret, "listener %s: entry function %s does not seal its router; %s", spec.ID, spec.Func, shape)
	}
	kv, ok := lit.Elts[0].(*ast.KeyValueExpr)
	if !ok {
		return a.errorf(lit, "listener %s: entry function %s does not seal its router; %s", spec.ID, spec.Func, shape)
	}
	call, ok := unwrapParen(kv.Value).(*ast.CallExpr)
	if !ok || calleeFunc(call, pkg.info()) != ctorObj {
		return a.errorf(kv, "listener %s: entry function %s seals %s rather than a call to constructor %s",
			spec.ID, spec.Func, a.set.exprText(kv.Value), spec.Constructor)
	}
	for _, arg := range call.Args {
		if a.set.tupleRouterKind(pkg.info().TypeOf(arg)) != ctorNone {
			return a.errorf(arg, "listener %s: entry function %s passes a router into constructor %s; "+
				"the walk cannot see where it came from", spec.ID, spec.Func, spec.Constructor)
		}
	}
	return a.checkSealedType(spec, pkg, lit)
}

// checkSealedType requires the literal's type to be a sealed type: a named,
// unexported struct declared in the listener package, whose fields are all
// unexported, none embedded and none a router, and whose method set (on the
// value and on the pointer) is exactly ServeHTTP.
func (a *Analyzer) checkSealedType(spec ListenerSpec, pkg *pkgSource, lit *ast.CompositeLit) error {
	t := pkg.info().TypeOf(lit)
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return a.errorf(lit, "listener %s: %s seals its router in %s, which is not a named type",
			spec.ID, spec.Func, typeLabel(t))
	}
	name := named.Obj().Name()
	if named.Obj().Pkg() != pkg.Pkg.Types {
		return a.errorf(lit, "listener %s: sealed type %s is declared outside %s", spec.ID, name, spec.Dir)
	}
	if token.IsExported(name) {
		return a.errorf(lit, "listener %s: sealed type %s is exported; it must be unexported", spec.ID, name)
	}
	if named.TypeParams().Len() > 0 {
		return a.errorf(lit, "listener %s: sealed type %s is generic; it must be a plain struct", spec.ID, name)
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return a.errorf(lit, "listener %s: sealed type %s is not a struct", spec.ID, name)
	}
	for i := 0; i < st.NumFields(); i++ {
		field := st.Field(i)
		switch {
		case field.Embedded():
			return a.errorf(lit, "listener %s: sealed type %s embeds %s; an embedded field is exported and "+
				"promotes its methods, so the router would be reachable through it", spec.ID, name, typeLabel(field.Type()))
		case field.Exported():
			return a.errorf(lit, "listener %s: sealed type %s has exported field %s; every field must be unexported",
				spec.ID, name, field.Name())
		case a.set.producesRouter(field.Type()):
			return a.errorf(lit, "listener %s: sealed type %s holds a router in field %s (%s); the field must be "+
				"an http.Handler", spec.ID, name, field.Name(), typeLabel(field.Type()))
		}
	}
	for _, recv := range []types.Type{named, types.NewPointer(named)} {
		set := types.NewMethodSet(recv)
		if set.Len() != 1 || set.At(0).Obj().Name() != methodServeHTTP {
			return a.errorf(lit, "listener %s: sealed type %s must have exactly one method, ServeHTTP; "+
				"it has %s", spec.ID, name, methodNames(set))
		}
	}
	if !types.Implements(named, httpHandlerInterface(pkg)) {
		return a.errorf(lit, "listener %s: sealed type %s does not implement http.Handler", spec.ID, name)
	}
	return a.checkSealedUses(spec, pkg, named, lit)
}

// checkSealedUses closes the in-package door: the sealed type is constructed
// only in the entry function's return, and its fields are read only by its
// own ServeHTTP. Anything else — a second literal, a field read in a helper —
// would hand the router back out.
func (a *Analyzer) checkSealedUses(spec ListenerSpec, pkg *pkgSource, named *types.Named, entryLit *ast.CompositeLit) error {
	name := named.Obj().Name()
	serve := pkg.methods[name+"."+methodServeHTTP]
	fields := map[types.Object]bool{}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return a.errorf(entryLit, "listener %s: sealed type %s is not a struct", spec.ID, name)
	}
	for i := 0; i < st.NumFields(); i++ {
		fields[st.Field(i)] = true
	}
	info := pkg.info()
	for _, file := range pkg.Files {
		var err error
		ast.Inspect(file, func(n ast.Node) bool {
			if err != nil {
				return false
			}
			// A literal key (`sealed{h: ...}`) is a plain identifier, not a
			// selector, so the one write in the entry literal passes here and
			// is judged by checkSealedReturn.
			switch typed := n.(type) {
			case *ast.CompositeLit:
				if typed == entryLit || !types.Identical(types.Unalias(info.TypeOf(typed)), named) {
					return true
				}
				err = a.errorf(typed, "listener %s: sealed type %s is constructed outside the entry function %s",
					spec.ID, name, spec.Func)
				return false
			case *ast.SelectorExpr:
				if !fields[info.Uses[typed.Sel]] {
					return true
				}
				if serve != nil && within(serve, typed) {
					return true
				}
				err = a.errorf(typed, "listener %s: field %s of sealed type %s is read outside its ServeHTTP; "+
					"the router behind it would be reachable", spec.ID, typed.Sel.Name, name)
				return false
			}
			return true
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// checkConstructorCallers requires the entry function to be the only caller of
// the constructor, and every reference to it to be a call. Only non-test files
// are loaded, so a test may call the constructor to walk the router.
func (a *Analyzer) checkConstructorCallers(spec ListenerSpec, pkg *pkgSource, entry *ast.FuncDecl, ctorObj *types.Func) error {
	for _, src := range a.set.all {
		info := src.info()
		for _, file := range src.Files {
			var err error
			ast.Inspect(file, func(n ast.Node) bool {
				if err != nil {
					return false
				}
				ident, ok := n.(*ast.Ident)
				if !ok || info.Uses[ident] != ctorObj {
					return true
				}
				switch {
				case !a.set.callees[ident]:
					err = a.errorf(ident, "listener %s: constructor %s is referenced as a value; "+
						"it may only be called from the sealing entry function %s", spec.ID, spec.Constructor, spec.Func)
				case !within(entry, ident):
					err = a.errorf(ident, "listener %s: constructor %s is called outside the sealing entry "+
						"function %s; the router it returns would be a registration surface the walk never "+
						"sees", spec.ID, spec.Constructor, spec.Func)
				}
				return err == nil
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// auditExportedRouterReturns refuses any exported function or method in the
// audited packages whose result is a router. A listener's router leaves its
// package only sealed; an exported function handing one out would be a second
// door.
func (a *Analyzer) auditExportedRouterReturns() error {
	for _, dir := range sortedKeys(a.set.packages) {
		pkg := a.set.packages[dir]
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || !fn.Name.IsExported() {
					continue
				}
				obj, _ := pkg.info().Defs[fn.Name].(*types.Func)
				if obj == nil {
					continue
				}
				results := obj.Signature().Results()
				for i := 0; i < results.Len(); i++ {
					if kind := a.set.routerKind(results.At(i).Type()); kind != ctorNone {
						return a.errorf(fn, "%s.%s is exported and returns %s (%s); a router may leave a package "+
							"only sealed inside a listener entry function", pkg.Pkg.Name, fn.Name.Name,
							typeLabel(results.At(i).Type()), kind)
					}
				}
			}
		}
	}
	return nil
}

func (p *pkgSource) lookupFunc(recv, name string) *ast.FuncDecl {
	if recv == "" {
		return p.funcs[name]
	}
	return p.methods[recv+"."+name]
}

func within(decl *ast.FuncDecl, node ast.Node) bool {
	return decl.Body != nil && node.Pos() >= decl.Body.Pos() && node.End() <= decl.Body.End()
}

func methodNames(set *types.MethodSet) string {
	if set.Len() == 0 {
		return "no methods"
	}
	out := ""
	for i := 0; i < set.Len(); i++ {
		if i > 0 {
			out += ", "
		}
		out += set.At(i).Obj().Name()
	}
	return out
}

// httpHandlerInterface is net/http.Handler as the listener package imports it.
func httpHandlerInterface(pkg *pkgSource) *types.Interface {
	for _, imported := range pkg.Pkg.Types.Imports() {
		if imported.Path() != httpImportPath {
			continue
		}
		obj, ok := imported.Scope().Lookup(methodHandler).(*types.TypeName)
		if !ok {
			break
		}
		if iface, ok := obj.Type().Underlying().(*types.Interface); ok {
			return iface
		}
	}
	return types.NewInterfaceType(nil, nil)
}

package policy

import "github.com/open-policy-agent/opa/v1/ast"

// bundleInputKeys compiles a policy bundle and reports which top-level input
// keys its rules can read. whole is true when the analysis cannot bound the
// reads, including when the bundle does not compile; keys is then nil.
func bundleInputKeys(modules []ModuleSource) (keys map[string]bool, whole bool) {
	caps := LockedCapabilities()
	parsed := make(map[string]*ast.Module, len(modules))
	for _, module := range modules {
		m, err := parsePolicyModule(module.Path, module.Source, caps)
		if err != nil {
			return nil, true
		}
		if m != nil {
			parsed[module.Path] = m
		}
	}
	compiler := ast.NewCompiler().WithCapabilities(caps).WithDefaultRegoVersion(ast.RegoV1)
	compiler.Compile(parsed)
	if compiler.Failed() {
		return nil, true
	}
	return compiledInputKeys(compiler.Modules)
}

// compiledInputKeys over-approximates the top-level input keys that compiled
// modules can read. It follows the input document through the forms the
// compiler leaves it in: input.<key> references, local variables unified with
// input (the compiler binds every call argument that way), parameters of
// functions called with it, and object.get with a constant key. Any other use
// of the whole document (iterating it, a dynamic key, passing it to another
// builtin, comparing or returning it, a with modifier) can read every key, so
// it reports whole.
func compiledInputKeys(modules map[string]*ast.Module) (map[string]bool, bool) {
	a := inputKeyAnalysis{
		functions: make(map[string]int),
		tainted:   make(map[string]map[int]bool),
		keys:      make(map[string]bool),
	}
	var rules []*ast.Rule
	for _, module := range modules {
		for _, rule := range module.Rules {
			rules = append(rules, rule)
			if len(rule.Head.Args) > 0 {
				a.functions[rule.Ref().String()] = len(rule.Head.Args)
			}
		}
	}
	// Tainting a function parameter can expose more reads in that function's
	// body, so repeat until no new parameter receives the input.
	for {
		a.changed = false
		for _, rule := range rules {
			a.rule(rule)
			if a.whole {
				return nil, true
			}
		}
		if !a.changed {
			return a.keys, false
		}
	}
}

type inputKeyAnalysis struct {
	// functions maps each function's path to its arity.
	functions map[string]int
	// tainted records the function parameters that can receive the input.
	tainted map[string]map[int]bool
	keys    map[string]bool
	whole   bool
	changed bool

	// aliases holds the variables of the rule being analyzed that can hold
	// the whole input document.
	aliases map[ast.Var]bool
}

// rule analyzes one rule and its else chain, which share their parameters.
func (a *inputKeyAnalysis) rule(rule *ast.Rule) {
	a.aliases = map[ast.Var]bool{}
	params := a.tainted[rule.Ref().String()]
	for r := rule; r != nil; r = r.Else {
		for i := range params {
			if i >= len(r.Head.Args) {
				a.whole = true
				return
			}
			v, ok := r.Head.Args[i].Value.(ast.Var)
			if !ok {
				a.whole = true
				return
			}
			for j, other := range r.Head.Args {
				if j != i && other.Value.Compare(v) == 0 {
					a.whole = true
					return
				}
			}
			a.aliases[v] = true
		}
	}

	// A variable unified with the input holds it too.
	for grew := true; grew; {
		grew = false
		ast.WalkExprs(rule, func(expr *ast.Expr) bool {
			if !isUnification(expr) {
				return false
			}
			operands := expr.Operands()
			for i, j := range [2]int{1, 0} {
				if a.bare(operands[i]) {
					if v, ok := operands[j].Value.(ast.Var); ok && !a.aliases[v] {
						a.aliases[v] = true
						grew = true
					}
				}
			}
			return false
		})
	}

	var vis *ast.GenericVisitor
	vis = ast.NewGenericVisitor(func(x any) bool { return a.visit(vis, x) })
	for r := rule; r != nil; r = r.Else {
		for i, arg := range r.Head.Args {
			if !params[i] {
				vis.Walk(arg)
			}
		}
		vis.Walk(r.Head.Reference)
		if r.Head.Key != nil {
			vis.Walk(r.Head.Key)
		}
		if r.Head.Value != nil {
			vis.Walk(r.Head.Value)
		}
		vis.Walk(r.Body)
	}
}

// visit classifies one node. It returns true when it has handled the node's
// children itself.
func (a *inputKeyAnalysis) visit(vis *ast.GenericVisitor, x any) bool {
	if a.whole {
		return true
	}
	switch x := x.(type) {
	case *ast.Expr:
		if len(x.With) > 0 {
			a.whole = true
			return true
		}
		if operator := x.OperatorTerm(); operator != nil {
			a.call(vis, operator, x.Operands())
			return true
		}
	case ast.Call:
		if len(x) > 0 {
			a.call(vis, x[0], x[1:])
			return true
		}
	case ast.Var:
		if a.isAlias(x) {
			a.whole = true
			return true
		}
	case ast.Ref:
		if len(x) == 0 {
			return false
		}
		if head, ok := x[0].Value.(ast.Var); !ok || !a.isAlias(head) {
			return false
		}
		if len(x) < 2 {
			a.whole = true
			return true
		}
		key, ok := x[1].Value.(ast.String)
		if !ok {
			a.whole = true
			return true
		}
		a.keys[string(key)] = true
		for _, term := range x[1:] {
			vis.Walk(term)
		}
		return true
	}
	return false
}

// call classifies the arguments of a call. An argument holding the whole input
// is accepted only where the read it allows is known; every other argument is
// walked like any other term.
func (a *inputKeyAnalysis) call(vis *ast.GenericVisitor, operator *ast.Term, args []*ast.Term) {
	handled := make([]bool, len(args))
	op, _ := operator.Value.(ast.Ref)
	switch {
	case len(op) == 0:
	case isUnificationOp(op):
		// After alias propagation both sides hold the input or neither does.
		if len(args) == 2 && a.bare(args[0]) && a.bare(args[1]) {
			handled[0], handled[1] = true, true
		}
	case op.Equal(ast.ObjectGet.Ref()):
		if len(args) >= 2 && a.bare(args[0]) {
			if key, ok := constantFirstKey(args[1]); ok {
				a.keys[key] = true
				handled[0] = true
			}
		}
	default:
		if arity, ok := a.functions[op.String()]; ok {
			// args[arity], when present, receives the function's result.
			for i := 0; i < arity && i < len(args); i++ {
				if a.bare(args[i]) {
					handled[i] = true
					a.taint(op.String(), i)
				}
			}
		}
	}
	vis.Walk(operator)
	for i, arg := range args {
		if !handled[i] {
			vis.Walk(arg)
		}
	}
}

func (a *inputKeyAnalysis) taint(function string, param int) {
	params := a.tainted[function]
	if params == nil {
		params = map[int]bool{}
		a.tainted[function] = params
	}
	if !params[param] {
		params[param] = true
		a.changed = true
	}
}

func (a *inputKeyAnalysis) isAlias(v ast.Var) bool {
	return v.Equal(ast.InputRootDocument.Value) || a.aliases[v]
}

// bare reports whether term is the whole input document or a variable that
// can hold it.
func (a *inputKeyAnalysis) bare(term *ast.Term) bool {
	switch v := term.Value.(type) {
	case ast.Var:
		return a.isAlias(v)
	case ast.Ref:
		if len(v) != 1 {
			return false
		}
		head, ok := v[0].Value.(ast.Var)
		return ok && a.isAlias(head)
	}
	return false
}

func isUnification(expr *ast.Expr) bool {
	operator := expr.OperatorTerm()
	if operator == nil || len(expr.Operands()) != 2 {
		return false
	}
	op, _ := operator.Value.(ast.Ref)
	return isUnificationOp(op)
}

func isUnificationOp(op ast.Ref) bool {
	return op.Equal(ast.Equality.Ref()) || op.Equal(ast.Assign.Ref())
}

// constantFirstKey returns the top-level key an object.get key argument names:
// a string, or a path array whose first element is a string.
func constantFirstKey(term *ast.Term) (string, bool) {
	switch v := term.Value.(type) {
	case ast.String:
		return string(v), true
	case *ast.Array:
		if v.Len() > 0 {
			if key, ok := v.Elem(0).Value.(ast.String); ok {
				return string(key), true
			}
		}
	}
	return "", false
}

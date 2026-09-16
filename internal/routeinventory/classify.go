package routeinventory

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Request and response kinds. `unknown` is a deliberate value: the analyzer
// reports what the source proves and refuses to invent the rest.
const (
	KindNone      = "none"
	KindJSON      = "json"
	KindForm      = "form"
	KindMultipart = "multipart"
	KindBinary    = "binary"
	KindRedirect  = "redirect"
	KindWebSocket = "websocket"
	KindHTML      = "html"
	KindCSS       = "css"
	KindText      = "text"
	KindEventSt   = "event_stream"
)

// Handler identity kinds.
const (
	handlerKindMethod     = "method"
	handlerKindFunc       = "func"
	handlerKindLiteral    = "literal"
	handlerKindExpression = "expression"
	// handlerKindDelegation is a registration that hands a whole path subtree
	// to another inventoried listener.
	handlerKindDelegation = "delegation"
)

// Auth classes, from least to most privileged.
const (
	authPublic          = "public"
	authOptional        = "optional_auth"
	authAuthenticated   = "authenticated"
	authNodeBearer      = "node_bearer"
	authProfileScoped   = "profile_scoped"
	authPermissionGated = "permission_gated"
	authActingAdmin     = "acting_admin"
	// authDelegated marks a registration that forwards a subtree to another
	// listener. The auth that applies is the delegated listener's own, so
	// calling the delegation public would be a claim about routes it does not
	// own.
	authDelegated = "delegated"
)

// Auth traits.
const (
	traitAuthenticated = "authenticated"
	traitActingAdmin   = "acting_admin"
	traitRateLimited   = "rate_limited"
	traitViewerAccess  = "viewer_access"
	traitProfileReq    = "profile_required"
	traitOptionalView  = "optional_viewer_access"
	mwRequestID        = "middleware.RequestID"
)

// Middleware markers for router.go sites that register through a spread
// slice: the analyzer prints the slice identifier, not its elements, so the
// rules key on the identifier text.
const (
	markerApplePushDisplayAuth = "RequireApplePushDisplayAuth"
	markerDisplayMiddlewares   = "displayMiddlewares"
	markerPasswordChange       = "passwordChangeMiddlewares"
)

// streamObservers are the registration-site wrappers that enroll a route in
// stream telemetry. The repository already declares which routes carry media
// bytes there, so the inventory reads that declaration instead of guessing.
var streamObservers = map[string]bool{
	"observeNative": true,
	"observeProxy":  true,
	"observeNode":   true,
}

type handlerInfo struct {
	expr         string
	identity     string
	kind         string
	resolved     bool
	requestKind  string
	responseKind string
	streams      bool
	websocket    bool
}

type classifier struct{ set *sourceSet }

func newClassifier(set *sourceSet) *classifier {
	return &classifier{set: set}
}

// describe resolves a registration's handler expression to a stable identity
// and classifies its body. Identity comes from the type checker: a method
// value names its receiver type, a function names its package, and a closure
// is keyed by listener and path.
func (c *classifier) describe(handler ast.Expr, method, fullPath string, env *walkEnv) handlerInfo {
	info := handlerInfo{expr: c.set.exprText(handler)}
	inner, streams := unwrapHandler(handler)
	info.streams = streams

	var body *ast.BlockStmt
	var decl *ast.FuncDecl
	var params *ast.FieldList

	switch typed := inner.(type) {
	case *ast.FuncLit:
		info.kind = handlerKindLiteral
		info.identity = "literal:" + env.listener.ID + ":" + fullPath
		info.resolved = true
		body = typed.Body
		params = typed.Type.Params
	case *ast.SelectorExpr, *ast.Ident:
		var ident *ast.Ident
		switch named := typed.(type) {
		case *ast.SelectorExpr:
			ident = named.Sel
		case *ast.Ident:
			ident = named
		}
		fn, _ := env.info().Uses[ident].(*types.Func)
		switch {
		case fn == nil:
			info.kind = handlerKindExpression
			info.identity = c.set.exprText(inner)
		case fn.Signature().Recv() != nil:
			info.kind = handlerKindMethod
			info.identity = "(" + typeIdentity(fn.Signature().Recv().Type()) + ")." + fn.Name()
			info.resolved = true
			decl = c.set.funcDecls[fn]
		default:
			info.kind = handlerKindFunc
			info.identity = fn.Pkg().Path() + "." + fn.Name()
			info.resolved = true
			decl = c.set.funcDecls[fn]
		}
	default:
		info.kind = handlerKindExpression
		info.identity = c.set.exprText(inner)
	}

	var evidence *bodyEvidence
	switch {
	case decl != nil && c.set.packages[c.set.declPkg[decl].Dir] != nil:
		evidence = c.evidenceForDecl(decl, handlerOrigins(decl.Type.Params, c.set.declPkg[decl].info()), map[*ast.FuncDecl]bool{})
	case body != nil:
		evidence = c.evidenceForBody(body, env.pkg, handlerOrigins(params, env.info()), map[*ast.FuncDecl]bool{})
	}
	info.requestKind, info.responseKind, info.websocket = resolveKinds(evidence, method)
	info.identity = c.short(info.identity)
	return info
}

// short drops the module prefix so identities read as `internal/api/handlers.X`.
func (c *classifier) short(identity string) string {
	if c.set.modulePath == "" {
		return identity
	}
	return strings.ReplaceAll(identity, c.set.modulePath+"/", "")
}

// unwrapHandler strips the wrappers a registration site puts around a handler
// so the identity is the handler itself, and reports whether the route is
// enrolled in stream telemetry.
func unwrapHandler(expr ast.Expr) (ast.Expr, bool) {
	streams := false
	for {
		call, ok := expr.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return expr, streams
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if streamObservers[fun.Name] {
				streams = true
				expr = call.Args[len(call.Args)-1]
				continue
			}
			return expr, streams
		case *ast.SelectorExpr:
			// http.HandlerFunc(x) and friends: a conversion, not a wrapper.
			if fun.Sel.Name == "HandlerFunc" && len(call.Args) == 1 {
				expr = call.Args[0]
				continue
			}
			return expr, streams
		default:
			return expr, streams
		}
	}
}

// ---------------------------------------------------------------------------
// Body evidence
// ---------------------------------------------------------------------------

type bodyEvidence struct {
	returns          []httpOrigin
	requestJSON      bool
	requestForm      bool
	requestMultipart bool
	requestBinary    bool

	responseJSON     bool
	responseRedirect bool
	responseBinary   bool
	websocket        bool
	contentTypes     []string
}

// Only values descended from the registered handler's request/writer prove
// HTTP body kinds. A client response also has a Body, and encoding JSON into
// a buffer or setting an upstream response header says nothing about this route.
type httpOrigin uint8

const (
	incomingRequest httpOrigin = 1 << iota
	incomingBody
	outgoingWriter
	outgoingHeader
)

type bodyOrigins struct {
	values   map[types.Object]httpOrigin
	booleans map[types.Object]bool
	calls    map[*ast.CallExpr][]httpOrigin
}

func newBodyOrigins() bodyOrigins {
	return bodyOrigins{values: map[types.Object]httpOrigin{}, booleans: map[types.Object]bool{}, calls: map[*ast.CallExpr][]httpOrigin{}}
}

func handlerOrigins(params *ast.FieldList, info *types.Info) bodyOrigins {
	origins := newBodyOrigins()
	if params == nil {
		return origins
	}
	for _, field := range params.List {
		var origin httpOrigin
		switch typeIdentity(info.TypeOf(field.Type)) {
		case "*net/http.Request":
			origin = incomingRequest
		case "net/http.ResponseWriter":
			origin = outgoingWriter
		}
		for _, name := range field.Names {
			origins.values[info.Defs[name]] = origin
		}
	}
	return origins
}

func (origins bodyOrigins) expression(expr ast.Expr, info *types.Info) httpOrigin {
	switch value := unwrapParen(expr).(type) {
	case *ast.Ident:
		return origins.values[info.ObjectOf(value)]
	case *ast.UnaryExpr:
		if value.Op == token.AND {
			return origins.expression(value.X, info)
		}
	case *ast.CompositeLit:
		var origin httpOrigin
		for _, element := range value.Elts {
			if field, ok := element.(*ast.KeyValueExpr); ok {
				element = field.Value
			}
			origin |= origins.expression(element, info) & outgoingWriter
		}
		return origin
	case *ast.SelectorExpr:
		if value.Sel.Name == "Body" && origins.expression(value.X, info)&incomingRequest != 0 {
			return incomingBody
		}
	case *ast.CallExpr:
		if results := origins.calls[value]; len(results) > 0 {
			return results[0]
		}
		name := qualifiedCallee(value.Fun, info)
		switch name {
		case "io.LimitReader", "io.ReadAll", "io.NopCloser":
			if len(value.Args) > 0 {
				return origins.expression(value.Args[0], info) & incomingBody
			}
		case "net/http.MaxBytesReader":
			if len(value.Args) > 1 {
				return origins.expression(value.Args[1], info) & incomingBody
			}
		}
		if sel, ok := value.Fun.(*ast.SelectorExpr); ok {
			receiver := origins.expression(sel.X, info)
			switch sel.Sel.Name {
			case "Header":
				if receiver&outgoingWriter != 0 {
					return outgoingHeader
				}
			case "WithContext", "Clone":
				return receiver & incomingRequest
			case "Bytes":
				return receiver & incomingBody
			}
		}
	}
	return 0
}

// A known helper flag rules out an otherwise syntactically present branch.
// Unknown request-dependent conditions remain possible; false && unknown and
// true || unknown are the only partial Boolean expressions decided here.
func (origins bodyOrigins) boolean(expr ast.Expr, info *types.Info) (bool, bool) {
	if value := info.Types[expr].Value; value != nil && value.Kind() == constant.Bool {
		return constant.BoolVal(value), true
	}
	switch value := unwrapParen(expr).(type) {
	case *ast.Ident:
		result, known := origins.booleans[info.ObjectOf(value)]
		return result, known
	case *ast.UnaryExpr:
		if value.Op == token.NOT {
			result, known := origins.boolean(value.X, info)
			return !result, known
		}
	case *ast.BinaryExpr:
		left, leftKnown := origins.boolean(value.X, info)
		right, rightKnown := origins.boolean(value.Y, info)
		switch value.Op {
		case token.LAND:
			if (leftKnown && !left) || (rightKnown && !right) {
				return false, true
			}
			return left && right, leftKnown && rightKnown
		case token.LOR:
			if (leftKnown && left) || (rightKnown && right) {
				return true, true
			}
			return left || right, leftKnown && rightKnown
		case token.EQL:
			return left == right, leftKnown && rightKnown
		case token.NEQ:
			return left != right, leftKnown && rightKnown
		}
	}
	return false, false
}

func (origins bodyOrigins) assign(names []ast.Expr, values []ast.Expr, info *types.Info) {
	// Read RHS values before rebinding aliases (including parallel assignments).
	found := origins.expressions(values, info)
	found = append(found, make([]httpOrigin, len(names))...)
	bools := make([]bool, len(names))
	known := make([]bool, len(names))
	for i := range names {
		if i < len(values) {
			bools[i], known[i] = origins.boolean(values[i], info)
		}
	}
	for i, name := range names {
		if ident, ok := name.(*ast.Ident); ok {
			origins.values[info.ObjectOf(ident)] = found[i]
			if known[i] {
				origins.booleans[info.ObjectOf(ident)] = bools[i]
			} else {
				delete(origins.booleans, info.ObjectOf(ident))
			}
		}
	}
}

// A sole call may provide several assignment or return values.
func (origins bodyOrigins) expressions(values []ast.Expr, info *types.Info) []httpOrigin {
	if len(values) == 1 {
		if call, ok := values[0].(*ast.CallExpr); ok {
			if results := origins.calls[call]; len(results) > 0 {
				return slices.Clone(results)
			}
		}
	}
	found := make([]httpOrigin, len(values))
	for i, value := range values {
		found[i] = origins.expression(value, info)
	}
	return found
}

func (c *classifier) evidenceForDecl(decl *ast.FuncDecl, origins bodyOrigins, active map[*ast.FuncDecl]bool) *bodyEvidence {
	pkg := c.set.declPkg[decl]
	if pkg == nil {
		return nil
	}
	if active[decl] {
		return nil
	}
	active[decl] = true
	defer delete(active, decl)
	evidence := c.evidenceForBody(decl.Body, pkg, origins, active)
	// Follow module-local wrapper return values without widening the set of
	// packages whose HTTP body evidence the inventory claims to classify.
	if c.set.packages[pkg.Dir] == nil {
		return &bodyEvidence{returns: evidence.returns}
	}
	return evidence
}

func (c *classifier) evidenceForBody(body *ast.BlockStmt, pkg *pkgSource, origins bodyOrigins, active map[*ast.FuncDecl]bool) *bodyEvidence {
	evidence := &bodyEvidence{}
	if body == nil {
		return evidence
	}
	info := pkg.info()
	var inspectExpression func(ast.Expr)
	var inspectCall func(*ast.CallExpr)
	inspected := map[*ast.CallExpr]bool{}
	closures := map[types.Object]*ast.FuncLit{}
	inspectExpression = func(expr ast.Expr) {
		ast.Inspect(expr, func(node ast.Node) bool {
			if binary, ok := node.(*ast.BinaryExpr); ok && (binary.Op == token.LAND || binary.Op == token.LOR) {
				inspectExpression(binary.X)
				value, known := origins.boolean(binary.X, info)
				if !known || (binary.Op == token.LAND && value) || (binary.Op == token.LOR && !value) {
					inspectExpression(binary.Y)
				}
				return false
			}
			if _, ok := node.(*ast.FuncLit); ok {
				return false
			}
			if call, ok := node.(*ast.CallExpr); ok {
				inspectCall(call)
				return false
			}
			return true
		})
	}
	inspectCall = func(call *ast.CallExpr) {
		if inspected[call] {
			return
		}
		inspected[call] = true
		for _, arg := range call.Args {
			inspectExpression(arg)
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			inspectExpression(sel.X)
		}
		var closure *ast.FuncLit
		switch fun := unwrapParen(call.Fun).(type) {
		case *ast.FuncLit:
			closure = fun
		case *ast.Ident:
			closure = closures[info.ObjectOf(fun)]
		}
		if closure != nil {
			captured := newBodyOrigins()
			captured.values = maps.Clone(origins.values)
			captured.booleans = maps.Clone(origins.booleans)
			index := 0
			for _, field := range closure.Type.Params.List {
				for _, name := range field.Names {
					if index < len(call.Args) {
						captured.values[info.Defs[name]] = origins.expression(call.Args[index], info)
						if value, known := origins.boolean(call.Args[index], info); known {
							captured.booleans[info.Defs[name]] = value
						} else {
							delete(captured.booleans, info.Defs[name])
						}
					}
					index++
				}
			}
			nested := c.evidenceForBody(closure.Body, pkg, captured, active)
			merge(evidence, nested)
			origins.calls[call] = nested.returns
			return
		}

		name := calleeName(call.Fun)
		qualified := qualifiedCallee(call.Fun, info)
		var receiver httpOrigin
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			receiver = origins.expression(sel.X, info)
		}
		var args httpOrigin
		for _, arg := range call.Args {
			args |= origins.expression(arg, info)
		}
		first := httpOrigin(0)
		if len(call.Args) > 0 {
			first = origins.expression(call.Args[0], info)
		}

		switch {
		case (qualified == "encoding/json.NewDecoder" || qualified == "encoding/json.Unmarshal") && first&incomingBody != 0:
			evidence.requestJSON = true
		case (name == "ParseMultipartForm" || name == "FormFile" || name == "MultipartReader") && receiver&incomingRequest != 0:
			evidence.requestMultipart = true
		case (name == "ParseForm" || name == "PostFormValue" || name == "FormValue") && receiver&incomingRequest != 0:
			evidence.requestForm = true
		case qualified == "io.ReadAll" && first&incomingBody != 0:
			evidence.requestBinary = true
		case (qualified == "io.Copy" || qualified == "io.CopyN" || qualified == "io.CopyBuffer") && len(call.Args) > 1:
			evidence.requestBinary = evidence.requestBinary || origins.expression(call.Args[1], info)&incomingBody != 0
		}

		switch {
		case first&outgoingWriter != 0 && (qualified == "encoding/json.NewEncoder" || name == "writeJSON" || name == "writeError" ||
			name == "respondJSON" || name == "WriteJSON" || name == "writeJSONError"):
			evidence.responseJSON = true
		case qualified == "net/http.Redirect" && first&outgoingWriter != 0:
			evidence.responseRedirect = true
		case (qualified == "net/http.ServeContent" || qualified == "net/http.ServeFile" || qualified == "io.Copy" || qualified == "io.CopyN" || qualified == "io.CopyBuffer") && first&outgoingWriter != 0:
			evidence.responseBinary = true
		case name == methodServeHTTP && args&outgoingWriter != 0:
			evidence.responseBinary = true
		case (name == "Upgrade" || name == "Accept") && args&outgoingWriter != 0 && args&incomingRequest != 0:
			fn := calleeFunc(call, info)
			if fn != nil && fn.Pkg() != nil && strings.Contains(fn.Pkg().Path(), "websocket") {
				evidence.websocket = true
			}
		}

		if name == "Set" && receiver&outgoingHeader != 0 && len(call.Args) == 2 {
			if header, ok := stringLiteral(call.Args[0]); ok && strings.EqualFold(header, "Content-Type") {
				if value, ok := stringLiteral(call.Args[1]); ok {
					evidence.contentTypes = append(evidence.contentTypes, value)
				}
			}
		}

		if name == "ReadFrom" && first&incomingBody != 0 {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && typeIdentity(info.TypeOf(sel.X)) == "bytes.Buffer" {
					origins.values[info.ObjectOf(id)] |= incomingBody
					evidence.requestBinary = true
				}
			}
		}

		// The type checker distinguishes receiver methods, method expressions and
		// bare helpers, even when unrelated types use the same method name. Bind
		// only the actual arguments: an io.Reader helper may read either an inbound
		// upload or an upstream response depending on this call site.
		if fn := calleeFunc(call, info); fn != nil && args != 0 {
			if inner := c.set.funcDecls[fn]; inner != nil {
				bound := newBodyOrigins()
				offset := 0
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if selection := info.Selections[sel]; selection != nil && selection.Kind() == types.MethodExpr {
						offset = 1
					}
				}
				params := fn.Signature().Params()
				for i := range params.Len() {
					if i+offset < len(call.Args) {
						bound.values[params.At(i)] = origins.expression(call.Args[i+offset], info)
						if value, known := origins.boolean(call.Args[i+offset], info); known {
							bound.booleans[params.At(i)] = value
						}
					}
				}
				nested := c.evidenceForDecl(inner, bound, active)
				merge(evidence, nested)
				if nested != nil {
					origins.calls[call] = nested.returns
				}
			}
		}
	}
	var visit func(ast.Node) bool
	visit = func(node ast.Node) bool {
		switch statement := node.(type) {
		case *ast.IfStmt:
			if statement.Init != nil {
				ast.Inspect(statement.Init, visit)
			}
			inspectExpression(statement.Cond)
			if condition, known := origins.boolean(statement.Cond, info); known {
				if condition {
					ast.Inspect(statement.Body, visit)
				} else if statement.Else != nil {
					ast.Inspect(statement.Else, visit)
				}
				return false
			}
			// Neither branch may turn a conditional assignment into a known
			// flag for the statements that follow the if.
			before := maps.Clone(origins.booleans)
			ast.Inspect(statement.Body, visit)
			left := origins.booleans
			origins.booleans = before
			if statement.Else != nil {
				ast.Inspect(statement.Else, visit)
			}
			for object, value := range origins.booleans {
				if other, known := left[object]; !known || value != other {
					delete(origins.booleans, object)
				}
			}
			return false
		case *ast.AssignStmt:
			for _, value := range statement.Rhs {
				inspectExpression(value)
			}
			origins.assign(statement.Lhs, statement.Rhs, info)
			for i, lhs := range statement.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && i < len(statement.Rhs) {
					closure, _ := statement.Rhs[i].(*ast.FuncLit)
					closures[info.ObjectOf(id)] = closure
				}
			}
			return false
		case *ast.ValueSpec:
			for _, value := range statement.Values {
				inspectExpression(value)
			}
			names := make([]ast.Expr, len(statement.Names))
			for i, name := range statement.Names {
				names[i] = name
			}
			origins.assign(names, statement.Values, info)
			return false
		case *ast.ReturnStmt:
			for _, value := range statement.Results {
				inspectExpression(value)
			}
			results := origins.expressions(statement.Results, info)
			if len(results) > len(evidence.returns) {
				evidence.returns = append(evidence.returns, make([]httpOrigin, len(results)-len(evidence.returns))...)
			}
			for i, origin := range results {
				evidence.returns[i] |= origin
			}
			return false
		case *ast.CallExpr:
			inspectCall(statement)
			return false
		case *ast.FuncLit:
			// Its body is not executed merely by creating a function value.
			return false
		}
		return true
	}
	ast.Inspect(body, visit)
	slices.Sort(evidence.contentTypes)
	evidence.contentTypes = slices.Compact(evidence.contentTypes)
	return evidence
}

func merge(into, from *bodyEvidence) {
	if from == nil {
		return
	}
	into.requestJSON = into.requestJSON || from.requestJSON
	into.requestForm = into.requestForm || from.requestForm
	into.requestMultipart = into.requestMultipart || from.requestMultipart
	into.requestBinary = into.requestBinary || from.requestBinary
	into.responseJSON = into.responseJSON || from.responseJSON
	into.responseRedirect = into.responseRedirect || from.responseRedirect
	into.responseBinary = into.responseBinary || from.responseBinary
	into.websocket = into.websocket || from.websocket
	into.contentTypes = append(into.contentTypes, from.contentTypes...)
}

func resolveKinds(evidence *bodyEvidence, method string) (request, response string, websocket bool) {
	if evidence == nil {
		return unknownClassification, unknownClassification, false
	}
	switch {
	case evidence.requestMultipart:
		request = KindMultipart
	case evidence.requestForm:
		request = KindForm
	case evidence.requestJSON:
		request = KindJSON
	case evidence.requestBinary:
		request = KindBinary
	case method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions:
		request = KindNone
	default:
		request = unknownClassification
	}

	switch {
	case evidence.websocket:
		response = KindWebSocket
	case evidence.responseRedirect:
		response = KindRedirect
	case len(evidence.contentTypes) > 0:
		response = mediaKind(evidence.contentTypes)
	case evidence.responseJSON:
		response = KindJSON
	case evidence.responseBinary:
		response = KindBinary
	default:
		response = unknownClassification
	}
	return request, response, evidence.websocket
}

func mediaKind(contentTypes []string) string {
	for _, value := range contentTypes {
		media := strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
		switch {
		case strings.HasSuffix(media, "json"):
			return KindJSON
		case media == "text/html":
			return KindHTML
		case media == "text/css":
			return KindCSS
		case media == "text/event-stream":
			return KindEventSt
		case strings.HasPrefix(media, "text/"):
			return KindText
		case strings.HasPrefix(media, "video/"), strings.HasPrefix(media, "audio/"),
			strings.HasPrefix(media, "image/"), strings.HasPrefix(media, "font/"),
			strings.HasPrefix(media, "application/octet-stream"),
			strings.Contains(media, "mpegurl"):
			return KindBinary
		}
	}
	return unknownClassification
}

func calleeName(fun ast.Expr) string {
	switch typed := fun.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		return typed.Sel.Name
	case *ast.CallExpr:
		return calleeName(typed.Fun)
	}
	return ""
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

// ---------------------------------------------------------------------------
// Auth classification
// ---------------------------------------------------------------------------

type authRule struct {
	marker string
	class  string
	trait  string
	rank   int
}

// authRules maps middleware source expressions to the auth class they impose.
// An unmatched middleware is reported as `unclassified_middleware` rather than
// being silently treated as public.
var authRules = []authRule{
	{marker: "requireActingAdmin", class: authActingAdmin, trait: traitActingAdmin, rank: 60},
	{marker: "markerEditAccess", class: authPermissionGated, trait: "marker_edit", rank: 50},
	{marker: "metadataItemAccess", class: authPermissionGated, trait: "metadata_curation", rank: 50},
	{marker: "metadataCurationAccess", class: authPermissionGated, trait: "metadata_curation", rank: 50},
	{marker: "RequireViewerAccess", class: authProfileScoped, trait: traitViewerAccess, rank: 40},
	{marker: "RequireProfile", class: authProfileScoped, trait: traitProfileReq, rank: 40},
	// The Apple push display gate (internal/api/middleware/apple_push_display.go)
	// requires auth on both credential paths and resolves a profile: ordinary
	// tokens fall back to RequireAuth + viewer access + RequireProfile, and a
	// display token carries a ProfileID claim that is checked against the
	// session and written to X-Profile-Id. router.go registers it through the
	// spread slice `displayMiddlewares`, which is the identifier the analyzer
	// prints, so both spellings carry the same rules.
	{marker: markerApplePushDisplayAuth, class: authProfileScoped, trait: traitProfileReq, rank: 40},
	{marker: markerDisplayMiddlewares, class: authProfileScoped, trait: traitProfileReq, rank: 40},
	{marker: "RequireAuth", class: authAuthenticated, trait: traitAuthenticated, rank: 30},
	{marker: "requireBearer", class: authNodeBearer, trait: "node_bearer", rank: 30},
	{marker: "OptionalAuth", class: authOptional, trait: "optional_auth", rank: 20},
}

// traitOnlyRules are middleware that qualify a route without setting its auth
// class.
var traitOnlyRules = []authRule{
	{marker: "RateLimitMW", trait: traitRateLimited},
	{marker: "AuthEndpointHandler", trait: traitRateLimited},
	{marker: "demoGuard", trait: "demo_guarded"},
	{marker: "meterEgress", trait: "egress_metered"},
	{marker: "cors.Handler", trait: "cors"},
	{marker: "optionalProfileViewerAccess", trait: traitOptionalView},
	// router.go builds `passwordChangeMiddlewares` for POST
	// /api/v1/auth/account/password: optionalProfileViewerAccess plus, when a
	// limiter is configured, RateLimitMW.AuthEndpointHandler("password_change").
	// RequireAuth is applied separately by the enclosing group. Conditional
	// limiters are recorded as present, matching the RateLimitMW rules above.
	{marker: markerPasswordChange, trait: traitOptionalView},
	{marker: markerPasswordChange, trait: traitRateLimited},
	// The Apple push display gate always ends in RequireAuth-equivalent
	// authentication, viewer access, and RequireProfile; `displayMiddlewares`
	// also prepends RateLimitMW.Handler when a limiter is configured. See the
	// matching authRules entries for the class.
	{marker: markerApplePushDisplayAuth, trait: traitAuthenticated},
	{marker: markerApplePushDisplayAuth, trait: traitViewerAccess},
	{marker: markerDisplayMiddlewares, trait: traitAuthenticated},
	{marker: markerDisplayMiddlewares, trait: traitViewerAccess},
	{marker: markerDisplayMiddlewares, trait: traitRateLimited},
}

// infrastructureMiddleware is the base stack every request passes through. It
// is listed so a genuinely new middleware stands out as unclassified.
var infrastructureMiddleware = []string{
	"apimw.RequestID", mwRequestID, "middleware.Recoverer", "apimw.RequestLogger", "apimw.Metrics",
	"httpstream.CompressExcept", "httpstream.CompressWithExclusions", "clientip.Middleware", "activitylog.NewMiddleware",
	// netaccess.Middleware validates and strips the network access ingress
	// token on every listener; it records the access path and never grants
	// or changes authorization.
	"netaccess.Middleware",
}

func classifyAuth(middleware []string) (string, []string) {
	class := authPublic
	rank := 0
	traits := map[string]bool{}

	for _, mw := range middleware {
		matched := false
		for _, rule := range authRules {
			if !strings.Contains(mw, rule.marker) {
				continue
			}
			matched = true
			traits[rule.trait] = true
			if rule.rank > rank {
				rank, class = rule.rank, rule.class
			}
		}
		for _, rule := range traitOnlyRules {
			if strings.Contains(mw, rule.marker) {
				matched = true
				traits[rule.trait] = true
			}
		}
		if matched {
			continue
		}
		for _, known := range infrastructureMiddleware {
			if strings.Contains(mw, known) {
				matched = true
				break
			}
		}
		if !matched {
			traits["unclassified_middleware"] = true
		}
	}

	out := make([]string, 0, len(traits))
	for trait := range traits {
		out = append(out, trait)
	}
	sort.Strings(out)
	return class, out
}

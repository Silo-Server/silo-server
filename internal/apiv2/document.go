package apiv2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// The OpenAPI document: how a registration is described, and how the
// committed artifact is generated from the registries alone.

// Operation extensions the generated document carries so a reader (the spec
// lint, a client generator) sees the declaration the registry enforced.
const (
	extClass          = "x-silo-class"
	extPermission     = "x-silo-permission"
	extDemoRestricted = "x-silo-demo-restricted"
	extServiceBacked  = "x-silo-service-backed"
	// extGuarded, extConditional and extCreateOnly record the
	// optimistic-concurrency declarations so a reader of the document can
	// enumerate the operations that require If-Match, honor If-None-Match on
	// a read, or honor If-None-Match: * on a create.
	extGuarded     = "x-silo-guarded"
	extConditional = "x-silo-conditional"
	extCreateOnly  = "x-silo-create-only"
	// extRetrySafety records the mutation retry-safety strategy so a reader
	// of the document can see how each mutation tolerates a duplicate
	// submission or a retry after a lost response.
	extRetrySafety = "x-silo-retry-safety"
	// extExtensionBag marks the one legitimate use of additionalProperties:
	// a named bag whose keys are not fixed by this contract. The spec lint
	// refuses any other additionalProperties.
	extExtensionBag = "x-silo-extension-bag"
)

// securitySchemeBearer is the single security scheme. Session tokens and API
// keys both travel in `Authorization: Bearer`; the v1 gate decides which one
// it holds.
const securitySchemeBearer = "bearerAuth"

// profileHeader is the header every profile-resolving class reads;
// profileTokenHeader is the proof it reads alongside it for a PIN-locked
// profile.
const (
	profileHeader      = "X-Profile-Id"
	profileTokenHeader = "X-Profile-Token"
)

// paramInHeader is the OpenAPI parameter location of a request header.
const paramInHeader = "header"

// documentDeclaration writes the class-implied documentation onto the Huma
// operation before registration: the security requirement, the profile
// header, the extensions, and the shared problem statuses. Every status
// listed here is one the listener really produces for that shape of
// operation; the spec lint checks the committed artifact against the same
// table (internal/contractspec).
func documentDeclaration(op *Operation, input reflect.Type) {
	if op.Extensions == nil {
		op.Extensions = map[string]any{}
	}
	op.Extensions[extClass] = string(op.Class)
	if op.Permission != "" {
		op.Extensions[extPermission] = op.Permission
	}
	if op.DemoRestricted {
		op.Extensions[extDemoRestricted] = true
	}
	if op.ServiceBacked {
		op.Extensions[extServiceBacked] = true
	}
	if op.Guarded {
		op.Extensions[extGuarded] = true
		op.Parameters = append(op.Parameters, ifMatchParam())
	}
	if op.Conditional {
		op.Extensions[extConditional] = true
		op.Parameters = append(op.Parameters, ifMatchOptionalParam())
	}
	if op.Guarded {
		op.Parameters = append(op.Parameters, ifNoneMatchGuardedParam())
	}
	if op.CreateOnly {
		op.Extensions[extCreateOnly] = true
		op.Parameters = append(op.Parameters, ifMatchOptionalParam(), ifNoneMatchCreateParam())
	}
	if op.RetrySafety != "" {
		op.Extensions[extRetrySafety] = string(op.RetrySafety)
	}
	// Forward guard: the contract lets Idempotency-Key appear only on an
	// operation that implements and documents it, never as an advertised
	// field the server ignores.
	if declaresHeader(input, idempotencyKeyField) && op.RetrySafety != RetrySafetyIdempotencyKey {
		panic(fmt.Sprintf("apiv2: %s: input declares header %s but retry safety is %q, not %s", op.OperationID, idempotencyKeyField, op.RetrySafety, RetrySafetyIdempotencyKey))
	}
	hasBody := declaresBody(input)
	hasPath := strings.Contains(op.Path, "{")
	for _, status := range ImpliedStatuses(op.Class, op.DemoRestricted, op.ServiceBacked, hasBody, hasPath, op.Guarded, op.Conditional, op.CreateOnly) {
		if status < http.StatusBadRequest {
			// 304 is a success shape, documented with its headers by
			// documentConcurrencyResponses, not a problem.
			continue
		}
		op.Errors = appendUnique(op.Errors, status)
	}
	if op.Class == ClassPublic {
		return
	}
	op.Security = []map[string][]string{{securitySchemeBearer: {}}}
	if resolvesProfile(op.Class) {
		class := op.Class
		if op.ProfileOptional {
			class = ClassAuthenticated // documented as optional
		}
		op.Parameters = append(op.Parameters, profileHeaderParam(class), profileTokenHeaderParam())
	}
}

// profileTokenHeaderParam documents X-Profile-Token, which viewer access
// reads next to X-Profile-Id: a PIN-locked profile resolves only with it,
// and without it the gate answers profile_verification_required. It is never
// required, since an unlocked profile needs no proof.
func profileTokenHeaderParam() *huma.Param {
	return &huma.Param{
		Name:        profileTokenHeader,
		In:          paramInHeader,
		Description: "Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked",
		Schema:      &huma.Schema{Type: "string", Examples: []any{"pvt_5f3a9c1e7b2d4e8fa0c6"}},
	}
}

// profileHeaderParam documents X-Profile-Id the way the class's gate chain
// really reads it. Only ClassProfileScoped runs RequireProfile, so only it
// requires the header. The acting-admin and permission gates accept an
// absent header exactly as v1's RequireActingAdmin does, and judge a present
// one: it must name a profile of the caller's account, and for acting_admin
// the account's primary profile.
func profileHeaderParam(class Class) *huma.Param {
	p := &huma.Param{
		Name:   profileHeader,
		In:     paramInHeader,
		Schema: &huma.Schema{Type: "string", Examples: []any{"1"}},
	}
	switch class {
	case ClassProfileScoped:
		p.Required = true
		p.Description = "The household profile acting for this request; it must belong to the authenticated account."
	case ClassActingAdmin:
		p.Description = "Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted."
	default:
		p.Description = "Optional. When present, it must name a profile of the authenticated account."
	}
	return p
}

// ifMatchParam documents the precondition a guarded operation requires. The
// handler binds the same header through its input struct; declaring the
// parameter here, before Huma sees the input, is what marks it required in
// the document without Huma refusing the request itself (a missing field is
// the 428 problem, not a 422).
func ifMatchParam() *huma.Param {
	return &huma.Param{
		Name:        ifMatchField,
		In:          paramInHeader,
		Required:    true,
		Description: "The resource's current ETag, or \"*\" to overwrite deliberately. A missing field is 428 precondition_required; a stale tag is 412 precondition_failed with the current ETag.",
		Schema:      &huma.Schema{Type: "string"},
	}
}

// ifNoneMatchCreateParam documents the optional create-only precondition:
// "*" refuses to overwrite an existing resource at the client-selected id.
// ifNoneMatchGuardedParam documents the optional second precondition a
// guarded mutation may bind: evaluated after If-Match succeeds, per RFC 9110
// 13.2.2.
func ifNoneMatchGuardedParam() *huma.Param {
	return &huma.Param{
		Name:        ifNoneMatchField,
		In:          paramInHeader,
		Required:    false,
		Description: "Optional second precondition, evaluated after If-Match succeeds: \"*\" or any tag matching the current representation is 412 precondition_failed with the current ETag.",
		Schema:      &huma.Schema{Type: "string"},
	}
}

// ifMatchOptionalParam documents the optional first precondition on a
// conditional read or a create-only write: evaluated before If-None-Match,
// per RFC 9110 13.2.2; a tag that does not match the current representation
// (or any tag against a missing resource) is 412.
func ifMatchOptionalParam() *huma.Param {
	return &huma.Param{
		Name:        ifMatchField,
		In:          paramInHeader,
		Required:    false,
		Description: "Optional first precondition, evaluated before If-None-Match: a tag that does not match the current representation is 412 precondition_failed.",
		Schema:      &huma.Schema{Type: "string"},
	}
}

func ifNoneMatchCreateParam() *huma.Param {
	return &huma.Param{
		Name:        ifNoneMatchField,
		In:          paramInHeader,
		Required:    false,
		Description: "\"*\" makes the request create-only: a resource already stored at this id is 412 precondition_failed with its current ETag. Absent, the request replaces or creates.",
		Schema:      &huma.Schema{Type: "string"},
	}
}

// documentConcurrencyResponses runs after Huma has derived the success
// responses: it documents the ETag header on every 2xx response of a guarded,
// conditional or create-only operation and adds the 304 response of a
// conditional read.
func documentConcurrencyResponses(oapi *huma.OpenAPI, op Operation) {
	if !op.Guarded && !op.Conditional && !op.CreateOnly {
		return
	}
	registered := registeredOperation(oapi, op)
	if registered == nil {
		return
	}
	etag := func() *huma.Header {
		return &huma.Header{
			Description: "The strong, opaque validator of the representation; send it back in If-Match on a guarded mutation or If-None-Match on a conditional read.",
			Schema:      &huma.Schema{Type: "string"},
		}
	}
	if op.Conditional {
		// Merge into a 304 the registration already declares (with its
		// own description or headers, such as Retry-After on a polled
		// job) rather than replacing it.
		key := strconv.Itoa(http.StatusNotModified)
		resp := registered.Responses[key]
		if resp == nil {
			resp = &huma.Response{Description: "The representation named by If-None-Match is current; no body."}
			registered.Responses[key] = resp
		}
		// A 304 is bodyless by contract and bufferResponse drops any body,
		// so a predeclared representation would document what is never
		// sent.
		resp.Content = nil
		mergeETagHeader(resp, etag)
	}
	for status, resp := range registered.Responses {
		code, err := strconv.Atoi(status)
		if err != nil {
			if strings.EqualFold(status, "2XX") {
				// The OpenAPI range key is a success too; the runtime output
				// sends ETag on every one, so the range documents it.
				mergeETagHeader(resp, etag)
			}
			continue
		}
		if code == http.StatusNoContent {
			// A 204 is bodyless by contract and bufferResponse drops any
			// body, so a predeclared representation would document what is
			// never sent, whatever the method.
			resp.Content = nil
		}
		switch {
		case code == http.StatusNoContent && op.Guarded && op.Method == http.MethodDelete:
			// A guarded DELETE's 204 has no representation to validate and
			// Register refuses an ETag field on its output.
			continue
		case code >= 200 && code < 300:
			// Every other success, including a bodyless 204 from a PUT or
			// PATCH output that carries only ETag, sends the header.
		case code == http.StatusPreconditionFailed && (op.Guarded || op.CreateOnly || op.Conditional):
			// A stale If-Match, a refused create-only If-None-Match, and a
			// lost compare-and-update race all answer 412 with the current
			// validator when the caller may see it (EvaluateIfMatch,
			// EvaluateCreateOnly, StaleVersionProblem), so a client can
			// reload without a second round trip.
		default:
			continue
		}
		mergeETagHeader(resp, etag)
	}
}

// registeredOperation finds the operation Huma recorded for op's method and
// path in the document, or nil.
func registeredOperation(oapi *huma.OpenAPI, op Operation) *huma.Operation {
	item := oapi.Paths[op.Path]
	if item == nil {
		return nil
	}
	switch op.Method {
	case http.MethodGet:
		return item.Get
	case http.MethodHead:
		return item.Head
	case http.MethodPost:
		return item.Post
	case http.MethodPut:
		return item.Put
	case http.MethodPatch:
		return item.Patch
	case http.MethodDelete:
		return item.Delete
	}
	return nil
}

// declaredResponseDescriptions collects the description a registration
// declared on each response, keyed by status, before Huma rewrites the
// error statuses.
func declaredResponseDescriptions(op Operation) map[string]string {
	out := map[string]string{}
	for status, resp := range op.Responses {
		if resp != nil && resp.Description != "" {
			out[status] = resp.Description
		}
	}
	return out
}

// documentDeclaredResponseDescriptions restores the descriptions a
// registration declared on responses Huma's defineErrors rewrote with the
// bare status text (a status in Errors that the registration also described,
// such as the 409 updateNavigationShortcut answers when the compare-and-set
// retries are exhausted). Content and headers stay as Huma documented them.
func documentDeclaredResponseDescriptions(oapi *huma.OpenAPI, op Operation, descriptions map[string]string) {
	if len(descriptions) == 0 {
		return
	}
	registered := registeredOperation(oapi, op)
	if registered == nil {
		return
	}
	for status, description := range descriptions {
		if resp := registered.Responses[status]; resp != nil {
			resp.Description = description
		}
	}
}

// mergeETagHeader documents ETag on resp as one canonical entry. Every
// case-equivalent key a registration declared by hand is collapsed into it,
// the declared description is kept (the canonical key's first, else the
// first declared one in sorted-key order), and the schema is replaced by
// the contract's string schema: registration requires the runtime field to be a string and
// NotModified writes one, so a hand-declared integer schema would have
// generated clients deserialize the header with the wrong type.
func mergeETagHeader(resp *huma.Response, etag func() *huma.Header) {
	if resp.Headers == nil {
		resp.Headers = map[string]*huma.Header{}
	}
	var keys []string
	for name := range resp.Headers {
		if strings.EqualFold(name, etagField) {
			keys = append(keys, name)
		}
	}
	if len(keys) == 0 {
		resp.Headers[etagField] = etag()
		return
	}
	// Keep the canonical key's description when it has one, else the
	// first declared description in sorted-key order.
	sort.Strings(keys)
	description := ""
	if h := resp.Headers[etagField]; h != nil && h.Description != "" {
		description = h.Description
	}
	for _, name := range keys {
		if description == "" && resp.Headers[name] != nil && resp.Headers[name].Description != "" {
			description = resp.Headers[name].Description
		}
		delete(resp.Headers, name)
	}
	merged := etag()
	if description != "" {
		merged.Description = description
	}
	resp.Headers[etagField] = merged
}

// resolvesProfile reports whether the class runs viewer access and so needs
// the profile header.
func resolvesProfile(class Class) bool {
	return class == ClassProfileScoped || class == ClassActingAdmin || class == ClassPermissionGated
}

// ImpliedStatuses lists the problem statuses an operation of the given shape
// documents, in ascending order. Shared by every operation: 400 (malformed
// request), 406 (negotiation), 422 (undeclared query parameter), 500. A body
// adds 408 (the body-read deadline), 413 and 415; a path parameter adds 404,
// as does a profile-resolving class (viewer access answers 404 for an unknown
// X-Profile-Id before the handler runs); a service-backed handler adds 503
// (its service is not wired); every gated class adds 401, 429 and 503 (a
// gate the wiring lacks fails closed); a class that resolves a profile or a
// demo-restricted mutation adds 403; a guarded mutation adds 412 and 428; a
// conditional read adds 304; a create-only PUT adds 412. Statuses an
// operation produces on its own (409 for a name conflict, say) are declared
// on that operation's Errors, not here.
func ImpliedStatuses(class Class, demoRestricted, serviceBacked, hasBody, hasPath, guarded, conditional, createOnly bool) []int {
	set := map[int]bool{
		http.StatusBadRequest: true, http.StatusNotAcceptable: true,
		http.StatusUnprocessableEntity: true, http.StatusInternalServerError: true,
	}
	if guarded {
		set[http.StatusPreconditionFailed] = true
		set[http.StatusPreconditionRequired] = true
	}
	if conditional {
		set[http.StatusNotModified] = true
		set[http.StatusPreconditionFailed] = true
	}
	if createOnly {
		set[http.StatusPreconditionFailed] = true
	}
	if hasBody {
		set[http.StatusRequestTimeout] = true
		set[http.StatusRequestEntityTooLarge] = true
		set[http.StatusUnsupportedMediaType] = true
	}
	if hasPath || resolvesProfile(class) {
		set[http.StatusNotFound] = true
	}
	if serviceBacked {
		set[http.StatusServiceUnavailable] = true
	}
	if class != ClassPublic {
		set[http.StatusUnauthorized] = true
		set[http.StatusTooManyRequests] = true
		set[http.StatusServiceUnavailable] = true
		if demoRestricted || resolvesProfile(class) {
			set[http.StatusForbidden] = true
		}
	}
	out := make([]int, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sortInts(out)
	return out
}

func sortInts(s []int) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func appendUnique(s []int, v int) []int {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// declaresBody reports whether the input type carries a structured or raw
// body, the way Huma decides it.
func declaresBody(t reflect.Type) bool {
	if t == nil || t.Kind() != reflect.Struct {
		return false
	}
	_, body := t.FieldByName("Body")
	_, raw := t.FieldByName("RawBody")
	return body || raw
}

// registerAll is the one list of domain registrations. The listener and the
// generator both call it, so the served router and the committed artifact
// describe the same operations.
func registerAll(reg *Registry) {
	// Alphabetical by domain file; registration order is deterministic.
	registerAccount(reg)
	registerAdminUsers(reg)
	registerPreferences(reg)
	registerProfileSections(reg)
	registerProfiles(reg)
	registerProgress(reg)
	registerSettings(reg)
	registerSystem(reg)
	registerOpenAPIDocument(reg)
}

// DeclaredOperations lists what the domain registrations declare, without a
// router, database or network: the same registrations GenerateOpenAPI
// documents. The migration ledger's reconcile test reads the Guarded set
// from it.
func DeclaredOperations() []Declared {
	reg := &Registry{api: huma.NewAPI(humaConfig(), noopAdapter{})}
	registerAll(reg)
	return reg.Declared()
}

// GenerateOpenAPI renders the contract document from the registries alone:
// no database, network, credentials, optional providers or environmental
// branching reach it, and nothing in it varies between builds (no timestamps,
// hostnames, local paths, examples or server URLs). The output is
// canonical JSON: sorted object keys, two-space indentation, one trailing
// newline. cmd/apiv2-openapi writes it to contracts/api/v2/openapi.json.
func GenerateOpenAPI() ([]byte, error) {
	api := huma.NewAPI(humaConfig(), noopAdapter{})
	reg := &Registry{api: api}
	registerAll(reg)
	raw, err := api.OpenAPI().MarshalJSON()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// noopAdapter lets the generator register operations without a router: the
// document is a property of the registrations, not of the transport.
type noopAdapter struct{}

func (noopAdapter) Handle(*huma.Operation, func(huma.Context))   {}
func (noopAdapter) ServeHTTP(http.ResponseWriter, *http.Request) {}

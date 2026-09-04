package apiv2

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
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
	// extExtensionBag marks the one legitimate use of additionalProperties:
	// a named bag whose keys are not fixed by this contract. The spec lint
	// refuses any other additionalProperties.
	extExtensionBag = "x-silo-extension-bag"
)

// securitySchemeBearer is the single security scheme. Session tokens and API
// keys both travel in `Authorization: Bearer`; the v1 gate decides which one
// it holds.
const securitySchemeBearer = "bearerAuth"

// profileHeader is the header every profile-resolving class reads.
const profileHeader = "X-Profile-Id"

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
	hasBody := declaresBody(input)
	hasPath := strings.Contains(op.Path, "{")
	for _, status := range ImpliedStatuses(op.Class, op.DemoRestricted, hasBody, hasPath) {
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
		op.Parameters = append(op.Parameters, profileHeaderParam(class))
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
		In:     "header",
		Schema: &huma.Schema{Type: "string"},
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

// resolvesProfile reports whether the class runs viewer access and so needs
// the profile header.
func resolvesProfile(class Class) bool {
	return class == ClassProfileScoped || class == ClassActingAdmin || class == ClassPermissionGated
}

// ImpliedStatuses lists the problem statuses an operation of the given shape
// documents, in ascending order. Shared by every operation: 400 (malformed
// request), 406 (negotiation), 422 (undeclared query parameter), 500. A body
// adds 413 and 415; a path parameter adds 404; every gated class adds 401, 429
// and 503 (a gate the wiring lacks fails closed); a class that resolves a
// profile or a demo-restricted mutation adds 403.
func ImpliedStatuses(class Class, demoRestricted, hasBody, hasPath bool) []int {
	set := map[int]bool{
		http.StatusBadRequest: true, http.StatusNotAcceptable: true,
		http.StatusUnprocessableEntity: true, http.StatusInternalServerError: true,
	}
	if hasBody {
		set[http.StatusRequestEntityTooLarge] = true
		set[http.StatusUnsupportedMediaType] = true
	}
	if hasPath {
		set[http.StatusNotFound] = true
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
	registerSystem(reg)
	registerOpenAPIDocument(reg)
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

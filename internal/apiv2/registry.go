package apiv2

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/policy"
)

// Class is the authorization class an operation declares. The class selects
// which of the existing gates (internal/api/middleware) run in front of the
// handler; a handler never inspects credentials itself.
type Class string

const (
	// ClassPublic runs no gate; usable before login.
	ClassPublic Class = "public"
	// ClassAuthenticated requires a valid bearer token or API key with a
	// live session, then applies the rate limiter and the demo guard.
	ClassAuthenticated Class = "authenticated"
	// ClassProfileScoped is ClassAuthenticated plus a declared X-Profile-Id
	// that resolves, through viewer access, to a profile of the caller's
	// account that is verified (unlocked) for this request.
	ClassProfileScoped Class = "profile_scoped"
	// ClassActingAdmin is ClassAuthenticated plus the admin role; a declared
	// X-Profile-Id must be the account's primary profile, and an absent one
	// is accepted, exactly as v1's RequireActingAdmin decides.
	ClassActingAdmin Class = "acting_admin"
	// ClassPermissionGated is ClassAuthenticated plus one named permission
	// (Operation.Permission) decided by the policy engine.
	ClassPermissionGated Class = "permission_gated"
)

var classes = map[Class]bool{
	ClassPublic: true, ClassAuthenticated: true, ClassProfileScoped: true,
	ClassActingAdmin: true, ClassPermissionGated: true,
}

// Metadata keys the registry stores on the Huma operation so the gate can
// read the declaration at request time.
const (
	metaClass           = "silo.class"
	metaPermission      = "silo.permission"
	metaDemoRestricted  = "silo.demo_restricted"
	metaProfileOptional = "silo.profile_optional"
	metaMaxBodyBytes    = "silo.max_body_bytes"
	metaGuarded         = "silo.guarded"
	metaConditional     = "silo.conditional"
	metaCreateOnly      = "silo.create_only"
)

// knownPermissions is the policy permission set an operation may name. It is
// the policy engine's own vocabulary: a name outside it decides nothing.
var knownPermissions = map[string]bool{
	policy.PermissionActingAdmin:      true,
	policy.PermissionMarkerEdit:       true,
	policy.PermissionMetadataCuration: true,
}

// Operation is a Huma operation plus Silo's declarations.
type Operation struct {
	huma.Operation
	// Class is required.
	Class Class
	// Permission names the policy permission a ClassPermissionGated
	// operation needs; empty otherwise. It must be one of the policy
	// engine's permissions. metadata_curation is item-scoped: its gate reads
	// the {id} path parameter, so an operation naming it must declare one.
	Permission string
	// DemoRestricted marks a mutation that demo mode refuses to non-admins.
	// It is meaningless on ClassPublic, where no gate runs.
	DemoRestricted bool
	// ProfileOptional, on a ClassProfileScoped operation, accepts an absent
	// X-Profile-Id: viewer access still resolves and judges a present header,
	// but the profile-required gate is not run, as on the v1 routes whose
	// group runs viewer access without RequireProfile (profiles, devices).
	// The handler then decides what an account-scoped caller may do.
	ProfileOptional bool
	// MaxBodyBytes lowers this operation's structured-body cap; 0 takes
	// MaxJSONBodyBytes. Set it here rather than on the embedded Huma
	// operation: Register applies the framework's off-by-one convention and
	// records the declared limit so the 413 names it.
	MaxBodyBytes int64
	// ServiceBacked marks a handler that depends on a wired service and so
	// answers 503 dependency_unavailable when the wiring lacks it. Every
	// gated class already implies 503 (a missing gate fails closed); the
	// flag matters on ClassPublic, where only the handler can produce it.
	// The document and discovery operations answer from the build alone
	// and leave it unset.
	ServiceBacked bool
	// Guarded marks a PUT, PATCH or DELETE protected by optimistic
	// concurrency (docs/architecture/api-contract.md, "Optimistic
	// concurrency"): the input binds `header:"If-Match"` as a string, the
	// output carries `header:"ETag"`, and the handler evaluates the
	// precondition with EvaluateIfMatch after loading the resource. The
	// document gains 412 and 428, a required If-Match parameter, and the
	// ETag header on every success response.
	Guarded bool
	// Conditional marks a GET or HEAD that honors If-None-Match: the input
	// binds `header:"If-None-Match"`, the output carries `header:"ETag"` and
	// an int Status so the handler can answer 304 through NotModified.
	Conditional bool
	// CreateOnly marks a PUT with a client-selected resource ID that honors
	// `If-None-Match: *` as a create-only request: the input binds
	// `header:"If-None-Match"` as a string, the output carries
	// `header:"ETag"`, and the handler evaluates EvaluateCreateOnly against
	// the stored representation (nil when none). The document gains 412, an
	// optional If-None-Match parameter, and the ETag header on every success
	// response. Exclusive with Guarded: a guarded PUT already requires
	// If-Match and has no create path.
	CreateOnly bool
}

// Registry is the deterministic registration surface. Domain files call
// Register on it at router build; nothing registers later.
type Registry struct {
	api  huma.API
	deps Dependencies

	mu  sync.Mutex
	ops []Declared
}

// Declared is what the registry recorded for one operation; the runtime
// reconcile test compares it with the routes the router really serves.
type Declared struct {
	Method      string
	Path        string
	OperationID string
	Class       Class
	Guarded     bool
	Conditional bool
	CreateOnly  bool
}

// Declared lists every registered operation, sorted by method then path.
func (reg *Registry) Declared() []Declared {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	out := append([]Declared(nil), reg.ops...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// Register adds one operation. It panics on a declaration the contract
// forbids: registration runs at startup, where a panic is a build failure,
// not a request failure.
func Register[I, O any](reg *Registry, op Operation, handler func(context.Context, *I) (*O, error)) {
	if err := checkOperation(op); err != nil {
		panic(fmt.Sprintf("apiv2: %s: %v", op.OperationID, err))
	}
	var in I
	if err := checkInput(reflect.TypeOf(in)); err != nil {
		panic(fmt.Sprintf("apiv2: %s: %v", op.OperationID, err))
	}
	var out O
	if err := checkEnums(reflect.TypeOf(out)); err != nil {
		panic(fmt.Sprintf("apiv2: %s: %v", op.OperationID, err))
	}
	if err := checkConcurrencyShape(op, reflect.TypeOf(in), reflect.TypeOf(out)); err != nil {
		panic(fmt.Sprintf("apiv2: %s: %v", op.OperationID, err))
	}
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[metaClass] = op.Class
	op.Metadata[metaPermission] = op.Permission
	op.Metadata[metaDemoRestricted] = op.DemoRestricted
	op.Metadata[metaProfileOptional] = op.ProfileOptional
	op.Metadata[metaGuarded] = op.Guarded
	op.Metadata[metaConditional] = op.Conditional
	op.Metadata[metaCreateOnly] = op.CreateOnly
	documentDeclaration(&op, reflect.TypeOf(in))
	limit := op.MaxBodyBytes
	if limit == 0 {
		limit = MaxJSONBodyBytes
	}
	op.Metadata[metaMaxBodyBytes] = limit
	// Huma reads through a LimitReader and reports "too large" when the count
	// reaches the limit, so a body of exactly N bytes needs N+1. Every limit
	// goes through this translation, the default and an override alike.
	op.Operation.MaxBodyBytes = limit + 1
	if op.BodyReadTimeout == 0 {
		op.BodyReadTimeout = BodyReadTimeout
		if reg.deps.bodyReadTimeout > 0 {
			op.BodyReadTimeout = reg.deps.bodyReadTimeout
		}
	}
	reg.mu.Lock()
	reg.ops = append(reg.ops, Declared{Method: op.Method, Path: op.Path, OperationID: op.OperationID, Class: op.Class,
		Guarded: op.Guarded, Conditional: op.Conditional, CreateOnly: op.CreateOnly})
	reg.mu.Unlock()
	huma.Register(reg.api, op.Operation, handler)
	documentConcurrencyResponses(reg.api.OpenAPI(), op)
}

func checkOperation(op Operation) error {
	if op.OperationID == "" || !isLowerCamel(op.OperationID) {
		return fmt.Errorf("operation id %q must be lowerCamelCase", op.OperationID)
	}
	if len(op.Tags) != 1 || op.Tags[0] != strings.ToLower(op.Tags[0]) {
		return fmt.Errorf("exactly one lowercase domain tag is required, got %v", op.Tags)
	}
	if !strings.HasPrefix(op.Path, Prefix+"/") {
		return fmt.Errorf("path %q must start with %s/", op.Path, Prefix)
	}
	if strings.HasSuffix(op.Path, "/") {
		return fmt.Errorf("path %q must not end with a slash", op.Path)
	}
	if !classes[op.Class] {
		return fmt.Errorf("unknown operation class %q", op.Class)
	}
	if (op.Class == ClassPermissionGated) != (op.Permission != "") {
		return fmt.Errorf("permission %q must be set exactly when the class is %s", op.Permission, ClassPermissionGated)
	}
	if op.Permission != "" && !knownPermissions[op.Permission] {
		return fmt.Errorf("permission %q is not a policy permission; a name the policy engine does not know decides nothing", op.Permission)
	}
	if op.Permission == policy.PermissionMetadataCuration && !declaresPathParam(op.Path, "id") {
		return fmt.Errorf("permission %s is item scoped and its gate reads the {id} path parameter, "+
			"but path %q declares none", policy.PermissionMetadataCuration, op.Path)
	}
	if op.DemoRestricted && op.Class == ClassPublic {
		return fmt.Errorf("demo restriction is inert on class %s: no gate runs in front of a public operation", ClassPublic)
	}
	if op.ProfileOptional && op.Class != ClassProfileScoped {
		return fmt.Errorf("profile optional is only meaningful on class %s", ClassProfileScoped)
	}
	if op.MaxBodyBytes < 0 {
		return fmt.Errorf("max body bytes %d must not be negative", op.MaxBodyBytes)
	}
	if op.Operation.MaxBodyBytes != 0 {
		return fmt.Errorf("set apiv2.Operation.MaxBodyBytes, not the embedded Huma field: Register owns the limit translation")
	}
	if op.Guarded && op.Method != http.MethodPut && op.Method != http.MethodPatch && op.Method != http.MethodDelete {
		return fmt.Errorf("guarded is for PUT, PATCH and DELETE; %s has no If-Match precondition to guard", op.Method)
	}
	if op.Conditional && op.Method != http.MethodGet && op.Method != http.MethodHead {
		return fmt.Errorf("conditional is for GET and HEAD; %s has no If-None-Match read to answer 304", op.Method)
	}
	if op.CreateOnly && op.Method != http.MethodPut {
		return fmt.Errorf("create-only is for PUT with a client-selected id; %s has no If-None-Match: * create to refuse", op.Method)
	}
	if op.CreateOnly && op.Guarded {
		return fmt.Errorf("create-only and guarded are exclusive: a guarded PUT requires If-Match and has no create path")
	}
	return nil
}

// checkConcurrencyShape requires the transport fields a guarded,
// conditional or create-only declaration relies on: the precondition header
// as a string on the input, ETag as a string on the output, and for a
// conditional read an int Status so the handler can answer 304.
func checkConcurrencyShape(op Operation, in, out reflect.Type) error {
	if op.CreateOnly {
		if !declaresHeaderString(in, ifNoneMatchField) {
			return fmt.Errorf("create-only: input must declare a string field with `header:\"%s\"`", ifNoneMatchField)
		}
		if !declaresHeaderString(out, etagField) {
			return fmt.Errorf("create-only: output must declare a string field with `header:\"%s\"`", etagField)
		}
	}
	if op.Guarded {
		if !declaresHeaderString(in, ifMatchField) {
			return fmt.Errorf("guarded: input must declare a string field with `header:\"%s\"`", ifMatchField)
		}
		if !declaresHeaderString(out, etagField) {
			return fmt.Errorf("guarded: output must declare a string field with `header:\"%s\"`", etagField)
		}
	}
	if op.Conditional {
		if !declaresHeaderString(in, ifNoneMatchField) {
			return fmt.Errorf("conditional: input must declare a string field with `header:\"%s\"`", ifNoneMatchField)
		}
		if !declaresHeaderString(out, etagField) {
			return fmt.Errorf("conditional: output must declare a string field with `header:\"%s\"`", etagField)
		}
		if f, ok := out.FieldByName("Status"); !ok || f.Type.Kind() != reflect.Int || len(f.Index) != 1 {
			return fmt.Errorf("conditional: output must declare a direct int Status field so the handler can answer 304")
		}
	}
	return nil
}

// declaresHeaderString reports whether struct type t (or an embedded struct)
// has a string field bound to the named header, compared case-insensitively
// as HTTP header names are.
func declaresHeaderString(t reflect.Type, name string) bool {
	if t == nil || t.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct && declaresHeaderString(f.Type, name) {
			return true
		}
		if strings.EqualFold(f.Tag.Get("header"), name) && f.Type.Kind() == reflect.String {
			return true
		}
	}
	return false
}

// declaresPathParam reports whether a route path declares {name} as one whole
// segment.
func declaresPathParam(path, name string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "{"+name+"}" {
			return true
		}
	}
	return false
}

func isLowerCamel(s string) bool {
	if s == "" || !unicode.IsLower(rune(s[0])) {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// checkInput enforces the query-parameter rules that Huma leaves to the
// caller: a slice query parameter carries explicit `explode` so it is read
// from repeated keys, never split on commas; and request enums are lowercase.
func checkInput(t reflect.Type) error {
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			if err := checkInput(f.Type); err != nil {
				return err
			}
			continue
		}
		if q := f.Tag.Get("query"); q != "" && f.Type.Kind() == reflect.Slice {
			parts := strings.Split(q, ",")
			explode := false
			for _, p := range parts[1:] {
				if p == "explode" {
					explode = true
				}
			}
			if !explode {
				return fmt.Errorf("slice query parameter %q must declare explode (repeated keys)", parts[0])
			}
		}
	}
	return checkEnums(t)
}

// checkEnums walks a transport type and requires every `enum` tag value to be
// lowercase (docs/architecture/api-contract.md, "Wire data representation").
func checkEnums(t reflect.Type) error {
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type) error
	walk = func(t reflect.Type) error {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || seen[t] {
			return nil
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if e := f.Tag.Get("enum"); e != "" && e != strings.ToLower(e) {
				return fmt.Errorf("enum on %s.%s must be lowercase: %q", t.Name(), f.Name, e)
			}
			if err := walk(f.Type); err != nil {
				return err
			}
		}
		return nil
	}
	if t == nil {
		return nil
	}
	return walk(t)
}

// methodOrder is the order the Allow header lists methods in: safe first,
// then mutations, so the field reads the same for every path.
var methodOrder = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions,
}

// AllowedMethods lists the methods declared for the operation path that
// matches a concrete request path, in methodOrder. It is the source of the
// Allow header on a 405.
func (reg *Registry) AllowedMethods(path string) []string {
	found := map[string]bool{}
	for _, d := range reg.Declared() {
		if pathMatches(d.Path, path) {
			found[d.Method] = true
		}
	}
	out := make([]string, 0, len(found))
	for _, m := range methodOrder {
		if found[m] {
			out = append(out, m)
			delete(found, m)
		}
	}
	rest := make([]string, 0, len(found))
	for m := range found {
		rest = append(rest, m)
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// pathMatches reports whether a concrete request path is an instance of a
// declared operation path. A {name} segment matches one non-empty segment;
// v2 declares no wildcards, so nothing else is variable.
func pathMatches(pattern, path string) bool {
	ps := strings.Split(strings.TrimSuffix(pattern, "/"), "/")
	rs := strings.Split(strings.TrimSuffix(path, "/"), "/")
	if len(ps) != len(rs) {
		return false
	}
	for i, seg := range ps {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			if rs[i] == "" {
				return false
			}
			continue
		}
		if seg != rs[i] {
			return false
		}
	}
	return true
}

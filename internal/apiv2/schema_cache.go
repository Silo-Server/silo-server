package apiv2

import (
	"encoding"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

const schemaRefPrefix = "#/components/schemas/"

var (
	timeType            = reflect.TypeFor[time.Time]()
	schemaProviderType  = reflect.TypeFor[huma.SchemaProvider]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// baseSchemaSnapshot holds the read-only component schemas produced once by
// registerAll. Runtime listeners reuse these schemas through
// runtimeSchemaRegistry instead of running Huma's reflection and message
// precomputation on every NewHandler call.
type baseSchemaSnapshot struct {
	schemas map[string]*huma.Schema
	types   map[string]reflect.Type
	seen    map[reflect.Type]string // dereferenced struct type -> component name
}

var sharedRuntimeSchemas = sync.OnceValue(func() *baseSchemaSnapshot {
	api := huma.NewAPI(humaConfig(), noopAdapter{})
	reg := &Registry{api: api}
	registerAll(reg)

	src := api.OpenAPI().Components.Schemas
	srcMap := src.Map()
	snap := &baseSchemaSnapshot{
		schemas: make(map[string]*huma.Schema, len(srcMap)),
		types:   make(map[string]reflect.Type, len(srcMap)),
		seen:    make(map[reflect.Type]string, len(srcMap)),
	}
	for name, schema := range srcMap {
		snap.schemas[name] = schema
		if typ := src.TypeFromRef(schemaRefPrefix + name); typ != nil {
			snap.types[name] = typ
			snap.seen[typ] = name
		}
	}
	return snap
})

// runtimeSchemaRegistry is a huma.Registry backed by a read-only
// baseSchemaSnapshot. Struct types already in the snapshot resolve without
// reflection; new types (such as testRegister probes) and direct Map writes
// stay isolated in a per-instance overlay.
type runtimeSchemaRegistry struct {
	base      *baseSchemaSnapshot
	schemas   map[string]*huma.Schema
	types     map[string]reflect.Type
	seen      map[reflect.Type]bool
	seenForms map[string]bool
	aliases   map[reflect.Type]reflect.Type
	copied    bool
}

var _ huma.Registry = (*runtimeSchemaRegistry)(nil)

func newRuntimeSchemaRegistry() *runtimeSchemaRegistry {
	return &runtimeSchemaRegistry{base: sharedRuntimeSchemas()}
}

func runtimeHumaConfig() huma.Config {
	cfg := humaConfig()
	cfg.Components.Schemas = newRuntimeSchemaRegistry()
	return cfg
}

func (r *runtimeSchemaRegistry) claimBaseFormSchema(name string) bool {
	if _, ok := r.base.schemas[name]; !ok {
		return false
	}
	if _, isStruct := r.base.types[name]; isStruct {
		return false
	}
	if r.seenForms == nil {
		r.seenForms = make(map[string]bool)
	}
	if r.seenForms[name] {
		return false
	}
	r.seenForms[name] = true
	return true
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func schemaGetsRef(t reflect.Type) bool {
	if t.Kind() != reflect.Struct || t == timeType {
		return false
	}
	ptrT := reflect.PointerTo(t)
	if t.Implements(schemaProviderType) || ptrT.Implements(schemaProviderType) {
		return false
	}
	if t.Implements(textUnmarshalerType) || ptrT.Implements(textUnmarshalerType) {
		return false
	}
	return true
}

func (r *runtimeSchemaRegistry) Schema(t reflect.Type, allowRef bool, hint string) *huma.Schema {
	origType := t
	t = derefType(t)
	if t.Kind() == reflect.Array || t.Kind() == reflect.Slice {
		origType = t
	}
	if alias, ok := r.aliases[t]; ok {
		return r.Schema(alias, allowRef, hint)
	}
	if !schemaGetsRef(t) {
		return huma.SchemaFromType(r, origType)
	}

	name := nativeSchemaName(origType, hint)
	if s, ok := r.lookupOverlay(name, t, allowRef); ok {
		return s
	}
	if s, ok := r.lookupBase(name, t, origType, allowRef); ok {
		return s
	}

	// Unknown struct type (e.g. a probe registered by testRegister): store
	// in this instance's overlay without touching the shared snapshot.
	r.ensureOverlay()
	r.schemas[name] = &huma.Schema{}
	r.types[name] = t
	r.seen[t] = true
	s := huma.SchemaFromType(r, origType)
	r.schemas[name] = s
	if allowRef {
		return &huma.Schema{Ref: schemaRefPrefix + name}
	}
	return s
}

func (r *runtimeSchemaRegistry) lookupOverlay(name string, t reflect.Type, allowRef bool) (*huma.Schema, bool) {
	s, ok := r.schemas[name]
	if !ok {
		return nil, false
	}
	if !r.seen[t] && r.base.types[name] != t {
		existing := r.types[name]
		if existing == nil {
			existing = r.base.types[name]
		}
		panic(fmt.Errorf("duplicate name: %s, new type: %s, existing type: %s", name, t, existing))
	}
	if allowRef {
		return &huma.Schema{Ref: schemaRefPrefix + name}, true
	}
	return s, true
}

func (r *runtimeSchemaRegistry) lookupBase(name string, t, origType reflect.Type, allowRef bool) (*huma.Schema, bool) {
	if _, ok := r.base.schemas[name]; !ok {
		return nil, false
	}
	if r.base.types[name] != t {
		panic(fmt.Errorf("duplicate name: %s, new type: %s, existing type: %s", name, t, r.base.types[name]))
	}
	if allowRef {
		return &huma.Schema{Ref: schemaRefPrefix + name}, true
	}
	// Return a fresh top-level schema so callers (such as Huma's
	// PrecomputeMessages or test assertions) never mutate the shared
	// snapshot; nested struct fields still resolve to base $refs.
	return huma.SchemaFromType(r, origType), true
}

func (r *runtimeSchemaRegistry) ensureOverlay() {
	if r.schemas == nil {
		r.schemas = make(map[string]*huma.Schema)
	}
	if r.types == nil {
		r.types = make(map[string]reflect.Type)
	}
	if r.seen == nil {
		r.seen = make(map[reflect.Type]bool)
	}
}

func (r *runtimeSchemaRegistry) SchemaFromRef(ref string) *huma.Schema {
	name, ok := strings.CutPrefix(ref, schemaRefPrefix)
	if !ok {
		return nil
	}
	if r.schemas != nil {
		if s, found := r.schemas[name]; found {
			return s
		}
	}
	return r.base.schemas[name]
}

func (r *runtimeSchemaRegistry) TypeFromRef(ref string) reflect.Type {
	name, ok := strings.CutPrefix(ref, schemaRefPrefix)
	if !ok {
		return nil
	}
	if r.types != nil {
		if t, found := r.types[name]; found {
			return t
		}
	}
	return r.base.types[name]
}

func (r *runtimeSchemaRegistry) Map() map[string]*huma.Schema {
	if !r.copied {
		merged := make(map[string]*huma.Schema, len(r.base.schemas)+len(r.schemas))
		for name, s := range r.base.schemas {
			if s == nil {
				merged[name] = nil
				continue
			}
			cp := *s
			cp.Required = slices.Clone(s.Required)
			merged[name] = &cp
		}
		maps.Copy(merged, r.schemas)
		r.schemas = merged
		r.copied = true
	}
	return r.schemas
}

func (r *runtimeSchemaRegistry) RegisterTypeAlias(t reflect.Type, alias reflect.Type) {
	if r.aliases == nil {
		r.aliases = make(map[reflect.Type]reflect.Type)
	}
	r.aliases[t] = alias
}

func (r *runtimeSchemaRegistry) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.Map())
}

func (r *runtimeSchemaRegistry) MarshalYAML() (any, error) {
	return r.Map(), nil
}

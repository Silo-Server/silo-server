package apiv2

import (
	"encoding"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

const schemaRefPrefix = "#/components/schemas/"

var errReadOnlySchemaRegistry = errors.New("apiv2: runtimeSchemaRegistry is read-only; use GenerateOpenAPI() or humaConfig()")

var (
	timeType            = reflect.TypeFor[time.Time]()
	schemaProviderType  = reflect.TypeFor[huma.SchemaProvider]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// baseSchemaSnapshot holds the read-only component schemas and declared
// operations produced once by registerAll. Runtime listeners without
// testRegister reuse these schemas through runtimeSchemaRegistry instead of
// running Huma's reflection and message precomputation on every NewHandler
// call.
type baseSchemaSnapshot struct {
	schemas  map[string]*huma.Schema
	types    map[string]reflect.Type
	declared []Declared
}

var sharedRuntimeSchemas = sync.OnceValue(func() *baseSchemaSnapshot {
	api := huma.NewAPI(humaConfig(), noopAdapter{})
	reg := &Registry{api: api}
	registerAll(reg)

	src := api.OpenAPI().Components.Schemas
	srcMap := src.Map()
	snap := &baseSchemaSnapshot{
		schemas:  make(map[string]*huma.Schema, len(srcMap)),
		types:    make(map[string]reflect.Type, len(srcMap)),
		declared: reg.Declared(),
	}
	for name, schema := range srcMap {
		snap.schemas[name] = schema
		if typ := src.TypeFromRef(schemaRefPrefix + name); typ != nil {
			snap.types[name] = typ
		}
	}
	return snap
})

// runtimeSchemaRegistry is a read-only huma.Registry backed by
// baseSchemaSnapshot. Production and standard test routers (deps.testRegister
// == nil) use it to resolve base struct types to $ref without re-running Huma
// reflection or message precomputation; callers that mutate or extend schemas
// (GenerateOpenAPI, testRegister) use humaConfig() instead.
type runtimeSchemaRegistry struct {
	base      *baseSchemaSnapshot
	seenForms map[string]bool
	aliases   map[reflect.Type]reflect.Type
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
	if _, ok := r.base.schemas[name]; !ok {
		panic(fmt.Errorf("apiv2: runtimeSchemaRegistry cannot register new schema %s (%s); use humaConfig()", name, t))
	}
	if r.base.types[name] != t {
		panic(fmt.Errorf("duplicate name: %s, new type: %s, existing type: %s", name, t, r.base.types[name]))
	}
	if !allowRef {
		panic(fmt.Errorf("apiv2: runtimeSchemaRegistry is read-only and does not support inline (allowRef=false) lookup of %s; use humaConfig()", name))
	}
	return &huma.Schema{Ref: schemaRefPrefix + name}
}

func (r *runtimeSchemaRegistry) SchemaFromRef(ref string) *huma.Schema {
	name, ok := strings.CutPrefix(ref, schemaRefPrefix)
	if !ok {
		return nil
	}
	return r.base.schemas[name]
}

func (r *runtimeSchemaRegistry) TypeFromRef(ref string) reflect.Type {
	name, ok := strings.CutPrefix(ref, schemaRefPrefix)
	if !ok {
		return nil
	}
	return r.base.types[name]
}

func (r *runtimeSchemaRegistry) Map() map[string]*huma.Schema {
	panic("apiv2: runtimeSchemaRegistry is read-only; use humaConfig() to inspect or mutate the schema map")
}

func (r *runtimeSchemaRegistry) RegisterTypeAlias(t reflect.Type, alias reflect.Type) {
	if r.aliases == nil {
		r.aliases = make(map[reflect.Type]reflect.Type)
	}
	r.aliases[t] = alias
}

func (r *runtimeSchemaRegistry) MarshalJSON() ([]byte, error) {
	return nil, errReadOnlySchemaRegistry
}

func (r *runtimeSchemaRegistry) MarshalYAML() (any, error) {
	return nil, errReadOnlySchemaRegistry
}

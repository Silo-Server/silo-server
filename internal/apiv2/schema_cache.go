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

// baseSchemaSnapshot holds the read-only component schemas, declared
// operations, and public capability schema names produced once by registerAll.
// Runtime listeners reuse these schemas through runtimeSchemaRegistry instead
// of running Huma's reflection and message precomputation on every NewHandler
// call.
type baseSchemaSnapshot struct {
	schemas                 map[string]*huma.Schema
	types                   map[string]reflect.Type
	declared                []Declared
	publicCapabilitySchemas map[string]bool
}

var sharedRuntimeSchemas = sync.OnceValue(func() *baseSchemaSnapshot {
	api := huma.NewAPI(humaConfig(), noopAdapter{})
	reg := &Registry{api: api}
	registerAll(reg)

	src := api.OpenAPI().Components.Schemas
	srcMap := src.Map()
	snap := &baseSchemaSnapshot{
		schemas:                 make(map[string]*huma.Schema, len(srcMap)),
		types:                   make(map[string]reflect.Type, len(srcMap)),
		declared:                reg.Declared(),
		publicCapabilitySchemas: maps.Clone(reg.publicCapabilitySchemas),
	}
	for name, schema := range srcMap {
		snap.schemas[name] = schema
		if typ := src.TypeFromRef(schemaRefPrefix + name); typ != nil {
			snap.types[name] = typ
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
	if s, ok := r.lookupBase(name, t, allowRef); ok {
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

func (r *runtimeSchemaRegistry) lookupBase(name string, t reflect.Type, allowRef bool) (*huma.Schema, bool) {
	baseSchema, ok := r.base.schemas[name]
	if !ok {
		return nil, false
	}
	if r.base.types[name] != t {
		panic(fmt.Errorf("duplicate name: %s, new type: %s, existing type: %s", name, t, r.base.types[name]))
	}
	if allowRef {
		return &huma.Schema{Ref: schemaRefPrefix + name}, true
	}
	// Return an isolated deep clone (with fresh precomputed validation maps) so
	// callers see post-registration refinements from baseSchemaSnapshot (such as
	// documentCapabilityScope) without mutating or racing on the shared snapshot.
	seen := make(map[*huma.Schema]*huma.Schema)
	cp := cloneSchema(baseSchema, seen)
	precomputeCloned(cp, make(map[*huma.Schema]bool), make(map[*huma.Schema]bool))
	return cp, true
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
		seen := make(map[*huma.Schema]*huma.Schema, len(r.base.schemas))
		merged := make(map[string]*huma.Schema, len(r.base.schemas)+len(r.schemas))
		for name, s := range r.base.schemas {
			merged[name] = cloneSchema(s, seen)
		}
		precomputed := make(map[*huma.Schema]bool, len(merged))
		walked := make(map[*huma.Schema]bool, len(merged))
		for _, s := range merged {
			precomputeCloned(s, precomputed, walked)
		}
		maps.Copy(merged, r.schemas)
		r.schemas = merged
		r.copied = true
	}
	return r.schemas
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// cloneSchema builds a fresh huma.Schema by copying exported fields explicitly
// rather than shallow-copying the struct, so Huma's unexported precomputed maps
// (msgRequired, msgDependentRequired, requiredMap, propertyNames) start nil and
// are populated by precomputeCloned without aliasing sharedRuntimeSchemas.
func cloneSchema(s *huma.Schema, seen map[*huma.Schema]*huma.Schema) *huma.Schema {
	if s == nil {
		return nil
	}
	if existing, ok := seen[s]; ok {
		return existing
	}
	cp := &huma.Schema{
		Type:               s.Type,
		Nullable:           s.Nullable,
		Title:              s.Title,
		Description:        s.Description,
		Ref:                s.Ref,
		Format:             s.Format,
		ContentEncoding:    s.ContentEncoding,
		Default:            cloneValue(s.Default),
		Examples:           cloneAnySlice(s.Examples),
		Enum:               cloneAnySlice(s.Enum),
		Minimum:            clonePtr(s.Minimum),
		ExclusiveMinimum:   clonePtr(s.ExclusiveMinimum),
		Maximum:            clonePtr(s.Maximum),
		ExclusiveMaximum:   clonePtr(s.ExclusiveMaximum),
		MultipleOf:         clonePtr(s.MultipleOf),
		MinLength:          clonePtr(s.MinLength),
		MaxLength:          clonePtr(s.MaxLength),
		Pattern:            s.Pattern,
		PatternDescription: s.PatternDescription,
		MinItems:           clonePtr(s.MinItems),
		MaxItems:           clonePtr(s.MaxItems),
		UniqueItems:        s.UniqueItems,
		Required:           slices.Clone(s.Required),
		MinProperties:      clonePtr(s.MinProperties),
		MaxProperties:      clonePtr(s.MaxProperties),
		ReadOnly:           s.ReadOnly,
		WriteOnly:          s.WriteOnly,
		Deprecated:         s.Deprecated,
		Extensions:         cloneAnyMap(s.Extensions),
		DependentRequired:  cloneStringSliceMap(s.DependentRequired),
		Discriminator:      cloneDiscriminator(s.Discriminator),
	}
	seen[s] = cp
	cp.Items = cloneSchema(s.Items, seen)
	cp.Not = cloneSchema(s.Not, seen)
	if addSchema, ok := s.AdditionalProperties.(*huma.Schema); ok {
		cp.AdditionalProperties = cloneSchema(addSchema, seen)
	} else {
		cp.AdditionalProperties = cloneValue(s.AdditionalProperties)
	}
	cp.Properties = cloneSchemaMap(s.Properties, seen)
	cp.OneOf = cloneSchemaSlice(s.OneOf, seen)
	cp.AnyOf = cloneSchemaSlice(s.AnyOf, seen)
	cp.AllOf = cloneSchemaSlice(s.AllOf, seen)
	return cp
}

func precomputeCloned(s *huma.Schema, precomputed, walked map[*huma.Schema]bool) {
	if s == nil || precomputed[s] {
		return
	}
	precomputed[s] = true
	s.PrecomputeMessages()
	walkAdditionalProperties(s, precomputed, walked)
}

func walkAdditionalProperties(s *huma.Schema, precomputed, walked map[*huma.Schema]bool) {
	if s == nil || walked[s] {
		return
	}
	walked[s] = true
	if addSchema, ok := s.AdditionalProperties.(*huma.Schema); ok {
		precomputeCloned(addSchema, precomputed, walked)
	}
	walkAdditionalProperties(s.Items, precomputed, walked)
	for _, prop := range s.Properties {
		walkAdditionalProperties(prop, precomputed, walked)
	}
	for _, sub := range s.OneOf {
		walkAdditionalProperties(sub, precomputed, walked)
	}
	for _, sub := range s.AnyOf {
		walkAdditionalProperties(sub, precomputed, walked)
	}
	for _, sub := range s.AllOf {
		walkAdditionalProperties(sub, precomputed, walked)
	}
	walkAdditionalProperties(s.Not, precomputed, walked)
}

func cloneStringSliceMap(src map[string][]string) map[string][]string {
	if src == nil {
		return nil
	}
	dst := make(map[string][]string, len(src))
	for k, v := range src {
		dst[k] = slices.Clone(v)
	}
	return dst
}

func cloneDiscriminator(src *huma.Discriminator) *huma.Discriminator {
	if src == nil {
		return nil
	}
	cp := *src
	cp.Mapping = maps.Clone(src.Mapping)
	return &cp
}

func cloneAnySlice(src []any) []any {
	if src == nil {
		return nil
	}
	dst := make([]any, len(src))
	for i, v := range src {
		dst[i] = cloneValue(v)
	}
	return dst
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = cloneValue(v)
	}
	return dst
}

func cloneValue(v any) any {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case map[string]any:
		return cloneAnyMap(x)
	case []any:
		return cloneAnySlice(x)
	case []string:
		return slices.Clone(x)
	case map[string]string:
		return maps.Clone(x)
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice:
		return cloneReflectSlice(rv)
	case reflect.Map:
		return cloneReflectMap(rv)
	default:
		return v
	}
}

func cloneReflectSlice(rv reflect.Value) any {
	if rv.IsNil() {
		return rv.Interface()
	}
	cp := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
	for i := range rv.Len() {
		elem := cloneValue(rv.Index(i).Interface())
		if elem != nil {
			cp.Index(i).Set(reflect.ValueOf(elem))
		}
	}
	return cp.Interface()
}

func cloneReflectMap(rv reflect.Value) any {
	if rv.IsNil() {
		return rv.Interface()
	}
	cp := reflect.MakeMapWithSize(rv.Type(), rv.Len())
	iter := rv.MapRange()
	for iter.Next() {
		elem := cloneValue(iter.Value().Interface())
		if elem == nil {
			cp.SetMapIndex(iter.Key(), reflect.Zero(rv.Type().Elem()))
		} else {
			cp.SetMapIndex(iter.Key(), reflect.ValueOf(elem))
		}
	}
	return cp.Interface()
}

func cloneSchemaMap(src map[string]*huma.Schema, seen map[*huma.Schema]*huma.Schema) map[string]*huma.Schema {
	if src == nil {
		return nil
	}
	dst := make(map[string]*huma.Schema, len(src))
	for k, v := range src {
		dst[k] = cloneSchema(v, seen)
	}
	return dst
}

func cloneSchemaSlice(src []*huma.Schema, seen map[*huma.Schema]*huma.Schema) []*huma.Schema {
	if src == nil {
		return nil
	}
	dst := make([]*huma.Schema, len(src))
	for i, v := range src {
		dst[i] = cloneSchema(v, seen)
	}
	return dst
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

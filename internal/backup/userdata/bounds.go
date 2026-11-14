package userdata

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Default bounds of a decoded document's fields (CheckBounds). A field's
// `bound` tag overrides its default: a byte length for a string or raw
// JSON, an element count for a slice or map, or "-" for a field its type
// checks itself (a type's whole document inside an account document).
const (
	defaultStringBound = 1024
	defaultRawBound    = 64 << 10
	defaultSliceBound  = MaxEntries
	defaultMapBound    = 64
)

var (
	rawMessageType = reflect.TypeFor[json.RawMessage]()
	timeType       = reflect.TypeFor[time.Time]()
)

// CheckBounds walks a decoded document and refuses a string, raw JSON, slice,
// or map over its bound, and raw JSON that is not valid. Every importer's
// document passes through it (DecodeStrict), so a field added without a
// bound gets the default rather than none.
func CheckBounds(v any) error {
	if err := checkBounds(reflect.ValueOf(v), -1); err != nil {
		return err.error()
	}
	return nil
}

// boundsError is a refusal with the path to the refused value, which the
// walk builds only on its way back from a refusal: a valid document of
// millions of entries makes no path strings.
type boundsError struct {
	// path is reversed: innermost segment first.
	path []string
	msg  string
}

func (e *boundsError) at(segment string) *boundsError {
	e.path = append(e.path, segment)
	return e
}

func (e *boundsError) error() error {
	var b strings.Builder
	for i := len(e.path) - 1; i >= 0; i-- {
		b.WriteString(e.path[i])
	}
	return fmt.Errorf("%s %s", b.String(), e.msg)
}

func refuse(format string, args ...any) *boundsError {
	return &boundsError{msg: fmt.Sprintf(format, args...)}
}

func checkBounds(v reflect.Value, bound int) *boundsError {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return checkBounds(v.Elem(), bound)
	case reflect.String:
		if limit := pick(bound, defaultStringBound); v.Len() > limit {
			return refuse("holds %d bytes, more than %d", v.Len(), limit)
		}
	case reflect.Slice:
		if v.Type() == rawMessageType {
			if limit := pick(bound, defaultRawBound); v.Len() > limit {
				return refuse("holds %d bytes, more than %d", v.Len(), limit)
			}
			if v.Len() > 0 && !json.Valid(v.Bytes()) {
				return refuse("is not valid JSON")
			}
			return nil
		}
		if limit := pick(bound, defaultSliceBound); v.Len() > limit {
			return refuse("holds %d entries, more than %d", v.Len(), limit)
		}
		for i := range v.Len() {
			if err := checkBounds(v.Index(i), -1); err != nil {
				return err.at("[" + strconv.Itoa(i) + "]")
			}
		}
	case reflect.Map:
		if limit := pick(bound, defaultMapBound); v.Len() > limit {
			return refuse("holds %d entries, more than %d", v.Len(), limit)
		}
		iter := v.MapRange()
		for iter.Next() {
			if err := checkBounds(iter.Key(), -1); err != nil {
				return err.at(" (key)").at("." + fmt.Sprint(iter.Key().Interface()))
			}
			if err := checkBounds(iter.Value(), -1); err != nil {
				return err.at("." + fmt.Sprint(iter.Key().Interface()))
			}
		}
	case reflect.Struct:
		if v.Type() == timeType {
			return nil
		}
		fields, err := boundedFieldsOf(v.Type())
		if err != nil {
			return err
		}
		for _, f := range fields {
			if err := checkBounds(v.Field(f.index), f.bound); err != nil {
				return err.at("." + f.name)
			}
		}
	}
	return nil
}

// boundedField is one exported struct field the walk checks, with its
// bound (-1 for the default) and JSON name.
type boundedField struct {
	index int
	bound int
	name  string
}

// boundedFields caches each struct type's checked fields; reading their
// tags for every element of a large document is most of a walk's cost.
var boundedFields sync.Map // reflect.Type -> []boundedField

func boundedFieldsOf(t reflect.Type) ([]boundedField, *boundsError) {
	if cached, ok := boundedFields.Load(t); ok {
		if fields, ok := cached.([]boundedField); ok {
			return fields, nil
		}
	}
	var fields []boundedField
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("bound")
		if tag == "-" {
			continue
		}
		bound := -1
		if tag != "" {
			n, err := strconv.Atoi(tag)
			if err != nil {
				return nil, refuse("has an invalid bound tag %q", tag).at("." + f.Name)
			}
			bound = n
		}
		fields = append(fields, boundedField{index: i, bound: bound, name: jsonName(f)})
	}
	boundedFields.Store(t, fields)
	return fields, nil
}

func pick(bound, fallback int) int {
	if bound >= 0 {
		return bound
	}
	return fallback
}

func jsonName(f reflect.StructField) string {
	if name, _, _ := cutComma(f.Tag.Get("json")); name != "" && name != "-" {
		return name
	}
	return f.Name
}

func cutComma(s string) (string, string, bool) {
	for i := range len(s) {
		if s[i] == ',' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

package userdata

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
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
	return checkBounds(reflect.ValueOf(v), "", -1)
}

func checkBounds(v reflect.Value, path string, bound int) error {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return checkBounds(v.Elem(), path, bound)
	case reflect.String:
		if limit := pick(bound, defaultStringBound); v.Len() > limit {
			return fmt.Errorf("%s holds %d bytes, more than %d", path, v.Len(), limit)
		}
	case reflect.Slice:
		if v.Type() == rawMessageType {
			if limit := pick(bound, defaultRawBound); v.Len() > limit {
				return fmt.Errorf("%s holds %d bytes, more than %d", path, v.Len(), limit)
			}
			if v.Len() > 0 && !json.Valid(v.Bytes()) {
				return fmt.Errorf("%s is not valid JSON", path)
			}
			return nil
		}
		if limit := pick(bound, defaultSliceBound); v.Len() > limit {
			return fmt.Errorf("%s holds %d entries, more than %d", path, v.Len(), limit)
		}
		for i := range v.Len() {
			if err := checkBounds(v.Index(i), path+"["+strconv.Itoa(i)+"]", -1); err != nil {
				return err
			}
		}
	case reflect.Map:
		if limit := pick(bound, defaultMapBound); v.Len() > limit {
			return fmt.Errorf("%s holds %d entries, more than %d", path, v.Len(), limit)
		}
		iter := v.MapRange()
		for iter.Next() {
			key := path + "." + fmt.Sprint(iter.Key().Interface())
			if err := errorsJoin(checkBounds(iter.Key(), key+" (key)", -1), checkBounds(iter.Value(), key, -1)); err != nil {
				return err
			}
		}
	case reflect.Struct:
		if v.Type() == timeType {
			return nil
		}
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("bound")
			if tag == "-" {
				continue
			}
			fieldBound := -1
			if tag != "" {
				n, err := strconv.Atoi(tag)
				if err != nil {
					return fmt.Errorf("%s.%s has an invalid bound tag %q", path, f.Name, tag)
				}
				fieldBound = n
			}
			if err := checkBounds(v.Field(i), path+"."+jsonName(f), fieldBound); err != nil {
				return err
			}
		}
	}
	return nil
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

func errorsJoin(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

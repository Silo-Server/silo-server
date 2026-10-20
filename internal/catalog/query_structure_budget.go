package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// ErrQueryComplexity identifies structural admission failures, including a
// composition whose individually valid parts exceed one effective definition.
var ErrQueryComplexity = errors.New("query definition exceeds structural budget")

const (
	queryMaxGroups           = 16
	queryMaxRules            = 128
	queryMaxLibraries        = 256
	queryMaxValueBytes       = 64 << 10
	queryLibraryIDsKey       = "library_ids"
	queryLegacyLibraryIDsKey = "filter_library_ids"
	queryGroupsKey           = "groups"
)

type queryStructureBudget struct {
	groups, rules, libraries, valueBytes int
}

func (b queryStructureBudget) validate() error {
	if b.groups > queryMaxGroups || b.rules > queryMaxRules || b.libraries > queryMaxLibraries || b.valueBytes > queryMaxValueBytes {
		return fmt.Errorf("%w: maximum 16 groups, 128 rules, 256 library IDs and 65536 encoded value bytes", ErrQueryComplexity)
	}
	return nil
}

// ValidateQueryStructure admits raw definitions before normalization or SQL
// construction. Multiple arguments share a single effective-definition budget.
// Library IDs count before deduplication; empty groups and discarded display
// rules still count. This limits work, never the number of matching items.
func ValidateQueryStructure(definitions ...QueryDefinition) error {
	var budget queryStructureBudget
	for _, def := range definitions {
		if def.structureErr != nil {
			return def.structureErr
		}
		budget.groups += len(def.Groups)
		budget.libraries += len(def.LibraryIDs)
		if err := budget.validate(); err != nil {
			return err
		}
		for _, group := range def.Groups {
			budget.rules += len(group.Rules)
			if err := budget.validate(); err != nil {
				return err
			}
			for _, rule := range group.Rules {
				// Bound the encoder's allocation even for programmatically supplied
				// arrays, maps, cycles and strings, before asking it for exact bytes.
				remaining := queryMaxValueBytes
				if !boundedQueryValue(reflect.ValueOf(rule.Value), &remaining, 0) {
					return ErrQueryComplexity
				}
				value, err := json.Marshal(rule.Value)
				if err != nil {
					return fmt.Errorf("encoding query value: %w", err)
				}
				budget.valueBytes += len(value)
				if err := budget.validate(); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func boundedQueryValue(v reflect.Value, remaining *int, depth int) bool {
	*remaining -= 1
	if *remaining < 0 || depth > 64 {
		return false
	}
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Interface:
		return boundedQueryValue(v.Elem(), remaining, depth+1)
	case reflect.String:
		*remaining -= v.Len()
	case reflect.Slice, reflect.Array:
		if v.Len() > *remaining {
			return false
		}
		for i := range v.Len() {
			if !boundedQueryValue(v.Index(i), remaining, depth+1) {
				return false
			}
		}
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String || v.Len() > *remaining {
			return false
		}
		iter := v.MapRange()
		for iter.Next() {
			if !boundedQueryValue(iter.Key(), remaining, depth+1) || !boundedQueryValue(iter.Value(), remaining, depth+1) {
				return false
			}
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
	default:
		return false
	}
	return *remaining >= 0
}

// ValidatePersonalCollectionQueryBudget checks the stored source and display
// fragment together, before either is normalized or built. Empty strings are
// absent definitions. Callers add any live browse overlay separately.
func ValidatePersonalCollectionQueryBudget(source, display string) error {
	_, err := validateQueryJSONStructure([]byte(source), []byte(display))
	return err
}

// ValidateLibraryCollectionQueryBudget admits bindings and the raw definition
// together, including browse overlays, before normalization or intersection can
// discard IDs. The legacy binding is used only when the binding list is empty.
func ValidateLibraryCollectionQueryBudget(libraryID int, libraryIDs []int, source []byte, overlays ...QueryDefinition) error {
	libraries := len(libraryIDs)
	if libraries == 0 && libraryID > 0 {
		libraries = 1
	}
	if err := (queryStructureBudget{libraries: libraries}).validate(); err != nil {
		return err
	}
	if err := ValidateQueryStructure(overlays...); err != nil {
		return err
	}
	definitions := [][]byte{source}
	for _, overlay := range overlays {
		raw, err := json.Marshal(overlay)
		if err != nil {
			return err
		}
		definitions = append(definitions, raw)
	}
	budget, err := validateQueryJSONStructure(definitions...)
	if err != nil {
		return err
	}
	budget.libraries += libraries
	return budget.validate()
}

func validateCollectionQueryComposition(source, display string, overlay QueryDefinition) error {
	if err := ValidateQueryStructure(overlay); err != nil {
		return err
	}
	rawOverlay, err := json.Marshal(overlay)
	if err != nil {
		return err
	}
	_, err = validateQueryJSONStructure([]byte(source), []byte(display), rawOverlay)
	return err
}

// validateQueryJSONStructure scans slices of the original JSON rather than
// decoding arbitrary groups/values into allocated Go objects. json.Valid owns
// syntax checking; the small walker only identifies budgeted fields, including
// escaped keys and repeated keys that a decoder would otherwise overwrite.
func validateQueryJSONStructure(definitions ...[]byte) (queryStructureBudget, error) {
	var budget queryStructureBudget
	for _, raw := range definitions {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		if !json.Valid(raw) {
			return budget, errors.New("invalid query definition JSON")
		}
		err := walkQueryJSONObject(raw, func(key string, value []byte) error {
			switch {
			case strings.EqualFold(key, queryLibraryIDsKey), strings.EqualFold(key, queryLegacyLibraryIDsKey):
				return walkQueryJSONArray(value, func([]byte) error {
					budget.libraries++
					return budget.validate()
				})
			case strings.EqualFold(key, "filter_library_id"):
				if !bytes.Equal(value, []byte("null")) {
					budget.libraries++
				}
			case strings.EqualFold(key, queryGroupsKey):
				return walkQueryJSONArray(value, func(group []byte) error {
					budget.groups++
					if err := budget.validate(); err != nil {
						return err
					}
					return walkQueryJSONObject(group, func(key string, rules []byte) error {
						if !strings.EqualFold(key, "rules") {
							return nil
						}
						return walkQueryJSONArray(rules, func(rule []byte) error {
							budget.rules++
							if err := budget.validate(); err != nil {
								return err
							}
							return walkQueryJSONObject(rule, func(key string, value []byte) error {
								if strings.EqualFold(key, "value") {
									budget.valueBytes += len(value)
								}
								return budget.validate()
							})
						})
					})
				})
			}
			return budget.validate()
		})
		if err != nil {
			return budget, err
		}
	}
	return budget, nil
}

func walkQueryJSONObject(raw []byte, visit func(string, []byte) error) error {
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	for i := skipQueryJSONSpace(raw, 1); raw[i] != '}'; {
		end := queryJSONValueEnd(raw, i)
		var key string
		// No recognized key can need more than 128 bytes, even fully escaped.
		if end-i <= 128 {
			_ = json.Unmarshal(raw[i:end], &key)
		}
		i = skipQueryJSONSpace(raw, end)
		i = skipQueryJSONSpace(raw, i+1) // colon
		end = queryJSONValueEnd(raw, i)
		if err := visit(key, raw[i:end]); err != nil {
			return err
		}
		i = skipQueryJSONSpace(raw, end)
		if raw[i] == ',' {
			i = skipQueryJSONSpace(raw, i+1)
		}
	}
	return nil
}

func walkQueryJSONArray(raw []byte, visit func([]byte) error) error {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	for i := skipQueryJSONSpace(raw, 1); raw[i] != ']'; {
		end := queryJSONValueEnd(raw, i)
		if err := visit(raw[i:end]); err != nil {
			return err
		}
		i = skipQueryJSONSpace(raw, end)
		if raw[i] == ',' {
			i = skipQueryJSONSpace(raw, i+1)
		}
	}
	return nil
}

func skipQueryJSONSpace(raw []byte, i int) int {
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\n' || raw[i] == '\r' || raw[i] == '\t') {
		i++
	}
	return i
}

func queryJSONValueEnd(raw []byte, start int) int {
	depth, quoted := 0, false
	for i := start; i < len(raw); i++ {
		c := raw[i]
		if quoted {
			switch c {
			case '\\':
				i++
			case '"':
				quoted = false
				if depth == 0 {
					return i + 1
				}
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '{', '[':
			depth++
		case '}', ']':
			if depth == 0 {
				return i
			}
			depth--
			if depth == 0 {
				return i + 1
			}
		case ',', ' ', '\n', '\r', '\t':
			if depth == 0 {
				return i
			}
		}
	}
	return len(raw)
}

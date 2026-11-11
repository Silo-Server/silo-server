package catalog

import (
	"encoding/json"
	"strings"
)

// NormalizeQueryDefinitionJSON is the canonical form of a collection's
// rules as its editors store them: decoded, normalized, and validated. Every
// writer of a collection definition goes through it, a restore or import
// included.
func NormalizeQueryDefinitionJSON(raw []byte, allowPersonalizedSorts, allowPersonalizedFields bool) (json.RawMessage, error) {
	var def QueryDefinition
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &def); err != nil {
			return nil, err
		}
	}
	def = def.Normalize()
	if err := def.ValidateWithOptions(allowPersonalizedSorts, allowPersonalizedFields); err != nil {
		return nil, err
	}
	return json.Marshal(def)
}

// NormalizeSmartCollectionQueryJSON is NormalizeQueryDefinitionJSON for a
// smart collection, whose item limit is capped (ApplySmartCollectionItemLimit).
func NormalizeSmartCollectionQueryJSON(raw []byte, allowPersonalizedSorts, allowPersonalizedFields bool) (json.RawMessage, error) {
	normalized, err := NormalizeQueryDefinitionJSON(raw, allowPersonalizedSorts, allowPersonalizedFields)
	if err != nil {
		return nil, err
	}
	var def QueryDefinition
	if err := json.Unmarshal(normalized, &def); err != nil {
		return nil, err
	}
	return json.Marshal(ApplySmartCollectionItemLimit(def))
}

// jsonNullText is JSON null.
const jsonNullText = "null"

// ErrInvalidSortConfig refuses a collection sort_config that is neither {}
// nor a supported sort.
var ErrInvalidSortConfig = sortConfigError{}

type sortConfigError struct{}

func (sortConfigError) Error() string {
	return "sort_config must be {} or {\"field\":\"<supported sort>\",\"order\":\"asc|desc\"}"
}

// NormalizeCollectionSortConfig is the canonical form of a collection's
// default sort as its editors store it.
//
// allowPersonalized must be false for library collection defaults because
// those defaults are shared across profiles. Viewer overrides are validated
// separately and may use profile-scoped fields for either collection kind.
func NormalizeCollectionSortConfig(raw json.RawMessage, allowPersonalized bool) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == jsonNullText || trimmed == "{}" {
		return "{}", nil
	}
	var cfg struct {
		Field string `json:"field"`
		Order string `json:"order"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", ErrInvalidSortConfig
	}
	if strings.TrimSpace(cfg.Field) == "" {
		// sort_config predates default sorting and may contain independent
		// collection modes such as {"mode":"manual_pins"}. Preserve those
		// configurations byte-for-byte instead of erasing them merely because
		// they do not contain a sort field.
		return string(raw), nil
	}
	qs, ok := NormalizeCollectionSort(cfg.Field, cfg.Order, allowPersonalized)
	if !ok {
		return "", ErrInvalidSortConfig
	}
	return EncodeCollectionDefaultSort(qs, true)
}

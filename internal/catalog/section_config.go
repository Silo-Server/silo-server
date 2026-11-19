package catalog

import (
	"encoding/json"
	"strings"
)

// SectionConfigScope is the media scope and libraries a stored section config
// limits a row to. A row and its See All page both read it through
// ParseSectionConfigScope, so the two keep to the same titles.
type SectionConfigScope struct {
	// MediaScope is the config's media_scope, or its legacy filter_type,
	// lowercased. Empty means every type.
	MediaScope string
	LibraryIDs []int
}

// ParseSectionConfigScope reads the media scope and libraries from a stored
// section config. Flat filter_library_id(s) keys take precedence; otherwise
// the scope and libraries come from the config's query definition.
func ParseSectionConfigScope(config json.RawMessage) SectionConfigScope {
	var flat struct {
		FilterType       string `json:"filter_type"`
		FilterLibraryID  *int   `json:"filter_library_id"`
		FilterLibraryIDs []int  `json:"filter_library_ids"`
	}
	if len(config) > 0 {
		_ = json.Unmarshal(config, &flat)
	}
	scope := SectionConfigScope{
		MediaScope: flat.FilterType,
		LibraryIDs: normalizeCatalogSectionLibraryIDs(flat.FilterLibraryID, flat.FilterLibraryIDs),
	}
	if len(scope.LibraryIDs) == 0 {
		if def, err := ParseSectionQueryDefinition(config); err == nil {
			scope.MediaScope = def.MediaScope
			scope.LibraryIDs = append([]int(nil), def.LibraryIDs...)
		}
	}
	scope.MediaScope = strings.ToLower(strings.TrimSpace(scope.MediaScope))
	return scope
}

// ParseSectionQueryDefinition parses a stored section config as a query
// definition. A config is legacy when a legacy key holds a value
// (filter_type, filter_library_id(s), a flat order, or a string sort);
// otherwise it is a structured query definition.
func ParseSectionQueryDefinition(config json.RawMessage) (QueryDefinition, error) {
	if len(config) == 0 {
		return QueryDefinition{}.Normalize(), nil
	}

	var legacy struct {
		FilterType       string          `json:"filter_type"`
		FilterLibraryID  *int            `json:"filter_library_id"`
		FilterLibraryIDs []int           `json:"filter_library_ids"`
		Sort             json.RawMessage `json:"sort"`
		Order            string          `json:"order"`
	}
	if err := json.Unmarshal(config, &legacy); err != nil {
		return QueryDefinition{}, err
	}

	if legacy.FilterType != "" || legacy.FilterLibraryID != nil || len(legacy.FilterLibraryIDs) > 0 || legacy.Order != "" || isLegacySectionSort(legacy.Sort) {
		return NormalizeLegacySectionFilter(config)
	}

	var def QueryDefinition
	if err := json.Unmarshal(config, &def); err != nil {
		return QueryDefinition{}, err
	}
	def = def.Normalize()
	return def, def.Validate()
}

func isLegacySectionSort(raw json.RawMessage) bool {
	return len(raw) > 0 && raw[0] == '"'
}

package handlers

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/Silo-Server/silo-server/internal/sections"
)

func isTraktBackedSection(sectionType string, config json.RawMessage) bool {
	var values struct {
		Source         string `json:"source"`
		SourceProvider string `json:"source_provider"`
	}
	if len(config) == 0 || json.Unmarshal(config, &values) != nil {
		return false
	}
	switch sectionType {
	case string(sections.SectionTrendingDiscover):
		return values.Source == adminCollectionTrakt
	case string(sections.SectionCollection):
		return values.SourceProvider == adminCollectionTrakt
	default:
		return false
	}
}

func jsonConfigEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil {
		return reflect.DeepEqual(leftValue, rightValue)
	}
	return bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right))
}

func hasSectionConfig(config json.RawMessage) bool {
	trimmed := bytes.TrimSpace(config)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func traktSectionKind(config json.RawMessage) string {
	if isTraktBackedSection(string(sections.SectionTrendingDiscover), config) {
		return string(sections.SectionTrendingDiscover)
	}
	if isTraktBackedSection(string(sections.SectionCollection), config) {
		return string(sections.SectionCollection)
	}
	return ""
}

func isTraktCollectionSourceConfig(config json.RawMessage) bool {
	var values struct {
		Provider string `json:"provider"`
		Mode     string `json:"mode"`
	}
	if len(config) == 0 || json.Unmarshal(config, &values) != nil {
		return false
	}
	return values.Provider == adminCollectionTrakt || strings.HasPrefix(values.Mode, adminCollectionTrakt+"_")
}

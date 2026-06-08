package sections

import (
	"encoding/json"
	"fmt"
	"strings"
)

type ContinueType string

const (
	ContinueTypeWatching  ContinueType = "watching"
	ContinueTypeListening ContinueType = "listening"
	ContinueTypeReading   ContinueType = "reading"
)

type continueTypeConfig struct {
	ContinueType string `json:"continue_type"`
	FilterType   string `json:"filter_type"`
	MediaScope   string `json:"media_scope"`
}

func ContinueTypeConfig(typ ContinueType) json.RawMessage {
	if !IsValidContinueType(typ) {
		typ = ContinueTypeWatching
	}
	raw, err := json.Marshal(map[string]string{"continue_type": string(typ)})
	if err != nil {
		return json.RawMessage(`{"continue_type":"watching"}`)
	}
	return raw
}

func ContinueTypeFromConfig(config json.RawMessage) ContinueType {
	typ, _ := ParseContinueType(config)
	return typ
}

func ParseContinueType(config json.RawMessage) (ContinueType, error) {
	var raw continueTypeConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &raw); err != nil {
			return ContinueTypeWatching, err
		}
	}

	if strings.TrimSpace(raw.ContinueType) != "" {
		typ := ContinueType(strings.ToLower(strings.TrimSpace(raw.ContinueType)))
		if !IsValidContinueType(typ) {
			return ContinueTypeWatching, fmt.Errorf("continue_type must be 'watching', 'listening', or 'reading'")
		}
		return typ, nil
	}

	switch strings.ToLower(strings.TrimSpace(raw.FilterType)) {
	case "audiobook":
		return ContinueTypeListening, nil
	case "ebook":
		return ContinueTypeReading, nil
	}

	switch strings.ToLower(strings.TrimSpace(raw.MediaScope)) {
	case "audiobook":
		return ContinueTypeListening, nil
	case "ebook":
		return ContinueTypeReading, nil
	}

	return ContinueTypeWatching, nil
}

func IsValidContinueType(typ ContinueType) bool {
	switch typ {
	case ContinueTypeWatching, ContinueTypeListening, ContinueTypeReading:
		return true
	default:
		return false
	}
}

func ContinueTypeMatchesItem(typ ContinueType, itemType string) bool {
	switch typ {
	case ContinueTypeWatching:
		return itemType == "movie" || itemType == "episode"
	case ContinueTypeListening:
		return itemType == "audiobook"
	case ContinueTypeReading:
		return itemType == "ebook"
	default:
		return false
	}
}

func ContinueTypeAllowsNextUp(typ ContinueType) bool {
	return typ == ContinueTypeWatching
}

func IsAudiobookLibraryType(libraryType string) bool {
	switch strings.ToLower(strings.TrimSpace(libraryType)) {
	case "audiobook", "audiobooks":
		return true
	default:
		return false
	}
}

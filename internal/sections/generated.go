package sections

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

const GeneratedHomeLibraryRecentSource = "home_library_recent"

const (
	generatedHomeLibraryRecentKindAdded            = "recently_added"
	generatedHomeLibraryRecentKindReleased         = "recently_released"
	generatedHomeLibraryRecentKindReleasedEpisodes = "recently_released_episodes"
)

type generatedHomeLibraryRecentConfig struct {
	FilterLibraryID    *int   `json:"filter_library_id"`
	GeneratedLibraryID *int   `json:"generated_library_id"`
	GeneratedKind      string `json:"generated_kind"`
	GeneratedSource    string `json:"generated_source"`
}

func GeneratedHomeLibraryRecentConfig(libraryID int) json.RawMessage {
	config, err := json.Marshal(generatedHomeLibraryRecentConfig{
		FilterLibraryID: &libraryID,
		GeneratedSource: GeneratedHomeLibraryRecentSource,
	})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return config
}

func GeneratedHomeLibraryRecentEpisodesConfig(libraryID int) json.RawMessage {
	config, err := json.Marshal(struct {
		catalog.QueryDefinition
		GeneratedLibraryID int    `json:"generated_library_id"`
		GeneratedKind      string `json:"generated_kind"`
		GeneratedSource    string `json:"generated_source"`
	}{
		QueryDefinition: catalog.QueryDefinition{
			LibraryIDs: []int{libraryID},
			MediaScope: "episode",
			Match:      "all",
			Groups:     []catalog.QueryGroup{},
			Sort:       catalog.QuerySort{Field: "release_date", Order: "desc"},
		}.Normalize(),
		GeneratedLibraryID: libraryID,
		GeneratedKind:      generatedHomeLibraryRecentKindReleasedEpisodes,
		GeneratedSource:    GeneratedHomeLibraryRecentSource,
	})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return config
}

func GeneratedHomeLibraryRecentTitle(sectionType SectionType, libraryName string) string {
	switch sectionType {
	case SectionRecentlyAdded:
		return fmt.Sprintf("Recently Added in %s", libraryName)
	case SectionRecentlyReleased:
		return fmt.Sprintf("Recently Released in %s", libraryName)
	default:
		return string(sectionType)
	}
}

func generatedHomeLibraryRecentTitle(kind string, sectionType SectionType, libraryName string) string {
	switch kind {
	case generatedHomeLibraryRecentKindReleasedEpisodes:
		return fmt.Sprintf("Recently Released Episodes in %s", libraryName)
	case generatedHomeLibraryRecentKindAdded:
		return fmt.Sprintf("Recently Added in %s", libraryName)
	case generatedHomeLibraryRecentKindReleased:
		return fmt.Sprintf("Recently Released in %s", libraryName)
	default:
		return GeneratedHomeLibraryRecentTitle(sectionType, libraryName)
	}
}

func generatedHomeLibraryRecentKindForSection(s *PageSection) string {
	if s == nil {
		return ""
	}
	_, kind, _ := parseGeneratedHomeLibraryRecentConfig(s.Config)
	if kind != "" {
		return kind
	}
	switch s.SectionType {
	case SectionRecentlyAdded:
		return generatedHomeLibraryRecentKindAdded
	case SectionRecentlyReleased:
		return generatedHomeLibraryRecentKindReleased
	default:
		return ""
	}
}

func ParseGeneratedHomeLibraryRecentConfig(config json.RawMessage) (int, bool) {
	id, _, ok := parseGeneratedHomeLibraryRecentConfig(config)
	return id, ok
}

func parseGeneratedHomeLibraryRecentConfig(config json.RawMessage) (int, string, bool) {
	var cfg generatedHomeLibraryRecentConfig
	if len(config) == 0 {
		return 0, "", false
	}
	if err := json.Unmarshal(config, &cfg); err != nil {
		return 0, "", false
	}
	if cfg.GeneratedSource != GeneratedHomeLibraryRecentSource {
		return 0, "", false
	}
	libraryID := cfg.GeneratedLibraryID
	if libraryID == nil {
		libraryID = cfg.FilterLibraryID
	}
	if libraryID == nil || *libraryID <= 0 {
		return 0, "", false
	}
	return *libraryID, cfg.GeneratedKind, true
}

func IsGeneratedHomeLibraryRecentSection(s *PageSection, libraryID int) bool {
	if s == nil || s.Scope != "home" || s.LibraryID != nil {
		return false
	}
	id, _, ok := parseGeneratedHomeLibraryRecentConfig(s.Config)
	if !ok {
		return false
	}
	if s.SectionType != SectionRecentlyAdded && s.SectionType != SectionRecentlyReleased && s.SectionType != SectionCustomFilter {
		return false
	}
	return ok && id == libraryID
}

func ShouldSyncGeneratedHomeLibraryRecentTitle(s *PageSection, oldLibraryName string) bool {
	if s == nil {
		return false
	}
	kind := generatedHomeLibraryRecentKindForSection(s)
	expected := generatedHomeLibraryRecentTitle(kind, s.SectionType, oldLibraryName)
	return strings.TrimSpace(s.Title) == expected
}

func GeneratedHomeLibraryRecentSyncedTitle(s *PageSection, libraryName string) string {
	if s == nil {
		return ""
	}
	kind := generatedHomeLibraryRecentKindForSection(s)
	return generatedHomeLibraryRecentTitle(kind, s.SectionType, libraryName)
}

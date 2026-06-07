package embeddings

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	maxOverviewRunes = 1000
	maxKeywords      = 5
)

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}

	count := 0
	for i := range s {
		if count == limit {
			return s[:i]
		}
		count++
	}

	return s
}

// BuildEmbeddingText constructs a structured text representation of a media item
// suitable for generating embeddings. Leads with semantic content (genres + overview)
// to prevent title-word dominance in the embedding space.
func BuildEmbeddingText(item *models.MediaItem) string {
	var parts []string

	// Lead with genres + type + overview for semantic dominance.
	typeName := mediaTypeLabel(item.Type)

	if len(item.Genres) > 0 && item.Overview != "" {
		overview := truncateRunes(item.Overview, maxOverviewRunes)
		parts = append(parts, fmt.Sprintf("%s %s about %s", strings.Join(item.Genres, ", "), typeName, overview))
	} else if len(item.Genres) > 0 {
		parts = append(parts, fmt.Sprintf("%s %s", strings.Join(item.Genres, ", "), typeName))
	} else if item.Overview != "" {
		overview := truncateRunes(item.Overview, maxOverviewRunes)
		parts = append(parts, fmt.Sprintf("%s. %s", typeName, overview))
	}

	// Title with year — present but no longer leading.
	if item.Year > 0 {
		parts = append(parts, fmt.Sprintf("%s (%d)", item.Title, item.Year))
	} else {
		parts = append(parts, item.Title)
	}

	if item.ContentRating != "" {
		parts = append(parts, fmt.Sprintf("Rated %s", item.ContentRating))
	}

	if item.Tagline != "" {
		parts = append(parts, fmt.Sprintf(`"%s"`, item.Tagline))
	}

	if item.Type == "audiobook" {
		// Audiobooks credit author (kind=7) and narrator (kind=8). Cast/
		// director/writer don't apply. Keep the SQL canonical_text builder
		// in recommendations/repo.go in sync with this shape.
		var authors, narrators []models.ItemPerson
		for _, p := range item.People {
			switch p.Kind {
			case models.PersonKindAuthor:
				authors = append(authors, p)
			case models.PersonKindNarrator:
				narrators = append(narrators, p)
			}
		}
		sortItemPeople(authors)
		sortItemPeople(narrators)
		if len(authors) > 0 {
			parts = append(parts, fmt.Sprintf("Written by %s", strings.Join(itemPersonNames(authors), ", ")))
		}
		if len(narrators) > 0 {
			parts = append(parts, fmt.Sprintf("Narrated by %s", strings.Join(itemPersonNames(narrators), ", ")))
		}
	} else {
		// Top-billed cast with character names (up to 5).
		var actors []models.ItemPerson
		for _, p := range item.People {
			if p.Kind == models.PersonKindActor {
				actors = append(actors, p)
			}
		}
		sortItemPeople(actors)
		if len(actors) > 0 {
			credits := make([]string, 0, 5)
			for i, p := range actors {
				if i >= 5 {
					break
				}
				if p.Character != "" {
					credits = append(credits, fmt.Sprintf("%s as %s", p.Name, p.Character))
				} else {
					credits = append(credits, p.Name)
				}
			}
			parts = append(parts, fmt.Sprintf("Cast: %s", strings.Join(credits, ", ")))
		}

		// Director(s).
		var directors []models.ItemPerson
		for _, p := range item.People {
			if p.Kind == models.PersonKindDirector {
				directors = append(directors, p)
			}
		}
		sortItemPeople(directors)
		if len(directors) > 0 {
			parts = append(parts, fmt.Sprintf("Directed by %s", strings.Join(itemPersonNames(directors), ", ")))
		}

		// Writer(s).
		var writers []models.ItemPerson
		for _, p := range item.People {
			if p.Kind == models.PersonKindWriter {
				writers = append(writers, p)
			}
		}
		sortItemPeople(writers)
		if len(writers) > 0 {
			parts = append(parts, fmt.Sprintf("Written by %s", strings.Join(itemPersonNames(writers), ", ")))
		}
	}

	if len(item.Keywords) > 0 {
		keywords := item.Keywords
		if len(keywords) > maxKeywords {
			keywords = keywords[:maxKeywords]
		}
		parts = append(parts, fmt.Sprintf("Keywords: %s", strings.Join(keywords, ", ")))
	}

	if item.OriginalLanguage != "" {
		parts = append(parts, fmt.Sprintf("Original language: %s", item.OriginalLanguage))
	}

	if len(item.Studios) > 0 {
		parts = append(parts, fmt.Sprintf("Studios: %s", strings.Join(item.Studios, ", ")))
	}

	if len(item.Networks) > 0 {
		parts = append(parts, fmt.Sprintf("Network: %s", strings.Join(item.Networks, ", ")))
	}

	if len(item.Countries) > 0 {
		countries := item.Countries
		if len(countries) > 2 {
			countries = countries[:2]
		}
		parts = append(parts, fmt.Sprintf("Country: %s", strings.Join(countries, ", ")))
	}

	return strings.ToValidUTF8(strings.Join(parts, ". "), "")
}

// mediaTypeLabel maps a media_items.type to the natural-language label
// used in the embedding canonical text. The exact strings here are part
// of the embedding-text contract — change them and every existing
// embedding goes stale (see canonicalText logic in repo.go).
func mediaTypeLabel(t string) string {
	switch t {
	case "series":
		return "TV series"
	case "audiobook":
		return "audiobook"
	default:
		return "movie"
	}
}

func sortItemPeople(people []models.ItemPerson) {
	sort.SliceStable(people, func(i, j int) bool {
		if people[i].SortOrder != people[j].SortOrder {
			return people[i].SortOrder < people[j].SortOrder
		}
		if people[i].Name != people[j].Name {
			return people[i].Name < people[j].Name
		}
		if people[i].Character != people[j].Character {
			return people[i].Character < people[j].Character
		}
		return people[i].ID < people[j].ID
	})
}

func itemPersonNames(people []models.ItemPerson) []string {
	names := make([]string, 0, len(people))
	for _, p := range people {
		names = append(names, p.Name)
	}
	return names
}

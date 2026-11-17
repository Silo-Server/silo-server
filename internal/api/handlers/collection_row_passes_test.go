package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// A row of collections has collections and no titles: the passes that drop
// empty rows and hide watched titles keep it while it has a collection, and
// drop it once it has none.
func TestRowOfCollectionsSurvivesTheEmptyRowPasses(t *testing.T) {
	row := func(id string, cards int) sections.SectionWithItems {
		s := sections.SectionWithItems{ResolvedSection: sections.ResolvedSection{ID: id, SectionType: sections.SectionCollectionPosters}, Items: []*models.MediaItem{}, TotalCount: cards}
		for range cards {
			s.Collections = append(s.Collections, sections.CollectionCard{ID: id})
		}
		return s
	}
	rows := []sections.SectionWithItems{row("shown", 2), row("empty", 0)}
	rows = filterWatchedHomeSectionItems(rows, map[string]*itemUserStateResponse{})
	rows = dropEmptyWatchedHomeSections(rows)
	rows = applyDiversityFilter(rows)
	rows = dropEmptySections(rows)
	if len(rows) != 1 || rows[0].ID != "shown" || len(rows[0].Collections) != 2 {
		t.Fatalf("rows after the passes = %+v", rows)
	}
}

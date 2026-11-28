package catalog

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestFoldSpannedEpisodesStopsAtAnEpisodeWithItsOwnFile(t *testing.T) {
	ep := func(n int) *models.Episode {
		return &models.Episode{ContentID: "e" + string(rune('0'+n)), SeasonNumber: 1, EpisodeNumber: n}
	}
	e1, e2, e3, e4 := ep(1), ep(2), ep(3), ep(4)
	omnibus := &models.MediaFile{ID: 1, EpisodeID: "e1", MultiEpisodeStart: 1, MultiEpisodeEnd: 3}
	served := map[string][]*models.MediaFile{
		"e1": {omnibus},
		// E2 also has a copy of its own, so it keeps its row. E3 must keep
		// its row too: folding it would show "E1–E3, 2 episodes" above E2.
		"e2": {omnibus, {ID: 2, EpisodeID: "e2"}},
		"e3": {omnibus},
		"e4": {{ID: 4, EpisodeID: "e4"}},
	}
	parts, folded := FoldSpannedEpisodes([]*models.Episode{e1, e2, e3, e4}, served)
	if len(parts) != 0 || len(folded) != 0 {
		t.Fatalf("parts = %v, folded = %v; want nothing folded", parts, folded)
	}

	served["e2"] = []*models.MediaFile{omnibus}
	parts, folded = FoldSpannedEpisodes([]*models.Episode{e1, e2, e3, e4}, served)
	if got := parts["e1"]; len(got) != 2 || got[0] != e2 || got[1] != e3 || !folded["e2"] || !folded["e3"] {
		t.Fatalf("parts = %v, folded = %v; want E2 and E3 folded into E1", parts, folded)
	}
}

package catalog

import (
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestEpisodeAvailability(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	past := now.Add(-24 * time.Hour)
	future := now.Add(24 * time.Hour)
	tests := []struct {
		name      string
		inLibrary bool
		airDate   *time.Time
		want      string
	}{
		{"in library", true, &past, ""},
		{"in library before airing", true, &future, ""},
		{"aired, no file", false, &past, models.EpisodeMissing},
		{"not aired yet", false, &future, models.EpisodeUnaired},
		{"no air date", false, nil, models.EpisodeUnaired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := episodeAvailability(tt.inLibrary, tt.airDate, now); got != tt.want {
				t.Fatalf("episodeAvailability = %q, want %q", got, tt.want)
			}
		})
	}
}

// Specials never show as missing; episodes still to air only when asked.
func TestMissingPredicates(t *testing.T) {
	const aired = "episodes.season_number > 0 AND episodes.air_date <= now()"
	for _, predicate := range []string{episodeOrMissingPredicate(false), missingEpisodePredicate(false)} {
		if !strings.Contains(predicate, aired) {
			t.Fatalf("aired-only predicate = %q", predicate)
		}
	}
	for _, predicate := range []string{episodeOrMissingPredicate(true), missingEpisodePredicate(true)} {
		if strings.Contains(predicate, "air_date") || !strings.Contains(predicate, "episodes.season_number > 0") {
			t.Fatalf("upcoming predicate = %q", predicate)
		}
	}
	for _, upcoming := range []bool{false, true} {
		if p := episodeOrMissingPredicate(upcoming); !strings.HasPrefix(p, "((") || !strings.Contains(p, ") OR EXISTS") {
			t.Fatalf("episodeOrMissingPredicate must keep library episodes: %q", p)
		}
	}
}

func TestInLibrary(t *testing.T) {
	held := &models.Episode{ContentID: "held"}
	missing := &models.Episode{ContentID: "missing", Availability: models.EpisodeMissing}
	unaired := &models.Episode{ContentID: "unaired", Availability: models.EpisodeUnaired}
	got := InLibrary([]*models.Episode{held, missing, nil, unaired})
	if len(got) != 1 || got[0] != held {
		t.Fatalf("InLibrary = %v, want only the held episode", got)
	}
}

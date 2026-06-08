package metadata

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestIsEpisodePlaceholderTitle(t *testing.T) {
	cases := map[string]bool{
		"":           false,
		"Pilot":      false,
		"TBA":        true,
		"tbd":        true,
		"Episode 12": true,
	}

	for title, want := range cases {
		if got := IsEpisodePlaceholderTitle(title); got != want {
			t.Fatalf("title %q placeholder=%v want %v", title, got, want)
		}
	}
}

func TestEpisodeHasIncompleteMetadata(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)

	complete := &models.Episode{
		Title:          "Pilot",
		Overview:       "Overview",
		StillPath:      "s3://still.jpg",
		MetadataSource: "provider",
	}
	if EpisodeHasIncompleteMetadata(complete, now) {
		t.Fatal("expected complete episode to be treated as complete")
	}

	recent := now.Add(-7 * 24 * time.Hour)
	incomplete := &models.Episode{
		Title:          "Episode 2",
		Overview:       "",
		AirDate:        &recent,
		MetadataSource: "scanner_fallback",
	}
	if !EpisodeHasIncompleteMetadata(incomplete, now) {
		t.Fatal("expected fallback placeholder episode to be treated as incomplete")
	}

	providerNumbered := &models.Episode{
		Title:          "Episode 1",
		Overview:       "Provider overview",
		TmdbID:         "3812334",
		MetadataSource: "provider",
	}
	if !EpisodeHasIncompleteMetadata(providerNumbered, now) {
		t.Fatal("expected provider numbered title to remain quality-incomplete")
	}
}

func TestEpisodeHasActionableMetadataDebt(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-7 * 24 * time.Hour)
	old := now.Add(-90 * 24 * time.Hour)

	cases := []struct {
		name string
		ep   *models.Episode
		want bool
	}{
		{
			name: "provider numbered title with overview",
			ep: &models.Episode{
				Title:          "Episode 1",
				Overview:       "Provider overview",
				TmdbID:         "3812334",
				MetadataSource: "provider",
			},
			want: false,
		},
		{
			name: "provider numbered title with empty overview",
			ep: &models.Episode{
				Title:          "Episode 1",
				TvdbID:         "9607609",
				MetadataSource: "provider",
			},
			want: false,
		},
		{
			name: "provider provisional title",
			ep: &models.Episode{
				Title:          "TBD",
				TmdbID:         "3812334",
				MetadataSource: "provider",
			},
			want: true,
		},
		{
			name: "scanner fallback numbered title",
			ep: &models.Episode{
				Title:          "Episode 1",
				MetadataSource: "scanner_fallback",
			},
			want: true,
		},
		{
			name: "missing title",
			ep: &models.Episode{
				TmdbID:         "3812334",
				MetadataSource: "provider",
			},
			want: true,
		},
		{
			name: "missing provider id",
			ep: &models.Episode{
				Title:          "Pilot",
				Overview:       "Provider overview",
				MetadataSource: "provider",
			},
			want: true,
		},
		{
			name: "recent provider episode missing still",
			ep: &models.Episode{
				Title:          "Pilot",
				Overview:       "Provider overview",
				TmdbID:         "3812334",
				AirDate:        &recent,
				MetadataSource: "provider",
			},
			want: true,
		},
		{
			name: "old provider episode missing still",
			ep: &models.Episode{
				Title:          "Pilot",
				Overview:       "Provider overview",
				TmdbID:         "3812334",
				AirDate:        &old,
				MetadataSource: "provider",
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EpisodeHasActionableMetadataDebt(tc.ep, now); got != tc.want {
				t.Fatalf("EpisodeHasActionableMetadataDebt() = %v, want %v", got, tc.want)
			}
		})
	}
}

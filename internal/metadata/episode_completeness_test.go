package metadata

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

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
			name: "old provider episode with real TBD title",
			ep: &models.Episode{
				Title:          "TBD",
				Overview:       "Provider overview",
				TmdbID:         "3812334",
				StillPath:      "s3://still.jpg",
				AirDate:        &old,
				MetadataSource: "provider",
			},
			want: false,
		},
		{
			name: "old provider episode with lowercase tba title",
			ep: &models.Episode{
				Title:          "tba",
				TmdbID:         "3812334",
				AirDate:        &old,
				MetadataSource: "provider",
			},
			want: false,
		},
		{
			name: "recent provider TBD episode missing still",
			ep: &models.Episode{
				Title:          "TBD",
				TmdbID:         "3812334",
				AirDate:        &recent,
				MetadataSource: "provider",
			},
			want: true,
		},
		{
			name: "old scanner fallback TBD title",
			ep: &models.Episode{
				Title:          "TBD",
				TmdbID:         "3812334",
				AirDate:        &old,
				MetadataSource: "scanner_fallback",
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

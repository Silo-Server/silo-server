package handlers

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// The episode's own still generates w780/w500 variants, but the series-artwork
// fallback (backdrop/poster) does not — applying w780 (or its w500 resolver
// fallback) to a backdrop 404s. The card path must keep the fallback on w300.
func TestEpisodeResponseShellImageVariant(t *testing.T) {
	tests := []struct {
		name       string
		still      string
		fallback   string
		wantSuffix string
	}{
		{"own still uses w780", "tvdb/series/1/seasons/2/episodes/3/still/original.webp", "", "/still/w780.webp"},
		{"backdrop fallback keeps w300", "", "tmdb/shows/1/backdrop/original.webp", "/backdrop/w300.webp"},
		{"poster fallback keeps w300", "", "tmdb/shows/1/poster/original.webp", "/poster/w300.webp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep := &models.Episode{StillPath: tt.still}
			_, path := episodeResponseShell(ep, episodeImageFallback{Path: tt.fallback})
			if !strings.HasSuffix(path, tt.wantSuffix) {
				t.Errorf("path = %q, want suffix %q", path, tt.wantSuffix)
			}
		})
	}
}

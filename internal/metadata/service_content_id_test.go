package metadata

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestFindContentIDFallsBackToContentID(t *testing.T) {
	item := &models.MediaItem{
		ContentID: "content-1",
	}

	if got := findContentID(item, "tmdb"); got != "content-1" {
		t.Fatalf("findContentID(tmdb) = %q, want content id fallback", got)
	}
}

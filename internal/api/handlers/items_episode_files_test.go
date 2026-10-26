package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The v2 episode view learns which files ffprobe rejected, while the frozen
// v1 episode JSON stays what it was.
func TestEpisodeFileResponsesCarryUnreadableOffTheV1Wire(t *testing.T) {
	failedAt := time.Now().UTC()
	files := []*models.MediaFile{
		{ID: 1, FilePath: "/library/show/S01E03.mkv", ProbeFailedAt: &failedAt},
		{ID: 2, FilePath: "/library/show/S01E04.mkv", Resolution: "1080p"},
	}
	resp := episodeFileResponses(files, catalog.AccessFilter{})
	if len(resp) != 2 || !resp[0].Unreadable || resp[1].Unreadable {
		t.Fatalf("responses = %+v", resp)
	}
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "nreadable") {
		t.Fatalf("v1 episode files leaked the v2 field: %s", body)
	}
}

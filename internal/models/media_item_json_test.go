package models

import (
	"encoding/json"
	"strings"
	"testing"
)

// Some frozen v1 responses serialize MediaItem directly, such as the admin
// section preview. Transient provenance must not reach them.
func TestMediaItemJSONOmitsTransientEpisodeStillProvenance(t *testing.T) {
	still := true
	wire, err := json.Marshal(MediaItem{ContentID: "episode-1", PosterIsEpisodeStill: &still, BackdropIsEpisodeStill: &still})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "IsEpisodeStill") || strings.Contains(string(wire), "is_episode_still") {
		t.Fatalf("MediaItem JSON carries provenance: %s", wire)
	}
}

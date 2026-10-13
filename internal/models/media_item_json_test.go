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

// The banner, thumb, clear art and disc slots are served by /api/v2 only; the
// frozen v1 responses that serialize MediaItem must not grow them.
func TestMediaItemJSONOmitsExtraArtwork(t *testing.T) {
	wire, err := json.Marshal(MediaItem{
		ContentID:          "movie-1",
		BannerPath:         "local/b",
		BannerSourcePath:   "file:///b",
		BannerThumbhash:    "bt",
		ThumbPath:          "local/t",
		ThumbSourcePath:    "file:///t",
		ThumbThumbhash:     "tt",
		ClearArtPath:       "local/c",
		ClearArtSourcePath: "file:///c",
		DiscPath:           "local/d",
		DiscSourcePath:     "file:///d",
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"BannerPath", "BannerSourcePath", "BannerThumbhash", "ThumbPath", "ThumbSourcePath", "ThumbThumbhash",
		"ClearArtPath", "ClearArtSourcePath", "DiscPath", "DiscSourcePath",
	} {
		if _, ok := fields[key]; ok {
			t.Fatalf("MediaItem JSON carries %s: %s", key, wire)
		}
	}
	if strings.Contains(string(wire), "local/") || strings.Contains(string(wire), "file://") {
		t.Fatalf("MediaItem JSON carries an extra artwork path: %s", wire)
	}
}

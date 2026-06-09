package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
)

// TestVirtualEpisodeLocationType verifies the fix: a playable episode with no
// underlying file versions (provider-metadata-only / unaired) reports
// LocationType=Virtual with no MediaSources, while a real episode with a
// version reports LocationType=FileSystem and gets MediaSources.
func TestVirtualEpisodeLocationType(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	fields := map[string]bool{"mediasources": true}

	// Fileless episode -> Virtual, no media sources.
	fileless := upstreamItemDetail{
		ContentID: "1001",
		Type:      "episode",
		Title:     "Unaired Episode",
		Versions:  nil,
	}
	dto := m.itemFromDetailWithFields(fileless, false, nil, fields)
	if dto.LocationType != "Virtual" {
		t.Errorf("fileless episode LocationType = %q, want Virtual", dto.LocationType)
	}
	if dto.VideoType != "" {
		t.Errorf("fileless episode VideoType = %q, want empty", dto.VideoType)
	}
	if len(dto.MediaSources) != 0 {
		t.Errorf("fileless episode MediaSources = %d, want 0", len(dto.MediaSources))
	}

	// Episode with a real file version -> FileSystem + MediaSources present.
	withFile := upstreamItemDetail{
		ContentID: "1002",
		Type:      "episode",
		Title:     "Real Episode",
		Versions: []catalog.FileVersion{{
			FileID:    42,
			Container: "mkv",
			Duration:  1200,
		}},
	}
	dto2 := m.itemFromDetailWithFields(withFile, false, nil, fields)
	if dto2.LocationType != "FileSystem" {
		t.Errorf("real episode LocationType = %q, want FileSystem", dto2.LocationType)
	}
	if len(dto2.MediaSources) != 1 {
		t.Errorf("real episode MediaSources = %d, want 1", len(dto2.MediaSources))
	}
}

// TestVirtualEpisodeListPaths verifies the list-side mappers honor the
// HasMediaFiles signal: false -> Virtual, true -> FileSystem, and nil
// (unknown, producing query did not check) preserves the historical
// FileSystem default.
func TestVirtualEpisodeListPaths(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	boolp := func(v bool) *bool { return &v }

	cases := []struct {
		name          string
		hasMediaFiles *bool
		wantLocation  string
		wantVideoType string
	}{
		{"fileless", boolp(false), "Virtual", ""},
		{"with file", boolp(true), "FileSystem", "VideoFile"},
		{"unknown", nil, "FileSystem", "VideoFile"},
	}

	for _, tc := range cases {
		t.Run("itemFromList/"+tc.name, func(t *testing.T) {
			dto := m.itemFromList(upstreamListItem{
				ContentID:     "2001",
				Type:          "episode",
				Title:         "Episode",
				HasMediaFiles: tc.hasMediaFiles,
			}, false, nil, nil)
			if dto.LocationType != tc.wantLocation {
				t.Errorf("LocationType = %q, want %q", dto.LocationType, tc.wantLocation)
			}
			if dto.VideoType != tc.wantVideoType {
				t.Errorf("VideoType = %q, want %q", dto.VideoType, tc.wantVideoType)
			}
		})
		t.Run("episodeFromUpstream/"+tc.name, func(t *testing.T) {
			dto := m.episodeFromUpstream(upstreamEpisode{
				ContentID:     "3001",
				Title:         "Episode",
				HasMediaFiles: tc.hasMediaFiles,
			}, false, nil)
			if dto.LocationType != tc.wantLocation {
				t.Errorf("LocationType = %q, want %q", dto.LocationType, tc.wantLocation)
			}
			if dto.VideoType != tc.wantVideoType {
				t.Errorf("VideoType = %q, want %q", dto.VideoType, tc.wantVideoType)
			}
		})
	}
}

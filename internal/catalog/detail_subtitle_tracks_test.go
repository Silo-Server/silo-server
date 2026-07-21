package catalog

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// External subtitles must carry their combined-index identity (externals
// occupy 0..n-1 in the playback selection space; see
// ResolveSubtitlePolicyV3 / session subtitle_urls). Before this test they all
// serialized the zero value, so any file with two external subs — or one
// external plus an embedded stream index 0 — published duplicate indexes and
// clients keying rows on index crashed or selected the wrong track.
func TestBuildVersionSubtitleTracksAssignsUniqueExternalIndexes(t *testing.T) {
	file := &models.MediaFile{
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 2, Codec: "subrip", Language: "en"},
			{Index: 3, Codec: "hdmv_pgs_subtitle", Language: "de"},
		},
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: "/media/movie.en.srt", Format: "srt", Language: "en"},
			{Path: "/media/movie.nl.srt", Format: "srt", Language: "nl"},
		},
	}

	tracks := buildVersionSubtitleTracks(file)
	if len(tracks) != 4 {
		t.Fatalf("tracks len = %d, want 4", len(tracks))
	}

	// Embedded entries keep their ffprobe stream indexes (existing contract).
	if tracks[0].Index != 2 || tracks[1].Index != 3 {
		t.Fatalf("embedded indexes = %d,%d, want 2,3", tracks[0].Index, tracks[1].Index)
	}

	// Externals carry their combined-space ordinal, not the zero value.
	if tracks[2].Index != 0 || !tracks[2].External {
		t.Fatalf("first external = index %d external %v, want 0/true", tracks[2].Index, tracks[2].External)
	}
	if tracks[3].Index != 1 || !tracks[3].External {
		t.Fatalf("second external = index %d external %v, want 1/true", tracks[3].Index, tracks[3].External)
	}
}

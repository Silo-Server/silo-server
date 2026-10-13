package scanner

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestSupportsVideoFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/tv/Show/Season 01/Show - S01E01.mkv", true},
		{"/tv/Show/Season 01/Show - S01E02.webm", true},
		{"/movies/Heat (1995)/Heat.mp4", true},
		{"/movies/Heat (1995)/Heat.m4v", true},
		{"/movies/Heat (1995)/Heat.MOV", true},
		{"/movies/Heat (1995)/Heat.3gp", true},
		{"/movies/Heat (1995)/Heat.3g2", true},
		{"/movies/Heat (1995)/Heat.f4v", true},
		{"/movies/Heat (1995)/Heat.avi", true},
		{"/movies/Heat (1995)/Heat.divx", true},
		{"/movies/Heat (1995)/Heat.ts", true},
		{"/movies/Heat (1995)/Heat.m2ts", true},
		{"/movies/Heat (1995)/Heat.MTS", true},
		{"/movies/Heat (1995)/Heat.mpg", true},
		{"/movies/Heat (1995)/Heat.mpeg", true},
		{"/movies/Heat (1995)/Heat.wmv", true},
		{"/movies/Heat (1995)/Heat.asf", true},
		{"/movies/Heat (1995)/Heat.flv", true},
		{"/movies/Heat (1995)/Heat.ogv", true},
		{"/movies/Heat (1995)/Heat.ogm", true},
		// A STREAM folder outside a disc structure is an ordinary folder.
		{"/movies/Heat (1995)/STREAM/Heat.m2ts", true},
		// Clips of a Blu-ray or AVCHD disc folder are not titles.
		{"/movies/Heat (1995)/BDMV/STREAM/00000.m2ts", false},
		{"/home/Camcorder/PRIVATE/AVCHD/bdmv/stream/00001.MTS", false},
		{"/movies/Heat (1995)/VIDEO_TS/VTS_01_1.VOB", false},
		{"/movies/Heat (1995)/Heat.vob", false},
		{"/movies/Heat (1995)/Heat.iso", false},
		{"/movies/Heat (1995)/Heat.rmvb", false},
		{"/movies/Heat (1995)/Heat.ogg", false},
		{"/movies/Heat (1995)/Heat.mp3", false},
		{"/movies/Heat (1995)/Heat.srt", false},
		{"/movies/Heat (1995)/Heat", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := SupportsVideoFile(tc.path); got != tc.want {
			t.Errorf("SupportsVideoFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestUnsupportedVideoFileReason(t *testing.T) {
	cases := []struct {
		path       string
		wantReason bool
	}{
		{"/movies/Heat (1995)/Heat.webm", false},
		{"/movies/Heat (1995)/Heat.srt", false},
		{"/movies/Heat (1995)/poster.jpg", false},
		{"/movies/Heat (1995)/BDMV/STREAM/00000.m2ts", true},
		{"/movies/Heat (1995)/VIDEO_TS/VTS_01_1.VOB", true},
		{"/movies/Heat (1995)/Heat.iso", true},
		{"/movies/Heat (1995)/Heat.rm", true},
		{"/movies/Heat (1995)/Heat.rmvb", true},
	}
	for _, tc := range cases {
		if got := unsupportedVideoFileReason(tc.path); (got != "") != tc.wantReason {
			t.Errorf("unsupportedVideoFileReason(%q) = %q, want reason: %v", tc.path, got, tc.wantReason)
		}
	}
}

// Every cataloged video extension must be served with a video media type;
// application/octet-stream would keep browsers and players from recognizing
// a direct-played original.
func TestVideoExtensionsHaveVideoMIMEType(t *testing.T) {
	for ext := range videoExtensions {
		if got := playback.MimeFromExtension("file" + ext); !strings.HasPrefix(got, "video/") {
			t.Errorf("playback.MimeFromExtension(%q) = %q, want a video/ type", "file"+ext, got)
		}
	}
	for ext := range unsupportedVideoExtensions {
		if videoExtensions[ext] {
			t.Errorf("%s is both supported and unsupported", ext)
		}
	}
}

func TestCollectLogicalFilePathsAdmitsCommonVideoContainers(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"Show/Season 02/Show - S02E01.mkv",
		"Show/Season 02/Show - S02E02.webm",
		"Show/Season 02/Show - S02E03.ts",
		"Show/Season 02/Show - S02E04.mov",
		"Show/Season 02/Show - S02E05.m2ts",
		"Show/Season 02/Show - S02E06.mpg",
		"Show/Season 02/Show - S02E07.ogv",
		"Show/Season 02/Show - S02E08.flv",
		"Show/Season 02/Show - S02E09.3gp",
		"Show/Season 02/Show - S02E10.vob",
		"Show/Season 02/Show - S02E10.srt",
		"Show/Season 03/BDMV/STREAM/00000.m2ts",
		"Show/Season 03/BDMV/STREAM/00001.m2ts",
		"Show/Season 03/VIDEO_TS/VTS_01_1.VOB",
	} {
		writeTestFile(t, filepath.Join(root, rel), "x")
	}

	for _, libraryType := range []string{"series", "movies"} {
		got := collectTestFilePaths(t, root, libraryType)
		assertFilePaths(t, got, root, []string{
			"Show/Season 02/Show - S02E01.mkv",
			"Show/Season 02/Show - S02E02.webm",
			"Show/Season 02/Show - S02E03.ts",
			"Show/Season 02/Show - S02E04.mov",
			"Show/Season 02/Show - S02E05.m2ts",
			"Show/Season 02/Show - S02E06.mpg",
			"Show/Season 02/Show - S02E07.ogv",
			"Show/Season 02/Show - S02E08.flv",
			"Show/Season 02/Show - S02E09.3gp",
		})
	}
}

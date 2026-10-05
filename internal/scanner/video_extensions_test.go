package scanner

import (
	"path/filepath"
	"sort"
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
		path string
		want string
	}{
		{"/movies/Heat (1995)/Heat.webm", ""},
		{"/movies/Heat (1995)/Heat.srt", ""},
		{"/movies/Heat (1995)/poster.jpg", ""},
		{"/movies/Heat (1995)/STREAM/Heat.m2ts", ""},
		{"/movies/Heat (1995)/BDMV/STREAM/00000.m2ts", UnsupportedReasonDiscStream},
		{"/movies/Heat (1995)/VIDEO_TS/VTS_01_1.VOB", UnsupportedReasonDVDVOB},
		{"/movies/Heat (1995)/Heat.iso", UnsupportedReasonDiscImage},
		{"/movies/Heat (1995)/Heat.rm", UnsupportedReasonRealMedia},
		{"/movies/Heat (1995)/Heat.rmvb", UnsupportedReasonRealMedia},
	}
	for _, tc := range cases {
		if got := unsupportedVideoFileReason(tc.path); got != tc.want {
			t.Errorf("unsupportedVideoFileReason(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestEveryUnsupportedReasonHasAMessage(t *testing.T) {
	reasons := []string{UnsupportedReasonDiscStream}
	for _, reason := range unsupportedVideoExtensions {
		reasons = append(reasons, reason)
	}
	for _, reason := range reasons {
		if UnsupportedReasonMessage(reason) == "" {
			t.Errorf("reason %q has no message", reason)
		}
	}
	if got := UnsupportedReasonMessage("unknown"); got != "" {
		t.Errorf("UnsupportedReasonMessage(unknown) = %q, want empty", got)
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

func TestCollectLogicalFilePathsReportsUnsupportedFiles(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"Heat (1995)/Heat (1995).mkv",
		"Heat (1995)/Heat (1995).srt",
		"Ronin (1998)/VIDEO_TS/VIDEO_TS.VOB",
		"Ronin (1998)/VIDEO_TS/VTS_01_1.VOB",
		"Ronin (1998)/VIDEO_TS/VTS_01_0.IFO",
		"Thief (1981)/BDMV/STREAM/00000.m2ts",
		"Thief (1981)/Thief (1981).iso",
		"Manhunter (1986)/Manhunter (1986).rmvb",
	} {
		writeTestFile(t, filepath.Join(root, rel), "x")
	}

	for _, libraryType := range []string{"series", "movies"} {
		_, _, walk, err := collectLogicalFilePaths(t.Context(), []string{root}, libraryType, nil)
		if err != nil {
			t.Fatalf("%s: collect logical paths: %v", libraryType, err)
		}
		got := make([]string, 0, len(walk.unsupported))
		for _, file := range walk.unsupported {
			rel, err := filepath.Rel(root, file.Path)
			if err != nil {
				t.Fatalf("rel %s: %v", file.Path, err)
			}
			got = append(got, rel+" "+file.Reason)
		}
		sort.Strings(got)
		want := []string{
			"Manhunter (1986)/Manhunter (1986).rmvb " + UnsupportedReasonRealMedia,
			"Ronin (1998)/VIDEO_TS/VIDEO_TS.VOB " + UnsupportedReasonDVDVOB,
			"Ronin (1998)/VIDEO_TS/VTS_01_1.VOB " + UnsupportedReasonDVDVOB,
			"Thief (1981)/BDMV/STREAM/00000.m2ts " + UnsupportedReasonDiscStream,
			"Thief (1981)/Thief (1981).iso " + UnsupportedReasonDiscImage,
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: unsupported files =\n%s\nwant\n%s", libraryType, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}

	// Only video walks report video files they skip.
	for _, libraryType := range []string{"audiobooks", "ebooks"} {
		_, _, walk, err := collectLogicalFilePaths(t.Context(), []string{root}, libraryType, nil)
		if err != nil {
			t.Fatalf("%s: collect logical paths: %v", libraryType, err)
		}
		if len(walk.unsupported) != 0 {
			t.Errorf("%s: unsupported files = %v, want none", libraryType, walk.unsupported)
		}
	}
}

// A file the scanner ignores for another reason is not reported as skipped
// for its type.
func TestCollectLogicalFilePathsLeavesIgnoredFilesUnreported(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"Heat (1995)/Heat (1995).mkv",
		"Heat (1995)/Sample/Heat-sample.rmvb",
		"@eaDir/Heat.rmvb",
		"Skipped/.ignore",
		"Skipped/Heat.vob",
	} {
		writeTestFile(t, filepath.Join(root, rel), "")
	}

	_, _, walk, err := collectLogicalFilePaths(t.Context(), []string{root}, "movies", nil)
	if err != nil {
		t.Fatalf("collect logical paths: %v", err)
	}
	if len(walk.unsupported) != 0 {
		t.Errorf("unsupported files = %v, want none", walk.unsupported)
	}
}

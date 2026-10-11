package scanner

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

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

// A search for the reason text an admin sees, its label or its explanation,
// finds the reason as a search for its code does.
func TestUnsupportedReasonsMatching(t *testing.T) {
	for _, tc := range []struct {
		search string
		want   []string
	}{
		{search: "Blu-ray or AVCHD folder", want: []string{UnsupportedReasonDiscStream}},
		{search: "dvd vob", want: []string{UnsupportedReasonDVDVOB}},
		{search: "Disc image", want: []string{UnsupportedReasonDiscImage}},
		{search: "disc_stream", want: []string{UnsupportedReasonDiscStream}},
		{search: "remux", want: []string{UnsupportedReasonDiscImage, UnsupportedReasonDiscStream, UnsupportedReasonDVDVOB}},
		{search: "Ronin", want: []string{}},
	} {
		if got := unsupportedReasonsMatching(tc.search); strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("unsupportedReasonsMatching(%q) = %v, want %v", tc.search, got, tc.want)
		}
	}
}

func TestEveryUnsupportedReasonHasALabel(t *testing.T) {
	for reason := range unsupportedReasonMessages {
		if unsupportedReasonLabels[reason] == "" {
			t.Errorf("reason %q has no label", reason)
		}
	}
}

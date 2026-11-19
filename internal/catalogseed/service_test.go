package catalogseed

import (
	"bytes"
	"compress/gzip"

	"errors"
	"io"

	"testing"
	"testing/iotest"
)

func TestRewriteFileRecordKeepsSidecarsInsideTheLibrary(t *testing.T) {
	rewrites := normalizeRewrites([]PathRewrite{{From: "/old/movies", To: "/media/movies"}})
	folderPaths := map[int][]string{7: {"/media/movies"}}

	tests := []struct {
		name     string
		sidecar  string
		wantPath string
		wantErr  bool
	}{
		{name: "beside the video", sidecar: "/old/movies/Film/Subs/Film.en.ass", wantPath: "/media/movies/Film/Subs/Film.en.ass"},
		{name: "outside the library", sidecar: "/etc/silo/config.ass", wantErr: true},
		{name: "climbs out of the library", sidecar: "/old/movies/Film/../../../etc/passwd", wantErr: true},
		{name: "sibling folder sharing the root's name", sidecar: "/media/movies-private/Film.en.srt", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := FileRecord{
				MediaFolderID:     7,
				FilePath:          "/old/movies/Film/Film.mkv",
				ExternalSubtitles: []ExternalSubtitleRecord{{Path: tt.sidecar, Format: "ass"}},
			}
			got, err := rewriteFileRecord(file, rewrites, folderPaths)
			if tt.wantErr {
				var unmatched *UnmatchedRootsError
				if !errors.As(err, &unmatched) {
					t.Fatalf("rewriteFileRecord error = %v, want *UnmatchedRootsError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("rewriteFileRecord: %v", err)
			}
			if got.ExternalSubtitles[0].Path != tt.wantPath {
				t.Fatalf("sidecar path = %q, want %q", got.ExternalSubtitles[0].Path, tt.wantPath)
			}
		})
	}
}

// A storage read cut off partway is the source's fault, not the bundle's, so
// the import reports it as a source failure rather than an invalid bundle.
func TestDecodeBundleReportsSourceFailure(t *testing.T) {
	data := gzipBytes(t, `{"items":[{"content_id":"a"},{"content_id":"b"}]}`)
	cutOff := errors.New("connection reset")
	for name, prefix := range map[string][]byte{"in the header": data[:4], "in the body": data[:len(data)-8]} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeBundle(io.MultiReader(bytes.NewReader(prefix), iotest.ErrReader(cutOff)))
			if !errors.Is(err, ErrReadingSource) || !errors.Is(err, cutOff) || errors.Is(err, ErrInvalidBundle) {
				t.Fatalf("decodeBundle error = %v, want ErrReadingSource wrapping the read error", err)
			}
		})
	}
}

func gzipBytes(t *testing.T, doc string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

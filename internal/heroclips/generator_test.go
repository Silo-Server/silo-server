package heroclips

import (
	"os"
	"path/filepath"
	"testing"
)

/*
 * The downloader names what it writes after the output name with the format id
 * between it and the extension, and it muxes the parts itself only where it can
 * find an encoder. A deployment whose encoder it cannot find leaves the parts
 * and nothing else, so these filenames are the ones a real 1080p trailer
 * produces — and they are what a clip is built from there.
 */
func TestDownloadPartsNamesTheFilesTheDownloaderLeaves(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  []string
	}{
		{
			name:  "a video part and an audio part are both inputs",
			files: []string{"source.f399.mp4", "source.f251.webm"},
			want:  []string{"source.f251.webm", "source.f399.mp4"},
		},
		{
			name:  "a single progressive part is the only input",
			files: []string{"source.f18.mp4"},
			want:  []string{"source.f18.mp4"},
		},
		{
			name:  "a transfer that has not finished is not an input",
			files: []string{"source.f399.mp4", "source.f399.mp4.part", "source.f251.webm.ytdl"},
			want:  []string{"source.f399.mp4"},
		},
		{
			name:  "whatever the downloader did not write is ignored",
			files: []string{"source.f399.mp4", "poster.jpg", "notes.txt"},
			want:  []string{"source.f399.mp4"},
		},
		{
			name:  "nothing downloaded at all is an error",
			files: []string{"poster.jpg"},
			want:  nil,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range test.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
					t.Fatalf("writing %s: %v", name, err)
				}
			}
			if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
				t.Fatalf("making a directory the downloader did not write: %v", err)
			}

			got, err := downloadParts(dir)
			if test.want == nil {
				if err == nil {
					t.Fatalf("downloadParts(%v) = %v, want an error", test.files, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("downloadParts: %v", err)
			}

			names := make([]string, 0, len(got))
			for _, path := range got {
				names = append(names, filepath.Base(path))
			}
			if len(names) != len(test.want) {
				t.Fatalf("downloadParts = %v, want %v", names, test.want)
			}
			for i, name := range names {
				if name != test.want[i] {
					t.Fatalf("downloadParts = %v, want %v", names, test.want)
				}
			}
		})
	}
}

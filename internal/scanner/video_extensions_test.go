package scanner

import (
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
		{"/movies/Heat (1995)/Heat.MOV", true},
		// A STREAM folder outside a disc structure is an ordinary folder.
		{"/movies/Heat (1995)/STREAM/Heat.m2ts", true},
		// Clips of a Blu-ray or AVCHD disc folder are not titles.
		{"/movies/Heat (1995)/BDMV/STREAM/00000.m2ts", false},
		{"/home/Camcorder/PRIVATE/AVCHD/bdmv/stream/00001.MTS", false},
		{"/movies/Heat (1995)/VIDEO_TS/VTS_01_1.VOB", false},
		{"/movies/Heat (1995)/Heat.mp3", false},
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
		{"/movies/Heat (1995)/BDMV/STREAM/00000.m2ts", true},
		{"/movies/Heat (1995)/VIDEO_TS/VTS_01_1.VOB", true},
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

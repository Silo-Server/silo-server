package mediacontainer

import (
	"slices"
	"testing"
)

// Every cataloged extension names the demuxers that read it, so a relayed
// input of any cataloged file is admitted.
func TestEveryExtensionHasDemuxers(t *testing.T) {
	for _, set := range []map[string]bool{videoExtensions, audioExtensions} {
		for ext := range set {
			if len(demuxers[ext]) == 0 {
				t.Errorf("%s has no demuxer", ext)
			}
		}
	}
	for ext := range demuxers {
		if !videoExtensions[ext] && !audioExtensions[ext] {
			t.Errorf("%s has demuxers but is not cataloged", ext)
		}
	}
	got := Demuxers()
	for _, want := range []string{"matroska", "mov", "m4v", "mpegts", "mpeg", "avi", "asf", "flv", "ogg", "mp3", "flac", "wav", "aac"} {
		if !slices.Contains(got, want) {
			t.Errorf("Demuxers() = %v, missing %s", got, want)
		}
	}
	for _, refused := range []string{"concat", "hls", "image2", "lavfi", "tee", "dash"} {
		if slices.Contains(got, refused) {
			t.Errorf("Demuxers() admits %s, which opens other files", refused)
		}
	}
}

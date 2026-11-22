package playback

import (
	"os"
	"testing"
)

// TestMain reports scale's reference input as supported: argument tests build
// graphs for fake FFmpeg paths the real probe cannot run. A test that needs the
// fallback overrides scaleReferenceSupported and restores it.
func TestMain(m *testing.M) {
	scaleReferenceSupported = func(string) bool { return true }
	os.Exit(m.Run())
}

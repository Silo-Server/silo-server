package mediaprobe

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// FFprobe receives the real file name when the catalog path is the
// fspath-encoded form of a non-UTF-8 name.
func TestProbePrimaryVideoTrackDecodesEncodedPath(t *testing.T) {
	rawPath := filepath.Join(t.TempDir(), "Takk for alt B\xe5rd Ylvis\xe5ker.mkv")
	if err := os.WriteFile(rawPath, []byte("media"), 0o600); err != nil {
		t.Skipf("file system rejects non-UTF-8 names: %v", err)
	}
	ffprobePath := filepath.Join(t.TempDir(), "ffprobe")
	// The fake answers only for an existing file. Holding ForkLock while it is
	// written keeps a concurrent fork from inheriting the write fd (ETXTBSY).
	script := `#!/bin/sh
for last; do :; done
[ -e "$last" ] || { echo "$last: No such file or directory" >&2; exit 1; }
printf '%s\n' '{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080}]}'
`
	syscall.ForkLock.Lock()
	err := os.WriteFile(ffprobePath, []byte(script), 0o755)
	syscall.ForkLock.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	track, err := ProbePrimaryVideoTrack(t.Context(), ffprobePath, fspath.Encode(rawPath))
	if err != nil {
		t.Fatalf("ProbePrimaryVideoTrack: %v", err)
	}
	if track.Codec != "h264" || track.Width != 1920 {
		t.Fatalf("track = %+v, want the h264 1920-wide stream", track)
	}
}

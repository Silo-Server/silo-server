package playback

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeAfterWaitStarts writes files into dir shortly after the caller's wait
// has begun, each to a temporary name renamed into place as FFmpeg's
// temp_file flag does. The returned channel closes once every file exists.
func writeAfterWaitStarts(t *testing.T, dir string, files [][2]string) <-chan struct{} {
	t.Helper()
	written := make(chan struct{})
	time.AfterFunc(30*time.Millisecond, func() {
		defer close(written)
		for _, f := range files {
			tmp := filepath.Join(dir, f[0]+".tmp")
			if err := os.WriteFile(tmp, []byte(f[1]), 0o600); err != nil {
				t.Error(err)
				return
			}
			if err := os.Rename(tmp, filepath.Join(dir, f[0])); err != nil {
				t.Error(err)
				return
			}
		}
	})
	return written
}

// Output FFmpeg finishes while a wait is in progress is returned by that wait,
// not only output that already existed when it began.
func TestWaitsReturnOutputWrittenDuringTheWait(t *testing.T) {
	t.Run("WaitForSegment", func(t *testing.T) {
		dir := t.TempDir()
		session := &TranscodeSession{outputDir: dir, running: true}
		written := writeAfterWaitStarts(t, dir, [][2]string{{"seg_00003.ts", "segment"}})

		path, err := session.WaitForSegment("seg_00003.ts", 5*time.Second)
		<-written
		if err != nil {
			t.Fatalf("WaitForSegment: %v", err)
		}
		if path != filepath.Join(dir, "seg_00003.ts") {
			t.Fatalf("WaitForSegment path = %q", path)
		}
	})

	t.Run("WaitForOpenSegment", func(t *testing.T) {
		dir := t.TempDir()
		session := &TranscodeSession{outputDir: dir, running: true}
		written := writeAfterWaitStarts(t, dir, [][2]string{{"seg_00003.ts", "segment"}})

		lease, err := session.WaitForOpenSegment("seg_00003.ts", 5*time.Second)
		<-written
		if err != nil {
			t.Fatalf("WaitForOpenSegment: %v", err)
		}
		defer func() { _ = lease.Close() }()
		if lease.Info.Size() != int64(len("segment")) {
			t.Fatalf("WaitForOpenSegment opened %d bytes", lease.Info.Size())
		}
	})

	t.Run("WaitForGenerationManifest", func(t *testing.T) {
		dir := t.TempDir()
		opts := startupTestOpts()
		session := fakeStartupSession(t, opts, dir, false, true)
		// A running session's playlist is ready once it lists the startup
		// segments and each of them is on disk.
		var files [][2]string
		var playlistBuilder strings.Builder
		playlistBuilder.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:2\n")
		for i := range startupSegmentRequirement(opts) {
			name := fmt.Sprintf("seg_%05d.ts", i)
			files = append(files, [2]string{name, "segment"})
			playlistBuilder.WriteString("#EXTINF:2,\n" + name + "\n")
		}
		playlist := playlistBuilder.String()
		files = append(files, [2]string{"stream.m3u8", playlist})
		written := writeAfterWaitStarts(t, dir, files)

		manifest, err := session.WaitForGenerationManifest(5 * time.Second)
		<-written
		if err != nil {
			t.Fatalf("WaitForGenerationManifest: %v", err)
		}
		if !bytes.Equal(manifest, []byte(playlist)) {
			t.Fatalf("WaitForGenerationManifest = %q", manifest)
		}
	})
}

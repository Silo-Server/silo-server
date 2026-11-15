//go:build linux

package transcodenode

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// A node with core source access, against the core's real routes in
// process, with real FFmpeg. The library lives in a directory only the core
// reads: FFmpeg runs through wrappers that fail if they are handed anything
// under it, and inotify counts every open of the file, which must equal the
// opens the core's source route makes. The node plays, seeks, survives a cut
// connection, and fails cleanly when the file changes.
//
//nolint:funlen,gocyclo // One walk through a node's life on one source; its steps share the running sessions.
func TestThinNodeReadsThroughTheCore(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	realFFmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	realFFprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	library := t.TempDir()
	media := filepath.Join(library, "movie.mkv")
	if out, err := exec.Command(realFFmpeg, "-nostdin", "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration=12",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=12",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "25", "-c:a", "aac", media).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg can't make the test file (%v): %s", err, out)
	}
	bin := t.TempDir()
	handedLibrary := filepath.Join(bin, "handed-the-library")
	for name, target := range map[string]string{"ffmpeg": realFFmpeg, "ffprobe": realFFprobe} {
		script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in *'" + library + "'*) echo \"$a\" >> '" + handedLibrary + "'; exit 99 ;; esac; done\nexec '" + target + "' \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	opens := watchOpens(t, media)
	var coreRequests, coreOpens, cuts atomic.Int64
	c := newThinClusterWith(t, thinClusterOptions{
		media: media, ffmpeg: filepath.Join(bin, "ffmpeg"), sourceAccess: nodepool.SourceAccessCore,
		wrapSources: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recorder := &statusRecorder{ResponseWriter: w}
				// Counted before the route opens the file, so the count is
				// never behind the opens the route has made.
				coreRequests.Add(1)
				if cuts.Add(-1) >= 0 {
					// Cut this answer after its first bytes.
					recorder.cutAfter = 32 << 10
				} else {
					cuts.Store(0)
				}
				defer func() {
					// Every read the grant admits opens the file once.
					if recorder.status != http.StatusForbidden {
						coreOpens.Add(1)
					}
				}()
				next.ServeHTTP(recorder, r)
			})
		},
	})
	if got := c.remote.SourceAccess(); got != nodesource.ModeCore {
		t.Fatalf("node source access = %q, want core", got)
	}
	start := func(sessionID string, seek float64, segment int, grant string) (int, string) {
		t.Helper()
		status, body := c.call(http.MethodPost, "/transcode/start", TranscodeStartRequest{
			SessionID: sessionID, SourceGrant: grant, InputPath: media,
			SourceVideoCodec: "h264", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
			SegmentDuration: 2, TotalDuration: 12, SeekSeconds: seek, StartSegmentNumber: segment,
		}, nil)
		return status, string(body)
	}
	segments := func(sessionID string) []string {
		c.node.mu.RLock()
		session := c.node.sessions[sessionID]
		c.node.mu.RUnlock()
		if session == nil {
			return nil
		}
		// A replacement start writes in a directory of its own.
		entries, _ := os.ReadDir(session.Opts().OutputDir)
		var names []string
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".ts") || strings.HasSuffix(entry.Name(), ".m4s") {
				names = append(names, entry.Name())
			}
		}
		return names
	}

	// Plays.
	if status, body := start("core-play", 0, 0, c.grant(thinNodeID)); status != http.StatusAccepted {
		t.Fatalf("start = %d %s", status, body)
	}
	c.node.mu.RLock()
	input := c.node.sessions["core-play"].Opts().InputPath
	c.node.mu.RUnlock()
	if !nodesource.IsRelayURL(input) || !fspath.IsRelayed(input) {
		t.Fatalf("session input %q is not a relay URL", input)
	}
	c.waitFor("the played session's segments", func() bool { return len(segments("core-play")) >= 3 })

	// Seeks: a restart of the session from segment 3.
	if status, body := start("core-play", 6, 3, c.grant(thinNodeID)); status != http.StatusAccepted {
		t.Fatalf("seek start = %d %s", status, body)
	}
	c.waitFor("the seek's segments", func() bool { return len(segments("core-play")) >= 2 })

	// Survives cut connections: the relay resumes each from where it stopped.
	cuts.Store(3)
	if status, body := start("core-cut", 0, 0, c.grant(thinNodeID)); status != http.StatusAccepted {
		t.Fatalf("start through cut connections = %d %s", status, body)
	}
	c.waitFor("the cut session's segments", func() bool { return len(segments("core-cut")) >= 3 })
	if cuts.Load() > 0 {
		t.Fatal("the core cut fewer reads than asked")
	}

	// The node never opened the library: FFmpeg was never handed it, and
	// every open of the file is one the core's route made.
	if data, err := os.ReadFile(handedLibrary); err == nil {
		t.Fatalf("FFmpeg was handed the library path: %s", data)
	}
	if coreOpens.Load() == 0 {
		t.Fatal("the core served no source reads")
	}
	// The sessions are still reading, so the two counts are taken in an order
	// that cannot fail a correct run: the opens seen so far first, the
	// requests the route has taken after. inotify may merge two opens into
	// one event, which only lowers the first count.
	if seen, served := opens.seen(t), coreRequests.Load(); seen > served {
		t.Fatalf("the file was opened %d times, the core's route took %d requests", seen, served)
	}

	// Fails cleanly when the file changes: the next renewal or read gets
	// 412, the sessions on it end, a grant for the old revision is refused,
	// and a relayed read fails as a read, not as a missing file.
	staleGrant := c.grant(thinNodeID)
	reader, err := fspath.OpenReader(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(media, later, later); err != nil {
		t.Fatal(err)
	}
	c.waitFor("the sessions on the changed file to end", func() bool {
		return !c.node.hostsSession("core-play") && !c.node.hostsSession("core-cut")
	})
	if _, err := reader.ReadAt(make([]byte, 4096), 1<<20); err == nil || !errors.As(err, new(*fs.PathError)) || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a read of the changed file = %v, want a failed read", err)
	}
	if status, _ := start("core-stale", 0, 0, staleGrant); status != http.StatusBadRequest {
		t.Fatalf("start with a grant for the old revision = %d, want 400", status)
	}
	if status, body := start("core-new", 0, 0, c.grant(thinNodeID)); status != http.StatusAccepted {
		t.Fatalf("start with a grant for the new revision = %d %s", status, body)
	}
}

// statusRecorder records a response's status and can cut its body.
type statusRecorder struct {
	http.ResponseWriter
	status   int
	written  int
	cutAfter int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.cutAfter > 0 && r.written+len(p) > r.cutAfter {
		n, _ := r.ResponseWriter.Write(p[:r.cutAfter-r.written])
		r.written += n
		_ = http.NewResponseController(r.ResponseWriter).Flush()
		panic(http.ErrAbortHandler)
	}
	n, err := r.ResponseWriter.Write(p)
	r.written += n
	return n, err
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// openWatch counts the opens of one file.
type openWatch struct {
	opens    atomic.Int64
	marker   string
	markerWd int32
	marked   chan struct{}
}

// seen is the number of opens of the file observed so far. It opens the
// watch's marker file and waits for that event: inotify delivers events in
// order, so every earlier open of the watched file has been counted by then.
func (w *openWatch) seen(t *testing.T) int64 {
	t.Helper()
	f, err := os.Open(w.marker)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	select {
	case <-w.marked:
	case <-time.After(10 * time.Second):
		t.Fatal("the open watch never saw its marker")
	}
	return w.opens.Load()
}

// watchOpens counts the opens of path from now on.
func watchOpens(t *testing.T, path string) *openWatch {
	t.Helper()
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Skipf("inotify unavailable: %v", err)
	}
	watch := &openWatch{marker: filepath.Join(t.TempDir(), "marker"), marked: make(chan struct{}, 1)}
	if err := os.WriteFile(watch.marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.InotifyAddWatch(fd, path, unix.IN_OPEN); err != nil {
		_ = unix.Close(fd)
		t.Skipf("inotify watch: %v", err)
	}
	markerWd, err := unix.InotifyAddWatch(fd, watch.marker, unix.IN_OPEN)
	if err != nil {
		_ = unix.Close(fd)
		t.Skipf("inotify watch: %v", err)
	}
	watch.markerWd = int32(markerWd)
	// A non-blocking descriptor in an *os.File uses the runtime poller, so
	// Close ends a pending Read.
	events := os.NewFile(uintptr(fd), "inotify")
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 64*(unix.SizeofInotifyEvent+unix.NAME_MAX+1))
		for {
			n, err := events.Read(buf)
			if err != nil {
				return
			}
			for off := 0; off+unix.SizeofInotifyEvent <= n; {
				event := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
				switch {
				case event.Mask&unix.IN_OPEN == 0:
				case event.Wd == watch.markerWd:
					select {
					case watch.marked <- struct{}{}:
					default:
					}
				default:
					watch.opens.Add(1)
				}
				off += unix.SizeofInotifyEvent + int(event.Len)
			}
		}
	}()
	t.Cleanup(func() {
		_ = events.Close()
		<-done
	})
	return watch
}

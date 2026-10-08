//go:build unix

package transcodenode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/workerdrain"
)

func TestTranscodeDrainCountsCanceledCopyRecoveryAfterStop(t *testing.T) {
	dir := t.TempDir()
	startedPath, releasePath, producerPath := filepath.Join(dir, "started.fifo"), filepath.Join(dir, "release.fifo"), filepath.Join(dir, "producer.fifo")
	for _, path := range []string{startedPath, releasePath, producerPath} {
		if err := unix.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	startedPipe, err := os.OpenFile(startedPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = startedPipe.Close() }()
	releasePipe, err := os.OpenFile(releasePath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = releasePipe.Close() }()
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	ffmpeg := filepath.Join(dir, "ffmpeg")
	manifest := "#EXTM3U\\n#EXT-X-VERSION:7\\n#EXT-X-TARGETDURATION:3\\n#EXT-X-MEDIA-SEQUENCE:9\\n#EXT-X-MAP:URI=\"init.mp4\"\\n#EXTINF:2.669000,\\nseg_00009.m4s\\n#EXTINF:1.669000,\\nseg_00010.m4s\\n#EXTINF:1.668000,\\nseg_00011.m4s\\n"
	script := fmt.Sprintf("#!/bin/sh\nfor arg in \"$@\"; do\n if [ \"$arg\" = framecrc ]; then\n printf 'started\\n' > %s\n read ignored < %s\n printf '%%s\\n' '#tb 0: 1/1000' '0, 18000, 18000, 41, 1024, 0x12345678'\n exit 0\n fi\ndone\nprintf '%%b' %s > %s\nprintf fragment > %s\nexec cat %s > /dev/null\n", quote(startedPath), quote(releasePath), quote(manifest), quote(filepath.Join(dir, "stream.m3u8")), quote(filepath.Join(dir, "seg_00011.m4s")), quote(producerPath))
	if err := os.WriteFile(ffmpeg, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { _, _ = releasePipe.WriteString("release\n") }) }
	defer unblock()
	started := make(chan error, 1)
	go func() { _, err := bufio.NewReader(startedPipe).ReadString('\n'); started <- err }()
	const sid = "copy-node-drain"
	session, err := playback.StartTranscode(t.Context(), playback.TranscodeOpts{SessionID: sid, InputPath: "/media/synthetic.mkv", OutputDir: dir, FFmpegPath: ffmpeg, SeekSeconds: 18.261, StreamOriginSeconds: 18, CopySeekAnchorResolved: true, TargetCodecVideo: "copy", TargetCodecAudio: "copy", SegmentDuration: 2, StartSegmentNumber: 9})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(dir, "stream.m3u8"))
		fragment, fragmentErr := os.Stat(filepath.Join(dir, "seg_00011.m4s"))
		if err == nil && strings.Contains(string(data), "seg_00011.m4s\n") && fragmentErr == nil && fragment.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual producer did not publish copy recovery manifest")
		}
		runtime.Gosched()
	}
	srv := newTestServer(t)
	srv.sessions[sid] = session
	srv.activeJobs.Store(1)
	var fenced atomic.Bool
	srv.drain = workerdrain.New(func(context.Context) (int, string, string, error) {
		if fenced.Load() {
			return 7, "durable-fence", "synthetic-realm", nil
		}
		return 7, "", "synthetic-realm", nil
	}, srv.drainActiveJobs, nil)
	listener := httptest.NewServer(srv.Handler())
	defer listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, listener.URL+"/transcode/"+sid+"/segment/seg_00010.m4s", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testSecret)
	returned := make(chan error, 1)
	go func() {
		response, err := listener.Client().Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		returned <- err
	}()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("actual routed copy recovery did not start detached FFmpeg")
	}
	cancel()
	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("canceled recovery did not return its cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("copy recovery request did not detach")
	}
	fenced.Store(true)
	private := func(method, path string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(method, listener.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+testSecret)
		response, err := listener.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	stopped := private(http.MethodDelete, "/transcode/"+sid)
	_ = stopped.Body.Close()
	if stopped.StatusCode != http.StatusNoContent || srv.activeJobs.Load() != 0 {
		t.Fatalf("original session stop did not finish: %d jobs=%d", stopped.StatusCode, srv.activeJobs.Load())
	}
	observe := func() workerdrain.Status {
		t.Helper()
		response := private(http.MethodGet, "/admin/drain")
		defer func() { _ = response.Body.Close() }()
		var status workerdrain.Status
		if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&status) != nil {
			t.Fatalf("copy recovery drain: %d", response.StatusCode)
		}
		return status
	}
	busy := observe()
	if busy.Drained || busy.ActiveJobs < 1 || busy.ActiveRequests != 0 || busy.ActiveReservations != 0 {
		t.Fatalf("stopped session hid detached recovery: %+v", busy)
	}
	unblock()
	deadline = time.Now().Add(5 * time.Second)
	for playback.CopySeekAnchorsInFlight() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("copy recovery child did not finish")
		}
		runtime.Gosched()
	}
	if zero := observe(); !zero.Drained || zero.ActiveJobs != 0 {
		t.Fatalf("finished recovery retained drain work: %+v", zero)
	}
}

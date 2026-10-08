//go:build unix

package transcodenode

import (
	"bufio"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/workerdrain"
	"golang.org/x/sys/unix"
)

func TestTranscodeDrainRetainsPreparationPublishedAfterFence(t *testing.T) {
	dir := t.TempDir()
	startedPath, releasePath := filepath.Join(dir, "started.fifo"), filepath.Join(dir, "release.fifo")
	for _, path := range []string{startedPath, releasePath} {
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
	ffmpeg := filepath.Join(dir, "ffmpeg.sh")
	program := fmt.Sprintf("#!/bin/sh\nfor last; do :; done\nprintf 'started\\n' > %s\nread ignored < %s\nprintf artifact > \"$last\"\n", quote(startedPath), quote(releasePath))
	if err := os.WriteFile(ffmpeg, []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t)
	server.watcher.Config().Playback.FFmpegPath = ffmpeg
	server.watcher.Config().Playback.HWAccel = "none"
	var fence atomic.Value
	fence.Store("")
	server.drain = workerdrain.New(func(context.Context) (int, string, string, error) {
		return 7, fence.Load().(string), "synthetic-realm", nil
	}, server.drainActiveJobs, nil)
	listener := httptest.NewServer(server.Handler())
	defer listener.Close()
	var once sync.Once
	unblock := func() { once.Do(func() { _, _ = releasePipe.WriteString("release\n") }) }
	started := make(chan error, 1)
	go func() { _, err := bufio.NewReader(startedPipe).ReadString('\n'); started <- err }()
	completed := make(chan error, 1)
	preparer := downloadprepare.HTTPPreparer{}
	request := downloadprepare.Request{ArtifactID: "prepared", InputPath: "/media/movie.mkv", TargetCodecVideo: "copy", TargetCodecAudio: "copy", AudioTrackIndex: -1}
	go func() { _, err := preparer.Prepare(t.Context(), listener.URL, testSecret, request); completed <- err }()
	defer func() { unblock(); <-completed }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("actual preparation child did not start")
	}
	fence.Store("durable-fence")
	proof, err := server.drain.Observe(t.Context())
	if err != nil || proof.Drained || proof.ActiveJobs < 1 || proof.ActiveRequests != 1 || proof.ActiveReservations != 0 {
		t.Fatalf("pre-fence preparation was not reserved: %+v %v", proof, err)
	}
	unblock()
	select {
	case err = <-completed:
		completed <- err
	case <-time.After(5 * time.Second):
		t.Fatal("actual preparation did not finish")
	}
	if err != nil {
		t.Fatal(err)
	}
	proof, err = server.drain.Observe(t.Context())
	if err != nil || proof.Drained || proof.ActiveJobs != 0 || proof.ActiveRequests != 0 || proof.ActiveReservations != 1 {
		t.Fatalf("publication lost artifact reservation: %+v %v", proof, err)
	}
	if err := preparer.Delete(t.Context(), listener.URL, testSecret, request.ArtifactID); err != nil {
		t.Fatal(err)
	}
	proof, err = server.drain.Observe(t.Context())
	if err != nil || !proof.Drained || proof.ActiveReservations != 0 {
		t.Fatalf("publication cleanup: %+v %v", proof, err)
	}
	for _, suffix := range []string{".mp4", ".mp4.part"} {
		if _, err := os.Stat(filepath.Join(server.artifactRoot, request.ArtifactID+suffix)); !os.IsNotExist(err) {
			t.Fatalf("artifact file cleanup: %v", err)
		}
	}
}

//go:build unix

package transcodenode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/workerdrain"
)

// The production tone-map cache runs a real child command on its shared
// background context. A FIFO makes its lifetime observable and deterministic;
// canceling its HTTP caller cannot be mistaken for the process exiting.
func TestTranscodeDrainCountsCanceledToneMapProbe(t *testing.T) {
	dir := t.TempDir()
	startedPath := filepath.Join(dir, "started.fifo")
	releasePath := filepath.Join(dir, "release.fifo")
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
	ffmpeg := filepath.Join(dir, "ffmpeg")
	script := fmt.Sprintf("#!/bin/sh\nif mkdir %s 2>/dev/null; then\n printf 'started\\n' > %s\n read ignored < %s\nfi\nexit 1\n", quote(filepath.Join(dir, "first")), quote(startedPath), quote(releasePath))
	if err = os.WriteFile(ffmpeg, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { _, _ = releasePipe.WriteString("release\n") }) }
	defer unblock()
	started := make(chan error, 1)
	go func() { _, err := bufio.NewReader(startedPipe).ReadString('\n'); started <- err }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	returned := make(chan error, 1)
	go func() { _, err := tonemap.Probe(ctx, ffmpeg, tonemap.BackendSoftware, ""); returned <- err }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("actual probe command did not start")
	}
	cancel()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled probe returned: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("caller did not detach from probe")
	}
	if tonemap.ProbesInFlight() < 1 {
		t.Fatal("actual child work disappeared with caller")
	}
	srv := newTestServer(t)
	srv.drain = workerdrain.New(func(context.Context) (int, string, string, error) { return 7, "durable-fence", "synthetic-realm", nil }, srv.drainActiveJobs, nil)
	listener := httptest.NewServer(srv.Handler())
	defer listener.Close()
	status := func() workerdrain.Status {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, listener.URL+"/admin/drain", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+testSecret)
		response, err := listener.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var status workerdrain.Status
		if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&status) != nil {
			t.Fatalf("drain status: %d", response.StatusCode)
		}
		return status
	}
	busy := status()
	if busy.Drained || busy.ActiveJobs < 1 || busy.ActiveRequests != 0 {
		t.Fatalf("detached execution falsely retired: %+v", busy)
	}
	unblock()
	deadline := time.Now().Add(5 * time.Second)
	for tonemap.ProbesInFlight() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("released child work did not exit")
		}
		runtime.Gosched()
	}
	zero := status()
	if !zero.Drained || zero.ActiveJobs != 0 {
		t.Fatalf("finished child work retained: %+v", zero)
	}
}

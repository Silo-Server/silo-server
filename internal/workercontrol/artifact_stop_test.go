//go:build unix

package workercontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

const artifactDeleteTestWork = "artifact"

// Real signed admission, opaque sources and prepared receipts are exercised.
// The task fake encoder writes payload bytes only, so this proves ownership
// rather than media semantic correctness.
func (f *stopFixture) prepareArtifact(ctx context.Context, label string, bitrate int) (uint64, error) {
	script := "#!/bin/sh\nfor last do :; done\nprintf '" + label + "' > \"$last\"\n"
	if err := os.WriteFile(f.runtime.options.FFmpeg, []byte(script), 0700); err != nil {
		return 0, err
	}
	body, err := json.Marshal(downloadprepare.Request{ArtifactID: artifactDeleteTestWork, InputPath: f.source, SourceVideoCodec: "h264", TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetBitrateKbps: bitrate})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.worker.URL+"/downloads/prepare", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	resp, err := nodehttp.Client(&http.Client{}).Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("prepare status=%d body=%s", resp.StatusCode, raw)
	}
	var result downloadprepare.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return 0, err
	}
	if result.ArtifactID != artifactDeleteTestWork || result.FileSize <= 0 || result.ExecutionFingerprint == "" {
		return 0, fmt.Errorf("invalid prepared receipt: %+v", result)
	}
	a, ok, err := f.store.Get(ctx, 17, artifactDeleteTestWork)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("prepared admission missing")
	}
	return a.Generation, nil
}

func preparedArtifactPath(f *stopFixture) string {
	return filepath.Join(f.runtime.options.Scratch, downloadprepare.ArtifactDirectoryName, artifactDeleteTestWork+".mp4")
}
func delayedArtifactDelete(t *testing.T, f *stopFixture, generation uint64) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, f.worker.URL+"/downloads/artifacts/"+artifactDeleteTestWork, nil)
	if err != nil {
		t.Fatal(err)
	}
	delayed, err := f.service.PrepareRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if delayed.Header.Get(workeridentity.HeaderGeneration) != strconv.FormatUint(generation, 10) {
		t.Fatal("deletion is not scoped to observed generation")
	}
	return delayed
}
func deliverArtifactDelete(t *testing.T, f *stopFixture, req *http.Request, want int) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("artifact delete status=%d want%d", resp.StatusCode, want)
	}
	<-f.stops
}
func assertPreparedArtifact(t *testing.T, f *stopFixture, want string, generation uint64) {
	t.Helper()
	raw, err := os.ReadFile(preparedArtifactPath(f))
	if err != nil || string(raw) != want {
		t.Fatalf("prepared artifact=%s error=%v", raw, err)
	}
	if _, err := os.Stat(preparedArtifactPath(f) + ".receipt.json"); err != nil {
		t.Fatal("prepared receipt missing:", err)
	}
	if active, err := f.store.IsActive(t.Context(), 17, artifactDeleteTestWork, generation); err != nil || !active {
		t.Fatalf("prepared generation inactive=%v err=%v", !active, err)
	}
}
func currentArtifactDelete(t *testing.T, f *stopFixture) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, f.worker.URL+"/downloads/artifacts/"+artifactDeleteTestWork, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := nodehttp.Client(&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("current artifact delete=%d", resp.StatusCode)
	}
	<-f.stops
	for _, path := range []string{preparedArtifactPath(f), preparedArtifactPath(f) + ".receipt.json"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("current deletion retained%s: %v", path, err)
		}
	}
}

func TestDelayedPreparedArtifactDeleteCannotRemoveNewerGeneration(t *testing.T) {
	f := newStopFixture(t)
	old, err := f.prepareArtifact(t.Context(), "old-ready", 1000)
	if err != nil {
		t.Fatal(err)
	}
	delayed := delayedArtifactDelete(t, f, old)
	next, err := f.prepareArtifact(t.Context(), "new-ready", 2000)
	if err != nil || next <= old {
		t.Fatalf("successor generation=%d err=%v", next, err)
	}
	deliverArtifactDelete(t, f, delayed.Clone(t.Context()), http.StatusNoContent)
	assertPreparedArtifact(t, f, "new-ready", next)
	deliverArtifactDelete(t, f, delayed.Clone(t.Context()), http.StatusUnauthorized)
	assertPreparedArtifact(t, f, "new-ready", next)
	currentArtifactDelete(t, f)
}

func TestPreparedArtifactDeleteChecksPendingDuringPublicationPromotionGap(t *testing.T) {
	f := newStopFixture(t)
	old, err := f.prepareArtifact(t.Context(), "old-ready", 1000)
	if err != nil {
		t.Fatal(err)
	}
	delayed := delayedArtifactDelete(t, f, old)
	published, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	handler := f.runtime.handler
	f.runtime.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		if r.Method == http.MethodPost && r.URL.Path == "/downloads/prepare" {
			close(published)
			<-release
		}
	})
	completed := make(chan error, 1)
	go func() { _, err := f.prepareArtifact(t.Context(), "new-ready", 2000); completed <- err }()
	select {
	case <-published:
	case <-time.After(3 * time.Second):
		t.Fatal("new artifact was not published")
	}
	f.runtime.mu.Lock()
	current := f.runtime.generations[artifactDeleteTestWork]
	pending := len(f.runtime.staged[artifactDeleteTestWork])
	f.runtime.mu.Unlock()
	if current > old || pending != 1 {
		t.Fatalf("fixture missed committed-byte/local-promotion gap: current=%d pending=%d", current, pending)
	}
	deliverArtifactDelete(t, f, delayed, http.StatusNoContent)
	raw, err := os.ReadFile(preparedArtifactPath(f))
	if err != nil || string(raw) != "new-ready" {
		t.Fatalf("delete during publication gap removed successor: %s %v", raw, err)
	}
	releaseOnce()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("prepared promotion did not finish")
	}
	currentArtifactDelete(t, f)
}

func TestPreparedArtifactDeleteRefreshesRetainedScratchAfterRestart(t *testing.T) {
	f := newStopFixture(t)
	old, err := f.prepareArtifact(t.Context(), "old-ready", 1000)
	if err != nil {
		t.Fatal(err)
	}
	delayed := delayedArtifactDelete(t, f, old)
	next, err := f.prepareArtifact(t.Context(), "new-ready", 2000)
	if err != nil {
		t.Fatal(err)
	}
	options := f.runtime.options
	address := strings.TrimPrefix(f.worker.URL, "http://")
	f.worker.Close()
	if err := f.runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close(context.Background()) })
	server := httptest.NewUnstartedServer(nil)
	_ = server.Listener.Close()
	server.Listener, err = net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed := &stopStatusWriter{ResponseWriter: w}
		restarted.Handler().ServeHTTP(observed, r)
		if r.Method == http.MethodDelete {
			f.stops <- stopObservation{r.Header.Get(workeridentity.HeaderDomain), r.Header.Get(workeridentity.HeaderWorkID), r.Header.Get(workeridentity.HeaderGeneration), observed.status}
		}
	})
	server.Start()
	t.Cleanup(server.Close)
	f.runtime, f.worker = restarted, server
	if current := restarted.generations[artifactDeleteTestWork]; current != 0 {
		t.Fatal("restart retained in-memory generation")
	}
	deliverArtifactDelete(t, f, delayed, http.StatusNoContent)
	assertPreparedArtifact(t, f, "new-ready", next)
	// Core has already fenced the current work before this unknown-local-state
	// delete arrives; a fresh empty snapshot must still allow its cleanup.
	currentArtifactDelete(t, f)
}

func TestPreparedArtifactDeleteExpiresWhileWaitingWithKnownOwnership(t *testing.T) {
	f := newStopFixture(t)
	// Disable only this fixture's asynchronous expiry callback. The signed
	// snapshot still expires normally, while local generation ownership stays
	// known so the callback's freshness decision is independent of scheduling.
	f.runtime.authority.Close()
	authority, err := workerwire.NewLocalAuthority(17, f.runtime.bootID, nonce(), nil)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.authority = authority
	generation, err := f.prepareArtifact(t.Context(), "ready", 1000)
	if err != nil {
		t.Fatal(err)
	}
	delayed := delayedArtifactDelete(t, f, generation)
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	handler := f.runtime.handler
	f.runtime.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			close(entered)
			<-release
		}
		handler.ServeHTTP(w, r)
	})
	result := make(chan int, 1)
	go func() {
		resp, err := http.DefaultClient.Do(delayed)
		if err != nil {
			result <- 0
			return
		}
		_ = resp.Body.Close()
		result <- resp.StatusCode
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("delete did not reach lifecycle wait")
	}
	f.runtime.mu.Lock()
	known := f.runtime.generations[artifactDeleteTestWork]
	f.runtime.mu.Unlock()
	if known != generation {
		t.Fatal("fixture missed known current ownership")
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, fresh := f.runtime.authority.ObservedGeneration(artifactDeleteTestWork); !fresh {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("signed authority did not expire")
		}
	}
	f.runtime.mu.Lock()
	stillKnown := f.runtime.generations[artifactDeleteTestWork]
	f.runtime.mu.Unlock()
	if stillKnown != generation {
		t.Fatal("expiry fixture lost known local generation")
	}
	releaseOnce()
	select {
	case status := <-result:
		if status != http.StatusServiceUnavailable {
			t.Fatalf("expired delete status=%d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("expired delete did not finish")
	}
	<-f.stops
	if raw, err := os.ReadFile(preparedArtifactPath(f)); err != nil || string(raw) != "ready" {
		t.Fatalf("expired delete removed retained artifact: %s err=%v", raw, err)
	}
}

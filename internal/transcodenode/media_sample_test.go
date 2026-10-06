package transcodenode

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

func speechRequestBody(t *testing.T, mutate func(*mediasample.Request)) []byte {
	t.Helper()
	req := mediasample.Request{
		Input:  "/media/film.mkv",
		Window: &mediasample.Window{StartSeconds: 60, DurationSeconds: 0.05},
		Audio:  &mediasample.AudioOutput{Speech: &mediasample.SpeechParams{}},
	}
	if mutate != nil {
		mutate(&req)
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func postMediaSample(server *Server, body []byte, authorized bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, mediasample.RemotePath, bytes.NewReader(body))
	if authorized {
		req.Header.Set("Authorization", "Bearer "+testSecret)
	}
	rec := httptest.NewRecorder()
	server.router().ServeHTTP(rec, req)
	return rec
}

func TestMediaSampleRunsSpeechRequest(t *testing.T) {
	server := newTestServer(t)
	// A stand-in ffmpeg that writes 5 frames of full-scale samples.
	samples := make([]byte, 2*400)
	for i := 0; i < len(samples); i += 2 {
		binary.LittleEndian.PutUint16(samples[i:], 20000)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pcm"), samples, 0o644); err != nil {
		t.Fatal(err)
	}
	ffmpeg := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\ncat "+filepath.Join(dir, "pcm")+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpeg

	rec := postMediaSample(server, speechRequestBody(t, nil), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var result mediasample.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Speech == nil || len(result.Speech.Levels) != 5 || result.Speech.StartSeconds != 60 {
		t.Fatalf("speech %+v", result.Speech)
	}
}

func TestMediaSampleRefusals(t *testing.T) {
	server := newTestServer(t)
	cases := map[string]struct {
		body       []byte
		authorized bool
		paths      InputPathAuthorizer
		status     int
		reason     mediasample.Reason
	}{
		"unauthorized": {body: speechRequestBody(t, nil), status: http.StatusUnauthorized},
		"invalid json": {body: []byte("{"), authorized: true, status: http.StatusBadRequest, reason: mediasample.ReasonInvalidRequest},
		"invalid request": {body: speechRequestBody(t, func(r *mediasample.Request) { r.Window = nil }), authorized: true,
			status: http.StatusBadRequest, reason: mediasample.ReasonInvalidRequest},
		"hardware": {body: speechRequestBody(t, func(r *mediasample.Request) {
			r.Audio = nil
			r.Window.KeyframesOnly = true
			r.Stats = &mediasample.StatsOutput{CropWidth: 1, CropHeight: 1, Width: 64}
			r.Attempts = []mediasample.Attempt{{Hardware: true}}
		}), authorized: true, status: http.StatusBadRequest, reason: mediasample.ReasonInvalidRequest},
		"unapproved path": {body: speechRequestBody(t, nil), authorized: true, paths: denyInputPaths{}, status: http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server.inputPaths = allowInputPaths{}
			if tc.paths != nil {
				server.inputPaths = tc.paths
			}
			rec := postMediaSample(server, tc.body, tc.authorized)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.reason != "" {
				var failure mediasample.RemoteFailure
				if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil || failure.Reason != tc.reason {
					t.Fatalf("failure %+v %v", failure, err)
				}
			}
		})
	}
}

func TestMediaSampleReportsSamplingFailure(t *testing.T) {
	server := newTestServer(t)
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\necho 'Stream map 0:a:0 matches no streams.' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpeg
	rec := postMediaSample(server, speechRequestBody(t, nil), true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	// The client turns the refusal into a RemoteError the caller can classify.
	node := httptest.NewServer(server.router())
	defer node.Close()
	_, err := mediasample.RemoteClient{}.Run(context.Background(), node.URL+mediasample.RemotePath, testSecret,
		mediasample.Request{Input: "/m.mkv", Window: &mediasample.Window{DurationSeconds: 1}, Audio: &mediasample.AudioOutput{Speech: &mediasample.SpeechParams{}}})
	var remoteErr *mediasample.RemoteError
	if !errors.As(err, &remoteErr) || remoteErr.Status != http.StatusUnprocessableEntity || remoteErr.Infrastructure() {
		t.Fatalf("remote error %#v", err)
	}
	if remoteErr.Reason != mediasample.ReasonNoStream {
		t.Fatalf("reason %q", remoteErr.Reason)
	}
}

func TestMediaSampleRefusalsClassifyForFallback(t *testing.T) {
	server := newTestServer(t)
	node := httptest.NewServer(server.router())
	defer node.Close()
	run := func(req mediasample.Request) *mediasample.RemoteError {
		_, err := mediasample.RemoteClient{}.Run(context.Background(), node.URL+mediasample.RemotePath, testSecret, req)
		var remoteErr *mediasample.RemoteError
		if !errors.As(err, &remoteErr) {
			t.Fatalf("error %#v", err)
		}
		return remoteErr
	}
	speech := mediasample.Request{Input: "/m.mkv", Window: &mediasample.Window{DurationSeconds: 1}, Audio: &mediasample.AudioOutput{Speech: &mediasample.SpeechParams{}}}

	// A node that cannot read the file is another node's problem, not the file's.
	server.inputPaths = denyInputPaths{}
	if refused := run(speech); refused.Status != http.StatusBadRequest || !refused.Infrastructure() {
		t.Fatalf("unapproved path: %#v", refused)
	}
	// A malformed request fails the same way anywhere.
	server.inputPaths = allowInputPaths{}
	bad := speech
	bad.Window = nil
	if refused := run(bad); refused.Status != http.StatusBadRequest || refused.Infrastructure() {
		t.Fatalf("invalid request: %#v", refused)
	}
}

func TestMediaSampleAdmitsConfiguredCapacity(t *testing.T) {
	server := newTestServer(t)
	server.watcher.Config().Playback.SubtitleSyncNodeCapacity = 1
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	// Each run records its start, then holds until released.
	script := "#!/bin/sh\nmktemp -p " + dir + " start.XXXXXX >/dev/null\nwhile [ ! -f " + dir + "/release ]; do sleep 0.01; done\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpeg
	started := func() int {
		matches, _ := filepath.Glob(filepath.Join(dir, "start.*"))
		return len(matches)
	}
	done := make(chan int, 2)
	for range 2 {
		go func() { done <- postMediaSample(server, speechRequestBody(t, nil), true).Code }()
	}
	deadline := time.Now().Add(10 * time.Second)
	for started() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // a second admitted run would have started by now
	if n := started(); n != 1 {
		t.Fatalf("%d runs started with capacity 1", n)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if code := <-done; code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
	}
	if n := started(); n != 2 {
		t.Fatalf("%d runs after release, want 2", n)
	}
}

func TestMediaSampleRefusesWhenNoSlotFreesUp(t *testing.T) {
	server := newTestServer(t)
	server.watcher.Config().Playback.SubtitleSyncNodeCapacity = 1
	server.mediaSampleWait = 50 * time.Millisecond
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nmktemp -p " + dir + " start.XXXXXX >/dev/null\nwhile [ ! -f " + dir + "/release ]; do sleep 0.01; done\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpeg
	first := make(chan int, 1)
	go func() { first <- postMediaSample(server, speechRequestBody(t, nil), true).Code }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if matches, _ := filepath.Glob(filepath.Join(dir, "start.*")); len(matches) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The slot is held: the next request waits its admission time, then is
	// refused as unavailable, which callers may run elsewhere.
	rec := postMediaSample(server, speechRequestBody(t, nil), true)
	var failure mediasample.RemoteFailure
	if rec.Code != http.StatusServiceUnavailable || json.Unmarshal(rec.Body.Bytes(), &failure) != nil || failure.Reason != mediasample.ReasonNodeUnavailable {
		t.Fatalf("over capacity: %d %s", rec.Code, rec.Body)
	}

	// Neither the refusal nor the finished run keeps a slot.
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := <-first; code != http.StatusOK {
		t.Fatalf("first run: %d", code)
	}
	if code := postMediaSample(server, speechRequestBody(t, nil), true).Code; code != http.StatusOK {
		t.Fatalf("after release: %d", code)
	}
}

package transcodenode

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// mediaSampleInput is the input, and the grant, of speechRequestBody.
const mediaSampleInput = "/media/film.mkv"

func speechRequestBody(t *testing.T, mutate func(*mediasample.Request)) []byte {
	t.Helper()
	req := mediasample.Request{
		Input:       mediaSampleInput,
		SourceGrant: mediaSampleInput,
		Window:      &mediasample.Window{StartSeconds: 60, DurationSeconds: 0.05},
		Audio:       &mediasample.AudioOutput{Speech: &mediasample.SpeechParams{}},
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

func postMediaSample(t *testing.T, server *Server, body []byte, authorized bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, mediasample.RemotePath, bytes.NewReader(body))
	if authorized {
		nodeauthtest.Sign(t, req, testNodeKey)
	}
	rec := httptest.NewRecorder()
	server.router().ServeHTTP(rec, req)
	return rec
}

func TestMediaSampleRefusals(t *testing.T) {
	server := newTestServer(t)
	cases := map[string]struct {
		body       []byte
		authorized bool
		refuse     error
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
		"refused grant": {body: speechRequestBody(t, nil), authorized: true, refuse: &nodesource.StatusError{Op: "open", Status: http.StatusForbidden},
			status: http.StatusBadRequest, reason: mediasample.ReasonNodeUnavailable},
		"mount mismatch": {body: speechRequestBody(t, nil), authorized: true, refuse: &nodesource.MountMismatchError{Path: "/m.mkv", NodeErr: os.ErrNotExist},
			status: http.StatusConflict, reason: mediasample.ReasonNodeUnavailable},
		"core unreachable": {body: speechRequestBody(t, nil), authorized: true, refuse: errors.New("connection refused"),
			status: http.StatusServiceUnavailable, reason: mediasample.ReasonNodeUnavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server.opener = &pathSources{}
			if tc.refuse != nil {
				sourcesOf(server).refuse(mediaSampleInput, tc.refuse)
			}
			rec := postMediaSample(t, server, tc.body, tc.authorized)
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
	go func() { first <- postMediaSample(t, server, speechRequestBody(t, nil), true).Code }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if matches, _ := filepath.Glob(filepath.Join(dir, "start.*")); len(matches) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The slot is held: the next request waits its admission time, then is
	// refused as unavailable, which callers may run elsewhere.
	rec := postMediaSample(t, server, speechRequestBody(t, nil), true)
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
	if code := postMediaSample(t, server, speechRequestBody(t, nil), true).Code; code != http.StatusOK {
		t.Fatalf("after release: %d", code)
	}
}

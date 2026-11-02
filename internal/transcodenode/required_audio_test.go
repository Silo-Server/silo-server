package transcodenode

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
)

func TestHandleStartRetainsRequiredAudio(t *testing.T) {
	server := newTestServer(t)
	server.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
	useFakeFFmpeg(t, server, "#!/bin/sh\nwhile :; do sleep 0.1; done\n")
	response := postStartRequest(t, server, TranscodeStartRequest{SessionID: "required-audio", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "copy", AudioTrackIndex: 1, RequireAudioTrack: true, SegmentDuration: 2})
	if response.Code != http.StatusAccepted {
		t.Fatalf("start status=%d: %s", response.Code, response.Body.String())
	}
	var started TranscodeStartResponse
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if !started.RequireAudioTrack {
		t.Fatal("worker did not confirm mandatory audio mapping")
	}
	server.mu.RLock()
	session := server.sessions["required-audio"]
	server.mu.RUnlock()
	if session == nil {
		t.Fatal("start did not register session")
	}
	defer func() { _ = session.CloseProcess() }()
	opts := session.Opts()
	if !opts.RequireAudioTrack || opts.AudioTrackIndex != 1 {
		t.Fatal("node start lost required audio selection")
	}
}

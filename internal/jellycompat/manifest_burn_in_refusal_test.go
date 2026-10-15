package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodepool/nodepooltest"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// remoteTextBurnInRefusal is what a node reading its source through the core
// answers a text subtitle burn-in it cannot run.
const remoteTextBurnInRefusal = "text subtitle burn-in needs local source access on this node"

// refusingTextBurnInNode is a transcode node that refuses every text burn-in
// start as a core-access node does, counting the refusals.
func refusingTextBurnInNode(t *testing.T, refusals *atomic.Int32) *httptest.Server {
	t.Helper()
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			var request transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.SubtitleBurnIn || request.SubtitleCodec != "subrip" {
				t.Errorf("refusing node start = %+v, %v; want a text burn-in", request, err)
			}
			refusals.Add(1)
			http.Error(w, remoteTextBurnInRefusal, http.StatusUnprocessableEntity)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(node.Close)
	return node
}

// newManifestBurnInHandler is a compat handler whose play session transcodes
// an SDR source burning in a text subtitle, routed over nodeURLs in order.
func newManifestBurnInHandler(t *testing.T, localFallback bool, nodeURLs ...string) (*PlaybackHandler, *testCompatSessionManager, PlaybackMediaSource) {
	t.Helper()
	mediaPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{ID: 42, FilePath: mediaPath, Bitrate: 8_000, VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}}
	version := testCompatVersion()
	version.FileID = file.ID
	version.Resolution = "1080p"
	source := testCompatSource(NewResourceIDCodec(), version)
	source.SupportsDirectPlay = false
	source.SupportsDirectStream = false
	source.SubtitleBurnIn, source.SubtitleTrackIndex, source.SubtitleCodec = true, 0, "subrip"

	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "compat-token", RouteItemID: "item", MediaSources: []PlaybackMediaSource{source},
		UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode",
	})
	sessionMgr := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {
			ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: file.ID,
			PlayMethod: playback.PlayTranscode, BasePlayMethod: playback.PlayTranscode,
		},
	}}
	transcodes := nodepooltest.NewTranscodePool()
	var nodes []*nodepool.Node
	for index, nodeURL := range nodeURLs {
		nodes = append(nodes, &nodepool.Node{ID: index + 1, URL: nodeURL, Enabled: true, Healthy: true, ActiveJobs: index})
	}
	transcodes.SetNodes(nodes)
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{ID: 9, URL: "https://proxy.example", Enabled: true, Healthy: true}})
	handler := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    sessionMgr,
		fileResolver:  testCompatFileResolver{file: file},
		NodePlanner:   nodepool.NewPlanner(proxies, transcodes),
		JWTSecret:     "test-secret",
		tm:            playback.NewTranscodeManager(),
	}
	withTestNodeKeys(handler)
	if !localFallback {
		requireCompatWorkerRouting(handler)
	}
	return handler, sessionMgr, source
}

func serveCompatMasterManifest(handler *PlaybackHandler, source PlaybackMediaSource) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/Videos/item/master.m3u8?PlaySessionId=play-1&MediaSourceId="+source.ID, nil)
	request = request.WithContext(context.WithValue(request.Context(), compatSessionKey, &Session{Token: "compat-token", StreamAppUserID: 7, ProfileID: "profile-1"}))
	recorder := httptest.NewRecorder()
	handler.HandleMasterManifest(recorder, request)
	return recorder
}

// A node that refuses a text subtitle burn-in because it reads its source
// through the core is excluded like any failed start: the session moves to
// the next node.
func TestHandleMasterManifestMovesATextBurnInRefusalToTheNextNode(t *testing.T) {
	var refusals atomic.Int32
	refusing := refusingTextBurnInNode(t, &refusals)
	var accepted atomic.Int32
	accepting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			var request transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.SubtitleBurnIn {
				t.Errorf("accepting node start = %+v, %v", request, err)
			}
			accepted.Add(1)
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: request.SessionID, Status: "started", HWAccel: request.HWAccel, ThrottleSeconds: request.ThrottleSeconds})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(accepting.Close)

	handler, sessionMgr, source := newManifestBurnInHandler(t, false, refusing.URL, accepting.URL)
	recorder := serveCompatMasterManifest(handler, source)

	if recorder.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307; body = %s", recorder.Code, recorder.Body.String())
	}
	if refusals.Load() != 1 || accepted.Load() != 1 {
		t.Fatalf("refusals = %d, accepted starts = %d; want one of each", refusals.Load(), accepted.Load())
	}
	if got := sessionMgr.sessions["upstream-1"].TranscodeNodeURL; got != accepting.URL {
		t.Fatalf("session transcode node = %q, want %q", got, accepting.URL)
	}
}

// With local execution allowed, a refused text burn-in falls back to the API
// server, which burns the subtitle in itself.
func TestHandleMasterManifestBurnsInLocallyAfterATextBurnInRefusal(t *testing.T) {
	previousTimeout := compatManifestStartupTimeout
	compatManifestStartupTimeout = 5 * time.Second
	t.Cleanup(func() { compatManifestStartupTimeout = previousTimeout })

	var refusals atomic.Int32
	refusing := refusingTextBurnInNode(t, &refusals)
	handler, sessionMgr, source := newManifestBurnInHandler(t, true, refusing.URL)
	handler.HWAccel = playback.HWAccelNone
	handler.TranscodeDir = t.TempDir()
	scratch := t.TempDir()
	argv := filepath.Join(scratch, "argv")
	handler.FFmpegPath = filepath.Join(scratch, "ffmpeg")
	ffmpegScript := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + argv + "\n" +
		"out=\"\"\n" +
		"for arg in \"$@\"; do case \"$arg\" in *.m3u8) out=\"$(dirname \"$arg\")\";; esac; done\n" +
		"[ -n \"$out\" ] || exit 0\n" +
		"mkdir -p \"$out\"\n" +
		"for name in seg_00000.ts seg_00001.ts seg_00002.ts; do printf segment > \"$out/$name\"; done\n" +
		"printf '#EXTM3U\\n#EXT-X-TARGETDURATION:2\\n#EXT-X-MEDIA-SEQUENCE:0\\n#EXTINF:2,\\nseg_00000.ts\\n#EXTINF:2,\\nseg_00001.ts\\n#EXTINF:2,\\nseg_00002.ts\\n' > \"$out/stream.m3u8\"\n" +
		"exec sleep 30\n"
	if err := os.WriteFile(handler.FFmpegPath, []byte(ffmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}

	recorder := serveCompatMasterManifest(handler, source)
	t.Cleanup(func() { handler.tm.CloseTranscodeSession("upstream-1", "") })

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	if refusals.Load() != 1 {
		t.Fatalf("refusals = %d, want the node tried once", refusals.Load())
	}
	if got := sessionMgr.sessions["upstream-1"].TranscodeNodeURL; got != "" {
		t.Fatalf("session transcode node = %q, want the API server", got)
	}
	runs, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runs), "subtitles=") {
		t.Fatalf("local transcode did not burn the subtitle in: %s", runs)
	}
}

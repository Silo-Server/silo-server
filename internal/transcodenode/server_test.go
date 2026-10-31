package transcodenode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/mediaprobe"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodeproxy"
)

const testSecret = "node-reconstruct-test-secret"

type allowInputPaths struct{}

func (allowInputPaths) Allowed(context.Context, string) (bool, error) {
	return true, nil
}

type blockingSessionTracker struct {
	trackStarted  chan struct{}
	trackRelease  chan struct{}
	removeStarted chan struct{}

	mu                sync.Mutex
	events            []string
	tracked           nodesessions.SessionInfo
	trackHasDeadline  bool
	removeHasDeadline bool
}

type recordingSessionTracker struct {
	mu      sync.Mutex
	tracked []nodesessions.SessionInfo
	removed []string
}

func (t *recordingSessionTracker) Track(_ context.Context, info nodesessions.SessionInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tracked = append(t.tracked, info)
}

func (t *recordingSessionTracker) Remove(_ context.Context, sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.removed = append(t.removed, sessionID)
}

func (*recordingSessionTracker) Cleanup(context.Context) {}
func (*recordingSessionTracker) NodeURL() string         { return "http://node" }
func (*recordingSessionTracker) NodeName() string        { return "node" }

type blockingResponseWriter struct {
	header       http.Header
	writeStarted chan struct{}
	releaseWrite chan struct{}
	startOnce    sync.Once
}

type abortableBlockingResponseWriter struct {
	header          http.Header
	writeStarted    chan struct{}
	deadlineExpired chan struct{}
	startOnce       sync.Once
	expireOnce      sync.Once
}

func newAbortableBlockingResponseWriter() *abortableBlockingResponseWriter {
	return &abortableBlockingResponseWriter{
		header:          make(http.Header),
		writeStarted:    make(chan struct{}),
		deadlineExpired: make(chan struct{}),
	}
}

func (w *abortableBlockingResponseWriter) Header() http.Header { return w.header }
func (*abortableBlockingResponseWriter) WriteHeader(int)       {}

func (w *abortableBlockingResponseWriter) Write([]byte) (int, error) {
	w.startOnce.Do(func() { close(w.writeStarted) })
	<-w.deadlineExpired
	return 0, os.ErrDeadlineExceeded
}

func (w *abortableBlockingResponseWriter) SetWriteDeadline(deadline time.Time) error {
	if !deadline.After(time.Now()) {
		w.expireOnce.Do(func() { close(w.deadlineExpired) })
	}
	return nil
}

func newBlockingResponseWriter() *blockingResponseWriter {
	return &blockingResponseWriter{
		header:       make(http.Header),
		writeStarted: make(chan struct{}),
		releaseWrite: make(chan struct{}),
	}
}

func (w *blockingResponseWriter) Header() http.Header { return w.header }
func (*blockingResponseWriter) WriteHeader(int)       {}
func (w *blockingResponseWriter) Write(p []byte) (int, error) {
	w.startOnce.Do(func() { close(w.writeStarted) })
	<-w.releaseWrite
	return len(p), nil
}

func newBlockingSessionTracker() *blockingSessionTracker {
	return &blockingSessionTracker{
		trackStarted:  make(chan struct{}),
		trackRelease:  make(chan struct{}),
		removeStarted: make(chan struct{}),
	}
}

func (t *blockingSessionTracker) Track(ctx context.Context, info nodesessions.SessionInfo) {
	_, hasDeadline := ctx.Deadline()
	t.mu.Lock()
	t.trackHasDeadline = hasDeadline
	t.tracked = info
	t.events = append(t.events, "track")
	t.mu.Unlock()
	close(t.trackStarted)
	<-t.trackRelease
	t.mu.Lock()
	t.events = append(t.events, "track-done")
	t.mu.Unlock()
}

func (t *blockingSessionTracker) Remove(ctx context.Context, _ string) {
	_, hasDeadline := ctx.Deadline()
	t.mu.Lock()
	t.removeHasDeadline = hasDeadline
	t.events = append(t.events, "remove")
	t.mu.Unlock()
	close(t.removeStarted)
}

func (*blockingSessionTracker) Cleanup(context.Context) {}
func (*blockingSessionTracker) NodeURL() string         { return "http://node" }
func (*blockingSessionTracker) NodeName() string        { return "node" }

// newTestServer builds a transcode Server whose config carries a known JWT secret
// so reconstructFromToken can verify forwarded stream tokens. The tracker is left
// nil: the guard-rejection cases never reach the spawn/track path.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	w := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = testSecret
	cfg.Playback.TranscodeDir = t.TempDir()
	cfg.Download.ArtifactDir = filepath.Join(cfg.Playback.TranscodeDir, downloadprepare.ArtifactDirectoryName)
	w.SetConfigForTest(cfg)
	return &Server{
		watcher:                   w,
		inputPaths:                allowInputPaths{},
		transcodeDir:              cfg.Playback.TranscodeDir,
		artifactRoot:              filepath.Join(cfg.Playback.TranscodeDir, downloadprepare.ArtifactDirectoryName),
		sessions:                  make(map[string]*playback.TranscodeSession),
		progressiveRemuxes:        make(map[string]progressiveRemuxRequest),
		stoppedProgressiveRemuxes: make(map[string]time.Time),
		lastAccess:                make(map[string]time.Time),
	}
}

func nodeToneMapTrack() models.VideoTrack {
	return models.VideoTrack{
		Codec: "hevc", Profile: "Main 10", BitDepth: 10, PixelFormat: "yuv420p10le",
		ColorRange: "tv", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
	}
}

func writeNodeToneMapFFprobe(t *testing.T, ffmpegPath string, track models.VideoTrack) {
	t.Helper()
	stream := map[string]any{
		"index": 0, "codec_name": track.Codec, "codec_type": "video", "profile": track.Profile,
		"level": track.Level, "width": track.Width, "height": track.Height, "avg_frame_rate": track.FrameRate,
		"pix_fmt": track.PixelFormat, "bits_per_raw_sample": track.BitDepth, "color_range": track.ColorRange,
		"color_primaries": track.ColorPrimaries, "color_transfer": track.ColorTransfer, "color_space": track.ColorSpace,
	}
	output, err := json.Marshal(map[string]any{"streams": []any{stream}})
	if err != nil {
		t.Fatal(err)
	}
	probePath := mediaprobe.FFprobePathFromFFmpeg(ffmpegPath)
	script := "#!/bin/sh\nprintf '%s' '" + strings.ReplaceAll(string(output), "'", "'\\''") + "'\n"
	if err := os.WriteFile(probePath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// useFakeFFmpeg installs script as the server's FFmpeg binary and returns its
// path.
func useFakeFFmpeg(t *testing.T, server *Server, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = path
	return path
}

// toneMapFFmpegScript is a fake FFmpeg that advertises software tone mapping,
// passes its smoke encode, and otherwise runs body.
func toneMapFFmpegScript(body string) string {
	return "#!/bin/sh\n" +
		"case \" $* \" in\n" +
		"  *\" -filters \"*) printf ' T.. zscale V->V scale\\n T.. tonemapx V->V tone-map\\n T.. sidedata V->V metadata\\n'; exit 0;;\n" +
		"  *\" -encoders \"*) printf ' V..... libx264 H.264\\n'; exit 0;;\n" +
		"  *\" -f null \"*) exit 0;;\n" +
		"esac\n" + body
}

// useToneMapFFmpeg installs toneMapFFmpegScript(body) with software encoding
// and an FFprobe that reports nodeToneMapTrack.
func useToneMapFFmpeg(t *testing.T, server *Server, body string) {
	t.Helper()
	ffmpegPath := useFakeFFmpeg(t, server, toneMapFFmpegScript(body))
	server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
	writeNodeToneMapFFprobe(t, ffmpegPath, nodeToneMapTrack())
}

// nodeToneMapRevision is the source revision of a size-byte file whose only
// video track is nodeToneMapTrack.
func nodeToneMapRevision(size int64) tonemap.SourceRevision {
	return tonemap.RevisionForFile(&models.MediaFile{ID: 1, FileSize: size, VideoTracks: []models.VideoTrack{nodeToneMapTrack()}})
}

// toneMapPrepareRequest is a software tone-mapped H.264 download of inputPath.
func toneMapPrepareRequest(artifactID, inputPath string, revision tonemap.SourceRevision) downloadprepare.Request {
	return downloadprepare.Request{
		ArtifactID: artifactID, InputPath: inputPath,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", AudioTrackIndex: -1,
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: revision,
	}
}

// writeNodeSource writes content as a media source and returns its path and
// size.
func writeNodeSource(t *testing.T, content string) (string, int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.mkv")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, int64(len(content))
}

// writeArtifact writes content as the artifact file name under the server's
// artifact root and returns its path.
func writeArtifact(t *testing.T, server *Server, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(server.artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(server.artifactRoot, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// blockReceiptRemoval puts a non-empty directory where outputPath's receipt
// belongs, so invalidating the receipt fails.
func blockReceiptRemoval(t *testing.T, outputPath string) {
	t.Helper()
	receiptPath := outputPath + ".receipt.json"
	if err := os.Mkdir(receiptPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(receiptPath, "blocks-removal"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func jsonRequest(t *testing.T, path string, body any) *http.Request {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
}

func postStartRequest(t *testing.T, server *Server, body any) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.handleStart(recorder, jsonRequest(t, "/transcode/start", body))
	return recorder
}

func postPrepare(t *testing.T, server *Server, body any) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.handleDownloadPrepare(recorder, jsonRequest(t, "/downloads/prepare", body))
	return recorder
}

// startRegisteredSession starts request, requires 202, and returns the
// response with the session it registered. The session is closed on cleanup.
func startRegisteredSession(t *testing.T, server *Server, request TranscodeStartRequest) (*httptest.ResponseRecorder, *playback.TranscodeSession) {
	t.Helper()
	recorder := postStartRequest(t, server, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body.String())
	}
	server.mu.RLock()
	session := server.sessions[request.SessionID]
	server.mu.RUnlock()
	if session == nil {
		t.Fatal("session was not registered")
	}
	t.Cleanup(func() { _ = session.CloseProcess() })
	return recorder, session
}

// progressiveRemuxClaims is a stream token for a progressive remux that node 11
// executes.
func progressiveRemuxClaims(sessionID, mediaPath, transportID string) streamtoken.Claims {
	return streamtoken.Claims{
		SessionID: sessionID, MediaPath: mediaPath, PlayMethod: string(playback.PlayRemux),
		TranscodeNode: "http://node", TranscodeTransportID: transportID,
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 11, RoutingEgress: string(noderouting.EgressProxy),
	}
}

// remuxRequest is a progressive remux request for the claims' transport that
// carries the signed claims as its stream token.
func remuxRequest(t *testing.T, method string, claims streamtoken.Claims) *http.Request {
	t.Helper()
	token, err := streamtoken.Sign(claims, testSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := withNodeRouteParams(httptest.NewRequest(method, "/remux/"+claims.TranscodeTransportID, nil),
		map[string]string{"session_id": claims.TranscodeTransportID})
	request.Header.Set("X-Silo-Stream-Token", token)
	return request
}

func stopRequest(id string) *http.Request {
	return withNodeRouteParams(httptest.NewRequest(http.MethodDelete, "/transcode/"+id, nil), map[string]string{"session_id": id})
}

func TestWriteToneMapRecipeErrorClassifiesLiveValidation(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "stale metadata", err: tonemap.ErrSourceRevisionChanged, wantStatus: http.StatusUnprocessableEntity, wantCode: ToneMapSourceRevisionChangedCode},
		{name: "probe unavailable", err: playback.ErrToneMapSourceValidationUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: ToneMapSourceValidationUnavailableCode},
		{name: "executor unavailable", err: playback.ErrToneMapExecutorUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: ToneMapExecutorUnavailableCode},
		{name: "preflight rejected", err: tonemap.ErrSourcePreflightRejected, wantStatus: http.StatusUnprocessableEntity, wantCode: "source_preflight_rejected"},
		{name: "generic capability timeout", err: context.DeadlineExceeded, wantStatus: http.StatusServiceUnavailable},
		{name: "generic recipe rejection", err: errors.New("unsupported recipe"), wantStatus: http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeToneMapRecipeError(recorder, tt.err)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			if got := recorder.Header().Get(ToneMapExecutionErrorHeader); got != tt.wantCode {
				t.Fatalf("classification header = %q, want %q", got, tt.wantCode)
			}
		})
	}
}

func TestToneMapExecutionEndpointsRejectLiveMetadataBeforeFFmpeg(t *testing.T) {
	for _, validation := range []struct {
		name       string
		probeBody  string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "stale metadata",
			probeBody:  `printf '%s' '{"streams":[{"index":0,"codec_name":"hevc","codec_type":"video","profile":"Main","pix_fmt":"yuv420p10le","bits_per_raw_sample":10,"color_range":"tv","color_primaries":"bt2020","color_transfer":"smpte2084","color_space":"bt2020nc"}]}'`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   ToneMapSourceRevisionChangedCode,
		},
		{name: "probe unavailable", probeBody: "exit 1", wantStatus: http.StatusServiceUnavailable, wantCode: ToneMapSourceValidationUnavailableCode},
	} {
		t.Run(validation.name, func(t *testing.T) {
			for _, endpoint := range []string{"start", "download"} {
				t.Run(endpoint, func(t *testing.T) {
					server := newTestServer(t)
					inputPath, size := writeNodeSource(t, "source")
					ffmpegMarker := filepath.Join(t.TempDir(), "ffmpeg-ran")
					ffmpegPath := useFakeFFmpeg(t, server, "#!/bin/sh\n"+
						"case \" $* \" in\n"+
						"  *\" -filters \"*) printf ' T.. zscale V->V scale\\n T.. tonemapx V->V tone-map\\n T.. sidedata V->V metadata\\n'; exit 0;;\n"+
						"  *\" -encoders \"*) printf ' V..... libx264 H.264\\n'; exit 0;;\n"+
						"esac\n"+
						"case \" $* \" in *\" -i "+inputPath+" \"*) touch '"+ffmpegMarker+"';; esac\nexit 0\n")
					probePath := mediaprobe.FFprobePathFromFFmpeg(ffmpegPath)
					if err := os.WriteFile(probePath, []byte("#!/bin/sh\n"+validation.probeBody+"\n"), 0o755); err != nil {
						t.Fatal(err)
					}
					server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
					revision := nodeToneMapRevision(size)

					var recorder *httptest.ResponseRecorder
					if endpoint == "start" {
						recorder = postStartRequest(t, server, TranscodeStartRequest{
							SessionID: "metadata-guard", InputPath: inputPath,
							TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
							ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
							ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
							ToneMapSourceRevision: revision,
						})
					} else {
						recorder = postPrepare(t, server, toneMapPrepareRequest("metadata-guard", inputPath, revision))
					}
					if recorder.Code != validation.wantStatus {
						t.Fatalf("status = %d, want %d; body = %s", recorder.Code, validation.wantStatus, recorder.Body.String())
					}
					if got := recorder.Header().Get(ToneMapExecutionErrorHeader); got != validation.wantCode {
						t.Fatalf("classification header = %q, want %q", got, validation.wantCode)
					}
					if _, statErr := os.Stat(ffmpegMarker); !os.IsNotExist(statErr) {
						t.Fatalf("FFmpeg ran before live metadata rejection: %v", statErr)
					}
				})
			}
		})
	}
}

func TestRestartSessionLockedRejectsChangedToneMapSourceWithoutStoppingLiveSession(t *testing.T) {
	server := newTestServer(t)
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(inputPath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	modified := info.ModTime()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	track := nodeToneMapTrack()
	writeNodeToneMapFFprobe(t, ffmpegPath, track)
	const sessionID = "tone-map-restart"
	session, err := playback.StartTranscode(context.Background(), playback.TranscodeOpts{
		SessionID:             sessionID,
		InputPath:             inputPath,
		OutputDir:             filepath.Join(dir, "output"),
		TargetCodecVideo:      "h264",
		TargetCodecAudio:      "aac",
		SegmentDuration:       2,
		FFmpegPath:            ffmpegPath,
		ToneMapPolicy:         tonemap.PolicySoftwareOnly,
		ToneMapMode:           tonemap.ModeSoftware,
		ToneMapSourceKind:     tonemap.SourcePQ,
		ToneMapFilter:         tonemap.SoftwareFilterBT2390,
		ToneMapRecipeVersion:  playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.RevisionForFile(&models.MediaFile{ID: 42, FileSize: info.Size(), FileModifiedAt: &modified, VideoTracks: []models.VideoTrack{track}}),
	})
	if err != nil {
		t.Fatalf("StartTranscode() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	server.sessions[sessionID] = session

	if err := os.WriteFile(inputPath, []byte("replacement bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := server.restartSessionLocked(context.Background(), sessionID, session, 20, 10); err == nil {
		t.Fatal("restartSessionLocked() accepted replacement source bytes")
	}
	if !session.IsRunning() {
		t.Fatal("failed restart validation stopped the live transcode session")
	}
}

func TestNewServerKeepsDefaultDownloadArtifactsInsideTranscodeVolume(t *testing.T) {
	w := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Playback.TranscodeDir = filepath.Join(t.TempDir(), "transcodes")
	w.SetConfigForTest(cfg)

	server := NewServer(w, nil)
	want := filepath.Join(cfg.Playback.TranscodeDir, downloadprepare.ArtifactDirectoryName)
	if server.artifactRoot != want {
		t.Fatalf("artifact root = %q, want mounted path %q", server.artifactRoot, want)
	}
	if _, protected := server.activeSessionIDs()[downloadprepare.ArtifactDirectoryName]; !protected {
		t.Fatal("default artifact directory is not protected from the transcode orphan sweep")
	}
}

func TestHandleHWCapabilitiesReturnsServiceUnavailableWhenProbeIsCanceled(t *testing.T) {
	server := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	server.handleHWCapabilities(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
}

func TestProxiedSegmentCompletionRequiresDownstreamAcknowledgement(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("`true` not found in PATH: %v", err)
	}

	const sessionID = "proxy-completion-session"
	server := newTestServer(t)
	outputDir := t.TempDir()
	session, err := playback.StartTranscode(context.Background(), playback.TranscodeOpts{
		SessionID:               sessionID,
		OutputDir:               outputDir,
		FFmpegPath:              truePath,
		TargetCodecVideo:        "h264",
		TargetCodecAudio:        "aac",
		SegmentDuration:         2,
		SegmentRetentionSeconds: 600,
	})
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	server.sessions[sessionID] = session
	server.lastAccess[sessionID] = time.Now()

	const segmentName = "seg_00007.ts"
	if err := os.WriteFile(filepath.Join(outputDir, segmentName), []byte("complete segment"), 0o600); err != nil {
		t.Fatal(err)
	}

	segmentReq := withNodeRouteParams(
		httptest.NewRequest(http.MethodGet, "/transcode/"+sessionID+"/segment/"+segmentName, nil),
		map[string]string{"session_id": sessionID, "name": segmentName},
	)
	segmentReq.Header.Set(transcodeproxy.RequestHeader, "1")
	segmentRR := httptest.NewRecorder()
	server.handleSegment(segmentRR, segmentReq)
	if segmentRR.Code != http.StatusOK {
		t.Fatalf("segment status = %d, body = %q", segmentRR.Code, segmentRR.Body.String())
	}
	generation := segmentRR.Header().Get(transcodeproxy.GenerationHeader)
	if generation == "" {
		t.Fatal("proxied segment omitted generation")
	}
	if got := session.LastRequestedSegment(); got != 0 {
		t.Fatalf("node counted proxy-hop completion as client completion: got %d, want 0", got)
	}

	ackReq := withNodeRouteParams(
		httptest.NewRequest(http.MethodPost, "/transcode/"+sessionID+"/segment/"+segmentName+"/downloaded", nil),
		map[string]string{"session_id": sessionID, "name": segmentName},
	)
	ackReq.Header.Set(transcodeproxy.GenerationHeader, generation)
	ackRR := httptest.NewRecorder()
	server.handleSegmentDownloaded(ackRR, ackReq)
	if ackRR.Code != http.StatusNoContent {
		t.Fatalf("ack status = %d, body = %q", ackRR.Code, ackRR.Body.String())
	}
	if got := session.LastRequestedSegment(); got != 7 {
		t.Fatalf("acknowledged segment = %d, want 7", got)
	}
}

func TestProxiedSegmentCompletionRejectsStaleGeneration(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("`true` not found in PATH: %v", err)
	}

	const sessionID = "stale-proxy-completion-session"
	server := newTestServer(t)
	session, err := playback.StartTranscode(context.Background(), playback.TranscodeOpts{
		SessionID:        sessionID,
		OutputDir:        t.TempDir(),
		FFmpegPath:       truePath,
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
	})
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	server.sessions[sessionID] = session
	server.lastAccess[sessionID] = time.Now()

	const segmentName = "seg_00007.ts"
	ackReq := withNodeRouteParams(
		httptest.NewRequest(http.MethodPost, "/transcode/"+sessionID+"/segment/"+segmentName+"/downloaded", nil),
		map[string]string{"session_id": sessionID, "name": segmentName},
	)
	ackReq.Header.Set(transcodeproxy.GenerationHeader, "999")
	ackRR := httptest.NewRecorder()
	server.handleSegmentDownloaded(ackRR, ackReq)
	if ackRR.Code != http.StatusNoContent {
		t.Fatalf("ack status = %d, body = %q", ackRR.Code, ackRR.Body.String())
	}
	if got := session.LastRequestedSegment(); got != 0 {
		t.Fatalf("stale acknowledgement advanced segment to %d", got)
	}
}

func withNodeRouteParams(req *http.Request, values map[string]string) *http.Request {
	routeCtx := chi.NewRouteContext()
	for key, value := range values {
		routeCtx.URLParams.Add(key, value)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func TestHandleDownloadPrepareKeepsStartupArtifactRootAcrossReload(t *testing.T) {
	server := newTestServer(t)
	artifactDir := server.artifactRoot
	server.watcher.Config().Download.ArtifactDir = t.TempDir()
	server.watcher.Config().Playback.TranscodeDir = t.TempDir()
	useFakeFFmpeg(t, server, "#!/bin/sh\nfor last; do :; done\nprintf artifact > \"$last\"\n")
	server.watcher.Config().Playback.HWAccel = "none"
	outputPath := filepath.Join(artifactDir, "artifact-1.mp4")
	rr := postPrepare(t, server, downloadprepare.Request{
		ArtifactID: "artifact-1", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "copy", TargetCodecAudio: "copy", AudioTrackIndex: -1,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("prepared output: %v", err)
	}
	responseBody := rr.Body.String()
	var result downloadprepare.Result
	if err := json.Unmarshal([]byte(responseBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.ArtifactID != "artifact-1" || result.FileSize != info.Size() || strings.Contains(responseBody, artifactDir) {
		t.Fatalf("prepare result = %+v body=%s", result, responseBody)
	}
	if got := server.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs after prepare = %d, want 0", got)
	}
}

func TestHandleDownloadPreparePublishesToneMapReceiptAndStatusAttestation(t *testing.T) {
	server := newTestServer(t)
	inputPath, size := writeNodeSource(t, "source")
	useToneMapFFmpeg(t, server, "for last; do :; done\nprintf artifact > \"$last\"\n")
	revision := nodeToneMapRevision(size)
	prepareRequest := toneMapPrepareRequest("artifact-tone-map", inputPath, revision)

	recorder := postPrepare(t, server, prepareRequest)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	want := downloadprepare.Result{
		ArtifactID:                       "artifact-tone-map",
		FileSize:                         int64(len("artifact")),
		ToneMapRecipeVersion:             playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapMode:                      tonemap.ModeSoftware,
		ToneMapSourceRevisionFingerprint: revision.Fingerprint(),
		ExecutionFingerprint:             prepareRequest.ExecutionFingerprint(),
	}
	var result downloadprepare.Result
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result != want {
		t.Fatalf("prepare result = %+v, want %+v", result, want)
	}
	outputPath := filepath.Join(server.artifactRoot, "artifact-tone-map.mp4")
	receiptBytes, err := os.ReadFile(outputPath + ".receipt.json")
	if err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	var receipt downloadprepare.Result
	if err := json.Unmarshal(receiptBytes, &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if receipt != want {
		t.Fatalf("stored receipt = %+v, want %+v", receipt, want)
	}
	if _, err := os.Stat(outputPath + ".receipt.json.part"); !os.IsNotExist(err) {
		t.Fatalf("receipt partial remains after publication: %v", err)
	}

	head := httptest.NewRequest(http.MethodHead, "/downloads/artifacts/artifact-tone-map", nil)
	nodeauthtest.Sign(t, head, testSecret)
	headRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(headRecorder, head)
	if headRecorder.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, body = %s", headRecorder.Code, headRecorder.Body.String())
	}
	if got := headRecorder.Header().Get("X-Silo-Tone-Map-Recipe-Version"); got != want.ToneMapRecipeVersion {
		t.Fatalf("recipe header = %q, want %q", got, want.ToneMapRecipeVersion)
	}
	if got := headRecorder.Header().Get("X-Silo-Tone-Map-Mode"); got != string(want.ToneMapMode) {
		t.Fatalf("mode header = %q, want %q", got, want.ToneMapMode)
	}
	if got := headRecorder.Header().Get("X-Silo-Tone-Map-Source-Revision-Fingerprint"); got != want.ToneMapSourceRevisionFingerprint {
		t.Fatalf("source revision header = %q, want %q", got, want.ToneMapSourceRevisionFingerprint)
	}
}

func TestExpectedDownloadPrepareResultAttestsExplicitAudioOutput(t *testing.T) {
	request := downloadprepare.Request{
		ArtifactID: "artifact-explicit-audio", TargetCodecAudio: "aac",
		TargetAudioChannels: 1, TargetAudioBitrateKbps: 256,
	}
	result, ok := expectedDownloadPrepareResult(request, 55)
	if !ok || result.ExecutionFingerprint != request.ExecutionFingerprint() {
		t.Fatalf("explicit audio output result = (%+v, %t), want exact execution receipt", result, ok)
	}
}

func TestHandleDownloadPrepareDoesNotReuseArtifactForPartialToneMapRecipe(t *testing.T) {
	server := newTestServer(t)
	writeArtifact(t, server, "artifact-partial.mp4", "ready")
	recorder := postPrepare(t, server, downloadprepare.Request{
		ArtifactID: "artifact-partial", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
	})
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleDownloadPrepareReceiptInvalidationFailurePreservesExistingArtifact(t *testing.T) {
	server := newTestServer(t)
	inputPath, size := writeNodeSource(t, "source")
	useFakeFFmpeg(t, server, toneMapFFmpegScript("for last; do :; done\nprintf newbytes > \"$last\"\n"))
	server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
	outputPath := writeArtifact(t, server, "artifact-invalidation.mp4", "oldbytes")
	blockReceiptRemoval(t, outputPath)
	recorder := postPrepare(t, server, toneMapPrepareRequest("artifact-invalidation", inputPath, tonemap.SourceRevision{MediaFileID: 1, FileSize: size}))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", recorder.Code, recorder.Body.String())
	}
	got, err := os.ReadFile(outputPath)
	if err != nil || string(got) != "oldbytes" {
		t.Fatalf("existing artifact after invalidation failure = %q, %v; want oldbytes", got, err)
	}
}

func TestHandleDownloadPrepareReexecutesValidRecipeAfterMissingOrMismatchedReceipt(t *testing.T) {
	for _, test := range []struct {
		name          string
		writeMismatch bool
	}{
		{name: "missing receipt"},
		{name: "mismatched receipt", writeMismatch: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			inputPath, size := writeNodeSource(t, "source")
			useToneMapFFmpeg(t, server, "for last; do :; done\nprintf newbytes > \"$last\"\n")
			outputPath := writeArtifact(t, server, "artifact-reexecute.mp4", "oldbytes")
			if test.writeMismatch {
				if err := writeDownloadArtifactReceipt(outputPath, downloadprepare.Result{
					ArtifactID: "artifact-reexecute", FileSize: int64(len("oldbytes")),
					ToneMapRecipeVersion: "stale", ToneMapMode: tonemap.ModeSoftware,
					ToneMapSourceRevisionFingerprint: "wrong",
				}); err != nil {
					t.Fatal(err)
				}
			}
			revision := nodeToneMapRevision(size)
			request := toneMapPrepareRequest("artifact-reexecute", inputPath, revision)
			recorder := postPrepare(t, server, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if got, err := os.ReadFile(outputPath); err != nil || string(got) != "newbytes" {
				t.Fatalf("artifact = %q, %v; want re-executed bytes", got, err)
			}
			receipt, err := readDownloadArtifactReceipt(outputPath)
			if err != nil || receipt.ToneMapRecipeVersion != request.ToneMapRecipeVersion ||
				receipt.ToneMapMode != request.ToneMapMode || receipt.ToneMapSourceRevisionFingerprint != revision.Fingerprint() {
				t.Fatalf("replacement receipt = %+v, %v", receipt, err)
			}
		})
	}
}

func TestHandleDownloadPrepareOrdinaryReexecutionClearsStaleReceipt(t *testing.T) {
	server := newTestServer(t)
	if err := os.MkdirAll(server.artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(server.artifactRoot, "artifact-ordinary-reexecute.mp4")
	if err := writeDownloadArtifactReceipt(outputPath, downloadprepare.Result{
		ArtifactID: "artifact-ordinary-reexecute", FileSize: int64(len("oldbytes")),
		ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapMode:          tonemap.ModeSoftware,
		ToneMapSourceRevisionFingerprint: tonemap.SourceRevision{
			MediaFileID: 1,
			FileSize:    8,
		}.Fingerprint(),
	}); err != nil {
		t.Fatal(err)
	}
	useFakeFFmpeg(t, server, "#!/bin/sh\nfor last; do :; done\nprintf newbytes > \"$last\"\n")
	recorder := postPrepare(t, server, downloadprepare.Request{
		ArtifactID: "artifact-ordinary-reexecute", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "copy", TargetCodecAudio: "copy", AudioTrackIndex: -1,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(downloadArtifactReceiptPath(outputPath)); !os.IsNotExist(err) {
		t.Fatalf("stale ordinary receipt remains after re-execution: %v", err)
	}
}

func TestHandleDownloadPrepareConcurrentSameArtifactPublishesOnce(t *testing.T) {
	server := newTestServer(t)
	inputPath, size := writeNodeSource(t, "source")
	counterPath := filepath.Join(t.TempDir(), "prepare-count")
	useToneMapFFmpeg(t, server, fmt.Sprintf("printf x >> %q\n", counterPath)+
		"sleep 0.1\n"+
		"for last; do :; done\n"+
		"printf newbytes > \"$last\"\n")
	request := toneMapPrepareRequest("artifact-concurrent", inputPath, nodeToneMapRevision(size))
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		prepare := jsonRequest(t, "/downloads/prepare", request)
		go func() {
			<-start
			recorder := httptest.NewRecorder()
			server.handleDownloadPrepare(recorder, prepare)
			results <- recorder
		}()
	}
	close(start)
	for range 2 {
		recorder := <-results
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	}
	if count, err := os.ReadFile(counterPath); err != nil || string(count) != "x" {
		t.Fatalf("prepare executions = %q, %v; want one", count, err)
	}
}

func TestHandleDownloadPrepareOptimisticReuseWaitsForInFlightReplacement(t *testing.T) {
	server := newTestServer(t)
	inputPath, _ := writeNodeSource(t, "source-a")
	useToneMapFFmpeg(t, server, "for last; do :; done\nprintf final-a > \"$last\"\n")
	const artifactID = "artifact-replacement-race"
	outputPath := writeArtifact(t, server, artifactID+".mp4", "oldbytes")
	revision := nodeToneMapRevision(8)
	if err := writeDownloadArtifactReceipt(outputPath, downloadprepare.Result{
		ArtifactID: artifactID, FileSize: 8,
		ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceRevisionFingerprint: revision.Fingerprint(),
	}); err != nil {
		t.Fatal(err)
	}
	request := toneMapPrepareRequest(artifactID, inputPath, revision)
	request.AudioTrackIndex = 0
	prepare := jsonRequest(t, "/downloads/prepare", request)
	unlock := server.lockSessionLifecycle("download-artifact-" + artifactID)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.handleDownloadPrepare(recorder, prepare)
		close(done)
	}()
	select {
	case <-done:
		unlock()
		t.Fatal("prepare reused an artifact while its replacement lock was held")
	case <-time.After(50 * time.Millisecond):
	}
	if err := os.WriteFile(outputPath, []byte("newbytes"), 0o600); err != nil {
		unlock()
		t.Fatal(err)
	}
	if err := writeDownloadArtifactReceipt(outputPath, downloadprepare.Result{
		ArtifactID: artifactID, FileSize: 8,
		ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceRevisionFingerprint: "replacement-revision",
	}); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("prepare did not resume after replacement completed")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after re-executing request A; body = %s", recorder.Code, recorder.Body.String())
	}
	if receipt, err := readDownloadArtifactReceipt(outputPath); err != nil || receipt.ToneMapSourceRevisionFingerprint != revision.Fingerprint() {
		t.Fatalf("final receipt = %+v, %v; want request A", receipt, err)
	}
}

func TestSessionOutputDirKeepsStartupPathAcrossReload(t *testing.T) {
	server := newTestServer(t)
	startupDir := server.transcodeDir
	server.watcher.Config().Playback.TranscodeDir = t.TempDir()

	if got, want := server.sessionOutputDir("session-1"), filepath.Join(startupDir, "session-1"); got != want {
		t.Fatalf("session output dir = %q, want startup path %q", got, want)
	}
}

func TestAudioRecipeRequestAndStartAttestationFailClosed(t *testing.T) {
	current := TranscodeStartRequest{
		SessionID: "downmix", InputPath: "/media/movie.mkv",
		SourceAudioChannels: 6,
		TargetCodecAudio:    "aac",
		TargetAudioChannels: 2,
		AudioRecipeVersion:  playback.TransformationAudioToAACRecipeVersionV3,
	}
	if err := validateAudioRecipeRequest(current); err != nil {
		t.Fatalf("current recipe rejected: %v", err)
	}
	defaultAAC := current
	defaultAAC.TargetCodecAudio = ""
	if err := validateAudioRecipeRequest(defaultAAC); err != nil {
		t.Fatalf("default AAC recipe rejected: %v", err)
	}
	if err := ValidateAudioRecipeAttestation(current, TranscodeStartResponse{AudioRecipeVersion: current.AudioRecipeVersion}); err != nil {
		t.Fatalf("current attestation rejected: %v", err)
	}
	if err := ValidateAudioRecipeAttestation(current, TranscodeStartResponse{}); !errors.Is(err, ErrAudioRecipeAttestationMismatch) {
		t.Fatalf("old-node response error = %v, want attestation mismatch", err)
	}

	legacy := TranscodeStartRequest{SessionID: "legacy", InputPath: "/media/movie.mkv"}
	if err := ValidateAudioRecipeAttestation(legacy, TranscodeStartResponse{}); err != nil {
		t.Fatalf("ordinary legacy response rejected: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*TranscodeStartRequest)
	}{
		{name: "unversioned", mutate: func(r *TranscodeStartRequest) { r.AudioRecipeVersion = "" }},
		{name: "stereo source", mutate: func(r *TranscodeStartRequest) { r.SourceAudioChannels = 2 }},
		{name: "unknown codec", mutate: func(r *TranscodeStartRequest) { r.TargetCodecAudio = "unknown" }},
		{name: "audio copy", mutate: func(r *TranscodeStartRequest) { r.TargetCodecAudio = "copy" }},
		{name: "surround preserving codec", mutate: func(r *TranscodeStartRequest) { r.TargetCodecAudio = "eac3" }},
		{name: "missing target channels", mutate: func(r *TranscodeStartRequest) { r.TargetAudioChannels = 0 }},
		{name: "surround target", mutate: func(r *TranscodeStartRequest) { r.TargetAudioChannels = 6 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := current
			test.mutate(&request)
			if err := validateAudioRecipeRequest(request); !errors.Is(err, ErrAudioRecipeAttestationMismatch) {
				t.Fatalf("validateAudioRecipeRequest() = %v, want attestation mismatch", err)
			}
		})
	}
}

func TestCopyFMP4RecipeRequestAndStartAttestationFailClosed(t *testing.T) {
	current := TranscodeStartRequest{
		SessionID: "copy-fmp4", InputPath: "/media/movie.mkv",
		TargetCodecVideo:      "copy",
		CopyFMP4RecipeVersion: playback.CopyFMP4RecipeVersion,
	}
	if err := validateCopyFMP4RecipeRequest(current); err != nil {
		t.Fatalf("current copy recipe rejected: %v", err)
	}
	if err := ValidateCopyFMP4RecipeAttestation(current, TranscodeStartResponse{CopyFMP4RecipeVersion: current.CopyFMP4RecipeVersion}); err != nil {
		t.Fatalf("current copy attestation rejected: %v", err)
	}
	if err := ValidateCopyFMP4RecipeAttestation(current, TranscodeStartResponse{}); !errors.Is(err, ErrCopyFMP4RecipeAttestationMismatch) {
		t.Fatalf("old-node response error = %v, want attestation mismatch", err)
	}

	legacyCopy := current
	legacyCopy.CopyFMP4RecipeVersion = ""
	if err := validateCopyFMP4RecipeRequest(legacyCopy); !errors.Is(err, ErrCopyFMP4RecipeAttestationMismatch) {
		t.Fatalf("unversioned copy request error = %v, want attestation mismatch", err)
	}
	encoded := TranscodeStartRequest{SessionID: "encoded", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264"}
	if err := ValidateCopyFMP4RecipeAttestation(encoded, TranscodeStartResponse{}); err != nil {
		t.Fatalf("ordinary encoded response rejected: %v", err)
	}
}

func TestCopyFMP4RecipeCardsFailClosedBeforeNodeReconstruction(t *testing.T) {
	current := playback.NewRecipeCard(7, "profile-1", 42, "http://node", playback.TranscodeOpts{
		SessionID: "copy-fmp4", TargetCodecVideo: "copy", SegmentDuration: 2,
	})
	if !recipeServesTransport(current, current.SessionID) || !recipeIsComplete(current) {
		t.Fatalf("current copy recipe was not accepted: %+v", current)
	}

	legacy := current
	legacy.PlayMethod = playback.PlayTranscode
	legacy.CopyFMP4RecipeVersion = ""
	if recipeServesTransport(legacy, legacy.SessionID) {
		t.Fatal("legacy unversioned copy recipe was accepted for reconstruction")
	}

	identity := playback.RecipeCardFromClaims(&streamtoken.Claims{
		SessionID: "copy-fmp4", PlayMethod: streamtoken.PlayMethodCopyFMP4Transcode,
		CopyFMP4RecipeVersion: playback.CopyFMP4RecipeVersion,
	})
	if !recipeServesTransport(identity, identity.SessionID) || recipeIsComplete(identity) {
		t.Fatalf("current identity-only copy recipe marker was not accepted for store lookup: %+v", identity)
	}
}

func TestValidateThrottleAttestation(t *testing.T) {
	request := TranscodeStartRequest{ThrottleSeconds: 180}
	if err := ValidateThrottleAttestation(request, TranscodeStartResponse{ThrottleSeconds: 180}); err != nil {
		t.Fatalf("matching attestation rejected: %v", err)
	}
	if err := ValidateThrottleAttestation(request, TranscodeStartResponse{}); !errors.Is(err, ErrThrottleAttestationMismatch) {
		t.Fatalf("missing attestation error = %v, want %v", err, ErrThrottleAttestationMismatch)
	}
	if err := ValidateThrottleAttestation(TranscodeStartRequest{}, TranscodeStartResponse{}); err != nil {
		t.Fatalf("disabled throttle rejected legacy node response: %v", err)
	}
}

// A node that ignores a requested keyframe playlist would serve FFmpeg's
// growing playlist under a recipe that records the planned one.
func TestValidateKeyframePlaylistAttestation(t *testing.T) {
	request := TranscodeStartRequest{KeyframePlaylist: true}
	if err := ValidateKeyframePlaylistAttestation(request, TranscodeStartResponse{KeyframePlaylist: true}); err != nil {
		t.Fatalf("matching attestation rejected: %v", err)
	}
	if err := ValidateKeyframePlaylistAttestation(request, TranscodeStartResponse{}); !errors.Is(err, ErrKeyframePlaylistAttestationMismatch) {
		t.Fatalf("missing attestation error = %v, want %v", err, ErrKeyframePlaylistAttestationMismatch)
	}
	if err := ValidateKeyframePlaylistAttestation(TranscodeStartRequest{}, TranscodeStartResponse{}); err != nil {
		t.Fatalf("request without a keyframe playlist rejected a legacy node response: %v", err)
	}
}

// A node that ignores requested MPEG-TS packaging would publish fMP4 under a
// plan that promises MPEG-TS.
func TestValidateCopyVideoMPEGTSAttestation(t *testing.T) {
	request := TranscodeStartRequest{CopyVideoMPEGTS: true}
	if err := ValidateCopyVideoMPEGTSAttestation(request, TranscodeStartResponse{CopyVideoMPEGTS: true}); err != nil {
		t.Fatalf("matching attestation rejected: %v", err)
	}
	if err := ValidateCopyVideoMPEGTSAttestation(request, TranscodeStartResponse{}); !errors.Is(err, ErrCopyVideoMPEGTSAttestationMismatch) {
		t.Fatalf("missing attestation error = %v, want %v", err, ErrCopyVideoMPEGTSAttestationMismatch)
	}
	if err := ValidateCopyVideoMPEGTSAttestation(TranscodeStartRequest{}, TranscodeStartResponse{}); err != nil {
		t.Fatalf("fMP4 request rejected a legacy node response: %v", err)
	}
}

// Requests the node cannot execute as written are refused with 400 before
// anything runs.
func TestHandleStartAndPrepareRejectInvalidRequests(t *testing.T) {
	for _, test := range []struct {
		name         string
		prepare      bool
		catalogPaths bool
		artifactDir  string // relative to the transcode root; empty keeps the default
		body         any
	}{
		{name: "start with unversioned source audio recipe", body: TranscodeStartRequest{
			SessionID: "unversioned-downmix", InputPath: "/media/movie.mkv",
			SourceAudioChannels: 6,
		}},
		{name: "start with unversioned copy fMP4 recipe", body: TranscodeStartRequest{
			SessionID: "unversioned-copy", InputPath: "/media/movie.mkv",
			TargetCodecVideo: "copy",
		}},
		{name: "start with session id outside the transcode root", body: TranscodeStartRequest{
			SessionID: "../sibling", InputPath: "/media/movie.mkv",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		}},
		{name: "start with session id naming the transcode root", body: TranscodeStartRequest{
			SessionID: ".", InputPath: "/media/movie.mkv",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		}},
		{name: "start with session id naming the prepared-download store", body: TranscodeStartRequest{
			SessionID: downloadprepare.ArtifactDirectoryName, InputPath: "/media/movie.mkv",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		}},
		{name: "start with session id naming the prepared-download store in another case", body: TranscodeStartRequest{
			SessionID: "Download-Artifacts", InputPath: "/media/movie.mkv",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		}},
		{name: "start with session id naming a configured prepared-download store", artifactDir: "prepared/store",
			body: TranscodeStartRequest{
				SessionID: "prepared", InputPath: "/media/movie.mkv",
				TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
			}},
		{name: "start with unapproved input path", catalogPaths: true,
			body: json.RawMessage(`{"session_id":"unsafe-input","input_path":"http://example.test/movie.mkv"}`)},
		{name: "prepare with partial stereo downmix recipe", prepare: true, body: downloadprepare.Request{
			ArtifactID: "artifact-unversioned-downmix", InputPath: "/media/movie.mkv",
			TargetCodecAudio: "aac", SourceAudioChannels: 6,
		}},
		{name: "prepare with invalid artifact id", prepare: true, body: downloadprepare.Request{
			ArtifactID: "../artifact-2", InputPath: "/media/movie.mkv",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			if test.catalogPaths {
				server.inputPaths = NewCatalogPathAuthorizer(staticCatalogPaths{})
			}
			if test.artifactDir != "" {
				server.artifactRoot = filepath.Join(server.transcodeDir, test.artifactDir)
			}
			post := postStartRequest
			if test.prepare {
				post = postPrepare
			}
			if recorder := post(t, server, test.body); recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestHandleStartAttestsRecipeVersions(t *testing.T) {
	for _, test := range []struct {
		name    string
		request TranscodeStartRequest
		receipt func(TranscodeStartResponse) string
		want    string
	}{
		{
			name: "stereo downmix",
			request: TranscodeStartRequest{
				SessionID: "attested-downmix", InputPath: "/media/movie.mkv",
				SourceAudioChannels: 6, AudioRecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
				TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetAudioChannels: 2, SegmentDuration: 2,
			},
			receipt: func(response TranscodeStartResponse) string { return response.AudioRecipeVersion },
			want:    playback.TransformationAudioToAACRecipeVersionV3,
		},
		{
			name: "copy fMP4",
			request: TranscodeStartRequest{
				SessionID: "attested-copy", InputPath: "/media/movie.mkv",
				SourceVideoCodec: "hevc", TargetCodecVideo: "copy", TargetCodecAudio: "copy",
				CopyFMP4RecipeVersion: playback.CopyFMP4RecipeVersion, SegmentDuration: 2,
			},
			receipt: func(response TranscodeStartResponse) string { return response.CopyFMP4RecipeVersion },
			want:    playback.CopyFMP4RecipeVersion,
		},
		{
			// Without the echo the API refuses the node rather than publish
			// fMP4 under a plan that promises MPEG-TS.
			name: "copy MPEG-TS",
			request: TranscodeStartRequest{
				SessionID: "attested-copy-ts", InputPath: "/media/movie.mkv",
				SourceVideoCodec: "hevc", TargetCodecVideo: "copy", TargetCodecAudio: "copy",
				CopyFMP4RecipeVersion: playback.CopyFMP4RecipeVersion, CopyVideoMPEGTS: true, SegmentDuration: 2,
			},
			receipt: func(response TranscodeStartResponse) string { return strconv.FormatBool(response.CopyVideoMPEGTS) },
			want:    "true",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newLoopingFFmpegServer(t)
			// The session is registered and closed by the helper.
			recorder, _ := startRegisteredSession(t, server, test.request)
			var response TranscodeStartResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if got := test.receipt(response); got != test.want {
				t.Fatalf("recipe receipt = %q, want %q", got, test.want)
			}
		})
	}
}

func TestHandleStartWaitsForForceReloadGate(t *testing.T) {
	server := newTestServer(t)
	useFakeFFmpeg(t, server, "#!/bin/sh\nexit 1\n")
	req := jsonRequest(t, "/transcode/start", TranscodeStartRequest{
		SessionID: "reload-gated-start", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, RequireReady: true,
	})
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	server.reloadMu.Lock()
	locked := true
	defer func() {
		if locked {
			server.reloadMu.Unlock()
		}
	}()
	go func() {
		server.handleStart(rr, req)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("start completed while force-reload gate was held")
	case <-time.After(50 * time.Millisecond):
	}
	server.reloadMu.Unlock()
	locked = false
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("start did not resume after force-reload gate was released")
	}
}

func TestHandleDownloadPrepareTrackingDoesNotBlockAndRemovesAfterTrack(t *testing.T) {
	server := newTestServer(t)
	tracker := newBlockingSessionTracker()
	server.tracker = tracker
	useFakeFFmpeg(t, server, "#!/bin/sh\nfor last; do :; done\ntouch \"$last\"\n")
	server.watcher.Config().Playback.HWAccel = "none"
	req := jsonRequest(t, "/downloads/prepare", downloadprepare.Request{
		ArtifactID: "artifact-tracking", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "copy", TargetCodecAudio: "copy", AudioTrackIndex: -1,
	})
	rr := httptest.NewRecorder()
	handlerDone := make(chan struct{})
	go func() {
		server.handleDownloadPrepare(rr, req)
		close(handlerDone)
	}()

	select {
	case <-tracker.trackStarted:
	case <-time.After(time.Second):
		t.Fatal("tracking did not start")
	}
	select {
	case <-handlerDone:
	case <-time.After(250 * time.Millisecond):
		close(tracker.trackRelease)
		<-handlerDone
		t.Fatal("download prepare waited for session tracking")
	}
	if rr.Code != http.StatusOK {
		close(tracker.trackRelease)
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	close(tracker.trackRelease)
	select {
	case <-tracker.removeStarted:
	case <-time.After(time.Second):
		t.Fatal("tracking cleanup was not scheduled")
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if !tracker.trackHasDeadline || !tracker.removeHasDeadline {
		t.Fatalf("tracking deadlines: track=%t remove=%t", tracker.trackHasDeadline, tracker.removeHasDeadline)
	}
	if got := strings.Join(tracker.events, ","); got != "track,track-done,remove" {
		t.Fatalf("tracking order = %q", got)
	}
}

func TestDownloadArtifactRoutesServeRangeAndDeleteNodeLocalFile(t *testing.T) {
	server := newTestServer(t)
	root := server.artifactRoot
	if want := filepath.Join(server.watcher.Config().Playback.TranscodeDir, downloadprepare.ArtifactDirectoryName); root != want {
		t.Fatalf("artifact root = %q, want %q", root, want)
	}
	if _, protected := server.activeSessionIDs()[downloadprepare.ArtifactDirectoryName]; !protected {
		t.Fatal("artifact directory is not protected from the orphan transcode sweep")
	}
	path := writeArtifact(t, server, "artifact-range.mp4", "0123456789")
	if err := os.WriteFile(path+".receipt.json", []byte(`{"artifact_id":"artifact-range"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/downloads/artifacts/artifact-range", nil)
	nodeauthtest.Sign(t, req, testSecret)
	req.Header.Set("Range", "bytes=3-6")
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusPartialContent || rr.Body.String() != "3456" {
		t.Fatalf("GET status=%d body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Range"); got != "bytes 3-6/10" {
		t.Fatalf("Content-Range = %q", got)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/downloads/artifacts/artifact-range", nil)
	nodeauthtest.Sign(t, deleteReq, testSecret)
	deleteRR := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusNoContent {
		t.Fatalf("DELETE status=%d body=%s", deleteRR.Code, deleteRR.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("artifact still exists: %v", err)
	}
	if _, err := os.Stat(path + ".receipt.json"); !os.IsNotExist(err) {
		t.Fatalf("artifact receipt still exists: %v", err)
	}
}

func TestDeleteDownloadArtifactReceiptInvalidationFailurePreservesArtifact(t *testing.T) {
	server := newTestServer(t)
	outputPath := writeArtifact(t, server, "artifact-delete-invalidation.mp4", "oldbytes")
	blockReceiptRemoval(t, outputPath)

	request := httptest.NewRequest(http.MethodDelete, "/downloads/artifacts/artifact-delete-invalidation", nil)
	nodeauthtest.Sign(t, request, testSecret)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", recorder.Code, recorder.Body.String())
	}
	if got, err := os.ReadFile(outputPath); err != nil || string(got) != "oldbytes" {
		t.Fatalf("artifact after receipt invalidation failure = %q, %v; want oldbytes", got, err)
	}
}

func TestDownloadArtifactHeadWaitsForInFlightPrepare(t *testing.T) {
	server := newTestServer(t)
	const artifactID = "artifact-in-flight"
	unlocks := server.lockSessionLifecycle("download-artifact-" + artifactID)
	req := httptest.NewRequest(http.MethodHead, "/downloads/artifacts/"+artifactID, nil)
	req.SetPathValue("artifact_id", artifactID)
	req = withNodeRouteParams(req, map[string]string{"artifact_id": artifactID})
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.handleDownloadArtifact(rr, req)
		close(done)
	}()
	select {
	case <-done:
		unlocks()
		t.Fatal("HEAD reported a result while preparation held the artifact lock")
	case <-time.After(50 * time.Millisecond):
	}
	path := filepath.Join(server.artifactRoot, artifactID+".mp4")
	if err := os.MkdirAll(server.artifactRoot, 0o755); err != nil {
		unlocks()
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("ready"), 0o600); err != nil {
		unlocks()
		t.Fatal(err)
	}
	unlocks()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HEAD did not resume after preparation published the artifact")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestDownloadArtifactHeadDoesNotWaitForConcurrentRelay(t *testing.T) {
	server := newTestServer(t)
	const artifactID = "artifact-concurrent-read"
	writeArtifact(t, server, artifactID+".mp4", "ready")

	blocked := newBlockingResponseWriter()
	releaseOnce := sync.Once{}
	release := func() { releaseOnce.Do(func() { close(blocked.releaseWrite) }) }
	defer release()
	getDone := make(chan struct{})
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/downloads/artifacts/"+artifactID, nil)
		nodeauthtest.Sign(t, req, testSecret)
		server.Handler().ServeHTTP(blocked, req)
		close(getDone)
	}()

	select {
	case <-blocked.writeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("GET did not begin relaying the artifact")
	}

	headDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodHead, "/downloads/artifacts/"+artifactID, nil)
		nodeauthtest.Sign(t, req, testSecret)
		rr := httptest.NewRecorder()
		server.Handler().ServeHTTP(rr, req)
		headDone <- rr
	}()
	select {
	case rr := <-headDone:
		if rr.Code != http.StatusOK {
			t.Fatalf("HEAD status = %d, body = %s", rr.Code, rr.Body.String())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("HEAD waited for a concurrent artifact relay")
	}

	release()
	select {
	case <-getDone:
	case <-time.After(2 * time.Second):
		t.Fatal("GET did not finish after the response writer was released")
	}
}

func TestDownloadArtifactRoutesRequireBearer(t *testing.T) {
	server := newTestServer(t)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodDelete} {
		rr := httptest.NewRecorder()
		server.Handler().ServeHTTP(rr, httptest.NewRequest(method, "/downloads/artifacts/artifact-1", nil))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d body=%s", method, rr.Code, rr.Body.String())
		}
	}
}

func TestHandleStartDistinctReplacementFailurePreservesPredecessor(t *testing.T) {
	server := newTestServer(t)
	server.sessions["public-session"] = &playback.TranscodeSession{}
	server.activeJobs.Store(1)

	useFakeFFmpeg(t, server, "#!/bin/sh\nexit 1\n")
	rr := postStartRequest(t, server, TranscodeStartRequest{
		SessionID: "public-session-legacy-replacement", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "copy", TargetCodecAudio: "aac", SegmentDuration: 2, RequireReady: true,
		CopyFMP4RecipeVersion: playback.CopyFMP4RecipeVersion,
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	server.mu.RLock()
	predecessor := server.sessions["public-session"]
	_, replacementRegistered := server.sessions["public-session-legacy-replacement"]
	server.mu.RUnlock()
	if predecessor == nil {
		t.Fatal("failed distinct replacement removed the active predecessor")
	}
	if replacementRegistered {
		t.Fatal("failed distinct replacement was registered")
	}
	if got := server.activeJobs.Load(); got != 1 {
		t.Fatalf("active jobs = %d, want predecessor only", got)
	}
}

func signCard(t *testing.T, card playback.RecipeCard) string {
	t.Helper()
	tok, err := streamtoken.Sign(card.ToClaims(), testSecret, time.Hour)
	if err != nil {
		t.Fatalf("sign card: %v", err)
	}
	return tok
}

func TestHandleProgressiveRemuxHeadDoesNotStartFFmpeg(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 11, true }
	mediaPath, _ := writeNodeSource(t, "source")
	ffmpegMarker := filepath.Join(t.TempDir(), "ffmpeg-ran")
	useFakeFFmpeg(t, server, "#!/bin/sh\ntouch \""+ffmpegMarker+"\"\n")
	claims := progressiveRemuxClaims("playback-head", mediaPath, "transport-head")
	claims.TranscodeNode = "http://registered-node"
	card := playback.RecipeCardFromClaims(&claims)
	server.SetRecipeStore(&stubRecipeStore{card: &card, ok: true})
	recorder := httptest.NewRecorder()

	server.handleRemux(recorder, remuxRequest(t, http.MethodHead, claims))

	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("response = %d content-type %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	if got := server.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs = %d, want no FFmpeg start", got)
	}
	if _, err := os.Stat(ffmpegMarker); !os.IsNotExist(err) {
		t.Fatalf("HEAD invoked ffmpeg: %v", err)
	}

	server.SetRecipeStore(&stubRecipeStore{})
	stoppedRecorder := httptest.NewRecorder()

	server.handleRemux(stoppedRecorder, remuxRequest(t, http.MethodHead, claims))

	if stoppedRecorder.Code != http.StatusGone {
		t.Fatalf("stopped HEAD status = %d, want 410", stoppedRecorder.Code)
	}
	if _, err := os.Stat(ffmpegMarker); !os.IsNotExist(err) {
		t.Fatalf("stopped HEAD invoked ffmpeg: %v", err)
	}
}

func TestProgressiveRemuxRunsOnThisNodeRejectsDifferentNodeID(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 12, true }

	claims := &streamtoken.Claims{
		TranscodeNode:          "http://same-node-url",
		RoutingExecutionNodeID: 11,
	}
	if server.progressiveRemuxRunsOnThisNode(claims) {
		t.Fatal("progressive remux accepted a token bound to a different node ID")
	}
	claims.RoutingExecutionNodeID = 0
	if server.progressiveRemuxRunsOnThisNode(claims) {
		t.Fatal("progressive remux accepted a token without a stable node ID")
	}
}

func TestHandleProgressiveRemuxRejectsConcurrentRequest(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 11, true }
	mediaPath, _ := writeNodeSource(t, "source")
	ffmpegLog := filepath.Join(t.TempDir(), "ffmpeg.log")
	useFakeFFmpeg(t, server, "#!/bin/sh\n"+
		"case \" $* \" in *\" -i "+mediaPath+" \"*) printf 'invoked\\n' >> \""+ffmpegLog+"\";; esac\n"+
		"printf node-remux-bytes\n")
	claims := progressiveRemuxClaims("playback-concurrent", mediaPath, "transport-concurrent")
	card := playback.RecipeCardFromClaims(&claims)
	server.SetRecipeStore(&stubRecipeStore{card: &card, ok: true})
	request := remuxRequest(t, http.MethodGet, claims)
	firstWriter := newBlockingResponseWriter()
	released := false
	defer func() {
		if !released {
			close(firstWriter.releaseWrite)
		}
	}()
	firstDone := make(chan struct{})
	go func() {
		server.handleRemux(firstWriter, request.Clone(request.Context()))
		close(firstDone)
	}()
	waitCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	select {
	case <-firstWriter.writeStarted:
	case <-waitCtx.Done():
		t.Fatal("first remux did not reach its response")
	}

	recorder := httptest.NewRecorder()
	server.handleRemux(recorder, request.Clone(request.Context()))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := server.activeJobs.Load(); got != 1 {
		t.Fatalf("active jobs = %d, want only the first remux", got)
	}
	close(firstWriter.releaseWrite)
	released = true
	select {
	case <-firstDone:
	case <-waitCtx.Done():
		t.Fatal("first remux did not finish after response release")
	}
	if got := server.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs after response = %d, want 0", got)
	}
	invocations, err := os.ReadFile(ffmpegLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(invocations), "invoked\n"); got != 1 {
		t.Fatalf("ffmpeg invocations = %d, want 1; log = %q", got, invocations)
	}
}

func TestHandleProgressiveRemuxRejectsProxyExecutionToken(t *testing.T) {
	server := newTestServer(t)
	claims := progressiveRemuxClaims("playback-1", "/media/movie.mkv", "transport-1")
	claims.RoutingExecution = string(noderouting.ExecutionProxy)
	claims.RoutingExecutionNodeID = 0
	rr := httptest.NewRecorder()

	server.handleRemux(rr, remuxRequest(t, http.MethodGet, claims))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := server.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs = %d, want no FFmpeg start", got)
	}
}

func TestHandleStopCancelsProgressiveRemux(t *testing.T) {
	server := newTestServer(t)
	tracker := &recordingSessionTracker{}
	server.tracker = tracker
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	releaseHandler := make(chan struct{})
	server.activeJobs.Add(1)
	server.progressiveRemuxes["transport-1"] = progressiveRemuxRequest{
		id: 1, playbackSessionID: "playback-1", cancel: cancel, done: done,
	}
	req := stopRequest("transport-1")
	rr := httptest.NewRecorder()
	waitCtx, cancelWait := context.WithTimeout(t.Context(), time.Second)
	defer cancelWait()
	go func() {
		<-ctx.Done()
		<-releaseHandler
		server.activeJobs.Add(-1)
		server.mu.Lock()
		delete(server.progressiveRemuxes, "transport-1")
		server.mu.Unlock()
		close(done)
	}()
	stopDone := make(chan struct{})
	go func() {
		server.handleStop(rr, req)
		close(stopDone)
	}()

	select {
	case <-ctx.Done():
	case <-waitCtx.Done():
		t.Fatal("progressive remux was not canceled")
	}
	server.mu.RLock()
	_, retained := server.progressiveRemuxes["transport-1"]
	server.mu.RUnlock()
	if !retained {
		t.Fatal("stop discarded the progressive remux completion handle before its handler exited")
	}
	select {
	case <-stopDone:
		t.Fatal("stop returned before the progressive remux handler exited")
	default:
	}
	close(releaseHandler)
	select {
	case <-stopDone:
	case <-waitCtx.Done():
		t.Fatal("stop did not return after the progressive remux handler exited")
	}
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := server.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs after stop = %d, want 0", got)
	}
	// A running remux is tracked under the viewer's playback session id.
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.removed) != 1 || tracker.removed[0] != "playback-1" {
		t.Fatalf("removed sessions = %v, want playback session %q", tracker.removed, "playback-1")
	}
}

func TestHandleStopReturnsUnavailableWhileProgressiveHandlerIsStuck(t *testing.T) {
	server := newTestServer(t)
	remuxCanceled := make(chan struct{})
	done := make(chan struct{})
	server.progressiveRemuxes["transport-stuck"] = progressiveRemuxRequest{
		id: 1, playbackSessionID: "playback-stuck",
		cancel: sync.OnceFunc(func() { close(remuxCanceled) }), done: done,
	}
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	waitCtx, cancelWait := context.WithTimeout(t.Context(), time.Second)
	defer cancelWait()
	req := withNodeRouteParams(httptest.NewRequest(http.MethodDelete, "/transcode/transport-stuck", nil).WithContext(requestCtx),
		map[string]string{"session_id": "transport-stuck"})
	rr := httptest.NewRecorder()
	stopDone := make(chan struct{})
	go func() {
		server.handleStop(rr, req)
		close(stopDone)
	}()

	select {
	case <-remuxCanceled:
	case <-waitCtx.Done():
		t.Fatal("stop did not cancel the progressive remux")
	}
	cancelRequest()
	select {
	case <-stopDone:
	case <-waitCtx.Done():
		t.Fatal("stop did not return after its shutdown wait was canceled")
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s; want retryable shutdown failure", rr.Code, rr.Body.String())
	}
	server.mu.Lock()
	delete(server.progressiveRemuxes, "transport-stuck")
	server.mu.Unlock()
	close(done)
}

func TestHandleStopReturnsUnavailableWhenProgressiveAuthorityDeleteFails(t *testing.T) {
	const transportID = "transport-delete-failure"
	server := newTestServer(t)
	server.SetRecipeStore(&stubRecipeStore{
		card: &playback.RecipeCard{SessionID: "playback-1", TranscodeTransportID: transportID, PlayMethod: playback.PlayRemux},
		ok:   true, delErr: errors.New("redis is down"),
	})
	rr := httptest.NewRecorder()

	server.handleStop(rr, stopRequest(transportID))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s; want durable revocation failure", rr.Code, rr.Body.String())
	}
	server.mu.RLock()
	fenced := server.stoppedProgressiveRemuxes[transportID].After(time.Now())
	server.mu.RUnlock()
	if !fenced {
		t.Fatal("failed durable revocation did not retain the process-local stop fence")
	}
}

func TestHandleStopDoesNotFenceOrdinaryHLSSession(t *testing.T) {
	const sessionID = "hls-session"
	server := newTestServer(t)
	server.sessions[sessionID] = &playback.TranscodeSession{}
	server.activeJobs.Store(1)
	server.SetRecipeStore(&stubRecipeStore{
		card: &playback.RecipeCard{SessionID: sessionID, PlayMethod: playback.PlayTranscode},
		ok:   true,
	})
	rr := httptest.NewRecorder()

	server.handleStop(rr, stopRequest(sessionID))

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	server.mu.RLock()
	_, fenced := server.stoppedProgressiveRemuxes[sessionID]
	server.mu.RUnlock()
	if fenced {
		t.Fatal("ordinary HLS teardown was retained in the progressive-remux fence map")
	}
}

// A stop removes the tracker record under the transport id unless a progressive
// remux is still running here: a native v3 HLS job is tracked under that id, not
// its card's playback session id, and an ended remux already removed its own.
func TestHandleStopRemovesTrackerRecordByTransportID(t *testing.T) {
	const transportID = "playback-1-plan-transport"
	remuxCard := nativeTransportCard("playback-1", transportID)
	remuxCard.PlayMethod = playback.PlayRemux
	for _, tc := range []struct {
		name    string
		card    *playback.RecipeCard
		running bool
	}{
		{name: "native HLS transcode", card: nativeTransportCard("playback-1", transportID), running: true},
		{name: "remux no longer running here", card: remuxCard},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			tracker := &recordingSessionTracker{}
			server.tracker = tracker
			if tc.running {
				server.sessions[transportID] = &playback.TranscodeSession{}
				server.activeJobs.Store(1)
			}
			server.SetRecipeStore(&stubRecipeStore{card: tc.card, ok: true})
			rr := httptest.NewRecorder()

			server.handleStop(rr, stopRequest(transportID))

			if rr.Code != http.StatusNoContent {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			tracker.mu.Lock()
			defer tracker.mu.Unlock()
			if len(tracker.removed) != 1 || tracker.removed[0] != transportID {
				t.Fatalf("removed sessions = %v, want transport %q", tracker.removed, transportID)
			}
		})
	}
}

func TestHandleProgressiveRemuxRefusesStoppedTransportAfterRestart(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 11, true }
	mediaPath, _ := writeNodeSource(t, "source")
	ffmpegLog := filepath.Join(t.TempDir(), "ffmpeg.log")
	claims := progressiveRemuxClaims("playback-1", mediaPath, "transport-1")
	card := playback.RecipeCardFromClaims(&claims)
	store := &stubRecipeStore{card: &card, ok: true}
	server.SetRecipeStore(store)
	server.handleStop(httptest.NewRecorder(), stopRequest("transport-1"))

	// A replacement process has no in-memory stop fence. The deleted durable
	// authority must still reject the old token before FFmpeg can start.
	restarted := newTestServer(t)
	restarted.nodeRowID = func() (int, bool) { return 11, true }
	useFakeFFmpeg(t, restarted, "#!/bin/sh\nprintf invoked > \""+ffmpegLog+"\"\n")
	restarted.SetRecipeStore(store)
	recorder := httptest.NewRecorder()

	restarted.handleRemux(recorder, remuxRequest(t, http.MethodGet, claims))

	if recorder.Code != http.StatusGone {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(ffmpegLog); !os.IsNotExist(err) {
		t.Fatalf("stopped transport invoked ffmpeg: %v", err)
	}
}

func TestHandleProgressiveRemuxWaitsForForceReloadGate(t *testing.T) {
	server := newTestServer(t)
	tracker := &recordingSessionTracker{}
	server.tracker = tracker
	server.nodeRowID = func() (int, bool) { return 11, true }
	mediaPath, _ := writeNodeSource(t, "source")
	useFakeFFmpeg(t, server, "#!/bin/sh\nprintf node-remux-bytes\n")
	claims := progressiveRemuxClaims("playback-reload-gate", mediaPath, "transport-reload-gate")
	card := playback.RecipeCardFromClaims(&claims)
	storeHit := make(chan struct{}, 1)
	server.SetRecipeStore(&stubRecipeStore{card: &card, ok: true, getHit: storeHit})
	request := remuxRequest(t, http.MethodGet, claims)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	waitCtx, waitCancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer waitCancel()

	server.reloadMu.Lock()
	locked := true
	defer func() {
		if locked {
			server.reloadMu.Unlock()
		}
	}()
	go func() {
		server.handleRemux(recorder, request)
		close(done)
	}()
	select {
	case <-storeHit:
		t.Fatal("remux validated durable authority outside the force-reload gate")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-done:
		t.Fatal("remux completed while force-reload gate was held")
	default:
	}
	server.reloadMu.Unlock()
	locked = false
	select {
	case <-storeHit:
	case <-waitCtx.Done():
		t.Fatal("remux did not validate durable authority after the force-reload gate opened")
	}
	select {
	case <-done:
	case <-waitCtx.Done():
		t.Fatal("remux did not resume after force-reload gate was released")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := server.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs after remux handler return = %d, want 0", got)
	}
	server.mu.RLock()
	_, active := server.progressiveRemuxes[claims.TranscodeTransportID]
	server.mu.RUnlock()
	if active {
		t.Fatal("completed remux handler remained registered")
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.tracked) != 1 || tracker.tracked[0].SessionID != claims.SessionID {
		t.Fatalf("tracked sessions = %#v, want viewer session %q", tracker.tracked, claims.SessionID)
	}
	if len(tracker.removed) != 1 || tracker.removed[0] != claims.SessionID {
		t.Fatalf("removed sessions = %v, want viewer session %q", tracker.removed, claims.SessionID)
	}
}

func TestForceReloadTeardownCancelsProgressiveRemux(t *testing.T) {
	server := newTestServer(t)
	server.tracker = newBlockingSessionTracker()
	server.registeredNodeURL = func() (string, bool) { return "", false }
	const transportID = "transport-force-reload"
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	releaseHandler := make(chan struct{})
	server.activeJobs.Add(1)
	server.progressiveRemuxes[transportID] = progressiveRemuxRequest{id: 1, cancel: cancel, done: done}
	store := &stubRecipeStore{ok: true, card: &playback.RecipeCard{TranscodeTransportID: transportID}}
	server.SetRecipeStore(store)
	handlerExited := make(chan struct{})
	go func() {
		defer close(handlerExited)
		<-ctx.Done()
		<-releaseHandler
		server.activeJobs.Add(-1)
		server.mu.Lock()
		delete(server.progressiveRemuxes, transportID)
		server.mu.Unlock()
		close(done)
	}()
	waitCtx, cancelWait := context.WithTimeout(t.Context(), time.Second)
	defer cancelWait()
	teardownResult := make(chan error, 1)
	go func() {
		teardownResult <- server.teardownForForceReload(waitCtx, "", 0)
	}()

	select {
	case <-ctx.Done():
	case <-waitCtx.Done():
		t.Fatal("force reload did not cancel the progressive remux")
	}
	if got := server.activeJobs.Load(); got != 1 {
		t.Fatalf("active jobs before handler exit = %d, want 1", got)
	}
	server.mu.RLock()
	_, draining := server.progressiveRemuxes[transportID]
	server.mu.RUnlock()
	if !draining {
		t.Fatal("force reload discarded the completion handle before the remux handler exited")
	}
	select {
	case err := <-teardownResult:
		t.Fatalf("force reload returned before the progressive remux handler exited: %v", err)
	default:
	}
	close(releaseHandler)
	select {
	case err := <-teardownResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-waitCtx.Done():
		t.Fatal("force reload did not return after the progressive remux handler exited")
	}
	<-handlerExited
	if got := server.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs after force reload = %d, want 0", got)
	}
	server.mu.RLock()
	_, active := server.progressiveRemuxes[transportID]
	fenced := server.stoppedProgressiveRemuxes[transportID].After(time.Now())
	server.mu.RUnlock()
	if active || !fenced {
		t.Fatalf("force-reload state = active %t, fenced %t; want removed and fenced", active, fenced)
	}
	if len(store.deletes) != 1 || store.deletes[0] != transportID {
		t.Fatalf("durable authority deletes = %v, want [%q]", store.deletes, transportID)
	}
	if len(store.revokedNodes) != 1 || store.revokedNodes[0] != server.tracker.NodeURL() {
		t.Fatalf("node authority revocations = %v, want [%q]", store.revokedNodes, server.tracker.NodeURL())
	}
}

func TestForceReloadTeardownRetainsUndrainedProgressiveRemuxForRetry(t *testing.T) {
	server := newTestServer(t)
	const transportID = "transport-force-reload-retry"
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	done := make(chan struct{})
	cancelCalls := make(chan struct{}, 2)
	server.progressiveRemuxes[transportID] = progressiveRemuxRequest{
		id: 2,
		cancel: func() {
			cancelRequest()
			cancelCalls <- struct{}{}
		},
		done: done,
	}
	waitCtx, cancelWait := context.WithTimeout(t.Context(), time.Second)
	defer cancelWait()

	firstCtx, cancelFirst := context.WithCancel(waitCtx)
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- server.teardownForForceReload(firstCtx, "", 0)
	}()
	select {
	case <-cancelCalls:
	case <-waitCtx.Done():
		t.Fatal("first force reload did not cancel the remux")
	}
	cancelFirst()
	select {
	case err := <-firstResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first force reload error = %v, want context canceled", err)
		}
	case <-waitCtx.Done():
		t.Fatal("first force reload did not return after its wait was canceled")
	}
	server.mu.RLock()
	_, retained := server.progressiveRemuxes[transportID]
	server.mu.RUnlock()
	if !retained {
		t.Fatal("timed-out force reload discarded the remux completion handle")
	}

	retryResult := make(chan error, 1)
	go func() {
		retryResult <- server.teardownForForceReload(waitCtx, "", 0)
	}()
	select {
	case <-cancelCalls:
	case <-waitCtx.Done():
		t.Fatal("force reload retry did not recapture the draining remux")
	}
	select {
	case err := <-retryResult:
		t.Fatalf("force reload retry returned before the remux drained: %v", err)
	default:
	}
	server.mu.Lock()
	delete(server.progressiveRemuxes, transportID)
	server.mu.Unlock()
	close(done)
	select {
	case err := <-retryResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-waitCtx.Done():
		t.Fatal("force reload retry did not return after the remux drained")
	}
	select {
	case <-requestCtx.Done():
	default:
		t.Fatal("force reload never canceled the remux request")
	}
}

func TestForceReloadTeardownRevokesDormantProgressiveAuthorities(t *testing.T) {
	server := newTestServer(t)
	server.tracker = newBlockingSessionTracker()
	server.nodeRowID = func() (int, bool) { return 84, true }
	server.registeredNodeURL = func() (string, bool) { return "https://new-registered-node.example", true }
	store := &stubRecipeStore{ok: true, card: &playback.RecipeCard{TranscodeTransportID: "dormant-transport"}}
	server.SetRecipeStore(store)

	if err := server.teardownForForceReload(t.Context(), "https://old-registered-node.example", 84); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(store.revokedNodeIDs, []int{84}) {
		t.Fatalf("stable node authority revocations = %v, want [84]", store.revokedNodeIDs)
	}

	wantRevoked := []string{"https://old-registered-node.example", "https://new-registered-node.example"}
	if !slices.Equal(store.revokedNodes, wantRevoked) {
		t.Fatalf("node authority revocations = %v, want %v", store.revokedNodes, wantRevoked)
	}
	if len(store.deletes) != 0 {
		t.Fatalf("dormant authority was unexpectedly enumerated: deletes = %v", store.deletes)
	}
}

func TestForceReloadTeardownRetriesFailedPreviousURLRevocation(t *testing.T) {
	server := newTestServer(t)
	server.registeredNodeURL = func() (string, bool) { return "https://new-registered-node.example", true }
	oldURL := "https://old-registered-node.example"
	store := &stubRecipeStore{revokeErrs: map[string]error{oldURL: errors.New("redis unavailable")}}
	server.SetRecipeStore(store)

	if err := server.teardownForForceReload(t.Context(), oldURL, 0); err == nil {
		t.Fatal("partial authority revocation unexpectedly succeeded")
	}
	delete(store.revokeErrs, oldURL)
	if err := server.teardownForForceReload(t.Context(), "https://new-registered-node.example", 0); err != nil {
		t.Fatalf("retry authority revocation: %v", err)
	}

	wantRevoked := []string{
		oldURL, "https://new-registered-node.example",
		oldURL, "https://new-registered-node.example",
	}
	if !slices.Equal(store.revokedNodes, wantRevoked) {
		t.Fatalf("node authority revocations = %v, want %v", store.revokedNodes, wantRevoked)
	}
	if len(server.pendingAuthorityRevocations) != 0 {
		t.Fatalf("pending authority revocations = %v, want none after retry", server.pendingAuthorityRevocations)
	}
}

func TestForceReloadTeardownRetriesStableNodeIDAfterProcessRestart(t *testing.T) {
	const nodeID = 84
	store := &stubRecipeStore{revokeNodeIDErr: errors.New("redis unavailable")}
	first := newTestServer(t)
	first.nodeRowID = func() (int, bool) { return nodeID, true }
	first.SetRecipeStore(store)

	if err := first.teardownForForceReload(t.Context(), "", nodeID); err == nil {
		t.Fatal("failed stable authority revocation unexpectedly succeeded")
	}

	// A replacement process has no pending in-memory revocations. Resolving the
	// same stream_nodes row must nevertheless retry the durable generation.
	store.revokeNodeIDErr = nil
	restarted := newTestServer(t)
	restarted.nodeRowID = func() (int, bool) { return nodeID, true }
	restarted.SetRecipeStore(store)
	if err := restarted.teardownForForceReload(t.Context(), "", nodeID); err != nil {
		t.Fatalf("retry stable authority revocation after restart: %v", err)
	}
	if !slices.Equal(store.revokedNodeIDs, []int{nodeID, nodeID}) {
		t.Fatalf("stable node authority revocations = %v, want [%d %d]", store.revokedNodeIDs, nodeID, nodeID)
	}
}

func requestWithToken(sessionID, token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/transcode/"+sessionID+"/master.m3u8", nil)
	if token != "" {
		r.Header.Set("X-Silo-Stream-Token", token)
	}
	return r
}

func transcodeCard(sessionID string) playback.RecipeCard {
	return playback.NewRecipeCard(7, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID:        sessionID,
		InputPath:        "/media/movie.mkv",
		TargetCodecVideo: "h264",
		SegmentDuration:  6,
	})
}

// reconstructFromToken must refuse — without spawning ffmpeg — every request that
// does not carry a valid, matching transcode token. These guards run before any
// StartTranscode, so they are safe to assert without ffmpeg or a media file.
func TestReconstructFromToken_RejectsUnusableTokens(t *testing.T) {
	const sid = "sess-123"
	s := newTestServer(t)

	t.Run("missing token header", func(t *testing.T) {
		if got, _ := s.reconstructFromToken(requestWithToken(sid, ""), sid, -1); got != nil {
			t.Fatalf("expected nil for missing token, got %v", got)
		}
	})

	t.Run("invalid signature", func(t *testing.T) {
		bad, err := streamtoken.Sign(transcodeCard(sid).ToClaims(), "wrong-secret", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if got, _ := s.reconstructFromToken(requestWithToken(sid, bad), sid, -1); got != nil {
			t.Fatalf("expected nil for bad signature, got %v", got)
		}
	})

	t.Run("session id mismatch", func(t *testing.T) {
		tok := signCard(t, transcodeCard("other-session"))
		if got, _ := s.reconstructFromToken(requestWithToken(sid, tok), sid, -1); got != nil {
			t.Fatalf("expected nil for session id mismatch, got %v", got)
		}
	})

	t.Run("non-transcode card", func(t *testing.T) {
		tok := signCard(t, playback.NewDirectRecipeCard(sid, 7, "profile-1", 42))
		if got, _ := s.reconstructFromToken(requestWithToken(sid, tok), sid, -1); got != nil {
			t.Fatalf("expected nil for direct-play card, got %v", got)
		}
	})

	// The jellycompat node hop signs an identity-only transcode token (the recipe
	// lives in the central compat store). Its card decodes as PlayTranscode for the
	// right session id but with no encode parameters; with no recipe store wired the
	// node must refuse it rather than spawn a malformed ffmpeg.
	t.Run("recipe-less transcode token, no recipe store", func(t *testing.T) {
		tok := signCard(t, playback.RecipeCard{
			SessionID:  sid,
			UserID:     7,
			PlayMethod: playback.PlayTranscode,
			InputPath:  "/media/movie.mkv",
		})
		if got, _ := s.reconstructFromToken(requestWithToken(sid, tok), sid, 5); got != nil {
			t.Fatalf("expected nil for recipe-less transcode token, got %v", got)
		}
	})
}

func TestReconstructionEndpointsClassifyExecutorDiscoveryFailure(t *testing.T) {
	for _, endpoint := range []string{"manifest", "segment"} {
		t.Run(endpoint, func(t *testing.T) {
			const sid = "tone-map-executor-unavailable"
			server := newTestServer(t)
			server.resolveToneMapRecipeFn = func(context.Context, *playback.TranscodeOpts) error {
				return fmt.Errorf("%w: %w", playback.ErrToneMapExecutorUnavailable, context.DeadlineExceeded)
			}
			card := transcodeCard(sid)
			card.ToneMapPolicy = tonemap.PolicySoftwareOnly
			card.ToneMapMode = tonemap.ModeSoftware
			card.ToneMapSourceKind = tonemap.SourcePQ
			card.ToneMapRecipeVersion = playback.TransformationHDRToSDRToneMapRecipeVersionV3
			card.ToneMapSourceRevision = tonemap.SourceRevision{MediaFileID: 42, FileSize: 1}

			params := map[string]string{"session_id": sid}
			if endpoint == "segment" {
				params["name"] = "0.ts"
			}
			request := withNodeRouteParams(requestWithToken(sid, signCard(t, card)), params)
			recorder := httptest.NewRecorder()
			if endpoint == "manifest" {
				server.handleManifest(recorder, request)
			} else {
				server.handleSegment(recorder, request)
			}
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503; body = %s", recorder.Code, recorder.Body.String())
			}
			if got := recorder.Header().Get(ToneMapExecutionErrorHeader); got != ToneMapExecutorUnavailableCode {
				t.Fatalf("classification = %q, want %q", got, ToneMapExecutorUnavailableCode)
			}
		})
	}
}

// stubRecipeStore is a recipeStore for the jellycompat node-restart fetch path.
type stubRecipeStore struct {
	card            *playback.RecipeCard
	ok              bool
	hits            int
	deletes         []string
	revokedNodes    []string
	revokedNodeIDs  []int
	delErr          error
	revokeErr       error
	revokeErrs      map[string]error
	revokeNodeIDErr error
	getHit          chan struct{}
}

func (s *stubRecipeStore) Get(context.Context, string) (*playback.RecipeCard, bool) {
	s.hits++
	if s.getHit != nil {
		select {
		case s.getHit <- struct{}{}:
		default:
		}
	}
	return s.card, s.ok
}

func (s *stubRecipeStore) DeleteUnlessMoved(_ context.Context, sessionID string, executor playback.RecipeExecutor) (bool, error) {
	if s.card != nil && executor.NamesAnotherNode(*s.card) {
		return true, nil
	}
	s.deletes = append(s.deletes, sessionID)
	if s.delErr != nil {
		return false, s.delErr
	}
	s.card = nil
	s.ok = false
	return false, nil
}

func (s *stubRecipeStore) RevokeNode(_ context.Context, nodeURL string) error {
	s.revokedNodes = append(s.revokedNodes, nodeURL)
	if err := s.revokeErrs[nodeURL]; err != nil {
		return err
	}
	return s.revokeErr
}

func (s *stubRecipeStore) RevokeNodeID(_ context.Context, nodeID int) error {
	s.revokedNodeIDs = append(s.revokedNodeIDs, nodeID)
	return s.revokeNodeIDErr
}

// When the forwarded token is recipe-less (jellycompat), the node consults the
// recipe store. A miss or an incomplete recipe must yield a clean nil (404) with
// no ffmpeg spawn — these assert the resolve guards without needing ffmpeg.
func TestReconstructFromToken_JellycompatRecipeFetch(t *testing.T) {
	const sid = "compat-sess-1"
	recipeLessToken := func(t *testing.T) string {
		return signCard(t, playback.RecipeCard{
			SessionID:  sid,
			UserID:     7,
			PlayMethod: playback.PlayTranscode,
			InputPath:  "/media/movie.mkv",
		})
	}

	t.Run("store miss -> nil", func(t *testing.T) {
		s := newTestServer(t)
		store := &stubRecipeStore{ok: false}
		s.SetRecipeStore(store)
		if got, _ := s.reconstructFromToken(requestWithToken(sid, recipeLessToken(t)), sid, 5); got != nil {
			t.Fatalf("expected nil on store miss, got %v", got)
		}
		if store.hits != 1 {
			t.Fatalf("recipe store consulted %d times, want 1", store.hits)
		}
	})

	t.Run("incomplete fetched recipe -> nil", func(t *testing.T) {
		s := newTestServer(t)
		// Right session id but missing encode params: must not spawn.
		s.SetRecipeStore(&stubRecipeStore{ok: true, card: &playback.RecipeCard{SessionID: sid, PlayMethod: playback.PlayTranscode}})
		if got, _ := s.reconstructFromToken(requestWithToken(sid, recipeLessToken(t)), sid, 5); got != nil {
			t.Fatalf("expected nil for incomplete fetched recipe, got %v", got)
		}
	})

	t.Run("fetched recipe for wrong session -> nil", func(t *testing.T) {
		s := newTestServer(t)
		s.SetRecipeStore(&stubRecipeStore{ok: true, card: &playback.RecipeCard{
			SessionID: "other", PlayMethod: playback.PlayTranscode, SegmentDuration: 6, TargetCodecVideo: "h264",
		}})
		if got, _ := s.reconstructFromToken(requestWithToken(sid, recipeLessToken(t)), sid, 5); got != nil {
			t.Fatalf("expected nil for wrong-session recipe, got %v", got)
		}
	})
}

// nativeTransportCard is the shape central stores for a header-authenticated
// remote transcode: the recipe is keyed by the plan-scoped TRANSPORT id the node
// serves it under, which is not the playback session id.
func nativeTransportCard(sessionID, transportID string) *playback.RecipeCard {
	return &playback.RecipeCard{
		SessionID:            sessionID,
		TranscodeTransportID: transportID,
		PlayMethod:           playback.PlayTranscode,
		InputPath:            "/media/movie.mkv",
		TargetCodecVideo:     "h264",
		TargetCodecAudio:     "aac",
		SegmentDuration:      2,
	}
}

// A header-authenticated attempt publishes no stream token, so nothing forwards
// one to this node — the request that arrives after a node restart carries only
// the static bearer that already authorized it. The stored recipe is then the
// only reconstruct source, and it is keyed by the transport id in the URL, so
// the node must accept the native (TranscodeTransportID) card shape too.
func TestReconstructFromToken_TokenlessRebuildsFromTheStoredTransportRecipe(t *testing.T) {
	const sessionID = "sess-tokenless-1"
	const transportID = sessionID + "-plan1234-abcd1234"

	s := newLoopingFFmpegServer(t)
	store := &stubRecipeStore{ok: true, card: nativeTransportCard(sessionID, transportID)}
	s.SetRecipeStore(store)

	session, err := s.reconstructFromToken(requestWithToken(transportID, ""), transportID, 5)
	if err != nil {
		t.Fatalf("reconstructFromToken() error = %v", err)
	}
	if session == nil {
		t.Fatal("tokenless request did not reconstruct; a node restart would 404 this session until the client replans")
	}
	defer func() { _ = session.CloseProcess() }()
	if store.hits != 1 {
		t.Fatalf("recipe store consulted %d times, want 1", store.hits)
	}
	if got := session.Opts().SessionID; got != transportID {
		t.Fatalf("rebuilt session id = %q, want the transport id %q the node serves under", got, transportID)
	}
	if got := session.Opts().StartSegmentNumber; got != 5 {
		t.Fatalf("rebuilt start segment = %d, want the segment the client is fetching", got)
	}
}

// Without a recipe to rebuild from, a tokenless request is still a genuine
// not-found: the node must never spawn ffmpeg on a guess.
func TestReconstructFromToken_TokenlessWithoutARecipeIsNotFound(t *testing.T) {
	const transportID = "sess-tokenless-2-plan1234-abcd1234"

	t.Run("no recipe store wired", func(t *testing.T) {
		s := newTestServer(t)
		if got, _ := s.reconstructFromToken(requestWithToken(transportID, ""), transportID, 5); got != nil {
			t.Fatalf("expected nil without a recipe store, got %v", got)
		}
	})

	t.Run("store miss", func(t *testing.T) {
		s := newTestServer(t)
		store := &stubRecipeStore{ok: false}
		s.SetRecipeStore(store)
		if got, _ := s.reconstructFromToken(requestWithToken(transportID, ""), transportID, 5); got != nil {
			t.Fatalf("expected nil on store miss, got %v", got)
		}
		if store.hits != 1 {
			t.Fatalf("recipe store consulted %d times, want 1", store.hits)
		}
	})

	t.Run("stored recipe for another transport", func(t *testing.T) {
		s := newTestServer(t)
		s.SetRecipeStore(&stubRecipeStore{ok: true, card: nativeTransportCard("sess-tokenless-2", "some-other-transport")})
		if got, _ := s.reconstructFromToken(requestWithToken(transportID, ""), transportID, 5); got != nil {
			t.Fatalf("expected nil for a recipe keyed to another transport, got %v", got)
		}
	})
}

// handleStop is a deliberate teardown, so it must drop the session's recipe to
// stop a buffered/retrying post-restart request from reconstructing a brand-new
// ffmpeg for an already-stopped session. A zero-value TranscodeSession needs no
// ffmpeg or media file to Close, so this asserts the wiring without a real spawn.
func TestHandleStop_DeletesRecipe(t *testing.T) {
	const sid = "stop-sess-1"
	s := newTestServer(t)
	s.tracker = nodesessions.NewTracker(nil, "node-url", "node-name", "transcode")
	store := &stubRecipeStore{}
	s.SetRecipeStore(store)

	s.sessions[sid] = &playback.TranscodeSession{}
	s.activeJobs.Store(1)

	rec := httptest.NewRecorder()
	s.handleStop(rec, stopRequest(sid))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("handleStop status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if len(store.deletes) != 1 || store.deletes[0] != sid {
		t.Fatalf("recipe deletes = %v, want [%q]", store.deletes, sid)
	}
	if _, ok := s.sessions[sid]; ok {
		t.Fatalf("session %q still registered after stop", sid)
	}
}

// A stop that stalls on a hung node can run after the API has moved the
// transport to another node and stored that node's card under the same
// transport id. The stop deletes this node's own card, never the successor's.
func TestHandleStop_KeepsRecipeMovedToAnotherNode(t *testing.T) {
	const sid = "stop-moved-1"
	for _, tc := range []struct {
		name       string
		nodeRowID  int
		card       playback.RecipeCard
		wantDelete bool
	}{
		{"own card", 1, playback.RecipeCard{TranscodeNodeURL: "http://old:9000", RoutingExecutionNodeID: 1}, true},
		{"moved to another node", 1, playback.RecipeCard{TranscodeNodeURL: "http://new:9000", RoutingExecutionNodeID: 2}, false},
		// Without a row id on the card a move cannot be told from a URL edit,
		// so the stop deletes it as before.
		{"card without a row id", 1, playback.RecipeCard{TranscodeNodeURL: "http://new:9000"}, true},
		// A node deleted and added again gets a new row; the cards it issued
		// before still name its URL.
		{"node re-added under a new row", 3, playback.RecipeCard{TranscodeNodeURL: "http://old:9000", RoutingExecutionNodeID: 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			s.tracker = nodesessions.NewTracker(nil, "node-url", "node-name", "transcode")
			s.nodeRowID = func() (int, bool) { return tc.nodeRowID, true }
			s.registeredNodeURL = func() (string, bool) { return "http://old:9000", true }
			card := tc.card
			store := &stubRecipeStore{card: &card, ok: true}
			s.SetRecipeStore(store)
			s.sessions[sid] = &playback.TranscodeSession{}
			s.activeJobs.Store(1)

			r := httptest.NewRequest(http.MethodDelete, "/transcode/"+sid, nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("session_id", sid)
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
			rec := httptest.NewRecorder()
			s.handleStop(rec, r)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("handleStop status = %d, want %d", rec.Code, http.StatusNoContent)
			}
			if deleted := len(store.deletes) == 1; deleted != tc.wantDelete {
				t.Fatalf("recipe deletes = %v, want delete %v", store.deletes, tc.wantDelete)
			}
		})
	}
}

// The node's registration can change while a job runs. A stop judges the
// stored card against the identity the job was registered under, so a node
// re-created under a new row and URL still deletes its own card, and one whose
// registration is no longer resolved still keeps the card of the node the
// transport moved to.
func TestHandleStop_JudgesRecipeByTheIdentityTheJobStartedUnder(t *testing.T) {
	const sid = "stop-started-as-1"
	startedAs := playback.RecipeExecutor{NodeID: 1, NodeURL: "http://old:9000"}
	for _, tc := range []struct {
		name       string
		currentID  int
		currentURL string
		card       playback.RecipeCard
		wantDelete bool
	}{
		{"re-created under a new row and URL", 5, "http://alias:9000", playback.RecipeCard{TranscodeNodeURL: "http://old:9000", RoutingExecutionNodeID: 1}, true},
		{"registration unresolved since, card moved", 0, "", playback.RecipeCard{TranscodeNodeURL: "http://new:9000", RoutingExecutionNodeID: 2}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			s.tracker = nodesessions.NewTracker(nil, "node-url", "node-name", "transcode")
			s.nodeRowID = func() (int, bool) { return tc.currentID, tc.currentID > 0 }
			s.registeredNodeURL = func() (string, bool) { return tc.currentURL, tc.currentURL != "" }
			card := tc.card
			store := &stubRecipeStore{card: &card, ok: true}
			s.SetRecipeStore(store)
			s.sessions[sid] = &playback.TranscodeSession{}
			s.noteStartedAsLocked(sid, startedAs)
			s.activeJobs.Store(1)

			r := httptest.NewRequest(http.MethodDelete, "/transcode/"+sid, nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("session_id", sid)
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
			rec := httptest.NewRecorder()
			s.handleStop(rec, r)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("handleStop status = %d, want %d", rec.Code, http.StatusNoContent)
			}
			if deleted := len(store.deletes) == 1; deleted != tc.wantDelete {
				t.Fatalf("recipe deletes = %v, want delete %v", store.deletes, tc.wantDelete)
			}
			if _, recorded := s.startedAs[sid]; recorded {
				t.Fatal("the stop left the job's start identity behind")
			}
		})
	}
}

// A hung node that resumes keeps the transport it lost in its session map
// when the API's late stop is refused. A force reload of that node tears the
// job down but leaves the card of the node the transport moved to.
func TestForceReloadTeardownKeepsRecipeMovedToAnotherNode(t *testing.T) {
	const sid = "reload-moved-1"
	for _, tc := range []struct {
		name       string
		card       playback.RecipeCard
		wantDelete bool
	}{
		{"own card", playback.RecipeCard{TranscodeNodeURL: "http://old:9000", RoutingExecutionNodeID: 1}, true},
		{"moved to another node", playback.RecipeCard{TranscodeNodeURL: "http://new:9000", RoutingExecutionNodeID: 2}, false},
		// The node's URL was edited since it started this transport.
		{"own URL-only card under an edited URL", playback.RecipeCard{TranscodeNodeURL: "http://before-edit:9000"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			s.tracker = nodesessions.NewTracker(nil, "node-url", "node-name", "transcode")
			s.nodeRowID = func() (int, bool) { return 1, true }
			s.registeredNodeURL = func() (string, bool) { return "http://old:9000", true }
			card := tc.card
			store := &stubRecipeStore{card: &card, ok: true}
			s.SetRecipeStore(store)
			s.sessions[sid] = &playback.TranscodeSession{}
			s.activeJobs.Store(1)

			if err := s.teardownForForceReload(t.Context(), "http://old:9000", 1); err != nil {
				t.Fatalf("teardown: %v", err)
			}
			if _, running := s.sessions[sid]; running {
				t.Fatal("force reload left the job running")
			}
			if deleted := len(store.deletes) == 1; deleted != tc.wantDelete {
				t.Fatalf("recipe deletes = %v, want delete %v", store.deletes, tc.wantDelete)
			}
		})
	}
}

// Regression for #2265: a stop kills the job, and a segment request for the
// same transport that was already in flight reaches the node right after. The
// node must answer it 410 rather than rebuild the job from the request's
// stream token, which would encode for nobody until the idle reaper. A node
// that lost the job some other way (a restart) must still rebuild it.
func TestHandleStop_LateSegmentDoesNotRebuildStoppedTranscode(t *testing.T) {
	const sessionID = "sess-late-segment"
	const transportID = sessionID + "-plan1234-abcd1234"

	ffmpegPath := filepath.Join(t.TempDir(), "looping-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nwhile :; do sleep 0.1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	newNode := func() *Server {
		s := newTestServer(t)
		s.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
		s.watcher.Config().Playback.FFmpegPath = ffmpegPath
		return s
	}
	token := signCard(t, *nativeTransportCard(sessionID, transportID))
	segmentRequest := func() *http.Request {
		const name = "seg_00005.ts"
		r := httptest.NewRequest(http.MethodGet, "/transcode/"+transportID+"/segment/"+name, nil)
		r.Header.Set("X-Silo-Stream-Token", token)
		return withNodeRouteParams(r, map[string]string{"session_id": transportID, "name": name})
	}
	registered := func(s *Server) *playback.TranscodeSession {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.sessions[transportID]
	}

	s := newNode()
	running, err := s.reconstructFromToken(requestWithToken(transportID, token), transportID, 5)
	if err != nil || running == nil {
		t.Fatalf("set up running job: session = %v, err = %v", running, err)
	}

	stop := withNodeRouteParams(httptest.NewRequest(http.MethodDelete, "/transcode/"+transportID, nil), map[string]string{"session_id": transportID})
	stopRec := httptest.NewRecorder()
	s.handleStop(stopRec, stop)
	if stopRec.Code != http.StatusNoContent {
		t.Fatalf("stop status = %d, want %d", stopRec.Code, http.StatusNoContent)
	}

	late := httptest.NewRecorder()
	s.handleSegment(late, segmentRequest())
	if late.Code != http.StatusGone {
		t.Fatalf("late segment status = %d, want %d (body %q)", late.Code, http.StatusGone, late.Body.String())
	}
	if got := registered(s); got != nil {
		_ = got.CloseProcess()
		t.Fatal("late segment rebuilt the stopped transcode")
	}

	// A request that found no job while the stop held the lifecycle lock reaches
	// spawnReconstruct past the early check; the fence must hold there too.
	if got, err := s.spawnReconstruct(segmentRequest(), transportID, 5, *nativeTransportCard(sessionID, transportID)); got != nil || !errors.Is(err, errTranscodeStopped) {
		if got != nil {
			_ = got.CloseProcess()
		}
		t.Fatalf("spawnReconstruct after stop = (%v, %v), want (nil, errTranscodeStopped)", got, err)
	}

	// A replacement process has no record of the stop and still recovers the
	// client's session from its token.
	restarted := newNode()
	rebuilt, err := restarted.reconstructFromToken(segmentRequest(), transportID, 5)
	if err != nil || rebuilt == nil {
		t.Fatalf("restarted node did not rebuild the session: session = %v, err = %v", rebuilt, err)
	}

	// A release moves the transport to another node under the same id. A
	// replica whose copy still names this node must still be able to rebuild it.
	release := withNodeRouteParams(httptest.NewRequest(http.MethodDelete, "/transcode/"+transportID+"?fence=0", nil), map[string]string{"session_id": transportID})
	releaseRec := httptest.NewRecorder()
	restarted.handleStop(releaseRec, release)
	if releaseRec.Code != http.StatusNoContent {
		t.Fatalf("release status = %d, want %d", releaseRec.Code, http.StatusNoContent)
	}
	rebuilt, err = restarted.reconstructFromToken(segmentRequest(), transportID, 5)
	if err != nil || rebuilt == nil {
		t.Fatalf("released transport was not rebuilt: session = %v, err = %v", rebuilt, err)
	}
	_ = rebuilt.CloseProcess()
}

// A fresh start under a stopped transport id is the API choosing that
// transport again (a Jellyfin software retry reuses the id right after
// stopping it), so it lifts the stop fence: a later idle reap must leave the
// job recoverable from its token.
func TestHandleStart_LiftsStopFence(t *testing.T) {
	const sessionID = "sess-restarted-after-stop"
	s := newTestServer(t)
	s.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
	ffmpegPath := filepath.Join(t.TempDir(), "looping-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nwhile :; do sleep 0.1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.watcher.Config().Playback.FFmpegPath = ffmpegPath

	stop := withNodeRouteParams(httptest.NewRequest(http.MethodDelete, "/transcode/"+sessionID, nil), map[string]string{"session_id": sessionID})
	s.handleStop(httptest.NewRecorder(), stop)
	if !s.transcodeStopped(sessionID) {
		t.Fatal("stop did not fence the transport")
	}

	body, err := json.Marshal(TranscodeStartRequest{
		SessionID: sessionID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handleStart(rec, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	s.mu.RLock()
	session := s.sessions[sessionID]
	s.mu.RUnlock()
	if session == nil {
		t.Fatal("start did not register the session")
	}
	_ = session.CloseProcess()
	if s.transcodeStopped(sessionID) {
		t.Fatal("start left the stop fence in place; an idle-reaped job could not be rebuilt")
	}
}

// The idle reaper must close only jobs whose last access predates the TTL;
// registration counts as an access, so a just-started job (including one still
// waiting on its manifest in the RequireReady flow) is spared. Zero-value
// TranscodeSessions Close without ffmpeg, so this runs without a real spawn.
func TestReapIdleSessions_ClosesOnlyIdleJobs(t *testing.T) {
	s := newTestServer(t)
	s.tracker = nodesessions.NewTracker(nil, "node-url", "node-name", "transcode")

	s.sessions["fresh-1"] = &playback.TranscodeSession{}
	s.sessions["stale-1"] = &playback.TranscodeSession{}
	s.lastAccess = map[string]time.Time{
		"fresh-1": time.Now(),
		"stale-1": time.Now().Add(-sessionIdleTTL - time.Minute),
	}
	s.activeJobs.Store(2)

	s.reapIdleSessions(sessionIdleTTL)

	s.mu.RLock()
	_, freshAlive := s.sessions["fresh-1"]
	_, staleAlive := s.sessions["stale-1"]
	_, staleTracked := s.lastAccess["stale-1"]
	s.mu.RUnlock()
	if !freshAlive {
		t.Fatal("recently accessed session was reaped")
	}
	if staleAlive {
		t.Fatal("idle session survived the reaper")
	}
	if staleTracked {
		t.Fatal("reaped session's idle clock was not dropped")
	}
	if got := s.activeJobs.Load(); got != 1 {
		t.Fatalf("activeJobs = %d, want 1", got)
	}
}

// A registered job with no recorded access (untracked registration) must not
// be closed; the sweep starts its idle clock instead of reaping a job that may
// be actively serving.
func TestReapIdleSessions_StartsClockForUntrackedJob(t *testing.T) {
	s := newTestServer(t)
	s.sessions["untracked-1"] = &playback.TranscodeSession{}
	s.activeJobs.Store(1)

	s.reapIdleSessions(sessionIdleTTL)

	s.mu.RLock()
	_, alive := s.sessions["untracked-1"]
	last, tracked := s.lastAccess["untracked-1"]
	s.mu.RUnlock()
	if !alive {
		t.Fatal("untracked session was reaped")
	}
	if !tracked || last.IsZero() {
		t.Fatal("sweep did not start the untracked session's idle clock")
	}
	if got := s.activeJobs.Load(); got != 1 {
		t.Fatalf("activeJobs = %d, want 1", got)
	}
}

// touchSession must refresh a registered job's idle clock and ignore ids with
// no live session (a reconstruct records its own first access on register).
func TestTouchSession_RefreshesIdleClock(t *testing.T) {
	s := newTestServer(t)
	s.sessions["live-1"] = &playback.TranscodeSession{}
	stale := time.Now().Add(-sessionIdleTTL - time.Minute)
	s.lastAccess = map[string]time.Time{"live-1": stale}

	s.touchSession("live-1")
	s.touchSession("ghost-1")

	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.lastAccess["live-1"].After(stale) {
		t.Fatal("touch did not refresh the live session's idle clock")
	}
	if _, ok := s.lastAccess["ghost-1"]; ok {
		t.Fatal("touch recorded access for an unregistered session")
	}
}

func TestSpawnReconstructRejectsSessionIDOutsideTranscodeRoot(t *testing.T) {
	server := newTestServer(t)
	logPath := filepath.Join(t.TempDir(), "invocations.log")
	useFakeFFmpeg(t, server, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+logPath+"\nexit 1\n")

	const sessionID = ".."
	card := *nativeTransportCard(sessionID, sessionID)
	session, err := server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), sessionID, -1, card)
	if session != nil {
		_ = session.Close()
		t.Fatal("reconstruct registered a session outside the transcode root")
	}
	if err != nil {
		t.Fatalf("spawnReconstruct err = %v, want nil", err)
	}
	if _, statErr := os.Stat(logPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("ffmpeg ran for a rejected session id (stat err %v)", statErr)
	}
}

func TestSpawnReconstructDoesNotRegisterFailedSoftwareRetry(t *testing.T) {
	server := newTestServer(t)
	logPath := filepath.Join(t.TempDir(), "invocations.log")
	useFakeFFmpeg(t, server, "#!/bin/sh\n"+
		"printf '%s\\n' \"$*\" >> "+logPath+"\n"+
		"case \"$*\" in\n"+
		"  *-hwaccels*) echo videotoolbox; exit 0 ;;\n"+
		"  *-encoders*) echo ' V..... h264_videotoolbox x'; exit 0 ;;\n"+
		"  *videotoolbox*'-f null'*) exit 0 ;;\n"+
		"  *) exit 1 ;;\n"+
		"esac\n")
	server.watcher.Config().Playback.HWAccel = tonemap.BackendVideoToolbox

	const sessionID = "failed-reconstruct-retry"
	card := playback.NewRecipeCard(7, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID: sessionID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "720p", TargetBitrateKbps: 2000,
		SegmentDuration: 2,
	})
	session, err := server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), sessionID, -1, card)
	if session != nil {
		_ = session.Close()
		t.Fatal("failed software retry returned a session")
	}
	if err == nil {
		t.Fatal("failed software retry returned no error")
	}
	server.mu.RLock()
	_, registered := server.sessions[sessionID]
	server.mu.RUnlock()
	if registered {
		t.Fatal("failed software retry was registered")
	}
	logData, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := strings.Count(string(logData), "-f hls"); got != 2 {
		t.Fatalf("real transcode attempts = %d, want hardware plus software retry:\n%s", got, logData)
	}
	if !strings.Contains(string(logData), "libx264") {
		t.Fatalf("reconstruction did not attempt the software retry:\n%s", logData)
	}
}

// A fresh /transcode/start must resolve this node's configured hw_device list
// through the shared GPU pool — the same path reconstruction uses — rather
// than bypassing it with an empty device.
func TestHandleStartUsesConfiguredHWDeviceList(t *testing.T) {
	server := newLoopingFFmpegServer(t)
	// Neither device exists, so resolution deterministically lands on the
	// first entry; the point is that the configured list reaches the session.
	server.watcher.Config().Playback.HWDevice = "/dev/dri/renderD888,/dev/dri/renderD889"

	_, session := startRegisteredSession(t, server, TranscodeStartRequest{
		SessionID: "hwdevice-start-1", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: "vaapi",
	})
	if got := session.Opts().HWDevice; got != "/dev/dri/renderD888" {
		t.Fatalf("session HWDevice = %q, want one concrete device from the configured list", got)
	}
}

func TestHandleStartAndReconstructArmRequestedThrottle(t *testing.T) {
	for _, test := range []struct {
		name        string
		reconstruct bool
	}{
		{name: "fresh start"},
		{name: "reconstruct", reconstruct: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newLoopingFFmpegServer(t)
			started := make(chan int, 1)
			server.startThrottler = func(_ *playback.TranscodeSession, seconds int) { started <- seconds }

			if test.reconstruct {
				card := nativeTransportCard("session-1", "transport-1")
				card.ThrottleSeconds = 180
				session, err := server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), "transport-1", -1, *card)
				if err != nil {
					t.Fatalf("spawnReconstruct: %v", err)
				}
				if session == nil {
					t.Fatal("session was not registered")
				}
				t.Cleanup(func() { _ = session.CloseProcess() })
			} else {
				startRegisteredSession(t, server, TranscodeStartRequest{
					SessionID: "session-1", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264",
					TargetCodecAudio: "aac", SegmentDuration: 2, ThrottleSeconds: 180,
				})
			}
			select {
			case got := <-started:
				if got != 180 {
					t.Fatalf("throttle seconds = %d, want 180", got)
				}
			case <-time.After(time.Second):
				t.Fatal("node did not arm the requested throttle")
			}
		})
	}
}

func TestShutdownDrainsSessionsAndRejectsNewStarts(t *testing.T) {
	server := newTestServer(t)
	for _, id := range []string{"one", "two"} {
		dir := server.sessionOutputDir(id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		server.sessions[id] = playback.NewTranscodeSessionForTest(dir)
		server.activeJobs.Add(1)
	}

	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := server.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs = %d, want 0", got)
	}
	server.mu.RLock()
	remaining := len(server.sessions)
	server.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("remaining sessions = %d, want 0", remaining)
	}
	for _, id := range []string{"one", "two"} {
		if _, err := os.Stat(server.sessionOutputDir(id)); !os.IsNotExist(err) {
			t.Fatalf("session directory %q still exists (err=%v)", id, err)
		}
	}

	rr := postStartRequest(t, server, TranscodeStartRequest{SessionID: "late", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", SegmentDuration: 2})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("late start status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

// TestHandleStartRejectsIncompleteOrStaleToneMapRecipe verifies nodes reject unsafe frozen recipes.
func TestHandleStartRejectsIncompleteOrStaleToneMapRecipe(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*TranscodeStartRequest)
	}{
		{name: "mode without policy", mutate: func(request *TranscodeStartRequest) {
			request.ToneMapPolicy = ""
		}},
		{name: "stale version", mutate: func(request *TranscodeStartRequest) {
			request.ToneMapRecipeVersion = "stale"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			request := TranscodeStartRequest{
				SessionID: "tone-map-invalid", InputPath: "/media/movie.mkv",
				TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
				ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
				ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
				ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1},
			}
			tt.mutate(&request)
			recorder := postStartRequest(t, server, request)
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if got := recorder.Header().Get(ToneMapExecutionErrorHeader); got != "" {
				t.Fatalf("invalid recipe carried live-validation classification %q", got)
			}
			server.mu.RLock()
			sessionCount := len(server.sessions)
			server.mu.RUnlock()
			if server.activeJobs.Load() != 0 || sessionCount != 0 {
				t.Fatal("invalid tone-map recipe started a node job")
			}
		})
	}
}

func TestHandleStartConfigChangeIsNotLiveSourceValidationFailure(t *testing.T) {
	server := newTestServer(t)
	dir := t.TempDir()
	probeStarted := filepath.Join(dir, "probe-started")
	releaseProbe := filepath.Join(dir, "release-probe")
	useFakeFFmpeg(t, server, "#!/bin/sh\n"+
		"touch '"+probeStarted+"'\n"+
		"while [ ! -e '"+releaseProbe+"' ]; do sleep 0.01; done\n"+
		"case \" $* \" in\n"+
		"  *\" -filters \"*) printf ' T.. zscale V->V scale\\n T.. tonemapx V->V tone-map\\n T.. sidedata V->V metadata\\n';;\n"+
		"  *\" -encoders \"*) printf ' V..... libx264 H.264\\n';;\n"+
		"esac\n")
	request := jsonRequest(t, "/transcode/start", TranscodeStartRequest{
		SessionID: "config-change", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1},
	})
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.handleStart(recorder, request)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(probeStarted); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tone-map capability probe did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	replacement := *server.watcher.Config()
	server.watcher.SetConfigForTest(&replacement)
	if err := os.WriteFile(releaseProbe, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("start handler did not return after capability probe release")
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get(ToneMapExecutionErrorHeader); got != "" {
		t.Fatalf("configuration change carried live-validation classification %q", got)
	}
}

func TestToneMapRecipeContextFailuresReturnServiceUnavailable(t *testing.T) {
	toneMapRequest := TranscodeStartRequest{
		SessionID: "tone-map-context", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		HWAccel:       tonemap.BackendQSV,
		ToneMapPolicy: tonemap.PolicyHardwareOnly, ToneMapMode: tonemap.ModeHardware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1},
	}
	downloadRequest := downloadprepare.Request{
		ArtifactID: "tone-map-context", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		ToneMapPolicy: tonemap.PolicyHardwareOnly, ToneMapMode: tonemap.ModeHardware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1},
	}
	tests := []struct {
		name   string
		body   any
		handle func(*Server, http.ResponseWriter, *http.Request)
	}{
		{name: "transcode start", body: toneMapRequest, handle: func(server *Server, w http.ResponseWriter, r *http.Request) { server.handleStart(w, r) }},
		{name: "download prepare", body: downloadRequest, handle: func(server *Server, w http.ResponseWriter, r *http.Request) { server.handleDownloadPrepare(w, r) }},
	}
	contexts := []struct {
		name string
		new  func() (context.Context, context.CancelFunc)
	}{
		{name: "canceled", new: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}},
	}
	for _, test := range tests {
		for _, contextTest := range contexts {
			t.Run(test.name+"/"+contextTest.name, func(t *testing.T) {
				server := newTestServer(t)
				ctx, cancel := contextTest.new()
				defer cancel()
				request := jsonRequest(t, "/", test.body).WithContext(ctx)
				recorder := httptest.NewRecorder()

				test.handle(server, recorder, request)

				if recorder.Code != http.StatusServiceUnavailable {
					t.Fatalf("status = %d, want 503; body = %s", recorder.Code, recorder.Body.String())
				}
				if got := recorder.Header().Get(ToneMapExecutionErrorHeader); got != "" {
					t.Fatalf("generic context failure carried live-validation classification %q", got)
				}
			})
		}
	}
}

func TestDownloadToneMapRecipeRejectionDoesNotWaitForArtifactLock(t *testing.T) {
	server := newTestServer(t)
	const artifactID = "tone-map-lock-order"
	unlock := server.lockSessionLifecycle("download-artifact-" + artifactID)
	defer unlock()
	request := jsonRequest(t, "/downloads/prepare", downloadprepare.Request{
		ArtifactID: artifactID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: "stale",
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1},
	})
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.handleDownloadPrepare(recorder, request)
		close(done)
	}()
	select {
	case <-done:
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("tone-map recipe rejection waited for the artifact lifecycle lock")
	}
}

func TestStartToneMapRecipeRejectionDoesNotWaitForReloadLock(t *testing.T) {
	server := newTestServer(t)
	server.reloadMu.Lock()
	defer server.reloadMu.Unlock()
	request := jsonRequest(t, "/transcode/start", TranscodeStartRequest{
		SessionID: "tone-map-start-lock-order", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: "stale",
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1},
	})
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.handleStart(recorder, request)
		close(done)
	}()
	select {
	case <-done:
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("tone-map recipe rejection waited for the reload lock")
	}
}

func TestReconstructToneMapRecipeRejectionDoesNotWaitForReloadLock(t *testing.T) {
	server := newTestServer(t)
	server.reloadMu.Lock()
	defer server.reloadMu.Unlock()
	card := playback.RecipeCard{
		SessionID: "tone-map-reconstruct-lock-order", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: "stale",
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1},
	}
	done := make(chan *playback.TranscodeSession, 1)
	go func() {
		session, _ := server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), card.SessionID, -1, card)
		done <- session
	}()
	select {
	case session := <-done:
		if session != nil {
			t.Fatal("stale tone-map reconstruction unexpectedly started")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("tone-map recipe rejection waited for the reload lock")
	}
}

func TestNodeReconstructLifecycleWaitDoesNotConsumeGlobalSlot(t *testing.T) {
	server := newTestServer(t)
	server.reconstructSem = make(chan struct{}, 1)
	server.reconstructSemOnce.Do(func() {})
	const sessionID = "blocked-node-reconstruct"
	unlock := server.lockSessionLifecycle(sessionID)
	done := make(chan struct{})
	go func() {
		_, _ = server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), sessionID, -1, transcodeCard(sessionID))
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		server.lifecycleMu.Lock()
		lock := server.lifecycleLocks[sessionID]
		refs := 0
		if lock != nil {
			refs = lock.refs
		}
		server.lifecycleMu.Unlock()
		if refs == 2 {
			break
		}
		if time.Now().After(deadline) {
			unlock()
			t.Fatal("node reconstruct did not begin waiting for the session lifecycle lock")
		}
		time.Sleep(time.Millisecond)
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	slotRelease, available := server.acquireReconstructSlot(probeCtx)
	cancel()
	if available {
		slotRelease()
	}
	server.mu.Lock()
	server.sessions[sessionID] = &playback.TranscodeSession{}
	server.mu.Unlock()
	unlock()
	<-done
	if !available {
		t.Fatal("node session-lock waiter consumed the only global reconstruct slot")
	}
}

func TestHandleStartPreservesVideoSampleEntry(t *testing.T) {
	server := newLoopingFFmpegServer(t)
	_, session := startRegisteredSession(t, server, TranscodeStartRequest{
		SessionID: "sample-entry-start-1", InputPath: "/media/movie.mkv",
		VideoSampleEntry: playback.VideoSampleEntryHVC1,
		TargetCodecVideo: "copy", TargetCodecAudio: "copy", SegmentDuration: 2,
		CopyFMP4RecipeVersion: playback.CopyFMP4RecipeVersion,
	})
	if got := session.Opts().VideoSampleEntry; got != playback.VideoSampleEntryHVC1 {
		t.Fatalf("VideoSampleEntry = %q", got)
	}
}

func TestShutdownDrainsProgressiveRemuxAndRejectsLateRequests(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 11, true }
	server.registeredNodeURL = func() (string, bool) { return "http://node", true }
	mediaPath, _ := writeNodeSource(t, "source")
	useFakeFFmpeg(t, server, "#!/bin/sh\n"+
		"for arg in \"$@\"; do\n"+
		"  if [ \"$arg\" = \"pipe:1\" ]; then\n"+
		"    while :; do printf node-remux-bytes; sleep 0.01; done\n"+
		"  fi\n"+
		"done\n"+
		"exit 0\n")
	claims := progressiveRemuxClaims("playback-force-reload", mediaPath, "transport-force-reload-real")
	card := playback.RecipeCardFromClaims(&claims)
	recipes := &stubRecipeStore{card: &card, ok: true}
	server.SetRecipeStore(recipes)
	request := remuxRequest(t, http.MethodGet, claims)
	writer := newAbortableBlockingResponseWriter()
	t.Cleanup(func() { _ = writer.SetWriteDeadline(time.Now()) })
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	t.Cleanup(cancelRequest)
	request = request.WithContext(context.WithValue(requestCtx, chi.RouteCtxKey, chi.RouteContext(request.Context())))
	handlerDone := make(chan struct{})
	go func() {
		server.handleRemux(writer, request)
		close(handlerDone)
	}()
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancelWait()
	select {
	case <-writer.writeStarted:
	case <-waitCtx.Done():
		t.Fatal("progressive remux did not reach the blocked client write")
	}
	if got := server.activeJobs.Load(); got != 1 {
		t.Fatalf("active jobs before shutdown = %d, want 1", got)
	}

	if err := server.Shutdown(waitCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handlerDone:
	case <-waitCtx.Done():
		t.Fatal("shutdown returned without draining the progressive remux handler")
	}
	if got := server.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs after shutdown = %d, want 0", got)
	}
	server.mu.RLock()
	_, active := server.progressiveRemuxes[claims.TranscodeTransportID]
	server.mu.RUnlock()
	if active {
		t.Fatal("shutdown left the drained remux registered")
	}

	if !recipes.ok || len(recipes.deletes) != 0 {
		t.Fatal("shutdown revoked reconstruction authority")
	}
	hits := recipes.hits

	// Shutdown retains durable authority, but neither HEAD nor GET may admit
	// work into this process after its drain has completed.
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		late := request.Clone(request.Context())
		late.Method = method
		rr := httptest.NewRecorder()
		server.handleRemux(rr, late)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("late %s status=%d body=%s", method, rr.Code, rr.Body.String())
		}
	}
	if recipes.hits != hits {
		t.Fatal("late request reached durable authority lookup")
	}

}

// Stops and remux registrations sweep the fence maps under mu; the sweep runs
// at most once per interval so a day of fences is not rescanned on every stop,
// while lookups keep ignoring entries that expired in between.
func TestPruneStopFencesRunsAtMostOncePerInterval(t *testing.T) {
	s := newTestServer(t)
	s.stoppedTranscodes = make(map[string]time.Time)
	start := time.Now()

	s.mu.Lock()
	s.stoppedTranscodes["expired-first"] = start.Add(-time.Second)
	s.pruneStopFencesLocked(start)
	_, kept := s.stoppedTranscodes["expired-first"]
	s.mu.Unlock()
	if kept {
		t.Fatal("first sweep left an expired fence")
	}

	s.mu.Lock()
	s.stoppedTranscodes["expired-second"] = start.Add(-time.Second)
	s.pruneStopFencesLocked(start.Add(stopFencePruneInterval / 2))
	_, kept = s.stoppedTranscodes["expired-second"]
	s.mu.Unlock()
	if !kept {
		t.Fatal("sweep ran again inside the prune interval")
	}
	if s.transcodeStopped("expired-second") {
		t.Fatal("an expired fence still refused reconstruction before the next sweep")
	}

	s.mu.Lock()
	s.pruneStopFencesLocked(start.Add(stopFencePruneInterval))
	_, kept = s.stoppedTranscodes["expired-second"]
	s.mu.Unlock()
	if kept {
		t.Fatal("sweep after the interval left an expired fence")
	}
}

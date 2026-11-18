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
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodeproxy"
)

// testNodeKey is the test node's own key (SILO_NODE_KEY), which verifies the
// requests the core signs to it. It is not the shared secret.
const testNodeKey = "snk1_1_dGVzdC1ub2RlLWtleS10ZXN0LW5vZGUta2V5LXRlc3Q"

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

// newTestServer builds a transcode Server on a test configuration, whose
// opener opens the path a job's grant names. The tracker is left nil: the
// guard-rejection cases never reach the spawn/track path.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{}
	cfg.Playback.TranscodeDir = t.TempDir()
	cfg.Download.ArtifactDir = filepath.Join(cfg.Playback.TranscodeDir, downloadprepare.ArtifactDirectoryName)
	return &Server{
		watcher:                   newTestRuntime(cfg),
		nodeKey:                   testNodeKey,
		opener:                    &pathSources{},
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

// jsonRequest posts body to path. A start or preparation request without a
// source grant is granted its input path, which the test server's opener
// opens as the source.
func jsonRequest(t *testing.T, path string, body any) *http.Request {
	t.Helper()
	switch request := body.(type) {
	case TranscodeStartRequest:
		if request.SourceGrant == "" {
			request.SourceGrant = request.InputPath
		}
		body = request
	case downloadprepare.Request:
		if request.SourceGrant == "" {
			request.SourceGrant = request.InputPath
		}
		body = request
	}
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

// remuxRequest is a progressive remux request for the claims' transport, and
// makes server's core resolve that transport to the claims' recipe with a
// grant for its media path.
func remuxRequest(t *testing.T, server *Server, method string, claims streamtoken.Claims) *http.Request {
	t.Helper()
	coreOf(server).resolve(claims.TranscodeTransportID, resolutionOf(playback.RecipeCardFromClaims(&claims)))
	request := withNodeRouteParams(httptest.NewRequest(method, "/remux/"+claims.TranscodeTransportID, nil),
		map[string]string{"session_id": claims.TranscodeTransportID})
	request.Header.Set("X-Silo-Stream-Token", "relay-forwarded-token")
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
	nodeauthtest.Sign(t, head, testNodeKey)
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
	// An older node echoes keyframe_playlist but ignores keyframe_plan_resume
	// and serves FFmpeg's playlist, whose segments the moved player can't use.
	resume := TranscodeStartRequest{KeyframePlaylist: true, KeyframePlanResume: true}
	if err := ValidateKeyframePlaylistAttestation(resume, TranscodeStartResponse{KeyframePlaylist: true}); !errors.Is(err, ErrKeyframePlaylistAttestationMismatch) {
		t.Fatalf("missing resume plan attestation error = %v, want %v", err, ErrKeyframePlaylistAttestationMismatch)
	}
	if err := ValidateKeyframePlaylistAttestation(resume, TranscodeStartResponse{KeyframePlaylist: true, KeyframePlanResume: true}); err != nil {
		t.Fatalf("matching resume plan attestation rejected: %v", err)
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

// A node that ignores the copied codec writes AAC into fMP4 without
// aac_adtstoasc and fails after the first segment. Runs that need no filter
// keep working on older nodes, which omit the receipt.
func TestValidateCopyAudioCodecAttestation(t *testing.T) {
	aacIntoFMP4 := TranscodeStartRequest{SourceVideoCodec: "h264", TargetCodecVideo: "copy", TargetCodecAudio: "copy", CopyAudioCodec: "aac"}
	aacIntoMPEGTS := aacIntoFMP4
	aacIntoMPEGTS.CopyVideoMPEGTS = true
	ac3IntoFMP4 := aacIntoFMP4
	ac3IntoFMP4.CopyAudioCodec = "ac3"
	for _, test := range []struct {
		name     string
		request  TranscodeStartRequest
		response TranscodeStartResponse
		wantErr  bool
	}{
		{name: "aac into fmp4 confirmed", request: aacIntoFMP4, response: TranscodeStartResponse{CopyAudioCodec: "aac"}},
		{name: "aac into fmp4 on an older node", request: aacIntoFMP4, wantErr: true},
		{name: "aac into mpeg-ts on an older node", request: aacIntoMPEGTS},
		{name: "ac3 into fmp4 on an older node", request: ac3IntoFMP4},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateCopyAudioCodecAttestation(test.request, test.response)
			if got := errors.Is(err, ErrCopyAudioCodecAttestationMismatch); got != test.wantErr {
				t.Fatalf("error = %v, want mismatch %v", err, test.wantErr)
			}
		})
	}
}

// Requests the node cannot execute as written are refused with 400 before
// anything runs.
func TestHandleStartAndPrepareRejectInvalidRequests(t *testing.T) {
	for _, test := range []struct {
		name        string
		prepare     bool
		refuseGrant bool
		artifactDir string // relative to the transcode root; empty keeps the default
		body        any
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
		{name: "start without a source grant",
			body: json.RawMessage(`{"session_id":"no-grant","input_path":"/media/movie.mkv"}`)},
		{name: "start with a grant the core refuses", refuseGrant: true, body: TranscodeStartRequest{
			SessionID: "refused-grant", InputPath: "/media/movie.mkv",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		}},
		{name: "prepare without a source grant", prepare: true,
			body: json.RawMessage(`{"artifact_id":"artifact-no-grant","input_path":"/media/movie.mkv"}`)},
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
			if test.refuseGrant {
				sourcesOf(server).refuse("/media/movie.mkv", &nodesource.StatusError{Op: "open", Status: http.StatusForbidden})
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
		{
			name: "copied AAC into fMP4",
			request: TranscodeStartRequest{
				SessionID: "attested-copy-aac", InputPath: "/media/movie.ts",
				SourceVideoCodec: "h264", TargetCodecVideo: "copy", TargetCodecAudio: "copy", CopyAudioCodec: "aac",
				CopyFMP4RecipeVersion: playback.CopyFMP4RecipeVersion, SegmentDuration: 2,
			},
			receipt: func(response TranscodeStartResponse) string { return response.CopyAudioCodec },
			want:    "aac",
		},
		{
			// The echo says the node understood the request; the source has
			// no keyframe index, so the node keeps FFmpeg's playlist and
			// says so (#4044).
			name: "keyframe playlist the node can't plan",
			request: TranscodeStartRequest{
				SessionID: "attested-unplanned", InputPath: "/media/movie.mkv",
				SourceVideoCodec: "h264", TargetCodecVideo: "copy", TargetCodecAudio: "copy",
				CopyFMP4RecipeVersion: playback.CopyFMP4RecipeVersion, KeyframePlaylist: true,
				SegmentDuration: 2, TotalDuration: 60,
			},
			receipt: func(response TranscodeStartResponse) string {
				return fmt.Sprintf("keyframe_playlist=%t keyframe_planned=%t", response.KeyframePlaylist, response.KeyframePlanned)
			},
			want: "keyframe_playlist=true keyframe_planned=false",
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
	nodeauthtest.Sign(t, req, testNodeKey)
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
	nodeauthtest.Sign(t, deleteReq, testNodeKey)
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
	nodeauthtest.Sign(t, request, testNodeKey)
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
		nodeauthtest.Sign(t, req, testNodeKey)
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
		nodeauthtest.Sign(t, req, testNodeKey)
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

func TestHandleProgressiveRemuxRejectsConcurrentRequest(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 11, true }
	mediaPath, _ := writeNodeSource(t, "source")
	ffmpegLog := filepath.Join(t.TempDir(), "ffmpeg.log")
	useFakeFFmpeg(t, server, "#!/bin/sh\n"+
		"case \" $* \" in *\" -i "+mediaPath+" \"*) printf 'invoked\\n' >> \""+ffmpegLog+"\";; esac\n"+
		"printf node-remux-bytes\n")
	claims := progressiveRemuxClaims("playback-concurrent", mediaPath, "transport-concurrent")
	request := remuxRequest(t, server, http.MethodGet, claims)
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

	resolvesBefore, opensBefore := len(coreOf(server).resolveCalls()), len(sourcesOf(server).opened)
	recorder := httptest.NewRecorder()
	server.handleRemux(recorder, request.Clone(request.Context()))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	// The node refuses from its own state before it asks the core anything.
	if resolves, opens := len(coreOf(server).resolveCalls())-resolvesBefore, len(sourcesOf(server).opened)-opensBefore; resolves != 0 || opens != 0 {
		t.Fatalf("the refused request made %d resolve calls and %d source opens, want none", resolves, opens)
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

// The core decides every progressive remux; the node answers its refusals as
// it answered the same conditions when it checked the token itself, and
// starts nothing. A transport this process stopped is refused before the core
// is asked.
func TestHandleProgressiveRemuxAnswersCoreRefusals(t *testing.T) {
	for _, test := range []struct {
		err  error
		want int
	}{
		{refusal(http.StatusForbidden, nodecontrol.ResolveInvalidToken), http.StatusUnauthorized},
		{refusal(http.StatusForbidden, nodecontrol.ResolveWrongRoute), http.StatusServiceUnavailable},
		{refusal(http.StatusGone, nodecontrol.ResolveDenied), http.StatusGone},
		{refusal(http.StatusGone, nodecontrol.ResolveStopped), http.StatusGone},
		{refusal(http.StatusGone, nodecontrol.ResolveSourceChanged), http.StatusBadRequest},
		{refusal(http.StatusNotFound, nodecontrol.ResolveNotFound), http.StatusNotFound},
		{&nodecontrol.StatusError{Path: nodecontrol.PathResolve, Status: http.StatusServiceUnavailable}, http.StatusServiceUnavailable},
		{errors.New("connection refused"), http.StatusServiceUnavailable},
	} {
		server := newTestServer(t)
		claims := progressiveRemuxClaims("playback-1", "/media/movie.mkv", "transport-1")
		request := remuxRequest(t, server, http.MethodGet, claims)
		coreOf(server).refuse("transport-1", test.err)
		rr := httptest.NewRecorder()

		server.handleRemux(rr, request)

		if rr.Code != test.want {
			t.Fatalf("%v: status = %d, want %d (body %s)", test.err, rr.Code, test.want, rr.Body.String())
		}
		if got := server.activeJobs.Load(); got != 0 || len(sourcesOf(server).opened) != 0 {
			t.Fatalf("%v: active jobs = %d, sources opened = %d, want neither", test.err, got, len(sourcesOf(server).opened))
		}
	}

	server := newTestServer(t)
	claims := progressiveRemuxClaims("playback-1", "/media/movie.mkv", "transport-1")
	server.stoppedProgressiveRemuxes["transport-1"] = time.Now().Add(time.Hour)
	rr := httptest.NewRecorder()
	server.handleRemux(rr, remuxRequest(t, server, http.MethodGet, claims))
	if rr.Code != http.StatusGone || len(coreOf(server).resolveCalls()) != 0 {
		t.Fatalf("stopped transport: status = %d after %d resolve calls, want 410 before any", rr.Code, len(coreOf(server).resolveCalls()))
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

// A stop removes the tracker record under the transport id unless a progressive
// remux is still running here: a native v3 HLS job is tracked under that id, not
// its card's playback session id, and an ended remux already removed its own.
func TestHandleStopRemovesTrackerRecordByTransportID(t *testing.T) {
	const transportID = "playback-1-plan-transport"
	for _, tc := range []struct {
		name    string
		running bool
	}{
		{name: "native HLS transcode", running: true},
		{name: "remux no longer running here"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			tracker := &recordingSessionTracker{}
			server.tracker = tracker
			if tc.running {
				server.sessions[transportID] = &playback.TranscodeSession{}
				server.activeJobs.Store(1)
			}
			rr := httptest.NewRecorder()

			server.handleStop(rr, stopRequest(transportID))

			if want := map[bool]int{true: http.StatusNoContent, false: http.StatusNotFound}[tc.running]; rr.Code != want {
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

func TestHandleProgressiveRemuxWaitsForForceReloadGate(t *testing.T) {
	server := newTestServer(t)
	tracker := &recordingSessionTracker{}
	server.tracker = tracker
	server.nodeRowID = func() (int, bool) { return 11, true }
	mediaPath, _ := writeNodeSource(t, "source")
	useFakeFFmpeg(t, server, "#!/bin/sh\nprintf node-remux-bytes\n")
	claims := progressiveRemuxClaims("playback-reload-gate", mediaPath, "transport-reload-gate")
	request := remuxRequest(t, server, http.MethodGet, claims)
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
	case <-done:
		t.Fatal("remux completed while force-reload gate was held")
	case <-time.After(50 * time.Millisecond):
	}
	server.reloadMu.Unlock()
	locked = false
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
	server.tracker = &recordingSessionTracker{}
	const transportID = "transport-force-reload"
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	releaseHandler := make(chan struct{})
	server.activeJobs.Add(1)
	server.progressiveRemuxes[transportID] = progressiveRemuxRequest{id: 1, cancel: cancel, done: done}
	core := coreOf(server)
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
		teardownResult <- server.teardownForForceReload(waitCtx)
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
	if core.revokes != 1 {
		t.Fatalf("recipe revocations = %d, want 1", core.revokes)
	}
}

// A force reload asks the core to revoke this node's recipes before it tears
// its sessions down, under the reload gate. A revocation the core refuses
// fails the reload, which the operator retries; the sessions go regardless.
func TestForceReloadRevokesThroughTheCoreFirst(t *testing.T) {
	for _, revokeErr := range []error{nil, &nodecontrol.StatusError{Path: nodecontrol.PathRevokeRecipes, Status: http.StatusServiceUnavailable}} {
		server := newTestServer(t)
		session := &playback.TranscodeSession{}
		server.sessions["live"] = session
		server.activeJobs.Store(1)
		core := &revokeCheckingCore{server: server, err: revokeErr}
		server.core = core
		rr := httptest.NewRecorder()

		server.handleForceReload(rr, httptest.NewRequest(http.MethodPost, "/admin/force-reload", nil))

		want := http.StatusNoContent
		if revokeErr != nil {
			want = http.StatusInternalServerError
		}
		if rr.Code != want || !core.sawLiveSession {
			t.Fatalf("revoke error %v: status = %d (want %d), revoked while the session was live = %v",
				revokeErr, rr.Code, want, core.sawLiveSession)
		}
		if server.hostsSession("live") {
			t.Fatalf("revoke error %v: the force reload left the session running", revokeErr)
		}
	}
}

// revokeCheckingCore records whether the node still ran its session when it
// asked for the revocation.
type revokeCheckingCore struct {
	fakeCore
	server         *Server
	err            error
	sawLiveSession bool
}

func (c *revokeCheckingCore) RevokeRecipes(context.Context) error {
	c.server.mu.RLock()
	c.sawLiveSession = len(c.server.sessions) > 0
	c.server.mu.RUnlock()
	return c.err
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
		firstResult <- server.teardownForForceReload(firstCtx)
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
		retryResult <- server.teardownForForceReload(waitCtx)
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
			coreOf(server).resolve(sid, resolutionOf(card))
			request := withNodeRouteParams(requestWithToken(sid, ""), params)
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

// A lost session rebuilds from the recipe the core resolves for the transport
// id in the URL, with the relay's stream token passed through unread, and
// reads the source the core grants. The rebuilt session is filed under the
// playback identity the core named, from the start the core named.
func TestReconstructLostRebuildsFromTheCoresResolution(t *testing.T) {
	const sessionID = "sess-tokenless-1"
	const transportID = sessionID + "-plan1234-abcd1234"

	s := newLoopingFFmpegServer(t)
	tracker := &recordingSessionTracker{}
	s.tracker = tracker
	resolution := resolutionOf(*nativeTransportCard(sessionID, transportID))
	resolution.SourceGrant = "/media/granted.mkv"
	resolution.StartedAt, resolution.StartedAtSource = time.Unix(1_700_000_000, 0), string(streamtoken.StartedAtSourceClaim)
	coreOf(s).resolve(transportID, resolution)
	request := requestWithToken(transportID, "relay-token")

	session, err := s.reconstructLost(request, transportID, 5)
	if err != nil || session == nil {
		t.Fatalf("reconstructLost() = %v, %v; a node restart would lose this session until the client replans", session, err)
	}
	defer func() { _ = session.CloseProcess() }()
	calls := coreOf(s).resolveCalls()
	if len(calls) != 1 || calls[0] != (nodecontrol.ResolveRequest{TransportID: transportID, Purpose: nodecontrol.ResolvePurposeHLS, StreamToken: "relay-token"}) {
		t.Fatalf("resolve calls = %+v, want one hls resolve carrying the relay's token", calls)
	}
	if got := session.Opts(); got.SessionID != transportID || got.StartSegmentNumber != 5 || got.InputPath != "/media/granted.mkv" {
		t.Fatalf("rebuilt session id %q, start segment %d, input %q; want the transport id, the segment the client fetches, the granted source",
			got.SessionID, got.StartSegmentNumber, got.InputPath)
	}
	if got := s.playbackSessionID(transportID); got != sessionID {
		t.Fatalf("telemetry session = %q, want the resolved playback session %q", got, sessionID)
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.tracked) != 1 || tracker.tracked[0].StartedAtUnixNano != resolution.StartedAt.UnixNano() ||
		tracker.tracked[0].StartedAtSource != resolution.StartedAtSource || tracker.tracked[0].AuthUserID != resolution.UserID {
		t.Fatalf("tracked = %+v, want the resolved identity and start", tracker.tracked)
	}
}

// A lost session the core will not rebuild answers as the node answered when
// it checked the token itself: denied 410, any other refusal 404, a core it
// cannot reach 503. A source the core refuses at open is not found.
func TestReconstructLostAnswersCoreRefusals(t *testing.T) {
	const transportID = "lost-transport"
	for _, test := range []struct {
		name    string
		prepare func(*Server)
		want    int
	}{
		{"denied", func(s *Server) { coreOf(s).refuse(transportID, refusal(http.StatusGone, nodecontrol.ResolveDenied)) }, http.StatusGone},
		{"bound to another node", func(s *Server) {
			coreOf(s).refuse(transportID, refusal(http.StatusForbidden, nodecontrol.ResolveWrongRoute))
		}, http.StatusNotFound},
		{"no recipe", func(s *Server) { coreOf(s) }, http.StatusNotFound},
		{"core unavailable", func(s *Server) {
			coreOf(s).refuse(transportID, &nodecontrol.StatusError{Path: nodecontrol.PathResolve, Status: http.StatusServiceUnavailable})
		}, http.StatusServiceUnavailable},
		{"core unreachable", func(s *Server) { coreOf(s).refuse(transportID, errors.New("connection refused")) }, http.StatusServiceUnavailable},
		{"source refused", func(s *Server) {
			coreOf(s).resolve(transportID, resolutionOf(*nativeTransportCard("viewer", transportID)))
			sourcesOf(s).refuse("/media/movie.mkv", &nodesource.StatusError{Op: "open", Status: http.StatusGone})
		}, http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newTestServer(t)
			test.prepare(s)
			rr := httptest.NewRecorder()
			s.handleManifest(rr, withNodeRouteParams(requestWithToken(transportID, ""), map[string]string{"session_id": transportID}))
			if rr.Code != test.want {
				t.Fatalf("status = %d, want %d (body %s)", rr.Code, test.want, rr.Body.String())
			}
			if s.hostsSession(transportID) {
				t.Fatal("a refused rebuild registered a session")
			}
		})
	}
}

// Regression for #2265: a stop kills the job, and a segment request for the
// same transport that was already in flight reaches the node right after. The
// node must answer it 410 without asking the core, which could still resolve
// the job from the request's stream token, and would encode for nobody until
// the idle reaper. A node that lost the job some other way (a restart) must
// still rebuild it.
func TestHandleStop_LateSegmentDoesNotRebuildStoppedTranscode(t *testing.T) {
	const sessionID = "sess-late-segment"
	const transportID = sessionID + "-plan1234-abcd1234"

	ffmpegPath := filepath.Join(t.TempDir(), "looping-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nwhile :; do sleep 0.1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolution := resolutionOf(*nativeTransportCard(sessionID, transportID))
	newNode := func() *Server {
		s := newTestServer(t)
		s.tracker = nodesessions.NewLocalTracker()
		s.watcher.Config().Playback.FFmpegPath = ffmpegPath
		coreOf(s).resolve(transportID, resolution)
		return s
	}
	segmentRequest := func() *http.Request {
		const name = "seg_00005.ts"
		r := httptest.NewRequest(http.MethodGet, "/transcode/"+transportID+"/segment/"+name, nil)
		r.Header.Set("X-Silo-Stream-Token", "relay-token")
		return withNodeRouteParams(r, map[string]string{"session_id": transportID, "name": name})
	}
	registered := func(s *Server) *playback.TranscodeSession {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.sessions[transportID]
	}

	s := newNode()
	running, err := s.reconstructLost(requestWithToken(transportID, "relay-token"), transportID, 5)
	if err != nil || running == nil {
		t.Fatalf("set up running job: session = %v, err = %v", running, err)
	}

	stop := withNodeRouteParams(httptest.NewRequest(http.MethodDelete, "/transcode/"+transportID, nil), map[string]string{"session_id": transportID})
	stopRec := httptest.NewRecorder()
	s.handleStop(stopRec, stop)
	if stopRec.Code != http.StatusNoContent {
		t.Fatalf("stop status = %d, want %d", stopRec.Code, http.StatusNoContent)
	}

	resolvesBefore := len(coreOf(s).resolveCalls())
	late := httptest.NewRecorder()
	s.handleSegment(late, segmentRequest())
	if late.Code != http.StatusGone {
		t.Fatalf("late segment status = %d, want %d (body %q)", late.Code, http.StatusGone, late.Body.String())
	}
	if got := len(coreOf(s).resolveCalls()); got != resolvesBefore {
		t.Fatalf("late segment asked the core %d times, want the stop fence to answer first", got-resolvesBefore)
	}
	if got := registered(s); got != nil {
		_ = got.CloseProcess()
		t.Fatal("late segment rebuilt the stopped transcode")
	}

	// A request that found no job while the stop held the lifecycle lock reaches
	// spawnReconstruct past the early check; the fence must hold there too.
	if got, err := s.spawnReconstruct(segmentRequest(), transportID, 5, resolution); got != nil || !errors.Is(err, errTranscodeStopped) {
		if got != nil {
			_ = got.CloseProcess()
		}
		t.Fatalf("spawnReconstruct after stop = (%v, %v), want (nil, errTranscodeStopped)", got, err)
	}

	// A replacement process has no record of the stop and still recovers the
	// client's session through the core.
	restarted := newNode()
	rebuilt, err := restarted.reconstructLost(segmentRequest(), transportID, 5)
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
	rebuilt, err = restarted.reconstructLost(segmentRequest(), transportID, 5)
	if err != nil || rebuilt == nil {
		t.Fatalf("released transport was not rebuilt: session = %v, err = %v", rebuilt, err)
	}
	_ = rebuilt.CloseProcess()
}

// A fresh start under a stopped transport id is the API choosing that
// transport again (a Jellyfin software retry reuses the id right after
// stopping it), so it lifts the stop fence: a later idle reap must leave the
// job recoverable through the core.
func TestHandleStart_LiftsStopFence(t *testing.T) {
	const sessionID = "sess-restarted-after-stop"
	s := newTestServer(t)
	s.tracker = nodesessions.NewLocalTracker()
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

	rec := postStartRequest(t, s, TranscodeStartRequest{
		SessionID: sessionID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
	})
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

func TestSpawnReconstructRejectsSessionIDOutsideTranscodeRoot(t *testing.T) {
	server := newTestServer(t)
	logPath := filepath.Join(t.TempDir(), "invocations.log")
	useFakeFFmpeg(t, server, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+logPath+"\nexit 1\n")

	const sessionID = ".."
	card := *nativeTransportCard(sessionID, sessionID)
	session, err := server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), sessionID, -1, resolutionOf(card))
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
	session, err := server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), sessionID, -1, resolutionOf(card))
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

// A rebuild reuses the lost session's output directory, which still holds the
// manifest and segments the previous process wrote. A hardware start that
// dies at startup must not be taken as ready from those: it gets the software
// retry, and when that fails too nothing is registered (#4099).
func TestSpawnReconstructIgnoresThePreviousProcessManifest(t *testing.T) {
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

	const sessionID = "reconstruct-old-manifest"
	outputDir := server.sessionOutputDir(sessionID)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var manifest strings.Builder
	manifest.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
	for i := range 6 {
		name := "segment" + strconv.Itoa(i) + ".ts"
		manifest.WriteString("#EXTINF:2.000000,\n" + name + "\n")
		if err := os.WriteFile(filepath.Join(outputDir, name), []byte("old segment"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outputDir, "stream.m3u8"), []byte(manifest.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	card := playback.NewRecipeCard(7, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID: sessionID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "720p", TargetBitrateKbps: 2000,
		SegmentDuration: 2,
	})
	session, err := server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), sessionID, -1, resolutionOf(card))
	if session != nil {
		// Its process already exited; report it rather than close it.
		t.Fatal("a rebuild whose processes both died returned a session, ready from the previous manifest")
	}
	if err == nil {
		t.Fatal("a rebuild whose processes both died returned no error")
	}
	server.mu.RLock()
	_, registered := server.sessions[sessionID]
	server.mu.RUnlock()
	if registered {
		t.Fatal("the dead rebuild was registered")
	}
	logData, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := strings.Count(string(logData), "-f hls"); got != 2 {
		t.Fatalf("real transcode attempts = %d, want hardware plus the software retry:\n%s", got, logData)
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
	runtimeOf(server).SetConfigForTest(&replacement)
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
		session, _ := server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), card.SessionID, -1, resolutionOf(card))
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
		_, _ = server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), sessionID, -1, resolutionOf(transcodeCard(sessionID)))
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
	mediaPath, _ := writeNodeSource(t, "source")
	useFakeFFmpeg(t, server, "#!/bin/sh\n"+
		"for arg in \"$@\"; do\n"+
		"  if [ \"$arg\" = \"pipe:1\" ]; then\n"+
		"    while :; do printf node-remux-bytes; sleep 0.01; done\n"+
		"  fi\n"+
		"done\n"+
		"exit 0\n")
	claims := progressiveRemuxClaims("playback-force-reload", mediaPath, "transport-force-reload-real")
	request := remuxRequest(t, server, http.MethodGet, claims)
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

	if live := sourcesOf(server).open(); len(live) != 0 {
		t.Fatalf("shutdown left %d sources open", len(live))
	}
	if coreOf(server).revokes != 0 {
		t.Fatal("shutdown revoked reconstruction authority")
	}

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

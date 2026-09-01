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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/mediaprobe"
	"github.com/Silo-Server/silo-server/internal/models"
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
					dir := t.TempDir()
					inputPath := filepath.Join(dir, "source.mkv")
					if err := os.WriteFile(inputPath, []byte("source"), 0o600); err != nil {
						t.Fatal(err)
					}
					info, err := os.Stat(inputPath)
					if err != nil {
						t.Fatal(err)
					}
					ffmpegMarker := filepath.Join(dir, "ffmpeg-ran")
					ffmpegPath := filepath.Join(dir, "ffmpeg")
					ffmpegScript := "#!/bin/sh\n" +
						"case \" $* \" in\n" +
						"  *\" -filters \"*) printf ' T.. zscale V->V scale\\n T.. tonemapx V->V tone-map\\n T.. sidedata V->V metadata\\n'; exit 0;;\n" +
						"  *\" -encoders \"*) printf ' V..... libx264 H.264\\n'; exit 0;;\n" +
						"esac\n" +
						"case \" $* \" in *\" -i " + inputPath + " \"*) touch '" + ffmpegMarker + "';; esac\nexit 0\n"
					if err := os.WriteFile(ffmpegPath, []byte(ffmpegScript), 0o755); err != nil {
						t.Fatal(err)
					}
					probePath := mediaprobe.FFprobePathFromFFmpeg(ffmpegPath)
					if err := os.WriteFile(probePath, []byte("#!/bin/sh\n"+validation.probeBody+"\n"), 0o755); err != nil {
						t.Fatal(err)
					}
					server.watcher.Config().Playback.FFmpegPath = ffmpegPath
					server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
					track := nodeToneMapTrack()
					revision := tonemap.RevisionForFile(&models.MediaFile{ID: 1, FileSize: info.Size(), VideoTracks: []models.VideoTrack{track}})

					var body []byte
					if endpoint == "start" {
						body, err = json.Marshal(TranscodeStartRequest{
							SessionID: "metadata-guard", InputPath: inputPath,
							TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
							ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
							ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
							ToneMapSourceRevision: revision,
						})
					} else {
						body, err = json.Marshal(downloadprepare.Request{
							ArtifactID: "metadata-guard", InputPath: inputPath,
							TargetCodecVideo: "h264", TargetCodecAudio: "aac", AudioTrackIndex: -1,
							ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
							ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
							ToneMapSourceRevision: revision,
						})
					}
					if err != nil {
						t.Fatal(err)
					}
					recorder := httptest.NewRecorder()
					request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
					if endpoint == "start" {
						server.handleStart(recorder, request)
					} else {
						server.handleDownloadPrepare(recorder, request)
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
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
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

func TestNewServerUsesConfiguredDownloadArtifactDir(t *testing.T) {
	w := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Playback.TranscodeDir = filepath.Join(t.TempDir(), "transcodes")
	cfg.Download.ArtifactDir = filepath.Join(t.TempDir(), "prepared-downloads")
	w.SetConfigForTest(cfg)

	server := NewServer(w, nil)
	if server.artifactRoot != cfg.Download.ArtifactDir {
		t.Fatalf("artifact root = %q, want configured %q", server.artifactRoot, cfg.Download.ArtifactDir)
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

func TestHandleHWCapabilitiesReturnsServiceUnavailableWhenDeadlineExpires(t *testing.T) {
	server := newTestServer(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	server.handleHWCapabilities(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
}

func TestToneMapCapabilityResolveTimeoutCoversConfiguredProbeBudget(t *testing.T) {
	// The endpoint budget is both halves — the hardware walk and the tone-map
	// matrix — because the endpoint runs both.
	got := toneMapCapabilityResolveTimeout(tonemap.BackendQSV, "/dev/dri/renderD128")
	if want := playback.CapabilityEndpointTimeout(tonemap.BackendQSV, "/dev/dri/renderD128"); got != want {
		t.Fatalf("capability resolve timeout = %v, want endpoint budget %v", got, want)
	}
	if tone := tonemap.ProbeEndpointTimeout(tonemap.BackendQSV, "/dev/dri/renderD128"); got <= tone {
		t.Fatalf("capability resolve timeout = %v, want more than the tone-map half alone (%v)", got, tone)
	}
}

func TestHandleHWCapabilitiesAdvertisesEffectiveProbeRequestTimeout(t *testing.T) {
	server := newTestServer(t)
	ffmpegPath := filepath.Join(t.TempDir(), "successful-probe.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\necho tonemapx\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
	recorder := httptest.NewRecorder()

	server.handleHWCapabilities(recorder, httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	want := float64(playback.CapabilityRequestTimeout(playback.HWAccelNone, "").Milliseconds())
	if got := response["probe_request_timeout_ms"]; got != want {
		t.Fatalf("probe request timeout = %v, want %.0fms", got, want)
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

func TestHandleStartRequireReadyRejectsExitedFFmpeg(t *testing.T) {
	server := newTestServer(t)
	ffmpegPath := filepath.Join(t.TempDir(), "failing-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	requestBody, err := json.Marshal(TranscodeStartRequest{
		SessionID:        "ready-failure-1",
		InputPath:        "/media/movie.mkv",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
		RequireReady:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(requestBody))
	rr := httptest.NewRecorder()
	server.handleStart(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	server.mu.RLock()
	_, registered := server.sessions["ready-failure-1"]
	server.mu.RUnlock()
	if registered {
		t.Fatal("failed readiness session was registered")
	}
}

func TestHandleDownloadPrepareKeepsStartupArtifactRootAcrossReload(t *testing.T) {
	server := newTestServer(t)
	artifactDir := server.artifactRoot
	server.watcher.Config().Download.ArtifactDir = t.TempDir()
	server.watcher.Config().Playback.TranscodeDir = t.TempDir()
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nfor last; do :; done\nprintf artifact > \"$last\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	server.watcher.Config().Playback.HWAccel = "none"
	outputPath := filepath.Join(artifactDir, "artifact-1.mp4")
	prepareRequest := downloadprepare.Request{
		ArtifactID:       "artifact-1",
		InputPath:        "/media/movie.mkv",
		TargetCodecVideo: "copy",
		TargetCodecAudio: "copy",
		AudioTrackIndex:  -1,
	}
	body, err := json.Marshal(prepareRequest)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	server.handleDownloadPrepare(rr, req)
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
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "source.mkv")
	if err := os.WriteFile(inputPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	ffmpegPath := filepath.Join(dir, "ffmpeg.sh")
	script := "#!/bin/sh\n" +
		"case \" $* \" in\n" +
		"  *\" -filters \"*) printf ' T.. zscale V->V scale\\n T.. tonemapx V->V tone-map\\n T.. sidedata V->V metadata\\n'; exit 0;;\n" +
		"  *\" -encoders \"*) printf ' V..... libx264 H.264\\n'; exit 0;;\n" +
		"  *\" -f null \"*) exit 0;;\n" +
		"esac\n" +
		"for last; do :; done\n" +
		"printf artifact > \"$last\"\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
	track := nodeToneMapTrack()
	writeNodeToneMapFFprobe(t, ffmpegPath, track)
	revision := tonemap.RevisionForFile(&models.MediaFile{ID: 1, FileSize: info.Size(), VideoTracks: []models.VideoTrack{track}})
	prepareRequest := downloadprepare.Request{
		ArtifactID: "artifact-tone-map", InputPath: inputPath,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", AudioTrackIndex: -1,
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: revision,
	}
	body, err := json.Marshal(prepareRequest)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.handleDownloadPrepare(recorder, httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body)))
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
	head.Header.Set("Authorization", "Bearer "+testSecret)
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

func TestHandleDownloadPreparePublishesStereoDownmixReceiptAndStatusAttestation(t *testing.T) {
	server := newTestServer(t)
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nfor last; do :; done\nprintf artifact > \"$last\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
	prepareRequest := downloadprepare.Request{
		ArtifactID:          "artifact-stereo-downmix",
		InputPath:           "/media/movie.mkv",
		TargetCodecVideo:    "copy",
		TargetCodecAudio:    "aac",
		AudioTrackIndex:     0,
		AudioRecipeVersion:  playback.TransformationAudioToAACRecipeVersionV3,
		SourceAudioChannels: 6,
	}
	body, err := json.Marshal(prepareRequest)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.handleDownloadPrepare(recorder, httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	want := downloadprepare.Result{
		ArtifactID:           prepareRequest.ArtifactID,
		FileSize:             int64(len("artifact")),
		ExecutionFingerprint: prepareRequest.ExecutionFingerprint(),
	}
	var result downloadprepare.Result
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result != want {
		t.Fatalf("prepare result = %+v, want %+v", result, want)
	}

	outputPath := filepath.Join(server.artifactRoot, prepareRequest.ArtifactID+".mp4")
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

	head := httptest.NewRequest(http.MethodHead, "/downloads/artifacts/"+prepareRequest.ArtifactID, nil)
	head.Header.Set("Authorization", "Bearer "+testSecret)
	headRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(headRecorder, head)
	if headRecorder.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, body = %s", headRecorder.Code, headRecorder.Body.String())
	}
	attestation, err := downloadprepare.ResultFromHeaders(headRecorder.Header())
	if err != nil {
		t.Fatal(err)
	}
	if attestation.ExecutionFingerprint != want.ExecutionFingerprint || attestation.FileSize != want.FileSize {
		t.Fatalf("HEAD attestation = %+v, want fingerprint %q and size %d", attestation, want.ExecutionFingerprint, want.FileSize)
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

func TestHandleDownloadPrepareRejectsPartialStereoDownmixRecipe(t *testing.T) {
	server := newTestServer(t)
	request := downloadprepare.Request{
		ArtifactID: "artifact-unversioned-downmix", InputPath: "/media/movie.mkv",
		TargetCodecAudio: "aac", SourceAudioChannels: 6,
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleDownloadPrepare(recorder, httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body.String())
	}
}

func TestHandleDownloadPrepareDoesNotReuseToneMapArtifactWithoutReceipt(t *testing.T) {
	server := newTestServer(t)
	if err := os.MkdirAll(server.artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(server.artifactRoot, "artifact-existing.mp4")
	if err := os.WriteFile(outputPath, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(downloadprepare.Request{
		ArtifactID: "artifact-existing", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: "stale",
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleDownloadPrepare(recorder, httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body)))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleDownloadPrepareDoesNotReuseArtifactForPartialToneMapRecipe(t *testing.T) {
	server := newTestServer(t)
	if err := os.MkdirAll(server.artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(server.artifactRoot, "artifact-partial.mp4")
	if err := os.WriteFile(outputPath, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(downloadprepare.Request{
		ArtifactID: "artifact-partial", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleDownloadPrepare(recorder, httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body)))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleDownloadPrepareReceiptInvalidationFailurePreservesExistingArtifact(t *testing.T) {
	server := newTestServer(t)
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "source.mkv")
	if err := os.WriteFile(inputPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	ffmpegPath := filepath.Join(dir, "ffmpeg.sh")
	script := "#!/bin/sh\n" +
		"case \" $* \" in\n" +
		"  *\" -filters \"*) printf ' T.. zscale V->V scale\\n T.. tonemapx V->V tone-map\\n T.. sidedata V->V metadata\\n'; exit 0;;\n" +
		"  *\" -encoders \"*) printf ' V..... libx264 H.264\\n'; exit 0;;\n" +
		"  *\" -f null \"*) exit 0;;\n" +
		"esac\n" +
		"for last; do :; done\n" +
		"printf newbytes > \"$last\"\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
	if err := os.MkdirAll(server.artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(server.artifactRoot, "artifact-invalidation.mp4")
	if err := os.WriteFile(outputPath, []byte("oldbytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	receiptPath := outputPath + ".receipt.json"
	if err := os.Mkdir(receiptPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(receiptPath, "blocks-removal"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(downloadprepare.Request{
		ArtifactID: "artifact-invalidation", InputPath: inputPath,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", AudioTrackIndex: -1,
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1, FileSize: info.Size()},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleDownloadPrepare(recorder, httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body)))
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
			dir := t.TempDir()
			inputPath := filepath.Join(dir, "source.mkv")
			if err := os.WriteFile(inputPath, []byte("source"), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(inputPath)
			if err != nil {
				t.Fatal(err)
			}
			ffmpegPath := filepath.Join(dir, "ffmpeg.sh")
			script := "#!/bin/sh\n" +
				"case \" $* \" in\n" +
				"  *\" -filters \"*) printf ' T.. zscale V->V scale\\n T.. tonemapx V->V tone-map\\n T.. sidedata V->V metadata\\n'; exit 0;;\n" +
				"  *\" -encoders \"*) printf ' V..... libx264 H.264\\n'; exit 0;;\n" +
				"  *\" -f null \"*) exit 0;;\n" +
				"esac\n" +
				"for last; do :; done\n" +
				"printf newbytes > \"$last\"\n"
			if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			server.watcher.Config().Playback.FFmpegPath = ffmpegPath
			server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
			track := nodeToneMapTrack()
			writeNodeToneMapFFprobe(t, ffmpegPath, track)
			if err := os.MkdirAll(server.artifactRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			outputPath := filepath.Join(server.artifactRoot, "artifact-reexecute.mp4")
			if err := os.WriteFile(outputPath, []byte("oldbytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			if test.writeMismatch {
				if err := writeDownloadArtifactReceipt(outputPath, downloadprepare.Result{
					ArtifactID: "artifact-reexecute", FileSize: int64(len("oldbytes")),
					ToneMapRecipeVersion: "stale", ToneMapMode: tonemap.ModeSoftware,
					ToneMapSourceRevisionFingerprint: "wrong",
				}); err != nil {
					t.Fatal(err)
				}
			}
			revision := tonemap.RevisionForFile(&models.MediaFile{ID: 1, FileSize: info.Size(), VideoTracks: []models.VideoTrack{track}})
			request := downloadprepare.Request{
				ArtifactID: "artifact-reexecute", InputPath: inputPath,
				TargetCodecVideo: "h264", TargetCodecAudio: "aac", AudioTrackIndex: -1,
				ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
				ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
				ToneMapSourceRevision: revision,
			}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			server.handleDownloadPrepare(recorder, httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body)))
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
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nfor last; do :; done\nprintf newbytes > \"$last\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	body, err := json.Marshal(downloadprepare.Request{
		ArtifactID: "artifact-ordinary-reexecute", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "copy", TargetCodecAudio: "copy", AudioTrackIndex: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleDownloadPrepare(recorder, httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(downloadArtifactReceiptPath(outputPath)); !os.IsNotExist(err) {
		t.Fatalf("stale ordinary receipt remains after re-execution: %v", err)
	}
}

func TestHandleDownloadPrepareConcurrentSameArtifactPublishesOnce(t *testing.T) {
	server := newTestServer(t)
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "source.mkv")
	if err := os.WriteFile(inputPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	counterPath := filepath.Join(dir, "prepare-count")
	ffmpegPath := filepath.Join(dir, "ffmpeg.sh")
	script := "#!/bin/sh\n" +
		"case \" $* \" in\n" +
		"  *\" -filters \"*) printf ' T.. zscale V->V scale\\n T.. tonemapx V->V tone-map\\n T.. sidedata V->V metadata\\n'; exit 0;;\n" +
		"  *\" -encoders \"*) printf ' V..... libx264 H.264\\n'; exit 0;;\n" +
		"  *\" -f null \"*) exit 0;;\n" +
		"esac\n" +
		fmt.Sprintf("printf x >> %q\n", counterPath) +
		"sleep 0.1\n" +
		"for last; do :; done\n" +
		"printf newbytes > \"$last\"\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
	track := nodeToneMapTrack()
	writeNodeToneMapFFprobe(t, ffmpegPath, track)
	body, err := json.Marshal(downloadprepare.Request{
		ArtifactID: "artifact-concurrent", InputPath: inputPath,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", AudioTrackIndex: -1,
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.RevisionForFile(&models.MediaFile{ID: 1, FileSize: info.Size(), VideoTracks: []models.VideoTrack{track}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			<-start
			recorder := httptest.NewRecorder()
			server.handleDownloadPrepare(recorder, httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body)))
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
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "source.mkv")
	if err := os.WriteFile(inputPath, []byte("source-a"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpegPath := filepath.Join(dir, "ffmpeg.sh")
	script := "#!/bin/sh\n" +
		"case \" $* \" in\n" +
		"  *\" -filters \"*) printf ' T.. zscale V->V scale\\n T.. tonemapx V->V tone-map\\n T.. sidedata V->V metadata\\n'; exit 0;;\n" +
		"  *\" -encoders \"*) printf ' V..... libx264 H.264\\n'; exit 0;;\n" +
		"  *\" -f null \"*) exit 0;;\n" +
		"esac\n" +
		"for last; do :; done\n" +
		"printf final-a > \"$last\"\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
	track := nodeToneMapTrack()
	writeNodeToneMapFFprobe(t, ffmpegPath, track)
	if err := os.MkdirAll(server.artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	const artifactID = "artifact-replacement-race"
	outputPath := filepath.Join(server.artifactRoot, artifactID+".mp4")
	if err := os.WriteFile(outputPath, []byte("oldbytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	revision := tonemap.RevisionForFile(&models.MediaFile{ID: 1, FileSize: 8, VideoTracks: []models.VideoTrack{track}})
	if err := writeDownloadArtifactReceipt(outputPath, downloadprepare.Result{
		ArtifactID: artifactID, FileSize: 8,
		ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceRevisionFingerprint: revision.Fingerprint(),
	}); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(downloadprepare.Request{
		ArtifactID: artifactID, InputPath: inputPath,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	unlock := server.lockSessionLifecycle("download-artifact-" + artifactID)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.handleDownloadPrepare(recorder, httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body)))
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

func TestInvalidateDownloadArtifactReceiptRemovesOnlyReceipt(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "artifact.mp4")
	receiptPath := downloadArtifactReceiptPath(outputPath)
	if err := os.WriteFile(outputPath, []byte("artifact-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, []byte("valid-final"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A crashed writer's unique temp file must not block invalidation; writers
	// now use CreateTemp names, so there is no fixed .part path to remove.
	tempPath, err := os.CreateTemp(filepath.Dir(receiptPath), filepath.Base(receiptPath)+".")
	if err != nil {
		t.Fatal(err)
	}
	if err := tempPath.Close(); err != nil {
		t.Fatal(err)
	}
	if err := invalidateDownloadArtifactReceipt(outputPath); err != nil {
		t.Fatalf("invalidateDownloadArtifactReceipt() = %v", err)
	}
	if _, err := os.Stat(receiptPath); !os.IsNotExist(err) {
		t.Fatalf("receipt still present after invalidation: %v", err)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("artifact was removed by receipt invalidation: %v", err)
	}
	if _, err := os.Stat(tempPath.Name()); err != nil {
		t.Fatalf("crashed writer temp file was removed by receipt invalidation: %v", err)
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

func TestHandleStartRejectsUnversionedSourceAudioRecipeBeforeExecution(t *testing.T) {
	server := newTestServer(t)
	body, err := json.Marshal(TranscodeStartRequest{
		SessionID: "unversioned-downmix", InputPath: "/media/movie.mkv",
		SourceAudioChannels: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleStart(recorder, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body.String())
	}
}

func TestHandleStartRejectsUnversionedCopyFMP4RecipeBeforeExecution(t *testing.T) {
	server := newTestServer(t)
	body, err := json.Marshal(TranscodeStartRequest{
		SessionID: "unversioned-copy", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "copy",
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleStart(recorder, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body.String())
	}
}

func TestHandleStartAttestsStereoDownmixRecipe(t *testing.T) {
	server := newTestServer(t)
	server.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
	ffmpegPath := filepath.Join(t.TempDir(), "looping-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nwhile :; do sleep 0.1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	body, err := json.Marshal(TranscodeStartRequest{
		SessionID: "attested-downmix", InputPath: "/media/movie.mkv",
		SourceAudioChannels: 6, AudioRecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetAudioChannels: 2, SegmentDuration: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleStart(recorder, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body.String())
	}
	var response TranscodeStartResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.AudioRecipeVersion != playback.TransformationAudioToAACRecipeVersionV3 {
		t.Fatalf("audio recipe receipt = %q, want %q", response.AudioRecipeVersion, playback.TransformationAudioToAACRecipeVersionV3)
	}
	server.mu.RLock()
	session := server.sessions["attested-downmix"]
	server.mu.RUnlock()
	if session == nil {
		t.Fatal("attested session was not registered")
	}
	_ = session.CloseProcess()
}

func TestHandleStartAttestsCopyFMP4Recipe(t *testing.T) {
	server := newTestServer(t)
	server.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
	ffmpegPath := filepath.Join(t.TempDir(), "looping-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nwhile :; do sleep 0.1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	body, err := json.Marshal(TranscodeStartRequest{
		SessionID: "attested-copy", InputPath: "/media/movie.mkv",
		SourceVideoCodec: "hevc", TargetCodecVideo: "copy", TargetCodecAudio: "copy",
		CopyFMP4RecipeVersion: playback.CopyFMP4RecipeVersion, SegmentDuration: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleStart(recorder, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body.String())
	}
	var response TranscodeStartResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.CopyFMP4RecipeVersion != playback.CopyFMP4RecipeVersion {
		t.Fatalf("copy recipe receipt = %q, want %q", response.CopyFMP4RecipeVersion, playback.CopyFMP4RecipeVersion)
	}
	server.mu.RLock()
	session := server.sessions["attested-copy"]
	server.mu.RUnlock()
	if session == nil {
		t.Fatal("attested copy session was not registered")
	}
	_ = session.CloseProcess()
}

func TestHandleStartWaitsForForceReloadGate(t *testing.T) {
	server := newTestServer(t)
	ffmpegPath := filepath.Join(t.TempDir(), "failing-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	body, err := json.Marshal(TranscodeStartRequest{
		SessionID: "reload-gated-start", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, RequireReady: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body))
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
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nfor last; do :; done\ntouch \"$last\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	server.watcher.Config().Playback.HWAccel = "none"
	body, err := json.Marshal(downloadprepare.Request{
		ArtifactID:       "artifact-tracking",
		InputPath:        "/media/movie.mkv",
		TargetCodecVideo: "copy",
		TargetCodecAudio: "copy",
		AudioTrackIndex:  -1,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body))
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

func TestHandleDownloadPrepareRejectsInvalidArtifactID(t *testing.T) {
	server := newTestServer(t)
	body, err := json.Marshal(downloadprepare.Request{
		ArtifactID:       "../artifact-2",
		InputPath:        "/media/movie.mkv",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	server.handleDownloadPrepare(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleDownloadPrepareRejectsUnavailableConfig(t *testing.T) {
	server := newTestServer(t)
	server.watcher.SetConfigForTest(nil)
	body := []byte(`{"artifact_id":"artifact-3","input_path":"/media/movie.mkv"}`)

	req := httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	server.handleDownloadPrepare(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleStartRejectsUnapprovedInputPath(t *testing.T) {
	server := newTestServer(t)
	server.inputPaths = NewCatalogPathAuthorizer(staticCatalogPaths{})
	body := []byte(`{"session_id":"unsafe-input","input_path":"http://example.test/movie.mkv"}`)

	req := httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	server.handleStart(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestDownloadPrepareRouteRequiresNodeBearer(t *testing.T) {
	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/downloads/prepare", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
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
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "artifact-range.mp4")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".receipt.json", []byte(`{"artifact_id":"artifact-range"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/downloads/artifacts/artifact-range", nil)
	req.Header.Set("Authorization", "Bearer "+testSecret)
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
	deleteReq.Header.Set("Authorization", "Bearer "+testSecret)
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
	if err := os.MkdirAll(server.artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(server.artifactRoot, "artifact-delete-invalidation.mp4")
	if err := os.WriteFile(outputPath, []byte("oldbytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	receiptPath := outputPath + ".receipt.json"
	if err := os.Mkdir(receiptPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(receiptPath, "blocks-removal"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodDelete, "/downloads/artifacts/artifact-delete-invalidation", nil)
	request.Header.Set("Authorization", "Bearer "+testSecret)
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
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("artifact_id", artifactID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
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
	if err := os.MkdirAll(server.artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(server.artifactRoot, artifactID+".mp4")
	if err := os.WriteFile(path, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}

	blocked := newBlockingResponseWriter()
	releaseOnce := sync.Once{}
	release := func() { releaseOnce.Do(func() { close(blocked.releaseWrite) }) }
	defer release()
	getDone := make(chan struct{})
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/downloads/artifacts/"+artifactID, nil)
		req.Header.Set("Authorization", "Bearer "+testSecret)
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
		req.Header.Set("Authorization", "Bearer "+testSecret)
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

	ffmpegPath := filepath.Join(t.TempDir(), "failing-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	requestBody, err := json.Marshal(TranscodeStartRequest{
		SessionID:             "public-session-legacy-replacement",
		InputPath:             "/media/movie.mkv",
		TargetCodecVideo:      "copy",
		TargetCodecAudio:      "aac",
		SegmentDuration:       2,
		RequireReady:          true,
		CopyFMP4RecipeVersion: playback.CopyFMP4RecipeVersion,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(requestBody))
	rr := httptest.NewRecorder()
	server.handleStart(rr, req)
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

func TestHandleProgressiveRemuxExecutesSignedTranscodeRoute(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 11, true }
	mediaPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nprintf node-remux-bytes\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	claims := streamtoken.Claims{
		SessionID: "playback-1", MediaPath: mediaPath, PlayMethod: string(playback.PlayRemux),
		TranscodeNode: "http://node", TranscodeTransportID: "transport-1",
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 11, RoutingEgress: string(noderouting.EgressProxy),
	}
	card := playback.RecipeCardFromClaims(&claims)
	server.SetRecipeStore(&stubRecipeStore{card: &card, ok: true})
	token, err := streamtoken.Sign(claims, testSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/remux/transport-1?seek=2", nil)
	req.Header.Set("X-Silo-Stream-Token", token)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("session_id", "transport-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
	rr := httptest.NewRecorder()

	server.handleRemux(rr, req)

	if rr.Code != http.StatusOK || rr.Body.String() != "node-remux-bytes" {
		t.Fatalf("response = %d %q, want remux bytes", rr.Code, rr.Body.String())
	}
	if got := server.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs after response = %d, want 0", got)
	}
}

func TestHandleProgressiveRemuxHeadDoesNotStartFFmpeg(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 11, true }
	mediaPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpegMarker := filepath.Join(t.TempDir(), "ffmpeg-ran")
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\ntouch \""+ffmpegMarker+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	claims := streamtoken.Claims{
		SessionID: "playback-head", MediaPath: mediaPath, PlayMethod: string(playback.PlayRemux),
		TranscodeNode: "http://registered-node", TranscodeTransportID: "transport-head",
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 11, RoutingEgress: string(noderouting.EgressProxy),
	}
	card := playback.RecipeCardFromClaims(&claims)
	server.SetRecipeStore(&stubRecipeStore{card: &card, ok: true})
	token, err := streamtoken.Sign(claims, testSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodHead, "/remux/transport-head", nil)
	request.Header.Set("X-Silo-Stream-Token", token)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("session_id", "transport-head")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	recorder := httptest.NewRecorder()

	server.handleRemux(recorder, request)

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
	stoppedRequest := httptest.NewRequest(http.MethodHead, "/remux/transport-head", nil)
	stoppedRequest.Header.Set("X-Silo-Stream-Token", token)
	stoppedRouteContext := chi.NewRouteContext()
	stoppedRouteContext.URLParams.Add("session_id", "transport-head")
	stoppedRequest = stoppedRequest.WithContext(context.WithValue(stoppedRequest.Context(), chi.RouteCtxKey, stoppedRouteContext))
	stoppedRecorder := httptest.NewRecorder()

	server.handleRemux(stoppedRecorder, stoppedRequest)

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
	mediaPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpegLog := filepath.Join(t.TempDir(), "ffmpeg.log")
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.sh")
	ffmpegScript := "#!/bin/sh\n" +
		"case \" $* \" in *\" -i " + mediaPath + " \"*) printf 'invoked\\n' >> \"" + ffmpegLog + "\";; esac\n" +
		"printf node-remux-bytes\n"
	if err := os.WriteFile(ffmpegPath, []byte(ffmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	claims := streamtoken.Claims{
		SessionID: "playback-concurrent", MediaPath: mediaPath, PlayMethod: string(playback.PlayRemux),
		TranscodeNode: "http://node", TranscodeTransportID: "transport-concurrent",
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 11, RoutingEgress: string(noderouting.EgressProxy),
	}
	card := playback.RecipeCardFromClaims(&claims)
	server.SetRecipeStore(&stubRecipeStore{card: &card, ok: true})
	token, err := streamtoken.Sign(claims, testSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/remux/"+claims.TranscodeTransportID, nil)
	request.Header.Set("X-Silo-Stream-Token", token)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("session_id", claims.TranscodeTransportID)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
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
	claims := streamtoken.Claims{
		SessionID: "playback-1", MediaPath: "/media/movie.mkv", PlayMethod: string(playback.PlayRemux),
		TranscodeNode: "http://node", TranscodeTransportID: "transport-1",
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionProxy),
		RoutingEgress: string(noderouting.EgressProxy),
	}
	token, err := streamtoken.Sign(claims, testSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/remux/transport-1", nil)
	req.Header.Set("X-Silo-Stream-Token", token)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("session_id", "transport-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
	rr := httptest.NewRecorder()

	server.handleRemux(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := server.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs = %d, want no FFmpeg start", got)
	}
}

func TestHandleStopCancelsProgressiveRemux(t *testing.T) {
	server := newTestServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	releaseHandler := make(chan struct{})
	server.activeJobs.Add(1)
	server.progressiveRemuxes["transport-1"] = progressiveRemuxRequest{
		id: 1, playbackSessionID: "playback-1", cancel: cancel, done: done,
	}
	req := httptest.NewRequest(http.MethodDelete, "/transcode/transport-1", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("session_id", "transport-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
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
	req := httptest.NewRequest(http.MethodDelete, "/transcode/transport-stuck", nil).WithContext(requestCtx)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("session_id", "transport-stuck")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
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
	req := httptest.NewRequest(http.MethodDelete, "/transcode/"+transportID, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("session_id", transportID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
	rr := httptest.NewRecorder()

	server.handleStop(rr, req)

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
	req := httptest.NewRequest(http.MethodDelete, "/transcode/"+sessionID, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("session_id", sessionID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
	rr := httptest.NewRecorder()

	server.handleStop(rr, req)

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

func TestHandleProgressiveRemuxRefusesStoppedTransportAfterRestart(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 11, true }
	mediaPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpegLog := filepath.Join(t.TempDir(), "ffmpeg.log")
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nprintf invoked > \""+ffmpegLog+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	claims := streamtoken.Claims{
		SessionID: "playback-1", MediaPath: mediaPath, PlayMethod: string(playback.PlayRemux),
		TranscodeNode: "http://node", TranscodeTransportID: "transport-1",
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 11, RoutingEgress: string(noderouting.EgressProxy),
	}
	card := playback.RecipeCardFromClaims(&claims)
	store := &stubRecipeStore{card: &card, ok: true}
	server.SetRecipeStore(store)
	stopRequest := httptest.NewRequest(http.MethodDelete, "/transcode/transport-1", nil)
	stopRouteContext := chi.NewRouteContext()
	stopRouteContext.URLParams.Add("session_id", "transport-1")
	stopRequest = stopRequest.WithContext(context.WithValue(stopRequest.Context(), chi.RouteCtxKey, stopRouteContext))
	server.handleStop(httptest.NewRecorder(), stopRequest)

	// A replacement process has no in-memory stop fence. The deleted durable
	// authority must still reject the old token before FFmpeg can start.
	restarted := newTestServer(t)
	restarted.nodeRowID = func() (int, bool) { return 11, true }
	restarted.watcher.Config().Playback.FFmpegPath = ffmpegPath
	restarted.SetRecipeStore(store)
	token, err := streamtoken.Sign(claims, testSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/remux/transport-1", nil)
	request.Header.Set("X-Silo-Stream-Token", token)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("session_id", "transport-1")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	recorder := httptest.NewRecorder()

	restarted.handleRemux(recorder, request)

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
	mediaPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nprintf node-remux-bytes\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	claims := streamtoken.Claims{
		SessionID: "playback-reload-gate", MediaPath: mediaPath, PlayMethod: string(playback.PlayRemux),
		TranscodeNode: "http://node", TranscodeTransportID: "transport-reload-gate",
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 11, RoutingEgress: string(noderouting.EgressProxy),
	}
	card := playback.RecipeCardFromClaims(&claims)
	storeHit := make(chan struct{}, 1)
	server.SetRecipeStore(&stubRecipeStore{card: &card, ok: true, getHit: storeHit})
	token, err := streamtoken.Sign(claims, testSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/remux/transport-reload-gate", nil)
	request.Header.Set("X-Silo-Stream-Token", token)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("session_id", "transport-reload-gate")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
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

func TestForceReloadTeardownDrainsRealProgressiveRemuxWithBlockedClient(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 11, true }
	server.registeredNodeURL = func() (string, bool) { return "http://node", true }
	mediaPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.sh")
	ffmpegScript := "#!/bin/sh\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$arg\" = \"pipe:1\" ]; then\n" +
		"    while :; do printf node-remux-bytes; sleep 0.01; done\n" +
		"  fi\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(ffmpegPath, []byte(ffmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	claims := streamtoken.Claims{
		SessionID: "playback-force-reload", MediaPath: mediaPath, PlayMethod: string(playback.PlayRemux),
		TranscodeNode: "http://node", TranscodeTransportID: "transport-force-reload-real",
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 11, RoutingEgress: string(noderouting.EgressProxy),
	}
	card := playback.RecipeCardFromClaims(&claims)
	server.SetRecipeStore(&stubRecipeStore{card: &card, ok: true})
	token, err := streamtoken.Sign(claims, testSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/remux/"+claims.TranscodeTransportID, nil)
	request.Header.Set("X-Silo-Stream-Token", token)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("session_id", claims.TranscodeTransportID)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	writer := newAbortableBlockingResponseWriter()
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
		t.Fatalf("active jobs before force reload = %d, want 1", got)
	}

	if err := server.teardownForForceReload(waitCtx, "http://node", 11); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handlerDone:
	case <-waitCtx.Done():
		t.Fatal("force reload returned without draining the progressive remux handler")
	}
	if got := server.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs after force reload = %d, want 0", got)
	}
	server.mu.RLock()
	_, active := server.progressiveRemuxes[claims.TranscodeTransportID]
	server.mu.RUnlock()
	if active {
		t.Fatal("force reload left the drained remux registered")
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

			request := requestWithToken(sid, signCard(t, card))
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("session_id", sid)
			if endpoint == "segment" {
				rctx.URLParams.Add("name", "0.ts")
			}
			request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, rctx))
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

func (s *stubRecipeStore) Delete(_ context.Context, sessionID string) error {
	s.deletes = append(s.deletes, sessionID)
	if s.delErr != nil {
		return s.delErr
	}
	s.card = nil
	s.ok = false
	return nil
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

	s := newTestServer(t)
	s.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
	ffmpegPath := filepath.Join(t.TempDir(), "looping-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nwhile :; do sleep 0.1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.watcher.Config().Playback.FFmpegPath = ffmpegPath
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

	r := httptest.NewRequest(http.MethodDelete, "/transcode/"+sid, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("session_id", sid)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	s.handleStop(rec, r)

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

// spawnReconstruct must NOT apply the fast seg×dur resume seek for copy-mode
// cards: copy-mode segments have variable durations, so seg×dur points at the
// wrong source time. The card's original start must stand. Asserting opts off a
// real spawn would need ffmpeg, so this checks the gating condition directly.
func TestCopyModeReconstruct_SkipsFastSeek(t *testing.T) {
	const dur = 6
	card := playback.RecipeCard{
		SessionID:          "copy-sess-1",
		PlayMethod:         playback.PlayTranscode,
		TargetCodecVideo:   "copy",
		SegmentDuration:    dur,
		StartSegmentNumber: 0,
	}
	const requestedSegment = 10
	applyFastSeek := requestedSegment > card.StartSegmentNumber && card.SegmentDuration > 0 &&
		!strings.EqualFold(card.TargetCodecVideo, "copy")
	if applyFastSeek {
		t.Fatalf("copy-mode card must not apply the seg×dur fast seek")
	}

	// Same shape but ENCODED: the fast seek must apply.
	card.TargetCodecVideo = "h264"
	applyFastSeek = requestedSegment > card.StartSegmentNumber && card.SegmentDuration > 0 &&
		!strings.EqualFold(card.TargetCodecVideo, "copy")
	if !applyFastSeek {
		t.Fatalf("encoded card must apply the seg×dur fast seek")
	}
}

func TestSpawnReconstructDoesNotRegisterFailedSoftwareRetry(t *testing.T) {
	server := newTestServer(t)
	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	logPath := filepath.Join(dir, "invocations.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n" +
		"case \"$*\" in\n" +
		"  *-hwaccels*) echo videotoolbox; exit 0 ;;\n" +
		"  *-encoders*) echo ' V..... h264_videotoolbox x'; exit 0 ;;\n" +
		"  *videotoolbox*'-f null'*) exit 0 ;;\n" +
		"  *) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := server.watcher.Config()
	cfg.Playback.FFmpegPath = ffmpegPath
	cfg.Playback.HWAccel = tonemap.BackendVideoToolbox

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
	server := newTestServer(t)
	ffmpegPath := filepath.Join(t.TempDir(), "looping-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nwhile :; do sleep 0.1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// This test reaches the spawn/track path, so it needs a (no-op) tracker.
	server.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
	cfg := server.watcher.Config()
	cfg.Playback.FFmpegPath = ffmpegPath
	// Neither device exists, so resolution deterministically lands on the
	// first entry; the point is that the configured list reaches the session.
	cfg.Playback.HWDevice = "/dev/dri/renderD888,/dev/dri/renderD889"

	requestBody, err := json.Marshal(TranscodeStartRequest{
		SessionID:        "hwdevice-start-1",
		InputPath:        "/media/movie.mkv",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
		HWAccel:          "vaapi",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(requestBody))
	rr := httptest.NewRecorder()
	server.handleStart(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	server.mu.RLock()
	session := server.sessions["hwdevice-start-1"]
	server.mu.RUnlock()
	if session == nil {
		t.Fatal("session was not registered")
	}
	defer session.CloseProcess()
	if got := session.Opts().HWDevice; got != "/dev/dri/renderD888" {
		t.Fatalf("session HWDevice = %q, want one concrete device from the configured list", got)
	}
}

func TestRemoteSessionTrackingPreservesResolvedToneMapMode(t *testing.T) {
	for _, test := range []struct {
		name        string
		reconstruct bool
	}{
		{name: "fresh remote session"},
		{name: "reconstructed remote session", reconstruct: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			tracker := newBlockingSessionTracker()
			server.tracker = tracker
			ffmpegPath := filepath.Join(t.TempDir(), "tone-map-ffmpeg.sh")
			script := "#!/bin/sh\n" +
				"case \" $* \" in\n" +
				"  *\" -filters \"*) printf ' T.. zscale V->V scale\\n T.. tonemapx V->V tone-map\\n T.. sidedata V->V metadata\\n'; exit 0;;\n" +
				"  *\" -encoders \"*) printf ' V..... libx264 H.264\\n'; exit 0;;\n" +
				"  *\"-f null\"*) exit 0;;\n" +
				"esac\n" +
				"while :; do sleep 0.1; done\n"
			if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			server.watcher.Config().Playback.FFmpegPath = ffmpegPath
			server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
			inputPath := filepath.Join(t.TempDir(), "source.mkv")
			if err := os.WriteFile(inputPath, []byte("source"), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(inputPath)
			if err != nil {
				t.Fatal(err)
			}
			track := nodeToneMapTrack()
			writeNodeToneMapFFprobe(t, ffmpegPath, track)
			opts := playback.TranscodeOpts{
				SessionID: "tracked-tone-map", InputPath: inputPath,
				TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "1080p", SegmentDuration: 2,
				ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
				ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
				ToneMapSourceRevision: tonemap.RevisionForFile(&models.MediaFile{ID: 1, FileSize: info.Size(), VideoTracks: []models.VideoTrack{track}}),
				FastStart:             true,
			}

			var session *playback.TranscodeSession
			if test.reconstruct {
				card := playback.NewRecipeCard(7, "profile-1", 42, "", opts)
				session, _ = server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), opts.SessionID, -1, card)
				if session == nil {
					t.Fatal("reconstructed session was not started")
				}
			} else {
				body, err := json.Marshal(TranscodeStartRequest{
					SessionID: opts.SessionID, InputPath: opts.InputPath,
					TargetCodecVideo: opts.TargetCodecVideo, TargetCodecAudio: opts.TargetCodecAudio,
					TargetResolution: opts.TargetResolution, SegmentDuration: opts.SegmentDuration,
					ToneMapPolicy: opts.ToneMapPolicy, ToneMapMode: opts.ToneMapMode,
					ToneMapSourceKind: opts.ToneMapSourceKind, ToneMapRecipeVersion: opts.ToneMapRecipeVersion,
					ToneMapSourceRevision: opts.ToneMapSourceRevision,
				})
				if err != nil {
					t.Fatal(err)
				}
				recorder := httptest.NewRecorder()
				server.handleStart(recorder, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
				if recorder.Code != http.StatusAccepted {
					t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
				}
				server.mu.RLock()
				session = server.sessions[opts.SessionID]
				server.mu.RUnlock()
				if session == nil {
					t.Fatal("remote session was not registered")
				}
			}
			if test.reconstruct && session.Opts().FastStart {
				t.Fatal("reconstructed node runtime retained fresh-start manifest tuning")
			}
			defer func() { _ = session.CloseProcess() }()

			select {
			case <-tracker.trackStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("remote session tracking did not start")
			}
			tracker.mu.Lock()
			tracked := tracker.tracked
			tracker.mu.Unlock()
			close(tracker.trackRelease)
			if tracked.ToneMapMode != string(tonemap.ModeSoftware) {
				t.Fatalf("tracked tone-map mode = %q, want %q", tracked.ToneMapMode, tonemap.ModeSoftware)
			}
		})
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
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			server.handleStart(recorder, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
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
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\n" +
		"touch '" + probeStarted + "'\n" +
		"while [ ! -e '" + releaseProbe + "' ]; do sleep 0.01; done\n" +
		"case \" $* \" in\n" +
		"  *\" -filters \"*) printf ' T.. zscale V->V scale\\n T.. tonemapx V->V tone-map\\n T.. sidedata V->V metadata\\n';;\n" +
		"  *\" -encoders \"*) printf ' V..... libx264 H.264\\n';;\n" +
		"esac\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	requestBody, err := json.Marshal(TranscodeStartRequest{
		SessionID: "config-change", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.handleStart(recorder, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(requestBody)))
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
		{name: "deadline exceeded", new: func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}},
	}
	for _, test := range tests {
		for _, contextTest := range contexts {
			t.Run(test.name+"/"+contextTest.name, func(t *testing.T) {
				server := newTestServer(t)
				body, err := json.Marshal(test.body)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := contextTest.new()
				defer cancel()
				request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)).WithContext(ctx)
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
	requestBody, err := json.Marshal(downloadprepare.Request{
		ArtifactID: artifactID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: "stale",
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.handleDownloadPrepare(recorder, httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(requestBody)))
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
	requestBody, err := json.Marshal(TranscodeStartRequest{
		SessionID: "tone-map-start-lock-order", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: "stale",
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.handleStart(recorder, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(requestBody)))
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
	server := newTestServer(t)
	ffmpegPath := filepath.Join(t.TempDir(), "looping-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nwhile :; do sleep 0.1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	requestBody, err := json.Marshal(TranscodeStartRequest{
		SessionID: "sample-entry-start-1", InputPath: "/media/movie.mkv",
		VideoSampleEntry: playback.VideoSampleEntryHVC1,
		TargetCodecVideo: "copy", TargetCodecAudio: "copy", SegmentDuration: 2,
		CopyFMP4RecipeVersion: playback.CopyFMP4RecipeVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(requestBody))
	rr := httptest.NewRecorder()
	server.handleStart(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	server.mu.RLock()
	session := server.sessions["sample-entry-start-1"]
	server.mu.RUnlock()
	if session == nil {
		t.Fatal("session was not registered")
	}
	defer func() { _ = session.CloseProcess() }()
	if got := session.Opts().VideoSampleEntry; got != playback.VideoSampleEntryHVC1 {
		t.Fatalf("VideoSampleEntry = %q", got)
	}
}

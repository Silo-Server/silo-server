package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const decodeRotationNeutralURI = "virtual://movie/tt-rot"

func decodeRotationCandidateURI(id string) string {
	return decodeRotationNeutralURI + "?result=" + id
}

// writePlaybackTestFFmpegDecodeFailureBeforeManifest emits the decoder's
// invalid-bitstream failure as a sustained storm and never writes a manifest,
// so the start's manifest wait can only resolve through the observation
// lifecycle: suspicion at the threshold, confirmation after the observation
// window, and then ErrSourceDecodeRejected. A manifest appearing inside the
// observation window would be recovery, not rejection, so producing one here
// would make the fixture race the deadline.
func writePlaybackTestFFmpegDecodeFailureBeforeManifest(t *testing.T) string {
	t.Helper()
	// The process stays alive emitting the decoder storm and never writes a
	// manifest, so the start's manifest wait can only resolve through the
	// observation lifecycle: suspicion at the threshold, confirmation after
	// the observation window, then ErrSourceDecodeRejected. It does not exit
	// early, so the rejection is a decoder verdict (reaping the process) rather
	// than an early-exit generic failure.
	script := "#!/bin/sh\n" +
		"i=0\n" +
		"while [ $i -lt 20 ]; do echo \"[hevc @ 0x1] Error submitting packet to decoder: Invalid data found when processing input\" >&2; i=$((i+1)); done\n" +
		"sleep 30\n"
	return writeDecodeRotationFFmpeg(t, "decode-fail-before-manifest.sh", script)
}

// writePlaybackTestFFmpegRejectAfterManifest writes a ready manifest first and
// only then emits the decoder failures, so a start commits a live generation
// that subsequently becomes source-rejected. This is the shape a real decoder
// rejection has once playback is already running.
func writePlaybackTestFFmpegRejectAfterManifest(t *testing.T) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"case \"$last\" in\n" +
		"  *.m3u8) out=\"$(dirname \"$last\")\"; mkdir -p \"$out\"; " +
		"printf '\\000\\000\\000\\010ftyp\\000\\000\\000\\010moov' > \"$out/init.mp4\"; printf x > \"$out/seg_0.m4s\"; " +
		"printf x > \"$out/seg_1.m4s\"; printf x > \"$out/seg_2.m4s\"; " +
		"printf '#EXTM3U\\n#EXT-X-VERSION:7\\n#EXT-X-TARGETDURATION:2\\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\\n#EXT-X-MAP:URI=\"init.mp4\"\\n" +
		"#EXTINF:2.0,\\nseg_0.m4s\\n#EXTINF:2.0,\\nseg_1.m4s\\n" +
		"#EXTINF:2.0,\\nseg_2.m4s\\n' > \"$last\" ;;\n" +
		"esac\n" +
		// The generation decoded video until its decoder turned on the source,
		// which is what makes this a *post-start* rejection: the muxed segments
		// carry real pictures, so the start commits before the storm arrives.
		playbackTestFFmpegProgress +
		"sleep 1\n" +
		"i=0\n" +
		"while [ $i -lt 20 ]; do echo \"[hevc @ 0x1] Error submitting packet to decoder: Invalid data found when processing input\" >&2; i=$((i+1)); done\n" +
		"sleep 30\n"
	return writeDecodeRotationFFmpeg(t, "decode-fail-after-manifest.sh", script)
}

func writeDecodeRotationFFmpeg(t *testing.T, name, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

type decodeRotationOptions struct {
	candidateIDs        []string
	softwareFallback    string
	decodeFailCalls     int32
	rejectAfterManifest bool
	// requestedResult makes the catalog row a concrete result= selection; empty
	// keeps it neutral.
	requestedResult string
	// probeEvidence gives the catalog row complete probe evidence so the P0
	// repeat-play fast path would normally apply.
	probeEvidence bool
	// requestedFailed marks the catalog row failed_at.
	requestedFailed bool
	// stampedCandidates marks additional catalog rows (by candidate id) with
	// an active failed_at verdict, simulating markers that landed on prior
	// rotation hops. Keys are candidate ids ("B"), values ignored (use true).
	stampedCandidates map[string]bool
	// requestedDelivered stamps last_delivered_at inside the delivery grace.
	requestedDelivered bool
	// startFailureCalls makes the next N transcode starts fail to start (a
	// non-decode transport failure), used to prove the rotation rebind is not
	// applied to the live session before the durable replacement commits.
	startFailureCalls int32
	// markerFails makes the source-rejected marker callback return an error
	// instead of succeeding, so a test can prove rotation no longer depends on
	// the asynchronous failed_at stamp landing.
	markerFails bool
	// markerBlocked makes the source-rejected marker callback block until the
	// test releases it, so a test can prove a wedged marker cannot stall the
	// rotation: the durable chain is written by the replan, not the callback.
	markerBlocked bool
}

// decodeRotationFixture is a virtual HLS-transcode handler whose provider
// candidates are named in order. The first N transcode starts are configured to
// reject the source; later starts are ready. It records the exclusion list every
// detailed resolve received, the candidates the rejection callback stamped, and
// the software-decode flag of every spawn.
type decodeRotationFixture struct {
	handler      *PlaybackHandler
	file         *models.MediaFile
	candidateIDs []string

	mu              sync.Mutex
	resolveExcluded [][]string
	marked          []string
	softwareSpawns  int
	transcodeCalls  int32
	// markerRelease unblocks a blocked marker callback when non-nil.
	markerRelease chan struct{}
}

func newDecodeRotationFixture(t *testing.T, opt decodeRotationOptions) *decodeRotationFixture {
	t.Helper()
	source := v3HandlerFixtureFile(t)
	source.ID = 610
	source.ContentID = "movie-rot"
	source.FilePath = decodeRotationNeutralURI
	source.VirtualOwnerInstallationID = 5
	source.CodecVideo = "hevc"
	source.Resolution = "1080p"
	source.Bitrate = 8_000
	source.VideoTracks = []models.VideoTrack{{
		Codec: "hevc", Profile: "Main", Level: 120, Width: 1920, Height: 1080,
		FrameRate: "24000/1001", Bitrate: 8_000, BitDepth: 8,
		VideoRange: videoRangeSDR, VideoRangeType: videoRangeSDR,
	}}
	source.Container = "virtual"
	if opt.probeEvidence {
		source.Container = "mkv"
		stamp := time.Now().Add(-time.Hour)
		source.ProbeUpdatedAt = &stamp
	}
	if opt.requestedResult != "" {
		source.FilePath = decodeRotationCandidateURI(opt.requestedResult)
	}
	if opt.requestedFailed {
		stamp := time.Now().Add(-time.Hour)
		source.FailedAt = &stamp
	}
	if opt.requestedDelivered {
		delivered := time.Now().Add(-time.Hour)
		source.LastDeliveredAt = &delivered
	}

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: map[int]*models.MediaFile{source.ID: source}})
	decodeBeforeFFmpeg := writePlaybackTestFFmpegDecodeFailureBeforeManifest(t)
	rejectAfterFFmpeg := writePlaybackTestFFmpegRejectAfterManifest(t)
	failingFFmpeg := writePlaybackTestFFmpegAlwaysFailing(t)
	readyFFmpeg := writePlaybackTestFFmpeg(t)
	baseConfig := playbackTestConfig(decodeBeforeFFmpeg, t.TempDir())
	handler.PlaybackConfig = func() config.PlaybackConfig {
		cfg := baseConfig()
		cfg.HWAccel = "qsv"
		cfg.SoftwareFallback = opt.softwareFallback
		return cfg
	}
	stubCopySeekAnchorV3(handler)
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
	}))
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{
		"transcode_enabled":                            "true",
		"allow_4k_transcode":                           "true",
		"playback.max_virtual_failover_attempts":       "3",
		"playback.transcode_hardware_tone_map_enabled": "false",
		"playback.transcode_software_tone_map_enabled": "false",
		"playback.transcode_vpp_tone_map_enabled":      "false",
	}}

	f := &decodeRotationFixture{handler: handler, file: source, candidateIDs: opt.candidateIDs}
	if opt.markerBlocked {
		f.markerRelease = make(chan struct{})
	}
	resolved := func(id string) ResolvedVirtualMedia {
		return ResolvedVirtualMedia{
			URL:         "http://127.0.0.1:9/stream?result=" + id,
			URI:         decodeRotationCandidateURI(id),
			CandidateID: id,
		}
	}
	handler.VirtualPlaybackResolver = VirtualPlaybackResolverFunc(func(_ context.Context, path string, _ int, _ string, _ int) (string, error) {
		return "http://127.0.0.1:9/stream?path=" + path, nil
	})
	handler.VirtualPlaybackStreamLister = VirtualPlaybackStreamListerFunc(func(_ context.Context, _ string, _ int, _ string, _ int) ([]VirtualPlaybackStream, error) {
		out := make([]VirtualPlaybackStream, 0, len(opt.candidateIDs))
		for _, id := range opt.candidateIDs {
			out = append(out, VirtualPlaybackStream{
				ID: id, URI: decodeRotationCandidateURI(id),
				Resolution: source.Resolution, CodecVideo: source.CodecVideo, CodecAudio: source.CodecAudio, Container: "mkv",
			})
		}
		return out, nil
	})
	// Candidate rows are looked up by exact URI so the requested row's failed
	// stamp is visible while its siblings stay eligible.
	handler.VirtualFileLookup = func(_ context.Context, path string) (*models.MediaFile, error) {
		if opt.requestedFailed && strings.TrimSpace(path) == source.FilePath {
			row := *source
			return &row, nil
		}
		// Stamped sibling rows: a catalog row per candidate carrying an
		// active failed_at verdict, as the decode marker leaves behind on
		// prior rotation hops.
		if id := virtualResultCandidateID(strings.TrimSpace(path)); id != "" && opt.stampedCandidates[id] {
			stamp := time.Now()
			return &models.MediaFile{
				ID:       900 + len(id),
				FilePath: path,
				FailedAt: &stamp,
			}, nil
		}
		return nil, nil
	}
	handler.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(_ context.Context, virtualURI string, _ int, _ int, _ string, _ bool, excluded []string, preferred string) (ResolvedVirtualMedia, error) {
		f.mu.Lock()
		f.resolveExcluded = append(f.resolveExcluded, append([]string(nil), excluded...))
		f.mu.Unlock()
		// Mirror the real resolver: the URI's pinned result= wins when it is
		// still eligible, then the preferred candidate, then the ranked list.
		if parsed, err := url.Parse(virtualURI); err == nil {
			if id := strings.TrimSpace(parsed.Query().Get("result")); id != "" && !containsStringExactV3(excluded, id) {
				return resolved(id), nil
			}
		}
		if preferred != "" && !containsStringExactV3(excluded, preferred) {
			return resolved(preferred), nil
		}
		for _, id := range opt.candidateIDs {
			if !containsStringExactV3(excluded, id) {
				return resolved(id), nil
			}
		}
		return ResolvedVirtualMedia{}, errors.New("no eligible virtual candidate")
	})
	handler.VirtualPlaybackSourceProber = func(_ context.Context, _ string, file *models.MediaFile) (*models.MediaFile, error) {
		file.VideoTracks = source.VideoTracks
		file.AudioTracks = source.AudioTracks
		file.CodecVideo, file.CodecAudio, file.Resolution, file.Container = "hevc", "aac", "1080p", "mkv"
		return file, nil
	}
	handler.StartTranscodeFunc = func(ctx context.Context, opts playback.TranscodeOpts) (*playback.TranscodeSession, error) {
		call := atomic.AddInt32(&f.transcodeCalls, 1)
		switch {
		case call <= opt.decodeFailCalls && opt.rejectAfterManifest:
			opts.FFmpegPath = rejectAfterFFmpeg
		case call <= opt.decodeFailCalls:
			opts.FFmpegPath = decodeBeforeFFmpeg
		case opt.startFailureCalls > 0 && call <= opt.decodeFailCalls+opt.startFailureCalls:
			opts.FFmpegPath = failingFFmpeg
		default:
			opts.FFmpegPath = readyFFmpeg
		}
		if opts.SoftwareVideoDecode {
			f.mu.Lock()
			f.softwareSpawns++
			f.mu.Unlock()
		}
		return playback.StartTranscode(ctx, opts)
	}
	handler.TranscodeManager().OnSourceRejected = func(_ context.Context, _ int, canonical string) error {
		f.mu.Lock()
		f.marked = append(f.marked, canonical)
		release := f.markerRelease
		markerFails := opt.markerFails
		f.mu.Unlock()
		if release != nil {
			<-release
		}
		if markerFails {
			return errors.New("marker persistence failed")
		}
		return nil
	}
	return f
}

func (f *decodeRotationFixture) request() playback.StartRequestV3 {
	start := v3HandlerStartRequest()
	start.FileID = f.file.ID
	start.QualityPreference = "auto"
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		Containers: []string{"hls"}, VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
	}
	return start
}

func (f *decodeRotationFixture) start(t *testing.T, start playback.StartRequestV3) (int, playback.DecisionResponseV3) {
	t.Helper()
	rr := httptest.NewRecorder()
	f.handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	var response playback.DecisionResponseV3
	if rr.Body.Len() > 0 {
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode start response: %v; body = %s", err, rr.Body.String())
		}
	}
	return rr.Code, response
}

func (f *decodeRotationFixture) sessionVirtualURI(t *testing.T, sessionID string) string {
	t.Helper()
	session, err := f.handler.sessionMgr.GetSession(sessionID)
	if err != nil || session == nil {
		t.Fatalf("GetSession(%q): %v", sessionID, err)
	}
	return session.VirtualSourceURI
}

// waitForSourceRejected waits on the live generation's observable decode
// verdict rather than racing it with a fixed sleep.
func (f *decodeRotationFixture) waitForSourceRejected(t *testing.T, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		live := f.handler.tm.GetTranscodeSession(sessionID)
		if live != nil && live.IsSourceRejected() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the decoder rejection was never observed on the live session")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *decodeRotationFixture) recordedExclusions() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.resolveExcluded))
	copy(out, f.resolveExcluded)
	return out
}

func (f *decodeRotationFixture) softwareSpawnCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.softwareSpawns
}

// waitForMarked waits on the observable rejection stamp the async transcode
// callback writes, rather than racing it with a fixed sleep.
func (f *decodeRotationFixture) waitForMarked(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.mu.Lock()
		marked := len(f.marked)
		f.mu.Unlock()
		if marked > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("rejected source was never stamped on the candidate")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func containsExclusion(exclusions [][]string, want ...string) bool {
	for _, got := range exclusions {
		if len(got) != len(want) {
			continue
		}
		match := true
		for i := range want {
			if got[i] != want[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func decodeRotationReplanRequest(start playback.StartRequestV3, plan *playback.PlanV3, replanID string) playback.ReplanRequestV3 {
	return playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, ClientFeatures: start.ClientFeatures,
		Operation: playback.ReplanOperationFailureRecoveryV3, PlaybackAttemptID: start.PlaybackAttemptID,
		ReplanRequestID: replanID, FailedPlanID: plan.PlanID,
		PlanAttemptID: replanID + "-attempt", PlanAttemptKey: plan.PlanAttemptKey,
		AttemptedPlanKeys: []string{plan.PlanAttemptKey}, AttemptCount: 1,
		PositionSeconds: 10, SelectedTracks: plan.SelectedTracks,
		Failure:               playback.FailureV3{Classification: "decode_error"},
		Capabilities:          start.Capabilities,
		ClientPlaybackContext: start.ClientPlaybackContext,
	}
}

// TestVirtualStartRotatesDecodeRejectedCandidate drives an auto start whose first
// provider release is rejected during startup. The server must substitute the
// second release before committing, so the client receives a playable plan (no
// 422) bound to the second candidate and the requested catalog id is preserved.
//
// The poison budget covers both hw_accel=auto stages of the rejected
// candidate: a decode verdict fails the manifest wait fast, so the automatic
// pipeline advances hardware→software on the same bytes before the rotation
// loop substitutes the sibling. Both attempts must fail for the rotation to
// engage; the healthy sibling then commits on its first attempt.
func TestVirtualStartRotatesDecodeRejectedCandidate(t *testing.T) {
	f := newDecodeRotationFixture(t, decodeRotationOptions{candidateIDs: []string{"A", "B"}, decodeFailCalls: 1})

	code, response := f.start(t, f.request())
	if code != http.StatusCreated {
		t.Fatalf("start status = %d, want %d; body = %+v", code, http.StatusCreated, response)
	}
	if response.Terminal != nil {
		t.Fatalf("auto start terminalled instead of rotating: %+v", response.Terminal)
	}
	if response.PlaybackPlan == nil || response.PlaybackPlan.Delivery != playback.DeliveryTranscodeHLSV3 {
		t.Fatalf("start plan = %#v, want a server-transcode plan", response.PlaybackPlan)
	}
	if response.PlaybackPlan.EffectiveMediaFileID != f.file.ID {
		t.Fatalf("effective file = %d, want the requested catalog id %d", response.PlaybackPlan.EffectiveMediaFileID, f.file.ID)
	}
	if got := f.sessionVirtualURI(t, response.SessionID); got != decodeRotationCandidateURI("B") {
		t.Fatalf("effective virtual uri = %q, want the rotated candidate B", got)
	}
	if !containsExclusion(f.recordedExclusions(), "A") {
		t.Fatalf("rotation never excluded the rejected candidate; exclusions = %v", f.recordedExclusions())
	}
	if got := f.softwareSpawnCount(); got != 0 {
		t.Fatalf("rotation spawned %d software-decode transcodes, want 0", got)
	}
	f.waitForMarked(t)
}

// TestVirtualStartRotationExcludesProbedRequestedRow repeats the start rotation
// with a requested row that already carries a pinned result= and complete probe
// evidence. The first generation still binds that row, but once it is rejected
// the exclusion must override the repeat-play fast path and rotate to the
// sibling; before the fix the fast path returned the excluded row and rotation
// terminalled.
func TestVirtualStartRotationExcludesProbedRequestedRow(t *testing.T) {
	f := newDecodeRotationFixture(t, decodeRotationOptions{
		// One poisoned start: the decode verdict is terminal for candidate A
		// (the automatic pipeline does not rebuild rejected bytes), so rotation
		// substitutes the sibling on the first failure.
		candidateIDs:    []string{"A", "B"},
		decodeFailCalls: 1,
		requestedResult: "A",
		probeEvidence:   true,
	})

	code, response := f.start(t, f.request())
	if code != http.StatusCreated {
		t.Fatalf("start status = %d, want %d; body = %+v", code, http.StatusCreated, response)
	}
	if response.Terminal != nil || response.PlaybackPlan == nil {
		t.Fatalf("probed-row rotation did not commit a replacement plan: terminal=%+v plan=%#v", response.Terminal, response.PlaybackPlan)
	}
	if got := f.sessionVirtualURI(t, response.SessionID); got != decodeRotationCandidateURI("B") {
		t.Fatalf("effective virtual uri = %q, want the rotated candidate B", got)
	}
}

// TestVirtualAutoStartSkipsFailedProbedRow proves a fresh auto start does not
// serve a requested row stamped failed_at even when it has complete probe
// evidence and would otherwise take the repeat-play fast path.
func TestVirtualAutoStartSkipsFailedProbedRow(t *testing.T) {
	f := newDecodeRotationFixture(t, decodeRotationOptions{
		candidateIDs:    []string{"A", "B"},
		requestedResult: "A",
		probeEvidence:   true,
		requestedFailed: true,
	})

	code, response := f.start(t, f.request())
	if code != http.StatusCreated {
		t.Fatalf("start status = %d, want %d; body = %+v", code, http.StatusCreated, response)
	}
	if response.Terminal != nil || response.PlaybackPlan == nil {
		t.Fatalf("failed-row start did not select a healthy sibling: terminal=%+v plan=%#v", response.Terminal, response.PlaybackPlan)
	}
	if got := f.sessionVirtualURI(t, response.SessionID); got != decodeRotationCandidateURI("B") {
		t.Fatalf("effective virtual uri = %q, want the healthy candidate B, not the failed A", got)
	}
}

// TestVirtualAutoStartSkipsFailedRowWithinDeliveryGrace proves the optimistic
// delivery-grace fast path does not serve a failed row: a row that delivered
// recently but is now stamped failed must still be skipped.
func TestVirtualAutoStartSkipsFailedRowWithinDeliveryGrace(t *testing.T) {
	f := newDecodeRotationFixture(t, decodeRotationOptions{
		candidateIDs:       []string{"A", "B"},
		requestedResult:    "A",
		requestedFailed:    true,
		requestedDelivered: true,
	})

	code, response := f.start(t, f.request())
	if code != http.StatusCreated {
		t.Fatalf("start status = %d, want %d; body = %+v", code, http.StatusCreated, response)
	}
	if response.Terminal != nil || response.PlaybackPlan == nil {
		t.Fatalf("grace failed-row start did not select a healthy sibling: terminal=%+v plan=%#v", response.Terminal, response.PlaybackPlan)
	}
	if got := f.sessionVirtualURI(t, response.SessionID); got != decodeRotationCandidateURI("B") {
		t.Fatalf("effective virtual uri = %q, want the healthy candidate B, not the failed A", got)
	}
}

// TestVirtualExplicitStartDecodeRejectionTerminals proves an explicit version
// pin is never substituted: a decode rejection terminalls with
// source_decode_failed plus the version-list hint, and the rejection is still
// stamped on the candidate.
func TestVirtualExplicitStartDecodeRejectionTerminals(t *testing.T) {
	f := newDecodeRotationFixture(t, decodeRotationOptions{
		// One poisoned start: the decode verdict is terminal for the pinned
		// candidate, so it terminates with source_decode_failed rather than
		// rebuilding rejected bytes.
		candidateIDs:    []string{"A", "B"},
		decodeFailCalls: 1,
	})
	start := f.request()
	start.FileSelection = playback.FileSelectionExplicitV3

	code, response := f.start(t, start)
	if code != http.StatusCreated {
		t.Fatalf("start status = %d, want %d; body = %+v", code, http.StatusCreated, response)
	}
	if response.PlaybackPlan != nil {
		t.Fatalf("explicit pin was substituted: %#v", response.PlaybackPlan)
	}
	if response.Terminal == nil || response.Terminal.Reason != sourceDecodeFailedReasonV3 {
		t.Fatalf("terminal = %#v, want reason %q", response.Terminal, sourceDecodeFailedReasonV3)
	}
	if !strings.Contains(response.Terminal.Message, "version list") {
		t.Fatalf("explicit decode terminal missing the alternate hint: %q", response.Terminal.Message)
	}
	for _, excluded := range f.recordedExclusions() {
		if len(excluded) > 0 {
			t.Fatalf("explicit pin excluded candidates %v; an explicit pin is never substituted", excluded)
		}
	}
	f.waitForMarked(t)
}

// TestVirtualDecodeRotationBoundedExhaustion proves rotation is bounded by the
// configured failover attempts and excludes every rejected id, so a provider
// that only offers bad releases cannot loop forever. A decoder-confirmed
// rejection is terminal for its candidate, so it is not repeated by the
// automatic hardware->software pipeline: each candidate costs exactly one
// transcode start, and the bound is the candidate count.
func TestVirtualDecodeRotationBoundedExhaustion(t *testing.T) {
	f := newDecodeRotationFixture(t, decodeRotationOptions{candidateIDs: []string{"A", "B", "C"}, decodeFailCalls: 3})

	code, response := f.start(t, f.request())
	if code != http.StatusCreated {
		t.Fatalf("start status = %d, want %d; body = %+v", code, http.StatusCreated, response)
	}
	if response.Terminal == nil || response.Terminal.Reason != sourceDecodeFailedReasonV3 {
		t.Fatalf("exhausted rotation terminal = %#v, want reason %q", response.Terminal, sourceDecodeFailedReasonV3)
	}
	if response.Terminal.Retryable {
		t.Fatalf("exhausted decode terminal is retryable: %#v", response.Terminal)
	}
	exclusions := f.recordedExclusions()
	if !containsExclusion(exclusions, "A") || !containsExclusion(exclusions, "A", "B") {
		t.Fatalf("rotation did not accumulate exclusions: %v", exclusions)
	}
	if calls := atomic.LoadInt32(&f.transcodeCalls); calls > 3 {
		t.Fatalf("transcode started %d times, want at most 3 candidates times 1 start", calls)
	}
}

// TestVirtualReplanDecodeErrorRotates drives a decode_error failure_recovery on
// a session whose live generation actually rejected the source. The replan must
// exclude the session-bound candidate and commit the next release, without
// demoting the HLS delivery and without spawning a software decode.
func TestVirtualReplanDecodeErrorRotates(t *testing.T) {
	f := newDecodeRotationFixture(t, decodeRotationOptions{
		candidateIDs:        []string{"A", "B"},
		decodeFailCalls:     1,
		rejectAfterManifest: true,
	})
	start := f.request()

	code, started := f.start(t, start)
	if code != http.StatusCreated || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d response=%+v", code, started)
	}
	defer f.handler.tm.CloseTranscodeSession(started.SessionID, "")
	if got := f.sessionVirtualURI(t, started.SessionID); got != decodeRotationCandidateURI("A") {
		t.Fatalf("start candidate = %q, want A", got)
	}
	f.waitForSourceRejected(t, started.SessionID)

	recovered := postPlaybackReplanV3(t, f.handler, started.SessionID, decodeRotationReplanRequest(start, started.PlaybackPlan, "decode-rotation-replan-0001"))
	if recovered.Terminal != nil || recovered.PlaybackPlan == nil {
		t.Fatalf("decode replan terminal=%+v", recovered.Terminal)
	}
	if recovered.PlaybackPlan.Delivery != playback.DeliveryTranscodeHLSV3 {
		t.Fatalf("rotated plan delivery = %s, want HLS", recovered.PlaybackPlan.Delivery)
	}
	if !containsExclusion(f.recordedExclusions(), "A") {
		t.Fatalf("replan did not exclude the rejected session candidate; exclusions = %v", f.recordedExclusions())
	}
	if got := f.sessionVirtualURI(t, started.SessionID); got != decodeRotationCandidateURI("B") {
		t.Fatalf("replanned effective virtual uri = %q, want B", got)
	}
	if got := f.softwareSpawnCount(); got != 0 {
		t.Fatalf("allow-policy rotation spawned %d software-decode transcodes, want 0", got)
	}
	record, err := f.handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	caps := record.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3]
	if !caps.Enabled || caps.FailureReason == demoteDeliveryReasonV3 {
		t.Fatalf("HLS delivery was demoted while rotation was pending: %+v", caps)
	}
}

// TestVirtualDecodeRotationFailureKeepsSessionBinding proves the rotation's
// replacement candidate is not written to the live session before the durable
// session replacement commits: when the rotated transport fails to start, the
// session still names the previous candidate, so later resolution and the
// serve-side recovery never disagree with the committed plan/record.
func TestVirtualDecodeRotationFailureKeepsSessionBinding(t *testing.T) {
	f := newDecodeRotationFixture(t, decodeRotationOptions{
		candidateIDs:        []string{"A", "B"},
		decodeFailCalls:     1,
		rejectAfterManifest: true,
		startFailureCalls:   5,
	})
	start := f.request()

	code, started := f.start(t, start)
	if code != http.StatusCreated || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d response=%+v", code, started)
	}
	defer f.handler.tm.CloseTranscodeSession(started.SessionID, "")
	f.waitForSourceRejected(t, started.SessionID)

	recovered := postPlaybackReplanV3(t, f.handler, started.SessionID, decodeRotationReplanRequest(start, started.PlaybackPlan, "rotation-failure-replan-0001"))
	if recovered.Terminal == nil {
		t.Fatalf("failed rotation unexpectedly committed a plan: %#v", recovered.PlaybackPlan)
	}
	if got := f.sessionVirtualURI(t, started.SessionID); got != decodeRotationCandidateURI("A") {
		t.Fatalf("failed rotation rebound the live session to %q, want the unchanged candidate A", got)
	}
}

// TestVirtualDecodeRotationRequiresServerEvidence proves a client-supplied
// decode_error on a healthy session is not treated as a decode rejection: the
// server's own decoder verdict is required, so the decode reason and the
// rotation-specific retirement are not applied. (The generic failure-recovery
// candidate exclusion is unchanged and is not a decode classification.)
func TestVirtualDecodeRotationRequiresServerEvidence(t *testing.T) {
	f := newDecodeRotationFixture(t, decodeRotationOptions{candidateIDs: []string{"A"}})
	start := f.request()

	code, started := f.start(t, start)
	if code != http.StatusCreated || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d response=%+v", code, started)
	}
	defer f.handler.tm.CloseTranscodeSession(started.SessionID, "")
	if live := f.handler.tm.GetTranscodeSession(started.SessionID); live == nil || live.IsSourceRejected() {
		t.Fatal("fixture precondition: live generation must be healthy")
	}

	recovered := postPlaybackReplanV3(t, f.handler, started.SessionID, decodeRotationReplanRequest(start, started.PlaybackPlan, "healthy-decode-replan-0001"))
	if recovered.Terminal == nil {
		t.Fatalf("healthy single-candidate decode replan unexpectedly planned a route: %#v", recovered.PlaybackPlan)
	}
	if recovered.Terminal.Reason == sourceDecodeFailedReasonV3 {
		t.Fatalf("healthy session was retagged as a decode rejection: %#v", recovered.Terminal)
	}
}

// TestVirtualDecodeRotationGPUOnlyRetiresOnNoAlternate proves gpu_only never
// spawns a software decode and, when no sibling candidate exists, the delivery
// is retired and the replan terminalls with source_decode_failed.
func TestVirtualDecodeRotationGPUOnlyRetiresOnNoAlternate(t *testing.T) {
	f := newDecodeRotationFixture(t, decodeRotationOptions{
		candidateIDs:        []string{"A"},
		softwareFallback:    "gpu_only",
		decodeFailCalls:     1,
		rejectAfterManifest: true,
	})
	start := f.request()

	code, started := f.start(t, start)
	if code != http.StatusCreated || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d response=%+v", code, started)
	}
	defer f.handler.tm.CloseTranscodeSession(started.SessionID, "")
	f.waitForSourceRejected(t, started.SessionID)

	recovered := postPlaybackReplanV3(t, f.handler, started.SessionID, decodeRotationReplanRequest(start, started.PlaybackPlan, "gpu-only-decode-replan-0001"))
	if recovered.Terminal == nil || recovered.Terminal.Reason != sourceDecodeFailedReasonV3 {
		t.Fatalf("gpu_only no-alternate terminal = %#v, want %q", recovered.Terminal, sourceDecodeFailedReasonV3)
	}
	if got := f.softwareSpawnCount(); got != 0 {
		t.Fatalf("gpu_only spawned %d software-decode transcodes, want 0", got)
	}
	record, err := f.handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	caps := record.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3]
	if caps.Enabled || caps.FailureReason != demoteDeliveryReasonV3 {
		t.Fatalf("gpu_only kept the exhausted HLS delivery eligible: %+v", caps)
	}
}

// TestVirtualDecodeRotationReplanIsIdempotent proves replaying the same
// replan_request_id returns the first response without rotating again.
func TestVirtualDecodeRotationReplanIsIdempotent(t *testing.T) {
	f := newDecodeRotationFixture(t, decodeRotationOptions{
		candidateIDs:        []string{"A", "B"},
		decodeFailCalls:     1,
		rejectAfterManifest: true,
	})
	start := f.request()

	code, started := f.start(t, start)
	if code != http.StatusCreated || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d response=%+v", code, started)
	}
	defer f.handler.tm.CloseTranscodeSession(started.SessionID, "")
	f.waitForSourceRejected(t, started.SessionID)

	recovery := decodeRotationReplanRequest(start, started.PlaybackPlan, "idempotent-decode-replan-0001")
	first := postPlaybackReplanV3(t, f.handler, started.SessionID, recovery)
	if first.PlaybackPlan == nil {
		t.Fatalf("first rotation replan returned a terminal: %#v", first.Terminal)
	}
	firstResolves := len(f.recordedExclusions())
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}

	// The replay must be byte-identical: replan idempotency keys on the request
	// id plus the body digest, and a changed body is a different request.
	second := postPlaybackReplanV3(t, f.handler, started.SessionID, recovery)
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("idempotent replay diverged:\n first=%s\nsecond=%s", firstJSON, secondJSON)
	}
	if got := len(f.recordedExclusions()); got != firstResolves {
		t.Fatalf("idempotent replay re-ran rotation: resolves %d -> %d", firstResolves, got)
	}
}

// writePlaybackTestFFmpegDelayedDecodeFailure emits a ready manifest first
// and only then the decoder storm, so a start commits before any verdict
// exists and the rejection lands on the committed generation. Starts that
// fail fast on the verdict (revoked generations never produce a manifest)
// would terminal instead of committing, which is a different path.
func writePlaybackTestFFmpegDelayedDecodeFailure(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg-delayed-decode-failure.sh")
	script := "#!/bin/sh\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"case \"$last\" in\n" +
		"  *.m3u8) out=\"$(dirname \"$last\")\"; mkdir -p \"$out\"; " +
		"printf '\\000\\000\\000\\010ftyp\\000\\000\\000\\010moov' > \"$out/init.mp4\"; printf x > \"$out/seg_0.m4s\"; " +
		"printf x > \"$out/seg_1.m4s\"; printf x > \"$out/seg_2.m4s\"; " +
		"printf '#EXTM3U\\n#EXT-X-VERSION:7\\n#EXT-X-TARGETDURATION:2\\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\\n#EXT-X-MAP:URI=\"init.mp4\"\\n" +
		"#EXTINF:2.0,\\nseg_0.m4s\\n#EXTINF:2.0,\\nseg_1.m4s\\n" +
		"#EXTINF:2.0,\\nseg_2.m4s\\n' > \"$last\" ;;\n" +
		"esac\n" +
		"sleep 2\n" +
		"i=0\n" +
		"while [ $i -lt 20 ]; do echo \"[hevc @ 0x1] Error submitting packet to decoder: Invalid data found when processing input\" >&2; i=$((i+1)); done\n" +
		"sleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

// TestTranscodeManifestDoesNotRotateAfterSourceRejected proves the manifest
// route keeps answering the permanent decode verdict for the generation already
// on screen and never swaps the live transcode session for a different one.
func TestTranscodeManifestDoesNotRotateAfterSourceRejected(t *testing.T) {
	handler, _ := hardwareDecodeFailureHandler(t)
	// Delay the storm past the commit: the start must observe a healthy
	// generation (manifest ready, no verdict) so the rejection under test
	// lands on the committed generation the manifest route serves. An
	// immediate storm would fail the start itself before anything commits.
	prevConfig := handler.PlaybackConfig
	delayedFFmpeg := writePlaybackTestFFmpegDelayedDecodeFailure(t)
	handler.PlaybackConfig = func() config.PlaybackConfig {
		cfg := prevConfig()
		cfg.FFmpegPath = delayedFFmpeg
		return cfg
	}

	start := v3HandlerStartRequest()
	start.QualityPreference = "auto"
	start.ClientFeatures = append(start.ClientFeatures, playback.FeatureHeaderAuthenticatedMediaV3)
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		Containers:        []string{"hls"},
		VideoCodecs:       []string{"h264"},
		AudioDecodeCodecs: []string{"aac"},
	}
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d body=%s", rr.Code, rr.Body.String())
	}
	defer handler.tm.CloseTranscodeSession(started.SessionID, "")

	live := handler.tm.GetTranscodeSession(started.SessionID)
	if live == nil {
		t.Fatal("start registered no live transcode session")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !live.IsSourceRejected() {
		if time.Now().After(deadline) {
			t.Fatal("the decoder rejection was never observed on the live session")
		}
		time.Sleep(5 * time.Millisecond)
	}

	recorder := httptest.NewRecorder()
	handler.HandleGetTranscodeManifest(recorder, playbackTestRequest(
		http.MethodGet,
		"/api/v1/playback/transcode/"+started.SessionID+"/master.m3u8",
		nil,
		map[string]string{"session_id": started.SessionID},
	))
	assertDecodeFailureResponse(t, recorder)
	if after := handler.tm.GetTranscodeSession(started.SessionID); after != live {
		t.Fatal("manifest route swapped the transcode generation after a decode rejection")
	}
}

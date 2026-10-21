package transcodenode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestHEVCNodeReportsCPUEncoderWithHardwareToneMapping(t *testing.T) {
	for _, reconstruct := range []bool{false, true} {
		name := "start"
		if reconstruct {
			name = "reconstruct"
		}
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t)
			tracker := newBlockingSessionTracker()
			server.tracker = tracker
			defer close(tracker.trackRelease)
			ffmpeg := useFakeFFmpeg(t, server, "#!/bin/sh\ncase \" $* \" in\n*' -c:v hevc_vaapi '*) exit 1 ;;\n*' -f null '*) exit 0 ;;\nesac\nexec sleep 30\n")
			server.watcher.Config().Playback.HWAccel = tonemap.BackendVAAPI
			server.resolveToneMapRecipeFn = func(_ context.Context, opts *playback.TranscodeOpts) error {
				opts.HWAccel = tonemap.BackendVAAPI
				opts.ToneMapFilter = tonemap.HardwareFilterVAAPI
				return nil
			}
			input, size := writeNodeSource(t, "source")
			writeNodeToneMapFFprobe(t, ffmpeg, nodeToneMapTrack())
			revision := nodeToneMapRevision(size)
			request := TranscodeStartRequest{
				SessionID: "encoder-report", InputPath: input, TargetCodecVideo: "hevc", TargetCodecAudio: "aac", SegmentDuration: 2,
				HWAccel: tonemap.BackendVAAPI, ToneMapPolicy: tonemap.PolicyHardwareOnly, ToneMapMode: tonemap.ModeHardware,
				ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapSourceRevision: revision,
			}
			var session *playback.TranscodeSession
			if reconstruct {
				card := playback.NewRecipeCard(7, "profile", 42, "", playback.TranscodeOpts{
					SessionID: request.SessionID, InputPath: input, TargetCodecVideo: "hevc", TargetCodecAudio: "aac", SegmentDuration: 2,
					HWAccel: tonemap.BackendVAAPI, ToneMapPolicy: request.ToneMapPolicy, ToneMapMode: request.ToneMapMode,
					ToneMapSourceKind: request.ToneMapSourceKind, ToneMapRecipeVersion: request.ToneMapRecipeVersion, ToneMapSourceRevision: revision,
					FastStart: true,
				})
				session, _ = server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), request.SessionID, -1, card)
				if session == nil {
					t.Fatal("reconstruction failed")
				}
				if session.Opts().FastStart {
					t.Fatal("reconstructed node runtime retained fresh-start manifest tuning")
				}
			} else {
				recorder := postStartRequest(t, server, request)
				if recorder.Code != http.StatusAccepted {
					t.Fatalf("start failed: %d %s", recorder.Code, recorder.Body.String())
				}
				var response TranscodeStartResponse
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.HWAccel != tonemap.BackendVAAPI || response.EncoderHWAccel != playback.HWAccelNone || response.ToneMapMode != tonemap.ModeHardware {
					t.Fatalf("response conflated encoder and tone-map backend: %#v", response)
				}
				server.mu.RLock()
				session = server.sessions[request.SessionID]
				server.mu.RUnlock()
			}
			if session == nil {
				t.Fatal("session was not registered")
			}
			defer func() { _ = session.CloseProcess() }()
			select {
			case <-tracker.trackStarted:
			case <-time.After(3 * time.Second):
				t.Fatal("session tracking did not start")
			}
			tracker.mu.Lock()
			tracked := tracker.tracked
			tracker.mu.Unlock()
			if tracked.HWAccel != playback.HWAccelNone || tracked.ToneMapMode != string(tonemap.ModeHardware) {
				t.Fatalf("node activity misreported encoder: %#v", tracked)
			}
		})
	}
}

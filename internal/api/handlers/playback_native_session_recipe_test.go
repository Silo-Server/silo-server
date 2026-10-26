package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// Exercise the public start and manifest handlers on opposite sides of an API
// restart. The original executor is also gone: the replacement must receive the
// committed native recipe, rather than silently starting ordinary fMP4 HLS.
func TestHeaderAuthenticatedNativeRecipeSurvivesRestart(t *testing.T) {
	for _, recipe := range []string{"mpegts", "separate_audio", "dv81"} {
		t.Run(recipe, func(t *testing.T) {
			for _, fence := range []string{"owner", "another account", "another profile", "stopped", "superseded plan"} {
				t.Run(fence, func(t *testing.T) {
					f, starts := newNativeHeaderAuthRestartFixtureV3(t, recipe)
					var initial transcodenode.TranscodeStartRequest
					select {
					case initial = <-starts:
					default:
						t.Fatal("native start did not reach the executor")
					}
					assertNativeRestartRequestV3(t, recipe, initial)
					ctx := newAuthorizedPlaybackContext()
					switch fence {
					case "another account":
						ctx = playbackCallerContextV3(2, "profile-1")
					case "another profile":
						ctx = playbackCallerContextV3(1, "profile-2")
					case "stopped":
						if _, _, err := f.attempts.StopAttempt(context.Background(), f.started.SessionID, "6f9619ff-8b86-4d01-b42d-00cf4fc964ff", nil); err != nil {
							t.Fatal(err)
						}
					case "superseded plan":
						card := f.recipes.cards[f.started.SessionID]
						card.PlanID = "plan:superseded"
						f.recipes.cards[f.started.SessionID] = card
					}
					f.nodes[f.startedOn].Close()
					restarted := f.restart()
					rr := httptest.NewRecorder()
					restarted.HandleGetTranscodeManifest(rr, nativeTranscodeServeRequestV3(ctx, f.started.SessionID, ""))
					if fence != "owner" {
						if rr.Code != http.StatusNotFound {
							t.Fatalf("refused recovery = %d (%s), want 404", rr.Code, rr.Body.String())
						}
						if _, err := restarted.sessionMgr.GetSession(f.started.SessionID); err == nil {
							t.Fatal("refused recovery registered a session")
						}
						select {
						case <-starts:
							t.Fatal("refused recovery started an executor")
						default:
						}
						return
					}
					if rr.Code != http.StatusOK {
						t.Fatalf("manifest after restart = %d (%s)", rr.Code, rr.Body.String())
					}
					select {
					case recovered := <-starts:
						assertNativeRestartRequestV3(t, recipe, recovered)
						if recovered.HLSCodecs != initial.HLSCodecs || recovered.HLSVideoRange != initial.HLSVideoRange || recovered.AudioTrackIndex != initial.AudioTrackIndex {
							t.Fatalf("recovery changed the committed rendition: start=%+v recovery=%+v", initial, recovered)
						}
					default:
						t.Fatal("recovery did not start a replacement executor")
					}
					session, err := restarted.sessionMgr.GetSession(f.started.SessionID)
					if err != nil || session.TranscodeNodeURL != f.other || !session.RequireMediaAuthorization {
						t.Fatalf("recovered session lost executor or header authorization: session=%+v err=%v", session, err)
					}
					rr = httptest.NewRecorder()
					restarted.HandleGetTranscodeSegment(rr, nativeTranscodeServeRequestV3(context.Background(), f.started.SessionID, "seg_00001.ts"))
					if rr.Code != http.StatusUnauthorized {
						t.Fatalf("unauthenticated recovered segment = %d, want 401", rr.Code)
					}
				})
			}
		})
	}
}

// The native recipes are intentionally unavailable on the frozen v1 surface.
// Match the API v2 router's context marker as well as its public URL.
func nativeTranscodeServeRequestV3(ctx context.Context, sessionID, segment string) *http.Request {
	request := transcodeServeRequestV3(WithNativeAPIV2(ctx), sessionID, segment)
	request.URL.Path = strings.Replace(request.URL.Path, "/api/v1/", "/api/v2/", 1)
	return request
}

func assertNativeRestartRequestV3(t *testing.T, recipe string, request transcodenode.TranscodeStartRequest) {
	t.Helper()
	if request.CopyVideoMPEGTS != (recipe == "mpegts") || request.SeparateAudioHLS != (recipe != "mpegts") || request.DV7ToDV81 != (recipe == "dv81") || request.TargetCodecVideo != "copy" || request.AudioTrackIndex != 1 {
		t.Fatalf("%s executor received a different native recipe: %+v", recipe, request)
	}
}

func newNativeHeaderAuthRestartFixtureV3(t *testing.T, recipe string) (headerAuthRestartFixtureV3, <-chan transcodenode.TranscodeStartRequest) {
	t.Helper()
	transformations := []playback.TransformationV3{
		{Name: playback.TransformationHLSSeparateAudioV3, Executor: playback.ExecutorServerV3, RecipeVersion: "1"},
		{Name: playback.TransformationServerDV7DV81V3, Executor: playback.ExecutorServerV3, RecipeVersion: "1"},
	}
	starts := make(chan transcodenode.TranscodeStartRequest, 8)
	newNode := func() *httptest.Server {
		node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/hw-capabilities":
				writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: transformations})
			case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
				var request transcodenode.TranscodeStartRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode executor start: %v", err)
				}
				starts <- request
				writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: request.SessionID, Status: "started", CopyVideoMPEGTS: request.CopyVideoMPEGTS, CopyFMP4RecipeVersion: request.CopyFMP4RecipeVersion, AudioRecipeVersion: request.AudioRecipeVersion, ThrottleSeconds: request.ThrottleSeconds, KeyframePlaylist: request.KeyframePlaylist})
			case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/transcode/"):
				_, _ = io.WriteString(w, "#EXTM3U\n#EXTINF:6,\nseg_00001.ts\n")
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		}))
		t.Cleanup(node.Close)
		return node
	}
	first, second := newNode(), newNode()
	workers := nodepool.NewTranscodePool()
	workers.SetNodes([]*nodepool.Node{{ID: 1, URL: first.URL, Enabled: true, Healthy: true}, {ID: 2, URL: second.URL, Enabled: true, Healthy: true}})
	planner := nodepool.NewPlanner(nodepool.NewProxyPool(), workers)
	file := v3HandlerFixtureFile(t)
	file.AudioTracks = append(file.AudioTracks, file.AudioTracks[0])
	file.AudioTracks[1].Default = false
	file.Container, file.CodecVideo = "mkv", "hevc"
	video := &file.VideoTracks[0]
	video.Codec, video.Profile, video.Level, video.BitDepth = "hevc", "Main 10", 123, 10
	video.VideoRange, video.VideoRangeType = "HDR", "HDR10"
	if recipe == "dv81" {
		video.DVProfile, video.DVLevel, video.DVBLCompatID = 7, 6, 6
		video.DVConfigPresent, video.DVBLCompatIDPresent, video.DVBLPresent, video.DVRPUPresent = true, true, true, true
		video.VideoRange, video.VideoRangeType = "DolbyVision", "DOVIWithEL"
	}
	ffmpeg, output := writePlaybackTestFFmpeg(t), t.TempDir()
	f := headerAuthRestartFixtureV3{nodes: map[string]*httptest.Server{first.URL: first, second.URL: second}, attempts: playback.NewMemoryPlanStoreV3(), recipes: &recordingRecipeCardStoreV3{}}
	nodeRecipes := &lockedRecipeCardStoreV3{}
	f.configure = func(h *PlaybackHandler) {
		h.fileResolver = testPlaybackFileResolver{file: file}
		h.JWTSecret = "test-secret"
		h.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "true", "allow_4k_transcode": "true"}}
		h.ItemAccess = allowAllPlaybackItemAccess{}
		h.PlaybackConfig = playbackTestConfig(ffmpeg, output)
		h.NodePlanner, h.PlanStoreV3, h.SessionRecipeStore, h.NodeRecipeStore = planner, f.attempts, f.recipes, nodeRecipes
		requireWorkerRoutingV3(h)
		h.v3RegistryTranscodeEnabled = h.playbackConfig().TranscodeEnabled
		h.v3Registry = playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{Name: playback.TransformationHLSSeparateAudioV3, RecipeVersion: "1", Available: true}, {Name: playback.TransformationServerDV7DV81V3, RecipeVersion: "1", Available: true}})
	}
	h := f.restart()
	start := v3HandlerStartRequest()
	audioIndex := 1
	start.AudioTrackIndex = &audioIndex
	feature := playback.FeatureHLSCopyMPEGTSV3
	if recipe != "mpegts" {
		feature = playback.ClientNativeHLSSeparateAudioV3
	}
	start.ClientFeatures = append(start.ClientFeatures, playback.FeatureHeaderAuthenticatedMediaV3, feature)
	hdr := &playback.HDRCapabilitiesV3{HDR10: true}
	if recipe == "dv81" {
		start.ClientFeatures = append(start.ClientFeatures, playback.FeatureServerDV7ToDV81V3)
		hdr.DolbyVisionProfiles = []int{8}
		hdr.DolbyVisionProfileLevels = []playback.DolbyVisionProfileCapabilityV3{{Profile: 8, MaxLevel: 6, BLCompatibilityIDs: []int{1}}}
	}
	start.Capabilities.CodecsVideo, start.Capabilities.CodecsVideoHardware = []string{"hevc"}, []string{"hevc"}
	start.Capabilities.VideoDecode = []playback.VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{123}, BitDepths: []int{10}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20000, Hardware: true}}
	start.Capabilities.HDRDetails, start.ClientPlaybackContext.Output.HDRDetails = hdr, hdr
	delete(start.ClientPlaybackContext.Deliveries, playback.DeliveryClassOriginalHTTPV3)
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true, VideoCodecs: []string{"hevc"}, AudioDecodeCodecs: []string{"aac"}, HDRDetails: hdr, Features: []string{"native_hls_playback_v1", feature}}
	rr := httptest.NewRecorder()
	h.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(WithNativeAPIV2(newAuthorizedPlaybackContext())))
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &f.started) != nil || f.started.PlaybackPlan == nil {
		t.Fatalf("native start = %d (%s)", rr.Code, rr.Body.String())
	}
	if strings.Contains(f.started.PlaybackPlan.Stream.URL, streamTokenParam+"=") {
		t.Fatal("header-authenticated native plan published a stream token")
	}
	session, err := h.sessionMgr.GetSession(f.started.SessionID)
	if err != nil || session.TranscodeNodeURL == "" {
		t.Fatalf("native start did not register a remote session: session=%+v err=%v", session, err)
	}
	f.startedOn, f.other = session.TranscodeNodeURL, second.URL
	if f.startedOn == second.URL {
		f.other = first.URL
	}
	return f, starts
}

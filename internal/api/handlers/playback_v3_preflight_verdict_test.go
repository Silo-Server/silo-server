package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

func preflightRejectedTransportErrorV3() *transportErrorV3 {
	return toneMapExecutionTransportErrorV3(
		fmt.Errorf("%w: decoded frame metadata does not match pq fallback", tonemap.ErrSourcePreflightRejected),
		"Failed to start the playback transport.",
	)
}

func ineligibleExecutorTransportErrorV3() *transportErrorV3 {
	return &transportErrorV3{
		reason: capabilityUnavailableReasonV3, message: "No available transcode executor can run the selected tone-map recipe.",
		retryable: true, cause: errors.New("executor lacks hardware pq tone mapping"), executorIneligible: true,
	}
}

func foldTransportErrorsV3(errs ...*transportErrorV3) *transportErrorV3 {
	var combined *transportErrorV3
	for _, err := range errs {
		combined = combineTransportErrorsV3(combined, err)
	}
	return combined
}

// TestCombineTransportErrorsV3IneligibleExecutorKeepsSourceVerdict covers
// #2266: a route skipped because its executor can't tone-map the source must
// not turn a final preflight rejection into a retryable, generic failure.
func TestCombineTransportErrorsV3IneligibleExecutorKeepsSourceVerdict(t *testing.T) {
	for name, got := range map[string]*transportErrorV3{
		"rejection then skip": combineTransportErrorsV3(preflightRejectedTransportErrorV3(), ineligibleExecutorTransportErrorV3()),
		"skip then rejection": combineTransportErrorsV3(ineligibleExecutorTransportErrorV3(), preflightRejectedTransportErrorV3()),
		// The reported start: two nodes reject the file, the API's executor
		// lacks hardware PQ tone mapping, the nodes reject it again, and the
		// API's software fallback rejects it too.
		"reported sequence": foldTransportErrorsV3(
			preflightRejectedTransportErrorV3(), preflightRejectedTransportErrorV3(), ineligibleExecutorTransportErrorV3(),
			preflightRejectedTransportErrorV3(), preflightRejectedTransportErrorV3(), preflightRejectedTransportErrorV3(),
		),
	} {
		t.Run(name, func(t *testing.T) {
			if got == nil || got.retryable || got.executorIneligible || got.reason != transcodeStartFailedReasonV3 || got.message != sourcePreflightRejectedMessageV3 {
				t.Fatalf("combined error = %#v, want the final preflight rejection", got)
			}
			if !errors.Is(got.cause, tonemap.ErrSourcePreflightRejected) || !strings.Contains(got.cause.Error(), "lacks hardware pq") {
				t.Fatalf("combined cause = %v, want both causes kept", got.cause)
			}
		})
	}

	onlySkipped := combineTransportErrorsV3(ineligibleExecutorTransportErrorV3(), ineligibleExecutorTransportErrorV3())
	if !onlySkipped.retryable || !onlySkipped.executorIneligible || onlySkipped.reason != capabilityUnavailableReasonV3 {
		t.Fatalf("only skipped routes = %#v, want a retryable capability failure", onlySkipped)
	}

	transient := toneMapExecutionTransportErrorV3(playback.ErrToneMapSourceValidationUnavailable, "Failed to start the playback transport.")
	if got := combineTransportErrorsV3(ineligibleExecutorTransportErrorV3(), transient); !got.retryable || got.reason != transcodeStartFailedReasonV3 {
		t.Fatalf("skip and transient failure = %#v, want the retryable transient failure", got)
	}

	// Two routes that both ran, one transient and one rejecting the file: a
	// retry may still work, so the result stays retryable and doesn't say it
	// won't, whichever ran last.
	for name, got := range map[string]*transportErrorV3{
		"transient then rejection": combineTransportErrorsV3(transient, preflightRejectedTransportErrorV3()),
		"rejection then transient": combineTransportErrorsV3(preflightRejectedTransportErrorV3(), transient),
	} {
		if !got.retryable || got.message == sourcePreflightRejectedMessageV3 || !errors.Is(got.cause, tonemap.ErrSourcePreflightRejected) {
			t.Fatalf("%s = %#v, want a retryable result without the final-verdict message", name, got)
		}
	}
}

// TestExhaustedRoutesErrorV3KeepsFinalPreflightRejection keeps a rejection
// final when the route loop runs out of attempts, and leaves every other
// exhausted loop retryable.
func TestExhaustedRoutesErrorV3KeepsFinalPreflightRejection(t *testing.T) {
	rejected := preflightRejectedTransportErrorV3()
	if got := exhaustedRoutesErrorV3(rejected); got != rejected {
		t.Fatalf("exhausted after a rejection = %#v, want the rejection", got)
	}
	transient := toneMapExecutionTransportErrorV3(playback.ErrToneMapSourceValidationUnavailable, "Failed to start the playback transport.")
	stale := toneMapExecutionTransportErrorV3(tonemap.ErrSourceRevisionChanged, "Failed to start the playback transport.")
	for name, lastErr := range map[string]*transportErrorV3{"none": nil, "transient": transient, "stale source": stale} {
		if got := exhaustedRoutesErrorV3(lastErr); got.reason != "route_preparation_failed" || !got.retryable {
			t.Fatalf("exhausted after %s = %#v, want a retryable route_preparation_failed", name, got)
		}
	}
}

// TestValidateLocalTransportCapabilitiesV3MarksOnlyDefiniteMismatchIneligible
// keeps a failed or slow capability read retryable in its own right.
func TestValidateLocalTransportCapabilitiesV3MarksOnlyDefiniteMismatchIneligible(t *testing.T) {
	toneMap := playback.TransformationV3{
		Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3,
		RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
	}
	result := playback.PlannerResultV3{
		Plan:        &playback.PlanV3{Transformations: []playback.TransformationV3{toneMap}},
		ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ,
	}
	for name, tc := range map[string]struct {
		probe            func(context.Context, string, string, string) (tonemap.Capabilities, error)
		registryProbeErr error
		wantIneligible   bool
	}{
		"executor lacks the source kind": {
			probe: func(context.Context, string, string, string) (tonemap.Capabilities, error) {
				return tonemap.Capabilities{{
					Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
					SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
				}}, nil
			},
			wantIneligible: true,
		},
		"capability probe timed out": {
			probe: func(context.Context, string, string, string) (tonemap.Capabilities, error) {
				return nil, context.DeadlineExceeded
			},
		},
		"transformation probe timed out": {
			probe: func(context.Context, string, string, string) (tonemap.Capabilities, error) {
				return tonemap.Capabilities{{
					Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
					SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
				}}, nil
			},
			registryProbeErr: context.DeadlineExceeded,
		},
	} {
		t.Run(name, func(t *testing.T) {
			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.PlaybackConfig = func() config.PlaybackConfig {
				return config.PlaybackConfig{HWAccel: playback.HWAccelNone, TranscodeEnabled: true}
			}
			if tc.registryProbeErr != nil {
				handler.v3RegistryProbe = func(context.Context, string, tonemap.Capabilities) (*playback.TransformationRegistryV3, error) {
					return playback.NewTransformationRegistryV3(nil), tc.registryProbeErr
				}
			} else {
				presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{
					Name: playback.TransformationHDRToSDRToneMapV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, Available: true,
				}}))
			}
			handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{
				config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
				config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
			}}
			handler.v3ToneMapProbe = tc.probe
			transportErr := handler.validateLocalTransportCapabilitiesV3(context.Background(), result)
			if transportErr == nil || !transportErr.retryable || transportErr.executorIneligible != tc.wantIneligible {
				t.Fatalf("validation error = %#v, want retryable with executorIneligible=%t", transportErr, tc.wantIneligible)
			}
		})
	}
}

// TestPrepareTransportV3IneligibleLocalExecutorKeepsPreflightRejection runs
// the route loop as reported in #2266: the API server's own executor can't
// tone-map the source and a transcode node rejects the file. The node's
// rejection must stay final.
func TestPrepareTransportV3IneligibleLocalExecutorKeepsPreflightRejection(t *testing.T) {
	required := []playback.TransformationV3{
		{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
		{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
		{Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
	}
	var nodeStarts atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: required, ToneMapCapabilities: tonemap.Capabilities{{
				Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
				SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
			}}})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			nodeStarts.Add(1)
			w.Header().Set(transcodenode.ToneMapExecutionErrorHeader, transcodenode.ToneMapSourcePreflightRejectedCode)
			w.WriteHeader(http.StatusUnprocessableEntity)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer node.Close()

	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{{URL: node.URL, Enabled: true, Healthy: true}})
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{HWAccel: playback.HWAccelNone, TranscodeEnabled: true, Routing: config.DefaultPlaybackRoutingPolicy()}
	}
	specs := make([]playback.TransformationSpecV3, 0, len(required))
	for _, transformation := range required {
		specs = append(specs, playback.TransformationSpecV3{Name: transformation.Name, RecipeVersion: transformation.RecipeVersion, Available: true})
	}
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3(specs))
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
	}}
	var localProbes atomic.Int32
	handler.v3ToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		localProbes.Add(1)
		return tonemap.Capabilities{{
			Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
			SourceKinds: []tonemap.SourceKind{tonemap.SourceHLG},
		}}, nil
	}
	file := stableToneMapTransportFileV3(t)
	result := playback.PlannerResultV3{
		Plan:       &playback.PlanV3{PlanID: "plan:ineligible-local", Delivery: playback.DeliveryTranscodeHLSV3, Transformations: required},
		PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetBitrateKbps: file.Bitrate,
		SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware, ToneMapSourceKind: tonemap.SourcePQ,
		ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapSourceRevision: tonemap.RevisionForFile(file),
	}

	_, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-ineligible-local", UserID: 7, ProfileID: "profile-1"},
		file,
		result,
		mediaAuthModeV3{},
	)
	if localProbes.Load() == 0 || nodeStarts.Load() == 0 {
		t.Fatalf("route loop did not reach both executors: local probes %d, node starts %d", localProbes.Load(), nodeStarts.Load())
	}
	if transportErr == nil || transportErr.retryable || transportErr.reason != transcodeStartFailedReasonV3 ||
		transportErr.message != sourcePreflightRejectedMessageV3 || !errors.Is(transportErr.cause, tonemap.ErrSourcePreflightRejected) {
		t.Fatalf("transport error = %#v, want the node's final preflight rejection", transportErr)
	}
}

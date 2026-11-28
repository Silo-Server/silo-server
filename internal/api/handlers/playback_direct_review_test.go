package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/directconn"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// A replan under an unreadable enrollment store, under a hard proxy_only
// policy, answers 503 dependency_unavailable as a start does, and stores no
// decision: the same replan request ID runs again once the store answers. A
// stored retryable terminal would replay for that ID after the outage ended.
func TestReplanUnderAnUnreadableStoreAnswers503AndRunsAgainOnRecovery(t *testing.T) {
	state := &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic"}}
	f := newDirectStartV3(t, state, 1)
	f.request.MediaTransport = playback.MediaTransportRegularV3
	f.handler.v3RouteNodeProbe = func(context.Context, string) bool { return true }
	proxyOnlyRoutingV3(f.handler)
	started, err := f.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), f.caller, f.request)
	if err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start: err=%v", err)
	}
	request := directReplanRequestV3(f, started.PlaybackPlan, playback.ReplanOperationQualityChangeV3, "replan-outage", playback.MediaTransportRegularV3)
	command := PlaybackReplanCommand{Request: request, Digest: "replan-outage-digest"}
	state.mu.Lock()
	state.err = errors.New("store unavailable")
	state.mu.Unlock()

	replanned, err := f.handler.ReplanPlaybackV2(newAuthorizedPlaybackContext(), f.caller, started.SessionID, command)

	if replanned.Terminal != nil {
		t.Fatalf("replan under an unreadable store stored terminal %q; want a 503 dependency_unavailable and no stored decision", replanned.Terminal.Reason)
	}
	operationErr, ok := errors.AsType[*PlaybackOperationError](err)
	if !ok || operationErr.Status != http.StatusServiceUnavailable || operationErr.Code != "dependency_unavailable" {
		t.Fatalf("replan under an unreadable store: err=%v; want a 503 dependency_unavailable", err)
	}

	state.mu.Lock()
	state.err = nil
	state.mu.Unlock()
	again, err := f.handler.ReplanPlaybackV2(newAuthorizedPlaybackContext(), f.caller, started.SessionID, command)
	if err != nil || again.PlaybackPlan == nil {
		reason := ""
		if again.Terminal != nil {
			reason = again.Terminal.Reason
		}
		t.Fatalf("the same replan after the store recovered: err=%v terminal=%q; want a plan", err, reason)
	}
}

// Under a hard proxy_only policy a device that takes no proxy origin has no
// legal route for anything, so the answer does not depend on what the media
// needs. A title that needs a conversion is refused with the same final
// routing_policy_unsatisfied as a title that plays as it is, not with the
// planner's retryable audio_conversion_unsupported; and when the enrollment
// store cannot be read, the answer is the 503.
func TestDirectOnlyDeviceUnderProxyOnlyIsRefusedWhateverTheMediaNeeds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   *directStateV3
		wantErr bool
	}{
		{name: "direct_only device", state: &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: directPolicyDirectOnlyV3}}},
		{name: "unreadable store", state: &directStateV3{err: errors.New("store unavailable")}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDirectStartV3(t, tc.state, 1)
			f.request.MediaTransport = playback.MediaTransportRegularV3
			proxyOnlyRoutingV3(f.handler)
			// The client plays AAC only, so this source's audio needs a conversion.
			file := f.handler.fileResolver.(mapPlaybackFileResolver).files[f.request.FileID]
			file.CodecAudio = "ac3"
			file.AudioTracks = []models.AudioTrack{{Codec: "ac3", Channels: 2, Layout: "stereo", Default: true}}

			response, err := f.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), f.caller, f.request)

			if tc.wantErr {
				operationErr, ok := errors.AsType[*PlaybackOperationError](err)
				if !ok || operationErr.Status != http.StatusServiceUnavailable || operationErr.Code != "dependency_unavailable" || response.Terminal != nil {
					t.Fatalf("start with an unreadable store: err=%v terminal=%+v; want a 503 dependency_unavailable and no stored decision", err, response.Terminal)
				}
				return
			}
			if err != nil || response.Terminal == nil {
				t.Fatalf("start: err=%v terminal=%+v; want a terminal refusal", err, response.Terminal)
			}
			if response.Terminal.Reason != "routing_policy_unsatisfied" || response.Terminal.Retryable {
				t.Fatalf("terminal %q retryable=%t; want routing_policy_unsatisfied, not retryable", response.Terminal.Reason, response.Terminal.Retryable)
			}
		})
	}
}

package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/directconn"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// The 503 a start answers when the enrollment store cannot be read carries the
// retry hint enrollment sends for the same outage (docs/architecture/api-contract.md:
// Retry-After accompanies applicable 503 responses).
func TestStartUnderAnUnreadableStoreAsksForARetry(t *testing.T) {
	f := newDirectStartV3(t, &directStateV3{err: errors.New("store unavailable")}, 1)
	f.request.MediaTransport = playback.MediaTransportRegularV3
	proxyOnlyRoutingV3(f.handler)

	_, err := f.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), f.caller, f.request)

	operationErr, ok := errors.AsType[*PlaybackOperationError](err)
	if !ok || operationErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("start: err=%v; want a 503", err)
	}
	if operationErr.RetryAfter != directconn.RetryAfterSeconds {
		t.Fatalf("503 Retry-After = %d, want %d", operationErr.RetryAfter, directconn.RetryAfterSeconds)
	}
}

// The replan's 503 for the same outage carries the same hint.
func TestReplanUnderAnUnreadableStoreAsksForARetry(t *testing.T) {
	state := &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic"}}
	f := newDirectStartV3(t, state, 1)
	f.request.MediaTransport = playback.MediaTransportRegularV3
	f.handler.v3RouteNodeProbe = func(context.Context, string) bool { return true }
	proxyOnlyRoutingV3(f.handler)
	started, err := f.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), f.caller, f.request)
	if err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start: err=%v", err)
	}
	request := directReplanRequestV3(f, started.PlaybackPlan, playback.ReplanOperationQualityChangeV3, "replan-retry-after", playback.MediaTransportRegularV3)
	state.mu.Lock()
	state.err = errors.New("store unavailable")
	state.mu.Unlock()

	_, err = f.handler.ReplanPlaybackV2(newAuthorizedPlaybackContext(), f.caller, started.SessionID, PlaybackReplanCommand{Request: request, Digest: "replan-retry-after-digest"})

	operationErr, ok := errors.AsType[*PlaybackOperationError](err)
	if !ok || operationErr.Status != http.StatusServiceUnavailable || operationErr.RetryAfter != directconn.RetryAfterSeconds {
		t.Fatalf("replan: err=%v; want a 503 with Retry-After %d", err, directconn.RetryAfterSeconds)
	}
}

// A confirmation key is cleared once, whatever the start's two host classes
// share: a regular start has one key for both, and an empty key clears nothing.
func TestClearKeysSkipsEmptyAndRepeatedKeys(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record string
		also   []string
		want   []string
	}{
		{name: "regular start", record: "regular", also: []string{"regular"}, want: []string{"regular"}},
		{name: "direct start", record: "direct", also: []string{"regular"}, want: []string{"direct", "regular"}},
		{name: "no key of the other class", record: "direct", also: []string{""}, want: []string{"direct"}},
		{name: "no key at all", record: "", also: nil, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := clearKeysV3(tc.record, tc.also...)
			if len(got) != len(tc.want) {
				t.Fatalf("clearKeysV3(%q, %q) = %q, want %q", tc.record, tc.also, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("clearKeysV3(%q, %q) = %q, want %q", tc.record, tc.also, got, tc.want)
				}
			}
		})
	}
}

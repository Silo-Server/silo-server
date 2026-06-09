package api

import (
	"context"
	"testing"
)

// AttachRequestRouter must tolerate absent dependencies (e.g. a build with the
// plugin service disabled) without panicking — fulfillment then degrades to the
// existing "no backend configured" failure rather than crashing the wiring.
func TestAttachRequestRouterNilDependenciesDoesNotPanic(t *testing.T) {
	AttachRequestRouter(nil, nil)
}

// A router adapter installed without a plugin service must return a controlled
// error rather than panic on the request path.
func TestPluginRequestRouterAdapterNilServiceReturnsError(t *testing.T) {
	adapter := PluginRequestRouterAdapter{}
	if _, err := adapter.RequestRouterClient(context.Background(), 1, "request_router.v1"); err == nil {
		t.Fatal("RequestRouterClient with nil Svc = nil error, want a controlled error")
	}
}

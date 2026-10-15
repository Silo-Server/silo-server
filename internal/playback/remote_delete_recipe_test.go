package playback

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
)

// orderedStop records the core's recipe deletion and the node's DELETE in the
// order they happen.
type orderedStop struct {
	mu        sync.Mutex
	events    []string
	deleteErr error
}

func (o *orderedStop) record(event string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

func (o *orderedStop) recorded() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

func (o *orderedStop) Delete(_ context.Context, transportID string) error {
	o.record("recipe " + transportID)
	return o.deleteErr
}

func newOrderedStopNode(t *testing.T, stop *orderedStop, status int) *httptest.Server {
	t.Helper()
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stop.record(r.Method + " " + r.URL.Path)
		w.WriteHeader(status)
	}))
	t.Cleanup(node.Close)
	return node
}

// Ending a transport is the core's decision: the recipe a node would rebuild
// it from goes before the node is told to stop, whatever the node answers.
func TestRemoteStopDeletesTheRecipeBeforeTheNodeDelete(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotFound, http.StatusServiceUnavailable} {
		stop := &orderedStop{}
		node := newOrderedStopNode(t, stop, status)
		manager := NewTranscodeManager()
		manager.NodeKeysFn = func() nodeauth.Keyring { return nodeauthtest.SharedSecret("stop-secret") }
		manager.NodeRecipesFn = func() NodeRecipeDeleter { return stop }

		manager.StopRemoteTranscode("transport-1", node.URL)
		want := []string{"recipe transport-1", "DELETE /transcode/transport-1"}
		if got := stop.recorded(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("node status %d: events = %v, want %v", status, got, want)
		}
	}
}

// A failed recipe deletion fails the stop, as the node's own 503 did when it
// deleted the recipe: the stop is not durable. The node still stops its
// process.
func TestRemoteStopSurfacesAFailedRecipeDeletion(t *testing.T) {
	stop := &orderedStop{deleteErr: errors.New("redis is down")}
	node := newOrderedStopNode(t, stop, http.StatusNoContent)
	manager := NewTranscodeManager()
	manager.NodeKeysFn = func() nodeauth.Keyring { return nodeauthtest.SharedSecret("stop-secret") }
	manager.NodeRecipesFn = func() NodeRecipeDeleter { return stop }

	err := manager.stopRemoteTranscode(t.Context(), "transport-2", node.URL)
	if err == nil || !errors.Is(err, stop.deleteErr) {
		t.Fatalf("stop = %v, want the recipe deletion failure", err)
	}
	if got := stop.recorded(); len(got) != 2 || got[1] != "DELETE /transcode/transport-2" {
		t.Fatalf("events = %v, want the node told to stop too", got)
	}
}

// A synchronous cancellation leaves the recipe to its caller: a user stop
// deletes it first, and a rollback that cannot cancel keeps the live route's
// authority for the retry.
func TestRemoteCancelLeavesTheRecipeToItsCaller(t *testing.T) {
	stop := &orderedStop{}
	node := newOrderedStopNode(t, stop, http.StatusServiceUnavailable)
	manager := NewTranscodeManager()
	manager.NodeKeysFn = func() nodeauth.Keyring { return nodeauthtest.SharedSecret("stop-secret") }
	manager.NodeRecipesFn = func() NodeRecipeDeleter { return stop }

	if err := manager.CancelRemoteTranscode(t.Context(), "transport-3", node.URL); err == nil {
		t.Fatal("cancel succeeded despite the node refusing it")
	}
	if got := stop.recorded(); len(got) != 1 || got[0] != "DELETE /transcode/transport-3" {
		t.Fatalf("events = %v, want only the node DELETE", got)
	}
}

// Without a node there is nothing to stop and no node recipe to delete.
func TestLocalStopDeletesNoNodeRecipe(t *testing.T) {
	stop := &orderedStop{}
	manager := NewTranscodeManager()
	manager.NodeRecipesFn = func() NodeRecipeDeleter { return stop }
	manager.StopRemoteTranscode("local-1", "")
	if got := stop.recorded(); len(got) != 0 {
		t.Fatalf("events = %v", got)
	}
}

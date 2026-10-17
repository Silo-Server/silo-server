package workercontrol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestLateRenewalFailureDoesNotRevokeReadyArtifact(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-resume:
			w.WriteHeader(http.StatusForbidden)
		case <-r.Context().Done():
		}
	}))
	defer core.Close()
	defer release()
	r := raceRuntime(t)
	r.options.Binding.CoreURL = core.URL
	if err := r.authority.SetNonce("test-nonce"); err != nil {
		t.Fatal(err)
	}
	if err := r.authority.Apply(raceSnapshot(r, 1, 4, 2000), time.Now()); err != nil {
		t.Fatal(err)
	}
	handles, err := r.stageSources(raceAssignment(4))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { r.renewals.renewDue(ctx, time.Now().Add(46*time.Second)); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("renewal did not start")
	}
	// A preparation finishes, and the core releases the source grant. The
	// already-sent renewal then observes that ordinary release as a 403.
	r.completeSourceWork("work", 4, handles)
	if !r.workAllowed("work", 4) {
		t.Fatal("ready artifact already lost authority")
	}
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("renewal did not complete")
	}
	if !r.workAllowed("work", 4) {
		t.Fatal("late response from retired source renewal permanently revoked a ready artifact")
	}
}

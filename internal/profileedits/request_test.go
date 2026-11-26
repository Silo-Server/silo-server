package profileedits

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The edit's response completes while the wake it triggers is still running.
// Regression: the wake ran inline after the handler, inside the response
// compressor, so a slow enqueue held every edit's response.
func TestMiddlewareDoesNotHoldTheResponseForTheWake(t *testing.T) {
	entered, release := make(chan int, 1), make(chan struct{})
	defer close(release)
	wake := Waker(func(_ context.Context, userID int) bool {
		entered <- userID
		<-release
		return true
	})
	handler := Middleware(wake)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		FromContext(r.Context()).noteAccount(7, 1)
		w.WriteHeader(http.StatusNoContent)
	}))

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPatch, "/profiles/p", nil))
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the response waited for the wake")
	}
	select {
	case account := <-entered:
		if account != 7 {
			t.Fatalf("woke account %d, want 7", account)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the edited account was never woken")
	}
}

package playback

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestStopRemoteTranscodeAfterFailure(t *testing.T) {
	deletes := make(chan string, 2)
	release := make(chan struct{})
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			<-release
			deletes <- r.URL.Path
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer node.Close()
	m := NewTranscodeManager()

	// An unreachable node is stopped in the background: the call returns
	// while the node is still holding the stop.
	unreachable := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	returned := make(chan struct{})
	go func() {
		m.StopRemoteTranscodeAfterFailure("session-a", node.URL, unreachable)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("stop after a connect failure blocked the caller")
	}
	release <- struct{}{}
	if got := <-deletes; got != "/transcode/session-a" {
		t.Fatalf("background stop deleted %q", got)
	}

	// Any other failure stops synchronously, as before.
	go func() { release <- struct{}{} }()
	m.StopRemoteTranscodeAfterFailure("session-b", node.URL, errors.New("node returned 500"))
	select {
	case got := <-deletes:
		if got != "/transcode/session-b" {
			t.Fatalf("stop deleted %q", got)
		}
	default:
		t.Fatal("stop after an ordinary failure returned before the node answered")
	}
}

package transcodeproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
)

func TestNodeUnreachable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadURL := "http://" + listener.Addr().String()
	_ = listener.Close()
	resp, refused := NodeClient().Get(deadURL + "/transcode/s/segment/seg_00001.ts")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if refused == nil {
		t.Fatal("request to a closed port succeeded")
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	live := httptest.NewServer(http.NotFoundHandler())
	defer live.Close()
	req, _ := http.NewRequestWithContext(canceledCtx, http.MethodGet, live.URL, nil)
	resp, canceled := NodeClient().Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if canceled == nil {
		t.Fatal("request with a canceled context succeeded")
	}

	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"refused connection", refused, true},
		{"failed name lookup", &net.DNSError{Err: "no such host", Name: "node.invalid", IsNotFound: true}, true},
		{"dial timeout", &net.OpError{Op: netOpDial, Net: "tcp", Err: os.ErrDeadlineExceeded}, true},
		{"caller canceled", canceled, false},
		{"out of file descriptors", &net.OpError{Op: netOpDial, Net: "tcp", Err: os.NewSyscallError("socket", syscall.EMFILE)}, false},
		{"reset mid-response", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, false},
		{"other", errors.New("boom"), false},
		{"nil", nil, false},
	} {
		if got := NodeUnreachable(tt.err); got != tt.want {
			t.Errorf("%s: NodeUnreachable(%v) = %v, want %v", tt.name, tt.err, got, tt.want)
		}
	}
}

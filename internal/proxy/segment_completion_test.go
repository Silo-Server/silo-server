package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/transcodeproxy"
)

func TestTranscodeProxyAcknowledgesOnlyFullDownstreamResponse(t *testing.T) {
	const (
		body       = "complete segment"
		generation = "incarnation:17"
	)
	tests := []struct {
		name        string
		rangeHeader string
		wantStatus  int
		wantAck     int32
	}{
		{name: "ordinary get", wantStatus: http.StatusOK, wantAck: 1},
		{name: "whole-file range", rangeHeader: "bytes=0-", wantStatus: http.StatusPartialContent, wantAck: 1},
		{name: "partial range", rangeHeader: "bytes=1-", wantStatus: http.StatusPartialContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var acknowledgements atomic.Int32
			seenRange := make(chan string, 1)
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					if r.Header.Get(transcodeproxy.RequestHeader) != "1" {
						t.Error("segment request omitted proxy marker")
					}
					seenRange <- r.Header.Get("Range")
					w.Header().Set(transcodeproxy.GenerationHeader, generation)
					http.ServeContent(w, r, "seg_00007.ts", time.Time{}, strings.NewReader(body))
				case http.MethodPost:
					if r.Header.Get(transcodeproxy.GenerationHeader) != generation {
						t.Errorf("ack generation = %q, want %q", r.Header.Get(transcodeproxy.GenerationHeader), generation)
					}
					acknowledgements.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
				}
			}))
			defer node.Close()

			server := newCompletionTestProxy(node.Client())
			claims := &streamtoken.Claims{SessionID: "public", TranscodeNode: node.URL}
			req := httptest.NewRequest(http.MethodGet, "/stream/transcode/token/segment/seg_00007.ts", nil)
			req.Header.Set("Range", tt.rangeHeader)
			rr := httptest.NewRecorder()
			server.proxyToTranscodeNode(rr, req, claims, "/transcode/remote/segment/seg_00007.ts", "")

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %q", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if got := <-seenRange; got != tt.rangeHeader {
				t.Fatalf("forwarded Range = %q, want %q", got, tt.rangeHeader)
			}
			if got := rr.Header().Get(transcodeproxy.GenerationHeader); got != "" {
				t.Fatalf("internal generation leaked downstream: %q", got)
			}
			if got := acknowledgements.Load(); got != tt.wantAck {
				t.Fatalf("acknowledgements = %d, want %d", got, tt.wantAck)
			}
		})
	}
}

func TestTranscodeProxyDoesNotAcknowledgeFailedDownstreamWrite(t *testing.T) {
	var acknowledgements atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			acknowledgements.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set(transcodeproxy.GenerationHeader, "incarnation:21")
		http.ServeContent(w, r, "seg_00007.ts", time.Time{}, strings.NewReader("complete segment"))
	}))
	defer node.Close()

	server := newCompletionTestProxy(node.Client())
	claims := &streamtoken.Claims{SessionID: "public", TranscodeNode: node.URL}
	req := httptest.NewRequest(http.MethodGet, "/stream/transcode/token/segment/seg_00007.ts", nil)
	w := &failingCompletionResponseWriter{header: make(http.Header), remaining: 5}
	server.proxyToTranscodeNode(w, req, claims, "/transcode/remote/segment/seg_00007.ts", "")

	if got := acknowledgements.Load(); got != 0 {
		t.Fatalf("failed downstream transfer produced %d acknowledgement(s)", got)
	}
}

func newCompletionTestProxy(client *http.Client) *Server {
	watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "proxy-test-secret"
	watcher.SetConfigForTest(cfg)
	server := NewServer(watcher, nil)
	server.httpClient = client
	return server
}

type failingCompletionResponseWriter struct {
	header    http.Header
	status    int
	remaining int
}

func (w *failingCompletionResponseWriter) Header() http.Header { return w.header }

func (w *failingCompletionResponseWriter) WriteHeader(status int) { w.status = status }

func (w *failingCompletionResponseWriter) Write(p []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, io.ErrClosedPipe
	}
	n := min(len(p), w.remaining)
	w.remaining -= n
	return n, io.ErrClosedPipe
}

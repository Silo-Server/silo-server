package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/transcodeproxy"
)

func TestStreamingProtocolOriginResponses(t *testing.T) {
	const secret = "synthetic-streaming-protocol"
	for _, tc := range []struct{ method, suffix string }{
		{http.MethodGet, "master.m3u8"}, {http.MethodHead, "master.m3u8"}, {http.MethodGet, "segment/seg_00001.ts"},
	} {
		t.Run(tc.method+tc.suffix, func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method || r.URL.Path != "/transcode/transport/"+tc.suffix || r.URL.RawQuery != "opaque=1" || r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("X-Silo-Stream-Token") == "" {
					t.Error("relay identity/query lost")
				}
				segment := strings.HasPrefix(tc.suffix, "segment/")
				if segment != (r.Header.Get("Range") == "bytes=0-2") || segment != (r.Header.Get(transcodeproxy.RequestHeader) == "1") {
					t.Error("representation forwarding drift")
				}
				w.Header().Set("Content-Type", "application/x-origin-fixture")
				w.Header().Set("X-Origin-Fixture", "preserved")
				w.Header().Set(transcodeproxy.GenerationHeader, "private")
				w.WriteHeader(418)
				_, _ = io.WriteString(w, "origin refusal")
			}))
			defer origin.Close()
			token, err := streamtoken.Sign(streamtoken.Claims{SessionID: "session", TranscodeTransportID: "transport", PlayMethod: "transcode", TranscodeNode: origin.URL}, secret, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(newSocketProxyServer(t, secret, clientip.NewResolver(nil)).Handler())
			defer server.Close()
			response := socketProxyRequest(t, server.Client(), tc.method, server.URL+"/stream/transcode/"+token+"/"+tc.suffix+"?opaque=1", map[string]string{"Range": "bytes=0-2"})
			if response.status != 418 || response.header.Get("X-Origin-Fixture") != "preserved" || response.header.Get(transcodeproxy.GenerationHeader) != "" {
				t.Fatal(response.status, response.header)
			}
			if tc.method == http.MethodHead {
				if response.body != "" {
					t.Fatal("HEAD bytes")
				}
			} else if response.body != "origin refusal" {
				t.Fatal(response.body)
			}
			found := false
			for _, op := range ProtocolStreaming() {
				if op.Method == tc.method && strings.HasPrefix(op.Path, "/stream/transcode/") && strings.HasSuffix(op.Path, strings.ReplaceAll(tc.suffix, "seg_00001.ts", "{name}")) {
					found = true
					if op.Responses["default"] == nil {
						t.Fatal("origin response omitted")
					}
				}
			}
			if !found {
				t.Fatal("missing relay description")
			}
		})
	}
}

func TestStreamingProtocolLocalRemuxHeadStartsWork(t *testing.T) {
	const secret = "synthetic-remux-head"
	proxy := newSocketProxyServer(t, secret, clientip.NewResolver(nil))
	binary := filepath.Join(t.TempDir(), "remux.sh")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n: > \"$0.started\"\nprintf 'synthetic remux'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	proxy.watcher.Config().Playback.FFmpegPath = binary
	token, err := streamtoken.Sign(streamtoken.Claims{SessionID: "remux", MediaPath: writeSocketProxyMedia(t), PlayMethod: "remux"}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(proxy.Handler())
	defer server.Close()
	response := socketProxyRequest(t, server.Client(), http.MethodHead, server.URL+"/stream/remux/"+token, nil)
	if response.status != 200 || response.body != "" {
		t.Fatal(response.status, response.body)
	}
	if _, err := os.Stat(binary + ".started"); err != nil {
		t.Fatal("existing local HEAD execution behavior changed", err)
	}
}

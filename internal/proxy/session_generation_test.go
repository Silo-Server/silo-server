package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

type proxyGenerationStore struct {
	ended      bool
	err        error
	calls      int
	generation string
}

func (s *proxyGenerationStore) WasSessionGenerationEnded(_ context.Context, _, generation string, _ time.Time) (bool, error) {
	s.calls++
	s.generation = generation
	return s.ended, s.err
}

func (*proxyGenerationStore) RecordEndedSessionGeneration(context.Context, string, string, time.Time) error {
	return nil
}

func proxyTestServer(t *testing.T, store playback.SessionGenerationTombstoneStore) (*Server, string) {
	t.Helper()
	w := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "proxy-generation-secret"
	cfg.Playback.TranscodeDir = t.TempDir()
	w.SetConfigForTest(cfg)
	tracker := nodesessions.NewTracker(nil, "http://proxy", "proxy", "proxy")
	return NewServer(w, tracker, store), cfg.Auth.JWTSecret
}

func signedProxyToken(t *testing.T, secret, generation, mediaPath string) string {
	t.Helper()
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:         "public-session",
		SessionGeneration: generation,
		MediaPath:         mediaPath,
		TranscodeNode:     "http://127.0.0.1:1",
	}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestProxyGenerationAdmissionPrecedesEveryMediaRouteSideEffect(t *testing.T) {
	routes := []string{
		"/stream/direct/%s",
		"/stream/remux/%s",
		"/stream/transcode/%s/master.m3u8",
		"/stream/transcode/%s/segment/segment00001.ts",
		"/stream/subtitles/%s/0",
		"/stream/subtitles/%s/0/fonts",
	}
	for _, tc := range []struct {
		name  string
		store *proxyGenerationStore
		want  int
	}{{"ended", &proxyGenerationStore{ended: true}, http.StatusGone}, {"store error", &proxyGenerationStore{err: errors.New("db down")}, http.StatusServiceUnavailable}} {
		t.Run(tc.name, func(t *testing.T) {
			server, secret := proxyTestServer(t, tc.store)
			token := signedProxyToken(t, secret, "32a4e124-9df7-4cfa-be49-e8e503316714", filepath.Join(t.TempDir(), "must-not-open"))
			for _, route := range routes {
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, strings.Replace(route, "%s", token, 1), nil)
				server.Handler().ServeHTTP(rr, req)
				if rr.Code != tc.want {
					t.Fatalf("route %s status = %d, body=%q", route, rr.Code, rr.Body.String())
				}
			}
			if tc.store.calls != len(routes) {
				t.Fatalf("authority calls = %d, want %d", tc.store.calls, len(routes))
			}
			if server.tracker.ActiveCount() != 0 {
				t.Fatal("denied requests changed tracking state")
			}
		})
	}
}

func TestProxyGenerationAdmissionAllowsValidAndLegacyDirectPlay(t *testing.T) {
	mediaPath := filepath.Join(t.TempDir(), "movie.bin")
	if err := os.WriteFile(mediaPath, []byte("generation-authorized"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, generation := range []string{"32a4e124-9df7-4cfa-be49-e8e503316714", ""} {
		store := &proxyGenerationStore{}
		server, secret := proxyTestServer(t, store)
		token := signedProxyToken(t, secret, generation, mediaPath)
		rr := httptest.NewRecorder()
		server.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/stream/direct/"+token, nil))
		if rr.Code != http.StatusOK || rr.Body.String() != "generation-authorized" {
			t.Fatalf("generation %q status/body = %d/%q", generation, rr.Code, rr.Body.String())
		}
		if store.calls != 1 || store.generation != generation {
			t.Fatalf("generation %q authority = %d/%q", generation, store.calls, store.generation)
		}
	}
}

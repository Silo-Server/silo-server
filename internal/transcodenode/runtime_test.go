package transcodenode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

type isolatedConfig struct{ cfg *config.Config }

func (c isolatedConfig) Config() *config.Config          { return c.cfg }
func (isolatedConfig) NodeRowID() (int, bool)            { return 7, true }
func (isolatedConfig) NodeRegisteredURL() (string, bool) { return "http://worker.invalid", true }
func (isolatedConfig) NodeDrainRequested() bool          { return false }
func (isolatedConfig) OnNodeDrainChange(func(bool))      {}
func (isolatedConfig) ForceReload(context.Context) error { return nil }

type isolatedTracker struct{}

func (isolatedTracker) Track(context.Context, nodesessions.SessionInfo) {}
func (isolatedTracker) Remove(context.Context, string)                  {}
func (isolatedTracker) Cleanup(context.Context)                         {}
func (isolatedTracker) NodeURL() string                                 { return "http://worker.invalid" }
func (isolatedTracker) NodeName() string                                { return "isolated" }

func TestRuntimeHasNoSharedSecretAuthFallback(t *testing.T) {
	cfg := &config.Config{}
	cfg.Playback.TranscodeDir = t.TempDir()
	cfg.Auth.JWTSecret = ""
	cfg.Database.URL = ""
	var calls int
	s, err := NewRuntimeServer(RuntimeOptions{Config: isolatedConfig{cfg}, Tracker: isolatedTracker{}, VerifyCommand: func(*http.Request) error { calls++; return errors.New("denied") }, VerifyToken: func(context.Context, string) (*streamtoken.Claims, error) { return nil, errors.New("denied") }, Denied: func(context.Context, string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/status", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || calls != 1 {
		t.Fatalf("status=%d verifier calls=%d", w.Code, calls)
	}
	cfg.Auth.JWTSecret = "shared-root"
	_, err = NewRuntimeServer(RuntimeOptions{Config: isolatedConfig{cfg}, Tracker: isolatedTracker{}, VerifyCommand: func(*http.Request) error { return nil }, VerifyToken: func(context.Context, string) (*streamtoken.Claims, error) { return nil, nil }, Denied: func(context.Context, string) bool { return false }})
	if err == nil {
		t.Fatal("standalone accepted shared JWT secret")
	}
}

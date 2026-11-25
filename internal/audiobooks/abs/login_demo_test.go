package abs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/demomode"
)

// demoSetting answers demo.enabled, or fails every read.
type demoSetting struct {
	on  bool
	err error
}

func (d demoSetting) Get(context.Context, string) (string, error) {
	if d.err != nil {
		return "", d.err
	}
	if d.on {
		return "true", nil
	}
	return "", nil
}

// failAfterFirst answers "" (demo off) once, then fails every read.
type failAfterFirst struct{ reads int }

func (f *failAfterFirst) Get(context.Context, string) (string, error) {
	f.reads++
	if f.reads > 1 {
		return "", errors.New("settings store unreachable")
	}
	return "", nil
}

// TestHandleLogout_DemoModeEndsOnlyThePresentedToken: demo visitors share
// one account and profile, so one visitor's sign-out must not end the
// others'. Catches logout revoking every token of the principal on a demo
// server, and a change to sign-out with demo mode off or for an
// administrator, where it still ends all of them.
func TestHandleLogout_DemoModeEndsOnlyThePresentedToken(t *testing.T) {
	// Off once, then every read fails: the shared demomode.Setting answers
	// the remembered off, so sign-out is unchanged.
	offThenFailing := demomode.New(&failAfterFirst{})
	if _, err := offThenFailing.Get(context.Background(), demomode.SettingKey); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		setting    DemoSettingsReader
		admin      bool
		othersLive bool
	}{
		{"demo off", demoSetting{}, false, false},
		{"demo on", demoSetting{on: true}, false, true},
		{"demo on, administrator", demoSetting{on: true}, true, false},
		{"demo setting never read", demoSetting{err: errors.New("settings store unreachable")}, false, true},
		{"read fails after an off read", offThenFailing, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store, cfg := newRefreshTestHandler(t)
			h.deps.DemoSettings = tc.setting
			h.deps.AccountIsAdmin = func(context.Context, string) (bool, error) { return tc.admin, nil }
			mine := mintAndPersistAccess(t, store, cfg, "1", "visitor-a")
			_ = mintAndPersistAccess(t, store, cfg, "1", "visitor-b")

			req := httptest.NewRequest(http.MethodPost, "/logout", nil)
			req.Header.Set("Authorization", "Bearer "+mine)
			rec := httptest.NewRecorder()
			h.handleLogout(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
			}
			if tok, _ := store.GetTokenByJTI(context.Background(), "visitor-a"); tok.RevokedAt == nil {
				t.Fatal("the presented token is still live")
			}
			other, _ := store.GetTokenByJTI(context.Background(), "visitor-b")
			if live := other.RevokedAt == nil; live != tc.othersLive {
				t.Fatalf("other token live = %v, want %v", live, tc.othersLive)
			}
		})
	}
}

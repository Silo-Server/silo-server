package abs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// persistRefresh mints a refresh token for userID with jti and stores it.
func persistRefresh(t *testing.T, store *memTokenStore, cfg *staticConfig, userID, jti string) string {
	t.Helper()
	refresh, err := IssueRefreshToken(cfg.secret, userID, "", nil, jti, time.Hour)
	if err != nil {
		t.Fatalf("mint refresh: %v", err)
	}
	if err := store.InsertToken(context.Background(), ABSToken{ID: jti, UserID: userID, JTI: jti, Type: tokenTypeRefresh, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return refresh
}

// TestHandleLogout_DemoModeRevokesPresentedRefreshToken: a demo sign-out
// that carries its refresh token (header or cookie) ends it too, since
// possession makes it the caller's own, while another visitor's tokens stay
// live and a refresh token of another account is ignored. Catches a demo
// sign-out leaving the client able to refresh, or ending a token it was not
// entitled to end.
func TestHandleLogout_DemoModeRevokesPresentedRefreshToken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		carry   func(*http.Request, string)
		owner   string
		revoked bool
	}{
		{"header", func(r *http.Request, tok string) { r.Header.Set("x-refresh-token", tok) }, "1", true},
		{"cookie", func(r *http.Request, tok string) { r.AddCookie(&http.Cookie{Name: "refresh_token", Value: tok}) }, "1", true},
		{"another account's", func(r *http.Request, tok string) { r.Header.Set("x-refresh-token", tok) }, "2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store, cfg := newRefreshTestHandler(t)
			h.deps.DemoSettings = demoSetting{on: true}
			mine := mintAndPersistAccess(t, store, cfg, "1", "visitor-a")
			presented := persistRefresh(t, store, cfg, tc.owner, "presented-refresh")
			_ = mintAndPersistAccess(t, store, cfg, "1", "visitor-b")
			_ = persistRefresh(t, store, cfg, "1", "visitor-b-refresh")

			req := httptest.NewRequest(http.MethodPost, "/logout", nil)
			req.Header.Set("Authorization", "Bearer "+mine)
			tc.carry(req, presented)
			rec := httptest.NewRecorder()
			h.handleLogout(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			revoked := func(jti string) bool {
				tok, _ := store.GetTokenByJTI(context.Background(), jti)
				return tok.RevokedAt != nil
			}
			if !revoked("visitor-a") {
				t.Fatal("the presented access token is still live")
			}
			if revoked("presented-refresh") != tc.revoked {
				t.Fatalf("presented refresh token revoked = %v, want %v", !tc.revoked, tc.revoked)
			}
			if revoked("visitor-b") || revoked("visitor-b-refresh") {
				t.Fatal("another visitor's tokens were revoked")
			}
		})
	}
}

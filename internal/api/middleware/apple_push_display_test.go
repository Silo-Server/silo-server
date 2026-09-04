package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/auth"
)

type fakeSessionValidator struct{ valid map[string]bool }

func (f *fakeSessionValidator) IsValid(_ context.Context, id string) (bool, error) {
	return f.valid[id], nil
}

func TestRequireApplePushDisplayAuth(t *testing.T) {
	jwt := auth.NewJWTService("test-secret", 15*time.Minute, 7*24*time.Hour)
	var lastLog *activitylog.LogContext
	sessions := &fakeSessionValidator{valid: map[string]bool{"sess-live": true}}
	am := NewAuthMiddleware(jwt, sessions, nil, nil)

	var gotProfile string
	var gotClaims *auth.Claims
	// afterAuth stands in for the rate limiter: it must observe claims on
	// both the display-token path and the access-token fallback.
	var afterAuthSawClaims int
	afterAuth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if GetClaims(r.Context()) != nil {
				afterAuthSawClaims++
			}
			next.ServeHTTP(w, r)
		})
	}
	// Viewer access on both paths: the display token skips the PIN proof but
	// the profile must still resolve as owned by the user.
	viewer := NewViewerAccessMiddleware(&fakeViewerResolver{
		profiles: map[string]int{"profile-1": 42, "profile-2": 42, "profile-pin": 42},
		pinned:   map[string]bool{"profile-pin": true},
	})
	postAuth := func(next http.Handler) http.Handler {
		return afterAuth(viewer.RequireViewerAccess(RequireProfile(next)))
	}
	fallback := func(next http.Handler) http.Handler { return am.RequireAuth(postAuth(next)) }
	inner := am.RequireApplePushDisplayAuth(fallback, postAuth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProfile = GetProfileID(r.Context())
		gotClaims = GetClaims(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	// Stand in for the activitylog middleware so attribution can be checked.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastLog = &activitylog.LogContext{}
		inner.ServeHTTP(w, r.WithContext(activitylog.SetLogContext(r.Context(), lastLog)))
	})

	admin := 7
	display, _, err := jwt.GenerateApplePushDisplayToken(42, "user", "sess-live", "profile-1", &admin)
	if err != nil {
		t.Fatal(err)
	}
	revoked, _, err := jwt.GenerateApplePushDisplayToken(42, "user", "sess-dead", "profile-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	deletedProfile, _, err := jwt.GenerateApplePushDisplayToken(42, "user", "sess-live", "profile-gone", nil)
	if err != nil {
		t.Fatal(err)
	}
	pinnedProfile, _, err := jwt.GenerateApplePushDisplayToken(42, "user", "sess-live", "profile-pin", nil)
	if err != nil {
		t.Fatal(err)
	}
	access, err := jwt.GenerateAccessToken(42, "user", "sess-live")
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := jwt.GenerateRefreshToken(42, "user", "sess-live")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		token       string
		profileHdr  string
		wantStatus  int
		wantProfile string
	}{
		{"display token binds profile from claims", display, "profile-other", http.StatusNoContent, "profile-1"},
		{"display token with revoked session", revoked, "", http.StatusUnauthorized, ""},
		{"display token for a deleted profile", deletedProfile, "", http.StatusNotFound, ""},
		{"display token skips the PIN proof", pinnedProfile, "", http.StatusNoContent, "profile-pin"},
		{"access token for a PIN profile still needs proof", access, "profile-pin", http.StatusForbidden, ""},
		{"access token still works through fallback chain", access, "profile-2", http.StatusNoContent, "profile-2"},
		{"access token without profile header hits fallback 400", access, "", http.StatusBadRequest, ""},
		{"refresh token rejected", refresh, "", http.StatusUnauthorized, ""},
		{"missing token", "", "", http.StatusUnauthorized, ""},
		{"display token in query string is rejected", "?" + display, "", http.StatusUnauthorized, ""},
		{"garbage token", "not-a-jwt", "", http.StatusUnauthorized, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProfile, gotClaims, afterAuthSawClaims = "", nil, 0
			target := "/api/v1/notifications/push/apple/display/d1"
			if strings.HasPrefix(tt.token, "?") {
				target += "?token=" + strings.TrimPrefix(tt.token, "?")
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			if tt.token != "" && !strings.HasPrefix(tt.token, "?") {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			if tt.profileHdr != "" {
				req.Header.Set("X-Profile-Id", tt.profileHdr)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if gotProfile != tt.wantProfile {
				t.Fatalf("profile = %q, want %q", gotProfile, tt.wantProfile)
			}
			if tt.wantStatus == http.StatusNoContent && (gotClaims == nil || gotClaims.UserID != 42) {
				t.Fatalf("claims = %+v", gotClaims)
			}
			if tt.wantStatus == http.StatusNoContent && afterAuthSawClaims != 1 {
				t.Fatalf("afterAuth saw claims %d times, want exactly 1", afterAuthSawClaims)
			}
			if tt.wantStatus == http.StatusNoContent && (lastLog.UserID == nil || *lastLog.UserID != 42 || lastLog.SessionID != "sess-live") {
				t.Fatalf("activity log context not attributed: %+v", lastLog)
			}
			if tt.token == display && (lastLog.ImpersonatorUserID == nil || *lastLog.ImpersonatorUserID != 7) {
				t.Fatalf("impersonator not carried through display token: %+v", lastLog)
			}
		})
	}
}

// fakeViewerResolver approximates access.Resolver: unknown profiles are not
// found, PIN-protected profiles need SkipPINVerification or a profile token.
type fakeViewerResolver struct {
	profiles map[string]int
	pinned   map[string]bool
}

func (f *fakeViewerResolver) Resolve(_ context.Context, in access.ResolveInput) (access.Scope, error) {
	if in.ProfileID == "" {
		return access.Scope{UserID: in.UserID}, nil
	}
	owner, ok := f.profiles[in.ProfileID]
	if !ok || owner != in.UserID {
		return access.Scope{}, access.ErrProfileNotFound
	}
	if f.pinned[in.ProfileID] && !in.SkipPINVerification && in.ProfileToken == "" {
		return access.Scope{}, access.ErrProfileUnverified
	}
	return access.Scope{UserID: in.UserID, ProfileID: in.ProfileID}, nil
}

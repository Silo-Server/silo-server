package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	handoffSession = "handoff"
	handoffProfile = "kid"
)

// handoffRequest runs one request through RequireAuth on a remote-playback
// session bound to handoffProfile, for an admin account. The token claims
// whatever an older release would have minted: the account's admin role and
// no purpose.
func handoffRequest(t *testing.T, method, target, profileID string) (*httptest.ResponseRecorder, *auth.Claims, string) {
	t.Helper()
	claims := auth.Claims{UserID: 42, Role: models.RoleAdmin, SessionID: handoffSession, TokenType: auth.TokenTypeAccess}
	sessions := &fakeSessionValidator{roles: map[string]string{}, remote: map[string]string{handoffSession: handoffProfile}}
	am := NewAuthMiddleware(claimsValidator{claims}, sessions, nil, nil)
	var seen *auth.Claims
	var seenProfile string
	h := am.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = GetClaims(r.Context())
		seenProfile = r.Header.Get("X-Profile-Id")
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Authorization", "Bearer token")
	if profileID != "" {
		req.Header.Set("X-Profile-Id", profileID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, seen, seenProfile
}

// A handoff session acts as its bound profile with the account role "user",
// whatever its token says, and reaches only the playback routes.
func TestRequireAuthBindsRemotePlaybackSession(t *testing.T) {
	for _, tc := range []struct {
		name, method, target, profile string
		allowed                       bool
	}{
		{"play without a profile header", http.MethodPost, "/api/v2/playback/start", "", true},
		{"play as the bound profile", http.MethodPost, "/api/v2/playback/start", handoffProfile, true},
		{"read the account", http.MethodGet, "/api/v2/account/me", handoffProfile, true},
		{"sign out", http.MethodPost, "/api/v2/auth/logout", handoffProfile, true},
		{"play as another profile", http.MethodPost, "/api/v2/playback/start", "parent", false},
		{"list accounts as an admin", http.MethodGet, "/api/v2/admin/users", "", false},
		{"approve a device login", http.MethodPost, "/api/v2/auth/device/approve", "", false},
		{"approve a handoff", http.MethodPost, "/api/v2/auth/device/approve-handoff", handoffProfile, false},
		{"create an API key", http.MethodPost, "/api/v2/api-keys", handoffProfile, false},
		{"list login sessions", http.MethodGet, "/api/v2/auth/sessions", "", false},
		{"change the password", http.MethodPost, "/api/v2/account/password", handoffProfile, false},
		{"create a profile", http.MethodPost, "/api/v2/profiles", handoffProfile, false},
		{"write a setting", http.MethodPut, "/api/v2/settings/values/ui.theme", handoffProfile, false},
		{"use the frozen v1 surface", http.MethodGet, "/api/v1/auth/me", "", false},
		{"a dot-dot spelling of a listed route", http.MethodGet, "/api/v2/stream/x/../../admin/users", "", false},
		{"a doubled slash", http.MethodGet, "/api/v2//account/me", "", false},
		{"an encoded slash", http.MethodGet, "/api/v2/subtitles/ai%2Fjobs/sync", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, claims, profile := handoffRequest(t, tc.method, tc.target, tc.profile)
			if !tc.allowed {
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
			}
			if claims.Role != models.RoleUser || !claims.IsRemotePlayback() || claims.ProfileID != handoffProfile || claims.IsOwnLoginSession() {
				t.Fatalf("claims were not narrowed: %+v", claims)
			}
			if profile != handoffProfile {
				t.Fatalf("X-Profile-Id = %q, want the bound profile", profile)
			}
		})
	}
}

// An ordinary sign-in keeps its own role check and every route.
func TestRequireAuthLeavesAccountSessionsAlone(t *testing.T) {
	claims := auth.Claims{UserID: 42, Role: models.RoleAdmin, SessionID: "own", TokenType: auth.TokenTypeAccess}
	am := NewAuthMiddleware(claimsValidator{claims}, &fakeSessionValidator{roles: map[string]string{"own": models.RoleAdmin}}, nil, nil)
	var seen *auth.Claims
	h := am.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = GetClaims(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v2/admin/users", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || seen.Role != models.RoleAdmin || !seen.IsOwnLoginSession() {
		t.Fatalf("account session: %d %+v", rec.Code, seen)
	}
}

// AttachSession (public routes such as the device-login lookup) narrows a
// handoff's claims, so they are not the account's own login session.
func TestAttachSessionNarrowsRemotePlayback(t *testing.T) {
	claims := auth.Claims{UserID: 42, Role: models.RoleAdmin, SessionID: handoffSession, TokenType: auth.TokenTypeAccess}
	am := NewAuthMiddleware(claimsValidator{claims}, &fakeSessionValidator{remote: map[string]string{handoffSession: handoffProfile}}, nil, nil)
	var seen *auth.Claims
	h := am.AttachSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = GetClaims(r.Context()) }))
	req := httptest.NewRequest(http.MethodGet, "/api/v2/auth/device", nil)
	req.Header.Set("Authorization", "Bearer token")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen == nil || seen.IsOwnLoginSession() || seen.Role != models.RoleUser {
		t.Fatalf("attached claims = %+v", seen)
	}
}

// A display token or download link an older release minted for a handoff
// session is refused.
func TestDerivedCredentialsRefuseRemotePlayback(t *testing.T) {
	jwt := auth.NewJWTService("test-secret", 15*time.Minute, 7*24*time.Hour)
	sessions := &fakeSessionValidator{remote: map[string]string{handoffSession: handoffProfile}}
	am := NewAuthMiddleware(jwt, sessions, nil, nil)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	display, _, err := jwt.GenerateApplePushDisplayToken(42, models.RoleAdmin, handoffSession, handoffProfile, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v2/notifications/push/apple/display/1", nil)
	req.Header.Set("Authorization", "Bearer "+display)
	rec := httptest.NewRecorder()
	am.RequireApplePushDisplayAuth(func(next http.Handler) http.Handler { return next }, nil)(ok).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("display token: %d %s", rec.Code, rec.Body.String())
	}

	link, _, err := jwt.GenerateDirectDownloadLinkToken(42, models.RoleAdmin, handoffSession, handoffProfile, nil, 9)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v2/direct-download?file_id=9&"+DirectDownloadLinkParam+"="+link, nil)
	rec = httptest.NewRecorder()
	am.RequireDirectDownloadAuth(ok).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("download link: %d %s", rec.Code, rec.Body.String())
	}
}

// RevalidateCurrent holds a handoff to its route list and bound profile.
func TestRevalidateCurrentHoldsRemotePlayback(t *testing.T) {
	claims := auth.Claims{UserID: 42, Role: models.RoleUser, SessionID: handoffSession, TokenType: auth.TokenTypeAccess, ProfileID: handoffProfile, SessionPurpose: models.SessionPurposeRemotePlayback}
	am := NewAuthMiddleware(claimsValidator{claims}, &fakeSessionValidator{remote: map[string]string{handoffSession: handoffProfile}}, nil,
		userLoaderFunc(func(context.Context, int) (*models.User, error) { return &models.User{ID: 42, Enabled: true}, nil }))
	if err := am.RevalidateCurrent(t.Context(), "Bearer token", &claims, http.MethodPost, "/api/v2/sync/progress/snapshots"); err != nil {
		t.Fatalf("listed route: %v", err)
	}
	if err := am.RevalidateCurrent(t.Context(), "Bearer token", &claims, http.MethodPost, "/api/v2/history/remove"); !errors.Is(err, ErrCurrentCredentialForbidden) {
		t.Fatalf("unlisted route: %v", err)
	}
	other := claims
	other.ProfileID = "parent"
	if err := am.RevalidateCurrent(t.Context(), "Bearer token", &other, http.MethodPost, "/api/v2/sync/progress/snapshots"); !errors.Is(err, ErrCurrentCredentialForbidden) {
		t.Fatalf("other profile: %v", err)
	}
}

type userLoaderFunc func(context.Context, int) (*models.User, error)

func (f userLoaderFunc) GetByID(ctx context.Context, id int) (*models.User, error) { return f(ctx, id) }

// The route list admits what the Apple and Android TV apps call during a
// handoff, and no operation the v2 contract marks as administrative or as
// account management.
func TestRemotePlaybackRoutesAgainstContract(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/api/v2/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Class       string `json:"x-silo-class"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	// Account and household management outside the admin class.
	refused := map[string]bool{
		"approveDeviceLogin": true, "approveDeviceHandoff": true, "denyDeviceLogin": true,
		"changePassword": true, "createPersonalAPIKey": true, "revokePersonalAPIKey": true, "listPersonalAPIKeys": true,
		"listSessions": true, "deleteSession": true, "createPluginLaunch": true, "getProviderLogout": true,
		"createProfile": true, "updateProfile": true, "deleteProfile": true, "verifyProfilePIN": true,
		"listAccountIdentities": true, "deleteAccountIdentity": true, "createAccountIdentityLinkTicket": true,
		"registerApplePushDevice": true, "createDirectDownloadLink": true, "createWatchTogetherSocketTicket": true,
		"endImpersonation": true, "listDevices": true,
	}
	clientOps := map[string]bool{
		"getCurrentUser": false, "logout": false, "listProfiles": false, "getPlaybackCapabilities": false,
		"startPlayback": false, "replanPlayback": false, "updatePlaybackProgress": false, "stopPlayback": false,
		"reportPlaybackRouteEvent": false, "createPlaybackControlSocketTicket": false, "getWatchState": false,
		"syncProgress": false, "listHomeSections": false, "createEventsSocketTicket": false,
		"getOverlayConfig": false, "listSubtitleSync": false, "getSubtitleSync": false, "startSubtitleSync": false,
		"setSubtitleTiming": false, "getStoredSubtitleSync": false, "syncStoredSubtitle": false,
		"setStoredSubtitleTiming": false, "getViewerSubtitleMetadata": false,
	}
	seen := map[string]bool{}
	for p, ops := range contract.Paths {
		for method, op := range ops {
			method = strings.ToUpper(method)
			// Path parameters as a client fills them.
			concrete := strings.NewReplacer("{num}", "1", "{media_file_id}", "7").Replace(p)
			for strings.Contains(concrete, "{") {
				start := strings.Index(concrete, "{")
				end := strings.Index(concrete[start:], "}")
				concrete = concrete[:start] + "x1" + concrete[start+end+1:]
			}
			allowed := remotePlaybackAllowsRoute(method, concrete)
			seen[op.OperationID] = true
			if _, ok := clientOps[op.OperationID]; ok {
				clientOps[op.OperationID] = clientOps[op.OperationID] || allowed
			}
			if allowed && (op.Class == "acting_admin" || op.Class == "permission_gated" || strings.HasPrefix(p, "/api/v2/admin") || refused[op.OperationID]) {
				t.Errorf("handoff admits %s %s (%s, %s)", method, p, op.OperationID, op.Class)
			}
		}
	}
	for op, allowed := range clientOps {
		if !seen[op] {
			t.Errorf("operation %s is not in the contract", op)
		} else if !allowed {
			t.Errorf("handoff refuses %s, which the TV apps call", op)
		}
	}
	for op := range refused {
		if !seen[op] {
			t.Errorf("operation %s is not in the contract", op)
		}
	}
}

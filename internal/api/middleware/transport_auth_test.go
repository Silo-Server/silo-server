package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

type transportAuthTokenValidator struct {
	claims *auth.Claims
	err    error
	calls  int
}

func (v *transportAuthTokenValidator) ValidateToken(string) (*auth.Claims, error) {
	v.calls++
	return v.claims, v.err
}

type transportAuthSessionValidator struct {
	valid bool
}

type transportAuthViewerResolver struct {
	calls int
	err   error
	order *[]string
}

func (r *transportAuthViewerResolver) Resolve(context.Context, access.ResolveInput) (access.Scope, error) {
	r.calls++
	if r.order != nil {
		*r.order = append(*r.order, "viewer")
	}
	return access.Scope{}, r.err
}

func (v transportAuthSessionValidator) IsValid(context.Context, string) (bool, error) {
	return v.valid, nil
}

func TestRequireTransportAuthAcceptsSessionCapabilityBeforeExpiredBearer(t *testing.T) {
	const secret = "transport-auth-test-secret"
	validator := &transportAuthTokenValidator{err: errors.New("expired access token")}
	middleware := NewAuthMiddleware(validator, transportAuthSessionValidator{}, nil, nil)
	token := signTransportAuthToken(t, secret, "playback-1", 7, 42, "direct", time.Hour)

	router := chi.NewRouter()
	handler := func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r.Context())
		if claims == nil || claims.UserID != 7 || claims.ProfileID != "profile-1" || claims.TokenType != auth.TokenTypeStream {
			t.Fatalf("auth claims = %#v, want stream user 7", claims)
		}
		streamClaims := GetTransportStreamClaims(r.Context())
		if streamClaims == nil || streamClaims.SessionID != "playback-1" || streamClaims.MediaFileID != 42 {
			t.Fatalf("stream claims = %#v, want bound playback recipe", streamClaims)
		}
		w.WriteHeader(http.StatusNoContent)
	}
	router.With(middleware.RequireTransportAuth(secret)).Get("/stream/{session_id}", handler)
	router.With(middleware.RequireTransportAuth(secret)).Head("/stream/{session_id}", handler)

	for _, carrier := range []struct {
		name  string
		apply func(*http.Request)
	}{
		{
			name: "query",
			apply: func(req *http.Request) {
				req.URL.RawQuery = streamtoken.QueryParameter + "=" + url.QueryEscape(token)
			},
		},
		{
			name: "header",
			apply: func(req *http.Request) {
				req.Header.Set(streamtoken.Header, token)
			},
		},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			req := httptest.NewRequest(method, "/stream/playback-1", nil)
			carrier.apply(req)
			req.Header.Set("Authorization", "Bearer expired-access-token")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("%s %s status = %d, body = %s", carrier.name, method, rec.Code, rec.Body.String())
			}
		}
	}
	if validator.calls != 0 {
		t.Fatalf("access validator calls = %d, want capability hot path", validator.calls)
	}
}

func TestRequireTransportAuthAcceptsVersionedPlaybackRecipes(t *testing.T) {
	const secret = "transport-auth-test-secret"
	for _, playMethod := range []string{
		streamtoken.PlayMethodToneMapTranscode,
		streamtoken.PlayMethodAudioDownmixTranscode,
		streamtoken.PlayMethodAudioDownmixRemux,
	} {
		t.Run(playMethod, func(t *testing.T) {
			validator := &transportAuthTokenValidator{err: errors.New("expired access token")}
			middleware := NewAuthMiddleware(validator, transportAuthSessionValidator{}, nil, nil)
			token := signTransportAuthToken(t, secret, "playback-1", 7, 42, playMethod, time.Hour)

			router := chi.NewRouter()
			router.With(middleware.RequireTransportAuth(secret)).Get("/stream/{session_id}", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "/stream/playback-1", nil)
			req.Header.Set(streamtoken.Header, token)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if validator.calls != 0 {
				t.Fatalf("access validator calls = %d, want capability hot path", validator.calls)
			}
		})
	}
}

func TestRequireTransportAuthAppliesViewerAccessOnlyToRegularAuthFallback(t *testing.T) {
	const secret = "transport-auth-test-secret"
	validator := &transportAuthTokenValidator{claims: &auth.Claims{
		UserID:    7,
		ProfileID: "profile-1",
		SessionID: "login-session-1",
		TokenType: auth.TokenTypeAccess,
	}}
	middleware := NewAuthMiddleware(validator, transportAuthSessionValidator{valid: true}, nil, nil)
	resolver := &transportAuthViewerResolver{err: access.ErrProfileUnverified}
	viewer := NewViewerAccessMiddleware(resolver)
	nextCalls := 0

	router := chi.NewRouter()
	router.With(middleware.RequireTransportAuth(secret), viewer.RequireTransportViewerAccess).Get("/stream/{session_id}", func(w http.ResponseWriter, _ *http.Request) {
		nextCalls++
		w.WriteHeader(http.StatusNoContent)
	})

	regularReq := httptest.NewRequest(http.MethodGet, "/stream/playback-1", nil)
	regularReq.Header.Set("Authorization", "Bearer access-token")
	regularReq.Header.Set("X-Profile-Id", "profile-1")
	regularRec := httptest.NewRecorder()
	router.ServeHTTP(regularRec, regularReq)
	if regularRec.Code != http.StatusForbidden || resolver.calls != 1 || nextCalls != 0 {
		t.Fatalf("regular fallback status=%d viewer_calls=%d next_calls=%d", regularRec.Code, resolver.calls, nextCalls)
	}

	capabilityReq := httptest.NewRequest(http.MethodGet, "/stream/playback-1", nil)
	capabilityReq.Header.Set(streamtoken.Header, signTransportAuthToken(t, secret, "playback-1", 7, 42, "direct", time.Hour))
	capabilityRec := httptest.NewRecorder()
	router.ServeHTTP(capabilityRec, capabilityReq)
	if capabilityRec.Code != http.StatusNoContent || resolver.calls != 1 || nextCalls != 1 {
		t.Fatalf("capability status=%d viewer_calls=%d next_calls=%d", capabilityRec.Code, resolver.calls, nextCalls)
	}
}

func TestTransportViewerAccessRunsAfterRateLimiting(t *testing.T) {
	validator := &transportAuthTokenValidator{claims: &auth.Claims{
		UserID:    7,
		ProfileID: "profile-1",
		SessionID: "login-session-1",
		TokenType: auth.TokenTypeAccess,
	}}
	middleware := NewAuthMiddleware(validator, transportAuthSessionValidator{valid: true}, nil, nil)
	order := []string{}
	resolver := &transportAuthViewerResolver{order: &order}
	viewer := NewViewerAccessMiddleware(resolver)
	rateLimit := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "rate")
			next.ServeHTTP(w, r)
		})
	}

	router := chi.NewRouter()
	router.With(middleware.RequireTransportAuth("transport-auth-test-secret"), rateLimit, viewer.RequireTransportViewerAccess).Get("/stream/{session_id}", func(w http.ResponseWriter, _ *http.Request) {
		order = append(order, "handler")
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/stream/playback-1", nil)
	req.Header.Set("Authorization", "Bearer access-token")
	req.Header.Set("X-Profile-Id", "profile-1")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || len(order) != 3 || order[0] != "rate" || order[1] != "viewer" || order[2] != "handler" {
		t.Fatalf("status=%d middleware_order=%v", rec.Code, order)
	}
}

func TestRequireTransportAuthRejectsInvalidCapabilityAndFallsBack(t *testing.T) {
	const secret = "transport-auth-test-secret"
	tests := []struct {
		name  string
		token string
	}{
		{
			name:  "another session",
			token: signTransportAuthToken(t, secret, "playback-other", 7, 42, "direct", time.Hour),
		},
		{
			name:  "download capability",
			token: signTransportAuthToken(t, secret, "playback-1", 7, 42, streamtoken.PlayMethodDownload, time.Hour),
		},
		{
			name:  "expired capability",
			token: signTransportAuthToken(t, secret, "playback-1", 7, 42, "direct", -time.Hour),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator := &transportAuthTokenValidator{err: errors.New("expired access token")}
			middleware := NewAuthMiddleware(validator, transportAuthSessionValidator{}, nil, nil)
			router := chi.NewRouter()
			router.With(middleware.RequireTransportAuth(secret)).Get("/stream/{session_id}", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})

			req := httptest.NewRequest(http.MethodGet, "/stream/playback-1", nil)
			req.Header.Set(streamtoken.Header, test.token)
			req.Header.Set("Authorization", "Bearer expired-access-token")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if validator.calls != 1 {
				t.Fatalf("access validator calls = %d, want one fallback attempt", validator.calls)
			}
		})
	}
}

func signTransportAuthToken(t *testing.T, secret, sessionID string, userID, mediaFileID int, playMethod string, ttl time.Duration) string {
	t.Helper()
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:   sessionID,
		UserID:      userID,
		ProfileID:   "profile-1",
		MediaFileID: mediaFileID,
		PlayMethod:  playMethod,
	}, secret, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

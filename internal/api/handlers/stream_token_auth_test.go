package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

const streamAuthTestSecret = "test-stream-secret"

// rejectingTokenValidator fails every bearer/JWT validation, so the end-to-end
// test's only successful auth path is the ?st= stream token.
type rejectingTokenValidator struct{}

func (rejectingTokenValidator) ValidateToken(string) (*auth.Claims, error) {
	return nil, errors.New("no bearer path in this test")
}

// alwaysValidSession treats every session as valid (unused by the ?st= path).
type alwaysValidSession struct{}

func (alwaysValidSession) IsValid(context.Context, string) (bool, error) { return true, nil }

// signTestStreamToken mints a signed ?st= token for a direct-play session card.
func signTestStreamToken(t *testing.T, sessionID string, userID int, profileID string, ttl time.Duration) string {
	t.Helper()
	card := playback.NewDirectRecipeCard(sessionID, userID, profileID, 55)
	tok, err := streamtoken.Sign(card.ToClaims(), streamAuthTestSecret, ttl)
	if err != nil {
		t.Fatalf("sign stream token: %v", err)
	}
	return tok
}

// streamTokenReq builds a request carrying ?st=<token> with the chi route param
// session_id set to pathSession (what the route matched), mirroring how the
// middleware sees a real request.
func streamTokenReq(pathSession, token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/"+pathSession+"?st="+token, nil)
	return withPlaybackRouteParam(req, "session_id", pathSession)
}

func TestNewStreamTokenAuthenticator_ValidTokenAuthenticates(t *testing.T) {
	authFn := NewStreamTokenAuthenticator(streamAuthTestSecret)
	tok := signTestStreamToken(t, "sess-abc", 42, "profile-9", time.Hour)

	claims, profileID, ok := authFn(streamTokenReq("sess-abc", tok))
	if !ok {
		t.Fatal("expected valid ?st= token to authenticate")
	}
	if claims == nil || claims.UserID != 42 {
		t.Fatalf("claims user id = %v, want 42", claims)
	}
	if claims.SessionID != "sess-abc" {
		t.Fatalf("claims session id = %q, want sess-abc", claims.SessionID)
	}
	if claims.Role != "" {
		t.Fatalf("claims role = %q, want empty (a ?st= token grants no role)", claims.Role)
	}
	if profileID != "profile-9" {
		t.Fatalf("profile id = %q, want profile-9", profileID)
	}
}

func TestNewStreamTokenAuthenticator_ExpiredTokenRejected(t *testing.T) {
	authFn := NewStreamTokenAuthenticator(streamAuthTestSecret)
	tok := signTestStreamToken(t, "sess-abc", 42, "profile-9", -time.Minute) // already expired

	if _, _, ok := authFn(streamTokenReq("sess-abc", tok)); ok {
		t.Fatal("expected expired ?st= token to be rejected")
	}
}

func TestNewStreamTokenAuthenticator_SessionMismatchRejected(t *testing.T) {
	authFn := NewStreamTokenAuthenticator(streamAuthTestSecret)
	// Token minted for sess-abc, but the path matched sess-other.
	tok := signTestStreamToken(t, "sess-abc", 42, "profile-9", time.Hour)

	if _, _, ok := authFn(streamTokenReq("sess-other", tok)); ok {
		t.Fatal("expected a ?st= token bound to a different session to be rejected")
	}
}

func TestNewStreamTokenAuthenticator_WrongSecretRejected(t *testing.T) {
	authFn := NewStreamTokenAuthenticator("a-different-secret")
	tok := signTestStreamToken(t, "sess-abc", 42, "profile-9", time.Hour)

	if _, _, ok := authFn(streamTokenReq("sess-abc", tok)); ok {
		t.Fatal("expected a ?st= token signed with a different secret to be rejected")
	}
}

func TestNewStreamTokenAuthenticator_MissingTokenRejected(t *testing.T) {
	authFn := NewStreamTokenAuthenticator(streamAuthTestSecret)
	req := withPlaybackRouteParam(httptest.NewRequest(http.MethodGet, "/api/v1/stream/sess-abc", nil), "session_id", "sess-abc")

	if _, _, ok := authFn(req); ok {
		t.Fatal("expected a request with no ?st= token to be rejected")
	}
}

// TestStreamTokenAuthenticator_EndToEndThroughRequireStreamAuth wires the real
// authenticator into RequireStreamAuth on a real chi router and confirms a
// valid ?st= token alone reaches the protected handler with the correct
// identity, while an expired / wrong-session / absent token gets a 401.
func TestStreamTokenAuthenticator_EndToEndThroughRequireStreamAuth(t *testing.T) {
	// A bearer path that rejects everything, so any success is via ?st=.
	am := apimw.NewAuthMiddleware(rejectingTokenValidator{}, alwaysValidSession{}, nil, nil)

	var gotUser int
	var gotProfile string
	var reached bool
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		gotUser = apimw.GetUserID(r.Context())
		gotProfile = apimw.GetProfileID(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	mux := chi.NewRouter()
	mux.With(am.RequireStreamAuth(NewStreamTokenAuthenticator(streamAuthTestSecret))).
		Get("/api/v1/stream/{session_id}", probe)

	t.Run("valid token authenticates with correct identity", func(t *testing.T) {
		reached, gotUser, gotProfile = false, 0, ""
		tok := signTestStreamToken(t, "sess-e2e", 77, "profile-e2e", time.Hour)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/stream/sess-e2e?st="+tok, nil))

		if !reached || rr.Code != http.StatusOK {
			t.Fatalf("expected 200 reaching handler; reached=%v code=%d body=%s", reached, rr.Code, rr.Body.String())
		}
		if gotUser != 77 || gotProfile != "profile-e2e" {
			t.Fatalf("identity = (user %d, profile %q), want (77, profile-e2e)", gotUser, gotProfile)
		}
	})

	t.Run("expired token 401s", func(t *testing.T) {
		reached = false
		tok := signTestStreamToken(t, "sess-e2e", 77, "profile-e2e", -time.Minute)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/stream/sess-e2e?st="+tok, nil))
		if reached || rr.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 without reaching handler; reached=%v code=%d", reached, rr.Code)
		}
	})

	t.Run("wrong-session token 401s", func(t *testing.T) {
		reached = false
		tok := signTestStreamToken(t, "sess-other", 77, "profile-e2e", time.Hour)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/stream/sess-e2e?st="+tok, nil))
		if reached || rr.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for wrong-session token; reached=%v code=%d", reached, rr.Code)
		}
	})

	t.Run("no credentials 401s", func(t *testing.T) {
		reached = false
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/stream/sess-e2e", nil))
		if reached || rr.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 with no credentials; reached=%v code=%d", reached, rr.Code)
		}
	})
}

// TestPlaybackRouteScoping_StreamTokenReachesDeliveryButNotControlOrMutations
// reproduces the router's /playback nesting (an ?st=-capable delivery subgroup
// alongside a RequireAuth-only subgroup for capability, control, and mutation
// routes) to guard two things at once: that the chi route tree builds without
// conflict, and that a ?st= token reaches ONLY the HLS delivery routes — never
// the capability probe or a mutation route co-located under /playback.
func TestPlaybackRouteScoping_StreamTokenReachesDeliveryButNotControlOrMutations(t *testing.T) {
	// Bearer path rejects everything, so success can only come from ?st=.
	am := apimw.NewAuthMiddleware(rejectingTokenValidator{}, alwaysValidSession{}, nil, nil)
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

	mux := chi.NewRouter()
	mux.Route("/api/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Route("/playback", func(r chi.Router) {
				// Delivery subgroup: bearer OR scoped ?st=.
				r.Group(func(r chi.Router) {
					r.Use(am.RequireStreamAuth(NewStreamTokenAuthenticator(streamAuthTestSecret)))
					r.Get("/transcode/{session_id}/master.m3u8", ok)
					r.Get("/transcode/{session_id}/segment/{name}", ok)
				})
				// Non-delivery subgroup: bearer only.
				r.Group(func(r chi.Router) {
					r.Use(am.RequireAuth)
					r.Get("/capability", ok)
					r.Get("/sessions/{session_id}/control/ws", ok)
					r.Group(func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Post("/start", ok)
					})
				})
			})
		})
	})

	tok := signTestStreamToken(t, "sess-scope", 5, "profile-scope", time.Hour)

	cases := []struct {
		name, method, path string
		want               int
	}{
		{"delivery manifest accepts st", http.MethodGet, "/api/v1/playback/transcode/sess-scope/master.m3u8?st=" + tok, http.StatusOK},
		{"delivery segment accepts st", http.MethodGet, "/api/v1/playback/transcode/sess-scope/segment/seg_0.m4s?st=" + tok, http.StatusOK},
		{"capability rejects st", http.MethodGet, "/api/v1/playback/capability?st=" + tok, http.StatusUnauthorized},
		{"control ws rejects st", http.MethodGet, "/api/v1/playback/sessions/sess-scope/control/ws?st=" + tok, http.StatusUnauthorized},
		{"mutation start rejects st", http.MethodPost, "/api/v1/playback/start?st=" + tok, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

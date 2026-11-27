package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestProxyStreamTokenChecksDemoSessionStanding(t *testing.T) {
	path := writeGrantMedia(t, "0123456789")

	card := playback.RecipeCard{
		SessionID:         "play-demo-1",
		UserID:            7,
		ProfileID:         "profile-1",
		MediaFileID:       42,
		DemoAuthSessionID: "demo-live",
		PlayMethod:        playback.PlayDirect,
		InputPath:         path,
	}
	claims := card.ToClaims()
	if claims.DemoAuthSessionID != "demo-live" {
		t.Fatalf("ToClaims().DemoAuthSessionID = %q, want demo-live", claims.DemoAuthSessionID)
	}
	if roundTrip := playback.RecipeCardFromClaims(&claims); roundTrip.DemoAuthSessionID != "demo-live" {
		t.Fatalf("RecipeCardFromClaims().DemoAuthSessionID = %q, want demo-live", roundTrip.DemoAuthSessionID)
	}

	sign := func(dsid string) string {
		t.Helper()
		c := card.ToClaims()
		c.DemoAuthSessionID = dsid
		tok, err := streamtoken.Sign(c, grantTestSecret, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}

	srv := NewServer(testWatcher(grantTestSecret), nodesessions.NewTracker(nil, "http://proxy-1", "proxy-1", "proxy"))
	srv.SetNodeKeys(nodeauthtest.NodeKey(grantTestSecret))
	srv.SetMediaGrantAuthority(stubGrantStore{}, stubLoginSessions{valid: map[string]bool{"demo-live": true}})

	t.Run("active demo session serves direct play", func(t *testing.T) {
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/stream/direct/"+sign("demo-live"), nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("active demo token status = %d %s, want 200", rr.Code, rr.Body.String())
		}
	})

	t.Run("revoked or disabled demo session is rejected", func(t *testing.T) {
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/stream/direct/"+sign("demo-revoked"), nil))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("revoked demo token status = %d, want 401", rr.Code)
		}
	})

	t.Run("store error answers retryable 503", func(t *testing.T) {
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/stream/direct/"+sign(loginStoreDown), nil))
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("store error status = %d, want 503", rr.Code)
		}
		if rr.Header().Get("Retry-After") == "" {
			t.Fatal("missing Retry-After on 503")
		}
	})

	t.Run("proxy without login session authority fails closed for demo tokens", func(t *testing.T) {
		noAuthSrv := NewServer(testWatcher(grantTestSecret), nodesessions.NewTracker(nil, "http://proxy-1", "proxy-1", "proxy"))
		noAuthSrv.SetNodeKeys(nodeauthtest.NodeKey(grantTestSecret))
		rr := httptest.NewRecorder()
		noAuthSrv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/stream/direct/"+sign("demo-live"), nil))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("unwired proxy demo token status = %d, want 401", rr.Code)
		}

		// Non-demo stream tokens still work without loginSessions.
		rr2 := httptest.NewRecorder()
		noAuthSrv.Handler().ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/stream/direct/"+sign(""), nil))
		if rr2.Code != http.StatusOK {
			t.Fatalf("ordinary token on unwired proxy status = %d, want 200", rr2.Code)
		}
	})
}

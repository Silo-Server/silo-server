package jellycompat

import (
	"slices"

	"github.com/Silo-Server/silo-server/internal/models"

	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

func TestCompatAPIKeyAccountRequiresCurrentEnabledOwner(t *testing.T) {
	for _, sessionPath := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("session=%v/enabled=%v", sessionPath, enabled), func(t *testing.T) {
				keys := &fakeAPIKeyValidator{key: &models.APIKey{ID: 7, UserID: 2, Key: "sa_test"}}
				users := &fakeAPIKeyUserLoader{user: &models.User{ID: 2, Role: "admin", Enabled: enabled}}
				_, provider, _ := adminKeyAuthWithPrimary(t, fixedNow)
				keyAuth := NewAdminAPIKeyAuthenticator(keys, users, provider, fixedNow)
				limiter := &accountRecorder{}
				mw := ratelimit.NewMiddleware(limiter, limiter, &rateLimitSettings{values: map[string]string{}}, false)
				if err := mw.Init(t.Context()); err != nil {
					t.Fatal(err)
				}
				wrap := keyAuth.RequireAdminAPIKey
				if sessionPath {
					wrap = RequireSessionOrAPIKeySession(NewAuthenticator(NewSessionStore(time.Hour, nil), nil), keyAuth)
				}
				probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if apimw.GetClaims(r.Context()) != nil {
						t.Fatal("compat must not grant native claims")
					}
					w.WriteHeader(204)
				})
				rec := httptest.NewRecorder()
				wrap(compatRateLimit(mw)(probe)).ServeHTTP(rec, apiKeyRequest())
				if enabled {
					if rec.Code != 204 || !slices.Equal(limiter.keys, []string{"account:2", "global"}) {
						t.Fatalf("status=%d, calls=%v", rec.Code, limiter.keys)
					}
				} else if rec.Code != 401 || len(limiter.keys) != 0 {
					t.Fatalf("disabled owner: status=%d, calls=%v", rec.Code, limiter.keys)
				}
			})
		}
	}
}

func (*accountRecorder) Close() {}

func (l *accountRecorder) Allow(_ context.Context, key string, _ ratelimit.Rate) ratelimit.AllowResult {
	l.keys = append(l.keys, key)
	return ratelimit.AllowResult{Allowed: true}
}

type accountRecorder struct{ keys []string }

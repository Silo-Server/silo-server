package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestOAuthNativeCapabilityFollowsPublicURL(t *testing.T) {
	deps := pilotDeps(nil, nil)
	svc := auth.NewOAuthHandler(auth.OAuthHandlerDeps{})
	deps.OAuth = svc
	h := newTestHandler(t, deps)
	for _, tc := range []struct {
		url, want string
	}{
		{"", `"native":false`},
		{"https://silo.example.test", `"native":true`},
		{"", `"native":false`},
	} {
		svc.SetHostBaseURL(tc.url)
		rec := do(t, h, http.MethodGet, Prefix+"/auth/oauth/capabilities", "", nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("capabilities for public URL %q: %d %s", tc.url, rec.Code, rec.Body.String())
		}
	}
}

type oauthLinkUsers struct{}

func (oauthLinkUsers) GetByID(context.Context, int) (*models.User, error) {
	return &models.User{ID: 1, Enabled: true, LocalPasswordLoginEnabled: true}, nil
}

func TestOAuthLinkResolverProblemClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want ProblemType
	}{
		{"missing provider", auth.ErrUnknownAuthInstallation, TypeNotFound},
		{"unavailable plugin", errors.New("plugin connection unavailable"), TypeProviderUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := auth.NewInMemoryOAuthStore()
			svc := auth.NewOAuthHandler(auth.OAuthHandlerDeps{
				Store: store, Users: oauthLinkUsers{}, HostBaseURL: "https://silo.example.test",
				ResolveClient: func(context.Context, int) (auth.OAuthClient, string, error) {
					return nil, "", tc.err
				},
			})
			deps := pilotDeps(nil, nil)
			deps.OAuth = svc
			h := newTestHandler(t, deps)
			requireProblem(t, do(t, h, http.MethodPost, Prefix+"/account/identities/link-ticket",
				`{"installation_id":"3","password":"right password"}`, bearer(memberToken)), tc.want)
			if err := store.InsertLinkTicket(t.Context(), auth.OAuthLinkTicket{
				Ticket: "resolver-ticket", UserID: 1, InstallationID: 3, ExpiresAt: time.Now().Add(time.Minute),
			}); err != nil {
				t.Fatal(err)
			}
			requireProblem(t, do(t, h, http.MethodPost, "https://silo.example.test"+Prefix+"/account/identities/link-start",
				`{"link_ticket":"resolver-ticket","next":"/settings/account"}`, bearer(memberToken)), tc.want)
		})
	}
}

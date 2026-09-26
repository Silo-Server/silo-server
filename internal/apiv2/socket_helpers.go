package apiv2

import (
	"context"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/danielgtaylor/huma/v2"
)

// socketIdentity captures the login session and profile proof that a one-use
// ticket delegates. API keys cannot delegate a login session.
func socketIdentity(ctx context.Context) (evt.SocketIdentity, *Problem) {
	claims := claimsFrom(ctx)
	if claims == nil || claims.TokenType != auth.TokenTypeAccess || claims.SessionID == "" || claims.ExpiresAt == nil || !claims.ExpiresAt.After(time.Now()) {
		return evt.SocketIdentity{}, NewProblem(TypePermissionDenied, "A current login session is required.")
	}
	identity := evt.SocketIdentity{ImpersonatorUserID: claims.ImpersonatorUserID, UserID: claims.UserID, SessionID: claims.SessionID, Role: claims.Role, ProfileID: profileFrom(ctx), AccessExpiresAt: claims.ExpiresAt.Time}
	if r := requestFrom(ctx); r != nil {
		identity.ProfileToken = r.Header.Get(profileTokenHeader)
	}
	return identity, nil
}

func socketResponses(statuses []string, refused, established, headerDescription string) map[string]*huma.Response {
	responses := map[string]*huma.Response{}
	for _, status := range statuses {
		responses[status] = &huma.Response{Description: refused, Content: map[string]*huma.MediaType{eventsPlainMedia: {Schema: &huma.Schema{Type: huma.TypeString}}}}
	}
	responses["101"] = &huma.Response{Description: established, Headers: map[string]*huma.Param{}}
	for _, header := range []string{eventsConnectionHeader, eventsUpgradeHeader, eventsAcceptHeader, eventsProtocolHeader} {
		responses["101"].Headers[header] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeString}, Description: headerDescription}
	}
	return responses
}

func socketHandler(handler http.Handler, unavailableMessage string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if handler == nil {
			http.Error(w, unavailableMessage, http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

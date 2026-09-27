package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// PluginLaunchService is the slice of *handlers.AuthHandler the v2 plugin
// launch uses: it mints the same five-minute plugin access token v1 issues.
type PluginLaunchService interface {
	PluginLaunchToken(claims *auth.Claims, profileID string) (string, error)
}

// PluginLaunch is the launch receipt: the cookie's lifetime in seconds.
type PluginLaunch struct {
	ExpiresIn int `json:"expires_in" doc:"Lifetime of the plugin access cookie in seconds" example:"300"`
}

// PluginLaunchOutput carries the receipt and the plugin access cookie. The
// cookie is scoped to the v2 plugin-content parent path, never to /.
type PluginLaunchOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie" doc:"silo_plugin_access on the v2 plugin-content parent path: five minutes, HttpOnly, SameSite=Lax, Secure on HTTPS"`
	Body      PluginLaunch
}

// pluginLaunchCookie builds the v2 plugin access cookie. Every attribute
// except the path matches the v1 cookie (docs/architecture/api-contract.md,
// "Credential continuity"): the path is the narrow v2 plugin-content parent,
// so the credential reaches plugin routes and assets and nothing else.
func pluginLaunchCookie(token string, secure bool) http.Cookie {
	return http.Cookie{
		Name:     auth.PluginAccessCookieName,
		Value:    token,
		Path:     plugins.ContentPrefix,
		MaxAge:   int(handlers.PluginLaunchTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	}
}

func registerPluginLaunch(reg *Registry) {
	op := humaOp(http.MethodPost, Prefix+"/auth/plugin-launch", "createPluginLaunch", "auth", "Issue the five-minute plugin access cookie on the v2 plugin-content parent path for the current login session and optional validated profile. Repeating the request reissues an equivalent cookie.")
	// An API key or any credential without a login session cannot launch a
	// plugin: the cookie delegates a session, so there must be one.
	op.Errors = []int{http.StatusForbidden}
	Register(reg, Operation{Operation: op, Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}, func(ctx context.Context, _ *struct{}) (*PluginLaunchOutput, error) {
		if reg.deps.PluginLaunch == nil {
			return nil, unavailable("plugin launch")
		}
		claims := claimsFrom(ctx)
		if claims == nil || claims.SessionID == "" || claims.TokenType != auth.TokenTypeAccess {
			return nil, NewProblem(TypePermissionDenied, "A current login session is required to launch a plugin.")
		}
		token, err := reg.deps.PluginLaunch.PluginLaunchToken(claims, profileFrom(ctx))
		if err != nil {
			return nil, serviceProblem(err)
		}
		secure := false
		if r := requestFrom(ctx); r != nil {
			secure = handlers.IsSecureRequest(r)
		}
		return &PluginLaunchOutput{SetCookie: pluginLaunchCookie(token, secure), Body: PluginLaunch{ExpiresIn: int(handlers.PluginLaunchTTL.Seconds())}}, nil
	})
}

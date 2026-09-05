package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
)

// The auth domain: opening and closing login sessions. The token pair a
// login issues is TokenPair (device_login.go); every response carrying it is
// Cache-Control: no-store, the listener default.

// LoginInput is a password login.
type LoginInput struct {
	Body struct {
		Username string `json:"username" minLength:"1" maxLength:"254" doc:"Login name or, for providers that accept it, email" example:"alice"`
		Password string `json:"password" minLength:"1" maxLength:"1024" doc:"Account password" example:"correct horse battery staple"`
		Provider string `json:"provider,omitempty" doc:"Authentication provider id exactly as listAuthProviders advertises it; unbounded because plugin ids are composite. Empty selects the default" example:""`
	}
}

// LoginOutput is the login response.
type LoginOutput struct {
	Body TokenPair
}

// CompleteOAuthLoginInput redeems the one-time code the OAuth callback
// redirected the browser with.
type CompleteOAuthLoginInput struct {
	Body struct {
		Code string `json:"code" minLength:"1" maxLength:"128" doc:"Completion code from the callback redirect; single use" example:"3f2b47eb7b36dd2d"`
	}
}

// OAuthCompletion is the credential the code redeemed, plus where the
// browser meant to go.
type OAuthCompletion struct {
	AccessToken  string `json:"access_token" doc:"Bearer access token" example:"eyJhbGciOi..."`
	RefreshToken string `json:"refresh_token" doc:"Refresh token for POST /auth/refresh" example:"eyJhbGciOi..."`
	ExpiresIn    int    `json:"expires_in" doc:"Access token lifetime in seconds" example:"3600"`
	Next         string `json:"next" doc:"Site-relative path the login started from; / when none was given" example:"/"`
}

// CompleteOAuthLoginOutput is the completeOAuthLogin response.
type CompleteOAuthLoginOutput struct {
	Body OAuthCompletion
}

// loginDomain names the login-session service in problems and the v1 rate
// limit bucket of the login operation.
const loginDomain = "login"

func registerAuth(reg *Registry) {
	end := humaOp(http.MethodPost, Prefix+"/auth/impersonation/end", "endImpersonation", "auth",
		"Return an impersonating session to the administrator who started it.")
	// v1 answers 400 not_impersonating when the session is not impersonating
	// anyone; on v2 that is a 409 conflict with the session's state, and a
	// refused return is 403.
	end.Errors = []int{http.StatusForbidden, http.StatusConflict}
	Register(reg, Operation{Operation: end, Class: ClassAuthenticated, ServiceBacked: true}, reg.endImpersonation)
	login := humaOp(http.MethodPost, Prefix+"/auth/login", "login", "auth",
		"Authenticate with a username and password and open a login session.")
	// Wrong credentials are 401 invalid_token; a disabled account is 403.
	login.Errors = []int{http.StatusUnauthorized, http.StatusForbidden}
	Register(reg, Operation{
		Operation: login,
		Class:     ClassPublic, ServiceBacked: true, RateLimitBucket: loginDomain,
	}, reg.login)
	complete := humaOp(http.MethodPost, Prefix+"/auth/oauth/complete", "completeOAuthLogin", "auth",
		"Redeem the one-time code an OAuth callback issued for the token pair.")
	// An unknown, used or expired code is 401 invalid_token.
	complete.Errors = []int{http.StatusUnauthorized}
	Register(reg, Operation{Operation: complete, Class: ClassPublic, ServiceBacked: true}, reg.completeOAuthLogin)
	logout := humaOp(http.MethodPost, Prefix+"/auth/logout", "logout", "auth",
		"Revoke the caller's login session.")
	// An API key passes the gate but owns no login session: 403 (v1: 401,
	// because its logout only accepts a JWT).
	logout.Errors = []int{http.StatusForbidden}
	Register(reg, Operation{Operation: logout, Class: ClassAuthenticated, ServiceBacked: true}, reg.logout)
}

func (reg *Registry) login(ctx context.Context, in *LoginInput) (*LoginOutput, error) {
	if reg.deps.Sessions == nil {
		return nil, unavailable(loginDomain)
	}
	input := handlers.LoginInput{
		Provider: in.Body.Provider,
		Username: in.Body.Username,
		Password: in.Body.Password,
		IP:       clientip.FromContext(ctx),
	}
	if r := requestFrom(ctx); r != nil {
		input.DeviceName = r.UserAgent()
	}
	view, err := reg.deps.Sessions.Login(ctx, input)
	if err != nil {
		return nil, loginProblem(err)
	}
	return &LoginOutput{Body: tokenPairFromView(view)}, nil
}

func (reg *Registry) logout(ctx context.Context, _ *struct{}) (*struct{}, error) {
	if reg.deps.Sessions == nil {
		return nil, unavailable(loginDomain)
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	// The gate admits an API key with no session id. v1 refuses one with 401
	// because its logout re-validates the bearer as a JWT; here the credential
	// was accepted, so the refusal is a permission problem rather than a
	// session-not-found 500 from revoking "".
	if claims.TokenType == auth.TokenTypeAPIKey || claims.SessionID == "" {
		return nil, NewProblem(TypePermissionDenied, "API keys cannot end a login session.")
	}
	if err := reg.deps.Sessions.Logout(ctx, claims); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

func (reg *Registry) endImpersonation(ctx context.Context, _ *struct{}) (*struct{}, error) {
	if reg.deps.Sessions == nil {
		return nil, unavailable(loginDomain)
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if err := reg.deps.Sessions.EndImpersonation(ctx, claims); err != nil {
		return nil, impersonationProblem(err)
	}
	return nil, nil
}

// completeOAuthLogin answers from the same completion store v1 POST
// /auth/oauth/complete redeems; the token pair carries no account view
// because the callback stored none (the client follows with getCurrentUser).
func (reg *Registry) completeOAuthLogin(ctx context.Context, in *CompleteOAuthLoginInput) (*CompleteOAuthLoginOutput, error) {
	if reg.deps.OAuth == nil {
		return nil, unavailable("oauth login")
	}
	c, err := reg.deps.OAuth.Complete(ctx, in.Body.Code)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrOAuthCompletionUnavailable):
			return nil, unavailable("oauth login")
		case errors.Is(err, auth.ErrOAuthCompletionInvalid):
			return nil, NewProblem(TypeInvalidToken, "The completion code is invalid, used, or expired.")
		case errors.Is(err, auth.ErrOAuthCodeRequired):
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationBody + ".code", Code: codeRequired, Detail: "A completion code is required."})
		}
		return nil, serviceProblem(err)
	}
	return &CompleteOAuthLoginOutput{Body: OAuthCompletion{AccessToken: c.AccessToken, RefreshToken: c.RefreshToken, ExpiresIn: c.ExpiresIn, Next: c.NextURL}}, nil
}

// loginProblem renders a login failure: v1's 401 invalid_credentials is the
// invalid_token type (the status default is authentication_required, which
// would tell the client to present a credential it just presented).
func loginProblem(err error) *Problem {
	apiErr, ok := err.(*handlers.APIError) //nolint:errorlint // Login returns the value directly
	if ok && apiErr.Status == http.StatusUnauthorized {
		return NewProblem(TypeInvalidToken, apiErr.Message+".")
	}
	return serviceProblem(err)
}

// impersonationProblem renders v1's 400 not_impersonating as the conflict
// it is: the request is well formed, the session is simply not impersonating.
func impersonationProblem(err error) *Problem {
	apiErr, ok := err.(*handlers.APIError) //nolint:errorlint // EndImpersonation returns the value directly
	if ok && apiErr.Status == http.StatusBadRequest {
		return NewProblem(TypeConflict, apiErr.Message+".")
	}
	return serviceProblem(err)
}

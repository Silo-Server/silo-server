package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
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
		Provider string `json:"provider,omitempty" maxLength:"64" doc:"Authentication provider id from listAuthProviders; empty selects the default" example:""`
	}
}

// LoginOutput is the login response.
type LoginOutput struct {
	Body TokenPair
}

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
		Class:     ClassPublic, ServiceBacked: true, RateLimitBucket: "login",
	}, reg.login)
	Register(reg, Operation{
		Operation: humaOp(http.MethodPost, Prefix+"/auth/logout", "logout", "auth",
			"Revoke the caller's login session."),
		Class: ClassAuthenticated, ServiceBacked: true,
	}, reg.logout)
}

func (reg *Registry) login(ctx context.Context, in *LoginInput) (*LoginOutput, error) {
	if reg.deps.Sessions == nil {
		return nil, unavailable("login")
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
		return nil, unavailable("login")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if err := reg.deps.Sessions.Logout(ctx, claims); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

func (reg *Registry) endImpersonation(ctx context.Context, _ *struct{}) (*struct{}, error) {
	if reg.deps.Sessions == nil {
		return nil, unavailable("login")
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

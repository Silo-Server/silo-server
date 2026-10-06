package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
)

// The auth domain: opening and closing login sessions. The token pair a
// login issues is TokenPair (device_login.go); every response carrying it is
// Cache-Control: no-store, the listener default.

// LoginInput is a password login.
type LoginInput struct {
	RawBody []byte
	Body    struct {
		Username string `json:"username" minLength:"1" maxLength:"254" doc:"Login name or, for providers that accept it, email" example:"alice"`
		Password string `json:"password" minLength:"1" maxLength:"1024" doc:"Account password" example:"correct horse battery staple"`
		Provider string `json:"provider,omitempty" doc:"Authentication provider id exactly as listAuthProviders advertises it; unbounded because plugin ids are composite. Empty routes by account: a name that may use its local password signs in locally, any other name goes to the enabled directory (LDAP) provider" example:""`
	}
}

// LoginOutput is the login response.
type LoginOutput struct {
	Body TokenPair
}

// NetworkSignInInput signs in the overlay peer of the request. The body is
// an empty JSON object: requiring it makes a cross-site form post fail the
// media-type check, so a page the person visits cannot sign their device in.
type NetworkSignInInput struct {
	ID      ID `path:"id" pattern:"^[1-9][0-9]*$" doc:"The network provider's plugin installation, as listAuthProviders shows it" example:"5"`
	RawBody []byte
	Body    struct{}
}

// networkSignInPath is the signInWithNetworkIdentity path of an
// installation, below the server base.
func networkSignInPath(installationID int) string {
	return Prefix + "/auth/network/" + strconv.Itoa(installationID) + "/sign-in"
}

// CompleteOAuthLoginInput redeems the one-time code the OAuth callback
// redirected the browser with.
type CompleteOAuthLoginInput struct {
	Body struct {
		Code         string `json:"code" minLength:"1" maxLength:"128" doc:"Completion code from the callback redirect; single use" example:"3f2b47eb7b36dd2d"`
		CodeVerifier string `json:"code_verifier,omitempty" maxLength:"128" doc:"PKCE code verifier of the challenge a native start sent (RFC 7636: 43 to 128 unreserved characters). Required for a code from startNativeOAuthLogin, refused for a web code; either mismatch is 400 invalid_grant and leaves the code redeemable" example:"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"`
	}
	Browser string `cookie:"silo_oauth_complete" doc:"Completion cookie the callback set in the browser it sent to the web completion page (HttpOnly, path-scoped to this operation, two minutes). The browser sends it by itself; a web code redeemed without it, or with another browser's, is 400 invalid_grant and stays redeemable. Native codes ignore it"`
}

// OAuthCompletion is the credential the code redeemed, plus where the
// browser meant to go.
type OAuthCompletion struct {
	AccessToken  string  `json:"access_token" doc:"Bearer access token" example:"eyJhbGciOi..."`
	RefreshToken string  `json:"refresh_token" doc:"Refresh token for POST /auth/refresh" example:"eyJhbGciOi..."`
	ExpiresIn    int     `json:"expires_in" doc:"Access token lifetime in seconds" example:"3600"`
	Next         string  `json:"next" doc:"Site-relative path the login started from; / when none was given" example:"/"`
	User         Account `json:"user" doc:"The account the tokens authenticate"`
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
	Register(reg, Operation{Operation: end, RetrySafety: RetrySafetyNaturalIdempotent, Class: ClassAuthenticated, ServiceBacked: true}, reg.endImpersonation)
	login := humaOp(http.MethodPost, Prefix+"/auth/login", "login", "auth",
		"Authenticate with a username and password and open a login session.")
	// Wrong credentials are 401 invalid_token; a disabled account, turned-off
	// local password sign-in (local_login_disabled), a directory refusal
	// (not_permitted, password_expired) or a network provider's refusal of
	// the account's person (not_permitted) is 403, and so is a directory sign-in
	// with no account while account creation is off (account_required); an
	// account a directory sign-in cannot create or link is 409 (email_in_use,
	// identity_linked_elsewhere).
	login.Errors = []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict}
	Register(reg, Operation{
		Operation:   login,
		RetrySafety: RetrySafetyNonRetryable,
		Class:       ClassPublic, ServiceBacked: true, RateLimitBucket: loginDomain,
	}, reg.login)
	complete := humaOp(http.MethodPost, Prefix+"/auth/oauth/complete", "completeOAuthLogin", "auth",
		"Redeem the one-time code an OAuth callback issued for the token pair.")
	complete.Description = "A code is valid for 60 seconds and redeems once. The login session opens at redemption, so a code that is never redeemed leaves no session. Redeeming a used code again revokes the session its redemption opened. A native code needs the code_verifier of its S256 challenge. A web code takes none and redeems only in the browser the callback answered, which holds the silo_oauth_complete cookie, so a code passed to another browser cannot sign that browser in."
	// An unknown, used or expired code is 401 invalid_token; a verifier
	// that does not fit the code is 400 invalid_grant.
	complete.Errors = []int{http.StatusBadRequest, http.StatusUnauthorized}
	Register(reg, Operation{Operation: complete, RetrySafety: RetrySafetyNonRetryable, Class: ClassPublic, ServiceBacked: true}, reg.completeOAuthLogin)
	network := humaOp(http.MethodPost, Prefix+"/auth/network/{id}/sign-in", "signInWithNetworkIdentity", "auth",
		"Sign in the owner of this device through a network identity provider and open a login session.")
	network.Description = "For a network provider (mode network in listAuthProviders), such as the Tailscale network access plugin: the provider's network already knows who owns the device a request came from, so there is no password and no browser. Only a request that arrived through that provider's own network address can sign in; listAuthProviders lists the provider, with the owner's name, only to such a request. The provider's plugin is asked who the device belongs to, and the answer goes through the same account resolution as other providers: a linked account signs in, an unknown person gets a new account while the binding's account creation is on, and the provider may set the account's role. The session is re-checked with the provider like other provider sessions. Send an empty JSON object as the body. Refusals, by problem type: 403 network_identity_required (the request did not come through that provider's network); 403 not_permitted (the provider refuses this device, for example a tagged device or one its policy leaves out, or the account's OIDC or LDAP provider refuses the account); 403 account_required; 403 permission_denied (the account is disabled); 409 email_in_use or identity_linked_elsewhere; 404 not_found (not an enabled network provider); 429 rate_limited; 503 provider_unavailable. Spends the login rate-limit budget."
	network.Errors = []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusTooManyRequests, http.StatusServiceUnavailable}
	Register(reg, Operation{
		Operation:   network,
		RetrySafety: RetrySafetyNonRetryable,
		Class:       ClassPublic, ServiceBacked: true, RateLimitBucket: loginDomain,
	}, reg.signInWithNetworkIdentity)
	logout := humaOp(http.MethodPost, Prefix+"/auth/logout", "logout", "auth",
		"Revoke the caller's login session.")
	// An API key passes the gate but owns no login session: 403 (v1: 401,
	// because its logout only accepts a JWT).
	logout.Errors = []int{http.StatusForbidden}
	Register(reg, Operation{Operation: logout, RetrySafety: RetrySafetyNaturalIdempotent, Class: ClassAuthenticated, ServiceBacked: true}, reg.logout)
}

func (reg *Registry) login(ctx context.Context, in *LoginInput) (*LoginOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}

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

func (reg *Registry) signInWithNetworkIdentity(ctx context.Context, in *NetworkSignInInput) (*LoginOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	if reg.deps.Sessions == nil {
		return nil, unavailable(loginDomain)
	}
	installationID, p := in.ID.positive("path.id")
	if p != nil {
		return nil, p
	}
	input := handlers.NetworkSignInInput{InstallationID: installationID, IP: clientip.FromContext(ctx)}
	if r := requestFrom(ctx); r != nil {
		input.DeviceName = r.UserAgent()
	}
	view, err := reg.deps.Sessions.NetworkSignIn(ctx, input)
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
// /auth/oauth/complete redeems, and adds the account the tokens
// authenticate.
func (reg *Registry) completeOAuthLogin(ctx context.Context, in *CompleteOAuthLoginInput) (*CompleteOAuthLoginOutput, error) {
	if reg.deps.OAuth == nil || reg.deps.Accounts == nil {
		return nil, unavailable("oauth login")
	}
	c, err := reg.deps.OAuth.Complete(ctx, in.Body.Code, in.Body.CodeVerifier, in.Browser)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrOAuthCompletionUnavailable):
			return nil, unavailable("oauth login")
		case errors.Is(err, auth.ErrOAuthCompletionInvalid):
			return nil, NewProblem(TypeInvalidToken, "The completion code is invalid, used, or expired.")
		case errors.Is(err, auth.ErrOAuthCompletionBrowser):
			return nil, NewProblem(TypeInvalidGrant, "This web completion code belongs to another browser.")
		case errors.Is(err, auth.ErrOAuthInvalidGrant):
			return nil, NewProblem(TypeInvalidGrant, "The code_verifier does not fit this completion code.")
		case errors.Is(err, auth.ErrOAuthCodeRequired):
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationBody + ".code", Code: codeRequired, Detail: "A completion code is required."})
		}
		return nil, serviceProblem(err)
	}
	if c.UserID <= 0 || c.User == nil || c.User.ID != c.UserID {
		return nil, NewProblem(TypeInvalidToken, "The completion code is invalid, used, or expired.")
	}
	view := reg.deps.Accounts.OAuthUserView(ctx, c.User)
	return &CompleteOAuthLoginOutput{Body: OAuthCompletion{AccessToken: c.AccessToken, RefreshToken: c.RefreshToken, ExpiresIn: c.ExpiresIn, Next: c.NextURL, User: accountFromView(view)}}, nil
}

// loginProblem renders a login failure: v1's 401 invalid_credentials is the
// invalid_token type (the status default is authentication_required, which
// would tell the client to present a credential it just presented).
func loginProblem(err error) *Problem {
	apiErr, ok := err.(*handlers.APIError) //nolint:errorlint // Login returns the value directly
	if ok && apiErr.Status == http.StatusUnauthorized {
		return NewProblem(TypeInvalidToken, apiErr.Message+".")
	}
	// v1 answers not_permitted; v2 tells the missing account apart, so the
	// client can say an administrator has to add one.
	if errors.Is(err, auth.ErrAccountRequired) {
		return NewProblem(TypeAccountRequired, "The sign-in provider admitted this person, but the server has no account for them; an administrator has to add one.")
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

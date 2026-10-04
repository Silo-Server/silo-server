package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
)

// The rest of the auth domain: provider discovery, token refresh, the
// caller's session list, first-run setup, invited signup, and the plugin
// launch cookie. Every credential response is Cache-Control: no-store, the
// listener default.

// AuthProvider is one way to sign in.
type AuthProvider struct {
	ID                string `json:"id" doc:"Provider id; the value login takes as provider" example:"local"`
	DisplayName       string `json:"display_name" doc:"Label for the sign-in button" example:"Silo account"`
	Mode              string `json:"mode" doc:"How the provider authenticates: credentials (login), oauth (the OAuth handshake) or network (signInWithNetworkIdentity: the provider's network says who owns the device; listed only to a request that arrived through that network). Clients ignore modes they do not know" example:"credentials"`
	Default           bool   `json:"default" doc:"Whether this is the provider login uses when none is named" example:"true"`
	IconURL           string `json:"icon_url,omitempty" doc:"Icon shown next to the button; absent when the provider ships none" example:"https://plugins.example.test/icon.svg"`
	InstallationID    ID     `json:"installation_id,omitempty" doc:"Plugin installation backing the provider; absent for the built-in provider" example:"3"`
	NativeStartPath   string `json:"native_start_path,omitempty" doc:"Path of startNativeOAuthLogin below the server base, for an oauth provider while OAuth sign-in is served (a public URL is configured); absent for credentials providers. An app appends it to its saved server base URL, which keeps a reverse proxy's path prefix, adds the PKCE and state parameters, and opens the result in the system browser. An oauth provider without it offers no native sign-in" example:"/api/v2/auth/oauth/3/native/start"`
	NetworkSignInPath string `json:"network_sign_in_path,omitempty" doc:"Path of signInWithNetworkIdentity below the server base, for a network provider; absent for other modes. An app appends it to its saved server base URL and POSTs {} to sign in, with no password and no browser" example:"/api/v2/auth/network/5/sign-in"`
	// NetworkIdentity is who the network provider says owns the requesting
	// device, for a "Continue as" label.
	NetworkIdentity *AuthProviderNetworkIdentity `json:"network_identity,omitempty" doc:"Who the network provider says owns the device that sent this request, for a Continue as label; present only for a network provider. It authorizes nothing: signInWithNetworkIdentity asks the provider again"`
}

// AuthProviderNetworkIdentity is the owner of the requesting device as a
// network provider names them.
type AuthProviderNetworkIdentity struct {
	DisplayName string `json:"display_name" doc:"Name at the provider; may be empty" example:"Alice Example"`
	Username    string `json:"username" doc:"Login name at the provider; may be empty" example:"alice@example.test"`
}

// AuthProviderCollection is the listAuthProviders response: the bounded list
// of configured providers, not paginated.
type AuthProviderCollection struct {
	Collection[AuthProvider]
	PasswordLogin bool `json:"password_login" doc:"Whether any listed provider takes a username and password. False on a server whose only sign-in is an OAuth provider with local password sign-in turned off: apps and TVs then hide the password form" example:"true"`
}

// AuthProviderCollectionOutput is the listAuthProviders response.
type AuthProviderCollectionOutput struct {
	Body AuthProviderCollection
}

// RefreshSessionInput exchanges a refresh token.
type RefreshSessionInput struct {
	Body struct {
		RefreshToken string `json:"refresh_token" minLength:"1" maxLength:"4096" doc:"The refresh token of the session to extend" example:"eyJhbGciOi..."`
	}
}

// RefreshedTokens is a refreshed credential; the account is unchanged, so
// it is not repeated.
type RefreshedTokens struct {
	AccessToken  string `json:"access_token" doc:"Bearer access token" example:"eyJhbGciOi..."`
	RefreshToken string `json:"refresh_token" doc:"Refresh token for the next refreshSession" example:"eyJhbGciOi..."`
	ExpiresIn    int    `json:"expires_in" doc:"Access token lifetime in seconds" example:"3600"`
}

// RefreshSessionOutput is the refreshSession response.
type RefreshSessionOutput struct {
	Body RefreshedTokens
}

// LoginSession is one live login session of the caller's account.
type LoginSession struct {
	ID         ID      `json:"id" doc:"Session identifier; the value deleteSession takes" example:"6f1c2a1e-8d3b-4f0e-9a7c-2b5d8e1f3a4c"`
	DeviceName string  `json:"device_name" doc:"User-Agent recorded at login; empty when none was sent" example:"Silo/1.0 (tvOS)"`
	IPAddress  string  `json:"ip_address" doc:"Client address recorded at login; empty when unknown" example:"203.0.113.7"`
	CreatedAt  Instant `json:"created_at" example:"2026-01-02T03:04:05.678Z"`
	ExpiresAt  Instant `json:"expires_at" example:"2026-02-01T03:04:05.678Z"`
}

// LoginSessionListInput is the listSessions query.
type LoginSessionListInput struct {
	LimitParam
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJjIjoiMjAyNi0wMS0wMlQwMzowNDowNS42NzhaIiwiaSI6InMxIn0"`
}

// loginSessionPosition is the cursor payload: the keyset (created_at, id) of
// the last session the previous page emitted. created_at is carried at full
// precision (RFC 3339 with nanoseconds) so the store resumes strictly after
// the row it names; equal timestamps are ordered by the unique id.
type loginSessionPosition struct {
	CreatedAt string `json:"c"`
	ID        string `json:"i"`
}

// LoginSessionCollection is the listSessions response: one page of the
// account's live sessions, newest first.
type LoginSessionCollection struct {
	Collection[LoginSession]
}

// LoginSessionCollectionOutput is the listSessions response.
type LoginSessionCollectionOutput struct {
	Body LoginSessionCollection
}

// DeleteSessionInput names the session to revoke.
type DeleteSessionInput struct {
	ID ID `path:"id" doc:"The session to revoke; it must belong to the caller's account" example:"6f1c2a1e-8d3b-4f0e-9a7c-2b5d8e1f3a4c"`
}

const setupDomain = "setup"

// SetupServerInput creates the first administrator.
type SetupServerInput struct {
	RawBody []byte
	Body    struct {
		Username             string `json:"username" minLength:"1" maxLength:"254" doc:"Login name of the administrator" example:"admin"`
		Email                string `json:"email" minLength:"1" maxLength:"254" doc:"Contact email of the administrator" example:"admin@example.test"`
		Password             string `json:"password" minLength:"8" maxLength:"72" doc:"Account password; at least 8 characters and at most 72 UTF-8 bytes" example:"correct horse battery staple"`
		CreateDefaultProfile bool   `json:"create_default_profile,omitempty" doc:"Also create the household's first profile" example:"true"`
		DefaultProfileName   string `json:"default_profile_name,omitempty" maxLength:"128" doc:"Name of that profile; the username when empty" example:"Alice"`
	}
}

// SignupStatus reports whether public invited signup is on.
type SignupStatus struct {
	Enabled bool `json:"enabled" doc:"True when signup accepts invite codes" example:"false"`
}

// SignupStatusOutput is the getSignupStatus response.
type SignupStatusOutput struct {
	Body SignupStatus
}

// SignupInput creates an account from an invite code.
type SignupInput struct {
	RawBody []byte
	Body    struct {
		Username             string `json:"username" minLength:"1" maxLength:"254" doc:"Login name" example:"alice"`
		Email                string `json:"email" minLength:"1" maxLength:"254" doc:"Contact email" example:"alice@example.test"`
		Password             string `json:"password" minLength:"8" maxLength:"72" doc:"Account password; at least 8 characters and at most 72 UTF-8 bytes" example:"correct horse battery staple"`
		InviteCode           string `json:"invite_code" minLength:"1" maxLength:"128" doc:"An active invite code" example:"WELCOME-2026"`
		CreateDefaultProfile bool   `json:"create_default_profile,omitempty" doc:"Also create the household's first profile" example:"true"`
		DefaultProfileName   string `json:"default_profile_name,omitempty" maxLength:"128" doc:"Name of that profile; the username when empty" example:"Alice"`
	}
}

// TokenPairOutput is a 201 credential response (setup, signup).
type TokenPairOutput struct {
	Body TokenPair
}

// opSignup is the signup operation id and its v1 rate limit bucket.
const opSignup = "signup"

// opListSessions is the operation id; the cursor scope is bound to it.
const opListSessions = "listSessions"

func registerAuthSessions(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/auth/providers", "listAuthProviders", "auth",
			"List the sign-in providers a client may offer."),
		Class: ClassPublic, ServiceBacked: true,
	}, reg.listAuthProviders)
	refresh := humaOp(http.MethodPost, Prefix+"/auth/refresh", "refreshSession", "auth",
		"Exchange a refresh token for a new token pair.")
	// A revoked session is 401 session_expired; any other refusal is 401
	// invalid_token. A session opened through an external sign-in provider
	// whose due re-check could not reach the provider, under the fail_closed
	// outage policy, is 503 provider_unavailable: the session stays valid
	// and the client retries later.
	refresh.Errors = []int{http.StatusUnauthorized, http.StatusServiceUnavailable}
	Register(reg, Operation{Operation: refresh, RetrySafety: RetrySafetyDomainIdentity, Class: ClassPublic, ServiceBacked: true}, reg.refreshSession)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/auth/sessions", opListSessions, "auth",
			"List the caller's live login sessions, newest first."),
		Class: ClassAuthenticated, ServiceBacked: true,
	}, func(ctx context.Context, in *LoginSessionListInput) (*LoginSessionCollectionOutput, error) {
		return reg.listSessions(ctx, cursors, in)
	})
	del := humaOp(http.MethodDelete, Prefix+"/auth/sessions/{id}", "deleteSession", "auth",
		"Revoke one of the caller's login sessions.")
	del.Errors = []int{http.StatusNotFound}
	Register(reg, Operation{Operation: del, RetrySafety: RetrySafetyNaturalIdempotent, Class: ClassAuthenticated, ServiceBacked: true}, reg.deleteSession)
	setup := humaOp(http.MethodPost, Prefix+"/auth/setup", "setupServer", "system",
		"Create the first administrator account and open its session.")
	setup.DefaultStatus = http.StatusCreated
	// Setup already completed is 409 conflict (v1: 401 setup_complete).
	setup.Errors = []int{http.StatusConflict}
	Register(reg, Operation{Operation: setup, RetrySafety: RetrySafetyNonRetryable, Class: ClassPublic, ServiceBacked: true, RateLimitBucket: setupDomain}, reg.setupServer)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/auth/signup", "getSignupStatus", "auth",
			"Report whether public invited signup is enabled."),
		Class: ClassPublic, ServiceBacked: true,
	}, reg.getSignupStatus)
	signup := humaOp(http.MethodPost, Prefix+"/auth/signup", opSignup, "auth",
		"Create an account from an invite code and open its session.")
	signup.DefaultStatus = http.StatusCreated
	// Signup disabled is 403; a rejected invite code is 422 at
	// body.invite_code; a taken username or email is 409 conflict.
	signup.Errors = []int{http.StatusForbidden, http.StatusConflict}
	Register(reg, Operation{Operation: signup, RetrySafety: RetrySafetyUniqueConstraint, Class: ClassPublic, ServiceBacked: true, RateLimitBucket: opSignup}, reg.signup)

}

func (reg *Registry) listAuthProviders(ctx context.Context, _ *struct{}) (*AuthProviderCollectionOutput, error) {
	if reg.deps.Sessions == nil {
		return nil, unavailable(loginDomain)
	}
	discovery, err := reg.deps.Sessions.DiscoverProviders(ctx)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]AuthProvider, 0, len(discovery.Providers))
	for _, p := range discovery.Providers {
		item := AuthProvider{ID: p.ID, DisplayName: p.DisplayName, Mode: p.Mode, Default: p.Default, IconURL: reg.authProviderIcon(ctx, p)}
		if p.InstallationID != 0 {
			item.InstallationID = IDFromInt(int64(p.InstallationID))
			if p.Mode == auth.ProviderModeOAuth && reg.deps.OAuth != nil {
				if reg.deps.OAuth.NativeSignInAvailable() {
					item.NativeStartPath = auth.NativeStartPath(Prefix, p.InstallationID)
				}
			}
			if p.Mode == auth.ProviderModeNetwork {
				item.NetworkSignInPath = networkSignInPath(p.InstallationID)
				if p.NetworkIdentity != nil {
					item.NetworkIdentity = &AuthProviderNetworkIdentity{DisplayName: p.NetworkIdentity.DisplayName, Username: p.NetworkIdentity.Username}
				}
			}
		}
		items = append(items, item)
	}
	return &AuthProviderCollectionOutput{Body: AuthProviderCollection{Collection: NewCollection(items), PasswordLogin: discovery.PasswordLogin}}, nil
}

func (reg *Registry) refreshSession(ctx context.Context, in *RefreshSessionInput) (*RefreshSessionOutput, error) {
	if reg.deps.Sessions == nil {
		return nil, unavailable(loginDomain)
	}
	pair, err := reg.deps.Sessions.Refresh(ctx, in.Body.RefreshToken)
	if err != nil {
		if errors.Is(err, auth.ErrProviderUnavailable) {
			return nil, NewProblem(TypeProviderUnavailable, "The sign-in provider could not confirm the account. Try again later; the session stays valid.")
		}
		var apiErr *handlers.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
			if apiErr.Code == "session_revoked" {
				return nil, NewProblem(TypeSessionExpired, apiErr.Message+".")
			}
			return nil, NewProblem(TypeInvalidToken, apiErr.Message+".")
		}
		return nil, serviceProblem(err)
	}
	return &RefreshSessionOutput{Body: RefreshedTokens{AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken, ExpiresIn: pair.ExpiresIn}}, nil
}

const loginSessionCursorSort = "-created_at,-id"

// listSessions pages the caller's live sessions by keyset. Unlike v1 GET
// /auth/sessions, which returns every retained row, expired and revoked
// sessions are excluded: they are not something the caller can act on, and
// the retained set is unbounded until retention deletes them.
func (reg *Registry) listSessions(ctx context.Context, cursors *Cursors, in *LoginSessionListInput) (*LoginSessionCollectionOutput, error) {
	if reg.deps.Sessions == nil {
		return nil, unavailable(loginDomain)
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	scope := CursorScope{
		OperationID: opListSessions,
		Security:    strconv.Itoa(claims.UserID),
		Sort:        loginSessionCursorSort,
		Tiebreaker:  "id",
	}
	var after *auth.SessionKey
	if in.Cursor != "" {
		var pos loginSessionPosition
		if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
			return nil, p
		}
		createdAt, err := time.Parse(time.RFC3339Nano, pos.CreatedAt)
		if err != nil {
			return nil, NewProblem(TypeInvalidCursor, "The cursor is malformed, tampered with, or belongs to a different query.")
		}
		after = &auth.SessionKey{CreatedAt: createdAt, ID: pos.ID}
	}
	sessions, hasMore, err := reg.deps.Sessions.ListSessionsPage(ctx, claims.UserID, after, in.Limit)
	if err != nil {
		return nil, serviceProblem(err)
	}
	next := ""
	if hasMore && len(sessions) > 0 {
		last := sessions[len(sessions)-1]
		next, err = cursors.Encode(scope, loginSessionPosition{CreatedAt: last.CreatedAt.UTC().Format(time.RFC3339Nano), ID: last.ID})
		if err != nil {
			return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
	}
	items := make([]LoginSession, 0, len(sessions))
	for _, s := range sessions {
		items = append(items, LoginSession{ID: ID(s.ID), DeviceName: s.DeviceName, IPAddress: s.IPAddress, CreatedAt: NewInstant(s.CreatedAt), ExpiresAt: NewInstant(s.ExpiresAt)})
	}
	return &LoginSessionCollectionOutput{Body: LoginSessionCollection{Collection: Paginated(items, next)}}, nil
}

func (reg *Registry) deleteSession(ctx context.Context, in *DeleteSessionInput) (*struct{}, error) {
	if reg.deps.Sessions == nil {
		return nil, unavailable(loginDomain)
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if err := reg.deps.Sessions.RevokeSession(ctx, string(in.ID), claims.UserID); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

func (reg *Registry) registration(ctx context.Context, username, email, password, invite string, createProfile bool, profileName string) handlers.RegistrationInput {
	in := handlers.RegistrationInput{
		Username: username, Email: email, Password: password, InviteCode: invite,
		CreateDefaultProfile: createProfile, DefaultProfileName: profileName,
		IP: clientip.FromContext(ctx),
	}
	if r := requestFrom(ctx); r != nil {
		in.DeviceName = r.UserAgent()
	}
	return in
}

func (reg *Registry) setupServer(ctx context.Context, in *SetupServerInput) (*TokenPairOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}

	if reg.deps.Sessions == nil {
		return nil, unavailable("account")
	}
	if p := invalidEmailProblem(in.Body.Email); p != nil {
		return nil, p
	}
	view, err := reg.deps.Sessions.SetupInitialUser(ctx, reg.registration(ctx, in.Body.Username, in.Body.Email, in.Body.Password, "", in.Body.CreateDefaultProfile, in.Body.DefaultProfileName))
	if err != nil {
		var apiErr *handlers.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "setup_complete" {
			return nil, NewProblem(TypeConflict, apiErr.Message+".")
		}
		return nil, registrationProblem(err)
	}
	return &TokenPairOutput{Body: tokenPairFromView(view)}, nil
}

func (reg *Registry) getSignupStatus(ctx context.Context, _ *struct{}) (*SignupStatusOutput, error) {
	if reg.deps.Sessions == nil {
		return nil, unavailable("account")
	}
	enabled, err := reg.deps.Sessions.SignupEnabled(ctx)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &SignupStatusOutput{Body: SignupStatus{Enabled: enabled}}, nil
}

func (reg *Registry) signup(ctx context.Context, in *SignupInput) (*TokenPairOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}

	if reg.deps.Sessions == nil {
		return nil, unavailable("account")
	}
	if p := invalidEmailProblem(in.Body.Email); p != nil {
		return nil, p
	}
	view, err := reg.deps.Sessions.Signup(ctx, reg.registration(ctx, in.Body.Username, in.Body.Email, in.Body.Password, in.Body.InviteCode, in.Body.CreateDefaultProfile, in.Body.DefaultProfileName))
	if err != nil {
		var apiErr *handlers.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "duplicate" {
			return nil, NewProblem(TypeConflict, apiErr.Message+".")
		}
		return nil, registrationProblem(err)
	}
	return &TokenPairOutput{Body: tokenPairFromView(view)}, nil
}

// invalidEmailProblem is the contract-level check on an account address: one
// bare mailbox with a dotted domain (internal/auth.ValidateEmail). It runs
// before the service so the problem is the same whatever backs it.
func invalidEmailProblem(email string) *Problem {
	if _, err := auth.ValidateEmail(email); err != nil {
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody + ".email", Code: codeInvalid, Detail: "Enter a valid email address, like name@example.com."})
	}
	return nil
}

// registrationProblem renders a setup or signup failure: a rejected member
// (an invite code the store refused) is a validation problem at that member;
// everything else follows the v1 status.
func registrationProblem(err error) *Problem {
	var apiErr *handlers.APIError
	if errors.As(err, &apiErr) && apiErr.Field != "" {
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody + "." + apiErr.Field, Code: codeInvalid, Detail: apiErr.Message})
	}
	return serviceProblem(err)
}

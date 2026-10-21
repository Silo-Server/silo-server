package apiv2

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
)

const (
	oauthCallbackStep   = "callback"
	oauthStateParameter = "state"
	oauthQueryParameter = "query"
	oauthCodeParameter  = "code"
	oauthPlainText      = "text/plain"
	oauthLocationHeader = "Location"

	oauthTag                 = "auth"
	oauthErrorParameter      = "error"
	oauthNextParameter       = "next"
	oauthLinkTicketParameter = "link_ticket"
	oauthPromptParameter     = "prompt"
)

type OAuthHandshakeCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         OAuthHandshakeCapabilitiesOutputBody
}

type OAuthHandshakeCapabilitiesOutputBody struct {
	Capability
	Available      bool `json:"available"`
	Native         bool `json:"native" doc:"Whether startNativeOAuthLogin serves the phone and desktop apps: the flow ends on org.siloserver.silo:/auth/callback with a one-time code bound to the app's PKCE S256 challenge, redeemed with completeOAuthLogin and its code_verifier"`
	Linking        bool `json:"linking" doc:"Whether createAccountIdentityLinkTicket issues link tickets: the web starts a linking flow with startAccountIdentityLink, an app passes the ticket as link_ticket to startNativeOAuthLogin and confirms the link with completeAccountIdentityLink"`
	ProviderLogout bool `json:"provider_logout" doc:"Whether getProviderLogout answers the provider end-session URL for web sign-out"`
	SelectAccount  bool `json:"select_account" doc:"Whether startOAuthLogin, initOAuthLogin and startNativeOAuthLogin accept prompt=select_account, which asks the provider to let the person choose another provider account (a Switch account sign-in)"`
}

// oauthStartParams are the query parameters of a flow start.
func oauthStartParams(native bool) []*huma.Param {
	params := []*huma.Param{{Name: "install_id", In: paramInPath, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Positive authentication-plugin installation ID."}}
	query := func(name string, required bool, schema *huma.Schema, description string) {
		params = append(params, &huma.Param{Name: name, In: oauthQueryParameter, Required: required, Schema: schema, Description: description})
	}
	str := &huma.Schema{Type: huma.TypeString}
	if native {
		query("code_challenge", false, &huma.Schema{Type: huma.TypeString, MinLength: new(43), MaxLength: new(43)}, "Required from the app. PKCE S256 challenge: unpadded base64url of SHA-256 of the app's code verifier (RFC 7636).")
		query("code_challenge_method", false, &huma.Schema{Type: huma.TypeString, Enum: []any{auth.PKCEMethodS256}}, "Required from the app. Always S256.")
		query("app_state", false, &huma.Schema{Type: huma.TypeString, MinLength: new(1), MaxLength: new(512)}, "Required from the app. Opaque value of 1 to 512 unreserved characters (A-Z a-z 0-9 - . _ ~); echoed as state on the app redirect.")
		query(oauthLinkTicketParameter, false, str, "Link ticket from createAccountIdentityLinkTicket: the flow ends on the app redirect with link=1 and a code the app confirms with completeAccountIdentityLink, signed in as the ticket's account. No session opens.")
		query(auth.OAuthNativeFlowParameter, false, &huma.Schema{Type: huma.TypeString, MinLength: new(1)}, "Set by the server when it moves a start that arrived on another origin to its public origin: the random ID of the start it keeps, single use, valid two minutes. A start with flow takes no other parameters; apps omit it.")
	} else {
		query(oauthNextParameter, false, str, "Site-relative path to return to; anything that is not a same-origin path becomes /.")
	}
	query(oauthPromptParameter, false, &huma.Schema{Type: huma.TypeString, Enum: []any{auth.OIDCPromptSelectAccount}}, "select_account asks the provider to let the person choose which provider account to sign in with, for a Switch account sign-in; omit it otherwise. Any other value is 400. A linking flow always asks for a fresh sign-in instead.")
	if !native {
		query(auth.OAuthBounceParameter, false, &huma.Schema{Type: huma.TypeString, Enum: []any{"1"}}, "Set by the server when it redirects a web start to its public origin; clients omit it.")
	}
	return params
}

func oauthHandshakeResponses(statuses ...string) map[string]*huma.Response {
	responses := map[string]*huma.Response{}
	for _, status := range statuses {
		switch status {
		case "302", "303", "307":
			responses[status] = &huma.Response{Description: "Redirect: to the provider, to the same start on the public origin, or to the flow's completion or failure location.", Headers: map[string]*huma.Header{oauthLocationHeader: {Schema: &huma.Schema{Type: huma.TypeString}}}}
		default:
			responses[status] = &huma.Response{Description: "Plain-text handshake failure.", Content: map[string]*huma.MediaType{oauthPlainText: {Schema: &huma.Schema{Type: huma.TypeString}}}}
		}
	}
	return responses
}

// oauthWebStartPath is the web start route under /auth/oauth/{install_id}/;
// the form-post init answers 303 there on another origin.
const oauthWebStartPath = "start"

func registerOAuthHandshakes(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/auth/oauth/capabilities", "getOAuthHandshakeCapabilities", oauthTag, "Discover browser and native-app OAuth handshake availability."), Class: ClassPublic, ServiceBacked: true}, func(_ context.Context, _ *CapabilityInput) (*OAuthHandshakeCapabilitiesOutput, error) {
		out := new(OAuthHandshakeCapabilitiesOutput)
		if svc := reg.deps.OAuth; svc != nil {
			out.Body.Available = true
			out.Body.Native = svc.NativeSignInAvailable()
			out.Body.Linking = svc.LinkingAvailable()
			out.Body.ProviderLogout = svc.ProviderLogoutAvailable()
			out.Body.SelectAccount = true
		}
		return out, nil
	})

	type startRoute struct {
		method, path, id, summary, description string
		native                                 bool
		responses                              []string
	}
	starts := []startRoute{
		{http.MethodPost, "init", "initOAuthLogin", "Start a web OAuth sign-in from a form post.",
			"A browser on another origin than the public URL is sent (303) to startOAuthLogin there, so the flow's browser-binding cookie is set on the origin of the callback. A provider that cannot start the flow, or a missing public URL, sends the browser (302) to /login?error=oauth_failed&reason=provider_unavailable.",
			false, []string{"302", "303", "400", "409", "500", "502"}},
		{http.MethodGet, oauthWebStartPath, "startOAuthLogin", "Start a web OAuth sign-in.",
			"Sets an HttpOnly, SameSite=Lax browser-binding cookie scoped to the callback path, then redirects to the provider. A browser on another origin than the public URL is first sent (302) to the same start there. A link_ticket is refused with 400: the web links with startAccountIdentityLink. Malformed parameters are plain-text 400s; a provider that cannot start the flow, or a missing public URL, sends the browser (302) to /login?error=oauth_failed&reason=provider_unavailable (login_failed for a server error), where the login page does not redirect to the provider again.",
			false, []string{"302", "400"}},
		{http.MethodGet, "native/start", "startNativeOAuthLogin", "Start an OAuth sign-in for a native app in the system browser.",
			"The app opens this start on its saved server base in ASWebAuthenticationSession or a Custom Tab: it appends native_start_path from listAuthProviders to its saved server base URL (an oauth provider without native_start_path offers no native sign-in), adds code_challenge, code_challenge_method=S256 and app_state, and never opens another origin. The server records the start origin, the origin the start arrived on (scheme://host[:port], scheme and host in lower case, default port omitted, IPv6 in brackets, no path or trailing slash), derived from the host a trusted proxy names in X-Forwarded-Host or else the Host header, and the scheme of the connection or the one a trusted proxy names. A start that arrives on another origin than the public URL is accepted only on the origin of a connected network access provider or on a local address: an IP literal in the loopback, private, link-local or 100.64.0.0/10 range, or a single-label, .local, .lan, .localdomain, .home.arpa or .internal host name; any other origin is a plain-text 400. An accepted start is kept on the server and the browser is sent (302) to this start on the public origin with only flow, the kept start's random single-use ID, valid two minutes; the flow sets its binding cookie there and goes to the provider. The flow ends on org.siloserver.silo:/auth/callback with code, state (the app_state), server (getServerIdentity's server_id) and iss, the start origin; a failure carries error=<reason> instead of code. Every app redirect of a sign-in or linking flow carries iss, success or failure. The app accepts the redirect only when iss equals the origin of its saved server base, and redeems the code only at that saved base; a server that redirects its start to another server gets that server's own origin as iss, which the app refuses. The Host header is chosen by whoever sends the start, so iss does not stop a hostile server on a local address that sends the app's start here itself; that is why other origins are refused. A start with a valid code_challenge and app_state whose provider cannot start the flow goes straight to the app redirect with error=provider_unavailable (login_failed for a server error); an unknown, used, expired or foreign link_ticket goes there with error=session_expired. An unknown, used, expired or foreign flow is a plain-text 400, as is a flow presented on another origin than the public URL, which leaves the kept start for the public origin. Redeem the code within 60 seconds with completeOAuthLogin and the code_verifier; a linking flow's code carries link=1 and is redeemed with completeAccountIdentityLink instead.",
			true, []string{"302", "400", "409", "500", "502"}},
	}
	for _, route := range starts {
		op := Operation{Operation: huma.Operation{Method: route.method, Path: Prefix + "/auth/oauth/{install_id}/" + route.path, OperationID: route.id, Summary: route.summary, Description: route.description, Tags: []string{oauthTag}, Parameters: oauthStartParams(route.native), Responses: oauthHandshakeResponses(route.responses...)}, Class: ClassPublic, ServiceBacked: true}
		if route.method == http.MethodPost {
			op.RetrySafety = RetrySafetyNonRetryable
		}
		RegisterRaw(reg, RawOperation{Operation: op, Protocol: "oauth-redirect", Reason: "Browser form submission and provider redirects require HTTP redirects and cookies, without JSON negotiation."}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reg.serveOAuthStart(w, r, route.path, route.native)
		}))
	}

	callbackParams := []*huma.Param{{Name: "install_id", In: paramInPath, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Positive authentication-plugin installation ID."}}
	for _, name := range []string{oauthStateParameter, oauthCodeParameter, oauthErrorParameter} {
		callbackParams = append(callbackParams, &huma.Param{Name: name, In: oauthQueryParameter, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Provider redirect value; validated by the owning handshake."})
	}
	callback := Operation{Operation: huma.Operation{Method: http.MethodGet, Path: Prefix + "/auth/oauth/{install_id}/" + oauthCallbackStep, OperationID: "finishOAuthCallback", Summary: "Continue the OAuth handshake at the provider's redirect back.",
		Description: "Accepts only the browser holding the flow's binding cookie. A web sign-in continues to /login/oauth-complete?code=; a failure to /login?error=oauth_failed&reason=<reason> (not_permitted, account_required, email_in_use, identity_linked_elsewhere, account_disabled, provider_unavailable, state_invalid, session_expired, already_linked, login_failed). A web linking flow returns to its next path with linked=1 or error=oauth_link_failed&reason=<reason>; a native flow to the app redirect, which carries iss (see startNativeOAuthLogin).",
		Tags:        []string{oauthTag}, Parameters: callbackParams, Responses: oauthHandshakeResponses("302", "400")}, Class: ClassPublic, ServiceBacked: true}
	RegisterRaw(reg, RawOperation{Operation: callback, Protocol: "oauth-redirect", Reason: "Browser form submission and provider callback require HTTP redirects, without JSON negotiation."}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		svc := reg.deps.OAuth
		if svc == nil {
			writeProblem(w, r, unavailable("OAuth handshake"))
			return
		}
		installID, err := intOfID(ID(chi.URLParam(r, "install_id")))
		if err != nil || installID <= 0 {
			http.Error(w, "invalid install_id", http.StatusBadRequest)
			return
		}
		svc.ServeCallback(w, r, Prefix, installID)
	}))
	registerOAuthAccountOperations(reg)
}

// serveOAuthStart answers the three flow starts. POST init bounces to the
// GET start on the public origin (303), the GET starts to themselves.
func (reg *Registry) serveOAuthStart(w http.ResponseWriter, r *http.Request, path string, native bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	svc := reg.deps.OAuth
	if svc == nil {
		writeProblem(w, r, unavailable("OAuth handshake"))
		return
	}
	installID, err := intOfID(ID(chi.URLParam(r, "install_id")))
	if err != nil || installID <= 0 {
		http.Error(w, "invalid install_id", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	req := auth.OAuthStartRequest{InstallID: installID, Prefix: Prefix, Native: native}
	if native && q.Has(auth.OAuthNativeFlowParameter) {
		// The server moved this start to its public origin; everything else
		// about it is on the server.
		req.Flow = q.Get(auth.OAuthNativeFlowParameter)
		if req.Flow == "" {
			http.Error(w, "flow must not be empty", http.StatusBadRequest)
			return
		}
		svc.ServeStart(w, r, req, "", 0)
		return
	}
	if q.Has(oauthPromptParameter) {
		req.Prompt = q.Get(oauthPromptParameter)
		if req.Prompt == "" {
			http.Error(w, "prompt must be select_account", http.StatusBadRequest)
			return
		}
	}
	req.LinkTicket = q.Get(oauthLinkTicketParameter)
	if native {
		if q.Get("code_challenge_method") != auth.PKCEMethodS256 {
			http.Error(w, "code_challenge_method must be S256", http.StatusBadRequest)
			return
		}
		// A native start on another origin than the public URL is parked on
		// the server, not bounced with its parameters.
		req.CodeChallenge, req.AppState = q.Get("code_challenge"), q.Get("app_state")
		svc.ServeStart(w, r, req, "", 0)
		return
	}
	req.Next = q.Get(oauthNextParameter)
	bounce := url.Values{}
	if req.Next != "" {
		bounce.Set(oauthNextParameter, req.Next)
	}
	if req.Prompt != "" {
		bounce.Set(oauthPromptParameter, req.Prompt)
	}
	bounce.Set(auth.OAuthBounceParameter, "1")
	target, status := path, http.StatusFound
	if path == "init" {
		target, status = oauthWebStartPath, http.StatusSeeOther
	}
	bounceURL := svc.PublicURL(Prefix+"/auth/oauth/"+strconv.Itoa(installID)+"/"+target, bounce)
	svc.ServeStart(w, r, req, bounceURL, status)
}

func (c OAuthHandshakeCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}

// AccountIdentityLinkTicketInput asks for a link ticket.
type AccountIdentityLinkTicketInput struct {
	RawBody []byte
	Body    struct {
		InstallationID ID     `json:"installation_id" doc:"OAuth auth plugin installation to link, as listAuthProviders shows it" example:"3"`
		Password       string `json:"password" minLength:"1" maxLength:"1024" doc:"The account's current local password" example:"correct horse battery staple"`
	}
}

// AccountIdentityLinkTicket starts one linking flow.
type AccountIdentityLinkTicket struct {
	Ticket    string  `json:"ticket" doc:"Single-use ticket. The web client passes it to startAccountIdentityLink (POST /api/v2/account/identities/link-start), and the flow finishes in that browser; the GET web start refuses it. An app passes it as link_ticket to startNativeOAuthLogin (native/start) and confirms the result with completeAccountIdentityLink (POST /api/v2/account/identities/link-complete)" example:"9d2c5f0e8b7a41c3a6e5d4c3b2a19087f6e5d4c3b2a1908f7e6d5c4b3a291807"`
	ExpiresAt Instant `json:"expires_at" doc:"The ticket must start its flow before this instant" example:"2026-01-02T03:09:05.678Z"`
}

// AccountIdentityLinkStartInput starts a web linking flow.
type AccountIdentityLinkStartInput struct {
	RawBody []byte
	Body    struct {
		LinkTicket string `json:"link_ticket" minLength:"1" maxLength:"128" doc:"Ticket from createAccountIdentityLinkTicket, issued to the caller's account" example:"9d2c5f0e8b7a41c3a6e5d4c3b2a19087f6e5d4c3b2a1908f7e6d5c4b3a291807"`
		Next       string `json:"next,omitempty" maxLength:"2048" doc:"Site-relative path the flow returns to with linked=1, or error=oauth_link_failed&reason=<reason>; anything that is not a same-origin path becomes /" example:"/settings/account"`
	}
}

// AccountIdentityLinkStart is where the browser goes next.
type AccountIdentityLinkStart struct {
	AuthorizeURL string `json:"authorize_url" doc:"Provider sign-in URL; navigate the same browser there" example:"https://id.example.test/authorize?client_id=silo&state=..."`
}

// AccountIdentityLinkStartOutput carries the flow's browser-binding cookie.
type AccountIdentityLinkStartOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie" doc:"The flow's browser-binding cookie: HttpOnly, SameSite=Lax, scoped to the callback path, living the flow's ten minutes plus an hour so a late callback still reaches its client, Secure on an https public URL"`
	Body      AccountIdentityLinkStart
}

// AccountIdentityLinkCompleteInput confirms a native linking flow.
type AccountIdentityLinkCompleteInput struct {
	RawBody []byte
	Body    struct {
		Code         string `json:"code" minLength:"1" maxLength:"128" doc:"Code from the app redirect of a linking flow (link=1); single use, 60 seconds" example:"3f2b47eb7b36dd2d"`
		CodeVerifier string `json:"code_verifier" minLength:"43" maxLength:"128" doc:"PKCE code verifier of the challenge the native start sent" example:"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"`
	}
}

// AccountIdentityLinkTicketOutput is the createAccountIdentityLinkTicket
// response.
type AccountIdentityLinkTicketOutput struct {
	Body AccountIdentityLinkTicket
}

// ProviderLogout is the provider sign-out a web client may visit.
type ProviderLogout struct {
	EndSessionURL string `json:"end_session_url" doc:"Provider end-session URL to navigate to after logout; empty when there is none" example:"https://id.example.test/logout?id_token_hint=eyJhbGciOi..."`
}

// ProviderLogoutOutput is the getProviderLogout response.
type ProviderLogoutOutput struct {
	Body ProviderLogout
}

func registerOAuthAccountOperations(reg *Registry) {
	ticket := humaOp(http.MethodPost, Prefix+"/account/identities/link-ticket", "createAccountIdentityLinkTicket", "account",
		"Confirm the account password and get a ticket that starts a provider linking flow.")
	ticket.Description = "A signed-in account re-enters its local password here for a single-use ticket (valid 5 minutes) that starts one linking flow: the web passes it to startAccountIdentityLink; an app passes it as link_ticket to startNativeOAuthLogin and confirms the result with completeAccountIdentityLink. The flow links the provider identity to this account instead of signing in; an identity linked to another account ends in identity_linked_elsewhere. An account without local password sign-in is 409 local_password_required; a wrong password is 422 at body.password; an API key or impersonation session is 403; an installation that is not an enabled OAuth provider is 404."
	ticket.Errors = []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict}
	Register(reg, Operation{Operation: ticket, Class: ClassAuthenticated, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable, RateLimitBucket: bucketPasswordChange},
		func(ctx context.Context, in *AccountIdentityLinkTicketInput) (*AccountIdentityLinkTicketOutput, error) {
			svc := reg.deps.OAuth
			if svc == nil || !svc.LinkingAvailable() {
				return nil, unavailable("OAuth linking")
			}
			if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
				return nil, p
			}
			claims, p := linkingClaims(ctx)
			if p != nil {
				return nil, p
			}
			installationID, p := in.Body.InstallationID.positive(locationBody + ".installation_id")
			if p != nil {
				return nil, p
			}
			issued, err := svc.IssueLinkTicket(ctx, claims.UserID, installationID, in.Body.Password)
			if err != nil {
				switch {
				case errors.Is(err, auth.ErrUnknownAuthInstallation):
					return nil, NewProblem(TypeNotFound, "No enabled OAuth sign-in provider has this installation.")
				case errors.Is(err, auth.ErrLinkTicketUnavailable):
					return nil, unavailable("OAuth linking")
				case errors.Is(err, auth.ErrProviderUnavailable):
					return nil, NewProblem(TypeProviderUnavailable, "The sign-in provider could not start the link.")
				}
				if p := linkingRefusalProblem(err, "provider"); p != nil {
					return nil, p
				}
				return nil, serviceProblem(err)
			}
			return &AccountIdentityLinkTicketOutput{Body: AccountIdentityLinkTicket{Ticket: issued.Ticket, ExpiresAt: NewInstant(issued.ExpiresAt)}}, nil
		})

	start := humaOp(http.MethodPost, Prefix+"/account/identities/link-start", "startAccountIdentityLink", "account",
		"Start a web linking flow with a link ticket and get the provider sign-in URL.")
	start.Description = "Consumes a ticket from createAccountIdentityLinkTicket issued to the caller's account, sets the flow's browser-binding cookie on this response and answers the provider URL for the same browser to open. Only the browser that made this request can finish the flow, so a ticket opened by anyone else links nothing. Call it on the public URL's origin (409 otherwise, or when no public URL is configured). The provider is asked for a fresh sign-in. The flow returns to next with linked=1, or error=oauth_link_failed&reason=<reason>. An unknown, used, expired or foreign ticket is 404; an API key or impersonation session is 403; a provider that cannot start the flow is 503 provider_unavailable."
	start.Errors = []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict}
	Register(reg, Operation{Operation: start, Class: ClassAuthenticated, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable},
		func(ctx context.Context, in *AccountIdentityLinkStartInput) (*AccountIdentityLinkStartOutput, error) {
			svc := reg.deps.OAuth
			if svc == nil || !svc.LinkingAvailable() {
				return nil, unavailable("OAuth linking")
			}
			if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
				return nil, p
			}
			claims, p := linkingClaims(ctx)
			if p != nil {
				return nil, p
			}
			if r := requestFrom(ctx); r == nil || !svc.OnPublicOrigin(r) {
				return nil, NewProblem(TypeConflict, "Open Silo at its public URL to link a sign-in.")
			}
			result, err := svc.StartLink(ctx, claims.UserID, Prefix, in.Body.LinkTicket, in.Body.Next)
			if err != nil {
				switch {
				case errors.Is(err, auth.ErrOAuthLinkTicketInvalid), errors.Is(err, auth.ErrUnknownAuthInstallation):
					return nil, NewProblem(TypeNotFound, "The link ticket is unknown, used or expired.")
				case errors.Is(err, auth.ErrOAuthPublicOriginRequired):
					return nil, NewProblem(TypeConflict, "Open Silo at its public URL to link a sign-in.")
				case errors.Is(err, auth.ErrProviderUnavailable):
					return nil, NewProblem(TypeProviderUnavailable, "The sign-in provider could not start the link.")
				case errors.Is(err, auth.ErrLinkTicketUnavailable):
					return nil, unavailable("OAuth linking")
				}
				return nil, serviceProblem(err)
			}
			return &AccountIdentityLinkStartOutput{SetCookie: *result.Cookie, Body: AccountIdentityLinkStart{AuthorizeURL: result.AuthorizeURL}}, nil
		})

	complete := humaOp(http.MethodPost, Prefix+"/account/identities/link-complete", "completeAccountIdentityLink", "account",
		"Confirm a native app's linking flow and link the provider identity to the caller's account.")
	complete.Description = "An app's linking flow (startNativeOAuthLogin with link_ticket) ends on the app redirect with link=1 and a code. The app redeems it here within 60 seconds with its code_verifier, signed in as the account the ticket was issued to; nothing is linked before. A verifier that does not fit is 400 invalid_grant and leaves the code redeemable; an unknown, used or expired code, or another account, is 401 invalid_token. An identity linked to another account is 409 identity_linked_elsewhere; an account already linked to the provider is 409 already_linked; a refusal by the provider is 403 not_permitted, or 403 account_disabled when the provider's account is disabled, locked or expired. An API key or impersonation session is 403."
	complete.DefaultStatus = http.StatusNoContent
	complete.Errors = []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict}
	Register(reg, Operation{Operation: complete, Class: ClassAuthenticated, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable},
		func(ctx context.Context, in *AccountIdentityLinkCompleteInput) (*struct{}, error) {
			svc := reg.deps.OAuth
			if svc == nil || !svc.LinkingAvailable() {
				return nil, unavailable("OAuth linking")
			}
			if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
				return nil, p
			}
			claims, p := linkingClaims(ctx)
			if p != nil {
				return nil, p
			}
			if err := svc.CompleteLink(ctx, claims.UserID, in.Body.Code, in.Body.CodeVerifier); err != nil {
				return nil, linkCompletionProblem(err)
			}
			return nil, nil
		})

	logout := humaOp(http.MethodGet, Prefix+"/auth/provider-logout", "getProviderLogout", oauthTag,
		"Get the OAuth provider sign-out URL for the caller's web logout.")
	logout.Description = "Call before logout while the session is still valid, then end the Silo session with logout and send the browser to end_session_url when it is not empty. It is empty unless the caller is the account's own login session (not an API key or an impersonation session), the account is linked to an enabled OAuth provider and the provider plugin offers an end-session URL; the plugin's own configuration turns provider logout on or off. The provider returns the browser to /login on the public origin."
	Register(reg, Operation{Operation: logout, Class: ClassAuthenticated, ServiceBacked: true},
		func(ctx context.Context, _ *struct{}) (*ProviderLogoutOutput, error) {
			svc := reg.deps.OAuth
			if svc == nil || !svc.ProviderLogoutAvailable() {
				return nil, unavailable("provider logout")
			}
			claims := claimsFrom(ctx)
			if claims == nil {
				return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
			}
			// The provider session belongs to the person who signed in, so
			// only their own login session gets its end-session URL: an
			// impersonating admin or an API key must not carry the
			// account's id_token_hint to the provider.
			if !claims.IsOwnLoginSession() {
				return &ProviderLogoutOutput{Body: ProviderLogout{}}, nil
			}
			endURL, err := svc.ProviderLogoutURL(ctx, claims.UserID)
			if err != nil {
				return nil, serviceProblem(err)
			}
			return &ProviderLogoutOutput{Body: ProviderLogout{EndSessionURL: endURL}}, nil
		})
}

// linkingClaims admits only the account's own signed-in session to the
// linking operations.
func linkingClaims(ctx context.Context) (*auth.Claims, *Problem) {
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if !claims.IsOwnLoginSession() {
		return nil, NewProblem(TypePermissionDenied, "Only the account's own signed-in session can link a sign-in.")
	}
	return claims, nil
}

// linkCompletionProblem renders a failed completeAccountIdentityLink.
func linkCompletionProblem(err error) error {
	switch {
	case errors.Is(err, auth.ErrOAuthCompletionUnavailable):
		return unavailable("OAuth linking")
	case errors.Is(err, auth.ErrOAuthCompletionInvalid):
		return NewProblem(TypeInvalidToken, "The link code is invalid, used, or expired.")
	case errors.Is(err, auth.ErrOAuthInvalidGrant):
		return NewProblem(TypeInvalidGrant, "The code_verifier does not fit this link code.")
	case errors.Is(err, auth.ErrOAuthCodeRequired):
		return validationProblem(locationBody+".code", codeRequired, "A link code is required.")
	}
	if p := linkingRefusalProblem(err, "provider"); p != nil {
		return p
	}
	return serviceProblem(err)
}

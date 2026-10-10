package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/go-chi/chi/v5"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
)

// OAuthClient is the host-side gRPC client surface the OAuth handler needs.
// Defined as an interface so handler tests can substitute a fake.
type OAuthClient interface {
	InitAuthorize(ctx context.Context, req *pluginv1.InitAuthorizeRequest) (*pluginv1.InitAuthorizeResponse, error)
	ExchangeCode(ctx context.Context, req *pluginv1.ExchangeCodeRequest) (*pluginv1.AuthenticateResponse, error)
}

// OAuthLoginCompleter wraps the post-ExchangeCode work. ResolveOAuthLogin
// looks up or provisions the account the AuthenticateResponse names and
// answers the identity it came through, without opening a session;
// OpenOAuthSession opens the session when the completion code is redeemed;
// LinkOAuthIdentity links the identity to the signed-in account a linking
// flow started from, without opening a session. Defined as an interface so
// tests can avoid spinning up the full auth.Service.
type OAuthLoginCompleter interface {
	ResolveOAuthLogin(ctx context.Context, in OAuthLoginInput) (*models.User, int64, error)
	OpenOAuthSession(ctx context.Context, db OAuthSessionDB, c OAuthCompletion) (*TokenPair, error)
	LinkOAuthIdentity(ctx context.Context, in OAuthLoginInput) (*models.User, error)
}

// OAuthLoginInput carries everything the completer needs to resolve the
// account of a sign-in; DeviceName and IP go to the session its completion
// code opens.
type OAuthLoginInput struct {
	InstallationID int
	CapabilityID   string
	Response       *pluginv1.AuthenticateResponse
	LinkingUserID  int // 0 = not linking
	DeviceName     string
	IP             string
}

// OAuthUserReader loads the account a link ticket is issued to.
type OAuthUserReader interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

// OAuthHandlerDeps wires the OAuthHandler. ResolveClient turns the URL's
// installation_id into a plugin gRPC client; LoginCompleter consumes the
// AuthenticateResponse and opens the session when the completion code is
// redeemed.
type OAuthHandlerDeps struct {
	Store           OAuthStore
	CompletionStore OAuthCompletionStore
	LinkTickets     OAuthLinkTicketStore
	// PendingLinks keeps native linking flows until the app confirms them.
	PendingLinks OAuthPendingLinkStore
	// NativeStarts keeps native starts that arrived on another origin than
	// the public URL while the browser moves to the public origin.
	NativeStarts   OAuthNativeStartStore
	StateSecret    []byte
	ResolveClient  func(ctx context.Context, installationID int) (OAuthClient, string, error) // returns (client, capabilityID, err)
	LoginCompleter OAuthLoginCompleter
	HostBaseURL    string
	StateTTL       time.Duration
	// FrontendCompletePath is the SPA path the callback redirects to after
	// minting a one-time completion code. The SPA exchanges that code for tokens.
	FrontendCompletePath string
	// ServerID names this deployment in the native app redirect, so the app
	// can check the answer came from the server it started with.
	ServerID func(ctx context.Context) (string, error)
	// RevokeSession ends the session a reused completion code opened.
	RevokeSession func(ctx context.Context, sessionID string) error
	// Users loads the account a link ticket is issued to.
	Users OAuthUserReader
	// LinkTicketTTL is how long a link ticket may wait for its flow start.
	LinkTicketTTL time.Duration
	// ProviderLogout answers the provider end-session URL of an account's
	// web sign-out; nil when provider logout is not wired.
	ProviderLogout func(ctx context.Context, userID int, postLogoutRedirectURI string) (string, error)
	// KnownOrigins lists origins besides the public URL that this server
	// answers on (the connected network access providers); read per native
	// start. Nil lists none.
	KnownOrigins func() []string
}

// OAuthHandler serves the OAuth sign-in flows of auth plugins: the web and
// native starts, the provider callback, completion-code redemption and
// link tickets (docs/architecture/external-sign-in.md).
type OAuthHandler struct {
	deps        OAuthHandlerDeps
	hostBaseURL atomic.Pointer[string]
}

// oauthCompletionTTL is how long a completion code may be redeemed.
const oauthCompletionTTL = time.Minute

func NewOAuthHandler(d OAuthHandlerDeps) *OAuthHandler {
	if d.StateTTL == 0 {
		d.StateTTL = 10 * time.Minute
	}
	if d.FrontendCompletePath == "" {
		d.FrontendCompletePath = "/login/oauth-complete"
	}
	if d.LinkTicketTTL == 0 {
		d.LinkTicketTTL = 5 * time.Minute
	}
	if d.CompletionStore == nil {
		if store, ok := d.Store.(OAuthCompletionStore); ok {
			d.CompletionStore = store
		}
	}
	if d.LinkTickets == nil {
		if store, ok := d.Store.(OAuthLinkTicketStore); ok {
			d.LinkTickets = store
		}
	}
	if d.PendingLinks == nil {
		if store, ok := d.Store.(OAuthPendingLinkStore); ok {
			d.PendingLinks = store
		}
	}
	if d.NativeStarts == nil {
		if store, ok := d.Store.(OAuthNativeStartStore); ok {
			d.NativeStarts = store
		}
	}
	h := &OAuthHandler{deps: d}
	h.SetHostBaseURL(d.HostBaseURL)
	return h
}

// SetHostBaseURL updates the externally reachable origin used for future
// OAuth redirects.
func (h *OAuthHandler) SetHostBaseURL(url string) {
	normalized := strings.TrimRight(strings.TrimSpace(url), "/")
	h.hostBaseURL.Store(&normalized)
}

func (h *OAuthHandler) currentHostBaseURL() string {
	if value := h.hostBaseURL.Load(); value != nil {
		return *value
	}
	return h.deps.HostBaseURL
}

// ErrMissingInstallID is returned when the URL path has no install_id.
var ErrMissingInstallID = errors.New("install_id required")

// Sign-in failure reasons the callback reports on /login (reason=), on a
// linking flow's return path, and on the native app redirect (error=).
const (
	OAuthReasonNotPermitted            = "not_permitted"
	OAuthReasonAccountRequired         = "account_required"
	OAuthReasonEmailInUse              = "email_in_use"
	OAuthReasonIdentityLinkedElsewhere = "identity_linked_elsewhere"
	OAuthReasonAccountDisabled         = "account_disabled"
	OAuthReasonProviderUnavailable     = "provider_unavailable"
	OAuthReasonStateInvalid            = "state_invalid"
	OAuthReasonSessionExpired          = "session_expired"
	OAuthReasonAlreadyLinked           = "already_linked"
	OAuthReasonLoginFailed             = "login_failed"
)

// oidcPromptLogin asks the provider for a fresh sign-in (OIDC prompt=login).
const oidcPromptLogin = "login"

// OIDCPromptSelectAccount asks the provider to let the person choose which
// of their provider accounts to sign in with (OIDC prompt=select_account),
// for a "Switch account" sign-in. It is the only prompt a start accepts.
const OIDCPromptSelectAccount = "select_account"

// OAuthBounceParameter marks a web start the server already redirected to
// its public origin, so a proxy that hides the origin cannot cause a loop.
const OAuthBounceParameter = "bounce"

// OAuthNativeFlowParameter carries the random ID of a native start the
// server moved to its public origin. It is the only thing the move puts in
// the URL.
const OAuthNativeFlowParameter = "flow"

// nativeStartUnknownMessage answers a parked native start ID that cannot
// open a flow here.
const nativeStartUnknownMessage = "This sign-in is unknown, used or expired. Start it again from the app."

// oauthNativeStartTTL bounds a parked native start: the move to the public
// origin is one redirect.
const oauthNativeStartTTL = 2 * time.Minute

// OAuthHandshakeError is a failure of the browser handshake that is answered
// as a plain-text status rather than a redirect.
type OAuthHandshakeError struct {
	Status  int
	Message string
}

func (e *OAuthHandshakeError) Error() string { return e.Message }

func writeHandshakeError(w http.ResponseWriter, err error) {
	var he *OAuthHandshakeError
	if errors.As(err, &he) {
		http.Error(w, he.Message, he.Status)
		return
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// CallbackURL is the absolute redirect URI of the callback under one API
// prefix ("/api/v1" or "/api/v2"). The provider must have the URI it is
// handed registered, so the init and callback of one flow share a prefix.
func (h *OAuthHandler) CallbackURL(prefix string, installID int) string {
	return h.currentHostBaseURL() + prefix + "/auth/oauth/" + strconv.Itoa(installID) + "/callback"
}

// PublicURL is path (with query) on the configured public origin; empty
// when no public URL is configured.
func (h *OAuthHandler) PublicURL(path string, query url.Values) string {
	base := h.currentHostBaseURL()
	if base == "" {
		return ""
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return base + path
}

// NativeStartPath is the native start of an installation below the server
// base. Apps append it to their saved server base URL.
func NativeStartPath(prefix string, installID int) string {
	return prefix + "/auth/oauth/" + strconv.Itoa(installID) + "/native/start"
}

// NativeSignInAvailable reports whether the native start can be served: a
// public URL is configured, so the flow has an origin to resume on and a
// callback the provider knows.
func (h *OAuthHandler) NativeSignInAvailable() bool {
	return h.currentHostBaseURL() != ""
}

// OnPublicOrigin reports whether r arrived on the configured public origin.
// The binding cookie must be set there, because the provider sends the
// browser back to the callback on that origin.
func (h *OAuthHandler) OnPublicOrigin(r *http.Request) bool {
	base, err := url.Parse(h.currentHostBaseURL())
	if err != nil || base.Host == "" {
		return true
	}
	return sameOrigin(clientip.RequestScheme(r), clientip.RequestHost(r), base)
}

// OAuthStartRequest is one flow start as a transport received it.
type OAuthStartRequest struct {
	InstallID int
	// Prefix is the API prefix of the flow's callback ("/api/v1" or
	// "/api/v2").
	Prefix string
	Next   string
	// LinkTicket makes a native flow a linking flow for the account that
	// requested the ticket. The link is made only when that account's app
	// redeems the flow's code (CompleteLink). Web linking flows start with
	// StartLink instead.
	LinkTicket string
	// Native flows end on the fixed app redirect with a completion code
	// bound to CodeChallenge; AppState is echoed there.
	Native        bool
	CodeChallenge string
	AppState      string
	// Prompt is the OIDC prompt a sign-in asks the provider for: empty (the
	// plugin's configured default) or OIDCPromptSelectAccount. Linking flows
	// always ask for a fresh sign-in instead.
	Prompt string
	// Flow is the ID of a native start the server parked and moved to its
	// public origin (OAuthNativeFlowParameter). The start is loaded from it;
	// the other fields but InstallID and Prefix are ignored.
	Flow string

	// startOrigin is the origin where a native flow first arrived, which
	// every app redirect of the flow names as iss. Only the server sets it.
	startOrigin string
}

// OAuthStartResult is where a started flow sends the browser and the
// binding cookie to set first.
type OAuthStartResult struct {
	AuthorizeURL string
	Cookie       *http.Cookie
}

// ServeStart answers one flow start. A web start that arrived on another
// origin than the public URL is sent to bounceURL there (once; bounceURL
// empty never bounces). A native start records the origin it arrived on as
// the flow's start origin; on another origin than the public URL that origin
// must pass knownStartOrigin (else a plain-text 400), and the start is
// parked on the server while the browser moves to the public origin carrying
// only the parked start's random ID (parkNativeStart). Otherwise the flow
// opens: its binding cookie is set and the browser goes to the provider.
func (h *OAuthHandler) ServeStart(w http.ResponseWriter, r *http.Request, req OAuthStartRequest, bounceURL string, bounceStatus int) {
	if req.Native && req.Flow != "" {
		h.resumeNativeStart(w, r, req)
		return
	}
	ctx := r.Context()
	onPublicOrigin := h.OnPublicOrigin(r)
	if req.Native {
		req.startOrigin = requestOrigin(r)
	}
	// An unknown start origin is refused before checkStart: a start failure
	// redirects to the app with that origin as iss, which only a known
	// origin may be.
	if req.Native && !onPublicOrigin && !h.knownStartOrigin(req.startOrigin) {
		slog.InfoContext(ctx, "oauth native start refused on an address this server does not know", "component", "auth", "installation_id", req.InstallID, "start_origin", req.startOrigin, "public_url", h.currentHostBaseURL())
		http.Error(w, "App sign-in is not available on this server address. Add the server with its public address or a local network address, then try again.", http.StatusBadRequest)
		return
	}
	if err := h.checkStart(ctx, req); err != nil {
		h.writeStartFailure(w, r, req, err)
		return
	}
	if !req.Native && !onPublicOrigin && bounceURL != "" && r.URL.Query().Get(OAuthBounceParameter) == "" {
		http.Redirect(w, r, bounceURL, bounceStatus)
		return
	}
	linkingUserID := 0
	if req.LinkTicket != "" {
		ticket, err := h.consumeLinkTicket(ctx, req.LinkTicket, req.InstallID)
		if err != nil {
			http.Redirect(w, r, h.nativeRedirect(ctx, req.startOrigin, req.AppState, "error", OAuthReasonSessionExpired), http.StatusFound)
			return
		}
		linkingUserID = ticket.UserID
	}
	if req.Native && !onPublicOrigin {
		h.parkNativeStart(w, r, req, linkingUserID)
		return
	}
	h.openFlow(w, r, req, linkingUserID)
}

// openFlow opens a checked flow: it sets the binding cookie and sends the
// browser to the provider.
func (h *OAuthHandler) openFlow(w http.ResponseWriter, r *http.Request, req OAuthStartRequest, linkingUserID int) {
	result, err := h.startFlow(r.Context(), req, linkingUserID)
	if err != nil {
		h.writeStartFailure(w, r, req, err)
		return
	}
	http.SetCookie(w, result.Cookie)
	http.Redirect(w, r, result.AuthorizeURL, http.StatusFound)
}

// parkNativeStart keeps a checked native start that arrived on another
// origin than the public URL, and sends the browser to the native start on
// the public origin with only the parked start's random ID. The start
// origin, challenge, app state and link stay on the server, so a page the
// browser passes through cannot name another start origin.
func (h *OAuthHandler) parkNativeStart(w http.ResponseWriter, r *http.Request, req OAuthStartRequest, linkingUserID int) {
	ctx := r.Context()
	failed := func(err error) {
		slog.WarnContext(ctx, "oauth native start could not be parked", "component", "auth", "installation_id", req.InstallID, "error", err)
		h.writeStartFailure(w, r, req, &OAuthHandshakeError{Status: http.StatusInternalServerError, Message: "native start store failed"})
	}
	if h.deps.NativeStarts == nil {
		failed(errors.New("native start store is unavailable"))
		return
	}
	slog.DebugContext(ctx, "oauth native start arrived on another origin than the public URL", "component", "auth", "installation_id", req.InstallID, "start_origin", req.startOrigin, "public_url", h.currentHostBaseURL())
	id, err := randomHex(32)
	if err != nil {
		failed(err)
		return
	}
	if err := h.deps.NativeStarts.InsertNativeStart(ctx, OAuthNativeStart{
		ID:             id,
		InstallationID: req.InstallID,
		CodeChallenge:  req.CodeChallenge,
		AppState:       req.AppState,
		Prompt:         req.Prompt,
		LinkingUserID:  linkingUserID,
		StartOrigin:    req.startOrigin,
		ExpiresAt:      time.Now().UTC().Add(oauthNativeStartTTL),
	}); err != nil {
		failed(err)
		return
	}
	query := url.Values{OAuthNativeFlowParameter: {id}}
	http.Redirect(w, r, h.PublicURL(NativeStartPath(req.Prefix, req.InstallID), query), http.StatusFound)
}

// resumeNativeStart opens the flow of a parked native start, keeping the
// start origin the start recorded. An unknown, used, expired or foreign ID
// is a plain-text 400: without the parked start there is no app state to
// answer the app with. So is an ID presented on another origin than the
// public URL, where the flow's binding cookie would not reach the callback;
// that refusal leaves the start for the public origin.
func (h *OAuthHandler) resumeNativeStart(w http.ResponseWriter, r *http.Request, req OAuthStartRequest) {
	ctx := r.Context()
	if h.deps.NativeStarts == nil {
		http.Error(w, "native sign-in is unavailable", http.StatusBadRequest)
		return
	}
	if !h.OnPublicOrigin(r) {
		slog.InfoContext(ctx, "oauth parked native start presented on another origin than the public URL", "component", "auth", "installation_id", req.InstallID, "origin", requestOrigin(r), "public_url", h.currentHostBaseURL())
		http.Error(w, nativeStartUnknownMessage, http.StatusBadRequest)
		return
	}
	start, err := h.deps.NativeStarts.ConsumeNativeStart(ctx, req.Flow)
	if err != nil || start.InstallationID != req.InstallID {
		if err != nil && !errors.Is(err, ErrOAuthNativeStartInvalid) {
			slog.WarnContext(ctx, "oauth native start lookup failed", "component", "auth", "installation_id", req.InstallID, "error", err)
		}
		http.Error(w, nativeStartUnknownMessage, http.StatusBadRequest)
		return
	}
	req = OAuthStartRequest{
		InstallID:     start.InstallationID,
		Prefix:        req.Prefix,
		Native:        true,
		CodeChallenge: start.CodeChallenge,
		AppState:      start.AppState,
		Prompt:        start.Prompt,
		startOrigin:   start.StartOrigin,
	}
	if err := h.checkStart(ctx, req); err != nil {
		h.writeStartFailure(w, r, req, err)
		return
	}
	h.openFlow(w, r, req, start.LinkingUserID)
}

// writeStartFailure answers a start that cannot open its flow. Malformed
// parameters stay plain-text 400s. A v2 start that failed on the provider
// or the server (no public URL, plugin unavailable, InitAuthorize failed)
// sends the browser somewhere it can recover from instead of a raw text
// page: a web start to the login page with provider_unavailable (which
// does not redirect to the provider again while it shows an error), a
// native start with a valid code_challenge and app_state to the app
// redirect. The frozen v1 init keeps its plain-text answers.
func (h *OAuthHandler) writeStartFailure(w http.ResponseWriter, r *http.Request, req OAuthStartRequest, err error) {
	var he *OAuthHandshakeError
	if req.Prefix == oauthV1Prefix || !errors.As(err, &he) || he.Status == http.StatusBadRequest {
		writeHandshakeError(w, err)
		return
	}
	reason := OAuthReasonProviderUnavailable
	if he.Status == http.StatusInternalServerError {
		reason = OAuthReasonLoginFailed
	}
	if req.Native {
		if !ValidCodeChallenge(req.CodeChallenge) || !ValidAppState(req.AppState) {
			writeHandshakeError(w, err)
			return
		}
		http.Redirect(w, r, h.nativeRedirect(r.Context(), req.startOrigin, req.AppState, "error", reason), http.StatusFound)
		return
	}
	http.Redirect(w, r, webFailureLocation(reason), http.StatusFound)
}

// oauthV1Prefix is the frozen v1 API prefix, whose init answers stay as
// they were.
const oauthV1Prefix = "/api/v1"

// oauthV2Prefix is the v2 API prefix.
const oauthV2Prefix = "/api/v2"

// consumeLinkTicket redeems a link ticket for installID. Any failure is
// ErrOAuthLinkTicketInvalid.
func (h *OAuthHandler) consumeLinkTicket(ctx context.Context, value string, installID int) (OAuthLinkTicket, error) {
	if h.deps.LinkTickets == nil {
		return OAuthLinkTicket{}, ErrOAuthLinkTicketInvalid
	}
	ticket, err := h.deps.LinkTickets.ConsumeLinkTicket(ctx, value)
	if err != nil {
		if !errors.Is(err, ErrOAuthLinkTicketInvalid) {
			slog.WarnContext(ctx, "oauth link ticket lookup failed", "component", "auth", "installation_id", installID, "error", err)
		}
		return OAuthLinkTicket{}, ErrOAuthLinkTicketInvalid
	}
	if installID != 0 && ticket.InstallationID != installID {
		return OAuthLinkTicket{}, ErrOAuthLinkTicketInvalid
	}
	return ticket, nil
}

// checkStart refuses a start that cannot succeed before anything is
// written or the browser is bounced.
func (h *OAuthHandler) checkStart(ctx context.Context, req OAuthStartRequest) error {
	if h.currentHostBaseURL() == "" {
		return &OAuthHandshakeError{Status: http.StatusConflict, Message: "Silo public URL is not configured"}
	}
	if req.LinkTicket != "" && !req.Native {
		return &OAuthHandshakeError{Status: http.StatusBadRequest, Message: "link_ticket is accepted by the native start only; web linking starts with startAccountIdentityLink"}
	}
	if req.Prompt != "" && req.Prompt != OIDCPromptSelectAccount {
		return &OAuthHandshakeError{Status: http.StatusBadRequest, Message: "prompt must be select_account"}
	}
	if req.Native {
		if !ValidCodeChallenge(req.CodeChallenge) {
			return &OAuthHandshakeError{Status: http.StatusBadRequest, Message: "code_challenge must be an S256 challenge"}
		}
		if !ValidAppState(req.AppState) {
			return &OAuthHandshakeError{Status: http.StatusBadRequest, Message: "app_state must be 1 to 512 unreserved characters"}
		}
	}
	if _, _, err := h.deps.ResolveClient(ctx, req.InstallID); err != nil {
		return &OAuthHandshakeError{Status: http.StatusBadGateway, Message: "auth plugin unavailable"}
	}
	return nil
}

// startFlow opens a flow with the plugin once checkStart passed: it stores
// the flow with the hash of a fresh browser binder, and returns the
// provider's authorize URL and the binding cookie. linkingUserID is the
// account a linking flow links to, 0 for a sign-in. A failure is an
// *OAuthHandshakeError.
func (h *OAuthHandler) startFlow(ctx context.Context, req OAuthStartRequest, linkingUserID int) (OAuthStartResult, error) {
	next := normalizeOAuthNext(req.Next)
	kind := OAuthFlowWeb
	if req.Native {
		kind = OAuthFlowNative
	}
	client, _, err := h.deps.ResolveClient(ctx, req.InstallID)
	if err != nil {
		return OAuthStartResult{}, &OAuthHandshakeError{Status: http.StatusBadGateway, Message: "auth plugin unavailable"}
	}

	nonce, err := randomHex(16)
	if err != nil {
		return OAuthStartResult{}, &OAuthHandshakeError{Status: http.StatusInternalServerError, Message: "rand failure"}
	}
	binder, binderHash, err := newOAuthBinder()
	if err != nil {
		return OAuthStartResult{}, &OAuthHandshakeError{Status: http.StatusInternalServerError, Message: "rand failure"}
	}

	now := time.Now().UTC()
	state := SignState(h.deps.StateSecret, StatePayload{
		Nonce:     nonce,
		InstallID: strconv.Itoa(req.InstallID),
		ExpiresAt: now.Add(h.deps.StateTTL),
	})
	redirectURI := h.CallbackURL(req.Prefix, req.InstallID)

	initReq := &pluginv1.InitAuthorizeRequest{
		RedirectUri: redirectURI,
		State:       state,
		Linking:     linkingUserID != 0,
	}
	if linkingUserID != 0 {
		// A link must come from a deliberate sign-in at the provider, not
		// from whatever provider session the browser already holds.
		initReq.Prompt = oidcPromptLogin
	} else if req.Prompt == OIDCPromptSelectAccount {
		initReq.Prompt = OIDCPromptSelectAccount
	}
	resp, err := client.InitAuthorize(ctx, initReq)
	if err != nil {
		slog.WarnContext(ctx, "oauth init_authorize failed", "component", "auth", "installation_id", req.InstallID, "error", err)
		return OAuthStartResult{}, &OAuthHandshakeError{Status: http.StatusBadGateway, Message: "plugin init_authorize failed"}
	}
	if resp.GetAuthorizeUrl() == "" {
		return OAuthStartResult{}, &OAuthHandshakeError{Status: http.StatusBadGateway, Message: "plugin returned empty authorize_url"}
	}

	psBytes, _ := json.Marshal(resp.GetProviderState().AsMap())
	sess := OAuthSession{
		State:         state,
		InstallID:     strconv.Itoa(req.InstallID),
		RedirectURI:   redirectURI,
		ProviderState: psBytes,
		NextURL:       next,
		BinderHash:    binderHash,
		Kind:          kind,
		ExpiresAt:     now.Add(h.deps.StateTTL),
	}
	if linkingUserID != 0 {
		sess.LinkingUserID = strconv.Itoa(linkingUserID)
	}
	if req.Native {
		sess.CodeChallenge = req.CodeChallenge
		sess.AppState = req.AppState
		sess.StartOrigin = req.startOrigin
	}
	if err := h.deps.Store.Insert(ctx, sess); err != nil {
		slog.WarnContext(ctx, "oauth session insert failed", "component", "auth", "installation_id", req.InstallID, "error", err)
		return OAuthStartResult{}, &OAuthHandshakeError{Status: http.StatusInternalServerError, Message: "store insert failed"}
	}

	return OAuthStartResult{
		AuthorizeURL: resp.GetAuthorizeUrl(),
		Cookie:       oauthBinderCookie(state, binder, redirectURI, int((h.deps.StateTTL+OAuthExpiredFlowGrace)/time.Second)),
	}, nil
}

// OAuthCallbackInput is the provider redirect as the transport received it.
type OAuthCallbackInput struct {
	InstallID int
	State     string
	Code      string
	// ProviderError is the OAuth error the provider redirected back with
	// instead of a code.
	ProviderError string
	// Binder is the value of the flow's binding cookie; empty when the
	// browser sent none.
	Binder string
	// CompletionBinder is the value of the completion cookie the transport
	// sets when the flow ends in a web completion code
	// (OAuthCompletionCookieName); the code stores its hash. A web flow
	// without one fails.
	CompletionBinder string
	UserAgent        string
	IP               string
}

// HandleInit serves POST /api/v1/auth/oauth/{install_id}/init. A browser on
// another origin than the public URL is sent there first (307 keeps the
// POST), so the binding cookie lands where the callback will read it.
func (h *OAuthHandler) HandleInit(w http.ResponseWriter, r *http.Request) {
	installID, err := strconv.Atoi(chi.URLParam(r, "install_id"))
	if err != nil || installID <= 0 {
		http.Error(w, "invalid install_id", http.StatusBadRequest)
		return
	}
	query := url.Values{}
	if next := r.URL.Query().Get("next"); next != "" {
		query.Set("next", next)
	}
	query.Set(OAuthBounceParameter, "1")
	bounce := h.PublicURL("/api/v1/auth/oauth/"+strconv.Itoa(installID)+"/init", query)
	h.ServeStart(w, r, OAuthStartRequest{InstallID: installID, Prefix: "/api/v1", Next: r.URL.Query().Get("next")}, bounce, http.StatusTemporaryRedirect)
}

// HandleCallback serves GET /api/v1/auth/oauth/{install_id}/callback.
func (h *OAuthHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	installID, err := strconv.Atoi(chi.URLParam(r, "install_id"))
	if err != nil || installID <= 0 {
		http.Error(w, "invalid install_id", http.StatusBadRequest)
		return
	}
	h.ServeCallback(w, r, "/api/v1", installID)
}

// ServeCallback answers the provider's redirect back under one API prefix:
// it reads the flow's binding cookie, finishes the flow, clears the cookie
// and redirects the browser on. A flow that ends in a web completion code
// also gets the completion cookie, on the complete path of each API
// version, so only this browser can redeem the code.
func (h *OAuthHandler) ServeCallback(w http.ResponseWriter, r *http.Request, prefix string, installID int) {
	query := r.URL.Query()
	state, code, providerError := query.Get("state"), query.Get("code"), query.Get("error")
	if state == "" || (code == "" && providerError == "") {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}
	binder := ""
	if cookie, err := r.Cookie(OAuthBinderCookieName(state)); err == nil {
		binder = cookie.Value
		http.SetCookie(w, oauthBinderCookie(state, "", h.CallbackURL(prefix, installID), -1))
	}
	completionBinder, _, err := newOAuthBinder()
	if err != nil {
		http.Redirect(w, r, webFailureLocation(OAuthReasonLoginFailed), http.StatusFound)
		return
	}
	location := h.Callback(r.Context(), OAuthCallbackInput{
		InstallID: installID, State: state, Code: code, ProviderError: providerError,
		Binder: binder, CompletionBinder: completionBinder, UserAgent: r.UserAgent(), IP: clientIP(r),
	})
	if strings.HasPrefix(location, h.webCompletionURL()+"?") {
		for _, apiPrefix := range []string{oauthV1Prefix, oauthV2Prefix} {
			http.SetCookie(w, oauthCompletionCookie(completionBinder, h.PublicURL(apiPrefix+oauthCompletePath, nil)))
		}
	}
	http.Redirect(w, r, location, http.StatusFound)
}

// oauthCompletePath is the complete operation below an API prefix.
const oauthCompletePath = "/auth/oauth/complete"

// webCompletionURL is the web page a web flow's callback sends the browser
// to with its completion code.
func (h *OAuthHandler) webCompletionURL() string {
	return h.currentHostBaseURL() + h.deps.FrontendCompletePath
}

// webFailureLocation is the login page reporting a failed sign-in.
func webFailureLocation(reason string) string {
	return "/login?error=oauth_failed&reason=" + url.QueryEscape(reason)
}

// failureLocation is where a failed flow sends the browser: the app
// redirect for a native flow, the flow's return path for a web linking
// flow, and the login page otherwise.
func (h *OAuthHandler) failureLocation(ctx context.Context, issuer string, sess OAuthSession, reason string) string {
	switch {
	case sess.Kind == OAuthFlowNative:
		return h.nativeRedirect(ctx, issuer, sess.AppState, "error", reason)
	case sess.LinkingUserID != "":
		return withQuery(normalizeOAuthNext(sess.NextURL), "error", "oauth_link_failed", "reason", reason)
	}
	return webFailureLocation(reason)
}

// nativeRedirect is the fixed app redirect carrying key=value, the app's
// state, the issuing origin and this server's identity.
func (h *OAuthHandler) nativeRedirect(ctx context.Context, issuer, appState, key, value string) string {
	q := url.Values{}
	q.Set(key, value)
	q.Set("state", appState)
	return h.appRedirect(ctx, issuer, q)
}

// appRedirect is the fixed app redirect with q plus iss and server. Every
// app redirect of the native and link flows, success or failure, ends here.
func (h *OAuthHandler) appRedirect(ctx context.Context, issuer string, q url.Values) string {
	q.Set(oauthIssuerParameter, issuer)
	h.addServerID(ctx, q)
	return NativeAppRedirectURI + "?" + q.Encode()
}

// oauthIssuerParameter names the start origin of the flow an app redirect
// ends: the origin where its native start first arrived, which is the app's
// saved server address. The app opens the native start on its saved server
// base, accepts a redirect only when iss equals that base's origin and
// redeems the code there. A saved server that redirects its start to
// another server gets that server's own origin as iss, which the app
// refuses, so it never receives the code. The start origin comes from the
// Host header, which the sender chooses: a saved server that sends the
// app's start here itself could name its own origin. knownStartOrigin
// therefore accepts only origins a server outside the local network cannot
// hold, so the residual relay needs a hostile server on a local address.
const oauthIssuerParameter = "iss"

// requestOrigin is the origin r arrived on, serialized like an iss and
// derived like OnPublicOrigin: the host a trusted proxy names in
// X-Forwarded-Host or the Host header, and the scheme of the connection or
// the one a trusted proxy names. On the public origin it is the public
// origin.
func requestOrigin(r *http.Request) string {
	scheme := clientip.RequestScheme(r)
	if scheme == "" {
		// Ambiguous proxy metadata: name the transport's scheme. A wrong
		// guess makes the app refuse the redirect, never accept a relay.
		scheme = schemeHTTP
		if r.TLS != nil {
			scheme = schemeHTTPS
		}
	}
	return serializeOrigin(scheme, clientip.RequestHost(r))
}

// cgnatRange is the shared address space of RFC 6598, which carrier NAT and
// overlay networks such as Tailscale use.
var cgnatRange = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

// localNameSuffixes are DNS suffixes reserved or conventionally used for
// names that resolve only inside a local network.
var localNameSuffixes = []string{".local", ".lan", ".localdomain", ".home.arpa", ".internal"}

// knownStartOrigin reports whether a native start that arrived on origin, an
// origin other than the public URL, may record it as its start origin. The
// Host header names that origin and whoever sends the request chooses it, so
// a hostile saved server could send the app's start here itself with its
// own origin as Host and get that origin as iss. Only origins such a server
// cannot hold from outside the local network are accepted: the origins of
// the connected network access providers, IP literals in the loopback,
// private, link-local and shared (RFC 6598) ranges, and single-label or
// local-use host names (.local, .lan, .localdomain, .home.arpa, .internal).
func (h *OAuthHandler) knownStartOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if h.deps.KnownOrigins != nil {
		for _, raw := range h.deps.KnownOrigins() {
			known, err := url.Parse(raw)
			if err == nil && known.Host != "" && serializeOrigin(known.Scheme, known.Host) == origin {
				return true
			}
		}
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnatRange.Contains(ip)
	}
	if host == "" {
		return false
	}
	if !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range localNameSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// serializeOrigin is scheme://host[:port] with the scheme and host in lower
// case, the default port of the scheme left out, and no path or trailing
// slash.
func serializeOrigin(scheme, host string) string {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	hostURL := url.URL{Host: strings.TrimSpace(host)}
	name, port := strings.ToLower(hostURL.Hostname()), hostURL.Port()
	if (scheme == schemeHTTPS && port == "443") || (scheme == schemeHTTP && port == "80") {
		port = ""
	}
	if strings.Contains(name, ":") {
		name = "[" + name + "]"
	}
	if port != "" {
		name += ":" + port
	}
	return scheme + "://" + name
}

// addServerID adds this server's identity to an app redirect.
func (h *OAuthHandler) addServerID(ctx context.Context, q url.Values) {
	if h.deps.ServerID == nil {
		return
	}
	if id, err := h.deps.ServerID(ctx); err == nil && id != "" {
		q.Set("server", id)
	} else if err != nil {
		slog.WarnContext(ctx, "oauth native redirect has no server identity", "component", "auth", "error", err)
	}
}

// Callback finishes the provider handshake and returns where the browser is
// sent. A web sign-in goes to the SPA completion page with a one-time code,
// a native one to the app redirect with a code bound to its challenge. A web
// linking flow links and returns to its path; a native linking flow parks
// the link under a code bound to the app's challenge (link=1), which the app
// confirms with CompleteLink. Linking never opens a session. Failures carry
// a reason code.
func (h *OAuthHandler) Callback(ctx context.Context, in OAuthCallbackInput) string {
	installID, state := in.InstallID, in.State
	payload, err := VerifyState(h.deps.StateSecret, state)
	// An authentic state past its expiry (a slow provider sign-in: MFA
	// enrolment, a password reset) still loads its flow, so the browser that
	// started it is sent to its own client's session_expired failure: the
	// app redirect or the linking page, not the web login.
	expired := errors.Is(err, ErrStateExpired)
	if (err != nil && !expired) || payload.InstallID != strconv.Itoa(installID) {
		return webFailureLocation(OAuthReasonStateInvalid)
	}

	sess, err := h.deps.Store.GetAndDelete(ctx, state)
	if err != nil {
		return webFailureLocation(OAuthReasonSessionExpired)
	}
	// The app redirect names where the native start first arrived.
	issuer := sess.StartOrigin
	fail := func(reason string) string { return h.failureLocation(ctx, issuer, sess, reason) }
	if !oauthBinderMatches(in.Binder, sess.BinderHash) {
		slog.WarnContext(ctx, "oauth callback without the browser that started the flow", "component", "auth", "installation_id", installID)
		// The browser holding the flow may not be this one, so it does not
		// learn where the flow would have gone.
		return webFailureLocation(OAuthReasonStateInvalid)
	}
	if expired || sess.ExpiresAt.Before(time.Now()) {
		return fail(OAuthReasonSessionExpired)
	}
	if in.ProviderError != "" {
		slog.InfoContext(ctx, "oauth provider returned an error", "component", "auth", "installation_id", installID, "provider_error", in.ProviderError)
		return fail(providerErrorReason(in.ProviderError))
	}
	if in.Code == "" {
		return fail(OAuthReasonStateInvalid)
	}

	client, capabilityID, err := h.deps.ResolveClient(ctx, installID)
	if err != nil {
		return fail(OAuthReasonProviderUnavailable)
	}

	var ps map[string]any
	_ = json.Unmarshal(sess.ProviderState, &ps)
	psStruct, _ := structpb.NewStruct(ps)

	resp, err := client.ExchangeCode(ctx, &pluginv1.ExchangeCodeRequest{
		Code:          in.Code,
		State:         state,
		RedirectUri:   sess.RedirectURI,
		ProviderState: psStruct,
	})
	if err != nil {
		slog.WarnContext(ctx, "oauth exchange_code failed", "component", "auth", "installation_id", installID, "error", err)
		return fail(OAuthReasonProviderUnavailable)
	}
	if resp.GetDenial() == pluginv1.AuthDenial_AUTH_DENIAL_UNSPECIFIED && resp.GetExternalSubject() == "" {
		return fail(OAuthReasonLoginFailed)
	}

	login := OAuthLoginInput{
		InstallationID: installID,
		CapabilityID:   capabilityID,
		Response:       resp,
		DeviceName:     in.UserAgent,
		IP:             in.IP,
	}
	if sess.LinkingUserID != "" {
		uid, err := strconv.Atoi(sess.LinkingUserID)
		if err != nil || uid <= 0 {
			return fail(OAuthReasonStateInvalid)
		}
		login.LinkingUserID = uid
		if sess.Kind == OAuthFlowNative {
			// The browser that ran a native flow may not belong to the
			// account that asked for the ticket, so the link waits for that
			// account's app to redeem the code with its verifier and bearer.
			return h.parkNativeLink(ctx, issuer, sess, login, fail)
		}
		if _, err := h.deps.LoginCompleter.LinkOAuthIdentity(ctx, login); err != nil {
			slog.WarnContext(ctx, "oauth identity link failed", "component", "auth", "installation_id", installID, "user_id", uid, "error", err)
			return fail(OAuthFailureReason(err))
		}
		return withQuery(normalizeOAuthNext(sess.NextURL), "linked", "1")
	}

	if sess.Kind != OAuthFlowNative && in.CompletionBinder == "" {
		// A web code without a browser binding could be redeemed anywhere.
		slog.WarnContext(ctx, "oauth web callback has no completion binder", "component", "auth", "installation_id", installID)
		return fail(OAuthReasonLoginFailed)
	}
	user, identityID, err := h.deps.LoginCompleter.ResolveOAuthLogin(ctx, login)
	if err == nil && user == nil {
		err = errors.New("account resolution answered no account")
	}
	if err != nil {
		slog.WarnContext(ctx, "oauth login completion failed", "component", "auth", "installation_id", installID, "error", err)
		return fail(OAuthFailureReason(err))
	}

	if h.deps.CompletionStore == nil {
		slog.WarnContext(ctx, "oauth completion store is unavailable", "component", "auth", "installation_id", installID)
		return fail(OAuthReasonLoginFailed)
	}
	completionCode, err := randomHex(32)
	if err != nil {
		slog.WarnContext(ctx, "oauth completion code generation failed", "component", "auth", "installation_id", installID, "error", err)
		return fail(OAuthReasonLoginFailed)
	}
	// The session opens when the code is redeemed (Complete), so a code
	// nobody redeems leaves none behind.
	completion := OAuthCompletion{
		Code:          completionCode,
		NextURL:       sess.NextURL,
		Kind:          sess.Kind,
		CodeChallenge: sess.CodeChallenge,
		UserID:        user.ID,
		IdentityID:    identityID,
		DeviceName:    login.DeviceName,
		IP:            login.IP,
		ExpiresAt:     time.Now().UTC().Add(oauthCompletionTTL),
	}
	if sess.Kind != OAuthFlowNative {
		completion.BrowserHash = oauthBinderHash(in.CompletionBinder)
	}
	if err := h.deps.CompletionStore.InsertCompletion(ctx, completion); err != nil {
		slog.WarnContext(ctx, "oauth completion insert failed", "component", "auth", "installation_id", installID, "error", err)
		return fail(OAuthReasonLoginFailed)
	}

	if sess.Kind == OAuthFlowNative {
		return h.nativeRedirect(ctx, issuer, sess.AppState, "code", completionCode)
	}
	values := url.Values{}
	values.Set("code", completionCode)
	return h.webCompletionURL() + "?" + values.Encode()
}

// parkNativeLink stores a native linking flow's answer under a one-time
// code bound to the app's challenge and sends the browser to the app with
// that code and link=1.
func (h *OAuthHandler) parkNativeLink(ctx context.Context, issuer string, sess OAuthSession, login OAuthLoginInput, fail func(string) string) string {
	if h.deps.PendingLinks == nil {
		slog.WarnContext(ctx, "oauth pending link store is unavailable", "component", "auth", "installation_id", login.InstallationID)
		return fail(OAuthReasonLoginFailed)
	}
	code, err := randomHex(32)
	if err != nil {
		return fail(OAuthReasonLoginFailed)
	}
	if err := h.deps.PendingLinks.InsertPendingLink(ctx, OAuthPendingLink{
		Code:           code,
		UserID:         login.LinkingUserID,
		InstallationID: login.InstallationID,
		CapabilityID:   login.CapabilityID,
		CodeChallenge:  sess.CodeChallenge,
		Response:       login.Response,
		ExpiresAt:      time.Now().UTC().Add(oauthCompletionTTL),
	}); err != nil {
		slog.WarnContext(ctx, "oauth pending link insert failed", "component", "auth", "installation_id", login.InstallationID, "error", err)
		return fail(OAuthReasonLoginFailed)
	}
	q := url.Values{}
	q.Set("code", code)
	q.Set("link", "1")
	q.Set("state", sess.AppState)
	return h.appRedirect(ctx, issuer, q)
}

// revokeIssuedSession ends the session a reused completion code opened.
func (h *OAuthHandler) revokeIssuedSession(ctx context.Context, sessionID string) {
	if sessionID == "" || h.deps.RevokeSession == nil {
		return
	}
	if err := h.deps.RevokeSession(ctx, sessionID); err != nil {
		slog.WarnContext(ctx, "oauth session revocation failed", "component", "auth", "error", err)
	}
}

// OAuthFailureReason is the reason code the login page shows for a failed
// sign-in resolution. Unknown failures stay login_failed.
func OAuthFailureReason(err error) string {
	switch {
	case errors.Is(err, ErrAccountRequired):
		// Before ErrNotPermitted, which it wraps.
		return OAuthReasonAccountRequired
	case errors.Is(err, ErrNotPermitted):
		return OAuthReasonNotPermitted
	case errors.Is(err, ErrEmailInUse):
		return OAuthReasonEmailInUse
	case errors.Is(err, ErrIdentityLinkedElsewhere):
		return OAuthReasonIdentityLinkedElsewhere
	case errors.Is(err, ErrAccountAlreadyLinked):
		return OAuthReasonAlreadyLinked
	case errors.Is(err, ErrUserDisabled):
		return OAuthReasonAccountDisabled
	case errors.Is(err, ErrProviderUnavailable):
		return OAuthReasonProviderUnavailable
	}
	return OAuthReasonLoginFailed
}

type OAuthCompleteRequest struct {
	Code string `json:"code"`
}

type OAuthCompleteResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	NextURL      string `json:"next"`
}

// HandleComplete serves v1 POST /auth/oauth/complete. v1 takes no
// code_verifier, so it redeems web codes only, with the browser's
// completion cookie.
func (h *OAuthHandler) HandleComplete(w http.ResponseWriter, r *http.Request) {
	if h.deps.CompletionStore == nil {
		http.Error(w, "oauth completion unavailable", http.StatusServiceUnavailable)
		return
	}
	var req OAuthCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	browser := ""
	if cookie, err := r.Cookie(OAuthCompletionCookieName); err == nil {
		browser = cookie.Value
	}
	completion, err := h.Complete(WithClientDevice(r.Context(), r.Header), req.Code, "", browser)
	if err != nil {
		if errors.Is(err, ErrOAuthCodeRequired) {
			http.Error(w, "code required", http.StatusBadRequest)
			return
		}
		http.Error(w, "invalid or expired completion code", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(OAuthCompleteResponse{
		AccessToken:  completion.AccessToken,
		RefreshToken: completion.RefreshToken,
		ExpiresIn:    completion.ExpiresIn,
		NextURL:      completion.NextURL,
	})
}

// Errors Complete reports; each transport renders them in its own shape.
var (
	ErrOAuthCompletionUnavailable = errors.New("oauth completion unavailable")
	ErrOAuthCodeRequired          = errors.New("oauth completion code required")
	ErrOAuthCompletionInvalid     = errors.New("oauth completion code invalid or expired")
)

// Complete redeems a one-time completion code: it opens the login session
// of the account the callback resolved and answers its token pair. A native
// code needs the verifier of its S256 challenge; a web code takes none and
// needs browser, the value of the completion cookie the callback set
// (ErrOAuthInvalidGrant otherwise; the code stays redeemable). A refused
// redemption opens no session. A second redemption revokes the session the
// first opened and is refused like an unknown code. v1 POST /auth/oauth/complete and v2
// completeOAuthLogin both call it.
func (h *OAuthHandler) Complete(ctx context.Context, code, verifier, browser string) (OAuthCompletion, error) {
	if h.deps.CompletionStore == nil || h.deps.LoginCompleter == nil {
		return OAuthCompletion{}, ErrOAuthCompletionUnavailable
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return OAuthCompletion{}, ErrOAuthCodeRequired
	}
	completion, err := h.deps.CompletionStore.RedeemCompletion(ctx, code, strings.TrimSpace(verifier), browser, h.deps.LoginCompleter.OpenOAuthSession)
	switch {
	case err == nil:
		return completion, nil
	case errors.Is(err, ErrOAuthCompletionReused):
		slog.WarnContext(ctx, "oauth completion code redeemed twice; revoking its session", "component", "auth",
			"user_id", completion.UserID, "flow", completion.Kind)
		h.revokeIssuedSession(ctx, completion.SessionID)
		return OAuthCompletion{}, ErrOAuthCompletionInvalid
	case errors.Is(err, ErrOAuthInvalidGrant):
		return OAuthCompletion{}, err
	case errors.Is(err, ErrOAuthCompletionNotFound):
		return OAuthCompletion{}, ErrOAuthCompletionInvalid
	}
	slog.WarnContext(ctx, "oauth completion redemption failed", "component", "auth", "error", err)
	return OAuthCompletion{}, ErrOAuthCompletionInvalid
}

// Link ticket errors.
var (
	// ErrLinkTicketPassword is a wrong local password confirming a link: a
	// link ticket request or a directory (credentials) link.
	ErrLinkTicketPassword = errors.New("password is incorrect")
	// ErrLinkTicketUnavailable reports that link tickets are not wired.
	ErrLinkTicketUnavailable = errors.New("oauth linking unavailable")
)

// LinkingAvailable reports whether link tickets can be issued.
func (h *OAuthHandler) LinkingAvailable() bool {
	return h != nil && h.deps.LinkTickets != nil && h.deps.PendingLinks != nil && h.deps.Users != nil
}

// IssueLinkTicket gives a signed-in account a single-use ticket that starts
// a linking flow for installationID, after it re-enters its local password.
// The account must have local password sign-in (a linked or provider-created
// account has none to confirm). The ticket travels on the start URL because
// the browser that runs the flow holds no bearer token.
func (h *OAuthHandler) IssueLinkTicket(ctx context.Context, userID, installationID int, password string) (OAuthLinkTicket, error) {
	if !h.LinkingAvailable() {
		return OAuthLinkTicket{}, ErrLinkTicketUnavailable
	}
	if _, _, err := h.deps.ResolveClient(ctx, installationID); err != nil {
		if errors.Is(err, ErrUnknownAuthInstallation) {
			return OAuthLinkTicket{}, err
		}
		return OAuthLinkTicket{}, ErrProviderUnavailable
	}
	if _, err := confirmLocalPassword(ctx, h.deps.Users, userID, password); err != nil {
		return OAuthLinkTicket{}, err
	}
	value, err := randomHex(32)
	if err != nil {
		return OAuthLinkTicket{}, err
	}
	ticket := OAuthLinkTicket{
		Ticket:         value,
		UserID:         userID,
		InstallationID: installationID,
		ExpiresAt:      time.Now().UTC().Add(h.deps.LinkTicketTTL),
	}
	if err := h.deps.LinkTickets.InsertLinkTicket(ctx, ticket); err != nil {
		return OAuthLinkTicket{}, err
	}
	auditAuthEvent(ctx, "identity_link_started", auditInstallationID, installationID, auditUserID, userID)
	return ticket, nil
}

// Link flow errors.
var (
	// ErrOAuthPublicOriginRequired refuses a web linking start that did not
	// arrive on the public origin, where the callback reads the binding
	// cookie.
	ErrOAuthPublicOriginRequired = errors.New("oauth linking must start on the public URL")
)

// StartLink opens a web linking flow for the signed-in account userID with
// a link ticket that account was issued. The caller is an authenticated
// request on the public origin, and the binding cookie goes to the browser
// that made it, so only that browser can finish the flow: a ticket someone
// else opens cannot link their provider identity to the ticket's account.
// An unknown, used, expired or foreign ticket is ErrOAuthLinkTicketInvalid.
func (h *OAuthHandler) StartLink(ctx context.Context, userID int, prefix, ticketValue, next string) (OAuthStartResult, error) {
	if !h.LinkingAvailable() {
		return OAuthStartResult{}, ErrLinkTicketUnavailable
	}
	if h.currentHostBaseURL() == "" {
		return OAuthStartResult{}, ErrOAuthPublicOriginRequired
	}
	ticket, err := h.consumeLinkTicket(ctx, ticketValue, 0)
	if err != nil {
		return OAuthStartResult{}, err
	}
	if ticket.UserID != userID {
		slog.WarnContext(ctx, "oauth link ticket presented by another account", "component", "auth",
			"installation_id", ticket.InstallationID, auditUserID, userID)
		return OAuthStartResult{}, ErrOAuthLinkTicketInvalid
	}
	if _, _, err := h.deps.ResolveClient(ctx, ticket.InstallationID); err != nil {
		if errors.Is(err, ErrUnknownAuthInstallation) {
			return OAuthStartResult{}, err
		}
		return OAuthStartResult{}, ErrProviderUnavailable
	}
	result, err := h.startFlow(ctx, OAuthStartRequest{InstallID: ticket.InstallationID, Prefix: prefix, Next: next}, userID)
	if err != nil {
		if he, ok := errors.AsType[*OAuthHandshakeError](err); ok && he.Status == http.StatusBadGateway {
			return OAuthStartResult{}, ErrProviderUnavailable
		}
		return OAuthStartResult{}, err
	}
	return result, nil
}

// CompleteLink makes the link a native linking flow parked: the app
// redeems the code with the verifier of its challenge, signed in as the
// account the link ticket was issued to. A wrong verifier is
// ErrOAuthInvalidGrant (the code stays redeemable); an unknown, used or
// expired code, or another account, is ErrOAuthCompletionInvalid. The link
// itself follows account resolution (ErrIdentityLinkedElsewhere,
// ErrAccountAlreadyLinked, ...).
func (h *OAuthHandler) CompleteLink(ctx context.Context, userID int, code, verifier string) error {
	if h.deps.PendingLinks == nil {
		return ErrOAuthCompletionUnavailable
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return ErrOAuthCodeRequired
	}
	link, err := h.deps.PendingLinks.RedeemPendingLink(ctx, code, strings.TrimSpace(verifier), userID)
	switch {
	case err == nil:
	case errors.Is(err, ErrOAuthInvalidGrant):
		return err
	case errors.Is(err, ErrOAuthCompletionNotFound):
		return ErrOAuthCompletionInvalid
	default:
		slog.WarnContext(ctx, "oauth pending link redemption failed", "component", "auth", "error", err)
		return ErrOAuthCompletionInvalid
	}
	_, err = h.deps.LoginCompleter.LinkOAuthIdentity(ctx, OAuthLoginInput{
		InstallationID: link.InstallationID,
		CapabilityID:   link.CapabilityID,
		Response:       link.Response,
		LinkingUserID:  userID,
	})
	if err != nil {
		slog.WarnContext(ctx, "oauth identity link failed", "component", "auth", "installation_id", link.InstallationID, auditUserID, userID, "error", err)
	}
	return err
}

// ProviderLogoutAvailable reports whether provider logout is wired.
func (h *OAuthHandler) ProviderLogoutAvailable() bool {
	return h != nil && h.deps.ProviderLogout != nil
}

// ProviderLogoutURL is the provider end-session URL the web client sends
// the browser to after ending an account's Silo session; empty when the
// administrator has not turned provider logout on, the account has no
// identity at an OAuth provider, or the plugin offers none. The provider
// returns the browser to the login page on the public origin.
func (h *OAuthHandler) ProviderLogoutURL(ctx context.Context, userID int) (string, error) {
	redirect := h.PostLogoutRedirectURL()
	if !h.ProviderLogoutAvailable() || redirect == "" {
		return "", nil
	}
	// The web logout waits for this answer before it ends the Silo session,
	// so a provider whose discovery is slow must not hold it up: past the
	// bound the answer is empty and the logout goes ahead without the
	// provider.
	ctx, cancel := context.WithTimeout(ctx, providerLogoutTimeout)
	defer cancel()
	return h.deps.ProviderLogout(ctx, userID, redirect)
}

// providerLogoutTimeout bounds the plugin's end-session URL lookup.
const providerLogoutTimeout = 2 * time.Second

// PostLogoutRedirectURL is where a provider sends the browser after a
// provider logout: the login page on the public origin, which the
// administrator registers at the provider. Empty when no public URL is
// configured.
func (h *OAuthHandler) PostLogoutRedirectURL() string {
	return h.PublicURL("/login", nil)
}

func clientIP(r *http.Request) string {
	if ip := strings.TrimSpace(clientip.FromContext(r.Context())); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return strings.TrimSpace(host)
	}
	return strings.Trim(strings.TrimSpace(r.RemoteAddr), "[]")
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

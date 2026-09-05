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

// OAuthLoginCompleter wraps the post-ExchangeCode work: lookup or provision
// the user identified by the AuthenticateResponse, create a session, mint a
// token pair. Defined as an interface so tests can avoid spinning up the
// full auth.Service.
type OAuthLoginCompleter interface {
	CompleteOAuthLogin(ctx context.Context, in OAuthLoginInput) (*TokenPair, *models.User, error)
}

// OAuthLoginInput carries everything the completer needs to issue a session.
type OAuthLoginInput struct {
	InstallationID int
	CapabilityID   string
	Response       *pluginv1.AuthenticateResponse
	LinkingUserID  int // 0 = not linking
	DeviceName     string
	IP             string
}

// OAuthHandlerDeps wires the OAuthHandler. ResolveClient turns the URL's
// installation_id into a plugin gRPC client; Provisioner consumes the
// AuthenticateResponse and produces a session.
type OAuthHandlerDeps struct {
	Store           OAuthStore
	CompletionStore OAuthCompletionStore
	StateSecret     []byte
	ResolveClient   func(ctx context.Context, installationID int) (OAuthClient, string, error) // returns (client, capabilityID, err)
	LoginCompleter  OAuthLoginCompleter
	HostBaseURL     string
	StateTTL        time.Duration
	// FrontendCompletePath is the SPA path the callback redirects to after
	// minting a one-time completion code. The SPA exchanges that code for tokens.
	FrontendCompletePath string
}

// OAuthHandler serves /init and /callback for OAuth-capable auth plugins.
type OAuthHandler struct {
	deps OAuthHandlerDeps
}

func NewOAuthHandler(d OAuthHandlerDeps) *OAuthHandler {
	if d.StateTTL == 0 {
		d.StateTTL = 10 * time.Minute
	}
	if d.FrontendCompletePath == "" {
		d.FrontendCompletePath = "/login/oauth-complete"
	}
	if d.CompletionStore == nil {
		if store, ok := d.Store.(OAuthCompletionStore); ok {
			d.CompletionStore = store
		}
	}
	return &OAuthHandler{deps: d}
}

// ErrMissingInstallID is returned when the URL path has no install_id.
var ErrMissingInstallID = errors.New("install_id required")

// HandleInit serves POST /api/v1/auth/oauth/{install_id}/init.
func (h *OAuthHandler) HandleInit(w http.ResponseWriter, r *http.Request) {
	installID, err := strconv.Atoi(chi.URLParam(r, "install_id"))
	if err != nil || installID <= 0 {
		http.Error(w, "invalid install_id", http.StatusBadRequest)
		return
	}

	authorizeURL, err := h.Init(r.Context(), installID, r.URL.Query().Get("next"), h.CallbackURL("/api/v1", installID))
	if err != nil {
		writeHandshakeError(w, err)
		return
	}

	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

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
	return strings.TrimRight(h.deps.HostBaseURL, "/") + prefix + "/auth/oauth/" + strconv.Itoa(installID) + "/callback"
}

// Init opens an OAuth session with the plugin and returns the provider's
// authorize URL the browser is sent to. v1 POST /auth/oauth/{install_id}/init
// and the v2 handshake both call it; a failure is an *OAuthHandshakeError.
func (h *OAuthHandler) Init(ctx context.Context, installID int, next, redirectURI string) (string, error) {
	next = normalizeOAuthNext(next)

	client, _, err := h.deps.ResolveClient(ctx, installID)
	if err != nil {
		return "", &OAuthHandshakeError{Status: http.StatusBadGateway, Message: "auth plugin unavailable"}
	}

	nonce, err := randomHex(16)
	if err != nil {
		return "", &OAuthHandshakeError{Status: http.StatusInternalServerError, Message: "rand failure"}
	}

	now := time.Now().UTC()
	state := SignState(h.deps.StateSecret, StatePayload{
		Nonce:     nonce,
		InstallID: strconv.Itoa(installID),
		ExpiresAt: now.Add(h.deps.StateTTL),
	})

	resp, err := client.InitAuthorize(ctx, &pluginv1.InitAuthorizeRequest{
		RedirectUri: redirectURI,
		State:       state,
		// Linking is wired in a follow-up — see TODO below.
	})
	if err != nil {
		slog.WarnContext(ctx, "oauth init_authorize failed", "component", "auth", "installation_id", installID, "error", err)
		return "", &OAuthHandshakeError{Status: http.StatusBadGateway, Message: "plugin init_authorize failed"}
	}
	if resp.GetAuthorizeUrl() == "" {
		return "", &OAuthHandshakeError{Status: http.StatusBadGateway, Message: "plugin returned empty authorize_url"}
	}

	psBytes, _ := json.Marshal(resp.GetProviderState().AsMap())
	sess := OAuthSession{
		State:         state,
		InstallID:     strconv.Itoa(installID),
		RedirectURI:   redirectURI,
		ProviderState: psBytes,
		NextURL:       next,
		ExpiresAt:     now.Add(h.deps.StateTTL),
		// TODO: when linking flow lands, read user_id from existing session
		// and set LinkingUserID here.
	}
	if err := h.deps.Store.Insert(ctx, sess); err != nil {
		slog.WarnContext(ctx, "oauth session insert failed", "component", "auth", "installation_id", installID, "error", err)
		return "", &OAuthHandshakeError{Status: http.StatusInternalServerError, Message: "store insert failed"}
	}

	return resp.GetAuthorizeUrl(), nil
}

// OAuthCallbackInput is the provider redirect as the transport received it.
type OAuthCallbackInput struct {
	InstallID int
	State     string
	Code      string
	UserAgent string
	IP        string
}

// HandleCallback serves GET /api/v1/auth/oauth/{install_id}/callback.
func (h *OAuthHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	installID, err := strconv.Atoi(chi.URLParam(r, "install_id"))
	if err != nil || installID <= 0 {
		http.Error(w, "invalid install_id", http.StatusBadRequest)
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, h.Callback(r.Context(), OAuthCallbackInput{
		InstallID: installID, State: state, Code: code, UserAgent: r.UserAgent(), IP: clientIP(r),
	}), http.StatusFound)
}

// Callback finishes the provider handshake and returns where the browser is
// sent: the SPA completion page carrying a one-time code, or the login page
// with a failure reason. v1 GET /auth/oauth/{install_id}/callback and the
// v2 handshake both call it.
func (h *OAuthHandler) Callback(ctx context.Context, in OAuthCallbackInput) string {
	installID, state, code := in.InstallID, in.State, in.Code
	payload, err := VerifyState(h.deps.StateSecret, state)
	if err != nil {
		return "/login?error=oauth_failed&reason=state_invalid"
	}
	if payload.InstallID != strconv.Itoa(installID) {
		return "/login?error=oauth_failed&reason=install_mismatch"
	}

	sess, err := h.deps.Store.GetAndDelete(ctx, state)
	if err != nil {
		return "/login?error=oauth_failed&reason=session_expired"
	}

	client, capabilityID, err := h.deps.ResolveClient(ctx, installID)
	if err != nil {
		return "/login?error=oauth_failed&reason=plugin_unavailable"
	}

	var ps map[string]any
	_ = json.Unmarshal(sess.ProviderState, &ps)
	psStruct, _ := structpb.NewStruct(ps)

	resp, err := client.ExchangeCode(ctx, &pluginv1.ExchangeCodeRequest{
		Code:          code,
		State:         state,
		RedirectUri:   sess.RedirectURI,
		ProviderState: psStruct,
	})
	if err != nil {
		slog.WarnContext(ctx, "oauth exchange_code failed", "component", "auth", "installation_id", installID, "error", err)
		return "/login?error=oauth_failed&reason=exchange_failed"
	}
	if resp.GetExternalSubject() == "" {
		return "/login?error=oauth_failed&reason=empty_subject"
	}

	linkingUserID := 0
	if sess.LinkingUserID != "" {
		if uid, err := strconv.Atoi(sess.LinkingUserID); err == nil {
			linkingUserID = uid
		}
	}

	pair, _, err := h.deps.LoginCompleter.CompleteOAuthLogin(ctx, OAuthLoginInput{
		InstallationID: installID,
		CapabilityID:   capabilityID,
		Response:       resp,
		LinkingUserID:  linkingUserID,
		DeviceName:     in.UserAgent,
		IP:             in.IP,
	})
	if err != nil {
		slog.WarnContext(ctx, "oauth login completion failed", "component", "auth", "installation_id", installID, "error", err)
		return "/login?error=oauth_failed&reason=login_failed"
	}

	if h.deps.CompletionStore == nil {
		slog.WarnContext(ctx, "oauth completion store is unavailable", "component", "auth", "installation_id", installID)
		return "/login?error=oauth_failed&reason=completion_unavailable"
	}
	completionCode, err := randomHex(32)
	if err != nil {
		slog.WarnContext(ctx, "oauth completion code generation failed", "component", "auth", "installation_id", installID, "error", err)
		return "/login?error=oauth_failed&reason=completion_failed"
	}
	now := time.Now().UTC()
	if err := h.deps.CompletionStore.InsertCompletion(ctx, OAuthCompletion{
		Code:         completionCode,
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
		NextURL:      sess.NextURL,
		ExpiresAt:    now.Add(time.Minute),
	}); err != nil {
		slog.WarnContext(ctx, "oauth completion insert failed", "component", "auth", "installation_id", installID, "error", err)
		return "/login?error=oauth_failed&reason=completion_failed"
	}

	values := url.Values{}
	values.Set("code", completionCode)
	completeURL := strings.TrimRight(h.deps.HostBaseURL, "/") + h.deps.FrontendCompletePath + "?" + values.Encode()
	return completeURL
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
	completion, err := h.Complete(r.Context(), req.Code)
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

// Complete redeems a one-time completion code for the token pair the
// callback stored; the code is consumed on success. v1 POST
// /auth/oauth/complete and v2 completeOAuthLogin both call it.
func (h *OAuthHandler) Complete(ctx context.Context, code string) (OAuthCompletion, error) {
	if h.deps.CompletionStore == nil {
		return OAuthCompletion{}, ErrOAuthCompletionUnavailable
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return OAuthCompletion{}, ErrOAuthCodeRequired
	}
	completion, err := h.deps.CompletionStore.GetAndDeleteCompletion(ctx, code)
	if err != nil {
		return OAuthCompletion{}, ErrOAuthCompletionInvalid
	}
	return completion, nil
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

func normalizeOAuthNext(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

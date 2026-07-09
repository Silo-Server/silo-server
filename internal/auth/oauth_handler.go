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

	next := normalizeOAuthNext(r.URL.Query().Get("next"))

	client, _, err := h.deps.ResolveClient(r.Context(), installID)
	if err != nil {
		http.Error(w, "auth plugin unavailable", http.StatusBadGateway)
		return
	}

	nonce, err := randomHex(16)
	if err != nil {
		http.Error(w, "rand failure", http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC()
	state := SignState(h.deps.StateSecret, StatePayload{
		Nonce:     nonce,
		InstallID: strconv.Itoa(installID),
		ExpiresAt: now.Add(h.deps.StateTTL),
	})
	redirectURI := strings.TrimRight(h.deps.HostBaseURL, "/") + "/api/v1/auth/oauth/" + strconv.Itoa(installID) + "/callback"

	resp, err := client.InitAuthorize(r.Context(), &pluginv1.InitAuthorizeRequest{
		RedirectUri: redirectURI,
		State:       state,
		// Linking is wired in a follow-up — see TODO below.
	})
	if err != nil {
		slog.WarnContext(r.Context(), "oauth init_authorize failed", "component", "auth", "installation_id", installID, "error", err)
		http.Error(w, "plugin init_authorize failed", http.StatusBadGateway)
		return
	}
	if resp.GetAuthorizeUrl() == "" {
		http.Error(w, "plugin returned empty authorize_url", http.StatusBadGateway)
		return
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
	if err := h.deps.Store.Insert(r.Context(), sess); err != nil {
		slog.WarnContext(r.Context(), "oauth session insert failed", "component", "auth", "installation_id", installID, "error", err)
		http.Error(w, "store insert failed", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, resp.GetAuthorizeUrl(), http.StatusFound)
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

	payload, err := VerifyState(h.deps.StateSecret, state)
	if err != nil {
		http.Redirect(w, r, "/login?error=oauth_failed&reason=state_invalid", http.StatusFound)
		return
	}
	if payload.InstallID != strconv.Itoa(installID) {
		http.Redirect(w, r, "/login?error=oauth_failed&reason=install_mismatch", http.StatusFound)
		return
	}

	sess, err := h.deps.Store.GetAndDelete(r.Context(), state)
	if err != nil {
		http.Redirect(w, r, "/login?error=oauth_failed&reason=session_expired", http.StatusFound)
		return
	}

	client, capabilityID, err := h.deps.ResolveClient(r.Context(), installID)
	if err != nil {
		http.Redirect(w, r, "/login?error=oauth_failed&reason=plugin_unavailable", http.StatusFound)
		return
	}

	var ps map[string]any
	_ = json.Unmarshal(sess.ProviderState, &ps)
	psStruct, _ := structpb.NewStruct(ps)

	resp, err := client.ExchangeCode(r.Context(), &pluginv1.ExchangeCodeRequest{
		Code:          code,
		State:         state,
		RedirectUri:   sess.RedirectURI,
		ProviderState: psStruct,
	})
	if err != nil {
		slog.WarnContext(r.Context(), "oauth exchange_code failed", "component", "auth", "installation_id", installID, "error", err)
		http.Redirect(w, r, "/login?error=oauth_failed&reason=exchange_failed", http.StatusFound)
		return
	}
	if resp.GetExternalSubject() == "" {
		http.Redirect(w, r, "/login?error=oauth_failed&reason=empty_subject", http.StatusFound)
		return
	}

	linkingUserID := 0
	if sess.LinkingUserID != "" {
		if uid, err := strconv.Atoi(sess.LinkingUserID); err == nil {
			linkingUserID = uid
		}
	}

	pair, _, err := h.deps.LoginCompleter.CompleteOAuthLogin(r.Context(), OAuthLoginInput{
		InstallationID: installID,
		CapabilityID:   capabilityID,
		Response:       resp,
		LinkingUserID:  linkingUserID,
		DeviceName:     r.UserAgent(),
		IP:             clientIP(r),
	})
	if err != nil {
		slog.WarnContext(r.Context(), "oauth login completion failed", "component", "auth", "installation_id", installID, "error", err)
		http.Redirect(w, r, "/login?error=oauth_failed&reason=login_failed", http.StatusFound)
		return
	}

	if h.deps.CompletionStore == nil {
		slog.WarnContext(r.Context(), "oauth completion store is unavailable", "component", "auth", "installation_id", installID)
		http.Redirect(w, r, "/login?error=oauth_failed&reason=completion_unavailable", http.StatusFound)
		return
	}
	completionCode, err := randomHex(32)
	if err != nil {
		slog.WarnContext(r.Context(), "oauth completion code generation failed", "component", "auth", "installation_id", installID, "error", err)
		http.Redirect(w, r, "/login?error=oauth_failed&reason=completion_failed", http.StatusFound)
		return
	}
	now := time.Now().UTC()
	if err := h.deps.CompletionStore.InsertCompletion(r.Context(), OAuthCompletion{
		Code:         completionCode,
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
		NextURL:      sess.NextURL,
		ExpiresAt:    now.Add(time.Minute),
	}); err != nil {
		slog.WarnContext(r.Context(), "oauth completion insert failed", "component", "auth", "installation_id", installID, "error", err)
		http.Redirect(w, r, "/login?error=oauth_failed&reason=completion_failed", http.StatusFound)
		return
	}

	values := url.Values{}
	values.Set("code", completionCode)
	completeURL := strings.TrimRight(h.deps.HostBaseURL, "/") + h.deps.FrontendCompletePath + "?" + values.Encode()
	http.Redirect(w, r, completeURL, http.StatusFound)
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
	code := strings.TrimSpace(req.Code)
	if code == "" {
		http.Error(w, "code required", http.StatusBadRequest)
		return
	}
	completion, err := h.deps.CompletionStore.GetAndDeleteCompletion(r.Context(), code)
	if err != nil {
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

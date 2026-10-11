// Package chatgpt connects a self-hosted Silo server to ChatGPT plan usage.
package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const CredentialsKey = "ai.chatgpt.credentials"
const issuer = "https://auth.openai.com"
const resource = "https://api.openai.com/v1"
const planScope = "chatgpt.tokens.use.direct"
const authorizationScope = "openid profile email offline_access resource.invoke " + planScope
const browserCallbackURI = "http://127.0.0.1:51121/auth/callback"

const (
	loginIdle                   = "idle"
	loginPending                = "pending"
	loginConnected              = "connected"
	loginFailed                 = "failed"
	loginExpired                = "expired"
	dynamicClientID             = "dynamic_agent_client"
	oauthClientID               = "client_id"
	oauthCode                   = "code"
	oauthAuthorizationCode      = "authorization_code"
	oauthState                  = "state"
	oauthResource               = "resource"
	oauthRefreshToken           = "refresh_token"
	oauthInvalidGrant           = "invalid_grant"
	oauthTemporarilyUnavailable = "temporarily_unavailable"
	visibilityListed            = "list"
	jwkKeyTypeRSA               = "RSA"
)

var ErrSignInRequired = errors.New("sign in with ChatGPT again")
var errInvalidAuthorizationGrant = errors.New("ChatGPT authorization code is invalid")
var ErrInvalidLogin = errors.New("ChatGPT sign-in attempt is invalid or expired")
var ErrInvalidCallbackURL = errors.New("paste the full ChatGPT callback URL from the browser address bar")
var ErrIdentity = errors.New("ChatGPT identity or plan permission could not be verified")
var ErrProvider = errors.New("ChatGPT is temporarily unavailable")

// Store must encrypt CredentialsKey and serialize its updates across nodes,
// without holding the general settings lock during provider calls.
type Store interface {
	Get(context.Context, string) (string, error)
	UpdateValueAtomic(context.Context, string, func(string) (string, error)) error
}

type account struct {
	ClientID       string    `json:"client_id"`
	Subject        string    `json:"subject"`
	Email          string    `json:"email"`
	AccessToken    string    `json:"access_token,omitempty"`
	RefreshToken   string    `json:"refresh_token,omitempty"`
	IDToken        string    `json:"id_token,omitempty"`
	Scopes         string    `json:"scopes,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
	NeedsLogin     bool      `json:"needs_login,omitzero"`
	PlanIneligible bool      `json:"plan_ineligible,omitzero"`
	UsageLimited   bool      `json:"usage_limited,omitzero"`
	UsagePauseID   string    `json:"usage_pause_id,omitempty"`
	DisconnectID   string    `json:"disconnect_id,omitempty"`
}

type pendingLogin struct {
	State       string    `json:"state"`
	Nonce       string    `json:"nonce"`
	Verifier    string    `json:"verifier"`
	RedirectURI string    `json:"redirect_uri"`
	ClientID    string    `json:"client_id"`
	Subject     string    `json:"subject"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type credentials struct {
	HostID               string        `json:"host_id"`
	Active               string        `json:"active"`
	HasConnected         bool          `json:"has_connected,omitzero"`
	Accounts             []account     `json:"accounts"`
	Pending              *pendingLogin `json:"pending,omitempty"`
	LoginResult          string        `json:"login_result"`
	LoginID              string        `json:"login_id"`
	LoginExpiresAt       time.Time     `json:"login_expires_at"`
	LoginFirstConnection bool          `json:"login_first_connection,omitzero"`
}

type AccountStatus struct {
	ClientID       string `json:"client_id"`
	Email          string `json:"email"`
	Connected      bool   `json:"connected"`
	NeedsLogin     bool   `json:"needs_login"`
	PlanIneligible bool   `json:"plan_ineligible,omitzero"`
	UsageLimited   bool   `json:"usage_limited,omitzero"`
}

type Status struct {
	ActiveClientID       string          `json:"active_client_id"`
	Accounts             []AccountStatus `json:"accounts"`
	LoginPending         bool            `json:"login_pending"`
	LoginResult          string          `json:"login_result" enum:"idle,pending,connected,failed,expired"`
	AttemptID            string          `json:"attempt_id"`
	LoginFirstConnection bool            `json:"login_first_connection,omitzero"`
}

type Login struct {
	AttemptID        string    `json:"attempt_id"`
	ExpiresAt        time.Time `json:"expires_at"`
	AuthorizationURL string    `json:"authorization_url"`
	CallbackURI      string    `json:"callback_uri"`
}

type Callback struct {
	State, Code, ClientID, Error string
}

type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Service reads credentials on demand so every node observes login, account
// switching, revocation, and rotating refresh tokens without a config reload.
type Service struct {
	store Store
	http  *http.Client
	// OnSettingUpdated notifies config consumers after automatic model changes commit.
	OnSettingUpdated func(context.Context, string, string)
	// Endpoints are fixed in production. Package tests replace them with local servers.
	authURL, tokenURL, discoveryURL, modelsURL string
}

func NewService(store Store) *Service {
	return &Service{store: store, http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		authURL: issuer + "/api/accounts/authorize", tokenURL: issuer + "/api/accounts/oauth/token",
		discoveryURL: issuer + "/.well-known/openid-configuration", modelsURL: resource + "/models"}
}

func decode(raw string) (credentials, error) {
	var c credentials
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return c, errors.New("ChatGPT credentials could not be read")
		}
	}
	// Before activation history was stored explicitly, only a successfully
	// activated registration could become Active. Preserve that history on the
	// next mutation, including disconnect and a failed reconnect.
	c.HasConnected = c.HasConnected || c.Active != ""
	return c, nil
}

func (s *Service) mutate(ctx context.Context, fn func(*credentials) error) error {
	return s.store.UpdateValueAtomic(ctx, CredentialsKey, func(raw string) (string, error) {
		c, err := decode(raw)
		if err != nil {
			return "", err
		}
		if err = fn(&c); err != nil {
			return "", err
		}
		data, err := json.Marshal(c)
		if err != nil {
			return "", err
		}
		return string(data), nil
	})
}

func status(c credentials) Status {
	out := Status{
		ActiveClientID: c.Active, Accounts: []AccountStatus{},
		LoginPending:         c.LoginResult == loginPending && time.Now().Before(c.LoginExpiresAt),
		LoginFirstConnection: c.LoginResult == loginConnected && c.LoginFirstConnection,
	}
	out.LoginResult = c.LoginResult
	out.AttemptID = c.LoginID
	if out.LoginResult == "" {
		out.LoginResult = loginIdle
	}
	if c.LoginResult == loginPending && !out.LoginPending {
		out.LoginResult = loginExpired
	}
	for _, a := range c.Accounts {
		out.Accounts = append(out.Accounts, AccountStatus{ClientID: a.ClientID, Email: a.Email,
			Connected: a.RefreshToken != "" && !a.NeedsLogin && !a.PlanIneligible && !a.UsageLimited, NeedsLogin: a.NeedsLogin, PlanIneligible: a.PlanIneligible, UsageLimited: a.UsageLimited})
	}
	return out
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	raw, err := s.store.Get(ctx, CredentialsKey)
	if err != nil {
		return Status{}, err
	}
	c, err := decode(raw)
	return status(c), err
}

func randomValue() string {
	var data [32]byte
	_, _ = rand.Read(data[:])
	return base64.RawURLEncoding.EncodeToString(data[:])
}

func hash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (s *Service) StartLogin(ctx context.Context, clientID string) (Login, error) {
	login := Login{ExpiresAt: time.Now().Add(10 * time.Minute), CallbackURI: browserCallbackURI}
	err := s.mutate(ctx, func(c *credentials) error {
		if c.HostID == "" {
			c.HostID = "urn:uuid:" + uuid.NewString()
		}
		p := &pendingLogin{State: randomValue(), Nonce: randomValue(), Verifier: randomValue(), ExpiresAt: login.ExpiresAt, RedirectURI: browserCallbackURI}
		if clientID != "" {
			i := slices.IndexFunc(c.Accounts, func(a account) bool { return a.ClientID == clientID })
			if i < 0 {
				return ErrIdentity
			}
			if c.Accounts[i].PlanIneligible {
				return ErrPlanIneligible
			}
			p.ClientID, p.Subject = c.Accounts[i].ClientID, c.Accounts[i].Subject
		}
		c.Pending = p
		c.LoginResult = loginPending
		c.LoginFirstConnection = false
		c.LoginID = p.State
		c.LoginExpiresAt = p.ExpiresAt
		login.AttemptID = p.State
		login.AuthorizationURL = s.authorizationURL(c, p)
		return nil
	})
	return login, err
}

func checkPending(c *credentials) (*pendingLogin, error) {
	p := c.Pending
	if p == nil || !time.Now().Before(p.ExpiresAt) {
		return nil, ErrInvalidLogin
	}
	return p, nil
}

func (s *Service) authorizationURL(c *credentials, p *pendingLogin) string {
	clientID := p.ClientID
	if clientID == "" {
		clientID = dynamicClientID
	}
	q := url.Values{oauthClientID: {clientID}, "ext_agent_host_id": {c.HostID}, "response_type": {oauthCode},
		"redirect_uri": {p.RedirectURI}, "scope": {authorizationScope},
		oauthResource: {resource}, oauthState: {p.State}, "nonce": {p.Nonce}, "code_challenge_method": {"S256"}, "code_challenge": {hash(p.Verifier)}}
	if p.ClientID == "" {
		q.Set("agent_name_hint", "Silo")
	} else {
		for _, a := range c.Accounts {
			if a.ClientID == p.ClientID {
				if a.NeedsLogin && a.Subject != "" && !hasPlanScope(a.Scopes) {
					q.Set("prompt", "consent")
				}
				if a.IDToken != "" {
					q.Set("id_token_hint", a.IDToken)
				}
				if a.Email != "" {
					q.Set("login_hint", a.Email)
				}
				break
			}
		}
	}
	return s.authURL + "?" + q.Encode()
}

// parseCallbackURL only parses the pasted address; it never requests that URL.
func parseCallbackURL(raw, redirectURI string) (Callback, error) {
	if len(raw) > 32768 {
		return Callback{}, ErrInvalidCallbackURL
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return Callback{}, ErrInvalidCallbackURL
	}
	q, err := url.ParseQuery(u.RawQuery)
	u.RawQuery, u.ForceQuery = "", false
	if err != nil || redirectURI == "" || u.String() != redirectURI {
		return Callback{}, ErrInvalidCallbackURL
	}
	for _, values := range q {
		if len(values) != 1 {
			return Callback{}, ErrInvalidCallbackURL
		}
	}
	callback := Callback{State: q.Get(oauthState), Code: q.Get(oauthCode), ClientID: q.Get(oauthClientID), Error: q.Get("error")}
	if callback.State == "" || len(callback.State) > 256 || len(callback.Code) > 8192 || len(callback.ClientID) > 256 || len(callback.Error) > 256 ||
		(callback.Code == "" && callback.Error == "") || (callback.Code != "" && callback.Error != "") {
		return Callback{}, ErrInvalidCallbackURL
	}
	return callback, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

func (s *Service) exchange(ctx context.Context, form url.Values) (tokenResponse, error) {
	var tokens tokenResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokens, ErrProvider
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		return tokens, &transientProviderError{}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && resp.StatusCode < 600) {
		return tokens, &transientProviderError{retryAfter: tokenRetryAfter(resp.Header.Get("Retry-After"))}
	}
	if resp.StatusCode == 400 || resp.StatusCode == 401 {
		var problem struct {
			Error string `json:"error"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&problem)
		if form.Get("grant_type") == oauthRefreshToken {
			if decodeErr != nil {
				return tokens, ErrProvider
			}
			// Only documented unusable-token errors require reauthorization.
			// Status alone does not establish that renewable credentials are lost.
			switch problem.Error {
			case oauthInvalidGrant, "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused":
				return tokens, ErrSignInRequired
			case oauthTemporarilyUnavailable, "server_error":
				return tokens, &transientProviderError{retryAfter: tokenRetryAfter(resp.Header.Get("Retry-After"))}
			default:
				return tokens, ErrProvider
			}
		}
		if form.Get("grant_type") == oauthAuthorizationCode && decodeErr == nil && problem.Error == oauthInvalidGrant {
			return tokens, errors.Join(ErrSignInRequired, errInvalidAuthorizationGrant)
		}
		return tokens, ErrSignInRequired
	}
	if resp.StatusCode != http.StatusOK {
		return tokens, ErrProvider
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&tokens) != nil {
		return tokens, ErrIdentity
	}
	// RFC 6749 section 5.1: omitted scopes retain the requested grant.
	// Normalize before validating that an initial plan grant is renewable.
	if tokens.Scope == "" && form.Get("grant_type") == oauthAuthorizationCode {
		tokens.Scope = authorizationScope
	}
	if tokens.AccessToken == "" || (tokens.RefreshToken == "" && form.Get("grant_type") != oauthRefreshToken && hasPlanScope(tokens.Scope)) || tokens.ExpiresIn <= 0 || !strings.EqualFold(tokens.TokenType, "Bearer") {
		return tokens, ErrIdentity
	}
	return tokens, nil
}

func hasPlanScope(scopes string) bool { return slices.Contains(strings.Fields(scopes), planScope) }

func (s *Service) CompleteLogin(ctx context.Context, callbackURL string) error {
	_, err := s.CompleteLoginWithSettings(ctx, callbackURL)
	return err
}

func (s *Service) CompleteLoginWithSettings(ctx context.Context, callbackURL string) (*SettingsTransition, error) {
	var pending pendingLogin
	var callback Callback
	// Persist consumption before exchanging the authorization code. A node
	// failure or lost HTTP reply cannot cause a second code exchange.
	err := s.mutate(ctx, func(c *credentials) error {
		p, err := checkPending(c)
		if err != nil {
			return err
		}
		callback, err = parseCallbackURL(callbackURL, p.RedirectURI)
		if err != nil {
			return err
		}
		if p.RedirectURI == "" || subtle.ConstantTimeCompare([]byte(callback.State), []byte(p.State)) != 1 {
			return ErrInvalidLogin
		}
		pending = *p
		c.Pending = nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Consumption has committed. Finish an accepted exchange independently of
	// the browser request so a lost connection cannot discard its credentials.
	completionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	clientID := pending.ClientID
	if clientID == "" {
		clientID = callback.ClientID
	}
	var tokens tokenResponse
	var expiresAt time.Time
	var claims *identityClaims
	var completionErr error
	var rejectedIdentity bool
	if callback.Error != "" || callback.Code == "" || clientID == "" || clientID == dynamicClientID || (callback.ClientID != "" && callback.ClientID != clientID) {
		completionErr = ErrIdentity
	} else {
		tokens, completionErr = s.exchange(completionCtx, url.Values{"grant_type": {oauthAuthorizationCode}, oauthClientID: {clientID},
			oauthCode: {callback.Code}, "code_verifier": {pending.Verifier}, "redirect_uri": {pending.RedirectURI}, oauthResource: {resource}})
		if completionErr == nil {
			expiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
			claims, completionErr = s.verifyIDToken(completionCtx, tokens.IDToken, clientID, pending.Nonce)
			if completionErr == nil && pending.Subject != "" && claims.Subject != pending.Subject {
				rejectedIdentity = true
				completionErr = ErrIdentity
			}
		}
	}
	var activationModels []Model
	var activationErr error
	if completionErr == nil && hasPlanScope(tokens.Scope) {
		activationModels, activationErr = s.loginModels(completionCtx, tokens)
	}
	// A slow catalog lookup can exhaust the provider deadline after the code
	// exchange succeeded. The independent save deadline must also outlast an
	// admitted one-minute refresh and its separate database commit before this
	// login can acquire the credential lock and persist its accepted tokens.
	saveCtx, cancelSave := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancelSave()
	transition, err := s.mutateSettingsWithSnapshot(saveCtx, func(c *credentials, values map[string]string) error {
		// A newer administrator attempt supersedes this completion, including
		// a callback already exchanging credentials on another node.
		if c.LoginID != pending.State {
			return ErrInvalidLogin
		}
		c.LoginResult = loginFailed
		c.LoginFirstConnection = false
		if completionErr != nil {
			_, temporaryIdentityFailure := errors.AsType[*transientProviderError](completionErr)
			if pending.ClientID == "" && (errors.Is(completionErr, errInvalidAuthorizationGrant) || (tokens.AccessToken != "" && temporaryIdentityFailure)) &&
				!slices.ContainsFunc(c.Accounts, func(a account) bool { return a.ClientID == clientID }) {
				// The callback issued a registration, but no identity was verified.
				// Retain only its ID so a fresh authorization can reuse it.
				c.Accounts = append(c.Accounts, account{ClientID: clientID, NeedsLogin: true})
			}
			return nil //nolint:nilerr // Commit the failed outcome before returning completionErr.
		}
		a := account{ClientID: clientID, Subject: claims.Subject, Email: claims.Email,
			AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, ExpiresAt: expiresAt,
			IDToken: tokens.IDToken, Scopes: tokens.Scope, NeedsLogin: !hasPlanScope(tokens.Scope)}
		if a.NeedsLogin {
			// Preserve accepted tokens for disconnect revocation. NeedsLogin
			// blocks inference until a verified sign-in grants plan consent.
			completionErr = ErrIdentity
		}
		i := slices.IndexFunc(c.Accounts, func(existing account) bool { return existing.ClientID == clientID })
		if i >= 0 {
			if c.Accounts[i].Subject != "" && c.Accounts[i].Subject != a.Subject {
				rejectedIdentity = true
				completionErr = ErrIdentity
				return nil
			}
			if a.NeedsLogin && c.Accounts[i].RefreshToken != "" && !c.Accounts[i].NeedsLogin {
				return nil // Preserve an existing usable account after declined re-consent.
			}
			// Renewing credentials does not prove that plan usage is available.
			a.UsageLimited = c.Accounts[i].UsageLimited
			a.UsagePauseID = c.Accounts[i].UsagePauseID
			a.PlanIneligible = c.Accounts[i].PlanIneligible
			c.Accounts[i] = a
		} else {
			c.Accounts = append(c.Accounts, a)
		}
		if a.NeedsLogin {
			return nil
		}
		if activationErr == nil {
			activationErr = reconcileModel(values, activationModels)
		}
		if activationErr != nil {
			// Preserve accepted rotating credentials even when the model catalog
			// cannot be reconciled. Activation can be retried through selection.
			if c.Active == clientID {
				c.Active = ""
			}
			completionErr = activationErr
			return nil
		}
		c.Active = clientID
		c.LoginFirstConnection = !c.HasConnected
		c.HasConnected = true
		c.LoginResult = loginConnected
		return nil
	})
	if rejectedIdentity && tokens.RefreshToken != "" {
		// Revoke only the newly issued session for the verified wrong identity,
		// after the settings transaction releases its lock. A lost browser or
		// failed outcome commit must not skip the bounded cleanup attempt.
		revocationCtx, cancelRevocation := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		s.revokeSession(revocationCtx, account{ClientID: clientID, RefreshToken: tokens.RefreshToken})
		cancelRevocation()
	}
	if err != nil {
		return nil, err
	}
	if completionErr != nil {
		return nil, completionErr
	}
	return transition, nil
}

// TokenSource pins a logical request to its initial registration. Each use reads
// current credentials for that registration, including rotations on other nodes.
func (s *Service) TokenSource(ctx context.Context) (func(context.Context) (string, error), error) {
	return s.tokenSource(ctx, false)
}

func (s *Service) tokenSource(ctx context.Context, recheckUsage bool) (func(context.Context) (string, error), error) {
	raw, err := s.store.Get(ctx, CredentialsKey)
	if err != nil {
		return nil, err
	}
	c, err := decode(raw)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(c.Accounts, func(a account) bool { return a.ClientID == c.Active })
	if i >= 0 && c.Accounts[i].PlanIneligible {
		return nil, ErrPlanIneligible
	}
	if i >= 0 && c.Accounts[i].UsageLimited && !recheckUsage {
		return nil, ErrUsageLimited
	}
	if i < 0 || c.Accounts[i].NeedsLogin || c.Accounts[i].RefreshToken == "" {
		return nil, ErrSignInRequired
	}
	clientID := c.Accounts[i].ClientID
	return func(ctx context.Context) (string, error) { return s.bearerToken(ctx, clientID, recheckUsage) }, nil
}

// BearerToken reads the active registration once, then serializes its refresh.
func (s *Service) BearerToken(ctx context.Context) (string, error) {
	source, err := s.TokenSource(ctx)
	if err != nil {
		return "", err
	}
	return source(ctx)
}

func (s *Service) bearerToken(ctx context.Context, clientID string, recheckUsage bool) (string, error) {
	raw, err := s.store.Get(ctx, CredentialsKey)
	if err != nil {
		return "", err
	}
	current, err := decode(raw)
	if err != nil {
		return "", err
	}
	for _, a := range current.Accounts {
		if a.ClientID == clientID && a.PlanIneligible {
			return "", ErrPlanIneligible
		}
		if a.ClientID == clientID && a.UsageLimited && !recheckUsage {
			return "", ErrUsageLimited
		}
		if a.ClientID == clientID && !a.NeedsLogin && a.AccessToken != "" && a.RefreshToken != "" && time.Until(a.ExpiresAt) > 2*time.Minute {
			return a.AccessToken, nil
		}
	}
	var token string
	var refreshErr error
	// Once rotation starts, persist its replacement even if the request or job
	// that needed it is canceled. The independent deadline bounds lock waits,
	// the token exchange, and the commit.
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	err = s.mutate(refreshCtx, func(c *credentials) error {
		i := slices.IndexFunc(c.Accounts, func(a account) bool { return a.ClientID == clientID })
		if i >= 0 && c.Accounts[i].PlanIneligible {
			return ErrPlanIneligible
		}
		if i >= 0 && c.Accounts[i].UsageLimited && !recheckUsage {
			return ErrUsageLimited
		}
		if i < 0 || c.Accounts[i].RefreshToken == "" || c.Accounts[i].NeedsLogin {
			return ErrSignInRequired
		}
		a := &c.Accounts[i]
		if a.AccessToken != "" && time.Until(a.ExpiresAt) > 2*time.Minute {
			token = a.AccessToken
			return nil
		}
		tokens, err := s.refreshExchange(refreshCtx, url.Values{"grant_type": {oauthRefreshToken}, oauthClientID: {a.ClientID}, oauthRefreshToken: {a.RefreshToken}, oauthResource: {resource}})
		if err != nil {
			if errors.Is(err, ErrSignInRequired) {
				a.NeedsLogin = true
			}
			refreshErr = err
			return nil
		}
		if tokens.RefreshToken == "" {
			tokens.RefreshToken = a.RefreshToken
		}
		if tokens.Scope == "" {
			tokens.Scope = a.Scopes
		}
		// Persist replacement credentials even when a reduced grant needs consent.
		a.AccessToken, a.RefreshToken, a.Scopes = tokens.AccessToken, tokens.RefreshToken, tokens.Scope
		a.ExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
		if !hasPlanScope(tokens.Scope) {
			a.NeedsLogin = true
			refreshErr = ErrSignInRequired
			return nil
		}
		// Identity remains bound to the validated sign-in token. An optional
		// refresh ID token is not used for identity or the reauthorization hint.
		token = a.AccessToken
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, refreshErr
}

// RejectToken invalidates only the exact credential rejected by the provider.
// Late responses cannot erase a replacement token committed by another node.
func (s *Service) RejectToken(ctx context.Context, rejected string, needsLogin bool) error {
	if rejected == "" {
		return nil
	}
	rejectionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return s.mutate(rejectionCtx, func(c *credentials) error {
		i := slices.IndexFunc(c.Accounts, func(a account) bool { return a.AccessToken == rejected })
		if i >= 0 {
			a := &c.Accounts[i]
			a.AccessToken = ""
			a.ExpiresAt = time.Time{}
			a.NeedsLogin = a.NeedsLogin || needsLogin
		}
		return nil
	})
}

func (s *Service) SelectAccount(ctx context.Context, clientID string) error {
	_, err := s.SelectAccountWithSettings(ctx, clientID)
	return err
}

type discovery struct {
	JWKSURI            string `json:"jwks_uri"`
	RevocationEndpoint string `json:"revocation_endpoint"`
}

func (s *Service) discovery(ctx context.Context) (discovery, error) {
	var d discovery
	if err := s.getJSON(ctx, s.discoveryURL, "", &d); err != nil {
		return d, err
	}
	// Discovery is pinned to OpenAI, including every URL used for credentials.
	// Tests use their own HTTP transport to serve those same URLs.
	for _, endpoint := range []string{d.JWKSURI, d.RevocationEndpoint} {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host != "auth.openai.com" || parsed.User != nil {
			return d, ErrIdentity
		}
	}
	return d, nil
}

type identityClaims struct {
	jwt.RegisteredClaims
	Nonce string `json:"nonce"`
	Email string `json:"email"`
}

func (s *Service) verifyIDToken(ctx context.Context, raw, clientID, nonce string) (*identityClaims, error) {
	for attempt := range 3 {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(&transientProviderError{}, err)
		}
		claims, err := s.verifyIDTokenOnce(ctx, raw, clientID, nonce)
		temporary, retry := errors.AsType[*transientProviderError](err)
		if !retry || attempt == 2 {
			return claims, err
		}
		if !waitProviderRetry(ctx, attempt, temporary.retryAfter) {
			return nil, errors.Join(err, ctx.Err())
		}
	}
	return nil, ErrProvider
}

func (s *Service) verifyIDTokenOnce(ctx context.Context, raw, clientID, nonce string) (*identityClaims, error) {
	d, err := s.discovery(ctx)
	if err != nil {
		return nil, err
	}
	var keys struct {
		Keys []struct{ Kty, Kid, N, E string } `json:"keys"`
	}
	if err := s.getJSON(ctx, d.JWKSURI, "", &keys); err != nil {
		return nil, err
	}
	claims := new(identityClaims)
	_, err = jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		kid, _ := token.Header["kid"].(string)
		for _, key := range keys.Keys {
			if key.Kty != jwkKeyTypeRSA || key.Kid != kid || kid == "" {
				continue
			}
			n, nErr := base64.RawURLEncoding.DecodeString(key.N)
			e, eErr := base64.RawURLEncoding.DecodeString(key.E)
			if nErr != nil || eErr != nil || len(e) > 4 || len(n) < 256 {
				return nil, ErrIdentity
			}
			return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
		}
		return nil, ErrIdentity
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(issuer), jwt.WithAudience(clientID), jwt.WithExpirationRequired())
	if err != nil || claims.Subject == "" || (nonce != "" && subtle.ConstantTimeCompare([]byte(nonce), []byte(claims.Nonce)) != 1) {
		return nil, ErrIdentity
	}
	return claims, nil
}

func (s *Service) getJSON(ctx context.Context, endpoint, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ErrProvider
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return &transientProviderError{}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && resp.StatusCode < 600) {
		return &transientProviderError{retryAfter: tokenRetryAfter(resp.Header.Get("Retry-After"))}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrSignInRequired
	}
	if resp.StatusCode != http.StatusOK {
		return ErrProvider
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out); err != nil {
		if _, networkFailure := errors.AsType[net.Error](err); errors.Is(err, io.ErrUnexpectedEOF) || networkFailure {
			return &transientProviderError{}
		}
		return ErrProvider
	}
	return nil
}

func (s *Service) Models(ctx context.Context, clientID string) ([]Model, error) {
	if clientID != "" {
		return s.modelsForSource(ctx, func(ctx context.Context) (string, error) {
			return s.bearerToken(ctx, clientID, true)
		})
	}
	// Catalog reads do not consume inference usage or clear a usage pause.
	source, err := s.tokenSource(ctx, true)
	if err != nil {
		return nil, err
	}
	return s.modelsForSource(ctx, source)
}

// Disconnect disables the account before attempting provider revocation. The
// refresh token remains available for bounded retries, then cleanup removes it
// unless a newer disconnect or successful reconnect superseded this operation.
// The boolean records whether OpenAI confirmed revocation.
func (s *Service) Disconnect(ctx context.Context, clientID string) (bool, error) {
	disconnectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	// Leave time to commit local cleanup after a provider timeout.
	deadline, _ := disconnectCtx.Deadline()
	revocationCtx, cancelRevocation := context.WithDeadline(disconnectCtx, deadline.Add(-5*time.Second))
	defer cancelRevocation()
	disconnectID := randomValue()
	var departing account
	err := s.mutate(disconnectCtx, func(c *credentials) error {
		i := slices.IndexFunc(c.Accounts, func(a account) bool { return a.ClientID == clientID })
		if i < 0 {
			return ErrIdentity
		}
		a := &c.Accounts[i]
		departing = *a
		a.AccessToken, a.IDToken, a.Scopes = "", "", ""
		a.ExpiresAt = time.Time{}
		a.NeedsLogin = true
		a.DisconnectID = disconnectID
		if c.Active == clientID {
			c.Active = ""
		}
		c.Pending = nil
		c.LoginID = ""
		c.LoginResult = loginIdle
		c.LoginFirstConnection = false
		return nil
	})
	if err != nil {
		return false, err
	}
	revoked := false
	if departing.RefreshToken != "" {
		revoked = s.revokeSession(revocationCtx, departing)
	}
	err = s.mutate(disconnectCtx, func(c *credentials) error {
		i := slices.IndexFunc(c.Accounts, func(a account) bool { return a.ClientID == clientID })
		if i >= 0 && c.Accounts[i].DisconnectID == disconnectID {
			c.Accounts[i].RefreshToken = ""
			c.Accounts[i].DisconnectID = ""
		}
		return nil
	})
	return revoked, err
}

func (s *Service) revokeSession(ctx context.Context, a account) bool {
	form := url.Values{"token": {a.RefreshToken}, "token_type_hint": {oauthRefreshToken}, oauthClientID: {a.ClientID}}.Encode()
	var endpoint string
	var retryAfter time.Duration
	for attempt := range 3 {
		if attempt > 0 && !waitProviderRetry(ctx, attempt-1, retryAfter) {
			return false
		}
		retryAfter = 0
		if ctx.Err() != nil {
			return false
		}
		if endpoint == "" {
			d, err := s.discovery(ctx)
			if err != nil {
				if errors.Is(err, ErrIdentity) || errors.Is(err, ErrSignInRequired) {
					return false
				}
				if temporary, ok := errors.AsType[*transientProviderError](err); ok {
					retryAfter = temporary.retryAfter
				}
				continue
			}
			endpoint = d.RevocationEndpoint
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form))
		if err != nil {
			return false
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := s.http.Do(req)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return true
		}
		if resp.StatusCode != http.StatusTooManyRequests && (resp.StatusCode < http.StatusInternalServerError || resp.StatusCode >= 600) {
			return false
		}
		retryAfter = tokenRetryAfter(resp.Header.Get("Retry-After"))
	}
	return false
}

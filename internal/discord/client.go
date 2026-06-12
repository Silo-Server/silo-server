// Package discord is a minimal Discord REST client covering exactly what the
// notification channel needs: the OAuth2 code exchange used for account
// linking, identity lookups, and bot DM delivery. No Gateway connection is
// held; everything is short-lived REST against a fixed trusted host.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultAPIBase = "https://discord.com/api/v10"
	// AuthorizeURL is the user-facing OAuth2 consent page.
	AuthorizeURL = "https://discord.com/oauth2/authorize"

	requestTimeout = 10 * time.Second
	// Discord asks bot user agents to identify themselves in this format.
	userAgent = "DiscordBot (https://github.com/Silo-Server/silo-server, 1.0)"

	// errorBodyLimit bounds how much of an error response is read for
	// diagnostics.
	errorBodyLimit = 4 << 10
)

// Sentinel errors for the failure modes callers branch on.
var (
	// ErrDMBlocked is Discord error 50007: the bot cannot DM this user. The
	// user does not share a guild with the bot or has server DMs disabled.
	ErrDMBlocked = errors.New("discord: cannot send messages to this user")
	// ErrUnauthorized means the bot token or OAuth credentials were rejected.
	ErrUnauthorized = errors.New("discord: unauthorized")
	// ErrRateLimited means Discord returned 429; retry later.
	ErrRateLimited = errors.New("discord: rate limited")
)

const dmBlockedCode = 50007

// User is the subset of a Discord user object the integration stores.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// Client makes Discord REST calls. Tokens are passed per call so callers can
// read live settings; the client itself holds no credentials.
type Client struct {
	httpClient *http.Client
	apiBase    string
	// limiter paces outbound calls well under Discord's global rate limits
	// (~50 req/s global, ~5 DMs/s); notification volume is far below this,
	// but digest-hour bursts across many accounts need smoothing.
	limiter *rate.Limiter
}

// NewClient creates a Client.
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: requestTimeout},
		apiBase:    defaultAPIBase,
		limiter:    rate.NewLimiter(rate.Every(250*time.Millisecond), 4),
	}
}

// ExchangeCode performs the OAuth2 authorization-code exchange and returns
// the user access token. Only the identify scope is requested at authorize
// time, so the token can do nothing beyond reading the user's own identity.
func (c *Client) ExchangeCode(ctx context.Context, clientID, clientSecret, code, redirectURI string) (string, error) {
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiBase+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)

	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.do(req, &token); err != nil {
		return "", fmt.Errorf("exchange oauth code: %w", err)
	}
	if token.AccessToken == "" {
		return "", errors.New("discord: token response missing access_token")
	}
	return token.AccessToken, nil
}

// GetUser returns the identity behind a user access token (GET /users/@me).
func (c *Client) GetUser(ctx context.Context, accessToken string) (User, error) {
	return c.getMe(ctx, "Bearer "+accessToken)
}

// GetBotUser returns the bot's own identity, verifying the bot token. Used by
// the admin "test" endpoint.
func (c *Client) GetBotUser(ctx context.Context, botToken string) (User, error) {
	return c.getMe(ctx, "Bot "+botToken)
}

func (c *Client) getMe(ctx context.Context, authorization string) (User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+"/users/@me", nil)
	if err != nil {
		return User{}, err
	}
	req.Header.Set("Authorization", authorization)

	var user User
	if err := c.do(req, &user); err != nil {
		return User{}, fmt.Errorf("get current user: %w", err)
	}
	if user.ID == "" {
		return User{}, errors.New("discord: user response missing id")
	}
	return user, nil
}

// OpenDMChannel opens (or returns the existing) DM channel with the user.
// Idempotent: Discord returns the same channel for repeated calls.
func (c *Client) OpenDMChannel(ctx context.Context, botToken, recipientDiscordUserID string) (string, error) {
	body, err := json.Marshal(map[string]string{"recipient_id": recipientDiscordUserID})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiBase+"/users/@me/channels", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+botToken)

	var channel struct {
		ID string `json:"id"`
	}
	if err := c.do(req, &channel); err != nil {
		return "", fmt.Errorf("open dm channel: %w", err)
	}
	if channel.ID == "" {
		return "", errors.New("discord: channel response missing id")
	}
	return channel.ID, nil
}

// SendDM posts a message payload (Discord message JSON, e.g. an embeds body)
// to a DM channel.
func (c *Client) SendDM(ctx context.Context, botToken, channelID string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiBase+"/channels/"+url.PathEscape(channelID)+"/messages", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+botToken)

	if err := c.do(req, nil); err != nil {
		return fmt.Errorf("send dm: %w", err)
	}
	return nil
}

// do executes the request under the rate limiter and decodes a 2xx JSON
// response into out (when non-nil). Non-2xx responses map to sentinel errors
// with the Discord error message attached.
func (c *Client) do(req *http.Request, out any) error {
	if err := c.limiter.Wait(req.Context()); err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, errorBodyLimit))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode discord response: %w", err)
	}
	return nil
}

// apiError maps a non-2xx response to a sentinel error, preserving Discord's
// message for logs and UI surfacing.
func apiError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
	var payload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &payload)

	switch {
	case payload.Code == dmBlockedCode:
		return ErrDMBlocked
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%w: %s", ErrUnauthorized, payload.Message)
	case resp.StatusCode == http.StatusTooManyRequests:
		return ErrRateLimited
	}
	message := payload.Message
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	return fmt.Errorf("discord: HTTP %d: %s", resp.StatusCode, message)
}

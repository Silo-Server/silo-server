package nodeenrollment

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
)

var (
	ErrInvalidTarget  = errors.New("enrollment requires a valid HTTP or HTTPS node URL")
	ErrOtherCustomer  = errors.New("node is bound to a different customer")
	ErrRejected       = errors.New("node rejected enrollment; check the provider token and worker connection settings")
	ErrStartupPending = errors.New("node binding may be saved but startup is not ready; retry enrollment to finish registration")
)

// Input contains the one-time token and optional worker-reachable endpoints.
// It is never persisted by the customer's registry.
type Input struct {
	Token       string
	DatabaseURL string
	RedisURL    string
}

// Client supplies credentials from server bootstrap, never from the browser.
// HTTP supports trusted internal networks; HTTPS retains certificate verification.
type Client struct {
	Bootstrap Binding
	JWTSecret string
	// CurrentJWTSecret overrides JWTSecret, including when it returns an empty
	// key. Core clients use the live config watcher for every receipt attempt.
	CurrentJWTSecret func() string
	HTTPClient       *http.Client
	ReadyTimeout     time.Duration
}

func (c *Client) target(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(raw) > 8192 || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidTarget
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", ErrInvalidTarget
	}
	return strings.TrimRight(raw, "/"), nil
}
func (c *Client) transport() *http.Client {
	client := http.Client{Timeout: 15 * time.Second}
	if c.HTTPClient != nil {
		client = *c.HTTPClient
	}
	if client.Timeout <= 0 || client.Timeout > 15*time.Second {
		client.Timeout = 15 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}
func validReceipt(receipt Receipt, secret string) bool {
	expected := Identity(secret)
	return subtle.ConstantTimeCompare([]byte(receipt.BindingID), []byte(expected)) == 1 && receipt.EncodingCapacity.MaxJobs >= 0 && receipt.EncodingCapacity.MaxJobs <= math.MaxInt32 && receipt.EncodingCapacity.ActiveJobs >= 0
}
func (c *Client) receipt(ctx context.Context, client *http.Client, base string) (Receipt, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+ReceiptPath, nil)
	if err != nil {
		return Receipt{}, false, ErrInvalidTarget
	}
	key := c.JWTSecret
	if c.CurrentJWTSecret != nil {
		key = c.CurrentJWTSecret()
	}
	if err := nodeauth.Sign(request, base, key); err != nil {
		return Receipt{}, false, ErrRejected
	}
	response, err := client.Do(request)
	if err != nil {
		return Receipt{}, false, ErrStartupPending
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusNotFound:
		return Receipt{}, false, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return Receipt{}, false, ErrOtherCustomer
	case http.StatusOK:
		var receipt Receipt
		decoder := json.NewDecoder(io.LimitReader(response.Body, MaxBodyBytes))
		if decoder.Decode(&receipt) != nil || !validReceipt(receipt, c.Bootstrap.SecretKey) {
			return Receipt{}, false, ErrOtherCustomer
		}
		return receipt, true, nil
	default:
		return Receipt{}, false, ErrRejected
	}
}

// Enroll recovers a lost first reply or failed registry write by verifying the
// already-bound runtime before attempting the one-time handoff again.
func (c *Client) Enroll(ctx context.Context, rawURL, name string, input Input) (Receipt, error) {
	base, err := c.target(rawURL)
	if err != nil {
		return Receipt{}, err
	}
	client := c.transport()
	receipt, bound, err := c.receipt(ctx, client, base)
	if err == nil && bound {
		return receipt, nil
	}
	if errors.Is(err, ErrOtherCustomer) || errors.Is(err, ErrRejected) {
		return Receipt{}, err
	}
	if err != nil {
		return Receipt{}, err
	}
	b := c.Bootstrap
	b.NodeURL = rawURL
	b.NodeName = name
	if input.DatabaseURL != "" {
		b.DatabaseURL = input.DatabaseURL
	}
	if input.RedisURL != "" {
		b.RedisURL = input.RedisURL
	}
	if err := b.Validate(); err != nil {
		return Receipt{}, fmt.Errorf("%w: core bootstrap or worker connection settings are invalid", ErrRejected)
	}
	if len(input.Token) < 32 || len(input.Token) > 512 {
		return Receipt{}, ErrRejected
	}
	data, err := json.Marshal(b)
	if err != nil {
		return Receipt{}, ErrRejected
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+EnrollmentPath, bytes.NewReader(data))
	if err != nil {
		return Receipt{}, ErrInvalidTarget
	}
	request.Header.Set("Authorization", "Bearer "+input.Token)
	request.Header.Set("Content-Type", "application/json")
	response, postErr := client.Do(request)
	if postErr == nil {
		_ = response.Body.Close()
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnprocessableEntity || (response.StatusCode >= 300 && response.StatusCode < 400) {
			return Receipt{}, ErrRejected
		}
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusConflict && response.StatusCode < 500 {
			return Receipt{}, ErrRejected
		}
	}
	// Both an acknowledged save and a lost reply require authenticated proof of
	// the actual runtime before registering it. No receipt from another tenant
	// can satisfy this check.
	timeout := c.ReadyTimeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		receipt, bound, err = c.receipt(readyCtx, client, base)
		if err == nil && bound {
			return receipt, nil
		}
		if errors.Is(err, ErrOtherCustomer) {
			return Receipt{}, err
		}
		select {
		case <-readyCtx.Done():
			return Receipt{}, ErrStartupPending
		case <-ticker.C:
		}
	}
}

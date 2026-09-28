package notifications

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	relayRegisterPath = "/v1/deployments/register"
	relayRotatePath   = "/v1/deployments/rotate"
	relayRenewPath    = "/v1/deployments/renew"
	relayRenewBefore  = 7 * 24 * time.Hour
	relayRenewJitter  = 24 * time.Hour
)

type RelayHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type RelayCredentialResult struct {
	Credential PushRelayCredential
	RequestID  string
	APNsTopics []string
}

type relayCredentialResponse struct {
	RequestID    string   `json:"request_id"`
	DeploymentID string   `json:"deployment_id"`
	APIKey       string   `json:"api_key"`
	KeyPrefix    string   `json:"key_prefix"`
	APNsTopics   []string `json:"apns_topics"`
	ExpiresAt    string   `json:"expires_at"`
}

type relayNestedError struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

type RelayCredentialError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e RelayCredentialError) Error() string { return e.Code }

// NormalizePushRelayURL accepts the official production origin or one exact
// operator-configured development/staging origin. Capabilities and device
// tokens must never be sent to an arbitrary URL supplied through the admin UI.
func NormalizePushRelayURL(raw, developmentOrigin string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	if value == "" {
		value = DefaultPushRelayURL
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return "", errors.New("relay_url must be an HTTPS origin")
	}
	allowed := map[string]bool{DefaultPushRelayURL: true}
	if override := strings.TrimRight(strings.TrimSpace(developmentOrigin), "/"); override != "" {
		allowed[override] = true
	}
	if !allowed[value] {
		return "", errors.New("relay_url is not an allowed Silo relay origin")
	}
	return value, nil
}

func IsLegacyPushRelayKey(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), "rk_")
}

const (
	// relayCodeDeploymentDisabled is the relay's 401 code for a deployment its
	// operator (or the deployment itself) disabled. It is the only relay
	// rejection that parks the credential for an administrator; every other
	// rejection is healed by registering a fresh deployment.
	relayCodeDeploymentDisabled = "deployment_disabled"
	// relayCodeTokenExpired is the relay's 401 code for a capability that is
	// past its expiry and may still be renewable.
	relayCodeTokenExpired = "token_expired"
)

// Parked reports whether the stored state waits for an explicit
// administrator registration. Older servers also set the marker on a stored
// capability the relay rejected; such a credential still has its key and is
// recovered automatically rather than treated as parked.
func (c PushRelayCredential) Parked() bool {
	return c.ReregistrationRequired && c.APIKey == ""
}

func LoadPushRelayCredential(ctx context.Context, settings *Settings) PushRelayCredential {
	return PushRelayCredential{
		RelayURL:               settings.PushRelayURL(ctx),
		DeploymentID:           settings.PushRelayDeploymentID(ctx),
		APIKey:                 settings.PushRelayAPIKey(ctx),
		ExpiresAt:              settings.PushRelayExpiresAt(ctx),
		KeyPrefix:              settings.PushRelayKeyPrefix(ctx),
		ReregistrationRequired: settings.PushRelayReregistrationRequired(ctx),
	}
}

func RelayCredentialNeedsRenewal(now, expiresAt time.Time, deploymentID string) bool {
	if expiresAt.IsZero() {
		return false
	}
	digest := sha256.Sum256([]byte(deploymentID))
	jitterSeconds := int64(digest[0])<<8 | int64(digest[1])
	jitter := time.Duration(jitterSeconds) * relayRenewJitter / 65535
	return !now.Before(expiresAt.Add(-(relayRenewBefore + jitter)))
}

func RelayRotationIdempotencyKey(deploymentID, capability string) string {
	digest := sha256.Sum256([]byte("silo-relay-rotation\x00" + deploymentID + "\x00" + capability))
	return "silo-rotate-" + hex.EncodeToString(digest[:])
}

func RequestRelayCredential(ctx context.Context, client RelayHTTPDoer, relayURL, path, capability, idempotencyKey string) (RelayCredentialResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+path, bytes.NewReader([]byte("{}")))
	if err != nil {
		return RelayCredentialResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Silo-Server/PushRelayCredential")
	if capability != "" {
		req.Header.Set("Authorization", "Bearer "+capability)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return RelayCredentialResult{}, RelayCredentialError{Status: http.StatusBadGateway, Code: "relay_unreachable", Message: "Push relay could not be reached"}
	}
	defer func() { _ = resp.Body.Close() }()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if readErr != nil {
		return RelayCredentialResult{}, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var parsed relayNestedError
		_ = json.Unmarshal(data, &parsed)
		code := strings.TrimSpace(parsed.Error.Code)
		if code == "" {
			code = fmt.Sprintf("relay_http_%d", resp.StatusCode)
		}
		message := strings.TrimSpace(parsed.Error.Message)
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return RelayCredentialResult{}, RelayCredentialError{
			Status:     resp.StatusCode,
			Code:       code,
			Message:    message,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}
	var parsed relayCredentialResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return RelayCredentialResult{}, RelayCredentialError{Status: http.StatusBadGateway, Code: "relay_bad_response", Message: "Push relay returned invalid JSON"}
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(parsed.ExpiresAt))
	if err != nil || strings.TrimSpace(parsed.DeploymentID) == "" || strings.TrimSpace(parsed.APIKey) == "" || strings.TrimSpace(parsed.KeyPrefix) == "" {
		return RelayCredentialResult{}, RelayCredentialError{Status: http.StatusBadGateway, Code: "relay_bad_response", Message: "Push relay returned an incomplete credential response"}
	}
	return RelayCredentialResult{
		Credential: PushRelayCredential{
			RelayURL:     relayURL,
			DeploymentID: strings.TrimSpace(parsed.DeploymentID),
			APIKey:       strings.TrimSpace(parsed.APIKey),
			ExpiresAt:    expiresAt,
			KeyPrefix:    strings.TrimSpace(parsed.KeyPrefix),
		},
		RequestID:  parsed.RequestID,
		APNsTopics: parsed.APNsTopics,
	}, nil
}

// settingsAtomicUpdater is the settings store's cross-process
// read-validate-write primitive (satisfied by the server settings repository).
type settingsAtomicUpdater interface {
	UpdateAtomic(ctx context.Context, update func(current map[string]string) (map[string]string, error)) error
}

// errRelaySettingsNotAtomic refuses a conditional credential write on a
// settings store without a read-validate-write primitive; a separate read and
// write could overwrite a concurrent administrator's clear or registration.
var errRelaySettingsNotAtomic = errors.New("push relay settings do not support atomic updates")

// ErrRelayReregistrationRequired reports that the stored relay state is
// parked behind an explicit administrator re-registration.
var ErrRelayReregistrationRequired = errors.New("push relay re-registration required")

// RegisterRelayCredentialIfAbsent registers with the relay and persists the
// result only if no usable credential landed in the meantime. Registration on
// the relay is stateless (it mints an identity and signs a capability without
// storing anything), so a losing registration is simply discarded and the
// stored winner is returned with registered=false. This is what keeps API
// replicas and the admin endpoint from overwriting each other without holding
// a database connection across the relay round trip, which would deadlock on
// a single-connection pool. force persists regardless, for explicit
// re-registration after the relay rejected the stored capability.
func RegisterRelayCredentialIfAbsent(ctx context.Context, settings *Settings, client RelayHTTPDoer, relayURL string, force bool) (RelayCredentialResult, bool, error) {
	updater, ok := settings.reader.(settingsAtomicUpdater)
	if !ok || force {
		result, err := RegisterRelayCredential(ctx, settings, client, relayURL)
		return result, err == nil, err
	}
	result, err := RequestRelayCredential(ctx, client, relayURL, relayRegisterPath, "", "")
	if err != nil {
		return RelayCredentialResult{}, false, err
	}
	// Yield to a usable credential that landed meanwhile, and to an
	// administrator's clear or a relay rejection (marker set): both park
	// the deployment behind an explicit re-register.
	return storeRelayCredentialUnless(ctx, updater, settings, result, func(stored PushRelayCredential) bool {
		return stored.ReregistrationRequired || (stored.APIKey != "" && !IsLegacyPushRelayKey(stored.APIKey))
	})
}

// storeRelayCredentialUnless atomically persists result unless yield decides
// the stored credential wins, in which case the stored credential is returned
// with stored=false.
func storeRelayCredentialUnless(ctx context.Context, updater settingsAtomicUpdater, settings *Settings, result RelayCredentialResult, yield func(stored PushRelayCredential) bool) (RelayCredentialResult, bool, error) {
	var stored PushRelayCredential
	won := false
	err := updater.UpdateAtomic(ctx, func(current map[string]string) (map[string]string, error) {
		stored = credentialFromValues(current)
		if yield(stored) {
			return nil, nil
		}
		won = true
		return relayCredentialValues(result.Credential), nil
	})
	if err != nil {
		return RelayCredentialResult{}, false, err
	}
	settings.Invalidate(SettingPushRelayURL, SettingPushRelayDeploymentID, SettingPushRelayAPIKey,
		SettingPushRelayExpiresAt, SettingPushRelayKeyPrefix, SettingPushRelayReregister)
	if !won {
		return RelayCredentialResult{Credential: stored}, false, nil
	}
	return result, true, nil
}

func credentialFromValues(values map[string]string) PushRelayCredential {
	expiresAt, _ := time.Parse(time.RFC3339, strings.TrimSpace(values[SettingPushRelayExpiresAt]))
	rereg, _ := strconv.ParseBool(strings.TrimSpace(values[SettingPushRelayReregister]))
	relayURL := strings.TrimRight(strings.TrimSpace(values[SettingPushRelayURL]), "/")
	if relayURL == "" {
		relayURL = DefaultPushRelayURL
	}
	return PushRelayCredential{
		RelayURL:               relayURL,
		DeploymentID:           strings.TrimSpace(values[SettingPushRelayDeploymentID]),
		APIKey:                 strings.TrimSpace(values[SettingPushRelayAPIKey]),
		ExpiresAt:              expiresAt,
		KeyPrefix:              strings.TrimSpace(values[SettingPushRelayKeyPrefix]),
		ReregistrationRequired: rereg,
	}
}

func RegisterRelayCredential(ctx context.Context, settings *Settings, client RelayHTTPDoer, relayURL string) (RelayCredentialResult, error) {
	result, err := RequestRelayCredential(ctx, client, relayURL, relayRegisterPath, "", "")
	if err != nil {
		return RelayCredentialResult{}, err
	}
	if err := settings.UpdatePushRelayCredential(ctx, result.Credential); err != nil {
		return RelayCredentialResult{}, err
	}
	return result, nil
}

func RotateRelayCredential(ctx context.Context, settings *Settings, client RelayHTTPDoer, current PushRelayCredential) (RelayCredentialResult, error) {
	key := RelayRotationIdempotencyKey(current.DeploymentID, current.APIKey)
	result, err := RequestRelayCredential(ctx, client, current.RelayURL, relayRotatePath, current.APIKey, key)
	if err != nil {
		return RelayCredentialResult{}, err
	}
	if result.Credential.DeploymentID != current.DeploymentID {
		return RelayCredentialResult{}, RelayCredentialError{Status: http.StatusBadGateway, Code: "relay_bad_response", Message: "Push relay changed the deployment during rotation"}
	}
	if err := settings.UpdatePushRelayCredential(ctx, result.Credential); err != nil {
		return RelayCredentialResult{}, err
	}
	return result, nil
}

func RenewRelayCredential(ctx context.Context, settings *Settings, client RelayHTTPDoer, current PushRelayCredential) (RelayCredentialResult, error) {
	result, err := RequestRelayCredential(ctx, client, current.RelayURL, relayRenewPath, current.APIKey, "")
	if err != nil {
		return RelayCredentialResult{}, err
	}
	if result.Credential.DeploymentID != current.DeploymentID {
		return RelayCredentialResult{}, RelayCredentialError{Status: http.StatusBadGateway, Code: "relay_bad_response", Message: "Push relay changed the deployment during renewal"}
	}
	if err := settings.UpdatePushRelayCredential(ctx, result.Credential); err != nil {
		return RelayCredentialResult{}, err
	}
	return result, nil
}

// replaceRejectedRelayCredential registers a fresh deployment in place of a
// capability the relay rejected. The relay rejects a capability whose
// generation was superseded (for example by another server holding a copy of
// the same credential, such as a database clone), whose signing key it no
// longer trusts, or that expired past its renewal grace; none of those is
// recoverable with the old identity, and registration needs no prior state.
//
// The replacement is persisted only while the rejected key is still stored and
// not parked, so concurrent replicas converge on one winner and an
// administrator's clear is never undone. A losing registration is discarded
// and the stored credential is returned with replaced=false.
func replaceRejectedRelayCredential(ctx context.Context, settings *Settings, client RelayHTTPDoer, relayURL, rejectedKey string) (RelayCredentialResult, bool, error) {
	result, err := RequestRelayCredential(ctx, client, relayURL, relayRegisterPath, "", "")
	if err != nil {
		return RelayCredentialResult{}, false, err
	}
	updater, ok := settings.reader.(settingsAtomicUpdater)
	if !ok {
		return RelayCredentialResult{}, false, errRelaySettingsNotAtomic
	}
	return storeRelayCredentialUnless(ctx, updater, settings, result, func(stored PushRelayCredential) bool {
		return stored.Parked() || stored.APIKey != rejectedKey
	})
}

// parkRelayCredential discards a credential the relay disabled and waits for
// an explicit administrator registration, in the same stored shape as an
// administrator's clear. It parks only while the disabled key is still
// stored, so a credential registered meanwhile (by an administrator or
// another replica) is kept and returned with parked=false.
func parkRelayCredential(ctx context.Context, settings *Settings, disabled PushRelayCredential) (PushRelayCredential, bool, error) {
	updater, ok := settings.reader.(settingsAtomicUpdater)
	if !ok {
		return PushRelayCredential{}, false, errRelaySettingsNotAtomic
	}
	cleared := PushRelayCredential{RelayURL: disabled.RelayURL, ReregistrationRequired: true}
	result, parked, err := storeRelayCredentialUnless(ctx, updater, settings, RelayCredentialResult{Credential: cleared}, func(stored PushRelayCredential) bool {
		return stored.APIKey != disabled.APIKey
	})
	return result.Credential, parked, err
}

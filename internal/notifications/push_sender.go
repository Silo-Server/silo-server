package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/telemetry"

	"github.com/Silo-Server/silo-server/internal/secret"
)

var pushRetrySchedule = []time.Duration{
	0,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
	30 * time.Minute,
}

const (
	pushMaxAttempts         = 5
	pushDispatchQueue       = 512
	pushRetryClaimLimit     = 100
	pushRelayRequestTimeout = 15 * time.Second
	pushRelayMaxRetryAfter  = 23 * time.Hour
	relayAppleSendPath      = "/v1/apple/send"
	relayFcmSendPath        = "/v1/fcm/send"
	relayModePrivateAlert   = "private_alert"

	// relayCodeIdempotencyKeyReuse is the relay's 422 code for a retry whose
	// payload differs from the one recorded under its idempotency key.
	relayCodeIdempotencyKeyReuse = "idempotency_key_reuse"

	// relayTargetRecheck is how long sends to a relay that rejected the push
	// target fields omit them before trying again.
	relayTargetRecheck = time.Hour

	// relayReplacementCooldown bounds automatic re-registration after the
	// relay rejects a capability.
	relayReplacementCooldown = 15 * time.Minute
)

var errRelayReplacementCoolingDown = errors.New("push relay credential was rejected; replacement is cooling down")

func pushRetryDelay(completedAttempt int) (time.Duration, bool) {
	if completedAttempt < 1 || completedAttempt >= pushMaxAttempts {
		return 0, false
	}
	return pushRetrySchedule[completedAttempt] - pushRetrySchedule[completedAttempt-1], true
}

func pushRetryDelayWithHint(completedAttempt int, retryAfter time.Duration) (time.Duration, bool) {
	delay, more := pushRetryDelay(completedAttempt)
	if retryAfter > 0 {
		// The relay retains idempotency state for 24 hours. Stay safely inside
		// that window even if APNs returns an unusually large Retry-After value.
		delay = min(retryAfter, pushRelayMaxRetryAfter)
	}
	return delay, more
}

func terminalAPNsDeviceRejection(status int, code, message string) bool {
	if status != http.StatusUnprocessableEntity || code != "apns_rejected" {
		return false
	}
	const prefix = "APNs rejected the notification:"
	reason := strings.TrimSpace(strings.TrimPrefix(message, prefix))
	switch reason {
	case "BadDeviceToken", "InvalidToken", "DeviceTokenNotForTopic", "Unregistered":
		return true
	default:
		return false
	}
}

// terminalFCMDeviceRejection recognizes the relay's fcm_rejected responses
// whose FCM error code means the registration token is permanently gone.
// Request-level rejections (e.g. INVALID_ARGUMENT payload complaints) keep the
// device enabled, matching the conservative APNs list above.
func terminalFCMDeviceRejection(status int, code, message string) bool {
	if status != http.StatusUnprocessableEntity || code != "fcm_rejected" {
		return false
	}
	const prefix = "FCM rejected the notification:"
	reason := strings.TrimSpace(strings.TrimPrefix(message, prefix))
	return reason == "UNREGISTERED"
}

func terminalDeviceRejection(platform string, status int, code, message string) bool {
	if platform == PushPlatformAndroid {
		return terminalFCMDeviceRejection(status, code, message)
	}
	return terminalAPNsDeviceRejection(status, code, message)
}

// pushRelayTarget names the deployment and profile a push belongs to, so a
// device signed into several servers or profiles fetches its display data
// from the right one. Both are omitted when the server identity cannot be
// read, and for a relay that predates them.
type pushRelayTarget struct {
	ServerID  string `json:"server_id,omitempty"`
	ProfileID string `json:"profile_id,omitempty"`
}

type pushRelayAppleRequest struct {
	Token          string  `json:"token"`
	Environment    string  `json:"environment"`
	Topic          string  `json:"topic"`
	Mode           string  `json:"mode"`
	ServerDeviceID string  `json:"server_device_id"`
	DeliveryID     string  `json:"delivery_id"`
	CollapseID     *string `json:"collapse_id,omitempty"`
	pushRelayTarget
}

type pushRelayAppleResponse struct {
	RequestID string `json:"request_id"`
	APNsID    string `json:"apns_id"`
	Status    string `json:"status"`
}

// pushRelayFcmRequest matches the relay's /v1/fcm/send contract: no
// environment or topic — the relay's Firebase project is the boundary.
type pushRelayFcmRequest struct {
	Token          string  `json:"token"`
	Mode           string  `json:"mode"`
	ServerDeviceID string  `json:"server_device_id"`
	DeliveryID     string  `json:"delivery_id"`
	CollapseID     *string `json:"collapse_id,omitempty"`
	pushRelayTarget
}

type pushRelayErrorResponse struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

type pushSendResult struct {
	OK             bool
	HTTPStatus     int
	RetryAfter     time.Duration
	RelayRequestID string
	UpstreamReason string
	Message        string
	TerminalDevice bool
}

type pushSender struct {
	devices             *PushDeviceRepository
	deliveries          *DeliveryRepository
	cipher              *secret.Cipher
	settings            *Settings
	client              *http.Client
	logger              *slog.Logger
	developmentRelayURL string
	now                 func() time.Time
	// serverID reads the deployment identity getServerIdentity reports.
	serverID func(context.Context) (string, error)

	// renewMu serializes credential renewal and replacement in this process;
	// lastRelayReplacement is guarded by it.
	renewMu              sync.Mutex
	lastRelayReplacement time.Time

	// targetMu guards relayWithoutTarget: per relay base URL, the time until
	// which sends omit the push target fields that relay rejected.
	targetMu           sync.Mutex
	relayWithoutTarget map[string]time.Time
}

func newPushSender(devices *PushDeviceRepository, deliveries *DeliveryRepository, cipher *secret.Cipher, settings *Settings) *pushSender {
	// The Worker allows APNs up to 10 seconds. Leave enough room for edge
	// routing and response processing so Silo receives the relay's classified
	// outcome instead of manufacturing an ambiguous client-side timeout.
	client := newNotificationHTTPClient(nil, pushRelayRequestTimeout)
	return &pushSender{
		devices:             devices,
		deliveries:          deliveries,
		cipher:              cipher,
		settings:            settings,
		client:              client,
		logger:              slog.Default().With("component", "notifications.apple_push"),
		developmentRelayURL: os.Getenv("SILO_PUSH_RELAY_DEVELOPMENT_URL"),
		now:                 time.Now,
		relayWithoutTarget:  map[string]time.Time{},
	}
}

func (s *pushSender) processAttempt(ctx context.Context, attempt PushDeliveryAttempt) *PushDeliveryAttempt {
	device, err := s.devices.getPushDeviceByID(ctx, attempt.PushDeviceID)
	if err != nil || device == nil {
		if err == nil {
			return s.finalize(ctx, attempt, PushOutcomeFailed, "", "push device deleted", nil, "", nil)
		}
		if ctx.Err() == nil {
			s.logger.WarnContext(ctx, "push device lookup failed", "attempt_id", attempt.ID, "error", err)
		}
		return nil
	}
	deliveryEnabled := s.settings.ApplePushDeliveryEnabled(ctx)
	if device.Platform == PushPlatformAndroid {
		deliveryEnabled = s.settings.AndroidPushDeliveryEnabled(ctx)
	}
	if !device.Enabled || device.PushMode != PushModePrivatePush || !deliveryEnabled {
		return s.finalize(ctx, attempt, PushOutcomeFailed, "delivery_disabled", "push delivery disabled", nil, "", nil)
	}
	if attempt.NotificationDeliveryID != nil {
		row, err := s.deliveries.GetRowByID(ctx, *attempt.NotificationDeliveryID)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.WarnContext(ctx, "push delivery lookup failed",
					"attempt_id", attempt.ID,
					"delivery_id", *attempt.NotificationDeliveryID,
					"error", err)
			}
			return nil
		}
		if row == nil {
			return s.finalize(ctx, attempt, PushOutcomeFailed, "delivery_missing", "delivery row missing", nil, "", nil)
		}
		// A device re-registered under another profile, or under one with the
		// same ID on another account, no longer gets the old owner's delivery.
		if row.UserID != device.UserID || row.ProfileID != device.ProfileID {
			return s.finalize(ctx, attempt, PushOutcomeFailed, "device_reassigned", "push device reassigned", nil, "", nil)
		}
	}

	ciphertext, aad := device.APNsTokenCiphertext, pushDeviceAPNsTokenAAD(device.ID)
	if device.Platform == PushPlatformAndroid {
		ciphertext, aad = device.FCMTokenCiphertext, pushDeviceFCMTokenAAD(device.ID)
	}
	token, err := s.cipher.Decrypt(ciphertext, aad)
	if err != nil {
		return s.finalize(ctx, attempt, PushOutcomeFailed, "decrypt_failed", "push token decrypt failed", nil, "", nil)
	}
	result := s.send(ctx, attempt, device, token)
	attemptNumber := attempt.AttemptNumber + 1
	if result.OK {
		updated, _ := s.devices.FinalizePushAttempt(ctx, attempt.ID, PushOutcomeDelivered, attemptNumber,
			result.RelayRequestID, &result.HTTPStatus, result.UpstreamReason, "", nil)
		_ = s.devices.RecordPushSuccess(ctx, device.ID)
		return updated
	}

	statusPtr := (*int)(nil)
	if result.HTTPStatus > 0 {
		statusPtr = &result.HTTPStatus
	}
	code := result.UpstreamReason
	if code == "" {
		code = result.Message
	}
	if code == "" {
		code = "push_delivery_failed"
	}
	_ = s.devices.RecordPushFailure(ctx, device.ID, code, result.TerminalDevice)

	delay, more := pushRetryDelayWithHint(attemptNumber, result.RetryAfter)
	if more && !result.TerminalDevice && retryableHTTPStatus(result.HTTPStatus) {
		nextRetry := time.Now().Add(delay)
		return s.finalize(ctx, attempt, PushOutcomeRetrying, result.UpstreamReason, result.Message, statusPtr, result.RelayRequestID, &nextRetry)
	}
	return s.finalize(ctx, attempt, PushOutcomeFailed, result.UpstreamReason, result.Message, statusPtr, result.RelayRequestID, nil)
}

func (s *pushSender) finalize(ctx context.Context, attempt PushDeliveryAttempt, outcome string, reason, message string, statusPtr *int, relayRequestID string, nextRetryAt *time.Time) *PushDeliveryAttempt {
	attemptNumber := attempt.AttemptNumber + 1
	updated, err := s.devices.FinalizePushAttempt(ctx, attempt.ID, outcome, attemptNumber, relayRequestID, statusPtr, reason, message, nextRetryAt)
	if err != nil && ctx.Err() == nil {
		s.logger.WarnContext(ctx, "finalize push attempt failed", "attempt_id", attempt.ID, "error", err)
	}
	return updated
}

func (s *pushSender) send(ctx context.Context, attempt PushDeliveryAttempt, device *PushDevice, token string) (result pushSendResult) {
	ctx, finishObservation := telemetry.StartDependency(ctx, "notifications", "worker", "push")
	defer func() { finishObservation(deliveryObservationError(ctx, result.OK)) }()
	credential, err := s.prepareRelayCredential(ctx)
	if err != nil {
		return pushSendResult{HTTPStatus: http.StatusServiceUnavailable, Message: err.Error(), UpstreamReason: "relay_credential_unavailable"}
	}
	target, identityKnown := s.relayTarget(ctx, device)
	result = s.sendWithCapability(ctx, attempt, device, token, target, credential.RelayURL, credential.APIKey)
	if result.HTTPStatus == http.StatusUnauthorized {
		recovered, err := s.recoverRelayCredential(ctx, credential, result.UpstreamReason)
		if err != nil {
			if result.UpstreamReason == relayCodeDeploymentDisabled {
				return result
			}
			return pushSendResult{HTTPStatus: http.StatusServiceUnavailable, Message: err.Error(), UpstreamReason: "relay_credential_unavailable"}
		}
		result = s.sendWithCapability(ctx, attempt, device, token, target, recovered.RelayURL, recovered.APIKey)
	}
	if !identityKnown && relayRecordedOtherTarget(result) {
		// The relay may have recorded this attempt with the target the failed
		// identity lookup could not name, so a later retry can still match it.
		return pushSendResult{HTTPStatus: http.StatusServiceUnavailable, Message: "server identity unavailable", UpstreamReason: "server_identity_unavailable"}
	}
	return result
}

// relayTarget names this deployment and the device's profile. A delivery's
// profile is the device's: processAttempt refuses a reassigned device. The
// target is all or nothing, and each part must meet the relay's 1-128
// character rule: the relay answers an empty or oversized one with 400
// invalid_field, which would fail every push. Without a target the push still
// goes out and the device falls back to its active server and profile, as it
// does for an older server. identityKnown is false only when the identity
// lookup failed, so a later send may name a target this one could not.
func (s *pushSender) relayTarget(ctx context.Context, device *PushDevice) (target pushRelayTarget, identityKnown bool) {
	if s.serverID == nil {
		return pushRelayTarget{}, true
	}
	serverID, err := s.serverID(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.WarnContext(ctx, "push sent without its server identity", "error", err)
		}
		return pushRelayTarget{}, false
	}
	if !relayTargetID(serverID) || !relayTargetID(device.ProfileID) {
		return pushRelayTarget{}, true
	}
	return pushRelayTarget{ServerID: serverID, ProfileID: device.ProfileID}, true
}

// relayTargetID applies the relay's opaque-ID length rule in bytes, which is
// never fewer than the UTF-16 units the relay counts.
func relayTargetID(id string) bool { return id != "" && len(id) <= 128 }

func (s *pushSender) prepareRelayCredential(ctx context.Context) (PushRelayCredential, error) {
	s.renewMu.Lock()
	defer s.renewMu.Unlock()
	current := LoadPushRelayCredential(ctx, s.settings)
	relayURL, err := NormalizePushRelayURL(current.RelayURL, s.developmentRelayURL)
	if err != nil {
		return PushRelayCredential{}, err
	}
	current.RelayURL = relayURL
	// An administrator's clear, or a relay that disabled this deployment,
	// parks the credential behind an explicit re-register; first-use
	// registration must not undo that decision.
	if current.Parked() {
		return PushRelayCredential{}, ErrRelayReregistrationRequired
	}
	// Older servers parked a capability the relay rejected while keeping it
	// stored. Heal that state instead of waiting for an administrator.
	if current.ReregistrationRequired {
		return s.replaceRejectedRelayCredentialLocked(ctx, current, "parked_by_older_server")
	}
	// Delivery is on by default, so the first send on a server that has never
	// registered self-registers with the relay instead of failing. Legacy
	// pre-capability keys take the same path.
	if current.APIKey == "" || IsLegacyPushRelayKey(current.APIKey) {
		return s.registerRelayCredential(ctx, relayURL)
	}
	if RelayCredentialNeedsRenewal(s.now(), current.ExpiresAt, current.DeploymentID) {
		result, err := RenewRelayCredential(ctx, s.settings, s.client, current)
		if err == nil {
			return result.Credential, nil
		}
		if code, rejected := relayCredentialRejection(err); rejected {
			return s.handleRelayRejectionLocked(ctx, current, code)
		}
		// A proactive refresh must not suppress delivery while the current
		// capability is still valid. Reactive token_expired handling remains the
		// final safety net once its actual expiry is reached.
		if s.now().Before(current.ExpiresAt) {
			return current, nil
		}
		return PushRelayCredential{}, err
	}
	return current, nil
}

// registerRelayCredential provisions the relay credential on first use. If
// another replica or the admin endpoint won the race, the stored credential
// is returned instead and its own relay origin is kept: a capability only
// works against the relay that issued it.
func (s *pushSender) registerRelayCredential(ctx context.Context, relayURL string) (PushRelayCredential, error) {
	result, registered, err := RegisterRelayCredentialIfAbsent(ctx, s.settings, s.client, relayURL, false)
	if err != nil {
		return PushRelayCredential{}, err
	}
	credential := result.Credential
	if !registered {
		if credential.ReregistrationRequired || credential.APIKey == "" {
			// An administrator cleared the relay while this registration was
			// in flight; the clear wins.
			return PushRelayCredential{}, ErrRelayReregistrationRequired
		}
		storedURL, err := NormalizePushRelayURL(credential.RelayURL, s.developmentRelayURL)
		if err != nil {
			return PushRelayCredential{}, err
		}
		credential.RelayURL = storedURL
	}
	return credential, nil
}

// recoverRelayCredential answers a relay 401 for the capability that was just
// sent: an expired capability is renewed, and any other rejection is healed
// by registering a fresh deployment.
func (s *pushSender) recoverRelayCredential(ctx context.Context, failed PushRelayCredential, code string) (PushRelayCredential, error) {
	s.renewMu.Lock()
	defer s.renewMu.Unlock()

	current := LoadPushRelayCredential(ctx, s.settings)
	if current.Parked() {
		return PushRelayCredential{}, ErrRelayReregistrationRequired
	}
	relayURL, err := NormalizePushRelayURL(current.RelayURL, s.developmentRelayURL)
	if err != nil {
		return PushRelayCredential{}, err
	}
	current.RelayURL = relayURL
	// Another sender may have renewed or replaced the capability while this
	// goroutine waited for the lock.
	if current.APIKey != "" && current.APIKey != failed.APIKey && !current.ReregistrationRequired {
		return current, nil
	}
	if code != relayCodeTokenExpired {
		return s.handleRelayRejectionLocked(ctx, current, code)
	}
	result, err := RenewRelayCredential(ctx, s.settings, s.client, current)
	if err == nil {
		return result.Credential, nil
	}
	if rejectionCode, rejected := relayCredentialRejection(err); rejected {
		// Past the renewal grace, or no longer the relay's current
		// generation: the old identity cannot be renewed.
		return s.handleRelayRejectionLocked(ctx, current, rejectionCode)
	}
	return PushRelayCredential{}, err
}

// handleRelayRejectionLocked parks a deployment the relay disabled and
// replaces any other rejected capability. Callers hold renewMu.
func (s *pushSender) handleRelayRejectionLocked(ctx context.Context, current PushRelayCredential, code string) (PushRelayCredential, error) {
	if code == relayCodeDeploymentDisabled {
		stored, parked, err := parkRelayCredential(ctx, s.settings, current)
		if err != nil {
			return PushRelayCredential{}, err
		}
		if !parked {
			return s.adoptStoredRelayCredential(stored)
		}
		s.logger.ErrorContext(ctx, "push relay disabled this deployment; register again from the admin settings to resume push delivery",
			"deployment_id", current.DeploymentID)
		return PushRelayCredential{}, ErrRelayReregistrationRequired
	}
	return s.replaceRejectedRelayCredentialLocked(ctx, current, code)
}

// adoptStoredRelayCredential returns the credential another writer stored
// while this one was deciding, keeping its own relay origin: a capability only
// works against the relay that issued it. A clear wins.
func (s *pushSender) adoptStoredRelayCredential(stored PushRelayCredential) (PushRelayCredential, error) {
	if stored.Parked() || stored.APIKey == "" {
		return PushRelayCredential{}, ErrRelayReregistrationRequired
	}
	storedURL, err := NormalizePushRelayURL(stored.RelayURL, s.developmentRelayURL)
	if err != nil {
		return PushRelayCredential{}, err
	}
	stored.RelayURL = storedURL
	return stored, nil
}

// replaceRejectedRelayCredentialLocked registers a new deployment in place of
// a rejected capability, at most once per cooldown per process so a relay
// that rejects fresh credentials too is not hammered. Callers hold renewMu.
func (s *pushSender) replaceRejectedRelayCredentialLocked(ctx context.Context, current PushRelayCredential, code string) (PushRelayCredential, error) {
	now := s.now()
	if !s.lastRelayReplacement.IsZero() && now.Sub(s.lastRelayReplacement) < relayReplacementCooldown {
		return PushRelayCredential{}, errRelayReplacementCoolingDown
	}
	s.lastRelayReplacement = now
	result, replaced, err := replaceRejectedRelayCredential(ctx, s.settings, s.client, current.RelayURL, current.APIKey)
	if err != nil {
		s.logger.WarnContext(ctx, "push relay rejected the stored capability and registering a replacement failed",
			"deployment_id", current.DeploymentID, "relay_code", code, "error", err)
		return PushRelayCredential{}, err
	}
	credential := result.Credential
	if !replaced {
		return s.adoptStoredRelayCredential(credential)
	}
	s.logger.WarnContext(ctx, "push relay rejected the stored capability; registered a new deployment",
		"previous_deployment_id", current.DeploymentID, "deployment_id", credential.DeploymentID, "relay_code", code)
	return credential, nil
}

// relayCredentialRejection reports the relay's code when it refused a
// credential request with 401.
func relayCredentialRejection(err error) (string, bool) {
	relayErr, ok := errors.AsType[RelayCredentialError](err)
	if !ok || relayErr.Status != http.StatusUnauthorized {
		return "", false
	}
	return relayErr.Code, true
}

// sendWithCapability posts one attempt to the relay. A relay that predates
// the push target fields rejects them before it records the idempotency key,
// so the attempt is resent once without them under the same key, and later
// sends to that relay omit them for relayTargetRecheck.
//
// Only that rejection (400 unexpected_field naming a target field) may mark a
// relay as lacking them.
//
// The fields are part of the relay's idempotency hash, and nothing records
// which choice an attempt was first sent with: an earlier server version,
// another replica, or a send inside relayTargetRecheck may have made the
// other one. The relay answers a retry that differs with 422
// idempotency_key_reuse, so the attempt is resent once with the other choice,
// which the relay replays or resumes instead of delivering again.
func (s *pushSender) sendWithCapability(ctx context.Context, attempt PushDeliveryAttempt, device *PushDevice, token string, target pushRelayTarget, relayURL, apiKey string) pushSendResult {
	sent := target
	if sent != (pushRelayTarget{}) && s.relayLacksTarget(relayURL) {
		sent = pushRelayTarget{}
	}
	result := s.postRelay(ctx, attempt, device, token, sent, relayURL, apiKey)
	switch {
	case sent != (pushRelayTarget{}) && relayRejectedTarget(result):
		s.markRelayLacksTarget(relayURL)
		s.logger.InfoContext(ctx, "push relay does not accept server_id and profile_id yet; sending without them")
		return s.postRelay(ctx, attempt, device, token, pushRelayTarget{}, relayURL, apiKey)
	case target != (pushRelayTarget{}) && relayRecordedOtherTarget(result):
		other := pushRelayTarget{}
		if sent == (pushRelayTarget{}) {
			other = target
		}
		// A relay that refuses the fields never recorded them, so its answer
		// says nothing about the payload the 422 was about.
		if retry := s.postRelay(ctx, attempt, device, token, other, relayURL, apiKey); !relayRejectedTarget(retry) {
			return retry
		}
	}
	return result
}

func (s *pushSender) markRelayLacksTarget(relayURL string) {
	s.targetMu.Lock()
	defer s.targetMu.Unlock()
	s.relayWithoutTarget[relayURL] = s.now().Add(relayTargetRecheck)
}

func (s *pushSender) relayLacksTarget(relayURL string) bool {
	s.targetMu.Lock()
	defer s.targetMu.Unlock()
	return s.now().Before(s.relayWithoutTarget[relayURL])
}

// relayRejectedTarget recognizes an older relay's strict-field rejection of
// a push target field. Every other 400 keeps its ordinary handling.
func relayRejectedTarget(result pushSendResult) bool {
	if result.HTTPStatus != http.StatusBadRequest || result.UpstreamReason != "unexpected_field" {
		return false
	}
	field := strings.TrimSpace(strings.TrimPrefix(result.Message, "unexpected field:"))
	return field == "server_id" || field == "profile_id"
}

// relayRecordedOtherTarget recognizes the relay refusing a retry whose payload
// differs from the one it recorded under the attempt's idempotency key.
func relayRecordedOtherTarget(result pushSendResult) bool {
	return result.HTTPStatus == http.StatusUnprocessableEntity && result.UpstreamReason == relayCodeIdempotencyKeyReuse
}

func (s *pushSender) postRelay(ctx context.Context, attempt PushDeliveryAttempt, device *PushDevice, token string, target pushRelayTarget, relayURL, apiKey string) pushSendResult {
	deliveryID := attempt.ID
	if attempt.NotificationDeliveryID != nil {
		deliveryID = *attempt.NotificationDeliveryID
	}
	collapseID := deliveryID
	sendPath := relayAppleSendPath
	var payload any = pushRelayAppleRequest{
		Token:           token,
		Environment:     device.APNsEnvironment,
		Topic:           device.APNsTopic,
		Mode:            relayModePrivateAlert,
		ServerDeviceID:  device.ServerDeviceID,
		DeliveryID:      deliveryID,
		CollapseID:      &collapseID,
		pushRelayTarget: target,
	}
	if device.Platform == PushPlatformAndroid {
		sendPath = relayFcmSendPath
		payload = pushRelayFcmRequest{
			Token:           token,
			Mode:            relayModePrivateAlert,
			ServerDeviceID:  device.ServerDeviceID,
			DeliveryID:      deliveryID,
			CollapseID:      &collapseID,
			pushRelayTarget: target,
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return pushSendResult{Message: "relay payload build failed", UpstreamReason: "payload_build_failed"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+sendPath, bytes.NewReader(body))
	if err != nil {
		return pushSendResult{Message: "invalid push relay URL", UpstreamReason: "invalid_relay_url"}
	}
	if req.URL.Scheme != schemeHTTPS {
		return pushSendResult{Message: "invalid push relay URL", UpstreamReason: "invalid_relay_url"}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Silo-Push/1.0")
	// One logical delivery attempt keeps the same key across every transport
	// retry. The relay can then distinguish a safe retry from a new delivery and
	// refuse to resend an ambiguous APNs outcome.
	req.Header.Set("Idempotency-Key", attempt.ID)

	resp, err := s.client.Do(req)
	if err != nil {
		return pushSendResult{Message: classifyWebhookError(err), UpstreamReason: "relay_unreachable"}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var parsed pushRelayAppleResponse
		_ = json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&parsed)
		if parsed.RequestID == "" {
			parsed.RequestID = resp.Header.Get("X-Request-ID")
		}
		return pushSendResult{
			OK:             true,
			HTTPStatus:     resp.StatusCode,
			RelayRequestID: parsed.RequestID,
		}
	}

	var parsed pushRelayErrorResponse
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	_ = json.Unmarshal(data, &parsed)
	code := parsed.Error.Code
	if code == "" {
		code = fmt.Sprintf("http_%d", resp.StatusCode)
	}
	message := parsed.Error.Message
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	if message == "" {
		message = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return pushSendResult{
		HTTPStatus:     resp.StatusCode,
		RetryAfter:     parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		RelayRequestID: parsed.Error.RequestID,
		UpstreamReason: code,
		Message:        strings.TrimSpace(message),
		TerminalDevice: terminalDeviceRejection(device.Platform, resp.StatusCode, code, message),
	}
}

// PushDispatcher implements the channel Dispatcher interface for Apple push
// on top of the shared channelDispatcher core, with the retry/recovery sweep
// integrated.
type PushDispatcher struct {
	core channelDispatcher[PushDeliveryAttempt]
}

func newPushDispatcher(sender *pushSender) *PushDispatcher {
	return &PushDispatcher{core: channelDispatcher[PushDeliveryAttempt]{
		channel:      "apple push",
		queue:        make(chan string, pushDispatchQueue),
		logger:       slog.Default().With("component", "notifications.apple_push.dispatch"),
		claimPending: sender.devices.ClaimPendingPushForDelivery,
		process: func(ctx context.Context, attempt PushDeliveryAttempt) {
			sender.processAttempt(ctx, attempt)
		},
		// The dispatcher claims attempts for every platform; processAttempt
		// applies the per-platform delivery toggle to each device.
		enabled:    sender.settings.PushDeliveryEnabled,
		claimDue:   sender.devices.ClaimDuePushAttempts,
		claimLimit: pushRetryClaimLimit,
	}}
}

// Dispatch queues the delivery's Apple push attempts for immediate send.
func (d *PushDispatcher) Dispatch(_ context.Context, delivery DeliveryRow) error {
	if d == nil {
		return nil
	}
	d.core.dispatch(delivery.ID)
	return nil
}

// Run consumes the dispatch queue and the retry/recovery sweep until ctx is
// canceled.
func (d *PushDispatcher) Run(ctx context.Context) {
	d.core.run(ctx)
}

type ApplePushTestResult struct {
	AttemptID      string
	PushDeviceID   string
	ServerDeviceID string
	// DeliveryID is the notification the test carried; empty when the
	// profile had none.
	DeliveryID     string
	Outcome        string
	RelayRequestID string
	UpstreamStatus *int
	UpstreamReason string
	FailureMessage string
}

func (s *System) SendApplePushTest(ctx context.Context, profileID, serverDeviceID string) (*ApplePushTestResult, error) {
	return s.sendPushTest(ctx, PushPlatformApple, profileID, serverDeviceID)
}

func (s *System) SendAndroidPushTest(ctx context.Context, profileID, serverDeviceID string) (*ApplePushTestResult, error) {
	return s.sendPushTest(ctx, PushPlatformAndroid, profileID, serverDeviceID)
}

func (s *System) sendPushTest(ctx context.Context, platform, profileID, serverDeviceID string) (*ApplePushTestResult, error) {
	if s == nil || s.pushDeviceRepo == nil || s.pushSender == nil {
		return nil, ErrPushDeliveryUnavailable
	}
	deliveryEnabled := s.Settings.ApplePushDeliveryEnabled(ctx)
	if platform == PushPlatformAndroid {
		deliveryEnabled = s.Settings.AndroidPushDeliveryEnabled(ctx)
	}
	if !deliveryEnabled {
		return nil, ErrPushDeliveryUnavailable
	}
	// No relay-credential gate here: a test push on a fresh install goes
	// through the same first-use registration as an ordinary delivery.
	attempt, device, err := s.pushDeviceRepo.EnqueueTestAttempt(ctx, platform, profileID, serverDeviceID)
	if err != nil {
		return nil, err
	}
	claimed, err := s.pushDeviceRepo.ClaimPushAttemptByID(ctx, attempt.ID)
	if err != nil {
		return nil, err
	}
	if len(claimed) != 1 {
		return nil, fmt.Errorf("push test attempt was not claimable")
	}
	updated := s.pushSender.processAttempt(ctx, claimed[0])
	if updated == nil {
		updated, _ = s.pushDeviceRepo.GetPushAttempt(ctx, attempt.ID)
	}
	if updated == nil {
		return nil, fmt.Errorf("push test attempt disappeared")
	}
	result := &ApplePushTestResult{
		AttemptID:      updated.ID,
		PushDeviceID:   updated.PushDeviceID,
		ServerDeviceID: device.ServerDeviceID,
		Outcome:        updated.Outcome,
		UpstreamStatus: updated.UpstreamStatus,
	}
	if updated.NotificationDeliveryID != nil {
		result.DeliveryID = *updated.NotificationDeliveryID
	}
	if updated.RelayRequestID != nil {
		result.RelayRequestID = *updated.RelayRequestID
	}
	if updated.UpstreamReason != nil {
		result.UpstreamReason = *updated.UpstreamReason
	}
	if updated.FailureMessage != nil {
		result.FailureMessage = *updated.FailureMessage
	}
	return result, nil
}

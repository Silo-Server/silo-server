package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

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
)

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

type pushRelayAppleRequest struct {
	Token          string  `json:"token"`
	Environment    string  `json:"environment"`
	Topic          string  `json:"topic"`
	Mode           string  `json:"mode"`
	ServerDeviceID string  `json:"server_device_id"`
	DeliveryID     string  `json:"delivery_id"`
	CollapseID     *string `json:"collapse_id,omitempty"`
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
	renewMu             sync.Mutex
	developmentRelayURL string
	now                 func() time.Time
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
		if row.ProfileID != device.ProfileID {
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

func (s *pushSender) send(ctx context.Context, attempt PushDeliveryAttempt, device *PushDevice, token string) pushSendResult {
	credential, err := s.prepareRelayCredential(ctx)
	if err != nil {
		return pushSendResult{HTTPStatus: http.StatusServiceUnavailable, Message: err.Error(), UpstreamReason: "relay_credential_unavailable"}
	}
	result := s.sendWithCapability(ctx, attempt, device, token, credential.RelayURL, credential.APIKey)
	if result.HTTPStatus != http.StatusUnauthorized || result.UpstreamReason != "token_expired" {
		if result.HTTPStatus == http.StatusUnauthorized {
			_ = s.markReregistrationRequired(ctx, credential)
		}
		return result
	}

	renewed, err := s.renewRelayCapability(ctx, credential.APIKey)
	if err != nil {
		return pushSendResult{
			HTTPStatus:     http.StatusServiceUnavailable,
			UpstreamReason: "relay_renewal_failed",
			Message:        "push relay capability renewal failed",
		}
	}
	return s.sendWithCapability(ctx, attempt, device, token, renewed.RelayURL, renewed.APIKey)
}

func (s *pushSender) prepareRelayCredential(ctx context.Context) (PushRelayCredential, error) {
	s.renewMu.Lock()
	defer s.renewMu.Unlock()
	current := LoadPushRelayCredential(ctx, s.settings)
	relayURL, err := NormalizePushRelayURL(current.RelayURL, s.developmentRelayURL)
	if err != nil {
		return PushRelayCredential{}, err
	}
	current.RelayURL = relayURL
	if current.APIKey == "" {
		return PushRelayCredential{}, fmt.Errorf("push relay API key not configured")
	}
	if IsLegacyPushRelayKey(current.APIKey) {
		result, err := RegisterRelayCredential(ctx, s.settings, s.client, relayURL)
		return result.Credential, err
	}
	if current.ReregistrationRequired {
		return PushRelayCredential{}, fmt.Errorf("push relay re-registration required")
	}
	if RelayCredentialNeedsRenewal(s.now(), current.ExpiresAt, current.DeploymentID) {
		result, err := RenewRelayCredential(ctx, s.settings, s.client, current)
		// A proactive refresh must not suppress delivery while the current
		// capability is still valid. Reactive token_expired handling remains the
		// final safety net once its actual expiry is reached.
		if err != nil && s.now().Before(current.ExpiresAt) {
			return current, nil
		}
		return result.Credential, err
	}
	return current, nil
}

func (s *pushSender) sendWithCapability(ctx context.Context, attempt PushDeliveryAttempt, device *PushDevice, token, relayURL, apiKey string) pushSendResult {
	deliveryID := attempt.ID
	if attempt.NotificationDeliveryID != nil {
		deliveryID = *attempt.NotificationDeliveryID
	}
	collapseID := deliveryID
	sendPath := relayAppleSendPath
	var payload any = pushRelayAppleRequest{
		Token:          token,
		Environment:    device.APNsEnvironment,
		Topic:          device.APNsTopic,
		Mode:           "private_alert",
		ServerDeviceID: device.ServerDeviceID,
		DeliveryID:     deliveryID,
		CollapseID:     &collapseID,
	}
	if device.Platform == PushPlatformAndroid {
		sendPath = relayFcmSendPath
		payload = pushRelayFcmRequest{
			Token:          token,
			Mode:           "private_alert",
			ServerDeviceID: device.ServerDeviceID,
			DeliveryID:     deliveryID,
			CollapseID:     &collapseID,
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

func (s *pushSender) renewRelayCapability(ctx context.Context, expiredKey string) (PushRelayCredential, error) {
	s.renewMu.Lock()
	defer s.renewMu.Unlock()

	// Another sender may have renewed while this goroutine waited for the lock.
	current := LoadPushRelayCredential(ctx, s.settings)
	if current.APIKey != "" && current.APIKey != expiredKey {
		return current, nil
	}
	relayURL, err := NormalizePushRelayURL(current.RelayURL, s.developmentRelayURL)
	if err != nil {
		return PushRelayCredential{}, err
	}
	current.RelayURL = relayURL
	result, err := RenewRelayCredential(ctx, s.settings, s.client, current)
	return result.Credential, err
}

func (s *pushSender) markReregistrationRequired(ctx context.Context, failed PushRelayCredential) error {
	s.renewMu.Lock()
	defer s.renewMu.Unlock()
	current := LoadPushRelayCredential(ctx, s.settings)
	if current.APIKey != failed.APIKey {
		return nil
	}
	return MarkRelayReregistrationRequired(ctx, s.settings, current)
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
	if s.Settings.PushRelayAPIKey(ctx) == "" {
		return nil, ErrPushDeliveryUnavailable
	}
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

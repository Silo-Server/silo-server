package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicevents "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/events"

	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// pluginSendTimeout bounds one SendNotification call. The SDK documents it.
const pluginSendTimeout = 15 * time.Second

// maxPluginRetryAfter caps the wait a plugin may ask for between attempts.
const maxPluginRetryAfter = 6 * time.Hour

// Outcomes of one send, as the sender acts on them.
type pluginSendOutcome int

const (
	pluginSendDelivered pluginSendOutcome = iota
	// pluginSendRetry is a temporary failure: try again on the schedule.
	pluginSendRetry
	// pluginSendFailed drops this message; the destination still works.
	pluginSendFailed
	// pluginSendInvalid drops the message and disables the destination.
	pluginSendInvalid
)

type pluginSendResult struct {
	outcome    pluginSendOutcome
	message    string
	retryAfter time.Duration
	duration   time.Duration
	// unavailable marks a send no plugin took: the kind's plugin is off,
	// removed, or couldn't be reached. The destination isn't at fault.
	unavailable bool
}

// pluginSendResultOf maps a plugin's answer onto what the sender does. A
// gRPC error, a timeout, or an unreachable plugin is temporary. An empty
// response acknowledges the message. An outcome from a newer SDK that this
// host doesn't know drops the message without touching the destination,
// which can neither loop nor lose a working destination. The plugin's
// message is stored and shown to the owner, so credential assignments in it
// are masked.
func pluginSendResultOf(resp *pluginv1.SendNotificationResponse, err error) pluginSendResult {
	if err != nil {
		if errors.Is(err, ErrPluginChannelUnavailable) {
			return pluginSendResult{outcome: pluginSendRetry, message: "The plugin that provides this destination isn't available.", unavailable: true}
		}
		return pluginSendResult{outcome: pluginSendRetry, message: "The plugin didn't answer."}
	}
	result := pluginSendResult{message: logredact.SanitizeText(resp.GetMessage())}
	switch resp.GetOutcome() {
	case pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_UNSPECIFIED,
		pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_DELIVERED:
		result.outcome = pluginSendDelivered
	case pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_RETRY:
		result.outcome = pluginSendRetry
		if seconds := resp.GetRetryAfterSeconds(); seconds > 0 {
			result.retryAfter = min(time.Duration(seconds)*time.Second, maxPluginRetryAfter)
		}
	case pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_DESTINATION_INVALID:
		result.outcome = pluginSendInvalid
	default:
		result.outcome = pluginSendFailed
	}
	return result
}

// pluginChannelHostBox lets the host be wired after NewSystem.
type pluginChannelHostBox struct{ host PluginChannelHost }

// pluginDestinationSender delivers claimed attempts through the plugin that
// provides each destination's kind. The post-commit dispatcher and the retry
// sweep share it, so both apply the same retry and disable rules.
type pluginDestinationSender struct {
	repo       *PluginDestinationRepository
	deliveries *DeliveryRepository
	cipher     *secret.Cipher
	hostBox    atomic.Pointer[pluginChannelHostBox]
	// Wired by NewSystem after construction.
	operational func(ctx context.Context, delivery Delivery, opts OperationalDispatch) (*InsertedDelivery, error)
	posterURL   func(ctx context.Context, posterPath, posterSourcePath string) string
	linkBase    func(ctx context.Context) string
	payload     func(ctx context.Context, row DeliveryRow) DeliveryRowPayload
	logger      *slog.Logger
}

func newPluginDestinationSender(repo *PluginDestinationRepository, deliveries *DeliveryRepository, cipher *secret.Cipher) *pluginDestinationSender {
	return &pluginDestinationSender{
		repo:       repo,
		deliveries: deliveries,
		cipher:     cipher,
		logger:     slog.Default().With("component", "notifications.plugin_destinations"),
	}
}

func (s *pluginDestinationSender) host() PluginChannelHost {
	if box := s.hostBox.Load(); box != nil {
		return box.host
	}
	return nil
}

func pluginDestinationConfigAAD(id string) string {
	return "notification_plugin_destination:" + id + ":config"
}

func (s *pluginDestinationSender) decryptConfig(d *PluginDestination) (map[string]any, error) {
	plaintext, err := s.cipher.Decrypt(d.ConfigCiphertext, pluginDestinationConfigAAD(d.ID))
	if err != nil {
		return nil, err
	}
	config := map[string]any{}
	if err := json.Unmarshal([]byte(plaintext), &config); err != nil {
		return nil, err
	}
	return config, nil
}

func (s *pluginDestinationSender) encryptConfig(id string, config map[string]any) (string, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	if len(raw) > maxPluginDestinationConfigBytes {
		return "", fmt.Errorf("%w: settings are larger than %d bytes", ErrPluginDestinationInvalid, maxPluginDestinationConfigBytes)
	}
	return s.cipher.Encrypt(string(raw), pluginDestinationConfigAAD(id))
}

// processAttempt delivers one claimed attempt and records the outcome.
func (s *pluginDestinationSender) processAttempt(ctx context.Context, attempt DeliveryAttempt) {
	host := s.host()
	if host == nil {
		// Plugins aren't wired on this server yet. The claim's lease runs
		// out and the sweep tries again.
		return
	}
	d, err := s.repo.getByIDUnscoped(ctx, attempt.TargetID)
	if err != nil {
		return
	}
	attemptNumber := attempt.AttemptNumber + 1
	// finalize records the outcome while this server's claim still holds.
	// Bookkeeping follows only when it did: after the lease ran out another
	// server owns the attempt and its result stands.
	finalize := func(outcome, message string, next *time.Time) bool {
		applied, err := s.repo.FinalizeAttempt(ctx, attempt, outcome, attemptNumber, message, next)
		if err != nil {
			s.logger.WarnContext(ctx, "plugin destination attempt finalize failed", "attempt_id", attempt.ID, "error", err)
		}
		return applied
	}
	if d == nil || !d.Enabled {
		finalize(WebhookOutcomeFailed, "destination deleted or turned off", nil)
		return
	}
	row, err := s.deliveries.GetRowByID(ctx, attempt.NotificationDeliveryID)
	if err != nil {
		// A database blip: leave the claim to expire instead of failing the
		// delivery for good.
		return
	}
	if row == nil || row.UserID != d.UserID || row.ProfileID != d.ProfileID {
		finalize(WebhookOutcomeFailed, "delivery doesn't belong to this destination", nil)
		return
	}

	result := s.send(ctx, host, d, *row, attemptNumber, false)
	switch result.outcome {
	case pluginSendDelivered:
		if finalize(WebhookOutcomeDelivered, "", nil) {
			if err := s.repo.RecordSuccess(ctx, d.ID); err != nil {
				s.logger.WarnContext(ctx, "plugin destination success bookkeeping failed", "destination_id", d.ID, "error", err)
			}
		}
		return
	case pluginSendFailed:
		if finalize(WebhookOutcomeFailed, result.message, nil) {
			s.recordFailure(ctx, d, result)
		}
		return
	case pluginSendInvalid:
		if finalize(WebhookOutcomeFailed, result.message, nil) {
			s.recordFailure(ctx, d, result)
			s.disable(ctx, d, "The destination's settings stopped working", result.message)
		}
		return
	}

	delay, more := webhookRetryDelay(attemptNumber)
	if more {
		if result.retryAfter > 0 {
			delay = result.retryAfter
		}
		next := time.Now().Add(delay)
		if finalize(WebhookOutcomeRetrying, result.message, &next) {
			s.recordFailure(ctx, d, result)
		}
		return
	}
	// The retry schedule ran out: about a day of failures in a row. When no
	// plugin took the last send, the message is dropped but the destination
	// stays enabled, so it resumes once its plugin is back.
	if result.unavailable {
		if finalize(WebhookOutcomeFailed, result.message, nil) {
			s.recordFailure(ctx, d, result)
		}
		return
	}
	if finalize(WebhookOutcomeAutoDisabled, result.message, nil) {
		s.recordFailure(ctx, d, result)
		s.disable(ctx, d, "Deliveries failed for 24 hours", result.message)
	}
}

func (s *pluginDestinationSender) recordFailure(ctx context.Context, d *PluginDestination, result pluginSendResult) {
	if err := s.repo.RecordFailure(ctx, d.ID, result.message); err != nil {
		s.logger.WarnContext(ctx, "plugin destination failure bookkeeping failed", "destination_id", d.ID, "error", err)
	}
}

// send renders row for the destination and asks its plugin to deliver it.
func (s *pluginDestinationSender) send(ctx context.Context, host PluginChannelHost, d *PluginDestination, row DeliveryRow, attempt int, test bool) pluginSendResult {
	started := time.Now()
	configMap, err := s.decryptConfig(d)
	if err != nil {
		return pluginSendResult{outcome: pluginSendInvalid, message: "The stored settings can't be read. Edit the destination to save them again."}
	}
	config, err := structpb.NewStruct(configMap)
	if err != nil {
		return pluginSendResult{outcome: pluginSendInvalid, message: "The stored settings can't be sent to the plugin."}
	}
	message := s.message(ctx, row, test)
	callCtx, cancel := context.WithTimeout(ctx, pluginSendTimeout)
	defer cancel()
	resp, err := host.SendNotification(callCtx, d.Ref(), &pluginv1.SendNotificationRequest{
		DestinationId: d.ID,
		Config:        config,
		Message:       message,
		DeliveryKey:   row.ID + ":" + d.ID,
		Attempt:       int32(attempt), //nolint:gosec // G115: at most webhookMaxAttempts
		Test:          test,
		CapabilityId:  d.CapabilityID,
	})
	result := pluginSendResultOf(resp, err)
	result.duration = time.Since(started)
	if err != nil && !errors.Is(err, ErrPluginChannelUnavailable) && ctx.Err() == nil {
		s.logger.WarnContext(ctx, "plugin destination send failed", "destination_id", d.ID, "plugin_id", d.PluginID, "error", logredact.SanitizeText(err.Error()))
	}
	return result
}

// message renders a delivery for a plugin: the same title, body and link a
// native push shows, an absolute link when the server has a public URL, a
// poster when the admin lets posters leave the server, and the details in
// the event catalog's Notification shape.
func (s *pluginDestinationSender) message(ctx context.Context, row DeliveryRow, test bool) *pluginv1.NotificationMessage {
	display := BuildNotificationDisplay(row)
	message := &pluginv1.NotificationMessage{
		Type:      row.Type,
		Title:     display.Title,
		Body:      display.Body,
		CreatedAt: timestamppb.New(row.CreatedAt),
	}
	if s.linkBase != nil {
		if base := strings.TrimRight(s.linkBase(ctx), "/"); base != "" {
			message.Url = base + display.URL
		}
	}
	if s.posterURL != nil {
		message.ImageUrl = s.posterURL(ctx, row.PosterPath, row.PosterSourcePath)
	}
	if test {
		message.Type = "test"
		return message
	}
	message.Details = s.details(ctx, row)
	return message
}

// details is the delivery in the public events.Notification shape, so a
// plugin sees the same fields here as in a notification.created event.
func (s *pluginDestinationSender) details(ctx context.Context, row DeliveryRow) *structpb.Struct {
	payload := PayloadForRow(row)
	if s.payload != nil {
		payload = s.payload(ctx, row)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	if raw, err = publicevents.Project(publicevents.NotificationCreated, raw); err != nil {
		return nil
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	// The delivery payload names only the profile; profile IDs repeat
	// across accounts.
	fields["user_id"] = row.UserID
	details, err := structpb.NewStruct(fields)
	if err != nil {
		return nil
	}
	return details
}

// disable turns the destination off and posts an in-app notice so the
// failure isn't silent. The notice reaches no webhook or plugin destination.
func (s *pluginDestinationSender) disable(ctx context.Context, d *PluginDestination, reason, lastError string) {
	full := reason
	if lastError != "" {
		full = fmt.Sprintf("%s (last error: %s)", reason, lastError)
	}
	disabled, err := s.repo.Disable(ctx, d.ID, full, d.ConfigCiphertext)
	if err != nil {
		s.logger.ErrorContext(ctx, "plugin destination auto-disable failed", "destination_id", d.ID, "error", err)
		return
	}
	if !disabled {
		// Its owner changed the settings, or turned it off, during the send.
		return
	}
	s.logger.WarnContext(ctx, "plugin destination auto-disabled", "destination_id", d.ID, "plugin_id", d.PluginID, "reason", full)
	if s.operational == nil {
		return
	}
	flags, err := json.Marshal(map[string]any{"destination_id": d.ID, "destination_name": d.Name})
	if err != nil {
		return
	}
	notice := Delivery{
		ID:          ulid.Make().String(),
		UserID:      d.UserID,
		ProfileID:   d.ProfileID,
		Type:        DeliveryTypePluginDestinationAutoDisabled,
		ReasonFlags: flags,
	}
	if _, err := s.operational(ctx, notice, OperationalDispatch{}); err != nil {
		s.logger.WarnContext(ctx, "plugin destination auto-disable notice failed", "destination_id", d.ID, "error", err)
	}
}

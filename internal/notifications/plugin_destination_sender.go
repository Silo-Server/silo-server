package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicevents "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/events"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// pluginSendTimeout bounds one SendNotification call. The SDK documents it.
const pluginSendTimeout = 15 * time.Second

// pluginConnectTimeout bounds reaching a delivery's plugin, starting it when
// needed. A cold start can take longer; the start goes on without the
// caller, and the attempt retries on the schedule. With pluginSendTimeout it
// keeps a whole claim inside its lease (pluginDestinationClaimLimit).
const pluginConnectTimeout = 10 * time.Second

// pluginClaimMargin is lease left over a send's timeout before it starts.
const pluginClaimMargin = 5 * time.Second

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
	// unavailable marks a send no plugin answered: the channel's plugin is
	// off, removed or unreachable, its process exited during the call, or
	// the call ran out of time. The destination isn't at fault.
	unavailable bool
	// claimLost marks a send skipped because this server's claim on the
	// attempt ran out (or nearly) before it started: another server may own
	// the attempt, so nothing is sent or recorded.
	claimLost bool
}

// pluginSendResultOf maps a plugin's answer onto what the sender does. A
// gRPC error, a timeout, or an unreachable plugin is temporary; a transport
// failure also marks the plugin unavailable (pluginTransportFailure). An empty
// response acknowledges the message. An outcome from a newer SDK that this
// host doesn't know drops the message without touching the destination,
// which can neither loop nor lose a working destination. The plugin's
// message is returned as written: send masks it before it is stored, shown
// or logged.
func pluginSendResultOf(resp *pluginv1.SendNotificationResponse, err error) pluginSendResult {
	if err != nil {
		switch {
		case errors.Is(err, ErrPluginChannelUnavailable):
			return pluginSendResult{outcome: pluginSendRetry, message: "The plugin that provides this destination isn't available.", unavailable: true}
		case pluginTransportFailure(err):
			return pluginSendResult{outcome: pluginSendRetry, message: "The plugin didn't answer.", unavailable: true}
		}
		return pluginSendResult{outcome: pluginSendRetry, message: "The plugin couldn't send the message."}
	}
	result := pluginSendResult{message: resp.GetMessage()}
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

// pluginTransportFailure reports whether a call's error means no plugin
// answered it: the connection failed or closed (the process exited), the
// plugin was unavailable, or the call ran out of time or was canceled. An
// error status the plugin itself returned is not one.
func pluginTransportFailure(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	st, ok := status.FromError(err)
	if !ok {
		return true
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled:
		return true
	}
	return false
}

// pluginChannelHostBox lets the host be wired after NewSystem.
type pluginChannelHostBox struct{ host PluginChannelHost }

// pluginDestinationSender delivers claimed attempts through the plugin that
// provides each destination's channel. The post-commit dispatcher and the retry
// sweep share it, so both apply the same retry and disable rules.
type pluginDestinationSender struct {
	repo       *PluginDestinationRepository
	deliveries *DeliveryRepository
	cipher     *secret.Cipher
	hostBox    atomic.Pointer[pluginChannelHostBox]
	// Wired by NewSystem after construction. insertNotice writes the
	// auto-disable notice in the transaction that disables the
	// destination; dispatchNotice sends it to live clients after commit.
	insertNotice   func(ctx context.Context, tx pgx.Tx, notice Delivery) (*InsertedDelivery, error)
	dispatchNotice func(ctx context.Context, row InsertedDelivery)
	posterURL      func(ctx context.Context, posterPath, posterSourcePath string) string
	linkBase       func(ctx context.Context) string
	payload        func(ctx context.Context, row DeliveryRow) DeliveryRowPayload
	logger         *slog.Logger
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

func (s *pluginDestinationSender) decryptConfig(d *PluginDestination) (pluginSettings, error) {
	plaintext, err := s.cipher.Decrypt(d.ConfigCiphertext, pluginDestinationConfigAAD(d.ID))
	if err != nil {
		return pluginSettings{}, err
	}
	return decodePluginSettings([]byte(plaintext))
}

func (s *pluginDestinationSender) encryptConfig(id string, config pluginSettings) (string, error) {
	if config.Values == nil {
		config.Values = map[string]any{}
	}
	if config.Secret == nil {
		config.Secret = []string{}
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	if len(raw) > maxPluginDestinationConfigBytes {
		return "", fmt.Errorf("%w: settings are larger than %d bytes", ErrPluginDestinationInvalid, maxPluginDestinationConfigBytes)
	}
	return s.cipher.Encrypt(string(raw), pluginDestinationConfigAAD(id))
}

// enqueueable returns the profiles' enabled destinations that can take a
// delivery now, read inside the delivery's transaction. A destination whose
// channel no enabled plugin provides gets nothing: its plugin is off or
// removed, and queued attempts would only retry for a day. Availability is
// read through tx too, so it is current and needs no second connection.
// Nothing is enqueued before the plugin runtime is wired.
func (s *pluginDestinationSender) enqueueable(ctx context.Context, tx pgx.Tx, profiles []profileKey) (map[profileKey][]PluginDestination, error) {
	host := s.host()
	if host == nil {
		return nil, nil
	}
	byProfile, err := s.repo.ListEnabledByProfiles(ctx, tx, profiles)
	if err != nil || len(byProfile) == 0 {
		return byProfile, err
	}
	refs, err := host.AvailableChannels(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("list available plugin channels: %w", err)
	}
	available := make(map[PluginChannelRef]struct{}, len(refs))
	for _, ref := range refs {
		available[ref] = struct{}{}
	}
	for key, destinations := range byProfile {
		kept := destinations[:0]
		for _, d := range destinations {
			if _, ok := available[d.Ref()]; ok {
				kept = append(kept, d)
			}
		}
		byProfile[key] = kept
	}
	return byProfile, nil
}

// pluginAttemptFinish is everything one attempt's outcome changes,
// committed together by finish.
type pluginAttemptFinish struct {
	outcome string
	message string
	next    *time.Time
	// success clears the failure streak; failure extends it.
	success bool
	failure bool
	// disableReason, when set, turns the destination off and posts a notice.
	disableReason string
	// quietSince, when set, disables only a destination that had no
	// successful send and no owner edit since then, when the failing
	// message was queued: the retry schedule ran out. The attempt's outcome
	// is auto_disabled when it disabled the destination and failed when not.
	quietSince *time.Time
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
	if d == nil || !d.Enabled {
		// A deleted destination's attempts cascade; this closes one that
		// survived, and a turned-off destination's.
		s.finish(ctx, attempt, d, pluginAttemptFinish{outcome: WebhookOutcomeFailed, message: "destination deleted or turned off"})
		return
	}
	row, err := s.deliveries.GetRowByID(ctx, attempt.NotificationDeliveryID)
	if err != nil {
		// A database blip: leave the claim to expire instead of failing the
		// delivery for good.
		return
	}
	if row == nil || row.UserID != d.UserID || row.ProfileID != d.ProfileID {
		s.finish(ctx, attempt, d, pluginAttemptFinish{outcome: WebhookOutcomeFailed, message: "delivery doesn't belong to this destination"})
		return
	}

	result := s.send(ctx, host, d, *row, &attempt)
	if result.claimLost {
		return
	}
	failed := pluginAttemptFinish{outcome: WebhookOutcomeFailed, message: result.message, failure: true}
	switch result.outcome {
	case pluginSendDelivered:
		s.finish(ctx, attempt, d, pluginAttemptFinish{outcome: WebhookOutcomeDelivered, success: true})
		return
	case pluginSendFailed:
		s.finish(ctx, attempt, d, failed)
		return
	case pluginSendInvalid:
		failed.disableReason = "The destination's settings stopped working"
		s.finish(ctx, attempt, d, failed)
		return
	}

	delay, more := webhookRetryDelay(attempt.AttemptNumber + 1)
	if more {
		// retry_after_seconds is a minimum wait: it can push the next
		// attempt later than the schedule, never earlier.
		next := time.Now().Add(max(delay, result.retryAfter))
		s.finish(ctx, attempt, d, pluginAttemptFinish{outcome: WebhookOutcomeRetrying, message: result.message, next: &next, failure: true})
		return
	}
	// The retry schedule ran out: about a day of failures for this message.
	// The destination is disabled only when nothing reached it since the
	// message was queued and its owner hasn't edited it since; otherwise
	// this one message is dropped. When no plugin answered the last send,
	// the message is dropped and the destination stays enabled, so it
	// resumes once its plugin is back.
	if !result.unavailable {
		queued := row.CreatedAt
		failed.disableReason = "Deliveries failed for 24 hours"
		failed.quietSince = &queued
	}
	s.finish(ctx, attempt, d, failed)
}

// finish records an attempt's outcome, the destination's bookkeeping, and
// an auto-disable with its notice in one transaction, so a server that dies
// part way leaves none of it: the claim then expires and the attempt runs
// again. Nothing applies when the claim no longer holds: another server owns
// the attempt and its result stands. The notice reaches live clients after
// commit. d is nil for a deleted destination.
func (s *pluginDestinationSender) finish(ctx context.Context, attempt DeliveryAttempt, d *PluginDestination, f pluginAttemptFinish) {
	notice, disabled, err := s.commitFinish(ctx, attempt, d, f)
	if err != nil {
		s.logger.WarnContext(ctx, "plugin destination attempt finalize failed", "attempt_id", attempt.ID, "error", err)
		return
	}
	if disabled {
		s.logger.WarnContext(ctx, "plugin destination auto-disabled", "destination_id", d.ID, "plugin_id", d.PluginID, "reason", disableText(f))
	}
	if notice != nil && s.dispatchNotice != nil {
		s.dispatchNotice(ctx, *notice)
	}
}

func (s *pluginDestinationSender) commitFinish(ctx context.Context, attempt DeliveryAttempt, d *PluginDestination, f pluginAttemptFinish) (*InsertedDelivery, bool, error) {
	tx, err := s.repo.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// The destination is locked before its attempt, in the order a delete
	// locks them (the row, then its attempts by cascade), so the two can't
	// deadlock.
	if d != nil {
		if err := s.repo.lockForFinish(ctx, tx, d.ID); err != nil {
			return nil, false, err
		}
	}
	// Disabling applies only while the destination still has the settings
	// the send used. It runs first so the attempt records whether it did;
	// a finalize that no longer holds the claim rolls it back.
	disabled := false
	if d != nil && f.disableReason != "" {
		if disabled, err = s.repo.Disable(ctx, tx, d.ID, disableText(f), d.ConfigCiphertext, f.quietSince); err != nil {
			return nil, false, err
		}
		if f.quietSince != nil && disabled {
			f.outcome = WebhookOutcomeAutoDisabled
		}
	}
	applied, err := s.repo.FinalizeAttempt(ctx, tx, attempt, f.outcome, attempt.AttemptNumber+1, f.message, f.next)
	if err != nil || !applied {
		return nil, false, err
	}
	if d == nil {
		return nil, false, tx.Commit(ctx)
	}
	if f.success {
		if err := s.repo.RecordSuccess(ctx, tx, d.ID); err != nil {
			return nil, false, err
		}
	}
	// Failure bookkeeping applies only while the destination still has the
	// settings the send used: an owner who saved new ones during the send
	// starts clean.
	if f.failure {
		if err := s.repo.RecordFailure(ctx, tx, d.ID, f.message, d.ConfigCiphertext); err != nil {
			return nil, false, err
		}
	}
	var notice *InsertedDelivery
	if disabled && s.insertNotice != nil {
		if notice, err = s.insertNotice(ctx, tx, autoDisableNotice(d)); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return notice, disabled, nil
}

func disableText(f pluginAttemptFinish) string {
	if f.message == "" {
		return f.disableReason
	}
	return fmt.Sprintf("%s (last error: %s)", f.disableReason, f.message)
}

// autoDisableNotice is the in-app notice that tells the owner a destination
// was turned off. It reaches no webhook or plugin destination.
func autoDisableNotice(d *PluginDestination) Delivery {
	flags, _ := json.Marshal(map[string]string{"destination_id": d.ID, "destination_name": d.Name})
	return Delivery{
		ID:          ulid.Make().String(),
		UserID:      d.UserID,
		ProfileID:   d.ProfileID,
		Type:        DeliveryTypePluginDestinationAutoDisabled,
		ReasonFlags: flags,
	}
}

// send renders row for the destination and asks its plugin to deliver it.
// claimed is the attempt this send is for, nil for a test message: just
// before the call the claim must still hold with a send's time left on it,
// or nothing is sent. Every message the result carries is masked against the
// destination's secret settings.
func (s *pluginDestinationSender) send(ctx context.Context, host PluginChannelHost, d *PluginDestination, row DeliveryRow, claimed *DeliveryAttempt) pluginSendResult {
	started := time.Now()
	stored, err := s.decryptConfig(d)
	if err != nil {
		// Like a webhook URL this server can't decrypt: a temporary
		// failure, since a server with the right key can still send.
		return pluginSendResult{outcome: pluginSendRetry, message: "The stored settings can't be read on this server."}
	}
	config, err := structpb.NewStruct(stored.Values)
	if err != nil {
		return pluginSendResult{outcome: pluginSendInvalid, message: "The stored settings can't be sent to the plugin."}
	}
	test := claimed == nil
	attemptNumber := 1
	if claimed != nil {
		attemptNumber = claimed.AttemptNumber + 1
	}
	message := s.message(ctx, row, test)

	connectCtx, cancelConnect := context.WithTimeout(ctx, pluginConnectTimeout)
	channel, client, err := host.Connect(connectCtx, d.Ref())
	cancelConnect()
	if err != nil {
		result := pluginSendResultOf(nil, err)
		result.duration = time.Since(started)
		s.logSendError(ctx, d, nil, stored, err)
		return result
	}
	if claimed != nil {
		held, err := s.repo.ClaimHeld(ctx, *claimed, pluginSendTimeout+pluginClaimMargin)
		if err != nil || !held {
			return pluginSendResult{claimLost: true}
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, pluginSendTimeout)
	defer cancel()
	resp, err := client.SendNotification(callCtx, &pluginv1.SendNotificationRequest{
		DestinationId: d.ID,
		Config:        config,
		Message:       message,
		DeliveryKey:   row.ID + ":" + d.ID,
		Attempt:       int32(attemptNumber),
		Test:          test,
		CapabilityId:  d.CapabilityID,
	})
	result := pluginSendResultOf(resp, err)
	result.message = redactPluginText(result.message, channel, stored)
	result.duration = time.Since(started)
	if err != nil {
		s.logSendError(ctx, d, channel, stored, err)
	}
	return result
}

// logSendError logs a failed call, masked like a stored message. An
// unavailable plugin and a canceled caller aren't worth a warning.
func (s *pluginDestinationSender) logSendError(ctx context.Context, d *PluginDestination, channel *PluginChannel, config pluginSettings, err error) {
	if errors.Is(err, ErrPluginChannelUnavailable) || ctx.Err() != nil {
		return
	}
	s.logger.WarnContext(ctx, "plugin destination send failed", "destination_id", d.ID, "plugin_id", d.PluginID,
		"error", redactPluginText(err.Error(), channel, config))
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
		// A request update keeps its poster in its reason flags;
		// PayloadForRow falls back to it.
		message.ImageUrl = s.posterURL(ctx, PayloadForRow(row).PosterPath, row.PosterSourcePath)
	}
	if test {
		message.Type = "test"
		return message
	}
	message.Details = s.details(ctx, row, message.ImageUrl)
	return message
}

// details is the delivery in the public events.Notification shape, so a
// plugin sees the same fields here as in a notification.created event.
// Every identifier is a string, as the catalog requires. poster_url is the
// message's image URL, which follows the administrator's poster policy; the
// delivery payload's own poster URL doesn't.
func (s *pluginDestinationSender) details(ctx context.Context, row DeliveryRow, posterURL string) *structpb.Struct {
	payload := PayloadForRow(row)
	if s.payload != nil {
		payload = s.payload(ctx, row)
	}
	notification := publicevents.Notification{
		ID:   payload.ID,
		Type: payload.Type,
		// The delivery payload names only the profile; profile IDs repeat
		// across accounts.
		UserID:        strconv.Itoa(row.UserID),
		ProfileID:     payload.ProfileID,
		SeriesID:      payload.SeriesID,
		EpisodeID:     payload.EpisodeID,
		SeriesTitle:   payload.SeriesTitle,
		EpisodeTitle:  payload.EpisodeTitle,
		SeasonNumber:  payload.SeasonNumber,
		EpisodeNumber: payload.EpisodeNumber,
		PosterPath:    payload.PosterPath,
		PosterURL:     posterURL,
		CreatedAt:     payload.CreatedAt,
	}
	if payload.LibraryID != nil {
		libraryID := strconv.Itoa(*payload.LibraryID)
		notification.LibraryID = &libraryID
	}
	// The catalog's typed reason drops any field of the stored flags it
	// doesn't declare, as for a notification.created event.
	if len(payload.ReasonFlags) > 0 {
		var reason publicevents.NotificationReason
		if err := json.Unmarshal(payload.ReasonFlags, &reason); err == nil {
			notification.ReasonFlags = &reason
		}
	}
	raw, err := json.Marshal(notification)
	if err != nil {
		return nil
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	details, err := structpb.NewStruct(fields)
	if err != nil {
		return nil
	}
	return details
}

package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Account-channel modes shared by every account-level notification channel
// (email, Discord). These channels are account-scoped: one setting covers
// every profile on the login account, and the sweep collapses cross-profile
// duplicates into a single send.
const (
	ChannelModeOff         = "off"
	ChannelModePerEpisode  = "per_episode"
	ChannelModeDailyDigest = "daily_digest"
	// ChannelModePerEpisodeAndDigest sends per-episode all day and, at the
	// digest hour, a digest recapping everything since the previous digest —
	// including items already sent individually.
	ChannelModePerEpisodeAndDigest = "per_episode_and_digest"
)

// ValidChannelMode reports whether mode is a recognized account-channel mode.
func ValidChannelMode(mode string) bool {
	switch mode {
	case ChannelModeOff, ChannelModePerEpisode, ChannelModeDailyDigest,
		ChannelModePerEpisodeAndDigest:
		return true
	}
	return false
}

// ModeIncludesPerEpisode reports whether the mode performs per-episode sends
// and is therefore subject to the admin per-episode allowance.
func ModeIncludesPerEpisode(mode string) bool {
	return mode == ChannelModePerEpisode || mode == ChannelModePerEpisodeAndDigest
}

const (
	channelPollInterval = time.Minute
	// channelNudgeDelay coalesces the per-row dispatch nudges of one fanout
	// batch (all rows commit before the first nudge fires) into one pass.
	channelNudgeDelay = 2 * time.Second
	// channelFetchLimit bounds one send's worth of watermark progress; the
	// next pass drains the remainder.
	channelFetchLimit = 200
	// channelMaxFailuresPerPass stops a pass early when sends keep failing —
	// transport trouble is almost always global, not per-recipient.
	channelMaxFailuresPerPass = 3

	channelFailureBackoffBase = time.Minute
	channelFailureBackoffMax  = 6 * time.Hour
)

// errChannelUnavailable aborts a sweep pass entirely: the channel's transport
// is unconfigured or globally down, so nothing else will send either. The
// failing account is not penalized with backoff.
var errChannelUnavailable = errors.New("notification channel unavailable")

// effectiveChannelMode coerces per-episode modes to the daily digest when the
// admin has disallowed per-episode sends, instead of silencing those accounts.
func effectiveChannelMode(mode string, allowPerEpisode bool) string {
	if ModeIncludesPerEpisode(mode) && !allowPerEpisode {
		return ChannelModeDailyDigest
	}
	return mode
}

// cursorLess orders cursors the way the delivery queries do: by
// (created_at, id).
func cursorLess(a, b Cursor) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

// channelDigestDue reports whether a daily digest should go out: today's send
// time (digestHour, local) has passed and no digest was stamped since.
func channelDigestDue(now time.Time, digestHour int, lastDigestAt *time.Time) bool {
	todaySend := time.Date(now.Year(), now.Month(), now.Day(), digestHour, 0, 0, 0, now.Location())
	if now.Before(todaySend) {
		return false
	}
	return lastDigestAt == nil || lastDigestAt.Before(todaySend)
}

// channelRetryEligible applies exponential backoff after failed sends:
// 1m, 2m, 4m, ... capped at channelFailureBackoffMax.
func channelRetryEligible(now time.Time, lastAttemptAt *time.Time, consecutiveFailures int) bool {
	if consecutiveFailures <= 0 || lastAttemptAt == nil {
		return true
	}
	backoff := channelFailureBackoffBase << min(consecutiveFailures-1, 30)
	if backoff > channelFailureBackoffMax || backoff <= 0 {
		backoff = channelFailureBackoffMax
	}
	return !now.Before(lastAttemptAt.Add(backoff))
}

// accountRecipient is the channel-agnostic sweep state for one account: the
// user-chosen mode plus the dispatch watermark and failure backoff counters.
// Channel-specific contact details (email address, Discord identity) stay
// inside the channel implementation.
type accountRecipient struct {
	UserID              int
	Mode                string
	WatermarkCreatedAt  time.Time
	WatermarkID         string
	LastDigestAt        *time.Time
	LastAttemptAt       *time.Time
	ConsecutiveFailures int
}

// accountChannel supplies the channel-specific pieces of the account
// watermark sweep: prefs-table access and the actual send. The engine owns
// the loop, eligibility, claim transaction, and watermark advancement.
type accountChannel interface {
	// name labels log lines.
	name() string
	// enabled gates a whole pass (kill switch + transport configured).
	enabled(ctx context.Context) bool
	// allowPerEpisode is the admin allowance for per-send mode.
	allowPerEpisode(ctx context.Context) bool
	// digestHour is the hour of day (0-23, server-local) for daily digests.
	digestHour(ctx context.Context) int
	// listRecipients returns every account with the channel switched on and a
	// usable destination. Disabled or deleted accounts must not appear.
	listRecipients(ctx context.Context) ([]accountRecipient, error)
	// claim locks the account's prefs row for one dispatch attempt with
	// FOR UPDATE SKIP LOCKED; (nil, nil) means another node holds the row.
	claim(ctx context.Context, tx pgx.Tx, userID int) (*accountRecipient, error)
	// markSent advances the watermark past everything the send covered and
	// resets failure backoff. digestAt is non-nil for digest sends.
	markSent(ctx context.Context, tx pgx.Tx, userID int, watermark Cursor, digestAt *time.Time) error
	// markFailure records a failed send for backoff; the watermark stays put
	// so the next eligible pass retries the same items.
	markFailure(ctx context.Context, tx pgx.Tx, userID int, sendErr error) error
	// send delivers one account's pending rows. It runs inside the claim
	// transaction; tx is for channel-state updates only (the engine owns
	// commit/rollback). Errors wrapping errChannelUnavailable abort the pass
	// without penalizing the account.
	send(ctx context.Context, tx pgx.Tx, userID int, mode string, rows []DeliveryRow) error
}

// accountChannelWorker drives one account-level channel. Unlike webhooks and
// web push it keeps no per-target outbox: deliveries already carry user_id,
// so a per-account watermark over notification_deliveries is the durable
// dispatch state. The watermark advances only after a successful send, and
// one send covers everything since the last one — which also collapses the
// duplicate rows an account gets when several of its profiles follow the
// same series.
type accountChannelWorker struct {
	pool       *pgxpool.Pool
	deliveries *DeliveryRepository
	channel    accountChannel
	logger     *slog.Logger
	nudge      chan struct{}
	now        func() time.Time
}

func newAccountChannelWorker(
	pool *pgxpool.Pool,
	deliveries *DeliveryRepository,
	channel accountChannel,
) *accountChannelWorker {
	return &accountChannelWorker{
		pool:       pool,
		deliveries: deliveries,
		channel:    channel,
		logger:     slog.Default().With("component", "notifications."+channel.name()),
		nudge:      make(chan struct{}, 1),
		now:        time.Now,
	}
}

// Nudge schedules a near-term pass so per-episode sends follow fanout within
// seconds instead of waiting for the next poll. Non-blocking.
func (w *accountChannelWorker) Nudge() {
	if w == nil {
		return
	}
	select {
	case w.nudge <- struct{}{}:
	default:
	}
}

// Run sweeps eligible accounts until ctx is canceled.
func (w *accountChannelWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(channelPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.nudge:
			select {
			case <-ctx.Done():
				return
			case <-time.After(channelNudgeDelay):
			}
		}
		if !w.channel.enabled(ctx) {
			continue
		}
		w.runPass(ctx)
	}
}

// runPass attempts one send per eligible account. Failures back off per
// account; the pass aborts entirely on errChannelUnavailable or after a few
// consecutive failures, since both indicate a global transport problem.
func (w *accountChannelWorker) runPass(ctx context.Context) {
	recipients, err := w.channel.listRecipients(ctx)
	if err != nil {
		w.logger.Error("channel pass: list recipients failed", "error", err)
		return
	}
	if len(recipients) == 0 {
		return
	}
	allowPerEpisode := w.channel.allowPerEpisode(ctx)
	digestHour := w.channel.digestHour(ctx)
	now := w.now()

	failures := 0
	for _, rec := range recipients {
		if ctx.Err() != nil || failures >= channelMaxFailuresPerPass {
			return
		}
		if !channelRetryEligible(now, rec.LastAttemptAt, rec.ConsecutiveFailures) {
			continue
		}
		mode := effectiveChannelMode(rec.Mode, allowPerEpisode)
		digestDue := channelDigestDue(now, digestHour, rec.LastDigestAt)
		switch mode {
		case ChannelModePerEpisode, ChannelModePerEpisodeAndDigest:
			if mode == ChannelModePerEpisodeAndDigest && digestDue {
				break // the digest leg has work regardless of pending rows
			}
			// Cheap pre-check so idle accounts don't open a claim
			// transaction every pass. A stale watermark only ever
			// produces a harmless extra claim.
			pending, err := w.deliveries.HasForUserSince(ctx, rec.UserID,
				Cursor{CreatedAt: rec.WatermarkCreatedAt, ID: rec.WatermarkID})
			if err != nil {
				w.logger.Warn("channel pass: pending check failed", "user_id", rec.UserID, "error", err)
				continue
			}
			if !pending {
				continue
			}
		case ChannelModeDailyDigest:
			if !digestDue {
				continue
			}
		default:
			continue
		}
		if err := w.processAccount(ctx, rec); err != nil {
			if errors.Is(err, errChannelUnavailable) {
				return // channel turned off mid-pass; nothing else will send either
			}
			failures++
			w.logger.Warn("channel send failed", "user_id", rec.UserID, "mode", mode, "error", err)
		}
	}
}

// processAccount sends one account's pending notifications under the prefs
// row lock. The send happens inside the claim transaction: the row lock is
// per-account and only contends with other nodes, and committing the
// watermark only after a successful send is what makes the channel durable.
func (w *accountChannelWorker) processAccount(ctx context.Context, rec accountRecipient) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin channel dispatch tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	claimed, err := w.channel.claim(ctx, tx, rec.UserID)
	if err != nil {
		return err
	}
	if claimed == nil {
		return nil // another node is handling this account
	}

	// Re-derive eligibility from the locked row: the pre-scan snapshot may
	// predate a user mode flip or another node's digest stamp.
	mode := effectiveChannelMode(claimed.Mode, w.channel.allowPerEpisode(ctx))
	digestDue := channelDigestDue(w.now(), w.channel.digestHour(ctx), claimed.LastDigestAt)

	// sendKind is the rendering the channel applies (per-episode alert vs
	// digest summary); for the combined mode it differs from the stored mode.
	sendKind := mode
	since := Cursor{CreatedAt: claimed.WatermarkCreatedAt, ID: claimed.WatermarkID}
	// fetchFrom is where this send reads rows from. Per-episode legs read
	// from the watermark (unsent rows); a combined-mode digest recaps the
	// whole window since the previous digest, which is usually behind the
	// watermark because its items already went out individually.
	fetchFrom := since
	var digestAt *time.Time

	switch mode {
	case ChannelModePerEpisode:
	case ChannelModeDailyDigest:
		if !digestDue {
			return nil
		}
		now := w.now()
		digestAt = &now
	case ChannelModePerEpisodeAndDigest:
		if digestDue {
			sendKind = ChannelModeDailyDigest
			now := w.now()
			digestAt = &now
			if claimed.LastDigestAt != nil {
				digestCursor := Cursor{CreatedAt: *claimed.LastDigestAt}
				if cursorLess(digestCursor, fetchFrom) {
					fetchFrom = digestCursor
				}
			}
		} else {
			sendKind = ChannelModePerEpisode
		}
	default:
		return nil
	}

	rows, err := w.deliveries.ListForUserSince(ctx, tx, rec.UserID, fetchFrom, channelFetchLimit)
	if err != nil {
		return err
	}

	if len(rows) == 0 {
		// Nothing new. Digests still stamp so eligibility stops re-checking
		// until tomorrow; the watermark needs no update.
		if digestAt != nil {
			if err := w.channel.markSent(ctx, tx, rec.UserID, since, digestAt); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		return nil
	}

	items := rows
	if mode == ChannelModeDailyDigest {
		// The digest-only mode reports what the user hasn't seen; rows
		// already read in another client are skipped but the watermark still
		// passes them. The combined mode's digest deliberately recaps
		// everything — its per-episode sends already covered the new rows.
		items = make([]DeliveryRow, 0, len(rows))
		for _, row := range rows {
			if row.ReadAt == nil {
				items = append(items, row)
			}
		}
	}

	last := rows[len(rows)-1]
	watermark := Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	if cursorLess(watermark, since) {
		// A combined-mode digest can read entirely behind the watermark;
		// the watermark only ever moves forward.
		watermark = since
	}

	if len(items) > 0 {
		if err := w.channel.send(ctx, tx, rec.UserID, sendKind, items); err != nil {
			if errors.Is(err, errChannelUnavailable) {
				return err
			}
			if markErr := w.channel.markFailure(ctx, tx, rec.UserID, err); markErr != nil {
				return errors.Join(err, markErr)
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return errors.Join(err, commitErr)
			}
			return err
		}
		w.logger.Info("notification sent",
			"user_id", rec.UserID, "mode", mode, "items", len(items))
	}

	if err := w.channel.markSent(ctx, tx, rec.UserID, watermark, digestAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// nudgeDispatcher plugs an account-channel worker into the MultiDispatcher: a
// new delivery just nudges the sweep, which reads everything since the
// watermark. No per-delivery state is kept, so dropped nudges cost only poll
// latency.
type nudgeDispatcher struct {
	worker *accountChannelWorker
}

func newNudgeDispatcher(worker *accountChannelWorker) *nudgeDispatcher {
	return &nudgeDispatcher{worker: worker}
}

// Dispatch implements Dispatcher.
func (d *nudgeDispatcher) Dispatch(_ context.Context, _ DeliveryRow) error {
	if d != nil {
		d.worker.Nudge()
	}
	return nil
}

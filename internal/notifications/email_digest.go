package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	emailPollInterval = time.Minute
	// emailNudgeDelay coalesces the per-row dispatch nudges of one fanout
	// batch (all rows commit before the first nudge fires) into one pass.
	emailNudgeDelay = 2 * time.Second
	// emailFetchLimit bounds one email's worth of watermark progress; the
	// next pass drains the remainder.
	emailFetchLimit = 200
	// emailMaxFailuresPerPass stops a pass early when sends keep failing —
	// SMTP trouble is almost always global, not per-recipient.
	emailMaxFailuresPerPass = 3

	emailFailureBackoffBase = time.Minute
	emailFailureBackoffMax  = 6 * time.Hour
)

// effectiveEmailMode coerces per-episode to the daily digest when the admin
// has disallowed per-episode email, instead of silencing those accounts.
func effectiveEmailMode(mode string, allowPerEpisode bool) string {
	if mode == EmailModePerEpisode && !allowPerEpisode {
		return EmailModeDailyDigest
	}
	return mode
}

// emailDigestDue reports whether a daily digest should go out: today's send
// time (digestHour, local) has passed and no digest was stamped since.
func emailDigestDue(now time.Time, digestHour int, lastDigestAt *time.Time) bool {
	todaySend := time.Date(now.Year(), now.Month(), now.Day(), digestHour, 0, 0, 0, now.Location())
	if now.Before(todaySend) {
		return false
	}
	return lastDigestAt == nil || lastDigestAt.Before(todaySend)
}

// emailRetryEligible applies exponential backoff after failed sends:
// 1m, 2m, 4m, ... capped at emailFailureBackoffMax.
func emailRetryEligible(now time.Time, lastAttemptAt *time.Time, consecutiveFailures int) bool {
	if consecutiveFailures <= 0 || lastAttemptAt == nil {
		return true
	}
	backoff := emailFailureBackoffBase << min(consecutiveFailures-1, 30)
	if backoff > emailFailureBackoffMax || backoff <= 0 {
		backoff = emailFailureBackoffMax
	}
	return !now.Before(lastAttemptAt.Add(backoff))
}

// EmailWorker delivers notification emails. Unlike webhooks and web push it
// keeps no per-target outbox: deliveries already carry user_id, so a per-user
// watermark over notification_deliveries is the durable dispatch state. The
// watermark advances only after a successful SMTP send, and one email covers
// everything since the last one — which also collapses the duplicate rows an
// account gets when several of its profiles follow the same series.
type EmailWorker struct {
	pool       *pgxpool.Pool
	deliveries *DeliveryRepository
	prefs      *EmailPrefsRepository
	settings   *Settings
	sender     mail.Sender
	logger     *slog.Logger
	nudge      chan struct{}
	now        func() time.Time
}

func newEmailWorker(
	pool *pgxpool.Pool,
	deliveries *DeliveryRepository,
	prefs *EmailPrefsRepository,
	settings *Settings,
	sender mail.Sender,
) *EmailWorker {
	return &EmailWorker{
		pool:       pool,
		deliveries: deliveries,
		prefs:      prefs,
		settings:   settings,
		sender:     sender,
		logger:     slog.Default().With("component", "notifications.email"),
		nudge:      make(chan struct{}, 1),
		now:        time.Now,
	}
}

// Nudge schedules a near-term pass so per-episode emails follow fanout within
// seconds instead of waiting for the next poll. Non-blocking.
func (w *EmailWorker) Nudge() {
	if w == nil {
		return
	}
	select {
	case w.nudge <- struct{}{}:
	default:
	}
}

// Run sweeps eligible accounts until ctx is canceled.
func (w *EmailWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(emailPollInterval)
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
			case <-time.After(emailNudgeDelay):
			}
		}
		if !w.settings.EmailEnabled(ctx) || !w.sender.Enabled(ctx) {
			continue
		}
		w.runPass(ctx)
	}
}

// runPass attempts one send per eligible account. Failures back off per
// account; the pass aborts entirely on ErrNotConfigured or after a few
// consecutive failures, since both indicate a global SMTP problem.
func (w *EmailWorker) runPass(ctx context.Context) {
	recipients, err := w.prefs.ListActiveRecipients(ctx)
	if err != nil {
		w.logger.Error("email pass: list recipients failed", "error", err)
		return
	}
	if len(recipients) == 0 {
		return
	}
	allowPerEpisode := w.settings.EmailAllowPerEpisode(ctx)
	digestHour := w.settings.EmailDigestHour(ctx)
	now := w.now()

	failures := 0
	for _, rec := range recipients {
		if ctx.Err() != nil || failures >= emailMaxFailuresPerPass {
			return
		}
		if !emailRetryEligible(now, rec.LastAttemptAt, rec.ConsecutiveFailures) {
			continue
		}
		mode := effectiveEmailMode(rec.Mode, allowPerEpisode)
		switch mode {
		case EmailModePerEpisode:
			// Cheap pre-check so idle accounts don't open a claim
			// transaction every pass. A stale watermark only ever
			// produces a harmless extra claim.
			pending, err := w.prefs.HasDeliveriesSince(ctx, rec.UserID,
				Cursor{CreatedAt: rec.WatermarkCreatedAt, ID: rec.WatermarkID})
			if err != nil {
				w.logger.Warn("email pass: pending check failed", "user_id", rec.UserID, "error", err)
				continue
			}
			if !pending {
				continue
			}
		case EmailModeDailyDigest:
			if !emailDigestDue(now, digestHour, rec.LastDigestAt) {
				continue
			}
		default:
			continue
		}
		if err := w.processAccount(ctx, rec); err != nil {
			if errors.Is(err, mail.ErrNotConfigured) {
				return // email turned off mid-pass; nothing else will send either
			}
			failures++
			w.logger.Warn("email send failed", "user_id", rec.UserID, "mode", mode, "error", err)
		}
	}
}

// processAccount sends one account's pending notifications under the prefs
// row lock. The SMTP send happens inside the claim transaction: the row lock
// is per-account and only contends with other nodes, and committing the
// watermark only after a successful send is what makes the channel durable.
func (w *EmailWorker) processAccount(ctx context.Context, rec emailRecipient) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin email dispatch tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	claimed, err := w.prefs.claimForUpdate(ctx, tx, rec.UserID)
	if err != nil {
		return err
	}
	if claimed == nil {
		return nil // another node is handling this account
	}

	// Re-derive eligibility from the locked row: the pre-scan snapshot may
	// predate a user mode flip or another node's digest stamp.
	mode := effectiveEmailMode(claimed.Mode, w.settings.EmailAllowPerEpisode(ctx))
	switch mode {
	case EmailModePerEpisode:
	case EmailModeDailyDigest:
		if !emailDigestDue(w.now(), w.settings.EmailDigestHour(ctx), claimed.LastDigestAt) {
			return nil
		}
	default:
		return nil
	}

	since := Cursor{CreatedAt: claimed.WatermarkCreatedAt, ID: claimed.WatermarkID}
	rows, err := w.deliveries.ListForUserSince(ctx, tx, rec.UserID, since, emailFetchLimit)
	if err != nil {
		return err
	}

	var digestAt *time.Time
	if mode == EmailModeDailyDigest {
		now := w.now()
		digestAt = &now
	}
	if len(rows) == 0 {
		// Nothing new. Digests still stamp so eligibility stops re-checking
		// until tomorrow; the watermark needs no update.
		if digestAt != nil {
			if err := w.prefs.markSent(ctx, tx, rec.UserID, since, digestAt); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		return nil
	}

	items := rows
	if mode == EmailModeDailyDigest {
		// The digest reports what the user hasn't seen; rows already read in
		// another client are skipped but the watermark still passes them.
		items = make([]DeliveryRow, 0, len(rows))
		for _, row := range rows {
			if row.ReadAt == nil {
				items = append(items, row)
			}
		}
	}

	last := rows[len(rows)-1]
	watermark := Cursor{CreatedAt: last.CreatedAt, ID: last.ID}

	if len(items) > 0 {
		content := composeNotificationEmail(mode, items, w.settings.EmailExternalURL(ctx))
		err = w.sender.Send(ctx, mail.Message{
			To:       []string{rec.Email},
			Subject:  content.Subject,
			TextBody: content.Text,
			HTMLBody: content.HTML,
		})
		if err != nil {
			if errors.Is(err, mail.ErrNotConfigured) {
				return err
			}
			if markErr := w.prefs.markFailure(ctx, tx, rec.UserID); markErr != nil {
				return errors.Join(err, markErr)
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return errors.Join(err, commitErr)
			}
			return err
		}
		w.logger.Info("notification email sent",
			"user_id", rec.UserID, "mode", mode, "items", len(items))
	}

	if err := w.prefs.markSent(ctx, tx, rec.UserID, watermark, digestAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Errors surfaced by SetEmailMode for the API layer to map to 4xx responses.
var (
	ErrEmailModeInvalid    = errors.New("invalid email notification mode")
	ErrEmailModeNotAllowed = errors.New("per-episode email is disabled by the administrator")
	ErrEmailNoAddress      = errors.New("account has no email address")
)

// EmailAvailable reports whether the email channel can deliver right now:
// a sender is wired, the kill switch is on, and SMTP is configured.
func (s *System) EmailAvailable(ctx context.Context) bool {
	return s != nil && s.emailWorker != nil &&
		s.Settings.EmailEnabled(ctx) && s.mailSender.Enabled(ctx)
}

// EmailMode returns the account's chosen email mode (off when never set).
func (s *System) EmailMode(ctx context.Context, userID int) (string, error) {
	if s == nil || s.EmailPrefs == nil {
		return EmailModeOff, nil
	}
	prefs, err := s.EmailPrefs.Get(ctx, userID)
	if err != nil {
		return "", err
	}
	return prefs.Mode, nil
}

// SetEmailMode validates and stores the account's email mode. Enabling
// requires an email address on the account and, for per-episode, the admin
// allowance.
func (s *System) SetEmailMode(ctx context.Context, userID int, mode string) error {
	if s == nil || s.EmailPrefs == nil {
		return ErrEmailModeInvalid
	}
	if !ValidEmailMode(mode) {
		return ErrEmailModeInvalid
	}
	if mode == EmailModePerEpisode && !s.Settings.EmailAllowPerEpisode(ctx) {
		return ErrEmailModeNotAllowed
	}
	if mode != EmailModeOff {
		var email string
		err := s.pool.QueryRow(ctx,
			`SELECT COALESCE(email, '') FROM users WHERE id = $1`, userID,
		).Scan(&email)
		if err != nil {
			return fmt.Errorf("look up account email: %w", err)
		}
		if email == "" {
			return ErrEmailNoAddress
		}
	}
	return s.EmailPrefs.SetMode(ctx, userID, mode)
}

// EmailDispatcher plugs the email worker into the MultiDispatcher: a new
// delivery just nudges the sweep, which reads everything since the watermark.
// No per-delivery state is kept, so dropped nudges cost only poll latency.
type EmailDispatcher struct {
	worker *EmailWorker
}

func newEmailDispatcher(worker *EmailWorker) *EmailDispatcher {
	return &EmailDispatcher{worker: worker}
}

// Dispatch implements Dispatcher.
func (d *EmailDispatcher) Dispatch(_ context.Context, _ DeliveryRow) error {
	if d != nil {
		d.worker.Nudge()
	}
	return nil
}

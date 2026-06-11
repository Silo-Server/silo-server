package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// emailChannel implements accountChannel over the shared SMTP core. The
// engine owns the sweep loop and watermark; this adapter only knows how to
// list/claim email prefs rows and compose+send one account's message.
type emailChannel struct {
	prefs    *EmailPrefsRepository
	settings *Settings
	sender   mail.Sender
}

// newEmailWorker assembles the email channel on the shared account-channel
// engine.
func newEmailWorker(
	pool *pgxpool.Pool,
	deliveries *DeliveryRepository,
	prefs *EmailPrefsRepository,
	settings *Settings,
	sender mail.Sender,
) *accountChannelWorker {
	return newAccountChannelWorker(pool, deliveries, &emailChannel{
		prefs:    prefs,
		settings: settings,
		sender:   sender,
	})
}

func (c *emailChannel) name() string { return "email" }

func (c *emailChannel) enabled(ctx context.Context) bool {
	return c.settings.EmailEnabled(ctx) && c.sender.Enabled(ctx)
}

func (c *emailChannel) allowPerEpisode(ctx context.Context) bool {
	return c.settings.EmailAllowPerEpisode(ctx)
}

func (c *emailChannel) digestHour(ctx context.Context) int {
	return c.settings.EmailDigestHour(ctx)
}

func (c *emailChannel) listRecipients(ctx context.Context) ([]accountRecipient, error) {
	return c.prefs.ListActiveRecipients(ctx)
}

func (c *emailChannel) claim(ctx context.Context, tx pgx.Tx, userID int) (*accountRecipient, error) {
	return c.prefs.claimForUpdate(ctx, tx, userID)
}

func (c *emailChannel) markSent(ctx context.Context, tx pgx.Tx, userID int, watermark Cursor, digestAt *time.Time) error {
	return c.prefs.markSent(ctx, tx, userID, watermark, digestAt)
}

func (c *emailChannel) markFailure(ctx context.Context, tx pgx.Tx, userID int, _ error) error {
	return c.prefs.markFailure(ctx, tx, userID)
}

// send composes and sends one account's pending notifications. The address is
// re-read under the claim so a mid-pass address removal fails cleanly instead
// of sending to a stale recipient.
func (c *emailChannel) send(ctx context.Context, tx pgx.Tx, userID int, mode string, rows []DeliveryRow) error {
	var email string
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(email, '') FROM users WHERE id = $1 AND enabled`, userID,
	).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && email == "") {
		return fmt.Errorf("account %d has no usable email address", userID)
	}
	if err != nil {
		return fmt.Errorf("look up account email: %w", err)
	}

	content := composeNotificationEmail(mode, rows, c.settings.EmailExternalURL(ctx))
	err = c.sender.Send(ctx, mail.Message{
		To:       []string{email},
		Subject:  content.Subject,
		TextBody: content.Text,
		HTMLBody: content.HTML,
	})
	if errors.Is(err, mail.ErrNotConfigured) {
		return fmt.Errorf("smtp not configured: %w", errChannelUnavailable)
	}
	return err
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
	if !ValidChannelMode(mode) {
		return ErrEmailModeInvalid
	}
	if ModeIncludesPerEpisode(mode) && !s.Settings.EmailAllowPerEpisode(ctx) {
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

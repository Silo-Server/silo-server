package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5"
)

// emailChannel implements accountChannel over the shared SMTP core, keyed by
// profile ID. The engine owns the sweep loop and watermark; this adapter only
// knows how to list/claim email prefs rows and compose+send one profile's
// message to its resolved destination (verified custom address, else the
// account email).
type emailChannel struct {
	prefs      *EmailPrefsRepository
	deliveries *DeliveryRepository
	settings   *Settings
	sender     mail.Sender
	// profileName resolves a display name for the email copy; best-effort
	// (empty on any failure). Set by NewSystem after construction.
	profileName func(ctx context.Context, userID int, profileID string) string
}

// The assertion also keeps staticcheck's unused-analysis aware that the
// adapter methods are consumed through the generic engine interface.
var _ accountChannel[string] = (*emailChannel)(nil)

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

func (c *emailChannel) listRecipients(ctx context.Context) ([]accountRecipient[string], error) {
	return c.prefs.ListActiveRecipients(ctx)
}

func (c *emailChannel) hasPendingSince(ctx context.Context, profileID string, since Cursor) (bool, error) {
	return c.deliveries.HasForProfileSince(ctx, profileID, since)
}

func (c *emailChannel) listSince(ctx context.Context, tx pgx.Tx, profileID string, since Cursor, until time.Time, limit int) ([]DeliveryRow, error) {
	return c.deliveries.ListForProfileSince(ctx, tx, profileID, since, until, limit)
}

func (c *emailChannel) claim(ctx context.Context, tx pgx.Tx, profileID string) (*accountRecipient[string], error) {
	return c.prefs.claimForUpdate(ctx, tx, profileID)
}

func (c *emailChannel) markSent(ctx context.Context, tx pgx.Tx, profileID string, watermark Cursor, digestAt *time.Time) error {
	return c.prefs.markSent(ctx, tx, profileID, watermark, digestAt)
}

func (c *emailChannel) markFailure(ctx context.Context, tx pgx.Tx, profileID string, _ error) error {
	return c.prefs.markFailure(ctx, tx, profileID)
}

// send composes and sends one profile's pending notifications. The
// destination is re-read under the claim so a mid-pass address removal fails
// cleanly instead of sending to a stale recipient.
func (c *emailChannel) send(ctx context.Context, tx pgx.Tx, profileID string, mode string, rows []DeliveryRow) error {
	email, userID, unsubscribeToken, err := c.prefs.destinationForSend(ctx, tx, profileID)
	if err != nil {
		return err
	}
	if email == "" {
		return fmt.Errorf("profile %s has no usable email address", profileID)
	}
	// The token is minted lazily, under the claim lock, right before the
	// first email that embeds it — this is the only mint point.
	if unsubscribeToken == "" {
		unsubscribeToken, _, err = newEmailToken()
		if err != nil {
			return err
		}
		if err := c.prefs.setUnsubscribeToken(ctx, tx, profileID, unsubscribeToken); err != nil {
			return err
		}
	}

	baseURL := c.settings.EmailExternalURL(ctx)
	opts := emailComposeOptions{
		BaseURL:        baseURL,
		UnsubscribeURL: emailUnsubscribeURL(baseURL, unsubscribeToken),
	}
	if c.profileName != nil {
		opts.ProfileName = c.profileName(ctx, userID, profileID)
	}

	content := composeNotificationEmail(mode, rows, opts)
	msg := mail.Message{
		To:       []string{email},
		Subject:  content.Subject,
		TextBody: content.Text,
		HTMLBody: content.HTML,
	}
	if opts.UnsubscribeURL != "" {
		// RFC 8058 one-click unsubscribe; the POST target is the same URL.
		msg.Headers = map[string]string{
			"List-Unsubscribe":      "<" + opts.UnsubscribeURL + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		}
	}
	err = c.sender.Send(ctx, msg)
	if errors.Is(err, mail.ErrNotConfigured) {
		return fmt.Errorf("smtp not configured: %w", errChannelUnavailable)
	}
	return err
}

// emailUnsubscribeURL builds the tokenized unsubscribe link; empty when no
// external URL is configured (the email then renders without one).
func emailUnsubscribeURL(baseURL, token string) string {
	if baseURL == "" || token == "" {
		return ""
	}
	return baseURL + "/api/v1/notifications/email/unsubscribe?token=" + token
}

// Errors surfaced by the email preference API layer to map to 4xx responses.
var (
	ErrEmailModeInvalid    = errors.New("invalid email notification mode")
	ErrEmailModeNotAllowed = errors.New("per-episode email is disabled by the administrator")
	ErrEmailNoAddress      = errors.New("profile has no verified email address")
)

// EmailAvailable reports whether the email channel can deliver right now:
// a sender is wired, the kill switch is on, and SMTP is configured.
func (s *System) EmailAvailable(ctx context.Context) bool {
	return s != nil && s.emailWorker != nil &&
		s.Settings.EmailEnabled(ctx) && s.mailSender.Enabled(ctx)
}

// EmailPreferencesState is one profile's email channel state as the API
// surfaces it.
type EmailPreferencesState struct {
	Mode         string
	CustomEmail  string
	PendingEmail string
	IsChild      bool
}

// EmailPreferences returns the profile's email notification state.
func (s *System) EmailPreferences(ctx context.Context, userID int, profileID string) (EmailPreferencesState, error) {
	state := EmailPreferencesState{Mode: EmailModeOff}
	if s == nil || s.EmailPrefs == nil {
		return state, nil
	}
	prefs, err := s.EmailPrefs.Get(ctx, profileID)
	if err != nil {
		return state, err
	}
	state.Mode = prefs.Mode
	state.CustomEmail = prefs.CustomEmail
	state.PendingEmail = prefs.PendingEmail
	state.IsChild = s.profileIsChild(ctx, userID, profileID)
	return state, nil
}

// SetEmailMode validates and stores the profile's email mode. Enabling
// requires the profile's own verified address and, for per-episode, the
// admin allowance.
func (s *System) SetEmailMode(ctx context.Context, userID int, profileID, mode string) error {
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
		prefs, err := s.EmailPrefs.Get(ctx, profileID)
		if err != nil {
			return err
		}
		if prefs.CustomEmail == "" {
			return ErrEmailNoAddress
		}
	}
	return s.EmailPrefs.SetMode(ctx, userID, profileID, mode)
}

// lookupProfile loads the profile from its account's userstore; nil on any
// failure (callers treat that as the safe default).
func (s *System) lookupProfile(ctx context.Context, userID int, profileID string) *userstore.Profile {
	if s == nil || s.stores == nil {
		return nil
	}
	store, err := s.stores.ForUser(ctx, userID)
	if err != nil {
		return nil
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil {
		return nil
	}
	return profile
}

// profileIsChild reports whether the profile is a child profile. Best-effort:
// lookup failures err on the safe side (treated as child, which only
// restricts custom-address edits).
func (s *System) profileIsChild(ctx context.Context, userID int, profileID string) bool {
	profile := s.lookupProfile(ctx, userID, profileID)
	return profile == nil || profile.IsChild
}

// lookupProfileName resolves the profile's display name for email copy;
// best-effort, empty on any failure.
func (s *System) lookupProfileName(ctx context.Context, userID int, profileID string) string {
	if profile := s.lookupProfile(ctx, userID, profileID); profile != nil {
		return profile.Name
	}
	return ""
}

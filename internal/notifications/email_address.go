package notifications

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/mail"
)

// emailVerifyTTL bounds how long a verification link stays usable.
const emailVerifyTTL = 24 * time.Hour

// Errors surfaced by the custom-address flow for the API layer.
var (
	ErrEmailInvalidAddress = errors.New("invalid email address")
	ErrEmailChildProfile   = errors.New("child profiles cannot set a custom notification address")
	ErrEmailNoLinkBase     = errors.New("no external URL is configured for verification links")
)

// newEmailToken mints a single-use capability token and its SHA-256 hex
// digest for at-rest storage.
func newEmailToken() (token, tokenHash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate email token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, hashEmailToken(token), nil
}

// hashEmailToken returns the at-rest digest of a verification token.
func hashEmailToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// emailLinkBase is the externally reachable base URL for tokenized email
// links: the admin's notifications.email.external_url, falling back to the
// server's public URL.
func (s *System) emailLinkBase(ctx context.Context) string {
	if base := s.Settings.EmailExternalURL(ctx); base != "" {
		return base
	}
	return s.publicURL
}

// SetPublicURL wires the server's externally reachable base URL, used as the
// fallback for verification links when notifications.email.external_url is
// unset. Optional.
func (s *System) SetPublicURL(url string) {
	if s != nil {
		s.publicURL = strings.TrimRight(url, "/")
	}
}

// RequestEmailAddress starts custom-address verification for the profile: it
// validates and stores the pending address, then emails it a single-use
// confirmation link. Notifications keep flowing to the previous destination
// until the new address is verified. Child profiles are refused — a session
// acting as a child profile must not be able to route the household's
// viewing activity to an arbitrary address.
func (s *System) RequestEmailAddress(ctx context.Context, userID int, profileID, address string) error {
	if s == nil || s.EmailPrefs == nil {
		return ErrEmailInvalidAddress
	}
	parsed, err := netmail.ParseAddress(strings.TrimSpace(address))
	if err != nil || parsed.Address != strings.TrimSpace(address) {
		return ErrEmailInvalidAddress
	}
	address = parsed.Address
	profile := s.lookupProfile(ctx, userID, profileID)
	if profile == nil || profile.IsChild {
		return ErrEmailChildProfile
	}
	linkBase := s.emailLinkBase(ctx)
	if linkBase == "" {
		return ErrEmailNoLinkBase
	}

	token, tokenHash, err := newEmailToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(emailVerifyTTL)
	if err := s.EmailPrefs.RequestPendingAddress(ctx, userID, profileID, address,
		tokenHash, expiresAt); err != nil {
		return err
	}

	verifyURL := linkBase + "/api/v1/notifications/email/verify?token=" + token
	content := composeVerificationEmail(profile.Name, verifyURL)
	err = s.mailSender.Send(ctx, mail.Message{
		To:       []string{address},
		Subject:  content.Subject,
		TextBody: content.Text,
		HTMLBody: content.HTML,
	})
	if err != nil {
		return fmt.Errorf("send verification email: %w", err)
	}
	return nil
}

// ClearEmailAddress removes the profile's verified address (and any
// in-flight verification), which also switches the channel off — there is no
// fallback destination. Restricted to non-child profiles like setting one,
// so a child session cannot drop a parent-configured destination.
func (s *System) ClearEmailAddress(ctx context.Context, userID int, profileID string) error {
	if s == nil || s.EmailPrefs == nil {
		return nil
	}
	if s.profileIsChild(ctx, userID, profileID) {
		return ErrEmailChildProfile
	}
	return s.EmailPrefs.ClearCustomAddress(ctx, profileID)
}

// VerifyEmailToken consumes a verification token from a clicked link,
// promoting that profile's pending address to the verified destination.
func (s *System) VerifyEmailToken(ctx context.Context, token string) (EmailVerifyOutcome, error) {
	if s == nil || s.EmailPrefs == nil || token == "" {
		return EmailVerifyInvalid, nil
	}
	return s.EmailPrefs.ConsumeVerifyToken(ctx, hashEmailToken(token))
}

// UnsubscribeEmail handles a tokenized unsubscribe link: the matching
// profile's email mode switches off.
func (s *System) UnsubscribeEmail(ctx context.Context, token string) (ok bool, err error) {
	if s == nil || s.EmailPrefs == nil || token == "" {
		return false, nil
	}
	return s.EmailPrefs.UnsubscribeByToken(ctx, token)
}

// composeVerificationEmail renders the address-confirmation message.
func composeVerificationEmail(profileName, verifyURL string) emailContent {
	who := "your profile"
	if profileName != "" {
		who = "the profile “" + profileName + "”"
	}
	text := fmt.Sprintf(
		"This address was entered as the notification destination for %s on a Silo server.\n\n"+
			"To confirm and start receiving notifications here, open this link:\n\n  %s\n\n"+
			"The link expires in 24 hours. If you didn't request this, ignore this email — "+
			"nothing will be sent to this address.\n", who, verifyURL)
	htmlBody := fmt.Sprintf(`<div style="font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;font-size:14px;line-height:1.5;color:#1a1a1a;max-width:560px;">
<p style="margin:0 0 8px;">This address was entered as the notification destination for %s on a Silo server.</p>
<p style="margin:0 0 16px;">To confirm and start receiving notifications here:</p>
<p style="margin:0 0 16px;"><a href="%s" style="background:#6d6df7;color:#fff;text-decoration:none;padding:10px 18px;border-radius:6px;display:inline-block;">Confirm this address</a></p>
<hr style="border:none;border-top:1px solid #e5e5e5;margin:16px 0 8px;">
<p style="margin:0;font-size:12px;color:#888;">The link expires in 24 hours. If you didn't request this, ignore this email — nothing will be sent to this address.</p>
</div>`,
		html.EscapeString(who), html.EscapeString(verifyURL))
	return emailContent{
		Subject: "Confirm your Silo notification address",
		Text:    text,
		HTML:    htmlBody,
	}
}

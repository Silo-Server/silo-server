// Package signinlinks makes, sends and redeems sign-in links: a single-use
// link an administrator makes for one account, so a person who lost every
// device gets back in, by the Silo app or by setting a password. The links
// themselves are stored and spent by internal/auth.
//
// Invariants: docs/architecture/app-keys.md, "Sign-in links".
package signinlinks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/models"
)

// PagePath is the web page a sign-in link opens; the token follows in the
// fragment.
const PagePath = "/sign-in-link"

var (
	// ErrNoEmail refuses emailing a link to an account with no usable
	// address.
	ErrNoEmail = errors.New("account has no usable email address")
	// ErrNoLinkBase refuses a link while the server has no public URL
	// (server.public_url), as an administrator's password reset link is
	// refused: a link built on the address the administrator happened to
	// use might not open for the person.
	ErrNoLinkBase = errors.New("no external URL is configured for sign-in links")
)

// store is the part of *auth.AppKeySignIn the service uses.
type store interface {
	IssueSignInLink(ctx context.Context, in auth.SignInLinkIssue) (auth.IssuedSignInLink, error)
	RedeemSignInLink(ctx context.Context, token string, canDeliverCode bool) (auth.SignInLinkRedemption, error)
	SetPasswordWithSignInLink(ctx context.Context, token, newPassword string) (int, error)
}

// users reads the account a link is for.
type users interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

// Service makes, sends and redeems sign-in links.
type Service struct {
	links    store
	users    users
	mail     mail.Sender
	settings mail.SettingReader
	brand    *mail.BrandLoader
	now      func() time.Time
	// sessionsRevoked runs after a set password signed the account out, so
	// state held outside auth_sessions is dropped too.
	sessionsRevoked func(ctx context.Context, userID int)
}

// NewService wires the sign-in link service. brand styles the email; nil
// sends Silo's default branding.
func NewService(links *auth.AppKeySignIn, users users, sender mail.Sender, settings mail.SettingReader, brand *mail.BrandLoader) *Service {
	return &Service{links: links, users: users, mail: sender, settings: settings, brand: brand, now: time.Now}
}

// OnSessionsRevoked registers the hook a set password runs after it signed
// the account out.
func (s *Service) OnSessionsRevoked(fn func(ctx context.Context, userID int)) {
	s.sessionsRevoked = fn
}

// Capabilities is what the server can do with sign-in links now. Whether a
// link can be made for one account is that account's: it must be allowed to
// sign in with a local password on this server (the admin account view's
// local_password_allowed), and the refusal says so.
type Capabilities struct {
	// Links: a link can be built, which needs the server's public URL.
	Links bool
	// Email: a link can also be emailed; a mail server is configured.
	Email bool
}

// Capabilities answers from the live settings.
func (s *Service) Capabilities(ctx context.Context) (Capabilities, error) {
	links := s.linkBase(ctx) != ""
	return Capabilities{Links: links, Email: links && s.mail != nil && s.mail.Enabled(ctx)}, nil
}

func (s *Service) linkBase(ctx context.Context) string {
	return mail.AccountLinkBase(ctx, s.settings, "")
}

// IssueInput is an administrator's request for a sign-in link.
type IssueInput struct {
	UserID    int
	IssuedBy  int
	ScopedKey bool
	SendEmail bool
}

// IssueResult is a stored link. With SendEmail it may come with an email
// error; the link is stored and returned either way.
type IssueResult struct {
	// URL embeds the raw token: shown only to the issuing administrator.
	URL       string
	ExpiresAt time.Time
	EmailSent bool
}

// Issue stores a new link for the account, replacing its earlier ones, and
// emails it when asked. The link is built on the server's public URL, as a
// password reset link is, for the answer and the email alike. An email
// request for an account with no address, or on a server without mail, and
// any request on a server with no public URL, are refused before a link is
// made, since a new link would replace one the person may hold. The address
// an email goes to is checked again when the link is stored (it must still be
// the account's), so a link is never sent to an address the account dropped
// meanwhile.
func (s *Service) Issue(ctx context.Context, in IssueInput) (*IssueResult, error) {
	var user *models.User
	delivery := "link"
	if in.SendEmail {
		delivery = "email"
		var err error
		if user, err = s.users.GetByID(ctx, in.UserID); err != nil {
			return nil, err
		}
		if _, err := auth.ValidateEmail(user.Email); err != nil {
			return nil, ErrNoEmail
		}
		if s.mail == nil || !s.mail.Enabled(ctx) {
			return nil, mail.ErrNotConfigured
		}
	}
	base := s.linkBase(ctx)
	if base == "" {
		return nil, ErrNoLinkBase
	}
	request := auth.SignInLinkIssue{UserID: in.UserID, IssuedBy: in.IssuedBy, ScopedKey: in.ScopedKey, Delivery: delivery}
	if user != nil {
		request.ExpectedEmail = &user.Email
	}
	issued, err := s.links.IssueSignInLink(ctx, request)
	if err != nil {
		return nil, err
	}
	result := &IssueResult{URL: mail.AccountLink(base, PagePath, issued.Token), ExpiresAt: issued.ExpiresAt}
	if !in.SendEmail {
		return result, nil
	}
	brand := s.brand.Load(ctx)
	content := composeEmail(brand, user.Username, mail.ServerName(ctx, s.settings), result.URL, result.ExpiresAt, s.now())
	if err := s.mail.Send(ctx, mail.Message{
		To: []string{user.Email}, Subject: content.Subject, TextBody: content.Text, HTMLBody: content.HTML,
		Inline: brand.InlineImages(),
	}); err != nil {
		return result, fmt.Errorf("sign-in link stored; email delivery failed or is uncertain: %w", err)
	}
	result.EmailSent = true
	return result, nil
}

// Redeem spends a link (auth.AppKeySignIn.RedeemSignInLink).
// canDeliverCode says the caller can hand an app code out; whether app key
// sign-in is on is read inside the redemption.
func (s *Service) Redeem(ctx context.Context, token string, canDeliverCode bool) (auth.SignInLinkRedemption, error) {
	return s.links.RedeemSignInLink(ctx, strings.TrimSpace(token), canDeliverCode)
}

// SetPassword sets the account's password with a redeemed link's
// set-password token and signs the account out everywhere.
func (s *Service) SetPassword(ctx context.Context, token, newPassword string) error {
	userID, err := s.links.SetPasswordWithSignInLink(ctx, strings.TrimSpace(token), newPassword)
	if err != nil {
		return err
	}
	if s.sessionsRevoked != nil {
		s.sessionsRevoked(ctx, userID)
	}
	return nil
}

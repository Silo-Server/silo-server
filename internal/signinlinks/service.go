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
	// ErrNoAddress reports a request whose server address could not be
	// worked out, so no link could be built.
	ErrNoAddress = errors.New("no server address for the link")
)

// store is the part of *auth.AppKeySignIn the service uses.
type store interface {
	IssueSignInLink(ctx context.Context, in auth.SignInLinkIssue) (auth.IssuedSignInLink, error)
	RedeemSignInLink(ctx context.Context, token string, keysOn bool) (auth.SignInLinkRedemption, error)
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

// EmailAvailable reports whether the server can email a link now.
func (s *Service) EmailAvailable(ctx context.Context) bool {
	return s.mail != nil && s.mail.Enabled(ctx)
}

// IssueInput is an administrator's request for a sign-in link.
type IssueInput struct {
	UserID    int
	IssuedBy  int
	ScopedKey bool
	SendEmail bool
	// ServerAddress is the server address the link starts with, as the app
	// code answers compute it.
	ServerAddress string
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
// emails it when asked. An email request for an account with no address,
// or on a server without mail, is refused before a link is made, since a
// new link would replace one the person may hold.
func (s *Service) Issue(ctx context.Context, in IssueInput) (*IssueResult, error) {
	address := strings.TrimRight(strings.TrimSpace(in.ServerAddress), "/")
	if address == "" {
		return nil, ErrNoAddress
	}
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
		if !s.EmailAvailable(ctx) {
			return nil, mail.ErrNotConfigured
		}
	}
	issued, err := s.links.IssueSignInLink(ctx, auth.SignInLinkIssue{
		UserID: in.UserID, IssuedBy: in.IssuedBy, ScopedKey: in.ScopedKey, Delivery: delivery,
	})
	if err != nil {
		return nil, err
	}
	result := &IssueResult{URL: address + PagePath + "#" + issued.Token, ExpiresAt: issued.ExpiresAt}
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

// Redeem spends a link (auth.AppKeySignIn.RedeemSignInLink). keysOn says
// whether app key sign-in is on; while it is off no app code is made.
func (s *Service) Redeem(ctx context.Context, token string, keysOn bool) (auth.SignInLinkRedemption, error) {
	return s.links.RedeemSignInLink(ctx, strings.TrimSpace(token), keysOn)
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

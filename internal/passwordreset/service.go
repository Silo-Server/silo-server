package passwordreset

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/models"
)

// DefaultTTL bounds how long a reset link stays usable.
const DefaultTTL = 24 * time.Hour

// SettingSelfService lets anyone ask for a reset link for their own account
// from the sign-in page. Off by default: an administrator opts the server in.
const SettingSelfService = "password_reset.self_service_enabled"

// Self-service limits. Nobody vouches for a link requested from the sign-in
// page, so it lives an hour rather than DefaultTTL, and an account gets at
// most one per cooldown however often its name is typed into the form.
const (
	SelfServiceTTL      = time.Hour
	selfServiceCooldown = 5 * time.Minute
	// selfServiceTimeout bounds one background request, SMTP send included.
	selfServiceTimeout = time.Minute
	// withdrawTimeout bounds retracting a link whose email failed.
	withdrawTimeout = 5 * time.Second
	// maxPendingRequests bounds concurrent background requests on one node.
	// Beyond it a request is dropped and logged; the requester can ask again.
	maxPendingRequests = 8
)

// Errors surfaced to the API layer.
var (
	ErrAccountDisabled = errors.New("account is disabled")
	ErrNoEmail         = errors.New("account has no usable email address")
	ErrNoLinkBase      = errors.New("no external URL is configured for reset links")
	ErrUnknownDelivery = errors.New("unknown reset link delivery")
	// ErrSelfServiceDisabled reports that an administrator has not turned
	// self-service reset on.
	ErrSelfServiceDisabled = errors.New("self-service password reset is disabled")
	// ErrSelfServiceNotConfigured reports a server that cannot email a link:
	// it lacks an external URL or a configured mail server.
	ErrSelfServiceNotConfigured = errors.New("self-service password reset needs email and a public URL")
	// ErrSessionStart reports a completed reset whose sign-in failed; the new
	// password is in place and the link is spent.
	ErrSessionStart = errors.New("password reset but login failed")
)

// Delivery is how an issued link reaches the account holder.
type Delivery string

const (
	// DeliveryEmail emails the link to the account's address.
	DeliveryEmail Delivery = "email"
	// DeliveryLink returns the link to the issuing administrator to share.
	DeliveryLink Delivery = "link"
)

// repository is the persistence surface Service needs (satisfied by
// *Repository; an interface so tests can fake it).
type repository interface {
	Issue(ctx context.Context, userID int, tokenHash string, issuedBy *int, expiresAt time.Time) error
	IssueUnlessRecent(ctx context.Context, userID int, tokenHash string, expiresAt time.Time, minAge time.Duration) (bool, error)
	Withdraw(ctx context.Context, userID int, tokenHash string) error
	Lookup(ctx context.Context, tokenHash string) (*Link, error)
	Complete(ctx context.Context, tokenHash, newPassword string) (*models.User, error)
}

// userDirectory reads the account a link is issued for, by ID or by the
// sign-in name its holder types.
type userDirectory interface {
	auth.LoginDirectory
	GetByID(ctx context.Context, id int) (*models.User, error)
}

// sessionStarter signs the account in after a reset. Satisfied by *auth.Service.
type sessionStarter interface {
	Login(ctx context.Context, username, password, deviceName, ip string) (*auth.TokenPair, *models.User, error)
}

// Service orchestrates issuing and completing reset links.
type Service struct {
	repo      repository
	users     userDirectory
	sessions  sessionStarter
	mail      mail.Sender
	settings  mail.SettingReader
	publicURL string
	ttl       time.Duration
	now       func() time.Time
	// sessionsRevoked runs after a completed reset revoked the account's
	// sessions, so state held outside auth_sessions is dropped too.
	sessionsRevoked func(ctx context.Context, userID int)
	// pending holds one slot per self-service request still running, and
	// async runs it; tests replace async to run requests inline.
	pending chan struct{}
	async   func(func())
}

// NewService wires the reset link service. publicURL is the link-base
// fallback when server.public_url is unset; may be empty.
func NewService(
	repo *Repository,
	users userDirectory,
	sessions sessionStarter,
	mailSender mail.Sender,
	settings mail.SettingReader,
	publicURL string,
) *Service {
	return &Service{
		repo:      repo,
		users:     users,
		sessions:  sessions,
		mail:      mailSender,
		settings:  settings,
		publicURL: publicURL,
		ttl:       DefaultTTL,
		now:       time.Now,
		pending:   make(chan struct{}, maxPendingRequests),
		async:     func(fn func()) { go fn() },
	}
}

// OnSessionsRevoked registers the hook a completed reset runs after revoking
// the account's sessions.
func (s *Service) OnSessionsRevoked(fn func(ctx context.Context, userID int)) {
	s.sessionsRevoked = fn
}

// Capabilities reports which deliveries the server can make right now.
type Capabilities struct {
	Link  bool
	Email bool
}

// Capabilities answers from the live settings: a link needs an external URL,
// and email additionally needs a configured mail server.
func (s *Service) Capabilities(ctx context.Context) Capabilities {
	link := s.linkBase(ctx) != ""
	return Capabilities{Link: link, Email: link && s.mail != nil && s.mail.Enabled(ctx)}
}

// IssueInput is an administrator's request for a reset link.
type IssueInput struct {
	UserID   int
	IssuedBy int
	Delivery Delivery
}

// IssueResult reports a stored link. It may accompany a delivery error; then
// EmailSent=false does not prove the message was not delivered.
type IssueResult struct {
	// URL is returned for DeliveryLink only. It embeds the raw token: the
	// caller must reveal it only to the issuing administrator.
	URL       string
	ExpiresAt time.Time
	EmailSent bool
}

// Issue stores a new link for the account, replacing any earlier one, and
// delivers it. The link lets its holder set a new password; nobody else sees
// that password.
func (s *Service) Issue(ctx context.Context, in IssueInput) (*IssueResult, error) {
	if in.Delivery != DeliveryEmail && in.Delivery != DeliveryLink {
		return nil, ErrUnknownDelivery
	}
	user, err := s.users.GetByID(ctx, in.UserID)
	if err != nil {
		return nil, err
	}
	if !user.LocalPasswordLoginEnabled || user.PasswordHash == "" {
		return nil, auth.ErrPasswordLoginDisabled
	}
	if !user.Enabled {
		return nil, ErrAccountDisabled
	}
	if in.Delivery == DeliveryEmail {
		if _, err := auth.ValidateEmail(user.Email); err != nil {
			return nil, ErrNoEmail
		}
		// Refuse before minting: a link nobody receives would still replace
		// the one the account may already hold.
		if s.mail == nil || !s.mail.Enabled(ctx) {
			return nil, mail.ErrNotConfigured
		}
	}
	linkBase := s.linkBase(ctx)
	if linkBase == "" {
		return nil, ErrNoLinkBase
	}

	token, tokenHash, err := auth.NewLinkToken()
	if err != nil {
		return nil, fmt.Errorf("generate password reset token: %w", err)
	}
	var issuedBy *int
	if in.IssuedBy > 0 {
		issuedBy = &in.IssuedBy
	}
	expiresAt := s.now().Add(s.ttl)
	if err := s.repo.Issue(ctx, user.ID, tokenHash, issuedBy, expiresAt); err != nil {
		return nil, err
	}

	resetURL := linkBase + "/reset-password/" + token
	result := &IssueResult{ExpiresAt: expiresAt}
	if in.Delivery == DeliveryLink {
		result.URL = resetURL
		return result, nil
	}
	content := composeResetEmail(false, user.Username, s.serverName(ctx), resetURL, expiresAt, s.now())
	err = s.mail.Send(ctx, mail.Message{
		To:       []string{user.Email},
		Subject:  content.Subject,
		TextBody: content.Text,
		HTMLBody: content.HTML,
	})
	if err != nil {
		return result, fmt.Errorf("reset link stored; email delivery failed or is uncertain: %w", err)
	}
	result.EmailSent = true
	return result, nil
}

// SelfService reports whether an administrator turned self-service reset on,
// and whether the server can deliver it: a link needs an external URL and
// email needs a configured mail server.
func (s *Service) SelfService(ctx context.Context) (enabled, configured bool, err error) {
	value, err := s.settings.Get(ctx, SettingSelfService)
	if err != nil {
		return false, false, fmt.Errorf("reading %s: %w", SettingSelfService, err)
	}
	enabled, _ = strconv.ParseBool(strings.TrimSpace(value))
	return enabled, s.Capabilities(ctx).Email, nil
}

// Request starts a reset the account holder asked for on the sign-in page,
// naming the account by sign-in name or email address. Only availability is
// answered here. The lookup, the link, and the email run in the background,
// so neither the result nor its timing tells the caller whether an account
// matched.
func (s *Service) Request(ctx context.Context, login string) error {
	enabled, configured, err := s.SelfService(ctx)
	switch {
	case err != nil:
		return err
	case !configured:
		return ErrSelfServiceNotConfigured
	case !enabled:
		return ErrSelfServiceDisabled
	}
	select {
	case s.pending <- struct{}{}:
	default:
		slog.WarnContext(ctx, "password reset request dropped: too many in flight", "component", "passwordreset")
		return nil
	}
	s.async(func() {
		defer func() { <-s.pending }()
		// Nothing waits on this task, so a panic here would take the server down.
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(ctx, "password reset request panicked", "component", "passwordreset", "panic", r)
			}
		}()
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), selfServiceTimeout)
		defer cancel()
		if err := s.sendRequested(bg, strings.TrimSpace(login)); err != nil {
			slog.WarnContext(bg, "password reset request failed", "component", "passwordreset", "error", err)
		}
	})
	return nil
}

// sendRequested emails a new link to the account login names. Every reason
// not to send (no such account, no usable address, an account that cannot
// use a reset, a link sent moments ago, a live link from an administrator)
// is a silent nil: the requester got the same answer either way.
func (s *Service) sendRequested(ctx context.Context, login string) error {
	user, err := auth.LookupLogin(ctx, s.users, login)
	if auth.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("looking up account: %w", err)
	}
	if !user.Enabled || !user.LocalPasswordLoginEnabled || user.PasswordHash == "" {
		return nil
	}
	if _, err := auth.ValidateEmail(user.Email); errors.Is(err, auth.ErrInvalidEmail) {
		return nil
	}
	linkBase := s.linkBase(ctx)
	if linkBase == "" {
		return ErrNoLinkBase
	}
	token, tokenHash, err := auth.NewLinkToken()
	if err != nil {
		return fmt.Errorf("generate password reset token: %w", err)
	}
	expiresAt := s.now().Add(SelfServiceTTL)
	stored, err := s.repo.IssueUnlessRecent(ctx, user.ID, tokenHash, expiresAt, selfServiceCooldown)
	if err != nil || !stored {
		return err
	}
	content := composeResetEmail(true, user.Username, s.serverName(ctx), linkBase+"/reset-password/"+token, expiresAt, s.now())
	if err := s.mail.Send(ctx, mail.Message{
		To:       []string{user.Email},
		Subject:  content.Subject,
		TextBody: content.Text,
		HTMLBody: content.HTML,
	}); err != nil {
		// A link that certainly was not sent is withdrawn, so the requester
		// can ask again now rather than after the cooldown. When delivery is
		// uncertain the link and its cooldown stay: it may have arrived, and
		// withdrawing would let repeated requests send more mail at once.
		if errors.Is(err, mail.ErrNotSent) || errors.Is(err, mail.ErrNotConfigured) {
			// The send may have used up ctx.
			wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), withdrawTimeout)
			defer cancel()
			if werr := s.repo.Withdraw(wctx, user.ID, tokenHash); werr != nil {
				err = errors.Join(err, werr)
			}
		}
		return fmt.Errorf("emailing requested reset link for account %d: %w", user.ID, err)
	}
	slog.InfoContext(ctx, "password reset link emailed on request", "component", "passwordreset", "user_id", user.ID)
	return nil
}

// LookupResult is the reset screen's view of a link: only what it renders.
type LookupResult struct {
	Username   string
	ServerName string
	ExpiresAt  time.Time
}

// Lookup resolves a raw token for the reset screen. Every unusable link is
// ErrNotFound.
func (s *Service) Lookup(ctx context.Context, token string) (*LookupResult, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrNotFound
	}
	link, err := s.repo.Lookup(ctx, auth.HashLinkToken(token))
	if err != nil {
		return nil, err
	}
	return &LookupResult{Username: link.Username, ServerName: s.serverName(ctx), ExpiresAt: link.ExpiresAt}, nil
}

// Complete spends the link and sets the new password, signing the account out
// everywhere, then signs this caller in. Login is a separate post-commit
// effect: a non-nil user with ErrSessionStart reports the committed reset so
// callers can direct the account holder to ordinary sign-in.
func (s *Service) Complete(ctx context.Context, token, password, deviceName, ip string) (*auth.TokenPair, *models.User, error) {
	if strings.TrimSpace(token) == "" {
		return nil, nil, ErrNotFound
	}
	// Validate first: a rejected password must not spend the link.
	if err := auth.ValidateNewPassword(password); err != nil {
		return nil, nil, err
	}
	user, err := s.repo.Complete(ctx, auth.HashLinkToken(token), password)
	if err != nil {
		return nil, nil, err
	}
	if s.sessionsRevoked != nil {
		s.sessionsRevoked(ctx, user.ID)
	}
	pair, loggedIn, err := s.sessions.Login(ctx, user.Username, password, deviceName, ip)
	if err != nil {
		return nil, user, errors.Join(ErrSessionStart, err)
	}
	return pair, loggedIn, nil
}

func (s *Service) linkBase(ctx context.Context) string {
	return mail.AccountLinkBase(ctx, s.settings, s.publicURL)
}

func (s *Service) serverName(ctx context.Context) string {
	return mail.ServerName(ctx, s.settings)
}

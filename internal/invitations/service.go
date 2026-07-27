package invitations

import (
	"context"
	"errors"
	"fmt"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/branding"
	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/models"
)

// DefaultTTL bounds how long a claim link stays usable.
const DefaultTTL = 7 * 24 * time.Hour

// Account roles an invitation may grant.
const (
	roleUser  = "user"
	roleAdmin = "admin"
)

// Errors surfaced to the API layer.
var (
	ErrInvalidEmail   = errors.New("invalid email address")
	ErrRoleNotAllowed = errors.New("inviter may not grant this role")
	ErrEmailTaken     = errors.New("an account with this email already exists")
	ErrNoLinkBase     = errors.New("no external URL is configured for invitation links")
)

// repository is the persistence surface Service needs (satisfied by
// *Repository; an interface so tests can fake it).
type repository interface {
	Create(ctx context.Context, input models.CreateInvitationInput, tokenHash string) (*models.Invitation, error)
	GetByID(ctx context.Context, id int64) (*models.Invitation, error)
	GetByTokenHash(ctx context.Context, tokenHash string) (*models.Invitation, error)
	List(ctx context.Context) ([]*models.Invitation, error)
	Accept(ctx context.Context, tokenHash string, userID int) error
	Revoke(ctx context.Context, id int64) error
	Delete(ctx context.Context, id int64) error
}

// userDirectory is the slice of the user repository the service needs.
type userDirectory interface {
	GetByEmail(ctx context.Context, email string) (*models.User, error)
	GetByUsername(ctx context.Context, username string) (*models.User, error)
	GetByID(ctx context.Context, id int) (*models.User, error)
}

// accountCreator provisions the account plus optional default profile.
// Satisfied by *auth.AccountProvisioner.
type accountCreator interface {
	CreateAccount(ctx context.Context, input auth.CreateAccountInput) (*models.User, error)
}

// sessionStarter logs the newly created user in. Satisfied by *auth.Service.
type sessionStarter interface {
	Login(ctx context.Context, username, password, deviceName, ip string) (*auth.TokenPair, *models.User, error)
}

// settingReader reads server settings (branding name, external URL).
type settingReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// Service orchestrates the invitation lifecycle.
type Service struct {
	repo      repository
	users     userDirectory
	accounts  accountCreator
	sessions  sessionStarter
	mail      mail.Sender
	settings  settingReader
	publicURL string
	ttl       time.Duration
	now       func() time.Time
}

// NewService wires the invitation service. publicURL is the server's
// externally reachable origin, used as the link-base fallback when
// notifications.email.external_url is unset; may be empty.
func NewService(
	repo *Repository,
	users userDirectory,
	accounts accountCreator,
	sessions sessionStarter,
	mailSender mail.Sender,
	settings settingReader,
	publicURL string,
) *Service {
	return &Service{
		repo:      repo,
		users:     users,
		accounts:  accounts,
		sessions:  sessions,
		mail:      mailSender,
		settings:  settings,
		publicURL: strings.TrimRight(publicURL, "/"),
		ttl:       DefaultTTL,
		now:       time.Now,
	}
}

// SendResult reports what happened to a newly created invitation.
type SendResult struct {
	Invitation *models.Invitation
	// ClaimURL is returned so the admin can copy the link when email is not
	// configured (EmailSent false). It embeds the raw token: the caller must
	// only reveal it to the inviting admin.
	ClaimURL  string
	EmailSent bool
}

// SendInput is the admin's request to invite someone.
type SendInput struct {
	Email         string
	Role          string
	AccessGroupID *int64
	LibraryIDs    []int
	CreateProfile bool
	ShowTour      bool
	Note          string
	// InvitedBy is the authenticated caller. The inviter's name for the
	// email and their admin status for the role-escalation check are read
	// from the database, not trusted from the request.
	InvitedBy int64
}

// Send validates, supersedes any live invitation for the address, stores the
// new one, and emails the claim link. When email is not configured the
// invitation is still created and the claim URL returned for manual delivery.
func (s *Service) Send(ctx context.Context, input SendInput) (*SendResult, error) {
	parsed, err := netmail.ParseAddress(strings.TrimSpace(input.Email))
	if err != nil || parsed.Address != strings.TrimSpace(input.Email) {
		return nil, ErrInvalidEmail
	}
	email := parsed.Address

	inviter, err := s.users.GetByID(ctx, int(input.InvitedBy))
	if err != nil {
		return nil, fmt.Errorf("resolving inviter: %w", err)
	}

	role := input.Role
	if role == "" {
		role = roleUser
	}
	if role != roleUser && role != roleAdmin {
		return nil, ErrRoleNotAllowed
	}
	if role == roleAdmin && inviter.Role != roleAdmin {
		return nil, ErrRoleNotAllowed
	}

	// Refuse addresses that already have an account. The address is also the
	// future username, so both unique columns are checked.
	if _, err := s.users.GetByEmail(ctx, email); err == nil {
		return nil, ErrEmailTaken
	} else if !auth.IsNotFound(err) {
		return nil, fmt.Errorf("checking email: %w", err)
	}
	if _, err := s.users.GetByUsername(ctx, email); err == nil {
		return nil, ErrEmailTaken
	} else if !auth.IsNotFound(err) {
		return nil, fmt.Errorf("checking username: %w", err)
	}

	linkBase := s.linkBase(ctx)
	if linkBase == "" {
		return nil, ErrNoLinkBase
	}

	token, tokenHash, err := NewToken()
	if err != nil {
		return nil, err
	}

	inv, err := s.repo.Create(ctx, models.CreateInvitationInput{
		Email:         email,
		Role:          role,
		AccessGroupID: input.AccessGroupID,
		LibraryIDs:    input.LibraryIDs,
		CreateProfile: input.CreateProfile,
		ShowTour:      input.ShowTour,
		Note:          strings.TrimSpace(input.Note),
		InvitedBy:     input.InvitedBy,
		ExpiresAt:     s.now().Add(s.ttl),
	}, tokenHash)
	if err != nil {
		return nil, err
	}

	claimURL := linkBase + "/invite/" + token
	result := &SendResult{Invitation: inv, ClaimURL: claimURL}

	content := composeInvitationEmail(
		inviter.Username, s.serverName(ctx), email, claimURL, inv.Note, inv.ExpiresAt, s.now())
	err = s.mail.Send(ctx, mail.Message{
		To:       []string{email},
		Subject:  content.Subject,
		TextBody: content.Text,
		HTMLBody: content.HTML,
	})
	switch {
	case err == nil:
		result.EmailSent = true
	case errors.Is(err, mail.ErrNotConfigured):
		// Degrade gracefully: the admin copies the link instead.
	default:
		return nil, fmt.Errorf("send invitation email: %w", err)
	}
	return result, nil
}

// Resend supersedes an invitation with a fresh token to the same address,
// re-using the original access choices. The old link stops working. The
// resending admin becomes the inviter of record.
func (s *Service) Resend(ctx context.Context, id, resentBy int64) (*SendResult, error) {
	prior, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.Send(ctx, SendInput{
		Email:         prior.Email,
		Role:          prior.Role,
		AccessGroupID: prior.AccessGroupID,
		LibraryIDs:    prior.LibraryIDs,
		CreateProfile: prior.CreateProfile,
		ShowTour:      prior.ShowTour,
		Note:          prior.Note,
		InvitedBy:     resentBy,
	})
}

// List returns all invitations, newest first.
func (s *Service) List(ctx context.Context) ([]*models.Invitation, error) {
	return s.repo.List(ctx)
}

// Revoke kills a live invitation link.
func (s *Service) Revoke(ctx context.Context, id int64) error {
	return s.repo.Revoke(ctx, id)
}

// LookupResult is the claim screen's view of an invitation: only what it
// renders, nothing else leaves the server pre-auth.
type LookupResult struct {
	Email       string
	InviterName string
	ServerName  string
	ExpiresAt   time.Time
	ShowTour    bool
}

// Lookup resolves a raw claim token for the claim screen. Unknown, expired,
// revoked, and accepted tokens all return ErrNotFound: a probe learns
// nothing about which.
func (s *Service) Lookup(ctx context.Context, token string) (*LookupResult, error) {
	inv, err := s.claimable(ctx, token)
	if err != nil {
		return nil, err
	}
	return &LookupResult{
		Email:       inv.Email,
		InviterName: inv.InvitedByName,
		ServerName:  s.serverName(ctx),
		ExpiresAt:   inv.ExpiresAt,
		ShowTour:    inv.ShowTour,
	}, nil
}

// Accept redeems the invitation: creates the account with the pre-bound
// access (username = email), claims the row, and logs the user in. Of two
// concurrent accepts exactly one wins; the loser's account creation is
// prevented by the users table's unique constraints, and the row claim by
// Accept's WHERE predicate.
func (s *Service) Accept(ctx context.Context, token, password, deviceName, ip string) (*auth.TokenPair, *models.User, error) {
	inv, err := s.claimable(ctx, token)
	if err != nil {
		return nil, nil, err
	}

	user, err := s.accounts.CreateAccount(ctx, auth.CreateAccountInput{
		User: models.CreateUserInput{
			Username:      inv.Email,
			Email:         inv.Email,
			Password:      password,
			Role:          inv.Role,
			LibraryIDs:    inv.LibraryIDs,
			AccessGroupID: inv.AccessGroupID,
		},
		DefaultProfile: auth.DefaultProfileOptions{
			Enabled: inv.CreateProfile,
			Name:    profileNameFromEmail(inv.Email),
		},
	})
	if err != nil {
		if auth.IsDuplicate(err) {
			// Lost a race with a concurrent accept, or the address gained an
			// account since the invitation was sent.
			return nil, nil, ErrNotClaimable
		}
		return nil, nil, fmt.Errorf("creating invited user: %w", err)
	}

	if err := s.repo.Accept(ctx, HashToken(token), user.ID); err != nil {
		// The row was consumed between claimable() and here. The account
		// exists; surface the claim failure rather than leaving a half-open
		// success. Admins can delete the orphan from the users screen.
		return nil, nil, err
	}

	return s.sessions.Login(ctx, inv.Email, password, deviceName, ip)
}

// claimable fetches a pending, unexpired, unrevoked invitation by raw token.
func (s *Service) claimable(ctx context.Context, token string) (*models.Invitation, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrNotFound
	}
	inv, err := s.repo.GetByTokenHash(ctx, HashToken(token))
	if err != nil {
		return nil, err
	}
	if inv.Status(s.now()) != models.InvitationStatusPending {
		return nil, ErrNotFound
	}
	return inv, nil
}

// linkBase resolves the externally reachable base URL for claim links:
// notifications.email.external_url, falling back to the server public URL.
func (s *Service) linkBase(ctx context.Context) string {
	if s.settings != nil {
		if base, err := s.settings.Get(ctx, "notifications.email.external_url"); err == nil {
			if base = strings.TrimRight(strings.TrimSpace(base), "/"); base != "" {
				return base
			}
		}
	}
	return s.publicURL
}

// serverName reads the branded server name for email copy and the claim
// screen, defaulting to "Silo".
func (s *Service) serverName(ctx context.Context) string {
	if s.settings != nil {
		if name, err := s.settings.Get(ctx, branding.KeyServerName); err == nil {
			if name = strings.TrimSpace(name); name != "" {
				return name
			}
		}
	}
	return branding.DefaultServerName
}

// profileNameFromEmail derives the default profile name from the address's
// local part ("marco@example.com" → "Marco").
func profileNameFromEmail(email string) string {
	local := email
	if at := strings.IndexByte(email, '@'); at > 0 {
		local = email[:at]
	}
	local = strings.TrimSpace(local)
	if local == "" {
		return ""
	}
	return strings.ToUpper(local[:1]) + local[1:]
}

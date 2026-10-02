package invitations

import (
	"context"
	"errors"
	"fmt"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/models"
)

// DefaultTTL bounds how long a claim link stays usable.
const DefaultTTL = 7 * 24 * time.Hour

// Account roles an invitation may grant.
const (
	roleUser  = models.RoleUser
	roleAdmin = models.RoleAdmin
)

// Errors surfaced to the API layer.
var (
	ErrInvalidEmail   = errors.New("invalid email address")
	ErrRoleNotAllowed = errors.New("inviter may not grant this role")
	ErrAdminGrouped   = errors.New("admin accounts cannot belong to an access group")
	ErrEmailTaken     = errors.New("an account with this email already exists")
	ErrSessionStart   = errors.New("invitation accepted but login failed")
	ErrNoLinkBase     = errors.New("no external URL is configured for invitation links")
	// ErrEmailUnavailable refuses an explicit email delivery when no mail
	// sender is configured, instead of creating a link the admin did not ask for.
	ErrEmailUnavailable = errors.New("email delivery is not configured")
	// ErrEmailRequired reports an accept of a link invitation without an address.
	ErrEmailRequired = errors.New("an email address is required to accept this invitation")
	// ErrNoAddress refuses to email a link invitation, which has no address.
	ErrNoAddress = errors.New("a link invitation has no address to email")
)

// Delivery is the admin's choice of how the claim link reaches the invitee.
type Delivery string

const (
	// DeliveryDefault emails the link when email is configured and otherwise
	// returns it for manual delivery. Callers that predate the choice use it.
	DeliveryDefault Delivery = ""
	// DeliveryLink sends nothing and returns the link to share. A new link
	// invitation has no address: the invitee enters theirs at accept.
	// Replacing an emailed invitation's link keeps its address.
	DeliveryLink Delivery = "link"
	// DeliveryEmail emails the link and fails when email is not configured.
	DeliveryEmail Delivery = "email"
)

// repository is the persistence surface Service needs (satisfied by
// *Repository; an interface so tests can fake it).
type repository interface {
	Create(ctx context.Context, input models.CreateInvitationInput, tokenHash string) (*models.Invitation, error)
	GetByID(ctx context.Context, id int64) (*models.Invitation, error)
	GetByTokenHash(ctx context.Context, tokenHash string) (*models.Invitation, error)
	List(ctx context.Context) ([]*models.Invitation, error)
	ListPage(context.Context, *PageKey, int) ([]*models.Invitation, bool, error)
	AcceptAs(ctx context.Context, tokenHash string, linkAddress func() (string, error), provision func(*models.Invitation, pgx.Tx) (*models.User, error)) (*models.User, error)
	Resend(ctx context.Context, id int64, input models.CreateInvitationInput, tokenHash string) (*models.Invitation, error)
	RecordEmailOutcome(ctx context.Context, id int64, delivery string) error
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
	CreateAccountInTransaction(ctx context.Context, tx pgx.Tx, input auth.CreateAccountInput) (*models.User, error)
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
	brand     *mail.BrandLoader
	publicURL string
	ttl       time.Duration
	now       func() time.Time
}

// NewService wires the invitation service. publicURL is the server's
// externally reachable origin, used as the link-base fallback when
// server.public_url is unset; may be empty. brand styles the email; nil sends
// Silo's default branding.
func NewService(
	repo *Repository,
	users userDirectory,
	accounts accountCreator,
	sessions sessionStarter,
	mailSender mail.Sender,
	settings settingReader,
	brand *mail.BrandLoader,
	publicURL string,
) *Service {
	return &Service{
		repo:      repo,
		users:     users,
		accounts:  accounts,
		sessions:  sessions,
		mail:      mailSender,
		settings:  settings,
		brand:     brand,
		publicURL: strings.TrimRight(publicURL, "/"),
		ttl:       DefaultTTL,
		now:       time.Now,
	}
}

// SendResult reports a committed invitation. It may accompany a delivery error;
// in that case EmailSent=false does not prove that the message was not delivered.
type SendResult struct {
	Invitation *models.Invitation
	// ClaimURL is returned so the admin can copy the link when email is not
	// configured (EmailSent false). It embeds the raw token: the caller must
	// only reveal it to the inviting admin.
	ClaimURL  string
	EmailSent bool
	// Delivery is the delivery the caller asked for.
	Delivery Delivery
}

// SendInput is the admin's request to invite someone.
type SendInput struct {
	// Email is required unless Delivery is DeliveryLink, which forbids it on
	// a new invitation.
	Email         string
	Delivery      Delivery
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
// new one, and emails the claim link as input.Delivery asks. With
// DeliveryDefault and no email configured the invitation is still created and
// the claim URL returned for manual delivery.
func (s *Service) Send(ctx context.Context, input SendInput) (*SendResult, error) {
	return s.send(ctx, input, nil)
}

func (s *Service) send(ctx context.Context, input SendInput, sourceID *int64) (*SendResult, error) {
	var email string
	switch input.Delivery {
	case DeliveryLink:
		if strings.TrimSpace(input.Email) == "" {
			break
		}
		// Only a replacement link keeps an address; a new one has none.
		if sourceID == nil {
			return nil, ErrInvalidEmail
		}
		fallthrough
	case DeliveryDefault, DeliveryEmail:
		parsed, err := parseEmail(input.Email)
		if err != nil {
			return nil, err
		}
		email = parsed
	default:
		return nil, ErrInvalidEmail
	}

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
	// Only the server Owner may grant the admin role.
	if role == roleAdmin && (inviter.Role != roleAdmin || !inviter.IsOwner) {
		return nil, ErrRoleNotAllowed
	}
	// Admins are never grouped; refuse here so the pending invitation does not
	// advertise a group that accept would silently drop.
	if role == roleAdmin && input.AccessGroupID != nil {
		return nil, ErrAdminGrouped
	}

	// Refuse addresses that already have an account. The address is also the
	// future username, so both unique columns are checked.
	if email != "" {
		if err := s.checkAddressFree(ctx, email); err != nil {
			return nil, err
		}
	}

	// An explicit email request without a mail sender creates no invitation.
	// Otherwise an address is always attempted: Enabled is false for unreadable
	// or invalid mail settings too, which must surface as a failed send rather
	// than as manual delivery. The send reports "not configured" itself.
	if input.Delivery == DeliveryEmail && !s.mail.Enabled(ctx) {
		return nil, ErrEmailUnavailable
	}
	stored := models.InvitationDeliveryLink
	if email != "" && input.Delivery != DeliveryLink {
		stored = models.InvitationDeliveryEmailUnconfirmed
	}

	linkBase := s.linkBase(ctx)
	if linkBase == "" {
		return nil, ErrNoLinkBase
	}

	token, tokenHash, err := NewToken()
	if err != nil {
		return nil, err
	}

	createInput := models.CreateInvitationInput{
		Email:         email,
		Delivery:      stored,
		Role:          role,
		AccessGroupID: input.AccessGroupID,
		LibraryIDs:    input.LibraryIDs,
		CreateProfile: input.CreateProfile,
		ShowTour:      input.ShowTour,
		Note:          strings.TrimSpace(input.Note),
		InvitedBy:     input.InvitedBy,
		ExpiresAt:     s.now().Add(s.ttl),
	}
	var inv *models.Invitation
	if sourceID == nil {
		inv, err = s.repo.Create(ctx, createInput, tokenHash)
	} else {
		inv, err = s.repo.Resend(ctx, *sourceID, createInput, tokenHash)
	}
	if err != nil {
		return nil, err
	}

	claimURL := linkBase + "/invite/" + token
	result := &SendResult{Invitation: inv, ClaimURL: claimURL, Delivery: input.Delivery}
	if inv.Delivery != models.InvitationDeliveryEmailUnconfirmed {
		return result, nil
	}

	brand := s.brand.Load(ctx)
	content := composeInvitationEmail(
		brand, inviter.Username, s.serverName(ctx), email, claimURL, inv.Note, inv.ExpiresAt, s.now())
	err = s.mail.Send(ctx, mail.Message{
		To:       []string{email},
		Subject:  content.Subject,
		TextBody: content.Text,
		HTMLBody: content.HTML,
		Inline:   brand.InlineImages(),
	})
	switch {
	case err == nil:
		result.EmailSent = true
		s.recordEmailOutcome(ctx, inv, models.InvitationDeliveryEmailSent)
	case errors.Is(err, mail.ErrNotConfigured) && input.Delivery == DeliveryDefault:
		// No mail sender: the admin copies the link instead.
		s.recordEmailOutcome(ctx, inv, models.InvitationDeliveryLink)
	default:
		return result, fmt.Errorf("invitation stored; email delivery failed or is uncertain: %w", err)
	}
	return result, nil
}

// recordEmailOutcome stores the send outcome on the committed invitation. A
// failed write leaves email_unconfirmed, which understates rather than
// misreports delivery, so it does not fail the send.
func (s *Service) recordEmailOutcome(ctx context.Context, inv *models.Invitation, delivery string) {
	if err := s.repo.RecordEmailOutcome(context.WithoutCancel(ctx), inv.ID, delivery); err == nil {
		inv.Delivery = delivery
	}
}

// EmailDeliveryAvailable reports whether invitations can be emailed now.
func (s *Service) EmailDeliveryAvailable(ctx context.Context) bool {
	return s.mail.Enabled(ctx)
}

func parseEmail(raw string) (string, error) {
	parsed, err := netmail.ParseAddress(strings.TrimSpace(raw))
	if err != nil || parsed.Address != strings.TrimSpace(raw) {
		return "", ErrInvalidEmail
	}
	return parsed.Address, nil
}

func (s *Service) checkAddressFree(ctx context.Context, email string) error {
	if _, err := s.users.GetByEmail(ctx, email); err == nil {
		return ErrEmailTaken
	} else if !auth.IsNotFound(err) {
		return fmt.Errorf("checking email: %w", err)
	}
	if _, err := s.users.GetByUsername(ctx, email); err == nil {
		return ErrEmailTaken
	} else if !auth.IsNotFound(err) {
		return fmt.Errorf("checking username: %w", err)
	}
	return nil
}

// Resend supersedes an invitation with a fresh token to the same address,
// re-using the original access choices. The old link stops working. The
// resending admin becomes the inviter of record. delivery works as for Send:
// DeliveryLink returns the new link and emails nothing, keeping any address.
// A link invitation has no address, so it always gets a new link and
// DeliveryEmail is refused with ErrNoAddress.
func (s *Service) Resend(ctx context.Context, id, resentBy int64, delivery Delivery) (*SendResult, error) {
	prior, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if prior.Email == "" {
		if delivery == DeliveryEmail {
			return nil, ErrNoAddress
		}
		delivery = DeliveryLink
	}
	return s.send(ctx, SendInput{
		Email:         prior.Email,
		Delivery:      delivery,
		Role:          prior.Role,
		AccessGroupID: prior.AccessGroupID,
		LibraryIDs:    prior.LibraryIDs,
		CreateProfile: prior.CreateProfile,
		ShowTour:      prior.ShowTour,
		Note:          prior.Note,
		InvitedBy:     resentBy,
	}, &id)
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
	// Email is empty for a link invitation; EmailRequired then tells the
	// claim screen to ask for an address.
	Email         string
	EmailRequired bool
	Note          string
	InviterName   string
	ServerName    string
	ExpiresAt     time.Time
	ShowTour      bool
	CreateProfile bool
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
		Email:         inv.Email,
		EmailRequired: inv.Email == "",
		Note:          inv.Note,
		InviterName:   inv.InvitedByName,
		ServerName:    s.serverName(ctx),
		ExpiresAt:     inv.ExpiresAt,
		ShowTour:      inv.ShowTour,
		CreateProfile: inv.CreateProfile,
	}, nil
}

// Accept commits the account, requested profile, and invitation claim together.
// Login is a separate post-commit effect: its failure never removes the account
// or makes the token reusable. A non-nil user with ErrSessionStart reports this
// committed outcome so callers can direct the invitee to ordinary sign-in.
//
// email is the address the invitee entered. A link invitation requires it and
// the account takes it; an emailed invitation ignores it.
func (s *Service) Accept(ctx context.Context, token, email, password, deviceName, ip string) (*auth.TokenPair, *models.User, error) {
	if strings.TrimSpace(token) == "" {
		return nil, nil, ErrNotFound
	}
	linkInvitation := false
	var entered string
	linkAddress := func() (string, error) {
		linkInvitation = true
		if strings.TrimSpace(email) == "" {
			return "", ErrEmailRequired
		}
		// The same rule as signup and the administrator create form.
		address, err := auth.ValidateEmail(email)
		if err != nil {
			return "", ErrInvalidEmail
		}
		entered = address
		return address, nil
	}
	user, err := s.repo.AcceptAs(ctx, HashToken(token), linkAddress, func(inv *models.Invitation, tx pgx.Tx) (*models.User, error) {
		address := inv.Email
		if address == "" {
			address = entered
		}
		return s.accounts.CreateAccountInTransaction(ctx, tx, auth.CreateAccountInput{
			User:           models.CreateUserInput{Username: address, Email: address, Password: password, Role: inv.Role, LibraryIDs: inv.LibraryIDs, AccessGroupID: inv.AccessGroupID},
			DefaultProfile: auth.DefaultProfileOptions{Enabled: inv.CreateProfile, Name: profileNameFromEmail(address)},
		})
	})
	if err != nil {
		if auth.IsDuplicate(err) {
			// The invitee chose a link invitation's address: it is theirs to
			// change. An emailed invitation's address was free at send.
			if linkInvitation {
				return nil, nil, ErrEmailTaken
			}
			return nil, nil, ErrNotClaimable
		}
		return nil, nil, err
	}
	pair, loggedIn, err := s.sessions.Login(ctx, user.Username, password, deviceName, ip)
	if err != nil {
		return nil, user, errors.Join(ErrSessionStart, err)
	}
	return pair, loggedIn, nil
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

// linkBase resolves the canonical externally reachable base URL for claim links.
func (s *Service) linkBase(ctx context.Context) string {
	return mail.AccountLinkBase(ctx, s.settings, s.publicURL)
}

// serverName reads the branded server name for email copy and the claim
// screen, defaulting to "Silo".
func (s *Service) serverName(ctx context.Context) string {
	return mail.ServerName(ctx, s.settings)
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

// SupportsDefaultProfile reports capability, not transient storage health.
func (s *Service) SupportsDefaultProfile() bool {
	p, ok := s.accounts.(interface{ SupportsTransactionalProfiles() bool })
	return ok && p.SupportsTransactionalProfiles()
}
func (s *Service) ListPage(ctx context.Context, after *PageKey, limit int) ([]*models.Invitation, bool, error) {
	return s.repo.ListPage(ctx, after, limit)
}
func (s *Service) GetByID(ctx context.Context, id int64) (*models.Invitation, error) {
	return s.repo.GetByID(ctx, id)
}

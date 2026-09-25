package auth

import (
	"context"
	"errors"
	"fmt"
	netmail "net/mail"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Sentinel errors for authentication operations.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUserDisabled       = errors.New("user account is disabled")
)

// Credentials holds the username and password for authentication.
type Credentials struct {
	Username string
	Password string
}

// AuthProvider is the interface for pluggable authentication backends.
// Implementations validate user credentials and session state.
type AuthProvider interface {
	// Authenticate validates the given credentials and returns the
	// authenticated user. Returns ErrInvalidCredentials if the username
	// does not exist or the password is wrong. Returns ErrUserDisabled
	// if the account is disabled.
	Authenticate(ctx context.Context, credentials Credentials) (*models.User, error)

	// ValidateSession checks whether the given session ID is still valid
	// (exists, not revoked, not expired).
	ValidateSession(ctx context.Context, sessionID string) (bool, error)
}

// LocalProvider authenticates users against the local PostgreSQL database
// using bcrypt password hashing.
type LocalProvider struct {
	users    *UserRepository
	sessions *SessionRepository
}

// looksLikeEmail reports whether the login identifier is a plausible bare
// email address, gating the email-column fallback in Authenticate.
func looksLikeEmail(identifier string) bool {
	identifier = strings.TrimSpace(identifier)
	if !strings.Contains(identifier, "@") {
		return false
	}
	parsed, err := netmail.ParseAddress(identifier)
	return err == nil && parsed.Address == identifier
}

// LoginDirectory is the account lookup behind a typed sign-in name.
// Satisfied by *UserRepository.
type LoginDirectory interface {
	GetByUsername(ctx context.Context, username string) (*models.User, error)
	GetByEmail(ctx context.Context, email string) (*models.User, error)
}

// LookupLogin resolves what someone types as their sign-in name to an
// account. The identifier may also be an email address: when the username
// lookup misses and the input parses as an email, the email column is tried.
// Invited accounts have username == email, but someone who signed up with a
// separate username should still be able to type the address they remember.
// Both columns are citext UNIQUE, and the user_login_identifiers table keeps
// them one identity space (no username equals another account's email), so
// the fallback cannot resolve ambiguously for accounts written since then.
func LookupLogin(ctx context.Context, users LoginDirectory, identifier string) (*models.User, error) {
	user, err := users.GetByUsername(ctx, identifier)
	if err != nil && IsNotFound(err) && looksLikeEmail(identifier) {
		user, err = users.GetByEmail(ctx, identifier)
	}
	return user, err
}

// NewLocalProvider creates a new LocalProvider backed by the given repositories.
func NewLocalProvider(users *UserRepository, sessions *SessionRepository) *LocalProvider {
	return &LocalProvider{
		users:    users,
		sessions: sessions,
	}
}

// Authenticate validates the username/password pair against the database.
// Returns ErrInvalidCredentials if the user is not found or the password
// does not match. Returns ErrUserDisabled if the user's account is disabled.
// The username may also be the account's email address (see LookupLogin).
func (p *LocalProvider) Authenticate(ctx context.Context, creds Credentials) (*models.User, error) {
	user, err := LookupLogin(ctx, p.users, creds.Username)
	if err != nil {
		if IsNotFound(err) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("looking up user: %w", err)
	}
	if !user.LocalPasswordLoginEnabled {
		return nil, ErrInvalidCredentials
	}

	if !CheckPassword(user, creds.Password) {
		return nil, ErrInvalidCredentials
	}

	if !user.Enabled {
		return nil, ErrUserDisabled
	}

	return user, nil
}

// ValidateSession checks whether the session identified by sessionID is
// currently valid (exists, not revoked, not expired).
func (p *LocalProvider) ValidateSession(ctx context.Context, sessionID string) (bool, error) {
	return p.sessions.IsValid(ctx, sessionID)
}

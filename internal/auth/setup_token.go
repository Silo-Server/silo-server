package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/streamlocation"
)

// First-run setup is open to a client on the local network. Anyone else must
// present a setup token: each API process that boots while no account exists
// prints one to its log and stores its SHA-256 in setup_tokens, so whichever
// replica answers the setup request can check it. Completing setup deletes
// every stored token, and a process deletes its own on graceful shutdown. A
// process that dies without shutting down leaves its token valid until setup
// completes.
var (
	// ErrSetupTokenRequired means a client outside the local network tried
	// setup without a token.
	ErrSetupTokenRequired = errors.New("setup token required")
	// ErrSetupTokenInvalid means the presented token matches no live token.
	ErrSetupTokenInvalid = errors.New("setup token invalid")
)

// SetupTokenRequired reports whether a setup request carried by ctx must
// present a setup token: it does unless the resolved client address is on
// the local network. An absent or unparsable address, or a request that came
// through a network-access provider, needs the token.
func SetupTokenRequired(ctx context.Context) bool {
	return streamlocation.FromContext(ctx) != streamlocation.Local
}

// SetupAccess is how a caller qualifies for first-run setup.
type SetupAccess struct {
	// Local is true when the request came from the local network.
	Local bool
	// Token is the setup token the caller presented, if any.
	Token string
}

func setupTokenHash(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(token))))
}

// IssueSetupToken stores a new setup token when no account exists yet and
// returns it so the caller can print it; it returns "" once setup is done.
// It stores the token under the setup lock, so a token is never stored after
// setup commits. A server that is already set up answers from one read
// without waiting on the lock.
func (r *UserRepository) IssueSetupToken(ctx context.Context) (string, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users)").Scan(&exists); err != nil {
		return "", fmt.Errorf("checking for accounts: %w", err)
	}
	if exists {
		return "", nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("beginning setup token issue: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := lockEmptyInitialSetup(ctx, tx); err != nil {
		if errors.Is(err, ErrSetupAlreadyComplete) {
			return "", nil
		}
		return "", err
	}
	token := rand.Text()
	hash := setupTokenHash(token)
	if _, err := tx.Exec(ctx, "INSERT INTO setup_tokens (token_hash) VALUES ($1)", hash[:]); err != nil {
		return "", fmt.Errorf("storing setup token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("committing setup token: %w", err)
	}
	return token, nil
}

// RevokeSetupToken deletes one setup token, for a process that is shutting
// down and takes the token it printed with it.
func (r *UserRepository) RevokeSetupToken(ctx context.Context, token string) error {
	hash := setupTokenHash(token)
	if _, err := r.pool.Exec(ctx, "DELETE FROM setup_tokens WHERE token_hash = $1", hash[:]); err != nil {
		return fmt.Errorf("revoking setup token: %w", err)
	}
	return nil
}

// checkSetupAccess runs inside the setup transaction. Every stored hash is
// compared in constant time so the answer does not depend on which row, if
// any, matched.
func checkSetupAccess(ctx context.Context, tx pgx.Tx, access SetupAccess) error {
	if access.Local {
		return nil
	}
	if strings.TrimSpace(access.Token) == "" {
		return ErrSetupTokenRequired
	}
	presented := setupTokenHash(access.Token)
	rows, _ := tx.Query(ctx, "SELECT token_hash FROM setup_tokens")
	stored, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return fmt.Errorf("reading setup tokens: %w", err)
	}
	match := 0
	for _, hash := range stored {
		match |= subtle.ConstantTimeCompare(hash, presented[:])
	}
	if match != 1 {
		return ErrSetupTokenInvalid
	}
	return nil
}

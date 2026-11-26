package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/appkeys"
)

// ErrKeyChallengeInvalid refuses a challenge that is unknown, already spent
// or expired.
var ErrKeyChallengeInvalid = errors.New("app key challenge unknown, used or expired")

// KeyChallenges issues and spends the challenges app key requests are signed
// on. They live in Postgres, not in a node's memory, so a challenge issued by
// one API process is spent once on any of them.
type KeyChallenges struct {
	pool *pgxpool.Pool
}

// NewKeyChallenges returns the challenge store on pool.
func NewKeyChallenges(pool *pgxpool.Pool) *KeyChallenges {
	return &KeyChallenges{pool: pool}
}

// Issue stores a new challenge and returns it as base64url. It expires
// appkeys.ChallengeLifetime from the database's clock, the clock that spends
// it.
func (c *KeyChallenges) Issue(ctx context.Context) (string, error) {
	raw := make([]byte, appkeys.ChallengeSize)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating app key challenge: %w", err)
	}
	sum := sha256.Sum256(raw)
	if _, err := c.pool.Exec(ctx, `INSERT INTO auth_key_challenges (challenge_hash, expires_at)
		VALUES ($1, NOW() + make_interval(secs => $2))`, sum[:], appkeys.ChallengeLifetime.Seconds()); err != nil {
		return "", fmt.Errorf("storing app key challenge: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Spend uses up a raw challenge. Deleting the row is the redemption, so of
// two requests carrying one challenge, on any nodes, at most one succeeds.
// An expired challenge is deleted and refused.
func (c *KeyChallenges) Spend(ctx context.Context, raw []byte) error {
	if len(raw) != appkeys.ChallengeSize {
		return ErrKeyChallengeInvalid
	}
	sum := sha256.Sum256(raw)
	var live bool
	err := c.pool.QueryRow(ctx, `DELETE FROM auth_key_challenges WHERE challenge_hash = $1
		RETURNING expires_at > NOW()`, sum[:]).Scan(&live)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrKeyChallengeInvalid
	}
	if err != nil {
		return fmt.Errorf("spending app key challenge: %w", err)
	}
	if !live {
		return ErrKeyChallengeInvalid
	}
	return nil
}

// grantUseRetention is how long a used grant's ID is kept past the grant's
// exp. The grant's exp is judged on the API process's clock and the row is
// deleted on the database's, so the margin covers a database clock that runs
// ahead: the row is gone only once every process refuses the grant by its
// exp.
const grantUseRetention = time.Hour

// DeleteExpiredChallenges removes challenges that expired unspent, the IDs
// of used grants past their exp and grantUseRetention, and app codes and
// sign-in links past their expiry, spent or not, for the retention step. A spent challenge has
// no row left. The challenge and grant deletes may wait, but no request holds
// one of their rows while it waits on anything: a challenge is spent by one
// statement, and a grant redemption holds only the fresh row it inserted.
// The app code delete skips rows a request holds. A sign-in link's row is
// held by a request that already holds its account and waits on nothing
// the delete holds.
func (c *KeyChallenges) DeleteExpiredChallenges(ctx context.Context) (int, error) {
	tag, err := c.pool.Exec(ctx, `DELETE FROM auth_key_challenges WHERE expires_at < NOW()`)
	if err != nil {
		return 0, fmt.Errorf("deleting expired app key challenges: %w", err)
	}
	grants, err := c.pool.Exec(ctx, `DELETE FROM auth_grant_uses WHERE expires_at < NOW() - make_interval(secs => $1)`,
		grantUseRetention.Seconds())
	if err != nil {
		return int(tag.RowsAffected()), fmt.Errorf("deleting expired grant uses: %w", err)
	}
	// Never waits: issuing a code deletes its invitation's own code and then
	// other expired codes for the name, holding the first while it waits on
	// the second, so a cleanup that waited could close a cycle with it. A
	// row it skips is being deleted by that request, or goes next hour.
	codes, err := c.pool.Exec(ctx, `DELETE FROM auth_app_codes WHERE code_hash IN (
		SELECT code_hash FROM auth_app_codes WHERE expires_at < NOW() FOR UPDATE SKIP LOCKED)`)
	if err != nil {
		return int(tag.RowsAffected() + grants.RowsAffected()), fmt.Errorf("deleting expired app codes: %w", err)
	}
	// A link past its expiry is dead, redeemed or not: its set-password
	// token never outlives it.
	links, err := c.pool.Exec(ctx, `DELETE FROM auth_sign_in_links WHERE expires_at < NOW()`)
	if err != nil {
		return int(tag.RowsAffected() + grants.RowsAffected() + codes.RowsAffected()), fmt.Errorf("deleting expired sign-in links: %w", err)
	}
	return int(tag.RowsAffected() + grants.RowsAffected() + codes.RowsAffected() + links.RowsAffected()), nil
}

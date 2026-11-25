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
// of used grants past their exp and grantUseRetention, and app codes past
// their expiry, spent or not, for the retention step. A spent challenge has
// no row left.
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
	codes, err := c.pool.Exec(ctx, `DELETE FROM auth_app_codes WHERE expires_at < NOW()`)
	if err != nil {
		return int(tag.RowsAffected() + grants.RowsAffected()), fmt.Errorf("deleting expired app codes: %w", err)
	}
	return int(tag.RowsAffected() + grants.RowsAffected() + codes.RowsAffected()), nil
}

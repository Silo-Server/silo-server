package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// ResetPasswordInTransaction replaces a local password on behalf of a
// password reset owned by the caller's transaction. The account chose the new
// password, so it also settles any temporary one. Every login session the
// account holds, or impersonates from, is revoked in the same transaction, as
// are its Audiobookshelf-compatible sessions and any device sign-in it
// approved that has not been collected yet: whoever held the old password is
// signed out everywhere. Personal API keys are credentials the account
// created on purpose and survive; see docs/architecture/password-resets.md.
func ResetPasswordInTransaction(ctx context.Context, tx pgx.Tx, userID int, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE users
		SET password_hash = $2, password_change_required = false, updated_at = NOW()
		WHERE id = $1`, userID, string(hash))
	if err != nil {
		return fmt.Errorf("resetting password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `
		UPDATE auth_sessions SET revoked_at = NOW()
		WHERE (user_id = $1 OR impersonator_user_id = $1) AND revoked_at IS NULL`, userID); err != nil {
		return fmt.Errorf("revoking sessions after password reset: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE abs_sessions SET revoked_at = NOW()
		WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
		return fmt.Errorf("revoking Audiobookshelf sessions after password reset: %w", err)
	}
	// An approved code would otherwise mint a fresh session when its device
	// next polls, after the reset.
	if _, err := tx.Exec(ctx, `
		UPDATE device_login_requests SET status = $2, updated_at = NOW()
		WHERE approved_by_user_id = $1 AND status = $3`,
		userID, DeviceLoginStatusDenied, DeviceLoginStatusApproved); err != nil {
		return fmt.Errorf("withdrawing device sign-in approvals after password reset: %w", err)
	}
	return nil
}

// NewLinkToken mints a raw bearer token for an emailed or copied account link
// (invitation claim, password reset) and its SHA-256 hex digest for at-rest
// storage. Only the digest is stored, so a database dump yields no usable link.
func NewLinkToken() (token, tokenHash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashLinkToken(token), nil
}

// HashLinkToken returns the at-rest digest of a link token.
func HashLinkToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

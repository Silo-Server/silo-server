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
// password, so it also settles any temporary one, and every sign-in is
// revoked in the same transaction (RevokeSignInsInTransaction): whoever held
// the old password is signed out everywhere.
func ResetPasswordInTransaction(ctx context.Context, tx pgx.Tx, userID int, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), passwordHashCost)
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
	return RevokeSignInsInTransaction(ctx, tx, userID)
}

// RevokeSignInsInTransaction signs an account out everywhere within the
// caller's transaction: every login session it holds or impersonates from,
// its Audiobookshelf- and Jellyfin-compatible sessions, and any device sign-in
// it approved that the device has not collected yet, which would otherwise
// mint a fresh session afterwards. Personal API keys are credentials the
// account created on purpose and survive; see
// docs/architecture/password-resets.md. Callers run the OnUserSessionsRevoked
// hook after commit so every replica drops the Jellyfin-compatible sessions it
// holds in memory.
func RevokeSignInsInTransaction(ctx context.Context, tx pgx.Tx, userID int) error {
	return RevokeSignInsForUsersInTransaction(ctx, tx, []int{userID})
}

// RevokeSignInsForUsersInTransaction is RevokeSignInsInTransaction for several
// accounts at once, in a fixed number of statements however many there are.
func RevokeSignInsForUsersInTransaction(ctx context.Context, tx pgx.Tx, userIDs []int) error {
	if len(userIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE auth_sessions SET revoked_at = NOW()
		WHERE (user_id = ANY($1::int[]) OR impersonator_user_id = ANY($1::int[])) AND revoked_at IS NULL`, userIDs); err != nil {
		return fmt.Errorf("revoking login sessions: %w", err)
	}
	if err := revokeAudiobookshelfSessionsInTransaction(ctx, tx, userIDs); err != nil {
		return err
	}
	if err := deleteJellyfinSessionsInTransaction(ctx, tx, userIDs); err != nil {
		return err
	}
	return withdrawDeviceSignInApprovalsInTransaction(ctx, tx, userIDs)
}

func revokeAudiobookshelfSessionsInTransaction(ctx context.Context, tx pgx.Tx, userIDs []int) error {
	if _, err := tx.Exec(ctx, `
		UPDATE abs_sessions SET revoked_at = NOW()
		WHERE user_id = ANY($1::int[]) AND revoked_at IS NULL`, userIDs); err != nil {
		return fmt.Errorf("revoking Audiobookshelf sessions: %w", err)
	}
	return nil
}

// deleteJellyfinSessionsInTransaction deletes the accounts' stored
// Jellyfin-compatible sessions, so no replica can load one again once the
// transaction commits. Copies a replica already holds in memory stay usable
// until the OnUserSessionsRevoked hook drops them.
func deleteJellyfinSessionsInTransaction(ctx context.Context, tx pgx.Tx, userIDs []int) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM jellycompat_sessions WHERE streamapp_user_id = ANY($1::int[])`, userIDs); err != nil {
		return fmt.Errorf("deleting Jellyfin-compatible sessions: %w", err)
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

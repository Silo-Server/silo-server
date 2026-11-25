package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auth/passwordhash"
)

// ResetPasswordInTransaction replaces a local password on behalf of a
// password reset owned by the caller's transaction. The account chose the new
// password, so it also settles any temporary one, and every sign-in is
// revoked in the same transaction (RevokeSignInsInTransaction): whoever held
// the old password is signed out everywhere.
func ResetPasswordInTransaction(ctx context.Context, tx pgx.Tx, userID int, newPassword string) error {
	hash, err := passwordhash.Generate(ctx, []byte(newPassword), passwordHashCost)
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
	return revokeSignInsInTransaction(ctx, tx, userID, nil)
}

// RevokeOtherSignInsInTransaction is RevokeSignInsInTransaction except that
// keepSessionID, the login session asking for it, stays signed in.
func RevokeOtherSignInsInTransaction(ctx context.Context, tx pgx.Tx, userID int, keepSessionID string) error {
	return revokeSignInsInTransaction(ctx, tx, userID, &keepSessionID)
}

// revokeSignInsInTransaction revokes every login session except
// keepSessionID; nil keeps none. It takes one account: an uninstall deletes
// an installation's identities in an order of its own, so a transaction that
// locked several accounts' identities at once could meet it halfway.
func revokeSignInsInTransaction(ctx context.Context, tx pgx.Tx, userID int, keepSessionID *string) error {
	userIDs := []int{userID}
	// Identities, device sign-in requests, then sessions: the order a plugin
	// uninstall takes them (see lockIdentitiesBeforeSessions). Every caller
	// holds the account row, which approving and collecting a device sign-in
	// also lock first, so withdrawing the approvals before the sessions end
	// leaves no window for a session to approve or a device to collect.
	if err := lockIdentitiesBeforeSessions(ctx, tx, userID, false); err != nil {
		return err
	}
	if _, err := withdrawDeviceSignInApprovalsInTransaction(ctx, tx, userIDs); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE auth_sessions SET revoked_at = NOW()
		WHERE (user_id = ANY($1::int[]) OR impersonator_user_id = ANY($1::int[])) AND revoked_at IS NULL
		  AND id IS DISTINCT FROM $2::text`, userIDs, keepSessionID); err != nil {
		return fmt.Errorf("revoking login sessions: %w", err)
	}
	if _, err := revokeAudiobookshelfSessionsInTransaction(ctx, tx, userIDs); err != nil {
		return err
	}
	_, err := deleteJellyfinSessionsInTransaction(ctx, tx, userIDs)
	return err
}

func revokeAudiobookshelfSessionsInTransaction(ctx context.Context, tx pgx.Tx, userIDs []int) (int64, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE abs_sessions SET revoked_at = NOW()
		WHERE user_id = ANY($1::int[]) AND revoked_at IS NULL`, userIDs)
	if err != nil {
		return 0, fmt.Errorf("revoking Audiobookshelf sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// deleteJellyfinSessionsInTransaction deletes the accounts' stored
// Jellyfin-compatible sessions, so no replica can load one again once the
// transaction commits. Persistent compatibility lookups check the stored row;
// the OnUserSessionsRevoked hook also discards replicas' cached copies.
func deleteJellyfinSessionsInTransaction(ctx context.Context, tx pgx.Tx, userIDs []int) (int64, error) {
	tag, err := tx.Exec(ctx, `
		DELETE FROM jellycompat_sessions WHERE streamapp_user_id = ANY($1::int[])`, userIDs)
	if err != nil {
		return 0, fmt.Errorf("deleting Jellyfin-compatible sessions: %w", err)
	}
	return tag.RowsAffected(), nil
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

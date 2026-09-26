package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrEmailVerificationConflict    = errors.New("verification intent conflicts with its original owner or address")
	ErrEmailVerificationUnavailable = errors.New("durable email verification is unavailable")
	ErrEmailLegacyWriter            = errors.New("this profile requires durable email verification")
)

// EmailVerificationIntent is a retained domain request. The caller must capture
// ID, account, profile and address before sending, and reuse them after uncertainty.
// ProfileName and LinkBase are server observations used only for a new message.
type EmailVerificationIntent struct {
	ID          string
	UserID      int
	ProfileID   string
	Address     string
	ProfileName string
	LinkBase    string
}

// EmailVerificationReceipt describes durable admission, never provider delivery.
// Current is false after expiry, clear, verification or a newer pending address.
type EmailVerificationReceipt struct {
	ID        string
	ExpiresAt time.Time
	Current   bool
}

func emailVerificationAAD(id string) string { return "notification-email-verification:" + id }

// QueueVerification commits the pending token and its encrypted message in one
// transaction. It does not send, claim work, or decide ambiguous SMTP retries.
func (r *EmailPrefsRepository) QueueVerification(ctx context.Context, in EmailVerificationIntent, cipher *secret.Cipher) (EmailVerificationReceipt, error) {
	empty := EmailVerificationReceipt{}
	if r == nil || r.pool == nil || cipher == nil {
		return empty, ErrEmailVerificationUnavailable
	}
	id, err := uuid.Parse(in.ID)
	if err != nil || id.String() != in.ID || in.UserID <= 0 || strings.TrimSpace(in.ProfileID) == "" {
		return empty, ErrEmailInvalidAddress
	}
	parsed, err := netmail.ParseAddress(strings.TrimSpace(in.Address))
	if err != nil || parsed.Address != strings.TrimSpace(in.Address) {
		return empty, ErrEmailInvalidAddress
	}
	address := parsed.Address
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockEmailVerificationProfile(ctx, tx, in.UserID, in.ProfileID); err != nil {
		return empty, err
	}
	var owner int
	var currentHash string
	var lastSent *time.Time
	var sendsToday int
	err = tx.QueryRow(ctx, `SELECT user_id,pending_token_hash,pending_last_sent_at,verify_sends_today FROM notification_email_prefs WHERE profile_id=$1`, in.ProfileID).Scan(&owner, &currentHash, &lastSent, &sendsToday)
	if err != nil {
		return empty, err
	}
	if owner != in.UserID {
		return empty, ErrEmailVerificationConflict
	}
	var storedUser int
	var storedProfile, storedAddress, storedHash string
	result := EmailVerificationReceipt{ID: in.ID}
	err = tx.QueryRow(ctx, `SELECT user_id,profile_id,address_hash,pending_token_hash,expires_at FROM notification_email_verifications WHERE id=$1`, in.ID).Scan(&storedUser, &storedProfile, &storedAddress, &storedHash, &result.ExpiresAt)
	if err == nil {
		if storedUser != in.UserID || storedProfile != in.ProfileID || storedAddress != hashEmailToken(address) {
			return empty, ErrEmailVerificationConflict
		}
		result.Current = currentHash == storedHash && result.ExpiresAt.After(time.Now())
		return result, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	if in.LinkBase == "" {
		return empty, ErrEmailNoLinkBase
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if lastSent != nil && now.Sub(*lastSent) < emailVerifyMinInterval {
		return empty, ErrEmailVerifyRateLimited
	}
	if lastSent == nil || !sameUTCDay(*lastSent, now) {
		sendsToday = 0
	}
	if sendsToday >= emailVerifyDailyCap {
		return empty, ErrEmailVerifyRateLimited
	}
	used, err := addressInUse(ctx, tx, address, in.ProfileID, in.UserID)
	if err != nil {
		return empty, err
	}
	if used {
		return empty, ErrEmailAddressInUse
	}
	token, hash, err := newEmailToken()
	if err != nil {
		return empty, err
	}
	result.ExpiresAt = now.Add(emailVerifyTTL)
	result.Current = true
	content := composeVerificationEmail(in.ProfileName, strings.TrimRight(in.LinkBase, "/")+"/api/v2/notifications/email/verify?token="+token)
	payload, err := json.Marshal(mail.Message{To: []string{address}, Subject: content.Subject, TextBody: content.Text, HTMLBody: content.HTML})
	if err != nil {
		return empty, err
	}
	encrypted, err := cipher.Encrypt(string(payload), emailVerificationAAD(in.ID))
	if err != nil {
		return empty, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO notification_email_verifications(id,user_id,profile_id,address_hash,pending_token_hash,expires_at,payload_ciphertext) VALUES($1,$2,$3,$4,$5,$6,$7)`, in.ID, in.UserID, in.ProfileID, hashEmailToken(address), hash, result.ExpiresAt, encrypted)
	if isUniqueViolation(err) {
		return empty, ErrEmailVerificationConflict
	}
	if err != nil {
		return empty, err
	}
	_, err = tx.Exec(ctx, `UPDATE notification_email_prefs SET pending_email=$2,pending_token_hash=$3,pending_expires_at=$4,pending_last_sent_at=$5,verify_sends_today=$6,updated_at=$5 WHERE profile_id=$1`, in.ProfileID, address, hash, result.ExpiresAt, now, sendsToday+1)
	if err != nil {
		return empty, err
	}
	return result, tx.Commit(ctx)
}

// Lock before inspecting either rate state or retained intent. Inserting the
// default row also serializes first requests, where SELECT alone has no row.
func lockEmailVerificationProfile(ctx context.Context, tx pgx.Tx, user int, profile string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO notification_email_prefs(profile_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, profile, user); err != nil {
		return err
	}
	var owner int
	if err := tx.QueryRow(ctx, `SELECT user_id FROM notification_email_prefs WHERE profile_id=$1 FOR UPDATE`, profile).Scan(&owner); err != nil {
		return fmt.Errorf("lock email verification profile: %w", err)
	}
	return nil
}

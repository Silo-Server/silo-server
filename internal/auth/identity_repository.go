package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// LinkedIdentity is one external identity linked to a Silo account: the
// plugin installation, the provider's exact subject, and what the provider
// last said about the person.
type LinkedIdentity struct {
	ID              int64
	InstallationID  int
	ExternalSubject string
	UserID          int
	Issuer          string
	Username        string
	Email           string
	DisplayName     string
	LinkedAt        time.Time
	LastSignInAt    *time.Time
	// LastCheckedAt and LastCheckStatus are the latest provider answer about
	// the identity: a sign-in (active) or a re-check (see ProviderRecheck).
	// Nil and "" before the first.
	LastCheckedAt   *time.Time
	LastCheckStatus string
}

type dbQuerier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

const identityColumns = `id, plugin_installation_id, external_subject, user_id, issuer, username, email,
	display_name, linked_at, last_sign_in_at, last_checked_at, last_check_status`

func scanIdentity(row pgx.Row) (*LinkedIdentity, error) {
	var identity LinkedIdentity
	if err := row.Scan(&identity.ID, &identity.InstallationID, &identity.ExternalSubject, &identity.UserID,
		&identity.Issuer, &identity.Username, &identity.Email, &identity.DisplayName,
		&identity.LinkedAt, &identity.LastSignInAt, &identity.LastCheckedAt, &identity.LastCheckStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrIdentityNotFound
		}
		return nil, fmt.Errorf("scanning external identity: %w", err)
	}
	return &identity, nil
}

// lockExternalSubject serializes every write about one (installation,
// subject) pair across nodes for the rest of the transaction, so two first
// sign-ins of the same person cannot both create an account.
func lockExternalSubject(ctx context.Context, tx pgx.Tx, installationID int, subject string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		fmt.Sprintf("silo:auth-identity:%d:%s", installationID, subject)); err != nil {
		return fmt.Errorf("locking external identity: %w", err)
	}
	return nil
}

func identityBySubject(ctx context.Context, db dbQuerier, installationID int, subject string) (*LinkedIdentity, error) {
	return scanIdentity(db.QueryRow(ctx, `SELECT `+identityColumns+`
		FROM plugin_auth_identities
		WHERE plugin_installation_id = $1 AND external_subject = $2`, installationID, subject))
}

func accountHasIdentityAt(ctx context.Context, db dbQuerier, userID, installationID int) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM plugin_auth_identities WHERE user_id = $1 AND plugin_installation_id = $2)`,
		userID, installationID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking linked identities: %w", err)
	}
	return exists, nil
}

func listIdentitiesForUser(ctx context.Context, db dbQuerier, userID int) ([]LinkedIdentity, error) {
	rows, err := db.Query(ctx, `SELECT `+identityColumns+`
		FROM plugin_auth_identities WHERE user_id = $1 ORDER BY linked_at, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("listing external identities: %w", err)
	}
	defer rows.Close()
	identities := []LinkedIdentity{}
	for rows.Next() {
		identity, err := scanIdentity(rows)
		if err != nil {
			return nil, err
		}
		identities = append(identities, *identity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing external identities: %w", err)
	}
	return identities, nil
}

// usableIdentityCount counts links whose installation and sign-in binding
// are enabled. A disabled provider cannot be a fallback after unlinking.
func usableIdentityCount(ctx context.Context, db rowQuerier, userID int, excludeID int64) (int, error) {
	var count int
	err := db.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_identities i
		JOIN plugin_installations p ON p.id = i.plugin_installation_id AND p.enabled
		WHERE i.user_id = $1 AND i.id <> $2 AND EXISTS (
			SELECT 1 FROM plugin_auth_bindings b WHERE b.plugin_installation_id = p.id AND b.enabled)`,
		userID, excludeID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting enabled sign-in identities: %w", err)
	}
	return count, nil
}

// insertIdentity writes a new link. signedIn records the sign-in that
// created it, which is also a provider check that answered active; an
// administrator link has neither yet.
func insertIdentity(ctx context.Context, db dbQuerier, userID, installationID int, identity ExternalIdentity, signedIn bool) (*LinkedIdentity, error) {
	return scanIdentity(db.QueryRow(ctx, `
		INSERT INTO plugin_auth_identities (
			plugin_installation_id, external_subject, user_id, issuer, username, email, display_name,
			linked_at, last_sign_in_at, last_checked_at, last_check_status, last_authenticated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), CASE WHEN $8 THEN NOW() END, CASE WHEN $8 THEN NOW() END,
			CASE WHEN $8 THEN $9 ELSE '' END, CASE WHEN $8 THEN NOW() END)
		RETURNING `+identityColumns,
		installationID, identity.Subject, userID, identity.Issuer, identity.Username, identity.Email,
		identity.DisplayName, signedIn, CheckStatusActive))
}

// recordIdentitySignIn refreshes what the provider said about the person
// and stamps the sign-in. The provider just vouched for the person, so it
// also counts as a check that answered active: the next re-check is due one
// interval from now. It also supersedes a pending refusal.
func recordIdentitySignIn(ctx context.Context, db dbQuerier, id int64, identity ExternalIdentity) error {
	if _, err := db.Exec(ctx, `
		UPDATE plugin_auth_identities
		SET issuer = $2, username = $3, email = $4, display_name = $5,
			last_sign_in_at = NOW(), last_checked_at = NOW(), last_check_status = $6,
			last_authenticated_at = NOW(), pending_refusal = '', updated_at = NOW()
		WHERE id = $1`,
		id, identity.Issuer, identity.Username, identity.Email, identity.DisplayName, CheckStatusActive); err != nil {
		return fmt.Errorf("recording external sign-in: %w", err)
	}
	return nil
}

func deleteIdentity(ctx context.Context, db dbQuerier, userID int, identityID int64) (*LinkedIdentity, error) {
	return scanIdentity(db.QueryRow(ctx, `
		DELETE FROM plugin_auth_identities WHERE id = $1 AND user_id = $2
		RETURNING `+identityColumns, identityID, userID))
}

func identityByID(ctx context.Context, db dbQuerier, id int64) (*LinkedIdentity, error) {
	return scanIdentity(db.QueryRow(ctx, `SELECT `+identityColumns+` FROM plugin_auth_identities WHERE id = $1`, id))
}

func identityForUser(ctx context.Context, db dbQuerier, userID int, identityID int64) (*LinkedIdentity, error) {
	return scanIdentity(db.QueryRow(ctx, `SELECT `+identityColumns+`
		FROM plugin_auth_identities WHERE id = $1 AND user_id = $2 FOR UPDATE`, identityID, userID))
}

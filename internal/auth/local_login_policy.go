package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

var (
	// ErrLocalLoginDisabled refuses a correct local password because the
	// server turned local password sign-in off and the account is not a
	// break-glass admin.
	ErrLocalLoginDisabled = errors.New("local password sign-in is turned off on this server")
	// ErrBreakGlassRequired refuses a change that would leave the server with
	// local password sign-in off and no enabled break-glass admin that can
	// still use its password.
	ErrBreakGlassRequired = errors.New("at least one enabled break-glass admin with password sign-in is required while local password sign-in is off")
)

// rowQuerier is what the policy reads need: a pool or a transaction.
type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// The policy settings are plain (never encrypted) server_settings rows, read
// straight from the database on every decision so that a change made on any
// node, or with the CLI, applies at once everywhere.
func readServerSetting(ctx context.Context, db rowQuerier, key string) (string, error) {
	var value string
	err := db.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading setting %s: %w", key, err)
	}
	return strings.TrimSpace(value), nil
}

func readBoolSetting(ctx context.Context, db rowQuerier, key string, fallback bool) (bool, error) {
	value, err := readServerSetting(ctx, db, key)
	if err != nil {
		return fallback, err
	}
	// Normalization stores only true or false; anything else is the default.
	return settingBool(value, fallback), nil
}

func localPasswordLoginAllowed(ctx context.Context, db rowQuerier) (bool, error) {
	return readBoolSetting(ctx, db, config.AuthLocalPasswordLoginSettingKey, true)
}

// EnsureLocalPasswordLoginAllowedInTransaction serializes local account
// creation with settings changes, so a refused signup cannot spend an invite.
func EnsureLocalPasswordLoginAllowedInTransaction(ctx context.Context, tx pgx.Tx) error {
	if err := lockServerSettings(ctx, tx); err != nil {
		return err
	}
	allowed, err := localPasswordLoginAllowed(ctx, tx)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrLocalLoginDisabled
	}
	return nil
}

func emailAutoMatchEnabled(ctx context.Context, db rowQuerier) (bool, error) {
	return readBoolSetting(ctx, db, config.AuthEmailAutoMatchSettingKey, false)
}

// LocalPasswordLoginAllowed reports the server-wide local password switch
// (auth.local_password_login, default on).
func (r *UserRepository) LocalPasswordLoginAllowed(ctx context.Context) (bool, error) {
	return localPasswordLoginAllowed(ctx, r.pool)
}

// EnableLocalPasswordLogin turns the server-wide switch back on, for the
// recovery command.
func (r *UserRepository) EnableLocalPasswordLogin(ctx context.Context) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockServerSettings(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, 'true')
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, config.AuthLocalPasswordLoginSettingKey)
		if err != nil {
			return fmt.Errorf("writing %s: %w", config.AuthLocalPasswordLoginSettingKey, err)
		}
		return nil
	})
}

// EnableAccountLocalLogin turns one account's local password sign-in back
// on, for the recovery command: an account linked to an external provider
// has it off, so an outage of that provider, or removing its plugin, would
// otherwise leave the account (possibly the Owner or the only admin) with no
// way in. With a non-empty temporaryPassword the account also gets that
// password, which it must replace at its next sign-in, and every sign-in it
// holds is revoked. login is a username or email.
func (r *UserRepository) EnableAccountLocalLogin(ctx context.Context, login, temporaryPassword string) (*models.User, error) {
	user, err := LookupLogin(ctx, r, login)
	if err != nil {
		return nil, err
	}
	var hash string
	if temporaryPassword != "" {
		raw, err := bcrypt.GenerateFromPassword([]byte(temporaryPassword), passwordHashCost)
		if err != nil {
			return nil, fmt.Errorf("hashing password: %w", err)
		}
		hash = string(raw)
	}
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET local_password_login_enabled = true,
				password_hash = CASE WHEN $2 = '' THEN password_hash ELSE $2 END,
				password_change_required = CASE WHEN $2 = '' THEN password_change_required ELSE true END,
				updated_at = NOW()
			WHERE id = $1`, user.ID, hash)
		if err != nil {
			return fmt.Errorf("enabling local password sign-in: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if hash == "" {
			return nil
		}
		return RevokeSignInsInTransaction(ctx, tx, user.ID)
	})
	if err != nil {
		return nil, err
	}
	return r.GetByID(ctx, user.ID)
}

// lockServerSettings takes the advisory lock every server_settings mutation
// holds, so a break-glass change and a local-login setting change cannot
// validate against each other's stale state.
func lockServerSettings(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, config.ServerSettingsMutationLock); err != nil {
		return fmt.Errorf("acquiring settings lock: %w", err)
	}
	return nil
}

// usableBreakGlassCount counts enabled break-glass admins that can still
// use a local password, other than excludeID.
func usableBreakGlassCount(ctx context.Context, db rowQuerier, excludeID int) (int, error) {
	var count int
	err := db.QueryRow(ctx, `SELECT COUNT(*) FROM users
		WHERE break_glass AND role = 'admin' AND enabled AND local_password_login_enabled AND id <> $1`,
		excludeID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting break-glass accounts: %w", err)
	}
	return count, nil
}

// CheckLocalLoginSettingChange refuses turning local password sign-in off
// while no break-glass admin could still sign in. The caller runs it inside
// the settings mutation, which already holds the settings lock; before and
// after are the effective values.
func CheckLocalLoginSettingChange(ctx context.Context, db rowQuerier, before, after string) error {
	wasOn := settingBool(before, true)
	isOn := settingBool(after, true)
	if !wasOn || isOn {
		return nil
	}
	count, err := usableBreakGlassCount(ctx, db, 0)
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrBreakGlassRequired
	}
	return nil
}

func settingBool(value string, fallback bool) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

// usableBreakGlass reports whether user, as stored, counts toward the
// break-glass requirement.
func usableBreakGlass(user *models.User) bool {
	return user != nil && user.BreakGlass && user.Role == models.RoleAdmin && user.Enabled && user.LocalPasswordLoginEnabled
}

// EnsureBreakGlassAfterAdminChange refuses an administrator change to current
// (input nil deletes it) that would remove the last usable break-glass admin
// while local password sign-in is off. It runs in the account mutation's
// transaction and takes the settings lock, so a concurrent settings write
// cannot turn local sign-in off in between.
func EnsureBreakGlassAfterAdminChange(ctx context.Context, tx pgx.Tx, current *models.User, input *models.UpdateUserInput) error {
	if !usableBreakGlass(current) {
		return nil
	}
	if input != nil {
		after := *current
		if input.BreakGlass != nil {
			after.BreakGlass = *input.BreakGlass
		}
		if input.Role != nil {
			after.Role = *input.Role
			if after.Role != models.RoleAdmin {
				after.BreakGlass = false
			}
		}
		if input.Enabled != nil {
			after.Enabled = *input.Enabled
		}
		if input.LocalPasswordLoginEnabled != nil {
			after.LocalPasswordLoginEnabled = *input.LocalPasswordLoginEnabled
		}
		if usableBreakGlass(&after) {
			return nil
		}
	}
	if err := lockServerSettings(ctx, tx); err != nil {
		return err
	}
	allowed, err := localPasswordLoginAllowed(ctx, tx)
	if err != nil || allowed {
		return err
	}
	count, err := usableBreakGlassCount(ctx, tx, current.ID)
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrBreakGlassRequired
	}
	return nil
}

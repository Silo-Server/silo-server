package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// SiloCredValidator implements abs.ProfileCredentialValidator using silo's
// existing auth.Service. In-process validation avoids the HTTP round-trip
// the plugin previously made to POST /api/v1/auth/login.
type SiloCredValidator struct {
	Auth *auth.Service
	Pool *pgxpool.Pool
}

// Validate checks (username, password) against silo's local auth provider
// and returns the string user ID, profile ID, and display name.
//
// Accepts two username formats:
//
//   - "alice"         — primary profile for user alice. profileID will be the
//                       alice user's primary profile.
//   - "alice#kids"    — the "kids" household profile under user alice.
//                       Authentication still uses alice's password; only the
//                       profile selector differs. If alice has no profile
//                       named "kids" (case-insensitive), returns
//                       ErrInvalidCredentials so we don't leak which arm of
//                       the user#profile pair was wrong.
//
// The ABS compat layer represents user/profile IDs as strings; we format the
// integer user ID and UUID profile ID accordingly. Users with no profiles at
// all (newly created accounts pre-setup) get an empty profileID and the ABS
// handler treats that as "primary".
func (v *SiloCredValidator) Validate(
	ctx context.Context,
	username, password string,
) (userID, profileID, displayName string, err error) {
	if v.Auth == nil {
		return "", "", "", errors.New("auth service not configured")
	}

	authName, profileSelector := splitUserProfile(username)

	// Authenticate using just the user portion. DeviceName and IP are
	// informational for session bookkeeping only.
	_, user, err := v.Auth.Login(ctx, authName, password, "abs-compat", "")
	if err != nil {
		// Propagate auth sentinel errors as-is so callers can distinguish
		// "bad credentials" (ErrInvalidCredentials) from "service down".
		return "", "", "", err
	}

	userIDStr := strconv.Itoa(user.ID)
	displayName = user.Username

	if profileSelector != "" {
		// Explicit profile requested — look it up by name. A miss here is
		// reported as ErrInvalidCredentials (rather than a separate "no
		// such profile" error) so an attacker can't enumerate profiles
		// using a valid user/password.
		pid, pname, pidErr := profileForUserByName(ctx, v.Pool, user.ID, profileSelector)
		if pidErr != nil {
			return "", "", "", fmt.Errorf("lookup profile by name: %w", pidErr)
		}
		if pid == "" {
			return "", "", "", auth.ErrInvalidCredentials
		}
		profileID = pid
		if pname != "" {
			displayName = pname
		}
		return userIDStr, profileID, displayName, nil
	}

	// No profile selector — fall back to the primary profile.
	pid, pname, pidErr := primaryProfileForUser(ctx, v.Pool, user.ID)
	if pidErr == nil && pid != "" {
		profileID = pid
		if pname != "" {
			displayName = pname
		}
	}

	return userIDStr, profileID, displayName, nil
}

// splitUserProfile separates a username like "alice#kids" into the
// authentication username ("alice") and the profile selector ("kids").
// Plain "alice" returns ("alice", ""). Whitespace is trimmed on both sides.
// A trailing "#" with no profile name ("alice#") collapses to ("alice", "").
func splitUserProfile(raw string) (user, profile string) {
	raw = strings.TrimSpace(raw)
	i := strings.LastIndexByte(raw, '#')
	if i < 0 {
		return raw, ""
	}
	return strings.TrimSpace(raw[:i]), strings.TrimSpace(raw[i+1:])
}

// primaryProfileForUser returns the (id, name) of the primary profile for
// the given user from the user_profiles table. Returns ("", "", nil) when no
// profiles exist (newly created user, pre-profile-setup).
func primaryProfileForUser(ctx context.Context, pool *pgxpool.Pool, userID int) (id, name string, err error) {
	if pool == nil {
		return "", "", fmt.Errorf("no pgx pool available")
	}
	row := pool.QueryRow(ctx,
		`SELECT id, name FROM user_profiles
		 WHERE user_id = $1 AND is_primary = TRUE
		 LIMIT 1`,
		userID,
	)
	err = row.Scan(&id, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		// No primary profile — acceptable for a brand-new user or a user who
		// hasn't set up profiles yet. Return empty strings; the ABS handler
		// interprets empty profileID as "use primary / no profile".
		return "", "", nil
	}
	return id, name, err
}

// profileForUserByName returns the (id, name) of a profile under the given
// user matched by name (case-insensitive). Returns ("", "", nil) when no
// matching profile exists, so callers can map that to invalid-credentials
// without unwrapping a sentinel.
func profileForUserByName(ctx context.Context, pool *pgxpool.Pool, userID int, profileName string) (id, name string, err error) {
	if pool == nil {
		return "", "", fmt.Errorf("no pgx pool available")
	}
	row := pool.QueryRow(ctx,
		`SELECT id, name FROM user_profiles
		 WHERE user_id = $1 AND LOWER(name) = LOWER($2)
		 LIMIT 1`,
		userID, profileName,
	)
	err = row.Scan(&id, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return id, name, err
}

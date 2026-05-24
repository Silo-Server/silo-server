package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"

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

// Validate checks (username, password) against silo's local auth provider and
// returns the string user ID, primary profile ID, and display name.
//
// The ABS compat layer represents user/profile IDs as strings; we format the
// integer user ID and UUID profile ID accordingly. If the user has no profiles
// yet, profileID is returned empty and the ABS handler treats the missing
// profile as "primary".
func (v *SiloCredValidator) Validate(
	ctx context.Context,
	username, password string,
) (userID, profileID, displayName string, err error) {
	if v.Auth == nil {
		return "", "", "", errors.New("auth service not configured")
	}

	// DeviceName and IP are informational for session bookkeeping only.
	_, user, err := v.Auth.Login(ctx, username, password, "abs-compat", "")
	if err != nil {
		// Propagate auth sentinel errors as-is so callers can distinguish
		// "bad credentials" (ErrInvalidCredentials) from "service down".
		return "", "", "", err
	}

	userIDStr := strconv.Itoa(user.ID)
	displayName = user.Username

	// Resolve the primary profile for this user from the central
	// user_profiles table. Primary is the first profile created per user
	// (is_primary = true). If none exists, return empty profileID.
	pid, pname, pidErr := primaryProfileForUser(ctx, v.Pool, user.ID)
	if pidErr == nil && pid != "" {
		profileID = pid
		if pname != "" {
			displayName = pname
		}
	}

	return userIDStr, profileID, displayName, nil
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

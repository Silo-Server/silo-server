package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SiloCredValidator implements abs.ProfileCredentialValidator and
// abs.ProfileVerifier using silo's existing auth.Service. In-process
// validation avoids the HTTP round-trip the plugin previously made to
// POST /api/v1/auth/login.
type SiloCredValidator struct {
	Auth *auth.Service
	Pool *pgxpool.Pool
	// Stores reads the account's profiles and checks their PINs.
	Stores userstore.UserStoreProvider
	// PINAttempts is the native API's profile PIN limiter, so password#pin
	// guesses here count against the same per-profile budget. Nil allows
	// every attempt.
	PINAttempts *ratelimit.AttemptLimiter
}

// Validate checks (username, password) against silo's local auth provider
// and the selected profile's PIN, and returns the signed-in principal.
//
// Accepts two username formats:
//
//   - "alice"         — the profile named "alice" if there is one, otherwise
//     the primary profile for user alice. A primary
//     profile with a PIN is refused (abs.ErrProfilePINRequired):
//     it has to be named, as below.
//   - "alice#kids"    — the "kids" household profile under user alice.
//     Authentication still uses alice's password; only the
//     profile selector differs. If alice has no profile
//     named "kids" (case-insensitive), returns
//     ErrInvalidCredentials so we don't leak which arm of
//     the user#profile pair was wrong.
//
// A PIN-protected profile needs its PIN after the last '#' in the password
// ("password#pin"), as on the Jellyfin-compatible listener: the full password
// is tried first, and the part before the last '#' only when that fails.
// PIN attempts count against the profile's shared lockout, and only once the
// account password has matched.
//
// The ABS compat layer represents user/profile IDs as strings; we format the
// integer user ID and UUID profile ID accordingly. An account with no
// profiles at all (newly created, pre-setup) gets an empty profileID.
func (v *SiloCredValidator) Validate(
	ctx context.Context,
	username, password string,
) (abs.Principal, error) {
	if v.Auth == nil {
		return abs.Principal{}, errors.New("auth service not configured")
	}
	if v.Stores == nil {
		return abs.Principal{}, errors.New("user store not configured")
	}

	authName, profileSelector := splitUserProfile(username)

	// Authenticate using just the user portion. DeviceName and IP are
	// informational for session bookkeeping only.
	// CompatLogin refuses a temporary password: Audiobookshelf clients cannot
	// run the password change it requires.
	basePw, pin := auth.SplitPasswordPIN(password)
	_, user, usedFallback, err := v.Auth.CompatLoginWithLocalFallback(ctx, authName, password, basePw, "abs-compat", "")
	if err != nil {
		// Propagate auth sentinel errors as-is so callers can distinguish
		// "bad credentials" (ErrInvalidCredentials) from "service down".
		return abs.Principal{}, err
	}
	if !usedFallback {
		// The full password matched, so no PIN was split out of it.
		pin = ""
	}

	store, err := v.Stores.ForUser(ctx, user.ID)
	if err != nil {
		return abs.Principal{}, fmt.Errorf("open user store: %w", err)
	}
	// The listing carries each profile's PIN revision, read before the PIN
	// is checked: a PIN change in between leaves the token on the older
	// revision, which ProfileVerifier refuses.
	profiles, err := store.ListProfiles(ctx)
	if err != nil {
		return abs.Principal{}, fmt.Errorf("list profiles: %w", err)
	}
	principal := abs.Principal{UserID: strconv.Itoa(user.ID), DisplayName: user.Username}
	// The account's own username, not the name typed: a sign-in by email
	// selects the same account-named profile.
	profile, err := selectSignInProfile(profiles, user.Username, profileSelector)
	if err != nil {
		return abs.Principal{}, err
	}
	if profile == nil {
		// An account with no profiles signs in without one.
		return principal, nil
	}

	if profile.PINHash != "" {
		if pin == "" {
			return abs.Principal{}, abs.ErrProfilePINRequired
		}
		// Counted before the PIN is checked, so concurrent guesses cannot
		// overrun the limit, and while the profile is locked even the right
		// PIN is refused.
		attemptKey := ratelimit.ProfilePINKey(user.ID, profile.ID)
		if _, ok := v.PINAttempts.Reserve(ctx, attemptKey); !ok {
			return abs.Principal{}, abs.ErrProfilePINLocked
		}
		valid, err := store.VerifyPIN(ctx, profile.ID, pin)
		if err != nil {
			return abs.Principal{}, fmt.Errorf("verify profile PIN: %w", err)
		}
		if !valid {
			return abs.Principal{}, abs.ErrProfilePINIncorrect
		}
		v.PINAttempts.Reset(ctx, attemptKey)
	}

	principal.ProfileID = profile.ID
	principal.PINRevision = profile.PINRevision
	if profile.Name != "" {
		principal.DisplayName = profile.Name
	}
	return principal, nil
}

// selectSignInProfile picks the profile a sign-in names: the one called
// profileName (case-insensitive). With no profile name, a profile named like
// the account counts as named, as on the Jellyfin-compatible listener;
// otherwise the primary, which is not picked implicitly when it has a PIN.
// Returns nil without an error only for an account with no profiles.
func selectSignInProfile(profiles []userstore.Profile, accountName, profileName string) (*userstore.Profile, error) {
	name := profileName
	if name == "" {
		name = accountName
	}
	for i := range profiles {
		if strings.EqualFold(profiles[i].Name, name) {
			return &profiles[i], nil
		}
	}
	if profileName != "" {
		// A miss is reported as ErrInvalidCredentials (rather than a
		// separate "no such profile" error) so an attacker can't enumerate
		// profiles using a valid user/password.
		return nil, auth.ErrInvalidCredentials
	}
	if len(profiles) == 0 {
		return nil, nil
	}
	for i := range profiles {
		if !profiles[i].IsPrimary {
			continue
		}
		if profiles[i].PINHash != "" {
			return nil, abs.ErrProfilePINRequired
		}
		return &profiles[i], nil
	}
	// Every account with profiles has a primary; refuse rather than sign in
	// without one.
	return nil, auth.ErrInvalidCredentials
}

// VerifyTokenProfile implements abs.ProfileVerifier.
func (v *SiloCredValidator) VerifyTokenProfile(ctx context.Context, userID, profileID string, pinRevision *int64) error {
	if v.Stores == nil {
		return errors.New("user store not configured")
	}
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return abs.ErrTokenProfileUnverified
	}
	store, err := v.Stores.ForUser(ctx, uid)
	if err != nil {
		return fmt.Errorf("open user store: %w", err)
	}
	if profileID == "" {
		// Signed in before the account had profiles. Once it has one, the
		// token would bypass that profile's limits and PIN.
		profiles, err := store.ListProfiles(ctx)
		if err != nil {
			return fmt.Errorf("list profiles: %w", err)
		}
		if len(profiles) > 0 {
			return abs.ErrTokenProfileUnverified
		}
		return nil
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	if profile == nil {
		return abs.ErrTokenProfileUnverified
	}
	if profile.PINHash != "" && (pinRevision == nil || *pinRevision != profile.PINRevision) {
		return abs.ErrTokenProfileUnverified
	}
	return nil
}

// ResolveUsername returns the ABS display name for (userID, profileID) without
// re-authenticating — used by GET /me, which only has the token claims. It
// mirrors Validate's display-name logic: the profile name when a profile is
// set and named, otherwise the account username. Returns "" when unresolvable
// so the caller falls back to the userID.
func (v *SiloCredValidator) ResolveUsername(ctx context.Context, userID, profileID string) string {
	if v.Pool == nil {
		return ""
	}
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return ""
	}
	if strings.TrimSpace(profileID) != "" {
		var name string
		err := v.Pool.QueryRow(ctx,
			`SELECT name FROM user_profiles WHERE user_id = $1 AND id = $2`, uid, profileID,
		).Scan(&name)
		if err == nil && strings.TrimSpace(name) != "" {
			return name
		}
	}
	var username string
	if err := v.Pool.QueryRow(ctx,
		`SELECT username FROM users WHERE id = $1`, uid,
	).Scan(&username); err == nil {
		return username
	}
	return ""
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

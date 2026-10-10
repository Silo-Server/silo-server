package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

var (
	// ErrProfileRequired indicates the user omitted the required profile suffix.
	ErrProfileRequired = errors.New("username must include a profile suffix like username#profile")
	// ErrProfileNotFound indicates the requested profile does not exist.
	ErrProfileNotFound = errors.New("profile not found")
	// ErrProfileAmbiguous indicates multiple profiles matched case-insensitively.
	ErrProfileAmbiguous = errors.New("profile name is ambiguous")
	// ErrProfileHasPIN indicates the matched profile requires PIN verification.
	ErrProfileHasPIN = errors.New("profile is PIN protected")
	// ErrInvalidPIN indicates the provided PIN did not match.
	ErrInvalidPIN = errors.New("invalid profile PIN")
)

// LoginResolver performs username#profile login resolution against Silo.
type LoginResolver struct {
	authService    *auth.Service
	storeProvider  userstore.UserStoreProvider
	sessions       *SessionStore
	tokenGenerator func() string
	now            func() time.Time
	// pinAttempts is the native API's profile PIN limiter, so password#pin
	// guesses here count against the same per-profile budget. Nil allows
	// every attempt.
	pinAttempts *ratelimit.AttemptLimiter
}

// NewLoginResolver creates a new login resolver using direct auth service.
func NewLoginResolver(authService *auth.Service, storeProvider userstore.UserStoreProvider, sessions *SessionStore, tokenGenerator func() string, now func() time.Time) *LoginResolver {
	if tokenGenerator == nil {
		tokenGenerator = uuidNewString
	}
	if now == nil {
		now = time.Now
	}
	return &LoginResolver{
		authService:    authService,
		storeProvider:  storeProvider,
		sessions:       sessions,
		tokenGenerator: tokenGenerator,
		now:            now,
	}
}

// WithPINAttempts sets the per-profile PIN attempt limiter and returns r.
func (r *LoginResolver) WithPINAttempts(l *ratelimit.AttemptLimiter) *LoginResolver {
	r.pinAttempts = l
	return r
}

// Resolve authenticates the account and profile, returning a compat session.
//
// PIN-protected profiles are supported via the password#pin convention:
// the user appends their profile PIN after a '#' in the password field.
// The resolver tries the full password first, then falls back to splitting
// at the last '#' if the password was wrong and a '#' is present. The
// convention applies to local accounts only: a name that signs in with a
// directory (LDAP) gets exactly one attempt with the password as typed, so
// a PIN never reaches the directory and a failed bind is never doubled.
func (r *LoginResolver) Resolve(ctx context.Context, combinedUsername, password, userAgent, remoteIP string) (*Session, error) {
	accountUsername, requestedProfile, hasExplicitProfile, err := r.parseLogin(ctx, combinedUsername)
	if err != nil {
		return nil, err
	}

	// Try auth with full password first, fall back to base#pin split.
	basePw, pinCandidate := splitPasswordPIN(password)
	// CompatLogin refuses a temporary password: Jellyfin clients cannot run the
	// password change it requires.
	tokenPair, user, usedFallback, err := r.authService.CompatLoginWithLocalFallback(ctx, accountUsername, password, basePw, userAgent, remoteIP)
	if err != nil {
		return nil, mapAuthError(err)
	}
	if !usedFallback {
		// Full password succeeded — no PIN was split out.
		pinCandidate = ""
	}

	store, err := r.storeProvider.ForUser(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("open user store: %w", err)
	}

	storeProfiles, err := store.ListProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}

	profiles := make([]upstreamProfile, 0, len(storeProfiles))
	for _, p := range storeProfiles {
		profiles = append(profiles, upstreamProfile{
			ID:     p.ID,
			Name:   p.Name,
			Avatar: p.Avatar,
			HasPIN: p.PINHash != "",
		})
	}

	profile, err := selectProfile(accountUsername, requestedProfile, hasExplicitProfile, profiles)
	if err != nil {
		return nil, err
	}

	if profile.HasPIN {
		if pinCandidate == "" {
			return nil, fmt.Errorf("%w: use password#pin format to access PIN-protected profiles", ErrProfileHasPIN)
		}
		// A locked profile fails like a wrong PIN: Jellyfin clients have no
		// lockout response, so only the message says why.
		attemptKey := ratelimit.ProfilePINKey(user.ID, profile.ID)
		if _, ok := r.pinAttempts.Reserve(ctx, attemptKey); !ok {
			return nil, fmt.Errorf("%w: too many incorrect PINs for profile %s; try again later", ErrInvalidPIN, profile.Name)
		}
		valid, verifyErr := store.VerifyPIN(ctx, profile.ID, pinCandidate)
		if verifyErr != nil {
			return nil, fmt.Errorf("verifying profile PIN: %w", verifyErr)
		}
		if !valid {
			return nil, fmt.Errorf("%w: incorrect PIN for profile %s", ErrInvalidPIN, profile.Name)
		}
		r.pinAttempts.Reset(ctx, attemptKey)
	}

	now := r.now()
	session := Session{
		Token:                 r.tokenGenerator(),
		Username:              combinedUsername,
		AccountUsername:       user.Username,
		ProfileID:             profile.ID,
		ProfileName:           profile.Name,
		PseudoUserID:          PseudoUserID(user.ID, profile.ID),
		StreamAppUserID:       user.ID,
		StreamAppAccessToken:  tokenPair.AccessToken,
		StreamAppRefreshToken: tokenPair.RefreshToken,
		StreamAppTokenExpiry:  now.Add(time.Duration(tokenPair.ExpiresIn) * time.Second),
		CreatedAt:             now,
	}
	if err := r.sessions.Put(session); err != nil {
		return nil, err
	}

	return &session, nil
}

// splitPasswordPIN splits "password#pin" at the last '#'.
// Returns ("", "") if there is no '#' or the split would produce empty parts.
func splitPasswordPIN(password string) (basePw, pin string) {
	idx := strings.LastIndex(password, "#")
	if idx <= 0 || idx >= len(password)-1 {
		return "", ""
	}
	return password[:idx], password[idx+1:]
}

// parseLogin reads username#profile. When no account has the name before
// the last '#' and that name would not go to the directory, but the whole
// name is an account, such as one named after an old Discord name like
// "name#1234", the whole name is the account and no profile is named. A name
// the directory may still provision keeps the split, so a local account can
// never block a directory user's first sign-in.
func (r *LoginResolver) parseLogin(ctx context.Context, combinedUsername string) (accountUsername string, profileName string, hasExplicitProfile bool, err error) {
	accountUsername, profileName, hasExplicitProfile, err = parseProfileLogin(combinedUsername)
	if err != nil || !hasExplicitProfile {
		return accountUsername, profileName, hasExplicitProfile, err
	}
	splitExists, err := r.authService.HasLoginName(ctx, accountUsername)
	if err != nil {
		return "", "", false, err
	}
	if splitExists {
		return accountUsername, profileName, true, nil
	}
	usesDirectory, err := r.authService.PasswordLoginUsesDirectory(ctx, accountUsername)
	if err != nil {
		return "", "", false, err
	}
	if usesDirectory {
		return accountUsername, profileName, true, nil
	}
	whole := strings.TrimSpace(combinedUsername)
	wholeExists, err := r.authService.HasLoginName(ctx, whole)
	if err != nil {
		return "", "", false, err
	}
	if wholeExists {
		return whole, "", false, nil
	}
	return accountUsername, profileName, true, nil
}

func parseProfileLogin(username string) (accountUsername string, profileName string, hasExplicitProfile bool, err error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", "", false, ErrProfileRequired
	}

	idx := strings.LastIndex(username, "#")
	if idx == -1 {
		return username, "", false, nil
	}
	if idx == 0 || idx >= len(username)-1 {
		return "", "", false, ErrProfileRequired
	}

	accountUsername = strings.TrimSpace(username[:idx])
	profileName = strings.TrimSpace(username[idx+1:])
	if accountUsername == "" || profileName == "" {
		return "", "", false, ErrProfileRequired
	}
	return accountUsername, profileName, true, nil
}

func selectProfile(accountUsername, requestedProfile string, hasExplicitProfile bool, profiles []upstreamProfile) (upstreamProfile, error) {
	if hasExplicitProfile {
		return selectNamedProfile(requestedProfile, profiles)
	}

	accountMatches := make([]upstreamProfile, 0, 1)
	for _, profile := range profiles {
		if strings.EqualFold(profile.Name, accountUsername) {
			accountMatches = append(accountMatches, profile)
		}
	}
	switch len(accountMatches) {
	case 1:
		return accountMatches[0], nil
	case 0:
	default:
		return upstreamProfile{}, fmt.Errorf("%w: %s", ErrProfileAmbiguous, accountUsername)
	}
	if len(profiles) == 0 {
		return upstreamProfile{}, ErrProfileRequired
	}

	usableProfiles := make([]upstreamProfile, 0, len(profiles))
	for _, profile := range profiles {
		if !profile.HasPIN {
			usableProfiles = append(usableProfiles, profile)
		}
	}
	switch len(usableProfiles) {
	case 0:
		return upstreamProfile{}, fmt.Errorf("%w: use username#profile and password#pin format", ErrProfileHasPIN)
	case 1:
		return usableProfiles[0], nil
	default:
		return upstreamProfile{}, ErrProfileRequired
	}
}

func selectNamedProfile(profileName string, profiles []upstreamProfile) (upstreamProfile, error) {
	matches := make([]upstreamProfile, 0, 1)
	for _, profile := range profiles {
		if strings.EqualFold(profile.Name, profileName) {
			matches = append(matches, profile)
		}
	}

	switch len(matches) {
	case 0:
		return upstreamProfile{}, fmt.Errorf("%w: %s", ErrProfileNotFound, profileName)
	case 1:
		return matches[0], nil
	default:
		return upstreamProfile{}, fmt.Errorf("%w: %s", ErrProfileAmbiguous, profileName)
	}
}

// Jellyfin's answer to a refused sign-in: the error code, and the message
// of a plain credentials failure.
const (
	loginErrorCode            = "InvalidUsernameOrPassword"
	invalidCredentialsMessage = "Invalid username or password"
)

func mapLoginError(err error) (int, string, string) {
	if httpErr, ok := err.(*HTTPError); ok {
		switch httpErr.StatusCode {
		case http.StatusUnauthorized:
			// mapAuthError words every 401: a sign-in policy refusal names
			// its reason, anything else is a plain credentials failure.
			message := httpErr.Message
			if message == "" {
				message = invalidCredentialsMessage
			}
			return http.StatusUnauthorized, loginErrorCode, message
		case http.StatusServiceUnavailable:
			return http.StatusServiceUnavailable, "ServiceUnavailable", httpErr.Error()
		}
		return http.StatusBadGateway, "UpstreamError", httpErr.Error()
	}
	if errors.Is(err, ErrProfileRequired) || errors.Is(err, ErrProfileNotFound) || errors.Is(err, ErrProfileAmbiguous) || errors.Is(err, ErrProfileHasPIN) || errors.Is(err, ErrInvalidPIN) || errors.Is(err, auth.ErrPasswordChangeRequired) {
		return http.StatusUnauthorized, loginErrorCode, err.Error()
	}
	return http.StatusInternalServerError, "ServerError", "Unexpected login failure"
}

// mapAuthError converts auth.Service errors to compat HTTPError.
func mapAuthError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, auth.ErrPasswordChangeRequired) {
		// Kept as the sentinel, like the PIN errors, so the client sees why.
		return fmt.Errorf("%w: sign in to Silo to replace your temporary password", err)
	}
	switch {
	case errors.Is(err, auth.ErrProviderUnavailable):
		return &HTTPError{StatusCode: http.StatusServiceUnavailable, Message: "The sign-in provider is unavailable"}
	case errors.Is(err, auth.ErrLocalLoginDisabled), errors.Is(err, auth.ErrNotPermitted),
		errors.Is(err, auth.ErrEmailInUse), errors.Is(err, auth.ErrIdentityLinkedElsewhere),
		errors.Is(err, auth.ErrProviderPasswordExpired):
		// Refusals of the server's sign-in policy look like any failed
		// sign-in to a Jellyfin client; the reason goes in the message.
		return &HTTPError{StatusCode: http.StatusUnauthorized, Message: err.Error()}
	}
	// auth.Service returns plain errors for bad credentials
	errMsg := err.Error()
	if strings.Contains(errMsg, "invalid credentials") || strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "disabled") {
		return &HTTPError{StatusCode: http.StatusUnauthorized, Message: invalidCredentialsMessage}
	}
	return err
}

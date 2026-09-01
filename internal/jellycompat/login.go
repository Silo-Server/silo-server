package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
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

// Resolve authenticates the account and profile, returning a compat session.
//
// PIN-protected profiles are supported via the password#pin convention:
// the user appends their profile PIN after a '#' in the password field.
// The resolver tries the full password first, then falls back to splitting
// at the last '#' if authentication fails and a '#' is present.
func (r *LoginResolver) Resolve(ctx context.Context, combinedUsername, password, userAgent, remoteIP string) (*Session, error) {
	accountUsername, requestedProfile, hasExplicitProfile, err := parseProfileLogin(combinedUsername)
	if err != nil {
		return nil, err
	}

	// Try auth with full password first, fall back to base#pin split.
	basePw, pinCandidate := splitPasswordPIN(password)
	tokenPair, user, err := r.authService.Login(ctx, accountUsername, password, userAgent, remoteIP)
	if err != nil && basePw != "" {
		// Full password failed and there's a # — try the base portion.
		tokenPair, user, err = r.authService.Login(ctx, accountUsername, basePw, userAgent, remoteIP)
		if err != nil {
			return nil, mapAuthError(err)
		}
		// basePw succeeded; pinCandidate holds the extracted PIN.
	} else if err != nil {
		return nil, mapAuthError(err)
	} else {
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
		valid, verifyErr := store.VerifyPIN(ctx, profile.ID, pinCandidate)
		if verifyErr != nil {
			return nil, fmt.Errorf("verifying profile PIN: %w", verifyErr)
		}
		if !valid {
			return nil, fmt.Errorf("%w: incorrect PIN for profile %s", ErrInvalidPIN, profile.Name)
		}
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
		StreamAppSessionID:    tokenPair.SessionID,
		AuthRevision:          user.AuthRevision,
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

func mapLoginError(err error) (int, string, string) {
	if httpErr, ok := err.(*HTTPError); ok {
		if httpErr.StatusCode == http.StatusUnauthorized {
			return http.StatusUnauthorized, "InvalidUsernameOrPassword", "Invalid username or password"
		}
		return http.StatusBadGateway, "UpstreamError", httpErr.Error()
	}
	if errors.Is(err, ErrProfileRequired) || errors.Is(err, ErrProfileNotFound) || errors.Is(err, ErrProfileAmbiguous) || errors.Is(err, ErrProfileHasPIN) || errors.Is(err, ErrInvalidPIN) {
		return http.StatusUnauthorized, "InvalidUsernameOrPassword", err.Error()
	}
	return http.StatusInternalServerError, "ServerError", "Unexpected login failure"
}

// mapAuthError converts auth.Service errors to compat HTTPError.
func mapAuthError(err error) error {
	if err == nil {
		return nil
	}
	// auth.Service returns plain errors for bad credentials
	errMsg := err.Error()
	if strings.Contains(errMsg, "invalid credentials") || strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "disabled") {
		return &HTTPError{StatusCode: http.StatusUnauthorized, Message: "Invalid username or password"}
	}
	return err
}

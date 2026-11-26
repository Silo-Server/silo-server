package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// AppKeyService signs devices in with app keys and manages a person's own
// keys (docs/architecture/app-keys.md). *handlers.AuthHandler implements it.
type AppKeyService interface {
	// AppKeySignInEnabled reads the auth.app_key_sign_in server setting.
	AppKeySignInEnabled(context.Context) (bool, error)
	// IssueKeyChallenge stores a challenge and answers it with the
	// server's clock in Unix seconds.
	IssueKeyChallenge(context.Context) (string, int64, error)
	IntroduceAppKey(context.Context, auth.KeyRequest) (*handlers.AppKeyAnswer, error)
	SignInWithAppKey(context.Context, auth.KeyRequest) (*handlers.AppKeyAnswer, error)
	EnrollAppKeys(context.Context, auth.KeyEnrollRequest) (*handlers.AppKeyAnswer, error)
	RedeemAppKeyGrant(context.Context, auth.KeyRequest) (*handlers.AppKeyAnswer, error)
	ListOwnAppKeys(context.Context, *auth.Claims) (auth.OwnAppKeys, error)
	RevokeOwnDeviceKey(ctx context.Context, claims *auth.Claims, id string) error
	ReplacePersonKey(context.Context, *auth.Claims, appkeys.PublicKey, auth.ActingProfile) error
	ListPendingAppKeys(context.Context, *auth.Claims) ([]*models.AppDeviceKey, error)
	ApprovePendingAppKey(ctx context.Context, claims *auth.Claims, id string, acting auth.ActingProfile) error
	DenyPendingAppKey(ctx context.Context, claims *auth.Claims, id string) error
	// ActsAsVerifiedMainProfile is the main-profile rule of the operations
	// that create lasting access (requireVerifiedMainProfile).
	IssueSessionAppCode(ctx context.Context, claims *auth.Claims, acting auth.ActingProfile) (auth.IssuedAppCode, error)
	RedeemAppCode(context.Context, auth.KeyRedeemRequest) (*handlers.AppKeyAnswer, error)
	// AppKeySessionAccess answers what the caller's session may do with
	// keys (getAppKeySessionAccess).
	AppKeySessionAccess(context.Context, *auth.Claims) (auth.SessionKeyAccess, error)
	ActsAsVerifiedMainProfile(ctx context.Context, userID int, activeProfileID string, verify func(*userstore.Profile) error) (bool, auth.ActingProfile, error)
	// VerifyProfileSet verifies a signed profile set and spends its
	// challenge.
	VerifyProfileSet(context.Context, auth.KeyRequest) (*auth.ProfileSetAuthority, error)
	// SessionMayManageKeys refuses a login session that may not manage
	// keys or read or send the profile set (auth.ErrSessionNotAllowed).
	SessionMayManageKeys(context.Context, *auth.Claims) error
	// SessionMayIssueCredential answers auth.ErrSessionNotAllowed for a
	// session another app opened with a grant, which must not be handed a
	// personal API key or a linked sign-in.
	SessionMayIssueCredential(context.Context, *auth.Claims) error
}

// credentialSessionProblem refuses handing the account a credential that
// outlives the asking session (a personal API key, a linked sign-in
// provider account) when another app opened that session with a grant: the
// app's access must end when its key is revoked. Without app keys wired no
// such session exists.
func (reg *Registry) credentialSessionProblem(ctx context.Context, claims *auth.Claims) *Problem {
	if reg.deps.AppKeys == nil {
		return nil
	}
	err := reg.deps.AppKeys.SessionMayIssueCredential(ctx, claims)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrSessionNotAllowed):
		return NewProblem(TypePermissionDenied, handlers.SessionNotAllowedDetail)
	}
	return serviceProblem(err)
}

// sessionReachesOtherDevices reports whether the caller's session may see or
// change what the account's other devices hold: false for a session another
// app's key opened, true for every other caller, a device sign-in included.
// It reads the claim the authentication gate set from the session lookup
// (auth.Claims.IsGrantedApp), so it costs no query. The *Problem result is
// always nil; it keeps the callers' shape.
func (reg *Registry) sessionReachesOtherDevices(ctx context.Context) (bool, *Problem) {
	return !grantedAppSession(claimsFrom(ctx)), nil
}

// otherDevicesRule is the caller's login session and the rule a playback
// service asks, only for a stream another login session started, whether
// the caller may act on it. The rule is nil for every caller but a session
// another app's key opened, which it refuses with auth.ErrSessionNotAllowed.
func (reg *Registry) otherDevicesRule(ctx context.Context) (string, func(context.Context) error) {
	claims := claimsFrom(ctx)
	if claims == nil {
		return "", nil
	}
	if !grantedAppSession(claims) {
		return claims.SessionID, nil
	}
	return claims.SessionID, func(context.Context) error { return auth.ErrSessionNotAllowed }
}

// appKeyDomain names the capability in its problems.
const appKeyDomain = "app key sign-in"

// Suites the app key operations speak. Signed requests use ES256 over
// P-256; replies that carry session tokens are sealed with an ECDH P-256
// key agreement, HKDF-SHA256 and AES-256-GCM.
const (
	appKeySignatureSuite   = "ES256"
	appKeySealedReplySuite = "P256-HKDF-SHA256-AES256GCM"
)

// Rate-limit buckets. Every signed request that can open a session spends
// key_sign_in; asking for a challenge spends key_challenge.
const (
	bucketKeyChallenge = "key_challenge"
	bucketKeySignIn    = "key_sign_in"
)

// AppKeyCapability is the app key capability document.
type AppKeyCapability struct {
	Capability
	Protocol          string   `json:"protocol" enum:"silo-sync-v1" doc:"The app key protocol this server speaks" example:"silo-sync-v1"`
	ServerID          string   `json:"server_id" doc:"This server's identity, which every signed payload names (getServerIdentity)" example:"3f2a9d5e-6b1c-4c7e-9a0d-2f4b8c1e7a35"`
	Introduce         bool     `json:"introduce" doc:"Whether a person key can introduce a new device key and open its first session" example:"true"`
	SignIn            bool     `json:"sign_in" doc:"Whether an enrolled device key can open a login session" example:"true"`
	Enroll            bool     `json:"enroll" doc:"Whether a signed-in account can enroll a person key and a device key" example:"true"`
	AppCodes          bool     `json:"app_codes" doc:"Whether this server issues and redeems one-time app codes" example:"false"`
	Grants            bool     `json:"grants" doc:"Whether another app can enroll its own key with a grant signed by the person key" example:"false"`
	SignInLinks       bool     `json:"sign_in_links" doc:"Whether an administrator can send a single-use sign-in link" example:"false"`
	ProfileSet        bool     `json:"profile_set" doc:"Whether the account's profiles can be read and written as one versioned document" example:"false"`
	SignatureSuites   []string `json:"signature_suites" doc:"Signature suites signed requests may use" example:"[\"ES256\"]"`
	SealedReplySuites []string `json:"sealed_reply_suites" doc:"Suites a sealed reply may use" example:"[\"P256-HKDF-SHA256-AES256GCM\"]"`
}

// AppKeyCapabilityOutput is the getAppKeyCapability response.
type AppKeyCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AppKeyCapability
}

func registerAppKeys(reg *Registry) {
	op := humaOp(http.MethodGet, Prefix+"/auth/keys/capability", "getAppKeyCapability", "app-keys",
		"Describe app key sign-in support.")
	op.Description = "Public, so an app can ask before it signs in. state is available while the auth.app_key_sign_in server setting is on and disabled while it is off; it is always disabled on a server in demo mode, where every visitor shares one account. Each feature field turns true once this server supports that operation."
	Register(reg, Operation{Operation: op, Class: ClassPublic, ServiceBacked: true}, reg.getAppKeyCapability)
	registerAppKeySignIn(reg)
	registerAppKeyManagement(reg)
	registerAppKeyPending(reg)
	registerAppCodes(reg)
}

func (reg *Registry) getAppKeyCapability(ctx context.Context, _ *CapabilityInput) (*AppKeyCapabilityOutput, error) {
	state := StateNotConfigured
	if svc := reg.deps.AppKeys; svc != nil {
		enabled, err := reg.appKeySwitch(ctx, svc)
		if err != nil {
			return nil, serviceProblem(err)
		}
		state = enabledCapabilityState(enabled)
	}
	serverID, err := reg.serverID(ctx)
	if err != nil {
		return nil, err
	}
	return &AppKeyCapabilityOutput{
		Body: AppKeyCapability{
			Capability:        Capability{State: state},
			Protocol:          appkeys.Protocol,
			ServerID:          serverID,
			Introduce:         true,
			SignIn:            true,
			Enroll:            true,
			AppCodes:          true,
			Grants:            true,
			ProfileSet:        reg.deps.ProfileSet != nil && reg.deps.ProfileSet.ProfileSetSupported(),
			SignatureSuites:   []string{appKeySignatureSuite},
			SealedReplySuites: []string{appKeySealedReplySuite},
		},
	}, nil
}

// appKeySwitch reports whether app key sign-in is on: the
// auth.app_key_sign_in server setting, and never in demo mode. A demo
// server's visitors share one account, and a key any of them enrolled would
// be a lasting credential on an account that isn't theirs, so there the
// capability says disabled and no key is enrolled or signs in, for
// administrators too.
func (reg *Registry) appKeySwitch(ctx context.Context, svc AppKeyService) (bool, error) {
	if demo, err := demoMode(ctx, reg.deps.DemoSettings); err != nil || demo {
		return false, err
	}
	return svc.AppKeySignInEnabled(ctx)
}

// appKeysOn answers the service while the switch is on, and the
// capability problem otherwise: not_configured without the service,
// capability_disabled while auth.app_key_sign_in is off or the server is in
// demo mode.
func (reg *Registry) appKeysOn(ctx context.Context) (AppKeyService, error) {
	svc, err := reg.appKeysWired()
	if err != nil {
		return nil, err
	}
	enabled, err := reg.appKeySwitch(ctx, svc)
	if err != nil {
		return nil, serviceProblem(err)
	}
	if !enabled {
		return nil, CapabilityProblem(StateDisabled, appKeyDomain)
	}
	return svc, nil
}

// appKeysWired answers the service whatever the switch says, for the
// routes that stay open while it is off: a person's own key list and its
// deletes.
func (reg *Registry) appKeysWired() (AppKeyService, error) {
	if reg.deps.AppKeys == nil {
		return nil, CapabilityProblem(StateNotConfigured, appKeyDomain)
	}
	return reg.deps.AppKeys, nil
}

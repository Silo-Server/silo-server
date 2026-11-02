package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

type OrderedApplePushService interface {
	OrderedApplePushAvailable() bool
	RegisterApplePush(context.Context, notifications.ApplePushCommand, evt.SocketIdentity) (notifications.ApplePushReceipt, string, time.Time, error)
}
type ApplePushRegistrationBody struct {
	DeviceID        string `json:"device_id" minLength:"1" maxLength:"128"`
	APNsToken       string `json:"apns_token" minLength:"64" maxLength:"512" writeOnly:"true"`
	APNsEnvironment string `json:"apns_environment" enum:"production,sandbox"`
	APNsTopic       string `json:"apns_topic" enum:"org.siloserver.silo"`
	PushMode        string `json:"push_mode,omitempty" enum:"off,in_app_only,private_push"`
}
type ApplePushRegistrationInput struct {
	AndroidPushAuthority
	Body ApplePushRegistrationBody
}
type ApplePushRegistrationReceipt struct {
	Generation            ID     `json:"generation"`
	ID                    ID     `json:"id"`
	ServerDeviceID        ID     `json:"server_device_id"`
	PushMode              string `json:"push_mode"`
	Enabled               bool   `json:"enabled"`
	Removed               bool   `json:"removed" doc:"The retained intent references a deleted registration. Exact replay never resurrects it."`
	DisplayToken          string `json:"display_token,omitempty"`
	DisplayTokenExpiresAt string `json:"display_token_expires_at,omitempty" format:"date-time"`
	ServerID              string `json:"server_id,omitempty" doc:"This deployment's identity, the server_id getServerIdentity reports. Key this registration's push credentials by it and the registering profile: pushes name both. Absent when unavailable." example:"3f2a9d5e-6b1c-4c7e-9a0d-2f4b8c1e7a35"`
}
type ApplePushRegistrationOutput struct{ Body ApplePushRegistrationReceipt }

// ApplePushRetirementService stops delivery to an Apple installation on the
// authority of its installation key alone.
type ApplePushRetirementService interface {
	ApplePushRetirementAvailable() bool
	RetireApplePush(context.Context, notifications.ApplePushRetirement) (notifications.ApplePushReceipt, error)
}
type ApplePushRetirementBody struct {
	DeviceID string `json:"device_id" minLength:"1" maxLength:"128"`
}
type ApplePushRetirementInput struct {
	AndroidPushAuthority
	Body ApplePushRetirementBody
}
type ApplePushRetirementReceipt struct {
	Generation ID   `json:"generation" doc:"The retirement's generation, which is now the installation's newest intent."`
	Removed    bool `json:"removed" doc:"Always true: the server delivers nothing to this installation until it registers again with a higher generation."`
}
type ApplePushRetirementOutput struct{ Body ApplePushRetirementReceipt }

// ApplePushResetService clears an Apple installation for the account that
// owns it, so a reinstalled app with a new installation key can register.
type ApplePushResetService interface {
	ApplePushResetAvailable() bool
	ResetApplePushInstallation(context.Context, notifications.ApplePushReset) (bool, error)
}
type ApplePushResetAuthority struct {
	InstallationKey string `header:"X-Push-Installation-Key" required:"true" minLength:"43" maxLength:"43" doc:"The caller's current installation key, the one its next registration will send. An installation already bound to it is left alone, so a retried reset cannot undo the registration that followed it."`
}
type ApplePushResetBody struct {
	DeviceID string `json:"device_id" minLength:"1" maxLength:"128"`
}
type ApplePushResetInput struct {
	ApplePushResetAuthority
	Body ApplePushResetBody
}
type ApplePushResetReceipt struct {
	Reset bool `json:"reset" doc:"True when the server cleared the installation's binding and its Apple push registrations. False when there was nothing to clear: no ordered record for this device_id, or one already bound to the sent key, whose generation order still applies. After a cleared or absent record, the next registration with that key binds at any positive generation."`
}
type ApplePushResetOutput struct{ Body ApplePushResetReceipt }
type ApplePushRegistrationCapability struct {
	Capability
	RegistrationAvailable      bool `json:"registration_available" doc:"Local ordered storage and current-login validation are configured; not provider delivery or cluster rollout readiness."`
	InstallationResetAvailable bool `json:"installation_reset_available" doc:"resetApplePushInstallation is served: after a push_installation_key_mismatch refusal the app can reset its own installation and register again."`
}
type ApplePushRegistrationCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         ApplePushRegistrationCapability
}

func applePushProblem(err error) error {
	switch {
	case errors.Is(err, notifications.ErrPushInstallationKeyMismatch):
		return NewProblem(TypePushInstallationKeyMismatch, "This device's push installation is bound to another installation key.")
	case errors.Is(err, notifications.ErrPushDeviceInvalid), errors.Is(err, notifications.ErrPushDeviceUnsupported):
		return NewProblem(TypeValidationFailed, "Invalid Apple push registration.")
	case errors.Is(err, notifications.ErrPushDeviceUnavailable):
		return unavailable("ordered Apple push registration")
	default:
		return orderedPushProblem(err)
	}
}
func registerOrderedApplePush(reg *Registry) {
	capOp := Operation{Operation: humaOp(http.MethodGet, Prefix+"/devices/push/apple/capabilities", "getApplePushRegistrationCapabilities", "notifications", "Describe ordered Apple registration support, separately from delivery and rollout readiness."), Class: ClassProfileScoped, ServiceBacked: true}
	Register(reg, capOp, func(ctx context.Context, _ *CapabilityInput) (*ApplePushRegistrationCapabilityOutput, error) {
		return &ApplePushRegistrationCapabilityOutput{Body: ApplePushRegistrationCapability{Capability: Capability{Allowed: new(capabilityLoginAllowed(ctx))}, RegistrationAvailable: reg.deps.OrderedApplePush != nil && reg.deps.OrderedApplePush.OrderedApplePushAvailable(), InstallationResetAvailable: reg.deps.ApplePushReset != nil && reg.deps.ApplePushReset.ApplePushResetAvailable()}}, nil
	})
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/devices/push/apple", "registerApplePushDevice", "notifications", "Apply an ordered Apple installation intent; exact replay may renew display credentials without changing registration state."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyDomainIdentity}
	op.Errors = []int{409}
	op.MaxBodyBytes = 4096
	Register(reg, op, func(ctx context.Context, in *ApplePushRegistrationInput) (*ApplePushRegistrationOutput, error) {
		if reg.deps.OrderedApplePush == nil || !reg.deps.OrderedApplePush.OrderedApplePushAvailable() {
			return nil, unavailable("ordered Apple push registration")
		}
		claims, p := currentLoginSession(ctx)
		if p != nil {
			return nil, p
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		generation, err := pushGeneration(in.Generation)
		if err != nil {
			return nil, err
		}
		cmd := notifications.ApplePushCommand{UserID: user, ProfileID: profile, InstallationKey: in.InstallationKey, Generation: generation, ApplePushRegistrationInput: notifications.ApplePushRegistrationInput{DeviceID: in.Body.DeviceID, APNsToken: in.Body.APNsToken, APNsEnvironment: in.Body.APNsEnvironment, APNsTopic: in.Body.APNsTopic, PushMode: in.Body.PushMode}}
		identity := evt.SocketIdentity{UserID: user, ProfileID: profile, SessionID: claims.SessionID, Role: claims.Role, ImpersonatorUserID: claims.ImpersonatorUserID, AccessExpiresAt: claims.ExpiresAt.Time}
		if r := requestFrom(ctx); r != nil {
			identity.ProfileToken = r.Header.Get("X-Profile-Token")
			identity.DeviceID = socketDeviceID(r)
		}
		receipt, token, expiry, err := reg.deps.OrderedApplePush.RegisterApplePush(ctx, cmd, identity)
		if err != nil {
			return nil, applePushProblem(err)
		}
		out := ApplePushRegistrationReceipt{Generation: ID(strconv.FormatInt(receipt.Generation, 10)), ID: ID(receipt.RegistrationID), ServerDeviceID: ID(receipt.ServerDeviceID), PushMode: receipt.PushMode, Enabled: receipt.Enabled, Removed: receipt.Removed, DisplayToken: token, ServerID: reg.knownServerID(ctx)}
		if token != "" {
			out.DisplayTokenExpiresAt = expiry.UTC().Format(time.RFC3339Nano)
		}
		return &ApplePushRegistrationOutput{Body: out}, nil
	})
	retire := Operation{Operation: humaOp(http.MethodPost, Prefix+"/devices/push/apple/retire", "retireApplePushInstallation", "notifications", "Stop delivery to an Apple installation without a login session, proved by its installation key. Send it after removing this server, when the session is already gone."), Class: ClassPublic, ServiceBacked: true, RetrySafety: RetrySafetyDomainIdentity, RateLimitBucket: bucketApplePushRetire}
	retire.Errors = []int{http.StatusForbidden, http.StatusConflict, http.StatusTooManyRequests}
	retire.MaxBodyBytes = 1024
	Register(reg, retire, func(ctx context.Context, in *ApplePushRetirementInput) (*ApplePushRetirementOutput, error) {
		if reg.deps.ApplePushRetirement == nil || !reg.deps.ApplePushRetirement.ApplePushRetirementAvailable() {
			return nil, unavailable("Apple push retirement")
		}
		generation, err := pushGeneration(in.Generation)
		if err != nil {
			return nil, err
		}
		receipt, err := reg.deps.ApplePushRetirement.RetireApplePush(ctx, notifications.ApplePushRetirement{DeviceID: in.Body.DeviceID, InstallationKey: in.InstallationKey, Generation: generation})
		if err != nil {
			return nil, applePushProblem(err)
		}
		return &ApplePushRetirementOutput{Body: ApplePushRetirementReceipt{Generation: ID(strconv.FormatInt(receipt.Generation, 10)), Removed: receipt.Removed}}, nil
	})
	reset := Operation{Operation: humaOp(http.MethodPost, Prefix+"/devices/push/apple/reset", "resetApplePushInstallation", "notifications", "Clear this account's Apple installation for a device_id so an app that lost its installation key, as after a reinstall, can register again from any generation."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyDomainIdentity}
	reset.Errors = []int{http.StatusForbidden}
	reset.MaxBodyBytes = 1024
	Register(reg, reset, func(ctx context.Context, in *ApplePushResetInput) (*ApplePushResetOutput, error) {
		if reg.deps.ApplePushReset == nil || !reg.deps.ApplePushReset.ApplePushResetAvailable() {
			return nil, unavailable("Apple push installation reset")
		}
		claims, p := currentLoginSession(ctx)
		if p != nil {
			return nil, p
		}
		done, err := reg.deps.ApplePushReset.ResetApplePushInstallation(ctx, notifications.ApplePushReset{UserID: claims.UserID, DeviceID: in.Body.DeviceID, InstallationKey: in.InstallationKey})
		if err != nil {
			return nil, applePushProblem(err)
		}
		return &ApplePushResetOutput{Body: ApplePushResetReceipt{Reset: done}}, nil
	})
}

// currentLoginSession returns the claims of a bounded login session; display
// credentials and session-less tokens cannot change push installations.
func currentLoginSession(ctx context.Context) (*auth.Claims, *Problem) {
	claims := claimsFrom(ctx)
	if claims == nil || claims.TokenType != auth.TokenTypeAccess || claims.SessionID == "" || claims.ExpiresAt == nil || !claims.ExpiresAt.After(time.Now()) {
		return nil, NewProblem(TypePermissionDenied, "A current login session is required.")
	}
	return claims, nil
}

// bucketApplePushRetire names the per-IP budget for session-less retirement.
// The 256-bit installation key is the guard; the budget is anti-probe hygiene.
const bucketApplePushRetire = "apple_push_retire"

func pushGeneration(value string) (int64, error) {
	generation, err := strconv.ParseInt(value, 10, 64)
	if err != nil || generation <= 0 || strconv.FormatInt(generation, 10) != value {
		return 0, NewProblem(TypeValidationFailed, "X-Push-Generation must be a positive canonical decimal int64.")
	}
	return generation, nil
}

func (c ApplePushRegistrationCapability) capabilityState() string {
	return configuredCapabilityState(c.RegistrationAvailable)
}

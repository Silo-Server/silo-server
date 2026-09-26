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
}
type ApplePushRegistrationOutput struct{ Body ApplePushRegistrationReceipt }
type ApplePushRegistrationCapability struct {
	Capability
	RegistrationAvailable bool `json:"registration_available" doc:"Local ordered storage and current-login validation are configured; not provider delivery or cluster rollout readiness."`
}
type ApplePushRegistrationCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         ApplePushRegistrationCapability
}

func applePushProblem(err error) error {
	switch {
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
		return &ApplePushRegistrationCapabilityOutput{Body: ApplePushRegistrationCapability{Capability: Capability{Allowed: new(capabilityLoginAllowed(ctx))}, RegistrationAvailable: reg.deps.OrderedApplePush != nil && reg.deps.OrderedApplePush.OrderedApplePushAvailable()}}, nil
	})
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/devices/push/apple", "registerApplePushDevice", "notifications", "Apply an ordered Apple installation intent; exact replay may renew display credentials without changing registration state."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyDomainIdentity}
	op.Errors = []int{409}
	op.MaxBodyBytes = 4096
	Register(reg, op, func(ctx context.Context, in *ApplePushRegistrationInput) (*ApplePushRegistrationOutput, error) {
		if reg.deps.OrderedApplePush == nil || !reg.deps.OrderedApplePush.OrderedApplePushAvailable() {
			return nil, unavailable("ordered Apple push registration")
		}
		claims := claimsFrom(ctx)
		if claims.TokenType != auth.TokenTypeAccess || claims.SessionID == "" || claims.ExpiresAt == nil || !claims.ExpiresAt.After(time.Now()) {
			return nil, NewProblem(TypePermissionDenied, "A current login session is required.")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		generation, err := strconv.ParseInt(in.Generation, 10, 64)
		if err != nil || generation <= 0 || strconv.FormatInt(generation, 10) != in.Generation {
			return nil, NewProblem(TypeValidationFailed, "X-Push-Generation must be a positive canonical decimal int64.")
		}
		cmd := notifications.ApplePushCommand{UserID: user, ProfileID: profile, InstallationKey: in.InstallationKey, Generation: generation, ApplePushRegistrationInput: notifications.ApplePushRegistrationInput{DeviceID: in.Body.DeviceID, APNsToken: in.Body.APNsToken, APNsEnvironment: in.Body.APNsEnvironment, APNsTopic: in.Body.APNsTopic, PushMode: in.Body.PushMode}}
		identity := evt.SocketIdentity{UserID: user, ProfileID: profile, SessionID: claims.SessionID, Role: claims.Role, ImpersonatorUserID: claims.ImpersonatorUserID, AccessExpiresAt: claims.ExpiresAt.Time}
		if r := requestFrom(ctx); r != nil {
			identity.ProfileToken = r.Header.Get("X-Profile-Token")
		}
		receipt, token, expiry, err := reg.deps.OrderedApplePush.RegisterApplePush(ctx, cmd, identity)
		if err != nil {
			return nil, applePushProblem(err)
		}
		out := ApplePushRegistrationReceipt{Generation: ID(strconv.FormatInt(receipt.Generation, 10)), ID: ID(receipt.RegistrationID), ServerDeviceID: ID(receipt.ServerDeviceID), PushMode: receipt.PushMode, Enabled: receipt.Enabled, Removed: receipt.Removed, DisplayToken: token}
		if token != "" {
			out.DisplayTokenExpiresAt = expiry.UTC().Format(time.RFC3339Nano)
		}
		return &ApplePushRegistrationOutput{Body: out}, nil
	})
}

func (c ApplePushRegistrationCapability) capabilityState() string {
	return configuredCapabilityState(c.RegistrationAvailable)
}

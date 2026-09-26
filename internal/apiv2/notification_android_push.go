package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type OrderedAndroidPushService interface {
	OrderedAndroidPushAvailable() bool
	ApplyAndroidPush(context.Context, notifications.AndroidPushCommand) (notifications.AndroidPushReceipt, error)
}
type AndroidPushAuthority struct {
	InstallationKey string `header:"X-Push-Installation-Key" required:"true" minLength:"43" maxLength:"43" doc:"Secret 32-byte base64url installation credential. Persist privately; never rotate on retry or account switch."`
	Generation      string `header:"X-Push-Generation" required:"true" maxLength:"19" doc:"Positive decimal int64, persisted and incremented for each new installation intent across account/profile switches. Reuse exactly after uncertainty."`
}
type AndroidPushRegistrationBody struct {
	DeviceID string `json:"device_id" minLength:"1" maxLength:"128"`
	Platform string `json:"platform" enum:"android"`
	Token    string `json:"token" minLength:"64" maxLength:"512" writeOnly:"true"`
	PushMode string `json:"push_mode,omitempty" enum:"off,in_app_only,private_push"`
}
type AndroidPushRegistrationInput struct {
	AndroidPushAuthority
	Body AndroidPushRegistrationBody
}
type AndroidPushRemovalInput struct {
	AndroidPushAuthority
	DeviceID string `path:"device_id"`
}
type AndroidPushRegistrationReceipt struct {
	Generation     ID     `json:"generation"`
	RegistrationID ID     `json:"registration_id"`
	ServerDeviceID ID     `json:"server_device_id"`
	PushMode       string `json:"push_mode"`
}
type AndroidPushRegistrationOutput struct {
	Body AndroidPushRegistrationReceipt
}
type AndroidPushRegistrationCapability struct {
	Capability
	RegistrationAvailable bool     `json:"registration_available" doc:"Local ordered registration storage is configured; does not assert provider delivery availability."`
	Platforms             []string `json:"platforms"`
}
type AndroidPushRegistrationCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AndroidPushRegistrationCapability
}

func orderedPushProblem(err error) error {
	switch {
	case errors.Is(err, notifications.ErrPushGenerationConflict), errors.Is(err, notifications.ErrPushLegacyWriter):
		return NewProblem(TypeConflict, "The installation generation, current owner, or bootstrap ownership conflicts. Do not rebase an uncertain command.")
	case errors.Is(err, notifications.ErrPushInstallationProof):
		return NewProblem(TypePermissionDenied, "The installation credential is invalid.")
	case errors.Is(err, notifications.ErrPushDeviceInvalid), errors.Is(err, notifications.ErrPushDeviceUnsupported):
		return NewProblem(TypeValidationFailed, "Invalid Android push registration.")
	case errors.Is(err, notifications.ErrPushDeviceUnavailable):
		return unavailable("ordered Android push registration")
	default:
		return serviceProblem(err)
	}
}
func (reg *Registry) androidPushCommand(ctx context.Context, authority AndroidPushAuthority, device string) (notifications.AndroidPushCommand, error) {
	if reg.deps.OrderedAndroidPush == nil || !reg.deps.OrderedAndroidPush.OrderedAndroidPushAvailable() {
		return notifications.AndroidPushCommand{}, unavailable("ordered Android push registration")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return notifications.AndroidPushCommand{}, p
	}
	generation, err := strconv.ParseInt(authority.Generation, 10, 64)
	if err != nil || generation <= 0 || strconv.FormatInt(generation, 10) != authority.Generation {
		return notifications.AndroidPushCommand{}, NewProblem(TypeValidationFailed, "X-Push-Generation must be a positive canonical decimal int64.")
	}
	return notifications.AndroidPushCommand{UserID: user, ProfileID: profile, DeviceID: device, InstallationKey: authority.InstallationKey, Generation: generation}, nil
}
func registerOrderedAndroidPush(reg *Registry) {
	capOp := notificationOperation(http.MethodGet, "/push/devices/capabilities", "getPushRegistrationCapabilities")
	capOp.Summary = "Describe ordered registration support separately from push delivery availability."
	Register(reg, capOp, func(context.Context, *CapabilityInput) (*AndroidPushRegistrationCapabilityOutput, error) {
		return &AndroidPushRegistrationCapabilityOutput{Body: AndroidPushRegistrationCapability{RegistrationAvailable: reg.deps.OrderedAndroidPush != nil && reg.deps.OrderedAndroidPush.OrderedAndroidPushAvailable(), Platforms: []string{notifications.PushPlatformAndroid}}}, nil
	})
	op := notificationOperation(http.MethodPost, "/push/devices", "registerPushDevice")
	op.Summary = "Apply one ordered Android installation registration intent. Replay only the exact persisted generation, credential and payload; never advance a generation to retry uncertainty."
	op.RetrySafety = RetrySafetyDomainIdentity
	op.Errors = []int{409}
	op.MaxBodyBytes = 4096
	Register(reg, op, func(ctx context.Context, in *AndroidPushRegistrationInput) (*AndroidPushRegistrationOutput, error) {
		cmd, err := reg.androidPushCommand(ctx, in.AndroidPushAuthority, in.Body.DeviceID)
		if err != nil {
			return nil, err
		}
		cmd.Token, cmd.PushMode = in.Body.Token, in.Body.PushMode
		result, err := reg.deps.OrderedAndroidPush.ApplyAndroidPush(ctx, cmd)
		if err != nil {
			return nil, orderedPushProblem(err)
		}
		return &AndroidPushRegistrationOutput{Body: AndroidPushRegistrationReceipt{Generation: ID(strconv.FormatInt(result.Generation, 10)), RegistrationID: ID(result.RegistrationID), ServerDeviceID: ID(result.ServerDeviceID), PushMode: result.PushMode}}, nil
	})
	remove := notificationOperation(http.MethodDelete, "/push/devices/{device_id}", "unregisterPushDevice")
	remove.Summary = "Apply an ordered removal for the installation's current account/profile. Tombstones prevent stale registration replay."
	remove.RetrySafety = RetrySafetyDomainIdentity
	remove.DefaultStatus = http.StatusNoContent
	remove.Errors = []int{409}
	Register(reg, remove, func(ctx context.Context, in *AndroidPushRemovalInput) (*struct{}, error) {
		cmd, err := reg.androidPushCommand(ctx, in.AndroidPushAuthority, in.DeviceID)
		if err != nil {
			return nil, err
		}
		cmd.Remove = true
		if _, err = reg.deps.OrderedAndroidPush.ApplyAndroidPush(ctx, cmd); err != nil {
			return nil, orderedPushProblem(err)
		}
		return &struct{}{}, nil
	})
}

func (c AndroidPushRegistrationCapability) capabilityState() string {
	return configuredCapabilityState(c.RegistrationAvailable)
}

package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// Device sign-in approved in advance: the account's own signed-in session
// approves a sign-in for a device that is not running yet, bound to the
// device's fingerprint. The device redeems it later with pollDeviceLogin,
// sending the device code and the secret behind the fingerprint. A waiting
// one is listed and revocable by its account and by administrators. See
// docs/architecture/device-login.md, "Approval in advance".

// PreapproveDeviceLoginInput names the device a sign-in is approved for.
type PreapproveDeviceLoginInput struct {
	RawBody []byte
	Body    struct {
		DeviceName     string `json:"device_name" minLength:"1" maxLength:"128" doc:"Name of the device, shown in the waiting list and on its session" example:"Living room TV"`
		DevicePlatform string `json:"device_platform,omitempty" maxLength:"64" doc:"Platform of the device" example:"tvos"`
		Binding        string `json:"binding" pattern:"^[A-Za-z0-9_-]{43}$" doc:"The device's fingerprint: base64url without padding of SHA-256 of a 32-byte secret the device keeps. Only a poll sending that secret as binding_secret redeems the sign-in" example:"Zmh6rfhivXdsj8GLjp-OIAiXFIVu4jOzkCpZHQ1fKSU"`
	}
}

// DevicePreapprovalCreated is what the approver hands to the device.
type DevicePreapprovalCreated struct {
	ID         string  `json:"id" doc:"Identifier of the waiting sign-in, for the list and revoke" example:"6f1c2a1e-8d3b-4f0e-9a7c-2b5d8e1f3a4c"`
	DeviceCode string  `json:"device_code" doc:"Secret the device polls with; returned only here" example:"d3v1c3c0d3"`
	ExpiresAt  Instant `json:"expires_at" doc:"When the sign-in stops being redeemable, 30 days after it was approved" example:"2026-02-01T03:04:05.678Z"`
}

// DevicePreapprovalCreatedOutput is the preapproveDeviceLogin response.
type DevicePreapprovalCreatedOutput struct {
	Body DevicePreapprovalCreated
}

// DevicePreapproval is a waiting pre-approved sign-in. It never carries the
// device code or the fingerprint.
type DevicePreapproval struct {
	ID             string  `json:"id" doc:"Identifier of the waiting sign-in" example:"6f1c2a1e-8d3b-4f0e-9a7c-2b5d8e1f3a4c"`
	DeviceName     string  `json:"device_name" doc:"Device name it was approved for" example:"Living room TV"`
	DevicePlatform string  `json:"device_platform" doc:"Platform it was approved for" example:"tvos"`
	CreatedAt      Instant `json:"created_at" doc:"When it was approved" example:"2026-01-02T03:04:05.678Z"`
	ExpiresAt      Instant `json:"expires_at" doc:"When it stops being redeemable" example:"2026-02-01T03:04:05.678Z"`
}

// DevicePreapprovalCollectionOutput is a bounded list of waiting sign-ins.
type DevicePreapprovalCollectionOutput struct {
	Body Collection[DevicePreapproval]
}

// DevicePreapprovalInput names one of the caller's waiting sign-ins.
type DevicePreapprovalInput struct {
	ID ID `path:"id" pattern:"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$" doc:"The waiting sign-in" example:"6f1c2a1e-8d3b-4f0e-9a7c-2b5d8e1f3a4c"`
}

// AdminDevicePreapprovalInput names one of an account's waiting sign-ins.
type AdminDevicePreapprovalInput struct {
	UserID ID `path:"user_id"`
	ID     ID `path:"id" pattern:"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$" doc:"The waiting sign-in" example:"6f1c2a1e-8d3b-4f0e-9a7c-2b5d8e1f3a4c"`
}

// preapprovalSessionRule says who may approve in advance.
const preapprovalSessionRule = "Only the account's own signed-in session: an API key, a session an administrator opened as the account, a remote-playback session, or a session opened by device sign-in or by another app's key is 403 permission_denied. A session opened before this server recorded how sessions open is refused the same way; signing in again clears that."

func registerDevicePreapprovals(reg *Registry) {
	create := humaOp(http.MethodPost, Prefix+"/auth/device/preapprove", "preapproveDeviceLogin", "device-login",
		"Approve a device sign-in in advance for the device holding the secret behind a fingerprint.")
	create.Description = preapprovalSessionRule + " The sign-in is single use and lasts 30 days. An account has at most 20 waiting; another is 409 conflict. Refused in demo mode for a caller who is not an administrator, and the capability then reports preapprove false." + mainProfileRule
	create.DefaultStatus = http.StatusCreated
	create.Errors = []int{http.StatusForbidden, http.StatusConflict}
	Register(reg, withOrigins(mainProfileOnly(Operation{
		Operation: create, RetrySafety: RetrySafetyNonRetryable,
		ServiceBacked: true, Audited: true, RateLimitBucket: bucketDeviceStart, DemoRestricted: true,
	}), grantedRefusedSignIn), reg.preapproveDeviceLogin)
	list := humaOp(http.MethodGet, Prefix+"/auth/device/preapprovals", "listDevicePreapprovals", "device-login",
		"List the account's device sign-ins approved in advance and not yet redeemed.")
	list.Description = preapprovalSessionRule + demoVisitorKeyListRule
	list.Errors = []int{http.StatusForbidden}
	Register(reg, Operation{Operation: list, Class: ClassAuthenticated, ServiceBacked: true, Origins: grantedRefusedSignIn}, reg.listDevicePreapprovals)
	revoke := humaOp(http.MethodDelete, Prefix+"/auth/device/preapprovals/{id}", "revokeDevicePreapproval", "device-login",
		"Revoke a device sign-in approved in advance; the device's next poll answers denied.")
	// A demo's visitors share one account: withdrawing a sign-in someone
	// else approved is refused like approving one in advance.
	revoke.Description = preapprovalSessionRule + " Refused in demo mode for a caller who is not an administrator."
	revoke.DefaultStatus = http.StatusNoContent
	revoke.Errors = []int{http.StatusForbidden, http.StatusNotFound}
	Register(reg, Operation{Operation: revoke, RetrySafety: RetrySafetyNaturalIdempotent, Class: ClassAuthenticated, ServiceBacked: true, DemoRestricted: true, Origins: grantedRefusedSignIn}, reg.revokeDevicePreapproval)

	root := "/{user_id}/device-preapprovals"
	adminList := adminAccountOperation(http.MethodGet, root, "listAdminUserDevicePreapprovals", false)
	adminList.Summary = "List a user's device sign-ins approved in advance and not yet redeemed."
	adminList.Errors = []int{http.StatusNotFound, http.StatusForbidden}
	Register(reg, adminList, reg.listAdminDevicePreapprovals)
	adminRevoke := adminAccountOperation(http.MethodDelete, root+"/{id}", "revokeAdminUserDevicePreapproval", false)
	adminRevoke.Summary = "Revoke one of a user's device sign-ins approved in advance."
	adminRevoke.DefaultStatus = http.StatusNoContent
	adminRevoke.RetrySafety = RetrySafetyNaturalIdempotent
	adminRevoke.Errors = []int{http.StatusNotFound, http.StatusForbidden}
	Register(reg, adminRevoke, reg.revokeAdminDevicePreapproval)
}

// ownLoginSession admits only the account's own login session.
func ownLoginSession(ctx context.Context) (*auth.Claims, *Problem) {
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if !claims.IsOwnLoginSession() {
		return nil, NewProblem(TypePermissionDenied, "Only the account's own signed-in session can approve a device in advance or manage those approvals.")
	}
	return claims, nil
}

func (reg *Registry) preapproveDeviceLogin(ctx context.Context, in *PreapproveDeviceLoginInput) (*DevicePreapprovalCreatedOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	if reg.deps.Devices == nil {
		return nil, unavailable("device login")
	}
	claims, p := ownLoginSession(ctx)
	if p != nil {
		return nil, p
	}
	// A sign-in approved in advance is lasting access to the account every
	// profile shares, so it takes the household's main profile, as enrolling
	// a key does. Approving a sign-in that is waiting now keeps no such rule.
	acting, err := reg.requireVerifiedMainProfile(ctx, reg.deps.Devices)
	if err != nil {
		return nil, err
	}
	result, err := reg.deps.Devices.PreapproveDeviceLogin(ctx, auth.DevicePreapprovalInput{
		DeviceName: in.Body.DeviceName, DevicePlatform: in.Body.DevicePlatform, Binding: in.Body.Binding, Acting: acting,
	}, claims.UserID)
	if errors.Is(err, auth.ErrActingProfileChanged) {
		return nil, mainProfileChangedProblem()
	}
	if err != nil {
		return nil, deviceProblem(err)
	}
	return &DevicePreapprovalCreatedOutput{Body: DevicePreapprovalCreated{
		ID: result.ID, DeviceCode: result.DeviceCode, ExpiresAt: NewInstant(result.ExpiresAt),
	}}, nil
}

func (reg *Registry) listDevicePreapprovals(ctx context.Context, _ *struct{}) (*DevicePreapprovalCollectionOutput, error) {
	if reg.deps.Devices == nil {
		return nil, unavailable("device login")
	}
	claims, p := ownLoginSession(ctx)
	if p != nil {
		return nil, p
	}
	if visitor, p := demoVisitorOwnSession(ctx, reg.deps.DemoSettings); p != nil {
		return nil, p
	} else if visitor {
		return devicePreapprovalPage(nil), nil
	}
	items, err := reg.deps.Devices.ListDevicePreapprovals(ctx, claims.UserID)
	if err != nil {
		return nil, deviceProblem(err)
	}
	return devicePreapprovalPage(items), nil
}

func (reg *Registry) revokeDevicePreapproval(ctx context.Context, in *DevicePreapprovalInput) (*struct{}, error) {
	if reg.deps.Devices == nil {
		return nil, unavailable("device login")
	}
	claims, p := ownLoginSession(ctx)
	if p != nil {
		return nil, p
	}
	if err := reg.deps.Devices.RevokeDevicePreapproval(ctx, claims.UserID, string(in.ID)); err != nil {
		return nil, deviceProblem(err)
	}
	return nil, nil
}

func (reg *Registry) listAdminDevicePreapprovals(ctx context.Context, in *AdminLoginSessionInput) (*DevicePreapprovalCollectionOutput, error) {
	if reg.deps.Devices == nil {
		return nil, unavailable("device login")
	}
	id, p := adminAccountID(in.UserID)
	if p != nil {
		return nil, p
	}
	items, err := reg.deps.Devices.AdminListDevicePreapprovals(ctx, id)
	if err != nil {
		return nil, deviceProblem(err)
	}
	return devicePreapprovalPage(items), nil
}

func (reg *Registry) revokeAdminDevicePreapproval(ctx context.Context, in *AdminDevicePreapprovalInput) (*struct{}, error) {
	if reg.deps.Devices == nil {
		return nil, unavailable("device login")
	}
	id, p := adminAccountID(in.UserID)
	if p != nil {
		return nil, p
	}
	if err := reg.deps.Devices.AdminRevokeDevicePreapproval(ctx, id, string(in.ID)); err != nil {
		return nil, deviceProblem(err)
	}
	return nil, nil
}

func devicePreapprovalPage(items []auth.DevicePreapproval) *DevicePreapprovalCollectionOutput {
	out := make([]DevicePreapproval, 0, len(items))
	for _, it := range items {
		out = append(out, DevicePreapproval{
			ID: it.ID, DeviceName: it.DeviceName, DevicePlatform: it.DevicePlatform,
			CreatedAt: NewInstant(it.CreatedAt), ExpiresAt: NewInstant(it.ExpiresAt),
		})
	}
	return &DevicePreapprovalCollectionOutput{Body: NewCollection(out)}
}

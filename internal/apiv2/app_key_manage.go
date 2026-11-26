package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/models"
)

// AppPersonKey is the account's person key as its owner sees it.
type AppPersonKey struct {
	KeyID             string  `json:"kid" doc:"The person key's ID" example:"PgujdSfBbNNyXngLWCsOgw"`
	EnrolledAt        Instant `json:"enrolled_at" doc:"When the key was enrolled"`
	EnrolledIPAddress string  `json:"enrolled_ip_address" doc:"Client address of the request that enrolled the key, decided as a login session's ip_address is, except that an unspecified address (0.0.0.0 or ::) is stored as none; empty when unknown, which includes every key enrolled before this server recorded it, and for a caller who is not an administrator while the server runs in demo mode" example:"203.0.113.7"`
}

// AppDeviceKey is one device or app key of the account.
type AppDeviceKey struct {
	ID                string   `json:"id" format:"uuid" doc:"The server's ID for the device key" example:"0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11"`
	KeyID             string   `json:"kid" doc:"The device key's ID" example:"zT321tSgcB7emUH9k0B0oA"`
	Kind              string   `json:"kind" enum:"device,app" doc:"device: an install of the person's own app; app: another app the person allowed" example:"device"`
	Name              string   `json:"name" doc:"The name the device gave itself" example:"Sam's iPhone"`
	Platform          string   `json:"platform" doc:"The platform the device named; empty when it named none" example:"ios"`
	Status            string   `json:"status" enum:"active,pending" doc:"active, or pending while an administrator's device waits for approval. A waiting key older than 24 hours is left out of the list without anyone denying or revoking it" example:"active"`
	EnrolledBy        string   `json:"enrolled_by" enum:"introduce,session,app-code,grant" doc:"How the key was enrolled: introduced by the person key, from a signed-in session, with an app code, or through a grant" example:"introduce"`
	AllowedBy         *string  `json:"allowed_by" nullable:"true" doc:"For a key a grant enrolled, the name of the account whose person key signed the grant; null otherwise" example:"sam"`
	EnrolledAt        Instant  `json:"enrolled_at" doc:"When the key was enrolled"`
	EnrolledIPAddress string   `json:"enrolled_ip_address" doc:"Client address of the request that enrolled the key, decided as a login session's ip_address is, except that an unspecified address (0.0.0.0 or ::) is stored as none; empty when unknown, which includes every key enrolled before this server recorded it, and for a caller who is not an administrator while the server runs in demo mode" example:"203.0.113.7"`
	LastUsedAt        *Instant `json:"last_used_at" nullable:"true" doc:"When the key last opened a login session. enroll and an introduce that signs in open one, so a key not used since shows enrolled_at; null for a key that has never opened one: a key waiting for approval, and a key approved since that has not signed in yet. status, not this member, says whether a key is waiting"`
}

// AppKeyList is a person's own keys.
type AppKeyList struct {
	PersonKey *AppPersonKey  `json:"person_key,omitempty" doc:"The account's person key; absent when it has none"`
	Devices   []AppDeviceKey `json:"devices" doc:"Device and app keys, newest first; revoked and denied keys are left out"`
}

// AppKeyListOutput is the listAppKeys response.
type AppKeyListOutput struct {
	Body AppKeyList
}

// RevokeDeviceKeyInput names one of the account's device keys.
type RevokeDeviceKeyInput struct {
	ID string `path:"id" format:"uuid" doc:"The device key's id from listAppKeys" example:"0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11"`
}

// ReplacePersonKeyInput is the replacement person key.
type ReplacePersonKeyInput struct {
	RawBody []byte
	Body    struct {
		PersonKey string `json:"person_key" minLength:"87" maxLength:"87" doc:"The new person key: base64url of its 65-byte uncompressed P-256 point" example:"BIXPevG2yxPrIkwoRlSuZ0c0DldsVX_3OET3S0YeP0uDN7Lj-SQMccAMdvNimX-OvE62RqjlO_Fll-F9MU_kw5Q"`
	}
}

// PersonKeyReplaced names the account's person key after a replacement.
type PersonKeyReplaced struct {
	PersonKeyID string `json:"person_key_id" doc:"The new person key's ID (kid)" example:"PgujdSfBbNNyXngLWCsOgw"`
}

// PersonKeyReplacedOutput is the replacePersonKey response.
type PersonKeyReplacedOutput struct {
	Body PersonKeyReplaced
}

const ownKeySessionRule = "Only the account's own login session: an API key and an administrator acting as the account are 403 permission_denied. A session opened before this server recorded how sessions open is refused the same way; signing in again clears that."

func registerAppKeyManagement(reg *Registry) {
	list := humaOp(http.MethodGet, Prefix+"/auth/keys", "listAppKeys", "app-keys", "List the signed-in account's app keys.")
	list.Description = pendingSessionRule + " The list names every device the person signed in, so a TV's or another app's session can't read it. Answers while auth.app_key_sign_in is off, so keys can still be reviewed and revoked."
	list.Errors = []int{http.StatusForbidden}
	Register(reg, Operation{Operation: list, Class: ClassAuthenticated, ServiceBacked: true, Origins: grantedRefusedSignIn}, reg.listAppKeys)

	revoke := humaOp(http.MethodDelete, Prefix+"/auth/keys/devices/{id}", "revokeAppDeviceKey", "app-keys",
		"Revoke one of the signed-in account's device keys and end the sessions it opened.")
	revoke.Description = ownKeySessionRule + " A device may revoke its own key, which ends the session making the request. A session opened by another app's key may revoke only that key, and one opened by device sign-in none (403 permission_denied). Revoking a revoked key changes nothing. Answers while auth.app_key_sign_in is off. Refused in demo mode for a caller who is not an administrator."
	revoke.DefaultStatus = http.StatusNoContent
	revoke.Errors = []int{http.StatusForbidden, http.StatusNotFound}
	// A demo's visitors share one account: revoking a key ends every session
	// it opened, as ending another visitor's session does.
	Register(reg, Operation{Operation: revoke, RetrySafety: RetrySafetyNaturalIdempotent, Class: ClassAuthenticated, ServiceBacked: true, DemoRestricted: true, Origins: grantedOwnKeyRevoke}, reg.revokeAppDeviceKey)

	replace := humaOp(http.MethodPut, Prefix+"/auth/keys/person", "replacePersonKey", "app-keys",
		"Replace the signed-in account's person key.")
	replace.Description = ownKeySessionRule + " A session opened by device sign-in or by another app's key is refused too. The old person key is revoked: new devices need the new one, and devices already enrolled keep their own keys. A device the old key introduced that is still waiting for approval is denied with it. Sending the current key again changes nothing; a public key enrolled anywhere else on this server is 409 conflict. 409 capability_disabled while auth.app_key_sign_in is off." + mainProfileRule
	replace.Errors = []int{http.StatusForbidden, http.StatusConflict}
	Register(reg, withOrigins(mainProfileOnly(Operation{Operation: replace, RetrySafety: RetrySafetyNaturalIdempotent, ServiceBacked: true, Audited: true}), grantedRefusedSignIn), reg.replacePersonKey)
}

func (reg *Registry) listAppKeys(ctx context.Context, _ *struct{}) (*AppKeyListOutput, error) {
	svc, err := reg.appKeysWired()
	if err != nil {
		return nil, err
	}
	keys, err := svc.ListOwnAppKeys(ctx, claimsFrom(ctx))
	if err != nil {
		return nil, appKeyProblem(err)
	}
	hide := reg.hideEnrolledAddresses(ctx)
	out := AppKeyList{Devices: make([]AppDeviceKey, 0, len(keys.Devices))}
	if keys.Person != nil {
		person := appPersonKeyOf(keys.Person)
		if hide {
			person.EnrolledIPAddress = ""
		}
		out.PersonKey = &person
	}
	for _, key := range keys.Devices {
		device := appDeviceKeyOf(key, keys.AllowedBy)
		if hide {
			device.EnrolledIPAddress = ""
		}
		out.Devices = append(out.Devices, device)
	}
	return &AppKeyListOutput{Body: out}, nil
}

// hideEnrolledAddresses reports whether the caller's own key lists leave out
// enrolled_ip_address: in demo mode every visitor shares one account, so a
// caller who is not an administrator sees no address. A demo setting that
// can't be read hides them too (demoRestricted answers restricted).
func (reg *Registry) hideEnrolledAddresses(ctx context.Context) bool {
	restricted, _ := demoRestricted(ctx, reg.deps.DemoSettings)
	return restricted
}

// appPersonKeyOf is the person key object of a key.
func appPersonKeyOf(key *models.AppPersonKey) AppPersonKey {
	return AppPersonKey{KeyID: key.KeyID, EnrolledAt: NewInstant(key.CreatedAt), EnrolledIPAddress: key.EnrolledIP}
}

// appDeviceKeyOf is the device object of a key. allowedBy is the account
// name shown beside a key a grant enrolled.
func appDeviceKeyOf(key *models.AppDeviceKey, allowedBy string) AppDeviceKey {
	device := AppDeviceKey{
		ID: key.ID, KeyID: key.KeyID, Kind: key.Kind, Name: key.Name, Platform: key.Platform, Status: key.Status,
		EnrolledBy: enrolledBy(key.EnrolledVia), EnrolledAt: NewInstant(key.CreatedAt), EnrolledIPAddress: key.EnrolledIP,
	}
	if key.EnrolledVia == models.AppKeyEnrolledGrant {
		device.AllowedBy = &allowedBy
	}
	if key.LastUsedAt != nil {
		used := NewInstant(*key.LastUsedAt)
		device.LastUsedAt = &used
	}
	return device
}

// enrolledBy is the wire spelling of how a key was enrolled.
func enrolledBy(via string) string {
	if via == models.AppKeyEnrolledAppCode {
		return "app-code"
	}
	return via
}

func (reg *Registry) revokeAppDeviceKey(ctx context.Context, in *RevokeDeviceKeyInput) (*struct{}, error) {
	svc, err := reg.appKeysWired()
	if err != nil {
		return nil, err
	}
	if err := svc.RevokeOwnDeviceKey(ctx, claimsFrom(ctx), in.ID); err != nil {
		return nil, appKeyProblem(err)
	}
	return nil, nil
}

func (reg *Registry) replacePersonKey(ctx context.Context, in *ReplacePersonKeyInput) (*PersonKeyReplacedOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	svc, err := reg.appKeysOn(ctx)
	if err != nil {
		return nil, err
	}
	acting, err := reg.requireVerifiedMainProfile(ctx, svc)
	if err != nil {
		return nil, err
	}
	key, err := appkeys.ParsePublicKey(in.Body.PersonKey)
	if err != nil {
		return nil, validationProblem(locationBody+".person_key", codeInvalid, "person_key is not a P-256 public key.")
	}
	if err := svc.ReplacePersonKey(ctx, claimsFrom(ctx), key, acting); err != nil {
		return nil, appKeyProblem(err)
	}
	return &PersonKeyReplacedOutput{Body: PersonKeyReplaced{PersonKeyID: key.KeyID()}}, nil
}

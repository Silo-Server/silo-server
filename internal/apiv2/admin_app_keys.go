package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// AdminAppKeyService is an administrator's view of one account's app keys
// (docs/architecture/app-keys.md, "Administrators"). *handlers.AuthHandler
// implements it; the mutations read the acting administrator from the
// request and apply the Owner rules.
type AdminAppKeyService interface {
	AdminListAppKeys(ctx context.Context, userID int) (auth.AdminAccountKeys, error)
	AdminRevokeAppDeviceKey(ctx context.Context, userID int, id string) error
	AdminRevokeAppPersonKey(ctx context.Context, userID int) error
	AdminDenyPendingAppKey(ctx context.Context, userID int, id string) error
}

// AdminAppPersonKey is an account's person key as an administrator sees it.
type AdminAppPersonKey struct {
	AppPersonKey
	EnrolledBy    string `json:"enrolled_by" enum:"session,app-code" doc:"How the key was enrolled: from a signed-in session or with an app code" example:"session"`
	ProviderBound bool   `json:"provider_bound" doc:"Bound to a sign-in provider account of this account, from enrollment or since that account was linked: it vouches for a device only while that provider account is linked. It says what the key is tied to, not that the provider was asked just now"`
	IdentityGone  bool   `json:"identity_gone" doc:"Provider-bound and its provider account is no longer linked to the account: it can't introduce a device again"`
	IdentityID    *ID    `json:"identity_id" nullable:"true" doc:"The provider account the key is bound to, as listAdminUserIdentities identifies it; null when the key is bound to none. Unlinking that provider account revokes the key. With identity_gone true the ID is historical: that provider account is no longer linked and the identities list does not have it" example:"4"`
}

// AdminAppDeviceKey is one of an account's device keys as an administrator
// sees it: the person's own list object plus how it signs in.
type AdminAppDeviceKey struct {
	AppDeviceKey
	ProviderBound bool     `json:"provider_bound" doc:"Bound to a sign-in provider account of this account, from enrollment, from the person key that introduced it, or since that account was linked. The server checks that the account is still linked at each sign-in and asks the provider again when its recheck is due, not every time"`
	IdentityGone  bool     `json:"identity_gone" doc:"Provider-bound and its provider account is no longer linked to the account: it can't sign in again and is revoked at its next try. The person's own list leaves such a key out"`
	IdentityID    *ID      `json:"identity_id" nullable:"true" doc:"The provider account the key is bound to, as listAdminUserIdentities identifies it; null when the key is bound to none. Unlinking that provider account revokes the key. With identity_gone true the ID is historical: that provider account is no longer linked and the identities list does not have it" example:"4"`
	WaitingUntil  *Instant `json:"waiting_until" nullable:"true" doc:"For a key with status pending, when it stops waiting for approval (24 hours after it asked); null otherwise"`
}

// AdminAppKeyList is an account's app keys as an administrator sees them.
type AdminAppKeyList struct {
	PersonKey *AdminAppPersonKey  `json:"person_key,omitempty" doc:"The account's person key; absent when it has none"`
	Devices   []AdminAppDeviceKey `json:"devices" doc:"Active keys, keys waiting for approval inside their 24 hours, and keys whose provider account is gone, newest first; revoked and denied keys are left out"`
}

// AdminAppKeyListOutput is the listAdminUserAppKeys response.
type AdminAppKeyListOutput struct {
	Body AdminAppKeyList
}

// AdminAppKeysInput names the account.
type AdminAppKeysInput struct {
	UserID ID `path:"user_id"`
}

// AdminAppDeviceKeyInput names one of the account's device keys.
type AdminAppDeviceKeyInput struct {
	UserID ID     `path:"user_id"`
	ID     string `path:"id" format:"uuid" doc:"The device key's id from listAdminUserAppKeys" example:"0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11"`
}

// AdminPendingAppKeyInput names one of the account's waiting device keys;
// the body is an empty JSON object.
type AdminPendingAppKeyInput struct {
	UserID  ID     `path:"user_id"`
	ID      string `path:"id" format:"uuid" doc:"The waiting device key's id from listAdminUserAppKeys" example:"0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11"`
	RawBody []byte
	Body    struct{}
}

// adminKeyRule is who may change another account's keys.
const adminKeyRule = " Any administrator may act on an ordinary account and on their own; only the server Owner may act on another administrator or on the Owner (403 permission_denied). Answers while auth.app_key_sign_in is off."

func adminAppKeyOperation(method, path, id, summary, description string) Operation {
	op := adminAccountOperation(method, "/{user_id}/keys"+path, id, false)
	op.Summary = summary
	op.Description = description
	op.Errors = []int{http.StatusNotFound, http.StatusForbidden}
	if method != http.MethodGet {
		op.DefaultStatus = http.StatusNoContent
		op.RetrySafety = RetrySafetyNaturalIdempotent
	}
	return op
}

func registerAdminAppKeys(reg *Registry) {
	Register(reg, adminAppKeyOperation(http.MethodGet, "", "listAdminUserAppKeys",
		"List an account's Silo app keys: its person key, its devices and the devices waiting for approval.",
		"Shows what the person's own listAppKeys shows, plus how each key signs in and the keys whose sign-in provider account is gone. Any administrator may list any account. Answers while auth.app_key_sign_in is off."),
		reg.listAdminUserAppKeys)
	Register(reg, adminAppKeyOperation(http.MethodDelete, "/devices/{id}", "revokeAdminUserDeviceKey",
		"Revoke one of an account's device keys and end the sessions it opened.",
		"The device is signed out and its key can't sign in again. Revoking a revoked key changes nothing; a key of another account is 404 not_found. Audited as user.device_key_revoked."+adminKeyRule),
		reg.revokeAdminUserDeviceKey)
	// The request names no key: it revokes whichever person key the account
	// holds when it arrives. A blind repeat after the person has set up a new
	// one would revoke that, so it is not declared safe to replay.
	revokePerson := adminAppKeyOperation(http.MethodDelete, "/person", "revokeAdminUserPersonKey",
		"Revoke an account's person key.",
		"No new device can be introduced with it, and the devices it introduced that are still waiting for approval are denied. Devices already enrolled keep their keys and sessions. The person sets up a new person key from a signed-in device (replacePersonKey). An account with no person key, as straight after a first call, changes nothing and records nothing (204); an unknown account is 404 not_found. The request names no key, so it acts on the person key the account holds when it arrives: read listAdminUserAppKeys before repeating it, since a repeat after the person has set up a new key revokes that one. Audited as user.person_key_revoked."+adminKeyRule)
	revokePerson.RetrySafety = RetrySafetyNonRetryable
	Register(reg, revokePerson, reg.revokeAdminUserPersonKey)
	Register(reg, adminAppKeyOperation(http.MethodPost, "/pending/{id}/deny", "denyAdminUserPendingKey",
		"Deny one of an account's device keys waiting for approval.",
		"The key's next sign-in answers 403 key_denied and neither list shows it. Denying a denied key changes nothing; an active, revoked or unknown key is 404 not_found. No administrator operation approves a waiting key: only the person does, from one of their own sessions (approvePendingAppKey). Audited as user.device_key_denied."+adminKeyRule),
		reg.denyAdminUserPendingKey)
}

func (reg *Registry) adminAppKeys() (AdminAppKeyService, *Problem) {
	if reg.deps.AdminAppKeys == nil {
		return nil, unavailable("app key administration")
	}
	return reg.deps.AdminAppKeys, nil
}

// adminAppKeyProblem maps an administrator key operation's error: a missing
// account as account administration answers it, the rest as the app key
// operations do.
func adminAppKeyProblem(err error) error {
	if errors.Is(err, auth.ErrNotFound) {
		return adminAccountError(err)
	}
	return appKeyProblem(err)
}

func (reg *Registry) listAdminUserAppKeys(ctx context.Context, in *AdminAppKeysInput) (*AdminAppKeyListOutput, error) {
	svc, p := reg.adminAppKeys()
	if p != nil {
		return nil, p
	}
	id, p := adminAccountID(in.UserID)
	if p != nil {
		return nil, p
	}
	keys, err := svc.AdminListAppKeys(ctx, id)
	if err != nil {
		return nil, adminAppKeyProblem(err)
	}
	out := AdminAppKeyList{Devices: make([]AdminAppDeviceKey, 0, len(keys.Devices))}
	if person := keys.Person; person != nil {
		out.PersonKey = &AdminAppPersonKey{
			AppPersonKey:  appPersonKeyOf(person.AppPersonKey),
			EnrolledBy:    enrolledBy(person.EnrolledVia),
			ProviderBound: person.ProviderBound,
			IdentityGone:  person.IdentityGone,
			IdentityID:    keyIdentityID(person.ProviderBound, person.IdentityID),
		}
	}
	for _, key := range keys.Devices {
		device := AdminAppDeviceKey{
			AppDeviceKey:  appDeviceKeyOf(key.AppDeviceKey, keys.AllowedBy),
			ProviderBound: key.ProviderBound,
			IdentityGone:  key.IdentityGone,
			IdentityID:    keyIdentityID(key.ProviderBound, key.IdentityID),
		}
		if device.Status == "pending" {
			until := NewInstant(key.CreatedAt.Add(appkeys.PendingKeyLifetime))
			device.WaitingUntil = &until
		}
		out.Devices = append(out.Devices, device)
	}
	return &AdminAppKeyListOutput{Body: out}, nil
}

func (reg *Registry) revokeAdminUserDeviceKey(ctx context.Context, in *AdminAppDeviceKeyInput) (*struct{}, error) {
	svc, p := reg.adminAppKeys()
	if p != nil {
		return nil, p
	}
	id, p := adminAccountID(in.UserID)
	if p != nil {
		return nil, p
	}
	if err := svc.AdminRevokeAppDeviceKey(ctx, id, in.ID); err != nil {
		return nil, adminAppKeyProblem(err)
	}
	return nil, nil
}

func (reg *Registry) revokeAdminUserPersonKey(ctx context.Context, in *AdminAppKeysInput) (*struct{}, error) {
	svc, p := reg.adminAppKeys()
	if p != nil {
		return nil, p
	}
	id, p := adminAccountID(in.UserID)
	if p != nil {
		return nil, p
	}
	if err := svc.AdminRevokeAppPersonKey(ctx, id); err != nil {
		return nil, adminAppKeyProblem(err)
	}
	return nil, nil
}

func (reg *Registry) denyAdminUserPendingKey(ctx context.Context, in *AdminPendingAppKeyInput) (*struct{}, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	svc, p := reg.adminAppKeys()
	if p != nil {
		return nil, p
	}
	id, p := adminAccountID(in.UserID)
	if p != nil {
		return nil, p
	}
	if err := svc.AdminDenyPendingAppKey(ctx, id, in.ID); err != nil {
		return nil, adminAppKeyProblem(err)
	}
	return nil, nil
}

// keyIdentityID is the wire identity a provider-bound key is bound to, nil
// for a key that isn't provider-bound or whose session had no identity.
func keyIdentityID(providerBound bool, identityID *int64) *ID {
	if !providerBound || identityID == nil {
		return nil
	}
	id := IDFromInt(*identityID)
	return &id
}

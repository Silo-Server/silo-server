package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// The administrator's management of one account's household profiles: the
// same bodies, validation and writes as the profiles operations, addressed by
// account instead of by the signed-in household. The household-manager check
// does not apply; the acting-admin check and the Owner rules replace it. That is how an
// administrator resets a forgotten profile PIN, including the primary
// profile's: updateAdminUserProfile with "pin": null removes it, and a new
// value replaces it. Writes are audited like every /admin mutation.

// AdminProfileInput addresses one profile of an account.
type AdminProfileInput struct {
	ID        ID     `path:"id" doc:"The account" example:"7"`
	ProfileID string `path:"profile_id" minLength:"1" maxLength:"128" doc:"A profile of the account" example:"p-owner"`
}

// AdminProfileCreateInput is the createAdminUserProfile request.
type AdminProfileCreateInput struct {
	ID   ID `path:"id" doc:"The account" example:"7"`
	Body ProfileCreate
	// RawBody is the document as sent; see ProfileUpdateInput.
	RawBody []byte
}

// AdminProfileUpdateInput is the updateAdminUserProfile request.
type AdminProfileUpdateInput struct {
	ID        ID     `path:"id" doc:"The account" example:"7"`
	ProfileID string `path:"profile_id" minLength:"1" maxLength:"128" doc:"A profile of the account" example:"p-owner"`
	Body      ProfileUpdate
	// RawBody is the document as sent; see ProfileUpdateInput.
	RawBody []byte
}

const adminProfilePath = "/{id}/profiles/{profile_id}"

func registerAdminProfiles(reg *Registry) {
	get := adminAccountOperation(http.MethodGet, adminProfilePath, "getAdminUserProfile", false)
	get.Summary = "Get one profile of an account."
	get.Description = "The profile as listProfiles reports it to its own household. 404 not_found when the profile does not belong to the account."
	Register(reg, get, func(ctx context.Context, in *AdminProfileInput) (*ProfileOutput, error) {
		svc, id, p := reg.adminProfilesTarget(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		view, err := svc.GetProfile(ctx, id, in.ProfileID)
		if err != nil {
			return nil, profileProblem(err)
		}
		return profileOutputOf(view)
	})

	create := adminAccountOperation(http.MethodPost, "/{id}/profiles", "createAdminUserProfile", false)
	create.Summary = "Create a profile on an account."
	create.Description = "Takes the createProfile body. A household already at the account's profile limit, or a name another profile on the account uses, is 409 conflict. The first profile on an account becomes its primary profile. Only the server Owner may change the Owner's or another admin account's profiles (403 permission_denied otherwise): a primary profile's PIN guards that account's admin access."
	create.DefaultStatus = http.StatusCreated
	create.Errors = append(create.Errors, http.StatusConflict)
	Register(reg, create, func(ctx context.Context, in *AdminProfileCreateInput) (*ProfileCreatedOutput, error) {
		svc, id, p := reg.adminProfilesTarget(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		req, p := reg.profileCreateRequest(ctx, in.RawBody, in.Body)
		if p != nil {
			return nil, p
		}
		view, err := svc.CreateProfile(ctx, handlers.ProfileCreateCommand{UserID: id, Request: req, AccountAdmin: true})
		if err != nil {
			return nil, profileProblem(err)
		}
		profile, p := profileOf(view)
		if p != nil {
			return nil, p
		}
		return &ProfileCreatedOutput{Location: Prefix + "/admin/users/" + string(in.ID) + "/profiles/" + string(profile.ID), Body: profile}, nil
	})

	update := adminAccountOperation(http.MethodPatch, adminProfilePath, "updateAdminUserProfile", false)
	update.Summary = "Update one profile of an account; omitted members are unchanged."
	update.Description = "Takes the updateProfile body, every member included. \"pin\": null removes the profile's PIN and a new value replaces it; either ends the profile tokens issued for the old PIN and clears its wrong-attempt lockout. A name another profile on the account uses is 409 conflict. 404 not_found when the profile does not belong to the account. Only the server Owner may change the Owner's or another admin account's profiles (403 permission_denied otherwise): a primary profile's PIN guards that account's admin access."
	update.Errors = append(update.Errors, http.StatusConflict)
	Register(reg, update, func(ctx context.Context, in *AdminProfileUpdateInput) (*ProfileOutput, error) {
		svc, id, p := reg.adminProfilesTarget(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		req, p := reg.profileUpdateRequest(ctx, in.RawBody, in.Body)
		if p != nil {
			return nil, p
		}
		view, err := svc.UpdateProfile(ctx, handlers.ProfileUpdateCommand{UserID: id, ProfileID: in.ProfileID, Request: req, AccountAdmin: true})
		if err != nil {
			return nil, profileProblem(err)
		}
		return profileOutputOf(view)
	})

	del := adminAccountOperation(http.MethodDelete, adminProfilePath, "deleteAdminUserProfile", false)
	del.Summary = "Delete one profile of an account."
	del.Description = "Removes the profile and its per-profile data, as deleteProfile does. The primary profile cannot be deleted (409 conflict); delete the account instead. 404 not_found when the profile does not belong to the account. Only the server Owner may change the Owner's or another admin account's profiles (403 permission_denied otherwise): a primary profile's PIN guards that account's admin access."
	del.DefaultStatus = http.StatusNoContent
	del.Errors = append(del.Errors, http.StatusConflict)
	Register(reg, del, func(ctx context.Context, in *AdminProfileInput) (*struct{}, error) {
		svc, id, p := reg.adminProfilesTarget(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		if err := svc.DeleteProfile(ctx, handlers.ProfileDeleteCommand{UserID: id, ProfileID: in.ProfileID, AccountAdmin: true}); err != nil {
			return nil, profileProblem(err)
		}
		return nil, nil
	})
}

// adminProfilesTarget resolves the addressed account, 404 when it does not
// exist; the profile service then finds the profile only in that account's
// store.
func (reg *Registry) adminProfilesTarget(ctx context.Context, rawID ID) (ProfileService, int, *Problem) {
	accounts, p := reg.adminAccounts()
	if p != nil {
		return nil, 0, p
	}
	if reg.deps.Profiles == nil {
		return nil, 0, unavailable("profile")
	}
	id, p := adminAccountID(rawID)
	if p != nil {
		return nil, 0, p
	}
	if _, err := accounts.GetAdminAccount(ctx, id); err != nil {
		return nil, 0, adminAccountError(err)
	}
	return reg.deps.Profiles, id, nil
}

func profileOutputOf(view handlers.ProfileView) (*ProfileOutput, error) {
	profile, p := profileOf(view)
	if p != nil {
		return nil, p
	}
	return &ProfileOutput{Body: profile}, nil
}

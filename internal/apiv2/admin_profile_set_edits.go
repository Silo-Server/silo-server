package apiv2

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// An administrator's view of the edits made on this server to an account's
// profiles from a profile set, and the owner's way to end the ones the set
// leaves alone: a field the owner set (an override) and a profile the owner
// removed (hidden). Who may call them is as for the admin profile routes.

// ProfileSetEditsService lists and clears the edits made on this server to
// an account's profiles from a set. *handlers.ProfileHandler implements it.
type ProfileSetEditsService interface {
	ListProfileSetEdits(ctx context.Context, userID int) ([]handlers.ProfileSetEditView, error)
	ClearProfileSetOverride(ctx context.Context, cmd handlers.ProfileSetClearCommand) (handlers.ProfileSetClearResult, error)
	ClearProfileSetHidden(ctx context.Context, cmd handlers.ProfileSetClearCommand) (handlers.ProfileSetClearResult, error)
	DismissProfileSetEdit(ctx context.Context, cmd handlers.ProfileSetClearCommand) error
}

// AdminProfileSetEditActor is the account that made an edit.
type AdminProfileSetEditActor struct {
	ID       ID     `json:"id" doc:"The account: the administrator's for an owner edit, the person's own otherwise" example:"1"`
	Username string `json:"username,omitempty" doc:"Its username; absent once that account no longer exists" example:"admin"`
}

// AdminProfileSetEdit is one edit record of an account's profiles from a
// set.
type AdminProfileSetEdit struct {
	Kind          string                    `json:"kind" enum:"override,hidden,unmerged,settled" doc:"override: a field the owner set here, which the person's set leaves as it is until cleared. hidden: a profile of the set the owner removed here, which the set does not bring back until cleared. unmerged: the person's own edit here (a field, or with no field a profile they removed) that their app has not merged yet. settled: the person's edit of a field the owner had set, kept after their app merged it as the record that the owner's value no longer applies. New kinds may be added; ignore one you don't know" example:"override"`
	SyncID        string                    `json:"sync_id" format:"uuid" example:"0f8fad5b-d9cb-469f-a165-70867728950e"`
	ProfileID     ID                        `json:"profile_id,omitempty" doc:"The profile on this server; absent for a removed profile" example:"p-kid"`
	ProfileName   string                    `json:"profile_name" doc:"The profile's name; a removed profile's name when it was removed" example:"Kid"`
	Field         string                    `json:"field,omitempty" doc:"An item field name or settings.<key>, as getProfileSet names it; absent for a removed profile. An open string" example:"max_content_rating"`
	By            string                    `json:"by" enum:"account,owner" example:"owner"`
	Actor         *AdminProfileSetEditActor `json:"actor,omitempty" doc:"Who made the last edit; absent when that is not recorded"`
	At            Instant                   `json:"at" doc:"When the last edit was made" example:"2026-11-25T03:50:00.000Z"`
	Value         ProfileSetValue           `json:"value,omitempty" doc:"The profile's value in force, as a set item encodes it (pin is \"set\" or \"clear\"). For a setting, what the profile resolves to, inherited included. Absent for a removed profile and for a picture whose upload this server kept no hash for"`
	Inherited     bool                      `json:"inherited" doc:"For a setting: the profile has no value of its own, and value is what it inherits" example:"false"`
	SetValue      ProfileSetValue           `json:"set_value,omitempty" doc:"The field's value in the person's last accepted set, as the set encodes it (pin is set, keep or clear). Absent when the server holds none, or when set_resets is true" example:"\"\""`
	SetResets     bool                      `json:"set_resets" doc:"For a setting: the person's set resets it, so the profile inherits" example:"false"`
	ClearApplies  bool                      `json:"clear_applies" doc:"For an override: clearing it puts the set's value back at once. False for a PIN the set sent as digits or kept (this server never stores digits; the owner's PIN stays until the person's app sends a newer set), for a picture this server doesn't hold, and for a field the set never carried" example:"true"`
	ETag          string                    `json:"etag,omitempty" doc:"Current validator for this override; send it as If-Match when clearing it (clearAdminUserProfileOverride). It changes whenever what the clear would do changes. Present on override records only" example:"\"pso1-mFzDq0dX7eTsc3m6SfN0Lw\""`
	ReplacedOwner bool                      `json:"replaced_owner" doc:"For an unmerged edit: it replaced a value the owner had set" example:"false"`
}

// AdminProfileSetEdits is the listAdminUserProfileSetEdits answer.
type AdminProfileSetEdits struct {
	Edits []AdminProfileSetEdit `json:"edits" doc:"Every record, ordered by sync_id then field. Empty for an account with none, and where profiles are not kept in PostgreSQL"`
}

// AdminProfileSetEditsOutput is the listAdminUserProfileSetEdits response.
type AdminProfileSetEditsOutput struct {
	Body AdminProfileSetEdits
}

// AdminProfileSetCleared is what clearing an owner's record did.
type AdminProfileSetCleared struct {
	Applied    bool `json:"applied" doc:"Whether the set's value was written now. Always false for a hidden profile: the clear creates nothing" example:"true"`
	WakeQueued bool `json:"wake_queued" doc:"Whether this call queued a push telling the person's apps, after the clear committed. False with push off, with no phone registered, within a minute after an earlier wake of the account, while a wake for the account is in flight on this server, and when queueing the wake failed or timed out; the change then reaches the app the next time it syncs" example:"false"`
}

// AdminProfileSetClearedOutput is the response of the two clears.
type AdminProfileSetClearedOutput struct {
	Body AdminProfileSetCleared
}

// AdminProfileSetEditsInput addresses an account's edit records.
type AdminProfileSetEditsInput struct {
	ID ID `path:"id" doc:"The account" example:"7"`
}

// AdminProfileSetFieldInput addresses one field record of a profile.
type AdminProfileSetFieldInput struct {
	ID        ID     `path:"id" doc:"The account" example:"7"`
	ProfileID string `path:"profile_id" minLength:"1" maxLength:"128" doc:"A profile of the account" example:"p-kid"`
	Field     string `path:"field" maxLength:"200" pattern:"^(name|picture|pin|is_child|max_content_rating|max_advisory_age|require_advisory_age|requests_blocked|settings\\.[a-z0-9_.]+)$" doc:"An item field name or settings.<key>" example:"max_content_rating"`
}

// AdminProfileOverrideState is the state clearAdminUserProfileOverride puts
// an override in. It is a state, not a command, with one value: the route
// ends an override and nothing else.
type AdminProfileOverrideState struct {
	State string `json:"state" enum:"cleared" doc:"cleared: the owner's value no longer applies, and the person's set applies to the field again" example:"cleared"`
}

// AdminProfileOverrideClearInput is the clearAdminUserProfileOverride
// request.
type AdminProfileOverrideClearInput struct {
	ID          ID     `path:"id" doc:"The account" example:"7"`
	ProfileID   string `path:"profile_id" minLength:"1" maxLength:"128" doc:"A profile of the account" example:"p-kid"`
	Field       string `path:"field" maxLength:"200" pattern:"^(name|picture|pin|is_child|max_content_rating|max_advisory_age|require_advisory_age|requests_blocked|settings\\.[a-z0-9_.]+)$" doc:"An item field name or settings.<key>" example:"max_content_rating"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	RawBody     []byte
	Body        AdminProfileOverrideState
}

// overrideETag is the validator of an override: its token, opaque, as a
// strong EntityTag, so quoting and comparison are the shared type's.
// RenderETag does not fit: it takes a monotonic row version, and an override
// has none; its token is a digest of what the clear would do and of the
// edit row's revision, already bound to the account, profile and field.
func overrideETag(token string) EntityTag {
	return EntityTag{Opaque: "pso1-" + token}
}

// AdminProfileSetHiddenInput addresses a profile the owner removed.
type AdminProfileSetHiddenInput struct {
	ID     ID     `path:"id" doc:"The account" example:"7"`
	SyncID string `path:"sync_id" format:"uuid" doc:"The removed profile's sync_id" example:"6c9a2f2e-4f3b-4c55-9d43-1f0e2b7a8c11"`
}

const adminProfileSetEditsRule = " Only the server Owner may change the Owner's or another admin account's profiles (403 permission_denied otherwise). 501 capability_unsupported where profiles are not kept in PostgreSQL."

func registerAdminProfileSetEdits(reg *Registry) {
	list := adminAccountOperation(http.MethodGet, "/{id}/profile-set/edits", "listAdminUserProfileSetEdits", false)
	list.Summary = "List the edits made on this server to an account's profiles from its app."
	list.Description = "The owner's overrides and hidden profiles, the person's own edits their app has not merged, and settled records of the person's edits over the owner's, each with who made the last edit and when. An override is not a lock: the person may change the field here too, and their edit then replaces the owner's (unmerged, then settled). 404 not_found for an unknown account."
	Register(reg, list, func(ctx context.Context, in *AdminProfileSetEditsInput) (*AdminProfileSetEditsOutput, error) {
		svc, id, p := reg.adminProfileSetEditsTarget(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		views, err := svc.ListProfileSetEdits(ctx, id)
		if err != nil {
			return nil, adminProfileSetEditsProblem(err)
		}
		out := &AdminProfileSetEditsOutput{Body: AdminProfileSetEdits{Edits: make([]AdminProfileSetEdit, 0, len(views))}}
		for _, v := range views {
			out.Body.Edits = append(out.Body.Edits, adminProfileSetEditOf(v))
		}
		return out, nil
	})

	override := adminAccountOperation(http.MethodPatch, "/{id}/profiles/{profile_id}/overrides/{field}", "clearAdminUserProfileOverride", true)
	override.GuardedReceipt = true
	override.Summary = "Clear the owner's override of one field of a profile from the account's app."
	override.Description = "Takes {\"state\": \"cleared\"}, the only state an override can be put in: this operation ends an override and does nothing else, and a different change to the override would be a new state added on purpose. " +
		"Requires If-Match with the override's etag from listAdminUserProfileSetEdits, so the clear does what the owner was shown: a missing If-Match is 428 precondition_required, and an etag that no longer matches (the person's app sent a newer set that changes what the clear would write or whether it can, or the override was edited since) is 412 precondition_failed with the current ETag. Either way nothing is written and the override stays. The precondition exists so that a clear does what the confirmation the owner read said it would. If-Match: * is the convention's deliberate overwrite: a caller that sends it has been shown no confirmation and says outright that it takes whatever the person's set holds at that moment. First-party clients never send it; the web card always sends the etag of the record it showed. " +
		"The etag is compared inside the clear's transaction, on the profile rows it has locked, so a set can't land between the check and the write. Refusals come in this order: 404 not_found when the profile has no override of the field (including one the person has since replaced), then 428 and 412, then 409 for a name. " +
		"Puts the value of the person's last accepted set back at once, in one transaction, and the set applies to the field again from then on; nothing it writes counts as an edit here. A PIN the set sent as digits, or kept, can't be put back (this server never stores digits): the PIN stays as the owner set it until the person's app sends a newer set. When the set has no PIN for the profile, clearing removes the owner's PIN at once. A picture this server doesn't hold waits for its upload. For a setting the settings contract mirrors in another key, both keys' overrides are cleared. Putting back a name another profile on the account now has (compared trimmed and without case) is refused with 409 conflict, as the owner's own rename to it is, and nothing is written; the override stays. " +
		"Answers a receipt: applied, whether the set's value was written now, and wake_queued, whether this call queued a push telling the person's apps after the clear committed. The clears of a hidden profile and of a settled record restore no value, so they stay unguarded DELETEs." + adminProfileSetEditsRule
	override.Errors = append(override.Errors, http.StatusConflict, http.StatusNotImplemented)
	Register(reg, override, func(ctx context.Context, in *AdminProfileOverrideClearInput) (*AdminProfileSetClearedOutput, error) {
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		svc, id, p := reg.adminProfileSetEditsTarget(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		res, err := svc.ClearProfileSetOverride(ctx, handlers.ProfileSetClearCommand{
			UserID: id, ProfileID: in.ProfileID, Field: in.Field,
			Precondition: func(token string) error {
				tag := overrideETag(token)
				if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
					if p.Status == http.StatusPreconditionFailed {
						return StaleVersionProblem(tag)
					}
					return p
				}
				return nil
			},
		})
		if err != nil {
			return nil, adminProfileSetEditsProblem(err)
		}
		return &AdminProfileSetClearedOutput{Body: AdminProfileSetCleared{Applied: res.Applied, WakeQueued: res.WakeQueued}}, nil
	})

	hidden := adminAccountOperation(http.MethodDelete, "/{id}/profile-set/hidden/{sync_id}", "clearAdminUserHiddenProfile", false)
	hidden.Summary = "Let a profile the owner removed come back from the account's app."
	hidden.Description = "Clears the owner's removal, and creates nothing: the profile is not recreated here. A newer set from the person's app creates it, as a new profile with no history here, only if that set has an item with this sync_id and the household has room for another profile; otherwise the item is in the set's left_off, or not in the set at all, and nothing comes back. A newer set that has the item can still be refused as a whole (for example, its name is now another profile's on the account), and then nothing is written. If that set pairs the sync_id with a profile of the account outside the set, that profile takes the sync_id and keeps its history instead of a new profile being created. Then the server tries to wake the person's apps: wake_queued says only that this call queued a push, not that the profile returned. 404 not_found when the owner removed no profile with that sync_id. 409 conflict when the household kept changing while the clear ran: nothing was written; read the list again and retry." + adminProfileSetEditsRule
	hidden.Errors = append(hidden.Errors, http.StatusConflict, http.StatusNotImplemented)
	Register(reg, hidden, func(ctx context.Context, in *AdminProfileSetHiddenInput) (*AdminProfileSetClearedOutput, error) {
		svc, id, p := reg.adminProfileSetEditsTarget(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		res, err := svc.ClearProfileSetHidden(ctx, handlers.ProfileSetClearCommand{UserID: id, SyncID: in.SyncID})
		if err != nil {
			return nil, adminProfileSetEditsProblem(err)
		}
		return &AdminProfileSetClearedOutput{Body: AdminProfileSetCleared{WakeQueued: res.WakeQueued}}, nil
	})

	dismiss := adminAccountOperation(http.MethodDelete, "/{id}/profiles/{profile_id}/settled-edits/{field}", "dismissAdminUserSettledProfileEdit", false)
	dismiss.Summary = "Dismiss the settled record of the person's edit over the owner's value."
	dismiss.Description = "Deletes the record only; the profile is unchanged and the person's apps are not told. 404 not_found when the field has no settled record. 409 conflict when the household kept changing while the dismissal ran: nothing was written; read the list again and retry." + adminProfileSetEditsRule
	dismiss.DefaultStatus = http.StatusNoContent
	dismiss.Errors = append(dismiss.Errors, http.StatusConflict, http.StatusNotImplemented)
	Register(reg, dismiss, func(ctx context.Context, in *AdminProfileSetFieldInput) (*struct{}, error) {
		svc, id, p := reg.adminProfileSetEditsTarget(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		if err := svc.DismissProfileSetEdit(ctx, handlers.ProfileSetClearCommand{UserID: id, ProfileID: in.ProfileID, Field: in.Field}); err != nil {
			return nil, adminProfileSetEditsProblem(err)
		}
		return nil, nil
	})
}

// adminProfileSetEditsTarget resolves the addressed account, 404 when it
// does not exist.
func (reg *Registry) adminProfileSetEditsTarget(ctx context.Context, rawID ID) (ProfileSetEditsService, int, *Problem) {
	accounts, p := reg.adminAccounts()
	if p != nil {
		return nil, 0, p
	}
	if reg.deps.ProfileSetEdits == nil {
		return nil, 0, unavailable(profileSetDomain)
	}
	id, p := adminAccountID(rawID)
	if p != nil {
		return nil, 0, p
	}
	if _, err := accounts.GetAdminAccount(ctx, id); err != nil {
		return nil, 0, adminAccountError(err)
	}
	return reg.deps.ProfileSetEdits, id, nil
}

func adminProfileSetEditsProblem(err error) error {
	switch {
	case errors.Is(err, userstore.ErrProfileSetUnsupported):
		return CapabilityProblem(StateUnsupported, profileSetDomain)
	case errors.Is(err, userstore.ErrProfileSetAccountGone):
		return NewProblem(TypeNotFound, "The account no longer exists.")
	}
	return profileProblem(err)
}

func adminProfileSetEditOf(v handlers.ProfileSetEditView) AdminProfileSetEdit {
	out := AdminProfileSetEdit{
		Kind: v.Kind, SyncID: v.SyncID, ProfileID: ID(v.ProfileID), ProfileName: v.ProfileName, Field: v.Field, By: v.By,
		At: NewInstant(v.At), Value: ProfileSetValue(bytes.Clone(v.Value)), Inherited: v.Inherited,
		SetValue: ProfileSetValue(bytes.Clone(v.SetValue)), SetResets: v.SetResets, ClearApplies: v.ClearApplies,
		ReplacedOwner: v.ReplacedOwner,
	}
	if v.Token != "" {
		out.ETag = overrideETag(v.Token).String()
	}
	if v.Actor.UserID > 0 {
		out.Actor = &AdminProfileSetEditActor{ID: ID(strconv.Itoa(v.Actor.UserID)), Username: v.Actor.Username}
	}
	return out
}

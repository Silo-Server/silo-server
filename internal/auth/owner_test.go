package auth

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestOwnerChecks(t *testing.T) {
	owner := &models.User{ID: 1, Role: models.RoleAdmin, Enabled: true, IsOwner: true}
	admin := &models.User{ID: 2, Role: models.RoleAdmin, Enabled: true, LocalPasswordLoginEnabled: true}
	// providerAdmin and providerOwner sign in only through a provider.
	providerAdmin := &models.User{ID: 5, Role: models.RoleAdmin, Enabled: true}
	providerOwner := &models.User{ID: 1, Role: models.RoleAdmin, Enabled: true, IsOwner: true}
	user := &models.User{ID: 4, Role: models.RoleUser, Enabled: true}
	asOwner := OwnerActor{ID: owner.ID, IsOwner: true}
	asAdmin := OwnerActor{ID: 3}
	demote := models.UpdateUserInput{Role: new(models.RoleUser)}
	promote := models.UpdateUserInput{Role: new(models.RoleAdmin)}
	disable := models.UpdateUserInput{Enabled: new(false)}
	rename := models.UpdateUserInput{Username: new("renamed")}
	// A non-Owner admin the Owner restricted.
	limited := &models.User{ID: 5, Role: models.RoleAdmin, Enabled: true, MaxStreams: new(2), LibraryIDs: []int{2, 1}, DownloadTranscodeAllowed: new(false), MaxProfiles: new(2)}
	resend := models.UpdateUserInput{
		MaxStreams:               models.SetValue(2),
		LibraryIDs:               models.SetValue([]int{1, 2}),
		DownloadTranscodeAllowed: models.SetValue(false),
		MaxTranscodes:            models.ClearValue[int](),
		RequestsAllowed:          models.ClearValue[bool](),
		MaxProfiles:              models.SetValue(2),
	}

	cases := []struct {
		name string
		err  error
		want error
	}{
		{"admin edits owner", CheckOwnerUpdate(asAdmin, owner, rename), ErrOwnerProtected},
		{"admin deletes owner", CheckOwnerDelete(asAdmin, owner), ErrOwnerProtected},
		{"admin targets owner", CheckOwnerTarget(asAdmin, owner), ErrOwnerProtected},
		{"no actor targets owner", CheckOwnerTarget(OwnerActor{}, owner), ErrOwnerProtected},
		{"owner edits self", CheckOwnerUpdate(asOwner, owner, rename), nil},
		{"owner demotes self", CheckOwnerUpdate(asOwner, owner, demote), ErrOwnerStanding},
		{"owner disables self", CheckOwnerUpdate(asOwner, owner, disable), ErrOwnerStanding},
		{"owner keeps admin role", CheckOwnerUpdate(asOwner, owner, models.UpdateUserInput{Role: new(models.RoleAdmin), Enabled: new(true)}), nil},
		{"owner deletes self", CheckOwnerDelete(asOwner, owner), ErrOwnerStanding},
		{"owner edits admin", CheckOwnerUpdate(asOwner, admin, demote), nil},
		{"owner deletes admin", CheckOwnerDelete(asOwner, admin), nil},
		{"owner promotes user", CheckOwnerUpdate(asOwner, user, promote), nil},
		{"owner grants admin", CheckGrantAdmin(asOwner, models.RoleAdmin), nil},
		{"admin edits admin", CheckOwnerUpdate(asAdmin, admin, rename), ErrAdminProtected},
		{"admin disables admin", CheckOwnerUpdate(asAdmin, admin, disable), ErrAdminProtected},
		{"admin deletes admin", CheckOwnerDelete(asAdmin, admin), ErrAdminProtected},
		{"admin targets admin", CheckOwnerTarget(asAdmin, admin), ErrAdminProtected},
		{"admin edits self", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, rename), nil},
		{"admin demotes self", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, demote), ErrSelfStanding},
		{"admin disables self", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, disable), ErrSelfStanding},
		{"admin deletes self", CheckOwnerDelete(OwnerActor{ID: admin.ID}, admin), ErrSelfStanding},
		{"admin resends own role", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, promote), nil},
		{"admin promotes user", CheckOwnerUpdate(asAdmin, user, promote), ErrAdminProtected},
		{"admin edits user", CheckOwnerUpdate(asAdmin, user, disable), nil},
		{"admin makes itself break-glass", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, models.UpdateUserInput{BreakGlass: new(true)}), ErrBreakGlassOwnerOnly},
		{"admin resends its break-glass flag", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, models.UpdateUserInput{BreakGlass: new(false)}), nil},
		{"owner makes an admin break-glass", CheckOwnerUpdate(asOwner, admin, models.UpdateUserInput{BreakGlass: new(true)}), nil},
		{"owner makes itself break-glass", CheckOwnerUpdate(asOwner, owner, models.UpdateUserInput{BreakGlass: new(true)}), nil},
		{"provider-only admin sets own password", CheckOwnerUpdate(OwnerActor{ID: providerAdmin.ID}, providerAdmin, models.UpdateUserInput{Password: new("long-enough")}), ErrSelfPasswordOwnerOnly},
		{"provider-only admin edits self", CheckOwnerUpdate(OwnerActor{ID: providerAdmin.ID}, providerAdmin, rename), nil},
		{"local admin sets own password", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, models.UpdateUserInput{Password: new("long-enough")}), nil},
		{"provider-only owner sets own password", CheckOwnerUpdate(asOwner, providerOwner, models.UpdateUserInput{Password: new("long-enough")}), nil},
		{"owner sets a provider-only admin's password", CheckOwnerUpdate(asOwner, providerAdmin, models.UpdateUserInput{Password: new("long-enough")}), nil},
		{"admin deletes user", CheckOwnerDelete(asAdmin, user), nil},
		{"admin grants admin", CheckGrantAdmin(asAdmin, models.RoleAdmin), ErrAdminProtected},
		{"admin grants user", CheckGrantAdmin(asAdmin, models.RoleUser), nil},
		{"admin lifts own stream limit", CheckOwnerUpdate(OwnerActor{ID: limited.ID}, limited, models.UpdateUserInput{MaxStreams: models.ClearValue[int]()}), ErrAdminPolicyProtected},
		{"admin changes own stream limit", CheckOwnerUpdate(OwnerActor{ID: limited.ID}, limited, models.UpdateUserInput{MaxStreams: models.SetValue(4)}), ErrAdminPolicyProtected},
		{"admin sets own override", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, models.UpdateUserInput{DownloadTranscodeAllowed: models.SetValue(false)}), ErrAdminPolicyProtected},
		{"admin widens own libraries", CheckOwnerUpdate(OwnerActor{ID: limited.ID}, limited, models.UpdateUserInput{LibraryIDs: models.SetValue([]int{1, 2, 3})}), ErrAdminPolicyProtected},
		{"admin clears own libraries", CheckOwnerUpdate(OwnerActor{ID: limited.ID}, limited, models.UpdateUserInput{LibraryIDs: models.ClearValue[[]int]()}), ErrAdminPolicyProtected},
		{"admin raises own profile limit", CheckOwnerUpdate(OwnerActor{ID: limited.ID}, limited, models.UpdateUserInput{MaxProfiles: models.SetValue(10)}), ErrAdminPolicyProtected},
		{"admin clears own profile limit", CheckOwnerUpdate(OwnerActor{ID: limited.ID}, limited, models.UpdateUserInput{MaxProfiles: models.ClearValue[int]()}), ErrAdminPolicyProtected},
		{"admin resends own policy", CheckOwnerUpdate(OwnerActor{ID: limited.ID}, limited, resend), nil},
		{"admin edits own email beside policy", CheckOwnerUpdate(OwnerActor{ID: limited.ID}, limited, models.UpdateUserInput{Email: new("new@example.test"), MaxStreams: models.SetValue(2)}), nil},
		{"owner limits admin", CheckOwnerUpdate(asOwner, admin, models.UpdateUserInput{MaxStreams: models.SetValue(2)}), nil},
		{"owner lifts admin limit", CheckOwnerUpdate(asOwner, limited, models.UpdateUserInput{MaxStreams: models.ClearValue[int]()}), nil},
		{"owner changes own policy", CheckOwnerUpdate(asOwner, owner, models.UpdateUserInput{MaxStreams: models.SetValue(2)}), nil},
		{"admin limits user", CheckOwnerUpdate(asAdmin, user, models.UpdateUserInput{MaxStreams: models.SetValue(2)}), nil},
	}
	for _, tc := range cases {
		if !errors.Is(tc.err, tc.want) || (tc.want == nil && tc.err != nil) {
			t.Errorf("%s: err = %v, want %v", tc.name, tc.err, tc.want)
		}
	}
}

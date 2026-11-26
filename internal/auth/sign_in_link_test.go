package auth

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Who may make a sign-in link is who may set that account's password as an
// administrator: each row's answer is the Owner rules' answer to a password
// write (CheckOwnerUpdate, as updateAdminUser judges one), and a scoped API
// key never reaches an administrator. Regression guard: a link is at least
// as strong as a new password, so a looser rule would let an administrator
// into an account they may not set the password of.
func TestCheckSignInLinkIssuer(t *testing.T) {
	user := &models.User{ID: 10, Role: models.RoleUser, LocalPasswordLoginEnabled: true}
	admin := &models.User{ID: 11, Role: models.RoleAdmin, LocalPasswordLoginEnabled: true}
	owner := &models.User{ID: 12, Role: models.RoleAdmin, IsOwner: true, LocalPasswordLoginEnabled: true}
	selfOff := &models.User{ID: 13, Role: models.RoleAdmin}
	ownerOff := &models.User{ID: 12, Role: models.RoleAdmin, IsOwner: true}
	asAdmin := OwnerActor{ID: 13}
	asOwner := OwnerActor{ID: 12, IsOwner: true}
	for _, tc := range []struct {
		name   string
		actor  OwnerActor
		target *models.User
		scoped bool
		want   error
	}{
		{"an administrator for an ordinary account", asAdmin, user, false, nil},
		{"an administrator for another administrator", asAdmin, admin, false, ErrAdminProtected},
		{"an administrator for the Owner", asAdmin, owner, false, ErrOwnerProtected},
		{"the Owner for another administrator", asOwner, admin, false, nil},
		{"an administrator for their own account with password sign-in off", asAdmin, selfOff, false, ErrSelfPasswordOwnerOnly},
		{"the Owner for their own account with password sign-in off", asOwner, ownerOff, false, nil},
		{"a scoped API key for an ordinary account", asAdmin, user, true, nil},
		{"a scoped API key for an administrator", asOwner, admin, true, ErrScopedKeyAdminCredentials},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckSignInLinkIssuer(tc.actor, tc.target, tc.scoped)
			if !errors.Is(got, tc.want) || (tc.want == nil && got != nil) {
				t.Fatalf("CheckSignInLinkIssuer = %v, want %v", got, tc.want)
			}
			if tc.scoped {
				return
			}
			password := "a new password"
			admin := CheckOwnerUpdate(tc.actor, tc.target, models.UpdateUserInput{Password: &password, LocalPasswordLoginEnabled: new(true)})
			if !errors.Is(admin, tc.want) || (tc.want == nil && admin != nil) {
				t.Fatalf("an administrator's password write = %v, the link's rule %v: they must agree", admin, got)
			}
		})
	}
}

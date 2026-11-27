package apiv2

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// AdminSignInRiskService lists the accounts a sign-in switch change would
// leave with no way in (auth.AccountsAtRisk).
type AdminSignInRiskService interface {
	AccountsAtRisk(ctx context.Context, change auth.SignInChange) ([]auth.AccountAtRisk, error)
}

// SignInRiskInput names the change.
type SignInRiskInput struct {
	Change string `query:"change" required:"true" enum:"app_keys_off,local_passwords_off" doc:"The switch going off: app_keys_off for auth.app_key_sign_in, local_passwords_off for auth.local_password_login"`
}

// AccountAtRisk is one account the change leaves with no way in.
type AccountAtRisk struct {
	ID                  ID       `json:"id" doc:"The account's ID"`
	Username            string   `json:"username" doc:"The account's username, or its email when it has none"`
	Role                string   `json:"role" enum:"admin,user"`
	IsOwner             bool     `json:"is_owner"`
	BreakGlass          bool     `json:"break_glass"`
	HasPassword         bool     `json:"has_password" doc:"Whether the account has a usable local password, whatever the server's local password setting"`
	SignInLinkExpiresAt *Instant `json:"sign_in_link_expires_at" nullable:"true" doc:"When the account's live, unused sign-in link expires; null for none. The list does not fill it, so it is always null"`
}

// SignInRiskOutput is the listSignInAccountsAtRisk response.
type SignInRiskOutput struct {
	Body struct {
		Items []AccountAtRisk `json:"items" doc:"Every account the change leaves at risk, in username order. Not paged: it holds only accounts whose way in would be their keys alone, or would be gone"`
	}
}

func registerAdminSignInRisk(reg *Registry) {
	op := humaOp(http.MethodGet, Prefix+"/admin/sign-in/accounts-at-risk", "listSignInAccountsAtRisk", "admin-settings",
		"List the accounts a sign-in switch change would leave with no way in.")
	op.Description = "app_keys_off lists the enabled accounts whose Silo app keys sign in now and that have no password that signs in and no sign-in provider account: with app keys off they have no way in. updateAdminSettings refuses turning app keys off while it lists any account. The list is the same while app keys are already off, where it names the accounts that still have no way in. " +
		"local_passwords_off lists the enabled accounts the change would leave with Silo app keys as their only way in (a key bound to a sign-in provider account, or a break-glass account's, keeps working), or with none (no password that signs in, and keys that need local passwords): the same accounts, from the same query, as the blocking group of listAdminLocalPasswordOffAccounts, which also lists the accounts that only lose password sign-in. Linking a provider account takes one off the list; a password takes off only a break-glass administrator, the one kind a password signs in once local passwords are off. updateAdminSettings refuses turning local passwords off while it lists any account."
	Register(reg, Operation{Operation: op, Class: ClassActingAdmin, ServiceBacked: true}, func(ctx context.Context, in *SignInRiskInput) (*SignInRiskOutput, error) {
		if reg.deps.AdminSignInRisk == nil {
			return nil, unavailable("sign-in account checks")
		}
		accounts, err := reg.deps.AdminSignInRisk.AccountsAtRisk(ctx, auth.SignInChange(in.Change))
		if err != nil {
			return nil, adminSettingsInspectionProblem(err)
		}
		out := &SignInRiskOutput{}
		out.Body.Items = make([]AccountAtRisk, 0, len(accounts))
		for _, a := range accounts {
			item := AccountAtRisk{ID: ID(strconv.Itoa(a.ID)), Username: a.Username, Role: a.Role, IsOwner: a.IsOwner,
				BreakGlass: a.BreakGlass, HasPassword: a.HasPassword}
			if a.SignInLinkExpiresAt != nil {
				item.SignInLinkExpiresAt = new(NewInstant(*a.SignInLinkExpiresAt))
			}
			out.Body.Items = append(out.Body.Items, item)
		}
		return out, nil
	})
}

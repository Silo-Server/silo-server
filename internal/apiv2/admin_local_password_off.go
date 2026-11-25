package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// AdminLocalPasswordOffInput asks for one page of one group of the accounts
// that lose their way in when local password sign-in is turned off.
type AdminLocalPasswordOffInput struct {
	Group          string `query:"group" required:"true" enum:"email_match,admin_link" doc:"email_match: accounts email auto-match can link at their first provider sign-in. admin_link: accounts nothing links until an administrator does"`
	EmailAutoMatch string `query:"email_auto_match" enum:"true,false" doc:"Answer as if auth.email_auto_match had this value, for an edit that changes both settings; omitted uses the saved value"`
	LimitParam
	Cursor string `query:"cursor" maxLength:"8192"`
}

// AdminLocalPasswordOffAccount is one account that loses its way in.
type AdminLocalPasswordOffAccount struct {
	ID       ID     `json:"id" example:"12"`
	Username string `json:"username" example:"alice"`
	Email    string `json:"email" doc:"The account's email; empty when it has none" example:"alice@example.test"`
	Role     string `json:"role" enum:"admin,user" example:"user"`
	IsOwner  bool   `json:"is_owner" doc:"Whether the account is the server Owner" example:"false"`
	Reason   string `json:"reason" enum:"email_match,email_match_off,administrator,no_email" doc:"What decides the account's outcome. email_match: an ordinary account with an email, and email auto-match is on, so the provider's first sign-in for that verified email links it. email_match_off: the same, with email auto-match off. administrator: an admin or the Owner, which email auto-match never links. no_email: an ordinary account without an email" example:"email_match"`
}

// AdminLocalPasswordOffTotals counts both groups.
type AdminLocalPasswordOffTotals struct {
	EmailMatch int `json:"email_match" doc:"Accounts in the email_match group" example:"3"`
	AdminLink  int `json:"admin_link" doc:"Accounts in the admin_link group" example:"1"`
}

// AdminLocalPasswordOffAccounts is the listAdminLocalPasswordOffAccounts
// response body.
type AdminLocalPasswordOffAccounts struct {
	Collection[AdminLocalPasswordOffAccount]
	EmailAutoMatch bool                        `json:"email_auto_match" doc:"The auth.email_auto_match value the grouping used" example:"true"`
	Totals         AdminLocalPasswordOffTotals `json:"totals"`
}

// AdminLocalPasswordOffAccountsOutput is the listAdminLocalPasswordOffAccounts
// response.
type AdminLocalPasswordOffAccountsOutput struct {
	Body AdminLocalPasswordOffAccounts
}

// localPasswordOffPosition is the last row of a page.
type localPasswordOffPosition struct {
	Username string `json:"u"`
	ID       int    `json:"i"`
}

func registerLocalPasswordOffAccounts(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/sign-in/local-password-off/accounts", "listAdminLocalPasswordOffAccounts", "admin-users",
		"List the accounts that lose their way in when local password sign-in is turned off."), Class: ClassActingAdmin, ServiceBacked: true}
	op.Description = "Answers for turning auth.local_password_login off: enabled accounts whose local password signs in today, that are not break-glass admins and have no linked identity at an enabled provider. Break-glass admins keep their password and are not listed; neither are disabled accounts. Each account is in one group: email_match when email auto-match can link it at its first provider sign-in (the provider must assert that email as verified), otherwise admin_link. totals counts both groups whichever is asked for. Ordered by username; pages are live, not a snapshot."
	Register(reg, op, func(ctx context.Context, in *AdminLocalPasswordOffInput) (*AdminLocalPasswordOffAccountsOutput, error) {
		svc, p := reg.externalSignIn()
		if p != nil {
			return nil, p
		}
		query := auth.LocalPasswordOffQuery{Group: in.Group, Limit: in.Limit}
		if in.EmailAutoMatch != "" {
			query.EmailAutoMatch = new(in.EmailAutoMatch == strconv.FormatBool(true))
		}
		scope := CursorScope{OperationID: "listAdminLocalPasswordOffAccounts",
			Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx) + "/" + viewerScopeDigest(ctx),
			Filter:   in.Group + "/" + in.EmailAutoMatch, Sort: "username_asc", Tiebreaker: "id_asc"}
		if in.Cursor != "" {
			var position localPasswordOffPosition
			if p := cursors.Decode(scope, in.Cursor, &position); p != nil {
				return nil, p
			}
			query.AfterUsername, query.AfterID = position.Username, position.ID
		}
		page, err := svc.ListLocalPasswordOffAccounts(ctx, query)
		if err != nil {
			return nil, serviceProblem(err)
		}
		if len(page.Accounts) > in.Limit {
			return nil, serviceProblem(errors.New("local password off page exceeds limit"))
		}
		items := make([]AdminLocalPasswordOffAccount, 0, len(page.Accounts))
		for _, account := range page.Accounts {
			items = append(items, AdminLocalPasswordOffAccount{
				ID: IDFromInt(int64(account.ID)), Username: account.Username, Email: account.Email,
				Role: account.Role, IsOwner: account.IsOwner, Reason: account.Reason,
			})
		}
		next := ""
		if page.More && len(page.Accounts) > 0 {
			last := page.Accounts[len(page.Accounts)-1]
			next, err = cursors.Encode(scope, localPasswordOffPosition{Username: last.Username, ID: last.ID})
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		return &AdminLocalPasswordOffAccountsOutput{Body: AdminLocalPasswordOffAccounts{
			Collection:     Paginated(items, next),
			EmailAutoMatch: page.EmailAutoMatch,
			Totals:         AdminLocalPasswordOffTotals{EmailMatch: page.EmailMatchTotal, AdminLink: page.AdminLinkTotal},
		}}, nil
	})
}

package apiv2

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/activitylog"
)

type AdminAccountActivityService interface {
	UserIPsPage(context.Context, int, int, int, activitylog.IPPagePosition) ([]activitylog.UserIPEntry, bool, error)
	IPUsersPage(context.Context, string, int, int, activitylog.IPPagePosition) ([]activitylog.IPUserEntry, bool, error)
}
type AdminUserIPsInput struct {
	ID   ID  `path:"id"`
	Days int `query:"days" default:"30" minimum:"1" maximum:"365"`
	LimitParam
	Cursor string `query:"cursor"`
}
type AdminIPUsersInput struct {
	IP   string `query:"ip" required:"true"`
	Days int    `query:"days" default:"30" minimum:"1" maximum:"365"`
	LimitParam
	Cursor string `query:"cursor"`
}
type AdminUserIP struct {
	ClientIP     string  `json:"client_ip"`
	FirstSeen    Instant `json:"first_seen"`
	LastSeen     Instant `json:"last_seen"`
	RequestCount int     `json:"request_count"`
}
type AdminIPUser struct {
	UserID       ID      `json:"user_id"`
	Username     string  `json:"username"`
	FirstSeen    Instant `json:"first_seen"`
	LastSeen     Instant `json:"last_seen"`
	RequestCount int     `json:"request_count"`
}
type AdminUserIPsOutput struct{ Body Collection[AdminUserIP] }
type AdminIPUsersOutput struct{ Body Collection[AdminIPUser] }

func registerAdminAccountActivity(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, adminAccountOperation(http.MethodGet, "/{id}/ips", "listAdminUserIPs", false), func(ctx context.Context, in *AdminUserIPsInput) (*AdminUserIPsOutput, error) {
		if reg.deps.AdminAccountActivity == nil {
			return nil, unavailable("account activity")
		}
		svc, p := reg.adminAccounts()
		if p != nil {
			return nil, p
		}
		id, p := adminAccountID(in.ID)
		if p != nil {
			return nil, p
		}
		if _, err := svc.GetAdminAccount(ctx, id); err != nil {
			return nil, adminAccountError(err)
		}
		scope := adminPolicyListScope(ctx, "listAdminUserIPs", strconv.Itoa(id)+"/"+strconv.Itoa(in.Days)+"/"+strconv.Itoa(in.Limit), "-last_seen,-ip", "ip")
		pos := activitylog.IPPagePosition{Until: time.Now().UTC()}
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
				return nil, p
			}
		}
		rows, more, err := reg.deps.AdminAccountActivity.UserIPsPage(ctx, id, in.Days, in.Limit, pos)
		if err != nil {
			return nil, serviceProblem(err)
		}
		items := make([]AdminUserIP, 0, len(rows))
		for _, r := range rows {
			items = append(items, AdminUserIP{ClientIP: r.ClientIP, FirstSeen: NewInstant(r.FirstSeen), LastSeen: NewInstant(r.LastSeen), RequestCount: r.RequestCount})
		}
		next := ""
		if more && len(rows) > 0 {
			last := rows[len(rows)-1]
			pos.LastSeen = last.LastSeen
			pos.IP = last.ClientIP
			next, err = cursors.Encode(scope, pos)
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		return &AdminUserIPsOutput{Body: Paginated(items, next)}, nil
	})
	op := adminAccountOperation(http.MethodGet, "", "listAdminIPUsers", false)
	op.Path = Prefix + "/admin/ips"
	Register(reg, op, func(ctx context.Context, in *AdminIPUsersInput) (*AdminIPUsersOutput, error) {
		if reg.deps.AdminAccountActivity == nil {
			return nil, unavailable("account activity")
		}
		address, err := netip.ParseAddr(in.IP)
		if err != nil {
			return nil, NewProblem(TypeValidationFailed, "A valid IP address is required.")
		}
		ip := address.String()
		scope := adminPolicyListScope(ctx, "listAdminIPUsers", ip+"/"+strconv.Itoa(in.Days)+"/"+strconv.Itoa(in.Limit), "-last_seen,-user_id", "user_id")
		pos := activitylog.IPPagePosition{Until: time.Now().UTC()}
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
				return nil, p
			}
		}
		rows, more, err := reg.deps.AdminAccountActivity.IPUsersPage(ctx, ip, in.Days, in.Limit, pos)
		if err != nil {
			return nil, serviceProblem(err)
		}
		items := make([]AdminIPUser, 0, len(rows))
		for _, r := range rows {
			items = append(items, AdminIPUser{UserID: ID(strconv.Itoa(r.UserID)), Username: r.Username, FirstSeen: NewInstant(r.FirstSeen), LastSeen: NewInstant(r.LastSeen), RequestCount: r.RequestCount})
		}
		next := ""
		if more && len(rows) > 0 {
			last := rows[len(rows)-1]
			pos.LastSeen = last.LastSeen
			pos.UserID = last.UserID
			next, err = cursors.Encode(scope, pos)
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		return &AdminIPUsersOutput{Body: Paginated(items, next)}, nil
	})
}

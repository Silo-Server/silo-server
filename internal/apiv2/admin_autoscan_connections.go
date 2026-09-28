package apiv2

import (
	"cmp"
	"context"
	"slices"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminAutoscanConnectionsService interface {
	ReadAdminAutoscanConnections(context.Context) ([]handlers.AdminAutoscanConnectionView, error)
}
type AdminAutoscanConnection struct {
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	Kind                 string  `json:"kind"`
	BaseURL              string  `json:"base_url,omitempty"`
	RequestIntegrationID *string `json:"request_integration_id,omitempty"`
	HasAPIKey            bool    `json:"has_api_key"`
}
type AdminAutoscanConnectionsInput struct {
	LimitParam
	Cursor string `query:"cursor" maxLength:"8192"`
}
type AdminAutoscanConnectionsOutput struct {
	Body Collection[AdminAutoscanConnection]
}
type adminAutoscanConnectionPosition struct{ Name, ID string }

func compareAdminAutoscanConnectionPosition(a, b adminAutoscanConnectionPosition) int {
	return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
}
func adminAutoscanConnectionOf(c handlers.AdminAutoscanConnectionView) AdminAutoscanConnection {
	return AdminAutoscanConnection{ID: c.ID, Name: c.Name, Kind: c.Kind, BaseURL: c.BaseURL, RequestIntegrationID: c.RequestIntegrationID, HasAPIKey: c.HasAPIKey}
}
func registerAdminAutoscanConnections(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/autoscan/connections", "listAdminAutoscanConnections", "admin-autoscan", "Read configured connections without credentials. Each response page enumerates the full configured connection list."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminAutoscanConnectionsInput) (*AdminAutoscanConnectionsOutput, error) {
		if reg.deps.AdminAutoscanConnections == nil {
			return nil, unavailable("autoscan connections")
		}
		scope := CursorScope{OperationID: "listAdminAutoscanConnections", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: strconv.Itoa(in.Limit), Sort: "name", Tiebreaker: "id"}
		var after adminAutoscanConnectionPosition
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
		}
		rows, err := reg.deps.AdminAutoscanConnections.ReadAdminAutoscanConnections(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		rows = slices.Clone(rows)
		slices.SortFunc(rows, func(a, b handlers.AdminAutoscanConnectionView) int {
			return compareAdminAutoscanConnectionPosition(adminAutoscanConnectionPosition{a.Name, a.ID}, adminAutoscanConnectionPosition{b.Name, b.ID})
		})
		items := make([]AdminAutoscanConnection, 0, in.Limit)
		next := ""
		var last adminAutoscanConnectionPosition
		for _, row := range rows {
			pos := adminAutoscanConnectionPosition{row.Name, row.ID}
			if in.Cursor != "" && compareAdminAutoscanConnectionPosition(pos, after) <= 0 {
				continue
			}
			if len(items) == in.Limit {
				next, err = cursors.Encode(scope, last)
				if err != nil {
					return nil, serviceProblem(err)
				}
				break
			}
			items = append(items, adminAutoscanConnectionOf(row))
			last = pos
		}
		return &AdminAutoscanConnectionsOutput{Body: Paginated(items, next)}, nil
	})
}

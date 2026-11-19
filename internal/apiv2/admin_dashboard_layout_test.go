package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeDashboardLayout struct {
	users []int
	view  handlers.AdminDashboardLayoutView
	err   error
}

func (f *fakeDashboardLayout) ReadAdminDashboardLayout(_ context.Context, user int) (handlers.AdminDashboardLayoutView, error) {
	f.users = append(f.users, user)
	return f.view, f.err
}

// adminDashboardLayoutFixtureCases records the read for an account with no
// saved layout, so the null members are validated against the schema.
func adminDashboardLayoutFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "admin_dashboard_layout_unsaved", operationID: "getAdminDashboardLayout", method: "GET", path: Prefix + "/admin/dashboard/layout", headers: bearer(adminToken), status: 200, assertHeaders: []string{"Content-Type", "ETag"}, schema: "#/components/schemas/AdminDashboardLayout", scenario: "An account with no saved layout reads null for both members and still gets a validator for its first save."}}
}

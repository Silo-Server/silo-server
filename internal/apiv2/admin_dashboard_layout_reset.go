package apiv2

import "context"

type AdminDashboardLayoutResetService interface {
	ResetAdminDashboardLayout(context.Context, int) error
}

func registerAdminDashboardLayoutReset(reg *Registry) {
	op := Operation{Operation: humaOp("DELETE", Prefix+"/admin/dashboard/layout", "resetAdminDashboardLayout", "admin-observability", "Delete this administrator account's stored layout. Already absent is success; no revision or durable receipt. Clients must not automatically replay across another layout write."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, op, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		if reg.deps.AdminDashboardLayoutResets == nil {
			return nil, unavailable("dashboard layout")
		}
		if err := reg.deps.AdminDashboardLayoutResets.ResetAdminDashboardLayout(ctx, claimsFrom(ctx).UserID); err != nil {
			return nil, serviceProblem(err)
		}
		return nil, nil
	})
}

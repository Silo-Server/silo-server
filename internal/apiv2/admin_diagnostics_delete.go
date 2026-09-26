package apiv2

import "context"

type AdminDiagnosticDeleteService interface {
	DeleteAdminDiagnosticReport(context.Context, string) error
}

func registerAdminDiagnosticDelete(reg *Registry) {
	op := Operation{Operation: humaOp("DELETE", Prefix+"/admin/diagnostics/reports/{id}", "deleteAdminDiagnosticReport", "admin-observability", "Delete report metadata synchronously, with best-effort stored bundle cleanup. An already absent report is success."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	Register(reg, op, func(ctx context.Context, in *AdminDiagnosticIDInput) (*struct{}, error) {
		if reg.deps.AdminDiagnosticDeletes == nil {
			return nil, unavailable("diagnostic reports")
		}
		if err := reg.deps.AdminDiagnosticDeletes.DeleteAdminDiagnosticReport(ctx, in.ID); err != nil {
			return nil, adminDiagnosticReadProblem(err)
		}
		return nil, nil
	})
}

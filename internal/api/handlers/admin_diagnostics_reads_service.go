package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

func (h *DiagnosticsHandler) ListAdminDiagnosticReports(ctx context.Context, filters diagnostics.ListFilters) (diagnostics.ListResult, error) {
	admin, ok := h.admin()
	if !ok {
		return diagnostics.ListResult{}, diagnostics.ErrReportStoreUnavailable
	}
	return admin.ListForAdmin(ctx, filters)
}
func (h *DiagnosticsHandler) GetAdminDiagnosticReport(ctx context.Context, id string) (*diagnostics.Report, error) {
	admin, ok := h.admin()
	if !ok {
		return nil, diagnostics.ErrReportStoreUnavailable
	}
	return admin.GetReport(ctx, id)
}

package handlers

import (
	"context"
	"errors"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

// DeleteAdminDiagnosticReport makes the report absent. The owning service
// deletes its database row before best-effort object cleanup; this is not a job.
func (h *DiagnosticsHandler) DeleteAdminDiagnosticReport(ctx context.Context, id string) error {
	admin, ok := h.admin()
	if !ok {
		return diagnostics.ErrReportStoreUnavailable
	}
	report, err := admin.DeleteReport(ctx, id)
	if errors.Is(err, diagnostics.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if report != nil {
		h.diagnosticsLogger().InfoContext(ctx, "diagnostic report deleted", "component", "diagnostics", "admin_user_id", apimw.GetUserID(ctx), "report_id", report.ID)
	}
	return nil
}

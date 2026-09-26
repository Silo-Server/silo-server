package handlers

import (
	"context"
	"io"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

type AdminDiagnosticDownload struct {
	Filename string
	Size     *int64
	Body     io.ReadCloser
}

// OpenAdminDiagnosticDownload streams a ready report through the API host. It
// never generates a presigned URL or exposes an object-store location.
func (h *DiagnosticsHandler) OpenAdminDiagnosticDownload(ctx context.Context, id string) (AdminDiagnosticDownload, error) {
	admin, ok := h.admin()
	if !ok {
		return AdminDiagnosticDownload{}, diagnostics.ErrReportStoreUnavailable
	}
	report, err := admin.GetReport(ctx, id)
	if err != nil {
		return AdminDiagnosticDownload{}, err
	}
	if report == nil {
		return AdminDiagnosticDownload{}, diagnostics.ErrNotFound
	}
	if report.State != diagnostics.StateReady {
		return AdminDiagnosticDownload{}, diagnostics.ErrReportNotReady
	}
	body, err := admin.OpenReportDownload(ctx, report)
	if err != nil {
		return AdminDiagnosticDownload{}, err
	}
	h.diagnosticsLogger().InfoContext(ctx, "diagnostic report downloaded", "component", "diagnostics", "admin_user_id", apimw.GetUserID(ctx), "report_id", report.ID)
	return AdminDiagnosticDownload{Filename: diagnosticsReportFilename(report), Size: report.BlobBytes, Body: body}, nil
}

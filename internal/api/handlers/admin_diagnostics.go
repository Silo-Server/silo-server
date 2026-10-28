package handlers

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/diagnostics"
)

const diagnosticsDownloadExpiry = 15 * time.Minute

type AdminDiagnosticsService interface {
	ListForAdmin(ctx context.Context, filters diagnostics.ListFilters) (diagnostics.ListResult, error)
	GetReport(ctx context.Context, id string) (*diagnostics.Report, error)
	PresignReportDownload(ctx context.Context, report *diagnostics.Report, expiry time.Duration) (string, error)
	OpenReportDownload(ctx context.Context, report *diagnostics.Report) (io.ReadCloser, error)
	DeleteReport(ctx context.Context, id string) (*diagnostics.Report, error)
}

type adminDiagnosticsEffectiveTTL interface {
	EffectiveReportDownloadTTL(requested time.Duration) time.Duration
}

type diagnosticsDownloadURLResponse struct {
	DownloadURL string    `json:"download_url"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (h *DiagnosticsHandler) admin() (AdminDiagnosticsService, bool) {
	if h.adminService == nil {
		return nil, false
	}
	return h.adminService, true
}

func (h *DiagnosticsHandler) diagnosticsLogger() *slog.Logger {
	if h.logger == nil {
		return slog.Default()
	}
	return h.logger
}

func diagnosticsReportFilename(report *diagnostics.Report) string {
	name := strings.TrimSpace(report.ShortID)
	if name == "" {
		name = strings.TrimSpace(report.ID)
	}
	if name == "" {
		name = "report"
	}
	return "silo-diagnostics-" + name + ".tar.gz"
}

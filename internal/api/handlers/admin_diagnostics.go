package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/diagnostics"
	"github.com/Silo-Server/silo-server/internal/httpstream"
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

func RegisterAdminDiagnosticsRoutes(r chi.Router, h *DiagnosticsHandler) {
	if h == nil {
		return
	}
	r.Route("/diagnostics/reports", func(r chi.Router) {
		r.Get("/", h.HandleAdminListReports)
		r.Get("/{id}", h.HandleAdminGetReport)
		r.Get("/{id}/download", h.HandleAdminDownloadReport)
		r.Delete("/{id}", h.HandleAdminDeleteReport)
	})
}

func (h *DiagnosticsHandler) HandleAdminListReports(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.admin()
	if !ok {
		writeDiagnosticsAdminUnavailable(w)
		return
	}
	filters, err := parseDiagnosticsReportListFilters(r)
	if err != nil {
		writeDiagnosticsAdminError(w, err)
		return
	}
	result, err := admin.ListForAdmin(r.Context(), filters)
	if err != nil {
		writeDiagnosticsAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *DiagnosticsHandler) HandleAdminGetReport(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.admin()
	if !ok {
		writeDiagnosticsAdminUnavailable(w)
		return
	}
	report, err := admin.GetReport(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDiagnosticsAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (h *DiagnosticsHandler) HandleAdminDownloadReport(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.admin()
	if !ok {
		writeDiagnosticsAdminUnavailable(w)
		return
	}
	reportID := chi.URLParam(r, "id")
	report, err := admin.GetReport(r.Context(), reportID)
	if err != nil {
		writeDiagnosticsAdminError(w, err)
		return
	}
	if report.State != diagnostics.StateReady {
		writeDiagnosticsAdminError(w, diagnostics.ErrReportNotReady)
		return
	}

	ttl := diagnosticsEffectiveDownloadTTL(admin, diagnosticsDownloadExpiry)
	if !diagnosticsProxyDownloadRequested(r) {
		if url, err := admin.PresignReportDownload(r.Context(), report, ttl); err == nil && strings.TrimSpace(url) != "" {
			h.auditDownload(r, report.ID)
			writeJSON(w, http.StatusOK, diagnosticsDownloadURLResponse{
				DownloadURL: url,
				ExpiresAt:   time.Now().UTC().Add(ttl),
			})
			return
		}
	}

	body, err := admin.OpenReportDownload(r.Context(), report)
	if err != nil {
		writeDiagnosticsAdminError(w, err)
		return
	}
	defer body.Close()

	h.auditDownload(r, report.ID)
	// Without presigning this is the only delivery path, and a bundle can
	// outlast the API server's absolute WriteTimeout on a slow link.
	sw := httpstream.NewRollingDeadlineWriter(w)
	sw.Header().Set("Content-Type", diagnostics.ReportDownloadContentType)
	sw.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", diagnosticsReportFilename(report)))
	if report.BlobBytes != nil && *report.BlobBytes >= 0 {
		sw.Header().Set("Content-Length", strconv.FormatInt(*report.BlobBytes, 10))
	}
	sw.WriteHeader(http.StatusOK)
	if _, err := io.Copy(sw, body); err != nil {
		h.diagnosticsLogger().WarnContext(r.Context(), "diagnostic report download stream failed",
			"component", "diagnostics",
			"admin_user_id", currentAdminUserID(r),
			"report_id", report.ID,
			"error", err,
		)
	}
}

func (h *DiagnosticsHandler) HandleAdminDeleteReport(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.admin()
	if !ok {
		writeDiagnosticsAdminUnavailable(w)
		return
	}
	reportID := chi.URLParam(r, "id")
	report, err := admin.DeleteReport(r.Context(), reportID)
	if err != nil {
		writeDiagnosticsAdminError(w, err)
		return
	}
	h.diagnosticsLogger().InfoContext(r.Context(), "diagnostic report deleted",
		"component", "diagnostics",
		"admin_user_id", currentAdminUserID(r),
		"report_id", report.ID,
	)
	w.WriteHeader(http.StatusNoContent)
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

func (h *DiagnosticsHandler) auditDownload(r *http.Request, reportID string) {
	h.diagnosticsLogger().InfoContext(r.Context(), "diagnostic report downloaded",
		"component", "diagnostics",
		"admin_user_id", currentAdminUserID(r),
		"report_id", reportID,
	)
}

func parseDiagnosticsReportListFilters(r *http.Request) (diagnostics.ListFilters, error) {
	filters := diagnostics.ListFilters{
		Platform:   strings.TrimSpace(r.URL.Query().Get("platform")),
		ReportType: strings.TrimSpace(r.URL.Query().Get("report_type")),
		Cursor:     strings.TrimSpace(r.URL.Query().Get("cursor")),
		Limit:      parseLimit(r, 50),
	}

	userID, err := parseOptionalIntQuery(r, "user_id")
	if err != nil {
		return diagnostics.ListFilters{}, err
	}
	filters.UserID = userID

	if from, err := parseTimeQuery(r, "from"); err != nil {
		return diagnostics.ListFilters{}, err
	} else {
		filters.From = from
	}
	if to, err := parseTimeQuery(r, "to"); err != nil {
		return diagnostics.ListFilters{}, err
	} else {
		filters.To = to
	}

	shortID := strings.TrimSpace(r.URL.Query().Get("short_id"))
	if shortID != "" {
		normalized, err := diagnostics.ParseShortID(shortID)
		if err != nil {
			return diagnostics.ListFilters{}, err
		}
		filters.ShortID = normalized
	}
	return filters, nil
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

func diagnosticsProxyDownloadRequested(r *http.Request) bool {
	raw := strings.TrimSpace(r.URL.Query().Get("proxy"))
	return raw == "1" || strings.EqualFold(raw, "true") || strings.EqualFold(raw, "yes")
}

func diagnosticsEffectiveDownloadTTL(admin AdminDiagnosticsService, requested time.Duration) time.Duration {
	ttl := requested
	if effective, ok := admin.(adminDiagnosticsEffectiveTTL); ok {
		ttl = effective.EffectiveReportDownloadTTL(requested)
	}
	if ttl <= 0 {
		return requested
	}
	return ttl
}

func writeDiagnosticsAdminUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "service_unavailable", "Diagnostics reports are not configured")
}

func writeDiagnosticsAdminError(w http.ResponseWriter, err error) {
	var parseErr *requestParseError
	switch {
	case errors.As(err, &parseErr):
		writeError(w, http.StatusBadRequest, "bad_request", parseErr.Error())
	case errors.Is(err, diagnostics.ErrInvalidShortID):
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid short_id")
	case errors.Is(err, diagnostics.ErrInvalidCursor):
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid cursor")
	case errors.Is(err, diagnostics.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Diagnostic report not found")
	case errors.Is(err, diagnostics.ErrReportNotReady):
		writeError(w, http.StatusConflict, "not_ready", "Diagnostic report is not ready")
	case errors.Is(err, diagnostics.ErrStorageUnavailable):
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Diagnostics storage is not available")
	case diagnostics.IsObjectNotFound(err):
		writeError(w, http.StatusNotFound, "not_found", "Diagnostic report bundle not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Diagnostics report operation failed")
	}
}

package apiv2

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
)

type AdminDiagnosticDownloadService interface {
	OpenAdminDiagnosticDownload(context.Context, string) (handlers.AdminDiagnosticDownload, error)
}

func adminDiagnosticDownloadProblem(err error) *Problem {
	switch {
	case errors.Is(err, diagnostics.ErrNotFound), diagnostics.IsObjectNotFound(err):
		return NewProblem(TypeNotFound, "Diagnostic report bundle not found")
	case errors.Is(err, diagnostics.ErrReportNotReady):
		return NewProblem(TypeConflict, "Diagnostic report is not ready")
	case errors.Is(err, diagnostics.ErrReportStoreUnavailable), errors.Is(err, diagnostics.ErrStorageUnavailable):
		return unavailable("diagnostic reports")
	default:
		return NewProblem(TypeInternalError, "Diagnostic report download failed")
	}
}
func registerAdminDiagnosticDownload(reg *Registry) {
	operation := humaOp("GET", Prefix+"/admin/diagnostics/reports/{id}/download", "downloadAdminDiagnosticReport", "admin-observability", "Stream a ready diagnostic bundle through the API host. Range requests receive the complete bundle; no presigned URL is returned.")
	operation.Parameters = []*huma.Param{{Name: "id", In: "path", Required: true, Schema: &huma.Schema{Type: "string", MinLength: new(1), MaxLength: new(128)}}}
	operation.Responses = map[string]*huma.Response{
		"200": {Description: "Complete gzip-compressed diagnostic bundle", Content: map[string]*huma.MediaType{diagnostics.ReportDownloadContentType: {Schema: &huma.Schema{Type: "string", Format: "binary"}}}, Headers: map[string]*huma.Param{"Content-Disposition": {Schema: &huma.Schema{Type: "string"}}, "Content-Length": {Schema: &huma.Schema{Type: "integer"}}, "Accept-Ranges": {Schema: &huma.Schema{Type: "string"}}}},
	}
	for _, status := range []int{400, 404, 409, 500, 503} {
		operation.Responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{problemContentType: {Schema: &huma.Schema{Ref: "#/components/schemas/Problem"}}}}
	}
	RegisterRaw(reg, RawOperation{Operation: Operation{Operation: operation, Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(operation.Method), ServiceBacked: true}, Protocol: "diagnostic-bundle", Reason: "The existing administrator consumer downloads a gzip archive; bytes must not pass through JSON encoding or response buffering."}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if strings.TrimSpace(id) == "" || len(id) > 128 {
			writeProblem(w, r, NewProblem(TypeMalformedRequest, "Invalid report identifier"))
			return
		}
		if reg.deps.AdminDiagnosticDownloads == nil {
			writeProblem(w, r, unavailable("diagnostic reports"))
			return
		}
		download, err := reg.deps.AdminDiagnosticDownloads.OpenAdminDiagnosticDownload(r.Context(), id)
		if err != nil {
			writeProblem(w, r, adminDiagnosticDownloadProblem(err))
			return
		}
		if download.Body == nil {
			writeProblem(w, r, NewProblem(TypeInternalError, "Diagnostic report download failed"))
			return
		}
		defer download.Body.Close()
		// A bundle can outlast the API server's absolute WriteTimeout on a slow
		// link, and Accept-Ranges: none means it cannot resume. Roll the write
		// deadline forward while bytes keep flowing.
		sw := httpstream.NewRollingDeadlineWriter(w)
		sw.Header().Set("Content-Type", diagnostics.ReportDownloadContentType)
		sw.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{dispositionFilenameParam: download.Filename}))
		sw.Header().Set("Cache-Control", "no-store")
		sw.Header().Set("Accept-Ranges", "none")
		if download.Size != nil && *download.Size >= 0 {
			sw.Header().Set("Content-Length", strconv.FormatInt(*download.Size, 10))
		}
		sw.WriteHeader(http.StatusOK)
		if _, err := io.Copy(sw, download.Body); err != nil {
			slog.WarnContext(r.Context(), "diagnostic report stream interrupted", "component", "diagnostics", "report_id", id)
		}
	}))
}

package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/catalogseed"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/danielgtaylor/huma/v2"
)

const opExportAdminCatalog = "exportAdminCatalog"
const mediaTypeCatalogGzip = "application/gzip"

type AdminCatalogTransferService interface {
	ExportCatalog(context.Context, catalogseed.ExportOptions) ([]byte, error)
	CreateCatalogExportJob(context.Context, int, catalogseed.ExportOptions) (*models.AdminJob, error)
	ImportCatalog(context.Context, handlers.CatalogImportSourceSelection, catalogseed.ImportOptions) (*catalogseed.ImportResult, error)
	CreateCatalogImportJob(context.Context, int, handlers.CatalogImportSourceSelection, catalogseed.ImportOptions) (*models.AdminJob, error)
	PublishCatalogExportJob(context.Context, string) (*models.AdminJob, error)
}
type AdminCatalogExportRequest struct {
	LibraryIDs []ID `json:"library_ids,omitempty"`
}
type AdminCatalogExportInput struct {
	Body    AdminCatalogExportRequest
	RawBody []byte
}
type AdminCatalogExportOutput struct {
	ContentType        string `header:"Content-Type"`
	ContentDisposition string `header:"Content-Disposition"`
	ContentLength      string `header:"Content-Length"`
	Body               func(huma.Context)
}
type AdminCatalogImportRequest struct {
	LocalPath    string                    `json:"local_path,omitempty"`
	ExportJobID  string                    `json:"export_job_id,omitempty"`
	ArtifactKey  string                    `json:"artifact_key,omitempty"`
	RemoteURL    string                    `json:"remote_url,omitempty"`
	ConflictMode string                    `json:"conflict_mode" enum:"skip_existing,overwrite_existing"`
	PathRewrites []catalogseed.PathRewrite `json:"path_rewrites"`
}
type AdminCatalogImportInput struct {
	Body    AdminCatalogImportRequest
	RawBody []byte
}
type AdminCatalogImportOutput struct{ Body catalogseed.ImportResult }
type AdminCatalogJobAcceptedOutput struct {
	Location   string `header:"Location"`
	RetryAfter string `header:"Retry-After"`
	Body       AdminTaskJob
}
type AdminCatalogPublished struct {
	JobID     ID       `json:"job_id"`
	URL       string   `json:"url"`
	ExpiresAt *Instant `json:"expires_at,omitempty" doc:"Signed URL expiry; the artifact may be removed sooner"`
}
type AdminCatalogPublishedOutput struct{ Body AdminCatalogPublished }

func catalogTransferOp(path, id, summary string) Operation {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/catalog"+path, id, "admin-catalog", summary), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNonRetryable}
	switch path {
	case "/export-jobs", "/import-jobs", "/export-jobs/{id}/publish":
		op.Errors = append(op.Errors, http.StatusConflict)
	}
	return op
}
func registerAdminCatalogTransfer(reg *Registry) {
	export := catalogTransferOp("/export", opExportAdminCatalog, "Export a catalog seed synchronously as gzip bytes. Prefer persisted export jobs for large catalogs.")
	export.Responses = map[string]*huma.Response{"200": {Description: "Compressed catalog seed", Content: map[string]*huma.MediaType{mediaTypeCatalogGzip: {Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}}}}
	Register(reg, export, func(ctx context.Context, in *AdminCatalogExportInput) (*AdminCatalogExportOutput, error) {
		if reg.deps.AdminCatalogTransfer == nil {
			return nil, unavailable("catalog transfer")
		}
		opts, p := catalogExportOptions(in)
		if p != nil {
			return nil, p
		}
		data, err := reg.deps.AdminCatalogTransfer.ExportCatalog(ctx, opts)
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &AdminCatalogExportOutput{ContentType: mediaTypeCatalogGzip, ContentDisposition: `attachment; filename="silo-catalog-seed.json.gz"`, ContentLength: strconv.Itoa(len(data)), Body: func(ctx huma.Context) { _, _ = ctx.BodyWriter().Write(data) }}, nil
	})
	exportJob := catalogTransferOp("/export-jobs", "createCatalogExportJob", "Persist a queued catalog export job. HTTP 202 does not mean export completion.")
	exportJob.DefaultStatus = http.StatusAccepted
	Register(reg, exportJob, func(ctx context.Context, in *AdminCatalogExportInput) (*AdminCatalogJobAcceptedOutput, error) {
		if reg.deps.AdminCatalogTransfer == nil {
			return nil, unavailable("catalog transfer")
		}
		opts, p := catalogExportOptions(in)
		if p != nil {
			return nil, p
		}
		job, err := reg.deps.AdminCatalogTransfer.CreateCatalogExportJob(ctx, claimsFrom(ctx).UserID, opts)
		if err != nil {
			return nil, catalogTransferProblem(err)
		}
		return reg.catalogJobAccepted(ctx, job), nil
	})
	Register(reg, catalogTransferOp("/import", "importAdminCatalog", "Import synchronously. Success means the catalog transaction committed; do not automatically retry a lost response."), func(ctx context.Context, in *AdminCatalogImportInput) (*AdminCatalogImportOutput, error) {
		if reg.deps.AdminCatalogTransfer == nil {
			return nil, unavailable("catalog transfer")
		}
		source, opts, p := catalogImportOptions(in)
		if p != nil {
			return nil, p
		}
		result, err := reg.deps.AdminCatalogTransfer.ImportCatalog(ctx, source, opts)
		if err != nil {
			return nil, catalogTransferProblem(err)
		}
		return &AdminCatalogImportOutput{Body: *result}, nil
	})
	importJob := catalogTransferOp("/import-jobs", "createCatalogImportJob", "Persist a queued import job. A worker later reads the source; local paths must be available there and remote content can change.")
	importJob.DefaultStatus = http.StatusAccepted
	Register(reg, importJob, func(ctx context.Context, in *AdminCatalogImportInput) (*AdminCatalogJobAcceptedOutput, error) {
		if reg.deps.AdminCatalogTransfer == nil {
			return nil, unavailable("catalog transfer")
		}
		source, opts, p := catalogImportOptions(in)
		if p != nil {
			return nil, p
		}
		job, err := reg.deps.AdminCatalogTransfer.CreateCatalogImportJob(ctx, claimsFrom(ctx).UserID, source, opts)
		if err != nil {
			return nil, catalogTransferProblem(err)
		}
		return reg.catalogJobAccepted(ctx, job), nil
	})
	Register(reg, catalogTransferOp("/export-jobs/{id}/publish", "publishCatalogExportJob", "Save a seven-day signed download URL. An existing saved URL is returned without renewing its expiry; the storage ACL is unchanged."), func(ctx context.Context, in *AdminTaskJobInput) (*AdminCatalogPublishedOutput, error) {
		if reg.deps.AdminCatalogTransfer == nil {
			return nil, unavailable("catalog transfer")
		}
		job, err := reg.deps.AdminCatalogTransfer.PublishCatalogExportJob(ctx, in.ID)
		if err != nil {
			return nil, catalogTransferProblem(err)
		}
		out := &AdminCatalogPublishedOutput{Body: AdminCatalogPublished{JobID: ID(job.ID), URL: job.PublicURL}}
		if job.PublishedAt != nil {
			out.Body.ExpiresAt = instantPtr(new(job.PublishedAt.Add(7 * 24 * time.Hour)))
		}
		return out, nil
	})
}
func catalogExportOptions(in *AdminCatalogExportInput) (catalogseed.ExportOptions, *Problem) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return catalogseed.ExportOptions{}, p
	}
	opts := catalogseed.ExportOptions{}
	for _, id := range in.Body.LibraryIDs {
		n, p := libraryID(id)
		if p != nil {
			return opts, p
		}
		opts.LibraryIDs = append(opts.LibraryIDs, n)
	}
	return opts, nil
}
func catalogImportOptions(in *AdminCatalogImportInput) (handlers.CatalogImportSourceSelection, catalogseed.ImportOptions, *Problem) {
	source := handlers.CatalogImportSourceSelection{LocalPath: in.Body.LocalPath, ExportJobID: in.Body.ExportJobID, ArtifactKey: in.Body.ArtifactKey, RemoteURL: in.Body.RemoteURL}
	opts := catalogseed.ImportOptions{ConflictMode: catalogseed.ConflictMode(in.Body.ConflictMode), PathRewrites: in.Body.PathRewrites}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return source, opts, p
	}
	n := 0
	for _, value := range []string{source.LocalPath, source.ExportJobID, source.ArtifactKey, source.RemoteURL} {
		if value != "" {
			n++
		}
	}
	if n != 1 {
		return source, opts, NewProblem(TypeValidationFailed, "Select exactly one catalog import source.")
	}
	for _, rewrite := range opts.PathRewrites {
		if rewrite.From == "" || rewrite.To == "" {
			return source, opts, NewProblem(TypeValidationFailed, "Every path rewrite needs a source and destination.")
		}
	}
	return source, opts, nil
}
func (reg *Registry) catalogJobAccepted(ctx context.Context, job *models.AdminJob) *AdminCatalogJobAcceptedOutput {
	return &AdminCatalogJobAcceptedOutput{Location: Prefix + "/admin/jobs/" + job.ID, RetryAfter: "5", Body: reg.adminTaskJobOf(ctx, job, true)}
}
func catalogTransferProblem(err error) *Problem {
	if conflict, ok := errors.AsType[*adminjob.ActiveJobConflictError](err); ok {
		p := NewProblem(TypeConflict, "A catalog job is already queued or running.")
		if conflict.Job != nil {
			p = p.WithHeader("Location", Prefix+"/admin/jobs/"+conflict.Job.ID)
		}
		return p
	}
	if errors.Is(err, adminjob.ErrJobNotFound) {
		return NewProblem(TypeNotFound, "Job not found")
	}
	if roots, ok := errors.AsType[*catalogseed.UnmatchedRootsError](err); ok {
		p := NewProblem(TypeValidationFailed, "Additional path rewrites are required.")
		for _, root := range roots.Roots {
			p.Errors = append(p.Errors, ProblemError{Location: "body.path_rewrites", Code: "path_rewrite_required", Detail: "Source root requires a rewrite: " + root})
		}
		return p
	}
	if errors.Is(err, catalogseed.ErrInvalidBundle) || errors.Is(err, catalogseed.ErrUnsupportedBundleVersion) || errors.Is(err, catalogseed.ErrInvalidConflictMode) {
		return NewProblem(TypeValidationFailed, "Invalid or unsupported catalog seed import.")
	}
	return serviceProblem(err)
}

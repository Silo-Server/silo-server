package apiv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	adminJobSchemaRef = "#/components/schemas/AdminJob"
	jobLocationHeader = "Location"
)

// JobFailure is safe terminal failure data, not an HTTP problem response.
type JobFailure struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Retryable bool   `json:"retryable"`
}
type JobProgress struct {
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Unit    string `json:"unit"`
}
type LibraryRefreshJobResult struct {
	LibraryID ID  `json:"library_id"`
	Refreshed int `json:"refreshed"`
	Failed    int `json:"failed"`
}
type LibraryDeletionJobResult struct {
	LibraryID            ID  `json:"library_id"`
	DeletedMediaFiles    int `json:"deleted_media_files"`
	DeletedItemLinks     int `json:"deleted_item_links"`
	DeletedOrphanedItems int `json:"deleted_orphaned_items"`
}

// AdminJob is the canonical safe projection of accepted library work. Internal
// request documents, operator messages, storage keys and diagnostics never leave
// the job owner through this representation.
type AdminJob struct {
	TemplateResult *AdminTemplateResult      `json:"template_result,omitempty" nullable:"false"`
	ID             string                    `json:"id"`
	Kind           string                    `json:"kind"`
	State          string                    `json:"state"`
	Terminal       bool                      `json:"terminal"`
	Cancelable     bool                      `json:"cancelable"`
	CreatedAt      Instant                   `json:"created_at"`
	StartedAt      *Instant                  `json:"started_at,omitempty"`
	FinishedAt     *Instant                  `json:"finished_at,omitempty"`
	Progress       *JobProgress              `json:"progress,omitempty"`
	RefreshResult  *LibraryRefreshJobResult  `json:"refresh_result,omitempty"`
	DeletionResult *LibraryDeletionJobResult `json:"deletion_result,omitempty"`
	Failure        *JobFailure               `json:"failure,omitempty"`
}

func adminJobOf(job *models.AdminJob) AdminJob {
	out := AdminJob{ID: job.ID, Kind: job.JobType, State: job.Status,
		CreatedAt: NewInstant(job.RequestedAt), StartedAt: instantPtr(job.StartedAt), FinishedAt: instantPtr(job.CompletedAt)}
	switch job.Status {
	case adminjob.StatusCompleted:
		out.State = "succeeded"
		out.Terminal = true
	case adminjob.StatusFailed:
		out.Terminal = true
	case adminjob.StatusCancelled:
		out.State = "canceled"
		out.Terminal = true
	}
	if job.CancelRequested && !out.Terminal {
		out.State = "canceling"
	}
	out.Cancelable = (job.JobType == adminjob.JobTypeLibraryRefresh || job.JobType == adminjob.JobTypeStorageTransition) && !out.Terminal
	if job.JobType == adminjob.JobTypeLibraryRefresh && job.ProgressTotal > 0 && job.ProgressCurrent >= 0 && job.ProgressCurrent <= job.ProgressTotal {
		out.Progress = &JobProgress{Current: job.ProgressCurrent, Total: job.ProgressTotal, Unit: "items"}
	}
	if job.Status == adminjob.StatusFailed {
		out.Failure = &JobFailure{Type: ProblemTypeOrigin + "job_failed", Title: "Job failed", Detail: "The operation could not finish. Inspect administrator diagnostics before submitting new work.", Retryable: false}
	}
	if job.Status == adminjob.StatusCompleted {
		switch job.JobType {
		case adminjob.JobTypeLibraryRefresh:
			var result adminjob.LibraryRefreshResult
			if json.Unmarshal(job.ResultPayload, &result) == nil {
				out.RefreshResult = &LibraryRefreshJobResult{LibraryID: IDFromInt(int64(result.LibraryID)), Refreshed: result.RefreshedOK + result.PipelineOK, Failed: result.RefreshedFailed + result.PipelineFailed}
			}
		case adminjob.JobTypeDeleteLibrary:
			var result adminjob.DeleteLibraryResult
			if json.Unmarshal(job.ResultPayload, &result) == nil {
				out.DeletionResult = &LibraryDeletionJobResult{LibraryID: IDFromInt(int64(result.LibraryID)), DeletedMediaFiles: result.DeletedMediaFiles, DeletedItemLinks: result.DeletedItemLinks, DeletedOrphanedItems: result.DeletedOrphanedItems}
			}
		}
	}
	return out
}
func jsonDocument(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}
func libraryJobLocation(id string) string { return Prefix + "/library-jobs/" + id }
func acceptedJob(job *models.AdminJob) *AdminJobAcceptedOutput {
	return &AdminJobAcceptedOutput{Location: libraryJobLocation(job.ID), RetryAfter: "5", Body: adminJobOf(job)}
}

type LibraryJobService interface {
	GetByID(context.Context, string) (*models.AdminJob, error)
	RequestCancellation(context.Context, string) (*models.AdminJob, error)
}
type LibraryJobInput struct {
	JobID       string `path:"job_id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type CancelLibraryJobInput struct {
	JobID string `path:"job_id"`
}
type LibraryJobOutput struct {
	Status     int
	ETag       string `header:"ETag"`
	RetryAfter string `header:"Retry-After"`
	Location   string `header:"Location"`
	Body       AdminJob
}

func registerLibraryJobs(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/library-jobs/{job_id}", "getLibraryJob", "libraries", "Poll accepted library work. Terminal jobs remain available for at least 24 hours."), Class: ClassAuthenticated, ServiceBacked: true, Conditional: true}, reg.getLibraryJob)
	op := humaOp(http.MethodPost, Prefix+"/library-jobs/{job_id}/cancel", "cancelLibraryJob", "libraries", "Request best-effort cancellation of metadata refresh. Completed metadata changes remain. Deletion cannot be canceled.")
	op.DefaultStatus = http.StatusAccepted
	op.Errors = []int{http.StatusConflict}
	op.Responses = map[string]*huma.Response{"200": {
		Description: "The job was already canceled.",
		Content:     map[string]*huma.MediaType{mediaTypeJSON: {Schema: &huma.Schema{Ref: adminJobSchemaRef}}},
		Headers:     map[string]*huma.Header{etagField: {Schema: &huma.Schema{Type: huma.TypeString}}, jobLocationHeader: {Schema: &huma.Schema{Type: huma.TypeString}}},
	}}
	Register(reg, Operation{Operation: op, Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(op.Method), ServiceBacked: true, RetrySafety: RetrySafetyCoalescing}, reg.cancelLibraryJob)
}
func (reg *Registry) visibleLibraryJob(ctx context.Context, id string) (*models.AdminJob, error) {
	claims := claimsFrom(ctx)
	if claims == nil || claims.Role != models.RoleAdmin {
		return nil, NewProblem(TypeNotFound, "Job not found")
	}
	if reg.deps.LibraryJobs == nil {
		return nil, unavailable("library jobs")
	}
	job, err := reg.deps.LibraryJobs.GetByID(ctx, id)
	if errors.Is(err, adminjob.ErrJobNotFound) {
		return nil, NewProblem(TypeNotFound, "Job not found")
	}
	if err != nil {
		return nil, serviceProblem(err)
	}
	if job.JobType != adminjob.JobTypeLibraryRefresh && job.JobType != adminjob.JobTypeDeleteLibrary {
		return nil, NewProblem(TypeNotFound, "Job not found")
	}
	return job, nil
}
func libraryJobOutput(job *models.AdminJob) *LibraryJobOutput {
	out := &LibraryJobOutput{Body: adminJobOf(job)}
	data, _ := json.Marshal(out.Body)
	sum := sha256.Sum256(data)
	out.ETag = EntityTag{Opaque: hex.EncodeToString(sum[:])}.String()
	if !out.Body.Terminal {
		out.RetryAfter = "5"
	}
	return out
}
func (reg *Registry) getLibraryJob(ctx context.Context, in *LibraryJobInput) (*LibraryJobOutput, error) {
	job, err := reg.visibleLibraryJob(ctx, in.JobID)
	if err != nil {
		return nil, err
	}
	out := libraryJobOutput(job)
	tag, _ := ParseEntityTag(out.ETag)
	if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
		return nil, p
	} else if matched {
		return NotModified(out, tag), nil
	}
	return out, nil
}
func (reg *Registry) cancelLibraryJob(ctx context.Context, in *CancelLibraryJobInput) (*LibraryJobOutput, error) {
	if _, err := reg.visibleLibraryJob(ctx, in.JobID); err != nil {
		return nil, err
	}
	job, err := reg.deps.LibraryJobs.RequestCancellation(ctx, in.JobID)
	if errors.Is(err, adminjob.ErrJobNotCancellable) {
		return nil, NewProblem(TypeJobNotCancelable, "This job cannot be canceled")
	}
	if errors.Is(err, adminjob.ErrJobNotFound) {
		return nil, NewProblem(TypeNotFound, "Job not found")
	}
	if err != nil {
		return nil, serviceProblem(err)
	}
	out := libraryJobOutput(job)
	out.Location = libraryJobLocation(job.ID)
	out.Status = http.StatusAccepted
	if job.Status == adminjob.StatusCancelled {
		out.Status = http.StatusOK
	}
	return out, nil
}

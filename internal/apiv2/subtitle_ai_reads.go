package apiv2

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles/ai"
)

type SubtitleAIReadService interface {
	ListSubtitleAIJobs(context.Context, catalogpkg.AccessFilter, int) ([]ai.Job, error)
	GetSubtitleAIJob(context.Context, catalogpkg.AccessFilter, int64) (*ai.Job, error)
	SubtitleAIQuota(context.Context, int, string, bool) (ai.QuotaStatus, error)
}

type SubtitleAIJob struct {
	ID               ID      `json:"id"`
	MediaFileID      ID      `json:"media_file_id"`
	Kind             string  `json:"kind"`
	SourceIndex      int     `json:"source_index"`
	SourceLanguage   string  `json:"source_language"`
	TargetLanguage   string  `json:"target_language"`
	Engine           string  `json:"engine"`
	Model            string  `json:"model"`
	Status           string  `json:"status"`
	Progress         float64 `json:"progress"`
	ProgressMessage  string  `json:"progress_message"`
	ResultSubtitleID *ID     `json:"result_subtitle_id" nullable:"true"`
	ErrorMessage     string  `json:"error_message,omitempty"`
	CreatedAt        Instant `json:"created_at"`
	UpdatedAt        Instant `json:"updated_at"`
}
type SubtitleAIJobsInput struct {
	MediaFileID ID `query:"media_file_id" required:"true"`
}
type SubtitleAIJobInput struct {
	JobID ID `path:"job_id"`
}
type SubtitleAIJobsOutput struct {
	Body struct {
		Jobs []SubtitleAIJob `json:"jobs"`
	}
}
type SubtitleAIJobOutput struct {
	Body struct {
		Job SubtitleAIJob `json:"job"`
	}
}
type SubtitleAIQuotaOutput struct{ Body ai.QuotaStatus }

func projectSubtitleAIJob(job *ai.Job) SubtitleAIJob {
	out := SubtitleAIJob{ID: ID(strconv.FormatInt(job.ID, 10)), MediaFileID: ID(strconv.Itoa(job.MediaFileID)), Kind: string(job.Kind), SourceIndex: job.SourceIndex, SourceLanguage: job.SourceLanguage, TargetLanguage: job.TargetLanguage, Engine: job.Engine, Model: job.Model, Status: string(job.Status), Progress: job.Progress, ProgressMessage: job.ProgressMessage, CreatedAt: NewInstant(job.CreatedAt), UpdatedAt: NewInstant(job.UpdatedAt)}
	if job.ResultSubtitleID != nil {
		out.ResultSubtitleID = new(ID(strconv.Itoa(*job.ResultSubtitleID)))
	}
	if job.ErrorMessage != "" {
		out.ErrorMessage = "Subtitle processing failed."
		if strings.Contains(job.ErrorMessage, llm.ErrQuotaExhausted.Error()) {
			out.ErrorMessage = "The AI provider has no available credit or has reached its spending limit. Ask a server administrator to check AI Services."
		}
	}
	return out
}

func (reg *Registry) subtitleAIReadAccess(ctx context.Context) (catalogpkg.AccessFilter, *Problem) {
	if reg.deps.SubtitleAIReads == nil || reg.deps.CatalogAccess == nil {
		return catalogpkg.AccessFilter{}, NewProblem(TypeDependencyUnavailable, "Subtitle AI is unavailable.")
	}
	filter, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
	if err != nil {
		return catalogpkg.AccessFilter{}, catalogProblem(err, "query")
	}
	return filter, nil
}

func registerSubtitleAIReads(reg *Registry) {
	op := func(path, id string) Operation {
		return Operation{Operation: humaOp(http.MethodGet, Prefix+path, id, "subtitles", "Read authorized subtitle AI state."), Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true}
	}
	list := op("/subtitles/ai/jobs", "listSubtitleAIJobs")
	list.Description = "Read up to 50 recent jobs for one accessible media file. This is a bounded recent-activity view, not a complete job history."
	Register(reg, list, func(ctx context.Context, in *SubtitleAIJobsInput) (*SubtitleAIJobsOutput, error) {
		id, p := in.MediaFileID.positive("query.media_file_id")
		if p != nil {
			return nil, p
		}
		filter, p := reg.subtitleAIReadAccess(ctx)
		if p != nil {
			return nil, p
		}
		jobs, err := reg.deps.SubtitleAIReads.ListSubtitleAIJobs(ctx, filter, id)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := new(SubtitleAIJobsOutput)
		out.Body.Jobs = make([]SubtitleAIJob, 0, len(jobs))
		for i := range jobs {
			out.Body.Jobs = append(out.Body.Jobs, projectSubtitleAIJob(&jobs[i]))
		}
		return out, nil
	})
	Register(reg, op("/subtitles/ai/jobs/{job_id}", "getSubtitleAIJob"), func(ctx context.Context, in *SubtitleAIJobInput) (*SubtitleAIJobOutput, error) {
		id, idProblem := in.JobID.positive64("path.job_id")
		if idProblem != nil {
			return nil, validationProblem("path.job_id", "invalid", "Expected a positive job identifier.")
		}
		filter, p := reg.subtitleAIReadAccess(ctx)
		if p != nil {
			return nil, p
		}
		job, err := reg.deps.SubtitleAIReads.GetSubtitleAIJob(ctx, filter, id)
		if err != nil {
			return nil, serviceProblem(err)
		}
		if job == nil {
			return nil, NewProblem(TypeInternalError, "Subtitle job returned no state.")
		}
		out := new(SubtitleAIJobOutput)
		out.Body.Job = projectSubtitleAIJob(job)
		return out, nil
	})
	Register(reg, op("/subtitles/ai/quota", "getSubtitleAIQuota"), func(ctx context.Context, _ *struct{}) (*SubtitleAIQuotaOutput, error) {
		if reg.deps.SubtitleAIReads == nil {
			return nil, NewProblem(TypeDependencyUnavailable, "Subtitle AI is unavailable.")
		}
		quota, err := reg.deps.SubtitleAIReads.SubtitleAIQuota(ctx, apimw.GetUserID(ctx), apimw.GetProfileID(ctx), apimw.IsAdmin(ctx))
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &SubtitleAIQuotaOutput{Body: quota}, nil
	})
}

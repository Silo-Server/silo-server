package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

type SubtitleAICancelService interface {
	CancelSubtitleAIJob(context.Context, catalogpkg.AccessFilter, int64) error
}

func registerSubtitleAICancel(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/subtitles/ai/jobs/{job_id}/cancel", "cancelSubtitleAIJob", "subtitles", "Request cancellation of an authorized subtitle AI job."), Class: ClassProfileScoped, ProfileOptional: true, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.DefaultStatus = http.StatusNoContent
	op.Description = "A terminal job remains unchanged. Completion can win a concurrent cancellation; read the job to determine the outcome. Cancellation does not erase earlier output or confirm that provider compute and live cues stopped immediately."
	Register(reg, op, func(ctx context.Context, in *SubtitleAIJobInput) (*struct{}, error) {
		id, idProblem := in.JobID.positive64("path.job_id")
		if idProblem != nil {
			return nil, validationProblem("path.job_id", "invalid", "Expected a positive job identifier.")
		}
		if reg.deps.SubtitleAICancel == nil || reg.deps.CatalogAccess == nil {
			return nil, NewProblem(TypeDependencyUnavailable, "Subtitle AI is unavailable.")
		}
		filter, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
		if err != nil {
			return nil, catalogProblem(err, "query")
		}
		if err := reg.deps.SubtitleAICancel.CancelSubtitleAIJob(ctx, filter, id); err != nil {
			return nil, serviceProblem(err)
		}
		return nil, nil
	})
}

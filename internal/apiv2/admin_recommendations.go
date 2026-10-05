package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// AdminRecommendationsService is the existing process-local recommendation worker.
type AdminRecommendationsService interface {
	StatusCounts(context.Context) (int, int, int, int, int, error)
	IsRunning(recommendations.JobName) bool
	LastRuns(context.Context) (map[recommendations.JobName]taskmanager.ExecutionResult, error)
	EmbeddingLockConflict(context.Context) (string, error)
	CacheRefreshedAt(context.Context) (*time.Time, error)
	TriggerEmbeddings() error
	TriggerTasteProfiles() error
	TriggerCowatch() error
	TriggerRecommendations() error
	ResetEmbeddings(context.Context) (recommendations.EmbeddingsReset, error)
}
type AdminRecommendationJobStatus struct {
	Running bool                       `json:"running"`
	Count   int                        `json:"count"`
	Total   *int                       `json:"total,omitempty"`
	LastRun *AdminRecommendationJobRun `json:"last_run,omitempty" doc:"The newest finished run of this job on any server. Absent until one finishes."`
}
type AdminRecommendationJobRun struct {
	Status      string         `json:"status" enum:"completed,failed" doc:"completed also covers runs that finished with partial failures; result counts them."`
	StartedAt   Instant        `json:"started_at"`
	CompletedAt Instant        `json:"completed_at"`
	Error       string         `json:"error,omitempty" doc:"Why the run failed, with credentials masked. Present only when status is failed."`
	Result      map[string]any `json:"result,omitempty" doc:"Counts the run reported. Keys differ by job and may grow."`
}
type AdminRecommendationsStatus struct {
	Embeddings       AdminRecommendationJobStatus `json:"embeddings"`
	TasteProfiles    AdminRecommendationJobStatus `json:"taste_profiles"`
	Cowatch          AdminRecommendationJobStatus `json:"cowatch"`
	Recommendations  AdminRecommendationJobStatus `json:"recommendations"`
	LockConflict     string                       `json:"lock_conflict" doc:"Why the stored embedding lock rejects the embedding settings this server runs with; empty when it does not."`
	CacheRefreshedAt *Instant                     `json:"cache_refreshed_at,omitempty" doc:"When the newest cached recommendation row was written. Absent when nothing is cached."`
}
type AdminRecommendationsStatusOutput struct{ Body AdminRecommendationsStatus }
type AdminRecommendationStarted struct {
	Status string `json:"status" enum:"started" doc:"This process started background work; no durable job is created."`
}
type AdminRecommendationStartedOutput struct{ Body AdminRecommendationStarted }
type AdminRecommendationEmbeddingsReset struct {
	Embeddings    int64 `json:"embeddings" doc:"Item embeddings deleted."`
	TasteProfiles int64 `json:"taste_profiles" doc:"Taste profiles deleted."`
	TasteClusters int64 `json:"taste_clusters" doc:"Taste clusters deleted."`
	CachedRows    int64 `json:"cached_rows" doc:"Per-profile cached recommendation rows deleted. Global rows are kept."`
}
type AdminRecommendationEmbeddingsResetOutput struct {
	Body AdminRecommendationEmbeddingsReset
}

func registerAdminRecommendations(reg *Registry) {
	op := func(method, path, id, summary string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/recommendations"+path, id, "admin-recommendations", summary), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: isMutatingMethod(method)}
		if method != http.MethodGet {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	Register(reg, op(http.MethodGet, "/status", "getAdminRecommendationsStatus", "Read persisted counts, each job's last run, the embedding lock conflict and this process's running flags."), reg.getAdminRecommendationsStatus)
	for _, action := range []struct {
		path, id string
		start    func(AdminRecommendationsService) error
	}{
		{"embeddings", "triggerAdminRecommendationEmbeddings", AdminRecommendationsService.TriggerEmbeddings},
		{"taste-profiles", "triggerAdminRecommendationTasteProfiles", AdminRecommendationsService.TriggerTasteProfiles},
		{"cowatch", "triggerAdminRecommendationCowatch", AdminRecommendationsService.TriggerCowatch},
		{"recommendations", "triggerAdminRecommendationRefresh", AdminRecommendationsService.TriggerRecommendations},
	} {
		Register(reg, op(http.MethodPost, "/trigger/"+action.path, action.id, "Start process-local recommendation work without a durable job receipt."), func(ctx context.Context, _ *struct{}) (*AdminRecommendationStartedOutput, error) {
			if reg.deps.AdminRecommendations == nil {
				return nil, unavailable("recommendations")
			}
			if err := action.start(reg.deps.AdminRecommendations); err != nil {
				switch {
				case errors.Is(err, recommendations.ErrJobRunningElsewhere):
					return nil, NewProblem(TypeConflict, "This recommendation job is already running on another server.")
				case errors.Is(err, recommendations.ErrJobRunning):
					return nil, NewProblem(TypeConflict, "This recommendation job is already running on this process.")
				default:
					return nil, serviceProblem(err)
				}
			}
			return &AdminRecommendationStartedOutput{Body: AdminRecommendationStarted{Status: "started"}}, nil
		})
	}
	reset := op(http.MethodPost, "/embeddings/reset", "resetAdminRecommendationEmbeddings", "Delete the embedding lock, every item embedding, taste profiles and per-profile cached rows in one transaction.")
	reset.Description = "Recovers a server whose embedding lock pins a model it can no longer use, or switches embedding models. " +
		"It refuses with 409 while the embedding, taste profile or recommendation job, or a stale profile sweep, runs on any server, " +
		"while a catalog import with embeddings runs, and while saved embedding settings wait for a server restart. " +
		"Run the embedding job afterwards to build the new embedding space."
	reset.Errors = append(reset.Errors, http.StatusConflict)
	Register(reg, reset, reg.resetAdminRecommendationEmbeddings)
}

func (reg *Registry) resetAdminRecommendationEmbeddings(ctx context.Context, _ *struct{}) (*AdminRecommendationEmbeddingsResetOutput, error) {
	w := reg.deps.AdminRecommendations
	if w == nil {
		return nil, unavailable("recommendations")
	}
	res, err := w.ResetEmbeddings(ctx)
	if err != nil {
		if errors.Is(err, recommendations.ErrJobRunning) || errors.Is(err, recommendations.ErrJobRunningElsewhere) || errors.Is(err, recommendations.ErrStaleSweepRunning) {
			return nil, NewProblem(TypeConflict, "Recommendation work that uses embeddings is running on this or another server. Reset embeddings after it finishes.")
		}
		if errors.Is(err, recommendations.ErrCatalogImportRunning) {
			return nil, NewProblem(TypeConflict, "A catalog import with embeddings is running. Reset embeddings after it finishes.")
		}
		if errors.Is(err, recommendations.ErrEmbeddingSettingsPendingRestart) {
			return nil, NewProblem(TypeConflict, "The saved embedding settings take effect after a server restart. Restart the server, then reset embeddings.")
		}
		return nil, serviceProblem(err)
	}
	return &AdminRecommendationEmbeddingsResetOutput{Body: AdminRecommendationEmbeddingsReset{
		Embeddings: res.Embeddings, TasteProfiles: res.TasteProfiles, TasteClusters: res.TasteClusters, CachedRows: res.CachedRows,
	}}, nil
}

func (reg *Registry) getAdminRecommendationsStatus(ctx context.Context, _ *struct{}) (*AdminRecommendationsStatusOutput, error) {
	w := reg.deps.AdminRecommendations
	if w == nil {
		return nil, unavailable("recommendations")
	}
	embedded, total, taste, cache, cowatch, err := w.StatusCounts(ctx)
	if err != nil {
		return nil, serviceProblem(err)
	}
	lastRuns, err := w.LastRuns(ctx)
	if err != nil {
		return nil, serviceProblem(err)
	}
	conflict, err := w.EmbeddingLockConflict(ctx)
	if err != nil {
		return nil, serviceProblem(err)
	}
	refreshedAt, err := w.CacheRefreshedAt(ctx)
	if err != nil {
		return nil, serviceProblem(err)
	}
	jobStatus := func(name recommendations.JobName, count int) AdminRecommendationJobStatus {
		status := AdminRecommendationJobStatus{Running: w.IsRunning(name), Count: count}
		if run, ok := lastRuns[name]; ok {
			status.LastRun = adminRecommendationJobRunOf(run)
		}
		return status
	}
	out := &AdminRecommendationsStatusOutput{Body: AdminRecommendationsStatus{
		Embeddings:       jobStatus(recommendations.JobEmbeddings, embedded),
		TasteProfiles:    jobStatus(recommendations.JobTasteProfiles, taste),
		Cowatch:          jobStatus(recommendations.JobCowatch, cowatch),
		Recommendations:  jobStatus(recommendations.JobRecommendations, cache),
		LockConflict:     conflict,
		CacheRefreshedAt: instantPtr(refreshedAt),
	}}
	if total != 0 {
		out.Body.Embeddings.Total = new(total)
	}
	return out, nil
}

func adminRecommendationJobRunOf(run taskmanager.ExecutionResult) *AdminRecommendationJobRun {
	out := &AdminRecommendationJobRun{Status: run.Status, StartedAt: NewInstant(run.StartedAt), CompletedAt: NewInstant(run.CompletedAt)}
	if run.ErrorMessage != "" {
		// Job errors can quote provider responses; mask credentials the
		// way stored diagnostics are masked.
		out.Error = logredact.SanitizeText(run.ErrorMessage)
	}
	if len(run.ResultData) > 0 {
		var result map[string]any
		if json.Unmarshal(run.ResultData, &result) == nil {
			out.Result = result
		}
	}
	return out
}

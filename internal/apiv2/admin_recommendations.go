package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// AdminRecommendationsService is the existing process-local recommendation worker.
type AdminRecommendationsService interface {
	StatusCounts(context.Context) (int, int, int, int, int, error)
	IsRunning(recommendations.JobName) bool
	TriggerEmbeddings() error
	TriggerTasteProfiles() error
	TriggerCowatch() error
	TriggerRecommendations() error
}
type AdminRecommendationJobStatus struct {
	Running bool `json:"running"`
	Count   int  `json:"count"`
	Total   *int `json:"total,omitempty"`
}
type AdminRecommendationsStatus struct {
	Embeddings      AdminRecommendationJobStatus `json:"embeddings"`
	TasteProfiles   AdminRecommendationJobStatus `json:"taste_profiles"`
	Cowatch         AdminRecommendationJobStatus `json:"cowatch"`
	Recommendations AdminRecommendationJobStatus `json:"recommendations"`
}
type AdminRecommendationsStatusOutput struct{ Body AdminRecommendationsStatus }
type AdminRecommendationStarted struct {
	Status string `json:"status" enum:"started" doc:"This process started background work; no durable job is created."`
}
type AdminRecommendationStartedOutput struct{ Body AdminRecommendationStarted }

func registerAdminRecommendations(reg *Registry) {
	op := func(method, path, id, summary string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/recommendations"+path, id, "admin-recommendations", summary), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: isMutatingMethod(method)}
		if method != http.MethodGet {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	Register(reg, op(http.MethodGet, "/status", "getAdminRecommendationsStatus", "Read persisted counts and this process's running flags."), reg.getAdminRecommendationsStatus)
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
				return nil, NewProblem(TypeConflict, "This recommendation job is already running on this process.")
			}
			return &AdminRecommendationStartedOutput{Body: AdminRecommendationStarted{Status: "started"}}, nil
		})
	}
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
	out := &AdminRecommendationsStatusOutput{Body: AdminRecommendationsStatus{
		Embeddings:      AdminRecommendationJobStatus{Running: w.IsRunning(recommendations.JobEmbeddings), Count: embedded},
		TasteProfiles:   AdminRecommendationJobStatus{Running: w.IsRunning(recommendations.JobTasteProfiles), Count: taste},
		Cowatch:         AdminRecommendationJobStatus{Running: w.IsRunning(recommendations.JobCowatch), Count: cowatch},
		Recommendations: AdminRecommendationJobStatus{Running: w.IsRunning(recommendations.JobRecommendations), Count: cache},
	}}
	if total != 0 {
		out.Body.Embeddings.Total = new(total)
	}
	return out, nil
}

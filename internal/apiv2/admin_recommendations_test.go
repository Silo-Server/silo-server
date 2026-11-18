package apiv2

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// fakeAdminRecommendationsTime is the fixed time the fake reports, so the
// contract fixtures stay byte-stable.
var fakeAdminRecommendationsTime = time.Date(2026, 10, 3, 3, 0, 0, 0, time.UTC)

type fakeAdminRecommendations struct {
	calls int
	last  recommendations.JobName
	err   error

	runsErr      error
	noRuns       bool
	conflict     string
	conflictKey  string
	conflictArgs map[string]string
	conflictErr  error
	noRefresh    bool
	refreshErr   error
	resets       int
	resetErr     error
	sweepErr     error
}

func (f *fakeAdminRecommendations) StatusCounts(context.Context) (int, int, int, int, int, error) {
	return 3, 10, 4, 5, 6, f.err
}
func (*fakeAdminRecommendations) IsRunning(job recommendations.JobName) bool {
	return job == recommendations.JobEmbeddings
}
func (f *fakeAdminRecommendations) LastRuns(context.Context) (map[recommendations.JobName]recommendations.JobRuns, error) {
	if f.runsErr != nil || f.noRuns {
		return map[recommendations.JobName]recommendations.JobRuns{}, f.runsErr
	}
	return map[recommendations.JobName]recommendations.JobRuns{
		recommendations.JobEmbeddings: {
			Full: &taskmanager.ExecutionResult{
				TaskKey: "recommendations.embeddings", Status: "failed",
				StartedAt: fakeAdminRecommendationsTime, CompletedAt: fakeAdminRecommendationsTime.Add(90 * time.Second),
				ResultData:   json.RawMessage(`{"embedded":0}`),
				ErrorMessage: `embed batch: POST https://user:hunter2@embed.example/v1/embeddings: api_key=secret-value rejected`,
			},
			Partial: &taskmanager.ExecutionResult{
				TaskKey: "recommendations.embeddings.catch_up", Status: "completed",
				StartedAt: fakeAdminRecommendationsTime.Add(15 * time.Minute), CompletedAt: fakeAdminRecommendationsTime.Add(16 * time.Minute),
				ResultData: json.RawMessage(`{"missing_only":true,"embedded":2}`),
			},
		},
		recommendations.JobRecommendations: {
			Full: &taskmanager.ExecutionResult{
				TaskKey: "recommendations.cache", Status: "completed",
				StartedAt: fakeAdminRecommendationsTime.Add(2 * time.Hour), CompletedAt: fakeAdminRecommendationsTime.Add(2*time.Hour + time.Minute),
				ResultData: json.RawMessage(`{"profiles":2,"processed":2,"cached_rows":14,"build_errors":0}`),
			},
		},
	}, nil
}
func (f *fakeAdminRecommendations) EmbeddingLockConflict(context.Context) (string, string, map[string]string, error) {
	return f.conflict, f.conflictKey, f.conflictArgs, f.conflictErr
}
func (f *fakeAdminRecommendations) CacheRefreshedAt(context.Context) (*time.Time, error) {
	if f.refreshErr != nil || f.noRefresh {
		return nil, f.refreshErr
	}
	at := fakeAdminRecommendationsTime.Add(2*time.Hour + time.Minute)
	return &at, nil
}
func (f *fakeAdminRecommendations) StaleSweep(context.Context) (recommendations.StaleSweepStatus, error) {
	if f.sweepErr != nil {
		return recommendations.StaleSweepStatus{}, f.sweepErr
	}
	if f.noRuns {
		return recommendations.StaleSweepStatus{}, nil
	}
	return recommendations.StaleSweepStatus{Pending: 7, LastRun: &taskmanager.ExecutionResult{
		TaskKey: "recommendations.stale_sweep", Status: "completed",
		StartedAt: fakeAdminRecommendationsTime.Add(3 * time.Hour), CompletedAt: fakeAdminRecommendationsTime.Add(3*time.Hour + 20*time.Second),
		ResultData: json.RawMessage(`{"stale":3,"refreshed":2,"failed":1,"deferred":0,"queued":0}`),
	}}, nil
}
func (f *fakeAdminRecommendations) ResetEmbeddings(context.Context) (recommendations.EmbeddingsReset, error) {
	f.resets++
	if f.resetErr != nil {
		return recommendations.EmbeddingsReset{}, f.resetErr
	}
	return recommendations.EmbeddingsReset{Embeddings: 120, TasteProfiles: 3, TasteClusters: 9, CachedRows: 41}, nil
}
func (f *fakeAdminRecommendations) start(job recommendations.JobName) error {
	f.calls++
	f.last = job
	return f.err
}
func (f *fakeAdminRecommendations) TriggerEmbeddings() error {
	return f.start(recommendations.JobEmbeddings)
}
func (f *fakeAdminRecommendations) TriggerTasteProfiles() error {
	return f.start(recommendations.JobTasteProfiles)
}
func (f *fakeAdminRecommendations) TriggerCowatch() error { return f.start(recommendations.JobCowatch) }
func (f *fakeAdminRecommendations) TriggerRecommendations() error {
	return f.start(recommendations.JobRecommendations)
}

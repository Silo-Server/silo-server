package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
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

	runsErr     error
	noRuns      bool
	conflict    string
	conflictErr error
	noRefresh   bool
	refreshErr  error
	resets      int
	resetErr    error
}

func (f *fakeAdminRecommendations) StatusCounts(context.Context) (int, int, int, int, int, error) {
	return 3, 10, 4, 5, 6, f.err
}
func (*fakeAdminRecommendations) IsRunning(job recommendations.JobName) bool {
	return job == recommendations.JobEmbeddings
}
func (f *fakeAdminRecommendations) LastRuns(context.Context) (map[recommendations.JobName]taskmanager.ExecutionResult, error) {
	if f.runsErr != nil || f.noRuns {
		return map[recommendations.JobName]taskmanager.ExecutionResult{}, f.runsErr
	}
	return map[recommendations.JobName]taskmanager.ExecutionResult{
		recommendations.JobEmbeddings: {
			TaskKey: "recommendations.embeddings", Status: "failed",
			StartedAt: fakeAdminRecommendationsTime, CompletedAt: fakeAdminRecommendationsTime.Add(90 * time.Second),
			ResultData:   json.RawMessage(`{"embedded":0}`),
			ErrorMessage: `embed batch: POST https://user:hunter2@embed.example/v1/embeddings: api_key=secret-value rejected`,
		},
		recommendations.JobRecommendations: {
			TaskKey: "recommendations.cache", Status: "completed",
			StartedAt: fakeAdminRecommendationsTime.Add(2 * time.Hour), CompletedAt: fakeAdminRecommendationsTime.Add(2*time.Hour + time.Minute),
			ResultData: json.RawMessage(`{"profiles":2,"processed":2,"cached_rows":14,"build_errors":0}`),
		},
	}, nil
}
func (f *fakeAdminRecommendations) EmbeddingLockConflict(context.Context) (string, error) {
	return f.conflict, f.conflictErr
}
func (f *fakeAdminRecommendations) CacheRefreshedAt(context.Context) (*time.Time, error) {
	if f.refreshErr != nil || f.noRefresh {
		return nil, f.refreshErr
	}
	at := fakeAdminRecommendationsTime.Add(2*time.Hour + time.Minute)
	return &at, nil
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

func TestAdminRecommendationsTransport(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminRecommendations{}
	deps.AdminRecommendations = f
	h := newTestHandler(t, deps)
	rec := do(t, h, "GET", Prefix+"/admin/recommendations/status", "", bearer(adminToken))
	var status AdminRecommendationsStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || !status.Embeddings.Running || status.Embeddings.Count != 3 || status.Embeddings.Total == nil || *status.Embeddings.Total != 10 || status.TasteProfiles.Count != 4 || status.Recommendations.Count != 5 || status.Cowatch.Count != 6 {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	for _, tc := range []struct {
		path string
		job  recommendations.JobName
	}{
		{"embeddings", recommendations.JobEmbeddings}, {"taste-profiles", recommendations.JobTasteProfiles}, {"cowatch", recommendations.JobCowatch}, {"recommendations", recommendations.JobRecommendations},
	} {
		path := Prefix + "/admin/recommendations/trigger/" + tc.path
		before := f.calls
		rec = do(t, h, "POST", path, "", bearer(adminToken))
		if rec.Code != 200 || f.calls != before+1 || f.last != tc.job || rec.Header().Get("Location") != "" || !strings.Contains(rec.Body.String(), `"status":"started"`) {
			t.Fatalf("start %s: %d %s %#v", tc.path, rec.Code, rec.Body, f)
		}
		for _, busy := range []struct {
			err   error
			where string
		}{
			{fmt.Errorf("private worker detail: %w", recommendations.ErrJobRunning), "this process"},
			{fmt.Errorf("private worker detail: %w", recommendations.ErrJobRunningElsewhere), "another server"},
		} {
			f.err = busy.err
			rec = do(t, h, "POST", path, "", bearer(adminToken))
			if rec.Code != 409 || !strings.Contains(rec.Body.String(), busy.where) || strings.Contains(rec.Body.String(), "private worker") {
				t.Fatalf("conflict %s (%s): %d %s", tc.path, busy.where, rec.Code, rec.Body)
			}
		}
		// Failing to take the cluster lock is not a conflict.
		f.err = errors.New("private lock detail")
		rec = do(t, h, "POST", path, "", bearer(adminToken))
		if rec.Code != 500 || strings.Contains(rec.Body.String(), "private lock") {
			t.Fatalf("lock failure %s: %d %s", tc.path, rec.Code, rec.Body)
		}
		before = f.calls
		rec = do(t, h, "POST", path, "", bearer(memberToken))
		if rec.Code != 403 || f.calls != before {
			t.Fatalf("member triggered work: %d calls=%d", rec.Code, f.calls)
		}
		f.err = nil
	}
	f.err = errors.New("private database details")
	rec = do(t, h, "GET", Prefix+"/admin/recommendations/status", "", bearer(adminToken))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "private database") {
		t.Fatalf("status failure: %d %s", rec.Code, rec.Body)
	}
	deps.AdminRecommendations = nil
	h = newTestHandler(t, deps)
	for _, tc := range []struct{ method, path string }{{"GET", "/status"}, {"POST", "/trigger/embeddings"}, {"POST", "/trigger/taste-profiles"}, {"POST", "/trigger/cowatch"}, {"POST", "/trigger/recommendations"}, {"POST", "/embeddings/reset"}} {
		rec = do(t, h, tc.method, Prefix+"/admin/recommendations"+tc.path, "", bearer(adminToken))
		if rec.Code != 503 {
			t.Fatalf("unwired %s: %d %s", tc.path, rec.Code, rec.Body)
		}
	}
}

func TestAdminRecommendationsStatusReportsRunsConflictAndRefresh(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminRecommendations{conflict: `Embeddings were created with model "a", not "b". Reset embeddings before switching models.`}
	deps.AdminRecommendations = f
	h := newTestHandler(t, deps)

	rec := do(t, h, "GET", Prefix+"/admin/recommendations/status", "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	var status AdminRecommendationsStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.LockConflict != f.conflict {
		t.Fatalf("lock_conflict = %q", status.LockConflict)
	}
	if status.CacheRefreshedAt == nil || !status.CacheRefreshedAt.Equal(fakeAdminRecommendationsTime.Add(2*time.Hour+time.Minute)) {
		t.Fatalf("cache_refreshed_at = %v", status.CacheRefreshedAt)
	}
	emb := status.Embeddings.LastRun
	if emb == nil || emb.Status != "failed" || !emb.StartedAt.Equal(fakeAdminRecommendationsTime) || !emb.CompletedAt.Equal(fakeAdminRecommendationsTime.Add(90*time.Second)) {
		t.Fatalf("embeddings last run = %+v", emb)
	}
	// The stored error reaches the admin with credentials masked.
	if !strings.Contains(emb.Error, "embed.example") || strings.Contains(emb.Error, "hunter2") || strings.Contains(emb.Error, "secret-value") {
		t.Fatalf("embeddings last run error = %q", emb.Error)
	}
	if emb.Result["embedded"] != float64(0) {
		t.Fatalf("embeddings last run result = %v", emb.Result)
	}
	cache := status.Recommendations.LastRun
	if cache == nil || cache.Status != "completed" || cache.Error != "" || cache.Result["cached_rows"] != float64(14) {
		t.Fatalf("cache last run = %+v", cache)
	}
	if status.TasteProfiles.LastRun != nil || status.Cowatch.LastRun != nil {
		t.Fatalf("jobs with no recorded run report one: %+v %+v", status.TasteProfiles.LastRun, status.Cowatch.LastRun)
	}

	// Nothing recorded, no conflict and an empty cache: last_run and
	// cache_refreshed_at are absent and lock_conflict is empty.
	f.conflict, f.noRuns, f.noRefresh = "", true, true
	rec = do(t, h, "GET", Prefix+"/admin/recommendations/status", "", bearer(adminToken))
	body := rec.Body.String()
	if rec.Code != 200 || strings.Contains(body, "last_run") || strings.Contains(body, "cache_refreshed_at") || !strings.Contains(body, `"lock_conflict":""`) {
		t.Fatalf("empty status: %d %s", rec.Code, body)
	}

	for name, fail := range map[string]func(){
		"runs":     func() { f.runsErr = errors.New("private history detail") },
		"conflict": func() { f.conflictErr = errors.New("private history detail") },
		"refresh":  func() { f.refreshErr = errors.New("private history detail") },
	} {
		f.runsErr, f.conflictErr, f.refreshErr = nil, nil, nil
		fail()
		rec = do(t, h, "GET", Prefix+"/admin/recommendations/status", "", bearer(adminToken))
		if rec.Code != 500 || strings.Contains(rec.Body.String(), "private history") {
			t.Fatalf("%s failure: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestAdminRecommendationsResetEmbeddings(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminRecommendations{}
	deps.AdminRecommendations = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/recommendations/embeddings/reset"

	rec := do(t, h, "POST", path, "", bearer(adminToken))
	if rec.Code != 200 || f.resets != 1 {
		t.Fatalf("reset: %d %s resets=%d", rec.Code, rec.Body, f.resets)
	}
	var out AdminRecommendationEmbeddingsReset
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out != (AdminRecommendationEmbeddingsReset{Embeddings: 120, TasteProfiles: 3, TasteClusters: 9, CachedRows: 41}) {
		t.Fatalf("reset body = %+v", out)
	}

	for _, busy := range []error{
		fmt.Errorf("private worker detail: %w", recommendations.ErrJobRunning),
		fmt.Errorf("private worker detail: %w", recommendations.ErrJobRunningElsewhere),
		recommendations.ErrStaleSweepRunning,
	} {
		f.resetErr = busy
		rec = do(t, h, "POST", path, "", bearer(adminToken))
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), "Reset embeddings after it finishes") || strings.Contains(rec.Body.String(), "private worker") {
			t.Fatalf("busy reset (%v): %d %s", busy, rec.Code, rec.Body)
		}
	}
	f.resetErr = errors.New("private database detail")
	rec = do(t, h, "POST", path, "", bearer(adminToken))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "private database") {
		t.Fatalf("failed reset: %d %s", rec.Code, rec.Body)
	}

	before := f.resets
	rec = do(t, h, "POST", path, "", bearer(memberToken))
	if rec.Code != 403 || f.resets != before {
		t.Fatalf("member reset embeddings: %d resets=%d", rec.Code, f.resets)
	}
}

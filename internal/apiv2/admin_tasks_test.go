package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type fakeAdminTasks struct {
	schedule              taskmanager.Schedule
	starts, writes, reads int
	expected              int64
	mismatch              bool
	publicLinks           bool
}

func newFakeAdminTasks() *fakeAdminTasks {
	return &fakeAdminTasks{schedule: taskmanager.Schedule{Revision: 2, Triggers: []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 1000}}}}
}
func (f *fakeAdminTasks) ListTasks(bool) []taskmanager.TaskInfo {
	return []taskmanager.TaskInfo{f.GetTaskInfo("fixture")}
}
func (f *fakeAdminTasks) ListRelevantTasks(context.Context) []taskmanager.TaskInfo {
	return f.ListTasks(false)
}
func (f *fakeAdminTasks) GetTaskInfo(key string) taskmanager.TaskInfo {
	if key != "fixture" && key != "refresh_metadata" {
		return taskmanager.TaskInfo{}
	}
	return taskmanager.TaskInfo{Key: key, Name: "Fixture task", Category: taskmanager.TaskCategorySystem, State: taskmanager.TaskStateIdle, Triggers: f.schedule.Triggers}
}
func (f *fakeAdminTasks) StartTask(key string) (taskmanager.TaskInfo, error) {
	f.starts++
	info := f.GetTaskInfo(key)
	info.State = taskmanager.TaskStateRunning
	return info, nil
}
func (f *fakeAdminTasks) CancelTask(string) error { return nil }
func (f *fakeAdminTasks) GetSchedule(context.Context, string) (taskmanager.Schedule, error) {
	f.reads++
	return f.schedule, nil
}
func (f *fakeAdminTasks) UpdateSchedule(_ context.Context, _ string, revision int64, triggers []taskmanager.TriggerConfig) (taskmanager.Schedule, error) {
	f.writes++
	f.expected = revision
	if f.mismatch {
		return taskmanager.Schedule{}, &taskmanager.ScheduleConflict{Actual: 3}
	}
	f.schedule.Revision++
	f.schedule.Triggers = triggers
	return f.schedule, nil
}
func (f *fakeAdminTasks) ListPage(_ context.Context, key string, _ time.Time, before int64, limit int) ([]taskmanager.ExecutionResult, error) {
	out := []taskmanager.ExecutionResult{}
	for id := int64(3); id > 0; id-- {
		if before != 0 && id >= before {
			continue
		}
		out = append(out, taskmanager.ExecutionResult{ID: id, TaskKey: key, StartedAt: fixedTime(), CompletedAt: fixedTime(), Status: "failed", ErrorMessage: "private database endpoint", ResultData: []byte(`{"path":"private"}`)})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}
func (f *fakeAdminTasks) GetMetrics(context.Context, int) (*models.MetadataRefreshMetrics, error) {
	return &models.MetadataRefreshMetrics{}, nil
}
func (f *fakeAdminTasks) ListAdminTaskJobs(_ context.Context, _ string, _ time.Time, before string, limit int) ([]*models.AdminJob, error) {
	out := []*models.AdminJob{}
	for _, id := range []string{"c", "b", "a"} {
		if before != "" && id >= before {
			continue
		}
		job, _ := f.GetAdminTaskJob(context.Background(), id)
		out = append(out, job)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}
func (f *fakeAdminTasks) GetAdminTaskJob(_ context.Context, id string) (*models.AdminJob, error) {
	if id == "missing" {
		return nil, adminjob.ErrJobNotFound
	}
	return &models.AdminJob{ID: id, JobType: adminjob.JobTypeItemRefresh, CreatedByUserID: 1, Status: adminjob.StatusCompleted, RequestedAt: fixedTime(), RequestPayload: []byte(`{"scan_path":"private"}`), ResultPayload: []byte(`{"requested_content_id":"item","refresh_content_id":"item","detail_content_id":"detail","scan_path":"private","scan_result":{"New":3},"artwork_cache_warning":"private cache failure"}`)}, nil
}
func (f *fakeAdminTasks) AdminTaskJobDownload(context.Context, *models.AdminJob) (string, *time.Time) {
	return "", nil
}
func (f *fakeAdminTasks) AdminTaskJobPublicLinkSupported() bool { return f.publicLinks }

type fakeStorageTransitionJobs struct {
	job *models.AdminJob
}

func (f *fakeStorageTransitionJobs) ListAdminTaskJobs(context.Context, string, time.Time, string, int) ([]*models.AdminJob, error) {
	return []*models.AdminJob{f.job}, nil
}
func (f *fakeStorageTransitionJobs) GetAdminTaskJob(_ context.Context, id string) (*models.AdminJob, error) {
	if id != f.job.ID {
		return nil, adminjob.ErrJobNotFound
	}
	return f.job, nil
}
func (f *fakeStorageTransitionJobs) AdminTaskJobDownload(context.Context, *models.AdminJob) (string, *time.Time) {
	return "", nil
}
func (f *fakeStorageTransitionJobs) AdminTaskJobPublicLinkSupported() bool { return false }
func (f *fakeStorageTransitionJobs) RequestAdminTaskJobCancellation(_ context.Context, id string) (*models.AdminJob, error) {
	if id != f.job.ID {
		return nil, adminjob.ErrJobNotFound
	}
	if f.job.Status == adminjob.StatusCancelled {
		return f.job, nil
	}
	if f.job.Status != adminjob.StatusQueued && f.job.Status != adminjob.StatusRunning {
		return nil, adminjob.ErrJobNotCancellable
	}
	f.job.CancelRequested = true
	return f.job, nil
}

func TestStorageTransitionJobCancellationContract(t *testing.T) {
	job := &models.AdminJob{ID: "transition", JobType: adminjob.JobTypeStorageTransition, Status: adminjob.StatusRunning, RequestedAt: fixedTime()}
	jobs := &fakeStorageTransitionJobs{job: job}
	deps, _ := libraryDeps(t)
	deps.AdminTaskJobs = jobs
	h := newTestHandler(t, deps)
	monitor := Prefix + "/admin/jobs/transition"
	cancel := monitor + "/cancel"

	get := do(t, h, http.MethodGet, monitor, "", bearer(adminToken))
	var body AdminTaskJob
	decodeJSON(t, get.Body, &body)
	if get.Code != http.StatusOK || !body.Cancelable {
		t.Fatalf("active transition monitor: %d %s", get.Code, get.Body)
	}
	list := do(t, h, http.MethodGet, Prefix+"/admin/jobs", "", bearer(adminToken))
	var page Collection[AdminTaskJob]
	decodeJSON(t, list.Body, &page)
	if list.Code != http.StatusOK || len(page.Items) != 1 || !page.Items[0].Cancelable {
		t.Fatalf("active transition list: %d %s", list.Code, list.Body)
	}

	for range 2 {
		response := do(t, h, http.MethodPost, cancel, "", bearer(adminToken))
		decodeJSON(t, response.Body, &body)
		if response.Code != http.StatusAccepted || response.Header().Get("Retry-After") != "5" || body.State != "canceling" || !body.Cancelable {
			t.Fatalf("pending transition cancellation: %d %s", response.Code, response.Body)
		}
	}
	job.Status = adminjob.StatusCancelled
	response := do(t, h, http.MethodPost, cancel, "", bearer(adminToken))
	decodeJSON(t, response.Body, &body)
	if response.Code != http.StatusOK || response.Header().Get("Retry-After") != "" || body.State != "canceled" || body.Cancelable {
		t.Fatalf("already canceled transition: %d %s", response.Code, response.Body)
	}
	for _, status := range []string{adminjob.StatusCompleted, adminjob.StatusFailed} {
		job.Status = status
		requireProblem(t, do(t, h, http.MethodPost, cancel, "", bearer(adminToken)), TypeJobNotCancelable)
	}
}

func TestStorageTransitionJobProjectsSafeProgressAndFailure(t *testing.T) {
	job := &models.AdminJob{
		ID: "transition", JobType: adminjob.JobTypeStorageTransition, Status: adminjob.StatusRunning,
		RequestedAt: fixedTime(), ProgressCurrent: 19, Message: "copying private/key", ErrorMessage: "secret endpoint",
		ResultPayload: json.RawMessage(`{"phase":"copying","verified_objects":19,"failure_category":"secret endpoint","source_identity":"private/bucket"}`),
	}
	deps, _ := libraryDeps(t)
	deps.AdminTaskJobs = &fakeStorageTransitionJobs{job: job}
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/jobs/transition"
	for _, requestPath := range []string{path, Prefix + "/admin/jobs?kind=storage_transition"} {
		response := do(t, h, http.MethodGet, requestPath, "", bearer(adminToken))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", requestPath, response.Code, response.Body)
		}
		if strings.Contains(response.Body.String(), "private/key") || strings.Contains(response.Body.String(), "private/bucket") || strings.Contains(response.Body.String(), "secret endpoint") {
			t.Fatalf("private transition details leaked in %s: %s", requestPath, response.Body)
		}
		var projected AdminTaskJob
		if requestPath == path {
			decodeJSON(t, response.Body, &projected)
		} else {
			var page Collection[AdminTaskJob]
			decodeJSON(t, response.Body, &page)
			projected = page.Items[0]
		}
		if projected.StorageTransitionResult == nil || projected.StorageTransitionResult.Phase != "copying" || projected.StorageTransitionResult.VerifiedObjects != 19 || projected.StorageTransitionResult.FailureCategory != "" {
			t.Fatalf("safe progress projection = %+v", projected.StorageTransitionResult)
		}
	}
	job.Status = adminjob.StatusFailed
	job.ResultPayload = json.RawMessage(`{"phase":"secret endpoint","verified_objects":19,"failure_category":"target_check_failed"}`)
	response := do(t, h, http.MethodGet, path, "", bearer(adminToken))
	var failed AdminTaskJob
	decodeJSON(t, response.Body, &failed)
	if response.Code != http.StatusOK || failed.StorageTransitionResult == nil || failed.StorageTransitionResult.Phase != "failed" || failed.StorageTransitionResult.FailureCategory != "target_check_failed" || strings.Contains(response.Body.String(), "secret endpoint") {
		t.Fatalf("safe failure projection = %d %s", response.Code, response.Body)
	}
}

func TestStorageTransitionJobHidesProgressFromEarlierClaim(t *testing.T) {
	job := &models.AdminJob{
		ID: "requeued-transition", JobType: adminjob.JobTypeStorageTransition, Status: adminjob.StatusQueued,
		RequestedAt: fixedTime(), ClaimGeneration: 1, ProgressCurrent: 7, ProgressTotal: 10,
		ResultPayload: json.RawMessage(`{"phase":"copying","verified_objects":7,"claim_generation":1}`),
	}
	deps, _ := libraryDeps(t)
	deps.AdminTaskJobs = &fakeStorageTransitionJobs{job: job}
	h := newTestHandler(t, deps)
	read := func() AdminTaskJob {
		t.Helper()
		response := do(t, h, http.MethodGet, Prefix+"/admin/jobs/"+job.ID, "", bearer(adminToken))
		if response.Code != http.StatusOK {
			t.Fatalf("job response: %d %s", response.Code, response.Body)
		}
		var projected AdminTaskJob
		decodeJSON(t, response.Body, &projected)
		return projected
	}
	assertWaiting := func(projected AdminTaskJob, wantPhase string) {
		t.Helper()
		result := projected.StorageTransitionResult
		if projected.Progress != nil || result == nil || result.Phase != wantPhase || result.VerifiedObjects != 0 || result.ManualRestartRequired {
			t.Fatalf("waiting transition exposed stale progress: %+v", projected)
		}
	}

	assertWaiting(read(), "queued")
	job.Status = adminjob.StatusRunning
	job.ClaimGeneration = 2
	assertWaiting(read(), "checking_target")

	job.ProgressCurrent = 2
	job.ResultPayload = json.RawMessage(`{"phase":"copying","verified_objects":2,"claim_generation":2}`)
	current := read()
	if current.StorageTransitionResult == nil || current.StorageTransitionResult.Phase != "copying" || current.StorageTransitionResult.VerifiedObjects != 2 || current.Progress == nil || current.Progress.Current != 2 {
		t.Fatalf("current claim progress = %+v", current)
	}

	// A committed result stays in the database for restart repair. Queued
	// status does not confirm that the restarted worker has recovered it yet.
	job.Status = adminjob.StatusQueued
	job.ResultPayload = json.RawMessage(`{"phase":"restart_pending","verified_objects":7,"copied_objects":7,"manual_restart_required":true,"commit_outcome_unknown":true}`)
	assertWaiting(read(), "queued")
	job.Status = adminjob.StatusCompleted
	completed := read()
	if completed.StorageTransitionResult == nil || completed.StorageTransitionResult.Phase != "restart_pending" || completed.StorageTransitionResult.VerifiedObjects != 7 || !completed.StorageTransitionResult.ManualRestartRequired {
		t.Fatalf("recovered committed result = %+v", completed.StorageTransitionResult)
	}
}

func adminTasksTestHandler(t *testing.T, f *fakeAdminTasks) http.Handler {
	deps, _ := libraryDeps(t)
	deps.AdminTasks = f
	deps.AdminTaskHistory = f
	deps.AdminTaskMetrics = f
	deps.AdminTaskJobs = f
	return newTestHandler(t, deps)
}
func TestAdminTaskGuardAndLocalAcknowledgment(t *testing.T) {
	f := newFakeAdminTasks()
	h := adminTasksTestHandler(t, f)
	base := Prefix + "/admin/tasks/fixture"
	get := do(t, h, "GET", base+"/triggers", "", bearer(adminToken))
	if get.Code != 200 {
		t.Fatalf("%d %s", get.Code, get.Body)
	}
	tag := get.Header().Get("ETag")
	requireProblem(t, do(t, h, "PUT", base+"/triggers", `{"triggers":[]}`, bearer(adminToken)), TypePreconditionRequired)
	requireProblem(t, do(t, h, "PUT", base+"/triggers", `{"triggers":[]}`, with(bearer(adminToken), "If-Match", `"stale"`)), TypePreconditionFailed)
	f.mismatch = true
	before := f.reads
	stale := do(t, h, "PUT", base+"/triggers", `{"triggers":[]}`, with(bearer(adminToken), "If-Match", tag))
	requireProblem(t, stale, TypePreconditionFailed)
	if f.expected != 2 || f.writes != 1 || f.reads != before+1 || stale.Header().Get("ETag") == tag {
		t.Fatalf("guard re-read/retry: %+v", f)
	}
	f.mismatch = false
	saved := do(t, h, "PUT", base+"/triggers", `{"triggers":[]}`, with(bearer(adminToken), "If-Match", tag))
	if saved.Code != 200 {
		t.Fatalf("%d %s", saved.Code, saved.Body)
	}
	started := do(t, h, "POST", base+"/run", "", bearer(adminToken))
	if started.Code != 200 || started.Header().Get("Location") != "" || !strings.Contains(started.Body.String(), `"execution_scope":"process"`) {
		t.Fatalf("false job acceptance: %d %s", started.Code, started.Body)
	}
}
func TestAdminTaskAuthorizationAndValidation(t *testing.T) {
	f := newFakeAdminTasks()
	h := adminTasksTestHandler(t, f)
	base := Prefix + "/admin/tasks/fixture"
	for _, suffix := range []string{"/run", "/cancel"} {
		requireProblem(t, do(t, h, "POST", base+suffix, "", nil), TypeAuthenticationRequired)
		r := do(t, h, "POST", base+suffix, "", bearer(memberToken))
		if r.Code != 403 {
			t.Fatalf("non-admin: %d %s", r.Code, r.Body)
		}
	}
	for _, body := range []string{`{"triggers":null}`, `{"triggers":[{"type":"interval","interval_ms":0}]}`, `{"triggers":[{"type":"daily","time_of_day":"25:00"}]}`, `{"triggers":[{"type":"unknown"}]}`} {
		requireProblem(t, do(t, h, "PUT", base+"/triggers", body, with(bearer(adminToken), "If-Match", "*")), TypeValidationFailed)
	}
	if f.writes != 0 || f.starts != 0 {
		t.Fatalf("unauthorized/invalid side effects: %+v", f)
	}
}
func TestAdminTaskHistoryAndJobCursorBinding(t *testing.T) {
	f := newFakeAdminTasks()
	h := adminTasksTestHandler(t, f)
	for _, path := range []string{Prefix + "/admin/tasks/fixture/history", Prefix + "/admin/jobs"} {
		first := do(t, h, "GET", path+"?limit=2", "", bearer(adminToken))
		if first.Code != 200 {
			t.Fatalf("%d %s", first.Code, first.Body)
		}
		if strings.Contains(first.Body.String(), "private") {
			t.Fatal("private owner data escaped projection")
		}
		var page Collection[json.RawMessage]
		if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Page == nil || !page.Page.HasMore || len(page.Items) != 2 {
			t.Fatalf("missing page: %s", first.Body)
		}
		next := path + "?limit=2&cursor=" + page.Page.NextCursor
		r := do(t, h, "GET", next, "", bearer(adminToken))
		if r.Code != 200 {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
		requireProblem(t, do(t, h, "GET", next, "", bearer(otherAdminToken)), TypeInvalidCursor)
		requireProblem(t, do(t, h, "GET", next, "", with(bearer(adminToken), "X-Profile-Id", "p-primary")), TypeInvalidCursor)
		mismatch := next + "&kind=other"
		if strings.Contains(path, "/tasks/") {
			mismatch = strings.Replace(next, "/fixture/", "/refresh_metadata/", 1)
		}
		requireProblem(t, do(t, h, "GET", mismatch, "", bearer(adminToken)), TypeInvalidCursor)
	}
}

func TestAdminJobOwnerReadIsSafe(t *testing.T) {
	f := newFakeAdminTasks()
	h := adminTasksTestHandler(t, f)
	response := do(t, h, "GET", Prefix+"/admin/jobs/a", "", bearer(memberToken))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"new_files":3`) || !strings.Contains(response.Body.String(), `"artwork_cache_incomplete":true`) || !strings.Contains(response.Body.String(), `"detail_content_id":"detail"`) || strings.Contains(response.Body.String(), "private") {
		t.Fatalf("owner projection: %d %s", response.Code, response.Body)
	}
	requireProblem(t, do(t, h, "GET", Prefix+"/admin/jobs/missing", "", bearer(memberToken)), TypeNotFound)
}

func TestAdminTaskScheduleRejectsNestedNullWithoutWrites(t *testing.T) {
	for _, body := range []string{
		`{"triggers":[{"type":"weekly","time_of_day":"10:00","day_of_week":null}]}`,
		`{"triggers":[{"type":"weekly","time_of_day":"10:00","max_runtime_ms":null}]}`,
		`{"triggers":[{"type":"startup","interval_ms":null}]}`,
		`{"triggers":[{"type":"startup","time_of_day":null}]}`,
		`{"triggers":[{"type":"startup"},null]}`,
		`{"triggers":[{"type":"startup"},{"type":"weekly","time_of_day":"10:00","day_of_week":null}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			f := newFakeAdminTasks()
			h := adminTasksTestHandler(t, f)
			requireProblem(t, do(t, h, "PUT", Prefix+"/admin/tasks/fixture/triggers", body, with(bearer(adminToken), "If-Match", "*")), TypeValidationFailed)
			if f.writes != 0 || f.schedule.Revision != 2 {
				t.Fatalf("null changed schedule: %+v", f)
			}
		})
	}
	for _, body := range []string{
		`{"triggers":[{"type":"startup"}]}`,
		`{"triggers":[{"type":"startup","interval_ms":0,"time_of_day":"","day_of_week":0,"max_runtime_ms":0}]}`,
		`{"triggers":[{"type":"weekly","time_of_day":"10:00","day_of_week":0,"max_runtime_ms":0}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			f := newFakeAdminTasks()
			h := adminTasksTestHandler(t, f)
			response := do(t, h, "PUT", Prefix+"/admin/tasks/fixture/triggers", body, with(bearer(adminToken), "If-Match", "*"))
			if response.Code != 200 || f.writes != 1 {
				t.Fatalf("omission/zero rejected: %d %s", response.Code, response.Body)
			}
			if f.schedule.Triggers[0].DayOfWeek != 0 || f.schedule.Triggers[0].MaxRuntimeMs != 0 {
				t.Fatalf("zero/default changed: %+v", f.schedule)
			}
		})
	}
}

func TestTaskExecutionExposesMaintenanceStepsWithoutErrors(t *testing.T) {
	completed := time.Date(2026, 9, 23, 5, 0, 1, 0, time.UTC)
	result := taskmanager.ExecutionResult{
		TaskKey:     "database_maintenance",
		Status:      "failed",
		StartedAt:   completed.Add(-time.Second),
		CompletedAt: completed,
		ResultData: json.RawMessage(`{"steps":[` +
			`{"key":"cleanup_activity_log","name":"Cleanup Activity Log","status":"completed","result":{"deleted":2}},` +
			`{"key":"cleanup_policy_decision_log","name":"Cleanup Policy Decision Log","status":"failed","error":"partition exists"}]}`),
	}

	got := taskExecutionOf(result)
	want := []AdminTaskStepResult{
		{Key: "cleanup_activity_log", Name: "Cleanup Activity Log", Status: "completed"},
		{Key: "cleanup_policy_decision_log", Name: "Cleanup Policy Decision Log", Status: "failed"},
	}
	if !reflect.DeepEqual(got.Steps, want) {
		t.Fatalf("Steps = %+v, want %+v", got.Steps, want)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "partition exists") {
		t.Fatalf("step error text leaked into the v2 execution: %s", body)
	}

	other := taskExecutionOf(taskmanager.ExecutionResult{TaskKey: "match_media", ResultData: result.ResultData})
	if other.Steps != nil {
		t.Fatalf("Steps for another task = %+v, want none", other.Steps)
	}
}

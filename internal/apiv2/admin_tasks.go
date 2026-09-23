package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type AdminTaskService interface {
	ListTasks(bool) []taskmanager.TaskInfo
	ListRelevantTasks(context.Context) []taskmanager.TaskInfo
	GetTaskInfo(string) taskmanager.TaskInfo
	StartTask(string) (taskmanager.TaskInfo, error)
	CancelTask(string) error
	GetSchedule(context.Context, string) (taskmanager.Schedule, error)
	UpdateSchedule(context.Context, string, int64, []taskmanager.TriggerConfig) (taskmanager.Schedule, error)
}
type AdminTaskHistoryService interface {
	ListPage(context.Context, string, time.Time, int64, int) ([]taskmanager.ExecutionResult, error)
}

type AdminTaskTrigger struct {
	Type         string `json:"type" enum:"interval,daily,weekly,startup"`
	IntervalMs   int64  `json:"interval_ms,omitzero" minimum:"0" maximum:"9223372036854"`
	TimeOfDay    string `json:"time_of_day,omitempty"`
	DayOfWeek    int    `json:"day_of_week,omitzero" minimum:"0" maximum:"6"`
	MaxRuntimeMs int64  `json:"max_runtime_ms,omitzero" minimum:"0" maximum:"9223372036854"`
}
type AdminTaskExecution struct {
	AdminTaskExecutionSummary
	ID ID `json:"id"`
}

type AdminTaskMarkerResult struct {
	Submitted         int `json:"submitted"`
	Skipped           int `json:"skipped"`
	Failed            int `json:"failed"`
	RetryAfterSeconds int `json:"retry_after_seconds"`
}

// AdminTaskStepResult reports one step of a task that runs several steps.
// Error details stay in server logs, like other task failures.
type AdminTaskStepResult struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Status string `json:"status" enum:"completed,failed"`
}
type AdminTaskExecutionSummary struct {
	ResultData   *AdminTaskMarkerResult `json:"result_data,omitempty"`
	Steps        []AdminTaskStepResult  `json:"steps,omitempty" doc:"Per-step outcomes for tasks that run several steps, such as database_maintenance."`
	TaskKey      string                 `json:"task_key"`
	StartedAt    Instant                `json:"started_at"`
	CompletedAt  Instant                `json:"completed_at"`
	Status       string                 `json:"status" enum:"completed,failed,cancelled"` //nolint:misspell // Existing task state vocabulary.
	DurationMs   int64                  `json:"duration_ms"`
	ErrorMessage string                 `json:"error_message,omitempty"`
}
type AdminTask struct {
	Key            string                     `json:"key"`
	Name           string                     `json:"name"`
	Description    string                     `json:"description"`
	Category       string                     `json:"category" enum:"library,metadata,system"`
	State          string                     `json:"state" enum:"idle,running,cancelling"` //nolint:misspell // Existing task state vocabulary.
	Progress       float64                    `json:"progress"`
	ManualOnly     bool                       `json:"manual_only"`
	LastExecution  *AdminTaskExecutionSummary `json:"last_execution,omitempty"`
	Triggers       []AdminTaskTrigger         `json:"triggers"`
	NextRunAt      *Instant                   `json:"next_run_at,omitempty"`
	ExecutionScope string                     `json:"execution_scope" enum:"process" doc:"Live execution and cancellation concern this server process; no durable dispatch or cluster-wide status is promised."`
}
type AdminTaskSchedule struct {
	TaskKey  string             `json:"task_key"`
	Triggers []AdminTaskTrigger `json:"triggers"`
}
type AdminTasksInput struct {
	IncludeHidden bool `query:"include_hidden"`
}
type AdminTaskInput struct {
	Key string `path:"key"`
}
type AdminTaskOutput struct{ Body AdminTask }
type AdminTasksOutput struct{ Body Collection[AdminTask] }
type AdminTaskScheduleOutput struct {
	ETag string `header:"ETag"`
	Body AdminTaskSchedule
}
type AdminTaskScheduleInput struct {
	// RawBody preserves explicit nulls that Huma otherwise treats as omissions.
	RawBody     []byte
	Key         string `path:"key"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        struct {
		Triggers []AdminTaskTrigger `json:"triggers" maxItems:"32"`
	}
}
type AdminTaskHistoryInput struct {
	Key    string `path:"key"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"200"`
}
type AdminTaskHistoryOutput struct {
	Body Collection[AdminTaskExecution]
}
type adminTaskHistoryPosition struct {
	Completed time.Time `json:"completed"`
	ID        int64     `json:"id"`
}

func taskTriggersOf(configs []taskmanager.TriggerConfig) []AdminTaskTrigger {
	out := make([]AdminTaskTrigger, 0, len(configs))
	for _, c := range configs {
		out = append(out, AdminTaskTrigger{Type: string(c.Type), IntervalMs: c.IntervalMs, TimeOfDay: c.TimeOfDay, DayOfWeek: c.DayOfWeek, MaxRuntimeMs: c.MaxRuntimeMs})
	}
	return out
}
func taskExecutionOf(e taskmanager.ExecutionResult) AdminTaskExecution {
	out := AdminTaskExecution{ID: IDFromInt(e.ID), AdminTaskExecutionSummary: AdminTaskExecutionSummary{TaskKey: e.TaskKey, StartedAt: NewInstant(e.StartedAt), CompletedAt: NewInstant(e.CompletedAt), Status: e.Status, DurationMs: e.DurationMs}}
	if e.TaskKey == "contribute_markers" && len(e.ResultData) > 0 {
		var result AdminTaskMarkerResult
		if json.Unmarshal(e.ResultData, &result) == nil {
			out.ResultData = &result
		}
	}
	if e.TaskKey == "database_maintenance" && len(e.ResultData) > 0 {
		var result struct {
			Steps []AdminTaskStepResult `json:"steps"`
		}
		if json.Unmarshal(e.ResultData, &result) == nil {
			out.Steps = result.Steps
		}
	}
	if e.Status == adminjob.StatusFailed {
		out.ErrorMessage = "Task failed. Inspect administrator diagnostics for details."
	}
	return out
}
func taskOf(t taskmanager.TaskInfo) AdminTask {
	out := AdminTask{Key: t.Key, Name: t.Name, Description: t.Description, Category: string(t.Category), State: string(t.State), Progress: t.Progress, ManualOnly: t.ManualOnly, Triggers: taskTriggersOf(t.Triggers), NextRunAt: instantPtr(t.NextRunAt), ExecutionScope: "process"}
	if t.LastExecution != nil {
		out.LastExecution = new(taskExecutionOf(*t.LastExecution).AdminTaskExecutionSummary)
	}
	return out
}
func adminTaskOp(method, path, id, summary string) Operation {
	op := Operation{Operation: humaOp(method, Prefix+"/admin/tasks"+path, id, "admin-tasks", summary), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: isMutatingMethod(method)}
	if method != http.MethodGet {
		op.RetrySafety = RetrySafetyNonRetryable
	}
	return op
}
func registerAdminTasks(reg *Registry) {
	Register(reg, adminTaskOp("GET", "/{key}/metrics", "getAdminTaskMetrics", "Read bounded refresh-debt metrics."), reg.getAdminTaskMetrics)
	Register(reg, adminTaskOp("GET", "", "listAdminTasks", "List the finite task registry and process-local runtime state."), reg.listAdminTasks)
	Register(reg, adminTaskOp("GET", "/{key}", "getAdminTask", "Read task state on this process."), reg.getAdminTask)
	Register(reg, adminTaskOp("POST", "/{key}/run", "runAdminTask", "Start a task on this process. This acknowledgment does not persist work intent."), reg.runAdminTask)
	Register(reg, adminTaskOp("POST", "/{key}/cancel", "cancelAdminTask", "Request cancellation on this process; effects already performed remain."), reg.cancelAdminTask)
	Register(reg, adminTaskOp("GET", "/{key}/triggers", "getAdminTaskSchedule", "Read persisted schedule configuration and its canonical validator."), reg.getAdminTaskSchedule)
	update := adminTaskOp("PUT", "/{key}/triggers", "updateAdminTaskSchedule", "Replace a captured schedule and apply it on this process. Other processes reload on restart.")
	update.Guarded = true
	Register(reg, update, reg.updateAdminTaskSchedule)
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, adminTaskOp("GET", "/{key}/history", "listAdminTaskHistory", "Read bounded persisted execution history."), func(ctx context.Context, in *AdminTaskHistoryInput) (*AdminTaskHistoryOutput, error) {
		return reg.listAdminTaskHistory(ctx, cursors, in)
	})
}
func (reg *Registry) taskInfo(key string) (taskmanager.TaskInfo, error) {
	if reg.deps.AdminTasks == nil {
		return taskmanager.TaskInfo{}, unavailable("admin tasks")
	}
	info := reg.deps.AdminTasks.GetTaskInfo(key)
	if info.Key == "" {
		return info, NewProblem(TypeNotFound, "Task not found")
	}
	return info, nil
}
func taskProblem(err error) error {
	switch {
	case errors.Is(err, taskmanager.ErrTaskNotFound):
		return NewProblem(TypeNotFound, "Task not found")
	case errors.Is(err, taskmanager.ErrTaskAlreadyRunning):
		return NewProblem(TypeConflict, "Task is already running on this process")
	case errors.Is(err, taskmanager.ErrTaskNotRunning):
		return NewProblem(TypeConflict, "Task is not running on this process")
	case errors.Is(err, taskmanager.ErrTaskManualOnly):
		return NewProblem(TypeValidationFailed, "Manual-only tasks do not accept schedules")
	default:
		return serviceProblem(err)
	}
}
func (reg *Registry) listAdminTasks(ctx context.Context, in *AdminTasksInput) (*AdminTasksOutput, error) {
	if reg.deps.AdminTasks == nil {
		return nil, unavailable("admin tasks")
	}
	items := []AdminTask{}
	var listed []taskmanager.TaskInfo
	if in.IncludeHidden {
		listed = reg.deps.AdminTasks.ListTasks(true)
	} else {
		listed = reg.deps.AdminTasks.ListRelevantTasks(ctx)
	}
	for _, t := range listed {
		items = append(items, taskOf(t))
	}
	return &AdminTasksOutput{Body: Collection[AdminTask]{Items: items}}, nil
}
func (reg *Registry) getAdminTask(_ context.Context, in *AdminTaskInput) (*AdminTaskOutput, error) {
	t, err := reg.taskInfo(in.Key)
	if err != nil {
		return nil, err
	}
	return &AdminTaskOutput{Body: taskOf(t)}, nil
}
func (reg *Registry) runAdminTask(_ context.Context, in *AdminTaskInput) (*AdminTaskOutput, error) {
	if _, err := reg.taskInfo(in.Key); err != nil {
		return nil, err
	}
	t, err := reg.deps.AdminTasks.StartTask(in.Key)
	if err != nil {
		return nil, taskProblem(err)
	}
	return &AdminTaskOutput{Body: taskOf(t)}, nil
}
func (reg *Registry) cancelAdminTask(_ context.Context, in *AdminTaskInput) (*AdminTaskOutput, error) {
	if _, err := reg.taskInfo(in.Key); err != nil {
		return nil, err
	}
	if err := reg.deps.AdminTasks.CancelTask(in.Key); err != nil {
		return nil, taskProblem(err)
	}
	return &AdminTaskOutput{Body: taskOf(reg.deps.AdminTasks.GetTaskInfo(in.Key))}, nil
}
func taskScheduleTag(ctx context.Context, key string, revision int64) EntityTag {
	return collectionEditorTag(ctx, "admin-task-schedule", key, revision)
}
func (reg *Registry) getAdminTaskSchedule(ctx context.Context, in *AdminTaskInput) (*AdminTaskScheduleOutput, error) {
	if _, err := reg.taskInfo(in.Key); err != nil {
		return nil, err
	}
	s, err := reg.deps.AdminTasks.GetSchedule(ctx, in.Key)
	if err != nil {
		return nil, taskProblem(err)
	}
	return &AdminTaskScheduleOutput{ETag: taskScheduleTag(ctx, in.Key, s.Revision).String(), Body: AdminTaskSchedule{TaskKey: in.Key, Triggers: taskTriggersOf(s.Triggers)}}, nil
}
func (reg *Registry) updateAdminTaskSchedule(ctx context.Context, in *AdminTaskScheduleInput) (*AdminTaskScheduleOutput, error) {
	if _, err := reg.taskInfo(in.Key); err != nil {
		return nil, err
	}
	s, err := reg.deps.AdminTasks.GetSchedule(ctx, in.Key)
	if err != nil {
		return nil, taskProblem(err)
	}
	tag := taskScheduleTag(ctx, in.Key, s.Revision)
	if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
		return nil, p
	}
	expected := s.Revision
	if strings.TrimSpace(in.IfMatch) == "*" {
		expected = -1
	}
	if in.Body.Triggers == nil {
		return nil, NewProblem(TypeValidationFailed, "Triggers must be an array")
	}
	var raw struct {
		Triggers []map[string]json.RawMessage `json:"triggers"`
	}
	if err := json.Unmarshal(in.RawBody, &raw); err != nil {
		return nil, NewProblem(TypeValidationFailed, "Invalid schedule body")
	}
	for _, trigger := range raw.Triggers {
		if trigger == nil {
			return nil, NewProblem(TypeValidationFailed, "Each trigger must be an object")
		}
		for _, value := range trigger {
			if bytes.Equal(bytes.TrimSpace(value), jsonNull) {
				return nil, NewProblem(TypeValidationFailed, "Trigger fields cannot be null; omit optional fields to use their defaults")
			}
		}
	}
	configs := make([]taskmanager.TriggerConfig, 0, len(in.Body.Triggers))
	for _, c := range in.Body.Triggers {
		if c.Type == string(taskmanager.TriggerTypeInterval) && c.IntervalMs < 1 {
			return nil, NewProblem(TypeValidationFailed, "Interval must be positive")
		}
		if c.Type == string(taskmanager.TriggerTypeDaily) || c.Type == string(taskmanager.TriggerTypeWeekly) {
			if _, err := time.Parse("15:04", c.TimeOfDay); err != nil {
				return nil, NewProblem(TypeValidationFailed, "Schedule time must be HH:MM")
			}
		}
		configs = append(configs, taskmanager.TriggerConfig{Type: taskmanager.TriggerType(c.Type), IntervalMs: c.IntervalMs, TimeOfDay: c.TimeOfDay, DayOfWeek: c.DayOfWeek, MaxRuntimeMs: c.MaxRuntimeMs})
	}
	saved, err := reg.deps.AdminTasks.UpdateSchedule(ctx, in.Key, expected, configs)
	if mismatch, ok := errors.AsType[*taskmanager.ScheduleConflict](err); ok {
		return nil, StaleVersionProblem(taskScheduleTag(ctx, in.Key, mismatch.Actual))
	}
	if err != nil {
		return nil, taskProblem(err)
	}
	return &AdminTaskScheduleOutput{ETag: taskScheduleTag(ctx, in.Key, saved.Revision).String(), Body: AdminTaskSchedule{TaskKey: in.Key, Triggers: taskTriggersOf(saved.Triggers)}}, nil
}
func (reg *Registry) listAdminTaskHistory(ctx context.Context, cursors *Cursors, in *AdminTaskHistoryInput) (*AdminTaskHistoryOutput, error) {
	if _, err := reg.taskInfo(in.Key); err != nil {
		return nil, err
	}
	if reg.deps.AdminTaskHistory == nil {
		return nil, unavailable("task history")
	}
	scope := CursorScope{OperationID: "listAdminTaskHistory", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: in.Key, Sort: "-completed_at", Tiebreaker: "-id"}
	var position adminTaskHistoryPosition
	if in.Cursor != "" {
		if err := cursors.Decode(scope, in.Cursor, &position); err != nil {
			return nil, err
		}
	}
	rows, err := reg.deps.AdminTaskHistory.ListPage(ctx, in.Key, position.Completed, position.ID, in.Limit+1)
	if err != nil {
		return nil, serviceProblem(err)
	}
	hasMore := len(rows) > in.Limit
	if hasMore {
		rows = rows[:in.Limit]
	}
	next := ""
	items := make([]AdminTaskExecution, 0, len(rows))
	for _, e := range rows {
		items = append(items, taskExecutionOf(e))
	}
	if hasMore {
		last := rows[len(rows)-1]
		next, err = cursors.Encode(scope, adminTaskHistoryPosition{Completed: last.CompletedAt, ID: last.ID})
		if err != nil {
			return nil, err
		}
	}
	return &AdminTaskHistoryOutput{Body: Paginated(items, next)}, nil
}

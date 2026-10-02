package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

const refreshMetadataTaskKey = "refresh_metadata"

// TaskManagerAPI is the subset of TaskManager used by the handler.
type TaskManagerAPI interface {
	ListTasks(includeHidden bool) []taskmanager.TaskInfo
	GetTaskInfo(key string) taskmanager.TaskInfo
	RunTask(ctx context.Context, key string) error
	CancelTask(key string) error
	UpdateTriggers(key string, triggers []taskmanager.TriggerConfig) error
}

// TaskHistoryLister lists execution history for a task.
type TaskHistoryLister interface {
	List(ctx context.Context, taskKey string, limit int) ([]taskmanager.ExecutionResult, error)
}

type TaskMetricsProvider interface {
	GetRefreshMetadataMetrics(ctx context.Context) (any, error)
}

// TaskHandler handles REST API requests for /api/v1/admin/tasks.
type TaskHandler struct {
	mgr     TaskManagerAPI
	history TaskHistoryLister
	metrics TaskMetricsProvider
}

// NewTaskHandler creates a new TaskHandler.
func NewTaskHandler(mgr TaskManagerAPI, history TaskHistoryLister, metrics TaskMetricsProvider) *TaskHandler {
	return &TaskHandler{mgr: mgr, history: history, metrics: metrics}
}

// HandleListTasks handles GET /api/v1/admin/tasks
func (h *TaskHandler) HandleListTasks(w http.ResponseWriter, r *http.Request) {
	includeHidden := r.URL.Query().Get("include_hidden") == "true"
	tasks := h.mgr.ListTasks(includeHidden)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tasks)
}

// HandleGetTask handles GET /api/v1/admin/tasks/{key}
func (h *TaskHandler) HandleGetTask(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	info := h.mgr.GetTaskInfo(key)
	if info.Key == "" {
		http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// HandleRunTask handles POST /api/v1/admin/tasks/{key}/run
func (h *TaskHandler) HandleRunTask(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	info := h.mgr.GetTaskInfo(key)
	if info.Key == "" {
		http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
		return
	}

	if info.State == taskmanager.TaskStateRunning || info.State == taskmanager.TaskStateCancelling {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "task is already running"})
		return
	}

	go h.mgr.RunTask(context.Background(), key)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

// HandleCancelTask handles POST /api/v1/admin/tasks/{key}/cancel
func (h *TaskHandler) HandleCancelTask(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	err := h.mgr.CancelTask(key)
	if err != nil {
		if errors.Is(err, taskmanager.ErrTaskNotFound) {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}
		if errors.Is(err, taskmanager.ErrTaskNotRunning) {
			http.Error(w, `{"error":"task is not running"}`, http.StatusConflict)
			return
		}
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "cancelling"})
}

// HandleUpdateTriggers handles PUT /api/v1/admin/tasks/{key}/triggers
func (h *TaskHandler) HandleUpdateTriggers(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	var triggers []taskmanager.TriggerConfig
	if err := json.NewDecoder(r.Body).Decode(&triggers); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if err := h.mgr.UpdateTriggers(key, triggers); err != nil {
		if errors.Is(err, taskmanager.ErrTaskNotFound) {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}
		if errors.Is(err, taskmanager.ErrTaskManualOnly) {
			http.Error(w, `{"error":"manual-only task does not accept scheduled triggers"}`, http.StatusBadRequest)
			return
		}
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.mgr.GetTaskInfo(key))
}

// HandleGetHistory handles GET /api/v1/admin/tasks/{key}/history
func (h *TaskHandler) HandleGetHistory(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	results, err := h.history.List(r.Context(), key, limit)
	if err != nil {
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	if results == nil {
		results = []taskmanager.ExecutionResult{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func (h *TaskHandler) HandleGetMetrics(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key != refreshMetadataTaskKey || h.metrics == nil {
		http.Error(w, `{"error":"task metrics not found"}`, http.StatusNotFound)
		return
	}

	metrics, err := h.metrics.GetRefreshMetadataMetrics(r.Context())
	if err != nil {
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// TaskKey is the backup task's key.
const TaskKey = "backup"

// Task runs the nightly backup and "Back up now". Every API server registers
// it and fires the same daily trigger; the subsystem lock lets one of them
// back up, and the others record a skip.
type Task struct {
	service *Service
}

// NewTask wraps service as a taskmanager task.
func NewTask(service *Service) *Task { return &Task{service: service} }

func (t *Task) Key() string  { return TaskKey }
func (t *Task) Name() string { return "Back Up Server" }
func (t *Task) Description() string {
	return "Backs up the database and uploaded files to the backup destination, then deletes backups beyond the configured count"
}
func (t *Task) Category() taskmanager.TaskCategory { return taskmanager.TaskCategorySystem }
func (t *Task) IsHidden() bool                     { return false }

// DefaultTriggers runs daily at backup.time. SyncSchedule keeps a stored
// schedule on the setting afterwards.
func (t *Task) DefaultTriggers() []taskmanager.TriggerConfig {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return scheduleFor(t.service.backupTime(ctx))
}

// ShouldRun gates scheduled runs on backup.enabled. "Back up now" runs either
// way.
func (t *Task) ShouldRun(ctx context.Context) (bool, error) {
	if t.service.opts.Settings == nil {
		return true, nil
	}
	raw, err := t.service.opts.Settings.Get(ctx, config.BackupEnabledSettingKey)
	if err != nil {
		return false, err
	}
	return config.AdminSettingEnabled(config.BackupEnabledSettingKey, raw), nil
}

func (t *Task) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	result, err := t.service.RunTask(ctx, progress.Report)
	data, _ := json.Marshal(result)
	progress.SetResultData(data)
	if err != nil {
		return err
	}
	switch {
	case result.Skipped != "":
		progress.Report(100, strings.ToUpper(result.Skipped[:1])+result.Skipped[1:])
	case result.Status == StatusCompleteWithWarnings:
		progress.Report(100, fmt.Sprintf("Backed up %s; %d referenced files were missing", result.ID, len(result.MissingBlobs)))
	}
	return nil
}

func (s *Service) backupTime(ctx context.Context) string {
	if s.opts.Settings == nil {
		return config.DefaultBackupTime
	}
	raw, err := s.opts.Settings.Get(ctx, config.BackupTimeSettingKey)
	if err != nil {
		return config.DefaultBackupTime
	}
	hour, minute, ok := config.ParseBackupTime(raw)
	if !ok {
		return config.DefaultBackupTime
	}
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

func scheduleFor(at string) []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: at}}
}

// ScheduleManager is the part of the task manager SyncSchedule needs.
type ScheduleManager interface {
	GetTaskInfo(key string) taskmanager.TaskInfo
	UpdateTriggers(key string, triggers []taskmanager.TriggerConfig) error
}

// SyncSchedule points the backup task's trigger at backup.time. The setting
// is the one place the time is edited; main calls this at startup and on
// every settings change, on each server.
func (s *Service) SyncSchedule(ctx context.Context, mgr ScheduleManager) error {
	want := scheduleFor(s.backupTime(ctx))
	if slices.Equal(mgr.GetTaskInfo(TaskKey).Triggers, want) {
		return nil
	}
	return mgr.UpdateTriggers(TaskKey, want)
}

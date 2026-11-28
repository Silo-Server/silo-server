package backup

import (
	"context"
	"encoding/json"
	"errors"
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
	at, err := s.readBackupTime(ctx)
	if err != nil {
		return config.DefaultBackupTime
	}
	return at
}

// readBackupTime is backupTime, except that a setting it cannot read is an
// error rather than the default. An unset or invalid backup.time is still
// the default.
func (s *Service) readBackupTime(ctx context.Context) (string, error) {
	if s.opts.Settings == nil {
		return config.DefaultBackupTime, nil
	}
	raw, err := s.opts.Settings.Get(ctx, config.BackupTimeSettingKey)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", config.BackupTimeSettingKey, err)
	}
	hour, minute, ok := config.ParseBackupTime(raw)
	if !ok {
		return config.DefaultBackupTime, nil
	}
	return fmt.Sprintf("%02d:%02d", hour, minute), nil
}

// nightlySlot is the scheduled time a nightly run at now belongs to: the
// latest backup.time (HH:MM) at or before now, on the wall clock of loc,
// the zone the daily trigger fires in (Options.Location, time.Local
// unless set).
func nightlySlot(now time.Time, at string, loc *time.Location) time.Time {
	hour, minute, ok := config.ParseBackupTime(at)
	if !ok {
		hour, minute, _ = config.ParseBackupTime(config.DefaultBackupTime)
	}
	local := now.In(loc)
	slot := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if slot.After(local) {
		slot = slot.AddDate(0, 0, -1)
	}
	return slot
}

// slotBackedUp reports whether a scheduled backup already succeeded for
// slot, on any server: a recorded slot after this server's previous one,
// the same wall-clock time a calendar day earlier (AddDate, so a daylight
// saving change, when consecutive slots are 23 or 25 hours apart, still
// backs up each night). Servers in different time zones compute different
// slots for one night, and another zone's slot for the same night lands
// after this server's previous one, so the later server skips; a mismatch,
// or a backup.time moved earlier, at worst skips one night's run and never
// takes a second copy. A failed nightly records no slot, so the next
// server still tries.
func (s *Service) slotBackedUp(ctx context.Context, slot time.Time) (bool, error) {
	if s.opts.Status == nil {
		return false, nil
	}
	rec, err := s.opts.Status.Read(ctx)
	if err != nil {
		return false, fmt.Errorf("reading the latest nightly backup: %w", err)
	}
	return slotDone(rec.LastNightlySlot, slot), nil
}

// slotDone is slotBackedUp's rule for the latest recorded slot.
func slotDone(last *time.Time, slot time.Time) bool {
	return last != nil && last.After(slot.AddDate(0, 0, -1))
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

// lockScheduled takes the lock for a run the task manager started: a
// manual one, or the nightly one, which skips when the lock is held or
// another server already backed up its slot. A nil run is a skip or an
// error; a run carrying err holds the lock only so failAdmission can report
// it.
func (s *Service) lockScheduled(ctx context.Context) (*pendingRun, RunResult, error) {
	kind := KindNightly
	if taskmanager.StartedManually(ctx) {
		kind = KindManual
	}
	held, err := s.opts.Locker.TryLock(ctx)
	if errors.Is(err, ErrBusy) {
		return nil, RunResult{Skipped: SkippedBusy}, nil
	}
	if err != nil {
		return nil, RunResult{}, err
	}
	if kind != KindNightly {
		return &pendingRun{held: held, kind: kind}, RunResult{}, nil
	}
	// Every API server fires the trigger; under the lock, the first to finish
	// the slot's backup leaves the others nothing to do. The run records this
	// slot, so moving backup.time while it runs cannot leave tonight open for
	// a second copy. A backup.time it cannot read fails the run
	// (failAdmission): the default time would name another slot, which could
	// skip tonight or leave it open for another server.
	at, readErr := s.readBackupTime(ctx)
	if readErr != nil {
		return unadmittedRun(held, kind, readErr)
	}
	slot := nightlySlot(s.opts.Now(), at, s.opts.Location)
	done, err := s.slotBackedUp(ctx, slot)
	if err != nil || done {
		held.Release(context.WithoutCancel(ctx))
		if err != nil {
			return nil, RunResult{}, err
		}
		return nil, RunResult{Skipped: SkippedAlreadyDone}, nil
	}
	return &pendingRun{held: held, kind: kind, slot: slot}, RunResult{}, nil
}

// unadmittedRun is a run that holds the lock only to be reported failed with
// cause (failAdmission).
func unadmittedRun(held Held, kind Kind, cause error) (*pendingRun, RunResult, error) {
	return &pendingRun{held: held, kind: kind, err: cause}, RunResult{}, nil
}

// failAdmission ends a scheduled run lockScheduled could not admit, such as
// one whose backup.time could not be read. It is published as a failed run in
// backup status, with no nightly slot, so neither a later run that night nor
// another server takes it as done, and its error fails the task.
func (s *Service) failAdmission(ctx context.Context, run *pendingRun) (RunResult, error) {
	defer run.held.Release(context.WithoutCancel(ctx))
	started := s.opts.Now()
	id := FolderName(started, run.kind)
	op := s.beginOperation(ctx, run.held, ActiveOperation{Operation: OperationBackup, BackupID: id, Kind: run.kind, StartedAt: started})
	op.end(ctx, &LastRun{ID: id, Kind: run.kind, StartedAt: started, FinishedAt: s.opts.Now(), Status: RunFailed, Error: run.err.Error()})
	return RunResult{}, run.err
}

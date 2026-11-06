package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type scheduledTaskClient interface {
	Run(ctx context.Context, req *pluginv1.RunScheduledTaskRequest) (*pluginv1.RunScheduledTaskResponse, error)
}

type scheduledTaskResolver interface {
	ScheduledTaskClient(ctx context.Context, installationID int, capabilityID string) (scheduledTaskClient, error)
}

type taskInstallationStore interface {
	ListEnabled(ctx context.Context) ([]*Installation, error)
	ListCapabilities(ctx context.Context, installationID int) ([]*Capability, error)
}

type taskBindingStore interface {
	ListTaskBindings(ctx context.Context) ([]*TaskBinding, error)
}

// taskScheduler is the part of the task manager the registry drives.
type taskScheduler interface {
	RegisterLive(task taskmanager.Task) error
	Unregister(key string)
	ReloadSchedule(ctx context.Context, key string) error
}

// TaskSyncInterval is how often an API server re-syncs plugin tasks without
// an event, so a missed plugins_changed announcement costs at most one
// interval. TaskSyncRetryWait is the wait before retrying a failed sync.
const (
	TaskSyncInterval  = DefaultLifecyclePollInterval
	TaskSyncRetryWait = 10 * time.Second
)

// TaskRegistry keeps the task manager's plugin tasks in step with the
// database: one task per scheduled_task.v1 capability of an enabled
// installation whose binding is not disabled. Sync registers new tasks,
// unregisters gone ones, updates names and default schedules in place and
// installs schedules another server saved. It never writes a schedule. Every
// API server runs its own registry, so a change made on one server reaches
// the others through cache.EventPluginTasksChanged or
// cache.EventPluginsChanged and the periodic resync (RunSyncs).
type TaskRegistry struct {
	installations taskInstallationStore
	bindings      taskBindingStore
	host          scheduledTaskResolver
	scheduler     taskScheduler

	mu         sync.Mutex
	registered map[string]*pluginTask
	// wake asks RunSyncs for a sync; one pending request is enough.
	wake chan struct{}
}

func NewTaskRegistry(
	installations taskInstallationStore,
	bindings taskBindingStore,
	host scheduledTaskResolver,
	scheduler taskScheduler,
) *TaskRegistry {
	return &TaskRegistry{
		installations: installations,
		bindings:      bindings,
		host:          host,
		scheduler:     scheduler,
		registered:    make(map[string]*pluginTask),
		wake:          make(chan struct{}, 1),
	}
}

func NewTaskRegistryWithTypedResolver(
	installations taskInstallationStore,
	bindings taskBindingStore,
	resolver interface {
		ScheduledTaskClient(ctx context.Context, installationID int, capabilityID string) (*pluginhost.ScheduledTaskClient, error)
	},
	scheduler taskScheduler,
) *TaskRegistry {
	return NewTaskRegistry(installations, bindings, scheduledTaskResolverFunc(func(
		ctx context.Context,
		installationID int,
		capabilityID string,
	) (scheduledTaskClient, error) {
		return resolver.ScheduledTaskClient(ctx, installationID, capabilityID)
	}), scheduler)
}

// Sync reconciles the task manager with the stored installations and
// bindings. A read failure changes nothing, so a transient database error
// never unregisters a task.
func (r *TaskRegistry) Sync(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	specs, err := r.scan(ctx)
	if err != nil {
		return err
	}

	var errs []error
	wanted := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		wanted[spec.key] = struct{}{}
		if task, ok := r.registered[spec.key]; ok {
			task.update(spec)
			// A default changed by a binding write or plugin update, or a
			// schedule saved on the task page of any server, applies here.
			if err := r.scheduler.ReloadSchedule(ctx, spec.key); err != nil {
				errs = append(errs, fmt.Errorf("reload plugin task %s schedule: %w", spec.key, err))
			}
			continue
		}
		// Registering loads a saved schedule, else the defaults the scan
		// just read. A task whose schedule cannot be read is left
		// unregistered, so the retry after the failed sync registers it.
		task := &pluginTask{installationID: spec.installationID, capabilityID: spec.capabilityID, resolver: r.host}
		task.update(spec)
		if err := r.scheduler.RegisterLive(task); err != nil {
			errs = append(errs, fmt.Errorf("register plugin task %s: %w", spec.key, err))
			continue
		}
		r.registered[spec.key] = task
	}
	for key := range r.registered {
		if _, ok := wanted[key]; !ok {
			r.scheduler.Unregister(key)
			delete(r.registered, key)
		}
	}
	return errors.Join(errs...)
}

// RequestSync asks RunSyncs for a sync without waiting for it. Requests made
// while one is pending or running collapse into one more sync.
func (r *TaskRegistry) RequestSync() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// SyncNow syncs before returning, for a change made on this server, and
// leaves a failed sync to RunSyncs.
func (r *TaskRegistry) SyncNow(ctx context.Context) {
	if err := r.Sync(ctx); err != nil {
		slog.WarnContext(ctx, "sync plugin tasks failed; retrying", "component", "plugins", "error", err)
		r.RequestSync()
	}
}

// RunSyncs syncs on every RequestSync and every interval until ctx ends, so
// a server that missed a change announcement, or whose sync failed, catches
// up without a restart. A failed sync is retried after retryWait.
func (r *TaskRegistry) RunSyncs(ctx context.Context, interval, retryWait time.Duration) {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-timer.C:
		}
		next := interval
		if err := r.Sync(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.WarnContext(ctx, "sync plugin tasks failed; retrying", "component", "plugins", "retry_in", retryWait, "error", err)
			next = retryWait
		}
		timer.Reset(next)
	}
}

// pluginTaskSpec is one task the database says should be registered.
type pluginTaskSpec struct {
	key            string
	installationID int
	capabilityID   string
	name           string
	description    string
	triggers       []taskmanager.TriggerConfig
}

// scan lists the scheduled task capabilities of enabled installations whose
// binding is not disabled.
func (r *TaskRegistry) scan(ctx context.Context) ([]pluginTaskSpec, error) {
	installations, err := r.installations.ListEnabled(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled plugin installations: %w", err)
	}
	bindingRows, err := r.bindings.ListTaskBindings(ctx)
	if err != nil {
		return nil, fmt.Errorf("list plugin task bindings: %w", err)
	}
	bindings := make(map[string]*TaskBinding, len(bindingRows))
	for _, binding := range bindingRows {
		bindings[pluginTaskKey(binding.InstallationID, binding.CapabilityID)] = binding
	}

	var specs []pluginTaskSpec
	for _, installation := range installations {
		// Builtin installations expose no scheduled tasks and cannot be
		// launched; defense-in-depth alongside the capability-type filter.
		if installation.IsBuiltin() {
			continue
		}
		capabilities, err := r.installations.ListCapabilities(ctx, installation.ID)
		if err != nil {
			return nil, fmt.Errorf("list plugin capabilities for installation %d: %w", installation.ID, err)
		}
		for _, capability := range capabilities {
			if capability == nil || capability.Type != "scheduled_task.v1" {
				continue
			}
			key := pluginTaskKey(installation.ID, capability.ID)
			binding := bindings[key]
			if binding != nil && !binding.Enabled {
				continue
			}
			name, description := pluginTaskPresentation(installation.PluginID, capability)
			specs = append(specs, pluginTaskSpec{
				key:            key,
				installationID: installation.ID,
				capabilityID:   capability.ID,
				name:           name,
				description:    description,
				triggers:       pluginTaskTriggers(binding, capability),
			})
		}
	}
	return specs, nil
}

// pluginTask is a registered plugin task. Sync updates its presentation and
// default schedule in place, so a plugin upgrade never interrupts a run.
type pluginTask struct {
	installationID int
	capabilityID   string
	resolver       scheduledTaskResolver

	mu          sync.RWMutex
	name        string
	description string
	triggers    []taskmanager.TriggerConfig
}

func (t *pluginTask) update(spec pluginTaskSpec) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.name, t.description, t.triggers = spec.name, spec.description, spec.triggers
}

type scheduledTaskResolverFunc func(ctx context.Context, installationID int, capabilityID string) (scheduledTaskClient, error)

func (f scheduledTaskResolverFunc) ScheduledTaskClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (scheduledTaskClient, error) {
	return f(ctx, installationID, capabilityID)
}

func (t *pluginTask) Key() string { return pluginTaskKey(t.installationID, t.capabilityID) }
func (t *pluginTask) Name() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.name
}
func (t *pluginTask) Description() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.description
}
func (t *pluginTask) Category() taskmanager.TaskCategory { return taskmanager.TaskCategorySystem }
func (t *pluginTask) IsHidden() bool                     { return false }
func (t *pluginTask) DefaultTriggers() []taskmanager.TriggerConfig {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]taskmanager.TriggerConfig(nil), t.triggers...)
}

// FollowsDefaultTriggers keeps a plugin task's default out of task_schedules,
// so a saved schedule only ever means an administrator's edit and the task
// otherwise follows its binding and manifest default.
func (t *pluginTask) FollowsDefaultTriggers() bool { return true }

func (t *pluginTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Running plugin task")

	client, err := t.resolver.ScheduledTaskClient(ctx, t.installationID, t.capabilityID)
	if err != nil {
		return fmt.Errorf("load plugin task client: %w", err)
	}

	response, err := client.Run(ctx, &pluginv1.RunScheduledTaskRequest{TaskKey: t.Key()})
	if err != nil {
		return fmt.Errorf("run plugin task %s: %w", t.Key(), err)
	}
	if response.GetOutput() != nil {
		if data, marshalErr := json.Marshal(response.GetOutput().AsMap()); marshalErr == nil {
			progress.SetResultData(data)
		}
	}

	progress.Report(100, "Plugin task complete")
	return nil
}

// pluginTaskPresentation prefers the display name and description the plugin
// manifest declares for the capability, falling back to its identifiers.
func pluginTaskPresentation(pluginID string, capability *Capability) (string, string) {
	name := pluginID + " / " + capability.ID
	description := "Runs plugin scheduled task " + capability.ID
	if displayName, _ := capability.Metadata["display_name"].(string); strings.TrimSpace(displayName) != "" {
		name = strings.TrimSpace(displayName) + " (" + pluginID + ")"
	}
	if text, _ := capability.Metadata["description"].(string); strings.TrimSpace(text) != "" {
		description = strings.TrimSpace(text)
	}
	return name, description
}

func taskBindingKey(installationID int, capabilityID string) string {
	return fmt.Sprintf("%d/%s", installationID, capabilityID)
}

func pluginTaskKey(installationID int, capabilityID string) string {
	return fmt.Sprintf("plugin:%d:%s", installationID, capabilityID)
}

// pluginTaskTriggers is a plugin task's default schedule: the binding's
// trigger, else the manifest's scheduled_task.default_trigger, else one run
// at startup. A trigger the task manager cannot run falls through to the
// next source. A schedule saved on the task page outranks all three; the task
// manager reads it first.
func pluginTaskTriggers(binding *TaskBinding, capability *Capability) []taskmanager.TriggerConfig {
	if trigger := bindingTrigger(binding); trigger != nil {
		return []taskmanager.TriggerConfig{*trigger}
	}
	if trigger := manifestDefaultTrigger(capability); trigger != nil {
		return []taskmanager.TriggerConfig{*trigger}
	}
	return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeStartup}}
}

// bindingTrigger is the runnable trigger stored on the binding, normalized,
// or nil.
func bindingTrigger(binding *TaskBinding) *taskmanager.TriggerConfig {
	if binding == nil || len(binding.Trigger) == 0 {
		return nil
	}
	raw, err := json.Marshal(binding.Trigger)
	if err != nil {
		return nil
	}
	var trigger taskmanager.TriggerConfig
	if json.Unmarshal(raw, &trigger) != nil || !runnableTrigger(trigger) {
		return nil
	}
	trigger = trigger.Normalized()
	return &trigger
}

// manifestDefaultTrigger is the runnable default_trigger the plugin manifest
// declares for the capability, or nil. Capability metadata holds the
// descriptor as protojson, so interval_ms arrives as a string.
func manifestDefaultTrigger(capability *Capability) *taskmanager.TriggerConfig {
	if capability == nil {
		return nil
	}
	value, ok := capability.Metadata["scheduled_task"]
	if !ok {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var descriptor pluginv1.ScheduledTaskDescriptor
	if (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &descriptor) != nil {
		return nil
	}
	return ManifestDefaultTrigger(&descriptor)
}

// ManifestDefaultTrigger is the schedule a scheduled_task.v1 descriptor
// declares, normalized, when the task manager can run it, or nil.
func ManifestDefaultTrigger(descriptor *pluginv1.ScheduledTaskDescriptor) *taskmanager.TriggerConfig {
	declared := descriptor.GetDefaultTrigger()
	if declared == nil {
		return nil
	}
	trigger := taskmanager.TriggerConfig{
		Type:       taskmanager.TriggerType(declared.GetType()),
		IntervalMs: declared.GetIntervalMs(),
		TimeOfDay:  declared.GetTimeOfDay(),
		DayOfWeek:  int(declared.GetDayOfWeek()),
	}
	if !runnableTrigger(trigger) ||
		(trigger.Type == taskmanager.TriggerTypeInterval && trigger.IntervalMs < publicmanifest.MinTriggerIntervalMs) {
		return nil
	}
	trigger = trigger.Normalized()
	return &trigger
}

// runnableTrigger reports whether the task manager can schedule trigger:
// its type is one the manager knows and the fields that type reads are
// valid.
func runnableTrigger(trigger taskmanager.TriggerConfig) bool {
	switch trigger.Type {
	case taskmanager.TriggerTypeStartup:
		return true
	case taskmanager.TriggerTypeInterval:
		return trigger.IntervalMs > 0
	case taskmanager.TriggerTypeDaily:
		return validTimeOfDay(trigger.TimeOfDay)
	case taskmanager.TriggerTypeWeekly:
		return validTimeOfDay(trigger.TimeOfDay) && trigger.DayOfWeek >= 0 && trigger.DayOfWeek <= 6
	default:
		return false
	}
}

func validTimeOfDay(value string) bool {
	_, err := time.Parse("15:04", value)
	return err == nil
}

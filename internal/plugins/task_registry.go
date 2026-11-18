package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	GetTaskBinding(ctx context.Context, installationID int, capabilityID string) (*TaskBinding, error)
}

type TaskRegistry struct {
	installations taskInstallationStore
	bindings      taskBindingStore
	host          scheduledTaskResolver
}

func NewTaskRegistry(
	installations taskInstallationStore,
	bindings taskBindingStore,
	host scheduledTaskResolver,
) *TaskRegistry {
	return &TaskRegistry{
		installations: installations,
		bindings:      bindings,
		host:          host,
	}
}

func NewTaskRegistryWithTypedResolver(
	installations taskInstallationStore,
	bindings taskBindingStore,
	resolver interface {
		ScheduledTaskClient(ctx context.Context, installationID int, capabilityID string) (*pluginhost.ScheduledTaskClient, error)
	},
) *TaskRegistry {
	return NewTaskRegistry(installations, bindings, scheduledTaskResolverFunc(func(
		ctx context.Context,
		installationID int,
		capabilityID string,
	) (scheduledTaskClient, error) {
		return resolver.ScheduledTaskClient(ctx, installationID, capabilityID)
	}))
}

func (r *TaskRegistry) Tasks(ctx context.Context) ([]taskmanager.Task, error) {
	installations, err := r.installations.ListEnabled(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled plugin installations: %w", err)
	}

	var tasks []taskmanager.Task
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

			binding, err := r.bindings.GetTaskBinding(ctx, installation.ID, capability.ID)
			if err != nil && !errors.Is(err, ErrTaskBindingNotFound) {
				return nil, err
			}
			if binding != nil && !binding.Enabled {
				continue
			}

			name, description := pluginTaskPresentation(installation.PluginID, capability)
			task := &pluginTask{
				installationID: installation.ID,
				capabilityID:   capability.ID,
				name:           name,
				description:    description,
				resolver:       r.host,
				triggers:       pluginTaskTriggers(binding, capability),
			}
			tasks = append(tasks, task)
		}
	}

	return tasks, nil
}

type pluginTask struct {
	installationID int
	capabilityID   string
	name           string
	description    string
	resolver       scheduledTaskResolver
	triggers       []taskmanager.TriggerConfig
}

type scheduledTaskResolverFunc func(ctx context.Context, installationID int, capabilityID string) (scheduledTaskClient, error)

func (f scheduledTaskResolverFunc) ScheduledTaskClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (scheduledTaskClient, error) {
	return f(ctx, installationID, capabilityID)
}

func (t *pluginTask) Key() string                        { return pluginTaskKey(t.installationID, t.capabilityID) }
func (t *pluginTask) Name() string                       { return t.name }
func (t *pluginTask) Description() string                { return t.description }
func (t *pluginTask) Category() taskmanager.TaskCategory { return taskmanager.TaskCategorySystem }
func (t *pluginTask) IsHidden() bool                     { return false }
func (t *pluginTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return append([]taskmanager.TriggerConfig(nil), t.triggers...)
}

// FollowsDefaultTriggers keeps a plugin task's default out of task_schedules,
// so a saved schedule only ever means an administrator's edit and the task
// otherwise follows its binding and manifest default.
func (t *pluginTask) FollowsDefaultTriggers() bool { return true }

// ClaimsFirings makes each scheduled firing run on one API server: every API
// server registers the task, and a plugin's work is not safe to repeat per
// server.
func (t *pluginTask) ClaimsFirings() bool { return true }

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

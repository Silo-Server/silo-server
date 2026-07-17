package plugins

import (
	"context"
	"encoding/json"
	"fmt"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
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
			if err != nil && err != ErrTaskBindingNotFound {
				return nil, err
			}
			if binding != nil && !binding.Enabled {
				continue
			}

			task := &pluginTask{
				installationID: installation.ID,
				capabilityID:   capability.ID,
				name:           installation.PluginID + " / " + capability.ID,
				description:    "Runs plugin scheduled task " + capability.ID,
				resolver:       r.host,
				triggers:       defaultPluginTaskTriggers(binding),
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

func taskBindingKey(installationID int, capabilityID string) string {
	return fmt.Sprintf("%d/%s", installationID, capabilityID)
}

func pluginTaskKey(installationID int, capabilityID string) string {
	return fmt.Sprintf("plugin:%d:%s", installationID, capabilityID)
}

func defaultPluginTaskTriggers(binding *TaskBinding) []taskmanager.TriggerConfig {
	if binding == nil || len(binding.Trigger) == 0 {
		return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeStartup}}
	}

	var trigger taskmanager.TriggerConfig
	raw, err := json.Marshal(binding.Trigger)
	if err != nil || json.Unmarshal(raw, &trigger) != nil || trigger.Type == "" {
		return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeStartup}}
	}
	return []taskmanager.TriggerConfig{trigger}
}

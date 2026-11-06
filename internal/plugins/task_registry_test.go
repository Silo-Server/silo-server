package plugins

import (
	"slices"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// scheduledTaskCapability stores a capability the way an install does, so the
// default trigger reaches the registry in its protojson metadata shape.
func scheduledTaskCapability(t *testing.T, id, displayName string, trigger *pluginv1.ScheduledTaskTrigger) *Capability {
	t.Helper()
	descriptor := &pluginv1.CapabilityDescriptor{Type: "scheduled_task.v1", Id: id, DisplayName: displayName}
	if trigger != nil {
		descriptor.ScheduledTask = &pluginv1.ScheduledTaskDescriptor{DefaultTrigger: trigger}
	}
	records, err := CapabilityRecordsFromManifest(&pluginv1.PluginManifest{Capabilities: []*pluginv1.CapabilityDescriptor{descriptor}})
	if err != nil {
		t.Fatal(err)
	}
	return &records[0]
}

func TestPluginTaskTriggers(t *testing.T) {
	startup := taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeStartup}
	fiveMinutes := taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeInterval, IntervalMs: 300_000}
	manifestFiveMinutes := &pluginv1.ScheduledTaskTrigger{Type: "interval", IntervalMs: 300_000}
	for _, tc := range []struct {
		name     string
		binding  *TaskBinding
		declared *pluginv1.ScheduledTaskTrigger
		want     taskmanager.TriggerConfig
	}{
		{name: "no binding and no default", want: startup},
		{name: "no binding uses the interval default", declared: manifestFiveMinutes, want: fiveMinutes},
		{name: "daily default", declared: &pluginv1.ScheduledTaskTrigger{Type: "daily", TimeOfDay: "03:30"},
			want: taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "03:30"}},
		{name: "weekly default on Sunday", declared: &pluginv1.ScheduledTaskTrigger{Type: "weekly", TimeOfDay: "04:00", DayOfWeek: 0},
			want: taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeWeekly, TimeOfDay: "04:00"}},
		{name: "startup default", declared: &pluginv1.ScheduledTaskTrigger{Type: "startup"}, want: startup},
		{name: "interval default below the minimum", declared: &pluginv1.ScheduledTaskTrigger{Type: "interval", IntervalMs: 59_999}, want: startup},
		{name: "default of an unknown type", declared: &pluginv1.ScheduledTaskTrigger{Type: "cron"}, want: startup},
		{name: "daily default without a valid time", declared: &pluginv1.ScheduledTaskTrigger{Type: "daily", TimeOfDay: "25:00"}, want: startup},
		{name: "weekly default on no weekday", declared: &pluginv1.ScheduledTaskTrigger{Type: "weekly", TimeOfDay: "04:00", DayOfWeek: 7}, want: startup},
		{name: "binding overrides the default",
			binding:  &TaskBinding{Enabled: true, Trigger: map[string]any{"type": "daily", "time_of_day": "02:00"}},
			declared: manifestFiveMinutes,
			want:     taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "02:00"}},
		{name: "binding interval may be shorter than the manifest minimum",
			binding: &TaskBinding{Enabled: true, Trigger: map[string]any{"type": "interval", "interval_ms": 1000}},
			want:    taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeInterval, IntervalMs: 1000}},
		{name: "empty binding trigger uses the default", binding: &TaskBinding{Enabled: true, Trigger: map[string]any{}}, declared: manifestFiveMinutes, want: fiveMinutes},
		{name: "malformed binding trigger uses the default",
			binding:  &TaskBinding{Enabled: true, Trigger: map[string]any{"type": "interval", "interval_ms": "soon"}},
			declared: manifestFiveMinutes, want: fiveMinutes},
		{name: "binding trigger keeps only the fields its type reads",
			binding: &TaskBinding{Enabled: true, Trigger: map[string]any{"type": "daily", "time_of_day": "02:00", "day_of_week": 3, "interval_ms": 5}},
			want:    taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "02:00"}},
		{name: "binding trigger of an unknown type uses the default",
			binding:  &TaskBinding{Enabled: true, Trigger: map[string]any{"type": "cron", "expression": "0 * * * *"}},
			declared: manifestFiveMinutes, want: fiveMinutes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pluginTaskTriggers(tc.binding, scheduledTaskCapability(t, "sync", "", tc.declared))
			if !slices.Equal(got, []taskmanager.TriggerConfig{tc.want}) {
				t.Fatalf("triggers = %+v, want %+v", got, tc.want)
			}
		})
	}
}

package tasks

import (
	"context"
	"encoding/json"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type SyncMarkersTask struct{ service *markers.PopulationService }

func NewSyncMarkersTask(service *markers.PopulationService) *SyncMarkersTask {
	return &SyncMarkersTask{service: service}
}
func (t *SyncMarkersTask) Key() string  { return "sync_markers" }
func (t *SyncMarkersTask) Name() string { return "Sync online markers" }
func (t *SyncMarkersTask) Description() string {
	return "Fetches missing markers from enabled online providers and refreshes saved markers. Requires Save to library."
}
func (t *SyncMarkersTask) Category() taskmanager.TaskCategory { return taskmanager.TaskCategoryLibrary }
func (t *SyncMarkersTask) IsHidden() bool                     { return false }
func (t *SyncMarkersTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "03:00"}}
}
func (t *SyncMarkersTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.service == nil {
		progress.Report(100, "Online markers are unavailable")
		return nil
	}
	summary, err := t.service.Sync(ctx, progress.Report)
	if data, marshalErr := json.Marshal(summary); marshalErr == nil {
		progress.SetResultData(data)
	}
	return err
}

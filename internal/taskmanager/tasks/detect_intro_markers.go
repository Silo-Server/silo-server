package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/intromarkers"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type MarkerSettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

type DetectIntroMarkersTask struct {
	analyzer *intromarkers.Analyzer
	settings MarkerSettingsReader
}

func NewDetectIntroMarkersTask(analyzer *intromarkers.Analyzer, settings MarkerSettingsReader) *DetectIntroMarkersTask {
	return &DetectIntroMarkersTask{analyzer: analyzer, settings: settings}
}

func (t *DetectIntroMarkersTask) Key() string  { return "detect_intro_markers" }
func (t *DetectIntroMarkersTask) Name() string { return "Detect markers on this server" }
func (t *DetectIntroMarkersTask) Description() string {
	return "Analyzes files for intros in libraries with marker detection enabled."
}
func (t *DetectIntroMarkersTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *DetectIntroMarkersTask) IsHidden() bool { return false }

func (t *DetectIntroMarkersTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "03:30"},
	}
}

func (t *DetectIntroMarkersTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.analyzer == nil {
		progress.Report(100, "Intro marker analyzer unavailable")
		return nil
	}
	mode := markers.ModeLocal
	if t.settings != nil {
		raw, err := t.settings.Get(ctx, markers.SettingMode)
		if err != nil {
			return fmt.Errorf("loading marker mode: %w", err)
		}
		mode = markers.NormalizeMode(raw)
	}
	if !markers.ShouldRunLocal(mode) {
		progress.Report(100, fmt.Sprintf("Marker population skipped; mode is %s", mode))
		return nil
	}
	summary, err := t.analyzer.Run(ctx, func(percent float64, message string) {
		progress.Report(percent, message)
	})
	if data, marshalErr := json.Marshal(summary); marshalErr == nil {
		progress.SetResultData(data)
	}
	if err != nil {
		return fmt.Errorf("detecting intro markers: %w", err)
	}
	return nil
}

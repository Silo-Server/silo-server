package tasks

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// ArtifactProcessor drains the durable download-artifact encode queue (recover
// stranded jobs + encode pending ones) and evicts old artifacts.
type ArtifactProcessor interface {
	RunOnce(ctx context.Context) error
	Cleanup(ctx context.Context) error
}

// EncodeDownloadArtifactsTask hosts the prepare-to-file (remux/transcode) encode
// worker on the task manager so jobs are tracked, observable, and cancellable.
type EncodeDownloadArtifactsTask struct {
	proc ArtifactProcessor
}

// NewEncodeDownloadArtifactsTask creates the encode worker task.
func NewEncodeDownloadArtifactsTask(proc ArtifactProcessor) *EncodeDownloadArtifactsTask {
	return &EncodeDownloadArtifactsTask{proc: proc}
}

func (t *EncodeDownloadArtifactsTask) Key() string  { return "encode_download_artifacts" }
func (t *EncodeDownloadArtifactsTask) Name() string { return "Prepare Download Artifacts" }
func (t *EncodeDownloadArtifactsTask) Description() string {
	return "Encodes queued remux/transcode download artifacts, recovers stranded jobs, and evicts old artifacts"
}
func (t *EncodeDownloadArtifactsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *EncodeDownloadArtifactsTask) IsHidden() bool { return false }

func (t *EncodeDownloadArtifactsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	// Startup runs the crash-recovery sweep; the interval is a safety net behind
	// the low-latency RunTask kick fired when a new artifact is enqueued.
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 30000},
	}
}

func (t *EncodeDownloadArtifactsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Processing download artifact queue")
	if err := t.proc.RunOnce(ctx); err != nil {
		return err
	}
	if err := t.proc.Cleanup(ctx); err != nil {
		return err
	}
	progress.Report(100, "Download artifact queue drained")
	return nil
}

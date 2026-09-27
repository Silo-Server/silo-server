package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/intromarkers"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type MarkerSettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// markerAnalysisRunner runs one library-wide marker analysis pass.
// *intromarkers.Analyzer implements it.
type markerAnalysisRunner interface {
	// Preflight reports what this server lacks to compare season groups. An
	// error matching mediasample.ErrUnsupported means ffmpeg lacks Chromaprint;
	// any other error means the check itself failed.
	Preflight(ctx context.Context) error
	// Run analyzes episodes, then movies.
	Run(ctx context.Context, progress intromarkers.ProgressFunc) (intromarkers.RunSummary, error)
	// RunEpisodes analyzes episodes only.
	RunEpisodes(ctx context.Context, progress intromarkers.ProgressFunc) (intromarkers.RunSummary, error)
}

// detectMarkersAdvisoryLock spells "SILOMRKR".
const detectMarkersAdvisoryLock int64 = 0x53494C4F4D524B52

// DetectIntroMarkersTask runs the library-wide marker analysis. Every API
// process runs the task manager, so an advisory lock keeps one analysis pass
// running across the cluster; the other servers skip their run instead of
// repeating the same ffmpeg work. A server whose ffmpeg cannot fingerprint
// runs its chapter-only episode pass without the lock, so it never makes a
// capable server skip, and leaves movies to the lock holder. Playback-time
// and per-item analysis do not go through this task and are not serialized
// by it.
type DetectIntroMarkersTask struct {
	analyzer markerAnalysisRunner
	settings MarkerSettingsReader
	lock     clusterLock
}

// NewDetectIntroMarkersTask constructs the task. A nil pool runs without the
// cluster lock.
func NewDetectIntroMarkersTask(pool *pgxpool.Pool, analyzer *intromarkers.Analyzer, settings MarkerSettingsReader) *DetectIntroMarkersTask {
	task := &DetectIntroMarkersTask{settings: settings}
	if pool != nil {
		task.lock = advisoryClusterLock{pool: pool, key: detectMarkersAdvisoryLock, name: "marker detection"}
	}
	// Keep a nil analyzer a nil interface so Execute reports it unavailable.
	if analyzer != nil {
		task.analyzer = analyzer
	}
	return task
}

func (t *DetectIntroMarkersTask) Key() string  { return "detect_intro_markers" }
func (t *DetectIntroMarkersTask) Name() string { return "Detect markers on this server" }
func (t *DetectIntroMarkersTask) Description() string {
	return "Analyzes files for intros and credits in libraries with marker detection enabled."
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

const detectMarkersRunningElsewhere = "Skipped: another server is already detecting markers"

// detectMarkersSkipped is the result data of a run that another server covered.
type detectMarkersSkipped struct {
	Skipped bool   `json:"skipped"`
	Reason  string `json:"reason"`
}

func (t *DetectIntroMarkersTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.analyzer == nil {
		progress.Report(100, "Marker analyzer unavailable")
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
	lock, run := t.lock, t.analyzer.Run
	if err := t.analyzer.Preflight(ctx); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		// Only a capability listing that lacks Chromaprint proves this server
		// can only read chapters; leave the lock to a server that can also
		// compare season groups, and the movie pass, which the lock keeps to
		// one server, to the lock holder. A failed listing proves nothing and
		// Run probes again, so keep the lock.
		if errors.Is(err, mediasample.ErrUnsupported) {
			lock, run = nil, t.analyzer.RunEpisodes
		}
	}
	if lock != nil {
		release, acquired, err := lock.TryAcquire(ctx)
		if err != nil {
			return fmt.Errorf("claiming marker detection: %w", err)
		}
		if !acquired {
			// Another server covers this run. Succeed with a recorded skip rather
			// than fail, so a healthy cluster's run history stays free of errors.
			if data, marshalErr := json.Marshal(detectMarkersSkipped{Skipped: true, Reason: detectMarkersRunningElsewhere}); marshalErr == nil {
				progress.SetResultData(data)
			}
			progress.Report(100, detectMarkersRunningElsewhere)
			return nil
		}
		defer release()
	}

	summary, err := run(ctx, func(percent float64, message string) {
		progress.Report(percent, message)
	})
	if data, marshalErr := json.Marshal(summary); marshalErr == nil {
		progress.SetResultData(data)
	}
	if err != nil {
		return fmt.Errorf("detecting markers: %w", err)
	}
	return nil
}

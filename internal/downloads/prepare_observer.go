package downloads

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// Admin realtime events describing the preparation queue.
const (
	// PreparationChangedEvent means a job's state, worker or membership in
	// the active list changed; listeners re-read the list.
	PreparationChangedEvent = "download_preparation.changed"
	// PreparationProgressEvent carries a job's newest progress reading.
	PreparationProgressEvent = "download_preparation.progress"
)

// PreparationEvent is published to the admin realtime channel. Progress is
// set only on PreparationProgressEvent.
type PreparationEvent struct {
	Name       string
	ArtifactID string
	Progress   *ArtifactProgress
}

// PreparationNotifier publishes preparation events to admin listeners.
type PreparationNotifier func(ctx context.Context, event PreparationEvent)

// progressFlushInterval bounds how often one attempt writes its progress and
// notifies listeners. FFmpeg reports twice a second; nobody reads that fast.
const progressFlushInterval = 5 * time.Second

// prepareObserver receives live facts about one prepare attempt from the
// preparer that executes it. It travels in the context so preparers need no
// extra parameters; a context without one gets a no-op.
type prepareObserver interface {
	// prepareWorker records where the attempt runs. A nil node means the API
	// server itself.
	prepareWorker(nodeID *int, name string)
	playback.PrepareProgressSink
	// prepareProgressUnavailable reports that the worker cannot report
	// progress, for example a node that predates the progress endpoint.
	prepareProgressUnavailable()
}

type prepareObserverKey struct{}

func withPrepareObserver(ctx context.Context, observer prepareObserver) context.Context {
	return context.WithValue(ctx, prepareObserverKey{}, observer)
}

func prepareObserverFrom(ctx context.Context) prepareObserver {
	if observer, ok := ctx.Value(prepareObserverKey{}).(prepareObserver); ok && observer != nil {
		return observer
	}
	return noopPrepareObserver{}
}

type noopPrepareObserver struct{}

func (noopPrepareObserver) prepareWorker(*int, string)               {}
func (noopPrepareObserver) PrepareProgress(playback.PrepareProgress) {}
func (noopPrepareObserver) prepareProgressUnavailable()              {}

// attemptObserver persists one claimed attempt's live state. Worker and
// availability changes are written at once; progress readings are coalesced
// and written by run at most once per progressFlushInterval.
type attemptObserver struct {
	m  *ArtifactManager
	id string

	mu     sync.Mutex
	latest *playback.PrepareProgress
}

func (m *ArtifactManager) newAttemptObserver(id string) *attemptObserver {
	return &attemptObserver{m: m, id: id}
}

func (o *attemptObserver) prepareWorker(nodeID *int, name string) {
	kind := WorkerServer
	if nodeID != nil {
		kind = WorkerNode
	} else if name == "" {
		name = o.m.owner
	}
	o.mu.Lock()
	o.latest = nil
	o.mu.Unlock()
	ctx, cancel := o.m.liveStateContext()
	defer cancel()
	if applied, err := o.m.repo.RecordWorker(ctx, o.id, o.m.owner, kind, nodeID, name); err != nil {
		slog.WarnContext(ctx, "recording download artifact worker failed", "component", "downloads", "artifact_id", o.id, "error", err)
	} else if applied {
		o.m.notifyPreparation(ctx, PreparationEvent{Name: PreparationChangedEvent, ArtifactID: o.id})
	}
}

func (o *attemptObserver) PrepareProgress(p playback.PrepareProgress) {
	o.mu.Lock()
	o.latest = &p
	o.mu.Unlock()
}

func (o *attemptObserver) prepareProgressUnavailable() {
	ctx, cancel := o.m.liveStateContext()
	defer cancel()
	if applied, err := o.m.repo.RecordProgressUnavailable(ctx, o.id, o.m.owner); err != nil {
		slog.WarnContext(ctx, "recording download artifact progress unavailable failed", "component", "downloads", "artifact_id", o.id, "error", err)
	} else if applied {
		o.m.notifyPreparation(ctx, PreparationEvent{Name: PreparationChangedEvent, ArtifactID: o.id})
	}
}

// run flushes coalesced progress until ctx ends. The final reading is not
// flushed: the ready or failed transition that follows supersedes it.
func (o *attemptObserver) run(ctx context.Context) {
	ticker := time.NewTicker(progressFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.flush(ctx)
		}
	}
}

func (o *attemptObserver) flush(ctx context.Context) {
	o.mu.Lock()
	reading := o.latest
	o.latest = nil
	o.mu.Unlock()
	if reading == nil {
		return
	}
	progress := ArtifactProgress{
		EncodedSeconds:  reading.EncodedSeconds,
		DurationSeconds: reading.DurationSeconds,
		Speed:           reading.Speed,
	}
	applied, err := o.m.repo.RecordProgress(ctx, o.id, o.m.owner, progress)
	if err != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "recording download artifact progress failed", "component", "downloads", "artifact_id", o.id, "error", err)
		}
		return
	}
	if applied {
		progress.UpdatedAt = time.Now().UTC()
		o.m.notifyPreparation(ctx, PreparationEvent{Name: PreparationProgressEvent, ArtifactID: o.id, Progress: &progress})
	}
}

// liveStateContext bounds a live-state write made on behalf of an attempt.
// The write is advisory, so it must never stall the encode that reports it.
func (m *ArtifactManager) liveStateContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// SetPreparationNotifier wires admin realtime events for the preparation
// queue. Nil disables them.
func (m *ArtifactManager) SetPreparationNotifier(notify PreparationNotifier) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.prepNotify = notify
	m.mu.Unlock()
}

// SetFFmpegLogSink routes the FFmpeg output of encodes that run on the API
// server into the operational logs.
func (m *ArtifactManager) SetFFmpegLogSink(sink playback.FFmpegLogSink) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.ffmpegLogs = sink
	m.mu.Unlock()
}

func (m *ArtifactManager) notifyPreparation(ctx context.Context, event PreparationEvent) {
	m.mu.Lock()
	notify := m.prepNotify
	m.mu.Unlock()
	if notify != nil {
		notify(ctx, event)
	}
}

func (m *ArtifactManager) notifyPreparationChanged(ctx context.Context, artifactID string) {
	m.notifyPreparation(ctx, PreparationEvent{Name: PreparationChangedEvent, ArtifactID: artifactID})
}

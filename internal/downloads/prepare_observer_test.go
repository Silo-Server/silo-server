package downloads

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type recordingPrepareObserver struct {
	mu          sync.Mutex
	workers     []string
	nodeIDs     []*int
	readings    []playback.PrepareProgress
	unavailable int
}

func (o *recordingPrepareObserver) prepareWorker(nodeID *int, name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.workers = append(o.workers, name)
	o.nodeIDs = append(o.nodeIDs, nodeID)
}

func (o *recordingPrepareObserver) PrepareProgress(p playback.PrepareProgress) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.readings = append(o.readings, p)
}

func (o *recordingPrepareObserver) prepareProgressUnavailable() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.unavailable++
}

func (o *recordingPrepareObserver) snapshot() (workers []string, nodeIDs []*int, readings []playback.PrepareProgress, unavailable int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.workers...), append([]*int(nil), o.nodeIDs...), append([]playback.PrepareProgress(nil), o.readings...), o.unavailable
}

// progressRemotePreparer holds its prepare request open until the caller has
// polled progress twice, the way a real node holds a long encode.
type progressRemotePreparer struct {
	recordingRemotePreparer
	unsupported bool
	polled      chan struct{}
	once        sync.Once
	mu          sync.Mutex
	polls       int
}

func (p *progressRemotePreparer) Prepare(ctx context.Context, nodeURL, secret string, req downloadprepare.Request) (downloadprepare.Result, error) {
	select {
	case <-p.polled:
	case <-ctx.Done():
		return downloadprepare.Result{}, ctx.Err()
	case <-time.After(5 * time.Second):
	}
	return p.recordingRemotePreparer.Prepare(ctx, nodeURL, secret, req)
}

func (p *progressRemotePreparer) Progress(_ context.Context, _, _, artifactID string) (downloadprepare.Progress, error) {
	p.mu.Lock()
	p.polls++
	polls := p.polls
	p.mu.Unlock()
	if p.unsupported {
		p.once.Do(func() { close(p.polled) })
		return downloadprepare.Progress{}, downloadprepare.ErrProgressUnsupported
	}
	if polls >= 2 {
		p.once.Do(func() { close(p.polled) })
	}
	return downloadprepare.Progress{ArtifactID: artifactID, Running: true, EncodedSeconds: float64(10 * polls), DurationSeconds: 100, Speed: 2}, nil
}

func fastRemoteProgressPolls(t *testing.T) {
	t.Helper()
	previous := remoteProgressPollInterval
	remoteProgressPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { remoteProgressPollInterval = previous })
}

func singleNodePreparer(t *testing.T, remote downloadprepare.RemotePreparer) *NodeAwarePreparer {
	t.Helper()
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{ID: 17, Name: "gpu-01", URL: "http://gpu-01", Enabled: true, Healthy: true}})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	p := NewNodeAwarePreparer(&recordingEncodePreparer{}, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = remote
	return p
}

func TestNodeAwarePreparerReportsNodeAndRelaysProgress(t *testing.T) {
	fastRemoteProgressPolls(t)
	remote := &progressRemotePreparer{polled: make(chan struct{})}
	p := singleNodePreparer(t, remote)
	observer := &recordingPrepareObserver{}
	opts := playback.TranscodeOpts{InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac"}
	if _, err := p.PrepareFile(withPrepareObserver(context.Background(), observer), "artifact-1", opts, "/local/a.mp4"); err != nil {
		t.Fatal(err)
	}
	workers, nodeIDs, readings, unavailable := observer.snapshot()
	if len(workers) != 1 || workers[0] != "gpu-01" || nodeIDs[0] == nil || *nodeIDs[0] != 17 {
		t.Fatalf("workers = %v %v, want gpu-01 (17)", workers, nodeIDs)
	}
	if len(readings) < 2 || readings[0].EncodedSeconds != 10 || readings[0].DurationSeconds != 100 || readings[0].Speed != 2 {
		t.Fatalf("readings = %+v", readings)
	}
	if unavailable != 0 {
		t.Fatalf("unavailable = %d", unavailable)
	}
}

func TestNodeAwarePreparerMarksNodesWithoutProgressUnavailable(t *testing.T) {
	fastRemoteProgressPolls(t)
	remote := &progressRemotePreparer{polled: make(chan struct{}), unsupported: true}
	p := singleNodePreparer(t, remote)
	observer := &recordingPrepareObserver{}
	opts := playback.TranscodeOpts{InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac"}
	if _, err := p.PrepareFile(withPrepareObserver(context.Background(), observer), "artifact-2", opts, "/local/a.mp4"); err != nil {
		t.Fatal(err)
	}
	_, _, readings, unavailable := observer.snapshot()
	if unavailable != 1 || len(readings) != 0 {
		t.Fatalf("unavailable = %d readings = %+v, want one unavailable report", unavailable, readings)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.polls != 1 {
		t.Fatalf("polls = %d, want polling to stop after the first 404", remote.polls)
	}
}

func TestPlaybackPreparerReportsServerWorkerAndProgress(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ffmpeg")
	body := "#!/bin/sh\nprintf 'out_time_us=5000000\\nspeed=1.5x\\nprogress=end\\n'\neval \"touch \\${$#}\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	observer := &recordingPrepareObserver{}
	opts := playback.TranscodeOpts{
		InputPath: "/nonexistent/in.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		FFmpegPath: script, HWAccel: playback.HWAccelNone, TotalDuration: 10,
	}
	if _, err := NewPlaybackPreparer().PrepareFile(withPrepareObserver(context.Background(), observer), "a", opts, filepath.Join(dir, "out.mp4")); err != nil {
		t.Fatal(err)
	}
	workers, nodeIDs, readings, _ := observer.snapshot()
	if len(workers) != 1 || workers[0] != "" || nodeIDs[0] != nil {
		t.Fatalf("workers = %v %v, want the API server", workers, nodeIDs)
	}
	if len(readings) != 1 || readings[0].EncodedSeconds != 10 || readings[0].Speed != 1.5 {
		t.Fatalf("readings = %+v", readings)
	}
}

func TestAttemptObserverPersistsWorkerAndCoalescedProgress(t *testing.T) {
	repo, _, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, "hash-observer"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimNext(ctx, "api-7", time.Minute); err != nil {
		t.Fatal(err)
	}
	m := NewArtifactManager(repo, nil, nil, &recordingEncodePreparer{}, "api-7", nil, nil)
	var mu sync.Mutex
	var events []PreparationEvent
	m.SetPreparationNotifier(func(_ context.Context, event PreparationEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	})

	observer := m.newAttemptObserver(row.ID)
	observer.prepareWorker(nil, "")
	observer.flush(ctx) // nothing reported yet: no write, no event
	observer.PrepareProgress(playback.PrepareProgress{EncodedSeconds: 1, DurationSeconds: 50})
	observer.PrepareProgress(playback.PrepareProgress{EncodedSeconds: 20, DurationSeconds: 50, Speed: 3})
	observer.flush(ctx)

	got, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkerKind != WorkerServer || got.WorkerName != "api-7" {
		t.Fatalf("worker = %q %q, want server api-7", got.WorkerKind, got.WorkerName)
	}
	if got.Progress == nil || got.Progress.EncodedSeconds != 20 || got.Progress.Speed != 3 {
		t.Fatalf("progress = %+v, want the newest reading only", got.Progress)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0].Name != PreparationChangedEvent || events[1].Name != PreparationProgressEvent ||
		events[1].Progress == nil || events[1].Progress.EncodedSeconds != 20 || events[1].ArtifactID != row.ID {
		t.Fatalf("events = %+v", events)
	}
}

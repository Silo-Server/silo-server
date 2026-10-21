package downloads

import (
	"context"
	"sync"
	"testing"
	"time"

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
	p := newPoolPreparer(&recordingEncodePreparer{}, &nodepool.Node{ID: 17, Name: "gpu-01", URL: "http://gpu-01", Enabled: true, Healthy: true})
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

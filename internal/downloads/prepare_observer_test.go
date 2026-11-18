package downloads

import (
	"context"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
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

func (p *progressRemotePreparer) Prepare(ctx context.Context, nodeURL string, secret nodeauth.Keyring, req downloadprepare.Request) (downloadprepare.Result, error) {
	select {
	case <-p.polled:
	case <-ctx.Done():
		return downloadprepare.Result{}, ctx.Err()
	case <-time.After(5 * time.Second):
	}
	return p.recordingRemotePreparer.Prepare(ctx, nodeURL, secret, req)
}

func (p *progressRemotePreparer) Progress(_ context.Context, _ string, _ nodeauth.Keyring, artifactID string) (downloadprepare.Progress, error) {
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

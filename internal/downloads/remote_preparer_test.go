package downloads

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type recordingEncodePreparer struct{ calls int }

func (p *recordingEncodePreparer) PrepareFile(_ context.Context, _ string, _ playback.TranscodeOpts, outputPath string) (PreparedArtifact, error) {
	p.calls++
	return PreparedArtifact{OutputPath: outputPath, FileSize: 8}, nil
}

type recordingRemotePreparer struct {
	nodeURL   string
	secret    string
	request   downloadprepare.Request
	deleted   string
	deleteURL string
}

func (p *recordingRemotePreparer) Prepare(_ context.Context, nodeURL, secret string, req downloadprepare.Request) (downloadprepare.Result, error) {
	p.nodeURL, p.secret, p.request = nodeURL, secret, req
	return downloadprepare.Result{ArtifactID: req.ArtifactID, FileSize: 1234}, nil
}

func (p *recordingRemotePreparer) Stat(_ context.Context, _, _ string, artifactID string) (downloadprepare.Result, error) {
	return downloadprepare.Result{ArtifactID: artifactID, FileSize: 1234}, nil
}

func (p *recordingRemotePreparer) Delete(_ context.Context, nodeURL, _ string, artifactID string) error {
	p.deleted = artifactID
	p.deleteURL = nodeURL
	return nil
}

type staticArtifactOriginLookup struct {
	node *nodepool.Node
	err  error
}

func (l staticArtifactOriginLookup) GetByID(context.Context, int) (*nodepool.Node, error) {
	return l.node, l.err
}

type unavailableRemotePreparer struct{}

func (unavailableRemotePreparer) Prepare(context.Context, string, string, downloadprepare.Request) (downloadprepare.Result, error) {
	return downloadprepare.Result{}, os.ErrNotExist
}
func (unavailableRemotePreparer) Stat(context.Context, string, string, string) (downloadprepare.Result, error) {
	return downloadprepare.Result{}, downloadprepare.ErrArtifactNotFound
}
func (unavailableRemotePreparer) Delete(context.Context, string, string, string) error { return nil }

type responseLostRemotePreparer struct{}

func (responseLostRemotePreparer) Prepare(context.Context, string, string, downloadprepare.Request) (downloadprepare.Result, error) {
	return downloadprepare.Result{}, context.DeadlineExceeded
}
func (responseLostRemotePreparer) Stat(_ context.Context, _, _ string, artifactID string) (downloadprepare.Result, error) {
	return downloadprepare.Result{ArtifactID: artifactID, FileSize: 55}, nil
}
func (responseLostRemotePreparer) Delete(context.Context, string, string, string) error { return nil }

type indeterminateRemotePreparer struct{}

func (indeterminateRemotePreparer) Prepare(context.Context, string, string, downloadprepare.Request) (downloadprepare.Result, error) {
	return downloadprepare.Result{}, context.DeadlineExceeded
}
func (indeterminateRemotePreparer) Stat(context.Context, string, string, string) (downloadprepare.Result, error) {
	return downloadprepare.Result{}, os.ErrDeadlineExceeded
}
func (indeterminateRemotePreparer) Delete(context.Context, string, string, string) error { return nil }

type mismatchedRecoveryRemotePreparer struct{}

func (mismatchedRecoveryRemotePreparer) Prepare(context.Context, string, string, downloadprepare.Request) (downloadprepare.Result, error) {
	return downloadprepare.Result{}, context.DeadlineExceeded
}
func (mismatchedRecoveryRemotePreparer) Stat(context.Context, string, string, string) (downloadprepare.Result, error) {
	return downloadprepare.Result{ArtifactID: "unexpected-artifact", FileSize: 55}, nil
}
func (mismatchedRecoveryRemotePreparer) Delete(context.Context, string, string, string) error {
	return nil
}

func TestNodeAwarePreparerUsesLeastLoadedHealthyNode(t *testing.T) {
	group := "host-a"
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{
		{URL: "http://busy", Enabled: true, Healthy: true, ActiveJobs: 3},
		{ID: 17, URL: "http://idle", Enabled: true, Healthy: true, ActiveJobs: 1, Group: &group},
		{URL: "http://unhealthy", Enabled: true, Healthy: false},
	})
	local := &recordingEncodePreparer{}
	remote := &recordingRemotePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = remote

	opts := playback.TranscodeOpts{InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac"}
	prepared, err := p.PrepareFile(context.Background(), "artifact-1", opts, "/local/artifact-1.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if local.calls != 0 {
		t.Fatalf("local calls = %d, want 0", local.calls)
	}
	if remote.nodeURL != "http://idle" || remote.secret != "secret" || remote.request.ArtifactID != "artifact-1" || remote.request.InputPath != opts.InputPath {
		t.Fatalf("remote call = node %q secret %q request %+v", remote.nodeURL, remote.secret, remote.request)
	}
	if prepared.OutputPath != "" || prepared.OriginNodeID != 17 || prepared.OriginNodeURL != "http://idle" || prepared.OriginNodeGroup != group || prepared.OriginArtifactID != "artifact-1" || prepared.FileSize != 1234 {
		t.Fatalf("prepared = %+v", prepared)
	}
}

func TestNodeAwarePreparerResolvesCurrentArtifactNodeURL(t *testing.T) {
	group := "host-new"
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{ID: 17, URL: "http://new-url", Group: &group, Enabled: true, Healthy: true}})
	p := NewNodeAwarePreparer(nil, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), nil)
	artifact := &Artifact{OriginNodeID: 17, OriginNodeURL: "http://old-url", OriginNodeGroup: "host-old", OriginArtifactID: "artifact-1"}
	if err := p.ResolveArtifact(context.Background(), artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.OriginNodeURL != "http://new-url" || artifact.OriginNodeGroup != group {
		t.Fatalf("artifact = %+v", artifact)
	}
}

func TestNodeAwarePreparerUsesCurrentDisabledNodeURLForCleanup(t *testing.T) {
	group := "host-new"
	pool := nodepool.NewTranscodePool()
	remote := &recordingRemotePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	p := NewNodeAwarePreparer(nil, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = remote
	p.SetOriginLookup(staticArtifactOriginLookup{node: &nodepool.Node{
		ID: 17, Type: nodepool.NodeTypeTranscode, URL: "http://new-url", Group: &group, Enabled: false,
	}})
	artifact := &Artifact{OriginNodeID: 17, OriginNodeURL: "http://old-url", OriginNodeGroup: "host-old", OriginArtifactID: "artifact-1"}
	if err := p.ResolveArtifact(context.Background(), artifact); !errors.Is(err, ErrArtifactOriginRemoved) {
		t.Fatalf("ResolveArtifact error = %v, want ErrArtifactOriginRemoved", err)
	}
	if artifact.OriginNodeURL != "http://new-url" || artifact.OriginNodeGroup != group {
		t.Fatalf("refreshed artifact = %+v", artifact)
	}
	if err := p.DeleteArtifact(context.Background(), artifact); err != nil {
		t.Fatal(err)
	}
	if remote.deleteURL != "http://new-url" || remote.deleted != "artifact-1" {
		t.Fatalf("cleanup target = %q %q", remote.deleteURL, remote.deleted)
	}
}

func TestNodeAwarePreparerRecoversEnabledOriginMissingFromPool(t *testing.T) {
	group := "host-new"
	pool := nodepool.NewTranscodePool()
	p := NewNodeAwarePreparer(nil, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), nil)
	p.SetOriginLookup(staticArtifactOriginLookup{node: &nodepool.Node{
		ID: 17, Type: nodepool.NodeTypeTranscode, URL: "http://new-url", Group: &group, Enabled: true,
	}})
	artifact := &Artifact{OriginNodeID: 17, OriginNodeURL: "http://old-url", OriginNodeGroup: "host-old", OriginArtifactID: "artifact-1"}
	if err := p.ResolveArtifact(context.Background(), artifact); err != nil {
		t.Fatalf("ResolveArtifact error = %v", err)
	}
	if artifact.OriginNodeURL != "http://new-url" || artifact.OriginNodeGroup != group {
		t.Fatalf("refreshed artifact = %+v", artifact)
	}
}

func TestNodeAwarePreparerReportsRemovedArtifactOrigin(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{ID: 17, URL: "http://disabled", Enabled: false, Healthy: true}})
	p := NewNodeAwarePreparer(nil, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), nil)
	artifact := &Artifact{OriginNodeID: 17, OriginNodeURL: "http://removed", OriginArtifactID: "artifact-1"}
	if err := p.ResolveArtifact(context.Background(), artifact); !errors.Is(err, ErrArtifactOriginRemoved) {
		t.Fatalf("ResolveArtifact error = %v, want ErrArtifactOriginRemoved", err)
	}
}

func TestNodeAwarePreparerFallsBackLocallyWithoutEligibleCapacity(t *testing.T) {
	limit := 1
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{URL: "http://full", Enabled: true, Healthy: true, ActiveJobs: 1, MaxJobs: &limit}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = &recordingRemotePreparer{}

	prepared, err := p.PrepareFile(context.Background(), "artifact-2", playback.TranscodeOpts{}, "/artifacts/job-2.mp4")
	if err != nil || prepared.OutputPath == "" || local.calls != 1 {
		t.Fatalf("prepared=%+v err=%v local calls=%d", prepared, err, local.calls)
	}
}

func TestNodeAwarePreparerRetainsRequestedLocatorAfterMismatchedRecovery(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{ID: 17, URL: "http://idle", Enabled: true, Healthy: true}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = mismatchedRecoveryRemotePreparer{}

	prepared, err := p.PrepareFile(context.Background(), "artifact-requested", playback.TranscodeOpts{}, "/local/job.mp4")
	if err == nil {
		t.Fatal("expected mismatched recovery error")
	}
	if !prepared.Remote() || prepared.OriginArtifactID != "artifact-requested" || prepared.FileSize != 0 {
		t.Fatalf("prepared locator = %+v, want requested remote artifact", prepared)
	}
	if local.calls != 0 {
		t.Fatalf("local calls = %d, want no fallback after indeterminate remote result", local.calls)
	}
}

func TestNodeAwarePreparerFallsBackLocallyWithoutNodeCredentials(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{ID: 18, URL: "http://idle", Enabled: true, Healthy: true}})
	local := &recordingEncodePreparer{}
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return &config.Config{} })
	p.remote = &recordingRemotePreparer{}

	if _, err := p.PrepareFile(context.Background(), "artifact-3", playback.TranscodeOpts{}, "/artifacts/job-3.mp4"); err != nil {
		t.Fatal(err)
	}
	if local.calls != 1 {
		t.Fatalf("local calls = %d, want 1", local.calls)
	}
}

func TestNodeAwarePreparerUsesRemoteWithDefaultNodeLocalArtifactDir(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{ID: 19, URL: "http://idle", Enabled: true, Healthy: true}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = &recordingRemotePreparer{}

	prepared, err := p.PrepareFile(context.Background(), "artifact-local", playback.TranscodeOpts{}, "/local/job.mp4")
	if err != nil || !prepared.Remote() || local.calls != 0 {
		t.Fatalf("prepared=%+v err=%v local calls=%d", prepared, err, local.calls)
	}
}

func TestNodeAwarePreparerFallsBackLocallyWhenRemoteUnavailable(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{URL: "http://idle", Enabled: true, Healthy: true}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = unavailableRemotePreparer{}

	prepared, err := p.PrepareFile(context.Background(), "artifact-4", playback.TranscodeOpts{}, "/local/job-4.mp4")
	if err != nil || prepared.OutputPath == "" || local.calls != 1 {
		t.Fatalf("prepared=%+v err=%v local calls=%d", prepared, err, local.calls)
	}
}

func TestNodeAwarePreparerRecoversCompletedArtifactAfterResponseLoss(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{ID: 21, URL: "http://idle", Enabled: true, Healthy: true}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = responseLostRemotePreparer{}

	prepared, err := p.PrepareFile(context.Background(), "artifact-5", playback.TranscodeOpts{}, "/local/job-5.mp4")
	if err != nil || !prepared.Remote() || prepared.FileSize != 55 || local.calls != 0 {
		t.Fatalf("prepared=%+v err=%v local calls=%d", prepared, err, local.calls)
	}
}

func TestNodeAwarePreparerDoesNotFallBackAfterLeaseCancellation(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{URL: "http://idle", Enabled: true, Healthy: true}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = unavailableRemotePreparer{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.PrepareFile(ctx, "artifact-6", playback.TranscodeOpts{}, "/local/job-6.mp4")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if local.calls != 0 {
		t.Fatalf("local calls = %d, want 0", local.calls)
	}
}

func TestNodeAwarePreparerDoesNotFallBackWhenRecoveryProbeIsIndeterminate(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{ID: 20, URL: "http://idle", Enabled: true, Healthy: true}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = indeterminateRemotePreparer{}

	if _, err := p.PrepareFile(context.Background(), "artifact-7", playback.TranscodeOpts{}, "/local/job-7.mp4"); err == nil {
		t.Fatal("expected indeterminate remote error")
	}
	if local.calls != 0 {
		t.Fatalf("local calls = %d, want 0", local.calls)
	}
}

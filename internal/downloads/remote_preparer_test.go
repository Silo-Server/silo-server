package downloads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type recordingEncodePreparer struct {
	calls int
}

func (p *recordingEncodePreparer) PrepareFile(context.Context, playback.TranscodeOpts, string) error {
	p.calls++
	return nil
}

type recordingRemotePreparer struct {
	nodeURL string
	secret  string
	request downloadprepare.Request
}

type unavailableRemotePreparer struct{}

func (unavailableRemotePreparer) Prepare(context.Context, string, string, downloadprepare.Request) error {
	return os.ErrNotExist
}

type responseLostRemotePreparer struct{}

func (responseLostRemotePreparer) Prepare(_ context.Context, _, _ string, req downloadprepare.Request) error {
	if err := os.WriteFile(req.OutputPath, []byte("prepared"), 0o600); err != nil {
		return err
	}
	return context.DeadlineExceeded
}

func (p *recordingRemotePreparer) Prepare(_ context.Context, nodeURL, secret string, req downloadprepare.Request) error {
	p.nodeURL = nodeURL
	p.secret = secret
	p.request = req
	return os.WriteFile(req.OutputPath, []byte("prepared"), 0o600)
}

func TestNodeAwarePreparerUsesLeastLoadedHealthyNode(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{
		{URL: "http://busy", Enabled: true, Healthy: true, ActiveJobs: 3},
		{URL: "http://idle", Enabled: true, Healthy: true, ActiveJobs: 1},
		{URL: "http://unhealthy", Enabled: true, Healthy: false},
	})
	local := &recordingEncodePreparer{}
	remote := &recordingRemotePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	cfg.Download.ArtifactDir = t.TempDir()
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = remote

	opts := playback.TranscodeOpts{InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac"}
	outputPath := filepath.Join(cfg.Download.ArtifactDir, "job-1.mp4")
	if err := p.PrepareFile(context.Background(), opts, outputPath); err != nil {
		t.Fatal(err)
	}
	if local.calls != 0 {
		t.Fatalf("local calls = %d, want 0", local.calls)
	}
	if remote.nodeURL != "http://idle" || remote.secret != "secret" {
		t.Fatalf("remote call = node %q secret %q", remote.nodeURL, remote.secret)
	}
	if remote.request.InputPath != opts.InputPath || remote.request.OutputPath == outputPath || filepath.Dir(remote.request.OutputPath) != filepath.Dir(outputPath) || filepath.Ext(remote.request.OutputPath) != ".mp4" {
		t.Fatalf("remote request = %+v", remote.request)
	}
	if got, err := os.ReadFile(outputPath); err != nil || string(got) != "prepared" {
		t.Fatalf("promoted output = %q, %v", got, err)
	}
}

func TestNodeAwarePreparerFallsBackLocallyWithoutEligibleCapacity(t *testing.T) {
	limit := 1
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{URL: "http://full", Enabled: true, Healthy: true, ActiveJobs: 1, MaxJobs: &limit}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	cfg.Download.ArtifactDir = t.TempDir()
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = &recordingRemotePreparer{}

	if err := p.PrepareFile(context.Background(), playback.TranscodeOpts{}, "/artifacts/job-2.mp4"); err != nil {
		t.Fatal(err)
	}
	if local.calls != 1 {
		t.Fatalf("local calls = %d, want 1", local.calls)
	}
}

func TestNodeAwarePreparerFallsBackLocallyWithoutNodeCredentials(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{URL: "http://idle", Enabled: true, Healthy: true}})
	local := &recordingEncodePreparer{}
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return &config.Config{} })
	p.remote = &recordingRemotePreparer{}

	if err := p.PrepareFile(context.Background(), playback.TranscodeOpts{}, "/artifacts/job-3.mp4"); err != nil {
		t.Fatal(err)
	}
	if local.calls != 1 {
		t.Fatalf("local calls = %d, want 1", local.calls)
	}
}

func TestNodeAwarePreparerKeepsDefaultNodeLocalArtifactDirLocal(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{URL: "http://idle", Enabled: true, Healthy: true}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = &recordingRemotePreparer{}

	if err := p.PrepareFile(context.Background(), playback.TranscodeOpts{}, filepath.Join(t.TempDir(), "job-local.mp4")); err != nil {
		t.Fatal(err)
	}
	if local.calls != 1 {
		t.Fatalf("local calls = %d, want 1", local.calls)
	}
}

func TestNodeAwarePreparerFallsBackLocallyWhenRemoteOutputIsUnavailable(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{URL: "http://idle", Enabled: true, Healthy: true}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	cfg.Download.ArtifactDir = t.TempDir()
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = unavailableRemotePreparer{}

	if err := p.PrepareFile(context.Background(), playback.TranscodeOpts{}, filepath.Join(cfg.Download.ArtifactDir, "job-4.mp4")); err != nil {
		t.Fatal(err)
	}
	if local.calls != 1 {
		t.Fatalf("local calls = %d, want 1", local.calls)
	}
}

func TestNodeAwarePreparerPromotesCompletedOutputAfterResponseLoss(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{URL: "http://idle", Enabled: true, Healthy: true}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	cfg.Download.ArtifactDir = t.TempDir()
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = responseLostRemotePreparer{}
	outputPath := filepath.Join(cfg.Download.ArtifactDir, "job-5.mp4")

	if err := p.PrepareFile(context.Background(), playback.TranscodeOpts{}, outputPath); err != nil {
		t.Fatal(err)
	}
	if local.calls != 0 {
		t.Fatalf("local calls = %d, want 0", local.calls)
	}
	if got, err := os.ReadFile(outputPath); err != nil || string(got) != "prepared" {
		t.Fatalf("promoted output = %q, %v", got, err)
	}
}

func TestNodeAwarePreparerDoesNotFallBackAfterLeaseCancellation(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{URL: "http://idle", Enabled: true, Healthy: true}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	cfg.Download.ArtifactDir = t.TempDir()
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = unavailableRemotePreparer{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.PrepareFile(ctx, playback.TranscodeOpts{}, filepath.Join(cfg.Download.ArtifactDir, "job-6.mp4"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if local.calls != 0 {
		t.Fatalf("local calls = %d, want 0", local.calls)
	}
}

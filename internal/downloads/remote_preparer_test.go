package downloads

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

type recordingEncodePreparer struct{ calls int }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (p *recordingEncodePreparer) PrepareFile(_ context.Context, _ string, _ playback.TranscodeOpts, outputPath string) (PreparedArtifact, error) {
	p.calls++
	return PreparedArtifact{OutputPath: outputPath, FileSize: 8}, nil
}

type staticDownloadSettings map[string]string

// GetAll returns the fixed settings used by a preparer test.
func (s staticDownloadSettings) GetAll(context.Context) (map[string]string, error) {
	return s, nil
}

type failingDownloadSettings struct{ err error }

func (s failingDownloadSettings) GetAll(context.Context) (map[string]string, error) {
	return nil, s.err
}

type recordingRemotePreparer struct {
	nodeURL   string
	secret    string
	request   downloadprepare.Request
	deleted   string
	deleteURL string
}

type nilCandidatePlanner struct {
	invoked bool
}

type nonReservingCapacityPlanner struct {
	node            *nodepool.Node
	reservationUsed bool
}

func (p *nonReservingCapacityPlanner) ReserveTranscodeWork(string) (*nodepool.Node, func()) {
	return nil, nil
}

func (p *nonReservingCapacityPlanner) ReserveTranscodeWorkWith(string, func(*nodepool.Node) bool) (*nodepool.Node, func()) {
	p.reservationUsed = true
	return nil, nil
}

func (p *nonReservingCapacityPlanner) TranscodeWorkAvailableWith(eligible func(*nodepool.Node) bool) bool {
	return eligible(p.node)
}

func (p *nonReservingCapacityPlanner) TranscodeNode(int) (*nodepool.Node, bool) {
	return nil, false
}

func (p *nonReservingCapacityPlanner) TranscodeNodeURLs() []string {
	return []string{p.node.URL}
}

func (p *nilCandidatePlanner) ReserveTranscodeWork(string) (*nodepool.Node, func()) {
	return nil, nil
}

func (p *nilCandidatePlanner) ReserveTranscodeWorkWith(_ string, eligible func(*nodepool.Node) bool) (*nodepool.Node, func()) {
	p.invoked = true
	if eligible(nil) {
		panic("nil transcode node was eligible")
	}
	return nil, nil
}

func (p *nilCandidatePlanner) TranscodeNode(int) (*nodepool.Node, bool) {
	return nil, false
}

func (p *recordingRemotePreparer) Prepare(_ context.Context, nodeURL, secret string, req downloadprepare.Request) (downloadprepare.Result, error) {
	p.nodeURL, p.secret, p.request = nodeURL, secret, req
	return downloadprepare.Result{ArtifactID: req.ArtifactID, FileSize: 1234, ExecutionFingerprint: req.ExecutionFingerprint()}, nil
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

type attestationRemotePreparer struct {
	prepareResult downloadprepare.Result
	prepareErr    error
	statResult    downloadprepare.Result
	statErr       error
	deleteErr     error
	deletes       int
}

func (p *attestationRemotePreparer) Prepare(context.Context, string, string, downloadprepare.Request) (downloadprepare.Result, error) {
	return p.prepareResult, p.prepareErr
}

func (p *attestationRemotePreparer) Stat(context.Context, string, string, string) (downloadprepare.Result, error) {
	return p.statResult, p.statErr
}

func (p *attestationRemotePreparer) Delete(context.Context, string, string, string) error {
	p.deletes++
	return p.deleteErr
}

func newToneMapPreparerTest(t *testing.T, remote downloadprepare.RemotePreparer) (*NodeAwarePreparer, *recordingEncodePreparer, playback.TranscodeOpts) {
	t.Helper()
	const nodeURL = "http://tone-map-node"
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{ID: 22, URL: nodeURL, Enabled: true, Healthy: true}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	preparer := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	preparer.remote = remote
	preparer.capabilities[nodeURL] = remoteToneMapCapabilities{
		capabilities: tonemap.Capabilities{{
			Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
			SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
		}},
		expiresAt: time.Now().Add(time.Minute),
	}
	opts := playback.TranscodeOpts{
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 42, FileSize: 100, StreamSignature: "stream"},
	}
	return preparer, local, opts
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

func TestNodeAwarePreparerRequiresAudioToAACV2ForSurroundDownmix(t *testing.T) {
	capabilityNode := func(recipeVersion string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{Transformations: []playback.TransformationV3{{
				Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: recipeVersion,
			}}})
		}))
	}
	legacy := capabilityNode("1")
	defer legacy.Close()
	current := capabilityNode(playback.TransformationAudioToAACRecipeVersionV3)
	defer current.Close()

	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{
		{ID: 1, URL: legacy.URL, Enabled: true, Healthy: true},
		{ID: 2, URL: current.URL, Enabled: true, Healthy: true, ActiveJobs: 1},
	})
	local := &recordingEncodePreparer{}
	remote := &recordingRemotePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.remote = remote
	opts := playback.TranscodeOpts{
		InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac", SourceAudioChannels: 6,
	}
	prepared, err := p.PrepareFile(context.Background(), "artifact-audio-v2", opts, "/local/artifact.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if local.calls != 0 || remote.nodeURL != current.URL {
		t.Fatalf("local calls = %d, remote node = %q, want v2 node %q", local.calls, remote.nodeURL, current.URL)
	}
	if remote.request.SourceAudioChannels != 6 || prepared.OriginNodeID != 2 {
		t.Fatalf("remote request = %#v prepared = %#v", remote.request, prepared)
	}
}

func TestNodeAwarePreparerRejectsUnattestedOrMismatchedToneMapPrepareResult(t *testing.T) {
	revision := tonemap.SourceRevision{MediaFileID: 42, FileSize: 100, StreamSignature: "stream"}
	valid := downloadprepare.Result{
		ArtifactID:                       "artifact-tone-map",
		FileSize:                         55,
		ToneMapRecipeVersion:             playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapMode:                      tonemap.ModeSoftware,
		ToneMapSourceRevisionFingerprint: revision.Fingerprint(),
	}
	tests := []struct {
		name   string
		result downloadprepare.Result
	}{
		{name: "empty artifact id", result: downloadprepare.Result{FileSize: 55}},
		{name: "old node omits receipt", result: downloadprepare.Result{ArtifactID: "artifact-tone-map", FileSize: 55}},
		{name: "recipe version", result: func() downloadprepare.Result { result := valid; result.ToneMapRecipeVersion = "stale"; return result }()},
		{name: "mode", result: func() downloadprepare.Result {
			result := valid
			result.ToneMapMode = tonemap.ModeHardware
			return result
		}()},
		{name: "source revision", result: func() downloadprepare.Result {
			result := valid
			result.ToneMapSourceRevisionFingerprint = "wrong"
			return result
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &attestationRemotePreparer{prepareResult: test.result, statErr: downloadprepare.ErrArtifactNotFound}
			preparer, local, opts := newToneMapPreparerTest(t, remote)
			prepared, err := preparer.PrepareFile(context.Background(), "artifact-tone-map", opts, "/local/artifact.mp4")
			if err != nil || prepared.OutputPath == "" || local.calls != 1 {
				t.Fatalf("prepared=%+v err=%v local calls=%d, want local fallback", prepared, err, local.calls)
			}
			if remote.deletes != 1 {
				t.Fatalf("remote deletes = %d, want rejected artifact cleanup", remote.deletes)
			}
		})
	}
}

func TestNodeAwarePreparerRejectsUnattestedOrMismatchedToneMapRecovery(t *testing.T) {
	revision := tonemap.SourceRevision{MediaFileID: 42, FileSize: 100, StreamSignature: "stream"}
	tests := []struct {
		name      string
		recovered downloadprepare.Result
	}{
		{name: "missing receipt", recovered: downloadprepare.Result{ArtifactID: "artifact-tone-map", FileSize: 55}},
		{name: "mismatched receipt", recovered: downloadprepare.Result{
			ArtifactID: "artifact-tone-map", FileSize: 55,
			ToneMapRecipeVersion:             playback.TransformationHDRToSDRToneMapRecipeVersionV3,
			ToneMapMode:                      tonemap.ModeSoftware,
			ToneMapSourceRevisionFingerprint: revision.Fingerprint() + "-wrong",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &attestationRemotePreparer{
				prepareErr: context.DeadlineExceeded,
				statResult: test.recovered,
			}
			preparer, local, opts := newToneMapPreparerTest(t, remote)
			prepared, err := preparer.PrepareFile(context.Background(), "artifact-tone-map", opts, "/local/artifact.mp4")
			if err != nil || prepared.OutputPath == "" || local.calls != 1 {
				t.Fatalf("prepared=%+v err=%v local calls=%d, want local fallback", prepared, err, local.calls)
			}
			if remote.deletes != 1 {
				t.Fatalf("remote deletes = %d, want rejected recovery cleanup", remote.deletes)
			}
		})
	}
}

func TestNodeAwarePreparerRecoversExactlyAttestedToneMapArtifactAfterResponseLoss(t *testing.T) {
	remote := &attestationRemotePreparer{prepareErr: context.DeadlineExceeded}
	preparer, local, opts := newToneMapPreparerTest(t, remote)
	remote.statResult = downloadprepare.Result{
		ArtifactID:                       "artifact-tone-map",
		FileSize:                         55,
		ToneMapRecipeVersion:             opts.ToneMapRecipeVersion,
		ToneMapMode:                      opts.ToneMapMode,
		ToneMapSourceRevisionFingerprint: opts.ToneMapSourceRevision.Fingerprint(),
		ExecutionFingerprint:             downloadprepare.NewRequest("artifact-tone-map", opts).ExecutionFingerprint(),
	}

	prepared, err := preparer.PrepareFile(context.Background(), "artifact-tone-map", opts, "/local/artifact.mp4")
	if err != nil || !prepared.Remote() || prepared.FileSize != 55 {
		t.Fatalf("prepared=%+v err=%v, want recovered remote artifact", prepared, err)
	}
	if local.calls != 0 || remote.deletes != 0 {
		t.Fatalf("local calls=%d remote deletes=%d, want neither", local.calls, remote.deletes)
	}
}

func TestRemotePrepareResultRejectsPartialToneMapRecipeWithoutAttestation(t *testing.T) {
	opts := playback.TranscodeOpts{
		ToneMapPolicy:        tonemap.PolicySoftwareOnly,
		ToneMapSourceKind:    tonemap.SourcePQ,
		ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.SourceRevision{
			MediaFileID: 42,
			FileSize:    100,
		},
	}
	if remotePrepareResultMatches(downloadprepare.Result{ArtifactID: "artifact-partial", FileSize: 55}, "artifact-partial", downloadprepare.NewRequest("artifact-partial", opts)) {
		t.Fatal("partial tone-map recipe accepted an unattested result")
	}
}

func TestRemotePrepareResultRejectsPartialAudioRecipeWithoutAttestation(t *testing.T) {
	partial := downloadprepare.Request{
		ArtifactID: "artifact-partial-audio", TargetCodecAudio: "eac3",
		SourceAudioChannels: 6, AudioRecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
	}
	if remotePrepareResultMatches(downloadprepare.Result{ArtifactID: partial.ArtifactID, FileSize: 55}, partial.ArtifactID, partial) {
		t.Fatal("partial audio recipe accepted an unattested result")
	}
}

func TestRemotePrepareResultRequiresAttestationForExplicitAudioOutput(t *testing.T) {
	request := downloadprepare.NewRequest("artifact-explicit-audio", playback.TranscodeOpts{
		TargetCodecAudio: "aac", SourceAudioChannels: 2,
		TargetAudioChannels: 1, TargetAudioBitrateKbps: 256,
	})
	unattested := downloadprepare.Result{ArtifactID: request.ArtifactID, FileSize: 55}
	if remotePrepareResultMatches(unattested, request.ArtifactID, request) {
		t.Fatal("explicit audio output accepted an unattested mixed-generation result")
	}
	attested := unattested
	attested.ExecutionFingerprint = request.ExecutionFingerprint()
	if !remotePrepareResultMatches(attested, request.ArtifactID, request) {
		t.Fatal("explicit audio output rejected its exact execution receipt")
	}
}

func TestNodeAwarePreparerDoesNotDispatchPartialToneMapRecipeAsOrdinary(t *testing.T) {
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{ID: 23, URL: "http://ordinary-node", Enabled: true, Healthy: true}})
	local := &recordingEncodePreparer{}
	remote := &recordingRemotePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	preparer := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	preparer.remote = remote
	preparer.capabilities["http://ordinary-node"] = remoteToneMapCapabilities{
		capabilities: tonemap.Capabilities{{
			Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
			SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
		}},
		expiresAt: time.Now().Add(time.Minute),
	}
	opts := playback.TranscodeOpts{ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3}

	prepared, err := preparer.PrepareFile(context.Background(), "artifact-partial", opts, "/local/artifact.mp4")
	if err != nil || prepared.OutputPath == "" || local.calls != 1 {
		t.Fatalf("prepared=%+v err=%v local calls=%d, want local handling", prepared, err, local.calls)
	}
	if remote.request.ArtifactID != "" {
		t.Fatalf("partial recipe was dispatched as ordinary remote request: %+v", remote.request)
	}
}

func TestNodeAwarePreparerPreservesIndeterminateToneMapResultWhenCleanupFails(t *testing.T) {
	remote := &attestationRemotePreparer{
		prepareResult: downloadprepare.Result{ArtifactID: "artifact-tone-map", FileSize: 55},
		statErr:       downloadprepare.ErrArtifactNotFound,
		deleteErr:     os.ErrPermission,
	}
	preparer, local, opts := newToneMapPreparerTest(t, remote)
	prepared, err := preparer.PrepareFile(context.Background(), "artifact-tone-map", opts, "/local/artifact.mp4")
	if err == nil || !prepared.Remote() || prepared.OriginArtifactID != "artifact-tone-map" {
		t.Fatalf("prepared=%+v err=%v, want indeterminate remote result", prepared, err)
	}
	if local.calls != 0 {
		t.Fatalf("local calls = %d, want no fallback after failed cleanup", local.calls)
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

func TestNodeAwarePreparerRejectsNilToneMapCandidate(t *testing.T) {
	planner := &nilCandidatePlanner{}
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	p := NewNodeAwarePreparer(local, planner, func() *config.Config { return cfg })

	prepared, err := p.PrepareFile(context.Background(), "artifact-nil-candidate", playback.TranscodeOpts{
		ToneMapMode: tonemap.ModeSoftware, ToneMapSourceKind: tonemap.SourcePQ,
	}, "/artifacts/job.mp4")
	if err != nil || prepared.OutputPath == "" || local.calls != 1 {
		t.Fatalf("prepared=%+v err=%v local calls=%d", prepared, err, local.calls)
	}
	if !planner.invoked {
		t.Fatal("eligible planner was not invoked")
	}
}

// TestNodeAwarePreparerHonorsDisabledLocalFallback verifies policy can forbid local retry.
func TestNodeAwarePreparerHonorsDisabledLocalFallback(t *testing.T) {
	limit := 1
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{URL: "http://full", Enabled: true, Healthy: true, ActiveJobs: 1, MaxJobs: &limit}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	p := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	p.SetSettingsReader(staticDownloadSettings{config.PlaybackLocalTranscodeFallbackSettingKey: "false"})

	if _, err := p.PrepareFile(context.Background(), "artifact-no-fallback", playback.TranscodeOpts{}, "/artifacts/job.mp4"); err == nil {
		t.Fatal("expected unavailable-node error with local fallback disabled")
	}
	if local.calls != 0 {
		t.Fatalf("local calls = %d, want 0", local.calls)
	}
}

func TestNodeAwarePreparerSettingsFailureDeniesLocalFallback(t *testing.T) {
	limit := 1
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{URL: "http://full", Enabled: true, Healthy: true, ActiveJobs: 1, MaxJobs: &limit}})
	local := &recordingEncodePreparer{}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	preparer := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	preparer.SetSettingsReader(failingDownloadSettings{err: errors.New("settings unavailable")})

	_, err := preparer.PrepareFile(context.Background(), "artifact-settings-unavailable", playback.TranscodeOpts{}, "/artifacts/job.mp4")
	if err == nil || !strings.Contains(err.Error(), "local transcode fallback is disabled") {
		t.Fatalf("PrepareFile error = %v, want local fallback denied", err)
	}
	if local.calls != 0 {
		t.Fatalf("local calls = %d, want 0 when fallback policy cannot be read", local.calls)
	}
}

func TestNodeAwarePreparerReportsSoftwareCapacityWhenHardwareIsFull(t *testing.T) {
	limit := 1
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{
		{URL: "http://hardware", Enabled: true, Healthy: true, ActiveJobs: 1, MaxJobs: &limit},
		{URL: "http://software", Enabled: true, Healthy: true, MaxJobs: &limit},
	})
	preparer := NewNodeAwarePreparer(nil, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), nil)
	preparer.capabilities = map[string]remoteToneMapCapabilities{
		"http://hardware": {
			capabilities: tonemap.Capabilities{{
				Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV,
				SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
			}},
			expiresAt: time.Now().Add(time.Minute),
		},
		"http://software": {
			capabilities: tonemap.Capabilities{{
				Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
				SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
			}},
			expiresAt: time.Now().Add(time.Minute),
		},
	}

	hardwareAvailable, err := preparer.ToneMapModeAvailable(context.Background(), tonemap.ModeHardware, tonemap.SourcePQ)
	if err != nil || hardwareAvailable {
		t.Fatalf("hardware availability = %t, %v; want false", hardwareAvailable, err)
	}
	softwareAvailable, err := preparer.ToneMapModeAvailable(context.Background(), tonemap.ModeSoftware, tonemap.SourcePQ)
	if err != nil || !softwareAvailable {
		t.Fatalf("software availability = %t, %v; want true", softwareAvailable, err)
	}
}

func TestToneMapModeAvailableDoesNotReserveCapacity(t *testing.T) {
	node := &nodepool.Node{URL: "http://software", Enabled: true, Healthy: true}
	planner := &nonReservingCapacityPlanner{node: node}
	preparer := NewNodeAwarePreparer(nil, planner, nil)
	preparer.capabilities = map[string]remoteToneMapCapabilities{
		node.URL: {
			capabilities: tonemap.Capabilities{{
				Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
				SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
			}},
			expiresAt: time.Now().Add(time.Minute),
		},
	}

	available, err := preparer.ToneMapModeAvailable(context.Background(), tonemap.ModeSoftware, tonemap.SourcePQ)
	if err != nil || !available {
		t.Fatalf("software availability = %t, %v; want true", available, err)
	}
	if planner.reservationUsed {
		t.Fatal("capacity probe created a transcode reservation")
	}
}

// TestNodeAwarePreparerCollectsToneMapCapabilitiesConcurrently verifies node discovery overlaps.
func TestNodeAwarePreparerCollectsToneMapCapabilitiesConcurrently(t *testing.T) {
	var active atomic.Int32
	var once sync.Once
	bothStarted := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if active.Add(1) == 2 {
			once.Do(func() { close(bothStarted) })
		}
		<-release
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{ToneMapCapabilities: tonemap.Capabilities{{
			Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
			SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
		}}})
	})
	first := httptest.NewServer(handler)
	defer first.Close()
	second := httptest.NewServer(handler)
	defer second.Close()
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{
		{URL: first.URL, Enabled: true, Healthy: true},
		{URL: second.URL, Enabled: true, Healthy: true},
	})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	preparer := NewNodeAwarePreparer(nil, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	type capabilityResult struct {
		capabilities tonemap.Capabilities
		err          error
	}
	result := make(chan capabilityResult, 1)
	go func() {
		capabilities, err := preparer.ToneMapCapabilities(context.Background())
		result <- capabilityResult{capabilities: capabilities, err: err}
	}()
	select {
	case <-bothStarted:
		close(release)
	case <-time.After(time.Second):
		close(release)
		t.Fatal("prepared-download node probes did not overlap")
	}
	got := <-result
	if got.err != nil {
		t.Fatalf("capability inventory error = %v", got.err)
	}
	if len(got.capabilities) != 2 {
		t.Fatalf("capabilities = %#v, want both nodes", got.capabilities)
	}
}

func TestNodeAwarePreparerUsesTargetNodeProbeBudget(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("request method = %q, want GET", request.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.URL.Path != "/hw-capabilities" {
			t.Errorf("request path = %q, want /hw-capabilities", request.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got, want := request.Header.Get("Authorization"), "Bearer secret"; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{
			ProbeRequestTimeoutMillis: (161 * time.Second).Milliseconds(),
			ToneMapCapabilities: tonemap.Capabilities{{
				Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
				SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
			}},
		})
	}))
	defer remote.Close()
	secondTimeout := make(chan time.Duration, 1)
	releaseSecond := make(chan struct{})
	var requests atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if requests.Add(1) == 2 {
			deadline, _ := request.Context().Deadline()
			secondTimeout <- time.Until(deadline)
			select {
			case <-releaseSecond:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}
		return http.DefaultTransport.RoundTrip(request)
	})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	cfg.Playback.HWAccel = tonemap.BackendQSV
	cfg.Playback.HWDevice = "/central/device"
	preparer := NewNodeAwarePreparer(nil, nil, func() *config.Config { return cfg })
	preparer.probeClient = &http.Client{Transport: transport}

	if got, want := preparer.remoteToneMapProbeTimeout(remote.URL), playback.CapabilityRequestTimeout(tonemap.BackendQSV, "/central/device"); got != want {
		t.Fatalf("unknown-node probe timeout = %s, want configured probe budget %s", got, want)
	}
	if _, err := preparer.toneMapCapabilitiesForNode(context.Background(), remote.URL); err != nil {
		t.Fatal(err)
	}
	if got, want := preparer.remoteToneMapProbeTimeout(remote.URL), 161*time.Second; got != want {
		t.Fatalf("cached remote probe timeout = %s, want target-node budget %s", got, want)
	}
	key := strings.TrimRight(remote.URL, "/")
	preparer.capabilityMu.Lock()
	entry := preparer.capabilities[key]
	entry.expiresAt = time.Now().Add(-time.Second)
	preparer.capabilities[key] = entry
	preparer.capabilityMu.Unlock()
	probeResult := make(chan error, 1)
	go func() {
		_, err := preparer.toneMapCapabilitiesForNode(context.Background(), remote.URL)
		probeResult <- err
	}()
	var remaining time.Duration
	select {
	case remaining = <-secondTimeout:
	case <-time.After(time.Second):
		close(releaseSecond)
		t.Fatal("second capability request did not start")
	}
	if remaining <= 160*time.Second || remaining > 161*time.Second {
		close(releaseSecond)
		t.Fatalf("second request timeout = %s, want cached 161s budget", remaining)
	}
	close(releaseSecond)
	if err := <-probeResult; err != nil {
		t.Fatal(err)
	}
	if got, want := preparer.ToneMapCapabilityTimeout(), 5*time.Minute; got != want {
		t.Fatalf("remote-only planning timeout = %s, want %s", got, want)
	}
}

func TestNormalizeRemoteToneMapProbeTimeout(t *testing.T) {
	for _, test := range []struct {
		name   string
		millis int64
		want   time.Duration
	}{
		{name: "missing", want: 5 * time.Second},
		{name: "too small", millis: time.Second.Milliseconds(), want: 5 * time.Second},
		{name: "node specific", millis: (161 * time.Second).Milliseconds(), want: 161 * time.Second},
		{
			// The ceiling is derived from the probe formula, not picked, so the
			// expectation is too — a round number was already below what a
			// nine-device node legitimately advertises.
			name:   "too large",
			millis: (24 * time.Hour).Milliseconds(),
			want:   playback.MaxCapabilityRequestTimeout(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeRemoteToneMapProbeTimeout(test.millis); got != test.want {
				t.Fatalf("normalized timeout = %s, want %s", got, test.want)
			}
		})
	}
}

func TestNodeAwarePreparerCapabilityFailurePreservesNodeProbeBudget(t *testing.T) {
	const nodeURL = "https://node.example"
	preparer := NewNodeAwarePreparer(nil, nil, nil)
	preparer.capabilities[nodeURL] = remoteToneMapCapabilities{
		probeRequestTimeout: 161 * time.Second,
		expiresAt:           time.Now().Add(-time.Second),
	}

	preparer.cacheToneMapCapabilityFailure(nodeURL, preparer.capabilityInvalidationsFor(nodeURL), context.DeadlineExceeded)

	if got, want := preparer.remoteToneMapProbeTimeout(nodeURL), 161*time.Second; got != want {
		t.Fatalf("probe timeout after transient failure = %s, want preserved node budget %s", got, want)
	}
}

func TestNodeAwarePreparerDerivesProbeBudgetWithoutCachedAdvertisement(t *testing.T) {
	preparer := NewNodeAwarePreparer(nil, nil, nil)

	if got, want := preparer.remoteToneMapProbeTimeout("https://node.example"), playback.CapabilityRequestTimeout("", ""); got != want {
		t.Fatalf("uncached probe timeout = %s, want derived budget %s", got, want)
	}
}

// TestNodeAwarePreparerCachesCapabilityFailuresBriefly verifies transient failures use a bounded cache.
func TestNodeAwarePreparerCachesCapabilityFailuresBriefly(t *testing.T) {
	var hits atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	preparer := NewNodeAwarePreparer(nil, nil, func() *config.Config { return cfg })
	if _, err := preparer.toneMapCapabilitiesForNode(context.Background(), remote.URL); err == nil {
		t.Fatal("initial failed capability request returned no error")
	}
	if got, err := preparer.toneMapCapabilitiesForNode(context.Background(), remote.URL); err == nil || len(got) != 0 {
		t.Fatalf("cached failure = %#v, %v; want the transient error preserved", got, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("requests = %d, want one during the failure TTL", hits.Load())
	}
	key := strings.TrimRight(remote.URL, "/")
	preparer.capabilityMu.Lock()
	entry := preparer.capabilities[key]
	entry.expiresAt = time.Now().Add(-time.Second)
	preparer.capabilities[key] = entry
	preparer.capabilityMu.Unlock()
	if _, err := preparer.toneMapCapabilitiesForNode(context.Background(), remote.URL); err == nil {
		t.Fatal("expired failure did not trigger a fresh request")
	}
	if hits.Load() != 2 {
		t.Fatalf("requests = %d, want a retry after failure-cache expiry", hits.Load())
	}
}

func TestNodeAwarePreparerCapabilityErrorsRedactNodeURLSecrets(t *testing.T) {
	const (
		username       = "probe-operator"
		password       = "node-password"
		querySecret    = "query-secret"
		fragmentSecret = "fragment-secret"
	)
	nodeURL := "https://" + username + ":" + password + "@node.example:9443/transcode?access_token=" + querySecret + "#" + fragmentSecret
	planner := &nonReservingCapacityPlanner{node: &nodepool.Node{URL: nodeURL}}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "jwt-secret"
	preparer := NewNodeAwarePreparer(nil, planner, func() *config.Config { return cfg })
	preparer.probeClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}

	_, err := preparer.toneMapCapabilitiesByNode(context.Background())
	if err == nil {
		t.Fatal("capability probe returned no error")
	}
	message := err.Error()
	for _, secret := range []string{username, password, querySecret, fragmentSecret} {
		if strings.Contains(message, secret) {
			t.Fatalf("capability error contains %q: %q", secret, message)
		}
	}
	if !strings.Contains(message, "https://node.example:9443/transcode") || !strings.Contains(message, "connection refused") {
		t.Fatalf("capability error lost useful diagnostics: %q", message)
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
	if err != nil || prepared.OutputPath == "" {
		t.Fatalf("prepared=%+v err=%v, want local fallback after cleanup", prepared, err)
	}
	if local.calls != 1 {
		t.Fatalf("local calls = %d, want fallback after rejected remote result cleanup", local.calls)
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

// nodeLookupPlanner is a planner that can resolve a node by URL, as the real
// one does. It reserves nothing: these tests only price probes.
type nodeLookupPlanner struct {
	node *nodepool.Node
}

func (p *nodeLookupPlanner) ReserveTranscodeWork(string) (*nodepool.Node, func()) {
	return nil, func() {}
}

func (p *nodeLookupPlanner) TranscodeNode(int) (*nodepool.Node, bool) { return nil, false }

func (p *nodeLookupPlanner) TranscodeNodeByURL(nodeURL string) (*nodepool.Node, bool) {
	if p.node == nil || p.node.URL != nodeURL {
		return nil, false
	}
	return p.node, true
}

// The cluster setting describes the cluster. A node overridden onto four render
// devices walks four of them cold, and pricing that walk at the cluster's single
// device cancels it partway — which drops the node from the capability map and
// sends the download local, or fails it where local fallback is off.
func TestNodeAwarePreparerColdProbeBudgetFollowsTheNodeOverride(t *testing.T) {
	const nodeURL = "https://node.example"
	devices := "/dev/dri/renderD128,/dev/dri/renderD129,/dev/dri/renderD130,/dev/dri/renderD131"
	backend := tonemap.BackendQSV
	planner := &nodeLookupPlanner{node: &nodepool.Node{
		ID: 1, URL: nodeURL, HWAccelOverride: &backend, HWDeviceOverride: &devices,
	}}
	cfg := &config.Config{}
	cfg.Playback.HWAccel = tonemap.BackendQSV
	cfg.Playback.HWDevice = "/dev/dri/renderD128"
	preparer := NewNodeAwarePreparer(nil, planner, func() *config.Config { return cfg })

	want := playback.CapabilityRequestTimeout(backend, devices)
	if got := preparer.remoteToneMapProbeTimeout(nodeURL); got != want {
		t.Fatalf("cold probe timeout = %s, want the node's four-device budget %s", got, want)
	}
	if cluster := playback.CapabilityRequestTimeout(cfg.Playback.HWAccel, cfg.Playback.HWDevice); want <= cluster {
		t.Fatalf("fixture is inert: the override budget %s must exceed the cluster's %s", want, cluster)
	}
}

// What the node last advertised is its own measurement of its own matrix, and it
// survives an API restart because it is stored with the report — so where it
// exceeds what this replica can price, it is the answer.
func TestNodeAwarePreparerColdProbeBudgetTakesTheStoredAdvertisement(t *testing.T) {
	const nodeURL = "https://node.example"
	planner := &nodeLookupPlanner{node: &nodepool.Node{
		ID: 1, URL: nodeURL,
		Capabilities: json.RawMessage(`{"resolved":"qsv","probe_request_timeout_ms":161000}`),
	}}
	cfg := &config.Config{}
	preparer := NewNodeAwarePreparer(nil, planner, func() *config.Config { return cfg })

	if got, want := preparer.remoteToneMapProbeTimeout(nodeURL), 161*time.Second; got != want {
		t.Fatalf("cold probe timeout = %s, want the advertised %s", got, want)
	}
}

// A budget learned before an operator widened the node's device set describes
// the node as it was. Keeping it would cancel every cold retry at the old
// one-device deadline — and a budget is only ever learned from a read that
// completes, so nothing would replace it.
func TestNodeAwarePreparerRepricesALearnedBudgetAfterTheDeviceSetGrows(t *testing.T) {
	const nodeURL = "https://node.example"
	devices := "/dev/dri/renderD128,/dev/dri/renderD129,/dev/dri/renderD130,/dev/dri/renderD131"
	backend := tonemap.BackendQSV
	planner := &nodeLookupPlanner{node: &nodepool.Node{
		ID: 1, URL: nodeURL, HWAccelOverride: &backend, HWDeviceOverride: &devices,
	}}
	cfg := &config.Config{}
	cfg.Playback.HWAccel = tonemap.BackendQSV
	cfg.Playback.HWDevice = "/dev/dri/renderD128"
	preparer := NewNodeAwarePreparer(nil, planner, func() *config.Config { return cfg })
	// What the node advertised while it was still on one device.
	learned := playback.CapabilityRequestTimeout(backend, "/dev/dri/renderD128")
	preparer.capabilities[nodeURL] = remoteToneMapCapabilities{
		probeRequestTimeout: learned,
		expiresAt:           time.Now().Add(-time.Second),
	}

	want := playback.CapabilityRequestTimeout(backend, devices)
	if got := preparer.remoteToneMapProbeTimeout(nodeURL); got != want {
		t.Fatalf("probe timeout after the override grew = %s, want the four-device %s", got, want)
	}
	if want <= learned {
		t.Fatalf("fixture is inert: the four-device budget %s must exceed the learned %s", want, learned)
	}
}

// The other direction: a node whose own measurement exceeds what this replica
// can price for it keeps that measurement. An API replica has none of the node's
// cards, so its pricing is a floor rather than the truth.
func TestNodeAwarePreparerKeepsALearnedBudgetLargerThanThePolicyPrice(t *testing.T) {
	const nodeURL = "https://node.example"
	planner := &nodeLookupPlanner{node: &nodepool.Node{ID: 1, URL: nodeURL}}
	cfg := &config.Config{}
	preparer := NewNodeAwarePreparer(nil, planner, func() *config.Config { return cfg })
	learned := playback.MaxCapabilityRequestTimeout()
	preparer.capabilities[nodeURL] = remoteToneMapCapabilities{
		probeRequestTimeout: learned,
		expiresAt:           time.Now().Add(-time.Second),
	}

	if got := preparer.remoteToneMapProbeTimeout(nodeURL); got != learned {
		t.Fatalf("probe timeout = %s, want the node's own larger measurement %s", got, learned)
	}
}

// A policy edit or a capability-hash change makes this cache wrong the moment it
// lands. Left for its TTL, a download planned from it selects the node for a
// tone-map executor it no longer has, and the reconfigured worker rejects the
// recipe or the download falls back locally for no reason.
func TestNodeAwarePreparerInvalidateNodeCapabilitiesDropsTheInventory(t *testing.T) {
	const nodeURL = "https://node.example"
	preparer := NewNodeAwarePreparer(nil, nil, nil)
	preparer.capabilities[nodeURL] = remoteToneMapCapabilities{
		capabilities:        tonemap.Capabilities{{Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV}},
		probeRequestTimeout: 161 * time.Second,
		expiresAt:           time.Now().Add(time.Minute),
	}

	// The stored URL carries a trailing slash the pools have already dropped; it
	// still has to reach the entry planning reads.
	preparer.InvalidateNodeCapabilities(nodeURL + "/")

	entry := preparer.capabilities[nodeURL]
	if len(entry.capabilities) != 0 || !entry.expiresAt.IsZero() {
		t.Fatalf("inventory survived the invalidation: %+v", entry)
	}
	// The budget describes how long the node takes to answer, which a policy
	// change does not alter — and the read this invalidation triggers is the
	// cold one that most needs the real number.
	if entry.probeRequestTimeout != 161*time.Second {
		t.Fatalf("probe budget = %s, want the learned 161s preserved", entry.probeRequestTimeout)
	}
}

// A policy edit invalidates while planning is already asking the node. The
// answer in flight describes the node before the edit, so installing it would
// restore the pre-edit inventory for a full TTL — and downloads would keep
// selecting a tone-map executor the reconfigured worker no longer has.
func TestNodeAwarePreparerDoesNotCacheAnOvertakenCapabilityFetch(t *testing.T) {
	var hits atomic.Int32
	invalidated := make(chan struct{})
	released := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			// The edit lands while this request is still open.
			close(invalidated)
			<-released
		}
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{
			ToneMapCapabilities: tonemap.Capabilities{{
				Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware,
				Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
			}},
		})
	}))
	defer remote.Close()
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	preparer := NewNodeAwarePreparer(nil, nil, func() *config.Config { return cfg })

	fetched := make(chan error, 1)
	go func() {
		_, err := preparer.toneMapCapabilitiesForNode(context.Background(), remote.URL)
		fetched <- err
	}()
	<-invalidated
	preparer.InvalidateNodeCapabilities(remote.URL)
	close(released)
	if err := <-fetched; err != nil {
		t.Fatalf("the caller waiting on the fetch got an error: %v", err)
	}

	// Nothing durable was written, so the next planning pass asks the node again
	// rather than reading the overtaken answer.
	key := nodepool.NormalizeNodeURL(remote.URL)
	preparer.capabilityMu.Lock()
	entry, cached := preparer.capabilities[key]
	preparer.capabilityMu.Unlock()
	if cached && len(entry.capabilities) > 0 {
		t.Fatalf("an overtaken fetch repopulated the cache: %+v", entry)
	}
	if _, err := preparer.toneMapCapabilitiesForNode(context.Background(), remote.URL); err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("node was asked %d times, want a second read after the invalidation", hits.Load())
	}
}

// A negative entry does not merely go stale, it takes the node out of planning
// for its TTL. A fetch failing because the node was mid-reload — exactly what a
// policy edit causes — would otherwise keep downloads off the node that edit had
// just reconfigured, after the change that would have fixed it already landed.
func TestNodeAwarePreparerDoesNotCacheAnOvertakenCapabilityFailure(t *testing.T) {
	var hits atomic.Int32
	invalidated := make(chan struct{})
	released := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			close(invalidated)
			<-released
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{
			ToneMapCapabilities: tonemap.Capabilities{{
				Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware,
				Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
			}},
		})
	}))
	defer remote.Close()
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	preparer := NewNodeAwarePreparer(nil, nil, func() *config.Config { return cfg })

	failed := make(chan error, 1)
	go func() {
		_, err := preparer.toneMapCapabilitiesForNode(context.Background(), remote.URL)
		failed <- err
	}()
	<-invalidated
	preparer.InvalidateNodeCapabilities(remote.URL)
	close(released)
	if err := <-failed; err == nil {
		t.Fatal("the node answered 503 and the caller saw no error")
	}

	// The reconfigured node is asked again rather than sitting behind a negative
	// entry the invalidation should have outranked.
	capabilities, err := preparer.toneMapCapabilitiesForNode(context.Background(), remote.URL)
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if len(capabilities) != 1 {
		t.Fatalf("capabilities = %#v, want the reconfigured node's answer", capabilities)
	}
	if hits.Load() != 2 {
		t.Fatalf("node was asked %d times, want a second read after the invalidation", hits.Load())
	}
}

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

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodepool/nodepooltest"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/nodesource/nodesourcetest"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
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

func (p *recordingRemotePreparer) Prepare(_ context.Context, nodeURL string, keys nodeauth.Keyring, req downloadprepare.Request) (downloadprepare.Result, error) {
	p.nodeURL, p.request = nodeURL, req
	p.secret, _ = keys.KeyFor(nodeauth.TargetTranscode, nodeURL)
	return downloadprepare.Result{ArtifactID: req.ArtifactID, FileSize: 1234, ExecutionFingerprint: req.ExecutionFingerprint()}, nil
}

func (p *recordingRemotePreparer) Stat(_ context.Context, _ string, _ nodeauth.Keyring, artifactID string) (downloadprepare.Result, error) {
	return downloadprepare.Result{ArtifactID: artifactID, FileSize: 1234}, nil
}

func (p *recordingRemotePreparer) Delete(_ context.Context, nodeURL string, _ nodeauth.Keyring, artifactID string) error {
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

func (unavailableRemotePreparer) Prepare(context.Context, string, nodeauth.Keyring, downloadprepare.Request) (downloadprepare.Result, error) {
	return downloadprepare.Result{}, os.ErrNotExist
}
func (unavailableRemotePreparer) Stat(context.Context, string, nodeauth.Keyring, string) (downloadprepare.Result, error) {
	return downloadprepare.Result{}, downloadprepare.ErrArtifactNotFound
}
func (unavailableRemotePreparer) Delete(context.Context, string, nodeauth.Keyring, string) error {
	return nil
}

type indeterminateRemotePreparer struct{}

func (indeterminateRemotePreparer) Prepare(context.Context, string, nodeauth.Keyring, downloadprepare.Request) (downloadprepare.Result, error) {
	return downloadprepare.Result{}, context.DeadlineExceeded
}
func (indeterminateRemotePreparer) Stat(context.Context, string, nodeauth.Keyring, string) (downloadprepare.Result, error) {
	return downloadprepare.Result{}, os.ErrDeadlineExceeded
}
func (indeterminateRemotePreparer) Delete(context.Context, string, nodeauth.Keyring, string) error {
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

func (p *attestationRemotePreparer) Prepare(context.Context, string, nodeauth.Keyring, downloadprepare.Request) (downloadprepare.Result, error) {
	return p.prepareResult, p.prepareErr
}

func (p *attestationRemotePreparer) Stat(context.Context, string, nodeauth.Keyring, string) (downloadprepare.Result, error) {
	return p.statResult, p.statErr
}

func (p *attestationRemotePreparer) Delete(context.Context, string, nodeauth.Keyring, string) error {
	p.deletes++
	return p.deleteErr
}

// newPoolPreparer builds a preparer that plans over a transcode pool of nodes
// and signs node requests with the JWT secret "secret".
func newPoolPreparer(local EncodePreparer, nodes ...*nodepool.Node) *NodeAwarePreparer {
	pool := nodepooltest.NewTranscodePool()
	pool.SetNodes(nodes)
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	preparer := NewNodeAwarePreparer(local, nodepool.NewPlanner(nodepool.NewProxyPool(), pool), func() *config.Config { return cfg })
	preparer.SetNodeKeys(nodeauthtest.NodeKey(cfg.Auth.JWTSecret))
	return preparer
}

// softwarePQToneMap is a software BT.2390 tone map for PQ sources.
func softwarePQToneMap() tonemap.Capabilities {
	return tonemap.Capabilities{{
		Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
		SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
	}}
}

// cachedToneMap is a node capability entry that stays fresh for a minute.
func cachedToneMap(capabilities tonemap.Capabilities) remoteToneMapCapabilities {
	return remoteToneMapCapabilities{capabilities: capabilities, expiresAt: time.Now().Add(time.Minute)}
}

func newToneMapPreparerTest(t *testing.T, remote downloadprepare.RemotePreparer) (*NodeAwarePreparer, *recordingEncodePreparer, playback.TranscodeOpts) {
	t.Helper()
	const nodeURL = "http://tone-map-node"
	local := &recordingEncodePreparer{}
	preparer := newPoolPreparer(local, &nodepool.Node{ID: 22, URL: nodeURL, Enabled: true, Healthy: true})
	preparer.remote = remote
	preparer.capabilities[nodeURL] = cachedToneMap(softwarePQToneMap())
	opts := playback.TranscodeOpts{
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 42, FileSize: 100, StreamSignature: "stream"},
	}
	return preparer, local, opts
}

func TestRemotePrepareResultRequiresPreparedTracksAttestation(t *testing.T) {
	file := &models.MediaFile{AudioTracks: []models.AudioTrack{{Codec: "aac"}, {Codec: "aac"}}}
	request := downloadprepare.NewRequest("artifact-tracks", playback.TranscodeOpts{
		TargetCodecAudio: "aac", PreparedTracks: playback.PlanPreparedTracks(file, "aac", -1),
	})
	// A node without the layout ignores it and attests its legacy recipe.
	legacyRequest := request
	legacyRequest.TrackRecipeVersion, legacyRequest.PreparedTracks = "", nil
	legacy := downloadprepare.Result{ArtifactID: request.ArtifactID, FileSize: 55, ExecutionFingerprint: legacyRequest.ExecutionFingerprint()}
	if remotePrepareResultMatches(legacy, request.ArtifactID, request) {
		t.Fatal("multi-track request accepted a legacy single-track receipt")
	}
	if remotePrepareResultMatches(downloadprepare.Result{ArtifactID: request.ArtifactID, FileSize: 55}, request.ArtifactID, request) {
		t.Fatal("multi-track request accepted an unattested result")
	}
	attested := downloadprepare.Result{ArtifactID: request.ArtifactID, FileSize: 55, ExecutionFingerprint: request.ExecutionFingerprint()}
	if !remotePrepareResultMatches(attested, request.ArtifactID, request) {
		t.Fatal("multi-track request rejected its exact execution receipt")
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
			if test.name != "old node omits receipt" {
				remote.prepareResult.ExecutionFingerprint = downloadprepare.NewRequest("artifact-tone-map", opts).ExecutionFingerprint()
			}
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
			if test.name != "missing receipt" {
				remote.statResult.ExecutionFingerprint = downloadprepare.NewRequest("artifact-tone-map", opts).ExecutionFingerprint()
			}
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
	local := &recordingEncodePreparer{}
	remote := &recordingRemotePreparer{}
	preparer := newPoolPreparer(local, &nodepool.Node{ID: 23, URL: "http://ordinary-node", Enabled: true, Healthy: true})
	preparer.remote = remote
	preparer.capabilities["http://ordinary-node"] = cachedToneMap(softwarePQToneMap())
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

func TestNodeAwarePreparerUsesCurrentDisabledNodeURLForCleanup(t *testing.T) {
	group := "host-new"
	remote := &recordingRemotePreparer{}
	p := newPoolPreparer(nil)
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
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{ToneMapCapabilities: softwarePQToneMap()})
	})
	first := httptest.NewServer(handler)
	defer first.Close()
	second := httptest.NewServer(handler)
	defer second.Close()
	preparer := newPoolPreparer(nil,
		&nodepool.Node{URL: first.URL, Enabled: true, Healthy: true},
		&nodepool.Node{URL: second.URL, Enabled: true, Healthy: true},
	)
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
	preparer.SetNodeKeys(nodeauthtest.NodeKey(cfg.Auth.JWTSecret))
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
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{ToneMapCapabilities: softwarePQToneMap()})
	}))
	defer remote.Close()
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	preparer := NewNodeAwarePreparer(nil, nil, func() *config.Config { return cfg })
	preparer.SetNodeKeys(nodeauthtest.NodeKey(cfg.Auth.JWTSecret))

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
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{ToneMapCapabilities: softwarePQToneMap()})
	}))
	defer remote.Close()
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "secret"
	preparer := NewNodeAwarePreparer(nil, nil, func() *config.Config { return cfg })
	preparer.SetNodeKeys(nodeauthtest.NodeKey(cfg.Auth.JWTSecret))

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

// A job dispatched to a node carries a source grant naming that node; a
// source that cannot be granted is prepared here instead.
func TestNodeAwarePreparerGrantsTheChosenNodeItsSource(t *testing.T) {
	opts := playback.TranscodeOpts{InputPath: "/media/movie.mkv", MediaFileID: 42, TargetCodecVideo: "h264", TargetCodecAudio: "aac"}

	local := &recordingEncodePreparer{}
	remote := &recordingRemotePreparer{}
	p := newPoolPreparer(local, &nodepool.Node{ID: 17, URL: "http://idle", Enabled: true, Healthy: true})
	p.remote = remote
	p.SetSourceGrants(nodesourcetest.Granter{})
	if _, err := p.PrepareFile(context.Background(), "artifact-1", opts, "/local/artifact-1.mp4"); err != nil {
		t.Fatal(err)
	}
	if want := nodesourcetest.Grant(17, 42, "/media/movie.mkv"); remote.request.SourceGrant != want || local.calls != 0 {
		t.Fatalf("remote grant = %q (local calls %d), want %q", remote.request.SourceGrant, local.calls, want)
	}

	local, remote = &recordingEncodePreparer{}, &recordingRemotePreparer{}
	p = newPoolPreparer(local, &nodepool.Node{ID: 17, URL: "http://idle", Enabled: true, Healthy: true})
	p.remote = remote
	p.SetSourceGrants(nodesourcetest.Granter{Err: nodesource.ErrSourceGone})
	if _, err := p.PrepareFile(context.Background(), "artifact-2", opts, "/local/artifact-2.mp4"); err != nil {
		t.Fatal(err)
	}
	if local.calls != 1 || remote.request.ArtifactID != "" {
		t.Fatalf("local calls = %d, remote request = %+v; want the job prepared here", local.calls, remote.request)
	}
}

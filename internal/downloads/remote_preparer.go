package downloads

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"golang.org/x/sync/singleflight"
)

// NodeAwarePreparer keeps artifact queue ownership central while executing the
// expensive FFmpeg process on a healthy transcode node when capacity permits.
// The node retains completed bytes behind an authenticated opaque-id endpoint;
// integrated installations and unavailable pools fall back to local work.
type NodeAwarePreparer struct {
	local   EncodePreparer
	planner nodepool.TranscodeWorkPlanner
	liveCfg func() *config.Config
	remote  downloadprepare.RemotePreparer
	// nodeKeys signs every request to a node; without it every job runs
	// locally and remote artifacts are unreachable.
	nodeKeys         nodeauth.Keyring
	originLookup     artifactOriginLookup
	settings         SettingsReader
	probeClient      *http.Client
	capabilityMu     sync.Mutex
	capabilities     map[string]remoteToneMapCapabilities
	capabilityFlight singleflight.Group
	// capabilityInvalidations counts how many times each node's inventory has
	// been dropped. A fetch snapshots it before asking the node and refuses to
	// install its answer if it has moved since — otherwise a probe already in
	// flight when an operator changed the node's policy writes the report it
	// was sent to collect, restoring the pre-edit inventory for a full TTL.
	capabilityInvalidations map[string]uint64
	// storageFull reports a node over its prepared-file budget or disk
	// ceiling with nothing left to free; such a node gets no new jobs.
	storageFull func(nodeID int) bool
	// sourceGrants grants a node each job's source; nil sends none.
	sourceGrants nodesource.FileGranter
}

// SetNodeKeys wires the keyring that signs requests to transcode nodes.
func (p *NodeAwarePreparer) SetNodeKeys(keys nodeauth.Keyring) {
	p.nodeKeys = keys
}

// SetSourceGrants wires the grants every remote job's source is sent with.
func (p *NodeAwarePreparer) SetSourceGrants(grants nodesource.FileGranter) {
	p.sourceGrants = grants
}

// SetStorageGate wires the storage check placement applies to every job.
func (p *NodeAwarePreparer) SetStorageGate(full func(nodeID int) bool) {
	p.storageFull = full
}

func (p *NodeAwarePreparer) hasStorage(n *nodepool.Node) bool {
	return n != nil && (p.storageFull == nil || !p.storageFull(n.ID))
}

// remoteToneMapCapabilities caches one node's validated inventory; an empty
// slice with a short expiry represents a recent lookup failure.
type remoteToneMapCapabilities struct {
	capabilities        tonemap.Capabilities
	transformations     []playback.TransformationV3
	transportFeatures   []string
	err                 error
	expiresAt           time.Time
	probeRequestTimeout time.Duration
}

const (
	remoteToneMapCapabilityTTL      = time.Minute
	remoteToneMapCapabilityErrorTTL = 15 * time.Second
	remoteToneMapProbeMinTimeout    = 5 * time.Second
)

// eligibleTranscodeWorkPlanner reserves work only on nodes that satisfy a
// lock-safe capability predicate.
type eligibleTranscodeWorkPlanner interface {
	ReserveTranscodeWorkWith(workID string, kind nodepool.JobKind, eligible func(*nodepool.Node) bool) (*nodepool.Node, func())
}

type transcodeWorkCapacityPlanner interface {
	TranscodeWorkAvailableWith(eligible func(*nodepool.Node) bool) bool
}

// transcodeNodeEnumerator lists the currently enabled transcode pool for
// concurrent capability discovery.
type transcodeNodeEnumerator interface {
	TranscodeNodeURLs() []string
}

type artifactOriginLookup interface {
	GetByID(ctx context.Context, id int) (*nodepool.Node, error)
}

// NewNodeAwarePreparer creates a preparer that can select local or pooled execution.
func NewNodeAwarePreparer(local EncodePreparer, planner nodepool.TranscodeWorkPlanner, liveCfg func() *config.Config) *NodeAwarePreparer {
	if local == nil {
		local = playbackPreparer{}
	}
	return &NodeAwarePreparer{
		local:        local,
		planner:      planner,
		liveCfg:      liveCfg,
		remote:       downloadprepare.HTTPPreparer{},
		capabilities: make(map[string]remoteToneMapCapabilities),
	}
}

// SetOriginLookup supplies the authoritative node record used when the active
// pool temporarily misses an enabled node, and to recover a changed URL after
// a disabled node has left that pool.
func (p *NodeAwarePreparer) SetOriginLookup(lookup artifactOriginLookup) {
	p.originLookup = lookup
}

// SetSettingsReader supplies the live local-fallback policy. It is wired by
// ArtifactManager together with the tone-map settings used to freeze recipes.
func (p *NodeAwarePreparer) SetSettingsReader(settings SettingsReader) {
	p.settings = settings
}

// LocalFallbackAllowed reports whether this pooled preparer may execute on
// the API host when no compatible node is available.
func (p *NodeAwarePreparer) LocalFallbackAllowed(ctx context.Context) bool {
	if p == nil || p.planner == nil || p.settings == nil {
		return true
	}
	values, err := p.settings.GetAll(ctx)
	if err != nil {
		slog.WarnContext(ctx, "load local transcode fallback setting failed", "component", "downloads", "error", err)
		return false
	}
	return !strings.EqualFold(values[config.DownloadLocalTranscodeFallbackSettingKey], "false")
}

// prepareLocally enforces the live local-fallback policy before delegating to
// the API host's artifact preparer.
func (p *NodeAwarePreparer) prepareLocally(ctx context.Context, artifactID string, opts playback.TranscodeOpts, outputPath string) (PreparedArtifact, error) {
	if !p.LocalFallbackAllowed(ctx) {
		return PreparedArtifact{}, errors.New("no eligible transcode node and local transcode fallback is disabled")
	}
	if p.storageFull != nil && p.storageFull(0) {
		return PreparedArtifact{}, ErrStorageFull
	}
	return p.local.PrepareFile(ctx, artifactID, opts, outputPath)
}

// maxCapacityRefusals bounds how many nodes in a row may refuse one
// preparation at their own job cap or prepared-download budget before it
// takes the path any other refusal takes.
const maxCapacityRefusals = 4

// PrepareFile routes an artifact job to a compatible node or allowed local fallback.
func (p *NodeAwarePreparer) PrepareFile(ctx context.Context, artifactID string, opts playback.TranscodeOpts, outputPath string) (PreparedArtifact, error) {
	return p.prepareFile(ctx, artifactID, opts, outputPath, 0, nil)
}

// prepareFile is PrepareFile after refusals nodes gave it at their own job
// cap or prepared-download budget. Such a node started nothing. One at its
// job cap is held back by the planner; one at its budget is in storageFull,
// which the next reservation leaves out and counts as turned away for space.
// Either way the next reservation picks another node, or none and the local
// fallback.
func (p *NodeAwarePreparer) prepareFile(ctx context.Context, artifactID string, opts playback.TranscodeOpts, outputPath string, refusals int, storageFull map[string]struct{}) (PreparedArtifact, error) {
	cfg := p.config()
	keys := p.nodeKeys
	if cfg == nil || keys == nil || p.remote == nil || p.planner == nil || !downloadprepare.ValidArtifactID(artifactID) {
		return p.prepareLocally(ctx, artifactID, opts, outputPath)
	}
	request := downloadprepare.NewRequest(artifactID, opts)
	node, release, storageRejected := p.reserveNode(ctx, artifactID, request, opts, storageFull)
	if node == nil {
		if storageRejected && !p.LocalFallbackAllowed(ctx) {
			return PreparedArtifact{}, ErrStorageFull
		}
		return p.prepareLocally(ctx, artifactID, opts, outputPath)
	}

	grant, err := nodesource.GrantFile(ctx, p.sourceGrants, node.ID, &models.MediaFile{ID: opts.MediaFileID, FilePath: opts.InputPath})
	if err != nil {
		release()
		slog.WarnContext(ctx, "remote download artifact prepare cannot grant its source; falling back to local", "component", "downloads", "artifact_id", artifactID, "node", node.URL, "error", err)
		return p.prepareLocally(ctx, artifactID, opts, outputPath)
	}
	request.SourceGrant = grant
	slog.InfoContext(ctx, "dispatching download artifact prepare", "component", "downloads", "artifact_id", artifactID, "node", node.URL)
	observer := prepareObserverFrom(ctx)
	nodeID := node.ID
	observer.prepareWorker(&nodeID, node.Name)
	stopProgress := p.pollRemoteProgress(ctx, node.URL, keys, artifactID, observer)
	result, err := p.remote.Prepare(ctx, node.URL, keys, request)
	stopProgress()
	release()
	if errors.Is(err, downloadprepare.ErrNodeAtCapacity) {
		nodepool.ReportAtCapacity(p.planner, node.URL)
	}
	if errors.Is(err, downloadprepare.ErrNodeAtCapacity) && refusals+1 < maxCapacityRefusals && ctx.Err() == nil {
		slog.InfoContext(ctx, "transcode node is at its job limit; placing the download artifact prepare again", "component", "downloads", "artifact_id", artifactID, "node", node.URL)
		return p.prepareFile(ctx, artifactID, opts, outputPath, refusals+1, storageFull)
	}
	if errors.Is(err, downloadprepare.ErrNodeStorageFull) && refusals+1 < maxCapacityRefusals && ctx.Err() == nil {
		slog.InfoContext(ctx, "transcode node's prepared-download directory is at its budget; placing the download artifact prepare again", "component", "downloads", "artifact_id", artifactID, "node", node.URL)
		full := maps.Clone(storageFull)
		if full == nil {
			full = map[string]struct{}{}
		}
		full[strings.TrimRight(node.URL, "/")] = struct{}{}
		return p.prepareFile(ctx, artifactID, opts, outputPath, refusals+1, full)
	}
	prepareReturned := err == nil
	if prepareReturned {
		if remotePrepareResultMatches(result, artifactID, request) {
			return remotePreparedArtifact(node, result), nil
		}
		err = rejectedRemotePrepareResultError("prepare", result, artifactID)
	}
	if ctx.Err() != nil {
		return remotePreparedArtifact(node, downloadprepare.Result{ArtifactID: artifactID}), ctx.Err()
	}
	if prepareReturned {
		if deleteErr := p.remote.Delete(ctx, node.URL, keys, artifactID); deleteErr != nil {
			return remotePreparedArtifact(node, downloadprepare.Result{ArtifactID: artifactID}),
				errors.Join(err, fmt.Errorf("delete rejected remote download artifact: %w", deleteErr))
		}
		slog.WarnContext(ctx, "remote download artifact prepare was not attested; falling back to local", "component", "downloads", "artifact_id", artifactID, "node", node.URL, "error", err)
		return p.prepareLocally(ctx, artifactID, opts, outputPath)
	}
	// A completed encode can outlive a lost HTTP response. Probe the same opaque
	// id before falling back so retry/recovery does not duplicate expensive work.
	if recovered, statErr := p.remote.Stat(ctx, node.URL, keys, artifactID); statErr == nil {
		if remotePrepareResultMatches(recovered, artifactID, request) {
			slog.InfoContext(ctx, "recovered completed download artifact after lost response", "component", "downloads", "artifact_id", artifactID, "node", node.URL)
			return remotePreparedArtifact(node, recovered), nil
		}
		recoveryErr := rejectedRemotePrepareResultError("recovery", recovered, artifactID)
		if deleteErr := p.remote.Delete(ctx, node.URL, keys, artifactID); deleteErr != nil {
			return remotePreparedArtifact(node, downloadprepare.Result{ArtifactID: artifactID}),
				errors.Join(err, recoveryErr, fmt.Errorf("delete rejected remote download artifact: %w", deleteErr))
		}
		err = errors.Join(err, recoveryErr)
	} else if !errors.Is(statErr, downloadprepare.ErrArtifactNotFound) {
		slog.WarnContext(ctx, "remote download artifact recovery probe failed", "component", "downloads", "artifact_id", artifactID, "node", node.URL, "error", statErr)
		// The POST may have completed even though its response was lost. If the
		// follow-up probe is also indeterminate, retry the same opaque id later
		// instead of falling back locally and orphaning completed remote bytes.
		return remotePreparedArtifact(node, downloadprepare.Result{ArtifactID: artifactID}), errors.Join(err, fmt.Errorf("remote download artifact recovery probe: %w", statErr))
	}
	if ctx.Err() != nil {
		return PreparedArtifact{}, ctx.Err()
	}
	slog.WarnContext(ctx, "remote download artifact prepare unavailable; falling back to local", "component", "downloads", "artifact_id", artifactID, "node", node.URL, "error", err)
	return p.prepareLocally(ctx, artifactID, opts, outputPath)
}

// reserveNode reserves the node a preparation runs on: one that can run its
// recipe and has room for it. storageRejected reports a node turned away only
// for want of space, so a job no node can take says it waits for space rather
// than failing. A nil node means none. refusedForStorage holds the nodes that
// already refused this preparation at their own budget: they are left out,
// and count as turned away for space.
func (p *NodeAwarePreparer) reserveNode(ctx context.Context, artifactID string, request downloadprepare.Request, opts playback.TranscodeOpts, refusedForStorage map[string]struct{}) (node *nodepool.Node, release func(), storageRejected bool) {
	hasStorage := func(n *nodepool.Node) bool {
		if n != nil {
			if _, refused := refusedForStorage[strings.TrimRight(n.URL, "/")]; refused {
				storageRejected = true
				return false
			}
		}
		if p.hasStorage(n) {
			return true
		}
		storageRejected = true
		return false
	}
	workID := "download-prepare-" + artifactID
	// Classed by the work it does, as the node counts it: a download that
	// re-encodes video competes with playback encodes for the same GPU.
	downloadJobKind := request.JobKind()
	selector, selective := p.planner.(eligibleTranscodeWorkPlanner)
	switch {
	case request.ToneMapRequested() || request.StereoDownmixBoostRequested() || request.PreparedTracksRequested():
		if selective {
			node, release = selector.ReserveTranscodeWorkWith(workID, downloadJobKind, p.recipeCapable(ctx, request, opts, hasStorage))
		}
	case selective && (p.storageFull != nil || len(refusedForStorage) > 0):
		node, release = selector.ReserveTranscodeWorkWith(workID, downloadJobKind, hasStorage)
	case len(refusedForStorage) > 0:
		// This planner cannot leave a node out, so no other node is tried.
		storageRejected = true
	default:
		node, release = p.planner.ReserveTranscodeWork(workID, downloadJobKind)
	}
	return node, release, storageRejected
}

// recipeCapable is the reservation predicate of a preparation whose recipe
// needs node capabilities: tone mapping, the stereo downmix boost, or
// prepared tracks. hasStorage runs last, so only a node that could run the
// recipe counts as turned away for space.
func (p *NodeAwarePreparer) recipeCapable(ctx context.Context, request downloadprepare.Request, opts playback.TranscodeOpts, hasStorage func(*nodepool.Node) bool) func(*nodepool.Node) bool {
	var required []map[string]struct{}
	if request.ToneMapRequested() {
		required = append(required, p.capableToneMapNodeURLs(ctx, opts.ToneMapMode, opts.ToneMapSourceKind))
	}
	if request.StereoDownmixBoostRequested() {
		required = append(required, p.audioBoostCapableNodeURLs(ctx))
	}
	if request.PreparedTracksRequested() {
		required = append(required, p.preparedTracksCapableNodeURLs(ctx))
	}
	return func(candidate *nodepool.Node) bool {
		if candidate == nil {
			return false
		}
		nodeURL := strings.TrimRight(candidate.URL, "/")
		for _, capable := range required {
			if _, supported := capable[nodeURL]; !supported {
				return false
			}
		}
		return hasStorage(candidate)
	}
}

// remoteProgressReader is implemented by remote preparers that can read a
// node's live progress for an attempt in flight.
type remoteProgressReader interface {
	Progress(ctx context.Context, nodeURL string, keys nodeauth.Keyring, artifactID string) (downloadprepare.Progress, error)
}

// remoteProgressPollInterval matches the attempt observer's flush interval, so
// each flush has a reading that is at most one interval old.
var remoteProgressPollInterval = progressFlushInterval

// pollRemoteProgress relays a node's progress for one attempt to observer
// until the returned stop function is called. A node that predates the
// progress endpoint is reported once as unable to report progress.
func (p *NodeAwarePreparer) pollRemoteProgress(ctx context.Context, nodeURL string, keys nodeauth.Keyring, artifactID string, observer prepareObserver) func() {
	reader, ok := p.remote.(remoteProgressReader)
	if !ok {
		observer.prepareProgressUnavailable()
		return func() {}
	}
	pollCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(remoteProgressPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
			}
			progress, err := reader.Progress(pollCtx, nodeURL, keys, artifactID)
			switch {
			case errors.Is(err, downloadprepare.ErrProgressUnsupported):
				observer.prepareProgressUnavailable()
				return
			case err != nil:
				if pollCtx.Err() == nil {
					slog.DebugContext(pollCtx, "reading remote download prepare progress failed", "component", "downloads", "artifact_id", artifactID, "node", nodeURL, "error", err)
				}
			case progress.Running:
				observer.PrepareProgress(playback.PrepareProgress{
					EncodedSeconds:  progress.EncodedSeconds,
					DurationSeconds: progress.DurationSeconds,
					Speed:           progress.Speed,
				})
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func remotePrepareResultMatches(result downloadprepare.Result, artifactID string, request downloadprepare.Request) bool {
	if result.ArtifactID != artifactID {
		return false
	}
	if request.AudioRecipeRequested() && !request.StereoDownmixBoostRequested() {
		return false
	}
	if !request.ExecutionAttestationRequested() {
		return true
	}
	if request.ToneMapRequested() {
		if !request.ValidToneMapAttestation() ||
			result.ToneMapRecipeVersion != request.ToneMapRecipeVersion ||
			result.ToneMapMode != request.ToneMapMode ||
			result.ToneMapSourceRevisionFingerprint != request.ToneMapSourceRevision.Fingerprint() {
			return false
		}
	}
	return result.ExecutionFingerprint == request.ExecutionFingerprint()
}

func rejectedRemotePrepareResultError(operation string, result downloadprepare.Result, artifactID string) error {
	if result.ArtifactID != artifactID {
		return fmt.Errorf("remote download artifact %s returned artifact id %q, want %q", operation, result.ArtifactID, artifactID)
	}
	return fmt.Errorf("remote download artifact %s returned mismatched recipe attestation for %q", operation, artifactID)
}

// ToneMapCapabilities reports the validated executor union of enabled pooled
// transcode nodes. Selection rechecks the same per-node records before
// reserving work, so heterogeneous pools cannot receive an incompatible job.
func (p *NodeAwarePreparer) ToneMapCapabilities(ctx context.Context) (tonemap.Capabilities, error) {
	result := tonemap.Capabilities{}
	byNode, err := p.toneMapCapabilitiesByNode(ctx)
	for _, capabilities := range byNode {
		result = append(result, capabilities...)
	}
	return result, err
}

// ToneMapModeAvailable reports whether an enabled compatible node has
// reservable capacity now. Capability discovery completes before the planner
// lock is acquired so the eligibility predicate remains lock-safe.
func (p *NodeAwarePreparer) ToneMapModeAvailable(ctx context.Context, mode tonemap.Mode, kind tonemap.SourceKind) (bool, error) {
	selector, ok := p.planner.(transcodeWorkCapacityPlanner)
	if !ok {
		return false, nil
	}
	byNode, probeErr := p.toneMapCapabilitiesByNode(ctx)
	capable := make(map[string]struct{})
	for nodeURL, capabilities := range byNode {
		if capabilities.Supports(mode, kind) {
			capable[nodepool.NormalizeNodeURL(nodeURL)] = struct{}{}
		}
	}
	available := selector.TranscodeWorkAvailableWith(func(candidate *nodepool.Node) bool {
		if candidate == nil {
			return false
		}
		_, supported := capable[strings.TrimRight(candidate.URL, "/")]
		return supported
	})
	if available {
		return true, nil
	}
	return false, probeErr
}

// capableToneMapNodeURLs returns the normalized URLs of nodes that validated
// the exact mode and source kind frozen in an artifact recipe.
func (p *NodeAwarePreparer) capableToneMapNodeURLs(ctx context.Context, mode tonemap.Mode, kind tonemap.SourceKind) map[string]struct{} {
	result := make(map[string]struct{})
	byNode, _ := p.toneMapCapabilitiesByNode(ctx)
	for nodeURL, capabilities := range byNode {
		if capabilities.Supports(mode, kind) {
			result[nodeURL] = struct{}{}
		}
	}
	return result
}

// audioBoostCapableNodeURLs returns nodes advertising the exact audio_to_aac
// recipe version that consumes SourceAudioChannels.
func (p *NodeAwarePreparer) audioBoostCapableNodeURLs(ctx context.Context) map[string]struct{} {
	return p.nodeURLsSupporting(ctx, func(entry remoteToneMapCapabilities) bool {
		return supportsAudioBoostTransformation(entry.transformations)
	})
}

// preparedTracksCapableNodeURLs returns nodes that execute the multi-track
// prepared-download layout. An older node would encode the legacy layout and
// fail attestation only after spending the whole encode.
func (p *NodeAwarePreparer) preparedTracksCapableNodeURLs(ctx context.Context) map[string]struct{} {
	return p.nodeURLsSupporting(ctx, func(entry remoteToneMapCapabilities) bool {
		return slices.Contains(entry.transportFeatures, playback.TransportFeaturePreparedTracksV1)
	})
}

// nodeURLsSupporting returns the normalized URLs of enabled nodes whose
// capability report satisfies supports. Capability fetches share the existing
// bounded cache and singleflight used by tone-map discovery.
func (p *NodeAwarePreparer) nodeURLsSupporting(ctx context.Context, supports func(remoteToneMapCapabilities) bool) map[string]struct{} {
	result := make(map[string]struct{})
	enumerator, ok := p.planner.(transcodeNodeEnumerator)
	if !ok {
		return result
	}
	nodeURLs := enumerator.TranscodeNodeURLs()
	supported := make([]bool, len(nodeURLs))
	var wg sync.WaitGroup
	for i, nodeURL := range nodeURLs {
		wg.Add(1)
		go func(i int, nodeURL string) {
			defer wg.Done()
			supported[i], _ = p.nodeCapabilitySupports(ctx, nodeURL, supports)
		}(i, nodeURL)
	}
	wg.Wait()
	for i, ok := range supported {
		if ok {
			result[strings.TrimRight(nodeURLs[i], "/")] = struct{}{}
		}
	}
	return result
}

func (p *NodeAwarePreparer) nodeCapabilitySupports(ctx context.Context, nodeURL string, supports func(remoteToneMapCapabilities) bool) (bool, error) {
	nodeURL = nodepool.NormalizeNodeURL(nodeURL)
	if entry, ok := p.cachedRemoteCapabilitiesForNode(nodeURL, time.Now()); ok {
		return supports(entry), entry.err
	}
	if _, err := p.toneMapCapabilitiesForNode(ctx, nodeURL); err != nil {
		return false, err
	}
	entry, ok := p.cachedRemoteCapabilitiesForNode(nodeURL, time.Now())
	if !ok {
		return false, errors.New("transcode node capability result was not cached")
	}
	return supports(entry), entry.err
}

func supportsAudioBoostTransformation(transformations []playback.TransformationV3) bool {
	for _, transformation := range transformations {
		if strings.EqualFold(strings.TrimSpace(transformation.Name), playback.TransformationAudioToAACV3) &&
			strings.EqualFold(strings.TrimSpace(transformation.Executor), playback.ExecutorServerV3) &&
			strings.TrimSpace(transformation.RecipeVersion) == playback.TransformationAudioToAACRecipeVersionV3 {
			return true
		}
	}
	return false
}

// toneMapCapabilitiesByNode fetches the enabled pool concurrently and keeps
// each inventory attached to its node for safe heterogeneous selection.
func (p *NodeAwarePreparer) toneMapCapabilitiesByNode(ctx context.Context) (map[string]tonemap.Capabilities, error) {
	enumerator, ok := p.planner.(transcodeNodeEnumerator)
	if !ok {
		return map[string]tonemap.Capabilities{}, nil
	}
	nodeURLs := enumerator.TranscodeNodeURLs()
	results := make([]struct {
		capabilities tonemap.Capabilities
		err          error
	}, len(nodeURLs))
	var wg sync.WaitGroup
	for i, nodeURL := range nodeURLs {
		wg.Add(1)
		go func(i int, nodeURL string) {
			defer wg.Done()
			results[i].capabilities, results[i].err = p.toneMapCapabilitiesForNode(ctx, nodeURL)
		}(i, nodeURL)
	}
	wg.Wait()
	byNode := make(map[string]tonemap.Capabilities, len(nodeURLs))
	var resultErr error
	for i, result := range results {
		if result.err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("node %s: %w", logredact.SanitizeURL(nodeURLs[i]), result.err))
			continue
		}
		if result.capabilities != nil {
			byNode[strings.TrimRight(nodeURLs[i], "/")] = result.capabilities
		}
	}
	return byNode, resultErr
}

// toneMapCapabilitiesForNode returns a defensive copy of a fresh cached
// inventory or retrieves the node's authenticated hardware capabilities.
func (p *NodeAwarePreparer) toneMapCapabilitiesForNode(ctx context.Context, nodeURL string) (tonemap.Capabilities, error) {
	nodeURL = nodepool.NormalizeNodeURL(nodeURL)
	if capabilities, err, ok := p.cachedToneMapCapabilitiesForNode(nodeURL, time.Now()); ok {
		return capabilities, err
	}
	// Keep the shared probe alive when one waiter disconnects; fetch applies
	// the node-specific probe timeout, while each caller may stop waiting below.
	sharedCtx := context.WithoutCancel(ctx)
	resultCh := p.capabilityFlight.DoChan(nodeURL, func() (any, error) {
		if capabilities, err, ok := p.cachedToneMapCapabilitiesForNode(nodeURL, time.Now()); ok {
			return capabilities, err
		}
		return p.fetchToneMapCapabilitiesForNode(sharedCtx, nodeURL)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return nil, result.Err
		}
		capabilities, ok := result.Val.(tonemap.Capabilities)
		if !ok {
			return nil, errors.New("invalid shared transcode-node capability result")
		}
		return append(tonemap.Capabilities(nil), capabilities...), nil
	}
}

func (p *NodeAwarePreparer) cachedToneMapCapabilitiesForNode(nodeURL string, now time.Time) (tonemap.Capabilities, error, bool) {
	entry, ok := p.cachedRemoteCapabilitiesForNode(nodeURL, now)
	if !ok {
		return nil, nil, false
	}
	return append(tonemap.Capabilities(nil), entry.capabilities...), entry.err, true
}

func (p *NodeAwarePreparer) cachedRemoteCapabilitiesForNode(nodeURL string, now time.Time) (remoteToneMapCapabilities, bool) {
	p.capabilityMu.Lock()
	entry, ok := p.capabilities[nodeURL]
	p.capabilityMu.Unlock()
	if !ok || !now.Before(entry.expiresAt) {
		return remoteToneMapCapabilities{}, false
	}
	entry.capabilities = append(tonemap.Capabilities(nil), entry.capabilities...)
	entry.transformations = append([]playback.TransformationV3(nil), entry.transformations...)
	return entry, true
}

func (p *NodeAwarePreparer) fetchToneMapCapabilitiesForNode(ctx context.Context, nodeURL string) (tonemap.Capabilities, error) {
	// Snapshotted before the node is asked anything, so an invalidation landing
	// during the request is visible at install time.
	generation := p.capabilityInvalidationsFor(nodeURL)
	if p.nodeKeys == nil {
		err := errors.New("transcode node credentials unavailable")
		p.cacheToneMapCapabilityFailure(nodeURL, generation, err)
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, p.remoteToneMapProbeTimeout(nodeURL))
	defer cancel()
	info, status, err := transcodenode.FetchHWCapabilities(requestCtx, p.probeClient, nodeURL, nodeauth.TargetTranscode, p.nodeKeys)
	if err != nil {
		p.cacheToneMapCapabilityFailure(nodeURL, generation, err)
		return nil, err
	}
	if status != http.StatusOK {
		err := fmt.Errorf("transcode node returned %d", status)
		p.cacheToneMapCapabilityFailure(nodeURL, generation, err)
		return nil, err
	}
	entry := remoteToneMapCapabilities{
		capabilities:        append(tonemap.Capabilities(nil), info.ToneMapCapabilities...),
		transformations:     append([]playback.TransformationV3(nil), info.Transformations...),
		transportFeatures:   append([]string(nil), info.TransportFeatures...),
		expiresAt:           time.Now().Add(remoteToneMapCapabilityTTL),
		probeRequestTimeout: playback.NormalizeProbeRequestTimeout(info.ProbeRequestTimeoutMillis, remoteToneMapProbeMinTimeout),
	}
	p.capabilityMu.Lock()
	if p.capabilityInvalidations[nodeURL] == generation {
		if p.capabilities == nil {
			p.capabilities = make(map[string]remoteToneMapCapabilities)
		}
		p.capabilities[nodeURL] = entry
	}
	p.capabilityMu.Unlock()
	// The answer still goes back to the caller that is waiting on it, overtaken
	// or not. Its request is already in flight, most policy edits do not remove
	// the executor it is about to pick, and refusing would fail a download over
	// a change that probably does not affect it. What must not happen is the
	// durable part: nothing is written, so the next caller asks the node again
	// rather than reading this answer for a minute.
	return append(tonemap.Capabilities(nil), entry.capabilities...), nil
}

// capabilityInvalidationsFor reports how many times a node's inventory has been
// dropped.
func (p *NodeAwarePreparer) capabilityInvalidationsFor(nodeURL string) uint64 {
	p.capabilityMu.Lock()
	defer p.capabilityMu.Unlock()
	return p.capabilityInvalidations[nodeURL]
}

// ToneMapCapabilityTimeout returns the complete cold-node capability budget
// used when pooled nodes are the only eligible tone-map executors.
func (p *NodeAwarePreparer) ToneMapCapabilityTimeout() time.Duration {
	return remoteOnlyToneMapPlanTimeout
}

func (p *NodeAwarePreparer) remoteToneMapProbeTimeout(nodeURL string) time.Duration {
	nodeURL = nodepool.NormalizeNodeURL(nodeURL)
	p.capabilityMu.Lock()
	timeout := p.capabilities[nodeURL].probeRequestTimeout
	p.capabilityMu.Unlock()
	// The larger of what was learned from this node and what it currently
	// describes; neither dominates.
	//
	// A learned budget is preserved across failures on purpose, so a cold retry
	// is not cut short by a fallback — but it describes the node as it was, and
	// an operator who widens hw_device_override leaves one behind that prices a
	// smaller device set than the node now walks. Every retry would be canceled
	// at that deadline, and since a budget is only ever learned from a read that
	// completes, nothing would replace it.
	//
	// What the node currently describes is its own stored report and its own
	// override — not the cluster setting, which says nothing about a node
	// overridden onto four devices. Pricing four at the cluster's one cancels the
	// matrix mid-walk, which drops the node from the capability map and sends the
	// download local, or fails it outright where local fallback is off.
	var node *nodepool.Node
	if lookup, ok := p.planner.(transcodeNodeLookup); ok {
		if found, ok := lookup.TranscodeNodeByURL(nodeURL); ok {
			node = found
		}
	}
	hwAccel, hwDevice := "", ""
	if cfg := p.config(); cfg != nil {
		hwAccel, hwDevice = cfg.Playback.HWAccel, cfg.Playback.HWDevice
	}
	// The whole capability read, not just its tone-map half: the node runs a
	// hardware walk first, and that walk scales with the device set it walks.
	cold := playback.ColdCapabilityRequestTimeout(
		node.StoredCapabilities(),
		node.EffectiveHWAccel(hwAccel),
		node.EffectiveHWDevice(hwDevice),
		playback.CapabilityRequestTimeout(hwAccel, hwDevice),
	)
	if cold > timeout {
		return cold
	}
	return timeout
}

// transcodeNodeLookup resolves the pooled record behind a transcode node URL,
// which carries that node's stored capability report and its acceleration
// override. Optional, like the planner's other capabilities: without it this
// path falls back to the cluster-wide setting. *nodepool.Planner implements it.
type transcodeNodeLookup interface {
	TranscodeNodeByURL(nodeURL string) (*nodepool.Node, bool)
}

// InvalidateNodeCapabilities drops one node's cached inventory so the next
// prepared download reads it again.
//
// It exists for the same reason the playback-v3 cache has one: an operator
// changing a node's acceleration policy, or the health sweep noticing the node's
// capability hash move, makes this cache wrong the moment it lands — and a
// download planned from it selects the node for a tone-map executor it no longer
// has, so the reconfigured worker rejects the recipe or the download falls back
// locally for no reason. A minute of TTL is a minute of that.
//
// The learned probe budget survives, exactly as it does across a failure: how
// long this node takes to answer has not changed, and the read the invalidation
// triggers is the cold one that most needs the real number.
func (p *NodeAwarePreparer) InvalidateNodeCapabilities(nodeURL string) {
	if p == nil || nodeURL == "" {
		return
	}
	nodeURL = nodepool.NormalizeNodeURL(nodeURL)
	p.capabilityMu.Lock()
	defer p.capabilityMu.Unlock()
	// Counted whether or not anything is cached. A cold cache is the case where
	// dropping an entry does nothing and a fetch is most likely to be in flight:
	// the invalidation that follows a policy edit arrives while planning is
	// already asking the node, and without a mark that fetch's answer would be
	// installed after the edit as though it described the node afterwards.
	if p.capabilityInvalidations == nil {
		p.capabilityInvalidations = make(map[string]uint64)
	}
	p.capabilityInvalidations[nodeURL]++
	entry, ok := p.capabilities[nodeURL]
	if !ok {
		return
	}
	p.capabilities[nodeURL] = remoteToneMapCapabilities{probeRequestTimeout: entry.probeRequestTimeout}
}

// cacheToneMapCapabilityFailure negatively caches an unreachable or invalid
// node briefly so repeated artifact planning does not amplify the failure.
//
// Fenced on the same invalidation count a successful result is, and for a
// sharper reason: a negative entry does not merely go stale, it takes the node
// out of planning entirely for its TTL. A fetch that failed because the node
// was mid-reload — which is exactly what a policy edit causes — would otherwise
// keep downloads off the node it was just reconfigured for, falling back
// locally or failing outright where local fallback is off, after the change
// that would have fixed it had already landed.
func (p *NodeAwarePreparer) cacheToneMapCapabilityFailure(nodeURL string, generation uint64, err error) {
	nodeURL = nodepool.NormalizeNodeURL(nodeURL)
	p.capabilityMu.Lock()
	defer p.capabilityMu.Unlock()
	if p.capabilityInvalidations[nodeURL] != generation {
		return
	}
	if p.capabilities == nil {
		p.capabilities = make(map[string]remoteToneMapCapabilities)
	}
	probeRequestTimeout := p.capabilities[nodeURL].probeRequestTimeout
	p.capabilities[nodeURL] = remoteToneMapCapabilities{
		capabilities:        tonemap.Capabilities{},
		err:                 err,
		expiresAt:           time.Now().Add(remoteToneMapCapabilityErrorTTL),
		probeRequestTimeout: probeRequestTimeout,
	}
}

func remotePreparedArtifact(node *nodepool.Node, result downloadprepare.Result) PreparedArtifact {
	group := ""
	if node.Group != nil {
		group = *node.Group
	}
	return PreparedArtifact{
		OriginNodeID:     node.ID,
		OriginNodeURL:    strings.TrimRight(node.URL, "/"),
		OriginNodeGroup:  group,
		OriginArtifactID: result.ArtifactID,
		FileSize:         result.FileSize,
	}
}

func (p *NodeAwarePreparer) ResolveArtifact(ctx context.Context, artifact *Artifact) error {
	if artifact == nil || artifact.OriginNodeID == 0 || p.planner == nil {
		return ErrArtifactOriginRemoved
	}
	node, ok := p.planner.TranscodeNode(artifact.OriginNodeID)
	if !ok || node == nil {
		if p.originLookup != nil {
			configured, err := p.originLookup.GetByID(ctx, artifact.OriginNodeID)
			switch {
			case err == nil && configured != nil && configured.Type == nodepool.NodeTypeTranscode:
				applyArtifactOrigin(artifact, configured)
				if configured.Enabled {
					return nil
				}
			case err != nil && !errors.Is(err, nodepool.ErrNodeNotFound):
				return fmt.Errorf("looking up artifact origin node: %w", err)
			}
		}
		return ErrArtifactOriginRemoved
	}
	applyArtifactOrigin(artifact, node)
	return nil
}

func applyArtifactOrigin(artifact *Artifact, node *nodepool.Node) {
	artifact.OriginNodeURL = strings.TrimRight(node.URL, "/")
	artifact.OriginNodeGroup = ""
	if node.Group != nil {
		artifact.OriginNodeGroup = *node.Group
	}
}

func (p *NodeAwarePreparer) StatArtifact(ctx context.Context, artifact *Artifact) (downloadprepare.Result, error) {
	if err := p.ResolveArtifact(ctx, artifact); err != nil {
		return downloadprepare.Result{}, err
	}
	keys, err := p.remoteCredentials(artifact)
	if err != nil {
		return downloadprepare.Result{}, err
	}
	return p.remote.Stat(ctx, artifact.OriginNodeURL, keys, artifact.OriginArtifactID)
}

func (p *NodeAwarePreparer) DeleteArtifact(ctx context.Context, artifact *Artifact) error {
	// Prefer the authoritative current URL, including for a disabled node. A
	// deleted node has no newer record, so retain the last persisted URL as the
	// best-effort cleanup target.
	_ = p.ResolveArtifact(ctx, artifact)
	keys, err := p.remoteCredentials(artifact)
	if err != nil {
		return err
	}
	return p.remote.Delete(ctx, artifact.OriginNodeURL, keys, artifact.OriginArtifactID)
}

func (p *NodeAwarePreparer) remoteCredentials(artifact *Artifact) (nodeauth.Keyring, error) {
	if artifact == nil || strings.TrimSpace(artifact.OriginNodeURL) == "" || !downloadprepare.ValidArtifactID(artifact.OriginArtifactID) {
		return nil, errors.New("remote artifact locator is incomplete")
	}
	if p.nodeKeys == nil || p.remote == nil {
		return nil, errors.New("remote artifact credentials are unavailable")
	}
	return p.nodeKeys, nil
}

func (p *NodeAwarePreparer) config() *config.Config {
	if p == nil || p.liveCfg == nil {
		return nil
	}
	return p.liveCfg()
}

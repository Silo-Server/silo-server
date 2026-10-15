package subsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

// Execution modes, from the subtitles.sync_execution setting.
const (
	ExecutionLocal           = "local"
	ExecutionPreferTranscode = "prefer_transcode_nodes"
	ExecutionTranscodeOnly   = "transcode_nodes_only"
)

// Settings keys the sampler reads.
const (
	SettingExecution    = "subtitles.sync_execution"
	SettingNodeCapacity = "subtitles.sync_node_capacity"
)

// DefaultExecution sends speech decoding to transcode nodes when there are
// any: they sit next to the media and keep the decode off the API server.
const DefaultExecution = ExecutionPreferTranscode

// SettingsReader reads server settings.
type SettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// NodeSource lists the transcode nodes that may take new work: those a
// request can be signed for. *nodepool.TranscodePool is one.
type NodeSource interface {
	KeyedNodes() []*nodepool.Node
}

// remoteRunner runs one request on a node; mediasample.RemoteClient is one.
type remoteRunner interface {
	Run(ctx context.Context, endpoint string, keys nodeauth.Keyring, req mediasample.Request) (mediasample.Result, error)
}

// localRunner runs one request with this server's ffmpeg.
type localRunner func(ctx context.Context, req mediasample.Request) (mediasample.Result, error)

// sampler runs a file's speech requests on a transcode node or locally.
type sampler struct {
	settings SettingsReader
	nodes    NodeSource
	// keys signs requests to nodes; without it every request runs locally.
	keys nodeauth.Keyring
	// grants grants the reserved node the file it samples.
	grants       nodesource.FileGranter
	remote       remoteRunner
	local        localRunner
	reservations *nodepool.Reservations
	// now is the clock grant renewal reads; nil is time.Now.
	now func() time.Time
}

func newSampler(settings SettingsReader, nodes NodeSource, keys nodeauth.Keyring, ffmpegPath func() string) *sampler {
	return &sampler{
		settings:     settings,
		nodes:        nodes,
		keys:         keys,
		remote:       mediasample.RemoteClient{},
		reservations: &nodepool.Reservations{},
		local: func(ctx context.Context, req mediasample.Request) (mediasample.Result, error) {
			return mediasample.Runner{FFmpegPath: ffmpegPath(), Workload: processmetrics.Analysis}.Run(ctx, req)
		},
	}
}

// remoteRequestTimeout covers a node's wait for a free slot, a window's decode
// timeout, and a minute to transfer the result.
const remoteRequestTimeout = mediasample.MaxRemoteAdmissionWait + (windowTimeoutSeconds+60)*time.Second

// grantRenewAfter is how long a node's source grant is used for windows
// before a fresh one is minted. A window's grant must still be valid when the
// node opens it, which can be a whole remoteRequestTimeout after the request
// starts; a file's windows can take longer than a grant lives.
const grantRenewAfter = nodesource.GrantLifetime - remoteRequestTimeout

// errNoNode reports that transcode_nodes_only found no node to run on.
var errNoNode = errors.New("no transcode node available for subtitle sync")

// run executes reqs, all sampling file, in order and returns their results
// and where they ran ("local" or the node's name). All requests of a file run
// in one place: a node is reserved once for the whole set. Under
// prefer_transcode_nodes a node that fails for reasons of its own hands the
// remaining requests to this server. done, when set, is called with the
// number of requests finished after each one.
func (s *sampler) run(ctx context.Context, file *models.MediaFile, reqs []mediasample.Request, done func(int)) ([]mediasample.Result, string, error) {
	if done == nil {
		done = func(int) {}
	}
	mode := s.execution(ctx)
	if mode == ExecutionLocal {
		results, err := s.runLocal(ctx, reqs, nil, done)
		return results, ExecutionLocal, err
	}
	node, release := s.reserve(ctx)
	if node == nil {
		if mode == ExecutionTranscodeOnly {
			return nil, "", errNoNode
		}
		results, err := s.runLocal(ctx, reqs, nil, done)
		return results, ExecutionLocal, err
	}
	defer release()

	grant, err := nodesource.GrantFile(ctx, s.grants, node.ID, file)
	if err != nil {
		if mode == ExecutionTranscodeOnly {
			return nil, nodeLabel(node), fmt.Errorf("grant the node its source: %w", err)
		}
		slog.WarnContext(ctx, "subtitle sync node cannot be granted its source; sampling locally", "component", "subsync",
			"node", node.Name, "error", err)
		results, err := s.runLocal(ctx, reqs, nil, done)
		return results, ExecutionLocal, err
	}
	mintedAt := s.clock()
	endpoint := nodepool.NodeEndpoint(node.URL, mediasample.RemotePath)
	results := make([]mediasample.Result, 0, len(reqs))
	for i, req := range reqs {
		if err := ctx.Err(); err != nil {
			return nil, nodeLabel(node), err
		}
		if s.clock().Sub(mintedAt) >= grantRenewAfter {
			fresh, err := s.renewGrant(ctx, node.ID, file, grant)
			if err != nil {
				return nil, nodeLabel(node), err
			}
			grant, mintedAt = fresh, s.clock()
		}
		req.SourceGrant = grant
		result, err := s.runRemote(ctx, endpoint, req)
		if err == nil {
			results = append(results, result)
			done(len(results))
			continue
		}
		var remoteErr *mediasample.RemoteError
		if mode == ExecutionPreferTranscode && errors.As(err, &remoteErr) && remoteErr.Infrastructure() && ctx.Err() == nil {
			slog.WarnContext(ctx, "subtitle sync node sampling failed; continuing locally", "component", "subsync",
				"node", node.Name, "error", err)
			all, localErr := s.runLocal(ctx, reqs[i:], results, done)
			return all, ExecutionLocal, localErr
		}
		return nil, nodeLabel(node), err
	}
	return results, nodeLabel(node), nil
}

// renewGrant mints a fresh grant for node nodeID to use file, for windows
// sampled after the job's first grant ages. The fresh grant must pin the same
// revision as current: windows of two versions of a file would align the
// subtitle against a mix of both, so a file that changed fails the job with
// nodesource.ErrSourceChanged.
func (s *sampler) renewGrant(ctx context.Context, nodeID int, file *models.MediaFile, current string) (string, error) {
	fresh, err := nodesource.GrantFile(ctx, s.grants, nodeID, file)
	if err != nil {
		return "", fmt.Errorf("renew the node's source grant: %w", err)
	}
	if fresh == "" && current == "" {
		// No granter wired: there is no grant to renew.
		return "", nil
	}
	pinned, err := nodesource.GrantRevision(current)
	if err != nil {
		return "", fmt.Errorf("read the node's source grant: %w", err)
	}
	renewed, err := nodesource.GrantRevision(fresh)
	if err != nil {
		return "", fmt.Errorf("read the node's renewed source grant: %w", err)
	}
	if renewed != pinned {
		return "", fmt.Errorf("%w: the file changed while its speech was sampled", nodesource.ErrSourceChanged)
	}
	return fresh, nil
}

func (s *sampler) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// runRemote bounds one node request: the node's admission wait and attempt
// timeout plus time to transfer the result. A node that accepts a request and then hangs
// would otherwise hold the job, its slot, and the node reservation forever.
func (s *sampler) runRemote(ctx context.Context, endpoint string, req mediasample.Request) (mediasample.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, remoteRequestTimeout)
	defer cancel()
	return s.remote.Run(ctx, endpoint, s.keys, req)
}

// runLocal runs reqs here, appending their results to those already done.
func (s *sampler) runLocal(ctx context.Context, reqs []mediasample.Request, results []mediasample.Result, done func(int)) ([]mediasample.Result, error) {
	for _, req := range reqs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := s.local(ctx, req)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
		done(len(results))
	}
	return results, nil
}

func (s *sampler) reserve(ctx context.Context) (*nodepool.Node, func()) {
	if s.nodes == nil || s.keys == nil {
		return nil, func() {}
	}
	node, release, outcome := s.reservations.Reserve(s.nodes.KeyedNodes(), s.nodeCapacity(ctx))
	if outcome != nodepool.Reserved {
		return nil, release
	}
	return node, release
}

func (s *sampler) execution(ctx context.Context) string {
	switch mode := s.setting(ctx, SettingExecution); mode {
	case ExecutionLocal, ExecutionPreferTranscode, ExecutionTranscodeOnly:
		return mode
	}
	return DefaultExecution
}

func (s *sampler) nodeCapacity(ctx context.Context) int {
	if n, err := strconv.Atoi(s.setting(ctx, SettingNodeCapacity)); err == nil && n > 0 {
		return n
	}
	return 1
}

func (s *sampler) setting(ctx context.Context, key string) string {
	if s.settings == nil {
		return ""
	}
	value, err := s.settings.Get(ctx, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func nodeLabel(node *nodepool.Node) string {
	if node.Name != "" {
		return "node:" + node.Name
	}
	return fmt.Sprintf("node:%d", node.ID)
}

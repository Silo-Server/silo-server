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
	"github.com/Silo-Server/silo-server/internal/nodepool"
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
	settingJWTSecret    = "auth.jwt_secret"
)

// DefaultExecution sends speech decoding to transcode nodes when there are
// any: they sit next to the media and keep the decode off the API server.
const DefaultExecution = ExecutionPreferTranscode

// SettingsReader reads server settings.
type SettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// NodeSource lists the transcode nodes.
type NodeSource interface {
	Nodes() []*nodepool.Node
}

// remoteRunner runs one request on a node; mediasample.RemoteClient is one.
type remoteRunner interface {
	Run(ctx context.Context, endpoint, secret string, req mediasample.Request) (mediasample.Result, error)
}

// localRunner runs one request with this server's ffmpeg.
type localRunner func(ctx context.Context, req mediasample.Request) (mediasample.Result, error)

// sampler runs a file's speech requests on a transcode node or locally.
type sampler struct {
	settings     SettingsReader
	nodes        NodeSource
	remote       remoteRunner
	local        localRunner
	reservations *nodepool.Reservations
}

func newSampler(settings SettingsReader, nodes NodeSource, ffmpegPath func() string) *sampler {
	return &sampler{
		settings:     settings,
		nodes:        nodes,
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

// errNoNode reports that transcode_nodes_only found no node to run on.
var errNoNode = errors.New("no transcode node available for subtitle sync")

// run executes reqs in order and returns their results and where they ran
// ("local" or the node's name). All requests of a file run in one place: a
// node is reserved once for the whole set. Under prefer_transcode_nodes a
// node that fails for reasons of its own hands the remaining requests to
// this server. done, when set, is called with the number of requests
// finished after each one.
func (s *sampler) run(ctx context.Context, reqs []mediasample.Request, done func(int)) ([]mediasample.Result, string, error) {
	if done == nil {
		done = func(int) {}
	}
	mode := s.execution(ctx)
	if mode == ExecutionLocal {
		results, err := s.runLocal(ctx, reqs, nil, done)
		return results, ExecutionLocal, err
	}
	node, release, secret := s.reserve(ctx)
	if node == nil {
		if mode == ExecutionTranscodeOnly {
			return nil, "", errNoNode
		}
		results, err := s.runLocal(ctx, reqs, nil, done)
		return results, ExecutionLocal, err
	}
	defer release()

	endpoint := nodepool.NodeEndpoint(node.URL, mediasample.RemotePath)
	results := make([]mediasample.Result, 0, len(reqs))
	for i, req := range reqs {
		if err := ctx.Err(); err != nil {
			return nil, nodeLabel(node), err
		}
		result, err := s.runRemote(ctx, endpoint, secret, req)
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

// runRemote bounds one node request: the node's admission wait and attempt
// timeout plus time to transfer the result. A node that accepts a request and then hangs
// would otherwise hold the job, its slot, and the node reservation forever.
func (s *sampler) runRemote(ctx context.Context, endpoint, secret string, req mediasample.Request) (mediasample.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, remoteRequestTimeout)
	defer cancel()
	return s.remote.Run(ctx, endpoint, secret, req)
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

func (s *sampler) reserve(ctx context.Context) (*nodepool.Node, func(), string) {
	if s.nodes == nil {
		return nil, func() {}, ""
	}
	secret := s.setting(ctx, settingJWTSecret)
	if secret == "" {
		return nil, func() {}, ""
	}
	node, release, outcome := s.reservations.Reserve(s.nodes.Nodes(), s.nodeCapacity(ctx))
	if outcome != nodepool.Reserved {
		return nil, release, ""
	}
	return node, release, secret
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

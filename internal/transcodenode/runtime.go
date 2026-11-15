package transcodenode

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// RuntimeConfig is where the server takes its configuration and its
// registered identity from: the core, through *nodeconfig.Remote.
type RuntimeConfig interface {
	// Config is the configuration the node runs with; nil before the first
	// read. Its pointer changes only when the configuration does, and
	// handlers refuse work when it changes under them.
	Config() *config.Config
	// NodeRowID is the node's stream_nodes id, once known.
	NodeRowID() (int, bool)
	// NodeDrainRequested reports an administrator's drain request.
	NodeDrainRequested() bool
	// OnNodeDrainChange registers fn to run when the drain request changes.
	OnNodeDrainChange(fn func(requested bool))
	// ForceReload rereads the configuration now.
	ForceReload(ctx context.Context) error
}

// SessionTracker records the node's active sessions for the admin views.
// *nodesessions.LocalTracker keeps them for the node's reports to the core.
// Handlers call it on the request path, so it must be quick.
type SessionTracker interface {
	Track(context.Context, nodesessions.SessionInfo)
	Remove(context.Context, string)
	Cleanup(context.Context)
}

// Core is what the node asks the core (internal/nodecontrol): the recipe and
// playback identity of a transport it lost or serves as a progressive remux,
// and the revocation of its recipes in a force reload. The node verifies no
// stream token and checks no deny marker itself. *nodecontrol.Client
// implements it.
type Core interface {
	Resolve(ctx context.Context, req nodecontrol.ResolveRequest) (*nodecontrol.Resolution, error)
	RevokeRecipes(ctx context.Context) error
}

// SourceOpener opens a job's input through the core with the grant the job
// carries, and keeps the grant renewed until the source is closed.
// *nodesource.Opener implements it.
type SourceOpener interface {
	Open(ctx context.Context, grant string) (nodesource.Source, error)
}

// SetCore wires the core's playback authority. Without it the node rebuilds
// no lost transcode and serves no progressive remux.
func (s *Server) SetCore(core Core) {
	s.core = core
}

// SetSourceOpener wires the core's source access. Without it the node
// refuses every job: it never runs FFmpeg on a path a request names.
func (s *Server) SetSourceOpener(sources SourceOpener) {
	s.opener = sources
}

// SetKeyRejected wires the report of whether the core refuses this node's
// key, which health carries as key_rejected so the core stops placing work
// here. It must be called during construction.
func (s *Server) SetKeyRejected(rejected func() bool) {
	s.keyRejected = rejected
}

package transcodenode

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
)

// RuntimeConfig is where the server takes its configuration and its
// registered identity from. A transcode node reads both from the core
// (*nodeconfig.Remote); *nodeconfig.Watcher, which reads the database, also
// implements it.
type RuntimeConfig interface {
	// Config is the configuration the node runs with. Its pointer changes only
	// when the configuration does; handlers refuse work when it changes under
	// them.
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
// *nodesessions.LocalTracker keeps them for the node's reports;
// *nodesessions.Tracker writes them to Redis. Starts call Track on the request
// path, under the session's lifecycle lock, so it must be quick.
type SessionTracker interface {
	Track(context.Context, nodesessions.SessionInfo)
	Remove(context.Context, string)
	Cleanup(context.Context)
	NodeURL() string
	NodeName() string
}

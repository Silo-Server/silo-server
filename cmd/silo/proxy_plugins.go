package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// proxyPluginHost is the plugin runtime a proxy node carries: the resident
// network access providers and nothing else. Each proxy runs its own instance
// of every enabled provider installation with its own overlay identity, kept
// under the instance-state scope node:<stream_nodes.id>, and reports that
// identity to the API through /health and the bearer network-access routes.
//
// No admin routes, no catalog, no installer, no metadata or other capability
// dispatch: lifecycle mutations happen on the API server and reach this node
// as cache.EventPluginsChanged, with a poll as the backstop.
type proxyPluginHost struct {
	host    *pluginhost.Host
	service *plugins.Service
	broker  *netaccess.Broker
	bus     cache.EventBus
	ctx     context.Context
}

// errProxyNodeRowUnknown is why a proxy runs no providers before the config
// watcher has matched this process to its stream_nodes row: without the row
// id there is no state scope to keep the overlay node key under.
var errProxyNodeRowUnknown = errors.New("this proxy's stream_nodes row is not known yet (NODE_URL or NODE_NAME must match an enabled proxy node); network access providers start once it resolves")

// newProxyPluginHost builds the proxy's plugin host, service and supervisor.
// It does not start anything; hooks() returns the listener-bracketing
// callbacks that do. cacheDir is this node's plugin cache root.
func newProxyPluginHost(
	ctx context.Context,
	pool *pgxpool.Pool,
	cipher *secret.Cipher,
	bus cache.EventBus,
	watcher *nodeconfig.Watcher,
	nodeName string,
	listen string,
	cacheDir string,
) *proxyPluginHost {
	broker := netaccess.NewBroker()
	installationStore := plugins.NewInstallationStore(pool)
	runtimeConfigStore := plugins.NewRuntimeConfigStore(pool, cipher)
	instanceState := plugins.NewInstanceStateStore(pool, cipher).ForNodeScope(watcher.NodeRowID)

	hostInfo := func(context.Context) (pluginhost.HostInfo, error) {
		live := watcher.Config()
		address := listen
		if live != nil && live.Server.Listen != "" {
			address = live.Server.Listen
		}
		info := pluginhost.HostInfo{
			Role: pluginhost.HostRoleProxy,
			Name: nodeName,
			// A proxy exposes only its own listener: clients fetch media here
			// and talk to the API server for everything else.
			Listeners: []pluginhost.HostListener{{
				Name:        pluginhost.ListenerAPI,
				Address:     pluginhost.LoopbackDialAddress(address),
				DefaultPort: pluginhost.DefaultPortAPI,
			}},
		}
		if id, ok := watcher.NodeRowID(); ok {
			info.NodeID = int64(id)
		}
		return info, nil
	}

	host := pluginhost.NewHost(pluginhost.Config{
		HostInfo:      hostInfo,
		InstanceState: instanceState,
		NetworkAccess: broker,
		GlobalConfigSetter: pluginhost.GlobalConfigSetterFunc(
			func(ctx context.Context, installationID int, key string, value map[string]any) error {
				return runtimeConfigStore.PutGlobalConfig(ctx, installationID, key, value)
			},
		),
		Logger: hclog.New(&hclog.LoggerOptions{
			Name:   "plugin-host",
			Level:  hclog.Info,
			Output: os.Stderr,
		}),
	})
	// Plugin binaries are rehydrated from plugin_archives into this node's
	// own cache dir (SILO_PLUGIN_CACHE_DIR): the API server's install paths
	// name directories on its machine, not this one.
	service := plugins.NewNodeService(installationStore, runtimeConfigStore, plugins.NewHostAdapter(host), cacheDir)
	host.SetExitHandler(service.HandleResidentExit)
	service.SetNetworkAccessHostInfo(hostInfo)
	service.SetNetworkAccessStatusSink(broker)
	// A proxy whose row is unknown must not start providers: their node keys
	// would have no scope. The gate is re-evaluated on every reconcile, so
	// the poll picks the row up once the watcher resolves it.
	service.SetResidentGate(func(context.Context) error {
		if _, ok := watcher.NodeRowID(); !ok {
			return errProxyNodeRowUnknown
		}
		return nil
	})
	return &proxyPluginHost{host: host, service: service, broker: broker, bus: bus, ctx: ctx}
}

// hooks returns the callbacks startStandaloneServer runs around the listener:
// residents start once the address is bound and stop before it drains.
func (p *proxyPluginHost) hooks() standaloneServerHooks {
	return standaloneServerHooks{
		afterListen: func() {
			p.service.StartResidents(p.ctx)
			if err := p.service.FollowLifecycleChanges(p.ctx, p.bus, plugins.DefaultLifecyclePollInterval); err != nil {
				slog.Warn("proxy plugin lifecycle subscription failed; reconciling on the poll only", "component", "plugins", "error", err)
			}
		},
		beforeDrain: func(ctx context.Context) {
			if err := p.service.StopResidents(ctx); err != nil {
				slog.ErrorContext(ctx, "resident plugin shutdown error", "component", "plugins", "error", err)
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.host.Shutdown(shutdownCtx); err != nil {
				slog.WarnContext(ctx, "failed to shut down plugin host", "component", "plugins", "error", err)
			}
		},
	}
}

// warnOnMultipleAPIReplicas registers this API replica's presence and logs
// the single-API constraint when another live replica is seen: network
// access providers keep their overlay node key under one shared "api" state
// scope, so two replicas would present one overlay identity from two
// machines. Best effort and never fatal; a Redis-less deployment cannot have
// a second replica reading the same state.
func warnOnMultipleAPIReplicas(ctx context.Context, presence *cache.APIReplicaPresence, service *plugins.Service) {
	if presence == nil || service == nil {
		return
	}
	if err := presence.Register(ctx); err != nil {
		slog.DebugContext(ctx, "api replica presence unavailable", "component", "plugins", "error", err)
		return
	}
	providers, err := service.ListNetworkAccessProviders(ctx)
	if err != nil || len(providers) == 0 {
		return
	}
	count, err := presence.Count(ctx)
	if err != nil {
		slog.DebugContext(ctx, "api replica census unavailable", "component", "plugins", "error", err)
		return
	}
	if count > 1 {
		slog.WarnContext(ctx, "network access providers are installed and more than one API replica is live; they share one overlay identity under the api state scope, which phase one does not support — run a single API server or disable the providers",
			"component", "plugins", "replicas", count)
	}
}

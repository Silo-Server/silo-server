package pluginhost

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	sdkruntime "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
)

type Config struct {
	Logger              hclog.Logger
	HealthCheckInterval time.Duration
	HealthFailureLimit  int

	// EventPublisher receives events that plugins publish via RuntimeHost.
	// Typically silo's *events.Hub. When nil, plugins that try to
	// PublishEvent will receive an error.
	EventPublisher EventPublisher
	// LibraryLister answers ListLibraries calls from plugins. When nil,
	// plugins will see empty library lists.
	LibraryLister LibraryLister
	// CatalogPresence answers CheckMediaPresence calls from plugins. When nil,
	// plugins will receive empty presence results.
	CatalogPresence CatalogPresenceLookup
	// InstalledPlugins answers ListInstalledPlugins calls from plugins. When nil,
	// plugins will receive an empty plugin list.
	InstalledPlugins InstalledPluginLister
	// GlobalConfigSetter persists SetGlobalConfigEntry calls from plugins. When
	// nil, SetGlobalConfigEntry returns an error.
	GlobalConfigSetter GlobalConfigSetter
	// VirtualCatalog owns virtual media registration requested by plugins.
	VirtualCatalog VirtualCatalogRegistrar
}

type StartRequest struct {
	InstallationID int
	BinaryPath     string
	Manifest       *pluginv1.PluginManifest
	Config         []*pluginv1.ConfigEntry
}

type Host struct {
	logger              hclog.Logger
	healthCheckInterval time.Duration
	healthFailureLimit  int

	eventPublisher     EventPublisher
	libraryLister      LibraryLister
	catalogPresence    CatalogPresenceLookup
	installedPlugins   InstalledPluginLister
	globalConfigSetter GlobalConfigSetter
	virtualCatalog     VirtualCatalogRegistrar

	mu        sync.RWMutex
	instances map[int]*instance
}

type instance struct {
	process      *plugin.Client
	protocol     plugin.ClientProtocol
	client       *Client
	cancelHealth context.CancelFunc
}

func NewHost(cfg Config) *Host {
	logger := cfg.Logger
	if logger == nil {
		logger = hclog.NewNullLogger()
	}
	interval := cfg.HealthCheckInterval
	if interval <= 0 {
		interval = DefaultHealthCheckInterval
	}
	failureLimit := cfg.HealthFailureLimit
	if failureLimit <= 0 {
		failureLimit = DefaultHealthFailureLimit
	}

	return &Host{
		logger:              logger,
		healthCheckInterval: interval,
		healthFailureLimit:  failureLimit,
		eventPublisher:      cfg.EventPublisher,
		libraryLister:       cfg.LibraryLister,
		catalogPresence:     cfg.CatalogPresence,
		installedPlugins:    cfg.InstalledPlugins,
		globalConfigSetter:  cfg.GlobalConfigSetter,
		virtualCatalog:      cfg.VirtualCatalog,
		instances:           make(map[int]*instance),
	}
}

func (h *Host) Start(ctx context.Context, req StartRequest) (*Client, error) {
	if req.InstallationID == 0 {
		return nil, fmt.Errorf("installation id is required")
	}
	if req.BinaryPath == "" {
		return nil, fmt.Errorf("plugin binary path is required")
	}
	if req.Manifest == nil {
		return nil, fmt.Errorf("plugin manifest is required")
	}

	h.mu.Lock()
	if existing, ok := h.instances[req.InstallationID]; ok {
		delete(h.instances, req.InstallationID)
		h.mu.Unlock()
		h.stopInstance(existing)
		h.mu.Lock()
	}
	h.mu.Unlock()

	process := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: HandshakeConfig(),
		AllowedProtocols: []plugin.Protocol{
			plugin.ProtocolGRPC,
		},
		Cmd:        exec.Command(req.BinaryPath),
		Plugins:    sdkruntime.DefaultPluginSet(sdkruntime.CapabilityServers{}),
		Logger:     h.logger,
		Stderr:     os.Stderr,
		SyncStdout: os.Stdout,
		SyncStderr: os.Stderr,
	})

	protocol, err := process.Client()
	if err != nil {
		process.Kill()
		return nil, fmt.Errorf("start plugin process: %w", err)
	}

	rawClient, err := protocol.Dispense(sdkruntime.PluginSetName)
	if err != nil {
		_ = protocol.Close()
		process.Kill()
		return nil, fmt.Errorf("dispense plugin runtime client: %w", err)
	}

	rpcClient, ok := rawClient.(*sdkruntime.Client)
	if !ok {
		_ = protocol.Close()
		process.Kill()
		return nil, fmt.Errorf("unexpected plugin runtime client type %T", rawClient)
	}

	if err := h.bindRuntimeHost(ctx, rpcClient, req.Manifest.GetPluginId(), req.InstallationID); err != nil {
		_ = protocol.Close()
		process.Kill()
		return nil, fmt.Errorf("bind runtime host: %w", err)
	}

	controlCtx, cancel := ensureDeadline(ctx, DefaultControlTimeout)
	defer cancel()

	liveManifestResponse, err := rpcClient.Runtime().GetManifest(controlCtx, &pluginv1.GetManifestRequest{})
	if err != nil {
		_ = protocol.Close()
		process.Kill()
		return nil, fmt.Errorf("fetch plugin manifest: %w", err)
	}
	if liveManifestResponse.GetManifest() == nil {
		_ = protocol.Close()
		process.Kill()
		return nil, fmt.Errorf("plugin runtime returned an empty manifest")
	}
	if !proto.Equal(req.Manifest, liveManifestResponse.GetManifest()) {
		_ = protocol.Close()
		process.Kill()
		return nil, fmt.Errorf("plugin runtime manifest does not match installed manifest")
	}
	configureCtx, configureCancel := ensureDeadline(ctx, DefaultControlTimeout)
	defer configureCancel()

	_, err = rpcClient.Runtime().Configure(configureCtx, &pluginv1.ConfigureRequest{
		Config: req.Config,
	})
	if err != nil && status.Code(err) != codes.Unimplemented {
		_ = protocol.Close()
		process.Kill()
		return nil, fmt.Errorf("configure plugin runtime: %w", err)
	}

	client := newClient(req.InstallationID, rpcClient, liveManifestResponse.GetManifest())

	healthCtx, healthCancel := context.WithCancel(context.Background())
	instance := &instance{
		process:      process,
		protocol:     protocol,
		client:       client,
		cancelHealth: healthCancel,
	}

	h.mu.Lock()
	h.instances[req.InstallationID] = instance
	h.mu.Unlock()

	go h.monitorHealth(healthCtx, req.InstallationID, instance)

	return client, nil
}

func (h *Host) Client(installationID int) (*Client, error) {
	h.mu.RLock()
	instance, ok := h.instances[installationID]
	h.mu.RUnlock()
	if !ok {
		return nil, ErrClientNotFound
	}

	instance.client.mu.RLock()
	unhealthy := instance.client.unhealthy
	instance.client.mu.RUnlock()
	if unhealthy {
		return nil, ErrPluginUnhealthy
	}

	return instance.client, nil
}

func (h *Host) Stop(installationID int) error {
	h.mu.Lock()
	instance, ok := h.instances[installationID]
	if ok {
		delete(h.instances, installationID)
	}
	h.mu.Unlock()

	if !ok {
		return ErrClientNotFound
	}
	h.stopInstance(instance)
	return nil
}

func (h *Host) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		h.mu.Lock()
		instances := make([]*instance, 0, len(h.instances))
		for installationID, instance := range h.instances {
			delete(h.instances, installationID)
			instances = append(instances, instance)
		}
		h.mu.Unlock()

		for _, instance := range instances {
			h.stopInstance(instance)
		}
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (h *Host) monitorHealth(ctx context.Context, installationID int, instance *instance) {
	ticker := time.NewTicker(h.healthCheckInterval)
	defer ticker.Stop()

	failures := 0
	healthClient := grpc_health_v1.NewHealthClient(instance.client.rpc.Conn())

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkCtx, cancel := context.WithTimeout(context.Background(), DefaultControlTimeout)
			_, err := healthClient.Check(checkCtx, &grpc_health_v1.HealthCheckRequest{Service: plugin.GRPCServiceName})
			cancel()
			if err == nil {
				failures = 0
				continue
			}

			failures++
			if failures < h.healthFailureLimit {
				continue
			}

			instance.client.markUnhealthy()
			h.logger.Error("plugin health check failed", "installation_id", installationID, "error", err)
			h.stopInstance(instance)
			return
		}
	}
}

func (h *Host) stopInstance(instance *instance) {
	if instance == nil {
		return
	}
	if instance.cancelHealth != nil {
		instance.cancelHealth()
	}
	if instance.protocol != nil {
		_ = instance.protocol.Close()
	}
	if instance.process != nil {
		instance.process.Kill()
	}
}

// bindRuntimeHost stands up a RuntimeHost gRPC server on a fresh broker
// stream and tells the plugin its stream ID via Runtime.BindHostBroker. The
// broker stream lives for the plugin's lifetime; closing the plugin process
// tears it down.
//
// Skipped when no RuntimeHost services are configured.
func (h *Host) bindRuntimeHost(ctx context.Context, sdkClient *sdkruntime.Client, pluginID string, installationID int) error {
	if h.eventPublisher == nil && h.libraryLister == nil && h.catalogPresence == nil && h.installedPlugins == nil && h.globalConfigSetter == nil && h.virtualCatalog == nil {
		return nil
	}

	broker := sdkClient.Broker()
	if broker == nil {
		// Non-gRPC protocol or client constructed directly (e.g. in tests);
		// broker is unavailable so skip binding.
		return nil
	}

	streamID := broker.NextId()
	go broker.AcceptAndServe(streamID, func(opts []grpc.ServerOption) *grpc.Server {
		s := grpc.NewServer(opts...)
		srv := NewRuntimeHostServerWithServices(
			h.eventPublisher,
			h.libraryLister,
			h.catalogPresence,
			h.installedPlugins,
			h.globalConfigSetter,
			h.virtualCatalog,
			pluginID,
			installationID,
		)
		pluginv1.RegisterRuntimeHostServer(s, srv)
		return s
	})

	bindCtx, cancel := ensureDeadline(ctx, DefaultControlTimeout)
	defer cancel()
	if _, err := sdkClient.Runtime().BindHostBroker(bindCtx, &pluginv1.BindHostBrokerRequest{BrokerId: streamID}); err != nil {
		// Plugins built against pre-RuntimeHost SDK versions don't implement
		// BindHostBroker. Tolerate that — they simply don't get host services
		// (PublishEvent / ListLibraries / CheckMediaPresence). Treat any
		// gRPC Unimplemented as a soft skip; everything else is still a hard
		// error because it indicates a real comm problem.
		if status.Code(err) == codes.Unimplemented {
			h.logger.Debug("plugin runtime does not implement BindHostBroker; skipping host bind", "plugin_id", pluginID)
			return nil
		}
		return fmt.Errorf("bind host broker: %w", err)
	}
	return nil
}

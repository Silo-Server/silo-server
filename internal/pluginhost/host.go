package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
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
	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

type Config struct {
	Logger              hclog.Logger
	HealthCheckInterval time.Duration
	HealthFailureLimit  int
	// ExitCheckInterval is how often the exit watcher polls the plugin
	// process for an exit. Zero takes DefaultExitCheckInterval.
	ExitCheckInterval time.Duration
	// IdleStopAfter is how long a process started with StopWhenIdle may go
	// unused before the host stops it. Zero never stops an idle process.
	IdleStopAfter time.Duration
	// IdleCheckInterval is how often such a process is checked. Zero takes
	// DefaultIdleCheckInterval.
	IdleCheckInterval time.Duration

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
	// HostInfo answers GetHostInfo. When nil, GetHostInfo is Unimplemented.
	HostInfo HostInfoFunc
	// InstanceState persists ReadInstanceState / WriteInstanceState for this
	// process's host scope. When nil, both RPCs fail with FailedPrecondition.
	InstanceState InstanceStateStore
	// RuntimeHostForStart binds host identity and state together for each launch.
	RuntimeHostForStart func(context.Context) (HostInfoFunc, InstanceStateStore, error)
	// NetworkAccess issues the ingress token for every network access provider
	// the host starts, revokes it when the process stops, and receives status
	// pushes. When nil, providers get no token and pushes are dropped.
	NetworkAccess NetworkAccessBroker
}

type StartRequest struct {
	InstallationID int
	BinaryPath     string
	Manifest       *pluginv1.PluginManifest
	Config         []*pluginv1.ConfigEntry
	// StopWhenIdle lets the host stop the process after Config.IdleStopAfter
	// without use. Only set it for a plugin whose callers go back through
	// Client or Start before every call, so the next one starts it again.
	// It is ignored for config test runs (negative installation ids).
	StopWhenIdle bool
}

type Host struct {
	logger              hclog.Logger
	healthCheckInterval time.Duration
	healthFailureLimit  int
	exitCheckInterval   time.Duration
	idleStopAfter       time.Duration
	idleCheckInterval   time.Duration
	// now is a test seam for the idle clock.
	now func() time.Time

	// exitHandler is told the installation id of a plugin whose process went
	// away on its own (exit or failed health), never one the host stopped on
	// purpose. The resident supervisor in internal/plugins uses it to
	// schedule a restart.
	exitMu      sync.RWMutex
	exitHandler func(installationID int)

	eventPublisher      EventPublisher
	libraryLister       LibraryLister
	catalogPresence     CatalogPresenceLookup
	installedPlugins    InstalledPluginLister
	globalConfigSetter  GlobalConfigSetter
	hostInfo            HostInfoFunc
	instanceState       InstanceStateStore
	runtimeHostForStart func(context.Context) (HostInfoFunc, InstanceStateStore, error)
	networkAccess       NetworkAccessBroker

	mu        sync.RWMutex
	instances map[int]*instance
	starting  map[int]chan struct{}
	// idleStopping holds, per installation, a channel closed once the idle
	// watcher has finished stopping a process it already removed from
	// instances.
	idleStopping map[int]chan struct{}
	startSeq     atomic.Uint64
}

type instance struct {
	process   *plugin.Client
	command   *exec.Cmd
	usageOnce sync.Once
	protocol  plugin.ClientProtocol
	client    *Client
	activity  *activity
	// installationID lets stopInstance revoke the ingress token of a network
	// access provider without a map lookup.
	installationID int
	// provider is the network_access_provider.v1 slug, empty otherwise, and
	// ingressToken the token issued to this process instance.
	provider     string
	ingressToken string
	// cancelMonitors stops the health probe and the exit watcher. Stop,
	// Shutdown and a replacing Start cancel it before tearing the process
	// down, which is how the monitors tell a deliberate stop from a crash.
	cancelMonitors context.CancelFunc
	retireOnce     sync.Once
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
	exitInterval := cfg.ExitCheckInterval
	if exitInterval <= 0 {
		exitInterval = DefaultExitCheckInterval
	}
	idleInterval := cfg.IdleCheckInterval
	if idleInterval <= 0 {
		idleInterval = DefaultIdleCheckInterval
	}

	return &Host{
		idleStopAfter:       max(cfg.IdleStopAfter, 0),
		idleCheckInterval:   idleInterval,
		now:                 time.Now,
		logger:              logger,
		healthCheckInterval: interval,
		healthFailureLimit:  failureLimit,
		exitCheckInterval:   exitInterval,
		eventPublisher:      cfg.EventPublisher,
		libraryLister:       cfg.LibraryLister,
		catalogPresence:     cfg.CatalogPresence,
		installedPlugins:    cfg.InstalledPlugins,
		globalConfigSetter:  cfg.GlobalConfigSetter,
		hostInfo:            cfg.HostInfo,
		instanceState:       cfg.InstanceState,
		runtimeHostForStart: cfg.RuntimeHostForStart,
		networkAccess:       cfg.NetworkAccess,
		instances:           make(map[int]*instance),
		starting:            make(map[int]chan struct{}),
		idleStopping:        make(map[int]chan struct{}),
	}
}

func (h *Host) Start(ctx context.Context, req StartRequest) (*Client, error) {
	startSeq := h.startSeq.Add(1)
	if req.InstallationID == 0 {
		return nil, fmt.Errorf("installation id is required")
	}
	if req.BinaryPath == "" {
		return nil, fmt.Errorf("plugin binary path is required")
	}
	if req.Manifest == nil {
		return nil, fmt.Errorf("plugin manifest is required")
	}
	// Serialize replacements per installation while allowing unrelated plugins
	// to launch independently. Failed-uninstall recovery can call Start
	// directly while the resident supervisor is already launching it.
	if err := h.acquireStart(ctx, req.InstallationID); err != nil {
		return nil, err
	}
	defer h.releaseStart(req.InstallationID)

	hostInfo, instanceState := h.hostInfo, h.instanceState
	if h.runtimeHostForStart != nil {
		var err error
		hostInfo, instanceState, err = h.runtimeHostForStart(ctx)
		if err != nil {
			return nil, fmt.Errorf("bind process host identity: %w", err)
		}
	}

	h.mu.Lock()
	if existing, ok := h.instances[req.InstallationID]; ok {
		delete(h.instances, req.InstallationID)
		h.mu.Unlock()
		h.stopInstance(existing)
		h.mu.Lock()
	}
	h.mu.Unlock()

	// A network access provider gets a fresh ingress token for this process
	// instance before it can ask GetHostInfo for it. Config test-runs
	// (negative installation ids) are not persisted installations and get
	// none.
	provider, isProvider := NetworkAccessProviderSlug(req.Manifest)
	var ingressToken string
	if !isProvider || req.InstallationID <= 0 || h.networkAccess == nil {
		provider = ""
	} else {
		token, err := h.networkAccess.Issue(req.InstallationID, provider)
		if err != nil {
			return nil, fmt.Errorf("issue ingress token: %w", err)
		}
		ingressToken = token
	}

	used := newActivity(h.now)
	command := exec.Command(req.BinaryPath)
	command.Env = ProcessEnv()
	process := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: HandshakeConfig(),
		GRPCDialOptions: []grpc.DialOption{
			grpc.WithChainUnaryInterceptor(used.unaryClient, observePluginRPC),
			grpc.WithChainStreamInterceptor(used.streamClient),
		},
		AllowedProtocols: []plugin.Protocol{
			plugin.ProtocolGRPC,
		},
		Cmd: command,
		// go-plugin appends the server's whole environment to Cmd.Env
		// unless told not to.
		SkipHostEnv: true,
		Plugins:     sdkruntime.DefaultPluginSet(sdkruntime.CapabilityServers{}),
		Logger:      h.logger,
		Stderr:      os.Stderr,
		SyncStdout:  os.Stdout,
		SyncStderr:  os.Stderr,
	})
	retained := false
	defer func() {
		if !retained {
			// Kill waits for go-plugin's existing Wait goroutine before it
			// returns. Only then is ProcessState safe to read.
			process.Kill()
			processmetrics.Record(processmetrics.Plugin, command.ProcessState, nil, nil)
			if provider != "" {
				h.networkAccess.Revoke(req.InstallationID, ingressToken)
			}
		}
	}()

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

	if err := h.bindRuntimeHost(ctx, rpcClient, used, req.Manifest.GetPluginId(), req.InstallationID, provider, ingressToken, hostInfo, instanceState); err != nil {
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
	// Fields and enum values from a newer SDK don't count as a mismatch: both
	// copies are compared without them, and the client keeps the live copy
	// that way.
	liveManifest := KnownManifest(liveManifestResponse.GetManifest())
	if !proto.Equal(KnownManifest(req.Manifest), liveManifest) {
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

	client := newClient(req.InstallationID, rpcClient, liveManifest, startSeq)
	client.ingressToken = ingressToken

	monitorCtx, monitorCancel := context.WithCancel(context.Background())
	instance := &instance{
		process:        process,
		command:        command,
		protocol:       protocol,
		client:         client,
		activity:       used,
		installationID: req.InstallationID,
		provider:       provider,
		ingressToken:   ingressToken,
		cancelMonitors: monitorCancel,
	}

	h.mu.Lock()
	h.instances[req.InstallationID] = instance
	h.mu.Unlock()
	retained = true

	go h.monitorHealth(monitorCtx, req.InstallationID, instance)
	go h.watchExit(monitorCtx, req.InstallationID, instance)
	if req.StopWhenIdle && req.InstallationID > 0 && h.idleStopAfter > 0 {
		go h.watchIdle(monitorCtx, req.InstallationID, instance)
	}

	return client, nil
}

func (h *Host) acquireStart(ctx context.Context, id int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h.mu.Lock()
		pending, busy := h.starting[id]
		if !busy {
			h.starting[id] = make(chan struct{})
			h.mu.Unlock()
			return nil
		}
		h.mu.Unlock()
		select {
		case <-pending:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (h *Host) releaseStart(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	close(h.starting[id])
	delete(h.starting, id)
}

// SetExitHandler registers the callback told about plugin processes that
// stop on their own: the process exited, or the gRPC health probe gave up on
// it. Deliberate stops (Stop, Shutdown, a replacing Start) are not reported.
// The callback runs on the monitor goroutine and must not block.
func (h *Host) SetExitHandler(handler func(installationID int)) {
	h.exitMu.Lock()
	h.exitHandler = handler
	h.exitMu.Unlock()
}

// Client returns the running client for a caller about to use it, which
// counts as a use: the idle watcher takes the write side of h.mu to remove an
// instance, so one returned here is not stopped for another idle period.
func (h *Host) Client(installationID int) (*Client, error) {
	h.mu.RLock()
	instance, ok := h.instances[installationID]
	if ok && instance.activity != nil {
		instance.activity.touch()
	}
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

// Running reports whether a healthy process is up for the installation
// without counting as a use of it, for status reads.
func (h *Host) Running(installationID int) bool {
	h.mu.RLock()
	instance, ok := h.instances[installationID]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	instance.client.mu.RLock()
	defer instance.client.mu.RUnlock()
	return !instance.client.unhealthy
}

func (h *Host) NextStartSeq() uint64 { return h.startSeq.Load() }

func (h *Host) Stop(installationID int) error {
	h.mu.Lock()
	instance, ok := h.instances[installationID]
	if ok {
		delete(h.instances, installationID)
	}
	idleStop := h.idleStopping[installationID]
	h.mu.Unlock()

	if !ok {
		// The process may still be exiting after an idle stop. Callers treat
		// a return from Stop as the process being gone.
		if idleStop != nil {
			<-idleStop
		}
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
		idleStops := make([]chan struct{}, 0, len(h.idleStopping))
		for _, idleStop := range h.idleStopping {
			idleStops = append(idleStops, idleStop)
		}
		h.mu.Unlock()

		for _, instance := range instances {
			h.stopInstance(instance)
		}
		for _, idleStop := range idleStops {
			<-idleStop
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

			h.retireInstance(ctx, installationID, instance, fmt.Errorf("plugin health check failed: %w", err))
			return
		}
	}
}

// watchExit polls the plugin process so a crash is noticed within one
// interval instead of at the next failed health probe (which needs several
// misses at DefaultHealthCheckInterval).
func (h *Host) watchExit(ctx context.Context, installationID int, instance *instance) {
	ticker := time.NewTicker(h.exitCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !instance.process.Exited() {
				continue
			}
			h.retireInstance(ctx, installationID, instance, errors.New("plugin process exited"))
			return
		}
	}
}

// retireInstance marks a monitored instance dead: it is unhealthy for
// callers holding the *Client, is removed from the host so the next Client
// lookup reports ErrClientNotFound, and its process is reaped. The exit
// handler is told only when this instance is still the one the host maps
// for the installation: a canceled ctx or a different (or missing) mapped
// instance means Stop, Shutdown or a replacing Start owns the teardown, so
// a crash noticed while one of those is in flight is not reported twice.
func (h *Host) retireInstance(ctx context.Context, installationID int, instance *instance, cause error) {
	instance.retireOnce.Do(func() {
		if ctx.Err() != nil {
			return
		}
		instance.client.markUnhealthy()
		h.mu.Lock()
		current, ok := h.instances[installationID]
		if ok && current == instance {
			delete(h.instances, installationID)
		}
		h.mu.Unlock()
		if !ok || current != instance {
			return
		}
		h.logger.Error("plugin instance retired", "installation_id", installationID, "error", cause)
		h.stopInstance(instance)

		h.exitMu.RLock()
		handler := h.exitHandler
		h.exitMu.RUnlock()
		if handler != nil {
			handler(installationID)
		}
	})
}

func (h *Host) stopInstance(instance *instance) {
	h.teardown(instance, false)
}

// teardown stops an instance already removed from the host. A graceful one
// leaves closing the connection to Kill, which then asks the plugin to shut
// down and waits briefly for it to exit by itself, so a routine idle stop is
// not logged and counted as a killed process.
func (h *Host) teardown(instance *instance, graceful bool) {
	if instance == nil {
		return
	}
	if instance.cancelMonitors != nil {
		instance.cancelMonitors()
	}
	if instance.protocol != nil && !graceful {
		_ = instance.protocol.Close()
	}
	if instance.process != nil {
		instance.process.Kill()
		instance.usageOnce.Do(func() {
			if instance.command != nil {
				processmetrics.Record(processmetrics.Plugin, instance.command.ProcessState, nil, nil)
			}
		})
	}
	// The token dies with the process: a stale one is refused until the next
	// Start issues a replacement and the plugin re-reads GetHostInfo.
	if instance.provider != "" && h.networkAccess != nil {
		h.networkAccess.Revoke(instance.installationID, instance.ingressToken)
	}
}

// bindRuntimeHost stands up a RuntimeHost gRPC server on a fresh broker
// stream and tells the plugin its stream ID via Runtime.BindHostBroker. The
// broker stream lives for the plugin's lifetime; closing the plugin process
// tears it down.
//
// Skipped when no RuntimeHost services are configured.
func (h *Host) bindRuntimeHost(ctx context.Context, sdkClient *sdkruntime.Client, used *activity, pluginID string, installationID int, provider, ingressToken string, hostInfo HostInfoFunc, instanceState InstanceStateStore) error {
	if h.eventPublisher == nil && h.libraryLister == nil && h.catalogPresence == nil && h.installedPlugins == nil && h.globalConfigSetter == nil &&
		hostInfo == nil && instanceState == nil && h.networkAccess == nil {
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
		s := grpc.NewServer(append(opts, grpc.ChainUnaryInterceptor(used.unaryServer, observePluginCallback))...)
		srv := NewRuntimeHostServerWithOptions(RuntimeHostOptions{
			Publisher:             h.eventPublisher,
			Libraries:             h.libraryLister,
			Catalog:               h.catalogPresence,
			InstalledPlugins:      h.installedPlugins,
			GlobalConfigSetter:    h.globalConfigSetter,
			HostInfo:              hostInfo,
			InstanceState:         instanceState,
			NetworkAccess:         h.networkAccess,
			Logger:                h.logger,
			PluginID:              pluginID,
			InstallationID:        installationID,
			NetworkAccessProvider: provider,
			IngressToken:          ingressToken,
		})
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

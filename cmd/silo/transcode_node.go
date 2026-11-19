package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/logfilter"
	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/Silo-Server/silo-server/internal/netadmin"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodedeclare"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/tcpcongestion"
	"github.com/Silo-Server/silo-server/internal/telemetry"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/tuning"
)

// envCoreURL is the URL a transcode node reaches the core at. With its own
// key (envNodeKey) it is all the node needs: everything else comes from the
// core.
const envCoreURL = "SILO_CORE_URL"

// transcodeNodeIgnoredEnv are variables a transcode node once read and now
// takes from the core or no longer needs: the database and Redis it read
// settings, recipes and deny markers from, the data cipher's key, and the URL
// and name it matched its row by. Shared env files still set them, so they
// are warned about rather than refused. NODE_URL and NODE_NAME stay ignored
// although a transcode node now declares its URL and name: an env file shared
// with a proxy sets them, and honoring them on upgrade would rewrite rows. A
// node declares them with SILO_NODE_URL and SILO_NODE_NAME (nodedeclare).
var transcodeNodeIgnoredEnv = []string{envDatabaseURL, envRedisURL, envSecretKey, envNodeURL, envNodeName}

// The variables a transcode node once read; see transcodeNodeIgnoredEnv.
const (
	envDatabaseURL = "DATABASE_URL"
	envRedisURL    = "REDIS_URL"
	envSecretKey   = "SECRET_KEY"
	envNodeURL     = "NODE_URL"
	envNodeName    = "NODE_NAME"
)

// transcodeNodeLogInterval flushes a partial batch of the node's log entries
// to the core, as an API server's consumer flushes to Postgres.
const transcodeNodeLogInterval = 2 * time.Second

// nodeHealthNotifyTimeout bounds telling the core about a drain change.
const nodeHealthNotifyTimeout = 5 * time.Second

// transcodeNodeMode reports whether this process runs as a transcode node:
// MODE=transcode in the environment or the .env file, which it loads as the
// bootstrap would.
func transcodeNodeMode(envFile string) bool {
	if envFile != "" {
		_ = godotenv.Load(envFile)
	}
	return strings.TrimSpace(os.Getenv("MODE")) == serverModeTranscode
}

// transcodeNodeEnv is what a transcode node reads from its environment.
type transcodeNodeEnv struct {
	coreURL string
	nodeKey string
	listen  string
	// ignored names the set variables a transcode node does not use.
	ignored []string
	// insecureCoreURL reports a plain-HTTP core URL off this host.
	insecureCoreURL bool
	// declaration is the configuration the node declares from its
	// environment; see nodedeclare.
	declaration nodepool.UpdateNodeInput
}

// loadTranscodeNodeEnv reads and checks the node's environment. A missing core
// URL or key, or a key that is not a well-formed node key, is an error: the
// node could not even ask the core for its configuration.
func loadTranscodeNodeEnv(getenv func(string) string) (transcodeNodeEnv, error) {
	env := transcodeNodeEnv{coreURL: strings.TrimSpace(getenv(envCoreURL))}
	if env.coreURL == "" {
		return transcodeNodeEnv{}, fmt.Errorf("%s is required: the URL this node reaches the main server at", envCoreURL)
	}
	parsed, err := url.Parse(env.coreURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return transcodeNodeEnv{}, fmt.Errorf("%s must be an absolute http or https URL", envCoreURL)
	}
	env.insecureCoreURL = parsed.Scheme == "http" && !loopbackHost(parsed.Hostname())
	key, err := transcodeNodeKey(getenv)
	if err != nil {
		return transcodeNodeEnv{}, err
	}
	env.nodeKey = key
	port := strings.TrimSpace(getenv("PORT"))
	if port == "" {
		port = "8080"
	}
	env.listen = ":" + port
	if env.declaration, err = nodedeclare.FromEnv(nodepool.NodeTypeTranscode, getenv); err != nil {
		return transcodeNodeEnv{}, err
	}
	for _, name := range transcodeNodeIgnoredEnv {
		if getenv(name) != "" {
			env.ignored = append(env.ignored, name)
		}
	}
	return env, nil
}

// loopbackHost reports whether host names this machine's loopback.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// runTranscodeNode runs this process as a transcode node. It reads no
// settings, holds no database, Redis, cipher, shared secret or event bus, and
// takes its configuration, playback authority and sources from the core
// through the node control plane (internal/nodecontrol). It returns when the
// node has shut down; a broken environment exits the process.
func runTranscodeNode() {
	env, err := loadTranscodeNodeEnv(os.Getenv)
	if err != nil {
		slog.Error("transcode node cannot start", "error", err)
		os.Exit(1)
	}
	if len(env.ignored) > 0 {
		slog.Warn("transcode nodes ignore these variables; the main server supplies their configuration", "variables", env.ignored)
	}
	if env.insecureCoreURL {
		slog.Warn("this node reaches the main server over plain HTTP: it takes its FFmpeg path, directories and input paths from the answers, "+
			"so the network between them must be trusted, or the URL must use HTTPS", "core_url", env.coreURL)
	}
	client, err := nodecontrol.NewClient(env.coreURL, env.nodeKey)
	if err != nil {
		slog.Error("transcode node cannot start", "error", err)
		os.Exit(1)
	}
	stopDebugListener, err := startBootstrapDebugListener(true)
	if err != nil {
		slog.Error("local profiling configuration", "error", err)
		os.Exit(1)
	}
	defer stopDebugListener()

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	remote := nodeconfig.NewRemote(client, env.listen)
	// The node runs with what it declares, whatever its row says.
	remote.SetDeclared(nodecontrol.RegistrationFrom(env.declaration))
	remote.OnLoad(normalizeLoadedConfig)
	// A 401 from the core means the key was rotated or the node deleted:
	// health says so, which takes the node out of placement, until a
	// configuration read succeeds again.
	client.SetOnUnauthorized(remote.ReportUnauthorized)
	// The listener starts only once the configuration is read; a signal that
	// arrives while the node waits for the core stops it cleanly.
	loadCtx, stopLoadSignals := signal.NotifyContext(appCtx, syscall.SIGTERM, syscall.SIGINT)
	// Declared before the first configuration read, so the configuration the
	// node loads already reflects what it declares.
	if !declareTranscodeNode(loadCtx, appCtx, client, remote, env.declaration) {
		stopLoadSignals()
		slog.Info("transcode node stopped before it declared its configuration", "reason", context.Cause(loadCtx))
		return
	}
	slog.Info("reading this node's configuration from the main server", "core_url", env.coreURL, "node_id", client.NodeID())
	err = remote.Load(loadCtx)
	stopLoadSignals()
	if err != nil {
		slog.Info("transcode node stopped before it read its configuration", "reason", context.Cause(loadCtx))
		return
	}
	cfg := remote.Config()
	nodeName := remote.NodeName()

	stopLogging := configureTranscodeNodeLogging(appCtx, cfg, remote, client, nodeName)
	defer stopLogging()
	slog.Info("silo starting", "mode", serverModeTranscode, "listen", cfg.Server.Listen, "log_level", cfg.Server.LogLevel,
		"node_id", client.NodeID(), "node_name", nodeName)

	// Every job's input opens through the core with the grant the job
	// carries: at the approved path on this node's own mount, or with core
	// source access over HTTP from the core through a loopback relay. The
	// mode is read at each open, so a change applies to new jobs.
	opener := nodesource.NewOpener(client.OpenSource, nil)
	opener.EnableCoreAccess(nodesource.CoreAccess{Fetch: client.ReadSource, Mode: remote.SourceAccess})
	defer func() { _ = opener.Close() }()

	tracker := nodesessions.NewLocalTracker()
	streamTelemetry := newNodeStreamTelemetry(nodeName)

	srv := transcodenode.NewServer(remote, tracker)
	applyDeclaredLimits(srv, env.declaration)
	srv.SetNodeKey(env.nodeKey)
	srv.SetKeyRejected(remote.KeyRejected)
	srv.SetCore(client)
	srv.SetSourceOpener(opener)
	srv.SetFFmpegLogSink(playback.NewSlogFFmpegLogSink(slog.Default(), nodeName))
	srv.SetStreamTelemetry(streamTelemetry)
	// The first capability snapshot waits on warmup so it measures a primed
	// encoder rather than racing it into a probe failure.
	srv.StartCapabilitySnapshots(appCtx, srv.StartHardwareEncoderWarmup(appCtx))
	// Sample host and GPU resources so /health, /status and /metrics can
	// report them without doing any work on the request.
	srv.StartMetricsSampler(appCtx)
	// Reclaim orphaned transcode dirs at boot and hourly thereafter.
	srv.StartOrphanSweeper(appCtx)
	// SIGTERM drains the node before it shuts down, and every drain change
	// is reported so placement reacts before the next health sweep.
	srv.SetDrainAnnouncer(nodeHealthNotifier(appCtx, client))

	go remote.Run(appCtx)
	var telemetrySweep func() streamtelemetry.Snapshot
	if streamTelemetry.Enabled() {
		telemetrySweep = streamTelemetry.Sweep
	}
	reporter := nodecontrol.NewReporter(client, tracker.Snapshot, telemetrySweep)
	reporting := make(chan struct{})
	go func() {
		defer close(reporting)
		reporter.Run(appCtx)
	}()

	shutdown := func(ctx context.Context) error {
		err := srv.Shutdown(ctx)
		// appCtx is canceled before this runs, so the reporter is stopping;
		// one last report then tells the core this node's sessions ended.
		select {
		case <-reporting:
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		}
		tracker.Cleanup(ctx)
		if flushErr := reporter.Flush(ctx); flushErr != nil {
			slog.WarnContext(ctx, "final report to the main server failed; this node's sessions expire from the admin views within a minute",
				"component", "transcodenode", "error", flushErr)
		}
		return err
	}
	tuningCfg, adjustments := tuning.Parse(os.Getenv)
	tuning.LogAdjustments(appCtx, adjustments)
	netadmin.LogState(appCtx)
	opts := tcpcongestion.Options{
		Congestion: tuningCfg.TCPCongestion == tuning.TCPCongestionBBR,
		SendBuffer: tuningCfg.TCPSendBuffer,
	}
	startStandaloneServer(serverModeTranscode, cfg.Server.Listen, srv.Handler(), appCancel, shutdown,
		standaloneServerHooks{drain: newTranscodeNodeDrain(srv, remote.KeyRejected)}, opts)
}

// declareTranscodeNode sends the node's declaration to the core, retrying
// while the core cannot take it, and then keeps it in line: every
// nodeconfig.DeclarationRefreshInterval it is sent again, so a row or lock
// changed on the core is put back without a restart. A declaration the core
// refuses at startup stops the process, naming the variable; one refused
// later is logged and the node keeps running with its own values. Against a
// core that predates declarations the node starts anyway: with something
// declared it warns once and keeps offering it at the configuration poll
// interval, rereading its configuration once it lands; with nothing declared
// there is nothing to unlock, and it says nothing. It reports false when ctx
// ended first.
func declareTranscodeNode(ctx, appCtx context.Context, client *nodecontrol.Client, remote *nodeconfig.Remote, declaration nodepool.UpdateNodeInput) bool {
	if len(declaration.SetFields()) == 0 {
		// Nothing to apply, so nothing to wait for.
		nodeconfig.Release(ctx, appCtx, client)
		return ctx.Err() == nil
	}
	registration := nodecontrol.RegistrationFrom(declaration)
	result, err := nodeconfig.Declare(ctx, client, registration)
	switch {
	case err == nil:
		slog.InfoContext(ctx, "the main server applied this node's declaration", "node_id", client.NodeID(), "env_managed", result.EnvManaged)
		go nodeconfig.MaintainDeclaration(appCtx, client, registration, false, nil)
		return true
	case errors.Is(err, nodeconfig.ErrRegistrationUnsupported):
		slog.WarnContext(ctx, "the main server does not accept node declarations: it predates them, or a gateway in front of it blocks this route. Starting without them and offering them again every poll",
			"route", "PUT "+nodecontrol.PathRegistration, "variables", declaredVariables(declaration))
		go nodeconfig.MaintainDeclaration(appCtx, client, registration, true, func(result *nodecontrol.RegistrationResult) {
			slog.InfoContext(appCtx, "the main server applied this node's declaration", "node_id", client.NodeID(), "env_managed", result.EnvManaged)
			remote.RequestReload()
		})
		return true
	}
	if refused, ok := errors.AsType[*nodecontrol.RegistrationError](err); ok {
		variable := nodedeclare.VariableFor(nodepool.NodeTypeTranscode, refused.Field)
		if variable == "" {
			variable = refused.Field
		}
		slog.ErrorContext(ctx, "transcode node cannot start: the main server refused its declaration", "variable", variable, "reason", refused.Reason, "detail", refused.Detail)
		os.Exit(1)
	}
	return false
}

// applyDeclaredLimits makes the node enforce the declared values it checks
// on each job: its job cap, its hardware acceleration and its prepared-
// download budget.
func applyDeclaredLimits(srv *transcodenode.Server, declaration nodepool.UpdateNodeInput) {
	if declaration.MaxJobs != nil {
		srv.SetJobCap(*declaration.MaxJobs)
	}
	if declaration.HWAccelOverride != nil {
		srv.SetDeclaredHWAccel(*declaration.HWAccelOverride)
	}
	if declaration.DownloadArtifactMaxBytesOverride != nil {
		srv.SetArtifactBudget(*declaration.DownloadArtifactMaxBytesOverride)
	}
}

// declaredVariables names the variables a declaration came from.
func declaredVariables(declaration nodepool.UpdateNodeInput) []string {
	fields := declaration.SetFields()
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		names = append(names, nodedeclare.VariableFor(nodepool.NodeTypeTranscode, field))
	}
	return names
}

// configureTranscodeNodeLogging installs the node's slog default: stderr as on
// every server, and the operational log capture an API server keeps, shipped
// to the core instead of written to Postgres. The node runs no partition DDL
// and opens no database for logs. The level and quiet filter follow the
// configuration; the format and capture level apply at startup, as they do
// on other servers. stop flushes what is queued.
func configureTranscodeNodeLogging(ctx context.Context, cfg *config.Config, remote *nodeconfig.Remote, client *nodecontrol.Client, nodeName string) (stop func()) {
	logLevelVar := new(slog.LevelVar)
	logLevelVar.Set(parseLogLevel(cfg.Server.LogLevel))

	// OpenTelemetry depends only on OTEL_* / SILO_OTEL_ENABLED; when disabled
	// it installs nothing.
	telemetryCfg := telemetry.LoadConfig(nodeName)
	telemetryProviders, telemetryShutdown, err := telemetry.Setup(ctx, telemetryCfg)
	if err != nil {
		slog.ErrorContext(ctx, "telemetry setup failed; continuing with telemetry disabled", "component", "app", "error", err)
		telemetryCfg.Enabled = false
	}
	var otelLogHandler slog.Handler
	if telemetryCfg.Enabled {
		otelLogHandler = telemetry.NewOTelHandler(telemetryProviders.LoggerProvider)
	}
	quietFilter := logfilter.New(buildBaseHandler(cfg.Server.LogFormat, logLevelVar, otelLogHandler), cfg.Server.LogQuiet)
	remote.OnChange(func(_, updated *config.Config) {
		logLevelVar.Set(parseLogLevel(updated.Server.LogLevel))
		quietFilter.SetQuiet(updated.Server.LogQuiet)
	})

	buffer := logstream.NewBuffer[opslog.Entry](logstream.StreamApp, logBufferSize)
	stopShipping := startLogDrain(func(ctx context.Context) {
		opslog.Ship(ctx, buffer.Chan(), nodecontrol.MaxLogBatchEntries, transcodeNodeLogInterval, client.ShipLogs)
	})
	// Outermost, so stderr, telemetry and the log shipped to the core all see
	// relay URLs, which are capabilities on this host, replaced by the name
	// of their source.
	slog.SetDefault(slog.New(nodesource.RedactingHandler(opslog.NewHandler(quietFilter, buffer, parseLogLevel(remote.CaptureLevel()), nodeName))))
	return func() {
		stopShipping()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetryShutdown(shutdownCtx); err != nil {
			slog.WarnContext(shutdownCtx, "telemetry shutdown error", "component", "app", "error", err)
		}
	}
}

// newNodeStreamTelemetry observes the node's media routes in process. Nothing
// sweeps or publishes it on its own: each report sweeps it and carries the
// snapshot to the core, which stores it where the API's telemetry readers
// look.
func newNodeStreamTelemetry(nodeName string) *streamtelemetry.Registry {
	cfg := streamtelemetry.ConfigFromEnv(nodeName)
	cfg.Distributed, cfg.DistributedExplicit = false, true
	return streamtelemetry.NewRegistry(cfg, streamtelemetry.NewLocalStore(), slog.Default())
}

// nodeHealthNotifier tells the core every time this node starts or stops
// draining, or finishes, so every API server rechecks it at once rather than
// at its next health sweep.
func nodeHealthNotifier(ctx context.Context, client *nodecontrol.Client) func() {
	return func() {
		go func() {
			callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeHealthNotifyTimeout)
			defer cancel()
			if err := client.NotifyHealthChanged(callCtx); err != nil {
				slog.WarnContext(callCtx, "tell the main server about a drain change; it sees it at its next health sweep",
					"component", "transcodenode", "error", err)
			}
		}()
	}
}

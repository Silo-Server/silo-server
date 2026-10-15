package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/logfilter"
	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/telemetry"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// A transcode node's environment. It needs the core's URL and its own key;
// everything else comes from the core.
const (
	envCoreURL = "SILO_CORE_URL"
	envNodeKey = "SILO_NODE_KEY"
)

// modeTranscode is MODE for a transcode node.
const modeTranscode = "transcode"

// What an older node read: the data cipher's key, the URL and name it
// matched its row by, the Redis it read recipes and deny markers from, and
// the database it approved media inputs against.
const (
	envSecretKey   = "SECRET_KEY"
	envNodeURL     = "NODE_URL"
	envNodeName    = "NODE_NAME"
	envRedisURL    = "REDIS_URL"
	envDatabaseURL = "DATABASE_URL"
)

// transcodeNodeIgnoredEnv are variables an older node read and a transcode
// node now takes from the core or no longer needs. Shared env files still set
// them, so they are warned about rather than refused.
var transcodeNodeIgnoredEnv = []string{envSecretKey, envNodeURL, envNodeName, envRedisURL, envDatabaseURL}

// transcodeNodeLogInterval flushes a partial batch of the node's log entries
// to the core, as the API's own consumer flushes to Postgres.
const transcodeNodeLogInterval = 2 * time.Second

// transcodeNodeMode reports whether this process runs as a transcode node:
// MODE=transcode in the environment or the .env file.
func transcodeNodeMode(envFile string) bool {
	if envFile != "" {
		_ = godotenv.Load(envFile)
	}
	return strings.TrimSpace(os.Getenv("MODE")) == modeTranscode
}

// transcodeNodeEnv is what a transcode node reads from its environment.
type transcodeNodeEnv struct {
	coreURL string
	nodeKey string
	listen  string
	// ignored names the set variables a transcode node does not use.
	ignored []string
}

// loadTranscodeNodeEnv reads and checks the node's environment. A missing
// core URL, a missing key, or a key that is not a node key is an error: the
// node cannot even ask the core for its configuration without them.
func loadTranscodeNodeEnv(getenv func(string) string) (transcodeNodeEnv, error) {
	env := transcodeNodeEnv{
		coreURL: strings.TrimSpace(getenv(envCoreURL)),
		nodeKey: strings.TrimSpace(getenv(envNodeKey)),
	}
	if env.coreURL == "" {
		return transcodeNodeEnv{}, fmt.Errorf("%s is required: the URL this node reaches the API at", envCoreURL)
	}
	if env.nodeKey == "" {
		return transcodeNodeEnv{}, fmt.Errorf("%s is required: issue this node a key in the admin node settings", envNodeKey)
	}
	if _, err := nodepool.ParseNodeKey(env.nodeKey); err != nil {
		return transcodeNodeEnv{}, fmt.Errorf("%s is malformed (%w); copy the key the admin node settings issued", envNodeKey, err)
	}
	port := strings.TrimSpace(getenv("PORT"))
	if port == "" {
		port = "8080"
	}
	env.listen = ":" + port
	for _, name := range transcodeNodeIgnoredEnv {
		if getenv(name) != "" {
			env.ignored = append(env.ignored, name)
		}
	}
	return env, nil
}

// runTranscodeNode runs this process as a transcode node. It reads no
// settings, holds no cipher, secret or event bus, and takes its configuration
// from the core through the node control plane (internal/nodecontrol). It
// returns when the node has shut down; a broken environment exits the
// process.
func runTranscodeNode() {
	env, err := loadTranscodeNodeEnv(os.Getenv)
	if err != nil {
		log.Fatalf("transcode node: %v", err)
	}
	if len(env.ignored) > 0 {
		slog.Warn("transcode nodes ignore these variables; the core supplies their configuration", "variables", env.ignored)
	}
	client, err := nodecontrol.NewClient(env.coreURL, env.nodeKey)
	if err != nil {
		log.Fatalf("transcode node: %v", err)
	}
	stopDebugListener, err := startBootstrapDebugListener(true)
	if err != nil {
		log.Fatalf("local profiling configuration: %v", err)
	}
	defer stopDebugListener()

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	remote := nodeconfig.NewRemote(client, env.listen)
	remote.OnLoad(normalizeLoadedConfig)
	slog.Info("reading this node's configuration from the core", "core_url", env.coreURL, "node_id", client.NodeID())
	if err := remote.Load(appCtx); err != nil {
		log.Fatalf("transcode node configuration: %v", err)
	}
	cfg := remote.Config()
	nodeName := remote.NodeName()

	stopLogging := configureTranscodeNodeLogging(appCtx, cfg, remote, client, nodeName)
	defer stopLogging()
	slog.Info("silo starting", "mode", modeTranscode, "listen", cfg.Server.Listen, "log_level", cfg.Server.LogLevel,
		"node_id", client.NodeID(), "node_name", nodeName)

	// Every job's input opens through the core with the grant the job
	// carries: at the approved path on this node's mount, or over HTTP from
	// the core, as the node's source_access says.
	sources, err := nodesource.NewRelay(nodesource.RelayOptions{
		Open:    client.OpenSource,
		CoreURL: env.coreURL,
		NodeID:  client.NodeID(),
		NodeKey: env.nodeKey,
		Mode:    remote.SourceAccess,
	})
	if err != nil {
		log.Fatalf("transcode node source access: %v", err)
	}
	defer func() { _ = sources.Close() }()

	tracker := nodesessions.NewLocalTracker(func() (string, string) {
		url, _ := remote.NodeRegisteredURL()
		return url, remote.NodeName()
	})
	streamTelemetry := newNodeStreamTelemetry(nodeName)

	srv := transcodenode.NewServer(remote, tracker)
	srv.SetNodeKey(env.nodeKey)
	// A 401 from the core means the key was rotated or the node deleted:
	// health says so, which takes the node out of placement, until a
	// configuration read succeeds again.
	srv.SetKeyRejected(remote.KeyRejected)
	client.SetOnUnauthorized(remote.ReportUnauthorized)
	srv.SetRecipeResolver(client)
	srv.SetSourceOpener(transcodenode.RelaySources(sources))
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
			slog.WarnContext(ctx, "final report to the core failed; this node's sessions expire from the admin views within a minute",
				"component", "transcodenode", "error", flushErr)
		}
		return err
	}
	startStandaloneServer(cfg.Server.Listen, srv.Handler(), appCancel, shutdown,
		standaloneServerHooks{drain: newTranscodeNodeDrain(srv)})
}

// configureTranscodeNodeLogging installs the node's slog default: stderr as on
// every server, and the operational log capture an API server keeps, shipped
// to the core instead of written to Postgres. The node never runs partition
// DDL or touches Postgres for logs. The level and quiet filter follow the
// configuration; the format and capture level apply at startup. stop flushes
// what is queued.
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
	// Relay URLs are bearer capabilities on this host; no log line, here or
	// shipped to the core, may carry one.
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
			callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := client.NotifyHealthChanged(callCtx); err != nil {
				slog.WarnContext(callCtx, "tell the core about a drain change; it sees it at its next health sweep",
					"component", "transcodenode", "error", err)
			}
		}()
	}
}

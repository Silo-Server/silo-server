package runtime

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtimehost"
)

const (
	ProtocolVersion  = 1
	MagicCookieKey   = "SILO_PLUGIN"
	MagicCookieValue = "silo-rpc-plugin-v1"
	PluginSetName    = "silo"
)

type CapabilityServers struct {
	Runtime          pluginv1.RuntimeServer
	MetadataProvider pluginv1.MetadataProviderServer
	MarkerProvider   pluginv1.MarkerProviderServer
	MediaAnalyzer    pluginv1.MediaAnalyzerServer
	ScheduledTask    pluginv1.ScheduledTaskServer
	ScanSource       pluginv1.ScanSourceServer
	RequestRouter    pluginv1.RequestRouterServer
	EventConsumer    pluginv1.EventConsumerServer
	AuthProvider     pluginv1.AuthProviderServer
	HttpRoutes       pluginv1.HttpRoutesServer
}

// Client wraps the gRPC connection to a plugin and provides typed accessors
// for plugin capabilities. The broker field is populated on the host side when
// the client is created via GRPCClient; it is nil when the client is
// constructed directly with NewClient.
type Client struct {
	conn   *grpc.ClientConn
	broker *plugin.GRPCBroker
}

type ServeConfig struct {
	Plugins plugin.PluginSet
	Logger  hclog.Logger
	Servers CapabilityServers
}

func HandshakeConfig() plugin.HandshakeConfig {
	return plugin.HandshakeConfig{
		ProtocolVersion:  ProtocolVersion,
		MagicCookieKey:   MagicCookieKey,
		MagicCookieValue: MagicCookieValue,
	}
}

func DefaultGRPCServer(opts []grpc.ServerOption) *grpc.Server {
	return grpc.NewServer(opts...)
}

func DefaultPluginSet(servers CapabilityServers) plugin.PluginSet {
	return plugin.PluginSet{
		PluginSetName: &GRPCPlugin{Servers: servers},
	}
}

func NewClient(conn *grpc.ClientConn) *Client {
	return &Client{conn: conn}
}

func (c *Client) Conn() *grpc.ClientConn {
	return c.conn
}

// Broker returns the gRPC broker that was injected when the client was
// dispensed via go-plugin on the host side. It is nil when the client was
// constructed directly via NewClient (e.g. in unit tests or plugin-side code).
func (c *Client) Broker() *plugin.GRPCBroker {
	return c.broker
}

func (c *Client) Runtime() pluginv1.RuntimeClient {
	return pluginv1.NewRuntimeClient(c.conn)
}

func (c *Client) MetadataProvider() pluginv1.MetadataProviderClient {
	return pluginv1.NewMetadataProviderClient(c.conn)
}

func (c *Client) MarkerProvider() pluginv1.MarkerProviderClient {
	return pluginv1.NewMarkerProviderClient(c.conn)
}

func (c *Client) MediaAnalyzer() pluginv1.MediaAnalyzerClient {
	return pluginv1.NewMediaAnalyzerClient(c.conn)
}

func (c *Client) ScheduledTask() pluginv1.ScheduledTaskClient {
	return pluginv1.NewScheduledTaskClient(c.conn)
}

func (c *Client) ScanSource() pluginv1.ScanSourceClient {
	return pluginv1.NewScanSourceClient(c.conn)
}

func (c *Client) RequestRouter() pluginv1.RequestRouterClient {
	return pluginv1.NewRequestRouterClient(c.conn)
}

func (c *Client) EventConsumer() pluginv1.EventConsumerClient {
	return pluginv1.NewEventConsumerClient(c.conn)
}

func (c *Client) AuthProvider() pluginv1.AuthProviderClient {
	return pluginv1.NewAuthProviderClient(c.conn)
}

func (c *Client) HttpRoutes() pluginv1.HttpRoutesClient {
	return pluginv1.NewHttpRoutesClient(c.conn)
}

type GRPCPlugin struct {
	plugin.Plugin
	Servers CapabilityServers
}

func (p *GRPCPlugin) GRPCServer(broker *plugin.GRPCBroker, server *grpc.Server) error {
	pluginHost.setBroker(broker)
	if p.Servers.Runtime == nil {
		return fmt.Errorf("runtime server is required")
	}

	pluginv1.RegisterRuntimeServer(server, p.Servers.Runtime)
	if p.Servers.MetadataProvider != nil {
		pluginv1.RegisterMetadataProviderServer(server, p.Servers.MetadataProvider)
	}
	if p.Servers.MarkerProvider != nil {
		pluginv1.RegisterMarkerProviderServer(server, p.Servers.MarkerProvider)
	}
	if p.Servers.MediaAnalyzer != nil {
		pluginv1.RegisterMediaAnalyzerServer(server, p.Servers.MediaAnalyzer)
	}
	if p.Servers.ScheduledTask != nil {
		pluginv1.RegisterScheduledTaskServer(server, p.Servers.ScheduledTask)
	}
	if p.Servers.ScanSource != nil {
		pluginv1.RegisterScanSourceServer(server, p.Servers.ScanSource)
	}
	if p.Servers.RequestRouter != nil {
		pluginv1.RegisterRequestRouterServer(server, p.Servers.RequestRouter)
	}
	if p.Servers.EventConsumer != nil {
		pluginv1.RegisterEventConsumerServer(server, p.Servers.EventConsumer)
	}
	if p.Servers.AuthProvider != nil {
		pluginv1.RegisterAuthProviderServer(server, p.Servers.AuthProvider)
	}
	if p.Servers.HttpRoutes != nil {
		pluginv1.RegisterHttpRoutesServer(server, p.Servers.HttpRoutes)
	}
	return nil
}

func (p *GRPCPlugin) GRPCClient(_ context.Context, broker *plugin.GRPCBroker, conn *grpc.ClientConn) (interface{}, error) {
	return &Client{conn: conn, broker: broker}, nil
}

// pluginHostState is a process-singleton holding the plugin-side broker and
// the host-assigned broker stream ID for RuntimeHost. The singleton model is
// appropriate because go-plugin runs exactly one plugin per process.
//
// The dialed *runtimehost.Client is cached: silo's bindRuntimeHost
// registers ONE go-plugin broker stream and AcceptAndServe handles a single
// connection on it (multiplexing many gRPC RPCs). Re-dialing per call would
// open a fresh stream the host isn't listening on, causing every call after
// the first to hang on its way to a timeout. Holding one client and reusing
// it keeps all RPCs on the original stream.
type pluginHostState struct {
	mu       sync.Mutex
	broker   *plugin.GRPCBroker
	brokerID uint32
	client   *runtimehost.Client
}

var pluginHost = &pluginHostState{}

func (s *pluginHostState) setBroker(b *plugin.GRPCBroker) {
	s.mu.Lock()
	s.broker = b
	// Invalidate stream state tied to a previous broker.
	s.brokerID = 0
	s.client = nil
	s.mu.Unlock()
}

func (s *pluginHostState) setBrokerID(id uint32) {
	s.mu.Lock()
	s.brokerID = id
	// new stream id → drop any cached client
	s.client = nil
	s.mu.Unlock()
}

func (s *pluginHostState) host() *runtimehost.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client
	}
	if s.broker == nil || s.brokerID == 0 {
		return nil
	}
	conn, err := s.broker.Dial(s.brokerID)
	if err != nil {
		return nil
	}
	s.client = runtimehost.NewClient(conn)
	return s.client
}

// SetHostBrokerID stores the broker stream ID assigned by the host. The
// generated runtimedefault.Server's BindHostBroker handler calls this when the
// host invokes Runtime.BindHostBroker. Plugin authors do not call this
// directly when they embed runtimedefault.Server.
func SetHostBrokerID(id uint32) { pluginHost.setBrokerID(id) }

// Host returns a runtimehost.Client connected to the silo host. Returns
// nil before the host has invoked Runtime.BindHostBroker (i.e. very briefly
// during plugin startup) or if the broker dial fails. Capability handlers
// should treat nil as transient and either skip or surface a temporary error.
//
// The first successful call dials the host broker stream and caches the
// *runtimehost.Client; later calls reuse the same client.
func Host() *runtimehost.Client { return pluginHost.host() }

func Serve(cfg ServeConfig) {
	// Handle "manifest" subcommand: print the plugin manifest as JSON and exit.
	if len(os.Args) > 1 && os.Args[1] == "manifest" {
		if cfg.Servers.Runtime == nil {
			fmt.Fprintln(os.Stderr, "runtime server is required to retrieve manifest")
			os.Exit(1)
		}
		resp, err := cfg.Servers.Runtime.GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to get manifest: %v\n", err)
			os.Exit(1)
		}
		marshaler := protojson.MarshalOptions{Indent: "  "}
		data, err := marshaler.Marshal(resp.GetManifest())
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to encode manifest: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
		os.Exit(0)
	}

	plugins := cfg.Plugins
	if len(plugins) == 0 {
		plugins = DefaultPluginSet(cfg.Servers)
	}

	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: HandshakeConfig(),
		Plugins:         plugins,
		GRPCServer:      plugin.DefaultGRPCServer,
		Logger:          cfg.Logger,
	})
}

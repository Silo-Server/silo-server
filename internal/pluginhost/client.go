package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	sdkruntime "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
)

var (
	ErrClientNotFound     = errors.New("plugin client not found")
	ErrCapabilityNotFound = errors.New("plugin capability not found")
	ErrPluginUnhealthy    = errors.New("plugin is unhealthy")
)

type Client struct {
	installationID int
	manifest       *pluginv1.PluginManifest
	rpc            *sdkruntime.Client
	capabilities   map[string]*pluginv1.CapabilityDescriptor

	mu        sync.RWMutex
	unhealthy bool
}

type MetadataProviderClient struct {
	client  pluginv1.MetadataProviderClient
	timeout time.Duration
}

type MarkerProviderClient struct {
	client  pluginv1.MarkerProviderClient
	timeout time.Duration
}

type MediaAnalyzerClient struct {
	client  pluginv1.MediaAnalyzerClient
	timeout time.Duration
}

type ScheduledTaskClient struct {
	client  pluginv1.ScheduledTaskClient
	timeout time.Duration
}

type ScanSourceClient struct {
	client  pluginv1.ScanSourceClient
	timeout time.Duration
}

type EventConsumerClient struct {
	client  pluginv1.EventConsumerClient
	timeout time.Duration
}

type AuthProviderClient struct {
	client  pluginv1.AuthProviderClient
	timeout time.Duration
}

type HTTPRoutesClient struct {
	client  pluginv1.HttpRoutesClient
	timeout time.Duration
}

func newClient(installationID int, rpc *sdkruntime.Client, manifest *pluginv1.PluginManifest) *Client {
	capabilities := make(map[string]*pluginv1.CapabilityDescriptor, len(manifest.GetCapabilities()))
	for _, capability := range manifest.GetCapabilities() {
		capabilities[capabilityKey(capability.GetType(), capability.GetId())] = capability
	}

	return &Client{
		installationID: installationID,
		manifest:       proto.Clone(manifest).(*pluginv1.PluginManifest),
		rpc:            rpc,
		capabilities:   capabilities,
	}
}

func (c *Client) Manifest() *pluginv1.PluginManifest {
	return proto.Clone(c.manifest).(*pluginv1.PluginManifest)
}

func (c *Client) Capabilities() []*pluginv1.CapabilityDescriptor {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]*pluginv1.CapabilityDescriptor, 0, len(c.capabilities))
	for _, capability := range c.capabilities {
		result = append(result, proto.Clone(capability).(*pluginv1.CapabilityDescriptor))
	}
	return result
}

func (c *Client) MetadataProvider(capabilityID string) (*MetadataProviderClient, error) {
	if err := c.requireCapability("metadata_provider.v1", capabilityID); err != nil {
		return nil, err
	}
	return &MetadataProviderClient{
		client:  c.rpc.MetadataProvider(),
		timeout: DefaultMetadataTimeout,
	}, nil
}

func (c *Client) MarkerProvider(capabilityID string) (*MarkerProviderClient, error) {
	if err := c.requireCapability("marker_provider.v1", capabilityID); err != nil {
		return nil, err
	}
	return &MarkerProviderClient{
		client:  c.rpc.MarkerProvider(),
		timeout: DefaultMarkerProviderTimeout,
	}, nil
}

func (c *Client) MediaAnalyzer(capabilityID string) (*MediaAnalyzerClient, error) {
	if err := c.requireCapability("media_analyzer.v1", capabilityID); err != nil {
		return nil, err
	}
	return &MediaAnalyzerClient{
		client:  c.rpc.MediaAnalyzer(),
		timeout: DefaultAnalyzerTimeout,
	}, nil
}

func (c *Client) ScheduledTask(capabilityID string) (*ScheduledTaskClient, error) {
	if err := c.requireCapability("scheduled_task.v1", capabilityID); err != nil {
		return nil, err
	}
	return &ScheduledTaskClient{
		client:  c.rpc.ScheduledTask(),
		timeout: DefaultControlTimeout,
	}, nil
}

func (c *Client) ScanSource(capabilityID string) (*ScanSourceClient, error) {
	if err := c.requireCapability("scan_source.v1", capabilityID); err != nil {
		return nil, err
	}
	return &ScanSourceClient{
		client:  c.rpc.ScanSource(),
		timeout: DefaultScanSourceTimeout,
	}, nil
}

func (c *Client) EventConsumer(capabilityID string) (*EventConsumerClient, error) {
	if err := c.requireCapability("event_consumer.v1", capabilityID); err != nil {
		return nil, err
	}
	return &EventConsumerClient{
		client:  c.rpc.EventConsumer(),
		timeout: DefaultEventTimeout,
	}, nil
}

func (c *Client) AuthProvider(capabilityID string) (*AuthProviderClient, error) {
	if err := c.requireCapability("auth_provider.v1", capabilityID); err != nil {
		return nil, err
	}
	return &AuthProviderClient{
		client:  c.rpc.AuthProvider(),
		timeout: DefaultAuthTimeout,
	}, nil
}

func (c *Client) HTTPRoutes(capabilityID string) (*HTTPRoutesClient, error) {
	if err := c.requireCapability("http_routes.v1", capabilityID); err != nil {
		return nil, err
	}
	return &HTTPRoutesClient{
		client:  c.rpc.HttpRoutes(),
		timeout: DefaultRouteTimeout,
	}, nil
}

func (c *Client) markUnhealthy() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unhealthy = true
}

func (c *Client) requireCapability(capabilityType, capabilityID string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.unhealthy {
		return ErrPluginUnhealthy
	}

	descriptor, ok := c.capabilities[capabilityKey(capabilityType, capabilityID)]
	if !ok || descriptor == nil {
		return fmt.Errorf("%w: %s/%s", ErrCapabilityNotFound, capabilityType, capabilityID)
	}
	return nil
}

func (c *MetadataProviderClient) Search(ctx context.Context, req *pluginv1.SearchMetadataRequest) (*pluginv1.SearchMetadataResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.Search(callCtx, req)
}

func (c *MetadataProviderClient) GetMetadata(ctx context.Context, req *pluginv1.GetMetadataRequest) (*pluginv1.GetMetadataResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.GetMetadata(callCtx, req)
}

func (c *MetadataProviderClient) GetPersonDetail(ctx context.Context, req *pluginv1.GetPersonDetailRequest) (*pluginv1.GetPersonDetailResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.GetPersonDetail(callCtx, req)
}

func (c *MetadataProviderClient) GetSeasons(ctx context.Context, req *pluginv1.GetSeasonsRequest) (*pluginv1.GetSeasonsResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.GetSeasons(callCtx, req)
}

func (c *MetadataProviderClient) GetEpisodes(ctx context.Context, req *pluginv1.GetEpisodesRequest) (*pluginv1.GetEpisodesResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.GetEpisodes(callCtx, req)
}

func (c *MetadataProviderClient) GetImages(ctx context.Context, req *pluginv1.GetImagesRequest) (*pluginv1.GetImagesResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.GetImages(callCtx, req)
}

func (c *MetadataProviderClient) ResolveImageURL(ctx context.Context, req *pluginv1.ResolveImageURLRequest) (*pluginv1.ResolveImageURLResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.ResolveImageURL(callCtx, req)
}

func (c *MetadataProviderClient) ResolveImageURLs(ctx context.Context, req *pluginv1.ResolveImageURLsRequest) (*pluginv1.ResolveImageURLsResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.ResolveImageURLs(callCtx, req)
}

func (c *MarkerProviderClient) FetchMarkers(ctx context.Context, req *pluginv1.FetchMarkersRequest) (*pluginv1.FetchMarkersResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.FetchMarkers(callCtx, req)
}

func (c *MarkerProviderClient) SubmitMarker(ctx context.Context, req *pluginv1.SubmitMarkerRequest) (*pluginv1.SubmitMarkerResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.SubmitMarker(callCtx, req)
}

func (c *MarkerProviderClient) GetMarkerProviderStats(ctx context.Context, req *pluginv1.GetMarkerProviderStatsRequest) (*pluginv1.MarkerProviderStatsResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.GetMarkerProviderStats(callCtx, req)
}

func (c *MediaAnalyzerClient) Analyze(ctx context.Context, req *pluginv1.AnalyzeMediaRequest) (*pluginv1.AnalyzeMediaResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.Analyze(callCtx, req)
}

func (c *ScheduledTaskClient) Run(ctx context.Context, req *pluginv1.RunScheduledTaskRequest) (*pluginv1.RunScheduledTaskResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.Run(callCtx, req)
}

func (c *ScanSourceClient) PollChanges(ctx context.Context, req *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.PollChanges(callCtx, req)
}

func (c *EventConsumerClient) HandleEvent(ctx context.Context, req *pluginv1.HandleEventRequest) (*pluginv1.HandleEventResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.HandleEvent(callCtx, req)
}

func (c *AuthProviderClient) Authenticate(ctx context.Context, req *pluginv1.AuthenticateRequest) (*pluginv1.AuthenticateResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.Authenticate(callCtx, req)
}

func (c *AuthProviderClient) InitAuthorize(ctx context.Context, req *pluginv1.InitAuthorizeRequest) (*pluginv1.InitAuthorizeResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.InitAuthorize(callCtx, req)
}

func (c *AuthProviderClient) ExchangeCode(ctx context.Context, req *pluginv1.ExchangeCodeRequest) (*pluginv1.AuthenticateResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.ExchangeCode(callCtx, req)
}

func (c *HTTPRoutesClient) Handle(ctx context.Context, req *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.Handle(callCtx, req)
}

func ensureDeadline(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func capabilityKey(capabilityType, capabilityID string) string {
	return capabilityType + "\x00" + capabilityID
}

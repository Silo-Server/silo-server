package requests

import (
	"context"
	"strconv"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// RequestRouterProvider fulfills a whole request and checks target status via a
// request_router.v1 plugin. The host owns governance (which qualities) and the
// target records; the provider is the plugin boundary.
type RequestRouterProvider interface {
	Fulfill(ctx context.Context, installationID int, capabilityID string, req Request, qualities []Quality, conns []ResolvedRouterConnection) ([]RouterTarget, string, error)
	CheckStatus(ctx context.Context, installationID int, capabilityID string, req Request, targets []RouterTargetRef, conns []ResolvedRouterConnection) ([]RouterTargetStatus, error)
	ListConfigOptions(ctx context.Context, installationID int, capabilityID string, conn ResolvedRouterConnection) (map[string][]RouterOption, error)
	TestConnection(ctx context.Context, installationID int, capabilityID string, conn ResolvedRouterConnection) (bool, string, error)
}

// ResolvedRouterConnection is a connection with plaintext credentials + parsed config.
type ResolvedRouterConnection struct {
	ID      string
	BaseURL string
	APIKey  string
	Config  map[string]any
}

type RouterTarget struct {
	Quality        Quality
	ConnectionID   string
	ExternalID     string
	ExternalStatus string
	Status         Status
	Message        string
}

type RouterTargetRef struct {
	Quality      Quality
	ConnectionID string
	ExternalID   string
}

type RouterTargetStatus struct {
	Quality        Quality
	ConnectionID   string
	Status         Status
	ExternalStatus string
	Message        string
}

type RouterOption struct {
	Value string
	Label string
}

// RouterClientResolver yields a per-(installation, capability) router client.
type RouterClientResolver interface {
	RequestRouterClient(ctx context.Context, installationID int, capabilityID string) (RouterClient, error)
}

// RouterClient mirrors *pluginhost.RequestRouterClient (exported so the api
// package can adapt the concrete type across packages without an import cycle).
type RouterClient interface {
	Fulfill(ctx context.Context, req *pluginv1.FulfillRequest) (*pluginv1.FulfillResponse, error)
	CheckStatus(ctx context.Context, req *pluginv1.CheckStatusRequest) (*pluginv1.CheckStatusResponse, error)
	ListConfigOptions(ctx context.Context, req *pluginv1.ListConfigOptionsRequest) (*pluginv1.ListConfigOptionsResponse, error)
	TestConnection(ctx context.Context, req *pluginv1.TestConnectionRequest) (*pluginv1.TestConnectionResponse, error)
}

type pluginRouterProvider struct{ resolver RouterClientResolver }

func NewPluginRouterProvider(r RouterClientResolver) RequestRouterProvider {
	return &pluginRouterProvider{resolver: r}
}

func routerItoa(n int) string { return strconv.Itoa(n) }

func routerDescriptor(req Request) *pluginv1.RequestDescriptor {
	ids := map[string]string{}
	if req.TMDBID != 0 {
		ids["tmdb"] = routerItoa(req.TMDBID)
	}
	if req.TVDBID != nil {
		ids["tvdb"] = routerItoa(*req.TVDBID)
	}
	if req.IMDbID != "" {
		ids["imdb"] = req.IMDbID
	}
	year := 0
	if req.Year != nil {
		year = *req.Year
	}
	return &pluginv1.RequestDescriptor{
		MediaType:          string(req.MediaType),
		Title:              req.Title,
		Year:               int32(year),
		ExternalIds:        ids,
		IsAnime:            req.IsAnime,
		RequesterUserId:    int32(req.RequestedByUserID),
		RequesterProfileId: req.RequestedByProfileID,
	}
}

func routerProtoConn(c ResolvedRouterConnection) *pluginv1.RouterConnection {
	cfg, _ := structpb.NewStruct(c.Config)
	return &pluginv1.RouterConnection{Id: c.ID, BaseUrl: c.BaseURL, ApiKey: c.APIKey, Config: cfg}
}

func routerProtoConns(conns []ResolvedRouterConnection) []*pluginv1.RouterConnection {
	out := make([]*pluginv1.RouterConnection, 0, len(conns))
	for _, c := range conns {
		out = append(out, routerProtoConn(c))
	}
	return out
}

func (p *pluginRouterProvider) Fulfill(ctx context.Context, installationID int, capabilityID string, req Request, qualities []Quality, conns []ResolvedRouterConnection) ([]RouterTarget, string, error) {
	client, err := p.resolver.RequestRouterClient(ctx, installationID, capabilityID)
	if err != nil {
		return nil, "", err
	}
	qs := make([]string, 0, len(qualities))
	for _, q := range qualities {
		qs = append(qs, string(q))
	}
	resp, err := client.Fulfill(ctx, &pluginv1.FulfillRequest{
		CapabilityId: capabilityID, Request: routerDescriptor(req), Qualities: qs, Connections: routerProtoConns(conns),
	})
	if err != nil {
		return nil, "", err
	}
	targets := make([]RouterTarget, 0, len(resp.GetTargets()))
	for _, t := range resp.GetTargets() {
		targets = append(targets, RouterTarget{
			Quality: Quality(t.GetQuality()), ConnectionID: t.GetConnectionId(),
			ExternalID: t.GetExternalId(), ExternalStatus: t.GetExternalStatus(),
			Status: Status(t.GetStatus()), Message: t.GetMessage(),
		})
	}
	return targets, resp.GetMessage(), nil
}

func (p *pluginRouterProvider) CheckStatus(ctx context.Context, installationID int, capabilityID string, req Request, targets []RouterTargetRef, conns []ResolvedRouterConnection) ([]RouterTargetStatus, error) {
	client, err := p.resolver.RequestRouterClient(ctx, installationID, capabilityID)
	if err != nil {
		return nil, err
	}
	refs := make([]*pluginv1.TargetRef, 0, len(targets))
	for _, t := range targets {
		refs = append(refs, &pluginv1.TargetRef{Quality: string(t.Quality), ConnectionId: t.ConnectionID, ExternalId: t.ExternalID})
	}
	resp, err := client.CheckStatus(ctx, &pluginv1.CheckStatusRequest{
		CapabilityId: capabilityID, Request: routerDescriptor(req), Targets: refs, Connections: routerProtoConns(conns),
	})
	if err != nil {
		return nil, err
	}
	out := make([]RouterTargetStatus, 0, len(resp.GetStatuses()))
	for _, st := range resp.GetStatuses() {
		out = append(out, RouterTargetStatus{
			Quality: Quality(st.GetQuality()), ConnectionID: st.GetConnectionId(),
			Status: Status(st.GetStatus()), ExternalStatus: st.GetExternalStatus(), Message: st.GetMessage(),
		})
	}
	return out, nil
}

func (p *pluginRouterProvider) ListConfigOptions(ctx context.Context, installationID int, capabilityID string, conn ResolvedRouterConnection) (map[string][]RouterOption, error) {
	client, err := p.resolver.RequestRouterClient(ctx, installationID, capabilityID)
	if err != nil {
		return nil, err
	}
	resp, err := client.ListConfigOptions(ctx, &pluginv1.ListConfigOptionsRequest{
		CapabilityId: capabilityID, Connection: routerProtoConn(conn),
	})
	if err != nil {
		return nil, err
	}
	out := map[string][]RouterOption{}
	for field, list := range resp.GetOptionsByField() {
		opts := make([]RouterOption, 0, len(list.GetOptions()))
		for _, o := range list.GetOptions() {
			opts = append(opts, RouterOption{Value: o.GetValue(), Label: o.GetLabel()})
		}
		out[field] = opts
	}
	return out, nil
}

func (p *pluginRouterProvider) TestConnection(ctx context.Context, installationID int, capabilityID string, conn ResolvedRouterConnection) (bool, string, error) {
	client, err := p.resolver.RequestRouterClient(ctx, installationID, capabilityID)
	if err != nil {
		return false, "", err
	}
	resp, err := client.TestConnection(ctx, &pluginv1.TestConnectionRequest{
		CapabilityId: capabilityID, Connection: routerProtoConn(conn),
	})
	if err != nil {
		return false, "", err
	}
	return resp.GetOk(), resp.GetMessage(), nil
}

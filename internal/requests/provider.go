package requests

import (
	"context"
	"fmt"
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
	Validate(ctx context.Context, installationID int, capabilityID string, conn ResolvedRouterConnection, siblings []ResolvedRouterConnection) (fieldErrors map[string]string, formError string, err error)
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
	// Progress is set by a plugin that declares reports_download_progress
	// while the target has downloads in flight; nil means none.
	Progress *DownloadProgress
}

type RouterOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
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
	Validate(ctx context.Context, req *pluginv1.ValidateRequest) (*pluginv1.ValidateResponse, error)
}

type pluginRouterProvider struct{ resolver RouterClientResolver }

// RouterFeatures reads the capability's declared features when the resolver
// can; otherwise the capability declares none.
func (p *pluginRouterProvider) RouterFeatures(ctx context.Context, installationID int, capabilityID string) (RouterFeatures, error) {
	reader, ok := p.resolver.(RouterFeatureReader)
	if !ok {
		return RouterFeatures{}, nil
	}
	return reader.RouterFeatures(ctx, installationID, capabilityID)
}

func NewPluginRouterProvider(r RouterClientResolver) RequestRouterProvider {
	return &pluginRouterProvider{resolver: r}
}

func routerDescriptor(req Request) *pluginv1.RequestDescriptor {
	ids := map[string]string{}
	if req.TMDBID != 0 {
		ids["tmdb"] = strconv.Itoa(req.TMDBID)
	}
	if req.TVDBID != nil && *req.TVDBID > 0 {
		ids["tvdb"] = strconv.Itoa(*req.TVDBID)
	}
	if req.IMDbID != "" {
		ids["imdb"] = req.IMDbID
	}
	year := 0
	if req.Year != nil {
		year = *req.Year
	}
	// Seasons name the series seasons requested; none means the whole
	// series. A plugin without supports_seasons ignores them.
	var seasons []int32
	if req.MediaType == MediaTypeSeries {
		for _, season := range req.Seasons {
			seasons = append(seasons, int32(season))
		}
	}
	return &pluginv1.RequestDescriptor{
		MediaType:          string(req.MediaType),
		Title:              req.Title,
		Year:               int32(year),
		ExternalIds:        ids,
		IsAnime:            req.IsAnime,
		RequesterUserId:    int64(req.RequestedByUserID),
		RequesterProfileId: req.RequestedByProfileID,
		RequesterEmail:     req.RequesterEmail,
		RequesterUsername:  req.RequesterUsername,
		Seasons:            seasons,
	}
}

func routerProtoConn(c ResolvedRouterConnection) (*pluginv1.RouterConnection, error) {
	cfg, err := structpb.NewStruct(c.Config)
	if err != nil {
		return nil, fmt.Errorf("router: encode connection %s config: %w", c.ID, err)
	}
	return &pluginv1.RouterConnection{Id: c.ID, BaseUrl: c.BaseURL, ApiKey: c.APIKey, Config: cfg}, nil
}

func routerProtoConns(conns []ResolvedRouterConnection) ([]*pluginv1.RouterConnection, error) {
	out := make([]*pluginv1.RouterConnection, 0, len(conns))
	for _, c := range conns {
		pc, err := routerProtoConn(c)
		if err != nil {
			return nil, err
		}
		out = append(out, pc)
	}
	return out, nil
}

func (p *pluginRouterProvider) Fulfill(ctx context.Context, installationID int, capabilityID string, req Request, qualities []Quality, conns []ResolvedRouterConnection) ([]RouterTarget, string, error) {
	client, err := p.resolver.RequestRouterClient(ctx, installationID, capabilityID)
	if err != nil {
		return nil, "", err
	}
	qs := make([]*pluginv1.RequestedQuality, 0, len(qualities))
	for _, q := range qualities {
		qs = append(qs, &pluginv1.RequestedQuality{Id: string(q), Is4K: q == Quality2160p})
	}
	pconns, err := routerProtoConns(conns)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Fulfill(ctx, &pluginv1.FulfillRequest{
		CapabilityId: capabilityID, Request: routerDescriptor(req), Qualities: qs, Connections: pconns,
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
	pconns, err := routerProtoConns(conns)
	if err != nil {
		return nil, err
	}
	resp, err := client.CheckStatus(ctx, &pluginv1.CheckStatusRequest{
		CapabilityId: capabilityID, Request: routerDescriptor(req), Targets: refs, Connections: pconns,
	})
	if err != nil {
		return nil, err
	}
	out := make([]RouterTargetStatus, 0, len(resp.GetStatuses()))
	for _, st := range resp.GetStatuses() {
		out = append(out, RouterTargetStatus{
			Quality: Quality(st.GetQuality()), ConnectionID: st.GetConnectionId(),
			Status: Status(st.GetStatus()), ExternalStatus: st.GetExternalStatus(), Message: st.GetMessage(),
			Progress: downloadProgressFromProto(st.GetProgress()),
		})
	}
	return out, nil
}

// downloadProgressFromProto normalizes a plugin's progress report. An unknown
// or empty phase reads as downloading, but a report with neither a phase nor
// a size carries nothing and maps to nil. Byte counts are clamped so that
// 0 <= left <= total; a total of 0 means the size is unknown.
func downloadProgressFromProto(p *pluginv1.DownloadProgress) *DownloadProgress {
	if p == nil {
		return nil
	}
	phase := DownloadPhase(p.GetPhase())
	total := max(p.GetBytesTotal(), 0)
	if phase == "" && total == 0 {
		return nil
	}
	switch phase {
	case DownloadPhaseQueued, DownloadPhaseDownloading, DownloadPhasePaused,
		DownloadPhaseStalled, DownloadPhaseImporting, DownloadPhaseImportBlocked:
	default:
		phase = DownloadPhaseDownloading
	}
	out := &DownloadProgress{
		Phase:      phase,
		BytesTotal: total,
		BytesLeft:  min(max(p.GetBytesLeft(), 0), total),
		Downloads:  int(max(p.GetDownloads(), 0)),
	}
	if eta := p.GetEstimatedCompletion(); eta != nil && eta.IsValid() {
		at := eta.AsTime().UTC()
		out.EstimatedCompletion = &at
	}
	return out
}

func (p *pluginRouterProvider) ListConfigOptions(ctx context.Context, installationID int, capabilityID string, conn ResolvedRouterConnection) (map[string][]RouterOption, error) {
	client, err := p.resolver.RequestRouterClient(ctx, installationID, capabilityID)
	if err != nil {
		return nil, err
	}
	pconn, err := routerProtoConn(conn)
	if err != nil {
		return nil, err
	}
	resp, err := client.ListConfigOptions(ctx, &pluginv1.ListConfigOptionsRequest{
		CapabilityId: capabilityID, Connection: pconn,
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
	pconn, err := routerProtoConn(conn)
	if err != nil {
		return false, "", err
	}
	resp, err := client.TestConnection(ctx, &pluginv1.TestConnectionRequest{
		CapabilityId: capabilityID, Connection: pconn,
	})
	if err != nil {
		return false, "", err
	}
	return resp.GetOk(), resp.GetMessage(), nil
}

func (p *pluginRouterProvider) Validate(ctx context.Context, installationID int, capabilityID string, conn ResolvedRouterConnection, siblings []ResolvedRouterConnection) (map[string]string, string, error) {
	client, err := p.resolver.RequestRouterClient(ctx, installationID, capabilityID)
	if err != nil {
		return nil, "", err
	}
	pc, err := routerProtoConn(conn)
	if err != nil {
		return nil, "", err
	}
	sibs, err := routerProtoConns(siblings)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Validate(ctx, &pluginv1.ValidateRequest{CapabilityId: capabilityID, Connection: pc, Siblings: sibs})
	if err != nil {
		return nil, "", err
	}
	return resp.GetFieldErrors(), resp.GetFormError(), nil
}

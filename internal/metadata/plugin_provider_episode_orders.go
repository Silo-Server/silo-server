package metadata

import (
	"context"
	"errors"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// EpisodeOrder is one of a provider's episode orders for a series.
type EpisodeOrder struct {
	ID           string
	Name         string
	Kind         string
	Description  string
	SeasonCount  int
	EpisodeCount int
	IsDefault    bool
}

// episodeOrderLister is the optional client side of a provider that lists
// episode orders (pluginhost.MetadataProviderClient).
type episodeOrderLister interface {
	SupportsEpisodeOrders() bool
	ListEpisodeOrders(ctx context.Context, req *pluginv1.ListEpisodeOrdersRequest) (*pluginv1.ListEpisodeOrdersResponse, error)
}

// ErrEpisodeOrdersUnsupported reports a provider that has a single episode
// order.
var ErrEpisodeOrdersUnsupported = errors.New("metadata: provider does not list episode orders")

// ListEpisodeOrders lists the series' episode orders at this provider. A
// plugin without the capability answers ErrEpisodeOrdersUnsupported; a
// series the provider does not know answers no orders.
func (p *PluginProvider) ListEpisodeOrders(ctx context.Context, providerIDs map[string]string, language string) ([]EpisodeOrder, error) {
	providerIDs = pluginRequestProviderIDs("series", providerIDs)
	providerID := providerIDs[p.capabilityID]
	if providerID == "" {
		return nil, nil
	}
	client, err := p.clientFactory(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return nil, err
	}
	lister, ok := client.(episodeOrderLister)
	if !ok || !lister.SupportsEpisodeOrders() {
		return nil, ErrEpisodeOrdersUnsupported
	}
	ids, err := structFromStringMap(providerIDs)
	if err != nil {
		return nil, err
	}
	response, err := lister.ListEpisodeOrders(ctx, &pluginv1.ListEpisodeOrdersRequest{
		SeriesProviderId: providerID,
		ProviderIds:      ids,
		Language:         language,
	})
	if err != nil {
		return nil, err
	}
	orders := make([]EpisodeOrder, 0, len(response.GetOrders()))
	for _, order := range response.GetOrders() {
		orders = append(orders, EpisodeOrder{
			ID:           order.GetId(),
			Name:         order.GetName(),
			Kind:         order.GetKind(),
			Description:  order.GetDescription(),
			SeasonCount:  int(order.GetSeasonCount()),
			EpisodeCount: int(order.GetEpisodeCount()),
			IsDefault:    order.GetIsDefault(),
		})
	}
	return orders, nil
}

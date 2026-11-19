package notifications

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// SetPluginChannelHost wires the plugin runtime into plugin destinations.
// Until it is called they list no channels, queue nothing and send nothing.
func (s *System) SetPluginChannelHost(host PluginChannelHost) {
	if s == nil || s.pluginDestinationSender == nil || host == nil {
		return
	}
	s.pluginDestinationSender.hostBox.Store(&pluginChannelHostBox{host: host})
}

// wirePluginDestinationSender gives the plugin destination sender the
// system's rendering and operational hooks once the System exists.
func (s *System) wirePluginDestinationSender() {
	if s.pluginDestinationSender == nil {
		return
	}
	// The notice reaches no webhook or plugin destination (loop guard). Its
	// channel settings are read before the sender's transaction begins.
	s.pluginDestinationSender.noticeWriter = func(ctx context.Context) func(context.Context, pgx.Tx, Delivery) (*InsertedDelivery, error) {
		channels := s.operationalChannels(ctx)
		return func(ctx context.Context, tx pgx.Tx, notice Delivery) (*InsertedDelivery, error) {
			return s.insertOperational(ctx, tx, notice, OperationalDispatch{}, nil, channels)
		}
	}
	s.pluginDestinationSender.dispatchNotice = s.dispatchOperationalRow
	s.pluginDestinationSender.posterURL = s.discordPosterURL
	s.pluginDestinationSender.linkBase = s.emailLinkBase
	s.pluginDestinationSender.payload = s.PayloadForRow
}

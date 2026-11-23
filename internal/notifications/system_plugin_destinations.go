package notifications

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/userstore"
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

// newPluginDestinationParts builds the plugin destination service, sender
// and dispatcher. All three are nil without an at-rest cipher: destination
// settings hold credentials. A profile is checked through the configured
// user store, which is not always Postgres.
func newPluginDestinationParts(pool *pgxpool.Pool, deliveries *DeliveryRepository, cipher *secret.Cipher, stores userstore.UserStoreProvider) (*PluginDestinationService, *pluginDestinationSender, *PluginDestinationDispatcher) {
	if cipher == nil {
		return nil, nil, nil
	}
	sender := newPluginDestinationSender(NewPluginDestinationRepository(pool), deliveries, cipher)
	service := newPluginDestinationService(sender.repo, sender)
	if stores != nil {
		service.profileExists = func(ctx context.Context, userID int, profileID string) (bool, error) {
			store, err := stores.ForUser(ctx, userID)
			if err != nil {
				return false, err
			}
			profile, err := store.GetProfile(ctx, profileID)
			return profile != nil, err
		}
	}
	return service, sender, newPluginDestinationDispatcher(sender)
}

// startPluginDestinations runs the dispatcher that sends queued attempts.
func (s *System) startPluginDestinations(ctx context.Context) {
	if s.pluginDestinationDispatcher == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.pluginDestinationDispatcher.Run(ctx)
	}()
}

// purgePluginDestinations deletes a removed profile's destinations.
func (s *System) purgePluginDestinations(ctx context.Context, userID int, profileID string) error {
	if s.pluginDestinationSender == nil {
		return nil
	}
	if err := s.pluginDestinationSender.repo.DeleteAllForProfile(ctx, userID, profileID); err != nil {
		return fmt.Errorf("purge plugin destinations: %w", err)
	}
	return nil
}

// prunePluginDestinationAttempts deletes attempts past their retention.
func (s *System) prunePluginDestinationAttempts(ctx context.Context, now time.Time) (int64, error) {
	if s.pluginDestinationSender == nil {
		return 0, nil
	}
	attempts, err := s.pluginDestinationSender.repo.DeleteOldAttempts(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("prune plugin destination attempts: %w", err)
	}
	return attempts, nil
}

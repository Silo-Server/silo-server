package downloads

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
)

type EpisodePageResolver interface {
	ListDownloadEpisodesPage(context.Context, string, *int, *catalog.EpisodePagePosition, int) ([]*models.Episode, bool, error)
}
type SubscriptionSyncPage struct {
	Registered int
	Examined   int
	Next       *catalog.EpisodePagePosition
}

// SyncSubscriptionPage bounds examined episodes for one current monitor before
// resolving their files. The caller iterates its bounded monitor list and pages
// each monitor; skipped/out-of-scope episodes still advance its cursor.
func (s *Service) SyncSubscriptionPage(ctx context.Context, userID int, profileID, deviceID, id string, after *catalog.EpisodePagePosition, limit int, filter catalog.AccessFilter, check func(*Subscription) error) (SubscriptionSyncPage, error) {
	if s.subRepo == nil {
		return SubscriptionSyncPage{}, ErrSubscriptionsUnavailable
	}
	pager, ok := s.episodeRepo.(EpisodePageResolver)
	if !ok {
		return SubscriptionSyncPage{}, ErrSubscriptionsUnavailable
	}
	if profileID == "" || deviceID == "" {
		return SubscriptionSyncPage{}, ErrProfileRequired
	}
	if limit < 1 || limit > 100 {
		return SubscriptionSyncPage{}, fmt.Errorf("sync page limit must be 1 to 100")
	}
	if _, _, err := s.downloadConfigForUser(ctx, userID, deviceID); err != nil {
		return SubscriptionSyncPage{}, err
	}

	sub, err := s.subRepo.GetByID(ctx, id, userID, profileID, deviceID)
	if err != nil {
		return SubscriptionSyncPage{}, err
	}
	if err := s.itemAccess.EnsureAccessible(ctx, sub.SeriesID, filter); err != nil {
		return SubscriptionSyncPage{}, err
	}
	if err := check(sub); err != nil {
		return SubscriptionSyncPage{}, err
	}
	if !sub.Active {
		return SubscriptionSyncPage{}, nil
	}
	episodes, more, err := pager.ListDownloadEpisodesPage(ctx, sub.SeriesID, nil, after, limit)
	if err != nil {
		return SubscriptionSyncPage{}, err
	}
	items, err := s.subscriptionEpisodeItems(ctx, sub, episodes)
	if err != nil {
		return SubscriptionSyncPage{}, err
	}
	out := SubscriptionSyncPage{Examined: len(episodes)}
	if more && len(episodes) > 0 {
		last := episodes[len(episodes)-1]
		out.Next = &catalog.EpisodePagePosition{SeasonNumber: last.SeasonNumber, EpisodeNumber: last.EpisodeNumber, ContentID: last.ContentID}
	}
	err = s.subRepo.WithLocked(ctx, userID, profileID, deviceID, id, func(locked *Subscription, tx pgx.Tx) error {
		if err := check(locked); err != nil {
			return err
		}
		if !locked.Active || !locked.UpdatedAt.Equal(sub.UpdatedAt) {
			return ErrStatusConflict
		}
		out.Registered, err = s.registerSubscriptionItems(ctx, locked, items, managedRegistryStore{tx})
		return err
	})
	return out, err
}

package watchstate

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RecordJellycompatParent commits child watch state and the parent favorite
// together before completion observers or watch providers see the edit.
func (s *Service) RecordJellycompatParent(ctx context.Context, userID int, profileID, parentID string, targetIDs []string, played, favorite bool) error {
	store, err := s.storeForUser(ctx, userID)
	if err != nil {
		return err
	}
	writer, ok := store.(userstore.JellycompatParentEditor)
	if !ok {
		return fmt.Errorf("atomic parent user data updates unavailable")
	}
	edit := userstore.JellycompatParentEdit{MediaItemID: parentID, IsFavorite: favorite, Played: played}
	if played {
		edit.Targets, edit.History = s.markWatchedBatchEntries(ctx, profileID, targetIDs, time.Now().UTC(), userstore.WatchHistorySourceJellycompat)
	} else {
		for _, id := range targetIDs {
			edit.Targets = append(edit.Targets, userstore.MarkWatchedTarget{MediaItemID: id})
		}
	}
	if err := writer.ApplyJellycompatParent(ctx, profileID, edit); err != nil {
		return err
	}
	if played && len(targetIDs) > 0 {
		s.notifyWatchedCompleted(ctx, userID, profileID, targetIDs)
	}
	return nil
}

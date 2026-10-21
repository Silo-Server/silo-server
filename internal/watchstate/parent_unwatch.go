package watchstate

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Parent kinds ParentUnwatchTargets expands.
const (
	ParentSeries = "series"
	ParentSeason = "season"
)

// EpisodePartitioner splits a series' or season's episodes into those a
// library holds a file for and those it doesn't, in one read.
// catalog.EpisodeRepository implements it.
type EpisodePartitioner interface {
	PartitionIDsBySeries(ctx context.Context, seriesID string) (withFile, fileless []string, err error)
	PartitionIDsBySeason(ctx context.Context, seasonID string) (withFile, fileless []string, err error)
}

// ParentUnwatchTargets returns the episodes marking a series or season
// unwatched must clear: every episode with a library file, plus the file-less
// ones the profile still has history or progress on. A watched episode whose
// file was deleted keeps its completed progress otherwise, and Next Up keeps
// anchoring the series on it. File-less episodes nobody watched stay out, so a
// long-running show with few files doesn't write thousands of hidden rows.
// Both sets come from one partition read, so an episode whose file is linked
// or unlinked mid-request still lands in one of them.
func (s *Service) ParentUnwatchTargets(ctx context.Context, userID int, profileID string, episodes EpisodePartitioner, parentKind, parentID string) ([]string, error) {
	var withFile, fileless []string
	var err error
	switch parentKind {
	case ParentSeries:
		withFile, fileless, err = episodes.PartitionIDsBySeries(ctx, parentID)
	case ParentSeason:
		withFile, fileless, err = episodes.PartitionIDsBySeason(ctx, parentID)
	default:
		return nil, fmt.Errorf("unwatch parent kind %q is not a series or season", parentKind)
	}
	if err != nil {
		return nil, err
	}
	if len(fileless) == 0 {
		return withFile, nil
	}
	store, err := s.storeForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	watched, err := WatchedItemIDs(ctx, store, profileID, fileless)
	if err != nil {
		return nil, err
	}
	return append(withFile, watched...), nil
}

// watchedItemIDBatch bounds each filter read below SQLite's default limit of
// 32,766 bound variables.
const watchedItemIDBatch = 1000

// WatchedItemIDs keeps the IDs the profile has visible history or resume
// progress for, once each, in input order.
func WatchedItemIDs(ctx context.Context, store userstore.UserStore, profileID string, ids []string) ([]string, error) {
	ids = compactItemIDs(ids)
	var watched []string
	for start := 0; start < len(ids); start += watchedItemIDBatch {
		batch := ids[start:min(start+watchedItemIDBatch, len(ids))]
		groups := make(map[string][]string, len(batch))
		for _, id := range batch {
			groups[id] = []string{id}
		}
		withHistory, err := store.LatestHistoryIDs(ctx, profileID, groups)
		if err != nil {
			return nil, err
		}
		withProgress, err := store.ListProgressByMediaItems(ctx, profileID, batch)
		if err != nil {
			return nil, err
		}
		for _, id := range batch {
			_, history := withHistory[id]
			_, progress := withProgress[id]
			if history || progress {
				watched = append(watched, id)
			}
		}
	}
	return watched, nil
}

func compactItemIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique
}

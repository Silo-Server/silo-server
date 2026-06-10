package recommendations

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

const signalPageSize = 1000

type signalRepo interface {
	GetWatchedItemIDSet(ctx context.Context, userID int, profileID string) (map[string]struct{}, error)
	GetWatchProgressForUser(ctx context.Context, userID int, profileID string) ([]WatchProgressRow, error)
	GetEbookReaderProgressForUser(ctx context.Context, userID int, profileID string) ([]WatchProgressRow, error)
	GetRecentCompletedItemIDs(ctx context.Context, userID int, profileID string, limit int) ([]string, error)
	GetRewatchCounts(ctx context.Context, userID int, profileID string) ([]RewatchCount, error)
	ResolveCanonicalItemIDSet(ctx context.Context, contentIDs []string) (map[string]struct{}, error)
}

// SignalReader centralizes profile-scoped recommendation signals. userstore is
// the source of truth when configured; repo SQL is retained for deployments
// without a store provider.
type SignalReader struct {
	repo          signalRepo
	storeProvider userstore.UserStoreProvider
}

func NewSignalReader(repo signalRepo, storeProvider userstore.UserStoreProvider) *SignalReader {
	return &SignalReader{
		repo:          repo,
		storeProvider: storeProvider,
	}
}

func (s *SignalReader) storeForUser(ctx context.Context, userID int) (userstore.UserStore, bool, error) {
	if s == nil || s.storeProvider == nil {
		return nil, false, nil
	}

	store, err := s.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, false, fmt.Errorf("open user store for user %d: %w", userID, err)
	}
	if store == nil {
		return nil, false, nil
	}
	return store, true, nil
}

func (s *SignalReader) WatchedItemIDSet(ctx context.Context, userID int, profileID string) (map[string]struct{}, error) {
	store, ok, err := s.storeForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.repo.GetWatchedItemIDSet(ctx, userID, profileID)
	}

	rawIDs := make([]string, 0, signalPageSize)
	if err := pageProgress(ctx, store, profileID, "all", func(progress []userstore.WatchProgress) error {
		for _, wp := range progress {
			if wp.Completed || watchedProgressThresholdMet(wp.PositionSeconds, wp.DurationSeconds) {
				rawIDs = append(rawIDs, wp.MediaItemID)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	ebookProgress, err := s.repo.GetEbookReaderProgressForUser(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	for _, wp := range ebookProgress {
		if wp.Completed || watchedProgressThresholdMet(wp.PositionSeconds, wp.DurationSeconds) {
			rawIDs = append(rawIDs, wp.MediaItemID)
		}
	}

	return s.repo.ResolveCanonicalItemIDSet(ctx, rawIDs)
}

func (s *SignalReader) WatchProgressForUser(ctx context.Context, userID int, profileID string) ([]WatchProgressRow, error) {
	store, ok, err := s.storeForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.repo.GetWatchProgressForUser(ctx, userID, profileID)
	}

	rows := make([]WatchProgressRow, 0, signalPageSize)
	if err := pageProgress(ctx, store, profileID, "all", func(progress []userstore.WatchProgress) error {
		for _, wp := range progress {
			rows = append(rows, WatchProgressRow{
				MediaItemID:     wp.MediaItemID,
				PositionSeconds: wp.PositionSeconds,
				DurationSeconds: wp.DurationSeconds,
				Completed:       wp.Completed,
				UpdatedAt:       parseSignalTime(wp.UpdatedAt, time.Time{}),
			})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	ebookProgress, err := s.repo.GetEbookReaderProgressForUser(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	rows = append(rows, ebookProgress...)

	return rows, nil
}

func (s *SignalReader) RecentCompletedItemIDs(ctx context.Context, userID int, profileID string, limit int) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}

	store, ok, err := s.storeForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.repo.GetRecentCompletedItemIDs(ctx, userID, profileID, limit)
	}

	progress, err := store.ListProgress(ctx, profileID, "completed", limit, 0)
	if err != nil {
		return nil, fmt.Errorf("list completed progress from store: %w", err)
	}
	completed := make([]WatchProgressRow, 0, len(progress))
	for _, wp := range progress {
		if !wp.Completed {
			continue
		}
		completed = append(completed, WatchProgressRow{
			MediaItemID: wp.MediaItemID,
			Completed:   true,
			UpdatedAt:   parseSignalTime(wp.UpdatedAt, time.Time{}),
		})
	}
	ebookProgress, err := s.repo.GetEbookReaderProgressForUser(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	for _, wp := range ebookProgress {
		if !wp.Completed {
			continue
		}
		completed = append(completed, wp)
	}
	sort.SliceStable(completed, func(i, j int) bool {
		left := completed[i].UpdatedAt
		right := completed[j].UpdatedAt
		if !left.Equal(right) {
			return left.After(right)
		}
		return completed[i].MediaItemID < completed[j].MediaItemID
	})

	ids := make([]string, 0, min(limit, len(completed)))
	for _, wp := range completed {
		ids = append(ids, wp.MediaItemID)
		if len(ids) == limit {
			break
		}
	}
	return ids, nil
}

func (s *SignalReader) RewatchCounts(ctx context.Context, userID int, profileID string) ([]RewatchCount, error) {
	store, ok, err := s.storeForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.repo.GetRewatchCounts(ctx, userID, profileID)
	}

	counts := make(map[string]*RewatchCount)
	offset := 0
	for {
		history, err := store.ListCompletedHistory(ctx, userstore.CompletedHistoryQuery{
			ProfileID: profileID,
			Limit:     signalPageSize,
			Offset:    offset,
		})
		if err != nil {
			return nil, fmt.Errorf("list completed history from store: %w", err)
		}
		for _, entry := range history {
			if !entry.Completed {
				continue
			}
			rc := counts[entry.MediaItemID]
			if rc == nil {
				rc = &RewatchCount{MediaItemID: entry.MediaItemID}
				counts[entry.MediaItemID] = rc
			}
			rc.Count++
			watchedAt := parseSignalTime(entry.WatchedAt, time.Time{})
			if watchedAt.After(rc.LastWatchedAt) {
				rc.LastWatchedAt = watchedAt
			}
		}
		if len(history) < signalPageSize {
			break
		}
		offset += len(history)
	}

	result := make([]RewatchCount, 0, len(counts))
	for _, rc := range counts {
		if rc.Count >= 2 {
			result = append(result, *rc)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].MediaItemID < result[j].MediaItemID
	})
	return result, nil
}

func pageProgress(ctx context.Context, store userstore.UserStore, profileID, status string, visit func([]userstore.WatchProgress) error) error {
	offset := 0
	for {
		progress, err := store.ListProgress(ctx, profileID, status, signalPageSize, offset)
		if err != nil {
			return fmt.Errorf("list progress from store: %w", err)
		}
		if err := visit(progress); err != nil {
			return err
		}
		if len(progress) < signalPageSize {
			return nil
		}
		offset += len(progress)
	}
}

func watchedProgressThresholdMet(positionSeconds, durationSeconds float64) bool {
	return durationSeconds > 0 && positionSeconds/durationSeconds >= 0.5
}

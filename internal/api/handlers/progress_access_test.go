package handlers

import (
	"context"
	"slices"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeProgressLookup records the arguments it was called with and returns a
// fixed accessibility map.
type fakeProgressLookup struct {
	accessible map[string]bool

	gotContentIDs []string
	gotAllowed    []int
	gotDisabled   []int
	gotRating     string
	gotLimits     access.MaturityLimits
}

func (f *fakeProgressLookup) GetItemsInFolder(context.Context, []string, int) (map[string]bool, error) {
	return nil, nil
}

func (f *fakeProgressLookup) FilterAccessibleContentIDs(
	_ context.Context, contentIDs []string, allowedFolderIDs, disabledFolderIDs []int, limits access.MaturityLimits,
) (map[string]bool, error) {
	f.gotContentIDs = contentIDs
	f.gotAllowed = allowedFolderIDs
	f.gotDisabled = disabledFolderIDs
	f.gotRating = limits.MaxContentRating
	f.gotLimits = limits
	return f.accessible, nil
}

func entries(ids ...string) []userstore.WatchProgress {
	out := make([]userstore.WatchProgress, 0, len(ids))
	for _, id := range ids {
		out = append(out, userstore.WatchProgress{MediaItemID: id})
	}
	return out
}

func ids(entries []userstore.WatchProgress) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.MediaItemID)
	}
	return out
}

// pageProgressStore serves rows already in listing order, resuming after the
// key's media item, and counts its reads.
type pageProgressStore struct {
	userstore.UserStore
	rows  []userstore.WatchProgress
	reads int
}

func (s *pageProgressStore) ListProgressPage(_ context.Context, _, _ string, after *userstore.ProgressKey, limit int) ([]userstore.WatchProgress, error) {
	s.reads++
	start := 0
	if after != nil {
		start = slices.IndexFunc(s.rows, func(r userstore.WatchProgress) bool { return r.MediaItemID == after.MediaItemID }) + 1
	}
	return s.rows[start:min(start+limit, len(s.rows))], nil
}

func (s *pageProgressStore) ForUser(context.Context, int) (userstore.UserStore, error) {
	return s, nil
}

func (s *pageProgressStore) Close() error { return nil }

package downloads

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type hintProgressStore struct {
	userstore.UserStore
	rows map[string]userstore.WatchProgress
	err  error
	// itemLookups and pageLimits record what each read asked for.
	itemLookups [][]string
	pageLimits  []int
}

func (s *hintProgressStore) ListProgressByMediaItems(_ context.Context, _ string, ids []string) (map[string]userstore.WatchProgress, error) {
	s.itemLookups = append(s.itemLookups, ids)
	if s.err != nil {
		return nil, s.err
	}
	out := map[string]userstore.WatchProgress{}
	for _, id := range ids {
		if row, ok := s.rows[id]; ok {
			row.MediaItemID = id
			out[id] = row
		}
	}
	return out, nil
}

// ListProgressPage returns the newest rows first, as the stores do.
func (s *hintProgressStore) ListProgressPage(_ context.Context, _ string, _ string, _ *userstore.ProgressKey, limit int) ([]userstore.WatchProgress, error) {
	s.pageLimits = append(s.pageLimits, limit)
	if s.err != nil {
		return nil, s.err
	}
	out := make([]userstore.WatchProgress, 0, len(s.rows))
	for id, row := range s.rows {
		row.MediaItemID = id
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return newerProgress(out[i], out[j]) })
	return out[:min(limit, len(out))], nil
}

type hintProgressStores struct{ store *hintProgressStore }

func (p hintProgressStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

type episodeFiles map[string][]*models.MediaFile

func (r episodeFiles) GetByID(context.Context, int) (*models.MediaFile, error) {
	return nil, errors.New("unused")
}

func (r episodeFiles) GetByContentID(_ context.Context, id string) ([]*models.MediaFile, error) {
	return r[id], nil
}

func (r episodeFiles) GetByEpisodeID(_ context.Context, id string) ([]*models.MediaFile, error) {
	return r[id], nil
}

func (r episodeFiles) ListByEpisodeIDs(_ context.Context, ids []string) (map[string][]*models.MediaFile, error) {
	out := map[string][]*models.MediaFile{}
	for _, id := range ids {
		out[id] = r[id]
	}
	return out, nil
}

func TestAutomaticPickSkipsVersionsTheProfileCannotPlay(t *testing.T) {
	// The profile last played the 2160p file but is now capped at 1080p; the
	// pick must not register a version serving would refuse.
	store := &hintProgressStore{rows: map[string]userstore.WatchProgress{
		"movie": {UpdatedAt: "2026-09-20T10:00:00Z", LastFileID: new(2)},
	}}
	svc := &Service{
		fileRepo:       episodeFiles{"movie": {{ID: 1, Resolution: "1080p"}, {ID: 2, Resolution: "2160p"}}},
		progressStores: hintProgressStores{store},
	}

	file, err := svc.resolveFile(context.Background(), 7, CreateRequest{ContentID: "movie", ProfileID: "p1", VersionFromHistory: true}, catalog.AccessFilter{MaxPlaybackQuality: "1080p"})
	if err != nil {
		t.Fatal(err)
	}
	if file.ID != 1 {
		t.Fatalf("resolveFile = file %d, want the allowed 1080p file 1", file.ID)
	}
}

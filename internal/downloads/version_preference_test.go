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

func TestVersionPreferenceDefaultsToHighestResolutionWithStableTieBreak(t *testing.T) {
	a := &models.MediaFile{ID: 9, Resolution: "1080p"}
	b := &models.MediaFile{ID: 4, Resolution: "1080p"}
	c := &models.MediaFile{ID: 2, Resolution: "720p"}

	for _, files := range [][]*models.MediaFile{{a, b, c}, {c, b, a}} {
		if got := (versionPreference{}).pick("movie", files); got.ID != 4 {
			t.Fatalf("pick = file %d, want 4 (highest resolution, lowest ID)", got.ID)
		}
	}
}

func TestVersionPreferencePrefersTheItemsLastPlayedFile(t *testing.T) {
	// The last-played 720p file is the one with sidecar subtitles; the
	// highest-resolution default would silently swap it for the 1080p file.
	withSubs := &models.MediaFile{ID: 1, Resolution: "720p"}
	larger := &models.MediaFile{ID: 2, Resolution: "1080p"}
	pref := versionPreference{lastFile: map[string]int{"movie": 1}}

	if got := pref.pick("movie", []*models.MediaFile{larger, withSubs}); got.ID != 1 {
		t.Fatalf("pick = file %d, want the last-played file 1", got.ID)
	}
}

func TestResolveFileUsesTheSeriesVersionForAnEpisode(t *testing.T) {
	store := &hintProgressStore{rows: map[string]userstore.WatchProgress{
		"ep-1": {UpdatedAt: "2026-09-01T10:00:00Z", LastFileID: new(100), LastResolution: new("2160p")},
		"ep-2": {UpdatedAt: "2026-09-20T10:00:00Z", LastFileID: new(201), LastResolution: new("720p"), LastHDR: new(false)},
		// A newer play of another series must not decide this one.
		"other-ep": {UpdatedAt: "2026-09-25T10:00:00Z", LastFileID: new(900), LastResolution: new("2160p")},
		"movie":    {UpdatedAt: "2026-09-26T10:00:00Z", LastFileID: new(950), LastResolution: new("2160p")},
	}}
	svc := &Service{
		fileRepo: episodeFiles{
			"ep-1":     {{ID: 100, ContentID: "series", EpisodeID: "ep-1", Resolution: "2160p"}},
			"ep-2":     {{ID: 201, ContentID: "series", EpisodeID: "ep-2", Resolution: "720p"}},
			"other-ep": {{ID: 900, ContentID: "other-series", EpisodeID: "other-ep", Resolution: "2160p"}},
			"ep-3": {
				{ID: 300, ContentID: "series", EpisodeID: "ep-3", Resolution: "2160p", HDR: true},
				{ID: 301, ContentID: "series", EpisodeID: "ep-3", Resolution: "720p"},
			},
		},
		progressStores: hintProgressStores{store},
	}

	file, err := svc.resolveFile(context.Background(), 7, CreateRequest{ContentID: "series", EpisodeID: "ep-3", ProfileID: "p1", VersionFromHistory: true}, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if file.ID != 301 {
		t.Fatalf("resolveFile = file %d, want 301 (the version type of the most recently played episode)", file.ID)
	}

	// The frozen v1 bridge keeps its highest-resolution default.
	file, err = svc.resolveFile(context.Background(), 7, CreateRequest{ContentID: "series", EpisodeID: "ep-3", ProfileID: "p1"}, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if file.ID != 300 {
		t.Fatalf("v1 resolveFile = file %d, want the highest resolution 300", file.ID)
	}
}

func TestEpisodeItemsFollowTheSeriesVersion(t *testing.T) {
	store := &hintProgressStore{rows: map[string]userstore.WatchProgress{
		"ep-1": {UpdatedAt: "2026-09-20T10:00:00Z", LastFileID: new(101), LastResolution: new("720p")},
	}}
	svc := &Service{
		fileRepo: episodeFiles{
			"ep-1": {{ID: 100, ContentID: "series", Resolution: "1080p"}, {ID: 101, ContentID: "series", Resolution: "720p"}},
			"ep-2": {{ID: 200, ContentID: "series", Resolution: "1080p"}, {ID: 201, ContentID: "series", Resolution: "720p"}},
			"ep-3": {{ID: 300, ContentID: "series", Resolution: "1080p"}, {ID: 301, ContentID: "series", Resolution: "720p"}},
		},
		progressStores: hintProgressStores{store},
	}

	// A page holding only ep-2 and ep-3 still follows ep-1's version.
	items, err := svc.episodeItems(context.Background(), 7, "p1", "series", []*models.Episode{{ContentID: "ep-2"}, {ContentID: "ep-3"}}, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := []int{items[0].file.ID, items[1].file.ID}
	if got[0] != 201 || got[1] != 301 {
		t.Fatalf("episode files = %v, want [201 301]", got)
	}
	// Each page reads its own episodes and one fixed window of recent
	// history, never the whole series.
	if len(store.itemLookups) != 1 || len(store.itemLookups[0]) != 2 {
		t.Fatalf("item lookups = %v, want one lookup of the 2 page episodes", store.itemLookups)
	}
	if len(store.pageLimits) != 1 || store.pageLimits[0] != seriesHistoryWindow {
		t.Fatalf("recent history reads = %v, want one read of %d rows", store.pageLimits, seriesHistoryWindow)
	}
}

func TestVersionPreferenceFallsBackWhenProgressIsUnavailable(t *testing.T) {
	files := episodeFiles{"movie": {{ID: 1, Resolution: "720p"}, {ID: 2, Resolution: "1080p"}}}
	for name, svc := range map[string]*Service{
		"no progress store": {fileRepo: files},
		"read error":        {fileRepo: files, progressStores: hintProgressStores{&hintProgressStore{err: errors.New("down")}}},
	} {
		t.Run(name, func(t *testing.T) {
			file, err := svc.resolveFile(context.Background(), 7, CreateRequest{ContentID: "movie", ProfileID: "p1", VersionFromHistory: true}, catalog.AccessFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if file.ID != 2 {
				t.Fatalf("resolveFile = file %d, want the highest resolution 2", file.ID)
			}
		})
	}
}

func TestMonitorSyncUsesHistoryOnlyForTheNativeSync(t *testing.T) {
	store := &hintProgressStore{rows: map[string]userstore.WatchProgress{
		"ep-1": {UpdatedAt: "2026-09-20T10:00:00Z", LastFileID: new(101), LastResolution: new("720p")},
	}}
	svc := &Service{
		fileRepo: episodeFiles{
			"ep-1": {{ID: 100, ContentID: "series", Resolution: "1080p"}, {ID: 101, ContentID: "series", Resolution: "720p"}},
			"ep-2": {{ID: 200, ContentID: "series", Resolution: "1080p"}, {ID: 201, ContentID: "series", Resolution: "720p"}},
		},
		progressStores: hintProgressStores{store},
	}
	sub := &Subscription{UserID: 7, ProfileID: "p1", SeriesID: "series", Mode: SubModeAll}
	episodes := []*models.Episode{{ContentID: "ep-2"}}

	for _, tc := range []struct {
		native bool
		want   int
	}{{native: true, want: 201}, {native: false, want: 200}} {
		items, err := svc.subscriptionEpisodeItems(context.Background(), sub, episodes, tc.native, catalog.AccessFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if items[0].file.ID != tc.want {
			t.Fatalf("native=%v: file %d, want %d", tc.native, items[0].file.ID, tc.want)
		}
	}
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

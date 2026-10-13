package catalog

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type trickplayLookupCounter struct {
	calls int
	ids   int
}

func (c *trickplayLookupCounter) TrickplayGrids(_ context.Context, ids []int) (map[int]TrickplayGrid, error) {
	c.calls++
	c.ids += len(ids)
	grids := make(map[int]TrickplayGrid, len(ids))
	for _, id := range ids {
		grids[id] = TrickplayGrid{Width: 300}
	}
	return grids, nil
}

func TestTrickplayAvailabilityBatchesItemDetails(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	for _, count := range []int{1, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			prefix := fmt.Sprintf("review-trickplay-batch-%d-", time.Now().UnixNano())
			ids := make([]string, count)
			files := &batchEquivFileFetcher{files: map[string][]*models.MediaFile{}}
			var folderID int
			if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders(type,name) VALUES('movies',$1) RETURNING id`, prefix).Scan(&folderID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id=$1`, folderID)
				batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id=ANY($1)`, ids)
			})
			for i := range count {
				id := fmt.Sprintf("%s%d", prefix, i)
				ids[i] = id
				batchEquivExec(t, pool, `INSERT INTO media_items(content_id,type,title,genres,default_metadata_language) VALUES($1,'movie',$1,'{}','en')`, id)
				batchEquivExec(t, pool, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, folderID)
				files.files[id] = []*models.MediaFile{{ID: i + 1, ContentID: id, MediaFolderID: folderID, FilePath: "/media/review.mkv", Container: "mkv", Duration: 100, VideoTracks: []models.VideoTrack{{Codec: "h264"}}}}
			}
			svc := NewDetailService(NewItemRepository(pool), nil, nil, nil, files)
			counter := &trickplayLookupCounter{}
			svc.SetTrickplayAvailability(counter)
			details, err := svc.GetItemDetailsByIDs(t.Context(), ids, AccessFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if len(details) != count {
				t.Fatalf("details=%d want=%d", len(details), count)
			}
			t.Logf("batch items=%d trickplay lookups=%d file ids=%d", len(details), counter.calls, counter.ids)
			if counter.calls != 1 {
				t.Fatalf("calls=%d want one batch", counter.calls)
			}
			for _, detail := range details {
				if len(detail.Versions) != 1 || detail.Versions[0].Trickplay == nil {
					t.Fatalf("batch lost availability: %+v", detail.Versions)
				}
			}
		})
	}
}

type trickplayEpisodeBatchFetcher struct {
	*versionsFileFetcher
	calls           int
	individualCalls int
	batchErr        error
	failedEpisode   string
}

func (f *trickplayEpisodeBatchFetcher) ListByEpisodeIDs(_ context.Context, ids []string) (map[string][]*models.MediaFile, error) {
	f.calls++
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	out := make(map[string][]*models.MediaFile, len(ids))
	for _, id := range ids {
		out[id] = f.files[id]
	}
	return out, nil
}

func (f *trickplayEpisodeBatchFetcher) GetByEpisodeID(ctx context.Context, id string) ([]*models.MediaFile, error) {
	f.individualCalls++
	if id == f.failedEpisode {
		return nil, errors.New("episode file lookup failed")
	}
	return f.versionsFileFetcher.GetByEpisodeID(ctx, id)
}

func TestEpisodeDetailsFallBackAfterBatchFileFailureDB(t *testing.T) {
	f := newVersionsFixture(t)
	failedEpisode := f.ids["episode"] + "-unreadable"
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO episodes(content_id,series_id,season_id,season_number,episode_number,title) VALUES($1,$2,$3,1,2,'Episode')`, failedEpisode, f.ids["series"], f.ids["season"]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES($1,$2)`, failedEpisode, f.library); err != nil {
		t.Fatal(err)
	}
	files := &trickplayEpisodeBatchFetcher{versionsFileFetcher: f.files, batchErr: errors.New("batch file lookup failed"), failedEpisode: failedEpisode}
	f.svc.fileFetcher = files
	counter := &trickplayLookupCounter{}
	f.svc.SetTrickplayAvailability(counter)
	details, err := f.svc.GetEpisodeDetailsForSeries(t.Context(), f.ids["series"], []string{f.ids["episode"], failedEpisode}, AccessFilter{})
	if err != nil {
		t.Fatalf("optional batch failure discarded the episode page: %v", err)
	}
	if len(details) != 1 || details[f.ids["episode"]] == nil || details[failedEpisode] != nil {
		t.Fatalf("fallback details=%v", details)
	}
	if files.calls != 1 || files.individualCalls != 2 || counter.calls != 1 {
		t.Fatalf("batch calls=%d individual calls=%d trickplay calls=%d", files.calls, files.individualCalls, counter.calls)
	}
	for _, version := range details[f.ids["episode"]].Versions {
		if version.Trickplay == nil {
			t.Fatal("fallback lost trickplay availability")
		}
	}
}

func TestTrickplayAvailabilityBatchesEpisodeDetails(t *testing.T) {
	f := newVersionsFixture(t)
	episodes := []string{f.ids["episode"]}
	for n := 2; n <= 4; n++ {
		id := fmt.Sprintf("%s-%d", f.ids["episode"], n)
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO episodes(content_id,series_id,season_id,season_number,episode_number,title) VALUES($1,$2,$3,1,$4,'Episode')`, id, f.ids["series"], f.ids["season"], n); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES($1,$2)`, id, f.library); err != nil {
			t.Fatal(err)
		}
		f.files.files[id] = f.files.files[f.ids["episode"]]
		episodes = append(episodes, id)
	}
	files := &trickplayEpisodeBatchFetcher{versionsFileFetcher: f.files}
	f.svc.fileFetcher = files
	counter := &trickplayLookupCounter{}
	f.svc.SetTrickplayAvailability(counter)
	details, err := f.svc.GetEpisodeDetailsForSeries(t.Context(), f.ids["series"], episodes, AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != len(episodes) || files.calls != 1 || counter.calls != 1 {
		t.Fatalf("details=%d files calls=%d availability calls=%d", len(details), files.calls, counter.calls)
	}
	for _, detail := range details {
		if len(detail.Versions) != 2 || detail.Versions[0].Trickplay == nil || detail.Versions[1].Trickplay == nil {
			t.Fatalf("episode lost availability: %+v", detail.Versions)
		}
	}
}

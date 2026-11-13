package downloads

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

type createEpisodePager struct {
	EpisodeResolver
	rows   []*models.Episode
	more   bool
	season *int
	after  *catalog.EpisodePagePosition
	limit  int
}

func (p *createEpisodePager) ListDownloadEpisodesPage(_ context.Context, _ string, season *int, after *catalog.EpisodePagePosition, limit int) ([]*models.Episode, bool, error) {
	p.season = season
	p.after = after
	p.limit = limit
	return p.rows, p.more, nil
}

type createItemResolver struct{}

func (createItemResolver) GetByID(context.Context, string) (*models.MediaItem, error) {
	return &models.MediaItem{ContentID: "series", Type: "series"}, nil
}

type createFileResolver struct {
	FileResolver
	rows   map[string][]*models.MediaFile
	single *models.MediaFile
}

func (f *createFileResolver) ListByEpisodeIDs(context.Context, []string) (map[string][]*models.MediaFile, error) {
	return f.rows, nil
}
func (f *createFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) {
	return f.single, nil
}

func TestDownloadCreatePagesPostgres(t *testing.T) {
	// Managed creates clear monitor exclusions, so the schema needs the
	// subscription tables too.
	repo := NewRepository(subscriptionMutationTestRepo(t).pool)
	_, err := repo.pool.Exec(t.Context(), `CREATE UNIQUE INDEX managed_identity ON downloads(user_id,profile_id,device_id,content_id,COALESCE(episode_id,'')) WHERE device_id IS NOT NULL;
 CREATE TABLE user_devices(user_id integer,profile_id text,device_id text,device_name text,device_platform text,last_seen_at timestamptz,PRIMARY KEY(user_id,profile_id,device_id))`)
	if err != nil {
		t.Fatal(err)
	}
	pager := &createEpisodePager{rows: []*models.Episode{{ContentID: "missing", SeasonNumber: 0, EpisodeNumber: 1}}, more: true}
	files := &createFileResolver{rows: map[string][]*models.MediaFile{}}
	access := &syncAccess{}
	svc := NewService(repo, nil, nil, files, createItemResolver{}, pager, fakeUserRepo{&models.User{ID: 1, DownloadAllowed: new(true)}}, access, nil, &config.DownloadConfig{Enabled: true})
	req := CreateRequest{ContentID: "series", ProfileID: "profile", DeviceID: "device", BatchID: "intent", StrictIdentity: true}
	page, err := svc.CreateSeriesPage(t.Context(), 1, req, new(0), nil, 1, catalog.AccessFilter{})
	if err != nil || len(page.Items) != 0 || len(page.Skipped) != 1 || page.Next == nil || page.Next.ContentID != "missing" || pager.season == nil || *pager.season != 0 || pager.limit != 1 {
		t.Fatalf("empty page %+v %v", page, err)
	}
	pager.rows = []*models.Episode{{ContentID: "special", SeasonNumber: 0, EpisodeNumber: 2}}
	pager.more = false
	files.rows = map[string][]*models.MediaFile{"special": {{ID: 42, ContentID: "series", EpisodeID: "special", FileSize: 10}}}
	seedSchemaFiles(t, repo, files.rows["special"][0])
	page, err = svc.CreateSeriesPage(t.Context(), 1, req, new(0), page.Next, 1, catalog.AccessFilter{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Status != StatusReady || page.Items[0].BatchID != "intent" || pager.after == nil {
		t.Fatalf("managed %+v %v", page, err)
	}
	id := page.Items[0].ID
	if _, err := repo.pool.Exec(t.Context(), `UPDATE downloads SET status='failed',revision=2,batch_id='newer' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	page, err = svc.CreateSeriesPage(t.Context(), 1, req, new(0), nil, 1, catalog.AccessFilter{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != id || page.Items[0].Status != StatusFailed || page.Items[0].BatchID != "newer" || page.Items[0].Revision != 2 {
		t.Fatalf("overwrote winner %+v %v", page, err)
	}
	req.ExpectedEntries = map[string]ManagedCreateExpectation{"special": {ID: id, Revision: 2}}
	page, err = svc.CreateSeriesPage(t.Context(), 1, req, new(0), nil, 1, catalog.AccessFilter{})
	if err != nil || page.Items[0].Revision != 3 || page.Items[0].Status != StatusReady || page.Items[0].BatchID != "intent" {
		t.Fatalf("explicit revival %+v %v", page, err)
	}
	if _, err := svc.CreateSeriesPage(t.Context(), 1, req, new(0), nil, 1, catalog.AccessFilter{}); !errors.Is(err, ErrStatusConflict) {
		t.Fatalf("stale guard %v", err)
	}
	req.DeviceID = ""
	req.ExpectedEntries = nil
	page, err = svc.CreateSeriesPage(t.Context(), 1, req, nil, nil, 1, catalog.AccessFilter{})
	if err != nil || len(page.Items) != 1 || page.Items[0].DeviceID != "" || page.Items[0].Status != StatusQueued || page.Items[0].BatchID != "intent" {
		t.Fatalf("ephemeral %+v %v", page, err)
	}
	// Single ephemeral registration uses the same policy and queued lifecycle.
	files.single = &models.MediaFile{ID: 43, ContentID: "movie", FileSize: 12, Container: "mp4", CodecVideo: "h264", CodecAudio: "aac"}
	seedSchemaFiles(t, repo, files.single)
	single, err := svc.Create(t.Context(), 1, CreateRequest{ContentID: "movie", FileID: 43, StrictIdentity: true}, catalog.AccessFilter{})
	if err != nil || single.DeviceID != "" || single.Status != StatusQueued || single.MediaFileID != 43 {
		t.Fatalf("single ephemeral %+v %v", single, err)
	}
	managed, err := svc.Create(t.Context(), 1, CreateRequest{ContentID: "movie", FileID: 43, StrictIdentity: true, ProfileID: "profile", DeviceID: "device", ExpectedRevision: new(0)}, catalog.AccessFilter{})
	if err != nil || managed.Status != StatusReady || managed.MediaFileID != 43 {
		t.Fatalf("single managed %+v %v", managed, err)
	}
	access.err = catalog.ErrItemNotFound
	if _, err := svc.CreateSeriesPage(t.Context(), 1, req, nil, nil, 1, catalog.AccessFilter{}); !errors.Is(err, catalog.ErrItemNotFound) {
		t.Fatal(err)
	}
	// Native file selection binds the requested catalog identity before policy
	// or artifact admission, even if the supplied file itself exists.
	access.err = nil
	files.single = &models.MediaFile{ID: 42, ContentID: "other"}
	if _, err := svc.Create(t.Context(), 1, CreateRequest{ContentID: "series", FileID: 42, StrictIdentity: true}, catalog.AccessFilter{}); !errors.Is(err, catalog.ErrItemNotFound) {
		t.Fatal(err)
	}
}

func TestSeriesPageFollowsTheVersionTemplatePostgres(t *testing.T) {
	repo := NewRepository(subscriptionMutationTestRepo(t).pool)
	pager := &createEpisodePager{rows: []*models.Episode{{ContentID: "ep-1", SeasonNumber: 1, EpisodeNumber: 1}, {ContentID: "ep-2", SeasonNumber: 1, EpisodeNumber: 2}}}
	uhd := func(id int, ep string) *models.MediaFile {
		return &models.MediaFile{ID: id, ContentID: "series", EpisodeID: ep, Resolution: "2160p", HDR: true, CodecVideo: "hevc", FileSize: 20}
	}
	hd := func(id int, ep string) *models.MediaFile {
		return &models.MediaFile{ID: id, ContentID: "series", EpisodeID: ep, Resolution: "1080p", CodecVideo: "h264", FileSize: 10}
	}
	template := hd(11, "ep-1")
	files := &createFileResolver{rows: map[string][]*models.MediaFile{"ep-1": {uhd(10, "ep-1"), template}, "ep-2": {uhd(20, "ep-2"), hd(21, "ep-2")}}, single: template}
	for _, episodeFiles := range files.rows {
		seedSchemaFiles(t, repo, episodeFiles...)
	}
	svc := NewService(repo, nil, nil, files, createItemResolver{}, pager, fakeUserRepo{&models.User{ID: 1, DownloadAllowed: new(true)}}, &syncAccess{}, nil, &config.DownloadConfig{Enabled: true})
	req := CreateRequest{ContentID: "series", ProfileID: "profile", BatchID: "intent", StrictIdentity: true}

	// Without a template the batch takes the highest resolution.
	page, err := svc.CreateSeriesPage(t.Context(), 1, req, new(1), nil, 10, catalog.AccessFilter{})
	if err != nil || len(page.Items) != 2 || page.Items[0].MediaFileID != 10 || page.Items[1].MediaFileID != 20 {
		t.Fatalf("default %+v %v", page.Items, err)
	}
	// With the 1080p SDR file of episode 1 as the template, every episode
	// takes its own 1080p SDR version.
	req.BatchID = "templated"
	req.VersionTemplateFileID = 11
	page, err = svc.CreateSeriesPage(t.Context(), 1, req, new(1), nil, 10, catalog.AccessFilter{})
	if err != nil || len(page.Items) != 2 || page.Items[0].MediaFileID != 11 || page.Items[1].MediaFileID != 21 {
		t.Fatalf("template %+v %v", page.Items, err)
	}
}

func TestSeasonWithAMultiEpisodeFileRegistersEachEpisodeOncePostgres(t *testing.T) {
	repo := NewRepository(subscriptionMutationTestRepo(t).pool)
	_, err := repo.pool.Exec(t.Context(), `CREATE UNIQUE INDEX managed_identity ON downloads(user_id,profile_id,device_id,content_id,COALESCE(episode_id,'')) WHERE device_id IS NOT NULL;
 CREATE TABLE user_devices(user_id integer,profile_id text,device_id text,device_name text,device_platform text,last_seen_at timestamptz,PRIMARY KEY(user_id,profile_id,device_id))`)
	if err != nil {
		t.Fatal(err)
	}
	// The 720p S01E01-E03 file is linked to episode 1, as the catalog links a
	// multi-episode file to its first episode. Episode 1 also has its own
	// 720p file, episode 2 its own 1080p file, and episode 3 only the range.
	own := &models.MediaFile{ID: 1, ContentID: "series", EpisodeID: "ep-1", Resolution: "720p", FileSize: 10}
	multi := &models.MediaFile{ID: 3, ContentID: "series", EpisodeID: "ep-1", Resolution: "720p", MultiEpisodeStart: 1, MultiEpisodeEnd: 3, FileSize: 30}
	second := &models.MediaFile{ID: 2, ContentID: "series", EpisodeID: "ep-2", Resolution: "1080p", FileSize: 20}
	seedSchemaFiles(t, repo, own, multi, second)
	pager := &createEpisodePager{rows: []*models.Episode{{ContentID: "ep-1", SeasonNumber: 1, EpisodeNumber: 1}, {ContentID: "ep-2", SeasonNumber: 1, EpisodeNumber: 2}, {ContentID: "ep-3", SeasonNumber: 1, EpisodeNumber: 3}}}
	files := &createFileResolver{rows: map[string][]*models.MediaFile{"ep-1": {own, multi}, "ep-2": {multi, second}, "ep-3": {multi}}, single: own}
	svc := NewService(repo, nil, nil, files, createItemResolver{}, pager, fakeUserRepo{&models.User{ID: 1, DownloadAllowed: new(true)}}, &syncAccess{}, nil, &config.DownloadConfig{Enabled: true})
	req := CreateRequest{ContentID: "series", ProfileID: "profile", DeviceID: "device", BatchID: "season", StrictIdentity: true, VersionTemplateFileID: 1}

	var pageWith func(string, CreateRequest, ...*models.Episode) CreatePage
	pageOf := func(device string, eps ...*models.Episode) CreatePage {
		t.Helper()
		return pageWith(device, req, eps...)
	}
	pageWith = func(device string, r CreateRequest, eps ...*models.Episode) CreatePage {
		t.Helper()
		pager.rows = eps
		r.DeviceID = device
		files.single = own // the version template the request names
		if r.VersionTemplateFileID == multi.ID {
			files.single = multi
		}
		page, err := svc.CreateSeriesPage(t.Context(), 1, r, new(1), nil, 10, catalog.AccessFilter{})
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	ep1, ep2, ep3 := pager.rows[0], pager.rows[1], pager.rows[2]
	registered := func(page CreatePage) map[string]int {
		got := map[string]int{}
		for _, d := range page.Items {
			got[d.EpisodeID] = d.MediaFileID
		}
		return got
	}

	// Following episode 1's own file, each episode with a file of its own gets
	// one entry, and episode 3 is skipped rather than refusing the page. One
	// episode per page reaches the same result: episode 3's page finds the
	// entry the first page registered for episode 1.
	whole := pageOf("device", ep1, ep2, ep3)
	if got := registered(whole); !reflect.DeepEqual(got, map[string]int{"ep-1": 1, "ep-2": 2}) || len(whole.Skipped) != 1 || whole.Skipped[0].EpisodeID != "ep-3" {
		t.Fatalf("season %v skipped %+v", got, whole.Skipped)
	}
	pageOf("paged", ep1)
	pageOf("paged", ep2)
	if third := pageOf("paged", ep3); len(third.Items) != 0 || len(third.Skipped) != 1 || third.Skipped[0].Reason != SkipReasonMultiEpisodeConflict {
		t.Fatalf("episode 3 page %+v skipped %+v", third.Items, third.Skipped)
	}
	// Without an entry for episode 1, as when a monitor leaves out a watched
	// episode 1, episode 3 registers the range under episode 1.
	if alone := pageOf("fresh", ep3); !reflect.DeepEqual(registered(alone), map[string]int{"ep-1": 3}) {
		t.Fatalf("episode 3 alone %+v skipped %+v", alone.Items, alone.Skipped)
	}

	// A page that keeps existing entries follows episode 1's entry, not its
	// pick. Episode 1 kept on the range serves episode 3 through that one
	// entry; kept on its own file, episode 3 is skipped.
	keep := req
	keep.ExpectedEntries = map[string]ManagedCreateExpectation{}
	rangeFirst := req
	rangeFirst.VersionTemplateFileID = 3
	pageWith("kept-range", rangeFirst, ep1, ep2, ep3)
	kept := pageWith("kept-range", keep, ep1, ep2, ep3)
	if got := registered(kept); !reflect.DeepEqual(got, map[string]int{"ep-1": 3, "ep-2": 2}) || len(kept.Items) != 2 || len(kept.Skipped) != 0 {
		t.Fatalf("kept range %+v skipped %+v", kept.Items, kept.Skipped)
	}
	pageWith("kept-own", req, ep1, ep2, ep3)
	keepRange := rangeFirst
	keepRange.ExpectedEntries = map[string]ManagedCreateExpectation{}
	ownKept := pageWith("kept-own", keepRange, ep1, ep2, ep3)
	if got := registered(ownKept); !reflect.DeepEqual(got, map[string]int{"ep-1": 1, "ep-2": 2}) || len(ownKept.Skipped) != 1 || ownKept.Skipped[0].EpisodeID != "ep-3" {
		t.Fatalf("kept own file %+v skipped %+v", ownKept.Items, ownKept.Skipped)
	}
}

func TestSeriesPageRejectsAVersionTemplateFromAnotherSeries(t *testing.T) {
	for name, template := range map[string]*models.MediaFile{
		"other series":       {ID: 11, ContentID: "other", EpisodeID: "ep-x"},
		"movie file":         {ID: 11, ContentID: "series"},
		"unreadable library": {ID: 11, ContentID: "series", EpisodeID: "ep-1", MediaFolderID: 2},
	} {
		t.Run(name, func(t *testing.T) {
			pager := &createEpisodePager{rows: []*models.Episode{{ContentID: "ep-1", SeasonNumber: 1, EpisodeNumber: 1}}}
			files := &createFileResolver{rows: map[string][]*models.MediaFile{"ep-1": {{ID: 10, ContentID: "series", EpisodeID: "ep-1"}}}, single: template}
			svc := NewService(nil, nil, nil, files, createItemResolver{}, pager, fakeUserRepo{&models.User{ID: 1, DownloadAllowed: new(true)}}, &syncAccess{}, nil, &config.DownloadConfig{Enabled: true})
			req := CreateRequest{ContentID: "series", ProfileID: "profile", BatchID: "intent", StrictIdentity: true, VersionTemplateFileID: 11}
			filter := catalog.AccessFilter{AllowedLibraryIDs: []int{0, 1}}
			if _, err := svc.CreateSeriesPage(t.Context(), 1, req, new(1), nil, 10, filter); !errors.Is(err, catalog.ErrItemNotFound) {
				t.Fatalf("err = %v, want ErrItemNotFound", err)
			}
		})
	}
}

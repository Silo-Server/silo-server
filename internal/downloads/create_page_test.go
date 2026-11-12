package downloads

import (
	"context"
	"errors"
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
func (f *createFileResolver) GetByEpisodeID(_ context.Context, id string) ([]*models.MediaFile, error) {
	return f.rows[id], nil
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

func TestSingleDownloadReachesTheEntryASeasonKeyedOnALaterEpisodePostgres(t *testing.T) {
	repo := NewRepository(subscriptionMutationTestRepo(t).pool)
	_, err := repo.pool.Exec(t.Context(), `CREATE UNIQUE INDEX managed_identity ON downloads(user_id,profile_id,device_id,content_id,COALESCE(episode_id,'')) WHERE device_id IS NOT NULL;
 CREATE TABLE user_devices(user_id integer,profile_id text,device_id text,device_name text,device_platform text,last_seen_at timestamptz,PRIMARY KEY(user_id,profile_id,device_id))`)
	if err != nil {
		t.Fatal(err)
	}
	// Episode 1 has its own file and shares a double-episode file with
	// episode 2, which is episode 2's only file.
	own := &models.MediaFile{ID: 1, ContentID: "series", EpisodeID: "ep-1", Resolution: "720p", FileSize: 10}
	double := &models.MediaFile{ID: 3, ContentID: "series", EpisodeID: "ep-1", Resolution: "1080p", MultiEpisodeStart: 1, MultiEpisodeEnd: 2, FileSize: 20}
	pager := &createEpisodePager{rows: []*models.Episode{{ContentID: "ep-1", SeasonNumber: 1, EpisodeNumber: 1}, {ContentID: "ep-2", SeasonNumber: 1, EpisodeNumber: 2}}}
	files := &createFileResolver{rows: map[string][]*models.MediaFile{"ep-1": {own, double}, "ep-2": {double}}, single: own}
	svc := NewService(repo, nil, nil, files, createItemResolver{}, pager, fakeUserRepo{&models.User{ID: 1, DownloadAllowed: new(true)}}, &syncAccess{}, nil, &config.DownloadConfig{Enabled: true})
	req := CreateRequest{ContentID: "series", ProfileID: "profile", DeviceID: "device", BatchID: "season", StrictIdentity: true, VersionTemplateFileID: 1}
	page, err := svc.CreateSeriesPage(t.Context(), 1, req, new(1), nil, 10, catalog.AccessFilter{})
	if err != nil || len(page.Items) != 2 || page.Items[1].EpisodeID != "ep-2" || page.Items[1].MediaFileID != 3 {
		t.Fatalf("season %+v %v", page.Items, err)
	}
	entry := page.Items[1]

	// Downloading episode 2 again by itself finds the entry the season made
	// for it, and leaves episode 1's own file alone.
	single, err := svc.Create(t.Context(), 1, CreateRequest{ContentID: "series", EpisodeID: "ep-2", ProfileID: "profile", DeviceID: "device", StrictIdentity: true, ExpectedDownloadID: entry.ID, ExpectedRevision: new(entry.Revision)}, catalog.AccessFilter{})
	if err != nil || single.ID != entry.ID || single.EpisodeID != "ep-2" || single.MediaFileID != 3 {
		t.Fatalf("single %+v %v", single, err)
	}
	first, err := repo.GetManagedEntry(t.Context(), 1, "profile", "device", "series", "ep-1")
	if err != nil || first.MediaFileID != 1 {
		t.Fatalf("episode 1 entry %+v %v", first, err)
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

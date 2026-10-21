package downloads

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// monitorFileResolver gives every episode one 10-byte file backed by the
// fixture's real media_files row, so managed rows satisfy their FK.
type monitorFileResolver struct {
	FileResolver
	fileID   int
	seriesID string
}

func (f *monitorFileResolver) ListByEpisodeIDs(_ context.Context, ids []string) (map[string][]*models.MediaFile, error) {
	out := make(map[string][]*models.MediaFile, len(ids))
	for _, id := range ids {
		out[id] = []*models.MediaFile{f.file(id)}
	}
	return out, nil
}

func (f *monitorFileResolver) GetByEpisodeID(_ context.Context, id string) ([]*models.MediaFile, error) {
	return []*models.MediaFile{f.file(id)}, nil
}

func (f *monitorFileResolver) file(episodeID string) *models.MediaFile {
	return &models.MediaFile{ID: f.fileID, ContentID: f.seriesID, EpisodeID: episodeID, FileSize: 10}
}

type monitorFixture struct {
	managedFixture
	svc      *Service
	subRepo  *SubscriptionRepository
	pager    *syncEpisodePager
	monitor  *Subscription
	seriesID string
	episodes []string
}

// seedMonitorFixture seeds a real-schema account whose device monitors a
// three-episode series (10 bytes per episode) with the given options.
func seedMonitorFixture(t *testing.T, maxStorageBytes int64, deleteWatched bool) monitorFixture {
	t.Helper()
	f := seedManagedFixture(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM user_watch_progress WHERE user_id = $1`, f.userID)
	})
	seriesID := f.contentID
	fx := monitorFixture{managedFixture: f, seriesID: seriesID, pager: &syncEpisodePager{}}
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("%s-ep-%d", seriesID, i)
		fx.episodes = append(fx.episodes, id)
		fx.pager.rows = append(fx.pager.rows, &models.Episode{ContentID: id, SeriesID: seriesID, SeasonNumber: 1, EpisodeNumber: i})
	}
	files := &monitorFileResolver{fileID: f.fileID, seriesID: seriesID}
	user := fakeUserRepo{&models.User{ID: f.userID, DownloadAllowed: new(true)}}
	fx.svc = NewService(f.repo, nil, nil, files, createItemResolver{}, fx.pager, user, &syncAccess{}, nil, &config.DownloadConfig{Enabled: true})
	fx.subRepo = NewSubscriptionRepository(f.pool)
	fx.svc.SetSubscriptions(fx.subRepo)
	fx.svc.SetProgressStores(pgstore.NewPostgresProvider(f.pool))
	monitor, err := fx.subRepo.CreateOrGet(ctx, &Subscription{
		ID: "monitor-" + seriesID, UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA, SeriesID: seriesID,
		Mode: SubModeAll, DeleteWatched: deleteWatched, MaxStorageBytes: maxStorageBytes,
	})
	if err != nil {
		t.Fatalf("create monitor: %v", err)
	}
	fx.monitor = monitor
	return fx
}

func (fx monitorFixture) sync(t *testing.T) int {
	t.Helper()
	n, err := fx.svc.SyncSubscriptions(context.Background(), fx.userID, fx.profileA, fx.deviceA, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return n
}

func (fx monitorFixture) syncPage(t *testing.T) int {
	t.Helper()
	page, err := fx.svc.SyncSubscriptionPage(context.Background(), fx.userID, fx.profileA, fx.deviceA, fx.monitor.ID, nil, 100, catalog.AccessFilter{}, func(*Subscription) error { return nil })
	if err != nil {
		t.Fatalf("sync page: %v", err)
	}
	return page.Registered
}

func (fx monitorFixture) entry(t *testing.T, episodeID string) *Download {
	t.Helper()
	row, err := fx.repo.GetManagedEntry(context.Background(), fx.userID, fx.profileA, fx.deviceA, fx.seriesID, episodeID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		t.Fatalf("get managed entry %s: %v", episodeID, err)
	}
	return row
}

func (fx monitorFixture) deleteEpisode(t *testing.T, episodeID string) {
	t.Helper()
	row := fx.entry(t, episodeID)
	if row == nil {
		t.Fatalf("episode %s is not registered", episodeID)
		return
	}
	if err := fx.svc.Delete(context.Background(), fx.userID, fx.profileA, fx.deviceA, row.ID); err != nil {
		t.Fatalf("delete %s: %v", episodeID, err)
	}
}

// TestMonitorSyncKeepsDeletedEpisodesDeletedPostgres is the guardrail for
// sync -> delete -> sync: a deleted episode must not be registered again.
func TestMonitorSyncKeepsDeletedEpisodesDeletedPostgres(t *testing.T) {
	t.Run("uncapped", func(t *testing.T) {
		fx := seedMonitorFixture(t, 0, false)
		if n := fx.sync(t); n != 3 {
			t.Fatalf("first sync registered %d, want 3", n)
		}
		fx.deleteEpisode(t, fx.episodes[0])
		n := fx.sync(t)
		t.Logf("uncapped: registered after delete = %d", n)
		if n != 0 || fx.entry(t, fx.episodes[0]) != nil {
			t.Fatalf("sync after delete registered %d (deleted episode back: %v), want 0", n, fx.entry(t, fx.episodes[0]) != nil)
		}
		// The native paged sync applies the same exclusion.
		if n := fx.syncPage(t); n != 0 {
			t.Fatalf("paged sync after delete registered %d, want 0", n)
		}
	})

	t.Run("capped", func(t *testing.T) {
		// 20 bytes admits two of the three 10-byte episodes.
		fx := seedMonitorFixture(t, 20, false)
		if n := fx.sync(t); n != 2 {
			t.Fatalf("first sync registered %d, want 2", n)
		}
		fx.deleteEpisode(t, fx.episodes[0])
		n := fx.sync(t)
		back := fx.entry(t, fx.episodes[0]) != nil
		t.Logf("capped: registered after delete = %d, deleted episode re-registered = %v", n, back)
		if back {
			t.Fatal("capped monitor spent its budget re-registering the deleted episode")
		}
		// The freed budget goes to the next episode the device has not had.
		if n != 1 || fx.entry(t, fx.episodes[2]) == nil {
			t.Fatalf("sync after delete registered %d, want 1 (episode 3)", n)
		}
	})
}

// TestMonitorSyncSkipsWatchedEpisodesPostgres covers delete_watched monitors:
// an episode the profile has completed is not registered, so the client never
// downloads what its retention pass would delete.
func TestMonitorSyncSkipsWatchedEpisodesPostgres(t *testing.T) {
	ctx := context.Background()
	for _, deleteWatched := range []bool{true, false} {
		t.Run(fmt.Sprintf("delete_watched=%v", deleteWatched), func(t *testing.T) {
			fx := seedMonitorFixture(t, 0, deleteWatched)
			store, err := pgstore.NewPostgresProvider(fx.pool).ForUser(ctx, fx.userID)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SetProgressAt(ctx, fx.profileA, fx.episodes[1], 1200, 1200, true, time.Now()); err != nil {
				t.Fatalf("seed completed progress: %v", err)
			}
			// Another profile's progress on the same account must not count.
			if err := store.SetProgressAt(ctx, fx.profileB, fx.episodes[2], 1200, 1200, true, time.Now()); err != nil {
				t.Fatalf("seed other profile progress: %v", err)
			}
			n := fx.sync(t)
			watchedRegistered := fx.entry(t, fx.episodes[1]) != nil
			t.Logf("delete_watched=%v: registered %d, watched episode registered = %v", deleteWatched, n, watchedRegistered)
			if deleteWatched && (n != 2 || watchedRegistered) {
				t.Fatalf("registered %d (watched episode: %v), want 2 without the watched episode", n, watchedRegistered)
			}
			if !deleteWatched && n != 3 {
				t.Fatalf("a monitor without delete_watched registered %d, want 3", n)
			}
		})
	}
}

type chunkedProgressStore struct {
	userstore.UserStore
	completed map[string]bool
	calls     []int
	err       error
}

func (s *chunkedProgressStore) ListProgressByMediaItems(_ context.Context, _ string, ids []string) (map[string]userstore.WatchProgress, error) {
	s.calls = append(s.calls, len(ids))
	if s.err != nil {
		return nil, s.err
	}
	out := map[string]userstore.WatchProgress{}
	for _, id := range ids {
		out[id] = userstore.WatchProgress{MediaItemID: id, Completed: s.completed[id]}
	}
	return out, nil
}

type chunkedProgressStores struct{ store *chunkedProgressStore }

func (p chunkedProgressStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

// TestWatchedFilterFailsOpen: a progress-store failure registers the monitor's
// episodes without the watched filter instead of failing the sync, so new
// episodes keep arriving.
func TestWatchedFilterFailsOpen(t *testing.T) {
	store := &chunkedProgressStore{completed: map[string]bool{"ep-1": true}, err: errors.New("progress store down")}
	svc := &Service{progressStores: chunkedProgressStores{store}, fileRepo: &monitorFileResolver{fileID: 1, seriesID: "series"}}
	episodes := []*models.Episode{{ContentID: "ep-0"}, {ContentID: "ep-1"}, {ContentID: "ep-2"}}
	items, err := svc.subscriptionEpisodeItems(t.Context(), &Subscription{UserID: 1, ProfileID: "profile", SeriesID: "series", Mode: SubModeAll, DeleteWatched: true}, episodes, false, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("sync items with the progress store down: %v", err)
	}
	if len(store.calls) != 1 || len(items) != 3 {
		t.Fatalf("lookups = %v, items = %d; want one failed lookup and all 3 episodes", store.calls, len(items))
	}
}

// TestConcurrentPreparedMonitorSyncsShareFreeSlotsDB pins that monitors of one
// account syncing prepared episodes at the same time share the account's free
// concurrent download slots instead of each claiming all of them.
func TestConcurrentPreparedMonitorSyncsShareFreeSlotsDB(t *testing.T) {
	ctx := context.Background()
	fx := seedMonitorFixture(t, 0, false)
	t.Cleanup(func() {
		_, _ = fx.pool.Exec(ctx, `DELETE FROM download_artifacts WHERE media_file_id = $1`, fx.fileID)
	})
	second, err := fx.subRepo.CreateOrGet(ctx, &Subscription{
		ID: "monitor-b-" + fx.seriesID, UserID: fx.userID, ProfileID: fx.profileB, DeviceID: fx.deviceB, SeriesID: fx.seriesID, Mode: SubModeAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	fx.svc.limiter = NewQuantityLimiter(fx.repo, 2, 0, 0)
	fx.svc.SetArtifactManager(NewArtifactManager(
		NewArtifactRepository(fx.pool), fx.repo, nil, stubQuotaPreparer{}, "monitor-slots-test",
		func() *config.Config { return nil }, nil,
	))
	prepared := QualityDecision{
		RequestedQuality: Quality5Mbps, EffectiveQuality: Quality5Mbps, DeliveryFormat: FormatTranscode,
		TargetBitrateKbps: 5000, RequiresArtifact: true,
		PrepareTarget: playback.PrepareTarget{Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", TargetBitrateKbps: 5000},
	}
	plan := monitorPlan{decisions: map[ManagedEntryKey]QualityDecision{}, prepared: true}
	for _, ep := range fx.episodes {
		it := managedItem{file: &models.MediaFile{ID: fx.fileID, ContentID: fx.seriesID, FileSize: 10}, contentID: fx.seriesID, episodeID: ep}
		plan.items = append(plan.items, it)
		plan.decisions[managedItemKey(it)] = prepared
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, sub := range []*Subscription{fx.monitor, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = fx.svc.registerMonitorPlan(ctx, sub, plan, func(register func(*Subscription, pgx.Tx) error) error {
				return fx.subRepo.WithLocked(ctx, sub.UserID, sub.ProfileID, sub.DeviceID, sub.ID, func(locked *Subscription, tx pgx.Tx) error {
					return register(locked, tx)
				})
			})
		}()
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var preparing int
	if err := fx.pool.QueryRow(ctx, `SELECT count(*) FROM downloads WHERE user_id = $1 AND status = 'preparing'`, fx.userID).Scan(&preparing); err != nil {
		t.Fatal(err)
	}
	if preparing != 2 {
		t.Fatalf("preparing downloads = %d, want the 2 free slots shared between both monitors", preparing)
	}
}

// TestPreparedMonitorSyncQueuesNothingForAStaleMonitorDB pins that a monitor
// edited or paused after a sync planned its episodes queues no encode jobs for
// that sync: no download row would link to them.
func TestPreparedMonitorSyncQueuesNothingForAStaleMonitorDB(t *testing.T) {
	ctx := context.Background()
	fx := seedMonitorFixture(t, 0, false)
	t.Cleanup(func() {
		_, _ = fx.pool.Exec(ctx, `DELETE FROM download_artifacts WHERE media_file_id = $1`, fx.fileID)
	})
	fx.svc.SetArtifactManager(NewArtifactManager(
		NewArtifactRepository(fx.pool), fx.repo, nil, stubQuotaPreparer{}, "monitor-stale-test",
		func() *config.Config { return nil }, nil,
	))
	prepared := QualityDecision{
		RequestedQuality: Quality5Mbps, EffectiveQuality: Quality5Mbps, DeliveryFormat: FormatTranscode,
		TargetBitrateKbps: 5000, RequiresArtifact: true,
		PrepareTarget: playback.PrepareTarget{Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", TargetBitrateKbps: 5000},
	}
	it := managedItem{file: &models.MediaFile{ID: fx.fileID, ContentID: fx.seriesID, FileSize: 10}, contentID: fx.seriesID, episodeID: fx.episodes[0]}
	plan := monitorPlan{items: []managedItem{it}, decisions: map[ManagedEntryKey]QualityDecision{managedItemKey(it): prepared}, prepared: true}
	if _, err := fx.subRepo.Mutate(ctx, fx.userID, fx.profileA, fx.deviceA, fx.monitor.ID, false, func(row *Subscription) error { row.Active = false; return nil }); err != nil {
		t.Fatal(err)
	}
	rows, err := fx.svc.registerMonitorPlan(ctx, fx.monitor, plan, func(register func(*Subscription, pgx.Tx) error) error {
		return fx.subRepo.WithLocked(ctx, fx.userID, fx.profileA, fx.deviceA, fx.monitor.ID, func(locked *Subscription, tx pgx.Tx) error {
			if !locked.Active || !locked.UpdatedAt.Equal(fx.monitor.UpdatedAt) {
				return nil
			}
			return register(locked, tx)
		})
	})
	if err != nil || len(rows) != 0 {
		t.Fatalf("stale monitor registered %+v %v", rows, err)
	}
	var jobs int
	if err := fx.pool.QueryRow(ctx, `SELECT count(*) FROM download_artifacts WHERE media_file_id = $1`, fx.fileID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("stale monitor queued %d encode jobs", jobs)
	}
}

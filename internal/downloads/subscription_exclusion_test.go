package downloads

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
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

func (fx monitorFixture) exclusions(t *testing.T) int {
	t.Helper()
	var n int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM download_subscription_exclusions WHERE subscription_id = $1`, fx.monitor.ID,
	).Scan(&n); err != nil {
		t.Fatalf("count exclusions: %v", err)
	}
	return n
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

// TestMonitorExclusionLifecyclePostgres: an explicit download of a deleted
// episode clears its exclusion, deleting the monitor drops its exclusions, and
// a new monitor starts from a clean slate.
func TestMonitorExclusionLifecyclePostgres(t *testing.T) {
	ctx := context.Background()
	fx := seedMonitorFixture(t, 0, false)
	if n := fx.sync(t); n != 3 {
		t.Fatalf("first sync registered %d, want 3", n)
	}
	fx.deleteEpisode(t, fx.episodes[0])
	if n := fx.exclusions(t); n != 1 {
		t.Fatalf("exclusions after delete = %d, want 1", n)
	}

	// The user downloads the deleted episode again on purpose.
	row, err := fx.svc.Create(ctx, fx.userID, CreateRequest{ContentID: fx.seriesID, EpisodeID: fx.episodes[0], ProfileID: fx.profileA, DeviceID: fx.deviceA}, catalog.AccessFilter{})
	if err != nil || row == nil || row.EpisodeID != fx.episodes[0] {
		t.Fatalf("manual re-download: %+v %v", row, err)
	}
	if n := fx.exclusions(t); n != 0 {
		t.Fatalf("exclusions after manual re-download = %d, want 0", n)
	}
	if n := fx.sync(t); n != 0 {
		t.Fatalf("sync after manual re-download registered %d, want 0", n)
	}

	// Deleting it again is remembered again.
	fx.deleteEpisode(t, fx.episodes[0])
	if n := fx.sync(t); n != 0 {
		t.Fatalf("sync after second delete registered %d, want 0", n)
	}

	// Downloading the whole series explicitly also clears it.
	if _, _, _, err := fx.svc.CreateSeries(ctx, fx.userID, CreateRequest{ContentID: fx.seriesID, ProfileID: fx.profileA, DeviceID: fx.deviceA}, catalog.AccessFilter{}); err != nil {
		t.Fatalf("series download: %v", err)
	}
	if n := fx.exclusions(t); n != 0 || fx.entry(t, fx.episodes[0]) == nil {
		t.Fatalf("exclusions after series download = %d, want 0 with the episode registered", n)
	}
	fx.deleteEpisode(t, fx.episodes[0])

	// Stopping the monitor drops its exclusions; a new monitor for the series
	// registers the episode again.
	if err := fx.svc.DeleteSubscriptionMonitor(ctx, fx.userID, fx.profileA, fx.deviceA, fx.monitor.ID, func(*Subscription) error { return nil }); err != nil {
		t.Fatalf("delete monitor: %v", err)
	}
	if n := fx.exclusions(t); n != 0 {
		t.Fatalf("exclusions after monitor delete = %d, want 0", n)
	}
	fresh, err := fx.subRepo.CreateOrGet(ctx, &Subscription{ID: "monitor-again-" + fx.seriesID, UserID: fx.userID, ProfileID: fx.profileA, DeviceID: fx.deviceA, SeriesID: fx.seriesID, Mode: SubModeAll})
	if err != nil {
		t.Fatal(err)
	}
	fx.monitor = fresh
	if n := fx.sync(t); n != 1 || fx.entry(t, fx.episodes[0]) == nil {
		t.Fatalf("new monitor registered %d, want the deleted episode back", n)
	}
}

// TestMonitorCreateForgetsDeletionsPostgres: a create for a series the device
// already monitors (for example a monitor an earlier app install left behind)
// returns that monitor and forgets the episodes deleted under it, through the
// native create and the bridge's re-monitor alike.
func TestMonitorCreateForgetsDeletionsPostgres(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		create func(*SubscriptionRepository, *Subscription) (*Subscription, error)
	}{
		{"native", func(r *SubscriptionRepository, s *Subscription) (*Subscription, error) { return r.CreateOrGet(ctx, s) }},
		{"bridge", func(r *SubscriptionRepository, s *Subscription) (*Subscription, error) { return r.Upsert(ctx, s) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := seedMonitorFixture(t, 0, false)
			if n := fx.sync(t); n != 3 {
				t.Fatalf("first sync registered %d, want 3", n)
			}
			fx.deleteEpisode(t, fx.episodes[0])
			again, err := tc.create(fx.subRepo, &Subscription{ID: "recreate-" + fx.seriesID, UserID: fx.userID, ProfileID: fx.profileA, DeviceID: fx.deviceA, SeriesID: fx.seriesID, Mode: SubModeAll})
			if err != nil || again.ID != fx.monitor.ID {
				t.Fatalf("re-create = %+v, %v; want the existing monitor %s", again, err, fx.monitor.ID)
			}
			if n := fx.exclusions(t); n != 0 {
				t.Fatalf("exclusions after re-create = %d, want 0", n)
			}
			if n := fx.sync(t); n != 1 || fx.entry(t, fx.episodes[0]) == nil {
				t.Fatalf("sync after re-create registered %d, want the deleted episode back", n)
			}
		})
	}
}

// TestManagedDeleteOutsideMonitorPostgres: deleting a movie, an episode of a
// series this device does not monitor, or another device's copy of a
// monitored episode records nothing.
func TestManagedDeleteOutsideMonitorPostgres(t *testing.T) {
	ctx := context.Background()
	fx := seedMonitorFixture(t, 0, false)
	movie := &Download{ID: "movie-" + fx.seriesID, UserID: fx.userID, ProfileID: fx.profileA, DeviceID: fx.deviceA, MediaFileID: fx.fileID, ContentID: "movie-" + fx.seriesID, Kind: KindQueued, Status: StatusReady, Format: FormatOriginal, Quality: QualityOriginal, EffectiveQuality: QualityOriginal, Revision: 1, FileSize: 10, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	other := *movie
	other.ID, other.ContentID, other.EpisodeID = "other-"+fx.seriesID, "other-series-"+fx.seriesID, "other-ep-"+fx.seriesID
	for _, d := range []*Download{movie, &other} {
		if err := fx.repo.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		if err := fx.svc.Delete(ctx, fx.userID, fx.profileA, fx.deviceA, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := fx.pool.QueryRow(ctx, `SELECT count(*) FROM download_subscription_exclusions x JOIN download_subscriptions s ON s.id = x.subscription_id WHERE s.user_id = $1`, fx.userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("exclusions = %d, want 0", n)
	}
	// Another device deleting the same episode leaves this device's monitor
	// alone.
	onB := *movie
	onB.ID, onB.ProfileID, onB.DeviceID, onB.ContentID, onB.EpisodeID = "b-"+fx.seriesID, fx.profileB, fx.deviceB, fx.seriesID, fx.episodes[0]
	if err := fx.repo.Create(ctx, &onB); err != nil {
		t.Fatal(err)
	}
	if err := fx.svc.Delete(ctx, fx.userID, fx.profileB, fx.deviceB, onB.ID); err != nil {
		t.Fatal(err)
	}
	if n := fx.exclusions(t); n != 0 {
		t.Fatalf("exclusions after another device's delete = %d, want 0", n)
	}
	if n := fx.sync(t); n != 3 {
		t.Fatalf("sync registered %d, want 3", n)
	}
}

// TestMonitorRetentionLoopPostgres replays the iOS monitoring run for a
// delete_watched monitor: sync, then the client's retention pass deletes the
// completed download. Across runs the watched episode must not come back.
func TestMonitorRetentionLoopPostgres(t *testing.T) {
	ctx := context.Background()
	fx := seedMonitorFixture(t, 0, true)
	if n := fx.sync(t); n != 3 {
		t.Fatalf("first sync registered %d, want 3", n)
	}
	store, err := pgstore.NewPostgresProvider(fx.pool).ForUser(ctx, fx.userID)
	if err != nil {
		t.Fatal(err)
	}
	// The profile finishes episode 1 after it was downloaded.
	if err := store.SetProgressAt(ctx, fx.profileA, fx.episodes[0], 1200, 1200, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	const runs = 5
	redownloads := 0
	for range runs {
		fx.sync(t)
		if fx.entry(t, fx.episodes[0]) == nil {
			continue
		}
		redownloads++
		fx.deleteEpisode(t, fx.episodes[0])
	}
	// The first run deletes the copy registered before the episode was watched.
	redownloads--
	t.Logf("watched episode re-registered in %d of %d monitoring runs", redownloads, runs)
	if redownloads != 0 {
		t.Fatalf("watched episode re-registered in %d runs, want 0", redownloads)
	}
}

// TestManagedDeleteWaitsForMonitorSyncPostgres: while a sync holds the monitor
// lock, a delete of one of its episodes waits and cannot commit, so the locked
// sync still sees the episode as held; once the delete commits, the next sync
// sees its exclusion.
func TestManagedDeleteWaitsForMonitorSyncPostgres(t *testing.T) {
	ctx := context.Background()
	fx := seedMonitorFixture(t, 0, false)
	if n := fx.sync(t); n != 3 {
		t.Fatalf("first sync registered %d, want 3", n)
	}
	row := fx.entry(t, fx.episodes[0])
	done := make(chan error, 1)
	err := fx.subRepo.WithLocked(ctx, fx.userID, fx.profileA, fx.deviceA, fx.monitor.ID, func(locked *Subscription, tx pgx.Tx) error {
		go func() { done <- fx.svc.Delete(ctx, fx.userID, fx.profileA, fx.deviceA, row.ID) }()
		if err := fx.waitForLockWait(ctx, "%INSERT INTO download_subscription_exclusions%"); err != nil {
			return fmt.Errorf("delete never waited on the monitor lock: %w", err)
		}
		key := ManagedEntryKey{ContentID: fx.seriesID, EpisodeID: fx.episodes[0]}
		candidates, err := managedRegistryStore{tx}.MonitorEntriesToRegister(ctx, locked, []ManagedEntryKey{key})
		if err != nil {
			return err
		}
		if candidates[key] {
			return errors.New("locked sync lost the row before the delete committed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n := fx.exclusions(t); n != 1 {
		t.Fatalf("exclusions = %d, want 1", n)
	}
	if n := fx.sync(t); n != 0 {
		t.Fatalf("sync after delete registered %d, want 0", n)
	}
}

// waitForLockWait polls until a backend running a query that matches pattern
// (a LIKE pattern) waits on a lock, checking every few milliseconds so a wait
// that never appears does not flood the shared test database.
func (fx monitorFixture) waitForLockWait(ctx context.Context, pattern string) error {
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err := fx.pool.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE $1)`, pattern).Scan(&waiting); err != nil {
			return err
		}
		if waiting {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-tick.C:
		}
	}
}

// TestManagedDeleteRacingMonitorRemovalPostgres: a managed delete that waits
// on the monitor lock while the monitor (or its whole device) is deleted still
// deletes the row. It records no exclusion for the vanished monitor instead of
// failing the exclusion's foreign key.
func TestManagedDeleteRacingMonitorRemovalPostgres(t *testing.T) {
	ctx := context.Background()

	t.Run("monitor deleted by its lock holder", func(t *testing.T) {
		fx := seedMonitorFixture(t, 0, false)
		if n := fx.sync(t); n != 3 {
			t.Fatalf("first sync registered %d, want 3", n)
		}
		row := fx.entry(t, fx.episodes[0])
		done := make(chan error, 1)
		// Mutate(remove) locks the monitor FOR UPDATE and deletes it, as the
		// stop-monitoring endpoints do.
		_, err := fx.subRepo.Mutate(ctx, fx.userID, fx.profileA, fx.deviceA, fx.monitor.ID, true, func(*Subscription) error {
			go func() { done <- fx.svc.Delete(ctx, fx.userID, fx.profileA, fx.deviceA, row.ID) }()
			return fx.waitForLockWait(ctx, "%INSERT INTO download_subscription_exclusions%")
		})
		if err != nil {
			t.Fatalf("delete monitor: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatalf("managed delete racing the monitor delete: %v", err)
		}
		if fx.entry(t, fx.episodes[0]) != nil {
			t.Fatal("managed row survived its delete")
		}
		if n := fx.exclusions(t); n != 0 {
			t.Fatalf("exclusions = %d, want 0", n)
		}
	})

	t.Run("device deleted", func(t *testing.T) {
		fx := seedMonitorFixture(t, 0, false)
		if n := fx.sync(t); n != 3 {
			t.Fatalf("first sync registered %d, want 3", n)
		}
		row := fx.entry(t, fx.episodes[0])
		deleted := make(chan error, 1)
		forgotten := make(chan error, 1)
		// A sync holds the monitor lock; the managed delete waits on it, and a
		// device delete then waits behind the managed delete's row.
		err := fx.subRepo.WithLocked(ctx, fx.userID, fx.profileA, fx.deviceA, fx.monitor.ID, func(*Subscription, pgx.Tx) error {
			go func() { deleted <- fx.svc.Delete(ctx, fx.userID, fx.profileA, fx.deviceA, row.ID) }()
			if err := fx.waitForLockWait(ctx, "%INSERT INTO download_subscription_exclusions%"); err != nil {
				return err
			}
			go func() {
				_, err := fx.pool.Exec(ctx, `DELETE FROM user_devices WHERE user_id = $1 AND profile_id = $2 AND device_id = $3`, fx.userID, fx.profileA, fx.deviceA)
				forgotten <- err
			}()
			return fx.waitForLockWait(ctx, "%DELETE FROM user_devices%")
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := <-deleted; err != nil {
			t.Fatalf("managed delete racing the device delete: %v", err)
		}
		if err := <-forgotten; err != nil {
			t.Fatalf("device delete: %v", err)
		}
		var left int
		if err := fx.pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM downloads WHERE user_id = $1 AND device_id = $2) +
			(SELECT count(*) FROM download_subscriptions WHERE user_id = $1 AND device_id = $2)`, fx.userID, fx.deviceA).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 0 || fx.exclusions(t) != 0 {
			t.Fatalf("device rows left = %d, exclusions = %d; want both 0", left, fx.exclusions(t))
		}
	})
}

// TestExplicitDownloadRacingDeletePostgres: an explicit download of a deleted
// episode registers the row, then clears the episode's exclusion in a separate
// statement. A delete of the new row that lands in between, whether committed
// before the clear or run but not yet committed, keeps its exclusion, so the
// next sync does not bring the episode back. The clear never waits on another
// transaction's row lock.
func TestExplicitDownloadRacingDeletePostgres(t *testing.T) {
	ctx := context.Background()
	// registered leaves the fixture where an explicit re-download stands
	// before its clear: the episode is registered again and the earlier
	// delete's exclusion is still recorded.
	registered := func(t *testing.T) (monitorFixture, *Download) {
		t.Helper()
		fx := seedMonitorFixture(t, 0, false)
		if n := fx.sync(t); n != 3 {
			t.Fatalf("first sync registered %d, want 3", n)
		}
		fx.deleteEpisode(t, fx.episodes[0])
		files := &monitorFileResolver{fileID: fx.fileID, seriesID: fx.seriesID}
		req := CreateRequest{ContentID: fx.seriesID, EpisodeID: fx.episodes[0], ProfileID: fx.profileA, DeviceID: fx.deviceA}
		item := managedItem{file: files.file(fx.episodes[0]), contentID: fx.seriesID, episodeID: fx.episodes[0]}
		rows, err := fx.svc.ensureManaged(ctx, fx.userID, req, []managedItem{item}, originalDecision(), "")
		if err != nil {
			t.Fatalf("explicit re-download: %v", err)
		}
		if n := fx.exclusions(t); n != 1 {
			t.Fatalf("exclusions before the clear = %d, want 1", n)
		}
		return fx, rows[0]
	}
	// forget runs the clear with a deadline, so a clear that waits on the
	// test's open transaction fails instead of hanging the test.
	forget := func(fx monitorFixture) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return fx.repo.ClearMonitorExclusions(ctx, fx.userID, fx.profileA, fx.deviceA, fx.seriesID, fx.episodes[:1])
	}
	stillDeleted := func(t *testing.T, fx monitorFixture) {
		t.Helper()
		if fx.entry(t, fx.episodes[0]) != nil {
			t.Fatal("managed row survived its delete")
		}
		if n := fx.exclusions(t); n != 1 {
			t.Fatalf("exclusions after the clear = %d, want the delete's 1", n)
		}
		if n := fx.sync(t); n != 0 {
			t.Fatalf("sync after the delete registered %d, want 0", n)
		}
	}

	t.Run("delete committed first", func(t *testing.T) {
		fx, row := registered(t)
		if err := fx.svc.Delete(ctx, fx.userID, fx.profileA, fx.deviceA, row.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if err := forget(fx); err != nil {
			t.Fatalf("clear: %v", err)
		}
		stillDeleted(t, fx)
	})

	t.Run("delete not yet committed", func(t *testing.T) {
		fx, row := registered(t)
		// DeleteManaged's statement has run: the row is gone and its
		// exclusion insert found the earlier exclusion and did nothing, so
		// the delete holds the row but not the exclusion. The clear still
		// sees the row in its snapshot and must not act on it.
		tx, err := fx.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		var deleted int
		if err := tx.QueryRow(ctx, deleteManagedSQL, row.ID, fx.userID, fx.profileA, fx.deviceA).Scan(&deleted); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if deleted != 1 {
			t.Fatalf("delete removed %d rows, want 1", deleted)
		}
		if err := forget(fx); err != nil {
			t.Fatalf("clear during the uncommitted delete: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit delete: %v", err)
		}
		stillDeleted(t, fx)
	})

	t.Run("monitor removal not yet committed", func(t *testing.T) {
		fx, _ := registered(t)
		// Removing the monitor (Mutate's DELETE) cascades to its exclusions
		// and holds them locked until it commits. The clear must not wait on
		// them; the removal drops the exclusion either way.
		tx, err := fx.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `DELETE FROM download_subscriptions WHERE id=$1`, fx.monitor.ID); err != nil {
			t.Fatalf("remove monitor: %v", err)
		}
		if err := forget(fx); err != nil {
			t.Fatalf("clear during the monitor removal: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit monitor removal: %v", err)
		}
		if fx.entry(t, fx.episodes[0]) == nil {
			t.Fatal("monitor removal deleted the explicit download")
		}
		if n := fx.exclusions(t); n != 0 {
			t.Fatalf("exclusions after the monitor removal = %d, want 0", n)
		}
	})
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

// TestDropWatchedEpisodesChunksLookups bounds each progress lookup for
// long-running series, whose IDs the SQLite user store binds one by one.
func TestDropWatchedEpisodesChunksLookups(t *testing.T) {
	store := &chunkedProgressStore{completed: map[string]bool{"ep-0": true, "ep-1200": true}}
	svc := &Service{progressStores: chunkedProgressStores{store}}
	episodes := make([]*models.Episode, 1201)
	for i := range episodes {
		episodes[i] = &models.Episode{ContentID: fmt.Sprintf("ep-%d", i)}
	}
	kept, err := svc.dropWatchedEpisodes(t.Context(), &Subscription{UserID: 1, ProfileID: "profile"}, episodes)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(store.calls) != "[500 500 201]" {
		t.Fatalf("lookup sizes = %v, want [500 500 201]", store.calls)
	}
	if len(kept) != 1199 || kept[0].ContentID != "ep-1" || kept[len(kept)-1].ContentID != "ep-1199" {
		t.Fatalf("kept %d episodes (%s..%s), want 1199 without the two finished", len(kept), kept[0].ContentID, kept[len(kept)-1].ContentID)
	}
}

// TestWatchedFilterFailsOpen: a progress-store failure registers the monitor's
// episodes without the watched filter instead of failing the sync, so new
// episodes keep arriving.
func TestWatchedFilterFailsOpen(t *testing.T) {
	store := &chunkedProgressStore{completed: map[string]bool{"ep-1": true}, err: errors.New("progress store down")}
	svc := &Service{progressStores: chunkedProgressStores{store}, fileRepo: &monitorFileResolver{fileID: 1, seriesID: "series"}}
	episodes := []*models.Episode{{ContentID: "ep-0"}, {ContentID: "ep-1"}, {ContentID: "ep-2"}}
	items, err := svc.subscriptionEpisodeItems(t.Context(), &Subscription{UserID: 1, ProfileID: "profile", SeriesID: "series", Mode: SubModeAll, DeleteWatched: true}, episodes)
	if err != nil {
		t.Fatalf("sync items with the progress store down: %v", err)
	}
	if len(store.calls) != 1 || len(items) != 3 {
		t.Fatalf("lookups = %v, items = %d; want one failed lookup and all 3 episodes", store.calls, len(items))
	}
}

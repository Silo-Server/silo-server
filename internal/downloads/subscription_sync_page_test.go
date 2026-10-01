package downloads

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

type syncEpisodePager struct {
	EpisodeResolver
	rows         []*models.Episode
	more         bool
	calls, limit int
}

func (p *syncEpisodePager) ListDownloadEpisodesPage(_ context.Context, _ string, _ *int, _ *catalog.EpisodePagePosition, limit int) ([]*models.Episode, bool, error) {
	p.calls++
	p.limit = limit
	return p.rows, p.more, nil
}
func (p *syncEpisodePager) ListBySeries(context.Context, string) ([]*models.Episode, error) {
	p.calls++
	return p.rows, nil
}

type syncFileResolver struct {
	FileResolver
	calls int
}

func (f *syncFileResolver) ListByEpisodeIDs(_ context.Context, ids []string) (map[string][]*models.MediaFile, error) {
	f.calls++
	out := map[string][]*models.MediaFile{}
	for _, id := range ids {
		out[id] = []*models.MediaFile{{ID: 42, ContentID: "series", EpisodeID: id, FileSize: 10}}
	}
	return out, nil
}

type syncAccess struct{ err error }

func (a *syncAccess) EnsureAccessible(context.Context, string, catalog.AccessFilter) error {
	return a.err
}

func TestSubscriptionSyncPagePostgres(t *testing.T) {
	subRepo := subscriptionMutationTestRepo(t)
	// One connection proves monitor locking never waits for another pool slot
	// to query storage or register episodes; those statements use the same tx.
	poolConfig := subRepo.pool.Config()
	poolConfig.MaxConns = 1
	single, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(single.Close)
	subRepo = NewSubscriptionRepository(single)
	repo := NewRepository(single)
	_, err = repo.pool.Exec(t.Context(), `CREATE UNIQUE INDEX managed_identity ON downloads(user_id,profile_id,device_id,content_id,COALESCE(episode_id,'')) WHERE device_id IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := subRepo.CreateOrGet(t.Context(), &Subscription{ID: "monitor", UserID: 1, ProfileID: "profile", DeviceID: "device", SeriesID: "series", Mode: SubModeSpecificSeasons, SeasonNumbers: []int{0}, MaxStorageBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	pager := &syncEpisodePager{rows: []*models.Episode{{ContentID: "skipped", SeasonNumber: 1, EpisodeNumber: 1}}, more: true}
	files := &syncFileResolver{}
	access := &syncAccess{}
	svc := NewService(repo, nil, nil, files, nil, pager, fakeUserRepo{&models.User{ID: 1, DownloadAllowed: new(true)}}, access, nil, &config.DownloadConfig{Enabled: true})
	svc.subRepo = subRepo
	guard := func(row *Subscription) error {
		if !row.UpdatedAt.Equal(monitor.UpdatedAt) {
			return ErrStatusConflict
		}
		return nil
	}
	page, err := svc.SyncSubscriptionPage(t.Context(), 1, "profile", "device", "monitor", nil, 1, catalog.AccessFilter{}, guard)
	if err != nil || page.Registered != 0 || page.Examined != 1 || page.Next == nil || page.Next.ContentID != "skipped" || files.calls != 0 || pager.limit != 1 {
		t.Fatalf("page %+v %v", page, err)
	}
	pager.rows = []*models.Episode{{ContentID: "special", SeasonNumber: 0, EpisodeNumber: 1}}
	pager.more = false
	page, err = svc.SyncSubscriptionPage(t.Context(), 1, "profile", "device", "monitor", page.Next, 1, catalog.AccessFilter{}, guard)
	if err != nil || page.Registered != 1 || page.Next != nil {
		t.Fatalf("register %+v %v", page, err)
	}
	page, err = svc.SyncSubscriptionPage(t.Context(), 1, "profile", "device", "monitor", nil, 1, catalog.AccessFilter{}, guard)
	if err != nil || page.Registered != 0 {
		t.Fatalf("replay %+v %v", page, err)
	}
	// A replayed file must not be charged twice against the remaining budget.
	monitor, err = subRepo.Mutate(t.Context(), 1, "profile", "device", "monitor", false, func(row *Subscription) error { row.MaxStorageBytes = 20; return nil })
	if err != nil {
		t.Fatal(err)
	}
	pager.rows = append(pager.rows, &models.Episode{ContentID: "special-two", SeasonNumber: 0, EpisodeNumber: 2})
	page, err = svc.SyncSubscriptionPage(t.Context(), 1, "profile", "device", "monitor", nil, 2, catalog.AccessFilter{}, guard)
	if err != nil || page.Registered != 1 {
		t.Fatalf("budget double-charged replay %+v %v", page, err)
	}
	// An edit after the captured validator fences the next page before metadata.
	_, err = subRepo.Mutate(t.Context(), 1, "profile", "device", "monitor", false, func(row *Subscription) error { row.Active = false; return nil })
	if err != nil {
		t.Fatal(err)
	}
	before := pager.calls
	if _, err := svc.SyncSubscriptionPage(t.Context(), 1, "profile", "device", "monitor", nil, 1, catalog.AccessFilter{}, guard); !errors.Is(err, ErrStatusConflict) {
		t.Fatal(err)
	}
	if pager.calls != before {
		t.Fatal("resolved metadata for stale monitor")
	}
	// A delayed bridge sync also rereads the paused row instead of using its
	// captured active snapshot. Already-registered episodes remain untouched.
	if n, err := svc.syncSubscription(t.Context(), monitor); err != nil || n != 0 || pager.calls != before {
		t.Fatalf("paused bridge %d %v", n, err)
	}
	if _, err := repo.GetManagedEntry(t.Context(), 1, "profile", "device", "series", "special"); err != nil {
		t.Fatal(err)
	}
	access.err = catalog.ErrItemNotFound
	if _, err := svc.SyncSubscriptionPage(t.Context(), 1, "profile", "device", "monitor", nil, 1, catalog.AccessFilter{}, func(*Subscription) error { t.Fatal("guard ran before access"); return nil }); !errors.Is(err, catalog.ErrItemNotFound) {
		t.Fatal(err)
	}
}

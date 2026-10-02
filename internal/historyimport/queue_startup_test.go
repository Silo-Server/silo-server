package historyimport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

type startupHistoryStore struct {
	userstore.UserStore
	entries chan userstore.WatchHistoryEntry
}

func (s startupHistoryStore) SetProgressIfNewer(context.Context, string, string, float64, float64, bool, time.Time) (bool, error) {
	return true, nil
}
func (s startupHistoryStore) AddHistoryIfMissing(_ context.Context, entry userstore.WatchHistoryEntry) (bool, error) {
	s.entries <- entry
	return true, nil
}

type startupIdentityItems struct{}

func (startupIdentityItems) GetByID(context.Context, string) (*models.MediaItem, error) {
	return &models.MediaItem{ContentID: "movie", Type: "movie"}, nil
}

type startupIdentityProviders struct{}

func (startupIdentityProviders) GetByContentID(context.Context, string) ([]*models.MediaItemProviderID, error) {
	return []*models.MediaItemProviderID{{ContentID: "movie", ItemType: "movie", Provider: "imdb", ProviderID: "tt1234567"}}, nil
}
func (startupIdentityProviders) FindContentIDByProviderIDs(context.Context, map[string]string, string, string) (string, error) {
	return "movie", nil
}

func TestQueueStartupWaitsForConfiguredIdentityAndObservers(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("Filters") == "IsPlayed" {
			_, _ = w.Write([]byte(`{"Items":[{"Id":"external-movie","Name":"Movie","Type":"Movie","ProviderIds":{"Imdb":"tt1234567"},"RunTimeTicks":1000000000,"UserData":{"Played":true,"LastPlayedDate":"2026-09-01T00:00:00Z"}}]}`))
		} else {
			_, _ = w.Write([]byte(`{"Items":[]}`))
		}
	}))
	defer upstream.Close()
	if _, err := repo.pool.Exec(ctx, `UPDATE history_import_sources SET base_url=$1 WHERE id=1`, upstream.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.pool.Exec(ctx, `CREATE TABLE media_items(content_id text,type text,title text,year integer,status text,imdb_id text,tmdb_id text,tvdb_id text);INSERT INTO media_items VALUES('movie','movie','Movie',2026,'matched','tt1234567',NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	run, err := repo.EnqueueAdminRun(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	store := startupHistoryStore{entries: make(chan userstore.WatchHistoryEntry, 1)}
	service := NewService(ctx, repo, importStoreProvider{store})
	service.wakeImportQueue()
	before, err := repo.GetRunByID(ctx, run.ID)
	if err != nil || before.Status != RunStatusQueued || calls.Load() != 0 {
		t.Fatalf("construction consumed queued work: %+v %v calls=%d", before, err, calls.Load())
	}
	select {
	case <-store.entries:
		t.Fatal("effect before activation")
	default:
	}
	service.SetStableIdentityResolver(watchstate.NewStableIdentityResolver(startupIdentityItems{}, nil, startupIdentityProviders{}))
	observed := make(chan Run, 8)
	service.AddObserver(queueObserverFunc(func(run Run) { observed <- run }))
	service.StartBackgroundWork()
	service.StartBackgroundWork() // Activation is idempotent.
	select {
	case entry := <-store.entries:
		if entry.Identity.StableType != "movie" || entry.Identity.ProviderIDs["imdb"] != "tt1234567" {
			t.Fatalf("unconfigured resolver produced %+v", entry.Identity)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued run did not resume")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-observed:
			if event.Status == RunStatusCompleted {
				return
			}
		case <-deadline.C:
			t.Fatal("configured observer did not see completion")
		}
	}
}

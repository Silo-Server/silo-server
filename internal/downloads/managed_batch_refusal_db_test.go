package downloads

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// relinkElsewhere links fileID to another title, as a retitle or rematch
// does after a request resolved the file.
func (f managedFixture) relinkElsewhere(t *testing.T, fileID int) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE media_files SET content_id = content_id || '-elsewhere', episode_id = NULL WHERE id = $1`, fileID); err != nil {
		t.Fatal(err)
	}
}

func (f managedFixture) downloadCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM downloads WHERE user_id = $1`, f.userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestManagedRequestRegistersNothingWhenAFileLeftItsTitleDB pins that a
// managed request registering several entries at once, such as a season,
// writes none of them when one entry's file left its title after the request
// resolved it, so the refused request holds no quota, and that a request for
// the entries still linked registers them.
func TestManagedRequestRegistersNothingWhenAFileLeftItsTitleDB(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)
	ep1, ep2 := f.contentID+"-ep-1", f.contentID+"-ep-2"
	item := func(episodeID string, fileID int) managedItem {
		return managedItem{file: &models.MediaFile{ID: fileID, ContentID: f.contentID, EpisodeID: episodeID, FileSize: 1024}, contentID: f.contentID, episodeID: episodeID}
	}
	first, second := item(ep1, f.episodeFile(t, ep1)), item(ep2, f.episodeFile(t, ep2))
	f.relinkElsewhere(t, second.file.ID)

	svc := NewService(f.repo, nil, NewQuantityLimiter(f.repo, 0, 0, 0), nil, nil, nil, nil, nil, nil, &config.DownloadConfig{Enabled: true})
	req := CreateRequest{ContentID: f.contentID, ProfileID: f.profileA, DeviceID: f.deviceA}
	if _, err := svc.ensureManaged(ctx, f.userID, req, []managedItem{first, second}, originalDecision(), "season"); !errors.Is(err, catalog.ErrItemNotFound) {
		t.Fatalf("season with a file of another title: err = %v, want not found", err)
	}
	if n := f.downloadCount(t); n != 0 {
		t.Fatalf("refused season registered %d downloads, want none", n)
	}
	rows, err := svc.ensureManaged(ctx, f.userID, req, []managedItem{first}, originalDecision(), "season")
	if err != nil || len(rows) != 1 || rows[0].EpisodeID != ep1 {
		t.Fatalf("season of the linked episode: %+v %v", rows, err)
	}
}

// TestMonitorSyncSkipsAnEpisodeWhoseFileLeftItsTitleDB pins the partial
// result a monitor sync keeps: an episode whose file left its title since the
// sync listed it is skipped, as an episode with no file is, and the others
// register.
func TestMonitorSyncSkipsAnEpisodeWhoseFileLeftItsTitleDB(t *testing.T) {
	fx := seedMonitorFixture(t, 0, false)
	fx.relinkElsewhere(t, fx.files.files[fx.episodes[1]])
	if n := fx.sync(t); n != 2 {
		t.Fatalf("sync registered %d, want the 2 episodes still linked", n)
	}
	if fx.entry(t, fx.episodes[1]) != nil || fx.entry(t, fx.episodes[0]) == nil || fx.entry(t, fx.episodes[2]) == nil {
		t.Fatal("sync registered the episode whose file left its title, or skipped a linked one")
	}
}

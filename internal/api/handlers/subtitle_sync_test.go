package handlers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/subsync"
)

type fakeSyncService struct {
	requests []int
	external []string
	changed  []string
}

func (f *fakeSyncService) Request(_ context.Context, subtitleID int, trigger string, _ *int) (*subsync.Job, error) {
	f.requests = append(f.requests, subtitleID)
	return &subsync.Job{ID: 1, SubtitleID: subtitleID, Trigger: trigger, Status: subsync.JobPending}, nil
}
func (f *fakeSyncService) RequestExternal(_ context.Context, fileID int, sidecar models.ExternalSubtitle, _ string, _ *int) (*subsync.Job, error) {
	f.external = append(f.external, sidecar.Path)
	return &subsync.Job{ID: 2, ExternalTimingID: 1, MediaFileID: fileID, Trigger: subsync.TriggerManual, Status: subsync.JobPending}, nil
}
func (f *fakeSyncService) Latest(context.Context, int) (*subsync.Job, error) { return nil, nil }
func (f *fakeSyncService) LatestExternal(context.Context, int64) (*subsync.Job, error) {
	return nil, nil
}
func (f *fakeSyncService) LatestForSubtitles(context.Context, []int) (map[int]*subsync.Job, error) {
	return nil, nil
}
func (f *fakeSyncService) LatestForExternal(context.Context, []int64) (map[int64]*subsync.Job, error) {
	return nil, nil
}
func (f *fakeSyncService) AutoSyncEnabled(context.Context) bool { return true }
func (f *fakeSyncService) TimingChanged(_ context.Context, target subtitles.SyncTarget) {
	f.changed = append(f.changed, target.Key())
}

// Anyone who can play the file may sync or retime its stored subtitles; only
// file access is checked.
func TestStoredSubtitleSyncNeedsOnlyFileAccess(t *testing.T) {
	for _, kind := range []string{"other_account", "unowned", "denied_file"} {
		t.Run(kind, func(t *testing.T) {
			repo := newMockSubtitleRepoForHandler()
			repo.subtitles[9] = &subtitles.DownloadedSubtitle{ID: 9, MediaFileID: 42, Format: subtitles.FormatSRT, DownloadedBy: new(2), Revision: 3}
			if kind == "unowned" {
				repo.subtitles[9].DownloadedBy = nil
			}
			h := NewSubtitleSearchHandler(subtitles.NewManager(repo, newMockBlobStoreForHandler()), repo, nil)
			sync := new(fakeSyncService)
			h.SetSyncService(sync, nil)
			checker := stubItemAccessChecker{}
			if kind == "denied_file" {
				checker.err = catalog.ErrItemNotFound
			}
			h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: checker}
			ctx := apimw.SetClaims(t.Context(), &auth.Claims{UserID: 1, Role: "user"})
			access := catalog.AccessFilter{UserID: 1}

			_, syncErr := h.RequestStoredSubtitleSync(ctx, access, 9)
			updated, timingErr := h.SetStoredSubtitleTiming(ctx, access, 9, 3, subtitles.Timing{OffsetMS: 500, Scale: 1})
			if kind == "denied_file" {
				if syncErr == nil || timingErr == nil || len(sync.requests) != 0 || repo.subtitles[9].Revision != 3 {
					t.Fatalf("file access not enforced: %v %v", syncErr, timingErr)
				}
				return
			}
			if syncErr != nil || timingErr != nil {
				t.Fatalf("viewer refused: %v %v", syncErr, timingErr)
			}
			if len(sync.requests) != 1 || updated.Timing.OffsetMS != 500 || len(sync.changed) != 1 {
				t.Fatalf("requests %v, timing %+v, changed %v", sync.requests, updated.Timing, sync.changed)
			}
		})
	}
}

type memoryExternalTimings struct {
	rows map[string]*subtitles.ExternalTiming
}

func (m *memoryExternalTimings) ExternalTiming(_ context.Context, _ int, sha string) (*subtitles.ExternalTiming, error) {
	return m.rows[sha], nil
}
func (m *memoryExternalTimings) ExternalTimings(context.Context, int) (map[string]*subtitles.ExternalTiming, error) {
	return m.rows, nil
}
func (m *memoryExternalTimings) SetExternalTiming(_ context.Context, fileID int, sha, path string, format subtitles.SubtitleFormat, timing subtitles.Timing, revision int64) (*subtitles.ExternalTiming, error) {
	row := m.rows[sha]
	switch {
	case row == nil && revision == 0:
		row = &subtitles.ExternalTiming{ID: int64(len(m.rows) + 1), MediaFileID: fileID, ContentSHA256: sha, Format: format}
		m.rows[sha] = row
	case row == nil || row.Revision != revision:
		return nil, subtitles.ErrExternalTimingChanged
	}
	row.Path, row.Timing = path, timing
	row.Revision++
	return row, nil
}

// Sync keys reach stored subtitles and sidecars alike; a sidecar's correction
// belongs to its bytes and needs only file access to change.
func TestSubtitleSyncBySyncKey(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	english := write("movie.en.srt", "1\n00:00:01,000 --> 00:00:02,000\nHello\n")
	micro := write("movie.fr.sub", "{25}{50}Bonjour")
	// A sidecar that exists but cannot be read: reading a directory fails.
	unreadable := filepath.Join(dir, "movie.it.srt")
	if err := os.Mkdir(unreadable, 0o700); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{ID: 42, ContentID: "movie", ExternalSubtitles: []models.ExternalSubtitle{
		{Path: english, Language: "en", Format: "srt"},
		{Path: micro, Language: "fr", Format: "sub"},
		{Path: filepath.Join(dir, "movie.de.srt"), Language: "de", Format: "srt"},
		{Path: unreadable, Language: "it", Format: "srt"},
	}}
	repo := newMockSubtitleRepoForHandler()
	repo.subtitles[9] = &subtitles.DownloadedSubtitle{ID: 9, MediaFileID: 42, Format: subtitles.FormatSRT, Revision: 3}
	repo.list = []subtitles.DownloadedSubtitle{*repo.subtitles[9], {ID: 10, MediaFileID: 42, Format: subtitles.FormatSUB}}
	h := NewSubtitleSearchHandler(subtitles.NewManager(repo, newMockBlobStoreForHandler()), repo, nil)
	sync := new(fakeSyncService)
	external := &memoryExternalTimings{rows: map[string]*subtitles.ExternalTiming{}}
	h.SetSyncService(sync, external)
	h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: file}, ItemAccess: stubItemAccessChecker{}}
	ctx := apimw.SetClaims(t.Context(), &auth.Claims{UserID: 1, Role: "user"})
	access := catalog.AccessFilter{UserID: 1}
	sidecarKey := subtitles.ExternalSyncKey(english)

	states, err := h.ListSubtitleSync(ctx, access, 42)
	if err != nil || len(states) != 2 || states[0].Key != "stored-9" || states[1].Key != sidecarKey || states[1].Revision != 0 {
		t.Fatalf("list %+v %v", states, err)
	}
	for _, key := range []string{subtitles.ExternalSyncKey(micro), subtitles.ExternalSyncKey(filepath.Join(dir, "movie.de.srt")), "stored-77", "bogus"} {
		if _, err := h.SubtitleSync(ctx, access, 42, key); !errors.Is(err, subtitles.ErrSubtitleNotFound) {
			t.Errorf("%s: %v", key, err)
		}
	}
	// The list leaves the unreadable sidecar out; asking for it reports why.
	if _, err := h.SubtitleSync(ctx, access, 42, subtitles.ExternalSyncKey(unreadable)); !errors.Is(err, errSidecarUnreadable) {
		t.Errorf("unreadable sidecar: %v", err)
	}

	if _, err := h.StartSubtitleSync(ctx, access, 42, sidecarKey); err != nil || len(sync.external) != 1 || sync.external[0] != english {
		t.Fatalf("start: %v %v", err, sync.external)
	}
	sha := states[1].ContentSHA256
	updated, err := h.SetSubtitleSyncTiming(ctx, access, 42, sidecarKey, 0, sha, subtitles.Timing{Scale: 1, OffsetMS: 750})
	if err != nil || updated.Timing.OffsetMS != 750 || updated.Revision != 1 || len(sync.changed) != 1 || sync.changed[0] != sidecarKey {
		t.Fatalf("set: %+v %v changed %v", updated, err, sync.changed)
	}
	if _, err := h.SetSubtitleSyncTiming(ctx, access, 42, sidecarKey, 0, sha, subtitles.Timing{Scale: 1}); !errors.Is(err, subtitles.ErrExternalTimingChanged) {
		t.Fatalf("stale write: %v", err)
	}
	if _, err := h.SetSubtitleSyncTiming(ctx, access, 42, sidecarKey, 1, "other-bytes", subtitles.Timing{Scale: 1}); !errors.Is(err, subtitles.ErrExternalTimingChanged) {
		t.Fatalf("write for other bytes: %v", err)
	}
}

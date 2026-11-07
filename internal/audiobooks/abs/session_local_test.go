package abs

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// positionRecordingProgressFake models just enough of the progress store for the
// offline-sync tests: GetProgress returns any pre-seeded row, and both the
// create (UpsertProgress) and update (UpdateProgressPosition) paths record the
// persisted position plus which path handled each item.
type positionRecordingProgressFake struct {
	fakeProgressStore
	mu      sync.Mutex
	seeded  map[string]*ProgressRow // contentID → existing row GetProgress returns
	calls   map[string]float64      // contentID → last persisted position (either path)
	viaPath map[string]string       // contentID → "create" | "update"
}

func (f *positionRecordingProgressFake) GetProgress(_ context.Context, _, _, contentID string) (*ProgressRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.seeded[contentID]; ok {
		return r, nil
	}
	return nil, nil
}

func (f *positionRecordingProgressFake) UpsertProgress(_ context.Context, row ProgressRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked(row.ContentID, row.CurrentSeconds, "create")
	return nil
}

func (f *positionRecordingProgressFake) UpdateProgressPosition(_ context.Context, _, _, contentID string, pos float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked(contentID, pos, "update")
	return nil
}

func (f *positionRecordingProgressFake) recordLocked(contentID string, pos float64, path string) {
	if f.calls == nil {
		f.calls = map[string]float64{}
		f.viaPath = map[string]string{}
	}
	f.calls[contentID] = pos
	f.viaPath[contentID] = path
}

func (f *positionRecordingProgressFake) pos(contentID string) (float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.calls[contentID]
	return v, ok
}

func (f *positionRecordingProgressFake) path(contentID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.viaPath[contentID]
}

func TestSyncLocalSession_ExistingRow_UsesUpdate(t *testing.T) {
	// A row already exists (book was played online before), so the offline sync
	// must advance it via the monotonic UPDATE path — not recreate it — to avoid
	// clobbering is_finished / progress_pct the user set explicitly.
	prog := &positionRecordingProgressFake{
		seeded: map[string]*ProgressRow{"book-1": {ContentID: "book-1", CurrentSeconds: 5}},
	}
	media := &stubMediaStore{known: map[string]*models.MediaItem{"book-1": nil}}
	h := New(Dependencies{MediaStore: media, ProgressStore: prog})

	body := []byte(`{"id":"sess-1","libraryItemId":"book-1","currentTime":200}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/session/local", nil, body, "1", "", h.handleSyncLocalSession)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if p := prog.path("book-1"); p != "update" {
		t.Errorf("persist path = %q, want update", p)
	}
	if got, _ := prog.pos("book-1"); got != 200 {
		t.Errorf("position = %v, want 200", got)
	}
}

// Plappa and Prologue never call /play: they report a live play through
// /session/local every few seconds. Live reports of one client session make
// one report-only native playback session; a late report (offline listening,
// or a client re-sending an old session) only moves the resume position.
func TestSyncLocalSession_TracksLivePlayback(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name         string
		startedAt    time.Time
		updatedAt    time.Time
		total        float64
		wantSessions int
		wantPlayed   float64
	}{
		// The ten seconds listened before the first report count.
		{name: "new play", startedAt: now.Add(-10 * time.Second), updatedAt: now, total: 10, wantSessions: 1, wantPlayed: 10},
		// Without a listening total they are measured from the position.
		{name: "new play without a total", startedAt: now.Add(-10 * time.Second), updatedAt: now, wantSessions: 1, wantPlayed: 10},
		// A play rebuilt after an idle expiry takes the client's running
		// total as its baseline: what it covers was counted before.
		{name: "rebuilt play", startedAt: now.Add(-time.Hour), updatedAt: now, total: 10, wantSessions: 1},
		{name: "late report", startedAt: now.Add(-time.Hour), updatedAt: now.Add(-15 * time.Minute), total: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			media := &playStartMediaStore{
				item: &models.MediaItem{ContentID: "book-1", Type: "audiobook"},
				files: []*models.MediaFile{
					{ID: 42, ContentID: "book-1", Duration: 44},
					{ID: 43, ContentID: "book-1", Duration: 5356},
				},
			}
			prog := &positionRecordingProgressFake{}
			nativeSessions := playback.NewSessionManager(0, 0)
			syncer := &recordingPlaybackSessionSyncer{}
			h := New(Dependencies{MediaStore: media, ProgressStore: prog, NativeSessions: nativeSessions, NativeSessionSyncer: syncer})

			// The second report repeats the running total and jumps ahead by a
			// scrub, so it credits nothing.
			for _, position := range []float64{40, 162.9} {
				body := fmt.Appendf(nil, `{"id":"client-sess-1","libraryItemId":"book-1","currentTime":%v,"timeListening":%v,`+
					`"startTime":30,"startedAt":%d,"updatedAt":%d,"deviceInfo":{"clientName":"Prologue","clientVersion":"2026.5.4"}}`,
					position, tc.total, tc.startedAt.UnixMilli(), tc.updatedAt.UnixMilli())
				rec := dispatchABSWithParams(http.MethodPost, "/api/session/local", nil, body, "1", "profile-1", h.handleSyncLocalSession)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
				}
			}

			if got, _ := prog.pos("book-1"); got != 162.9 {
				t.Errorf("resume position = %v, want 162.9", got)
			}
			sessions := nativeSessions.AllSessions()
			if len(sessions) != tc.wantSessions {
				t.Fatalf("native sessions = %d, want %d", len(sessions), tc.wantSessions)
			}
			// A started play reaches the admin views at once; later reports
			// wait for the periodic sync.
			if syncer.calls != tc.wantSessions {
				t.Errorf("admin view syncs = %d, want %d", syncer.calls, tc.wantSessions)
			}
			if tc.wantSessions == 0 {
				return
			}
			s := sessions[0]
			if s.MediaFileID != 42 || s.Position != 162.9 || s.TimelineDuration != 5400 {
				t.Errorf("session file/position/timeline = %d/%v/%v, want 42/162.9/5400", s.MediaFileID, s.Position, s.TimelineDuration)
			}
			if s.ClientName != "Prologue" || s.ClientVersion != "2026.5.4" {
				t.Errorf("client = %q %q, want Prologue 2026.5.4", s.ClientName, s.ClientVersion)
			}
			if math.Abs(s.Watch.PlayedSeconds-tc.wantPlayed) > 0.01 {
				t.Errorf("played seconds = %v, want %v", s.Watch.PlayedSeconds, tc.wantPlayed)
			}
			if !s.ReportOnly || !s.DisableProgressPersistence || s.IdleGrace != reportedPlaybackGrace {
				t.Errorf("report only = %v, progress persistence disabled = %v, idle grace = %v", s.ReportOnly, s.DisableProgressPersistence, s.IdleGrace)
			}
		})
	}
}

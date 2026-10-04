package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/subsync"
)

type fakeSubtitleSync struct {
	states     []handlers.SubtitleSyncState
	row        subtitles.DownloadedSubtitle
	job        *subsync.Job
	requests   int
	timing     *subtitles.Timing
	revision   int64
	requestErr error
}

func (f *fakeSubtitleSync) SyncAvailable() bool                  { return true }
func (f *fakeSubtitleSync) AutoSyncEnabled(context.Context) bool { return true }
func (f *fakeSubtitleSync) RequestStoredSubtitleSync(context.Context, catalogpkg.AccessFilter, int) (*subsync.Job, error) {
	f.requests++
	return f.job, f.requestErr
}
func (f *fakeSubtitleSync) StoredSubtitleSync(context.Context, catalogpkg.AccessFilter, int) (*subtitles.DownloadedSubtitle, *subsync.Job, error) {
	return &f.row, f.job, nil
}
func (f *fakeSubtitleSync) SubtitleSyncJobs(_ context.Context, ids []int) map[int]*subsync.Job {
	return map[int]*subsync.Job{f.row.ID: f.job}
}
func (f *fakeSubtitleSync) ExternalSyncAvailable() bool { return true }
func (f *fakeSubtitleSync) ListSubtitleSync(context.Context, catalogpkg.AccessFilter, int) ([]handlers.SubtitleSyncState, error) {
	return f.states, nil
}
func (f *fakeSubtitleSync) SubtitleSync(_ context.Context, _ catalogpkg.AccessFilter, _ int, key string) (*handlers.SubtitleSyncState, error) {
	for i := range f.states {
		if f.states[i].Key == key {
			state := f.states[i]
			return &state, nil
		}
	}
	return nil, subtitles.ErrSubtitleNotFound
}
func (f *fakeSubtitleSync) StartSubtitleSync(ctx context.Context, access catalogpkg.AccessFilter, fileID int, key string) (*handlers.SubtitleSyncState, error) {
	f.requests++
	state, err := f.SubtitleSync(ctx, access, fileID, key)
	if err == nil {
		state.Job = f.job
	}
	return state, err
}
func (f *fakeSubtitleSync) SetSubtitleSyncTiming(ctx context.Context, access catalogpkg.AccessFilter, fileID int, key string, revision int64, sha string, t subtitles.Timing) (*handlers.SubtitleSyncState, error) {
	state, err := f.SubtitleSync(ctx, access, fileID, key)
	if err != nil {
		return nil, err
	}
	if state.Revision != revision || state.ContentSHA256 != sha {
		return nil, subtitles.ErrExternalTimingChanged
	}
	f.timing, f.revision = &t, revision
	state.Timing = t
	state.Revision++
	return state, nil
}
func (f *fakeSubtitleSync) SetStoredSubtitleTiming(_ context.Context, _ catalogpkg.AccessFilter, _ int, revision int64, t subtitles.Timing) (*subtitles.DownloadedSubtitle, error) {
	f.timing, f.revision = &t, revision
	updated := f.row
	updated.Timing = t
	updated.Revision++
	return &updated, nil
}

func TestSubtitleSyncContract(t *testing.T) {
	deps, _ := catalogDeps(t)
	row := subtitles.DownloadedSubtitle{ID: 9, MediaFileID: 42, Provider: "opensubtitles", Language: "en", Format: subtitles.FormatSRT,
		Revision: 3, CreatedAt: fixedTime(), DownloadedBy: new(1), Timing: subtitles.Timing{Scale: 25 / 23.976, OffsetMS: -1200}}
	confidence := 0.83
	finished := fixedTime().Add(time.Minute)
	sync := &fakeSubtitleSync{row: row, job: &subsync.Job{ID: 77, SubtitleID: 9, Trigger: subsync.TriggerAuto, Status: string(subsync.StatusSynced),
		Confidence: &confidence, Result: &subtitles.Timing{Scale: 25 / 23.976, OffsetMS: -1200}, CreatedAt: fixedTime(), FinishedAt: &finished}}
	deps.SubtitleSync = sync
	deps.ViewerSubtitleDelete = &fakeViewerSubtitleDelete{row: row}
	h := newTestHandler(t, deps)
	path := Prefix + "/subtitles/stored/9"

	status := do(t, h, http.MethodGet, Prefix+"/subtitles/sync/status", "", viewerHeaders())
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"auto_sync":true`) {
		t.Fatalf("status %d %s", status.Code, status.Body)
	}

	read := do(t, h, http.MethodGet, path+"/sync", "", viewerHeaders())
	var got struct {
		Subtitle StoredSubtitle `json:"subtitle"`
	}
	if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &got) != nil {
		t.Fatalf("read %d %s", read.Code, read.Body)
	}
	if got.Subtitle.Timing.OffsetMS != -1200 || got.Subtitle.Sync == nil || got.Subtitle.Sync.ID != "77" ||
		got.Subtitle.Sync.Status != "synced" || got.Subtitle.Sync.Result == nil || *got.Subtitle.Sync.Confidence != confidence {
		t.Fatalf("projection %+v", got.Subtitle)
	}

	started := do(t, h, http.MethodPost, path+"/sync", "", viewerHeaders())
	if started.Code != http.StatusAccepted || sync.requests != 1 || !strings.Contains(started.Body.String(), `"id":"77"`) {
		t.Fatalf("start %d %s", started.Code, started.Body)
	}

	// Timing writes are guarded by the validator the sync read returns, the
	// same one viewer metadata carries.
	tag := read.Header().Get("ETag")
	if meta := do(t, h, http.MethodGet, path+"/metadata", "", viewerHeaders()); tag == "" || meta.Header().Get("ETag") != tag {
		t.Fatalf("sync read validator %q, metadata %q", tag, meta.Header().Get("ETag"))
	}
	body := `{"offset_ms":0,"scale":1}`
	requireProblem(t, do(t, h, http.MethodPut, path+"/timing", body, viewerHeaders()), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodPut, path+"/timing", body, with(viewerHeaders(), "If-Match", `"stale"`)), TypePreconditionFailed)
	requireProblem(t, do(t, h, http.MethodPut, path+"/timing", `{"offset_ms":0,"scale":2}`, with(viewerHeaders(), "If-Match", tag)), TypeValidationFailed)
	if sync.timing != nil {
		t.Fatal("refused timing write dispatched")
	}
	reset := do(t, h, http.MethodPut, path+"/timing", body, with(viewerHeaders(), "If-Match", tag))
	if reset.Code != 200 || sync.timing == nil || !sync.timing.IsIdentity() || sync.revision != 3 || reset.Header().Get("ETag") == tag {
		t.Fatalf("reset %d %s", reset.Code, reset.Body)
	}

	deps.SubtitleSync = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, path+"/sync", "", viewerHeaders()), TypeDependencyUnavailable)
}

func TestSubtitleSyncByKeyContract(t *testing.T) {
	deps, _ := catalogDeps(t)
	row := subtitles.DownloadedSubtitle{ID: 9, MediaFileID: 42, Provider: "opensubtitles", Language: "en", Format: subtitles.FormatSRT,
		ReleaseName: "Movie.2021.1080p", Revision: 3, CreatedAt: fixedTime(), Timing: subtitles.Timing{Scale: 1}}
	sidecar := &models.ExternalSubtitle{Path: "/media/Movie.2021.en.srt", Language: "en", Format: "srt"}
	externalKey := subtitles.ExternalSyncKey(sidecar.Path)
	sync := &fakeSubtitleSync{
		job: &subsync.Job{ID: 78, ExternalTimingID: 4, Trigger: subsync.TriggerManual, Status: subsync.JobPending, CreatedAt: fixedTime()},
		states: []handlers.SubtitleSyncState{
			{Key: "stored-9", MediaFileID: 42, Stored: &row, Timing: row.Timing, Revision: 3},
			{Key: externalKey, MediaFileID: 42, External: sidecar, ContentSHA256: strings.Repeat("ab", 32), Timing: subtitles.Timing{Scale: 1}},
		},
	}
	deps.SubtitleSync = sync
	h := newTestHandler(t, deps)
	base := Prefix + "/subtitles/42/sync"

	var list struct {
		Subtitles []map[string]any `json:"subtitles"`
	}
	rec := do(t, h, http.MethodGet, base, "", viewerHeaders())
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Subtitles) != 2 {
		t.Fatalf("list %d %s", rec.Code, rec.Body)
	}
	stored, external := list.Subtitles[0], list.Subtitles[1]
	if stored["key"] != "stored-9" || stored["source"] != "downloaded" || stored["stored_subtitle_id"] != "9" || stored["label"] != "Movie.2021.1080p" {
		t.Fatalf("stored entry %v", stored)
	}
	if external["key"] != externalKey || external["source"] != "external" || external["label"] != "Movie.2021.en.srt" || external["stored_subtitle_id"] != nil {
		t.Fatalf("sidecar entry %v", external)
	}
	if strings.Contains(rec.Body.String(), "/media/") {
		t.Fatalf("list exposes a filesystem path: %s", rec.Body)
	}

	read := do(t, h, http.MethodGet, base+"/"+externalKey, "", viewerHeaders())
	tag := read.Header().Get("ETag")
	if read.Code != 200 || tag == "" {
		t.Fatalf("read %d %s", read.Code, read.Body)
	}

	started := do(t, h, http.MethodPost, base+"/"+externalKey, "", viewerHeaders())
	if started.Code != http.StatusAccepted || sync.requests != 1 || !strings.Contains(started.Body.String(), `"sync":{"id":"78"`) ||
		strings.Contains(started.Body.String(), "subtitle_id") {
		t.Fatalf("start %d %s", started.Code, started.Body)
	}

	body := `{"offset_ms":-1200,"scale":1}`
	requireProblem(t, do(t, h, http.MethodPut, base+"/"+externalKey+"/timing", body, viewerHeaders()), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodPut, base+"/"+externalKey+"/timing", body, with(viewerHeaders(), "If-Match", `"stale"`)), TypePreconditionFailed)
	set := do(t, h, http.MethodPut, base+"/"+externalKey+"/timing", body, with(viewerHeaders(), "If-Match", tag))
	if set.Code != 200 || sync.timing == nil || sync.timing.OffsetMS != -1200 || set.Header().Get("ETag") == tag {
		t.Fatalf("set %d %s", set.Code, set.Body)
	}

	requireProblem(t, do(t, h, http.MethodGet, base+"/stored-77", "", viewerHeaders()), TypeNotFound)
	if rec := do(t, h, http.MethodGet, base+"/embedded-1", "", viewerHeaders()); rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("malformed key %d %s", rec.Code, rec.Body)
	}
}
